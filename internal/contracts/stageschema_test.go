package contracts_test

import (
	"encoding/json"
	"testing"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// Every avatar stage document must validate against the embedded schema the
// orchestrator now hands the provider as OutputSchema. A schema that drifts
// from the Go struct is worse than no schema: the provider rejects valid
// output, or the model is constrained to a fiction.
func TestStageDocumentsSatisfyTheirEmbeddedSchemas(t *testing.T) {
	cases := []struct {
		schema string
		value  any
	}{
		{"pm.v1", map[string]any{
			"epics": []any{map[string]any{"id": "E1", "title": "Export invoices", "story_ids": []string{"S1"}}},
			"stories": []any{map[string]any{
				"id": "S1", "epic_id": "E1", "actor": "finance", "need": "export invoices",
				"acceptance_criteria": []string{"The CSV has every invoice"}, "requirement_ids": []string{"REQ-1"},
			}},
		}},
		{"architect.v1", map[string]any{
			"summary":     "One service owns the export.",
			"constraints": []string{"No new datastore."},
			"workstreams": []any{map[string]any{
				"id": "billing-api", "role": "backend", "module_paths": []string{"internal/billing"},
			}},
			"contracts": []any{map[string]any{
				"id": "export-api", "owner_workstream": "billing-api", "signature": "Export(ctx, id) ([]byte, error)",
				"errors": []string{"RateLimited: the caller retries after Retry-After"},
			}},
			"proposed_gaps": []any{map[string]any{
				"id": "gap-1", "specialty": "security review", "purpose": "name trust boundaries",
				"requirement_ids": []string{"REQ-1"}, "deliverable": "A threat model.",
				"parent_id": "billing-api", "placement": "workstream",
			}},
		}},
		{"specialist.v1", specialistDoc()},
		{"gap_agent.v1", specialistDoc()},
		{"critic.v1", map[string]any{"critiques": []any{critique()}}},
		{"code_reviewer.v1", map[string]any{"critiques": []any{critique()}}},
		{"tech_lead.v1", map[string]any{"resolutions": []any{map[string]any{
			"finding_id": "C-1", "blocks": false, "rationale": "already handled upstream",
		}}}},
		{"synthesizer.v1", map[string]any{
			"executive_summary": "Finance can export invoices safely.",
			"coverage_caveats":  []string{"One MEDIUM tradeoff is accepted."},
			"hours_per_day":     6.0, "max_parallel": 4, "start_date": "2026-03-04",
		}},
	}
	for _, c := range cases {
		raw, err := json.Marshal(c.value)
		if err != nil {
			t.Fatalf("%s: marshal: %v", c.schema, err)
		}
		if err := contracts.Validate(c.schema, raw); err != nil {
			t.Errorf("%s: a valid stage document failed its own schema: %v", c.schema, err)
		}
	}
}

// A temporary agent may not propose another: the schema must reject the key
// the avatars layer also refuses, so a provider constraining output cannot
// produce one.
func TestGapAgentSchemaRejectsNestedProposals(t *testing.T) {
	doc := specialistDoc()
	doc["proposed_gaps"] = []any{map[string]any{"id": "gap-nested"}}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := contracts.Validate("gap_agent.v1", raw); err == nil {
		t.Fatal("gap_agent.v1 accepted proposed_gaps")
	}
}

// The architect schema bounds proposals at the plan's three, so a provider
// enforcing it cannot flood the admission step.
func TestArchitectSchemaBoundsProposals(t *testing.T) {
	gap := func(i int) map[string]any {
		return map[string]any{
			"id": "gap-" + string(rune('a'+i)), "specialty": "scope " + string(rune('a'+i)),
			"purpose": "fill a gap", "requirement_ids": []string{"REQ-1"},
			"deliverable": "one artifact",
		}
	}
	doc := map[string]any{
		"summary": "s", "constraints": []string{}, "workstreams": []any{}, "contracts": []any{},
		"proposed_gaps": []any{gap(0), gap(1), gap(2), gap(3)},
	}
	raw, _ := json.Marshal(doc)
	if err := contracts.Validate("architect.v1", raw); err == nil {
		t.Fatal("architect.v1 accepted four proposals; the plan bounds them at three")
	}
}

func critique() map[string]any {
	return map[string]any{
		"id": "C-1", "severity": "HIGH", "evidence": "the export ignores org_id",
		"required_change": "reject mismatched org_id", "status": "open",
	}
}

func specialistDoc() map[string]any {
	return map[string]any{
		"workstream_id": "billing-api",
		"summary":       "Own the export endpoint.",
		"tasks": []any{map[string]any{
			"id": "T-BE-1", "epic_id": "E1", "story_id": "S1", "workstream_id": "billing-api",
			"title": "Add the export endpoint",
			"steps": []string{"Write the failing test"},
			"targets": []any{map[string]any{
				"path": "internal/billing/export.go", "operation": "add",
			}},
			"acceptance_criteria": []string{"The CSV has every invoice in the range"},
			"verification": []any{map[string]any{
				"id": "V-BE-1", "argv": []string{"go", "test", "./internal/billing/"}, "timeout_seconds": 120,
			}},
			"human_estimate": map[string]any{"optimistic": 2.0, "likely": 3.0, "pessimistic": 5.0, "basis": "one endpoint"},
			"agent_estimate": map[string]any{"optimistic": 1.0, "likely": 2.0, "pessimistic": 3.0, "basis": "one endpoint"},
		}},
	}
}
