//go:build client && !server

package ingestion

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// stubGen replays canned model output. Ingestion never imports internal/host,
// so every test here runs against this stub.
type stubGen struct {
	mu        sync.Mutex
	responses []string
	requests  []contracts.GenerateRequest
}

// GenerateStream answers like a real adapter: a request that names a schema
// is validated here, and output that fails it comes back as ErrSchemaInvalid
// rather than as a result. A stub that skipped the check would exercise a
// repair path no host can reach.
func (s *stubGen) GenerateStream(_ context.Context, req contracts.GenerateRequest, _ func(contracts.GenerationEvent) error) (contracts.GenerationResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, req)
	i := len(s.requests) - 1
	if i >= len(s.responses) {
		i = len(s.responses) - 1
	}
	out := s.responses[i]
	if req.SchemaID != "" {
		if err := contracts.Validate(req.SchemaID, []byte(out)); err != nil {
			return contracts.GenerationResult{}, &contracts.ErrSchemaInvalid{
				SchemaID: req.SchemaID, Output: []byte(out), Err: err,
			}
		}
	}
	return contracts.GenerationResult{Content: json.RawMessage(out)}, nil
}

func (s *stubGen) payload(t *testing.T, i int) normalizePayload {
	t.Helper()
	var p normalizePayload
	if err := json.Unmarshal(s.requests[i].UserPayload, &p); err != nil {
		t.Fatalf("request %d payload: %v", i, err)
	}
	return p
}

const (
	ambiguityText = "The reporting queue retries failed exports"

	fullResponse = `{"schema_version":1,"product":{"title":"Billing export","problem":"Finance cannot reconcile invoices.",` +
		`"goals":["Export invoices as CSV"],"actors":["Finance analyst"],"flows":["Choose a date range and download"],` +
		`"requirements":["The system shall export invoices for a date range"],"acceptance_criteria":["One month returns every invoice"],` +
		`"constraints":[],"open_questions":[]},"technical_appendix":"Uses the invoices table.",` +
		`"ambiguities":[{"id":"x","text":"` + ambiguityText + `","question":"Product or technical?","guess":"technical"}],"warnings":[]}`

	emptyResponse = `{"schema_version":1,"product":{"title":"","problem":"","goals":[],"actors":[],"flows":[],` +
		`"requirements":[],"acceptance_criteria":[],"constraints":[],"open_questions":[]},"technical_appendix":"","ambiguities":[],"warnings":[]}`

	invalidResponse = `{"schema_version":1}`
)

func TestNormalizeClassifiesAndKeepsAmbiguityIDsStable(t *testing.T) {
	gen := &stubGen{responses: []string{fullResponse}}
	out, err := Normalize(context.Background(), gen, "Finance needs a CSV export of invoices.", nil)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if len(gen.requests) != 1 {
		t.Fatalf("model called %d times, want 1", len(gen.requests))
	}
	req := gen.requests[0]
	if req.Role != contracts.RoleIngestion || req.ModelSlot != contracts.SlotMain || req.SchemaID != NormalizedPRDSchemaID {
		t.Errorf("request = role %s slot %s schema %s", req.Role, req.ModelSlot, req.SchemaID)
	}
	if p := gen.payload(t, 0); p.DocumentMarkdown != "Finance needs a CSV export of invoices." || p.Section.Index != 1 || p.Section.Count != 1 {
		t.Errorf("payload = %+v", p)
	}

	if out.SchemaVersion != contracts.SchemaVersion || out.Product.Title != "Billing export" ||
		len(out.Product.Goals) != 1 || out.TechnicalAppendix != "Uses the invoices table." {
		t.Errorf("result = %+v", out)
	}
	if len(out.Ambiguities) != 1 {
		t.Fatalf("ambiguities = %+v, want exactly the one the model raised", out.Ambiguities)
	}
	wantID := ambiguityID(contracts.Ambiguity{Text: ambiguityText})
	if out.Ambiguities[0].ID != wantID || !strings.HasPrefix(wantID, "amb-") {
		t.Errorf("ambiguity ID = %q, want %q derived from the excerpt, not the model's ID", out.Ambiguities[0].ID, wantID)
	}
	if len(out.SourceRefs) != 1 || out.SourceRefs[0].SourceID != contracts.DocumentIDPRD {
		t.Errorf("source refs = %+v", out.SourceRefs)
	}
	if err := contracts.Validate(NormalizedPRDSchemaID, WireJSON(out)); err != nil {
		t.Errorf("WireJSON violates the schema: %v", err)
	}
}

