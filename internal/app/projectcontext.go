//go:build client && !server

package app

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/gather"
)

// Context pill states shown on Page 1.
const (
	contextGathering = "gathering"
	contextOK        = "ok"
	contextNone      = "none"
	contextFailed    = "failed"
)

// collectContext gathers the project context for text. It never fails a
// session: a gatherer error becomes an empty context with a note.
func (e *executor) collectContext(ctx context.Context, text string) contracts.ProjectContext {
	collect := e.cfg.Gather
	if collect == nil {
		collect = gather.Collect
	}
	pc, err := collect(ctx, gather.Options{Repo: e.cfg.Repo, Text: text})
	if err != nil {
		e.log.Warn("project context unavailable", "err", err)
		return contracts.ProjectContext{
			Source: contracts.ContextSourceNone, Hits: []contracts.ContextHit{},
			Notes: []string{"project context unavailable: " + notice(err)},
		}
	}
	return pc
}

// refreshContext gathers the project context for text and publishes it to the
// window through the status pills and GET /api/context. A slower, older run
// never overwrites a newer one.
func (e *executor) refreshContext(ctx context.Context, text string) {
	e.mu.Lock()
	e.contextSeq++
	seq := e.contextSeq
	if e.projectCtx == nil {
		e.pills.Context, e.pills.ContextDetail = contextGathering, "reading project notes, specs and decisions"
	}
	e.mu.Unlock()

	pc := e.collectContext(ctx, text)
	if ctx.Err() != nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if seq != e.contextSeq {
		return
	}
	e.projectCtx = &pc
	e.pills.Context, e.pills.ContextDetail = contextPill(pc)
}

// contextPill renders the Page 1 pill for a gathered context.
func contextPill(pc contracts.ProjectContext) (state, detail string) {
	where := map[string]string{
		contracts.ContextSourceRecall:       "the recall index",
		contracts.ContextSourceLegacyRecall: "the legacy recall index",
		contracts.ContextSourceFiles:        "project notes and docs",
	}[pc.Source]
	switch {
	case len(pc.Hits) > 0:
		return contextOK, fmt.Sprintf("%d related note(s) from %s", len(pc.Hits), where)
	case len(pc.Notes) > 0 && strings.Contains(pc.Notes[0], "unavailable"):
		return contextFailed, pc.Notes[0]
	case pc.Source == contracts.ContextSourceNone && len(pc.Notes) > 0 && strings.HasPrefix(pc.Notes[0], "no requirements"):
		return contextNone, "write requirements to find related notes"
	case where != "":
		return contextNone, "nothing related in " + where
	default:
		return contextNone, "no project notes, specs or decision records found"
	}
}

// ProjectContext returns the latest gathered context, or an empty one.
func (e *executor) ProjectContext() contracts.ProjectContext {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.projectCtx == nil {
		return contracts.ProjectContext{Source: contracts.ContextSourceNone, Hits: []contracts.ContextHit{}}
	}
	return *e.projectCtx
}

// normalizedText is the text the context is matched against once the document
// has been interpreted: the product view and the technical appendix.
func normalizedText(n contracts.NormalizedPRD) string {
	p := n.Product
	parts := []string{p.Title, p.Problem}
	for _, group := range [][]string{p.Goals, p.Actors, p.Flows, p.Requirements, p.AcceptanceCriteria, p.Constraints} {
		parts = append(parts, group...)
	}
	parts = append(parts, n.TechnicalAppendix)
	return strings.Join(parts, "\n")
}

// GET /api/context — the project context the technical avatars will receive.
func (s *appSession) handleContext(w http.ResponseWriter, _ *http.Request) {
	if s.exec == nil {
		writeJSON(w, http.StatusOK, contracts.ProjectContext{Source: contracts.ContextSourceNone, Hits: []contracts.ContextHit{}})
		return
	}
	writeJSON(w, http.StatusOK, s.exec.ProjectContext())
}
