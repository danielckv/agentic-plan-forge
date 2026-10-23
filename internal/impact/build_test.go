//go:build client && !server

package impact

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// stubGen replays canned replies and records every request, the shape
// internal/ingestion's normalizer tests use. A reply may be an error, so a
// deadline or a host failure is scripted like any other answer.
type stubGen struct {
	mu        sync.Mutex
	replies   []reply
	requests  []contracts.GenerateRequest
	exhausted int
}

type reply struct {
	content string
	err     error
}

func (s *stubGen) GenerateStream(_ context.Context, req contracts.GenerateRequest, _ func(contracts.GenerationEvent) error) (contracts.GenerationResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, req)
	i := len(s.requests) - 1
	if i >= len(s.replies) {
		s.exhausted++
		i = len(s.replies) - 1
	}
	r := s.replies[i]
	if r.err != nil {
		return contracts.GenerationResult{}, r.err
	}
	return contracts.GenerationResult{Content: json.RawMessage(r.content)}, nil
}

func (s *stubGen) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

func (s *stubGen) payload(t *testing.T, i int) payload {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	var p payload
	if err := json.Unmarshal(s.requests[i].UserPayload, &p); err != nil {
		t.Fatalf("request %d payload: %v", i, err)
	}
	return p
}

// candidateID finds the identifier the evidence pass gave one declaration, so
// a test scripts a reply the way the model would see it.
func candidateID(t *testing.T, in Input, symbolOrPath string) string {
	t.Helper()
	packs, _ := buildPacks(context.Background(), in)
	for _, p := range packs {
		for _, c := range p.Candidates {
			if c.Symbol.Name == symbolOrPath || c.Hit.Path == symbolOrPath {
				return c.ID
			}
		}
	}
	t.Fatalf("%q was never offered as a candidate", symbolOrPath)
	return ""
}

func modelInput(t *testing.T, g contracts.Generator) Input {
	t.Helper()
	in := fixture()
	in.Repo = t.TempDir()
	in.Gen = g
	return in
}

func wholeAndEpicLinks(m contracts.ImpactMap) []contracts.ImpactLink { return allLinks(m) }

// TestBuildRefusesAPathTheModelInvented is the guarantee the whole design
// rests on: the model answers with identifiers it was given, and anything else
// is dropped before the window sees it.
func TestBuildRefusesAPathTheModelInvented(t *testing.T) {
	in := modelInput(t, nil)
	real := candidateID(t, in, "ExportInvoices")

	gen := &stubGen{replies: []reply{{content: `{"schema_version":1,"entries":[
		{"epic_id":"E1","candidate_id":"` + real + `","area":"Invoice export","verdict":"change","reason":"Invoices are exported here today, and this adds the date range finance asked for.","confidence":"HIGH"},
		{"epic_id":"E1","candidate_id":"c9999","area":"Payments","verdict":"change","reason":"A part of the project that was never offered as a candidate.","confidence":"HIGH"},
		{"epic_id":"E1","area":"Monthly schedule","verdict":"add","reason":"Nothing in the project runs an export on a schedule yet.","confidence":"MEDIUM"}
	]}`}}}
	in.Gen = gen

	m, err := Build(context.Background(), in)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	for _, l := range wholeAndEpicLinks(m) {
		if l.Area == "Payments" {
			t.Fatal("an entry naming an identifier that was never offered reached the window")
		}
		if l.Path != "" && in.Graph.Symbols[l.Path] == nil {
			t.Errorf("link %s points at %q, which is not in the graph", l.ID, l.Path)
		}
	}

	var changed, added *contracts.ImpactLink
	for i, l := range epicLinks(m, "E1") {
		switch l.Verdict {
		case contracts.ImpactChange:
			changed = &epicLinks(m, "E1")[i]
		case contracts.ImpactAdd:
			added = &epicLinks(m, "E1")[i]
		}
	}
	if changed == nil || changed.Path != "internal/billing/export.go" || changed.Line != 12 || changed.SymbolName != "ExportInvoices" {
		t.Fatalf("the resolved link = %+v, want the location read from the graph", changed)
	}
	if changed.Source != contracts.ImpactSourceModel || changed.Reason == "" {
		t.Errorf("a model link must carry its sentence: %+v", changed)
	}
	if added == nil || added.Path != "" {
		t.Errorf("an \"add\" entry = %+v, want no path: there is nothing to point at yet", added)
	}

	hasNote := false
	for _, n := range m.Notes {
		if strings.Contains(n, "never offered") {
			hasNote = true
		}
	}
	if !hasNote {
		t.Errorf("the dropped answer was not reported: %v", m.Notes)
	}
}

