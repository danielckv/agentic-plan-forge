//go:build client && !server

package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"sync"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/ast"
	"github.com/danielckv/agentic-plan-forge/internal/avatars"
	"github.com/danielckv/agentic-plan-forge/internal/config"
	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/gather"
	"github.com/danielckv/agentic-plan-forge/internal/impact"
	"github.com/danielckv/agentic-plan-forge/internal/ingestion"
	"github.com/danielckv/agentic-plan-forge/internal/planstate"
	"github.com/danielckv/agentic-plan-forge/internal/relay"
	"github.com/danielckv/agentic-plan-forge/internal/sanitize"
	"github.com/danielckv/agentic-plan-forge/internal/storage"
	"github.com/danielckv/agentic-plan-forge/internal/synthesizer"
)

// Timings from the plan.
const (
	// normalizeDebounce is how long the executor waits after the last ingestion
	// event before re-interpreting the document.
	normalizeDebounce = 2 * time.Second
	// syncWait is how long the executor waits for followers' ARTIFACT_SYNCED
	// before stopping the loopback server.
	syncWait = 5 * time.Second
	// leaseRenewEvery is the executor lease renewal period (TTL is 15 s).
	leaseRenewEvery = relay.DefaultLeaseRenew
	// maxNoticeRunes bounds an error message shown to the window.
	maxNoticeRunes = 300

	artifactSpecRel     = "docs/plans/.plan/synthesized_spec.md"
	artifactManifestRel = "docs/plans/.plan/plan_manifest.json"
)

const (
	avatarPM          = "pm"
	avatarSynthesizer = "synthesizer"
	// busyDeliberation and busySynthesis name the pipeline work the executor
	// runs one at a time.
	busyDeliberation  = "deliberation"
	busySynthesis     = "synthesis"
	schemaSynthesisV1 = "synthesis.v1"
	avatarIngestion   = "ingestion"
)

// authority is the executor's view of the session authority. The Solo room and
// the Remote relay link both implement it, which is what makes the executor
// independent of the transport.
type authority interface {
	// Emit submits one executor event. A refusal is returned.
	Emit(t relay.WSEventType, payload any) error
	// Doc returns the shared document as the authority holds it.
	Doc(ctx context.Context) (text string, version uint64, err error)
	// Ingest appends parsed or composed Markdown to the shared document.
	Ingest(p relay.IngestAppendPayload) error
	// Local publishes a frame that exists only between this process and its
	// window. A non-empty sticky key replays it when the window reconnects.
	Local(t relay.WSEventType, payload any, sticky string)
	// Events is the authority-ordered stream.
	Events() *pump
	// HoldLease claims the executor lease and keeps it renewed until ctx ends.
	// lost is called once when the lease is lost.
	HoldLease(ctx context.Context, lost func(error)) error
}

// HostSummary describes which host and model serve each slot, shown in the
// Page 1 footer and the Page 2 model badges.
type HostSummary struct {
	Requested string `json:"requestedHost"`
	MainHost  string `json:"mainHost"`
	MainModel string `json:"mainModel"`
	JudgeHost string `json:"judgeHost"`
	// JudgeModel is empty when the critic shares the main model.
	JudgeModel       string `json:"judgeModel"`
	CriticSharesMain bool   `json:"criticSharesMain"`
	// Differs reports that the resolved main host is not the --host the
	// invoking skill passed: the owner must acknowledge it before confirming.
	Differs bool `json:"hostDiffers"`
	// Roles is where each avatar will run, resolved before the run starts so
	// the room can name the host under a face that has not spoken yet.
	Roles []RolePlacement `json:"roles,omitempty"`
	// Skipped names the hosts that could not serve and why, so an
	// installation signed in to three hosts but running on one can see which
	// one dropped out and at which probe.
	Skipped []HostSkip `json:"skipped,omitempty"`
}

// RolePlacement is one avatar role and the host and model serving it.
type RolePlacement struct {
	Role  string `json:"role"`
	Host  string `json:"host"`
	Model string `json:"model,omitempty"`
}

// HostSkip is one host that could not serve, and the probe that rejected it.
type HostSkip struct {
	Host   string `json:"host"`
	Probe  string `json:"probe"`
	Reason string `json:"reason"`
}

// StatusView is the body of GET /api/status: the Page 1 pills plus the
// generation host summary.
type StatusView struct {
	AST       string `json:"ast"`
	ASTDetail string `json:"astDetail"`
	DocHealth int    `json:"docHealth"`
	OCR       string `json:"ocr"`
	// Context and ContextDetail report the project context gathered for the
	// technical avatars: gathering, ok, none or failed.
	Context       string `json:"context"`
	ContextDetail string `json:"contextDetail"`
	// Impact and ImpactDetail report the impact map: mapping, quick, ok or
	// failed.
	Impact       string `json:"impact"`
	ImpactDetail string `json:"impactDetail"`
	// ImpactRevision moves on every published map. The window refetches the
	// impact panel and the plan files when it changes: the interpretation
	// revision does not move when the model result replaces the keyword
	// ranking, so keying on that alone would show the first paint forever.
	ImpactRevision uint64 `json:"impactRevision"`
	// Interpretation is the revision of the latest interpretation; the window
	// refetches the plan files when it changes.
	Interpretation uint64 `json:"interpretation"`
	// Repo names the repository this session plans, so the window states
	// which project it belongs to. RepoPath is the absolute path; it is shown
	// only through the loopback bridge and never leaves the machine.
	Repo     string `json:"repo"`
	RepoPath string `json:"repoPath"`
	// Branch is the checked-out branch once the scan has read it.
	Branch string `json:"branch,omitempty"`
	HostSummary
}

