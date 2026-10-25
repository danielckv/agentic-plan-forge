//go:build client && !server

package ast

import (
	"strings"
	"testing"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// snapshotGraph builds a graph with enough symbols per file that a small
// budget has to drop some, so the ranking is what decides who survives.
func snapshotGraph(paths ...string) *contracts.RepoContextGraph {
	g := &contracts.RepoContextGraph{
		SchemaVersion: contracts.SchemaVersion,
		Symbols:       map[string][]contracts.Symbol{},
	}
	for _, p := range paths {
		syms := make([]contracts.Symbol, 0, 12)
		for i := range 12 {
			syms = append(syms, contracts.Symbol{
				Name:          "Handler" + string(rune('A'+i)),
				QualifiedName: p + ".Handler" + string(rune('A'+i)),
				Kind:          contracts.SymbolFunction,
				Signature:     "func Handler(ctx context.Context, in Request) (Response, error)",
				Path:          p,
				Visibility:    VisibilityPublic,
				StartLine:     i * 10,
			})
		}
		g.Symbols[p] = syms
	}
	return g
}

func filesIn(g contracts.RepoContextGraph) []string {
	out := make([]string, 0, len(g.Symbols))
	for p := range g.Symbols {
		out = append(out, p)
	}
	return out
}

func has(files []string, want string) bool {
	for _, f := range files {
		if f == want {
			return true
		}
	}
	return false
}

func TestOwnedPathsOutrankKeywordMatchesWhenTheBudgetCuts(t *testing.T) {
	g := snapshotGraph(
		"internal/dispatch/plan.go",
		"internal/dispatch/assign.go",
		"web/src/pages/Report.tsx",
		"internal/billing/invoice.go",
	)
	// "report" matches the web file by name; the workstream owns dispatch.
	full, info := Snapshot(g, SnapshotOptions{Keywords: []string{"report"}})
	if info.Truncated {
		t.Fatalf("the fixture must fit the default budget; it did not: %+v", info)
	}
	if len(full.Symbols) != 4 {
		t.Fatalf("full snapshot has %d files, want 4", len(full.Symbols))
	}

	// Half the full size forces a cut, so ranking decides.
	half := info.SerializedBytes / 2
	scoped, sInfo := Snapshot(g, SnapshotOptions{
		Keywords: []string{"report"},
		Paths:    []string{"internal/dispatch"},
		MaxBytes: half,
	})
	if !sInfo.Truncated {
		t.Fatalf("a budget of %d bytes against %d must cut", half, info.SerializedBytes)
	}
	files := filesIn(scoped)
	for _, want := range []string{"internal/dispatch/plan.go", "internal/dispatch/assign.go"} {
		if !has(files, want) {
			t.Errorf("the owned file %s was cut; kept %v", want, files)
		}
	}
	if has(files, "internal/billing/invoice.go") && !has(files, "internal/dispatch/assign.go") {
		t.Errorf("an unowned file outranked an owned one: %v", files)
	}
}

func TestOwnedPathsMatchOnAPathBoundary(t *testing.T) {
	g := snapshotGraph(
		"internal/api/server.go",
		"internal/apikeys/rotate.go",
		"internal/other/thing.go",
	)
	_, info := Snapshot(g, SnapshotOptions{})
	scoped, _ := Snapshot(g, SnapshotOptions{
		Paths:    []string{"internal/api"},
		MaxBytes: info.SerializedBytes / 3,
	})
	files := filesIn(scoped)
	if !has(files, "internal/api/server.go") {
		t.Errorf("the owned file was cut; kept %v", files)
	}
	if has(files, "internal/apikeys/rotate.go") {
		t.Errorf("internal/apikeys matched the prefix internal/api; kept %v", files)
	}
}

func TestOwnedPathsRankRatherThanFilter(t *testing.T) {
	g := snapshotGraph("internal/dispatch/plan.go", "internal/billing/invoice.go")
	// A budget that fits everything keeps everything, owned or not: a
	// specialist still needs the contracts it consumes elsewhere.
	scoped, info := Snapshot(g, SnapshotOptions{Paths: []string{"internal/dispatch"}})
	if info.Truncated {
		t.Fatalf("the fixture must fit the default budget; it did not: %+v", info)
	}
	if len(scoped.Symbols) != 2 {
		t.Errorf("Paths filtered the snapshot to %d files; it must only rank", len(scoped.Symbols))
	}
}

func TestOwnedPathsAcceptGlobAndTrailingSeparatorForms(t *testing.T) {
	got := normalizePathPrefixes([]string{
		"internal/api/**", "web/src/*", "cmd/client/", "./internal/ast", "  ", ".", "/",
	})
	want := []string{"internal/api", "web/src", "cmd/client", "internal/ast"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("normalizePathPrefixes = %v, want %v", got, want)
	}
}

func TestNoOwnedPathsKeepsTheKeywordRanking(t *testing.T) {
	g := snapshotGraph(
		"internal/dispatch/plan.go",
		"internal/billing/invoice.go",
		"internal/other/thing.go",
	)
	_, info := Snapshot(g, SnapshotOptions{})
	scoped, _ := Snapshot(g, SnapshotOptions{
		Keywords: []string{"invoice"},
		MaxBytes: info.SerializedBytes / 3,
	})
	if files := filesIn(scoped); !has(files, "internal/billing/invoice.go") {
		t.Errorf("the keyword match was cut; kept %v", files)
	}
}
