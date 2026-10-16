//go:build client && !server

package avatars_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/avatars"
	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/relay"
	"github.com/danielckv/agentic-plan-forge/internal/synthesizer"
	"github.com/danielckv/agentic-plan-forge/internal/synthesizer/synthtest"
)

func raw(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// call is one recorded provider request.
type call struct {
	Key   string // pm, architect, specialist:<ws>, critic, synthesizer
	Req   contracts.GenerateRequest
	Start int // global sequence number when the call began
	End   int // global sequence number when it ended (0 while running)
	Nth   int // 1-based count of calls with the same key
}

// reply is a stub response. Partial streams a text delta before the result.
type reply struct {
	Content json.RawMessage
	Err     error
	Partial bool
}

// stub is a contracts.Generator that answers from a synthtest fixture.
type stub struct {
	t  *testing.T
	fx synthtest.Fixture

	mu          sync.Mutex
	seq         int
	calls       []*call
	counts      map[string]int
	inflight    int
	maxInflight int
	delay       time.Duration
	hooks       map[string]func(c *call, in input) (reply, bool)
}

// input is the decoded permitted input of a request. For a repair request it is
// the original input; Repair holds the validation errors.
type input struct {
	Fields map[string]json.RawMessage
	Repair json.RawMessage
}

func (i input) str(key string) string {
	var s string
	_ = json.Unmarshal(i.Fields[key], &s)
	return s
}

func newStub(t *testing.T, fx synthtest.Fixture) *stub {
	return &stub{t: t, fx: fx, counts: map[string]int{}, hooks: map[string]func(*call, input) (reply, bool){}}
}

// on installs a hook for a key. A hook that returns false falls through to the
// default answer.
func (s *stub) on(key string, fn func(c *call, in input) (reply, bool)) { s.hooks[key] = fn }

func keyOf(req contracts.GenerateRequest, in input) string {
	switch req.Role {
	case contracts.RoleSpecialist:
		return relay.StageSpecialistPrefix + in.str("workstream_id")
	case contracts.RoleProductManager:
		return "pm"
	case contracts.RoleArchitect:
		return "architect"
	case contracts.RoleCritic:
		return "critic"
	case contracts.RoleSynthesizer:
		return "synthesizer"
	}
	return string(req.Role)
}

func decodeInput(req contracts.GenerateRequest) input {
	var top map[string]json.RawMessage
	_ = json.Unmarshal(req.UserPayload, &top)
	if inner, ok := top["input"]; ok {
		if _, rep := top["repair"]; rep {
			var fields map[string]json.RawMessage
			_ = json.Unmarshal(inner, &fields)
			return input{Fields: fields, Repair: top["repair"]}
		}
	}
	return input{Fields: top}
}

func (s *stub) GenerateStream(ctx context.Context, req contracts.GenerateRequest, emit func(contracts.GenerationEvent) error) (contracts.GenerationResult, error) {
	in := decodeInput(req)
	key := keyOf(req, in)
	s.mu.Lock()
	s.seq++
	s.counts[key]++
	c := &call{Key: key, Req: req, Start: s.seq, Nth: s.counts[key]}
	s.calls = append(s.calls, c)
	s.inflight++
	if s.inflight > s.maxInflight {
		s.maxInflight = s.inflight
	}
	delay := s.delay
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.inflight--
		s.seq++
		c.End = s.seq
		s.mu.Unlock()
	}()

	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return contracts.GenerationResult{}, ctx.Err()
		}
	}
	r, ok := reply{}, false
	if h := s.hooks[key]; h != nil {
		r, ok = h(c, in)
	}
	if !ok {
		r = s.defaultReply(key, in)
	}
	if r.Partial {
		if err := emit(contracts.GenerationEvent{Kind: contracts.EventTextDelta, Text: "{"}); err != nil {
			return contracts.GenerationResult{}, err
		}
	}
	if r.Err != nil {
		return contracts.GenerationResult{}, r.Err
	}
	if len(r.Content) > 0 {
		if err := emit(contracts.GenerationEvent{Kind: contracts.EventPublicSummary, PublicSummary: "working on " + key}); err != nil {
			return contracts.GenerationResult{}, err
		}
		if err := emit(contracts.GenerationEvent{Kind: contracts.EventFinal, Text: string(r.Content)}); err != nil {
			return contracts.GenerationResult{}, err
		}
	}
	return contracts.GenerationResult{Content: r.Content, Receipt: contracts.ToolFreeReceipt{Host: "stub"}}, nil
}

