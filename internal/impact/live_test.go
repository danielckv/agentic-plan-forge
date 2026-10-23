//go:build client && !server

package impact

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/ast"
	"github.com/danielckv/agentic-plan-forge/internal/config"
	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/host"
)

// TestLiveMeasureEvidenceBudget sizes MaxEvidenceChars against a real host.
//
// It is skipped unless PLAN_FORGE_LIVE_IMPACT=1, because it spends a model
// call. ADR 0004 established the rule it measures: a stage's input bound comes
// from the 180 s call timeout, not from the model's context window, and the
// number belongs in an ADR rather than in a guess. Run it against this
// repository, which is its own best fixture:
//
//	PLAN_FORGE_LIVE_IMPACT=1 go test -tags client -run TestLiveMeasure \
//	    -timeout 20m -v ./internal/impact/
func TestLiveMeasureEvidenceBudget(t *testing.T) {
	if os.Getenv("PLAN_FORGE_LIVE_IMPACT") != "1" {
		t.Skip("set PLAN_FORGE_LIVE_IMPACT=1 to spend a model call measuring the evidence budget")
	}
	repo, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root, err := ast.RepoRoot(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	graph, err := ast.Scan(ctx, ast.ScanOptions{Root: root, ReadCache: true, SkipDocDrift: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("scanned %d files, %d modules", graph.Coverage.FilesScanned, len(graph.Modules))

	cfg := config.Default()
	if loaded, lerr := config.Load(""); lerr == nil {
		cfg = loaded
	}
	// The host and model are overridable, so the measurement can be repeated on
	// the same slot ADR 0004 used.
	invoked := host.Kind(os.Getenv("PLAN_FORGE_LIVE_HOST"))
	if m := os.Getenv("PLAN_FORGE_LIVE_MODEL"); m != "" {
		cfg.DefaultMainModel = m
	}
	t.Logf("host=%q main model=%q", invoked, cfg.DefaultMainModel)
	gen := host.NewRouter(cfg, invoked)

	in := Input{Repo: root, Graph: graph, Gen: gen, PRD: livePRD()}
	packs, notes := buildPacks(ctx, in)
	for _, p := range packs {
		t.Logf("pack %-28q %5d chars, %2d candidates", packName(p), p.chars(), len(p.Candidates))
	}
	batches := batchPacks(packs, MaxEvidenceChars)
	t.Logf("notes: %v", notes)
	t.Logf("%d packs -> %d batches at MaxEvidenceChars=%d", len(packs), len(batches), MaxEvidenceChars)

	for i, b := range batches {
		cost := 0
		for _, p := range b {
			cost += p.chars()
		}
		start := time.Now()
		links, notes, err := callWithRetries(ctx, in, b, i+1, len(batches))
		elapsed := time.Since(start)
		t.Logf("batch %d of %d: %d packs, %d chars -> %v (err=%v)", i+1, len(batches), len(b), cost, elapsed.Round(time.Second), err)
		if err != nil {
			t.Errorf("batch %d failed: %v", i+1, err)
			continue
		}
		if elapsed > contracts.DefaultGenerateTimeout {
			t.Errorf("batch %d took %v, over the %v call timeout: MaxEvidenceChars is too high", i+1, elapsed, contracts.DefaultGenerateTimeout)
		}
		for _, n := range notes {
			t.Logf("  note: %s", n)
		}
		for epicID, ls := range links {
			for _, l := range ls {
				where := l.Path
				if l.SymbolName != "" {
					where = l.SymbolName + " — " + where
				}
				t.Logf("  [%s] %-14s %-22s %s\n        %s", epicID, l.Verdict, l.Area, where, l.Reason)
			}
		}
	}
}

// livePRD is a deliberately non-technical brief about this repository's own
// Page 1, naming no identifier that appears in the source. A keyword match
// should find little; the model should find internal/app, internal/ast and
// internal/gather.
func livePRD() contracts.NormalizedPRD {
	return contracts.NormalizedPRD{
		Revision: 1,
		Product: contracts.ProductPRD{
			Title:   "Show what a plan touches",
			Problem: "A planner has to read code to find out which parts of the project a requirement affects.",
			Goals:   []string{"A planner sees what the work touches without reading code."},
			Actors:  []string{"planner", "product manager"},
			Requirements: []string{
				"Before the avatars start, the planner sees which parts of the project the requirements affect.",
				"Each part is explained in one sentence a non-technical reader understands.",
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
