//go:build client && !server

package host

import (
	"errors"
	"strings"
	"testing"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

func TestJSONCandidateUnwrapsWhatHostsActuallySend(t *testing.T) {
	const object = `{"schema_version":1,"title":"Divergence"}`
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "bare object is untouched", raw: object, want: object},
		{name: "whitespace is untouched", raw: "\n  " + object + "\n", want: "\n  " + object + "\n"},
		{name: "fenced with a language tag", raw: "```json\n" + object + "\n```", want: object},
		{name: "fenced without a tag", raw: "```\n" + object + "\n```", want: object},
		{name: "sentence before the object", raw: "Here is the result:\n" + object, want: object},
		{name: "note after the object", raw: object + "\n\nAmbiguities were listed above.", want: object},
		{name: "prose on both sides", raw: "Result:\n" + object + "\nAnd that is the classification.", want: object},
		{
			name: "a brace inside a string does not end the value",
			raw:  "```json\n" + `{"title":"a } brace","n":1}` + "\n```",
			want: `{"title":"a } brace","n":1}`,
		},
		{
			name: "no JSON value at all is returned unchanged",
			raw:  "I cannot classify this section.",
			want: "I cannot classify this section.",
		},
		{
			name: "a truncated object is returned unchanged",
			raw:  `{"schema_version":1,"title":"Divergence"`,
			want: `{"schema_version":1,"title":"Divergence"`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := string(jsonCandidate([]byte(tc.raw)))
			if got != tc.want {
				t.Errorf("jsonCandidate(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

// A schema failure must arrive as ErrSchemaInvalid carrying the output: the
// normalizer and the impact map both repair the reply by quoting it back, and
// a plain error would end the run on one malformed section.
func TestFinalizeGenerationReportsSchemaFailureWithTheOutput(t *testing.T) {
	req := contracts.GenerateRequest{SchemaID: "normalized_prd.v1", MaxOutput: 1 << 20}
	const bad = `{"schema_version":1}`

	_, err := finalizeGeneration(req, bad, contracts.ToolFreeReceipt{}, contracts.Usage{}, nil)
	if err == nil {
		t.Fatal("output that fails the schema must not be returned as a result")
	}
	var invalid *contracts.ErrSchemaInvalid
	if !errors.As(err, &invalid) {
		t.Fatalf("error = %T (%v), want *contracts.ErrSchemaInvalid", err, err)
	}
	if invalid.SchemaID != req.SchemaID {
		t.Errorf("SchemaID = %q, want %q", invalid.SchemaID, req.SchemaID)
	}
	if string(invalid.Output) != bad {
		t.Errorf("Output = %q, want the host output %q", invalid.Output, bad)
	}
	if !strings.Contains(err.Error(), bad) {
		t.Errorf("message = %q, want it to quote the output", err.Error())
	}
}

func TestFinalizeGenerationAcceptsAFencedReply(t *testing.T) {
	req := contracts.GenerateRequest{SchemaID: "normalized_prd.v1", MaxOutput: 1 << 20}
	object := `{"schema_version":1,"product":{"title":"Divergence","problem":"",` +
		`"goals":[],"actors":[],"flows":[],"requirements":[],"acceptance_criteria":[],` +
		`"constraints":[],"open_questions":[]},"technical_appendix":"",` +
		`"ambiguities":[],"warnings":[]}`

	res, err := finalizeGeneration(req, "```json\n"+object+"\n```", contracts.ToolFreeReceipt{}, contracts.Usage{}, nil)
	if err != nil {
		t.Fatalf("finalizeGeneration: %v", err)
	}
	if string(res.Content) != object {
		t.Errorf("Content = %q, want the unfenced object %q", res.Content, object)
	}
}