type approvalFailure struct {
	Code    string `json:"code"`
	RelPath string `json:"rel_path"`
	// AbsPath is shown only through the local bridge; it never leaves the machine.
	AbsPath string `json:"abs_path"`
	Message string `json:"message"`
}

// executorConfig wires an executor.
type executorConfig struct {
	Repo   string
	Config config.Config
	Gen    contracts.Generator
	Auth   authority
	Store  *storage.ArtifactStore
	Hosts  HostSummary
	Logger *slog.Logger
	// Upload publishes the committed artifacts to the relay (Remote mode).
	Upload func(ctx context.Context, approvalID string, spec, manifest []byte) error
	// WaitForSync is true in Remote mode: followers report ARTIFACT_SYNCED.
	WaitForSync bool
	// Finish reports the terminal outcome exactly once.
	Finish func(phase SessionPhase, message string, artifacts []string)
	// Approved hands the committed plan to the build controller, so the Build
	// page knows which approval it implements and what the spec says. It runs
	// after the artifacts are on disk, so a build can never start from a spec
	// that failed to commit.
	Approved func(approvalID string, spec []byte)
	// Scan overrides ast.Scan (tests).
	Scan func(ctx context.Context, opts ast.ScanOptions) (*contracts.RepoContextGraph, error)
	// Gather overrides gather.Collect (tests).
	Gather func(ctx context.Context, opts gather.Options) (contracts.ProjectContext, error)
	// Impact overrides impact.Build (tests).
	Impact func(ctx context.Context, in impact.Input) (contracts.ImpactMap, error)
}

// executor is the one LLM pipeline of a session. It reads the authority's
// ordered events and reacts: interpret the document, run the avatars, compile
// and commit the approved plan. It holds the executor lease; no other client
// ever calls a provider.
type executor struct {
	cfg executorConfig
	log *slog.Logger
	ctx context.Context

	normTrigger chan bool // true = immediate (Re-interpret)

	// checkpoints keeps the resumable drafts of the current run, in memory and
	// in docs/plans/.plan/run.json. An unfinished run is adopted by the next
	// run over the same baseline; an approved plan clears it.
	checkpoints *avatars.CheckpointStore

	mu        sync.Mutex
	phase     relay.Phase
	runID     string
	cycle     int
	runCancel context.CancelFunc
	runGen    int
	// busy names the pipeline work in flight: one deliberation or one
	// synthesis at a time. A second request for work that is already running is
	// refused, so a double click cannot cancel a call that is already paid for
	// and start it again.
	busy        string
	decisions   chan contracts.Decision
	runInput    *avatars.RunInput
	converged   *contracts.SynthesisInput
	synthesized *contracts.SynthesisInput
	synthHash   string
	resolutions map[string]contracts.AmbiguityResolution
	graph       *contracts.RepoContextGraph
	scanDone    chan struct{}
	projectCtx  *contracts.ProjectContext
	contextSeq  uint64
	// impactMap is the latest published map; impactRev moves with it so the
	// window refetches when the model result replaces the keyword ranking.
	impactMap    *contracts.ImpactMap
	impactRev    uint64
	impactSeq    uint64
	impactCancel context.CancelFunc
	lastScan     time.Time
	pills        StatusView
	norm         *contracts.NormalizedPRD
	normBase     *contracts.NormalizedPRD
	normText     string
	normKey      string
	normRev      uint64
	modelImpact  bool
	approvalID   string
	createdAt    time.Time
	failure      *approvalFailure
	members      map[string]bool
	synced       chan struct{}
	finished     bool
	leaseCancel  context.CancelFunc
}

func newExecutor(cfg executorConfig) *executor {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	e := &executor{
		cfg: cfg, log: cfg.Logger, phase: relay.PhaseIngestion,
		normTrigger: make(chan bool, 1),
		resolutions: map[string]contracts.AmbiguityResolution{},
		members:     map[string]bool{},
		synced:      make(chan struct{}, 64),
		scanDone:    make(chan struct{}),
		pills: StatusView{
			AST: "scanning", ASTDetail: "0 files parsed, 0 symbols indexed", OCR: "unknown",
			Context: contextGathering, ContextDetail: "reading project notes, specs and decisions",
			Impact: impactPending, ImpactDetail: impactPendingDetail, HostSummary: cfg.Hosts,
		},
	}
	e.checkpoints = avatars.NewCheckpointStore()
	e.checkpoints.Persist(newRunCheckpointSink(cfg.Repo), func(err error) {
		e.log.Warn("the unfinished run was not kept on disk", "err", err)
	})
	return e
}

