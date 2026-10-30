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
// test gives it, so the fallback rule can be observed directly.
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

// Oh-my-pi must not serve a run when Claude Code is installed and signed in.
func TestOMPYieldsToAReadyClaude(t *testing.T) {
	r := ompRouter(map[Kind]Adapter{
		KindClaude: &routingAdapter{kind: KindClaude, version: "2.1.0", ready: true},
	})
	// A pin to OMP must not survive either: the rule is about the host, not
	// about where it sits in a list.
	r.cfg.MainHost = string(KindOMP)
	res, err := r.Resolve(context.Background(), contracts.SlotMain)
	if err != nil {
		t.Fatalf("a ready Claude Code must serve the run: %v", err)
	}
	if res.Kind == KindOMP {
		t.Fatalf("resolved host = omp, want claude: oh-my-pi is a fallback only")
	}
	reasons := r.SkipReasons()
	var said bool
	for _, reason := range reasons {
		if reason.Kind == KindOMP && strings.Contains(reason.Reason, "fallback") {
			said = true
		}
	}
	if !said {
		t.Errorf("skip reasons must say why OMP was passed over, got %v", reasons)
	}
}

// The Gemini credential counts as a better host, because the Google API is the
// supported default and costs no process start per call.
func TestOMPYieldsToAStoredGeminiKey(t *testing.T) {
	r := ompRouter(map[Kind]Adapter{
		KindGemini: &routingAdapter{kind: KindGemini, version: geminiWireLabel, ready: true},
	})
	r.cfg.MainHost = string(KindOMP)
	res, err := r.Resolve(context.Background(), contracts.SlotMain)
	if err != nil {
		t.Fatalf("a stored Gemini key must serve the run: %v", err)
	}
	if res.Kind == KindOMP {
		t.Fatalf("resolved host = omp, want gemini: oh-my-pi is a fallback only")
	}
}

// With neither Claude nor Gemini/agy present, OMP is what keeps the tool
// working: it must be selected rather than reported as unusable.
func TestOMPIsUsedOnlyWhenNothingBetterIsReady(t *testing.T) {
	r := ompRouter(map[Kind]Adapter{
		// Present but signed out: it must not block the fallback.
		KindClaude: &routingAdapter{kind: KindClaude, version: "2.1.0", ready: false},
		KindGemini: &routingAdapter{kind: KindGemini, version: geminiWireLabel, ready: false, detectErr: errors.New("no key")},
	})
	res, err := r.Resolve(context.Background(), contracts.SlotMain)
	if err != nil {
		t.Fatalf("OMP must serve when no better host is ready: %v", err)
	}
	if res.Kind != KindOMP {
		t.Fatalf("resolved host = %s, want omp", res.Kind)
	}
}
