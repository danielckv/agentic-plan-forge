package contracts

import (
	"encoding/json"
	"time"
)

// SchemaVersion is the version stamped into persisted documents.
const SchemaVersion = 1

// DocumentIDPRD is the single collaborative document in protocol version 1.
// Unknown document IDs are rejected by the OT authority.
const DocumentIDPRD = "prd"

// SourceRef points a parsed block back at the file it came from.
type SourceRef struct {
	SourceID string `json:"source_id"`
	BlockID  string `json:"block_id,omitempty"`
	// Page is omitted for formats without pages. It is never guessed.
	Page    int    `json:"page,omitempty"`
	Heading string `json:"heading,omitempty"`
	SHA256  string `json:"sha256,omitempty"`
}

// DocumentBlock is one parsed unit of an input document.
type DocumentBlock struct {
	ID    string     `json:"id"`
	Kind  string     `json:"kind"`
	Text  string     `json:"text,omitempty"`
	Level int        `json:"level,omitempty"`
	Rows  [][]string `json:"rows,omitempty"`
	// Source locates the block in the original document.
	Source SourceRef `json:"source"`
	// Confidence is an extraction heuristic, never a claim about meaning.
	Confidence string   `json:"confidence"`
	Warnings   []string `json:"warnings,omitempty"`
}

// Block kinds.
const (
	BlockHeading   = "heading"
	BlockParagraph = "paragraph"
	BlockList      = "list"
	BlockTable     = "table"
)

// Extraction confidence tiers. They describe how reliably text was recovered,
// and drive how far documentation-drift findings are trusted.
const (
	ConfidenceHigh         = "HIGH"
	ConfidenceMedium       = "MEDIUM"
	ConfidenceLow          = "LOW"
	ConfidenceUnverifiable = "UNVERIFIABLE"
)

// ProductPRD is the product-only view of the requirements. It is the only
// requirement material the Product Manager avatar receives.
type ProductPRD struct {
	Title              string   `json:"title,omitempty"`
	Problem            string   `json:"problem,omitempty"`
	Goals              []string `json:"goals"`
	Actors             []string `json:"actors"`
	Flows              []string `json:"flows"`
	Requirements       []string `json:"requirements"`
	AcceptanceCriteria []string `json:"acceptance_criteria"`
	Constraints        []string `json:"constraints"`
	OpenQuestions      []string `json:"open_questions"`
}

// Ambiguity is a classification the normalizer could not settle alone.
type Ambiguity struct {
	ID       string `json:"id"`
	Text     string `json:"text"`
	Question string `json:"question"`
	Guess    string `json:"guess,omitempty"`
}

// AmbiguityResolution is an owner decision about one ambiguity.
type AmbiguityResolution struct {
	AmbiguityID string `json:"ambiguity_id"`
	Choice      string `json:"choice"`
	Note        string `json:"note,omitempty"`
	ResolvedBy  string `json:"resolved_by,omitempty"`
}

// Ambiguity resolution choices.
const (
	AmbiguityProduct   = "product"
	AmbiguityTechnical = "technical"
	AmbiguityDismiss   = "dismiss"
)

// NormalizedPRD is the interpreted requirements document: the product view, the
// technical appendix, provenance and the open ambiguities.
type NormalizedPRD struct {
	SchemaVersion     int         `json:"schema_version"`
	Revision          uint64      `json:"revision"`
	Product           ProductPRD  `json:"product"`
	TechnicalAppendix string      `json:"technical_appendix,omitempty"`
	SourceRefs        []SourceRef `json:"source_refs"`
	Ambiguities       []Ambiguity `json:"ambiguities"`
	Warnings          []string    `json:"warnings,omitempty"`
}

// Symbol is one extracted declaration. Paths are repository-relative.
type Symbol struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	QualifiedName   string   `json:"qualified_name,omitempty"`
	Kind            string   `json:"kind"`
	Language        string   `json:"language"`
	Path            string   `json:"path"`
	StartLine       int      `json:"start_line"`
	EndLine         int      `json:"end_line"`
	Signature       string   `json:"signature,omitempty"`
	SignatureSHA256 string   `json:"signature_sha256,omitempty"`
	Visibility      string   `json:"visibility,omitempty"`
	Imports         []string `json:"imports,omitempty"`
	Evidence        string   `json:"evidence,omitempty"`
}

