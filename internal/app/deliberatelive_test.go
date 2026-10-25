//go:build client && !server

package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/ast"
	"github.com/danielckv/agentic-plan-forge/internal/avatars"
	"github.com/danielckv/agentic-plan-forge/internal/config"
	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/gather"
	"github.com/danielckv/agentic-plan-forge/internal/host"
	"github.com/danielckv/agentic-plan-forge/internal/ingestion"
	"github.com/danielckv/agentic-plan-forge/internal/relay"
	"github.com/danielckv/agentic-plan-forge/internal/synthesizer"
)

// TestLiveDeliberationEndToEnd drives a long brief through the whole gate
// against real hosts: interpretation, repository grounding, the four avatars,
// and the synthesis lock. It asserts that every avatar produced the draft its
// stage owes, and that the synthesis compiles.
//
// It also records every provider call — role, host, model, payload size, wall
// time — and prints the critical path. The run's wall clock against the sum of
// its call durations is what says how much of the pipeline is already running
// in parallel, so a decision to make it faster rests on a number.
//
// It is skipped unless PLAN_FORGE_LIVE_DELIBERATION=1, because a full run
// spends real money. One cycle is run by default; PLAN_FORGE_LIVE_CYCLES
// raises it.
//
//	PLAN_FORGE_LIVE_DELIBERATION=1 PLAN_FORGE_LIVE_HOST=claude PLAN_FORGE_LIVE_MODEL=opus \
//	    go test -tags client -run TestLiveDeliberation -timeout 60m -v ./internal/app/
func TestLiveDeliberationEndToEnd(t *testing.T) {
	if os.Getenv("PLAN_FORGE_LIVE_DELIBERATION") != "1" {
		t.Skip("set PLAN_FORGE_LIVE_DELIBERATION=1 to spend model calls on a full deliberation")
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repo, err := ast.RepoRoot(context.Background(), wd)
	if err != nil {
		t.Fatal(err)
	}
	brief, err := os.ReadFile(filepath.Join("testdata", "long-brief.md"))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("brief: %d bytes, %d lines", len(brief), countLines(brief))

	cfg := config.Default()
	if loaded, lerr := config.Load(""); lerr == nil {
		cfg = loaded
	}
	if m := os.Getenv("PLAN_FORGE_LIVE_MODEL"); m != "" {
		cfg.DefaultMainModel = m
		cfg.DefaultJudgeModel = m
	}
	// PLAN_FORGE_LIVE_HOST names the invoked host, which is only one candidate
	// in the per-role rotation (ADR 0009): roleHostOrder sends specialists to
	// OMP first whatever the caller asked for. Pin every role to the named host
	// instead, or a run on a machine whose other hosts are stale fails at a
	// stage that has nothing to do with what is being measured. Measured here
	// on 2026-10-25: four specialist calls spent 17m42s and all four died with
	// "host advertised a non-empty or invalid tools list (update OMP)".
	if h := os.Getenv("PLAN_FORGE_LIVE_HOST"); h != "" {
		cfg.MainHost, cfg.JudgeHost = h, h
	}
	cycles := 1
	if v := os.Getenv("PLAN_FORGE_LIVE_CYCLES"); v != "" {
		if n, cerr := strconv.Atoi(v); cerr == nil && n > 0 {
			cycles = n
		}
	}

	rec := &callRecorder{inner: host.NewRouter(cfg, host.Kind(os.Getenv("PLAN_FORGE_LIVE_HOST"))), t0: time.Now()}
	ctx, cancel := context.WithTimeout(context.Background(), 55*time.Minute)
	defer cancel()

	// 1. Interpretation. A long brief splits into sections, one call each.
	markInterpret := time.Now()
	norm, err := ingestion.Normalize(ctx, rec, string(brief), nil)
	if err != nil {
		t.Fatalf("interpretation failed: %v", err)
	}
	norm.Revision = 1
	t.Logf("interpretation in %v: %d epics, %d requirements, %d ambiguities, appendix %d chars",
		time.Since(markInterpret).Round(time.Second), len(norm.Epics), len(norm.Product.Requirements),
		len(norm.Ambiguities), len(norm.TechnicalAppendix))
	if len(norm.Epics) == 0 {
		t.Fatal("the brief produced no epics; the rest of the run has nothing to plan")
	}

	// 2. Grounding, exactly as (*executor).deliberate builds it.
	markGround := time.Now()
	graph, err := ast.Scan(ctx, ast.ScanOptions{Root: repo, ReadCache: true, WriteCache: true})
	if err != nil {
		t.Fatalf("scan failed: %v", err)
	}
	keywords := productKeywords(norm.Product)
	snap, _ := ast.Snapshot(graph, ast.SnapshotOptions{Keywords: keywords})
	drift, driftInfo := ast.DriftDigest(graph.DocDriftReports, ast.DriftDigestOptions{Keywords: keywords})
	var notes []string
	if driftInfo.Statement != "" {
		notes = append(notes, driftInfo.Statement)
	}
	pc, err := gather.Collect(ctx, gather.Options{Repo: repo, Text: normalizedText(norm)})
	if err != nil {
		t.Fatalf("gather failed: %v", err)
	}
	t.Logf("grounding in %v: graph %d files (%d KiB), drift %d items, context %d excerpts",
		time.Since(markGround).Round(time.Second), len(snap.Symbols), jsonKiB(snap), len(drift), len(pc.Hits))

	// 3. The deliberation.
	in := avatars.RunInput{
		RunID: "live-deliberation", PRD: norm, Graph: snap, GraphHash: hashJSON(snap),
		DocDrift: drift, EvidenceNotes: notes, Context: pc, ContextHash: hashJSON(pc),
		Models:    avatars.ModelSlots{Main: cfg.DefaultMainModel, Judge: cfg.DefaultJudgeModel},
		MaxCycles: cycles, MaxParallel: cfg.MaxParallel,
		CallTimeout: time.Duration(cfg.StageTimeout()) * time.Second,
		NoNarrator:  true,
	}
	// A conflict between specialists opens a decision card and the orchestrator
	// waits for a human. The test answers with the recommended option so a run
	// never stalls until the context deadline, and reports what it answered.
	decisions := make(chan contracts.Decision, 16)
	seen := &avatarLog{decide: decisions}
	markRun := time.Now()
	si, runErr := avatars.Run(ctx, in, rec, seen.emit, decisions)
	runWall := time.Since(markRun)
	t.Logf("deliberation in %v", runWall.Round(time.Second))

	var paused *avatars.PausedError
	var stage *avatars.StageError
	switch {
	case runErr == nil:
	case errors.As(runErr, &paused):
		t.Logf("the run paused after %d cycles with open issues; the drafts still stand", cycles)
	case errors.As(runErr, &stage):
		rec.report(t, runWall)
		t.Fatalf("stage %q failed: %v", stage.Stage, stage)
	default:
		rec.report(t, runWall)
		t.Fatalf("deliberation failed: %v", runErr)
	}

	// 4. The synthesis lock.
	markSynth := time.Now()
	locked, err := avatars.Synthesize(ctx, in, si, rec, seen.emit)
	if err != nil {
		rec.report(t, runWall)
		t.Fatalf("synthesis failed: %v", err)
	}
	t.Logf("synthesis in %v", time.Since(markSynth).Round(time.Second))

	plan, err := synthesizer.Compile(locked, contracts.ApprovalMeta{})
	if err != nil {
		t.Fatalf("the locked design does not compile: %v", err)
	}
	t.Logf("specification: %q, %d bytes of Markdown, %d bytes of manifest",
		plan.Title, len(plan.SpecMarkdown), len(plan.ManifestJSON))

	// 5. Every avatar did its stage's work. The drafts are raw on the
	// SynthesisInput, so each is decoded and inspected rather than trusted.
	var pm synthesizer.PMDoc
	if err := json.Unmarshal(locked.PM, &pm); err != nil {
		t.Errorf("the Product Manager draft does not decode: %v", err)
	}
	if len(pm.Epics) == 0 || len(pm.Stories) == 0 {
		t.Errorf("the Product Manager produced %d epics and %d stories", len(pm.Epics), len(pm.Stories))
	}
	var arch synthesizer.ArchitectDoc
	if err := json.Unmarshal(locked.Architecture, &arch); err != nil {
		t.Errorf("the Technical Architect draft does not decode: %v", err)
	}
	if len(arch.Workstreams) == 0 {
		t.Error("the Technical Architect produced no workstreams")
	}
	if len(arch.Contracts) == 0 {
		t.Error("the Technical Architect froze no interface contracts")
	}
	tasks := 0
	for i, rawSpec := range locked.Specialists {
		var sd synthesizer.SpecialistDoc
		if err := json.Unmarshal(rawSpec, &sd); err != nil {
			t.Errorf("specialist draft %d does not decode: %v", i, err)
			continue
		}
		tasks += len(sd.Tasks)
	}
	if len(locked.Specialists) == 0 || tasks == 0 {
		t.Errorf("the Domain Specialists produced %d drafts and %d tasks", len(locked.Specialists), tasks)
	}
	if len(locked.Critiques) == 0 {
		t.Error("the Red-Team Critic produced no critique")
	}
	if arch.Synthesis == nil || arch.Synthesis.ExecutiveSummary == "" {
		t.Error("the Lead Synthesizer produced no executive summary")
	}
	t.Logf("drafts: %d epics, %d stories, %d workstreams, %d contracts, %d specialists, %d tasks, %d critiques",
		len(pm.Epics), len(pm.Stories), len(arch.Workstreams), len(arch.Contracts),
		len(locked.Specialists), tasks, len(locked.Critiques))
	for _, avatar := range []string{"pm", "architect", "critic", "synthesizer"} {
		if !seen.synced(avatar) {
			t.Errorf("avatar %q never reached a Synced status", avatar)
		}
	}
	t.Logf("avatars that reported: %v", seen.avatars())
	if d := seen.decisions(); len(d) > 0 {
		t.Logf("decision cards answered by the test: %v", d)
	}

	rec.report(t, runWall)
}

// --- measurement ------------------------------------------------------------------------------

// callRecord is one provider call, timed from the start of the test.
type callRecord struct {
	Role    contracts.RoleID
	Host    string
	Model   string
	Payload int
	Output  int
	Start   time.Duration
	End     time.Duration
	Err     string
}

func (c callRecord) dur() time.Duration { return c.End - c.Start }

// callRecorder wraps the real generator and times every call. It also tracks
// how many calls were ever in flight at once, which is the measured parallelism
// of the pipeline rather than the configured one.
type callRecorder struct {
	inner contracts.Generator
	t0    time.Time

	mu    sync.Mutex
	recs  []*callRecord
	live  int
	peak  int
	stamp []string
}

func (r *callRecorder) GenerateStream(ctx context.Context, req contracts.GenerateRequest, emit func(contracts.GenerationEvent) error) (contracts.GenerationResult, error) {
	c := &callRecord{Role: req.Role, Model: req.Model, Payload: len(req.UserPayload), Start: time.Since(r.t0)}
	r.mu.Lock()
	r.recs = append(r.recs, c)
	r.live++
	if r.live > r.peak {
		r.peak = r.live
	}
	r.mu.Unlock()

	res, err := r.inner.GenerateStream(ctx, req, func(ev contracts.GenerationEvent) error {
		if ev.Kind == contracts.EventPlacement && ev.Placement != nil {
			r.mu.Lock()
			c.Host = ev.Placement.Host
			if ev.Placement.Model != "" {
				c.Model = ev.Placement.Model
			}
			r.mu.Unlock()
		}
		return emit(ev)
	})

	r.mu.Lock()
	c.End = time.Since(r.t0)
	c.Output = len(res.Content)
	if err != nil {
		c.Err = err.Error()
	}
	r.live--
	r.mu.Unlock()
	return res, err
}

// report prints what the run spent, slowest role first, then the one number
// that matters for making it faster: wall clock against the sum of call time.
func (r *callRecorder) report(t *testing.T, wall time.Duration) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()

	type agg struct {
		role             contracts.RoleID
		calls            int
		total, slowest   time.Duration
		payload, outputs int
		hosts            map[string]bool
	}
	by := map[contracts.RoleID]*agg{}
	var sum time.Duration
	for _, c := range r.recs {
		a := by[c.Role]
		if a == nil {
			a = &agg{role: c.Role, hosts: map[string]bool{}}
			by[c.Role] = a
		}
		a.calls++
		a.total += c.dur()
		a.slowest = max(a.slowest, c.dur())
		a.payload += c.Payload
		a.outputs += c.Output
		if c.Host != "" {
			a.hosts[c.Host+"/"+c.Model] = true
		}
		sum += c.dur()
	}
	rows := make([]*agg, 0, len(by))
	for _, a := range by {
		rows = append(rows, a)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].total > rows[j].total })

	t.Logf("")
	t.Logf("%-12s %5s %9s %9s %9s %9s  %s", "role", "calls", "total", "slowest", "payload", "output", "placed on")
	for _, a := range rows {
		placed := make([]string, 0, len(a.hosts))
		for h := range a.hosts {
			placed = append(placed, h)
		}
		sort.Strings(placed)
		t.Logf("%-12s %5d %9s %9s %8dK %8dK  %v", a.role, a.calls,
			a.total.Round(time.Second), a.slowest.Round(time.Second),
			a.payload>>10, a.outputs>>10, placed)
	}
	for _, c := range r.recs {
		if c.Err != "" {
			t.Logf("  failed: %s after %v — %s", c.Role, c.dur().Round(time.Second), c.Err)
		}
	}
	t.Logf("")
	t.Logf("deliberation wall clock %v; provider call time %v; peak calls in flight %d",
		wall.Round(time.Second), sum.Round(time.Second), r.peak)
	if sum > 0 {
		t.Logf("overlap factor %.2f (1.00 means fully serial; the ceiling is MaxParallel)",
			float64(sum)/float64(max(wall, time.Nanosecond)))
	}
}

