package synthesizer

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/sanitize"
)

// SynthesisSchemaID is the schema of the pre-approval draft shared with
// followers: {spec_markdown, manifest_json}.
const SynthesisSchemaID = "synthesis.v1"

// Spec section headings, in the required order. The strings are part of the
// contract with the Page 3 renderer and the tests.
const (
	SectionExecutive     = "Executive summary"
	SectionReadiness     = "Plan readiness"
	SectionPhases        = "Implementation phases"
	SectionProduct       = "Product requirements and acceptance"
	SectionConstraints   = "Immutable constraints"
	SectionDiagrams      = "Architecture and sequence diagrams"
	SectionInterfaces    = "Exact interfaces"
	SectionEpics         = "Epics, stories and subtasks"
	SectionDocSync       = "Documentation Sync Tasks"
	SectionDAG           = "Dependency DAG"
	SectionVerification  = "Verification commands"
	SectionEstimates     = "Human and agent estimates"
	SectionDecisionsCave = "Decisions and coverage caveats"
)

// SectionOrder lists the level-2 headings of a compiled spec in order.
var SectionOrder = []string{
	SectionExecutive, SectionReadiness, SectionPhases, SectionProduct, SectionConstraints, SectionDiagrams, SectionInterfaces,
	SectionEpics, SectionDocSync, SectionDAG, SectionVerification, SectionEstimates, SectionDecisionsCave,
}

// Diagrams holds the generated Mermaid sources.
type Diagrams struct {
	Architecture string `json:"architecture"`
	Sequence     string `json:"sequence"`
	HumanGantt   string `json:"human_gantt"`
	AgentGantt   string `json:"agent_gantt"`
}

// CompiledPlan is the output of Compile: the two artifacts and what Page 3
// needs to show them. Compilation has no filesystem effects.
type CompiledPlan struct {
	Title          string
	SpecMarkdown   []byte
	ManifestJSON   []byte
	SpecSHA256     string
	ManifestSHA256 string
	Diagrams       Diagrams
	Schedule       DualSchedule
	// Phases is the implementation broken into dependency waves, derived from
	// the task graph rather than written by an avatar.
	Phases []Phase
}

