//go:build client && !server

package app

import (
	"fmt"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/danielckv/agentic-plan-forge/internal/ast"
	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/impact"
)

const (
	// maxModuleSymbols bounds one module listing in the component browser.
	maxModuleSymbols = 500
	// maxSearchSymbols bounds one name search.
	maxSearchSymbols = 50
	// componentsPerEpic and notesPerEpic bound the links on one plan file.
	componentsPerEpic = 8
	notesPerEpic      = 3
)

// ComponentRef is one public declaration of the repository, as the window
// shows it: a term the user can write and an epic can link to.
type ComponentRef struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Path      string `json:"path"`
	Line      int    `json:"line"`
	Signature string `json:"signature,omitempty"`
	Module    string `json:"module"`
	Language  string `json:"language"`
	// Verdict, Reason and Confidence are carried only when the impact map put
	// this declaration on an epic. The component browser leaves them empty: it
	// lists what exists, and claims nothing about the work.
	Verdict    string `json:"verdict,omitempty"`
	Reason     string `json:"reason,omitempty"`
	Confidence string `json:"confidence,omitempty"`
}

// ComponentModule is one block of the repository: a directory of one language
// and how many public declarations it holds.
type ComponentModule struct {
	Path     string `json:"path"`
	Language string `json:"language"`
	Symbols  int    `json:"symbols"`
}

// ComponentsView is the body of GET /api/components.
type ComponentsView struct {
	Ready     bool              `json:"ready"`
	Stack     []string          `json:"stack"`
	Modules   []ComponentModule `json:"modules"`
	Symbols   []ComponentRef    `json:"symbols"`
	Truncated bool              `json:"truncated"`
}

// PlanFile is one draft epic rendered as a Markdown file, with the code and
// notes it most likely touches.
type PlanFile struct {
	ID           string                 `json:"id"`
	Title        string                 `json:"title"`
	Goal         string                 `json:"goal"`
	Filename     string                 `json:"filename"`
	Markdown     string                 `json:"markdown"`
	Requirements int                    `json:"requirements"`
	Criteria     int                    `json:"criteria"`
	Components   []ComponentRef         `json:"components"`
	Notes        []contracts.ContextHit `json:"notes"`
	// Links is what this epic touches, with a verdict and a plain-language
	// reason each. Components and Notes are projections of it.
	Links []contracts.ImpactLink `json:"links"`
}

// PlanFilesView is the body of GET /api/plan-files. Revision is the
// interpretation the files were built from; zero means none yet.
type PlanFilesView struct {
	Revision uint64     `json:"revision"`
	Files    []PlanFile `json:"files"`
}

