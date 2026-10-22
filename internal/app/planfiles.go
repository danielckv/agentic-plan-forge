//go:build client && !server

package app

import (
	"fmt"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/danielckv/agentic-plan-forge/internal/ast"
	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/gather"
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

// PlanFiles renders the draft epics of the latest interpretation.
func (e *executor) PlanFiles() PlanFilesView {
	e.mu.Lock()
	norm, g, pc := e.norm, e.graph, e.projectCtx
	e.mu.Unlock()
	v := PlanFilesView{Files: []PlanFile{}}
	if norm == nil {
		return v
	}
	v.Revision = norm.Revision
	all := publicComponents(g)
	var notes []contracts.ContextHit
	if pc != nil {
		notes = pc.Hits
	}
	for _, epic := range norm.Epics {
		text := strings.Join(append(append([]string{epic.Title, epic.Goal}, epic.Requirements...), epic.AcceptanceCriteria...), "\n")
		terms := gather.QueryTerms(text, 20)
		f := PlanFile{
			ID: epic.ID, Title: epic.Title, Goal: epic.Goal, Filename: epicFilename(epic),
			Requirements: len(epic.Requirements), Criteria: len(epic.AcceptanceCriteria),
			Components: rankComponents(all, terms, componentsPerEpic),
			Notes:      rankNotes(notes, terms, notesPerEpic),
		}
		f.Markdown = renderPlanFile(epic, f.Components, f.Notes)
		v.Files = append(v.Files, f)
	}
	return v
}

// rankComponents scores each declaration against the epic's terms: three
// points when its name holds a term, one when only its path does.
func rankComponents(all []ComponentRef, terms []string, limit int) []ComponentRef {
	type scored struct {
		c     ComponentRef
		score int
	}
	var hits []scored
	for _, c := range all {
		name, p := strings.ToLower(c.Name), strings.ToLower(c.Path)
		score := 0
		for _, t := range terms {
			switch {
			case strings.Contains(name, t):
				score += 3
			case strings.Contains(p, t):
				score++
			}
		}
		if score > 0 {
			hits = append(hits, scored{c, score})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	out := make([]ComponentRef, 0, min(len(hits), limit))
	for i := 0; i < len(hits) && i < limit; i++ {
		out = append(out, hits[i].c)
	}
	return out
}

// rankNotes keeps the project-context excerpts that share the most terms with
// the epic.
func rankNotes(notes []contracts.ContextHit, terms []string, limit int) []contracts.ContextHit {
	type scored struct {
		h     contracts.ContextHit
		score float64
	}
	var hits []scored
	for _, h := range notes {
		if s := gather.Relevance(h.Heading+"\n"+h.Text, terms); s > 0 {
			hits = append(hits, scored{h, s})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	out := make([]contracts.ContextHit, 0, min(len(hits), limit))
	for i := 0; i < len(hits) && i < limit; i++ {
		out = append(out, hits[i].h)
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

// renderPlanFile writes one epic as Markdown: goal, requirements as a
// checklist, acceptance criteria, then the related code and notes.
func renderPlanFile(e contracts.DraftEpic, components []ComponentRef, notes []contracts.ContextHit) string {
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
	b.WriteString("\n## Related code\n\n")
	if len(components) == 0 {
		b.WriteString("_No existing component matches this epic yet. It may need new code._\n")
	} else {
		b.WriteString("| Component | Kind | Where |\n| :--- | :--- | :--- |\n")
		for _, c := range components {
			fmt.Fprintf(&b, "| `%s` | %s | `%s:%d` |\n", cellMD(c.Name), cellMD(c.Kind), cellMD(c.Path), c.Line)
		}
	}
	if len(notes) > 0 {
		b.WriteString("\n## Related notes\n\n")
		for _, n := range notes {
			line := "`" + cellMD(n.Path) + "`"
			if n.Heading != "" {
				line += " — " + oneLineMD(n.Heading)
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

// GET /api/plan-files — the draft epics as Markdown files.
func (s *appSession) handlePlanFiles(w http.ResponseWriter, _ *http.Request) {
	if s.exec == nil {
		writeJSON(w, http.StatusOK, PlanFilesView{Files: []PlanFile{}})
		return
	}
	writeJSON(w, http.StatusOK, s.exec.PlanFiles())
}
