//go:build client && !server

package avatars

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/danielckv/agentic-plan-forge/internal/ast"
	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/gather"
)

// A stage payload must fit contracts.MaxStagePayloadBytes. The caller bounds
// the graph snapshot, the drift list and the project context before the run
// starts, but a long PRD appendix or a large Product Manager draft can still
// push a payload past the bound. Rather than fail the stage, the payload gives
// up evidence in a fixed order, cheapest first, and names every cut in
// evidence_notes so the model treats the gap as unknown rather than absent.

// trimmable is a stage payload that can give up evidence to fit the bound.
type trimmable interface {
	// trim drops the next piece of evidence and returns the note that says
	// what is gone. ok is false when nothing droppable is left.
	trim() (note string, ok bool)
	// addNote records an evidence note in the payload.
	addNote(note string)
}

// contextNote states, in one line, the slice of evidence one call can see:
// how much of the repository graph, the project context and the drift list
// reached it, and whether anything was cut to fit. The room shows it under
// the avatar, so a reader can tell a gap that was cut from one that was never
// there. It reads the marshalled payload rather than each payload type, so
// one function covers every stage.
func contextNote(raw json.RawMessage) string {
	var p struct {
		Graph struct {
			Symbols map[string]json.RawMessage `json:"symbols"`
		} `json:"repository_graph"`
		Context *struct {
			Hits []json.RawMessage `json:"hits"`
		} `json:"project_context"`
		Drift []json.RawMessage `json:"documentation_drift"`
		Notes []string          `json:"evidence_notes"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return fmt.Sprintf("%d KiB of evidence", len(raw)>>10)
	}
	var parts []string
	if n := len(p.Graph.Symbols); n > 0 {
		parts = append(parts, fmt.Sprintf("graph %d files", n))
	}
	if p.Context != nil && len(p.Context.Hits) > 0 {
		parts = append(parts, fmt.Sprintf("context %d excerpts", len(p.Context.Hits)))
	}
	if n := len(p.Drift); n > 0 {
		parts = append(parts, fmt.Sprintf("drift %d items", n))
	}
	parts = append(parts, fmt.Sprintf("%d KiB", len(raw)>>10))
	note := strings.Join(parts, " · ")
	if len(p.Notes) > 0 {
		note += fmt.Sprintf(" · %d evidence cut", len(p.Notes))
	}
	return note
}

// fitPayload marshals p, trimming its evidence until it fits the stage bound.
// A payload that cannot be made to fit returns an ErrBound naming the parts
// that filled it. The result keeps p, so a call that outruns the clock can be
// retried from a smaller payload.
func fitPayload(avatar string, p trimmable) (*stageInput, error) {
	raw := mustJSON(p)
	for len(raw) > contracts.MaxStagePayloadBytes {
		note, ok := p.trim()
		if !ok {
			return nil, &contracts.ErrBound{
				Name:     avatar + " payload",
				Limit:    contracts.MaxStagePayloadBytes,
				Observed: int64(len(raw)),
				Detail:   largestParts(raw, 3),
			}
		}
		p.addNote(note)
		raw = mustJSON(p)
	}
	return &stageInput{raw: raw, src: p, note: contextNote(raw)}, nil
}

// largestParts names the biggest top-level fields of a payload, largest first.
func largestParts(raw json.RawMessage, n int) string {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || len(fields) == 0 {
		return ""
	}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if len(fields[keys[i]]) != len(fields[keys[j]]) {
			return len(fields[keys[i]]) > len(fields[keys[j]])
		}
		return keys[i] < keys[j]
	})
	if len(keys) > n {
		keys = keys[:n]
	}
	out := "largest parts:"
	for i, k := range keys {
		if i > 0 {
			out += ","
		}
		out += fmt.Sprintf(" %s %d bytes", k, len(fields[k]))
	}
	return out
}

// --- evidence trims ---------------------------------------------------------------------------

// storyKeywords ranks evidence against the stories the stage is planning.
func storyKeywords(stories []contracts.Story) []string {
	texts := make([]string, 0, len(stories)*3)
	for _, s := range stories {
		texts = append(texts, s.Actor, s.Need, s.Benefit)
		texts = append(texts, s.AcceptanceCriteria...)
	}
	return ast.KeywordsFromText(texts...)
}

// trimContext halves the project context, dropping the lowest-ranked excerpts
// first, then the oldest commits. A context that is already empty is dropped.
func trimContext(c *contracts.ProjectContext) (*contracts.ProjectContext, string, bool) {
	if c == nil {
		return nil, "", false
	}
	before := len(c.Hits)
	size := jsonSize(c)
	gather.FitBudget(c, max(size/2, 1))
	if len(c.Hits) == 0 && len(c.Recent) == 0 {
		return nil, noteOf("project context excerpts", before, 0), true
	}
	return c, noteOf("project context excerpts", before, len(c.Hits)), true
}

// trimGraph re-snapshots the repository graph at half its size, keeping the
// files the stories name. A graph with no symbols left has nothing to give.
func trimGraph(g *contracts.RepoContextGraph, stories []contracts.Story) (string, bool) {
	if len(g.Symbols) == 0 {
		return "", false
	}
	before := len(g.Symbols)
	snap, info := ast.Snapshot(g, ast.SnapshotOptions{
		Keywords: storyKeywords(stories), MaxBytes: max(jsonSize(g)/2, 1),
	})
	if info.IncludedFiles >= before {
		return "", false
	}
	*g = snap
	return noteOf("repository graph files", before, info.IncludedFiles), true
}

// trimText cuts a text to half its length, on a rune boundary.
func trimText(s string) (string, string, bool) {
	if s == "" {
		return s, "", false
	}
	r := []rune(s)
	kept := string(r[:len(r)/2])
	return kept, fmt.Sprintf("The technical appendix was cut to fit the stage budget: %d of %d characters are included.",
		len([]rune(kept)), len(r)), true
}

func noteOf(what string, total, kept int) string {
	if kept == 0 {
		return fmt.Sprintf("All %d %s were cut to fit the stage budget. Treat what they covered as unchecked, not as correct.", total, what)
	}
	return fmt.Sprintf("The %s were cut to fit the stage budget: %d of %d are included, most relevant first. Treat what the rest covered as unchecked, not as correct.", what, kept, total)
}

func jsonSize(v any) int {
	raw, err := json.Marshal(v)
	if err != nil {
		return 0
	}
	return len(raw)
}

// --- per-payload trim order -------------------------------------------------------------------

func (p *architectPayload) addNote(note string) { p.EvidenceNotes = append(p.EvidenceNotes, note) }

// trim gives up the project context, then the graph, then the PRD's technical
// appendix. The stories and the Product Manager draft are the stage's subject
// and are never cut.
func (p *architectPayload) trim() (string, bool) {
	if c, note, ok := trimContext(p.Context); ok {
		p.Context = c
		return note, true
	}
	if note, ok := trimGraph(&p.Graph, p.PM.Stories); ok {
		return note, true
	}
	if text, note, ok := trimText(p.TechnicalAppendix); ok {
		p.TechnicalAppendix = text
		return note, true
	}
	return "", false
}

func (p *specialistPayload) addNote(note string) { p.EvidenceNotes = append(p.EvidenceNotes, note) }

// trim gives up the project context, then the graph. The architecture, the
// contracts and the stories are the stage's subject and are never cut.
func (p *specialistPayload) trim() (string, bool) {
	if c, note, ok := trimContext(p.Context); ok {
		p.Context = c
		return note, true
	}
	if note, ok := trimGraph(&p.Graph, p.Stories); ok {
		return note, true
	}
	return "", false
}

func (p *criticPayload) addNote(note string) { p.EvidenceNotes = append(p.EvidenceNotes, note) }

// trim gives up the project context, then the graph. The drafts under review
// and every prior critique are never cut: the critic must re-list them all.
func (p *criticPayload) trim() (string, bool) {
	if c, note, ok := trimContext(p.Context); ok {
		p.Context = c
		return note, true
	}
	if note, ok := trimGraph(&p.Graph, p.Stories); ok {
		return note, true
	}
	return "", false
}