func TestNormalizeAppliesOwnerResolutions(t *testing.T) {
	id := ambiguityID(contracts.Ambiguity{Text: ambiguityText})
	cases := []struct {
		choice       string
		wantAppendix string
		wantReq      string
	}{
		{contracts.AmbiguityTechnical, ambiguityText + " (owner note: retry policy is internal)", ""},
		{contracts.AmbiguityProduct, "", ambiguityText + " (owner note: retry policy is internal)"},
		{contracts.AmbiguityDismiss, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.choice, func(t *testing.T) {
			gen := &stubGen{responses: []string{fullResponse}}
			out, err := Normalize(context.Background(), gen, "Finance needs a CSV export of invoices.",
				[]contracts.AmbiguityResolution{{AmbiguityID: id, Choice: tc.choice, Note: "retry policy is internal"}})
			if err != nil {
				t.Fatal(err)
			}
			if len(out.Ambiguities) != 0 {
				t.Errorf("a resolved ambiguity stays open: %+v", out.Ambiguities)
			}
			if tc.wantAppendix != "" && !strings.Contains(out.TechnicalAppendix, tc.wantAppendix) {
				t.Errorf("appendix = %q, want it to contain %q", out.TechnicalAppendix, tc.wantAppendix)
			}
			if tc.wantReq != "" && !containsString(out.Product.Requirements, tc.wantReq) {
				t.Errorf("requirements = %v, want %q", out.Product.Requirements, tc.wantReq)
			}
			if tc.choice == contracts.AmbiguityDismiss &&
				(strings.Contains(out.TechnicalAppendix, "retry policy") || len(out.Product.Requirements) != 1) {
				t.Errorf("a dismissed ambiguity must add nothing: %+v", out)
			}
		})
	}
}

