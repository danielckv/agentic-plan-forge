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

// TestPacksOfferNoTestFile guards what the first live run got wrong: the model
// was handed test functions and reported them as parts of the project the work
// touches. A test is how a change is checked, not a thing the plan changes.
func TestPacksOfferNoTestFile(t *testing.T) {
	in := fixture()
	in.Graph.Symbols["internal/billing/export_test.go"] = []contracts.Symbol{
		sym("internal/billing/export_test.go", "TestExportInvoices", contracts.SymbolFunction, 7, true),
	}
	in.Graph.Symbols["web/src/billing/export.test.ts"] = []contracts.Symbol{
		sym("web/src/billing/export.test.ts", "exportInvoicesSuite", contracts.SymbolFunction, 3, true),
	}

	packs, _ := buildPacks(context.Background(), in)
	for _, c := range allCandidates(packs) {
		if c.Symbol.Path != "" && IsTestPath(c.Symbol.Path) {
			t.Errorf("a test file was offered as a part of the project: %s", c.Symbol.Path)
		}
		if c.Block.Path != "" && IsTestPath(c.Block.Path) {
			t.Errorf("a test directory was offered as a part of the project: %s", c.Block.Path)
		}
	}
}

func TestIsTestPath(t *testing.T) {
	for p, want := range map[string]bool{
		"internal/app/planfiles_test.go":      true,
		"web/src/protocol/schema.test.ts":     true,
		"web/src/ui/Dialog.spec.tsx":          true,
		"tests/test_billing.py":               true,
		"internal/app/planfiles.go":           false,
		"internal/billing/latest_export.go":   false,
		"web/src/pages/composer/PlanRail.tsx": false,
	} {
		if got := IsTestPath(p); got != want {
			t.Errorf("IsTestPath(%q) = %v, want %v", p, got, want)
		}
	}
}

// TestPacksOfferWholeBlocksTheKeywordsMissed is the recall half of the design:
// requirements written in business language match no declaration name, so the
// model must still be able to name the directory the work belongs in.
func TestPacksOfferWholeBlocksTheKeywordsMissed(t *testing.T) {
	in := fixture()
	in.Graph.Modules = []contracts.ModuleBoundary{
		{ID: "mod:go:internal/billing", Path: "internal/billing", Language: "go"},
		{ID: "mod:go:internal/mail", Path: "internal/mail", Language: "go"},
	}
	// Requirements a keyword search cannot connect to any declaration name.
	in.PRD.Epics = []contracts.DraftEpic{{
		ID: "E1", Title: "Tell people when something finished",
		Requirements: []string{"Somebody should hear about it once the work completes."},
	}}

	packs, _ := buildPacks(context.Background(), in)
	var blockPaths []string
	for _, c := range allCandidates(packs) {
		if c.Block.Path != "" {
			blockPaths = append(blockPaths, c.Block.Path)
		}
	}
	if len(blockPaths) == 0 {
		t.Fatal("no whole directory was offered; the model could only pick what the keywords reached")
	}
	found := false
	for _, p := range blockPaths {
		if p == "internal/mail" {
			found = true
		}
	}
	if !found {
		t.Errorf("blocks = %v, want internal/mail among them: no keyword reaches it, so only the block can", blockPaths)
	}

	// A directory already represented by a ranked declaration is not repeated.
	in.PRD.Epics = []contracts.DraftEpic{{ID: "E1", Title: "Invoice export", Requirements: []string{"Export invoices"}}}
	packs, _ = buildPacks(context.Background(), in)
	for _, c := range allCandidates(packs) {
		if c.Block.Path == "internal/billing" {
			t.Error("internal/billing was offered whole as well as by declaration")
		}
	}
}

func TestBlockCandidatesResolveToTheirDirectory(t *testing.T) {
	in := fixture()
	in.Graph.Modules = []contracts.ModuleBoundary{{ID: "mod:go:internal/mail", Path: "internal/mail", Language: "go"}}
	in.PRD.Epics = []contracts.DraftEpic{{ID: "E1", Title: "Notify", Requirements: []string{"Somebody hears about it."}}}
	packs, _ := buildPacks(context.Background(), in)

	var blockID string
	for _, c := range allCandidates(packs) {
		if c.Block.Path == "internal/mail" {
			blockID = c.ID
		}
	}
	if blockID == "" {
		t.Fatal("internal/mail was not offered as a block")
	}

	links, _ := resolve(packs, wireMap{SchemaVersion: 1, Entries: []wireEntry{
		{EpicID: "E1", CandidateID: blockID, Area: "Notifications", Verdict: contracts.ImpactChange,
			Reason: "Messages to people are sent from here, and this adds one more.", Confidence: "HIGH"},
	}})
	got := links["E1"]
	if len(got) != 1 || got[0].Path != "internal/mail" || got[0].Area != "Notifications" {
		t.Fatalf("block link = %+v, want it pointing at internal/mail", got)
	}
	if got[0].SymbolName != "" || got[0].Line != 0 {
		t.Errorf("a whole directory must not claim a declaration or a line: %+v", got[0])
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