// --- what the room saw ------------------------------------------------------------------------

// avatarLog records the statuses each avatar published, so the test can assert
// that a stage ran rather than inferring it from the drafts alone.
type avatarLog struct {
	decide chan<- contracts.Decision

	mu       sync.Mutex
	st       map[string][]string
	answered []string
}

func (a *avatarLog) emit(ev contracts.PublicEvent) error {
	raw, err := json.Marshal(ev.Payload)
	if err != nil {
		return nil
	}
	if ev.Type == string(relay.EventDecisionPrompt) {
		a.answer(raw)
		return nil
	}
	var p struct {
		Avatar string `json:"avatar"`
		Status string `json:"status"`
	}
	if json.Unmarshal(raw, &p) != nil || p.Avatar == "" || p.Status == "" {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.st == nil {
		a.st = map[string][]string{}
	}
	a.st[p.Avatar] = append(a.st[p.Avatar], p.Status)
	return nil
}

// answer resolves a decision card with its recommended option, falling back to
// the first offered one. The channel is buffered, so this never blocks the
// orchestrator that called emit.
func (a *avatarLog) answer(raw json.RawMessage) {
	var d contracts.Decision
	if json.Unmarshal(raw, &d) != nil || d.ID == "" || len(d.Options) == 0 {
		return
	}
	pick := d.RecommendedOptionID
	if pick == "" {
		pick = d.Options[0].ID
	}
	a.mu.Lock()
	a.answered = append(a.answered, fmt.Sprintf("%s=%s", d.ID, pick))
	a.mu.Unlock()
	select {
	case a.decide <- contracts.Decision{ID: d.ID, ResolvedOptionID: pick, ResolvedBy: "live-test"}:
	default:
	}
}

func (a *avatarLog) decisions() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.answered...)
}

func (a *avatarLog) synced(avatar string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, s := range a.st[avatar] {
		if s == "Synced" {
			return true
		}
	}
	return false
}

func (a *avatarLog) avatars() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]string, 0, len(a.st))
	for k, v := range a.st {
		out = append(out, fmt.Sprintf("%s(%d)", k, len(v)))
	}
	sort.Strings(out)
	return out
}

// --- small helpers ----------------------------------------------------------------------------

func countLines(b []byte) int {
	n := 0
	for _, c := range b {
		if c == '\n' {
			n++
		}
	}
	return n
}

func jsonKiB(v any) int {
	b, err := json.Marshal(v)
	if err != nil {
		return 0
	}
	return len(b) >> 10
}
