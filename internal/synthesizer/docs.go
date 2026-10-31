// Package synthesizer turns validated avatar output into the approved plan:
// deterministic Markdown, a machine-readable manifest and the dual schedules.
// It performs no filesystem access and calls no model. Identical input always
// yields byte-identical output.
package synthesizer

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// Schema identifiers of the avatar stage documents. They label AVATAR_DRAFT
// payloads; the corresponding Go types below are the authoritative shapes.
const (
	SchemaPM          = "pm.v1"
	SchemaArchitect   = "architect.v1"
	SchemaSpecialist  = "specialist.v1"
	SchemaCritic      = "critic.v1"
	SchemaSynthesizer = "synthesizer.v1"
	// SchemaCodeReviewer labels the code reviewer's findings over the
	// assembled candidate; SchemaTechLead labels the technical leader's
	// integration record with per-finding resolutions.
	SchemaCodeReviewer = "code_reviewer.v1"
	SchemaTechLead     = "tech_lead.v1"
	// SchemaGapAgent labels the architect's expertise-gap proposals and the
	// server's admission record over them.
	SchemaGapAgent = "gap_agent.v1"
)

// Requirement and acceptance IDs handed to the Product Manager are positional:
// REQ-1 is the first requirement, AC-1 the first acceptance criterion.
const (
	RequirementPrefix = "REQ-"
	AcceptancePrefix  = "AC-"
)

// PMDoc is the Product Manager output: epics and stories traced to requirements.
type PMDoc struct {
	Epics   []contracts.Epic  `json:"epics"`
	Stories []contracts.Story `json:"stories"`
}

// DiagramNode is a node of a graph or a participant of a sequence diagram.
type DiagramNode struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// DiagramEdge is a directed edge of the architecture diagram.
type DiagramEdge struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Label string `json:"label,omitempty"`
}

// GraphDiagram is the typed architecture diagram IR.
type GraphDiagram struct {
	Nodes []DiagramNode `json:"nodes"`
	Edges []DiagramEdge `json:"edges"`
}

// Sequence message kinds.
const (
	MessageCall   = "call"
	MessageReturn = "return"
)

// SequenceMessage is one arrow of a sequence diagram.
type SequenceMessage struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Label string `json:"label"`
	Kind  string `json:"kind"`
}

// SequenceDiagram is the typed sequence diagram IR.
type SequenceDiagram struct {
	Participants []DiagramNode     `json:"participants"`
	Messages     []SequenceMessage `json:"messages"`
}

// DiagramSet carries both architect diagrams.
type DiagramSet struct {
	Architecture GraphDiagram    `json:"architecture"`
	Sequence     SequenceDiagram `json:"sequence"`
}

// AcceptedTradeoff records that a draft knowingly keeps a MEDIUM finding, with
// the reason. A MEDIUM critique is resolved or accepted here, never dropped.
type AcceptedTradeoff struct {
	CritiqueID string `json:"critique_id"`
	Rationale  string `json:"rationale"`
}

// ArchitectDoc is the Technical Architect output. Synthesis is set only by the
// Lead Synthesizer lock; an architect never supplies it.
type ArchitectDoc struct {
	Summary     string                        `json:"summary"`
	Constraints []string                      `json:"constraints"`
	Workstreams []contracts.Workstream        `json:"workstreams"`
	Contracts   []contracts.InterfaceContract `json:"contracts"`
	Diagrams    DiagramSet                    `json:"diagrams"`
	DocTasks    []contracts.TechnicalTask     `json:"doc_tasks"`
	Accepted    []AcceptedTradeoff            `json:"accepted_tradeoffs,omitempty"`
	Synthesis   *SynthesisDoc                 `json:"synthesis,omitempty"`
	// ProposedGaps lists expertise the design needs that no workstream owner
	// covers. The server verifies and admits at most MaxGapAgents of them;
	// a proposal here is not an admission.
	ProposedGaps []contracts.GapAgentDescriptor `json:"proposed_gaps,omitempty"`
}

// InterfaceClaim is what a specialist declares about one contract it produces
// or consumes. The deterministic comparison checks it against the architect.
type InterfaceClaim struct {
	ContractID   string   `json:"contract_id"`
	VersionHash  string   `json:"version_hash"`
	InputSchema  string   `json:"input_schema,omitempty"`
	OutputSchema string   `json:"output_schema,omitempty"`
	Errors       []string `json:"errors,omitempty"`
}

// SpecialistDoc is one Domain Specialist output for one workstream.
type SpecialistDoc struct {
	WorkstreamID string                    `json:"workstream_id"`
	Summary      string                    `json:"summary"`
	DataModels   []string                  `json:"data_models,omitempty"`
	Implements   []InterfaceClaim          `json:"implements,omitempty"`
	Consumes     []InterfaceClaim          `json:"consumes,omitempty"`
	Tasks        []contracts.TechnicalTask `json:"tasks"`
	Accepted     []AcceptedTradeoff        `json:"accepted_tradeoffs,omitempty"`
}

// CriticDoc is the Red-Team Critic output.
type CriticDoc struct {
	Critiques []contracts.Critique `json:"critiques"`
}

// TechLeadDoc is the Technical Leader's integration record: one entry per
// finding with its resolution and the plan sections the repair touched.
type TechLeadDoc struct {
	Resolutions []TechLeadResolution `json:"resolutions"`
}

// TechLeadResolution integrates one finding. An empty Sections with a
// blocking resolution means the finding was accepted without a repair, which
// requires a rationale.
type TechLeadResolution struct {
	FindingID string   `json:"finding_id"`
	Blocks    bool     `json:"blocks"`
	Sections  []string `json:"sections,omitempty"`
	Rationale string   `json:"rationale,omitempty"`
}

