//go:build client && !server

package avatars

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/relay"
	"github.com/danielckv/agentic-plan-forge/internal/synthesizer"
)

// errBox lets atomic.Value hold errors of different concrete types.
type errBox struct{ err error }

type taskBrief struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	StoryID string `json:"story_id,omitempty"`
}

type specialistBrief struct {
	WorkstreamID string      `json:"workstream_id"`
	Summary      string      `json:"summary"`
	Tasks        []taskBrief `json:"tasks"`
}

type synthesisSchedule struct {
	HoursPerDay float64 `json:"hours_per_day"`
	MaxParallel int     `json:"max_parallel"`
}

// synthesisPayload is everything the Lead Synthesizer sees: the surviving
// validated design in brief, the resolved decisions and the coverage notes. It
// carries no draft the compiler would not also compile.
type synthesisPayload struct {
	Product       contracts.ProductPRD          `json:"product"`
	Epics         []contracts.Epic              `json:"epics"`
	Stories       []contracts.Story             `json:"stories"`
	Summary       string                        `json:"architecture_summary"`
	Constraints   []string                      `json:"architecture_constraints"`
	Workstreams   []contracts.Workstream        `json:"workstreams"`
	Contracts     []contracts.InterfaceContract `json:"contracts"`
	DocTasks      []taskBrief                   `json:"documentation_tasks"`
	Specialists   []specialistBrief             `json:"specialists"`
	Critiques     []contracts.Critique          `json:"critiques"`
	Decisions     []contracts.Decision          `json:"resolved_decisions"`
	Coverage      contracts.Coverage            `json:"coverage"`
	DocDriftIDs   []string                      `json:"documentation_drift_ids"`
	Schedule      synthesisSchedule             `json:"schedule"`
	AcceptedCount int                           `json:"accepted_tradeoffs"`
}

func briefs(tasks []contracts.TechnicalTask) []taskBrief {
	out := make([]taskBrief, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, taskBrief{ID: t.ID, Title: t.Title, StoryID: t.StoryID})
	}
	return out
}

// Synthesize runs the Lead Synthesizer stage over a converged design. It
// receives only current validated artifacts and resolved decisions, and its
// strict output can lock narrative, constraints, caveats and schedule
// configuration only: the document has no task or contract fields, so it cannot
// invent work independently of the surviving designs. The returned input
// carries the lock inside the architecture document and has passed the
// compiler's full validation (traceability, dependencies, targets, coverage).
func Synthesize(ctx context.Context, in RunInput, si contracts.SynthesisInput, gen contracts.Generator, emit func(contracts.PublicEvent) error) (contracts.SynthesisInput, error) {
	if gen == nil {
		return contracts.SynthesisInput{}, errors.New("avatars: a generator is required")
	}
	if strings.TrimSpace(in.RunID) == "" {
		return contracts.SynthesisInput{}, errors.New("avatars: RunID is required")
	}
	o := newOrchestrator(in, gen, emit, nil)
	o.st = newRunState()
	cycle := max(1, o.store.Cycle(in.RunID))
	sp := stageSpec{
		avatar: relay.StageSynthesizer, role: contracts.RoleSynthesizer, slot: contracts.SlotMain,
		system: SynthesizerSystemPrompt(), schema: synthesizer.SchemaSynthesizer, cycle: cycle, status: statusThinking,
		summary: "Locking the surviving design and drafting the executive narrative.",
		step:    "master spec",
	}

	docs, err := synthesizer.Validate(si)
	if err != nil {
		return contracts.SynthesisInput{}, o.failStage(ctx, sp, err)
	}
	payload := mustJSON(o.synthesisPayload(si, docs))
	doc, _, err := runStage(o, ctx, sp, fixedInput(payload), parseSynthesisDoc)
	if err != nil {
		return contracts.SynthesisInput{}, err
	}
	// Schedule configuration is frozen into the run, never taken from the model.
	doc.HoursPerDay, doc.MaxParallel, doc.StartDate = in.HoursPerDay, in.parallel(), ""

	arch := docs.Architect
	arch.Synthesis = &doc
	out := si
	out.Architecture = mustJSON(arch)
	if _, err := synthesizer.Validate(out); err != nil {
		return contracts.SynthesisInput{}, o.failStage(ctx, sp, err)
	}
	if err := o.thought(ctx, relay.StageSynthesizer, cycle, statusSynced, "Design locked; ready to compile the specification."); err != nil {
		return contracts.SynthesisInput{}, err
	}
	return out, nil
}

func (o *orchestrator) synthesisPayload(si contracts.SynthesisInput, docs synthesizer.Docs) synthesisPayload {
	specs := make([]specialistBrief, 0, len(docs.Specialists))
	accepted := len(docs.Architect.Accepted)
	for _, s := range docs.Specialists {
		specs = append(specs, specialistBrief{WorkstreamID: s.WorkstreamID, Summary: s.Summary, Tasks: briefs(s.Tasks)})
		accepted += len(s.Accepted)
	}
	var driftIDs []string
	for _, d := range si.DocDrift {
		driftIDs = append(driftIDs, d.ID)
	}
	sort.Strings(driftIDs)
	return synthesisPayload{
		Product: si.PRD.Product, Epics: docs.PM.Epics, Stories: docs.PM.Stories,
		Summary: docs.Architect.Summary, Constraints: nonNil(docs.Architect.Constraints),
		Workstreams: docs.Architect.Workstreams, Contracts: docs.Architect.Contracts,
		DocTasks: briefs(docs.Architect.DocTasks), Specialists: specs,
		Critiques: nonNilCritiques(si.Critiques), Decisions: nonNilDecisions(si.Decisions),
		Coverage: si.Graph.Coverage, DocDriftIDs: nonNil(driftIDs), AcceptedCount: accepted,
		Schedule: synthesisSchedule{HoursPerDay: o.in.HoursPerDay, MaxParallel: o.in.parallel()},
	}
}

func nonNilCritiques(in []contracts.Critique) []contracts.Critique {
	if in == nil {
		return []contracts.Critique{}
	}
	return in
}

func nonNilDecisions(in []contracts.Decision) []contracts.Decision {
	if in == nil {
		return []contracts.Decision{}
	}
	return in
}
