package avatars_test

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/danielckv/agentic-plan-forge/internal/avatars"
	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

func preambles() map[string]string {
	return map[string]string{
		"pm":          avatars.PMPreamble,
		"architect":   avatars.ArchitectPreamble,
		"specialist":  avatars.SpecialistPreamble,
		"critic":      avatars.CriticPreamble,
		"synthesizer": avatars.SynthesizerPreamble,
	}
}

// The plan is the source of truth: each preamble must appear in it verbatim as
// goldenPreambles is the recorded mandate of each avatar.
const goldenPreambles = "testdata/preambles.golden.json"

// A preamble is what an avatar is for, and the asymmetries between them are
// the design: the Product Manager cannot see code, a specialist sees only its
// own workstream, the critic owns no draft. Reword one casually and the
// deliberation quietly becomes something else.
//
// This used to assert the preambles appeared verbatim in the original planning
// document. That document has been deleted — it was frozen history pinning
// live code, and the check skipped itself whenever the file was absent, so it
// could stop testing anything without anyone noticing. The mandate now lives
// in a fixture this test owns: changing a preamble means changing the golden
// file in the same commit, deliberately and in the diff.
func TestPreamblesMatchTheirRecordedMandate(t *testing.T) {
	current, err := json.MarshalIndent(preambles(), "", "  ")
	if err != nil {
		t.Fatalf("marshal preambles: %v", err)
	}
	current = append(current, '\n')

	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(goldenPreambles, current, 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Log("golden updated; re-run without UPDATE_GOLDEN=1")
		return
	}

	want, err := os.ReadFile(goldenPreambles)
	if err != nil {
		t.Fatalf("read %s: %v (regenerate with UPDATE_GOLDEN=1 go test ./internal/avatars/)", goldenPreambles, err)
	}
	if !bytes.Equal(bytes.TrimSpace(want), bytes.TrimSpace(current)) {
		t.Errorf("an avatar's mandate changed.\n\nIf that was deliberate, re-record it in the same commit:\n"+
			"    UPDATE_GOLDEN=1 go test ./internal/avatars/\n\nrecorded:\n%s\n\nnow:\n%s", want, current)
	}
}

func TestSystemPromptsStartWithTheirPreamble(t *testing.T) {
	// The architect answers in parts; every part carries the same preamble and
	// the same untrusted-input instruction as a whole stage would.
	prompts := map[string]string{
		"pm":                  avatars.PMSystemPrompt(),
		"architect":           avatars.ArchitectDesignSystemPrompt(),
		"architect diagrams":  avatars.ArchitectDiagramsSystemPrompt(),
		"architect doc tasks": avatars.ArchitectDocTasksSystemPrompt(),
		"specialist":          avatars.SpecialistSystemPrompt(),
		"critic":              avatars.CriticSystemPrompt(),
		"synthesizer":         avatars.SynthesizerSystemPrompt(),
		// The narrator is not one of the plan's five avatars, so it is absent
		// from preambles() and from the plan-verbatim check. It holds the
		// same boundary as the rest: its own preamble, the untrusted-input
		// instruction, and one declared reply shape.
		"narrator": avatars.NarratorSystemPrompt(),
	}
	pres := preambles()
	pres["architect diagrams"] = avatars.ArchitectPreamble
	pres["architect doc tasks"] = avatars.ArchitectPreamble
	pres["narrator"] = avatars.NarratorPreamble
	for name, p := range prompts {
		pre := pres[name]
		if !strings.HasPrefix(p, pre) {
			t.Errorf("%s system prompt does not start with its preamble", name)
		}
		rest := strings.TrimPrefix(p, pre)
		if !strings.Contains(rest, "untrusted data") {
			t.Errorf("%s system prompt lacks the untrusted-input instruction", name)
		}
		if !strings.Contains(rest, "Reply shape") {
			t.Errorf("%s system prompt lacks the output shape", name)
		}
	}
	roles := []contracts.RoleID{
		contracts.RoleProductManager, contracts.RoleArchitect, contracts.RoleSpecialist,
		contracts.RoleCritic, contracts.RoleSynthesizer, contracts.RoleNarrator,
	}
	for _, r := range roles {
		if avatars.Preamble(r) == "" {
			t.Errorf("role %s has no preamble", r)
		}
	}
	if avatars.Preamble(contracts.RoleIngestion) != "" {
		t.Error("the ingestion role has no avatar preamble")
	}
}