// Start claims the lease, starts the repository scan and runs the event loop
// until ctx ends.
func (e *executor) Start(ctx context.Context) error {
	e.ctx = ctx
	lctx, lcancel := context.WithCancel(ctx)
	e.mu.Lock()
	e.leaseCancel = lcancel
	e.mu.Unlock()
	if err := e.cfg.Auth.HoldLease(lctx, e.leaseLost); err != nil {
		lcancel()
		return err
	}
	go e.scan(ctx)
	go func() {
		text, _, err := e.cfg.Auth.Doc(ctx)
		if err != nil {
			text = ""
		}
		e.scheduleInitialInterpretation(text)
		e.refreshContext(ctx, text)
	}()
	go e.normalizeLoop(ctx)
	go e.eventLoop(ctx)
	return nil
}

func (e *executor) leaseLost(err error) {
	e.log.Warn("executor lease lost", "err", err)
	e.stopRun()
	e.cfg.Auth.Local(relay.EventError, relay.ErrorPayload{
		Code: contracts.CodeFenced, Message: "this machine no longer holds the executor lease; the run was stopped",
	}, "")
}

func notice(err error) string {
	if err == nil {
		return ""
	}
	return sanitize.Truncate(sanitize.Terminal(err.Error()), maxNoticeRunes)
}

// --- repository scan -----------------------------------------------------------------------------

func (e *executor) scan(ctx context.Context) {
	defer close(e.scanDone)
	e.mu.Lock()
	e.pills.OCR = ocrStatus()
	e.mu.Unlock()
	scan := e.cfg.Scan
	if scan == nil {
		scan = ast.Scan
	}
	opts := ast.ScanOptions{
		Root: e.cfg.Repo, ReadCache: true, WriteCache: true,
		Progress: func(p ast.ScanProgress) {
			e.mu.Lock()
			e.pills.ASTDetail = fmt.Sprintf("%d of %d files parsed", p.FilesDone, p.FilesTotal)
			e.mu.Unlock()
		},
	}
	g, err := scan(ctx, opts)
	if err != nil && ctx.Err() == nil {
		// The cache lives in the repository; a read-only checkout must still scan.
		opts.WriteCache = false
		g, err = scan(ctx, opts)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err != nil || g == nil {
		e.pills.AST = "failed"
		e.pills.ASTDetail = "the repository scan failed: " + notice(err)
		return
	}
	e.graph = g
	e.lastScan = time.Now()
	symbols := 0
	for _, syms := range g.Symbols {
		symbols += len(syms)
	}
	e.pills.ASTDetail = fmt.Sprintf("%d files parsed, %d symbols indexed", g.Coverage.FilesScanned, symbols)
	switch {
	case g.Coverage.TimedOut:
		e.pills.AST = "timeout"
	case len(g.Coverage.ParseGaps) > 0:
		e.pills.AST = "partial"
	case g.GitCommitHash == "":
		e.pills.AST = "not-a-Git-repo"
	default:
		e.pills.AST = "ok"
	}
	stale := 0
	for _, d := range g.DocDriftReports {
		if d.DriftType == contracts.DriftStaleSignature || d.DriftType == contracts.DriftDeprecatedFlow {
			stale++
		}
	}
	e.pills.DocHealth = stale
}

func ocrStatus() string {
	if _, err := exec.LookPath("pdftoppm"); err != nil {
		return "missing"
	}
	if _, err := exec.LookPath("tesseract"); err != nil {
		return "missing"
	}
	return "available"
}

// Status is the Page 1 pill and host summary.
func (e *executor) Status() StatusView {
	e.mu.Lock()
	v := e.pills
	v.Interpretation = e.normRev
	v.ImpactRevision = e.impactRev
	if e.graph != nil {
		v.Branch = e.graph.GitBranch
	}
	e.mu.Unlock()
	v.RepoPath = e.cfg.Repo
	v.Repo = filepath.Base(e.cfg.Repo)
	return v
}

// --- interpretation ---------------------------------------------------------------------------------

// Reinterpret re-runs normalization now and triggers the model-backed impact
// map (the Page 1 "Re-interpret" button).
func (e *executor) Reinterpret() {
	e.mu.Lock()
	e.modelImpact = true
	e.mu.Unlock()
	e.trigger(true)
}

// DocChanged schedules re-interpretation after ingestion or an ambiguity
// resolution, debounced.
func (e *executor) DocChanged() { e.trigger(false) }

// scheduleInitialInterpretation interprets a document that already has text
// when the session starts (a brief agreed before the window opened), so its
// epics appear without waiting for the first edit.
func (e *executor) scheduleInitialInterpretation(text string) {
	if strings.TrimSpace(text) != "" {
		e.trigger(false)
	}
}

func (e *executor) trigger(immediate bool) {
	select {
	case e.normTrigger <- immediate:
	default:
		if immediate {
			// A pending debounced trigger becomes immediate.
			select {
			case <-e.normTrigger:
			default:
			}
			select {
			case e.normTrigger <- true:
			default:
			}
		}
	}
}

func (e *executor) normalizeLoop(ctx context.Context) {
	for {
		var immediate bool
		select {
		case <-ctx.Done():
			return
		case immediate = <-e.normTrigger:
		}
		for !immediate {
			timer := time.NewTimer(normalizeDebounce)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case imm := <-e.normTrigger:
				timer.Stop()
				immediate = imm
				continue
			case <-timer.C:
			}
			break
		}
		e.mu.Lock()
		inIngestion := e.phase == relay.PhaseIngestion
		e.mu.Unlock()
		if !inIngestion {
			continue
		}
		if _, err := e.interpret(ctx); err != nil && ctx.Err() == nil {
			e.cfg.Auth.Local(relay.EventError, relay.ErrorPayload{
				Code: contracts.CodeInternal, Message: "interpretation failed: " + notice(err),
			}, "")
		}
	}
}

