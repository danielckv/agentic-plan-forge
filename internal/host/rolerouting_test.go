//go:build client && !server

package host

import (
	"context"
	"errors"
	"testing"

	"github.com/danielckv/agentic-plan-forge/internal/config"
	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// modelAdapter is a routingAdapter that advertises a model list, so the
// router can be asked whether a configured model belongs to a host.
type modelAdapter struct {
	routingAdapter
	models []string
}

func (a *modelAdapter) Models(context.Context) ([]ModelRef, error) {
	out := make([]ModelRef, 0, len(a.models))
	for _, id := range a.models {
		out = append(out, ModelRef{ID: id, Label: id})
	}
	return out, nil
}

func readyRouter(t *testing.T, cfg config.Config) *Router {
	t.Helper()
	return &Router{
		cfg: cfg,
		adapters: map[Kind]Adapter{
			KindClaude: &modelAdapter{
				routingAdapter: routingAdapter{kind: KindClaude, version: "2.1.0", ready: true},
				models:         []string{"haiku", "opus", "sonnet"},
			},
			KindOMP: &modelAdapter{
				routingAdapter: routingAdapter{kind: KindOMP, version: "18.0.0", ready: true},
				models:         []string{"claude-sonnet-5-5", "claude-opus-5-5"},
			},
			KindAGY: &modelAdapter{
				routingAdapter: routingAdapter{kind: KindAGY, version: "1.4.0", ready: true},
				models:         []string{"claude-opus-5-5-high", "gemini-3.8-flash-low"},
			},
		},
		ready:  make(map[Kind]resolvedAdapter),
		byRole: make(map[RoleLane]Placement),
		models: make(map[Kind]map[string]bool),
	}
}

// With nothing pinned and every host signed in, one run must not land on one
// host: that is the whole point of being signed in to three.
func TestRolesSpreadAcrossEveryReadyHost(t *testing.T) {
	r := readyRouter(t, config.Config{})
	roles := []contracts.RoleID{
		contracts.RoleProductManager, contracts.RoleArchitect, contracts.RoleSpecialist,
		contracts.RoleCritic, contracts.RoleSynthesizer, contracts.RoleNarrator,
	}
	used := map[Kind]int{}
	for _, role := range roles {
		slot := contracts.SlotMain
		if role == contracts.RoleCritic {
			slot = contracts.SlotJudge
		}
		p, err := r.PlaceRole(context.Background(), role, "", slot)
		if err != nil {
			t.Fatalf("%s: %v", role, err)
		}
		used[p.Kind]++
	}
	for _, kind := range []Kind{KindClaude, KindOMP, KindAGY} {
		if used[kind] == 0 {
			t.Errorf("no role was placed on %s; placements = %v", kind, used)
		}
	}
	// The critic must not review on the host the architect drafted on.
	arch, _ := r.PlaceRole(context.Background(), contracts.RoleArchitect, "", contracts.SlotMain)
	critic, _ := r.PlaceRole(context.Background(), contracts.RoleCritic, "", contracts.SlotJudge)
	if arch.Kind == critic.Kind {
		t.Errorf("critic and architect both on %s; the judge must be a second opinion", arch.Kind)
	}
}

// A placement is cached: an avatar may not change host between the calls of
// one run, or the room shows it moving mid-cycle.
func TestPlacementIsStablePerRole(t *testing.T) {
	r := readyRouter(t, config.Config{})
	first, err := r.PlaceRole(context.Background(), contracts.RoleArchitect, "", contracts.SlotMain)
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.PlaceRole(context.Background(), contracts.RoleArchitect, "", contracts.SlotMain)
	if err != nil {
		t.Fatal(err)
	}
	if first.Kind != second.Kind || first.Model != second.Model {
		t.Fatalf("architect moved from %s/%s to %s/%s", first.Kind, first.Model, second.Kind, second.Model)
	}
	if got := r.Placements()[RoleLane{Role: contracts.RoleArchitect}]; got.Kind != first.Kind {
		t.Fatalf("Placements reports %s, want %s", got.Kind, first.Kind)
	}
}

// An explicit role_models entry wins over the rotation, host and model both.
func TestRoleBindingPinsHostAndModel(t *testing.T) {
	r := readyRouter(t, config.Config{RoleModels: map[string]config.RoleBinding{
		"architect": {Host: "omp", Model: "claude-opus-5-5"},
		"narrator":  {Model: "gemini-3.8-flash-low"},
	}})
	arch, err := r.PlaceRole(context.Background(), contracts.RoleArchitect, "", contracts.SlotMain)
	if err != nil {
		t.Fatal(err)
	}
	if arch.Kind != KindOMP || arch.Model != "claude-opus-5-5" {
		t.Fatalf("architect = %s/%s, want omp/claude-opus-5-5", arch.Kind, arch.Model)
	}
	// A binding with a model but no host keeps the rotation's host.
	narrator, err := r.PlaceRole(context.Background(), contracts.RoleNarrator, "", contracts.SlotMain)
	if err != nil {
		t.Fatal(err)
	}
	if narrator.Kind != KindAGY || narrator.Model != "gemini-3.8-flash-low" {
		t.Fatalf("narrator = %s/%s, want agy/gemini-3.8-flash-low", narrator.Kind, narrator.Model)
	}
}

// An owner who names main_host means it: every MAIN role goes there. The
// spread is what happens when nothing is pinned, not a policy that overrides
// a configured host.
func TestConfiguredMainHostStillForcesOneHost(t *testing.T) {
	r := readyRouter(t, config.Config{MainHost: string(KindOMP)})
	for _, role := range []contracts.RoleID{
		contracts.RoleProductManager, contracts.RoleArchitect, contracts.RoleSpecialist,
	} {
		p, err := r.PlaceRole(context.Background(), role, "", contracts.SlotMain)
		if err != nil {
			t.Fatal(err)
		}
		if p.Kind != KindOMP {
			t.Errorf("%s placed on %s, want the configured omp", role, p.Kind)
		}
	}
}

// "opus" is a Claude Code alias and means nothing to OMP. Sending it there is
// the 404 ADR 0004 traced, so a slot model only applies to a host that lists
// it; every other host is left to its own default.
func TestSlotModelOnlyAppliesToAHostThatListsIt(t *testing.T) {
	r := readyRouter(t, config.Config{DefaultMainModel: "opus"})
	claude, err := r.PlaceRole(context.Background(), contracts.RoleProductManager, "", contracts.SlotMain)
	if err != nil {
		t.Fatal(err)
	}
	if claude.Kind != KindClaude || claude.Model != "opus" {
		t.Fatalf("pm = %s/%s, want claude/opus", claude.Kind, claude.Model)
	}
	spec, err := r.PlaceRole(context.Background(), contracts.RoleSpecialist, "", contracts.SlotMain)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Kind != KindOMP {
		t.Fatalf("specialist placed on %s, want omp", spec.Kind)
	}
	if spec.Model != "" {
		t.Fatalf("specialist model = %q, want the host default: omp does not list \"opus\"", spec.Model)
	}
}

// Signed in to one host only, every role still runs. Spreading is a
// preference, never a prerequisite.
func TestOneReadyHostStillServesEveryRole(t *testing.T) {
	only := &modelAdapter{
		routingAdapter: routingAdapter{kind: KindClaude, version: "2.1.0", ready: true},
		models:         []string{"opus"},
	}
	r := &Router{
		cfg:      config.Config{},
		adapters: map[Kind]Adapter{KindClaude: only},
		ready:    make(map[Kind]resolvedAdapter),
		byRole:   make(map[RoleLane]Placement),
		models:   make(map[Kind]map[string]bool),
	}
	for _, role := range []contracts.RoleID{
		contracts.RoleProductManager, contracts.RoleArchitect, contracts.RoleCritic, contracts.RoleNarrator,
	} {
		slot := contracts.SlotMain
		if role == contracts.RoleCritic {
			slot = contracts.SlotJudge
		}
		p, err := r.PlaceRole(context.Background(), role, "", slot)
		if err != nil {
			t.Fatalf("%s: %v", role, err)
		}
		if p.Kind != KindClaude {
			t.Errorf("%s placed on %s, want the only ready host", role, p.Kind)
		}
	}
}

// Every placement is announced before any output, so the room can name the
// host and model under the face while the call is still running.
func TestGenerateStreamAnnouncesThePlacementFirst(t *testing.T) {
	r := readyRouter(t, config.Config{})
	var kinds []contracts.GenerationEventKind
	var place *contracts.Placement
	_, err := r.GenerateStream(context.Background(),
		contracts.GenerateRequest{Role: contracts.RoleSpecialist, ModelSlot: contracts.SlotMain},
		func(ev contracts.GenerationEvent) error {
			kinds = append(kinds, ev.Kind)
			if ev.Kind == contracts.EventPlacement {
				place = ev.Placement
			}
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if len(kinds) == 0 || kinds[0] != contracts.EventPlacement {
		t.Fatalf("first event = %v, want the placement", kinds)
	}
	if place == nil || place.Host != string(KindOMP) || place.Role != contracts.RoleSpecialist {
		t.Fatalf("placement = %+v, want the specialist on omp", place)
	}
	if place.Version != "18.0.0" {
		t.Fatalf("placement version = %q, want the detected host version", place.Version)
	}
}

// ompTripwire is the error OMP returns once a call is open: Detect, Auth and
// Preflight all pass, and the tools list only arrives with the first streamed
// event. Observed live on 2026-10-25 for the specialist and critic roles.
func ompTripwire() error {
	return &contracts.ErrCapability{
		Host:   string(KindOMP),
		Reason: "host advertised a non-empty or invalid tools list",
		Remedy: "update OMP",
	}
}

// A host that passes every probe and then breaks the tool-free contract on the
// open call must cost one retry, not the stage. Before the failover the critic
// stayed pinned to the broken host and every retry died the same way.
func TestACallFailsOverWhenAHostBreaksTheToolFreeContract(t *testing.T) {
	r := readyRouter(t, config.Config{JudgeHost: string(KindOMP)})
	broken := r.adapters[KindOMP].(*modelAdapter)
	broken.generateErr = ompTripwire()

	res, err := r.GenerateStream(context.Background(), contracts.GenerateRequest{
		Role: contracts.RoleCritic, ModelSlot: contracts.SlotJudge,
	}, nil)
	if err != nil {
		t.Fatalf("critic call: %v", err)
	}
	if res.Receipt.Host == string(KindOMP) {
		t.Fatalf("critic answered from the broken host %s", res.Receipt.Host)
	}
	if broken.generateCalls != 1 {
		t.Fatalf("broken host called %d times, want 1", broken.generateCalls)
	}
}

// The demoted host must stay out of the rotation: a later role that would
// start there walks on instead of paying the same failure again.
func TestADemotedHostIsNotOfferedToTheNextRole(t *testing.T) {
	r := readyRouter(t, config.Config{JudgeHost: string(KindOMP)})
	broken := r.adapters[KindOMP].(*modelAdapter)
	broken.generateErr = ompTripwire()

	if _, err := r.GenerateStream(context.Background(), contracts.GenerateRequest{
		Role: contracts.RoleCritic, ModelSlot: contracts.SlotJudge,
	}, nil); err != nil {
		t.Fatalf("critic call: %v", err)
	}
	// The specialist rotation starts on OMP; it must not be placed there.
	p, err := r.PlaceRole(context.Background(), contracts.RoleSpecialist, "", contracts.SlotMain)
	if err != nil {
		t.Fatalf("specialist placement: %v", err)
	}
	if p.Kind == KindOMP {
		t.Fatalf("specialist placed on the demoted host %s", p.Kind)
	}
	if broken.generateCalls != 1 {
		t.Fatalf("broken host called %d times, want 1", broken.generateCalls)
	}
	var found bool
	for _, reason := range r.SkipReasons() {
		if reason.Kind == KindOMP && reason.Probe == "generate" {
			found = true
		}
	}
	if !found {
		t.Fatal("no generate skip reason recorded for the demoted host")
	}
}

// Every host broken means the caller sees the real capability error, not a
// placement error and not an endless retry.
func TestEveryHostBrokenReportsTheCapabilityError(t *testing.T) {
	r := readyRouter(t, config.Config{})
	for _, kind := range []Kind{KindClaude, KindOMP, KindAGY} {
		r.adapters[kind].(*modelAdapter).generateErr = ompTripwire()
	}
	_, err := r.GenerateStream(context.Background(), contracts.GenerateRequest{
		Role: contracts.RoleCritic, ModelSlot: contracts.SlotJudge,
	}, nil)
	var capability *contracts.ErrCapability
	if !errors.As(err, &capability) {
		t.Fatalf("error is %v, want a capability error", err)
	}
}