func TestApplyResolutionsReusesBaseWithoutModelCalls(t *testing.T) {
	gen := &stubGen{responses: []string{fullResponse}}
	base, err := Normalize(context.Background(), gen, "Finance needs a CSV export of invoices.", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(gen.requests) != 1 || len(base.Ambiguities) != 1 {
		t.Fatalf("requests = %d, ambiguities = %d", len(gen.requests), len(base.Ambiguities))
	}
	id := base.Ambiguities[0].ID

	prod, err := ApplyResolutions(base, []contracts.AmbiguityResolution{{AmbiguityID: id, Choice: contracts.AmbiguityProduct}})
	if err != nil {
		t.Fatal(err)
	}
	if len(prod.Ambiguities) != 0 || !containsString(prod.Product.Requirements, ambiguityText) {
		t.Errorf("product resolution = %+v", prod)
	}
	// base must remain unchanged so subsequent resolution edits can re-apply cleanly.
	if len(base.Ambiguities) != 1 || len(base.Product.Requirements) != 1 {
		t.Errorf("base mutated by ApplyResolutions: %+v", base)
	}
	tech, err := ApplyResolutions(base, []contracts.AmbiguityResolution{{AmbiguityID: id, Choice: contracts.AmbiguityTechnical}})
	if err != nil {
		t.Fatal(err)
	}
	if len(tech.Ambiguities) != 0 || !strings.Contains(tech.TechnicalAppendix, ambiguityText) || len(tech.Product.Requirements) != 1 {
		t.Errorf("technical resolution = %+v", tech)
	}
	if len(gen.requests) != 1 {
		t.Errorf("model called %d times, want 1", len(gen.requests))
	}
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func TestNormalizeRejectsUnknownResolutionChoice(t *testing.T) {
	gen := &stubGen{responses: []string{fullResponse}}
	_, err := Normalize(context.Background(), gen, "text",
		[]contracts.AmbiguityResolution{{AmbiguityID: "amb-1", Choice: "maybe"}})
	if err == nil {
		t.Fatal("an unknown resolution choice must be refused")
	}
	if len(gen.requests) != 0 {
		t.Errorf("no model call may be made for invalid resolutions, saw %d", len(gen.requests))
	}
}

func TestNormalizeShortInputListsEveryEmptyField(t *testing.T) {
	gen := &stubGen{responses: []string{emptyResponse}}
	out, err := Normalize(context.Background(), gen, "Add a CSV export.", nil)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, a := range out.Ambiguities {
		ids = append(ids, a.ID)
	}
	want := []string{"empty-goals", "empty-actors", "empty-flows", "empty-acceptance_criteria"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("chip IDs = %v, want %v", ids, want)
	}

	// Dismissing a chip removes it; the others stay.
	out, err = Normalize(context.Background(), &stubGen{responses: []string{emptyResponse}}, "Add a CSV export.",
		[]contracts.AmbiguityResolution{{AmbiguityID: "empty-goals", Choice: contracts.AmbiguityDismiss}})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Ambiguities) != 3 || out.Ambiguities[0].ID != "empty-actors" {
		t.Errorf("after dismissing one chip: %+v", out.Ambiguities)
	}
}

func TestNormalizeEmptyInputMakesNoModelCall(t *testing.T) {
	gen := &stubGen{responses: []string{fullResponse}}
	out, err := Normalize(context.Background(), gen, "  \n ", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(gen.requests) != 0 {
		t.Errorf("model called %d times for an empty document", len(gen.requests))
	}
	if len(out.SourceRefs) != 0 || len(out.Ambiguities) != 4 {
		t.Errorf("result = %+v, want no refs and one chip per empty field", out)
	}
}

func TestNormalizeRepairsInvalidOutputOnce(t *testing.T) {
	gen := &stubGen{responses: []string{invalidResponse, fullResponse}}
	out, err := Normalize(context.Background(), gen, "Finance needs a CSV export of invoices.", nil)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if len(gen.requests) != 2 {
		t.Fatalf("model called %d times, want 2 (one repair)", len(gen.requests))
	}
	if gen.payload(t, 0).Repair != nil {
		t.Error("the first call must not carry a repair request")
	}
	repair := gen.payload(t, 1).Repair
	if repair == nil || repair.Errors == "" || repair.PreviousOutput != invalidResponse {
		t.Errorf("second call repair = %+v, want the schema errors and the previous output", repair)
	}
	if out.Product.Title != "Billing export" {
		t.Errorf("result = %+v", out)
	}
}

func TestNormalizeFailsAfterOneFailedRepair(t *testing.T) {
	gen := &stubGen{responses: []string{invalidResponse}}
	_, err := Normalize(context.Background(), gen, "Finance needs a CSV export of invoices.", nil)
	if err == nil {
		t.Fatal("output that never validates must fail the call")
	}
	if len(gen.requests) != 2 {
		t.Errorf("model called %d times, want exactly 2: one attempt and one repair", len(gen.requests))
	}
	var coded *contracts.CodedError
	if !errors.As(err, &coded) || coded.Code != contracts.CodeInvalidPayload {
		t.Errorf("error = %v, want code %s", err, contracts.CodeInvalidPayload)
	}
}

func TestNormalizeSplitsLongDocumentsAtTopLevelHeadings(t *testing.T) {
	// Each part is 60% of one section, so no two parts fit together and the
	// document splits into exactly one section per part.
	var sb strings.Builder
	for _, name := range []string{"Part 1", "Part 2", "Part 3"} {
		sb.WriteString("# " + name + "\n\n" + strings.Repeat("word ", MaxSectionChars*6/10/5) + "\n\n")
	}
	text := sb.String()
	if utf8.RuneCountInString(text) <= MaxSectionChars {
		t.Fatalf("test document is only %d characters", utf8.RuneCountInString(text))
	}
	gen := &stubGen{responses: []string{fullResponse}}
	out, err := Normalize(context.Background(), gen, text, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(gen.requests) != 3 {
		t.Fatalf("model called %d times, want 3 sections", len(gen.requests))
	}
	var rebuilt strings.Builder
	for i := range gen.requests {
		p := gen.payload(t, i)
		if n := utf8.RuneCountInString(p.DocumentMarkdown); n > MaxSectionChars {
			t.Errorf("section %d has %d characters, over the %d limit", i+1, n, MaxSectionChars)
		}
		if p.Section.Index != i+1 || p.Section.Count != 3 {
			t.Errorf("section info %d = %+v", i, p.Section)
		}
		rebuilt.WriteString(p.DocumentMarkdown)
	}
	if rebuilt.String() != text {
		t.Error("the sections do not reassemble to the original document")
	}
	if !containsString(out.Warnings, "document normalized in 3 sections") {
		t.Errorf("warnings = %v", out.Warnings)
	}
	if len(out.Product.Goals) != 1 {
		t.Errorf("identical goals from every section must merge to one, got %v", out.Product.Goals)
	}
	if len(out.Ambiguities) != 1 || !strings.Contains(out.Ambiguities[0].Question, "section 1 of 3") {
		t.Errorf("ambiguities = %+v, want one tagged with its section", out.Ambiguities)
	}
	if len(out.SourceRefs) != 3 {
		t.Errorf("source refs = %+v, want one per section", out.SourceRefs)
	}
}

func TestBaselineHashIsStableAndSensitive(t *testing.T) {
	norm := contracts.NormalizedPRD{SchemaVersion: 1, Product: contracts.ProductPRD{Goals: []string{"a"}}}
	res := []contracts.AmbiguityResolution{
		{AmbiguityID: "b", Choice: contracts.AmbiguityProduct},
		{AmbiguityID: "a", Choice: contracts.AmbiguityDismiss},
	}
	h1, err := BaselineHash("text", norm, res)
	if err != nil {
		t.Fatal(err)
	}
	h2, _ := BaselineHash("text", norm, []contracts.AmbiguityResolution{res[1], res[0]})
	if h1 != h2 || len(h1) != 64 {
		t.Errorf("hash depends on resolution order or is malformed: %q vs %q", h1, h2)
	}
	if h3, _ := BaselineHash("text!", norm, res); h3 == h1 {
		t.Error("changing the document text must change the baseline hash")
	}
	norm.Product.Goals = []string{"changed"}
	if h4, _ := BaselineHash("text", norm, res); h4 == h1 {
		t.Error("changing the interpretation must change the baseline hash")
	}
}

func TestCanonicalJSONSortsKeys(t *testing.T) {
	got, err := CanonicalJSON(map[string]any{"b": 1, "a": []string{"x<y"}})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"a":["x<y"],"b":1}` {
		t.Errorf("CanonicalJSON = %s", got)
	}
}