func (e *executor) resolutionList() []contracts.AmbiguityResolution {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]contracts.AmbiguityResolution, 0, len(e.resolutions))
	for _, r := range e.resolutions {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AmbiguityID < out[j].AmbiguityID })
	return out
}

func resolutionKey(text string, rs []contracts.AmbiguityResolution) string {
	raw, _ := json.Marshal(rs)
	sum := sha256.Sum256(append([]byte(text+"\x00"), raw...))
	return hex.EncodeToString(sum[:])
}

// interpret runs ingestion.Normalize over the current document and publishes the
// result to the window. The raw normalization for a given document text is
// cached, so owner ambiguity resolutions apply in memory without another model
// call. Background triggers publish the deterministic lexical impact map; an
// explicit Reinterpret triggers the model-backed impact pass.
func (e *executor) interpret(ctx context.Context) (contracts.NormalizedPRD, error) {
	text, _, err := e.cfg.Auth.Doc(ctx)
	if err != nil {
		return contracts.NormalizedPRD{}, err
	}
	rs := e.resolutionList()
	key := resolutionKey(text, rs)
	e.mu.Lock()
	runModelImpact := e.modelImpact
	e.modelImpact = false
	if e.norm != nil && e.normKey == key {
		n := *e.norm
		e.mu.Unlock()
		if runModelImpact {
			go e.refreshGrounding(ctx, n)
		}
		return n, nil
	}
	var base *contracts.NormalizedPRD
	if e.normBase != nil && e.normText == text {
		cp := *e.normBase
		base = &cp
	}
	e.mu.Unlock()

	if base == nil {
		raw, err := ingestion.Normalize(ctx, e.cfg.Gen, text, nil)
		if err != nil {
			return contracts.NormalizedPRD{}, err
		}
		base = &raw
	}
	norm, err := ingestion.ApplyResolutions(*base, rs)
	if err != nil {
		return contracts.NormalizedPRD{}, err
	}
	e.mu.Lock()
	if e.normBase == nil || e.normText != text {
		cp := *base
		e.normBase, e.normText = &cp, text
	}
	e.normRev++
	norm.Revision = e.normRev
	e.norm, e.normKey = &norm, key
	e.mu.Unlock()
	e.publishNormalized(norm)
	if runModelImpact {
		go e.refreshGrounding(ctx, norm)
	} else {
		go e.refreshLexicalGrounding(ctx, norm)
	}
	return norm, nil
}

// publishNormalized shows the interpretation to the window as an
// AVATAR_DRAFT{schema_id:"normalized_prd.v1"} frame. The reducer accepts drafts
// only while deliberating, so this frame is window-only in both modes.
func (e *executor) publishNormalized(n contracts.NormalizedPRD) {
	content := ingestion.WireJSON(n)
	sum := sha256.Sum256(content)
	e.cfg.Auth.Local(relay.EventAvatarDraft, relay.AvatarDraftPayload{
		AvatarID: avatarIngestion, DraftID: "normalized-prd", DraftRevision: n.Revision,
		SchemaID: ingestion.NormalizedPRDSchemaID, Content: content,
		ContentHash: hex.EncodeToString(sum[:]), Complete: true,
	}, "draft:"+avatarIngestion)
}

// --- event loop ------------------------------------------------------------------------------------

func (e *executor) eventLoop(ctx context.Context) {
	events := e.cfg.Auth.Events()
	for {
		env, ok := events.next(ctx)
		if !ok {
			return
		}
		e.handle(env)
	}
}

func decodePayload(env relay.WSEnvelope, v any) bool {
	return json.Unmarshal(env.Payload, v) == nil
}