// The PM prompt must stay free of repository vocabulary beyond the preamble's
// own prohibition.
func TestPMSystemPromptCarriesNoRepositoryMaterial(t *testing.T) {
	rest := strings.TrimPrefix(avatars.PMSystemPrompt(), avatars.PMPreamble)
	for _, word := range []string{"AST", "symbol", "graph", "module", "repository"} {
		if strings.Contains(rest, word) {
			t.Errorf("PM prompt body mentions %q", word)
		}
	}
}

func keyOrder(t *testing.T, raw json.RawMessage) []string {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		t.Fatalf("not an object: %v %v", tok, err)
	}
	var keys []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, tok.(string))
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			t.Fatal(err)
		}
	}
	return keys
}

func TestBuildPMInputIsProductOnlyAndDeterministic(t *testing.T) {
	prd := contracts.ProductPRD{
		Title: "Invoice export", Problem: "Finance <cannot> export & reconcile.",
		Goals: []string{"g1"}, Actors: []string{"analyst"}, Flows: []string{"f1"},
		Requirements:       []string{"r1", "r2"},
		AcceptanceCriteria: []string{"a1"},
	}
	a, b := avatars.BuildPMInput(prd), avatars.BuildPMInput(prd)
	if !bytes.Equal(a, b) {
		t.Fatal("BuildPMInput is not deterministic")
	}
	want := []string{"title", "problem", "goals", "actors", "flows", "requirements", "acceptance_criteria", "constraints", "open_questions"}
	got := keyOrder(t, a)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("key order = %v, want %v", got, want)
	}
	var decoded struct {
		Requirements []struct{ ID, Text string } `json:"requirements"`
		Acceptance   []struct{ ID, Text string } `json:"acceptance_criteria"`
		Constraints  []string                    `json:"constraints"`
	}
	if err := json.Unmarshal(a, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Requirements) != 2 || decoded.Requirements[0].ID != "REQ-1" || decoded.Requirements[1].ID != "REQ-2" {
		t.Fatalf("requirement ids = %+v", decoded.Requirements)
	}
	if len(decoded.Acceptance) != 1 || decoded.Acceptance[0].ID != "AC-1" {
		t.Fatalf("acceptance ids = %+v", decoded.Acceptance)
	}
	if decoded.Constraints == nil {
		t.Fatal("empty product lists must encode as [], not null")
	}
	if !bytes.Contains(a, []byte("<cannot> export & reconcile")) {
		t.Fatal("product text must not be HTML-escaped")
	}
}

// TestPMInputHasExactlyTheProductFields guards the separation the product
// promises: requirements are kept apart from technical material, so product
// decisions are not made by code-level accident.
//
// The impact map that Page 1 shows is derived from the Product Manager's own
// output, never fed back into it. Adding it, the repository graph, or any
// other repository material to this payload must fail here, with this comment
// to explain why. See docs/adrs/0005-prd-impact-map.md.
func TestPMInputHasExactlyTheProductFields(t *testing.T) {
	raw := avatars.BuildPMInput(contracts.ProductPRD{Title: "Invoice export"})
	got := keyOrder(t, raw)
	want := []string{"title", "problem", "goals", "actors", "flows", "requirements", "acceptance_criteria", "constraints", "open_questions"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("the Product Manager payload is %v, want exactly the nine product fields %v", got, want)
	}
	for _, forbidden := range []string{
		"impact", "map", "areas", "links", "verdict", "graph", "symbols", "modules",
		"routes", "paths", "project_context", "doc_drift", "repository",
	} {
		for _, key := range got {
			if key == forbidden {
				t.Errorf("the Product Manager payload carries %q: it must never see repository material", key)
			}
		}
	}
}
