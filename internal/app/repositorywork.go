//go:build client && !server

package app

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/repowork"
)

// repositoryWorkTimeout bounds one index pass. The inventory reads the planning
// documents of a repository, not its source tree, so two seconds is generous;
// the bound exists so a huge history cannot hold a page request open.
const repositoryWorkTimeout = 2 * time.Second

// maxRepositoryWorkIDLen bounds the document id a query may carry.
const maxRepositoryWorkIDLen = 64

// RepositoryWork returns the bounded document inventory of this repository.
// A follower gets the same view: it is read-only and the paths are relative.
func (s *appSession) RepositoryWork(ctx context.Context) repowork.Inventory {
	ctx, cancel := context.WithTimeout(ctx, repositoryWorkTimeout)
	defer cancel()
	inv, err := repowork.Scan(ctx, repowork.Options{
		Root:       s.repo,
		ExtraRoots: s.cfg.RepositoryWorkRoots,
	})
	if err != nil {
		// A scan that could not run at all is still a readable page: the window
		// shows an empty list with the reason rather than an error page.
		inv = repowork.Inventory{Skipped: []repowork.Skipped{{Path: ".", Reason: err.Error()}}, Partial: true}
	}
	return inv
}

// RepositoryWorkDocument opens one indexed document after checking that its
// content still matches the hash the inventory published.
func (s *appSession) RepositoryWorkDocument(ctx context.Context, id string) (repowork.Document, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, repositoryWorkTimeout)
	defer cancel()
	return repowork.Open(ctx, s.repo, id)
}

// GET /api/repository-work — the planning documents of the repository and the
// next items the active state file states.
func (s *appSession) handleRepositoryWork(w http.ResponseWriter, r *http.Request) {
	if s.repo == "" {
		writeJSON(w, http.StatusOK, repowork.Inventory{Documents: []repowork.Document{}})
		return
	}
	writeJSON(w, http.StatusOK, s.RepositoryWork(r.Context()))
}

// GET /api/repository-work/document?id=… — one document's Markdown, re-read
// from disk and verified against the indexed hash.
func (s *appSession) handleRepositoryWorkDocument(w http.ResponseWriter, r *http.Request) {
	if s.repo == "" {
		writeAPIError(w, http.StatusNotFound, "this session has no repository")
		return
	}
	id := r.URL.Query().Get("id")
	if id == "" || len(id) > maxRepositoryWorkIDLen {
		writeAPIError(w, http.StatusBadRequest, "id is missing or too long")
		return
	}
	doc, raw, err := s.RepositoryWorkDocument(r.Context(), id)
	switch {
	case errors.Is(err, repowork.ErrNotFound):
		writeAPIError(w, http.StatusNotFound, "no such document")
		return
	case errors.Is(err, repowork.ErrChanged):
		writeAPIError(w, http.StatusConflict, "the document changed since it was indexed; refresh the work view")
		return
	case err != nil:
		writeAPIError(w, http.StatusInternalServerError, "the document could not be read")
		return
	}
	writeJSON(w, http.StatusOK, repositoryWorkDocumentView{Document: doc, Markdown: string(raw)})
}

// repositoryWorkDocumentView is the body of GET /api/repository-work/document.
type repositoryWorkDocumentView struct {
	repowork.Document
	Markdown string `json:"markdown"`
}
