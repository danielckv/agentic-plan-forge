//go:build client && !server

package host

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/danielckv/agentic-plan-forge/internal/config"
	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// tieredRouter is three ready hosts advertising the ids tierModels names, so
// placement is observed against the real table rather than a stand-in.
func tieredRouter(cfg config.Config) *Router {
	return &Router{
		cfg: cfg,
		adapters: map[Kind]Adapter{
			KindAnthropic: &modelAdapter{
				routingAdapter: routingAdapter{kind: KindAnthropic, version: anthropicLabel, ready: true},
				models:         []string{"claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5"},
			},
			KindGemini: &modelAdapter{
				routingAdapter: routingAdapter{kind: KindGemini, version: geminiWireLabel, ready: true},
				models:         []string{"gemini-3.1-pro-preview", "gemini-3-flash-preview"},
			},
			KindClaude: &modelAdapter{
				routingAdapter: routingAdapter{kind: KindClaude, version: "2.1.0", ready: true},
				models:         []string{"opus", "sonnet", "haiku"},
			},
		},
		ready:  make(map[Kind]resolvedAdapter),
		byRole: make(map[RoleLane]Placement),
		models: make(map[Kind]map[string]bool),
	}
}

// Every avatar gets a model Plan Forge named. Before this, an unpinned role
// sent no id and took the host's own choice, which on oh-my-pi meant the
// owner's `modelRoles` map: a run was observed drafting on a model that map
// names for widgets.
func TestEveryRoleIsPlacedOnANamedModel(t *testing.T) {
	r := tieredRouter(config.Config{})
	for _, role := range []contracts.RoleID{
		contracts.RoleProductManager, contracts.RoleArchitect, contracts.RoleSpecialist,
		contracts.RoleCritic, contracts.RoleSynthesizer, contracts.RoleIngestion,
	} {
		p, err := r.PlaceRole(context.Background(), role, "", contracts.SlotMain)
		if err != nil {
			t.Fatalf("%s: %v", role, err)
		}
		if p.Model == "" {
			t.Errorf("%s was placed on %s with no model named", role, p.Kind)
		}
	}
}

// The reasoning roles are never handed a light model to spread load. ADR 0009
// settled that: load spreads by role, never by downgrading the model that does
// the designing.
func TestReasoningRolesAskForTheStrongModel(t *testing.T) {
	for _, role := range []contracts.RoleID{
		contracts.RoleArchitect, contracts.RoleSynthesizer,
		contracts.RoleProductManager, contracts.RoleTechLead, contracts.RoleIngestion,
	} {
		if got := tierFor(role); got != tierStrong {
			t.Errorf("%s is tier %q, want %q", role, got, tierStrong)
		}
	}
	if got := tierFor(contracts.RoleNarrator); got != tierLight {
		t.Errorf("narrator is tier %q, want %q: it writes a status line that gates nothing", got, tierLight)
	}
	// A role nobody classified is drafting work, not a status line.
	if got := tierFor("a_role_added_later"); got != tierStandard {
		t.Errorf("an unknown role is tier %q, want %q", got, tierStandard)
	}
}

// The strong tier asks for an opus or argon model first on every host that
// has one. This is the owner's rule: the plan is written by Claude or Gemini's
// strongest, never by whatever answered.
func TestStrongTierLeadsWithOpusOrArgon(t *testing.T) {
	for kind, tiers := range tierModels {
		strong := tiers[tierStrong]
		if len(strong) == 0 {
			t.Errorf("%s names no strong model", kind)
			continue
		}
		first := strong[0]
		if !strings.Contains(first, "opus") && !strings.Contains(first, "argon") {
			t.Errorf("%s leads its strong tier with %q, want an opus or argon model", kind, first)
		}
		for _, tier := range []modelTier{tierStandard, tierLight} {
			if len(tiers[tier]) == 0 {
				t.Errorf("%s names no %s model", kind, tier)
			}
		}
	}
}

