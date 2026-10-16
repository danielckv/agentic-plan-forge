//go:build client && !server

package avatars

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/relay"
	"github.com/danielckv/agentic-plan-forge/internal/sanitize"
	"github.com/danielckv/agentic-plan-forge/internal/synthesizer"
)

// Thought statuses understood by the Page 2 avatar cards.
const (
	statusThinking   = "Thinking"
	statusDrafting   = "Drafting"
	statusCritiquing = "Critiquing"
	statusRevising   = "Revising"
	statusSynced     = "Synced"
	statusFailed     = "Failed"
	statusStale      = "Stale"
)

// orchestrator holds the state of one Run or Synthesize call.
type orchestrator struct {
	in        RunInput
	gen       contracts.Generator
	sink      func(contracts.PublicEvent) error
	decisions <-chan contracts.Decision
	store     *CheckpointStore
	maxC      int
	par       int

	emitMu sync.Mutex

	// mu guards st, specs and early while specialists run concurrently.
	mu    sync.Mutex
	st    *runState
	pmIn  json.RawMessage
	pm    synthesizer.PMDoc
	arch  synthesizer.ArchitectDoc
	specs map[string]synthesizer.SpecialistDoc
	// early holds the non-authoritative findings of the in-stage critic. The
	// barrier receives them as hints; they never certify or block anything.
	early []contracts.Critique
}

// genFailure is a failed provider call: whether the provider had already
// streamed output decides whether an automatic retry is allowed.
type genFailure struct {
	Err     error
	Partial bool
}

func (g *genFailure) Error() string { return g.Err.Error() }
func (g *genFailure) Unwrap() error { return g.Err }

var errNoResult = errors.New("the provider returned no terminal result")

// --- events -----------------------------------------------------------------------------------

func cleanSummary(s string) string {
	return sanitize.Truncate(sanitize.Terminal(s), maxSummaryRunes)
}

func (o *orchestrator) emitEvent(ctx context.Context, t relay.WSEventType, payload any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	o.emitMu.Lock()
	defer o.emitMu.Unlock()
	if o.sink == nil {
		return nil
	}
	if err := o.sink(contracts.PublicEvent{Type: string(t), Payload: payload}); err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		return &emitError{err: err}
	}
	return nil
}

func (o *orchestrator) thought(ctx context.Context, avatar string, cycle int, status, summary string) error {
	return o.emitEvent(ctx, relay.EventAvatarThought, relay.AvatarThoughtPayload{
		RunID: o.in.RunID, AvatarID: avatar, Cycle: cycle, Status: status, PublicSummary: cleanSummary(summary),
	})
}

func (o *orchestrator) cycleStart(ctx context.Context, cycle int) error {
	return o.emitEvent(ctx, relay.EventCycleStart, relay.CycleStartPayload{
		RunID: o.in.RunID, GraphHash: o.in.GraphHash, Cycle: cycle,
	})
}

func (o *orchestrator) nextRev(avatar string) uint64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.st.Revs[avatar]++
	return o.st.Revs[avatar]
}

func (o *orchestrator) publishDraft(ctx context.Context, avatar, schema string, cycle int, raw json.RawMessage, rev uint64) error {
	err := o.emitEvent(ctx, relay.EventAvatarDraft, relay.AvatarDraftPayload{
		RunID: o.in.RunID, AvatarID: avatar, Cycle: cycle, DraftID: avatar, DraftRevision: rev,
		SchemaID: schema, Content: raw, ContentHash: sha256Hex(raw), Complete: true,
	})
	if err != nil {
		return err
	}
	return o.thought(ctx, avatar, cycle, statusSynced, "Draft validated and published.")
}

func (o *orchestrator) publishCritique(ctx context.Context, cycle int, issues []contracts.Critique) error {
	if issues == nil {
		issues = []contracts.Critique{}
	}
	return o.emitEvent(ctx, relay.EventAvatarCritique, relay.AvatarCritiquePayload{
		RunID: o.in.RunID, Cycle: cycle, Issues: issues,
	})
}

func (o *orchestrator) save() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.store.save(o.in.RunID, o.st)
}

// --- provider calls ---------------------------------------------------------------------------