func TestBuildRequestShapeAndPayload(t *testing.T) {
	gen := &stubGen{replies: []reply{{content: `{"schema_version":1,"entries":[]}`}}}
	in := modelInput(t, gen)
	in.Repo = "/Users/someone/Develop/billing"

	if _, err := Build(context.Background(), in); err != nil {
		t.Fatalf("Build: %v", err)
	}
	req := gen.requests[0]
	if req.Role != contracts.RoleImpact {
		t.Errorf("role = %q, want %q so the receipt names this prompt", req.Role, contracts.RoleImpact)
	}
	if req.ModelSlot != contracts.SlotMain {
		t.Errorf("slot = %q, want MAIN", req.ModelSlot)
	}
	if req.SchemaID != MapSchemaID || req.Timeout != contracts.DefaultGenerateTimeout {
		t.Errorf("schema %q timeout %v", req.SchemaID, req.Timeout)
	}
	if len(req.UserPayload) > contracts.MaxStagePayloadBytes {
		t.Errorf("payload is %d bytes, over the stage bound", len(req.UserPayload))
	}
	if strings.Contains(string(req.UserPayload), in.Repo) || strings.Contains(string(req.UserPayload), "/Users/") {
		t.Error("the payload carries a machine path")
	}
	if p := gen.payload(t, 0); len(p.Epics) == 0 || p.Batch.Count < 1 {
		t.Errorf("payload = %+v", p)
	}
}

func TestBuildRepairsInvalidOutputOnce(t *testing.T) {
	in := modelInput(t, nil)
	real := candidateID(t, in, "ExportInvoices")
	good := `{"schema_version":1,"entries":[{"epic_id":"E1","candidate_id":"` + real + `","area":"Invoice export","verdict":"change","reason":"Invoices are exported here today.","confidence":"HIGH"}]}`

	gen := &stubGen{replies: []reply{
		{content: `{"schema_version":1,"entries":[{"epic_id":"E1","area":"X","verdict":"rewrite","reason":"bad verdict","confidence":"HIGH"}]}`},
		{content: good},
	}}
	in.Gen = gen

	m, err := Build(context.Background(), in)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if gen.calls() != 2 {
		t.Fatalf("calls = %d, want one repair retry", gen.calls())
	}
	p := gen.payload(t, 1)
	if p.Repair == nil || p.Repair.Errors == "" || p.Repair.PreviousOutput == "" {
		t.Errorf("the repair call carried %+v, want the errors and the previous output", p.Repair)
	}
	if len(epicLinks(m, "E1")) == 0 {
		t.Error("the repaired answer produced no links")
	}
}

func TestBuildFallsBackToKeywordsAndSaysWhy(t *testing.T) {
	for name, gen := range map[string]*stubGen{
		"the host failed":            {replies: []reply{{err: errors.New("claude: not signed in")}}},
		"the output never validated": {replies: []reply{{content: `{"nope":true}`}, {content: `{"nope":true}`}}},
	} {
		t.Run(name, func(t *testing.T) {
			in := modelInput(t, gen)
			m, err := Build(context.Background(), in)
			if err != nil {
				t.Fatalf("Build must not fail the page: %v", err)
			}
			links := epicLinks(m, "E1")
			if len(links) == 0 || links[0].SymbolName != "ExportInvoices" {
				t.Fatalf("E1 links = %+v, want the keyword matches", links)
			}
			if links[0].Source != contracts.ImpactSourceLexical {
				t.Errorf("link source = %q, want the fallback to admit what it is", links[0].Source)
			}
			if len(m.Notes) == 0 {
				t.Error("the fallback was silent")
			}
		})
	}
}

func TestBuildWithoutAGeneratorIsTheLexicalMap(t *testing.T) {
	in := modelInput(t, nil)
	in.Gen = nil
	m, err := Build(context.Background(), in)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if m.State != contracts.ImpactLexical {
		t.Errorf("state = %q, want %q", m.State, contracts.ImpactLexical)
	}
	if len(m.Notes) == 0 || !strings.Contains(strings.Join(m.Notes, " "), "no model host") {
		t.Errorf("notes = %v, want the missing host named", m.Notes)
	}
}

func TestBuildStopsOnACancelledRun(t *testing.T) {
	gen := &stubGen{replies: []reply{{content: `{"schema_version":1,"entries":[]}`}}}
	in := modelInput(t, gen)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	m, err := Build(ctx, in)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want the cancellation reported", err)
	}
	if m.Whole == nil || m.Epics == nil {
		t.Error("even a cancelled pass must return a renderable map")
	}
	if gen.calls() != 0 {
		t.Errorf("calls = %d, want none after cancellation", gen.calls())
	}
}