// publicComponents lists every public declaration of g, sorted by path and line.
func publicComponents(g *contracts.RepoContextGraph) []ComponentRef {
	if g == nil {
		return nil
	}
	var out []ComponentRef
	for _, syms := range g.Symbols {
		for _, s := range syms {
			if s.Visibility != ast.VisibilityPublic || s.Name == "" {
				continue
			}
			out = append(out, ComponentRef{
				ID: s.ID, Name: s.Name, Kind: s.Kind, Path: s.Path, Line: s.StartLine,
				Signature: s.Signature, Module: path.Dir(s.Path), Language: s.Language,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		if out[i].Line != out[j].Line {
			return out[i].Line < out[j].Line
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Components answers the component browser. With module set it lists that
// block's declarations; with query set it searches names; with neither it
// lists the blocks.
func (e *executor) Components(module, query string) ComponentsView {
	e.mu.Lock()
	g := e.graph
	e.mu.Unlock()
	v := ComponentsView{Ready: g != nil, Stack: []string{}, Modules: []ComponentModule{}, Symbols: []ComponentRef{}}
	if g == nil {
		return v
	}
	v.Stack = append(v.Stack, g.DetectedStack...)
	all := publicComponents(g)

	counts := map[[2]string]int{}
	for _, c := range all {
		counts[[2]string{c.Module, c.Language}]++
	}
	for key, n := range counts {
		v.Modules = append(v.Modules, ComponentModule{Path: key[0], Language: key[1], Symbols: n})
	}
	sort.Slice(v.Modules, func(i, j int) bool {
		if v.Modules[i].Path != v.Modules[j].Path {
			return v.Modules[i].Path < v.Modules[j].Path
		}
		return v.Modules[i].Language < v.Modules[j].Language
	})

	switch {
	case module != "":
		for _, c := range all {
			if c.Module == module {
				v.Symbols = append(v.Symbols, c)
			}
		}
		if len(v.Symbols) > maxModuleSymbols {
			v.Symbols, v.Truncated = v.Symbols[:maxModuleSymbols], true
		}
	case strings.TrimSpace(query) != "":
		v.Symbols, v.Truncated = searchComponents(all, query)
	}
	return v
}

// searchComponents ranks names against query: an exact name first, then a
// prefix, then a substring of the name, then a substring of the path.
func searchComponents(all []ComponentRef, query string) ([]ComponentRef, bool) {
	q := strings.ToLower(strings.TrimSpace(query))
	type scored struct {
		c    ComponentRef
		rank int
	}
	var hits []scored
	for _, c := range all {
		name := strings.ToLower(c.Name)
		switch {
		case name == q:
			hits = append(hits, scored{c, 0})
		case strings.HasPrefix(name, q):
			hits = append(hits, scored{c, 1})
		case strings.Contains(name, q):
			hits = append(hits, scored{c, 2})
		case strings.Contains(strings.ToLower(c.Path), q):
			hits = append(hits, scored{c, 3})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].rank < hits[j].rank })
	out := make([]ComponentRef, 0, min(len(hits), maxSearchSymbols))
	for i, h := range hits {
		if i == maxSearchSymbols {
			return out, true
		}
		out = append(out, h.c)
	}
	return out, false
}

// PlanFiles renders the draft epics of the latest interpretation, each with
// what it touches.
//
// The links come from the impact map, so the rail and the Page 1 panel can
// never disagree. Before the first map pass, or when the map belongs to an
// older interpretation, the keyword map is built here instead: it is the same
// function the map pass itself paints with first, so the rail is never empty
// and never shows a map for a document the user has since changed.
func (e *executor) PlanFiles() PlanFilesView {
	e.mu.Lock()
	norm, g, pc, m := e.norm, e.graph, e.projectCtx, e.impactMap
	repo := e.cfg.Repo
	e.mu.Unlock()
	v := PlanFilesView{Files: []PlanFile{}}
	if norm == nil {
		return v
	}
	v.Revision = norm.Revision

	if m == nil || m.PRDRevision != norm.Revision {
		in := impact.Input{Repo: repo, PRD: *norm, Graph: g}
		if pc != nil {
			in.Context = *pc
		}
		lex := impact.Lexical(in)
		m = &lex
	}

	all := publicComponents(g)
	for _, epic := range norm.Epics {
		links := linksFor(m, epic.ID)
		f := PlanFile{
			ID: epic.ID, Title: epic.Title, Goal: epic.Goal, Filename: epicFilename(epic),
			Requirements: len(epic.Requirements), Criteria: len(epic.AcceptanceCriteria),
			Links:      links,
			Components: componentsOf(links, all),
			Notes:      notesOf(links),
		}
		f.Markdown = renderPlanFile(epic, links)
		v.Files = append(v.Files, f)
	}
	return v
}

// linksFor returns the map's links for one epic, or an empty list.
func linksFor(m *contracts.ImpactMap, epicID string) []contracts.ImpactLink {
	if m == nil {
		return []contracts.ImpactLink{}
	}
	for _, e := range m.Epics {
		if e.EpicID == epicID {
			if e.Links == nil {
				return []contracts.ImpactLink{}
			}
			return e.Links
		}
	}
	return []contracts.ImpactLink{}
}

// componentsOf projects the code links back onto the declarations the
// component browser serves, so the chips, the counts and the @ completion the
// window already renders keep working. A link whose declaration is no longer
// in the graph is dropped rather than shown as a component that cannot be
// opened.
func componentsOf(links []contracts.ImpactLink, all []ComponentRef) []ComponentRef {
	byID := make(map[string]ComponentRef, len(all))
	for _, c := range all {
		byID[c.ID] = c
	}
	out := make([]ComponentRef, 0, len(links))
	for _, l := range links {
		if l.Kind != contracts.ImpactKindCode || l.SymbolID == "" {
			continue
		}
		c, ok := byID[l.SymbolID]
		if !ok {
			continue
		}
		c.Verdict, c.Reason, c.Confidence = l.Verdict, l.Reason, l.Confidence
		out = append(out, c)
	}
	return out
}

// notesOf returns the documentation, specification and decision links as the
// excerpts the Notes tab shows.
func notesOf(links []contracts.ImpactLink) []contracts.ContextHit {
	out := make([]contracts.ContextHit, 0, len(links))
	for _, l := range links {
		if l.Kind == contracts.ImpactKindCode {
			continue
		}
		out = append(out, contracts.ContextHit{Path: l.Path, Heading: l.Heading, Text: l.Provenance})
	}
	return out
}

var slugUnsafe = regexp.MustCompile(`[^a-z0-9]+`)

// epicFilename is the name the window shows for a plan file, such as
// e1-invoice-export.md.
func epicFilename(e contracts.DraftEpic) string {
	slug := strings.Trim(slugUnsafe.ReplaceAllString(strings.ToLower(e.Title), "-"), "-")
	if r := []rune(slug); len(r) > 48 {
		slug = strings.Trim(string(r[:48]), "-")
	}
	name := strings.ToLower(e.ID)
	if slug != "" {
		name += "-" + slug
	}
	return name + ".md"
}

// verdictWord is the verdict as a reader who does not read code reads it. An
// empty verdict is a keyword match, which claims nothing.
func verdictWord(l contracts.ImpactLink) string {
	switch l.Verdict {
	case contracts.ImpactChange:
		return "Change"
	case contracts.ImpactAdd:
		return "New"
	case contracts.ImpactReuse:
		return "Already there"
	case contracts.ImpactConflict:
		return "Clashes"
	default:
		return "Possibly involved"
	}
}

// linkWhere names where a link points, for the reader who does want the file.
func linkWhere(l contracts.ImpactLink) string {
	switch {
	case l.Path == "":
		return "_nothing yet_"
	case l.Line > 0:
		return fmt.Sprintf("`%s:%d`", cellMD(l.Path), l.Line)
	default:
		return "`" + cellMD(l.Path) + "`"
	}
}

// linkWhat names the thing a link points at: the declaration, the document's
// heading, or the plain-language area when there is nothing yet.
func linkWhat(l contracts.ImpactLink) string {
	switch {
	case l.SymbolName != "":
		return "`" + cellMD(l.SymbolName) + "`"
	case l.Heading != "":
		return cellMD(l.Heading)
	default:
		return cellMD(l.Area)
	}
}

// renderPlanFile writes one epic as Markdown: goal, requirements as a
// checklist, acceptance criteria, what the work touches, and anything it
// contradicts.
func renderPlanFile(e contracts.DraftEpic, links []contracts.ImpactLink) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s · %s\n\n", e.ID, oneLineMD(e.Title))
	if e.Goal != "" {
		fmt.Fprintf(&b, "> %s\n\n", oneLineMD(e.Goal))
	}
	b.WriteString("## Requirements\n\n")
	if len(e.Requirements) == 0 {
		b.WriteString("_None listed yet._\n")
	}
	for _, r := range e.Requirements {
		fmt.Fprintf(&b, "- [ ] %s\n", oneLineMD(r))
	}
	b.WriteString("\n## Acceptance criteria\n\n")
	if len(e.AcceptanceCriteria) == 0 {
		b.WriteString("_None listed yet._\n")
	}
	for _, a := range e.AcceptanceCriteria {
		fmt.Fprintf(&b, "- %s\n", oneLineMD(a))
	}
	var touches, conflicts []contracts.ImpactLink
	modelRead := false
	for _, l := range links {
		if l.Source == contracts.ImpactSourceModel {
			modelRead = true
		}
		if l.Verdict == contracts.ImpactConflict {
			conflicts = append(conflicts, l)
			continue
		}
		touches = append(touches, l)
	}

	b.WriteString("\n## What this touches\n\n")
	switch {
	case len(touches) > 0:
		b.WriteString("| Part of the project | What happens | Where | Why |\n| :--- | :--- | :--- | :--- |\n")
		for _, l := range touches {
			why := oneLineMD(l.Reason)
			if why == "" {
				why = "_matched by name_"
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", linkWhat(l), verdictWord(l), linkWhere(l), cellMD(why))
		}
	case modelRead:
		b.WriteString("_Nothing in this project matches this epic. It looks like new work._\n")
	default:
		b.WriteString("_Nothing matched by name yet. The full read of the project is still running._\n")
	}

	if len(conflicts) > 0 {
		b.WriteString("\n## Conflicts with decisions already made\n\n")
		for _, l := range conflicts {
			line := "`" + cellMD(l.Path) + "`"
			if l.Heading != "" {
				line += " — **" + oneLineMD(l.Heading) + "**"
			}
			if l.Reason != "" {
				line += ": " + oneLineMD(l.Reason)
			}
			fmt.Fprintf(&b, "- %s\n", line)
		}
	}
	return b.String()
}

// oneLineMD collapses whitespace so a model-written item cannot break the
// list or heading it sits in.
func oneLineMD(s string) string { return strings.Join(strings.Fields(s), " ") }

// cellMD makes a value safe inside a table cell and inline code.
func cellMD(s string) string {
	return strings.NewReplacer("|", "\\|", "`", "'").Replace(oneLineMD(s))
}

// GET /api/components[?module=PATH|?q=TEXT] — the component browser.
func (s *appSession) handleComponents(w http.ResponseWriter, r *http.Request) {
	if s.exec == nil {
		writeJSON(w, http.StatusOK, ComponentsView{Stack: []string{}, Modules: []ComponentModule{}, Symbols: []ComponentRef{}})
		return
	}
	q := r.URL.Query()
	module, query := q.Get("module"), q.Get("q")
	if len(module) > 1024 || len(query) > 200 {
		writeAPIError(w, http.StatusBadRequest, "module or q is too long")
		return
	}
	writeJSON(w, http.StatusOK, s.exec.Components(module, query))
}

// GET /api/component-graph — modules and files as a 2D graph. focus names a
// node id to start from; depth bounds the walk (1..4, default 2).
func (s *appSession) handleComponentGraph(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	focus := q.Get("focus")
	if len(focus) > 1024 {
		writeAPIError(w, http.StatusBadRequest, "focus is too long")
		return
	}
	depth := 0
	if raw := q.Get("depth"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxGraphDepth {
			writeAPIError(w, http.StatusBadRequest, "depth must be an integer between 1 and 4")
			return
		}
		depth = n
	}
	if s.exec == nil {
		writeJSON(w, http.StatusOK, GraphView{Stack: []string{}, Nodes: []GraphNode{}, Edges: []GraphEdge{}})
		return
	}
	writeJSON(w, http.StatusOK, s.exec.ComponentGraph(focus, depth))
}

// GET /api/plan-files — the draft epics as Markdown files.
func (s *appSession) handlePlanFiles(w http.ResponseWriter, _ *http.Request) {
	if s.exec == nil {
		writeJSON(w, http.StatusOK, PlanFilesView{Files: []PlanFile{}})
		return
	}
	writeJSON(w, http.StatusOK, s.exec.PlanFiles())
}