func (s *stub) defaultReply(key string, in input) reply {
	switch {
	case key == "pm":
		return reply{Content: raw(s.fx.PM)}
	case key == "architect":
		return reply{Content: raw(s.fx.Architect)}
	case strings.HasPrefix(key, relay.StageSpecialistPrefix):
		ws := strings.TrimPrefix(key, relay.StageSpecialistPrefix)
		for _, sp := range s.fx.Specialists {
			if sp.WorkstreamID == ws {
				return reply{Content: raw(sp)}
			}
		}
		s.t.Errorf("stub has no specialist draft for %q", ws)
	case key == "critic":
		return reply{Content: raw(synthesizer.CriticDoc{Critiques: s.fx.Critiques})}
	case key == "synthesizer":
		return reply{Content: raw(synthesizer.SynthesisDoc{ExecutiveSummary: "Finance can export invoices safely.", CoverageCaveats: []string{"One MEDIUM tradeoff is accepted."}})}
	}
	return reply{Err: fmt.Errorf("stub has no answer for %q", key)}
}

func (s *stub) count(key string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.counts[key]
}

func (s *stub) total() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func (s *stub) callsOf(key string) []*call {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*call
	for _, c := range s.calls {
		if c.Key == key {
			out = append(out, c)
		}
	}
	return out
}

func (s *stub) allCalls() []*call {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*call(nil), s.calls...)
}

func (s *stub) maxConcurrent() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.maxInflight
}

// sink records the public events and optionally reacts to them.
type sink struct {
	mu      sync.Mutex
	events  []contracts.PublicEvent
	onEvent func(contracts.PublicEvent) error
}

func (k *sink) emit(ev contracts.PublicEvent) error {
	k.mu.Lock()
	k.events = append(k.events, ev)
	fn := k.onEvent
	k.mu.Unlock()
	if fn != nil {
		return fn(ev)
	}
	return nil
}

func (k *sink) all() []contracts.PublicEvent {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]contracts.PublicEvent(nil), k.events...)
}

func (k *sink) ofType(t relay.WSEventType) []contracts.PublicEvent {
	var out []contracts.PublicEvent
	for _, ev := range k.all() {
		if ev.Type == string(t) {
			out = append(out, ev)
		}
	}
	return out
}

func (k *sink) cycleStarts() []int {
	var out []int
	for _, ev := range k.ofType(relay.EventCycleStart) {
		out = append(out, ev.Payload.(relay.CycleStartPayload).Cycle)
	}
	return out
}

// thoughts returns the AVATAR_THOUGHT payloads for one avatar.
func (k *sink) thoughts(avatar string) []relay.AvatarThoughtPayload {
	var out []relay.AvatarThoughtPayload
	for _, ev := range k.ofType(relay.EventAvatarThought) {
		if p := ev.Payload.(relay.AvatarThoughtPayload); p.AvatarID == avatar {
			out = append(out, p)
		}
	}
	return out
}

func (k *sink) drafts(avatar string) []relay.AvatarDraftPayload {
	var out []relay.AvatarDraftPayload
	for _, ev := range k.ofType(relay.EventAvatarDraft) {
		if p := ev.Payload.(relay.AvatarDraftPayload); p.AvatarID == avatar {
			out = append(out, p)
		}
	}
	return out
}

// resolver answers every DECISION_PROMPT through the returned channel with the
// option choose picks.
func (k *sink) resolver(choose func(relay.DecisionPromptPayload) string) <-chan contracts.Decision {
	ch := make(chan contracts.Decision, 16)
	k.mu.Lock()
	k.onEvent = func(ev contracts.PublicEvent) error {
		if ev.Type != string(relay.EventDecisionPrompt) {
			return nil
		}
		p := ev.Payload.(relay.DecisionPromptPayload)
		ch <- contracts.Decision{ID: p.DecisionID, InputRevision: 7, ResolvedOptionID: choose(p), ResolvedBy: "owner"}
		return nil
	}
	k.mu.Unlock()
	return ch
}

