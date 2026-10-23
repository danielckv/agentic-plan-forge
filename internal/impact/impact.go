//go:build client && !server

// Package impact explains, in plain language, which parts of a project the
// interpreted requirements touch: which code changes, what is new, what can be
// reused, and which recorded decisions the requirements contradict.
//
// It runs after ingestion.Normalize and never feeds it. The separation of
// product requirements from technical material is upstream of this package:
// the Product Manager avatar receives product fields only, and nothing here
// reaches it. The map is a sibling of the normalized PRD, shown on Page 1.
//
// Two paths produce the same shape. Lexical is deterministic keyword matching,
// available immediately and always. Build asks the MAIN model slot to judge a
// bounded list of candidates the keyword pass found, so the model supplies
// precision and plain language while the deterministic pass supplies recall.
// The model is shown numbered candidates and answers with their identifiers,
// so it cannot name a file that does not exist.
package impact

import (
	"context"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/ast"
	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/gather"
)

// MapSchemaID is the schema one model batch must satisfy.
const MapSchemaID = "impact_map.v1"

const (
	// PromptVersion invalidates cached maps when the system prompt changes.
	PromptVersion = 2
	// ExtractorVersion invalidates cached maps when evidence assembly changes:
	// candidate selection, ranking or excerpt clipping.
	ExtractorVersion = 2
)

const (
	// MaxSymbolCandidates and MaxDocCandidates bound one epic's evidence: what
	// the model is shown and may choose among.
	MaxSymbolCandidates = 20
	MaxDocCandidates    = 6
	// MaxCodeLinksPerEpic and MaxDocLinksPerEpic bound what reaches the window
	// for one epic. The two budgets are separate, as they were when the plan
	// rail owned this ranking, so a wide code match cannot crowd out the one
	// decision record the epic contradicts.
	MaxCodeLinksPerEpic = 8
	MaxDocLinksPerEpic  = 3
	// MaxWholeLinks bounds the links about the requirements as a whole.
	MaxWholeLinks = 12

	// maxTerms is how many query terms one epic is matched on. It matches the
	// 20 the plan rail used before this package owned the ranking.
	maxTerms = 20
	// maxEpics bounds the epics one pass maps. The normalizer asks the model
	// for at most twelve; a longer list is recorded as a gap, not silently cut.
	maxEpics = 12
)

// Input is everything one map pass is allowed to see.
type Input struct {
	// Repo is the absolute repository root. It is used for the cache and for
	// the per-epic gather, and never reaches the model.
	Repo string
	// PRD is the interpretation the map explains.
	PRD contracts.NormalizedPRD
	// Graph is the scanned repository, or nil when the scan failed.
	Graph *contracts.RepoContextGraph
	// Context is the project context gathered for the whole PRD.
	Context contracts.ProjectContext
	// Gen is the MAIN-slot generator. A nil generator selects the lexical map.
	Gen contracts.Generator
	// Gather overrides gather.Collect (tests).
	Gather func(ctx context.Context, opts gather.Options) (contracts.ProjectContext, error)
	// ReadCache and WriteCache control docs/plans/.plan/cache/impact.json.
	ReadCache, WriteCache bool
	// Now supplies the clock for GeneratedAt (tests).
	Now func() time.Time
}

func (in Input) now() time.Time {
	if in.Now != nil {
		return in.Now()
	}
	return time.Now()
}

// epics returns the epics one pass maps, and a note when the list was cut.
func (in Input) epics() ([]contracts.DraftEpic, string) {
	all := in.PRD.Epics
	if len(all) <= maxEpics {
		return all, ""
	}
	return all[:maxEpics], "only the first " + strconv.Itoa(maxEpics) + " epics were mapped; the rest are listed without links"
}

// Lexical returns the deterministic map: every link is a keyword match, with
// no verdict and no reason. It makes no model call, reads no cache and never
// fails, so Page 1 always has something true to show.
func Lexical(in Input) contracts.ImpactMap {
	m := newMap(in, contracts.ImpactLexical)
	syms := publicSymbols(in.Graph)
	epics, note := in.epics()
	if note != "" {
		m.Notes = append(m.Notes, note)
	}

	for _, epic := range epics {
		terms := gather.QueryTerms(epicText(epic), maxTerms)
		m.Epics = append(m.Epics, contracts.ImpactEpic{
			EpicID: epic.ID,
			Title:  epic.Title,
			Links:  lexicalLinks(epic.ID, syms, in.Context.Hits, terms, MaxCodeLinksPerEpic, MaxDocLinksPerEpic),
		})
	}
	wholeTerms := gather.QueryTerms(productText(in.PRD), maxTerms)
	m.Whole = lexicalLinks("", syms, in.Context.Hits, wholeTerms, MaxWholeLinks, MaxDocLinksPerEpic)
	finish(&m)
	return m
}

