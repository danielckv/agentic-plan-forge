//go:build client && !server

package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/ast"
	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/impact"
)

func readyMap(summary string) contracts.ImpactMap {
	return contracts.ImpactMap{
		SchemaVersion: contracts.SchemaVersion, State: contracts.ImpactReady, Summary: summary,
		Whole: []contracts.ImpactLink{}, Conflicts: []contracts.ImpactLink{},
		Epics: []contracts.ImpactEpic{{EpicID: "E1", Title: "Invoice export", Links: []contracts.ImpactLink{
			{ID: "E1:code", Area: "Invoice export", Kind: contracts.ImpactKindCode, Path: "internal/billing/export.go",
				Verdict: contracts.ImpactChange, Source: contracts.ImpactSourceModel, Reason: "A sentence."},
		}}},
	}
}

// mappedExecutor is a planned executor whose scan has already finished, so a
// map pass does not block on it.
func mappedExecutor(t *testing.T, build func(context.Context, impact.Input) (contracts.ImpactMap, error)) *executor {
	t.Helper()
	exec := plannedExecutor(t)
	exec.cfg.Impact = build
	exec.cfg.Scan = func(context.Context, ast.ScanOptions) (*contracts.RepoContextGraph, error) {
		return exec.graph, nil
	}
	exec.lastScan = time.Now()
	close(exec.scanDone)
	return exec
}

// TestRefreshImpactPaintsKeywordsBeforeTheModelAnswers is the behaviour Page 1
// depends on: the rail is never empty while the model reads, and the window is
// told to refetch twice, because the interpretation revision does not move
// when the model result replaces the keyword ranking.
func TestRefreshImpactPaintsKeywordsBeforeTheModelAnswers(t *testing.T) {
	release := make(chan struct{})
	first := make(chan contracts.ImpactMap, 1)
	var exec *executor
	exec = mappedExecutor(t, func(context.Context, impact.Input) (contracts.ImpactMap, error) {
		// What the window would see right now, before the model has answered.
		first <- exec.ImpactMap()
		<-release
		return readyMap("the model answered"), nil
	})

	done := make(chan struct{})
	go func() {
		exec.refreshImpact(context.Background(), *exec.norm)
		close(done)
	}()

	select {
	case m := <-first:
		if m.State != contracts.ImpactLexical {
			t.Errorf("state while the model reads = %q, want %q", m.State, contracts.ImpactLexical)
		}
		if len(m.Epics) == 0 {
			t.Error("the rail was empty while the model read")
		}
		if exec.Status().ImpactRevision == 0 {
			t.Error("the window was never told to fetch the first paint")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the keyword map was never published")
	}

	beforeModel := exec.Status().ImpactRevision
	close(release)
	<-done

	final := exec.ImpactMap()
	if final.State != contracts.ImpactReady || final.Summary != "the model answered" {
		t.Errorf("final map = %+v", final)
	}
	if exec.Status().ImpactRevision <= beforeModel {
		t.Error("the revision did not move when the model result landed; the window would show the first paint forever")
	}
}

func TestRefreshImpactLetsTheNewerPassWin(t *testing.T) {
	// One closure for both passes: the first blocks until the second has
	// finished, so the slow answer always comes back last.
	var passes atomic.Int32
	slow := make(chan struct{})
	exec := mappedExecutor(t, func(ctx context.Context, _ impact.Input) (contracts.ImpactMap, error) {
		if passes.Add(1) == 1 {
			<-slow
			return readyMap("the stale answer"), ctx.Err()
		}
		return readyMap("the current answer"), nil
	})

	done := make(chan struct{})
	go func() {
		exec.refreshImpact(context.Background(), *exec.norm)
		close(done)
	}()
	waitFor(t, func() bool { return passes.Load() == 1 })

	// A newer interpretation starts and finishes first.
	exec.refreshImpact(context.Background(), *exec.norm)

	close(slow)
	<-done

	if got := exec.ImpactMap().Summary; got != "the current answer" {
		t.Errorf("summary = %q, want the newer pass to win", got)
	}
}

func TestRefreshImpactCancelsTheCallWhenANewerPassStarts(t *testing.T) {
	cancelled := make(chan struct{}, 1)
	started := make(chan struct{}, 1)
	exec := mappedExecutor(t, func(ctx context.Context, _ impact.Input) (contracts.ImpactMap, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-ctx.Done()
		cancelled <- struct{}{}
		return contracts.ImpactMap{}, ctx.Err()
	})

	go exec.refreshImpact(context.Background(), *exec.norm)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the first pass never started")
	}

	exec.cancelImpact()
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("the in-flight call was not cancelled; a superseded PRD would still be billed")
	}
}