// instantSleep records requested backoffs and never waits.
type sleeper struct {
	mu   sync.Mutex
	took []time.Duration
}

func (s *sleeper) sleep(ctx context.Context, d time.Duration) error {
	s.mu.Lock()
	s.took = append(s.took, d)
	s.mu.Unlock()
	return ctx.Err()
}

func (s *sleeper) all() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Duration(nil), s.took...)
}

// harness wires a fixture, stub, sink and RunInput.
type harness struct {
	fx    synthtest.Fixture
	stub  *stub
	sink  *sink
	sleep *sleeper
	in    avatars.RunInput
}

func newHarness(t *testing.T, fx synthtest.Fixture) *harness {
	t.Helper()
	h := &harness{fx: fx, stub: newStub(t, fx), sink: &sink{}, sleep: &sleeper{}}
	h.in = avatars.RunInput{
		RunID: "run-" + t.Name(), PRD: fx.PRD, Graph: fx.Graph, GraphHash: "graph-hash", DocDrift: fx.DocDrift,
		Models:    avatars.ModelSlots{Main: "main-model", Judge: "judge-model"},
		MaxCycles: 3, MaxParallel: 4, Checkpoints: avatars.NewCheckpointStore(),
		RetryBackoff: 5 * time.Millisecond, Sleep: h.sleep.sleep,
	}
	return h
}

func (h *harness) run(ctx context.Context, decisions <-chan contracts.Decision) (contracts.SynthesisInput, error) {
	return avatars.Run(ctx, h.in, h.stub, h.sink.emit, decisions)
}

// bigFixture adds extra consumer workstreams that each own one task, so a stage
// has more specialists than the concurrency ceiling.
func bigFixture(extra int) synthtest.Fixture {
	fx := synthtest.Sample()
	for i := 1; i <= extra; i++ {
		id := fmt.Sprintf("ws-%d", i)
		fx.Architect.Workstreams = append(fx.Architect.Workstreams, contracts.Workstream{
			ID: id, Role: contracts.RoleBackend, ModulePaths: []string{fmt.Sprintf("internal/ws%d", i)}, DependsOn: []string{"billing-api"},
		})
		fx.Architect.Contracts[0].ConsumerWorkstreams = append(fx.Architect.Contracts[0].ConsumerWorkstreams, id)
	}
	synthesizer.NormalizeContracts(&fx.Architect)
	hash := fx.Architect.Contracts[0].VersionHash
	fx.Specialists[0].Implements[0].VersionHash = hash
	fx.Specialists[1].Consumes[0].VersionHash = hash
	for i := 1; i <= extra; i++ {
		id := fmt.Sprintf("ws-%d", i)
		claim := fx.Specialists[1].Consumes[0]
		fx.Specialists = append(fx.Specialists, synthesizer.SpecialistDoc{
			WorkstreamID: id, Summary: "Adopt the export contract in " + id + ".",
			Consumes: []synthesizer.InterfaceClaim{claim},
			Tasks: []contracts.TechnicalTask{{
				ID: "T-" + id, EpicID: "E1", StoryID: "S1", WorkstreamID: id, Title: "Use the export in " + id,
				Steps:              []string{"Call the export function"},
				DependsOn:          []string{"T-BE-1"},
				Targets:            []contracts.Target{{Path: fmt.Sprintf("internal/ws%d/use.go", i), Operation: contracts.OpAdd}},
				AcceptanceCriteria: []string{"The CSV contains every invoice in the range"},
				Verification:       []contracts.VerificationCommand{},
				HumanEstimate:      contracts.Estimate{Optimistic: 1, Likely: 2, Pessimistic: 3, Basis: "one new file"},
				AgentEstimate:      contracts.Estimate{Optimistic: 3, Likely: 5, Pessimistic: 8, Basis: "one new file"},
			}},
		})
	}
	return fx
}

func mustCompile(t *testing.T, si contracts.SynthesisInput) synthesizer.CompiledPlan {
	t.Helper()
	plan, err := synthesizer.Compile(si, contracts.ApprovalMeta{})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	return plan
}