// stageSpec describes one provider invocation.
type stageSpec struct {
	avatar  string
	role    contracts.RoleID
	slot    contracts.ModelSlot
	model   string
	system  string
	schema  string
	cycle   int
	status  string
	summary string
}

// requestSchemaID returns label when it names an embedded schema and "" when
// it does not. Host adapters validate the terminal result against an embedded
// schema when one is requested; the stage documents are validated here instead,
// so a label with no embedded schema must not be passed through.
func requestSchemaID(label string) string {
	for _, name := range contracts.SchemaNames() {
		if name == label {
			return label
		}
	}
	return ""
}

func (o *orchestrator) request(sp stageSpec, payload json.RawMessage) contracts.GenerateRequest {
	return contracts.GenerateRequest{
		Role: sp.role, ModelSlot: sp.slot, Model: sp.model, SystemPrompt: sp.system,
		UserPayload: payload, SchemaID: requestSchemaID(sp.schema), RunID: o.in.RunID,
		Timeout: o.in.callTimeout(), MaxOutput: contracts.MaxStageOutputBytes,
	}
}

// transient reports whether a provider failure may succeed on a retry. A tool
// use violation, a missing capability, an oversized result and a schema
// rejection would fail the same way again.
func transient(err error) bool {
	var tool *contracts.ErrToolUseObserved
	var capab *contracts.ErrCapability
	var bound *contracts.ErrBound
	if errors.As(err, &tool) || errors.As(err, &capab) || errors.As(err, &bound) {
		return false
	}
	var coded *contracts.CodedError
	if errors.As(err, &coded) && coded.Code == contracts.CodeInvalidPayload {
		return false
	}
	return true
}

// attempt runs one provider call and reports whether it streamed any output.
func (o *orchestrator) attempt(ctx context.Context, sp stageSpec, req contracts.GenerateRequest) (json.RawMessage, bool, error) {
	callCtx, cancel := context.WithTimeout(ctx, req.ClampTimeout())
	defer cancel()
	var sawOutput atomic.Bool
	var final atomic.Value
	var refused atomic.Value // always an errBox: atomic.Value rejects mixed concrete types
	res, err := o.gen.GenerateStream(callCtx, req, func(ev contracts.GenerationEvent) error {
		switch ev.Kind {
		case contracts.EventTextDelta:
			if ev.Text != "" {
				sawOutput.Store(true)
			}
		case contracts.EventPublicSummary:
			summary := ev.PublicSummary
			if summary == "" {
				summary = ev.Text
			}
			if summary != "" {
				sawOutput.Store(true)
				if terr := o.thought(callCtx, sp.avatar, sp.cycle, statusDrafting, summary); terr != nil {
					refused.Store(errBox{terr})
					return terr
				}
			}
		case contracts.EventFinal:
			sawOutput.Store(true)
			final.Store(ev.Text)
		}
		return callCtx.Err()
	})
	if v := refused.Load(); v != nil {
		return nil, sawOutput.Load(), v.(errBox).err
	}
	if err != nil {
		return nil, sawOutput.Load(), err
	}
	raw := bytes.TrimSpace(res.Content)
	if len(raw) == 0 {
		if text, ok := final.Load().(string); ok {
			raw = bytes.TrimSpace([]byte(text))
		}
	}
	if len(raw) == 0 {
		return nil, sawOutput.Load(), errNoResult
	}
	if len(raw) > contracts.MaxStageOutputBytes {
		return nil, true, &contracts.ErrBound{Name: "stage output", Limit: contracts.MaxStageOutputBytes, Observed: int64(len(raw))}
	}
	if len(res.Receipt.Observed) > 0 {
		return nil, true, &contracts.ErrToolUseObserved{Host: res.Receipt.Host, Event: strings.Join(res.Receipt.Observed, ",")}
	}
	return append(json.RawMessage(nil), raw...), true, nil
}

