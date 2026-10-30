//go:build client && !server

package app

import (
	"path"
	"sort"
	"strings"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// The 2D component graph: modules and files as nodes, imports and directory
// containment as edges. It draws the same AST evidence the component browser
// lists, so a reader can see which part of the repository depends on which.
// Nothing is invented: every node exists in the snapshot, and every edge is an
// import the scanner recorded or a module that physically holds the file.

// maxGraphNodes bounds one graph answer. The snapshot can hold tens of
// thousands of declarations; a view that big is unreadable as well as slow, and
// depth is the way to narrow it.
const maxGraphNodes = 400

// maxGraphDepth bounds the walk. Four hops covers "this file, its package, the
// packages it imports, and theirs".
const maxGraphDepth = 4

// GraphNode is one vertex of the 2D component graph.
type GraphNode struct {
	// ID is stable across requests: "module:<path>" or "file:<path>".
	ID       string `json:"id"`
	Kind     string `json:"kind"` // module | file
	Label    string `json:"label"`
	Path     string `json:"path"`
	Language string `json:"language,omitempty"`
	// Symbols counts the declarations the file or module holds.
	Symbols int `json:"symbols"`
	// Depth is the hop count from the focus; 0 is the focus itself.
	Depth int `json:"depth"`
}

// GraphEdge is one directed relationship between two nodes.
type GraphEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
	// Kind is "imports" (recorded by the scanner) or "contains" (a module
	// physically holding a file).
	Kind string `json:"kind"`
}

// GraphView is the body of GET /api/component-graph.
type GraphView struct {
	Ready bool        `json:"ready"`
	Stack []string    `json:"stack"`
	Focus string      `json:"focus,omitempty"`
	Depth int         `json:"depth"`
	Nodes []GraphNode `json:"nodes"`
	Edges []GraphEdge `json:"edges"`
	// Truncated reports that the walk stopped at maxGraphNodes. The window
	// says so rather than presenting a partial graph as whole.
	Truncated   bool `json:"truncated"`
	TruncatedAt int  `json:"truncated_at,omitempty"`
}

func moduleID(p string) string { return "module:" + p }
func fileID(p string) string   { return "file:" + p }

// ComponentGraph answers the 2D graph view. focus names a node id to start
// from; empty starts at every top-level module, which is the whole-repository
// view. depth bounds the walk, 1..maxGraphDepth, default 2.
func (e *executor) ComponentGraph(focus string, depth int) GraphView {
	e.mu.Lock()
	g := e.graph
	e.mu.Unlock()
	if depth < 1 {
		depth = 2
	}
	if depth > maxGraphDepth {
		depth = maxGraphDepth
	}
	v := GraphView{Ready: g != nil, Stack: []string{}, Depth: depth, Nodes: []GraphNode{}, Edges: []GraphEdge{}}
	if g == nil {
		return v
	}
	v.Stack = append(v.Stack, g.DetectedStack...)

	facts := fileFacts(g)

	// Adjacency both ways: a walk from a module reaches its files, and a walk
	// from a file reaches the module that holds it.
	adj := map[string][]string{}
	link := func(a, b string) {
		adj[a] = append(adj[a], b)
		adj[b] = append(adj[b], a)
	}
	for file, f := range facts {
		if f.module != "" {
			link(moduleID(f.module), fileID(file))
		}
		for _, target := range f.imports {
			link(fileID(file), fileID(target))
		}
	}

	var start []string
	if focus != "" {
		if _, ok := facts[strings.TrimPrefix(focus, "file:")]; !ok {
			if !strings.HasPrefix(focus, "module:") {
				return v // an unknown focus is an empty graph, not an error
			}
		}
		start = append(start, focus)
		v.Focus = focus
	} else {
		dirs := map[string]bool{}
		for _, f := range facts {
			dirs[f.module] = true
		}
		for dir := range dirs {
			start = append(start, moduleID(dir))
		}
		sort.Strings(start)
	}

	seen := map[string]bool{}
	type item struct {
		id    string
		depth int
	}
	queue := make([]item, 0, len(start))
	for _, id := range start {
		queue = append(queue, item{id, 0})
		seen[id] = true
	}
	edgeSeen := map[[3]string]bool{}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		node, ok := graphNodeFor(cur.id, cur.depth, facts)
		if !ok {
			continue
		}
		if len(v.Nodes) >= maxGraphNodes {
			v.Truncated, v.TruncatedAt = true, cur.depth
			break
		}
		v.Nodes = append(v.Nodes, node)
		neighbours := append([]string(nil), adj[node.ID]...)
		sort.Strings(neighbours)
		for _, next := range neighbours {
			kind := "imports"
			if node.Kind == "module" {
				kind = "contains"
			}
			if key := [3]string{node.ID, next, kind}; !edgeSeen[key] {
				edgeSeen[key] = true
				v.Edges = append(v.Edges, GraphEdge{From: node.ID, To: next, Kind: kind})
			}
			if seen[next] || cur.depth+1 > depth {
				continue
			}
			seen[next] = true
			queue = append(queue, item{next, cur.depth + 1})
		}
	}
	sort.SliceStable(v.Nodes, func(i, j int) bool {
		if v.Nodes[i].Depth != v.Nodes[j].Depth {
			return v.Nodes[i].Depth < v.Nodes[j].Depth
		}
		if v.Nodes[i].Kind != v.Nodes[j].Kind {
			return v.Nodes[i].Kind < v.Nodes[j].Kind
		}
		return v.Nodes[i].Path < v.Nodes[j].Path
	})
	sort.SliceStable(v.Edges, func(i, j int) bool {
		if v.Edges[i].From != v.Edges[j].From {
			return v.Edges[i].From < v.Edges[j].From
		}
		return v.Edges[i].To < v.Edges[j].To
	})
	return v
}