// Symbol kinds.
const (
	SymbolFunction  = "function"
	SymbolMethod    = "method"
	SymbolStruct    = "struct"
	SymbolInterface = "interface"
	SymbolClass     = "class"
	SymbolType      = "type"
	SymbolEnum      = "enum"
	SymbolTrait     = "trait"
	SymbolConstant  = "constant"
	SymbolVariable  = "variable"
	SymbolRoute     = "route"
)

// ModuleBoundary groups symbols that share a package or directory.
type ModuleBoundary struct {
	ID        string   `json:"id"`
	Path      string   `json:"path"`
	Language  string   `json:"language"`
	SymbolIDs []string `json:"symbol_ids,omitempty"`
}

// RouteEvidence records a detected route registration. It is evidence of a
// registration call, not proof of runtime behaviour.
type RouteEvidence struct {
	ID       string `json:"id"`
	Method   string `json:"method,omitempty"`
	Path     string `json:"path"`
	FilePath string `json:"file_path"`
	Line     int    `json:"line"`
	Evidence string `json:"evidence"`
}

// Coverage records what the scan did and did not see. Gaps are reported, never
// silently treated as absence.
type Coverage struct {
	FilesScanned     int      `json:"files_scanned"`
	FilesSkipped     int      `json:"files_skipped"`
	UnsupportedFiles int      `json:"unsupported_files"`
	ParseGaps        []string `json:"parse_gaps,omitempty"`
	TimedOut         bool     `json:"timed_out"`
	Notes            []string `json:"notes,omitempty"`
}

// RepoContextGraph is the deterministic codebase snapshot handed to the
// technical avatars. RootPath stays local: transmitted graphs use relative
// paths and hashes.
type RepoContextGraph struct {
	SchemaVersion   int                 `json:"schema_version"`
	RootPath        string              `json:"root_path"`
	GitCommitHash   string              `json:"git_commit_hash,omitempty"`
	GitBranch       string              `json:"git_branch,omitempty"`
	WorktreeHash    string              `json:"worktree_hash"`
	DetectedStack   []string            `json:"detected_stack"`
	Modules         []ModuleBoundary    `json:"modules"`
	Symbols         map[string][]Symbol `json:"symbols"`
	Routes          []RouteEvidence     `json:"routes,omitempty"`
	DocDriftReports []DocDriftItem      `json:"doc_drift_reports"`
	Coverage        Coverage            `json:"coverage"`
}

// Project context sources, in the order the gatherer tries them.
const (
	// ContextSourceRecall is the project-memory recall index under the Git
	// common directory (agents-local/recall.db).
	ContextSourceRecall = "recall-index"
	// ContextSourceLegacyRecall is the older recall index at
	// .agents/index/recall.db.
	ContextSourceLegacyRecall = "legacy-recall-index"
	// ContextSourceFiles is a direct read of the notes, specs, plans and ADRs.
	ContextSourceFiles = "files"
	// ContextSourceNone means no source held anything to read.
	ContextSourceNone = "none"
)

// ProjectContext is what the repository already says about itself before a
// plan starts: excerpts from its notes, specs, plans and decision records,
// plus where the branch stands. The technical avatars receive it; the Product
// Manager never does. Every path is repository-relative.
type ProjectContext struct {
	Source string       `json:"source"`
	Hits   []ContextHit `json:"hits"`
	// Branch and Head locate the working tree; Recent holds the newest commit
	// subjects, newest first.
	Branch string   `json:"branch,omitempty"`
	Head   string   `json:"head,omitempty"`
	Recent []string `json:"recent_commits,omitempty"`
	// Truncated reports that the byte budget dropped hits or commits.
	Truncated bool `json:"truncated,omitempty"`
	// Notes explain a source that was present but could not be read.
	Notes []string `json:"notes,omitempty"`
}