func (e *executor) handle(env relay.WSEnvelope) {
	switch env.EventType {
	case relay.EventRunAssigned:
		var p relay.RunAssignedPayload
		if decodePayload(env, &p) {
			e.startRun(p.RunID, 0)
		}
	case relay.EventRunSuperseded:
		e.stopRun()
	case relay.EventCycleStart:
		var p relay.CycleStartPayload
		if decodePayload(env, &p) {
			e.mu.Lock()
			e.cycle = p.Cycle
			e.mu.Unlock()
		}
	case relay.EventRetryStage:
		var p relay.RetryStagePayload
		if decodePayload(env, &p) {
			e.retryStage(p)
		}
	case relay.EventPageTransition:
		var p relay.PageTransitionPayload
		if decodePayload(env, &p) {
			e.transition(p.To)
		}
	case relay.EventDecisionResolve:
		var p relay.DecisionResolvePayload
		if decodePayload(env, &p) {
			e.resolved(p)
		}
	case relay.EventAmbiguityResolve:
		var p relay.AmbiguityResolvePayload
		if decodePayload(env, &p) {
			e.mu.Lock()
			e.resolutions[p.AmbiguityID] = contracts.AmbiguityResolution{
				AmbiguityID: p.AmbiguityID, Choice: p.Choice, Note: p.Note, ResolvedBy: env.SenderID,
			}
			e.mu.Unlock()
			e.trigger(true)
		}
	case relay.EventApprovalPrepared:
		var p relay.ApprovalPreparedPayload
		if decodePayload(env, &p) {
			go e.commitApproval(p)
		}
	case relay.EventPlanApproved:
		var p relay.PlanApprovedPayload
		if decodePayload(env, &p) {
			go e.approved(p)
		}
	case relay.EventArtifactSynced:
		select {
		case e.synced <- struct{}{}:
		default:
		}
	case relay.EventPresence:
		var p struct {
			MemberID string `json:"member_id"`
			Online   bool   `json:"online"`
		}
		if decodePayload(env, &p) && p.MemberID != "" {
			e.mu.Lock()
			if p.Online {
				e.members[p.MemberID] = true
			} else {
				delete(e.members, p.MemberID)
			}
			e.mu.Unlock()
		}
	case relay.EventLeaseLost, relay.EventFenced:
		e.leaseLost(errors.New(string(env.EventType)))
	}
}

func (e *executor) transition(to relay.Phase) {
	e.mu.Lock()
	e.phase = to
	runID := e.runID
	e.mu.Unlock()
	switch to {
	case relay.PhaseIngestion:
		e.stopRun()
		e.mu.Lock()
		e.converged, e.synthesized, e.synthHash, e.runInput = nil, nil, "", nil
		e.runID = ""
		e.mu.Unlock()
	case relay.PhaseDeliberation:
		// The page that shows the map is gone. The map already built stays, so
		// going back is instant, but an unfinished call is not worth paying for.
		e.cancelImpact()
	case relay.PhaseSynthesis:
		e.startSynthesis(runID)
	case relay.PhaseCancelled:
		e.stopRun()
		e.cancelImpact()
		e.finish(PhaseCancelled, "the session was cancelled", nil)
	}
}

func (e *executor) resolved(p relay.DecisionResolvePayload) {
	now := time.Now().UTC()
	d := contracts.Decision{
		ID: p.DecisionID, InputRevision: p.InputRevision, ResolvedOptionID: p.OptionID,
		ResolvedBy: p.ActorID, ResolvedAt: &now,
	}
	e.mu.Lock()
	ch := e.decisions
	e.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- d:
	default:
		e.log.Warn("decision channel full; resolution dropped", "decision", p.DecisionID)
	}
}

func (e *executor) finish(phase SessionPhase, message string, artifacts []string) {
	e.mu.Lock()
	if e.finished {
		e.mu.Unlock()
		return
	}
	e.finished = true
	e.mu.Unlock()
	if e.cfg.Finish != nil {
		e.cfg.Finish(phase, message, artifacts)
	}
}

// --- deliberation ------------------------------------------------------------------------------------

// claimRun marks the pipeline busy with stage. It reports the work already in
// flight when there is some, and claims nothing in that case.
func (e *executor) claimRun(stage string) (string, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.busy != "" {
		return e.busy, false
	}
	e.busy = stage
	return stage, true
}

// releaseRun frees the claim of a finished goroutine. A goroutine whose run was
// superseded frees nothing: the claim belongs to the run that replaced it.
func (e *executor) releaseRun(gen int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if gen == e.runGen {
		e.busy = ""
	}
}

