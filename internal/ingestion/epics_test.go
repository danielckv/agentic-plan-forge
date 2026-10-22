//go:build client && !server

package ingestion

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

func epicResponse(epics string) string {
	return `{"schema_version":1,"product":{"title":"Billing export","problem":"","goals":["Export invoices"],` +
		`"actors":["Finance analyst"],"flows":["Download"],"requirements":["Export invoices for a date range","Email the export"],` +
		`"acceptance_criteria":["One month returns every invoice"],"constraints":[],"open_questions":[]},` +
		`"technical_appendix":"","epics":` + epics + `,"ambiguities":[],"warnings":[]}`
}

// The interpretation groups requirements into epics. Plan Forge numbers them
// itself, drops a nameless one, and keeps them out of the product view the
// Product Manager receives.
func TestNormalizeReturnsNumberedEpics(t *testing.T) {
	gen := &stubGen{responses: []string{epicResponse(`[` +
		`{"id":"x","title":"Invoice export","goal":"Finance downloads invoices.","requirements":["Export invoices for a date range"],"acceptance_criteria":["One month returns every invoice"]},` +
		`{"id":"y","title":"  ","requirements":["Email the export"]},` +
		`{"title":"Delivery","goal":"Exports reach the inbox.","requirements":["Email the export"]}]`)}}
	out, err := Normalize(context.Background(), gen, strings.Repeat("Finance exports invoices monthly. ", 20), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Epics) != 2 {
		t.Fatalf("epics = %+v, want two (the nameless one dropped)", out.Epics)
	}
	if out.Epics[0].ID != "E1" || out.Epics[0].Title != "Invoice export" || out.Epics[1].ID != "E2" || out.Epics[1].Title != "Delivery" {
		t.Errorf("epics = %+v, want E1 Invoice export and E2 Delivery", out.Epics)
	}
	if out.Epics[1].AcceptanceCriteria == nil {
		t.Error("an epic without criteria must carry an empty list, not null")
	}
	if err := contracts.Validate(NormalizedPRDSchemaID, WireJSON(out)); err != nil {
		t.Errorf("the wire form violates the schema: %v", err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(WireJSON(out), &wire); err != nil {
		t.Fatal(err)
	}
	if _, ok := wire["epics"]; !ok {
		t.Error("the wire form the window reads has no epics")
	}
	if !strings.Contains(gen.requests[0].SystemPrompt, `Group the product requirements into "epics"`) {
		t.Error("the system prompt does not ask for epics")
	}
}

// A model that returns no epics (an older prompt, or no requirements) still
// validates, and the result simply has none.
func TestNormalizeWithoutEpics(t *testing.T) {
	gen := &stubGen{responses: []string{fullResponse}}
	out, err := Normalize(context.Background(), gen, strings.Repeat("Finance exports invoices monthly. ", 20), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Epics) != 0 {
		t.Errorf("epics = %+v, want none", out.Epics)
	}
}

// A long document is interpreted in sections. An epic named in two sections
// becomes one epic carrying the requirements of both, without duplicates.
func TestMergeWireFoldsEpicsAcrossSections(t *testing.T) {
	parts := []wireNormalized{
		{Epics: []contracts.DraftEpic{{Title: "Invoice export", Requirements: []string{"A", "B"}}}},
		{Epics: []contracts.DraftEpic{
			{Title: "invoice  EXPORT", Goal: "Finance downloads invoices.", Requirements: []string{"b", "C"}, AcceptanceCriteria: []string{"AC"}},
			{Title: "Delivery", Requirements: []string{"D"}},
		}},
	}
	merged := mergeWire(parts, []textSection{{heading: "one"}, {heading: "two"}})
	if len(merged.Epics) != 2 {
		t.Fatalf("epics = %+v, want two", merged.Epics)
	}
	e := merged.Epics[0]
	if e.Title != "Invoice export" || e.Goal != "Finance downloads invoices." || strings.Join(e.Requirements, ",") != "A,B,C" || len(e.AcceptanceCriteria) != 1 {
		t.Errorf("merged epic = %+v", e)
	}
}
