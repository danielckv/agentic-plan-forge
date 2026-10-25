//go:build client && !server

package avatars_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/danielckv/agentic-plan-forge/internal/ast"
	"github.com/danielckv/agentic-plan-forge/internal/avatars"
	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/synthesizer/synthtest"
)

// widen spreads extra exported symbols over four module trees, two of which
// the sample fixture's workstreams own, so a specialist's share of the
// repository is about a quarter of it. The fixture's own symbols are kept:
// a specialist draft names them, and the validator checks every target.
func widen(base contracts.RepoContextGraph, filesPerModule int) contracts.RepoContextGraph {
	g := base
	g.Symbols = map[string][]contracts.Symbol{}
	for p, syms := range base.Symbols {
		g.Symbols[p] = syms
	}
	for _, module := range []string{"internal/billing", "web/src", "internal/shipping", "internal/identity"} {
		for f := range filesPerModule {
			p := fmt.Sprintf("%s/file%02d.go", module, f)
			syms := make([]contracts.Symbol, 0, 10)
			for i := range 10 {
				syms = append(syms, contracts.Symbol{
					ID:            fmt.Sprintf("%s#S%d", p, i),
					Name:          fmt.Sprintf("Exported%s%d", strings.ToUpper(module[:1]), i),
					QualifiedName: fmt.Sprintf("%s.Exported%d", module, i),
					Kind:          contracts.SymbolFunction,
					Language:      "go",
					Path:          p,
					Visibility:    ast.VisibilityPublic,
					Signature:     "func Exported(ctx context.Context, in Request) (Response, error)",
					StartLine:     i * 12,
					EndLine:       i*12 + 9,
				})
			}
			g.Symbols[p] = syms
		}
	}
	return g
}

// graphOf decodes the repository graph out of a recorded stage payload.
func graphOf(t *testing.T, payload json.RawMessage) contracts.RepoContextGraph {
	t.Helper()
	var p struct {
		Graph contracts.RepoContextGraph `json:"repository_graph"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		t.Fatalf("payload does not decode: %v", err)
	}
	return p.Graph
}

// A specialist plans one workstream. It is given the module paths that
// workstream owns, not the whole repository the architect designed across:
// specialists run concurrently, so a graph that is mostly irrelevant is paid
// for in the input tokens and the latency of every one of those calls at once.
func TestSpecialistSeesItsOwnWorkstreamsCodeNotTheWholeRepository(t *testing.T) {
	h := newHarness(t, synthtest.Sample())
	h.in.Graph = widen(h.in.Graph, 14)

	if _, err := h.run(context.Background(), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}

	arch := h.stub.callsOf("architect")
	if len(arch) == 0 {
		t.Fatal("the architect never ran")
	}
	archGraph := graphOf(t, arch[0].Req.UserPayload)
	archFiles := len(archGraph.Symbols)
	if archFiles == 0 {
		t.Fatal("the architect received no graph")
	}

	for _, tc := range []struct{ key, owns, foreign string }{
		{"specialist:billing-api", "internal/billing/", "internal/shipping/"},
		{"specialist:web-ui", "web/src/", "internal/identity/"},
	} {
		cs := h.stub.callsOf(tc.key)
		if len(cs) == 0 {
			t.Fatalf("%s never ran", tc.key)
		}
		g := graphOf(t, cs[0].Req.UserPayload)
		owned, foreign := 0, 0
		for p := range g.Symbols {
			switch {
			case strings.HasPrefix(p, tc.owns):
				owned++
			case strings.HasPrefix(p, tc.foreign):
				foreign++
			}
		}
		t.Logf("%s: %d files (%d owned, %d from %s); architect saw %d",
			tc.key, len(g.Symbols), owned, foreign, tc.foreign, archFiles)

		if owned == 0 {
			t.Errorf("%s was given none of its own module %s", tc.key, tc.owns)
		}
		if len(g.Symbols) >= archFiles {
			t.Errorf("%s saw %d files and the architect saw %d; the specialist graph was not scoped",
				tc.key, len(g.Symbols), archFiles)
		}
		if owned <= foreign {
			t.Errorf("%s saw %d owned files and %d from %s; its own module must rank first",
				tc.key, owned, foreign, tc.foreign)
		}
	}
}

// Scoping ranks, it does not filter: a workstream that declares no module
// paths still gets a graph, bounded by the specialist budget.
func TestSpecialistWithNoModulePathsStillGetsAGraph(t *testing.T) {
	fx := synthtest.Sample()
	for i := range fx.Architect.Workstreams {
		fx.Architect.Workstreams[i].ModulePaths = nil
	}
	h := newHarness(t, fx)
	h.in.Graph = widen(h.in.Graph, 6)

	if _, err := h.run(context.Background(), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	cs := h.stub.callsOf("specialist:billing-api")
	if len(cs) == 0 {
		t.Fatal("the specialist never ran")
	}
	if g := graphOf(t, cs[0].Req.UserPayload); len(g.Symbols) == 0 {
		t.Error("a workstream with no module paths was given an empty graph")
	}
}

// The budget is a bound, not a target: a repository small enough to fit is
// handed over whole, so a small project loses nothing to scoping.
func TestASmallRepositoryReachesTheSpecialistWhole(t *testing.T) {
	h := newHarness(t, synthtest.Sample())
	small := widen(h.in.Graph, 1)
	h.in.Graph = small

	if _, err := h.run(context.Background(), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	cs := h.stub.callsOf("specialist:billing-api")
	if len(cs) == 0 {
		t.Fatal("the specialist never ran")
	}
	// The yardstick is an unscoped snapshot of the same graph: Snapshot keeps
	// only the files that carry exported symbols or routes, so the raw map is
	// not what "whole" means here.
	whole, info := ast.Snapshot(&small, ast.SnapshotOptions{})
	if info.Truncated || info.SerializedBytes > avatars.SpecialistGraphBudgetBytes {
		t.Fatalf("the fixture must fit the specialist budget to test this: %d bytes against %d, truncated=%v",
			info.SerializedBytes, avatars.SpecialistGraphBudgetBytes, info.Truncated)
	}
	g := graphOf(t, cs[0].Req.UserPayload)
	if len(g.Symbols) != len(whole.Symbols) {
		t.Errorf("a graph of %d files fitting %d bytes reached the specialist as %d files; it must arrive whole",
			len(whole.Symbols), avatars.SpecialistGraphBudgetBytes, len(g.Symbols))
	}
}