// stopRun cancels the active pipeline goroutine, if any.
func (e *executor) stopRun() {
	e.mu.Lock()
	cancel := e.runCancel
	e.runCancel = nil
	e.runGen++
	e.busy = ""
	e.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// newRunContext replaces the active run context and decision channel.
func (e *executor) newRunContext(runID string) (context.Context, int, chan contracts.Decision) {
	e.mu.Lock()
	old := e.runCancel
	ctx, cancel := context.WithCancel(e.ctx)
	e.runCancel = cancel
	e.runGen++
	gen := e.runGen
	e.runID = runID
	e.decisions = make(chan contracts.Decision, 64)
	ch := e.decisions
	e.mu.Unlock()
	if old != nil {
		old()
	}
	return ctx, gen, ch
}

func (e *executor) startRun(runID string, startCycle int) {
	if runID == "" {
		return
	}
	if busy, ok := e.claimRun(busyDeliberation); !ok {
		e.log.Info("a run is already in flight; the request was ignored", "busy", busy, "run", runID)
		return
	}
	e.mu.Lock()
	e.converged, e.synthesized, e.synthHash = nil, nil, ""
	e.cycle = startCycle
	e.mu.Unlock()
	ctx, gen, decisions := e.newRunContext(runID)
	go e.deliberate(ctx, gen, runID, startCycle, decisions)
}

func (e *executor) retryStage(p relay.RetryStagePayload) {
	e.mu.Lock()
	runID, phase, cycle := e.runID, e.phase, e.cycle
	e.mu.Unlock()
	if p.RunID != runID {
		return
	}
	switch {
	case phase == relay.PhaseSynthesis && p.StageID == relay.StageSynthesizer:
		e.startSynthesis(runID)
	case phase == relay.PhaseDeliberation:
		if cycle < 1 {
			cycle = 1
		}
		if _, ok := e.claimRun(busyDeliberation); !ok {
			e.stageAlreadyRunning(runID, p.StageID, cycle)
			return
		}
		ctx, gen, decisions := e.newRunContext(runID)
		go e.deliberate(ctx, gen, runID, cycle, decisions)
	}
}

// stageAlreadyRunning answers a retry of work that is still in flight. The
// window hears the stage is running rather than nothing at all, so a second
// click re-synchronises the card instead of cancelling a paid call.
func (e *executor) stageAlreadyRunning(runID, stage string, cycle int) {
	if stage == "" {
		stage = relay.StageArchitect
	}
	e.log.Info("retry ignored: the stage is still running", "stage", stage, "run", runID)
	_ = e.cfg.Auth.Emit(relay.EventAvatarThought, relay.AvatarThoughtPayload{
		RunID: runID, AvatarID: stage, Cycle: cycle, Status: "Thinking",
		PublicSummary: "This stage is already running; the retry was ignored. Wait for it to finish or stop the run.",
	})
}

func (e *executor) current(gen int) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return gen == e.runGen && !e.finished
}

func (e *executor) emitFor(ctx context.Context) func(contracts.PublicEvent) error {
	return func(ev contracts.PublicEvent) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return e.cfg.Auth.Emit(relay.WSEventType(ev.Type), ev.Payload)
	}
}

// stageFailed reports a failure that happened before any avatar could, such as
// an interpretation error, in the shape the window already renders.
func (e *executor) stageFailed(runID, avatar string, err error) {
	e.log.Warn("stage failed", "stage", avatar, "err", err)
	_ = e.cfg.Auth.Emit(relay.EventAvatarThought, relay.AvatarThoughtPayload{
		RunID: runID, AvatarID: avatar, Cycle: 1, Status: "failed",
		PublicSummary: notice(err),
	})
}

// cancelDeliberation gives the deliberation avatars a terminal status after a
// failure that stopped the run before they could start. Without it their cards
// stay queued for a run that is over.
func (e *executor) cancelDeliberation(runID, failed string) {
	for _, avatar := range []string{avatarPM, relay.StageArchitect, relay.StageCritic, avatarSynthesizer} {
		if avatar == failed {
			continue
		}
		_ = e.cfg.Auth.Emit(relay.EventAvatarThought, relay.AvatarThoughtPayload{
			RunID: runID, AvatarID: avatar, Cycle: 1, Status: "Cancelled",
			PublicSummary: "The run stopped before this avatar started. Retry the stage or revise the baseline.",
		})
	}
}