// lexicalLinks ranks the symbols and the context excerpts against terms and
// returns them as links, code first, under one budget each.
func lexicalLinks(epicID string, syms []contracts.Symbol, hits []contracts.ContextHit, terms []string, codeLimit, docLimit int) []contracts.ImpactLink {
	out := make([]contracts.ImpactLink, 0, codeLimit+docLimit)
	for _, s := range rankSymbols(syms, terms, codeLimit) {
		out = append(out, codeLink(epicID, s, "", "", "", contracts.ImpactSourceLexical))
	}
	for _, h := range rankHits(hits, terms, docLimit) {
		out = append(out, docLink(epicID, h, "", "", "", contracts.ImpactSourceLexical))
	}
	return out
}

// newMap returns an empty map stamped with the pass's identity. Every slice is
// non-nil so the JSON the window reads never holds a null array.
func newMap(in Input, state string) contracts.ImpactMap {
	m := contracts.ImpactMap{
		SchemaVersion: contracts.SchemaVersion,
		State:         state,
		PRDRevision:   in.PRD.Revision,
		Whole:         []contracts.ImpactLink{},
		Epics:         []contracts.ImpactEpic{},
		Conflicts:     []contracts.ImpactLink{},
		GeneratedAt:   in.now().UTC(),
	}
	if in.Graph != nil {
		m.WorktreeHash = in.Graph.WorktreeHash
	}
	return m
}

// finish collects the conflicts and writes the one-line summary. It is called
// once, after every link is in place.
func finish(m *contracts.ImpactMap) {
	m.Conflicts = m.Conflicts[:0]
	seen := map[string]bool{}
	for _, l := range allLinks(*m) {
		if l.Verdict != contracts.ImpactConflict || seen[l.ID] {
			continue
		}
		seen[l.ID] = true
		m.Conflicts = append(m.Conflicts, l)
	}
	m.Summary = summarize(*m)
}

// allLinks walks every link of a map, whole-PRD first.
func allLinks(m contracts.ImpactMap) []contracts.ImpactLink {
	out := append([]contracts.ImpactLink{}, m.Whole...)
	for _, e := range m.Epics {
		out = append(out, e.Links...)
	}
	return out
}

// summarize states the map in one sentence a reader who opens nothing else can
// act on. It counts distinct areas, not links, because one area can be touched
// by several epics.
func summarize(m contracts.ImpactMap) string {
	areas := map[string]bool{}
	byVerdict := map[string]map[string]bool{}
	for _, l := range allLinks(m) {
		areas[l.Area] = true
		if byVerdict[l.Verdict] == nil {
			byVerdict[l.Verdict] = map[string]bool{}
		}
		byVerdict[l.Verdict][l.Area] = true
	}
	if len(areas) == 0 {
		return "Nothing in this project matches these requirements yet."
	}
	var parts []string
	for _, v := range []struct{ verdict, phrase string }{
		{contracts.ImpactChange, "change"},
		{contracts.ImpactAdd, "are new"},
		{contracts.ImpactReuse, "are reused"},
		{contracts.ImpactConflict, "clash with a decision already made"},
	} {
		if n := len(byVerdict[v.verdict]); n > 0 {
			parts = append(parts, strconv.Itoa(n)+" "+v.phrase)
		}
	}
	head := "This work touches " + strconv.Itoa(len(areas)) + " part"
	if len(areas) != 1 {
		head += "s"
	}
	head += " of the project"
	if len(parts) == 0 {
		return head + ", matched by name."
	}
	return head + ": " + strings.Join(parts, ", ") + "."
}

// ---------------------------------------------------------------- candidates

// testFileSuffixes mark a file as a test across the languages the scanner
// parses. A test is how a change is checked, not a part of the project the
// plan changes, so offering one as something the requirements touch sends the
// reader to the wrong file.
var testFileSuffixes = []string{
	"_test.go", ".test.ts", ".test.tsx", ".test.js", ".test.jsx",
	".spec.ts", ".spec.tsx", ".spec.js", ".spec.jsx", "_test.py", "_test.rs",
}

