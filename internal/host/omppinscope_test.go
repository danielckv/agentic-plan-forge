//go:build client && !server

package host

import (
	"context"
	"testing"

	"github.com/danielckv/agentic-plan-forge/internal/config"
	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// A pin names one role. oh-my-pi must serve that role and nothing else, even
// when the invoking host is oh-my-pi itself — which is the common case, because
// the hook runs inside oh-my-pi and r.invokedHost is KindOMP for every role.
//
// Before the scope fix, ompPinned() was global: any single pin unlocked omp for
// every role, so the architect's pin leaked the synthesizer onto the CLI.
func TestOMPPinDoesNotLeakToUnpinnedRoles(t *testing.T) {
	r := ompRouter(map[Kind]Adapter{
		KindClaude: &routingAdapter{kind: KindClaude, version: "2.1.0", ready: true},
	})
	r.invokedHost = KindOMP
	r.cfg.RoleModels = map[string]config.RoleBinding{"architect": {Host: string(KindOMP)}}

	architect, err := r.PlaceRole(context.Background(), contracts.RoleArchitect, "lane-1", contracts.SlotMain)
	if err != nil {
		t.Fatalf("the pinned architect must be placed on omp: %v", err)
	}
	if architect.Kind != KindOMP {
		t.Fatalf("architect placed on %s, want omp", architect.Kind)
	}

	// Every other role is unpinned: it must land on a real default host, never
	// on the CLI the pin was scoped to.
	for _, role := range []contracts.RoleID{
		contracts.RoleProductManager,
		contracts.RoleSpecialist,
		contracts.RoleSynthesizer,
		contracts.RoleCritic,
	} {
		got, err := r.PlaceRole(context.Background(), role, "lane-1", contracts.SlotMain)
		if err != nil {
			t.Fatalf("%s: %v", role, err)
		}
		if got.Kind == KindOMP {
			t.Errorf("%s placed on omp; only the pinned role may use it", role)
		}
	}
}

// The same leak through the readiness cache: a pinned placement seeds
// r.ready[KindOMP], and an unpinned role that reaches omp later in the walk
// used to take the cached adapter without consulting the pin.
func TestOMPPinDoesNotLeakThroughTheReadyCache(t *testing.T) {
	r := ompRouter(map[Kind]Adapter{
		KindClaude: &routingAdapter{kind: KindClaude, version: "2.1.0", ready: true},
	})
	r.invokedHost = KindOMP
	r.cfg.RoleModels = map[string]config.RoleBinding{"architect": {Host: string(KindOMP)}}

	if _, err := r.PlaceRole(context.Background(), contracts.RoleArchitect, "lane-1", contracts.SlotMain); err != nil {
		t.Fatalf("pinned architect: %v", err)
	}
	if _, ok := r.ready[KindOMP]; !ok {
		t.Fatal("the pinned placement should have cached omp readiness; the test would not exercise the path")
	}

	got, err := r.PlaceRole(context.Background(), contracts.RoleSynthesizer, "lane-1", contracts.SlotMain)
	if err != nil {
		t.Fatalf("synthesizer: %v", err)
	}
	if got.Kind == KindOMP {
		t.Fatalf("synthesizer took omp from the readiness cache; the pin is scoped to the architect")
	}
}

// A main_host pin is slot-wide, so it does cover every role: that is the
// owner turning the spread off, not a role pin leaking.
func TestMainHostPinCoversEveryRole(t *testing.T) {
	r := ompRouter(map[Kind]Adapter{
		KindClaude: &routingAdapter{kind: KindClaude, version: "2.1.0", ready: true},
	})
	r.cfg.MainHost = string(KindOMP)
	for _, role := range []contracts.RoleID{contracts.RoleArchitect, contracts.RoleSpecialist, contracts.RoleSynthesizer} {
		got, err := r.PlaceRole(context.Background(), role, "lane-1", contracts.SlotMain)
		if err != nil {
			t.Fatalf("%s: %v", role, err)
		}
		if got.Kind != KindOMP {
			t.Errorf("%s placed on %s, want omp: a main_host pin is slot-wide", role, got.Kind)
		}
	}
}
