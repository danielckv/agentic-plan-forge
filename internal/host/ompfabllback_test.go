//go:build client && !server

package host

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/danielckv/agentic-plan-forge/internal/config"
	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// ompRouter builds a router with one ready OMP adapter beside whatever the
// test gives it, so the rule can be observed directly: oh-my-pi is a harness
// that runs Plan Forge, never a host that generates for it.
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

// An installation holding nothing but oh-my-pi cannot generate, and says so.
// It is never probed, so it never pays the CLI's start-up either.
func TestOMPIsNotAFallbackWhenNothingElseCanServe(t *testing.T) {
	r := ompRouter(map[Kind]Adapter{
		KindClaude: &routingAdapter{kind: KindClaude, version: "2.1.0", ready: false},
		KindGemini: &routingAdapter{kind: KindGemini, version: geminiWireLabel, ready: false, detectErr: errors.New("no key")},
	})
	omp := r.adapters[KindOMP].(*routingAdapter)
	_, err := r.Resolve(context.Background(), contracts.SlotMain)
	if err == nil {
		t.Fatal("an omp-only installation must report no usable host")
	}
	if omp.detectCalls != 0 || omp.preflightCalls != 0 {
		t.Fatalf("omp was probed (detect=%d preflight=%d); it is never a generation candidate",
			omp.detectCalls, omp.preflightCalls)
	}
}

// A main_host or judge_host naming oh-my-pi cannot reach the router, because
// config validation refuses it. Should one arrive anyway — an older file read
// by a newer binary — the router still walks past it rather than serving.
func TestOMPNeverServesEvenWhenNamed(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(*Router)
		slot contracts.ModelSlot
		role contracts.RoleID
		want Kind
	}{
		{
			name: "main_host",
			set:  func(r *Router) { r.cfg.MainHost = string(KindOMP) },
			slot: contracts.SlotMain,
			want: KindClaude,
		},
		{
			name: "judge_host",
			set:  func(r *Router) { r.cfg.JudgeHost = string(KindOMP) },
			slot: contracts.SlotJudge,
			want: KindClaude,
		},
		{
			name: "role_models",
			set: func(r *Router) {
				r.cfg.RoleModels = map[string]config.RoleBinding{
					"architect": {Host: string(KindOMP)},
				}
			},
			slot: contracts.SlotMain,
			role: contracts.RoleArchitect,
			want: KindClaude,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := ompRouter(map[Kind]Adapter{
				KindClaude: &routingAdapter{kind: KindClaude, version: "2.1.0", ready: true},
			})
			tc.set(r)
			var got Kind
			if tc.role != "" {
				p, err := r.PlaceRole(context.Background(), tc.role, "", tc.slot)
				if err != nil {
					t.Fatal(err)
				}
				got = p.Kind
			} else {
				res, err := r.Resolve(context.Background(), tc.slot)
				if err != nil {
					t.Fatal(err)
				}
				got = res.Kind
			}
			if got != tc.want {
				t.Fatalf("resolved host = %s, want %s: oh-my-pi does not generate", got, tc.want)
			}
			omp := r.adapters[KindOMP].(*routingAdapter)
			if omp.preflightCalls != 0 {
				t.Errorf("omp was preflighted %d times", omp.preflightCalls)
			}
			var named bool
			for _, reason := range r.SkipReasons() {
				if reason.Kind == KindOMP && strings.Contains(reason.Reason, "does not generate") {
					named = true
				}
			}
			if !named {
				t.Errorf("the skip reason must say oh-my-pi does not generate, got %v", r.SkipReasons())
			}
		})
	}
}
