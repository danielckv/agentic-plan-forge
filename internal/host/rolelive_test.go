//go:build client && !server

package host

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/config"
	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// liveRoleTimeout bounds one probe call. The payload is two tokens, so a host
// that has not answered in a minute is not going to.
const liveRoleTimeout = 60 * time.Second

// TestLiveEveryRoleReachesAHost sends one trivial call per avatar role through
// the real router and the installed hosts, and fails if any role cannot get an
// answer from anywhere.
//
// It exists because unit tests could not see the fault it covers. On
// 2026-10-25 this installation had all three hosts signed in and `doctor`
// called every one of them ready, yet the critic and the specialist failed
// every run: AGY passes Auth but not Preflight, so `main_host = agy` was
// dropped in silence, and OMP passes Preflight and only then advertises a
// tools list, which breaks the tool-free contract once the call is already
// open. Nothing below the live call can observe either.
//
// Skipped unless PLAN_FORGE_LIVE_ROLES=1: it spends host quota.
func TestLiveEveryRoleReachesAHost(t *testing.T) {
	if os.Getenv("PLAN_FORGE_LIVE_ROLES") != "1" {
		t.Skip("set PLAN_FORGE_LIVE_ROLES=1 to call the installed hosts")
	}
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	t.Logf("main_host=%q judge_host=%q", cfg.MainHost, cfg.JudgeHost)

	roles := []struct {
		role contracts.RoleID
		slot contracts.ModelSlot
	}{
		{contracts.RoleProductManager, contracts.SlotMain},
		{contracts.RoleArchitect, contracts.SlotMain},
		{contracts.RoleSpecialist, contracts.SlotMain},
		{contracts.RoleCritic, contracts.SlotJudge},
		{contracts.RoleSynthesizer, contracts.SlotMain},
		{contracts.RoleNarrator, contracts.SlotMain},
	}
	// One router for the whole table, exactly as a run uses it: a host demoted
	// by the first role that meets it must stay demoted for the rest.
	router := NewRouter(cfg, KindClaude)
	for _, probe := range roles {
		ctx, cancel := context.WithTimeout(context.Background(), liveRoleTimeout)
		res, err := router.GenerateStream(ctx, contracts.GenerateRequest{
			Role:         probe.role,
			ModelSlot:    probe.slot,
			SystemPrompt: `Reply with exactly one JSON object: {"ok":true}. No fences, no commentary.`,
			UserPayload:  json.RawMessage(`{"ping":1}`),
		}, nil)
		cancel()
		if err != nil {
			t.Errorf("%s (%s) reached no host: %v", probe.role, probe.slot, err)
			continue
		}
		t.Logf("%-12s %-5s answered from %s", probe.role, probe.slot, res.Receipt.Host)
	}
	for _, reason := range router.SkipReasons() {
		t.Logf("skipped %s at %s: %s", reason.Kind, reason.Probe, reason.Reason)
	}
}
