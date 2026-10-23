//go:build client && !server

package impact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/gather"
)

const (
	// MaxEvidenceChars is the largest evidence pack, in characters, sent to the
	// model in one call. Longer runs are split into more calls.
	//
	// The bound is the call timeout, not the model's context window: one call
	// must finish inside contracts.MaxGenerateTimeout or the host kills it.
	// This is the rule ADR 0004 established for normalization sections, applied
	// to the unit this stage splits on.
	MaxEvidenceChars = 8000
	// MinEvidenceChars is the size below which a pack that overran the call
	// timeout is no longer shrunk: at this size the timeout is not a size
	// problem, so shrinking again would only bill the same work twice.
	MinEvidenceChars = 1200

	// maxExcerptChars bounds one document excerpt inside a pack.
	maxExcerptChars = 600
	// maxSignatureChars bounds one declaration's signature inside a pack.
	maxSignatureChars = 300
	// maxTotalDocCandidates bounds the document excerpts across every pack, so
	// a twelve-epic PRD cannot spend the whole budget on prose.
	maxTotalDocCandidates = 40
)

// candidate is one thing the model may be shown and may link to.
//
// ID is the only handle the model is given, and it is unique across the whole
// pass. The model answers with ids, so a path it has never been shown cannot
// appear in the result.
type candidate struct {
	ID     string
	Kind   string
	Symbol contracts.Symbol
	Hit    contracts.ContextHit
}

// pack is one epic's evidence, or the whole-PRD pack when EpicID is empty.
type pack struct {
	EpicID             string
	Title              string
	Goal               string
	Requirements       []string
	AcceptanceCriteria []string
	Candidates         []candidate
}

// wirePack is the pack as the model sees it. It carries no absolute path, no
// score and no internal ranking: only what a reader needs to judge the link.
type wirePack struct {
	EpicID             string          `json:"epic_id"`
	Title              string          `json:"title"`
	Goal               string          `json:"goal,omitempty"`
	Requirements       []string        `json:"requirements"`
	AcceptanceCriteria []string        `json:"acceptance_criteria"`
	Candidates         []wireCandidate `json:"candidates"`
}

type wireCandidate struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	// Name is the declaration's name, or the document's heading.
	Name string `json:"name,omitempty"`
	// Where locates the candidate for a reader who wants to open it.
	Where     string `json:"where"`
	Signature string `json:"signature,omitempty"`
	Excerpt   string `json:"excerpt,omitempty"`
}

// wire renders a pack for the model.
func (p pack) wire() wirePack {
	w := wirePack{
		EpicID: p.EpicID, Title: p.Title, Goal: p.Goal,
		Requirements:       nonNil(p.Requirements),
		AcceptanceCriteria: nonNil(p.AcceptanceCriteria),
		Candidates:         make([]wireCandidate, 0, len(p.Candidates)),
	}
	for _, c := range p.Candidates {
		w.Candidates = append(w.Candidates, c.wire())
	}
	return w
}

func (c candidate) wire() wireCandidate {
	if c.Kind == contracts.ImpactKindCode {
		return wireCandidate{
			ID: c.ID, Kind: c.Kind, Name: c.Symbol.Name,
			Where:     c.Symbol.Path + ":" + strconv.Itoa(c.Symbol.StartLine),
			Signature: clip(c.Symbol.Signature, maxSignatureChars),
		}
	}
	return wireCandidate{
		ID: c.ID, Kind: c.Kind, Name: c.Hit.Heading,
		Where:   c.Hit.Path,
		Excerpt: clip(c.Hit.Text, maxExcerptChars),
	}
}

// packsWire renders a batch of packs for the model.
func packsWire(packs []pack) []wirePack {
	out := make([]wirePack, 0, len(packs))
	for _, p := range packs {
		out = append(out, p.wire())
	}
	return out
}

// chars is the pack's cost against the evidence budget: the size of what the
// model is actually sent.
func (p pack) chars() int {
	raw, err := json.Marshal(p.wire())
	if err != nil {
		return MaxEvidenceChars + 1
	}
	return len(raw)
}

