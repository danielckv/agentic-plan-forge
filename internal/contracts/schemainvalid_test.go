package contracts_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

func TestExcerptCutsOnARuneBoundary(t *testing.T) {
	raw := []byte(strings.Repeat("é", 50)) // 100 bytes, 50 runes
	got := contracts.Excerpt(raw, 11)
	if !utf8.ValidString(got) {
		t.Fatalf("excerpt %q is not valid UTF-8", got)
	}
	if want := strings.Repeat("é", 5) + "..."; got != want {
		t.Errorf("Excerpt = %q, want %q", got, want)
	}
}

func TestExcerptCollapsesWhitespaceAndKeepsShortInputWhole(t *testing.T) {
	got := contracts.Excerpt([]byte("{\n  \"a\": 1\n}"), 400)
	if want := `{ "a": 1 }`; got != want {
		t.Errorf("Excerpt = %q, want %q", got, want)
	}
}

func TestRepairEchoReportsOnlySchemaFailures(t *testing.T) {
	plain := errors.New("context deadline exceeded")
	if _, _, ok := contracts.RepairEcho(plain, 400); ok {
		t.Error("a non-schema error must not be reported as repairable")
	}

	cause := errors.New("missing properties 'product'")
	wrapped := fmt.Errorf("normalize section 5 of 11: %w", &contracts.ErrSchemaInvalid{
		SchemaID: "normalized_prd.v1",
		Output:   []byte(`{"schema_version":1}`),
		Err:      cause,
	})
	reason, output, ok := contracts.RepairEcho(wrapped, 400)
	if !ok {
		t.Fatal("a wrapped schema failure must be reported as repairable")
	}
	if reason != cause.Error() {
		t.Errorf("reason = %q, want the schema complaint %q", reason, cause.Error())
	}
	if output != `{"schema_version":1}` {
		t.Errorf("output = %q, want the host output", output)
	}
}

func TestErrSchemaInvalidUnwrapsToItsCause(t *testing.T) {
	cause := errors.New("invalid character 'A' after object key:value pair")
	err := &contracts.ErrSchemaInvalid{SchemaID: "normalized_prd.v1", Err: cause}
	if !errors.Is(err, cause) {
		t.Error("ErrSchemaInvalid must unwrap to its cause")
	}
	if !strings.Contains(err.Error(), "normalized_prd.v1") {
		t.Errorf("message = %q, want it to name the schema", err.Error())
	}
}