func TestImpactPillSpeaksPlainly(t *testing.T) {
	for name, tc := range map[string]struct {
		m             contracts.ImpactMap
		state, needle string
	}{
		"before anything": {contracts.ImpactMap{}, impactPending, "reading the code"},
		"keyword matches": {
			contracts.ImpactMap{State: contracts.ImpactLexical, Epics: []contracts.ImpactEpic{
				{Links: []contracts.ImpactLink{{Area: "Invoice export"}}},
			}}, impactQuick, "while the full read finishes",
		},
		"mapped": {readyMap(""), impactOK, "1 part(s) of the project"},
		"mapped with a clash": {
			contracts.ImpactMap{State: contracts.ImpactReady,
				Whole:     []contracts.ImpactLink{{Area: "Export format"}},
				Conflicts: []contracts.ImpactLink{{Area: "Export format"}}},
			impactOK, "clash(es) with a decision already made",
		},
		"nothing matched": {
			contracts.ImpactMap{State: contracts.ImpactReady}, impactOK, "nothing in this project matches",
		},
		"failed": {
			contracts.ImpactMap{State: contracts.ImpactFailed, Notes: []string{"the host is not signed in"}},
			impactFailed, "not signed in",
		},
	} {
		t.Run(name, func(t *testing.T) {
			state, detail := impactPill(tc.m)
			if state != tc.state {
				t.Errorf("state = %q, want %q", state, tc.state)
			}
			if !strings.Contains(detail, tc.needle) {
				t.Errorf("detail = %q, want it to mention %q", detail, tc.needle)
			}
			for _, jargon := range []string{"AST", "symbol", "repository graph", "worktree"} {
				if strings.Contains(detail, jargon) {
					t.Errorf("detail = %q uses %q, which the reader does not know", detail, jargon)
				}
			}
		})
	}
}

func TestRescanReplacesTheGraphOnlyWhenTheCodeMoved(t *testing.T) {
	exec := plannedExecutor(t)
	exec.graph.WorktreeHash = "wt-1"
	exec.graph.DocDriftReports = []contracts.DocDriftItem{{ID: "D1"}}
	close(exec.scanDone)

	calls := 0
	next := &contracts.RepoContextGraph{WorktreeHash: "wt-1"}
	exec.cfg.Scan = func(context.Context, ast.ScanOptions) (*contracts.RepoContextGraph, error) {
		calls++
		return next, nil
	}

	exec.lastScan = time.Now().Add(-2 * rescanInterval)
	exec.rescan(context.Background())
	if calls != 1 {
		t.Fatalf("scans = %d, want a stale scan to be refreshed", calls)
	}
	if exec.graph.DocDriftReports == nil || len(exec.graph.DocDriftReports) != 1 {
		t.Error("an unchanged tree lost its graph")
	}

	// A fresh scan is not repeated, so a typing burst does not thrash.
	exec.rescan(context.Background())
	if calls != 1 {
		t.Errorf("scans = %d, want the interval to hold", calls)
	}

	// The code moved: the graph is replaced, and the drift the re-read skipped
	// is carried over rather than blanking the doc-health pill.
	exec.lastScan = time.Now().Add(-2 * rescanInterval)
	next = &contracts.RepoContextGraph{WorktreeHash: "wt-2"}
	exec.rescan(context.Background())
	if exec.graph.WorktreeHash != "wt-2" {
		t.Errorf("worktree hash = %q, want the new scan", exec.graph.WorktreeHash)
	}
	if len(exec.graph.DocDriftReports) != 1 {
		t.Error("the carried-over drift reports were dropped")
	}
}

func TestImpactRoute(t *testing.T) {
	s := &appSession{exec: mappedExecutor(t, nil)}
	rec := httptest.NewRecorder()
	s.handleImpact(rec, httptest.NewRequest(http.MethodGet, "/api/impact", nil))
	if rec.Code != http.StatusOK || !json.Valid(rec.Body.Bytes()) {
		t.Fatalf("/api/impact = %d %s", rec.Code, rec.Body.String())
	}

	follower := &appSession{}
	rec = httptest.NewRecorder()
	follower.handleImpact(rec, httptest.NewRequest(http.MethodGet, "/api/impact", nil))
	var m contracts.ImpactMap
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m.State != contracts.ImpactPending || m.Whole == nil || m.Epics == nil || m.Conflicts == nil {
		t.Errorf("a follower got %+v, want a pending map with no null arrays", m)
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition never held")
}
