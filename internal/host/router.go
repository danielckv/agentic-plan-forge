//go:build client && !server

package host

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/danielckv/agentic-plan-forge/internal/config"
	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// SkipReason records why one candidate could not serve a model slot.
type SkipReason struct {
	Kind   Kind   `json:"host"`
	Probe  string `json:"probe"`
	Reason string `json:"reason"`
}

// Resolution is the selected host, version, and model for one slot.
type Resolution struct {
	Kind       Kind   `json:"host"`
	Version    string `json:"version"`
	Model      string `json:"model,omitempty"`
	SharesMain bool   `json:"shares_main,omitempty"`
}

// NoHostError means no authenticated adapter satisfied the tool-free gate. CLI
// callers map it to exit code 3.
type NoHostError struct {
	Slot    contracts.ModelSlot
	Reasons []SkipReason
}

func (e *NoHostError) Error() string {
	var details []string
	for _, reason := range e.Reasons {
		details = append(details, fmt.Sprintf("%s %s: %s", reason.Kind, reason.Probe, reason.Reason))
	}
	message := "no usable generation host; run `claude auth login`, `omp login`, or start `agy` once interactively to sign in"
	if len(details) != 0 {
		message += " (" + strings.Join(details, "; ") + ")"
	}
	return message
}

// ExitCode lets command dispatchers preserve the prerequisite exit contract.
func (e *NoHostError) ExitCode() int { return 3 }

type resolvedAdapter struct {
	adapter Adapter
	version string
}

// Router implements contracts.Generator by selecting an authenticated adapter
// independently for MAIN and JUDGE slots.
type Router struct {
	cfg         config.Config
	invokedHost Kind
	adapters    map[Kind]Adapter

	mu       sync.Mutex
	ready    map[Kind]resolvedAdapter
	main     *resolvedAdapter
	judge    *resolvedAdapter
	reasons  []SkipReason
}

var _ contracts.Generator = (*Router)(nil)

// NewRouter snapshots the registry. invokedHost is the --host value supplied by
// the invoking skill; an empty value means no invocation preference.
func NewRouter(cfg config.Config, invokedHost Kind) *Router {
	return &Router{
		cfg:         cfg,
		invokedHost: invokedHost,
		adapters:    registeredMap(cfg.HostEnvAllow),
		ready:       make(map[Kind]resolvedAdapter),
	}
}

// Resolve selects a ready adapter without invoking a model.
func (r *Router) Resolve(ctx context.Context, slot contracts.ModelSlot) (Resolution, error) {
	if slot == "" {
		slot = contracts.SlotMain
	}
	if slot != contracts.SlotMain && slot != contracts.SlotJudge {
		return Resolution{}, fmt.Errorf("unknown model slot %q", slot)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	resolved, shared, err := r.resolveLocked(ctx, slot)
	if err != nil {
		return Resolution{}, err
	}
	return Resolution{
		Kind:       resolved.adapter.Kind(),
		Version:    resolved.version,
		Model:      r.modelForSlot(slot),
		SharesMain: shared,
	}, nil
}

// GenerateStream implements contracts.Generator.
func (r *Router) GenerateStream(ctx context.Context, req contracts.GenerateRequest, emit func(contracts.GenerationEvent) error) (contracts.GenerationResult, error) {
	slot := req.ModelSlot
	if slot == "" {
		slot = contracts.SlotMain
		req.ModelSlot = slot
	}

	r.mu.Lock()
	resolved, _, err := r.resolveLocked(ctx, slot)
	r.mu.Unlock()
	if err != nil {
		return contracts.GenerationResult{}, err
	}
	if req.Model == "" {
		req.Model = r.modelForSlot(slot)
	}
	return resolved.adapter.Generate(ctx, req, emit)
}

// Models returns one adapter's discovered models. Configured full IDs are
// retained as a documented fallback when a CLI exposes no JSON list command.
func (r *Router) Models(ctx context.Context, kind Kind) ([]ModelRef, error) {
	r.mu.Lock()
	adapter, ok := r.adapters[kind]
	r.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("host adapter %q is not registered", kind)
	}
	models, err := adapter.Models(ctx)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]ModelRef, len(models)+2)
	for _, model := range models {
		if model.ID != "" {
			byID[model.ID] = model
		}
	}
	for _, id := range []string{r.cfg.DefaultMainModel, r.cfg.DefaultJudgeModel} {
		if id != "" {
			if _, exists := byID[id]; !exists {
				byID[id] = ModelRef{ID: id, Label: id}
			}
		}
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	result := make([]ModelRef, 0, len(ids))
	for _, id := range ids {
		result = append(result, byID[id])
	}
	return result, nil
}

