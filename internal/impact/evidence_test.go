//go:build client && !server

package impact

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/gather"
)

// wideGather stands in for a repository with more decision records than the
// whole-PRD context can carry.
func wideGather(hits ...contracts.ContextHit) func(context.Context, gather.Options) (contracts.ProjectContext, error) {
	return func(_ context.Context, _ gather.Options) (contracts.ProjectContext, error) {
		return contracts.ProjectContext{Hits: hits}, nil
	}
}

func allCandidates(packs []pack) []candidate {
	var out []candidate
	for _, p := range packs {
		out = append(out, p.Candidates...)
	}
	return out
}

func TestBuildPacksNumbersCandidatesDenselyAndUniquely(t *testing.T) {
	packs, _ := buildPacks(context.Background(), fixture())
	if len(packs) == 0 {
		t.Fatal("no packs")
	}
	seen := map[string]bool{}
	for i, c := range allCandidates(packs) {
		want := "c" + string(rune('0'+i+1))
		if i < 9 && c.ID != want {
			t.Errorf("candidate %d id = %q, want %q", i, c.ID, want)
		}
		if seen[c.ID] {
			t.Fatalf("candidate id %q is used twice; the model's answer would be ambiguous", c.ID)
		}
		seen[c.ID] = true
	}
}

func TestBuildPacksIsDeterministic(t *testing.T) {
	in := fixture()
	first, _ := buildPacks(context.Background(), in)
	second, _ := buildPacks(context.Background(), in)
	if a, b := packsDigest(first), packsDigest(second); a != b {
		t.Errorf("two passes digest to %q and %q; the evidence is not stable", a, b)
	}
	if packsDigest(first) == "" {
		t.Error("the digest is empty")
	}
}

func TestPacksDigestMovesWithTheEvidence(t *testing.T) {
	base, _ := buildPacks(context.Background(), fixture())
	want := packsDigest(base)

	changed := fixture()
	changed.Graph.Symbols["internal/billing/export.go"] = []contracts.Symbol{
		sym("internal/billing/export.go", "ExportInvoices", contracts.SymbolFunction, 99, true),
	}
	moved, _ := buildPacks(context.Background(), changed)
	if packsDigest(moved) == want {
		t.Error("a declaration moved to another line and the digest did not change")
	}

	reworded := fixture()
	reworded.PRD.Epics[0].Requirements = []string{"Export invoices for a chosen month"}
	rw, _ := buildPacks(context.Background(), reworded)
	if packsDigest(rw) == want {
		t.Error("a requirement changed and the digest did not change")
	}
}

// TestBuildPacksReachesDecisionsTheWholePRDContextMissed is the reason this
// stage gathers per epic rather than reusing the executor's context.
func TestBuildPacksReachesDecisionsTheWholePRDContextMissed(t *testing.T) {
	in := fixture()
	// The executor's context holds an unrelated note only.
	in.Context = contracts.ProjectContext{Hits: []contracts.ContextHit{
		{Path: "docs/plans/search.md", Heading: "Search", Text: "Ranking results.", Score: 1},
	}}
	in.Gather = wideGather(
		contracts.ContextHit{Path: "docs/adrs/0004-invoices.md", Heading: "Invoice export format",
			Text: "Invoices export as CSV and never as a spreadsheet.", Score: 9},
	)

	packs, _ := buildPacks(context.Background(), in)
	found := false
	for _, c := range allCandidates(packs) {
		if c.Kind == contracts.ImpactKindDecision && c.Hit.Path == "docs/adrs/0004-invoices.md" {
			found = true
		}
	}
	if !found {
		t.Error("the decision record that contradicts the epic was never offered as a candidate")
	}
}

func TestGatherPerEpicFallsBackToTheGatheredContext(t *testing.T) {
	in := fixture()
	in.Gather = func(context.Context, gather.Options) (contracts.ProjectContext, error) {
		return contracts.ProjectContext{}, errors.New("recall index unreadable")
	}
	terms := gather.QueryTerms(epicText(in.PRD.Epics[0]), maxTerms)
	hits := gatherPerEpic(context.Background(), in, epicText(in.PRD.Epics[0]), terms, MaxDocCandidates)
	if len(hits) != 1 || hits[0].Path != "docs/adrs/0004-invoices.md" {
		t.Errorf("fallback hits = %+v, want the invoice ADR from the executor's context", hits)
	}
}

func TestPacksCarryNoMachinePathAndNoPrivateSymbol(t *testing.T) {
	in := fixture()
	in.Repo = "/Users/someone/Develop/billing"
	packs, _ := buildPacks(context.Background(), in)
	raw, err := json.Marshal(packsWire(packs))
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{in.Repo, "/Users/", "writeRow"} {
		if strings.Contains(string(raw), leak) {
			t.Errorf("the payload carries %q:\n%s", leak, raw)
		}
	}
}

func TestBatchPacksRespectsTheEvidenceBudget(t *testing.T) {
	var packs []pack
	for i := 0; i < 12; i++ {
		p := pack{EpicID: "E" + strconv.Itoa(i), Title: "Invoice export"}
		for j := 0; j < MaxSymbolCandidates; j++ {
			p.Candidates = append(p.Candidates, candidate{
				ID: "c" + strconv.Itoa(i*100+j), Kind: contracts.ImpactKindCode,
				Symbol: sym("internal/billing/export.go", strings.Repeat("Export", 8), contracts.SymbolFunction, j, true),
			})
		}
		packs = append(packs, p)
	}
	batches := batchPacks(packs, MaxEvidenceChars)
	if len(batches) < 2 {
		t.Fatalf("batches = %d, want the budget to force more than one call", len(batches))
	}
	seen := 0
	for _, b := range batches {
		seen += len(b)
		cost := 0
		for _, p := range b {
			cost += p.chars()
		}
		if len(b) > 1 && cost > MaxEvidenceChars {
			t.Errorf("a multi-pack batch costs %d, over the %d budget", cost, MaxEvidenceChars)
		}
	}
	if seen != len(packs) {
		t.Errorf("batching kept %d of %d packs", seen, len(packs))
	}
}

func TestHalveBatchTerminates(t *testing.T) {
	b := []pack{{EpicID: "E1"}, {EpicID: "E2"}, {EpicID: "E3"}}
	halves, ok := halveBatch(b)
	if !ok || len(halves) != 2 || len(halves[0])+len(halves[1]) != 3 {
		t.Fatalf("halveBatch = %+v, %v", halves, ok)
	}
	if _, ok := halveBatch([]pack{{EpicID: "E1"}}); ok {
		t.Error("a one-pack batch reported as splittable; the retry would loop")
	}
}

func TestShrinkPackTerminates(t *testing.T) {
	p := pack{EpicID: "E1", Title: "Invoice export"}
	for i := 0; i < 20; i++ {
		p.Candidates = append(p.Candidates, candidate{
			ID: "c" + strconv.Itoa(i), Kind: contracts.ImpactKindCode,
			Symbol: sym("internal/billing/export.go", strings.Repeat("Export", 10), contracts.SymbolFunction, i, true),
		})
	}
	n := 0
	for {
		next, ok := shrinkPack(p)
		if !ok {
			break
		}
		if len(next.Candidates) >= len(p.Candidates) {
			t.Fatal("shrinking did not reduce the pack")
		}
		p = next
		if n++; n > 20 {
			t.Fatal("shrinking never terminated")
		}
	}
	if len(p.Candidates) == 0 {
		t.Error("shrinking emptied the pack instead of stopping at the floor")
	}
}
