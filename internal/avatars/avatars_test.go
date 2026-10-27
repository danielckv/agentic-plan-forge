//go:build client && !server

package avatars_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/avatars"
	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/relay"
	"github.com/danielckv/agentic-plan-forge/internal/synthesizer"
	"github.com/danielckv/agentic-plan-forge/internal/synthesizer/synthtest"
)

func barrierCalls(s *stub) []*call {
	var out []*call
	for _, c := range s.callsOf("critic") {
		var p struct {
			Barrier bool `json:"barrier"`
		}
		if json.Unmarshal(decodeInput(c.Req).rawFields(), &p) == nil && p.Barrier {
			out = append(out, c)
		}
	}
	return out
}

func (i input) rawFields() json.RawMessage { return raw(i.Fields) }

// --- PM isolation ------------------------------------------------------------------------------

// The PM request may carry product fields only. Seed the graph, the technical
// appendix, the drift report, the files of the working directory and the
// environment with sentinels and prove none reaches the PM request or system
// prompt, while the technical avatars do receive the graph material.
func TestPMRequestNeverCarriesRepositoryMaterial(t *testing.T) {
	const (
		graphSym  = "SentinelGraphSymbolQ7"
		rootPath  = "/SENTINEL_ROOT_PATH_91ac"
		appendix  = "SENTINEL_APPENDIX_c0de"
		driftText = "SENTINEL_DRIFT_TEXT_55e1"
		fileText  = "SENTINEL_FILE_CONTENT_3d9a"
		envValue  = "SENTINEL_ENV_VALUE_7b2f"
		modPath   = "internal/sentinelmod"
	)
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "SECRET.md"), []byte(fileText), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	t.Setenv("PLAN_FORGE_TEST_SECRET", envValue)
	t.Setenv("SENTINEL_PATHS", repo+":"+rootPath)

	fx := synthtest.Sample()
	fx.Graph.RootPath = rootPath
	fx.Graph.Modules = append(fx.Graph.Modules, contracts.ModuleBoundary{ID: "m-sentinel", Path: modPath, Language: "go"})
	fx.Graph.Symbols["internal/sentinel/x.go"] = []contracts.Symbol{{
		ID: "sym-sentinel", Name: graphSym, Kind: contracts.SymbolFunction, Language: "go",
		Path: "internal/sentinel/x.go", StartLine: 1, EndLine: 2, Signature: "func " + graphSym + "()",
	}}
	fx.DocDrift[0].Expected += " " + driftText
	fx.PRD.TechnicalAppendix = appendix

	h := newHarness(t, fx)
	h.in.PRD, h.in.Graph, h.in.DocDrift = fx.PRD, fx.Graph, fx.DocDrift
	// Force a repair round so the repair request is checked too.
	bad := fx.PM
	bad.Stories = append([]contracts.Story(nil), fx.PM.Stories...)
	bad.Stories[0].RequirementIDs = []string{"REQ-9"}
	h.stub.on("pm", func(c *call, in input) (reply, bool) {
		if c.Nth == 1 {
			return reply{Content: raw(bad)}, true
		}
		return reply{}, false
	})
	if _, err := h.run(context.Background(), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}

	pm := h.stub.callsOf("pm")
	if len(pm) != 2 {
		t.Fatalf("PM calls = %d, want 2 (initial + one repair)", len(pm))
	}
	secrets := []string{graphSym, rootPath, appendix, driftText, fileText, envValue, modPath, repo, "internal/billing", "ListInvoices"}
	for i, c := range pm {
		whole := string(raw(c.Req)) + c.Req.SystemPrompt + string(c.Req.UserPayload)
		for _, s := range secrets {
			if strings.Contains(whole, s) {
				t.Errorf("PM call %d leaks %q", i+1, s)
			}
		}
		if c.Req.Role != contracts.RoleProductManager || c.Req.ModelSlot != contracts.SlotMain {
			t.Errorf("PM call %d role/slot = %s/%s", i+1, c.Req.Role, c.Req.ModelSlot)
		}
		if !strings.HasPrefix(c.Req.SystemPrompt, avatars.PMPreamble) {
			t.Errorf("PM call %d system prompt does not start with the PM preamble", i+1)
		}
	}
	// The first PM request is exactly the product-only builder output.
	if string(pm[0].Req.UserPayload) != string(avatars.BuildPMInput(fx.PRD.Product)) {
		t.Errorf("first PM payload differs from BuildPMInput:\n%s", pm[0].Req.UserPayload)
	}
	// The repair carries the same permitted input plus the validation problems.
	var repair struct {
		Repair struct {
			Problems []string `json:"problems"`
		} `json:"repair"`
		Input json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(pm[1].Req.UserPayload, &repair); err != nil {
		t.Fatal(err)
	}
	if string(repair.Input) != string(avatars.BuildPMInput(fx.PRD.Product)) {
		t.Error("repair input differs from the permitted PM input")
	}
	if len(repair.Repair.Problems) == 0 || !strings.Contains(strings.Join(repair.Repair.Problems, " "), "REQ-9") {
		t.Errorf("repair problems = %v", repair.Repair.Problems)
	}

	// Sanity: the test is meaningful because the technical avatars do see the
	// graph, drift and appendix, but never the local root path. The architect
	// answers in parts: the design reads the graph and the appendix, the
	// documentation part reads the drift.
	arch := h.stub.callsOf("architect")[0].Req
	for _, want := range []string{graphSym, appendix, modPath} {
		if !strings.Contains(string(arch.UserPayload), want) {
			t.Errorf("architect design request is missing %q", want)
		}
	}
	docs := h.stub.callsOf(keyArchDocs)
	if len(docs) == 0 {
		t.Fatal("no documentation-task call was made for the drift item")
	}
	if !strings.Contains(string(docs[0].Req.UserPayload), driftText) {
		t.Errorf("documentation request is missing %q", driftText)
	}
	for _, c := range h.stub.allCalls() {
		if strings.Contains(string(c.Req.UserPayload), rootPath) || strings.Contains(c.Req.SystemPrompt, rootPath) {
			t.Errorf("%s request leaks the local root path", c.Key)
		}
	}
}