func hashJSON(v any) string {
	raw, _ := json.Marshal(v)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func productKeywords(p contracts.ProductPRD) []string {
	texts := []string{p.Title, p.Problem}
	for _, group := range [][]string{p.Goals, p.Actors, p.Flows, p.Requirements, p.AcceptanceCriteria} {
		texts = append(texts, group...)
	}
	return ast.KeywordsFromText(texts...)
}

func (e *executor) deliberate(ctx context.Context, gen int, runID string, startCycle int, decisions <-chan contracts.Decision) {
	defer e.releaseRun(gen)
	norm, err := e.interpret(ctx)
	if err != nil {
		if ctx.Err() == nil {
			e.stageFailed(runID, avatarPM, err)
			e.cancelDeliberation(runID, avatarPM)
		}
		return
	}
	select {
	case <-e.scanDone:
	case <-ctx.Done():
		return
	}
	e.mu.Lock()
	g := e.graph
	e.mu.Unlock()
	if g == nil {
		g = &contracts.RepoContextGraph{
			SchemaVersion: contracts.SchemaVersion,
			Coverage:      contracts.Coverage{Notes: []string{"the repository scan failed; no code context is available"}},
		}
	}
	keywords := productKeywords(norm.Product)
	snap, _ := ast.Snapshot(g, ast.SnapshotOptions{Keywords: keywords})
	// Drift detection caps items per document but not across them, so the list
	// is bounded here, the way the graph is.
	drift, driftInfo := ast.DriftDigest(g.DocDriftReports, ast.DriftDigestOptions{Keywords: keywords})
	var notes []string
	if driftInfo.Statement != "" {
		notes = append(notes, driftInfo.Statement)
	}
	// The context is gathered again for the frozen PRD, so the avatars never
	// see notes matched against a draft the user has since changed.
	pc := e.collectContext(ctx, normalizedText(norm))
	if ctx.Err() != nil {
		return
	}
	e.mu.Lock()
	e.projectCtx = &pc
	e.pills.Context, e.pills.ContextDetail = contextPill(pc)
	e.mu.Unlock()
	cfg := e.cfg.Config
	in := avatars.RunInput{
		RunID: runID, PRD: norm, Graph: snap, GraphHash: hashJSON(snap), DocDrift: drift,
		EvidenceNotes: notes, Context: pc, ContextHash: hashJSON(pc),
		Models:    avatars.ModelSlots{Main: cfg.DefaultMainModel, Judge: cfg.DefaultJudgeModel},
		MaxCycles: cfg.MaxCycles, MaxParallel: cfg.MaxParallel, StartCycle: startCycle,
		// The stages get their own budget, not contracts.DefaultGenerateTimeout:
		// the architect design part alone was measured at 110 seconds on opus
		// with a minimal payload (ADR 0009).
		CallTimeout: time.Duration(cfg.StageTimeout()) * time.Second,
		NoNarrator:  cfg.DisableNarrator,
		Checkpoints: e.checkpoints,
	}
	e.mu.Lock()
	e.runInput = &in
	e.mu.Unlock()

	si, err := avatars.Run(ctx, in, e.cfg.Gen, e.emitFor(ctx), decisions)
	if !e.current(gen) || ctx.Err() != nil {
		return
	}
	var paused *avatars.PausedError
	var stage *avatars.StageError
	switch {
	case err == nil:
		e.mu.Lock()
		e.converged = &si
		e.mu.Unlock()
	case errors.As(err, &paused):
		// The authority already paused the run on the critique that reached the
		// cap; the window shows the open blockers with retry and revise controls.
		e.log.Info("deliberation paused at the cycle cap", "cycle", paused.Cycle, "blockers", len(paused.Blockers))
	case errors.As(err, &stage):
		e.log.Warn("avatar stage failed", "stage", stage.Stage, "partial", stage.Partial, "err", stage.Err)
	default:
		e.cfg.Auth.Local(relay.EventError, relay.ErrorPayload{
			Code: contracts.CodeInternal, Message: "deliberation failed: " + notice(err),
		}, "")
	}
}

// --- synthesis ------------------------------------------------------------------------------------------

func (e *executor) startSynthesis(runID string) {
	e.mu.Lock()
	si, in := e.converged, e.runInput
	e.mu.Unlock()
	if si == nil || in == nil {
		e.cfg.Auth.Local(relay.EventError, relay.ErrorPayload{
			Code: contracts.CodeIllegalPhase, Message: "deliberation has not produced a validated design to synthesize",
		}, "")
		return
	}
	if _, ok := e.claimRun(busySynthesis); !ok {
		e.stageAlreadyRunning(runID, relay.StageSynthesizer, 1)
		return
	}
	ctx, gen, _ := e.newRunContext(runID)
	go e.synthesize(ctx, gen, runID, *in, *si)
}

func (e *executor) synthesize(ctx context.Context, gen int, runID string, in avatars.RunInput, si contracts.SynthesisInput) {
	defer e.releaseRun(gen)
	out, err := avatars.Synthesize(ctx, in, si, e.cfg.Gen, e.emitFor(ctx))
	if !e.current(gen) || ctx.Err() != nil {
		return
	}
	if err != nil {
		e.log.Warn("synthesis failed", "err", err)
		var stage *avatars.StageError
		if !errors.As(err, &stage) {
			e.stageFailed(runID, avatarSynthesizer, err)
		}
		return
	}
	plan, err := synthesizer.Compile(out, contracts.ApprovalMeta{})
	if err != nil {
		e.stageFailed(runID, avatarSynthesizer, err)
		return
	}
	content, err := plan.DraftContent()
	if err != nil {
		e.stageFailed(runID, avatarSynthesizer, err)
		return
	}
	hash := synthesizer.ContentHash(out)
	e.mu.Lock()
	e.synthesized, e.synthHash = &out, hash
	e.mu.Unlock()
	err = e.cfg.Auth.Emit(relay.EventAvatarDraft, relay.AvatarDraftPayload{
		RunID: runID, AvatarID: avatarSynthesizer, Cycle: 1, DraftID: "synthesis", DraftRevision: 1,
		SchemaID: schemaSynthesisV1, Content: content, ContentHash: hash, Complete: true,
	})
	if err != nil && ctx.Err() == nil {
		e.stageFailed(runID, avatarSynthesizer, err)
	}
}

// --- approval ---------------------------------------------------------------------------------------------

func (e *executor) artifactFailed(err error) {
	code := artifactCodeOf(err)
	rel := artifactSpecRel
	var rp interface{ RelPath() string }
	if errors.As(err, &rp) && (rp.RelPath() == artifactSpecRel || rp.RelPath() == artifactManifestRel) {
		rel = rp.RelPath()
	}
	e.mu.Lock()
	e.failure = &approvalFailure{
		Code: code, RelPath: rel, AbsPath: filepath.Join(e.cfg.Repo, filepath.FromSlash(rel)), Message: notice(err),
	}
	e.mu.Unlock()
	e.log.Warn("approval write failed", "code", code, "rel_path", rel)
	_ = e.cfg.Auth.Emit(relay.EventArtifactSaved, relay.ArtifactSavedPayload{
		OK: false, Error: &relay.ArtifactError{Code: code, RelPath: rel},
	})
}

// commitApproval runs after the authority prepared an approval: recompute the
// content hash, compile the final bytes with the approval identity, commit them
// transactionally, publish them in Remote mode, and report ARTIFACT_SAVED.
// PLAN_APPROVED is emitted by the authority only on a successful report.
func (e *executor) commitApproval(p relay.ApprovalPreparedPayload) {
	e.mu.Lock()
	out, want := e.synthesized, e.synthHash
	e.approvalID, e.createdAt, e.failure = p.ApprovalID, p.CreatedAt, nil
	e.mu.Unlock()
	if out == nil {
		e.artifactFailed(&hashError{msg: "no synthesized plan is available to commit"})
		return
	}
	if got := synthesizer.ContentHash(*out); got != want {
		e.artifactFailed(&hashError{msg: "the synthesized content no longer matches the approved hash"})
		return
	}
	meta := contracts.ApprovalMeta{ApprovalID: p.ApprovalID, CreatedAt: p.CreatedAt}
	plan, err := synthesizer.Compile(*out, meta)
	if err != nil {
		e.artifactFailed(err)
		return
	}
	res, err := e.cfg.Store.Commit(e.ctx, meta, plan.SpecMarkdown, plan.ManifestJSON)
	if err != nil {
		e.artifactFailed(err)
		return
	}
	if err := planstate.RecordApproval(e.cfg.Repo, res.ManifestSHA256, p.ApprovalID); err != nil {
		e.log.Warn("could not record the approval in the local ledger", "err", err)
	}
	if e.cfg.Approved != nil {
		e.cfg.Approved(p.ApprovalID, plan.SpecMarkdown)
	}
	// The plan is approved: the drafts it was built from are no longer work in
	// progress, so the unfinished run is removed from the plan directory.
	e.mu.Lock()
	runID := e.runID
	e.mu.Unlock()
	e.checkpoints.Forget(runID)
	if e.cfg.Upload != nil {
		if err := e.cfg.Upload(e.ctx, p.ApprovalID, plan.SpecMarkdown, plan.ManifestJSON); err != nil {
			e.log.Warn("artifact upload failed", "err", err)
			e.mu.Lock()
			e.failure = &approvalFailure{
				Code: relay.ArtifactEOTHER, RelPath: artifactSpecRel,
				AbsPath: filepath.Join(e.cfg.Repo, filepath.FromSlash(artifactSpecRel)), Message: "publishing to the relay failed: " + notice(err),
			}
			e.mu.Unlock()
			_ = e.cfg.Auth.Emit(relay.EventArtifactSaved, relay.ArtifactSavedPayload{
				OK: false, Error: &relay.ArtifactError{Code: relay.ArtifactEOTHER, RelPath: artifactSpecRel},
			})
			return
		}
	}
	err = e.cfg.Auth.Emit(relay.EventArtifactSaved, relay.ArtifactSavedPayload{
		OK: true, SpecSHA256: res.SpecSHA256, ManifestSHA256: res.ManifestSHA256,
	})
	if err != nil {
		e.log.Warn("authority refused ARTIFACT_SAVED", "err", err)
	}
}

type hashError struct{ msg string }

func (h *hashError) Error() string { return h.msg }

// artifactCodeOf classifies a failed commit for the relay's code allowlist. A
// precondition failure raised here is a hash mismatch; everything else is
// classified by the store.
func artifactCodeOf(err error) string {
	var he *hashError
	if errors.As(err, &he) {
		return relay.ArtifactEHASH
	}
	return storage.ArtifactCode(err)
}

// ApprovalFailure is the last failed approval, for the owner's window.
func (e *executor) ApprovalFailure() *approvalFailure {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.failure == nil {
		return nil
	}
	f := *e.failure
	return &f
}

// artifactPaths are the committed artifact locations in the repository.
func (e *executor) artifactPaths() []string {
	return []string{
		filepath.Join(e.cfg.Repo, filepath.FromSlash(artifactSpecRel)),
		filepath.Join(e.cfg.Repo, filepath.FromSlash(artifactManifestRel)),
	}
}

// approved runs once PLAN_APPROVED is on the wire. In Remote mode it gives
// followers up to five seconds to report ARTIFACT_SYNCED before the loopback
// server stops.
func (e *executor) approved(p relay.PlanApprovedPayload) {
	e.mu.Lock()
	e.phase = relay.PhaseApproved
	others := 0
	for id := range e.members {
		if id != ownerMemberID {
			others++
		}
	}
	e.mu.Unlock()
	if e.cfg.WaitForSync && others > 0 {
		timer := time.NewTimer(syncWait)
		defer timer.Stop()
		got := 0
	wait:
		for got < others {
			select {
			case <-e.synced:
				got++
			case <-timer.C:
				break wait
			case <-e.ctx.Done():
				break wait
			}
		}
	}
	e.finish(PhaseApproved, "approved", e.artifactPaths())
}

// phaseNow reports the phase the executor has observed.
func (e *executor) phaseNow() relay.Phase {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.phase
}
