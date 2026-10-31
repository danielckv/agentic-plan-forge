//go:build client && !server

package host

import (
	"context"
	"errors"
	"testing"

	"github.com/danielckv/agentic-plan-forge/internal/config"
	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// ompRouter builds a router with one ready OMP adapter beside whatever the
// test gives it, so the opt-in rule can be observed directly.
func ompRouter(others map[Kind]Adapter) *Router {
	adapters := map[Kind]Adapter{
		KindOMP: &routingAdapter{kind: KindOMP, version: "0.5.0", ready: true},
	}
	for kind, adapter := range others {
		adapters[kind] = adapter
	}
	return &Router{
		cfg:      config.Config{},
		adapters: adapters,
		ready:    make(map[Kind]resolvedAdapter),
		byRole:   make(map[RoleLane]Placement),
		models:   make(map[Kind]map[string]bool),
	}
}

// Without a pin, oh-my-pi never serves. Claude signed out and no provider key
// is an installation that cannot generate: it reports that, and the skip
// reason names the opt-in instead of silently paying a CLI's scaffolding.
func TestOMPIsNotAFallbackWhenNothingElseCanServe(t *testing.T) {
	r := ompRouter(map[Kind]Adapter{
		KindClaude: &routingAdapter{kind: KindClaude, version: "2.1.0", ready: false},
		KindGemini: &routingAdapter{kind: KindGemini, version: geminiWireLabel, ready: false, detectErr: errors.New("no key")},
	})
	omp := r.adapters[KindOMP].(*routingAdapter)
	_, err := r.Resolve(context.Background(), contracts.SlotMain)
	if err == nil {
		t.Fatal("an omp-only installation without a pin must report no usable host")
	}
	if omp.detectCalls != 0 || omp.preflightCalls != 0 {
		t.Fatalf("omp was probed (detect=%d preflight=%d); it must not be a candidate without a pin",
			omp.detectCalls, omp.preflightCalls)
	}
}

// A main_host pin is the owner wanting oh-my-pi, so it serves even when
// claude and gemini are available and ready.
func TestOMPServesWhenPinnedAsMainHost(t *testing.T) {
	r := ompRouter(map[Kind]Adapter{
		KindClaude: &routingAdapter{kind: KindClaude, version: "2.1.0", ready: true},
		KindGemini: &routingAdapter{kind: KindGemini, version: geminiWireLabel, ready: true},
	})
	r.cfg.MainHost = string(KindOMP)
	res, err := r.Resolve(context.Background(), contracts.SlotMain)
	if err != nil {
		t.Fatalf("an explicit main_host pin must be honored: %v", err)
	}
	if res.Kind != KindOMP {
		t.Fatalf("resolved host = %s, want omp", res.Kind)
	}
}

// A role_models pin is the same statement for one role: omp serves that role,
// and the scope tests below prove it does not leak to the others.
func TestOMPServesAPinnedRole(t *testing.T) {
	r := ompRouter(map[Kind]Adapter{
		KindClaude: &routingAdapter{kind: KindClaude, version: "2.1.0", ready: true},
	})
	r.cfg.RoleModels = map[string]config.RoleBinding{"architect": {Host: string(KindOMP)}}
	placement, err := r.PlaceRole(context.Background(), contracts.RoleArchitect, "lane-1", contracts.SlotMain)
	if err != nil {
		t.Fatalf("a role_models omp pin must be honored: %v", err)
	}
	if placement.Kind != KindOMP {
		t.Fatalf("placed host = %s, want omp", placement.Kind)
	}
}