// SynthesisDoc is the Lead Synthesizer lock. It has no task or contract fields
// by construction: the synthesizer cannot invent work independently of the
// surviving designs. HoursPerDay, MaxParallel and StartDate are schedule
// configuration frozen into the run so compilation never reads configuration
// or the clock.
type SynthesisDoc struct {
	ExecutiveSummary    string   `json:"executive_summary"`
	Constraints         []string `json:"constraints,omitempty"`
	CoverageCaveats     []string `json:"coverage_caveats,omitempty"`
	EstimateAssumptions []string `json:"estimate_assumptions,omitempty"`
	VerificationNotes   []string `json:"verification_notes,omitempty"`
	HoursPerDay         float64  `json:"hours_per_day,omitempty"`
	MaxParallel         int      `json:"max_parallel,omitempty"`
	StartDate           string   `json:"start_date,omitempty"`
}

// ValidationError lists every problem found in one stage document so a single
// repair round can address all of them.
type ValidationError struct {
	Stage    string
	Problems []string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("%s output invalid: %s", e.Stage, strings.Join(e.Problems, "; "))
}

func problemsToError(stage string, problems []string) error {
	if len(problems) == 0 {
		return nil
	}
	return &ValidationError{Stage: stage, Problems: problems}
}

// decodeStrict decodes exactly one JSON value, rejecting unknown fields and
// trailing data.
func decodeStrict(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing data after JSON value")
	}
	return nil
}

// DecodePM strictly decodes Product Manager output.
func DecodePM(raw []byte) (PMDoc, error) {
	var d PMDoc
	if err := decodeStrict(raw, &d); err != nil {
		return d, &ValidationError{Stage: "pm", Problems: []string{err.Error()}}
	}
	return d, nil
}

// DecodeArchitect strictly decodes Technical Architect output.
func DecodeArchitect(raw []byte) (ArchitectDoc, error) {
	var d ArchitectDoc
	if err := decodeStrict(raw, &d); err != nil {
		return d, &ValidationError{Stage: "architect", Problems: []string{err.Error()}}
	}
	if d.Synthesis != nil {
		return d, &ValidationError{Stage: "architect", Problems: []string{`"synthesis" is reserved for the Lead Synthesizer`}}
	}
	return d, nil
}

// DecodeSpecialist strictly decodes Domain Specialist output.
func DecodeSpecialist(raw []byte) (SpecialistDoc, error) {
	var d SpecialistDoc
	if err := decodeStrict(raw, &d); err != nil {
		return d, &ValidationError{Stage: "specialist", Problems: []string{err.Error()}}
	}
	NormalizeSpecialist(&d)
	return d, nil
}

// NormalizeSpecialist fills in what the specialist envelope already decides.
// A specialist drafts one workstream, so every task it writes belongs to that
// workstream: a task that leaves workstream_id empty inherits the draft's.
// A task that names a different workstream is left alone and Validate rejects
// it, because that is a real disagreement rather than an omitted field.
func NormalizeSpecialist(d *SpecialistDoc) {
	if d.WorkstreamID == "" {
		return
	}
	for i := range d.Tasks {
		if d.Tasks[i].WorkstreamID == "" {
			d.Tasks[i].WorkstreamID = d.WorkstreamID
		}
	}
}

// DecodeCritic strictly decodes Red-Team Critic output.
func DecodeCritic(raw []byte) (CriticDoc, error) {
	var d CriticDoc
	if err := decodeStrict(raw, &d); err != nil {
		return d, &ValidationError{Stage: "critic", Problems: []string{err.Error()}}
	}
	return d, nil
}

// DecodeTechLead strictly decodes Technical Leader output.
func DecodeTechLead(raw []byte) (TechLeadDoc, error) {
	var d TechLeadDoc
	if err := decodeStrict(raw, &d); err != nil {
		return d, &ValidationError{Stage: "tech_lead", Problems: []string{err.Error()}}
	}
	return d, nil
}

// DecodeSynthesis strictly decodes Lead Synthesizer output.
func DecodeSynthesis(raw []byte) (SynthesisDoc, error) {
	var d SynthesisDoc
	if err := decodeStrict(raw, &d); err != nil {
		return d, &ValidationError{Stage: "synthesizer", Problems: []string{err.Error()}}
	}
	return d, nil
}

// ---------------------------------------------------------------- helpers