// IsTestPath reports whether a repository-relative path is a test file.
func IsTestPath(rel string) bool {
	lower := strings.ToLower(rel)
	base := path.Base(lower)
	for _, suffix := range testFileSuffixes {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	return strings.HasPrefix(base, "test_") || strings.HasPrefix(base, "conftest.")
}

// publicSymbols lists every public declaration of g, sorted by path and line,
// so a ranking over them is stable. Test files are left out: see IsTestPath.
func publicSymbols(g *contracts.RepoContextGraph) []contracts.Symbol {
	if g == nil {
		return nil
	}
	var out []contracts.Symbol
	for p, syms := range g.Symbols {
		if IsTestPath(p) {
			continue
		}
		for _, s := range syms {
			if s.Visibility != ast.VisibilityPublic || s.Name == "" {
				continue
			}
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		if out[i].StartLine != out[j].StartLine {
			return out[i].StartLine < out[j].StartLine
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// rankSymbols scores each declaration against the epic's terms: three points
// when its name holds a term, one when only its path does. It is the ranking
// the plan rail used before this package owned it, unchanged.
func rankSymbols(all []contracts.Symbol, terms []string, limit int) []contracts.Symbol {
	if limit <= 0 {
		return nil
	}
	type scored struct {
		s     contracts.Symbol
		score int
	}
	var hits []scored
	for _, s := range all {
		name, p := strings.ToLower(s.Name), strings.ToLower(s.Path)
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
			hits = append(hits, scored{s, score})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	out := make([]contracts.Symbol, 0, min(len(hits), limit))
	for i := 0; i < len(hits) && i < limit; i++ {
		out = append(out, hits[i].s)
	}
	return out
}

// rankHits keeps the project-context excerpts that share the most terms with
// the epic.
func rankHits(hits []contracts.ContextHit, terms []string, limit int) []contracts.ContextHit {
	if limit <= 0 {
		return nil
	}
	type scored struct {
		h     contracts.ContextHit
		score float64
	}
	var scoredHits []scored
	for _, h := range hits {
		if s := gather.Relevance(h.Heading+"\n"+h.Text, terms); s > 0 {
			scoredHits = append(scoredHits, scored{h, s})
		}
	}
	sort.SliceStable(scoredHits, func(i, j int) bool { return scoredHits[i].score > scoredHits[j].score })
	out := make([]contracts.ContextHit, 0, min(len(scoredHits), limit))
	for i := 0; i < len(scoredHits) && i < limit; i++ {
		out = append(out, scoredHits[i].h)
	}
	return out
}

// -------------------------------------------------------------------- links

// codeLink describes one declaration. The caller supplies the verdict, reason
// and confidence; the location always comes from the graph.
func codeLink(epicID string, s contracts.Symbol, area, verdict, reason, source string) contracts.ImpactLink {
	if area == "" {
		area = path.Dir(s.Path)
	}
	return contracts.ImpactLink{
		ID:         linkID(epicID, contracts.ImpactKindCode, s.Path, s.Name),
		Area:       area,
		Kind:       contracts.ImpactKindCode,
		Path:       s.Path,
		Line:       s.StartLine,
		SymbolID:   s.ID,
		SymbolName: s.Name,
		SymbolKind: s.Kind,
		Verdict:    verdict,
		Reason:     reason,
		Source:     source,
		Provenance: s.Signature,
	}
}

// blockLink describes one directory of the project, for work that belongs
// somewhere without belonging to one declaration.
func blockLink(epicID, dir, area, verdict, reason, source string) contracts.ImpactLink {
	if area == "" {
		area = dir
	}
	return contracts.ImpactLink{
		ID:      linkID(epicID, contracts.ImpactKindCode, dir, ""),
		Area:    area,
		Kind:    contracts.ImpactKindCode,
		Path:    dir,
		Verdict: verdict,
		Reason:  reason,
		Source:  source,
	}
}

// docLink describes one documentation, specification or decision excerpt. The
// kind is read from the path, so a decision record is never reported as an
// ordinary document.
func docLink(epicID string, h contracts.ContextHit, area, verdict, reason, source string) contracts.ImpactLink {
	if area == "" {
		area = h.Heading
	}
	if area == "" {
		area = path.Base(h.Path)
	}
	return contracts.ImpactLink{
		ID:         linkID(epicID, docKind(h.Path), h.Path, h.Heading),
		Area:       area,
		Kind:       docKind(h.Path),
		Path:       h.Path,
		Heading:    h.Heading,
		Verdict:    verdict,
		Reason:     reason,
		Source:     source,
		Provenance: h.Text,
	}
}

// docKind classifies a project-context path. A decision record carries more
// weight than a note, and only a decision or a specification can be
// contradicted, so the distinction is not cosmetic.
func docKind(p string) string {
	lower := strings.ToLower(p)
	switch {
	case strings.Contains(lower, "/adrs/"), strings.Contains(lower, "/adr/"),
		strings.HasPrefix(lower, "adrs/"), strings.HasPrefix(lower, "adr/"):
		return contracts.ImpactKindDecision
	case strings.Contains(lower, "/specs/"), strings.HasPrefix(lower, "specs/"):
		return contracts.ImpactKindSpec
	default:
		return contracts.ImpactKindDoc
	}
}

// linkID is stable across rebuilds, so the window can key a row on it.
func linkID(epicID, kind, p, name string) string {
	id := kind + ":" + p
	if name != "" {
		id += "#" + name
	}
	if epicID != "" {
		return epicID + ":" + id
	}
	return "whole:" + id
}

// ---------------------------------------------------------------- PRD text

// epicText is the text one epic is matched on.
func epicText(e contracts.DraftEpic) string {
	parts := append([]string{e.Title, e.Goal}, e.Requirements...)
	parts = append(parts, e.AcceptanceCriteria...)
	return strings.Join(parts, "\n")
}

// productText is the text the whole-PRD links are matched on. It is the
// product view only: the technical appendix would bias the ranking towards the
// identifiers the author already happened to name.
func productText(n contracts.NormalizedPRD) string {
	p := n.Product
	parts := []string{p.Title, p.Problem}
	for _, group := range [][]string{p.Goals, p.Actors, p.Flows, p.Requirements, p.AcceptanceCriteria, p.Constraints} {
		parts = append(parts, group...)
	}
	return strings.Join(parts, "\n")
}
