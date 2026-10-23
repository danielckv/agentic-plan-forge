//go:build client && !server

package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/ast"
	"github.com/danielckv/agentic-plan-forge/internal/config"
	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/host"
	"github.com/danielckv/agentic-plan-forge/internal/impact"
)

// TestLiveImpactEndToEnd drives the whole Page 1 chain against the real
// repository and a real host: scan, gather, interpret-free map pass, publish,
// cache, and the second pass that must cost nothing.
//
// It is skipped unless PLAN_FORGE_LIVE_IMPACT=1, because it spends model calls.
//
//	PLAN_FORGE_LIVE_IMPACT=1 PLAN_FORGE_LIVE_HOST=claude PLAN_FORGE_LIVE_MODEL=opus \
//	    go test -tags client -run TestLiveImpact -timeout 20m -v ./internal/app/
func TestLiveImpactEndToEnd(t *testing.T) {
	if os.Getenv("PLAN_FORGE_LIVE_IMPACT") != "1" {
		t.Skip("set PLAN_FORGE_LIVE_IMPACT=1 to spend model calls on the end-to-end map")
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repo, err := ast.RepoRoot(context.Background(), wd)
	if err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	if loaded, lerr := config.Load(""); lerr == nil {
		cfg = loaded
	}
	if m := os.Getenv("PLAN_FORGE_LIVE_MODEL"); m != "" {
		cfg.DefaultMainModel = m
	}
	gen := host.NewRouter(cfg, host.Kind(os.Getenv("PLAN_FORGE_LIVE_HOST")))

	exec := newExecutor(executorConfig{Repo: repo, Config: cfg, Gen: gen, Logger: quietLogger()})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	go exec.scan(ctx)

	cachePath := filepath.Join(repo, filepath.FromSlash(impact.CachePath))
	before := time.Now()

	norm := livePRD()
	exec.refreshGrounding(ctx, norm)

	m := exec.ImpactMap()
	t.Logf("first pass in %v: state=%s revision=%d", time.Since(before).Round(time.Second), m.State, m.Revision)
	t.Logf("summary: %s", m.Summary)
	for _, e := range m.Epics {
		for _, l := range e.Links {
			t.Logf("  [%s] %-8s %-40s %s", e.EpicID, l.Verdict, l.Area, l.Path)
			if l.Reason != "" {
				t.Logf("        %s", l.Reason)
			}
		}
	}
	for _, n := range m.Notes {
		t.Logf("  note: %s", n)
	}

	if m.State != contracts.ImpactReady {
		t.Errorf("state = %q, want %q: the model pass did not complete", m.State, contracts.ImpactReady)
	}
	if m.Revision < 2 {
		t.Errorf("revision = %d, want at least 2: the keyword map must be published before the model map", m.Revision)
	}
	model := 0
	for _, e := range m.Epics {
		for _, l := range e.Links {
			if l.Source == contracts.ImpactSourceModel {
				model++
			}
			if l.Path != "" {
				if _, err := os.Stat(filepath.Join(repo, filepath.FromSlash(l.Path))); err != nil {
					t.Errorf("link %s points at %q, which does not exist", l.ID, l.Path)
				}
			}
			if impact.IsTestPath(l.Path) {
				t.Errorf("link %s points at a test file: %s", l.ID, l.Path)
			}
		}
	}
	if model == 0 {
		t.Error("no model link survived; the map fell back to keyword matches")
	}

	// The status pill speaks to the window.
	status := exec.Status()
	t.Logf("pill: %s — %s (revision %d)", status.Impact, status.ImpactDetail, status.ImpactRevision)
	if status.Impact != impactOK || status.ImpactRevision != m.Revision {
		t.Errorf("pill = %+v, want ok at revision %d", status, m.Revision)
	}

	// The cache landed, and git does not see it.
	info, err := os.Stat(cachePath)
	if err != nil {
		t.Fatalf("the map was not cached at %s: %v", impact.CachePath, err)
	}
	t.Logf("cache: %s, %d bytes", impact.CachePath, info.Size())

	// A second pass over the same interpretation and the same working tree must
	// not call the model again.
	second := time.Now()
	exec.refreshImpact(ctx, norm)
	elapsed := time.Since(second)
	t.Logf("second pass in %v", elapsed.Round(time.Millisecond))
	if elapsed > 20*time.Second {
		t.Errorf("the second pass took %v; the cache did not answer", elapsed)
	}
	if got := exec.ImpactMap(); got.State != contracts.ImpactReady || got.Summary != m.Summary {
		t.Errorf("cached map differs: %q vs %q", got.Summary, m.Summary)
	}
}

// livePRD is a deliberately non-technical brief about this repository's own
// first page, naming no identifier that appears in the source.
func livePRD() contracts.NormalizedPRD {
	return contracts.NormalizedPRD{
		Revision: 1,
		Product: contracts.ProductPRD{
			Title:   "Show what a plan touches",
			Problem: "A planner has to read code to find out which parts of the project a requirement affects.",
			Goals:   []string{"A planner sees what the work touches without reading code."},
			Actors:  []string{"planner"},
			Requirements: []string{
				"Before the avatars start, the planner sees which parts of the project the requirements affect.",
				"A requirement that contradicts a decision the team already recorded is flagged.",
			},
			AcceptanceCriteria: []string{"A planner who cannot read code can say what will change."},
		},
		Epics: []contracts.DraftEpic{
			{
				ID: "E1", Title: "Show what the work touches",
				Goal:               "A planner sees the affected parts of the project in plain language.",
				Requirements:       []string{"Before the avatars start, the planner sees which parts of the project the requirements affect."},
				AcceptanceCriteria: []string{"A planner who cannot read code can say what will change."},
			},
			{
				ID: "E2", Title: "Flag contradictions with past decisions",
				Goal:               "A requirement that reopens a settled decision is called out.",
				Requirements:       []string{"A requirement that contradicts a decision the team already recorded is flagged."},
				AcceptanceCriteria: []string{"The contradicted decision is named."},
			},
		},
	}
}