// buildPacks assembles one pack per epic plus a whole-PRD pack, ranked and
// bounded, and returns the notes explaining anything it could not cover.
//
// Document candidates come from a gather scoped to one epic, not from the
// whole-PRD context: that context holds twelve excerpts for the entire
// document, which for a six-epic PRD is two per epic — too thin for the
// decision record that contradicts one of them to be in the candidate set at
// all.
func buildPacks(ctx context.Context, in Input) ([]pack, []string) {
	var notes []string
	epics, note := in.epics()
	if note != "" {
		notes = append(notes, note)
	}
	syms := publicSymbols(in.Graph)
	if len(syms) == 0 {
		notes = append(notes, "the repository scan produced no declarations, so no code was offered as a candidate")
	}

	next := 0
	id := func() string {
		next++
		return "c" + strconv.Itoa(next)
	}
	docBudget := maxTotalDocCandidates

	build := func(epicID, title, goal string, reqs, criteria []string, text string, symLimit int) pack {
		terms := gather.QueryTerms(text, maxTerms)
		p := pack{EpicID: epicID, Title: title, Goal: goal, Requirements: reqs, AcceptanceCriteria: criteria}
		for _, s := range rankSymbols(syms, terms, symLimit) {
			p.Candidates = append(p.Candidates, candidate{ID: id(), Kind: contracts.ImpactKindCode, Symbol: s})
		}
		room := min(MaxDocCandidates, docBudget)
		for _, h := range gatherPerEpic(ctx, in, text, terms, room) {
			p.Candidates = append(p.Candidates, candidate{ID: id(), Kind: docKind(h.Path), Hit: h})
			docBudget--
		}
		return p
	}

	packs := make([]pack, 0, len(epics)+1)
	for _, e := range epics {
		packs = append(packs, build(e.ID, e.Title, e.Goal, e.Requirements, e.AcceptanceCriteria, epicText(e), MaxSymbolCandidates))
	}
	product := in.PRD.Product
	whole := build("", product.Title, product.Problem, product.Requirements, product.AcceptanceCriteria,
		productText(in.PRD), MaxSymbolCandidates)
	if len(whole.Candidates) > 0 || len(packs) == 0 {
		packs = append(packs, whole)
	}
	if docBudget == 0 {
		notes = append(notes, "the document excerpt budget was spent before every epic was covered")
	}
	return packs, notes
}

// gatherPerEpic collects the project notes, specs and decision records that
// match one epic. It falls back to re-ranking the context the executor already
// gathered when the gatherer is unavailable or fails, so a pack is never left
// without documents for a reason the user cannot see.
func gatherPerEpic(ctx context.Context, in Input, text string, terms []string, limit int) []contracts.ContextHit {
	if limit <= 0 {
		return nil
	}
	collect := in.Gather
	if collect == nil {
		collect = gather.Collect
	}
	if strings.TrimSpace(in.Repo) != "" && strings.TrimSpace(text) != "" {
		pc, err := collect(ctx, gather.Options{Repo: in.Repo, Text: text, MaxHits: limit})
		if err == nil && len(pc.Hits) > 0 {
			return rankHits(pc.Hits, terms, limit)
		}
	}
	return rankHits(in.Context.Hits, terms, limit)
}

// ------------------------------------------------------------------ batching

// batchPacks groups packs into model calls of at most maxChars characters. One
// pack never spans two calls: a pack larger than the budget on its own becomes
// its own call and is shrunk only if that call times out.
func batchPacks(packs []pack, maxChars int) [][]pack {
	var out [][]pack
	var cur []pack
	used := 0
	for _, p := range packs {
		cost := p.chars()
		if len(cur) > 0 && used+cost > maxChars {
			out = append(out, cur)
			cur, used = nil, 0
		}
		cur = append(cur, p)
		used += cost
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

// halveBatch splits a batch into two. It reports false for a batch of one pack,
// so a caller retrying a timeout stops splitting instead of looping.
func halveBatch(b []pack) ([][]pack, bool) {
	if len(b) < 2 {
		return nil, false
	}
	half := len(b) / 2
	return [][]pack{b[:half:half], b[half:]}, true
}

// shrinkPack drops the lowest-ranked half of a pack's candidates, keeping the
// order the ranking produced. It reports false once the pack is at or below
// MinEvidenceChars or has one candidate left: a pack that still outruns the
// call timeout there is answered lexically rather than retried forever.
func shrinkPack(p pack) (pack, bool) {
	if len(p.Candidates) < 2 || p.chars() <= MinEvidenceChars {
		return p, false
	}
	keep := len(p.Candidates) / 2
	out := p
	out.Candidates = p.Candidates[:keep:keep]
	return out, true
}

// packsDigest identifies the evidence a pass was built from. It covers the
// graph, the gathered context and the ranking in one value: if any of them
// moved, the packs moved, so a cached map keyed on it is never shown for a
// repository it was not built from.
func packsDigest(packs []pack) string {
	wires := packsWire(packs)
	sort.SliceStable(wires, func(i, j int) bool { return wires[i].EpicID < wires[j].EpicID })
	raw, err := json.Marshal(wires)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// clip bounds a string to n runes, marking a cut so a reader knows the text
// continues.
func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
