//go:build client && !server

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/danielckv/agentic-plan-forge/internal/config"
	"github.com/danielckv/agentic-plan-forge/internal/ingestion"
	"github.com/danielckv/agentic-plan-forge/internal/relay"
)

const (
	// ingestChunkBytes keeps one append comfortably under the 256 KiB cap once
	// the separator heading is added.
	ingestChunkBytes = 200 << 10
	maxRecoveryBytes = relay.MaxDocumentBytes
	maxAPIBody       = 8 << 10
)

// routes registers the application API behind the bridge's Host, Origin and
// Bearer checks. The bridge itself serves GET /, GET /ws, POST /api/session,
// POST /api/open-url and POST /api/upload.
func (s *appSession) routes() {
	s.bridge.Handle("GET /api/status", s.handleStatus)
	s.bridge.Handle("GET /api/context", s.handleContext)
	s.bridge.Handle("GET /api/components", s.handleComponents)
	s.bridge.Handle("GET /api/plan-files", s.handlePlanFiles)
	s.bridge.Handle("POST /api/reinterpret", s.handleReinterpret)
	s.bridge.Handle("GET /api/invite", s.handleInvite)
	s.bridge.Handle("GET /api/approval", s.handleApproval)
	s.bridge.Handle("GET /api/relay", s.handleRelay)
	s.bridge.Handle("POST /api/recovery", s.handleRecovery)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeAPIError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAPIBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	return true
}

// GET /api/status — the Page 1 pills and the generation host summary.
func (s *appSession) handleStatus(w http.ResponseWriter, _ *http.Request) {
	if s.exec == nil {
		writeJSON(w, http.StatusOK, StatusView{AST: "remote", OCR: "unknown"})
		return
	}
	writeJSON(w, http.StatusOK, s.exec.Status())
}

// POST /api/reinterpret — the Page 1 "Re-interpret" button.
func (s *appSession) handleReinterpret(w http.ResponseWriter, _ *http.Request) {
	if s.exec != nil {
		s.exec.Reinterpret()
	}
	w.WriteHeader(http.StatusAccepted)
}

// GET /api/invite — the invite URL for the Page 1 header Copy button. Remote
// owner only; a Solo session and a follower answer 404.
func (s *appSession) handleInvite(w http.ResponseWriter, _ *http.Request) {
	url, ok := s.inviteURL()
	if !ok {
		writeAPIError(w, http.StatusNotFound, "this session has no invite")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": url})
}

// GET /api/approval — the last failed approval, including the absolute path the
// owner's window shows. 204 when there is none.
func (s *appSession) handleApproval(w http.ResponseWriter, _ *http.Request) {
	if s.exec == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	f := s.exec.ApprovalFailure()
	if f == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, f)
}

// GET /api/relay — relay connection state for the offline banner.
func (s *appSession) handleRelay(w http.ResponseWriter, _ *http.Request) {
	if s.link == nil {
		writeJSON(w, http.StatusOK, RelayStatus{State: "local"})
		return
	}
	writeJSON(w, http.StatusOK, s.link.RelayStatus())
}

// POST /api/recovery {"text":"…"} — exports unconfirmed edits to
// PlanForgeCache()/recovery/<session>/<unix-ts>.md (0600) so nothing is silently
// dropped on a resync or a late push.
func (s *appSession) handleRecovery(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text string `json:"text"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRecoveryBytes+maxAPIBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || req.Text == "" || len(req.Text) > maxRecoveryBytes || !utf8.ValidString(req.Text) {
		writeAPIError(w, http.StatusBadRequest, "text of 1 byte to 512 KiB is required")
		return
	}
	path, err := writeRecovery(s.id, req.Text)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "could not save the recovery file")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"file": filepath.Base(path)})
}

func writeRecovery(sessionID, text string) (string, error) {
	cache, err := config.EnsureCache()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(cache, "recovery", sessionID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, fmt.Sprintf("%s-%d.md", sessionID, time.Now().Unix()))
	if err := atomicWrite(path, []byte(text)); err != nil {
		return "", err
	}
	return path, nil
}

// --- upload ----------------------------------------------------------------------------------

type uploadFileResult struct {
	Name     string   `json:"name"`
	Format   string   `json:"format,omitempty"`
	Blocks   int      `json:"blocks"`
	Bytes    int64    `json:"bytes"`
	Appended bool     `json:"appended"`
	Warnings []string `json:"warnings,omitempty"`
	Error    string   `json:"error,omitempty"`
}

// uploadHandler parses uploaded documents on this machine and appends their
// Markdown to the shared document. Original bytes are never written anywhere
// and the client's file name is display text only.
func (s *appSession) uploadHandler(ctx context.Context, files []relay.UploadedFile) (any, error) {
	opts := ingestion.UIOptions()
	opts.OCRLanguage = s.cfg.OCRLanguage
	results := make([]uploadFileResult, 0, len(files))
	var warnings []string
	failures := 0
	for _, f := range files {
		res := uploadFileResult{Name: f.Name, Bytes: f.Size}
		blocks, rep, err := ingestion.IngestWithOptions(ctx, f.Name, f.Content, f.Size, opts)
		res.Format, res.Blocks, res.Warnings = rep.Format, rep.Blocks, rep.Warnings
		if err != nil {
			res.Error = notice(err)
			failures++
			results = append(results, res)
			continue
		}
		md := ingestion.Render(blocks)
		if strings.TrimSpace(md) == "" {
			res.Error = "no text could be recovered from the document"
			if rep.Format == ingestion.FormatPDF {
				res.Error += "; " + ingestion.OCRInstallGuidance
			}
			failures++
			results = append(results, res)
			continue
		}
		if err := s.appendMarkdown(f.Name, md); err != nil {
			res.Error = notice(err)
			failures++
		} else {
			res.Appended = true
		}
		warnings = append(warnings, rep.Warnings...)
		results = append(results, res)
	}
	if failures == len(files) {
		return nil, errors.New(results[0].Error)
	}
	return map[string]any{"files": results, "warnings": warnings}, nil
}

func (s *appSession) appendMarkdown(name, md string) error {
	chunks := splitMarkdown(md, ingestChunkBytes)
	for i, chunk := range chunks {
		label := name
		if len(chunks) > 1 {
			label = fmt.Sprintf("%s (part %d of %d)", name, i+1, len(chunks))
		}
		if err := s.auth.Ingest(relay.IngestAppendPayload{Source: relay.IngestSourceUpload, SourceName: label, Markdown: chunk}); err != nil {
			return err
		}
	}
	return nil
}

// splitMarkdown cuts md into pieces of at most max bytes, preferring paragraph
// boundaries and never splitting a rune.
func splitMarkdown(md string, max int) []string {
	if len(md) <= max {
		return []string{md}
	}
	var out []string
	var cur bytes.Buffer
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, para := range strings.SplitAfter(md, "\n\n") {
		for len(para) > max {
			cut := max
			if i := strings.LastIndexByte(para[:cut], '\n'); i > 0 {
				cut = i + 1
			}
			for cut > 0 && !utf8.RuneStart(para[cut]) {
				cut--
			}
			if cut == 0 {
				cut = max
			}
			flush()
			out = append(out, para[:cut])
			para = para[cut:]
		}
		if cur.Len()+len(para) > max {
			flush()
		}
		cur.WriteString(para)
	}
	flush()
	return out
}