// --- ordering, models, barrier -----------------------------------------------------------------

func TestRunHappyPathStageOrderAndModels(t *testing.T) {
	h := newHarness(t, synthtest.Sample())
	h.stub.delay = 10 * time.Millisecond
	si, err := h.run(context.Background(), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	mustCompile(t, si)

	calls := h.stub.allCalls()
	if calls[0].Key != "pm" || calls[1].Key != "architect" {
		t.Fatalf("stage order = %s, %s; want pm then architect", calls[0].Key, calls[1].Key)
	}
	barrier := barrierCalls(h.stub)
	if len(barrier) != 1 {
		t.Fatalf("barrier calls = %d, want 1", len(barrier))
	}
	for _, key := range []string{"specialist:billing-api", "specialist:web-ui"} {
		cs := h.stub.callsOf(key)
		if len(cs) != 1 {
			t.Fatalf("%s calls = %d, want 1", key, len(cs))
		}
		if cs[0].End == 0 || cs[0].End > barrier[0].Start {
			t.Errorf("the critic barrier started (%d) before %s finished (%d)", barrier[0].Start, key, cs[0].End)
		}
	}
	// The barrier reviews the complete current drafts.
	var payload struct {
		Specialists []synthesizer.SpecialistDoc `json:"specialists"`
		Architect   synthesizer.ArchitectDoc    `json:"architect"`
	}
	if err := json.Unmarshal(barrier[0].Req.UserPayload, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Specialists) != 2 || len(payload.Architect.Contracts) != 1 {
		t.Errorf("barrier payload has %d specialists and %d contracts", len(payload.Specialists), len(payload.Architect.Contracts))
	}
	// Only the mandatory barrier review runs for the critic.
	critics := h.stub.callsOf("critic")
	if len(critics) != 1 || critics[0].End == 0 {
		t.Fatalf("critic calls = %d, want the barrier review only", len(critics))
	}

	for _, c := range calls {
		r := c.Req
		wantSlot := contracts.SlotMain
		if c.Key == "critic" {
			wantSlot = contracts.SlotJudge
		}
		if r.ModelSlot != wantSlot {
			t.Errorf("%s uses slot %s, want %s", c.Key, r.ModelSlot, wantSlot)
		}
		// No stage names a model. The router reads role_models and what the
		// serving host actually lists; a model named here would win over both,
		// which is how one host's model id reached every other host.
		if r.Model != "" {
			t.Errorf("%s named model %q; the router decides it", c.Key, r.Model)
		}
		// A Domain Specialist carries its workstream as its lane, so the
		// specialists of one cycle are placed on hosts independently.
		wantLane := ""
		if after, found := strings.CutPrefix(c.Key, relay.StageSpecialistPrefix); found {
			wantLane = after
		}
		if r.Lane != wantLane {
			t.Errorf("%s has lane %q, want %q", c.Key, r.Lane, wantLane)
		}
		if r.RunID != h.in.RunID || r.Timeout != avatars.DefaultStageTimeout || r.MaxOutput != contracts.MaxStageOutputBytes {
			t.Errorf("%s request bounds = %q %v %d", c.Key, r.RunID, r.Timeout, r.MaxOutput)
		}
		if r.SchemaID != "" {
			t.Errorf("%s requested schema %q, but no embedded schema exists for stage documents", c.Key, r.SchemaID)
		}
	}

	if got := h.sink.cycleStarts(); len(got) != 1 || got[0] != 1 {
		t.Errorf("cycle starts = %v", got)
	}
	events := h.sink.all()
	if events[0].Type != string(relay.EventCycleStart) {
		t.Errorf("first event = %s, want CYCLE_START", events[0].Type)
	}
	for _, avatar := range []string{"pm", "architect", "specialist:billing-api", "specialist:web-ui"} {
		d := h.sink.drafts(avatar)
		if len(d) != 1 || !d[0].Complete || d[0].DraftRevision != 1 || d[0].ContentHash == "" {
			t.Errorf("drafts for %s = %+v", avatar, d)
		}
	}
	// Only the barrier's critique reaches the authority: the in-stage pass is advisory.
	if n := len(h.sink.ofType(relay.EventAvatarCritique)); n != 1 {
		t.Errorf("AVATAR_CRITIQUE events = %d, want 1", n)
	}
	for _, c := range si.Critiques {
		if c.Severity == contracts.SeverityMedium && c.Status != contracts.CritiqueResolved {
			t.Errorf("MEDIUM critique %s left open: %+v", c.ID, c)
		}
	}
}

func TestSpecialistsRunAtMostMinOfMaxParallelAndFour(t *testing.T) {
	for _, tc := range []struct{ maxParallel, want int }{{1, 1}, {2, 2}, {8, 4}, {0, 4}} {
		h := newHarness(t, bigFixture(2)) // four specialists plus concurrent diagram and doc-task calls
		h.in.MaxParallel = tc.maxParallel
		h.stub.delay = 12 * time.Millisecond
		if _, err := h.run(context.Background(), nil); err != nil {
			t.Fatalf("MaxParallel=%d: Run: %v", tc.maxParallel, err)
		}
		if got := h.stub.maxConcurrent(); got != tc.want {
			t.Errorf("MaxParallel=%d: peak concurrent provider calls = %d, want %d", tc.maxParallel, got, tc.want)
		}
	}
}

func TestSpecialistClaimPrependingSignatureAutoAlignsWithoutDecisionPrompt(t *testing.T) {
	fx := synthtest.Sample()
	k := fx.Architect.Contracts[0]
	// Producer prepends the contract signature to input_schema while quoting the exact version_hash.
	fx.Specialists[0].Implements[0].InputSchema = k.Signature + ". " + k.InputSchema
	h := newHarness(t, fx)
	si, err := h.run(context.Background(), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n := len(h.sink.ofType(relay.EventDecisionPrompt)); n != 0 {
		t.Errorf("DECISION_PROMPT events = %d, want 0 when version_hash matches and schema restates the contract", n)
	}
	mustCompile(t, si)
}

// --- cycle cap ---------------------------------------------------------------------------------

func openHigh(id string) synthesizer.CriticDoc {
	return synthesizer.CriticDoc{Critiques: []contracts.Critique{{
		ID: id, Severity: contracts.SeverityHigh, TargetIDs: []string{"billing-api"},
		Evidence: "The export does not authorize the caller.", RequiredChange: "Authorize every export request.",
		Status: contracts.CritiqueOpen,
	}}}
}

func TestCycleCapPausesAndNeverStartsAFourthCycle(t *testing.T) {
	h := newHarness(t, synthtest.Sample())
	h.stub.on("critic", func(c *call, in input) (reply, bool) { return reply{Content: raw(openHigh("C-9"))}, true })

	_, err := h.run(context.Background(), nil)
	var paused *avatars.PausedError
	if !errors.As(err, &paused) {
		t.Fatalf("Run error = %v, want *PausedError", err)
	}
	if paused.Cycle != 3 || len(paused.Blockers) != 1 || paused.Blockers[0].ID != "C-9" {
		t.Fatalf("paused = %+v", paused)
	}
	if got := h.sink.cycleStarts(); len(got) != 3 || got[0] != 1 || got[1] != 2 || got[2] != 3 {
		t.Fatalf("cycle starts = %v, want [1 2 3]", got)
	}
	if n := len(barrierCalls(h.stub)); n != 3 {
		t.Errorf("critic barriers = %d, want one per cycle", n)
	}
	if n := len(h.sink.ofType(relay.EventAvatarCritique)); n != 3 {
		t.Errorf("AVATAR_CRITIQUE events = %d, want 3", n)
	}
	// Unaffected validated drafts are preserved; only the criticised draft re-runs.
	if h.stub.count("pm") != 1 || h.stub.count("architect") != 1 || h.stub.count("specialist:web-ui") != 1 {
		t.Errorf("unrelated drafts re-ran: pm=%d architect=%d web-ui=%d",
			h.stub.count("pm"), h.stub.count("architect"), h.stub.count("specialist:web-ui"))
	}
	if got := h.stub.count("specialist:billing-api"); got != 3 {
		t.Errorf("billing-api drafts = %d, want 3", got)
	}
	last := h.sink.thoughts("critic")
	if got := last[len(last)-1]; got.Status != "Stale" || !strings.Contains(got.PublicSummary, "Paused") {
		t.Errorf("final critic thought = %+v", got)
	}

	// Re-entering the same run never adds a hidden fourth cycle.
	callsBefore, eventsBefore := h.stub.total(), len(h.sink.all())
	h.in.StartCycle = 3
	_, err = h.run(context.Background(), nil)
	if !errors.As(err, &paused) || paused.Cycle != 3 {
		t.Fatalf("re-entry error = %v, want the same pause", err)
	}
	if h.stub.total() != callsBefore || len(h.sink.all()) != eventsBefore {
		t.Error("re-entering a paused run called a provider or emitted events")
	}

	// A user-requested rerun gets a new run ID and starts again from cycle 1.
	h.in.RunID, h.in.StartCycle = "run-new", 0
	h.stub.on("critic", func(c *call, in input) (reply, bool) { return reply{}, false })
	if _, err := h.run(context.Background(), nil); err != nil {
		t.Fatalf("rerun: %v", err)
	}
	starts := h.sink.ofType(relay.EventCycleStart)
	if p := starts[len(starts)-1].Payload.(relay.CycleStartPayload); p.RunID != "run-new" || p.Cycle != 1 {
		t.Errorf("rerun began with %+v", p)
	}
}

func TestMediumFindingsAtTheCapBecomeADecision(t *testing.T) {
	fx := synthtest.Sample()
	fx.Specialists[1].Accepted = nil // nothing is accepted, so MEDIUM C-2 stays open
	for _, tc := range []struct {
		choice string
		paused bool
	}{{"accept", false}, {"pause", true}} {
		h := newHarness(t, fx)
		ch := h.sink.resolver(func(p relay.DecisionPromptPayload) string { return tc.choice })
		si, err := h.run(context.Background(), ch)
		if got := h.sink.cycleStarts(); len(got) != 3 {
			t.Fatalf("%s: cycle starts = %v, want 3 cycles of revision first", tc.choice, got)
		}
		prompts := h.sink.ofType(relay.EventDecisionPrompt)
		if len(prompts) != 1 {
			t.Fatalf("%s: decision prompts = %d", tc.choice, len(prompts))
		}
		p := prompts[0].Payload.(relay.DecisionPromptPayload)
		if len(p.Options) != 2 || p.RecommendedOptionID != "accept" || p.RunID != h.in.RunID {
			t.Fatalf("%s: prompt = %+v", tc.choice, p)
		}
		if tc.paused {
			var paused *avatars.PausedError
			if !errors.As(err, &paused) || len(paused.Blockers) != 1 || paused.Blockers[0].ID != "C-2" {
				t.Fatalf("pause: error = %v", err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("accept: %v", err)
		}
		for _, c := range si.Critiques {
			if c.ID == "C-2" && (c.Status != contracts.CritiqueResolved || !strings.HasPrefix(c.ResolutionRef, "accepted:"+p.DecisionID)) {
				t.Errorf("C-2 = %+v", c)
			}
		}
		if len(si.Decisions) != 1 || si.Decisions[0].ResolvedOptionID != "accept" {
			t.Errorf("decisions = %+v", si.Decisions)
		}
		mustCompile(t, si)
	}
}

// --- decision cards ----------------------------------------------------------------------------

// conflictHarness makes web-ui declare a different input schema for the shared
// contract: both claims name the right contract version, so the deterministic
// comparison finds two valid alternatives and a human decides.
func conflictHarness(t *testing.T, followAdopt bool) *harness {
	fx := synthtest.Sample()
	h := newHarness(t, fx)
	variant := fx.Specialists[1]
	variant.Consumes = append([]synthesizer.InterfaceClaim(nil), variant.Consumes...)
	variant.Consumes[0].InputSchema = "ExportRequest{org_id string}"
	h.stub.on("specialist:web-ui", func(c *call, in input) (reply, bool) {
		if a, ok := in.Fields["adopt_claim"]; ok && followAdopt {
			var adopt struct {
				Claim synthesizer.InterfaceClaim `json:"claim"`
			}
			if err := json.Unmarshal(a, &adopt); err != nil {
				t.Errorf("adopt_claim: %v", err)
			}
			d := fx.Specialists[1]
			d.Consumes = []synthesizer.InterfaceClaim{adopt.Claim}
			return reply{Content: raw(d)}, true
		}
		return reply{Content: raw(variant)}, true
	})
	h.stub.on("specialist:billing-api", func(c *call, in input) (reply, bool) {
		if a, ok := in.Fields["adopt_claim"]; ok {
			var adopt struct {
				Claim synthesizer.InterfaceClaim `json:"claim"`
			}
			_ = json.Unmarshal(a, &adopt)
			d := fx.Specialists[0]
			d.Implements = []synthesizer.InterfaceClaim{adopt.Claim}
			return reply{Content: raw(d)}, true
		}
		return reply{}, false
	})
	return h
}

func TestConflictingSpecialistsOpenADecisionCard(t *testing.T) {
	h := conflictHarness(t, true)
	ch := h.sink.resolver(func(p relay.DecisionPromptPayload) string { return p.RecommendedOptionID })
	si, err := h.run(context.Background(), ch)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	prompts := h.sink.ofType(relay.EventDecisionPrompt)
	if len(prompts) != 1 {
		t.Fatalf("decision prompts = %d, want 1", len(prompts))
	}
	p := prompts[0].Payload.(relay.DecisionPromptPayload)
	if p.RunID != h.in.RunID || !strings.Contains(p.Question, "export-api") {
		t.Errorf("prompt = %+v", p)
	}
	if len(p.Options) != 2 || p.Options[0].ID != "adopt:billing-api" || p.Options[1].ID != "adopt:web-ui" {
		t.Fatalf("options = %+v, want two adopt options", p.Options)
	}
	if p.RecommendedOptionID != "adopt:billing-api" {
		t.Errorf("recommended = %q, want the producer's declaration", p.RecommendedOptionID)
	}
	for _, o := range p.Options {
		if o.Label == "" || o.Tradeoff == "" || o.Impact == "" {
			t.Errorf("option %+v lacks label, tradeoff or impact", o)
		}
	}
	// The loser is re-drafted with the chosen claim; the winner is untouched.
	if h.stub.count("specialist:web-ui") != 2 || h.stub.count("specialist:billing-api") != 1 {
		t.Errorf("drafts: web-ui=%d billing-api=%d", h.stub.count("specialist:web-ui"), h.stub.count("specialist:billing-api"))
	}
	redo := h.stub.callsOf("specialist:web-ui")[1]
	if _, ok := decodeInput(redo.Req).Fields["adopt_claim"]; !ok {
		t.Error("the re-draft did not receive the chosen claim")
	}
	// The critic barrier reviews the post-decision drafts.
	b := barrierCalls(h.stub)
	if len(b) != 1 || b[0].Start < redo.End {
		t.Errorf("barrier started before the decision's re-draft finished")
	}
	if len(si.Decisions) != 1 {
		t.Fatalf("decisions = %+v", si.Decisions)
	}
	d := si.Decisions[0]
	if d.ResolvedOptionID != "adopt:billing-api" || d.ResolvedBy != "owner" || d.ResolvedAt == nil || len(d.Options) != 2 {
		t.Errorf("recorded decision = %+v", d)
	}
	docs, err := synthesizer.DecodeInput(si)
	if err != nil {
		t.Fatal(err)
	}
	if issues := synthesizer.CompareInterfaces(docs.Architect, docs.Specialists); len(issues) != 0 {
		t.Errorf("interface issues remain: %+v", issues)
	}
	mustCompile(t, si)
}

func TestDecisionForTheOtherOptionRedraftsTheProducer(t *testing.T) {
	h := conflictHarness(t, true)
	ch := h.sink.resolver(func(p relay.DecisionPromptPayload) string { return "adopt:web-ui" })
	si, err := h.run(context.Background(), ch)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if h.stub.count("specialist:billing-api") != 2 || h.stub.count("specialist:web-ui") != 1 {
		t.Errorf("drafts: billing-api=%d web-ui=%d", h.stub.count("specialist:billing-api"), h.stub.count("specialist:web-ui"))
	}
	mustCompile(t, si)
}

func TestHumanChoiceIsBindingWhenTheModelIgnoresIt(t *testing.T) {
	h := conflictHarness(t, false) // web-ui keeps declaring its own schema
	ch := h.sink.resolver(func(p relay.DecisionPromptPayload) string { return "adopt:billing-api" })
	si, err := h.run(context.Background(), ch)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	docs, err := synthesizer.DecodeInput(si)
	if err != nil {
		t.Fatal(err)
	}
	if issues := synthesizer.CompareInterfaces(docs.Architect, docs.Specialists); len(issues) != 0 {
		t.Errorf("interface issues remain after the binding choice: %+v", issues)
	}
	revs := h.sink.drafts("specialist:web-ui")
	if len(revs) != 3 || revs[2].DraftRevision != 3 {
		t.Errorf("web-ui draft revisions = %+v, want initial, re-draft and aligned revision", revs)
	}
	mustCompile(t, si)
}

// The first authorized resolution wins; unknown cards, options the card never
// offered and later conflicting resolutions change nothing.
func TestFirstValidResolutionWins(t *testing.T) {
	h := conflictHarness(t, true)
	const id = "dec-iface-export-api-c1-1"
	ch := make(chan contracts.Decision, 8)
	ch <- contracts.Decision{ID: "no-such-card", ResolvedOptionID: "adopt:web-ui"}
	ch <- contracts.Decision{ID: id, ResolvedOptionID: "adopt:ghost"}
	ch <- contracts.Decision{ID: id, ResolvedOptionID: "adopt:billing-api", ResolvedBy: "editor-1"}
	ch <- contracts.Decision{ID: id, ResolvedOptionID: "adopt:web-ui", ResolvedBy: "editor-2"}
	si, err := h.run(context.Background(), ch)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(si.Decisions) != 1 || si.Decisions[0].ID != id || si.Decisions[0].ResolvedOptionID != "adopt:billing-api" || si.Decisions[0].ResolvedBy != "editor-1" {
		t.Fatalf("decisions = %+v", si.Decisions)
	}
}

// A pending card blocks the run. Cancelling keeps the card pending in the
// checkpoint, and a retry waits on it without redoing any finished draft.
func TestPendingDecisionSurvivesCancelAndResumes(t *testing.T) {
	h := conflictHarness(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.sink.onEvent = func(ev contracts.PublicEvent) error {
		if ev.Type == string(relay.EventDecisionPrompt) {
			cancel()
		}
		return nil
	}
	_, err := h.run(ctx, make(chan contracts.Decision))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context.Canceled while the card is pending", err)
	}
	if h.stub.count("pm") != 1 || h.stub.count("architect") != 1 {
		t.Fatalf("unexpected re-runs before resume")
	}

	h.sink.onEvent = nil
	ch := make(chan contracts.Decision, 1)
	ch <- contracts.Decision{ID: "dec-iface-export-api-c1-1", ResolvedOptionID: "adopt:billing-api", ResolvedBy: "owner"}
	si, err := h.run(context.Background(), ch)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if h.stub.count("pm") != 1 || h.stub.count("architect") != 1 || h.stub.count("specialist:billing-api") != 1 {
		t.Errorf("resume redid finished drafts: pm=%d architect=%d billing-api=%d",
			h.stub.count("pm"), h.stub.count("architect"), h.stub.count("specialist:billing-api"))
	}
	if h.stub.count("specialist:web-ui") != 2 {
		t.Errorf("web-ui drafts = %d, want the original plus one re-draft", h.stub.count("specialist:web-ui"))
	}
	if n := len(h.sink.ofType(relay.EventDecisionPrompt)); n != 1 {
		t.Errorf("the card was re-opened on resume: %d prompts", n)
	}
	mustCompile(t, si)
}

// --- stage failures ----------------------------------------------------------------------------

func TestTransientFailureBeforeOutputIsRetriedOnceWithBackoff(t *testing.T) {
	h := newHarness(t, synthtest.Sample())
	h.stub.on("architect", func(c *call, in input) (reply, bool) {
		if c.Nth == 1 {
			return reply{Err: errors.New("provider overloaded")}, true
		}
		return reply{}, false
	})
	if _, err := h.run(context.Background(), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if h.stub.count("architect") != 2 {
		t.Errorf("architect calls = %d, want the failure plus one retry", h.stub.count("architect"))
	}
	if got := h.sleep.all(); len(got) != 1 || got[0] != h.in.RetryBackoff {
		t.Errorf("backoff waits = %v, want one wait of the base backoff", got)
	}
}

func TestDefaultBackoffStartsAtOneSecond(t *testing.T) {
	h := newHarness(t, synthtest.Sample())
	h.in.RetryBackoff = 0
	h.stub.on("pm", func(c *call, in input) (reply, bool) {
		if c.Nth == 1 {
			return reply{Err: errors.New("connection reset")}, true
		}
		return reply{}, false
	})
	if _, err := h.run(context.Background(), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := h.sleep.all(); len(got) != 1 || got[0] != time.Second {
		t.Errorf("backoff waits = %v, want [1s]", got)
	}
}

func TestSecondTransientFailureFailsTheStage(t *testing.T) {
	h := newHarness(t, synthtest.Sample())
	h.stub.on("architect", func(c *call, in input) (reply, bool) {
		return reply{Err: errors.New("provider overloaded")}, true
	})
	_, err := h.run(context.Background(), nil)
	var stage *avatars.StageError
	if !errors.As(err, &stage) || stage.Stage != "architect" || stage.Partial {
		t.Fatalf("Run error = %v, want a non-partial architect StageError", err)
	}
	if h.stub.count("architect") != 2 {
		t.Errorf("architect calls = %d, want 2", h.stub.count("architect"))
	}
	th := h.sink.thoughts("architect")
	if last := th[len(th)-1]; last.Status != "Failed" || !strings.Contains(last.PublicSummary, "architect stage failed") {
		t.Errorf("last architect thought = %+v", last)
	}
	if h.stub.count("specialist:billing-api") != 0 || h.stub.count("critic") != 0 {
		t.Error("stages after the failed one must not run")
	}
}

func TestFailureAfterPartialOutputIsNeverRetried(t *testing.T) {
	h := newHarness(t, synthtest.Sample())
	h.stub.on("architect", func(c *call, in input) (reply, bool) {
		return reply{Partial: true, Err: errors.New("stream cut")}, true
	})
	_, err := h.run(context.Background(), nil)
	var stage *avatars.StageError
	if !errors.As(err, &stage) || !stage.Partial || stage.Stage != "architect" {
		t.Fatalf("Run error = %v, want a partial architect StageError", err)
	}
	if h.stub.count("architect") != 1 || len(h.sleep.all()) != 0 {
		t.Errorf("architect calls = %d, waits = %v; a partial failure must not be retried", h.stub.count("architect"), h.sleep.all())
	}
}

func TestNonRetryableFailuresAreNotRetried(t *testing.T) {
	for name, failure := range map[string]error{
		"tool use":   &contracts.ErrToolUseObserved{Host: "stub", Event: "tool_use"},
		"capability": &contracts.ErrCapability{Host: "stub", Reason: "no tool-free mode"},
		"bound":      &contracts.ErrBound{Name: "stage output", Limit: 1, Observed: 2},
		"schema":     contracts.Coded(contracts.CodeInvalidPayload, "schema rejected the output"),
	} {
		h := newHarness(t, synthtest.Sample())
		h.stub.on("pm", func(c *call, in input) (reply, bool) { return reply{Err: failure}, true })
		_, err := h.run(context.Background(), nil)
		var stage *avatars.StageError
		if !errors.As(err, &stage) || stage.Stage != "pm" {
			t.Fatalf("%s: Run error = %v", name, err)
		}
		if h.stub.count("pm") != 1 {
			t.Errorf("%s: PM calls = %d, want 1", name, h.stub.count("pm"))
		}
	}
}

func TestEmptyTerminalResultCountsAsFailure(t *testing.T) {
	h := newHarness(t, synthtest.Sample())
	h.stub.on("pm", func(c *call, in input) (reply, bool) {
		if c.Nth == 1 {
			return reply{}, true // exit 0 and no result
		}
		return reply{}, false
	})
	if _, err := h.run(context.Background(), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if h.stub.count("pm") != 2 {
		t.Errorf("PM calls = %d, want the empty result plus one retry", h.stub.count("pm"))
	}
}

// A call that outran its timeout is never repeated unchanged: that spends a
// second full timeout to fail the same way. The Product Manager payload is the
// nine product fields and has no evidence to give up, so the stage fails after
// one call. A payload that can shrink is retried smaller instead; see
// TestATimedOutStageRetriesWithLessEvidence.
func TestCallTimeoutIsBoundedAndNotRepeated(t *testing.T) {
	h := newHarness(t, synthtest.Sample())
	h.in.CallTimeout = 25 * time.Millisecond
	h.stub.delay = time.Hour
	_, err := h.run(context.Background(), nil)
	var stage *avatars.StageError
	if !errors.As(err, &stage) || stage.Stage != "pm" || !errors.Is(stage.Err, context.DeadlineExceeded) {
		t.Fatalf("Run error = %v, want a PM StageError wrapping the deadline", err)
	}
	if h.stub.count("pm") != 1 {
		t.Errorf("PM calls = %d, want 1: a timed-out call is not repeated unchanged", h.stub.count("pm"))
	}
	if got := h.stub.callsOf("pm")[0].Req.Timeout; got != 25*time.Millisecond {
		t.Errorf("request timeout = %v", got)
	}
	// An over-long configured timeout is clamped to the ceiling, and an unset
	// one takes the stage default rather than the ceiling: a hung host must
	// not hold a run open for the whole of contracts.MaxGenerateTimeout.
	h2 := newHarness(t, synthtest.Sample())
	h2.in.CallTimeout = time.Hour
	if _, err := h2.run(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if got := h2.stub.callsOf("pm")[0].Req.Timeout; got != contracts.MaxGenerateTimeout {
		t.Errorf("clamped timeout = %v, want %v", got, contracts.MaxGenerateTimeout)
	}
	h3 := newHarness(t, synthtest.Sample())
	h3.in.CallTimeout = 0
	if _, err := h3.run(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if got := h3.stub.callsOf("pm")[0].Req.Timeout; got != avatars.DefaultStageTimeout {
		t.Errorf("unset timeout = %v, want the stage default %v", got, avatars.DefaultStageTimeout)
	}
}

func TestInvalidOutputIsRepairedOnceThenFailsTheStage(t *testing.T) {
	h := newHarness(t, synthtest.Sample())
	bad := h.fx.PM
	bad.Stories = append([]contracts.Story(nil), h.fx.PM.Stories...)
	bad.Stories[0].RequirementIDs = []string{"REQ-9"}
	h.stub.on("pm", func(c *call, in input) (reply, bool) { return reply{Content: raw(bad)}, true })
	_, err := h.run(context.Background(), nil)
	var stage *avatars.StageError
	if !errors.As(err, &stage) || stage.Stage != "pm" || stage.Partial {
		t.Fatalf("Run error = %v, want a PM StageError", err)
	}
	var verr *synthesizer.ValidationError
	if !errors.As(err, &verr) {
		t.Errorf("the stage error should carry the validation problems: %v", err)
	}
	if h.stub.count("pm") != 2 {
		t.Errorf("PM calls = %d, want the draft plus exactly one repair", h.stub.count("pm"))
	}
	if h.stub.count("architect") != 0 {
		t.Error("the architect must not run on an invalid PM draft")
	}
}

func TestPMReferencingRepositorySymbolsIsRejected(t *testing.T) {
	h := newHarness(t, synthtest.Sample())
	bad := h.fx.PM
	bad.Stories = append([]contracts.Story(nil), h.fx.PM.Stories...)
	bad.Stories[0].Need = "to call ListInvoices from internal/billing/invoice.go"
	h.stub.on("pm", func(c *call, in input) (reply, bool) { return reply{Content: raw(bad)}, true })
	_, err := h.run(context.Background(), nil)
	var stage *avatars.StageError
	if !errors.As(err, &stage) || !strings.Contains(err.Error(), "technical detail") {
		t.Fatalf("Run error = %v, want a technical-reference rejection", err)
	}
}

func TestCancellationStopsProviderCallsAndLeavesAResumableCheckpoint(t *testing.T) {
	h := newHarness(t, synthtest.Sample())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.stub.on("architect", func(c *call, in input) (reply, bool) {
		cancel() // the user cancels while the architect call is in flight
		<-ctx.Done()
		return reply{Err: ctx.Err()}, true
	})
	done := make(chan error, 1)
	go func() { _, err := h.run(ctx, nil); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run error = %v, want context.Canceled", err)
		}
		var stage *avatars.StageError
		if errors.As(err, &stage) {
			t.Fatal("a cancellation is not a stage failure")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop after cancellation")
	}
	h.stub.mu.Lock()
	inflight := h.stub.inflight
	h.stub.mu.Unlock()
	if inflight != 0 {
		t.Errorf("%d provider call(s) still running after Run returned", inflight)
	}
	for _, th := range h.sink.thoughts("architect") {
		if th.Status == "Failed" {
			t.Error("cancellation must not report a failed stage")
		}
	}

	// Resume: the validated PM draft is reused.
	delete(h.stub.hooks, "architect")
	if _, err := h.run(context.Background(), nil); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if h.stub.count("pm") != 1 {
		t.Errorf("PM calls = %d, want 1: resume must reuse the validated draft", h.stub.count("pm"))
	}
}

func TestFailedSpecialistKeepsSiblingDraftsForTheRetry(t *testing.T) {
	h := newHarness(t, synthtest.Sample())
	h.stub.on("specialist:web-ui", func(c *call, in input) (reply, bool) {
		if c.Nth == 1 {
			return reply{Err: &contracts.ErrCapability{Host: "stub", Reason: "refused"}}, true
		}
		return reply{}, false
	})
	_, err := h.run(context.Background(), nil)
	var stage *avatars.StageError
	if !errors.As(err, &stage) || stage.Stage != "specialist:web-ui" {
		t.Fatalf("Run error = %v, want the web-ui StageError", err)
	}
	if len(barrierCalls(h.stub)) != 0 {
		t.Error("the barrier must not run while a specialist is failed")
	}
	if _, err := h.run(context.Background(), nil); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if h.stub.count("pm") != 1 || h.stub.count("architect") != 1 || h.stub.count("specialist:billing-api") != 1 || h.stub.count("specialist:web-ui") != 2 {
		t.Errorf("retry re-ran finished work: pm=%d architect=%d billing-api=%d web-ui=%d",
			h.stub.count("pm"), h.stub.count("architect"), h.stub.count("specialist:billing-api"), h.stub.count("specialist:web-ui"))
	}
}

func TestResumeRevalidatesTheBaseline(t *testing.T) {
	h := newHarness(t, synthtest.Sample())
	h.stub.on("architect", func(c *call, in input) (reply, bool) {
		if c.Nth == 1 {
			return reply{Err: &contracts.ErrCapability{Host: "stub", Reason: "refused"}}, true
		}
		return reply{}, false
	})
	if _, err := h.run(context.Background(), nil); err == nil {
		t.Fatal("expected the architect failure")
	}
	h.in.PRD.Revision++ // the baseline moved: a saved draft must not be replayed
	if _, err := h.run(context.Background(), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if h.stub.count("pm") != 2 {
		t.Errorf("PM calls = %d, want 2 after the PRD revision changed", h.stub.count("pm"))
	}
	if th := h.sink.thoughts("pm"); !containsStatus(th, "Stale") {
		t.Errorf("no Stale thought for the discarded checkpoint: %+v", th)
	}
}

func containsStatus(th []relay.AvatarThoughtPayload, status string) bool {
	for _, t := range th {
		if t.Status == status {
			return true
		}
	}
	return false
}

func TestRefusedEventStopsTheRunWithoutAStageFailure(t *testing.T) {
	h := newHarness(t, synthtest.Sample())
	h.sink.onEvent = func(ev contracts.PublicEvent) error {
		if ev.Type == string(relay.EventAvatarDraft) {
			return errors.New("FENCED: epoch is obsolete")
		}
		return nil
	}
	_, err := h.run(context.Background(), nil)
	var stage *avatars.StageError
	if err == nil || errors.As(err, &stage) || !strings.Contains(err.Error(), "FENCED") {
		t.Fatalf("Run error = %v, want the refusal, not a stage failure", err)
	}
}

func TestEarlyCriticFailureDoesNotFailTheStage(t *testing.T) {
	h := newHarness(t, synthtest.Sample())
	h.stub.delay = 10 * time.Millisecond
	h.stub.on("critic", func(c *call, in input) (reply, bool) {
		var p struct {
			Barrier bool `json:"barrier"`
		}
		_ = json.Unmarshal(in.rawFields(), &p)
		if !p.Barrier {
			return reply{Err: &contracts.ErrCapability{Host: "stub", Reason: "judge unavailable"}}, true
		}
		return reply{}, false
	})
	si, err := h.run(context.Background(), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	mustCompile(t, si)
}

// --- synthesizer -------------------------------------------------------------------------------

func TestSynthesizeLocksTheDesignWithoutInventingWork(t *testing.T) {
	h := newHarness(t, synthtest.Sample())
	h.in.HoursPerDay = 8
	si, err := h.run(context.Background(), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	h.stub.on("synthesizer", func(c *call, in input) (reply, bool) {
		// The model tries to set schedule configuration; the run's values win.
		return reply{Content: json.RawMessage(`{"executive_summary":"Finance exports invoices.","hours_per_day":99,"max_parallel":99,"start_date":"2031-02-03"}`)}, true
	})
	before := string(si.Architecture)
	out, err := avatars.Synthesize(context.Background(), h.in, si, h.stub, h.sink.emit)
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if string(si.Architecture) != before {
		t.Error("Synthesize mutated its input")
	}
	docs, err := synthesizer.DecodeInput(out)
	if err != nil {
		t.Fatal(err)
	}
	lock := docs.Synthesis
	if lock.ExecutiveSummary != "Finance exports invoices." || lock.HoursPerDay != 8 || lock.MaxParallel != 4 || lock.StartDate != "" {
		t.Errorf("lock = %+v, want the narrative from the model and the schedule from the run", lock)
	}
	if len(docs.Architect.Workstreams) != 2 || len(docs.Specialists) != 2 {
		t.Error("the lock must not change the surviving design")
	}
	plan := mustCompile(t, out)
	if !strings.Contains(string(plan.SpecMarkdown), "Finance exports invoices.") {
		t.Error("the executive summary is missing from the compiled specification")
	}
	if synthesizer.ContentHash(out) == "" {
		t.Error("the locked input has no content hash")
	}
	req := h.stub.callsOf("synthesizer")[0].Req
	if !strings.HasPrefix(req.SystemPrompt, avatars.SynthesizerPreamble) || req.ModelSlot != contracts.SlotMain || req.Role != contracts.RoleSynthesizer {
		t.Errorf("synthesizer request = %+v", req)
	}
	th := h.sink.thoughts("synthesizer")
	if len(th) < 2 || th[0].Status != "Thinking" || th[len(th)-1].Status != "Synced" {
		t.Errorf("synthesizer thoughts = %+v", th)
	}
}

func TestSynthesizeRejectsInventedFieldsAndReportsTheStageItself(t *testing.T) {
	h := newHarness(t, synthtest.Sample())
	si, err := h.run(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	h.stub.on("synthesizer", func(c *call, in input) (reply, bool) {
		return reply{Content: json.RawMessage(`{"executive_summary":"x","tasks":[{"id":"T-NEW"}]}`)}, true
	})
	_, err = avatars.Synthesize(context.Background(), h.in, si, h.stub, h.sink.emit)
	var stage *avatars.StageError
	if !errors.As(err, &stage) || stage.Stage != "synthesizer" {
		t.Fatalf("Synthesize error = %v, want a synthesizer StageError", err)
	}
	if h.stub.count("synthesizer") != 2 {
		t.Errorf("synthesizer calls = %d, want the draft plus one repair", h.stub.count("synthesizer"))
	}
	th := h.sink.thoughts("synthesizer")
	if th[len(th)-1].Status != "Failed" {
		t.Errorf("the stage failure must be reported by the stage: %+v", th)
	}
}