// ContextHit is one excerpt, ranked by how well it matches the requirements.
type ContextHit struct {
	Path    string  `json:"path"`
	Heading string  `json:"heading,omitempty"`
	Text    string  `json:"text"`
	Score   float64 `json:"score"`
}

// DocDriftItem is one documentation claim that no longer matches the code.
type DocDriftItem struct {
	ID              string `json:"id"`
	DocFilePath     string `json:"doc_file_path"`
	DocLine         int    `json:"doc_line"`
	CodeFilePath    string `json:"code_file_path,omitempty"`
	SymbolName      string `json:"symbol_name,omitempty"`
	DriftType       string `json:"drift_type"`
	Expected        string `json:"expected,omitempty"`
	Observed        string `json:"observed,omitempty"`
	EvidenceKind    string `json:"evidence_kind"`
	Confidence      string `json:"confidence"`
	RemediationTask string `json:"remediation_task"`
}

// Documentation drift types.
const (
	DriftStaleSignature = "STALE_SIGNATURE"
	DriftMissingDoc     = "MISSING_DOC"
	DriftDeprecatedFlow = "DEPRECATED_FLOW"
	DriftUnverifiable   = "UNVERIFIABLE"
)

// Epic groups stories for the synthesised plan.
type Epic struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Goal     string   `json:"goal,omitempty"`
	StoryIDs []string `json:"story_ids,omitempty"`
}

// Story is one user story with traceable acceptance criteria.
type Story struct {
	ID                 string   `json:"id"`
	EpicID             string   `json:"epic_id"`
	Actor              string   `json:"actor"`
	Need               string   `json:"need"`
	Benefit            string   `json:"benefit,omitempty"`
	AcceptanceCriteria []string `json:"acceptance_criteria"`
	RequirementIDs     []string `json:"requirement_ids,omitempty"`
}

// InterfaceContract is a frozen boundary between workstreams.
type InterfaceContract struct {
	ID                  string   `json:"id"`
	OwnerWorkstream     string   `json:"owner_workstream"`
	Language            string   `json:"language,omitempty"`
	Signature           string   `json:"signature"`
	InputSchema         string   `json:"input_schema,omitempty"`
	OutputSchema        string   `json:"output_schema,omitempty"`
	Errors              []string `json:"errors,omitempty"`
	ConsumerWorkstreams []string `json:"consumer_workstreams,omitempty"`
	VersionHash         string   `json:"version_hash"`
}

// Workstream is a subsystem owned by one specialist avatar.
type Workstream struct {
	ID          string   `json:"id"`
	Role        string   `json:"role"`
	ModulePaths []string `json:"module_paths"`
	DependsOn   []string `json:"depends_on,omitempty"`
	ContractIDs []string `json:"contract_ids,omitempty"`
}

// Workstream roles.
const (
	RoleBackend  = "backend"
	RoleData     = "data"
	RoleFrontend = "frontend"
)

// Estimate is a three-point estimate with an explicit basis.
type Estimate struct {
	Optimistic  float64 `json:"optimistic"`
	Likely      float64 `json:"likely"`
	Pessimistic float64 `json:"pessimistic"`
	Basis       string  `json:"basis"`
}

// Target is one file or symbol a task is expected to touch.
type Target struct {
	Path                    string `json:"path"`
	Operation               string `json:"operation"`
	SymbolID                string `json:"symbol_id,omitempty"`
	ExpectedSignatureSHA256 string `json:"expected_signature_sha256,omitempty"`
}

// Target operations.
const (
	OpAdd      = "add"
	OpModify   = "modify"
	OpRemove   = "remove"
	OpDocument = "document"
)

// VerificationCommand is a bounded, shell-free check.
type VerificationCommand struct {
	ID               string   `json:"id"`
	Argv             []string `json:"argv"`
	Cwd              string   `json:"cwd,omitempty"`
	TimeoutSeconds   int      `json:"timeout_seconds"`
	ExpectedExitCode int      `json:"expected_exit_code"`
}