// generate runs a provider call with the plan's retry rule: one retry of a
// transient failure that happened before any output, after an exponential
// backoff starting at one second. A failure after partial output is never
// retried: the caller must ask explicitly, so the same work is not billed twice.
func (o *orchestrator) generate(ctx context.Context, sp stageSpec, payload json.RawMessage) (json.RawMessage, error) {
	req := o.request(sp, payload)
	for attemptNo := 0; ; attemptNo++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		raw, sawOutput, err := o.attempt(ctx, sp, req)
		if err == nil {
			return raw, nil
		}
		if cerr := ctx.Err(); cerr != nil {
			return nil, cerr
		}
		var emitErr *emitError
		if errors.As(err, &emitErr) {
			return nil, err
		}
		if sawOutput {
			return nil, &genFailure{Err: err, Partial: true}
		}
		if attemptNo == 0 && transient(err) {
			if serr := o.in.sleep(ctx, o.in.backoff()<<attemptNo); serr != nil {
				return nil, serr
			}
			continue
		}
		return nil, &genFailure{Err: err}
	}
}

// failStage converts a failure into a StageError and tells the window. Context
// and event-sink failures are not stage failures and pass through unchanged.
func (o *orchestrator) failStage(ctx context.Context, sp stageSpec, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var emitErr *emitError
	if errors.As(err, &emitErr) {
		return err
	}
	stage := &StageError{Stage: sp.avatar, Err: err}
	var gf *genFailure
	if errors.As(err, &gf) {
		stage.Partial, stage.Err = gf.Partial, gf.Err
	}
	_ = o.thought(ctx, sp.avatar, sp.cycle, statusFailed, stage.Error())
	return stage
}

func problemsOf(err error) []string {
	var ve *synthesizer.ValidationError
	if errors.As(err, &ve) {
		return ve.Problems
	}
	return []string{err.Error()}
}

type repairInput struct {
	Repair struct {
		Instruction    string          `json:"instruction"`
		Problems       []string        `json:"problems"`
		PreviousOutput json.RawMessage `json:"previous_output,omitempty"`
		PreviousText   string          `json:"previous_text,omitempty"`
	} `json:"repair"`
	Input json.RawMessage `json:"input"`
}

// repairPayload wraps the original, unchanged input with the validation errors
// of the previous output. The permitted input is the same one: nothing new
// reaches the model.
func repairPayload(input, previous json.RawMessage, err error) json.RawMessage {
	var r repairInput
	r.Repair.Instruction = "Your previous output failed validation. Return one corrected JSON object in the same shape that fixes every listed problem."
	r.Repair.Problems = problemsOf(err)
	if json.Valid(previous) {
		r.Repair.PreviousOutput = previous
	} else {
		r.Repair.PreviousText = string(previous)
	}
	r.Input = input
	raw, merr := json.Marshal(r)
	if merr != nil {
		return input
	}
	return raw
}

// runStage runs one stage: call the provider, validate, and repair once with
// the validation errors and the same input. A second invalid result is a failed
// stage. Repair is separate from the transient retry and happens at most once.
func runStage[T any](o *orchestrator, ctx context.Context, sp stageSpec, payload json.RawMessage, parse func(json.RawMessage) (T, json.RawMessage, error)) (T, json.RawMessage, error) {
	var zero T
	if err := o.thought(ctx, sp.avatar, sp.cycle, sp.status, sp.summary); err != nil {
		return zero, nil, err
	}
	raw, err := o.generate(ctx, sp, payload)
	if err != nil {
		return zero, nil, o.failStage(ctx, sp, err)
	}
	doc, norm, perr := parse(raw)
	if perr == nil {
		return doc, norm, nil
	}
	if err := o.thought(ctx, sp.avatar, sp.cycle, statusRevising,
		fmt.Sprintf("The draft failed validation with %d problem(s); requesting one repair.", len(problemsOf(perr)))); err != nil {
		return zero, nil, err
	}
	raw, err = o.generate(ctx, sp, repairPayload(payload, raw, perr))
	if err != nil {
		return zero, nil, o.failStage(ctx, sp, err)
	}
	if doc, norm, perr = parse(raw); perr != nil {
		return zero, nil, o.failStage(ctx, sp, perr)
	}
	return doc, norm, nil
}

// --- payloads ---------------------------------------------------------------------------------

type revisionInput struct {
	PreviousDraft   json.RawMessage      `json:"previous_draft,omitempty"`
	RequiredChanges []contracts.Critique `json:"required_changes,omitempty"`
}