// SkipReasons returns a copy of readiness failures observed by this router.
func (r *Router) SkipReasons() []SkipReason {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]SkipReason(nil), r.reasons...)
}

func (r *Router) resolveLocked(ctx context.Context, slot contracts.ModelSlot) (resolvedAdapter, bool, error) {
	if slot == contracts.SlotMain {
		if r.main != nil {
			return *r.main, false, nil
		}
		resolved, err := r.selectCandidatesLocked(ctx, slot, r.mainCandidates())
		if err != nil {
			return resolvedAdapter{}, false, err
		}
		r.main = &resolved
		return resolved, false, nil
	}

	if r.judge != nil {
		return *r.judge, r.judgeSharesMainModel(), nil
	}
	if r.cfg.JudgeHost == "" {
		main, _, err := r.resolveLocked(ctx, contracts.SlotMain)
		if err != nil {
			return resolvedAdapter{}, false, err
		}
		r.judge = &main
		return main, r.judgeSharesMainModel(), nil
	}
	resolved, err := r.selectCandidatesLocked(ctx, slot, r.judgeCandidates())
	if err != nil {
		return resolvedAdapter{}, false, err
	}
	r.judge = &resolved
	return resolved, false, nil
}

func (r *Router) selectCandidatesLocked(ctx context.Context, slot contracts.ModelSlot, candidates []Kind) (resolvedAdapter, error) {
	var attempts []SkipReason
	skip := func(kind Kind, probe, reason string) {
		attempts = append(attempts, SkipReason{Kind: kind, Probe: probe, Reason: reason})
		r.recordSkip(kind, probe, reason)
	}
	for _, kind := range uniqueKinds(candidates) {
		adapter, ok := r.adapters[kind]
		if !ok {
			skip(kind, "detect", "adapter is not registered")
			continue
		}
		if ready, ok := r.ready[kind]; ok {
			return ready, nil
		}
		version, err := adapter.Detect(ctx)
		if err != nil {
			skip(kind, "detect", err.Error())
			continue
		}
		auth, err := adapter.Auth(ctx)
		if err != nil {
			skip(kind, "auth", err.Error())
			continue
		}
		if !auth.Ready {
			reason := auth.Detail
			if reason == "" {
				reason = "not authenticated"
			}
			skip(kind, "auth", reason)
			continue
		}
		if err := adapter.Preflight(ctx); err != nil {
			skip(kind, "preflight", err.Error())
			continue
		}
		ready := resolvedAdapter{adapter: adapter, version: version}
		r.ready[kind] = ready
		return ready, nil
	}
	return resolvedAdapter{}, &NoHostError{Slot: slot, Reasons: attempts}
}

func (r *Router) mainCandidates() []Kind {
	return []Kind{Kind(r.cfg.MainHost), r.invokedHost, KindClaude, KindOMP, KindAGY}
}

func (r *Router) judgeCandidates() []Kind {
	return []Kind{Kind(r.cfg.JudgeHost), r.invokedHost, KindClaude, KindOMP, KindAGY}
}

func (r *Router) modelForSlot(slot contracts.ModelSlot) string {
	if slot == contracts.SlotJudge {
		if r.cfg.DefaultJudgeModel != "" {
			return r.cfg.DefaultJudgeModel
		}
		return r.cfg.DefaultMainModel
	}
	return r.cfg.DefaultMainModel
}

func (r *Router) judgeSharesMainModel() bool {
	return r.cfg.JudgeHost == "" &&
		(r.cfg.DefaultJudgeModel == "" || r.cfg.DefaultJudgeModel == r.cfg.DefaultMainModel)
}

func (r *Router) recordSkip(kind Kind, probe, reason string) {
	if kind == "" || kind == "auto" {
		return
	}
	candidate := SkipReason{Kind: kind, Probe: probe, Reason: reason}
	for _, existing := range r.reasons {
		if existing == candidate {
			return
		}
	}
	r.reasons = append(r.reasons, candidate)
}

func uniqueKinds(input []Kind) []Kind {
	seen := make(map[Kind]bool)
	result := make([]Kind, 0, len(input))
	for _, kind := range input {
		if kind == "" || kind == "auto" || seen[kind] {
			continue
		}
		seen[kind] = true
		result = append(result, kind)
	}
	return result
}
