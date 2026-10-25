//go:build client && !server

package host

import (
	"context"
	"errors"
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

// roleHostOrder is the order each avatar role tries the adapters in. The
// orders are rotations of each other, so an installation signed in to more
// than one host spreads its roles across them instead of sending every call to
// one: one host's rate limit then slows one role rather than the whole run,
// and the critic reviews on a different host than the architect drafted on.
//
// A role walks its order until a host is ready, so an installation signed in
// to one host still works and says in its skip reasons what it fell back from.
// An explicit main_host or judge_host overrides the order entirely; a
// role_models entry overrides it for that role alone.
var roleHostOrder = map[contracts.RoleID][]Kind{
	contracts.RoleProductManager: {KindClaude, KindOMP, KindAGY},
	contracts.RoleArchitect:      {KindClaude, KindAGY, KindOMP},
	contracts.RoleSpecialist:     {KindOMP, KindAGY, KindClaude},
	contracts.RoleCritic:         {KindAGY, KindOMP, KindClaude},
	contracts.RoleSynthesizer:    {KindClaude, KindOMP, KindAGY},
	contracts.RoleNarrator:       {KindAGY, KindOMP, KindClaude},
	contracts.RoleIngestion:      {KindClaude, KindOMP, KindAGY},
	contracts.RoleImpact:         {KindOMP, KindClaude, KindAGY},
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

	mu sync.Mutex
	// unusable bars a host that broke the tool-free contract on an open call.
	// Detect, Auth and Preflight all passed; only the stream showed the fault.
	unusable map[Kind]bool
	ready    map[Kind]resolvedAdapter
	main     *resolvedAdapter
	judge    *resolvedAdapter
	byRole   map[contracts.RoleID]Placement
	models   map[Kind]map[string]bool
	reasons  []SkipReason
}

// Placement is the host, version and model one role's calls go to.
type Placement struct {
	adapter Adapter
	Kind    Kind
	Version string
	Model   string
}

var _ contracts.Generator = (*Router)(nil)

// NewRouter snapshots the registry. invokedHost is the --host value supplied by
// the invoking skill; an empty value means no invocation preference.
func NewRouter(cfg config.Config, invokedHost Kind) *Router {
	return &Router{
		cfg:         cfg,
		invokedHost: invokedHost,
		adapters:    registeredMap(cfg.HostEnvAllow),
		unusable:    make(map[Kind]bool),
		ready:       make(map[Kind]resolvedAdapter),
		byRole:      make(map[contracts.RoleID]Placement),
		models:      make(map[Kind]map[string]bool),
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

// GenerateStream implements contracts.Generator. It routes by role, so the
// avatars of one run are served by every host the installation is signed in
// to, and reports the placement before any output arrives.
func (r *Router) GenerateStream(ctx context.Context, req contracts.GenerateRequest, emit func(contracts.GenerationEvent) error) (contracts.GenerationResult, error) {
	slot := req.ModelSlot
	if slot == "" {
		slot = contracts.SlotMain
		req.ModelSlot = slot
	}
	// A host can pass Detect, Auth and Preflight and still break the tool-free
	// contract on the first streamed event: OMP advertises a tools list that
	// only shows up once a call is open. Preflight cannot see that, so the
	// placement is retried on the next candidate instead of failing the stage.
	// Without this a single broken host kills every role whose rotation starts
	// there, which is how the critic and the Product Manager both died on OMP.
	wanted := req.Model
	for attempt := 0; ; attempt++ {
		place, err := r.PlaceRole(ctx, req.Role, slot)
		if err != nil {
			return contracts.GenerationResult{}, err
		}
		req.Model = wanted
		if req.Model == "" {
			req.Model = place.Model
		}
		if emit != nil {
			err := emit(contracts.GenerationEvent{Kind: contracts.EventPlacement, Placement: &contracts.Placement{
				Role: req.Role, Host: string(place.Kind), Version: place.Version, Model: req.Model,
			}})
			if err != nil {
				return contracts.GenerationResult{}, err
			}
		}
		result, err := place.adapter.Generate(ctx, req, emit)
		if err == nil || attempt >= len(r.adapters) {
			return result, err
		}
		var capability *contracts.ErrCapability
		if !errors.As(err, &capability) || !r.demote(place.Kind, capability) {
			return result, err
		}
	}
}

// demote drops a host that broke the tool-free contract mid-call. Every role
// placed on it is unplaced so the next call re-runs the rotation, and the host
// is barred for the rest of the session: the same call would break the same
// way. It reports whether another candidate is left to try.
func (r *Router) demote(kind Kind, cause *contracts.ErrCapability) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.unusable[kind] {
		return false
	}
	if r.unusable == nil {
		r.unusable = make(map[Kind]bool, len(r.adapters))
	}
	r.unusable[kind] = true
	r.recordSkip(kind, "generate", cause.Reason)
	delete(r.ready, kind)
	for role, placed := range r.byRole {
		if placed.Kind == kind {
			delete(r.byRole, role)
		}
	}
	if r.main != nil && r.main.adapter.Kind() == kind {
		r.main = nil
	}
	if r.judge != nil && r.judge.adapter.Kind() == kind {
		r.judge = nil
	}
	for candidate := range r.adapters {
		if !r.unusable[candidate] {
			return true
		}
	}
	return false
}

// PlaceRole resolves the host and model one role's calls use. The answer is
// cached per role, so every call of a role in a session lands on the same host
// and the room never shows an avatar changing hosts mid-cycle.
func (r *Router) PlaceRole(ctx context.Context, role contracts.RoleID, slot contracts.ModelSlot) (Placement, error) {
	if slot == "" {
		slot = contracts.SlotMain
	}
	if slot != contracts.SlotMain && slot != contracts.SlotJudge {
		return Placement{}, fmt.Errorf("unknown model slot %q", slot)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if p, ok := r.byRole[role]; ok {
		return p, nil
	}
	resolved, err := r.selectCandidatesLocked(ctx, slot, r.roleCandidates(role, slot))
	if err != nil {
		return Placement{}, err
	}
	p := Placement{
		adapter: resolved.adapter,
		Kind:    resolved.adapter.Kind(),
		Version: resolved.version,
	}
	p.Model = r.modelForRoleLocked(ctx, role, slot, p.Kind)
	r.byRole[role] = p
	return p, nil
}

// Placements reports the host and model resolved for every role so far,
// keyed by role. It never resolves a new one.
func (r *Router) Placements() map[contracts.RoleID]Placement {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[contracts.RoleID]Placement, len(r.byRole))
	for role, p := range r.byRole {
		out[role] = p
	}
	return out
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
		if r.unusable[kind] {
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

// roleCandidates orders the adapters one role tries. A role_models host comes
// first, then an explicitly configured slot host, then the role's own
// rotation, then everything else. An owner who pins main_host still gets one
// host for every MAIN role; an owner who pins nothing gets the rotation, which
// is what spreads a run across the hosts it is signed in to.
func (r *Router) roleCandidates(role contracts.RoleID, slot contracts.ModelSlot) []Kind {
	var out []Kind
	if b, ok := r.cfg.RoleBindingFor(string(role)); ok {
		out = append(out, Kind(b.Host))
	}
	if slot == contracts.SlotJudge {
		out = append(out, Kind(r.cfg.JudgeHost))
	} else {
		out = append(out, Kind(r.cfg.MainHost))
	}
	out = append(out, roleHostOrder[role]...)
	return append(out, r.invokedHost, KindClaude, KindOMP, KindAGY)
}

// modelForRoleLocked picks the model id for a role on the host that will serve
// it. An explicit role_models model wins. Otherwise the configured slot model
// applies only to a host that actually lists it: "opus" means nothing to OMP,
// and sending it there is the 404 ADR 0004 traced. A host that lists no match
// is left to its own default.
func (r *Router) modelForRoleLocked(ctx context.Context, role contracts.RoleID, slot contracts.ModelSlot, kind Kind) string {
	if b, ok := r.cfg.RoleBindingFor(string(role)); ok && b.Model != "" {
		return b.Model
	}
	want := r.modelForSlot(slot)
	if want == "" || r.hostListsModelLocked(ctx, kind, want) {
		return want
	}
	return ""
}

// hostListsModelLocked reports whether a host advertises a model id. A host
// that cannot list its models answers true: the configured id is then the only
// evidence available, and refusing it would be worse than trying it.
func (r *Router) hostListsModelLocked(ctx context.Context, kind Kind, id string) bool {
	known, cached := r.models[kind]
	if !cached {
		adapter, ok := r.adapters[kind]
		if !ok {
			return true
		}
		models, err := adapter.Models(ctx)
		if err != nil || len(models) == 0 {
			r.models[kind] = nil
			return true
		}
		known = make(map[string]bool, len(models))
		for _, m := range models {
			known[m.ID] = true
		}
		r.models[kind] = known
	}
	if known == nil {
		return true
	}
	return known[id]
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