// TechnicalTask is one atomic unit of work in the plan.
type TechnicalTask struct {
	ID                 string                `json:"id"`
	EpicID             string                `json:"epic_id"`
	StoryID            string                `json:"story_id,omitempty"`
	WorkstreamID       string                `json:"workstream_id"`
	Title              string                `json:"title"`
	Steps              []string              `json:"steps"`
	DependsOn          []string              `json:"depends_on,omitempty"`
	Targets            []Target              `json:"targets"`
	AcceptanceCriteria []string              `json:"acceptance_criteria"`
	Verification       []VerificationCommand `json:"verification"`
	DocDriftIDs        []string              `json:"doc_drift_ids,omitempty"`
	HumanEstimate      Estimate              `json:"human_estimate"`
	AgentEstimate      Estimate              `json:"agent_estimate"`
}

// Critique is a severity-ranked objection from the red-team avatar.
type Critique struct {
	ID             string   `json:"id"`
	Severity       string   `json:"severity"`
	TargetIDs      []string `json:"target_ids,omitempty"`
	Evidence       string   `json:"evidence"`
	Impact         string   `json:"impact,omitempty"`
	RequiredChange string   `json:"required_change"`
	Status         string   `json:"status"`
	ResolutionRef  string   `json:"resolution_ref,omitempty"`
}

// Critique severities and statuses.
const (
	SeverityCritical = "CRITICAL"
	SeverityHigh     = "HIGH"
	SeverityMedium   = "MEDIUM"

	CritiqueOpen     = "open"
	CritiqueResolved = "resolved"
)

// DecisionOption is one choice on a human decision card.
type DecisionOption struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Tradeoff string `json:"tradeoff"`
	Impact   string `json:"impact,omitempty"`
}

// Decision is a trade-off that requires human judgement.
type Decision struct {
	ID                  string           `json:"id"`
	InputRevision       uint64           `json:"input_revision"`
	Question            string           `json:"question"`
	Options             []DecisionOption `json:"options"`
	RecommendedOptionID string           `json:"recommended_option_id,omitempty"`
	ResolvedOptionID    string           `json:"resolved_option_id,omitempty"`
	ResolvedBy          string           `json:"resolved_by,omitempty"`
	ResolvedAt          *time.Time       `json:"resolved_at,omitempty"`
}

// ApprovalMeta is the only compiler input that varies per approval, which keeps
// the content hash stable before an approval ID exists.
type ApprovalMeta struct {
	ApprovalID string    `json:"approval_id"`
	CreatedAt  time.Time `json:"created_at"`
}

// SynthesisInput is the validated material the synthesizer compiles.
type SynthesisInput struct {
	PRD          NormalizedPRD     `json:"prd"`
	PM           json.RawMessage   `json:"pm"`
	Architecture json.RawMessage   `json:"architecture"`
	Specialists  []json.RawMessage `json:"specialists,omitempty"`
	Critiques    []Critique        `json:"critiques,omitempty"`
	Decisions    []Decision        `json:"decisions,omitempty"`
	Graph        RepoContextGraph  `json:"graph"`
	DocDrift     []DocDriftItem    `json:"doc_drift,omitempty"`
}

// PlanManifest is the machine-readable half of an approved plan. It never
// contains absolute machine paths, credentials or raw model transcripts.
type PlanManifest struct {
	SchemaVersion        int                   `json:"schema_version"`
	ApprovalID           string                `json:"approval_id"`
	Title                string                `json:"title"`
	CreatedAt            time.Time             `json:"created_at"`
	GitBase              string                `json:"git_base,omitempty"`
	WorktreeHash         string                `json:"worktree_hash"`
	PRDSHA256            string                `json:"prd_sha256"`
	GraphSHA256          string                `json:"graph_sha256"`
	SpecSHA256           string                `json:"spec_sha256"`
	TaskDAG              []TechnicalTask       `json:"task_dag"`
	Targets              []Target              `json:"targets"`
	VerificationCommands []VerificationCommand `json:"verification_commands"`
	Decisions            []Decision            `json:"decisions,omitempty"`
	Coverage             Coverage              `json:"coverage"`
	EstimateAssumptions  []string              `json:"estimate_assumptions"`
}