// DraftContent returns the synthesis.v1 draft content {spec_markdown,
// manifest_json}, validated against the embedded schema.
func (p CompiledPlan) DraftContent() (json.RawMessage, error) {
	raw, err := json.Marshal(struct {
		SpecMarkdown string `json:"spec_markdown"`
		ManifestJSON string `json:"manifest_json"`
	}{string(p.SpecMarkdown), string(p.ManifestJSON)})
	if err != nil {
		return nil, err
	}
	if err := contracts.Validate(SynthesisSchemaID, raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// ContentHash returns the SHA-256 of the compiled output with ApprovalMeta
// zeroed, so the hash is stable before an approval ID exists. It is the value
// `APPROVE.content_sha256` carries and the `content_hash` of the synthesis.v1
// draft. It returns "" when the input does not compile.
func ContentHash(input contracts.SynthesisInput) string {
	plan, err := Compile(input, contracts.ApprovalMeta{})
	if err != nil {
		return ""
	}
	return plan.ContentSHA256()
}

// ContentSHA256 hashes the spec and manifest bytes of a plan compiled with a
// zeroed ApprovalMeta. For a plan compiled with real metadata the value is not
// the approval content hash; use ContentHash.
func (p CompiledPlan) ContentSHA256() string {
	h := sha256.New()
	h.Write(p.SpecMarkdown)
	h.Write([]byte{0})
	h.Write(p.ManifestJSON)
	return hex.EncodeToString(h.Sum(nil))
}

// Docs is the decoded and validated design carried by a SynthesisInput.
type Docs struct {
	PM          PMDoc
	Architect   ArchitectDoc
	Specialists []SpecialistDoc
	Synthesis   SynthesisDoc
}

// DecodeArchitectLocked decodes an architect document that may carry the
// synthesizer's lock.
func DecodeArchitectLocked(raw []byte) (ArchitectDoc, error) {
	var d ArchitectDoc
	if err := decodeStrict(raw, &d); err != nil {
		return d, &ValidationError{Stage: "architect", Problems: []string{err.Error()}}
	}
	return d, nil
}

// DecodeInput decodes the stage documents of a SynthesisInput without
// validating them against each other.
func DecodeInput(in contracts.SynthesisInput) (Docs, error) {
	var d Docs
	var err error
	if d.PM, err = DecodePM(in.PM); err != nil {
		return d, err
	}
	if d.Architect, err = DecodeArchitectLocked(in.Architecture); err != nil {
		return d, err
	}
	for _, raw := range in.Specialists {
		s, err := DecodeSpecialist(raw)
		if err != nil {
			return d, err
		}
		AlignSpecialistClaims(&s, d.Architect)
		d.Specialists = append(d.Specialists, s)
	}
	sort.SliceStable(d.Specialists, func(i, j int) bool { return d.Specialists[i].WorkstreamID < d.Specialists[j].WorkstreamID })
	if d.Architect.Synthesis != nil {
		d.Synthesis = *d.Architect.Synthesis
	}
	return d, nil
}

// GraphTruncated reports whether the graph is a bounded snapshot that left
// symbols out. Existence checks against a truncated graph are skipped: absence
// from a snapshot is not proof of absence.
func GraphTruncated(g contracts.RepoContextGraph) bool {
	for _, n := range g.Coverage.Notes {
		l := strings.ToLower(n)
		if strings.Contains(l, "truncat") || strings.Contains(l, "omitted") {
			return true
		}
	}
	return g.Coverage.TimedOut
}

// ContextOf builds the design context for validation.
func ContextOf(pm PMDoc, g contracts.RepoContextGraph, drift []contracts.DocDriftItem) DesignContext {
	return DesignContext{PM: pm, Graph: g, DocDrift: drift, Truncated: GraphTruncated(g)}
}

// Validate runs every pre-compilation check: stage documents, interface
// comparison, plan cross-checks, critiques and decisions. Compile calls it, so
// a plan that is not fully covered cannot be compiled.
func Validate(in contracts.SynthesisInput) (Docs, error) {
	docs, err := DecodeInput(in)
	if err != nil {
		return docs, err
	}
	ctx := ContextOf(docs.PM, in.Graph, in.DocDrift)
	var problems []string
	add := func(err error) {
		if err == nil {
			return
		}
		if ve, ok := err.(*ValidationError); ok {
			for _, p := range ve.Problems {
				problems = append(problems, ve.Stage+": "+p)
			}
			return
		}
		problems = append(problems, err.Error())
	}
	add(docs.PM.Validate(PMContext{PRD: in.PRD.Product, Symbols: SymbolNames(in.Graph)}))
	add(docs.Architect.Validate(ctx))
	for _, s := range docs.Specialists {
		add(s.Validate(docs.Architect, ctx))
	}
	add(CheckPlan(docs.PM, docs.Architect, docs.Specialists))
	for _, k := range docs.Architect.Contracts {
		if k.VersionHash != ContractHash(k) {
			problems = append(problems, fmt.Sprintf("contract %s has a stale version hash", k.ID))
		}
	}
	for _, is := range CompareInterfaces(docs.Architect, docs.Specialists) {
		problems = append(problems, fmt.Sprintf("interface %s: %s", is.Kind, is.Detail))
	}
	accepted := AcceptedIDs(docs)
	for _, c := range in.Critiques {
		if c.Status == contracts.CritiqueResolved {
			continue
		}
		switch c.Severity {
		case contracts.SeverityCritical, contracts.SeverityHigh:
			problems = append(problems, fmt.Sprintf("critique %s (%s) is still open", c.ID, c.Severity))
		default:
			if !accepted[c.ID] && !strings.HasPrefix(c.ResolutionRef, "accepted:") {
				problems = append(problems, fmt.Sprintf("critique %s (MEDIUM) is neither resolved nor recorded as an accepted tradeoff", c.ID))
			}
		}
	}
	for _, d := range in.Decisions {
		if d.ResolvedOptionID == "" {
			problems = append(problems, fmt.Sprintf("decision %s is unresolved", d.ID))
		}
	}
	if len(problems) > 0 {
		return docs, &ValidationError{Stage: "synthesis", Problems: problems}
	}
	return docs, nil
}

// AcceptedIDs returns the critique IDs a draft records as accepted tradeoffs
// with a rationale.
func AcceptedIDs(d Docs) map[string]bool {
	out := map[string]bool{}
	for _, a := range d.Architect.Accepted {
		if strings.TrimSpace(a.Rationale) != "" {
			out[a.CritiqueID] = true
		}
	}
	for _, s := range d.Specialists {
		for _, a := range s.Accepted {
			if strings.TrimSpace(a.Rationale) != "" {
				out[a.CritiqueID] = true
			}
		}
	}
	return out
}

// Compile validates the input and returns the spec Markdown and manifest
// bytes. It has no filesystem effects. Identical input and metadata yield
// byte-identical output: every list is emitted in stable ID order, all text is
// canonical LF, and nothing reads the clock or the environment.
func Compile(input contracts.SynthesisInput, meta contracts.ApprovalMeta) (CompiledPlan, error) {
	docs, err := Validate(input)
	if err != nil {
		return CompiledPlan{}, err
	}
	tasks := AllTasks(docs.Architect, docs.Specialists)
	ctx := ContextOf(docs.PM, input.Graph, input.DocDrift)
	for i := range tasks {
		tasks[i] = normalizeTask(tasks[i], ctx)
	}

	opts := ScheduleOptions{HoursPerDay: docs.Synthesis.HoursPerDay, MaxParallel: docs.Synthesis.MaxParallel}
	if docs.Synthesis.StartDate != "" {
		t, err := time.Parse("2006-01-02", docs.Synthesis.StartDate)
		if err != nil {
			return CompiledPlan{}, &ValidationError{Stage: "synthesis", Problems: []string{"start_date must be YYYY-MM-DD"}}
		}
		opts.Start = t
	}
	schedule, err := BuildScheduleWith(tasks, opts)
	if err != nil {
		return CompiledPlan{}, &ValidationError{Stage: "synthesis", Problems: []string{err.Error()}}
	}

	var diagrams Diagrams
	if diagrams.Architecture, err = RenderGraph(docs.Architect.Diagrams.Architecture); err != nil {
		return CompiledPlan{}, err
	}
	if diagrams.Sequence, err = RenderSequence(docs.Architect.Diagrams.Sequence); err != nil {
		return CompiledPlan{}, err
	}
	if diagrams.HumanGantt, err = RenderGantt(GanttHuman, schedule.Human, schedule); err != nil {
		return CompiledPlan{}, err
	}
	if diagrams.AgentGantt, err = RenderGantt(GanttAgent, schedule.Agent, schedule); err != nil {
		return CompiledPlan{}, err
	}

	phases, err := Phases(tasks, docs.PM.Epics)
	if err != nil {
		return CompiledPlan{}, &ValidationError{Stage: "synthesis", Problems: []string{err.Error()}}
	}

	title := strings.TrimSpace(sanitize.Terminal(input.PRD.Product.Title))
	if title == "" {
		title = "Plan-Forge specification"
	}
	spec := renderSpec(specInput{
		in: input, docs: docs, tasks: tasks, schedule: schedule, diagrams: diagrams,
		phases: phases, title: title, meta: meta,
	})
	specBytes := []byte(spec)
	manifest, err := buildManifest(input, docs, tasks, schedule, title, meta, specBytes)
	if err != nil {
		return CompiledPlan{}, err
	}
	if root := input.Graph.RootPath; len(root) > 1 {
		if strings.Contains(spec, root) || strings.Contains(string(manifest), root) {
			return CompiledPlan{}, &ValidationError{Stage: "synthesis", Problems: []string{"compiled output contains the local repository root path"}}
		}
	}
	return CompiledPlan{
		Title:          title,
		SpecMarkdown:   specBytes,
		ManifestJSON:   manifest,
		SpecSHA256:     sha256Hex(specBytes),
		ManifestSHA256: sha256Hex(manifest),
		Diagrams:       diagrams,
		Schedule:       schedule,
		Phases:         phases,
	}, nil
}

func normalizeTask(t contracts.TechnicalTask, ctx DesignContext) contracts.TechnicalTask {
	t.Steps = append([]string{}, t.Steps...)
	t.DependsOn = sortedNonNil(t.DependsOn)
	t.AcceptanceCriteria = append([]string{}, t.AcceptanceCriteria...)
	t.DocDriftIDs = sortedNonNil(t.DocDriftIDs)
	t.Targets = append([]contracts.Target{}, t.Targets...)
	FillSignatures(t.Targets, ctx)
	t.Verification = append([]contracts.VerificationCommand{}, t.Verification...)
	for i := range t.Verification {
		t.Verification[i].Argv = append([]string{}, t.Verification[i].Argv...)
	}
	return t
}

func sortedNonNil(in []string) []string {
	out := append([]string{}, in...)
	sort.Strings(out)
	return out
}

func buildManifest(in contracts.SynthesisInput, docs Docs, tasks []contracts.TechnicalTask, schedule DualSchedule, title string, meta contracts.ApprovalMeta, spec []byte) ([]byte, error) {
	graph := in.Graph
	graph.RootPath = ""
	prd, err := json.Marshal(in.PRD)
	if err != nil {
		return nil, err
	}
	gr, err := json.Marshal(graph)
	if err != nil {
		return nil, err
	}
	type key struct{ path, op, sym string }
	seen := map[key]bool{}
	var targets []contracts.Target
	var verify []contracts.VerificationCommand
	for _, t := range tasks {
		for _, tg := range t.Targets {
			k := key{tg.Path, tg.Operation, tg.SymbolID}
			if !seen[k] {
				seen[k] = true
				targets = append(targets, tg)
			}
		}
		verify = append(verify, t.Verification...)
	}
	sort.SliceStable(targets, func(i, j int) bool {
		a, b := targets[i], targets[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Operation != b.Operation {
			return a.Operation < b.Operation
		}
		return a.SymbolID < b.SymbolID
	})
	sort.SliceStable(verify, func(i, j int) bool { return verify[i].ID < verify[j].ID })
	decisions := append([]contracts.Decision{}, in.Decisions...)
	sort.SliceStable(decisions, func(i, j int) bool { return decisions[i].ID < decisions[j].ID })

	assumptions := append([]string{}, schedule.Assumptions...)
	assumptions = append(assumptions, docs.Synthesis.EstimateAssumptions...)
	m := contracts.PlanManifest{
		SchemaVersion:        contracts.SchemaVersion,
		ApprovalID:           meta.ApprovalID,
		Title:                title,
		CreatedAt:            meta.CreatedAt.UTC(),
		GitBase:              in.Graph.GitCommitHash,
		WorktreeHash:         in.Graph.WorktreeHash,
		PRDSHA256:            sha256Hex(prd),
		GraphSHA256:          sha256Hex(gr),
		SpecSHA256:           sha256Hex(spec),
		TaskDAG:              tasks,
		Targets:              nonNilTargets(targets),
		VerificationCommands: nonNilVerify(verify),
		Decisions:            decisions,
		Coverage:             in.Graph.Coverage,
		EstimateAssumptions:  assumptions,
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal manifest: %w", err)
	}
	return append(raw, '\n'), nil
}

func nonNilTargets(in []contracts.Target) []contracts.Target {
	if in == nil {
		return []contracts.Target{}
	}
	return in
}

func nonNilVerify(in []contracts.VerificationCommand) []contracts.VerificationCommand {
	if in == nil {
		return []contracts.VerificationCommand{}
	}
	return in
}