// oh-my-pi runs Plan Forge; it never serves it. It has no tier entry, so even
// a configuration that reached the router naming it gets nothing here.
func TestOMPHasNoDefaultModels(t *testing.T) {
	if _, ok := tierModels[KindOMP]; ok {
		t.Error("oh-my-pi must not appear in the model table: it does not generate")
	}
	if got := defaultModelsFor(KindOMP, contracts.RoleArchitect); len(got) != 0 {
		t.Errorf("defaultModelsFor(omp, architect) = %v, want none", got)
	}
}

// No model serves more than two avatars in one run. Four specialists on one
// model are one model's blind spots four times over.
func TestNoModelServesMoreThanTwoAvatars(t *testing.T) {
	r := tieredRouter(config.Config{})
	used := map[string]int{}
	for _, lane := range []string{"billing-api", "web-ui", "ws-docs", "search-index", "ws-auth", "ws-reports"} {
		p, err := r.PlaceRole(context.Background(), contracts.RoleSpecialist, lane, contracts.SlotMain)
		if err != nil {
			t.Fatal(err)
		}
		used[fmt.Sprintf("%s/%s", p.Kind, p.Model)]++
	}
	for pair, n := range used {
		if n > MaxAvatarsPerModel {
			t.Errorf("%s serves %d avatars, cap is %d (all placements: %v)", pair, n, MaxAvatarsPerModel, used)
		}
	}
	if len(used) < 3 {
		t.Errorf("six specialists landed on %d model(s): %v", len(used), used)
	}
}

// A temporary agent is throttled with the permanent ones: it is another
// avatar, and the cap counts avatars.
func TestTemporaryAgentsShareTheCap(t *testing.T) {
	r := tieredRouter(config.Config{})
	used := map[string]int{}
	for _, lane := range []string{"gap:security", "gap:perf", "gap:data", "gap:infra"} {
		p, err := r.PlaceRole(context.Background(), contracts.RoleGapAgent, lane, contracts.SlotMain)
		if err != nil {
			t.Fatal(err)
		}
		used[fmt.Sprintf("%s/%s", p.Kind, p.Model)]++
	}
	for pair, n := range used {
		if n > MaxAvatarsPerModel {
			t.Errorf("%s serves %d temporary agents, cap is %d", pair, n, MaxAvatarsPerModel)
		}
	}
}

// The cap spreads what Plan Forge chose. It never overrides what the owner
// chose: a pinned role uses its pin for every avatar of that role.
func TestTheCapNeverOverridesAPin(t *testing.T) {
	r := tieredRouter(config.Config{RoleModels: map[string]config.RoleBinding{
		"specialist": {Host: "gemini", Model: "gemini-3.1-pro-preview"},
	}})
	for _, lane := range []string{"a", "b", "c", "d", "e"} {
		p, err := r.PlaceRole(context.Background(), contracts.RoleSpecialist, lane, contracts.SlotMain)
		if err != nil {
			t.Fatal(err)
		}
		if p.Kind != KindGemini || p.Model != "gemini-3.1-pro-preview" {
			t.Fatalf("lane %s = %s/%s, want the pin", lane, p.Kind, p.Model)
		}
	}
}

// A run that cannot spread still runs. One model serving everything is worse
// than a spread run and better than no plan.
func TestOneHostStillPlacesEveryAvatar(t *testing.T) {
	r := &Router{
		cfg: config.Config{},
		adapters: map[Kind]Adapter{
			KindAnthropic: &modelAdapter{
				routingAdapter: routingAdapter{kind: KindAnthropic, version: anthropicLabel, ready: true},
				models:         []string{"claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5"},
			},
		},
		ready:  make(map[Kind]resolvedAdapter),
		byRole: make(map[RoleLane]Placement),
		models: make(map[Kind]map[string]bool),
	}
	for _, lane := range []string{"a", "b", "c", "d"} {
		if _, err := r.PlaceRole(context.Background(), contracts.RoleSpecialist, lane, contracts.SlotMain); err != nil {
			t.Fatalf("lane %s: %v", lane, err)
		}
	}
}