var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,63}$`)

// ValidID reports whether s is a stable identifier usable in IDs and anchors.
func ValidID(s string) bool { return idPattern.MatchString(s) }

// LocalRel reports whether p is a clean, forward-slash, repository-relative path
// that cannot leave the repository root.
func LocalRel(p string) bool {
	if p == "" || strings.ContainsAny(p, "\\\x00") || strings.HasPrefix(p, "/") {
		return false
	}
	if path.Clean(p) != p {
		return false
	}
	return filepath.IsLocal(filepath.FromSlash(p))
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// ContractHash is the deterministic version hash of a contract: it covers
// everything a consumer depends on and nothing that is merely descriptive.
func ContractHash(c contracts.InterfaceContract) string {
	canon := struct {
		ID        string   `json:"id"`
		Owner     string   `json:"owner"`
		Language  string   `json:"language"`
		Signature string   `json:"signature"`
		In        string   `json:"in"`
		Out       string   `json:"out"`
		Errors    []string `json:"errors"`
		Consumers []string `json:"consumers"`
	}{
		c.ID, c.OwnerWorkstream, c.Language, c.Signature, c.InputSchema, c.OutputSchema,
		nonNil(sortedCopy(c.Errors)), nonNil(sortedCopy(c.ConsumerWorkstreams)),
	}
	raw, _ := json.Marshal(canon)
	return sha256Hex(raw)
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// NormalizeContracts stamps every contract with its deterministic VersionHash.
// Models cannot compute hashes, so any value they supplied is replaced: the
// hash is the identity specialists must echo.
func NormalizeContracts(a *ArchitectDoc) {
	for i := range a.Contracts {
		a.Contracts[i].VersionHash = ContractHash(a.Contracts[i])
	}
}

// topoCycle reports a dependency cycle in the graph, naming one member, or "".
func topoCycle(deps map[string][]string) string {
	const (
		white = iota
		grey
		black
	)
	state := map[string]int{}
	keys := make([]string, 0, len(deps))
	for k := range deps {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var visit func(string) string
	visit = func(n string) string {
		state[n] = grey
		next := sortedCopy(deps[n])
		for _, m := range next {
			if _, known := deps[m]; !known {
				continue
			}
			switch state[m] {
			case grey:
				return m
			case white:
				if c := visit(m); c != "" {
					return c
				}
			}
		}
		state[n] = black
		return ""
	}
	for _, k := range keys {
		if state[k] == white {
			if c := visit(k); c != "" {
				return c
			}
		}
	}
	return ""
}

var technicalPathPattern = regexp.MustCompile(`[A-Za-z0-9_.-]+(?:/[A-Za-z0-9_.-]+)+\.[A-Za-z0-9]{1,8}\b|\b[A-Za-z0-9_-]+\.(?:go|ts|tsx|js|jsx|py|rs|java|rb|sql|sh|yaml|yml|toml)\b`)

// TechnicalReference reports whether text names a file path or one of the given
// symbol names. The Product Manager must not mention either.
func TechnicalReference(text string, symbols map[string]struct{}) string {
	if m := technicalPathPattern.FindString(text); m != "" {
		return m
	}
	if len(symbols) == 0 {
		return ""
	}
	for _, w := range strings.FieldsFunc(text, func(r rune) bool {
		return !(r == '_' || r == '.' || r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z')
	}) {
		if _, ok := symbols[w]; ok {
			return w
		}
	}
	return ""
}

// SymbolNames returns the symbol and qualified names of a graph that only code
// could have written: at least 4 characters and code-shaped. A name that reads
// as ordinary English is left out; see codeShaped.
func SymbolNames(g contracts.RepoContextGraph) map[string]struct{} {
	out := map[string]struct{}{}
	add := func(name string) {
		if len(name) >= 4 && codeShaped(name) {
			out[name] = struct{}{}
		}
	}
	for _, syms := range g.Symbols {
		for _, s := range syms {
			add(s.Name)
			add(s.QualifiedName)
		}
	}
	return out
}

// codeShaped reports whether a name carries a separator, a digit or a capital
// after the first character: `load_article`, `h2Title`, `billing.Invoice` and
// `ListInvoices` do, `article` and `Article` do not.
//
// The Product Manager never sees the repository, so it cannot know which words
// that repository happens to declare as identifiers. One 922-file repository
// declares `article`, `entry`, `load`, `start` and `connected`; 165 of its
// 18275 symbol names are plain English words, `user`, `search`, `request` and
// `status` among them. Matching a bare word rejected every draft the Product
// Manager could write, over language the prompt never forbade. A leak of the
// kind the prompt does forbid is code-shaped and still matches.
func codeShaped(name string) bool {
	for i, r := range name {
		switch {
		case r == '_' || r == '.':
			return true
		case r >= '0' && r <= '9':
			return true
		case i > 0 && r >= 'A' && r <= 'Z':
			return true
		}
	}
	return false
}

// ----------------------------------------------------------- PM validation

// PMContext is what PM validation needs. It carries product facts only.
type PMContext struct {
	PRD     contracts.ProductPRD
	Symbols map[string]struct{}
}

// Validate checks the PM output: schema-level shape, stable IDs, requirement
// traceability and the absence of technical references.
func (d PMDoc) Validate(c PMContext) error {
	var p []string
	if len(d.Epics) == 0 {
		p = append(p, "at least one epic is required")
	}
	if len(d.Stories) == 0 {
		p = append(p, "at least one story is required")
	}
	reqIDs := map[string]bool{}
	for i := range c.PRD.Requirements {
		reqIDs[fmt.Sprintf("%s%d", RequirementPrefix, i+1)] = true
	}
	epicIDs := map[string]bool{}
	for _, e := range d.Epics {
		switch {
		case !ValidID(e.ID):
			p = append(p, fmt.Sprintf("epic id %q is invalid", e.ID))
		case epicIDs[e.ID]:
			p = append(p, fmt.Sprintf("duplicate epic id %q", e.ID))
		}
		epicIDs[e.ID] = true
		if strings.TrimSpace(e.Title) == "" {
			p = append(p, fmt.Sprintf("epic %s has no title", e.ID))
		}
		if ref := TechnicalReference(e.Title+" "+e.Goal, c.Symbols); ref != "" {
			p = append(p, fmt.Sprintf("epic %s references technical detail %q", e.ID, ref))
		}
	}
	storyIDs := map[string]bool{}
	covered := map[string]bool{}
	for _, s := range d.Stories {
		switch {
		case !ValidID(s.ID):
			p = append(p, fmt.Sprintf("story id %q is invalid", s.ID))
		case storyIDs[s.ID]:
			p = append(p, fmt.Sprintf("duplicate story id %q", s.ID))
		}
		storyIDs[s.ID] = true
		if !epicIDs[s.EpicID] {
			p = append(p, fmt.Sprintf("story %s references unknown epic %q", s.ID, s.EpicID))
		}
		if strings.TrimSpace(s.Actor) == "" || strings.TrimSpace(s.Need) == "" {
			p = append(p, fmt.Sprintf("story %s needs an actor and a need", s.ID))
		}
		if len(s.AcceptanceCriteria) == 0 {
			p = append(p, fmt.Sprintf("story %s has no acceptance criteria", s.ID))
		}
		if len(s.RequirementIDs) == 0 {
			p = append(p, fmt.Sprintf("story %s traces to no requirement", s.ID))
		}
		for _, r := range s.RequirementIDs {
			if !reqIDs[r] {
				p = append(p, fmt.Sprintf("story %s references unknown requirement %q", s.ID, r))
			}
			covered[r] = true
		}
		text := s.Actor + " " + s.Need + " " + s.Benefit + " " + strings.Join(s.AcceptanceCriteria, " ")
		if ref := TechnicalReference(text, c.Symbols); ref != "" {
			p = append(p, fmt.Sprintf("story %s references technical detail %q", s.ID, ref))
		}
	}
	for i := range c.PRD.Requirements {
		id := fmt.Sprintf("%s%d", RequirementPrefix, i+1)
		if !covered[id] {
			p = append(p, fmt.Sprintf("requirement %s is not covered by any story", id))
		}
	}
	return problemsToError("pm", p)
}

// Normalize rebuilds each epic's story_ids from the stories so the two views
// cannot disagree, in stable order.
func (d *PMDoc) Normalize() {
	byEpic := map[string][]string{}
	for _, s := range d.Stories {
		byEpic[s.EpicID] = append(byEpic[s.EpicID], s.ID)
	}
	for i := range d.Epics {
		ids := byEpic[d.Epics[i].ID]
		sort.Strings(ids)
		d.Epics[i].StoryIDs = ids
	}
}

// ------------------------------------------------- architect/specialist checks

// DesignContext is the repository grounding used to validate technical drafts.
type DesignContext struct {
	PM        PMDoc
	Graph     contracts.RepoContextGraph
	DocDrift  []contracts.DocDriftItem
	Truncated bool
}

func (c DesignContext) symbolByID() map[string]contracts.Symbol {
	out := map[string]contracts.Symbol{}
	for _, syms := range c.Graph.Symbols {
		for _, s := range syms {
			out[s.ID] = s
		}
	}
	return out
}

// codeExtensions are the source extensions the scanner parses. A modify or
// remove target with one of them must appear in the graph; other files (docs,
// configuration, assets) carry no symbols, so their existence cannot be judged
// from the graph and is not asserted.
var codeExtensions = map[string]bool{
	".go": true, ".js": true, ".jsx": true, ".ts": true, ".tsx": true, ".py": true, ".rs": true,
}

// pathExists reports whether an existing path is consistent with the graph.
func (c DesignContext) pathExists(p string) bool {
	if _, ok := c.Graph.Symbols[p]; ok {
		return true
	}
	if !codeExtensions[strings.ToLower(path.Ext(p))] {
		return true
	}
	return false
}

func validateTargets(owner string, targets []contracts.Target, c DesignContext, p *[]string) {
	syms := c.symbolByID()
	for _, t := range targets {
		if !LocalRel(t.Path) {
			*p = append(*p, fmt.Sprintf("%s: target path %q must be a clean repository-relative path", owner, t.Path))
			continue
		}
		switch t.Operation {
		case contracts.OpAdd, contracts.OpModify, contracts.OpRemove, contracts.OpDocument:
		default:
			*p = append(*p, fmt.Sprintf("%s: target %q has unknown operation %q", owner, t.Path, t.Operation))
			continue
		}
		if t.SymbolID != "" && !c.Truncated {
			s, ok := syms[t.SymbolID]
			switch {
			case !ok:
				*p = append(*p, fmt.Sprintf("%s: target %q names symbol %q that does not exist", owner, t.Path, t.SymbolID))
			case s.Path != t.Path:
				*p = append(*p, fmt.Sprintf("%s: symbol %q lives in %q, not %q", owner, t.SymbolID, s.Path, t.Path))
			case t.ExpectedSignatureSHA256 != "" && s.SignatureSHA256 != "" && t.ExpectedSignatureSHA256 != s.SignatureSHA256:
				*p = append(*p, fmt.Sprintf("%s: symbol %q signature hash does not match the repository", owner, t.SymbolID))
			}
		}
		if c.Truncated {
			continue
		}
		switch t.Operation {
		case contracts.OpModify, contracts.OpRemove:
			if t.SymbolID == "" && !c.pathExists(t.Path) {
				*p = append(*p, fmt.Sprintf("%s: %s target %q does not exist; new targets must be operation add", owner, t.Operation, t.Path))
			}
		case contracts.OpAdd:
			if _, exists := c.Graph.Symbols[t.Path]; exists {
				*p = append(*p, fmt.Sprintf("%s: add target %q already exists; use modify", owner, t.Path))
			}
		}
	}
}

// FillSignatures stamps expected_signature_sha256 from the graph for every
// symbol-bearing target that lacks one. The repository, not the model, is the
// source of truth for signature hashes.
func FillSignatures(targets []contracts.Target, c DesignContext) {
	syms := c.symbolByID()
	for i := range targets {
		if targets[i].SymbolID == "" || targets[i].ExpectedSignatureSHA256 != "" {
			continue
		}
		if s, ok := syms[targets[i].SymbolID]; ok {
			targets[i].ExpectedSignatureSHA256 = s.SignatureSHA256
		}
	}
}

func validateTask(owner string, t contracts.TechnicalTask, c DesignContext, p *[]string) {
	if !ValidID(t.ID) {
		*p = append(*p, fmt.Sprintf("%s: task id %q is invalid", owner, t.ID))
	}
	if strings.TrimSpace(t.Title) == "" {
		*p = append(*p, fmt.Sprintf("%s: task %s has no title", owner, t.ID))
	}
	if len(t.Steps) == 0 {
		*p = append(*p, fmt.Sprintf("%s: task %s has no steps", owner, t.ID))
	}
	if len(t.AcceptanceCriteria) == 0 {
		*p = append(*p, fmt.Sprintf("%s: task %s has no acceptance criteria", owner, t.ID))
	}
	if len(t.Targets) == 0 {
		*p = append(*p, fmt.Sprintf("%s: task %s has no targets", owner, t.ID))
	}
	validateTargets(owner+"/"+t.ID, t.Targets, c, p)
	for _, v := range t.Verification {
		if err := ValidateVerification(v); err != nil {
			*p = append(*p, fmt.Sprintf("%s: task %s: %v", owner, t.ID, err))
		}
	}
	if err := ValidateEstimates(t); err != nil {
		*p = append(*p, fmt.Sprintf("%s: %v", owner, err))
	}
}

// ValidateVerification checks that a verification command is bounded and
// shell-free: an argv vector, a local cwd and a finite timeout.
func ValidateVerification(v contracts.VerificationCommand) error {
	if !ValidID(v.ID) {
		return fmt.Errorf("verification id %q is invalid", v.ID)
	}
	if len(v.Argv) == 0 || strings.TrimSpace(v.Argv[0]) == "" {
		return fmt.Errorf("verification %s has no argv", v.ID)
	}
	for _, a := range v.Argv {
		if strings.ContainsRune(a, 0) {
			return fmt.Errorf("verification %s argv contains NUL", v.ID)
		}
	}
	switch strings.ToLower(filepath.Base(v.Argv[0])) {
	case "sh", "bash", "zsh", "fish", "dash", "ksh", "cmd", "cmd.exe", "powershell", "powershell.exe", "pwsh":
		return fmt.Errorf("verification %s must not invoke a shell", v.ID)
	}
	if v.Cwd != "" && !LocalRel(v.Cwd) {
		return fmt.Errorf("verification %s cwd %q must be repository-relative", v.ID, v.Cwd)
	}
	if v.TimeoutSeconds <= 0 || v.TimeoutSeconds > 600 {
		return fmt.Errorf("verification %s timeout_seconds must be 1-600", v.ID)
	}
	return nil
}

// ArchitectContext extends DesignContext with the workstreams a specialist
// validates against.
type ArchitectContext struct {
	DesignContext
}

// Validate checks the architect draft: DAG, boundaries, contracts, diagrams,
// documentation tasks and requirement coverage inputs.
func (a ArchitectDoc) Validate(c DesignContext) error {
	p := designProblems(a, c)
	p = append(p, validateDiagrams(a.Diagrams)...)
	p = append(p, docTaskProblems(a, c, c.DocDrift)...)
	return problemsToError("architect", p)
}

// ValidateDesign checks only what the design part of the architect stage
// produces: the summary, the workstream DAG and the interface contracts. The
// stage runs in parts, and a part is validated against its own subject before
// the assembled draft is validated as a whole.
func (a ArchitectDoc) ValidateDesign(c DesignContext) error {
	return problemsToError("architect", designProblems(a, c))
}

// ValidateDiagrams checks only the diagram part of the architect stage.
func (a ArchitectDoc) ValidateDiagrams() error {
	return problemsToError("architect", validateDiagrams(a.Diagrams))
}

// ValidateDocTasks checks the documentation tasks of a draft, and that every
// confirmed item of drift is covered by one. drift is the subset of
// documentation drift the tasks are answering: a part that was given a batch
// of items covers that batch, and the assembled draft covers them all.
func (a ArchitectDoc) ValidateDocTasks(c DesignContext, drift []contracts.DocDriftItem) error {
	return problemsToError("architect", docTaskProblems(a, c, drift))
}

// MaxWorkstreams caps the number of workstreams in one plan so all specialists
// can run inside one parallel wave (MaxParallel <= 4).
const MaxWorkstreams = 4

func designProblems(a ArchitectDoc, c DesignContext) []string {
	var p []string
	if strings.TrimSpace(a.Summary) == "" {
		p = append(p, "summary is required")
	}
	if len(a.Workstreams) == 0 {
		p = append(p, "at least one workstream is required")
	}
	if len(a.Workstreams) > MaxWorkstreams {
		p = append(p, fmt.Sprintf("at most %d workstreams are allowed (got %d); group related modules into cohesive workstreams", MaxWorkstreams, len(a.Workstreams)))
	}
	ws := map[string]contracts.Workstream{}
	deps := map[string][]string{}
	for _, w := range a.Workstreams {
		switch {
		case !ValidID(w.ID):
			p = append(p, fmt.Sprintf("workstream id %q is invalid", w.ID))
		case ws[w.ID].ID != "":
			p = append(p, fmt.Sprintf("duplicate workstream id %q", w.ID))
		}
		switch w.Role {
		case contracts.RoleBackend, contracts.RoleData, contracts.RoleFrontend:
		default:
			p = append(p, fmt.Sprintf("workstream %s has unknown role %q", w.ID, w.Role))
		}
		for _, mp := range w.ModulePaths {
			if !LocalRel(mp) {
				p = append(p, fmt.Sprintf("workstream %s module path %q must be repository-relative", w.ID, mp))
			}
		}
		ws[w.ID] = w
		deps[w.ID] = w.DependsOn
	}
	for _, w := range a.Workstreams {
		for _, d := range w.DependsOn {
			if _, ok := ws[d]; !ok {
				p = append(p, fmt.Sprintf("workstream %s depends on unknown workstream %q", w.ID, d))
			}
		}
	}
	if m := topoCycle(deps); m != "" {
		p = append(p, fmt.Sprintf("workstream dependency cycle through %q", m))
	}
	ctr := map[string]bool{}
	for _, k := range a.Contracts {
		switch {
		case !ValidID(k.ID):
			p = append(p, fmt.Sprintf("contract id %q is invalid", k.ID))
		case ctr[k.ID]:
			p = append(p, fmt.Sprintf("duplicate contract id %q", k.ID))
		}
		ctr[k.ID] = true
		if _, ok := ws[k.OwnerWorkstream]; !ok {
			p = append(p, fmt.Sprintf("contract %s has unknown owner workstream %q", k.ID, k.OwnerWorkstream))
		}
		if strings.TrimSpace(k.Signature) == "" {
			p = append(p, fmt.Sprintf("contract %s has no signature", k.ID))
		}
		for _, cw := range k.ConsumerWorkstreams {
			if _, ok := ws[cw]; !ok {
				p = append(p, fmt.Sprintf("contract %s names unknown consumer %q", k.ID, cw))
			}
		}
	}
	return p
}

// docTaskProblems checks the documentation tasks and their coverage of drift.
func docTaskProblems(a ArchitectDoc, c DesignContext, drift []contracts.DocDriftItem) []string {
	var p []string
	taskIDs := map[string]bool{}
	taskDeps := map[string][]string{}
	for _, t := range a.DocTasks {
		validateTask("architect", t, c, &p)
		if taskIDs[t.ID] {
			p = append(p, fmt.Sprintf("duplicate task id %q", t.ID))
		}
		taskIDs[t.ID] = true
		taskDeps[t.ID] = t.DependsOn
		for _, tg := range t.Targets {
			if tg.Operation != contracts.OpDocument {
				p = append(p, fmt.Sprintf("documentation task %s must use operation document, got %q on %q", t.ID, tg.Operation, tg.Path))
			}
		}
	}
	if m := topoCycle(taskDeps); m != "" {
		p = append(p, fmt.Sprintf("documentation task dependency cycle through %q", m))
	}
	covered := map[string]bool{}
	for _, t := range a.DocTasks {
		for _, id := range t.DocDriftIDs {
			covered[id] = true
		}
	}
	for _, item := range drift {
		if item.DriftType == contracts.DriftUnverifiable {
			continue
		}
		if !covered[item.ID] {
			p = append(p, fmt.Sprintf("stale documentation %s (%s line %d) has no documentation task", item.ID, item.DocFilePath, item.DocLine))
		}
	}
	return p
}

func validateDiagrams(d DiagramSet) []string {
	var p []string
	nodes := map[string]bool{}
	if len(d.Architecture.Nodes) == 0 {
		p = append(p, "architecture diagram needs at least one node")
	}
	for _, n := range d.Architecture.Nodes {
		if !ValidID(n.ID) || strings.TrimSpace(n.Label) == "" {
			p = append(p, fmt.Sprintf("architecture node %q needs a valid id and a label", n.ID))
		}
		if nodes[n.ID] {
			p = append(p, fmt.Sprintf("duplicate architecture node %q", n.ID))
		}
		nodes[n.ID] = true
	}
	for _, e := range d.Architecture.Edges {
		if !nodes[e.From] || !nodes[e.To] {
			p = append(p, fmt.Sprintf("architecture edge %s->%s references an unknown node", e.From, e.To))
		}
	}
	parts := map[string]bool{}
	if len(d.Sequence.Participants) == 0 {
		p = append(p, "sequence diagram needs at least one participant")
	}
	for _, n := range d.Sequence.Participants {
		if !ValidID(n.ID) || strings.TrimSpace(n.Label) == "" {
			p = append(p, fmt.Sprintf("sequence participant %q needs a valid id and a label", n.ID))
		}
		if parts[n.ID] {
			p = append(p, fmt.Sprintf("duplicate sequence participant %q", n.ID))
		}
		parts[n.ID] = true
	}
	for _, m := range d.Sequence.Messages {
		if !parts[m.From] || !parts[m.To] {
			p = append(p, fmt.Sprintf("sequence message %s->%s references an unknown participant", m.From, m.To))
		}
		if m.Kind != MessageCall && m.Kind != MessageReturn {
			p = append(p, fmt.Sprintf("sequence message %s->%s has kind %q, want call or return", m.From, m.To, m.Kind))
		}
	}
	return p
}

// Validate checks one specialist draft against the architect's workstreams.
func (s SpecialistDoc) Validate(a ArchitectDoc, c DesignContext) error {
	var p []string
	ws := ""
	for _, w := range a.Workstreams {
		if w.ID == s.WorkstreamID {
			ws = w.ID
		}
	}
	if ws == "" {
		return problemsToError("specialist", []string{fmt.Sprintf("workstream %q is not declared by the architect", s.WorkstreamID)})
	}
	if strings.TrimSpace(s.Summary) == "" {
		p = append(p, "summary is required")
	}
	if len(s.Tasks) == 0 {
		p = append(p, "at least one task is required")
	}
	ids := map[string]bool{}
	deps := map[string][]string{}
	for _, t := range s.Tasks {
		validateTask(ws, t, c, &p)
		if t.WorkstreamID != ws {
			p = append(p, fmt.Sprintf("task %s belongs to workstream %q, not %q", t.ID, t.WorkstreamID, ws))
		}
		if ids[t.ID] {
			p = append(p, fmt.Sprintf("duplicate task id %q", t.ID))
		}
		ids[t.ID] = true
		deps[t.ID] = t.DependsOn
		storyKnown := false
		for _, st := range c.PM.Stories {
			if st.ID == t.StoryID {
				storyKnown = true
				if st.EpicID != t.EpicID {
					p = append(p, fmt.Sprintf("task %s epic %q does not match story %s epic %q", t.ID, t.EpicID, st.ID, st.EpicID))
				}
			}
		}
		if !storyKnown {
			p = append(p, fmt.Sprintf("task %s traces to unknown story %q", t.ID, t.StoryID))
		}
		for _, tg := range t.Targets {
			if tg.Operation == contracts.OpDocument {
				p = append(p, fmt.Sprintf("task %s: documentation targets belong to the architect", t.ID))
			}
		}
	}
	if m := topoCycle(deps); m != "" {
		p = append(p, fmt.Sprintf("task dependency cycle through %q", m))
	}
	contractIDs := map[string]bool{}
	for _, k := range a.Contracts {
		contractIDs[k.ID] = true
	}
	for _, cl := range append(append([]InterfaceClaim(nil), s.Implements...), s.Consumes...) {
		if !contractIDs[cl.ContractID] {
			p = append(p, fmt.Sprintf("claim names unknown contract %q", cl.ContractID))
		}
	}
	return problemsToError("specialist", p)
}

// ---------------------------------------------------- interface comparison

// Interface issue kinds.
const (
	// IssueBroken means a draft's claim does not match the architect's frozen
	// contract identity. The draft is invalid and must be repaired.
	IssueBroken = "broken"
	// IssueConflict means producer and consumer both name the correct contract
	// version but declare different shapes: two valid alternatives that need a
	// human decision.
	IssueConflict = "conflict"
)

// InterfaceIssue is one deterministic interface finding.
type InterfaceIssue struct {
	Kind       string
	ContractID string
	// Workstreams are the drafts involved, in sorted order.
	Workstreams []string
	Detail      string
}

func sameStrings(a, b []string) bool {
	x, y := sortedCopy(a), sortedCopy(b)
	if len(x) != len(y) {
		return false
	}
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

func normSchemaText(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// alignSchemaField returns archSchema when the specialist claim is blank, copies
// the contract signature into the schema field, or restates archSchema (for
// example by prepending the endpoint signature). Genuine alternative schemas
// that do not contain archSchema are returned unchanged so CompareInterfaces
// still opens a human decision card.
func alignSchemaField(claimSchema, archSchema, sig string) string {
	want := normSchemaText(archSchema)
	if want == "" {
		return claimSchema
	}
	got := normSchemaText(claimSchema)
	if got == "" || strings.Contains(got, want) {
		return archSchema
	}
	if sigNorm := normSchemaText(sig); sigNorm != "" && got == sigNorm {
		return archSchema
	}
	return claimSchema
}

func alignClaim(cl InterfaceClaim, k contracts.InterfaceContract) InterfaceClaim {
	if cl.VersionHash != ContractHash(k) {
		return cl
	}
	cl.InputSchema = alignSchemaField(cl.InputSchema, k.InputSchema, k.Signature)
	cl.OutputSchema = alignSchemaField(cl.OutputSchema, k.OutputSchema, k.Signature)
	return cl
}

// AlignSpecialistClaims normalizes specialist interface claims that quote the
// architect's exact contract version_hash and restate or omit the architect's
// schema text, preventing false-positive interface conflicts while preserving
// genuine schema alternatives.
func AlignSpecialistClaims(s *SpecialistDoc, a ArchitectDoc) {
	if s == nil {
		return
	}
	byID := make(map[string]contracts.InterfaceContract, len(a.Contracts))
	for _, k := range a.Contracts {
		byID[k.ID] = k
	}
	for i, cl := range s.Implements {
		if k, ok := byID[cl.ContractID]; ok {
			s.Implements[i] = alignClaim(cl, k)
		}
	}
	for i, cl := range s.Consumes {
		if k, ok := byID[cl.ContractID]; ok {
			s.Consumes[i] = alignClaim(cl, k)
		}
	}
}

// CompareInterfaces compares every contract's producer and consumer claims with
// the architect's frozen contract by ID, version hash, input and output schema
// and declared error cases. It is pure and order-independent: issues come back
// sorted by contract ID then workstream.
func CompareInterfaces(a ArchitectDoc, specs []SpecialistDoc) []InterfaceIssue {
	byWS := map[string]SpecialistDoc{}
	for _, s := range specs {
		byWS[s.WorkstreamID] = s
	}
	var out []InterfaceIssue
	claimOf := func(s SpecialistDoc, produce bool, id string) (InterfaceClaim, bool) {
		list := s.Consumes
		if produce {
			list = s.Implements
		}
		for _, cl := range list {
			if cl.ContractID == id {
				return cl, true
			}
		}
		return InterfaceClaim{}, false
	}
	for _, k := range a.Contracts {
		want := ContractHash(k)
		type seen struct {
			ws    string
			claim InterfaceClaim
		}
		var good []seen
		check := func(ws string, produce bool) {
			s, ok := byWS[ws]
			if !ok {
				return // a workstream that produced no draft is reported by coverage checks
			}
			role := "consume"
			if produce {
				role = "implement"
			}
			cl, ok := claimOf(s, produce, k.ID)
			if !ok {
				out = append(out, InterfaceIssue{
					IssueBroken, k.ID,
					[]string{ws},
					fmt.Sprintf("workstream %s does not declare that it %ss contract %s", ws, role, k.ID),
				})
				return
			}
			if cl.VersionHash != want {
				out = append(out, InterfaceIssue{
					IssueBroken, k.ID,
					[]string{ws},
					fmt.Sprintf("workstream %s %ss contract %s at version %q, want %q", ws, role, k.ID, cl.VersionHash, want),
				})
				return
			}
			good = append(good, seen{ws, alignClaim(cl, k)})
		}
		check(k.OwnerWorkstream, true)
		consumers := sortedCopy(k.ConsumerWorkstreams)
		for _, ws := range consumers {
			check(ws, false)
		}
		for i := range good {
			for j := i + 1; j < len(good); j++ {
				x, y := good[i].claim, good[j].claim
				if x.InputSchema != y.InputSchema || x.OutputSchema != y.OutputSchema || !sameStrings(x.Errors, y.Errors) {
					ws := sortedCopy([]string{good[i].ws, good[j].ws})
					out = append(out, InterfaceIssue{
						IssueConflict, k.ID, ws,
						fmt.Sprintf("workstreams %s and %s declare different schemas or error cases for contract %s", ws[0], ws[1], k.ID),
					})
				}
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].ContractID != out[j].ContractID {
			return out[i].ContractID < out[j].ContractID
		}
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return strings.Join(out[i].Workstreams, ",") < strings.Join(out[j].Workstreams, ",")
	})
	return out
}

// ------------------------------------------------------ cross-document checks

// PlanProblem is one cross-document defect. Targets name the drafts that must
// change: a workstream ID, "architect", or a story ID (which concerns the whole
// design). Message is deterministic so the same defect yields the same ID.
type PlanProblem struct {
	Message string
	Targets []string
}

// NeedsSpecialist reports whether a workstream is non-empty. Empty workstreams
// are omitted from the specialist stage, not faked.
func NeedsSpecialist(a ArchitectDoc, w contracts.Workstream) bool {
	if len(w.ModulePaths) > 0 || len(w.ContractIDs) > 0 {
		return true
	}
	for _, k := range a.Contracts {
		if k.OwnerWorkstream == w.ID {
			return true
		}
		for _, c := range k.ConsumerWorkstreams {
			if c == w.ID {
				return true
			}
		}
	}
	return false
}

// SpecialistWorkstreams returns the workstream IDs that need a specialist, in
// sorted order.
func SpecialistWorkstreams(a ArchitectDoc) []string {
	var ids []string
	for _, w := range a.Workstreams {
		if NeedsSpecialist(a, w) {
			ids = append(ids, w.ID)
		}
	}
	sort.Strings(ids)
	return ids
}

// PlanProblems validates the combined design: global task and verification ID
// uniqueness, the task dependency DAG, shared-target ownership, story coverage
// and the presence of every needed specialist draft. It is the traceability,
// dependency and target validation that precedes compilation.
func PlanProblems(pm PMDoc, a ArchitectDoc, specs []SpecialistDoc) []PlanProblem {
	var out []PlanProblem
	add := func(targets []string, format string, args ...any) {
		out = append(out, PlanProblem{Message: fmt.Sprintf(format, args...), Targets: sortedCopy(targets)})
	}
	tasks := AllTasks(a, specs)
	ids := map[string]string{}
	deps := map[string][]string{}
	owner := map[string]string{}
	verifyIDs := map[string]string{}
	for _, t := range tasks {
		if prev, dup := ids[t.ID]; dup {
			add([]string{prev, t.WorkstreamID}, "task id %q is used by %s and %s", t.ID, prev, t.WorkstreamID)
		}
		ids[t.ID] = t.WorkstreamID
		deps[t.ID] = t.DependsOn
		for _, v := range t.Verification {
			if prev, dup := verifyIDs[v.ID]; dup {
				add([]string{prev, t.WorkstreamID}, "verification id %q is used by tasks of %s and %s", v.ID, prev, t.WorkstreamID)
			}
			verifyIDs[v.ID] = t.WorkstreamID
		}
	}
	for _, t := range tasks {
		for _, d := range t.DependsOn {
			if _, ok := ids[d]; !ok {
				add([]string{t.WorkstreamID}, "task %s depends on unknown task %q", t.ID, d)
			}
		}
		for _, tg := range t.Targets {
			if tg.Operation == contracts.OpDocument {
				continue
			}
			if prev, ok := owner[tg.Path]; ok && prev != t.WorkstreamID {
				add([]string{prev, t.WorkstreamID}, "target %q is changed by workstreams %s and %s", tg.Path, prev, t.WorkstreamID)
			}
			owner[tg.Path] = t.WorkstreamID
		}
	}
	if m := topoCycle(deps); m != "" {
		add([]string{ids[m]}, "task dependency cycle through %q", m)
	}
	storyTasks := map[string]bool{}
	for _, t := range tasks {
		if t.StoryID != "" {
			storyTasks[t.StoryID] = true
		}
	}
	for _, s := range pm.Stories {
		if !storyTasks[s.ID] {
			add([]string{s.ID}, "story %s has no task: its acceptance criteria are uncovered", s.ID)
		}
	}
	have := map[string]bool{}
	for _, s := range specs {
		have[s.WorkstreamID] = true
	}
	for _, id := range SpecialistWorkstreams(a) {
		if !have[id] {
			add([]string{id}, "workstream %s has no specialist draft", id)
		}
	}
	return out
}

// CheckPlan returns the PlanProblems as one ValidationError.
func CheckPlan(pm PMDoc, a ArchitectDoc, specs []SpecialistDoc) error {
	var msgs []string
	for _, p := range PlanProblems(pm, a, specs) {
		msgs = append(msgs, p.Message)
	}
	return problemsToError("plan", msgs)
}

// AllTasks returns every task of the plan (architect documentation tasks and
// specialist tasks) sorted by task ID.
func AllTasks(a ArchitectDoc, specs []SpecialistDoc) []contracts.TechnicalTask {
	var out []contracts.TechnicalTask
	out = append(out, a.DocTasks...)
	for _, s := range specs {
		out = append(out, s.Tasks...)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
