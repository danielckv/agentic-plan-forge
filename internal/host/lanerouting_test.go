//go:build client && !server

package host

import (
	"context"
	"testing"

	"github.com/danielckv/agentic-plan-forge/internal/config"
	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// Four Domain Specialists draft at the same time. Placing them all on one host
// puts four concurrent calls behind one rate limit, which is the thing being
// signed in to three hosts is supposed to avoid.
func TestSpecialistLanesSpreadAcrossReadyHosts(t *testing.T) {
	r := readyRouter(t, config.Config{})
	lanes := []string{"billing-api", "web-ui", "ws-docs", "search-index"}
	used := map[Kind]int{}
	for _, lane := range lanes {
		p, err := r.PlaceRole(context.Background(), contracts.RoleSpecialist, lane, contracts.SlotMain)
		if err != nil {
			t.Fatalf("%s: %v", lane, err)
		}
		used[p.Kind]++
	}
	if len(used) < 2 {
		t.Fatalf("four specialist lanes landed on %d host(s): %v", len(used), used)
	}
}

// A lane keeps its host. The room must never show one specialist changing
// hosts between its draft and its revision.
func TestSpecialistLaneKeepsItsHost(t *testing.T) {
	r := readyRouter(t, config.Config{})
	first, err := r.PlaceRole(context.Background(), contracts.RoleSpecialist, "ws-docs", contracts.SlotMain)
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.PlaceRole(context.Background(), contracts.RoleSpecialist, "ws-docs", contracts.SlotMain)
	if err != nil {
		t.Fatal(err)
	}
	if first.Kind != second.Kind || first.Model != second.Model {
		t.Fatalf("lane ws-docs moved from %s/%s to %s/%s", first.Kind, first.Model, second.Kind, second.Model)
	}
	other, err := r.PlaceRole(context.Background(), contracts.RoleSpecialist, "billing-api", contracts.SlotMain)
	if err != nil {
		t.Fatal(err)
	}
	key := RoleLane{Role: contracts.RoleSpecialist, Lane: "billing-api"}
	if got := r.Placements()[key]; got.Kind != other.Kind {
		t.Fatalf("Placements reports %s for billing-api, want %s", got.Kind, other.Kind)
	}
}

// Pinning the role pins every lane of it. The spread is what happens when the
// owner chose nothing, never an override of what they did choose.
func TestPinnedSpecialistHostCoversEveryLane(t *testing.T) {
	r := readyRouter(t, config.Config{RoleModels: map[string]config.RoleBinding{
		"specialist": {Host: "omp", Model: "claude-opus-5-5"},
	}})
	for _, lane := range []string{"billing-api", "web-ui", "ws-docs"} {
		p, err := r.PlaceRole(context.Background(), contracts.RoleSpecialist, lane, contracts.SlotMain)
		if err != nil {
			t.Fatal(err)
		}
		if p.Kind != KindOMP || p.Model != "claude-opus-5-5" {
			t.Errorf("lane %s = %s/%s, want omp/claude-opus-5-5", lane, p.Kind, p.Model)
		}
	}
}

// A lane's host is derived from its name, not from the order the lanes were
// placed in. A retry that re-places one lane, or a resumed run that places
// them in a different order, must reach the same host as the first run.
func TestLanePlacementDoesNotDependOnOrder(t *testing.T) {
	lanes := []string{"billing-api", "web-ui", "ws-docs", "search-index"}
	first := map[string]Kind{}
	r := readyRouter(t, config.Config{})
	for _, lane := range lanes {
		p, err := r.PlaceRole(context.Background(), contracts.RoleSpecialist, lane, contracts.SlotMain)
		if err != nil {
			t.Fatal(err)
		}
		first[lane] = p.Kind
	}
	reversed := readyRouter(t, config.Config{})
	for i := len(lanes) - 1; i >= 0; i-- {
		p, err := reversed.PlaceRole(context.Background(), contracts.RoleSpecialist, lanes[i], contracts.SlotMain)
		if err != nil {
			t.Fatal(err)
		}
		if p.Kind != first[lanes[i]] {
			t.Errorf("lane %s placed on %s, want %s", lanes[i], p.Kind, first[lanes[i]])
		}
	}
}