// TestBuildShrinksAPackThatOverranTheCallTimeout covers ADR 0004's rule
// applied to this stage: the deadline is a size signal, and a cancelled run
// never is.
func TestBuildShrinksAPackThatOverranTheCallTimeout(t *testing.T) {
	in := modelInput(t, nil)
	in.PRD.Epics = in.PRD.Epics[:1]
	real := candidateID(t, in, "ExportInvoices")

	gen := &stubGen{replies: []reply{
		{err: context.DeadlineExceeded},
		{content: `{"schema_version":1,"entries":[{"epic_id":"E1","candidate_id":"` + real + `","area":"Invoice export","verdict":"change","reason":"Invoices are exported here today.","confidence":"HIGH"}]}`},
		{content: `{"schema_version":1,"entries":[]}`},
	}}
	in.Gen = gen

	m, err := Build(context.Background(), in)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if gen.calls() < 2 {
		t.Fatalf("calls = %d, want the timeout retried", gen.calls())
	}
	if m.State != contracts.ImpactReady {
		t.Errorf("state = %q, want the pass to have recovered", m.State)
	}
}

func TestBuildGivesUpQuietlyWhenEveryRetryTimesOut(t *testing.T) {
	in := modelInput(t, nil)
	gen := &stubGen{replies: []reply{{err: context.DeadlineExceeded}}}
	in.Gen = gen

	m, err := Build(context.Background(), in)
	if err != nil {
		t.Fatalf("Build must not fail the page: %v", err)
	}
	if len(epicLinks(m, "E1")) == 0 {
		t.Error("the epic lost its keyword links as well as its model links")
	}
	if len(m.Notes) == 0 {
		t.Error("a pass that gave up said nothing")
	}
	if gen.calls() > 40 {
		t.Errorf("calls = %d; the retry did not terminate", gen.calls())
	}
}

func TestBuildCachesAndReusesTheMap(t *testing.T) {
	in := modelInput(t, nil)
	real := candidateID(t, in, "ExportInvoices")
	good := `{"schema_version":1,"entries":[{"epic_id":"E1","candidate_id":"` + real + `","area":"Invoice export","verdict":"change","reason":"Invoices are exported here today.","confidence":"HIGH"}]}`

	gen := &stubGen{replies: []reply{{content: good}}}
	in.Gen = gen
	in.ReadCache, in.WriteCache = true, true

	first, err := Build(context.Background(), in)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	after := gen.calls()

	second, err := Build(context.Background(), in)
	if err != nil {
		t.Fatalf("Build (cached): %v", err)
	}
	if gen.calls() != after {
		t.Errorf("calls went %d to %d; the cached map was rebuilt", after, gen.calls())
	}
	if second.Summary != first.Summary || len(second.Epics) != len(first.Epics) {
		t.Errorf("the cached map differs:\n%+v\n%+v", first, second)
	}

	// An edit to the code moves the worktree hash, so the map is rebuilt.
	in.Graph.WorktreeHash = "wt-after-an-edit"
	if _, err := Build(context.Background(), in); err != nil {
		t.Fatalf("Build (after an edit): %v", err)
	}
	if gen.calls() == after {
		t.Error("the code changed and the cached map was served anyway")
	}
}

func TestBuildFindsAConflictWithADecisionRecord(t *testing.T) {
	in := modelInput(t, nil)
	adr := candidateID(t, in, "docs/adrs/0004-invoices.md")

	gen := &stubGen{replies: []reply{{content: `{"schema_version":1,"entries":[
		{"epic_id":"E1","candidate_id":"` + adr + `","area":"Export format","verdict":"conflicts_with","reason":"The recorded decision fixes the export as CSV, and these requirements ask for a spreadsheet.","confidence":"HIGH"}
	]}`}}}
	in.Gen = gen

	m, err := Build(context.Background(), in)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(m.Conflicts) != 1 {
		t.Fatalf("conflicts = %+v, want the one contradiction collected", m.Conflicts)
	}
	c := m.Conflicts[0]
	if c.Path != "docs/adrs/0004-invoices.md" || c.Kind != contracts.ImpactKindDecision {
		t.Errorf("conflict = %+v, want the decision record", c)
	}
	if !strings.Contains(m.Summary, "clash") {
		t.Errorf("summary = %q, want the clash stated", m.Summary)
	}
}

func TestResolveRefusesAContradictionAimedAtCode(t *testing.T) {
	in := fixture()
	packs, _ := buildPacks(context.Background(), in)
	var codeID string
	for _, p := range packs {
		for _, c := range p.Candidates {
			if c.Kind == contracts.ImpactKindCode && codeID == "" {
				codeID = c.ID
			}
		}
	}
	links, notes := resolve(packs, wireMap{SchemaVersion: 1, Entries: []wireEntry{
		{EpicID: "E1", CandidateID: codeID, Area: "Invoice export", Verdict: contracts.ImpactConflict,
			Reason: "Code cannot state a claim a requirement contradicts.", Confidence: "HIGH"},
	}})
	if len(links["E1"]) != 0 {
		t.Errorf("links = %+v, want the entry dropped: code states no claim", links["E1"])
	}
	if len(notes) == 0 {
		t.Error("the drop was silent")
	}
}