type architectPayload struct {
	Cycle             int                        `json:"cycle"`
	PM                synthesizer.PMDoc          `json:"pm"`
	TechnicalAppendix string                     `json:"technical_appendix"`
	Graph             contracts.RepoContextGraph `json:"repository_graph"`
	DocDrift          []contracts.DocDriftItem   `json:"documentation_drift"`
	Decisions         []contracts.Decision       `json:"resolved_decisions,omitempty"`
	Revision          *revisionInput             `json:"revision,omitempty"`
}

type adoptInput struct {
	DecisionID string                     `json:"decision_id"`
	ContractID string                     `json:"contract_id"`
	Claim      synthesizer.InterfaceClaim `json:"claim"`
}

type specialistPayload struct {
	Cycle        int                           `json:"cycle"`
	WorkstreamID string                        `json:"workstream_id"`
	Workstream   contracts.Workstream          `json:"workstream"`
	Workstreams  []contracts.Workstream        `json:"workstreams"`
	Summary      string                        `json:"architecture_summary"`
	Constraints  []string                      `json:"architecture_constraints"`
	Contracts    []contracts.InterfaceContract `json:"contracts"`
	Epics        []contracts.Epic              `json:"epics"`
	Stories      []contracts.Story             `json:"stories"`
	Graph        contracts.RepoContextGraph    `json:"repository_graph"`
	Decisions    []contracts.Decision          `json:"resolved_decisions,omitempty"`
	Adopt        *adoptInput                   `json:"adopt_claim,omitempty"`
	Revision     *revisionInput                `json:"revision,omitempty"`
}

type criticPayload struct {
	Cycle         int                         `json:"cycle"`
	Barrier       bool                        `json:"barrier"`
	Architect     synthesizer.ArchitectDoc    `json:"architect"`
	Specialists   []synthesizer.SpecialistDoc `json:"specialists"`
	NotYetDrafted []string                    `json:"workstreams_not_yet_drafted,omitempty"`
	Stories       []contracts.Story           `json:"stories"`
	Graph         contracts.RepoContextGraph  `json:"repository_graph"`
	KnownTargets  []string                    `json:"known_targets"`
	Prior         []contracts.Critique        `json:"prior_critiques,omitempty"`
	Deterministic []contracts.Critique        `json:"deterministic_findings,omitempty"`
	Earlier       []contracts.Critique        `json:"earlier_findings,omitempty"`
	Decisions     []contracts.Decision        `json:"resolved_decisions,omitempty"`
}

func mustJSON(v any) json.RawMessage {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(bytes.TrimRight(buf.Bytes(), "\n"))
}

// graphForPrompt is the snapshot handed to technical avatars, without the local
// root path.
func (o *orchestrator) graphForPrompt() contracts.RepoContextGraph {
	g := o.in.Graph
	g.RootPath = ""
	return g
}

// --- parsing and validation -------------------------------------------------------------------

func (o *orchestrator) designContext() synthesizer.DesignContext {
	return synthesizer.ContextOf(o.pm, o.in.Graph, o.in.DocDrift)
}

func (o *orchestrator) parsePM(raw json.RawMessage) (synthesizer.PMDoc, json.RawMessage, error) {
	d, err := synthesizer.DecodePM(raw)
	if err != nil {
		return d, nil, err
	}
	d.Normalize()
	if err := d.Validate(synthesizer.PMContext{PRD: o.in.PRD.Product, Symbols: synthesizer.SymbolNames(o.in.Graph)}); err != nil {
		return d, nil, err
	}
	return d, mustJSON(d), nil
}

func (o *orchestrator) parseArchitect(raw json.RawMessage) (synthesizer.ArchitectDoc, json.RawMessage, error) {
	d, err := synthesizer.DecodeArchitect(raw)
	if err != nil {
		return d, nil, err
	}
	synthesizer.NormalizeContracts(&d)
	if err := d.Validate(o.designContext()); err != nil {
		return d, nil, err
	}
	return d, mustJSON(d), nil
}