// fileFact is what the walk needs about one file.
type fileFact struct {
	module   string
	language string
	symbols  int
	imports  []string
}

// fileFacts collapses the snapshot into per-file rows: the module that holds
// the file, its language, its declaration count and its imports resolved to
// snapshot paths. An import that names something outside the snapshot is
// dropped, because an edge to a node the view cannot draw is noise.
func fileFacts(g *contracts.RepoContextGraph) map[string]fileFact {
	out := make(map[string]fileFact, len(g.Symbols))
	known := make(map[string]bool, len(g.Symbols))
	for file := range g.Symbols {
		known[file] = true
	}
	for file, syms := range g.Symbols {
		f := fileFact{module: path.Dir(file)}
		if f.module == "." {
			f.module = ""
		}
		f.symbols = len(syms)
		if len(syms) > 0 {
			f.language = syms[0].Language
		}
		for _, s := range syms {
			for _, imp := range s.Imports {
				if target := resolveImport(file, imp, known); target != "" && target != file {
					f.imports = append(f.imports, target)
				}
			}
		}
		sort.Strings(f.imports)
		out[file] = f
	}
	return out
}

// resolveImport maps one import string to a snapshot path, or "" when it names
// nothing in the snapshot.
//
// Two shapes are recorded by the scanners: a repository-relative path in some
// languages, and a package or module path in others (Go import paths, JS module
// specifiers). Only an exact match, a match with a known extension, or a
// directory holding an index file is accepted: guessing further would draw an
// edge that the evidence does not support.
func resolveImport(from, imp string, known map[string]bool) string {
	imp = strings.TrimSpace(imp)
	if imp == "" {
		return ""
	}
	candidates := []string{imp, path.Join(path.Dir(from), imp)}
	for _, base := range candidates {
		base = strings.TrimPrefix(base, "./")
		if known[base] {
			return base
		}
		for _, ext := range []string{".go", ".ts", ".tsx", ".js", ".jsx", ".py", ".rs", ".java", ".kt", ".rb", ".php", ".cs", ".swift", ".md"} {
			if known[base+ext] {
				return base + ext
			}
		}
		for _, idx := range []string{"/index.ts", "/index.tsx", "/index.js", "/index.jsx", "/mod.go"} {
			if known[base+idx] {
				return base + idx
			}
		}
	}
	return ""
}

// graphNodeFor builds one node, or reports false when the id names neither a
// file nor a directory that holds one.
func graphNodeFor(id string, depth int, facts map[string]fileFact) (GraphNode, bool) {
	if file, ok := strings.CutPrefix(id, "file:"); ok {
		f, found := facts[file]
		if !found {
			return GraphNode{}, false
		}
		return GraphNode{
			ID: id, Kind: "file", Label: path.Base(file), Path: file,
			Language: f.language, Symbols: f.symbols, Depth: depth,
		}, true
	}
	dir, ok := strings.CutPrefix(id, "module:")
	if !ok {
		return GraphNode{}, false
	}
	var symbols int
	langs := map[string]bool{}
	files := 0
	for _, f := range facts {
		if f.module != dir {
			continue
		}
		files++
		symbols += f.symbols
		if f.language != "" {
			langs[f.language] = true
		}
	}
	if files == 0 {
		return GraphNode{}, false
	}
	label := dir
	if label == "" {
		label = "."
	}
	node := GraphNode{ID: id, Kind: "module", Label: label, Path: dir, Symbols: symbols, Depth: depth}
	if len(langs) == 1 {
		for l := range langs {
			node.Language = l
		}
	}
	return node, true
}
