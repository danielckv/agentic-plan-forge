package contracts_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// fixtureDir is shared with the frontend's Ajv test, so one corpus proves both
// implementations agree about what a valid message looks like.
const fixtureDir = "../../tests/fixtures/schema"

func TestEmbeddedSchemasLoad(t *testing.T) {
	names := contracts.SchemaNames()
	if len(names) == 0 {
		t.Fatal("no embedded schemas found")
	}
	want := map[string]bool{
		"envelope.v1":       true,
		"normalized_prd.v1": true,
		"synthesis.v1":      true,
		"mermaid-denylist":  true,
	}
	for name := range want {
		found := false
		for _, got := range names {
			if got == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("schema %q is not embedded", name)
		}
	}
}

func TestValidateAcceptsAndRejectsFixtures(t *testing.T) {
	valid, err := filepath.Glob(filepath.Join(fixtureDir, "valid", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(valid) == 0 {
		t.Fatalf("no valid fixtures under %s", fixtureDir)
	}
	for _, path := range valid {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		schemaID := fixtureSchemaID(t, path)
		if err := contracts.Validate(schemaID, raw); err != nil {
			t.Errorf("%s: expected valid against %s, got %v", path, schemaID, err)
		}
	}

	invalid, err := filepath.Glob(filepath.Join(fixtureDir, "invalid", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(invalid) == 0 {
		t.Fatalf("no invalid fixtures under %s", fixtureDir)
	}
	for _, path := range invalid {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		schemaID := fixtureSchemaID(t, path)
		if err := contracts.Validate(schemaID, raw); err == nil {
			t.Errorf("%s: expected validation failure against %s", path, schemaID)
		} else if code := contracts.CodeOf(err); code != contracts.CodeInvalidPayload {
			t.Errorf("%s: expected %s, got %s", path, contracts.CodeInvalidPayload, code)
		}
	}
}

// fixtureSchemaID derives the schema a fixture targets from its file name, so
// adding a case needs no test edit: <schema-id>__<case>.json.
func fixtureSchemaID(t *testing.T, path string) string {
	t.Helper()
	base := strings.TrimSuffix(filepath.Base(path), ".json")
	id, _, ok := strings.Cut(base, "__")
	if !ok || id == "" {
		t.Fatalf("fixture %s must be named <schema-id>__<case>.json", path)
	}
	return id
}

func TestValidateRejectsUnknownSchema(t *testing.T) {
	if err := contracts.Validate("nope.v1", []byte(`{}`)); err == nil {
		t.Fatal("expected an error for an unknown schema")
	}
}

func TestValidateRejectsUnknownProperties(t *testing.T) {
	raw := []byte(`{"schema_version":1,"product":{"goals":[],"actors":[],"flows":[],"requirements":[],"acceptance_criteria":[],"constraints":[],"open_questions":[]},"ambiguities":[],"surprise":true}`)
	if err := contracts.Validate("normalized_prd.v1", raw); err == nil {
		t.Fatal("expected additionalProperties:false to reject the extra key")
	}
}

func TestSchemaPathWritesPrivateFile(t *testing.T) {
	path, err := contracts.SchemaPath("synthesis.v1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(path)) })
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("schema temp file mode = %o, want 600", perm)
	}
}

func TestMermaidDenylistIsShared(t *testing.T) {
	tokens, err := contracts.MermaidDenylistTokens()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"click", "style", "classDef", "linkStyle", "link", "links", "call", "href"}
	if len(tokens) != len(want) {
		t.Fatalf("denylist has %d tokens, want %d: %v", len(tokens), len(want), tokens)
	}
	for i := range want {
		if tokens[i] != want[i] {
			t.Fatalf("denylist[%d] = %q, want %q", i, tokens[i], want[i])
		}
	}
}

func TestErrorCodesAreStable(t *testing.T) {
	coded := contracts.Coded(contracts.CodeStaleBaseline, "baseline moved")
	if got := contracts.CodeOf(coded); got != contracts.CodeStaleBaseline {
		t.Fatalf("CodeOf = %s, want %s", got, contracts.CodeStaleBaseline)
	}
	wrapped := contracts.Wrap(contracts.CodeFenced, coded, "executor is stale")
	if got := contracts.CodeOf(wrapped); got != contracts.CodeFenced {
		t.Fatalf("CodeOf on wrapped error = %s, want %s", got, contracts.CodeFenced)
	}
	if got := contracts.CodeOf(os.ErrNotExist); got != contracts.CodeInternal {
		t.Fatalf("CodeOf on an uncoded error = %s, want %s", got, contracts.CodeInternal)
	}
}
