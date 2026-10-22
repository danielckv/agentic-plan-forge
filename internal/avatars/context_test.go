//go:build client && !server

package avatars_test

import (
	"context"
	"strings"
	"testing"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/synthesizer/synthtest"
)

const contextSentinel = "SENTINEL_PROJECT_CONTEXT_4e1a"

func withContext(h *harness) {
	h.in.Context = contracts.ProjectContext{
		Source: contracts.ContextSourceRecall,
		Hits: []contracts.ContextHit{{
			Path: "docs/adrs/0004-invoices.md", Heading: "Invoices", Text: "Invoices export as CSV. " + contextSentinel, Score: 3.2,
		}},
		Branch: "main", Head: "0123456789ab", Recent: []string{"Add invoice export skeleton"},
	}
	h.in.ContextHash = "context-1"
}

// The project context reaches the architect, every specialist and the critic,
// and never the Product Manager, whose input is product-only.
func TestProjectContextReachesTechnicalAvatarsOnly(t *testing.T) {
	h := newHarness(t, synthtest.Sample())
	withContext(h)
	if _, err := h.run(context.Background(), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, c := range h.stub.callsOf("pm") {
		if strings.Contains(string(c.Req.UserPayload)+c.Req.SystemPrompt, contextSentinel) {
			t.Fatal("the PM request carries project context")
		}
	}
	seen := map[contracts.RoleID]bool{}
	for _, c := range h.stub.allCalls() {
		if strings.Contains(string(c.Req.UserPayload), contextSentinel) {
			if !strings.Contains(string(c.Req.UserPayload), `"project_context"`) {
				t.Errorf("%s carries the context outside project_context", c.Key)
			}
			seen[c.Req.Role] = true
		}
	}
	for _, role := range []contracts.RoleID{contracts.RoleArchitect, contracts.RoleSpecialist, contracts.RoleCritic} {
		if !seen[role] {
			t.Errorf("%s never received the project context", role)
		}
	}
	if seen[contracts.RoleSynthesizer] {
		t.Error("the synthesizer received project context; it narrates the locked design only")
	}
}

// An empty context adds nothing to the payloads.
func TestEmptyProjectContextIsOmitted(t *testing.T) {
	h := newHarness(t, synthtest.Sample())
	if _, err := h.run(context.Background(), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, c := range h.stub.allCalls() {
		if strings.Contains(string(c.Req.UserPayload), `"project_context"`) {
			t.Fatalf("%s carries an empty project_context", c.Key)
		}
	}
}

// A saved checkpoint made with other project context is not replayed: the
// drafts it holds were grounded in notes that have since changed.
func TestResumeRevalidatesTheProjectContext(t *testing.T) {
	h := newHarness(t, synthtest.Sample())
	withContext(h)
	h.stub.on("architect", func(c *call, in input) (reply, bool) {
		if c.Nth == 1 {
			return reply{Err: &contracts.ErrCapability{Host: "stub", Reason: "refused"}}, true
		}
		return reply{}, false
	})
	if _, err := h.run(context.Background(), nil); err == nil {
		t.Fatal("expected the architect failure")
	}
	h.in.ContextHash = "context-2"
	if _, err := h.run(context.Background(), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if h.stub.count("pm") != 2 {
		t.Errorf("PM calls = %d, want 2 after the project context changed", h.stub.count("pm"))
	}
}