func (o *orchestrator) parseSpecialist(ws string) func(json.RawMessage) (synthesizer.SpecialistDoc, json.RawMessage, error) {
	return func(raw json.RawMessage) (synthesizer.SpecialistDoc, json.RawMessage, error) {
		d, err := synthesizer.DecodeSpecialist(raw)
		if err != nil {
			return d, nil, err
		}
		var problems []string
		if d.WorkstreamID != ws {
			problems = append(problems, fmt.Sprintf("workstream_id is %q but the assigned workstream is %q", d.WorkstreamID, ws))
		}
		o.mu.Lock()
		arch := o.arch
		o.mu.Unlock()
		if verr := d.Validate(arch, o.designContext()); verr != nil {
			problems = append(problems, problemsOf(verr)...)
		}
		// Claims must echo the frozen contract identity. This is the same
		// deterministic comparison that runs across drafts, applied early so a
		// broken claim is repaired inside the stage rather than in a new cycle.
		for _, is := range synthesizer.CompareInterfaces(arch, []synthesizer.SpecialistDoc{d}) {
			if is.Kind == synthesizer.IssueBroken && containsString(is.Workstreams, ws) {
				problems = append(problems, is.Detail)
			}
		}
		if len(problems) > 0 {
			return d, nil, &synthesizer.ValidationError{Stage: "specialist", Problems: problems}
		}
		return d, mustJSON(d), nil
	}
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// parseCritic validates critic output against the targets the orchestrator
// knows. Unknown targets are dropped when a known one remains.
func parseCritic(known map[string]bool) func(json.RawMessage) (synthesizer.CriticDoc, json.RawMessage, error) {
	return func(raw json.RawMessage) (synthesizer.CriticDoc, json.RawMessage, error) {
		d, err := synthesizer.DecodeCritic(raw)
		if err != nil {
			return d, nil, err
		}
		var problems []string
		seen := map[string]bool{}
		for i := range d.Critiques {
			c := &d.Critiques[i]
			switch {
			case !synthesizer.ValidID(c.ID):
				problems = append(problems, fmt.Sprintf("critique id %q is invalid", c.ID))
			case seen[c.ID]:
				problems = append(problems, fmt.Sprintf("duplicate critique id %q", c.ID))
			case strings.HasPrefix(c.ID, deterministicPrefix):
				problems = append(problems, fmt.Sprintf("critique id %q uses the reserved %q prefix", c.ID, deterministicPrefix))
			}
			seen[c.ID] = true
			switch c.Severity {
			case contracts.SeverityCritical, contracts.SeverityHigh, contracts.SeverityMedium:
			default:
				problems = append(problems, fmt.Sprintf("critique %s has severity %q, want CRITICAL, HIGH or MEDIUM", c.ID, c.Severity))
			}
			switch c.Status {
			case "":
				c.Status = contracts.CritiqueOpen
			case contracts.CritiqueOpen, contracts.CritiqueResolved:
			default:
				problems = append(problems, fmt.Sprintf("critique %s has status %q, want open or resolved", c.ID, c.Status))
			}
			if c.Status == contracts.CritiqueOpen && (strings.TrimSpace(c.Evidence) == "" || strings.TrimSpace(c.RequiredChange) == "") {
				problems = append(problems, fmt.Sprintf("open critique %s needs evidence and a required change", c.ID))
			}
			if c.Status == contracts.CritiqueResolved && c.Severity != contracts.SeverityMedium && strings.TrimSpace(c.ResolutionRef) == "" {
				problems = append(problems, fmt.Sprintf("resolved %s critique %s needs a resolution_ref", c.Severity, c.ID))
			}
			var kept []string
			for _, t := range c.TargetIDs {
				if known[t] {
					kept = append(kept, t)
				}
			}
			if len(kept) == 0 {
				problems = append(problems, fmt.Sprintf("critique %s names no known target", c.ID))
			}
			c.TargetIDs = kept
		}
		if len(problems) > 0 {
			return d, nil, &synthesizer.ValidationError{Stage: "critic", Problems: problems}
		}
		if d.Critiques == nil {
			d.Critiques = []contracts.Critique{}
		}
		return d, mustJSON(d), nil
	}
}

func parseSynthesisDoc(raw json.RawMessage) (synthesizer.SynthesisDoc, json.RawMessage, error) {
	d, err := synthesizer.DecodeSynthesis(raw)
	if err != nil {
		return d, nil, err
	}
	if strings.TrimSpace(d.ExecutiveSummary) == "" {
		return d, nil, &synthesizer.ValidationError{Stage: "synthesizer", Problems: []string{"executive_summary is required"}}
	}
	return d, mustJSON(d), nil
}

// sortedKeys returns the keys of a set in sorted order.
func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
