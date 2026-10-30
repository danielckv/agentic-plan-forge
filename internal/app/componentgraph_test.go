//go:build client && !server

package app

import (
	"testing"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// sampleGraph is a tiny snapshot: internal/a/x.go imports internal/b/y.go, and
// a stray import names something outside the snapshot.
func sampleGraph() *contracts.RepoContextGraph {
	sym := func(id, path, lang string, imports ...string) contracts.Symbol {
		return contracts.Symbol{ID: id, Name: id, Kind: "function", Language: lang, Path: path, StartLine: 1, EndLine: 2, Imports: imports}
	}
	return &contracts.RepoContextGraph{
		DetectedStack: []string{"go"},
		Symbols: map[string][]contracts.Symbol{
			"internal/a/x.go": {sym("A", "internal/a/x.go", "go", "internal/b/y.go")},
			"internal/a/z.go": {sym("Z", "internal/a/z.go", "go")},
			"internal/b/y.go": {sym("Y", "internal/b/y.go", "go", "example.com/outside")},
		},
	}
}

func TestComponentGraphDrawsModulesFilesAndImports(t *testing.T) {
	e := &executor{graph: sampleGraph()}
	v := e.ComponentGraph("", 2)
	if !v.Ready {
		t.Fatal("graph is not ready")
	}
	kinds := map[string]int{}
	for _, n := range v.Nodes {
		kinds[n.Kind]++
	}
	if kinds["module"] == 0 || kinds["file"] == 0 {
		t.Fatalf("expected module and file nodes, got %v", kinds)
	}
	var contains, imports int
	for _, e := range v.Edges {
		switch e.Kind {
		case "contains":
			contains++
		case "imports":
			imports++
		}
	}
	if contains == 0 || imports == 0 {
		t.Fatalf("edges: contains=%d imports=%d, want both", contains, imports)
	}
	// The outside import must not become a node: the view draws only what the
	// snapshot contains.
	for _, n := range v.Nodes {
		if n.Path == "example.com/outside" {
			t.Fatal("an import outside the snapshot became a node")
		}
	}
}

// depth bounds the walk in hops over the drawn edges. An import is one hop and
// a sibling file is two (through the module that holds both), so depth 1
// reaches the imported file and depth 2 also reaches the sibling.
func TestComponentGraphHonoursDepth(t *testing.T) {
	e := &executor{graph: sampleGraph()}
	shallow := e.ComponentGraph("file:internal/a/x.go", 1)
	deep := e.ComponentGraph("file:internal/a/x.go", 4)
	has := func(v GraphView, id string) bool {
		for _, n := range v.Nodes {
			if n.ID == id {
				return true
			}
		}
		return false
	}
	if !has(shallow, "file:internal/a/x.go") {
		t.Fatal("the focus node is missing at depth 1")
	}
	if !has(shallow, "module:internal/a") {
		t.Fatal("depth 1 did not reach the file's own module")
	}
	if !has(shallow, "file:internal/b/y.go") {
		t.Fatal("depth 1 did not reach the imported file; an import is one hop")
	}
	if has(shallow, "file:internal/a/z.go") {
		t.Fatal("depth 1 reached a sibling file; that is two hops through the module")
	}
	if !has(deep, "file:internal/a/z.go") {
		t.Fatal("depth 4 did not reach the sibling file")
	}
}

// An unknown focus is an empty graph, not an error and not a crash.
func TestComponentGraphUnknownFocusIsEmpty(t *testing.T) {
	e := &executor{graph: sampleGraph()}
	v := e.ComponentGraph("file:nope.go", 2)
	if len(v.Nodes) != 0 {
		t.Fatalf("unknown focus produced %d nodes", len(v.Nodes))
	}
}

// The depth is clamped rather than trusted: a caller asking for 99 gets the
// documented maximum.
func TestComponentGraphClampsDepth(t *testing.T) {
	e := &executor{graph: sampleGraph()}
	if got := e.ComponentGraph("", 99).Depth; got != maxGraphDepth {
		t.Fatalf("depth = %d, want %d", got, maxGraphDepth)
	}
	if got := e.ComponentGraph("", 0).Depth; got != 2 {
		t.Fatalf("default depth = %d, want 2", got)
	}
}

// A nil graph answers an empty, ready=false view with no null slices, because
// the window maps over them.
func TestComponentGraphNotReady(t *testing.T) {
	e := &executor{}
	v := e.ComponentGraph("", 2)
	if v.Ready || v.Nodes == nil || v.Edges == nil || v.Stack == nil {
		t.Fatalf("empty graph = %+v", v)
	}
}
