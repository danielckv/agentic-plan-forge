//go:build client && !server

package app

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/ast"
	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/impact"
)

// Impact pill states shown on Page 1.
const (
	impactPending = "mapping"
	impactQuick   = "quick"
	impactOK      = "ok"
	impactFailed  = "failed"

	impactPendingDetail = "reading the code, docs and past decisions"
)

// rescanInterval is how stale the repository scan may be before a map pass
// re-reads it.
//
// The scan runs once when the session starts and the graph is never replaced,
// so without this the worktree hash is frozen for the life of the window and
// the map would follow the code only across sessions. A warm scan is an lstat
// per file — the parse cache matches on size and mtime without reading the
// contents — so re-reading is cheap, and the interval only stops a typing
// burst from doing it repeatedly.
const rescanInterval = 10 * time.Second

// refreshGrounding runs the two passes that ground one interpretation, in
// order: the project context, which the map reads, and then the map itself.
// The repository is re-read first when the last scan has gone stale.
func (e *executor) refreshGrounding(ctx context.Context, norm contracts.NormalizedPRD) {
	e.rescan(ctx)
	e.refreshContext(ctx, normalizedText(norm))
	e.refreshImpact(ctx, norm)
}

// rescan re-reads the repository when the last scan is older than
// rescanInterval, so a map built now reflects the code as it is now. The graph
// is replaced only when the working tree actually moved: an unchanged tree
// leaves every consumer's hash alone.
func (e *executor) rescan(ctx context.Context) {
	e.mu.Lock()
	fresh := e.graph != nil && time.Since(e.lastScan) < rescanInterval
	e.mu.Unlock()
	if fresh || ctx.Err() != nil {
		return
	}
	select {
	case <-e.scanDone:
	case <-ctx.Done():
		return
	}

	scan := e.cfg.Scan
	if scan == nil {
		scan = ast.Scan
	}
	g, err := scan(ctx, ast.ScanOptions{Root: e.cfg.Repo, ReadCache: true, WriteCache: true, SkipDocDrift: true})
	if err != nil || g == nil || ctx.Err() != nil {
		// The first scan's graph stays. A failed re-read is not worth a pill
		// change: nothing the user can see has become less true.
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.lastScan = time.Now()
	if e.graph != nil && e.graph.WorktreeHash == g.WorktreeHash {
		return
	}
	// Doc drift is skipped above, so the first scan's reports are carried over
	// rather than dropped: Page 1's doc-health pill must not go blank.
	if e.graph != nil {
		g.DocDriftReports = e.graph.DocDriftReports
	}
	e.graph = g
}

// refreshImpact builds the impact map for norm and publishes it.
//
// It paints twice on purpose. The keyword map is published before the model
// call starts, so the rail shows something true immediately and says it is a
// keyword match; the model's answer replaces it when it lands.
//
// A pass is superseded two ways. impactSeq rejects a slow result once a newer
// interpretation has started, as refreshContext already does for the context.
// impactCancel stops the provider call outright, so a user who keeps typing
// does not pay for a map of a PRD they have replaced.
func (e *executor) refreshImpact(ctx context.Context, norm contracts.NormalizedPRD) {
	ictx, cancel := context.WithCancel(ctx)
	defer cancel()

	e.mu.Lock()
	e.impactSeq++
	seq := e.impactSeq
	if e.impactCancel != nil {
		e.impactCancel()
	}
	e.impactCancel = cancel
	e.mu.Unlock()

	select {
	case <-e.scanDone:
	case <-ictx.Done():
		return
	}

	e.mu.Lock()
	in := impact.Input{
		Repo: e.cfg.Repo, PRD: norm, Graph: e.graph, Gen: e.cfg.Gen,
		Gather: e.cfg.Gather, ReadCache: true, WriteCache: true,
	}
	if e.projectCtx != nil {
		in.Context = *e.projectCtx
	}
	e.mu.Unlock()

	if !e.publishImpact(seq, impact.Lexical(in)) {
		return
	}

	build := e.cfg.Impact
	if build == nil {
		build = impact.Build
	}
	m, err := build(ictx, in)
	if ictx.Err() != nil {
		return
	}
	if err != nil {
		e.log.Warn("impact map incomplete", "err", err)
		if m.State == "" {
			m = impact.Lexical(in)
			m.Notes = append(m.Notes, "the project could not be read this time: "+notice(err))
			m.State = contracts.ImpactFailed
		}
	}
	e.publishImpact(seq, m)
}

// publishImpact stores a map and its pill under the sequence guard. It reports
// false when a newer pass has started, so a slow answer never overwrites a
// fresher one.
func (e *executor) publishImpact(seq uint64, m contracts.ImpactMap) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if seq != e.impactSeq {
		return false
	}
	e.impactRev++
	m.Revision = e.impactRev
	e.impactMap = &m
	e.pills.Impact, e.pills.ImpactDetail = impactPill(m)
	return true
}

// cancelImpact stops any in-flight map pass. The map already published stays.
func (e *executor) cancelImpact() {
	e.mu.Lock()
	cancel := e.impactCancel
	e.impactCancel = nil
	e.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// ImpactMap returns the latest map, or an empty pending one.
func (e *executor) ImpactMap() contracts.ImpactMap {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.impactMap == nil {
		return emptyImpactMap()
	}
	return *e.impactMap
}

// emptyImpactMap is what a follower and a session before its first pass
// return. Every slice is present, so the window never decodes a null array.
func emptyImpactMap() contracts.ImpactMap {
	return contracts.ImpactMap{
		SchemaVersion: contracts.SchemaVersion,
		State:         contracts.ImpactPending,
		Whole:         []contracts.ImpactLink{},
		Epics:         []contracts.ImpactEpic{},
		Conflicts:     []contracts.ImpactLink{},
	}
}

// impactPill renders the Page 1 pill for a map, in the words a reader who does
// not read code would use.
func impactPill(m contracts.ImpactMap) (state, detail string) {
	parts := 0
	areas := map[string]bool{}
	for _, e := range m.Epics {
		for _, l := range e.Links {
			areas[l.Area] = true
		}
	}
	for _, l := range m.Whole {
		areas[l.Area] = true
	}
	parts = len(areas)

	switch m.State {
	case contracts.ImpactFailed:
		detail := "the project could not be read"
		if len(m.Notes) > 0 {
			detail = m.Notes[len(m.Notes)-1]
		}
		return impactFailed, detail
	case contracts.ImpactLexical:
		if parts == 0 {
			return impactQuick, "looking for the parts of the project this touches"
		}
		return impactQuick, fmt.Sprintf("%d possible match(es) while the full read finishes", parts)
	case contracts.ImpactReady:
		if parts == 0 {
			return impactOK, "nothing in this project matches these requirements yet"
		}
		if n := len(m.Conflicts); n > 0 {
			return impactOK, fmt.Sprintf("%d part(s) of the project, %d clash(es) with a decision already made", parts, n)
		}
		return impactOK, fmt.Sprintf("%d part(s) of the project", parts)
	default:
		return impactPending, impactPendingDetail
	}
}

// GET /api/impact — what the requirements touch.
func (s *appSession) handleImpact(w http.ResponseWriter, _ *http.Request) {
	if s.exec == nil {
		writeJSON(w, http.StatusOK, emptyImpactMap())
		return
	}
	writeJSON(w, http.StatusOK, s.exec.ImpactMap())
}
