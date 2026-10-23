//go:build client && !server

package avatars

import (
	"encoding/json"
	"strings"
	"testing"
)

// The cursor is what turns a byte count into "now on contracts". It follows a
// stream that arrives in arbitrary pieces, so the answer must not depend on
// where the deltas were cut.
func TestCursorNamesTheTopLevelKeyBeingFilled(t *testing.T) {
	doc := `{"summary":"map the export","constraints":["no new service"],` +
		`"workstreams":[{"id":"billing-api","role":"backend"}],` +
		`"contracts":[{"id":"export","owner_workstream":"billing-api"}]}`
	for _, chunk := range []int{1, 3, 7, 64, len(doc)} {
		var c jsonCursor
		seen := map[string]bool{}
		for i := 0; i < len(doc); i += chunk {
			end := min(i+chunk, len(doc))
			c.write(doc[i:end])
			if c.key != "" {
				seen[c.key] = true
			}
		}
		if c.key != "contracts" {
			t.Errorf("chunk %d: final key = %q, want contracts", chunk, c.key)
		}
		// A reader only ever samples the cursor between deltas, so a large
		// delta legitimately steps over keys. Only a byte-at-a-time stream
		// is expected to show every one.
		if chunk > 3 {
			continue
		}
		for _, want := range []string{"summary", "constraints", "workstreams", "contracts"} {
			if !seen[want] {
				t.Errorf("chunk %d: never reported %q", chunk, want)
			}
		}
	}
}

// A brace or a colon inside a string value must not move the cursor: a
// summary that quotes JSON would otherwise rename the part in progress.
func TestCursorIgnoresBracesAndColonsInsideStrings(t *testing.T) {
	var c jsonCursor
	c.write(`{"summary":"the shape is {\"id\": 1} and a : colon","workstreams":[`)
	if c.key != "workstreams" {
		t.Fatalf("key = %q, want workstreams", c.key)
	}
}

// A nested key must never be reported: only the top level names a part of the
// document the reader recognises.
func TestCursorReportsOnlyTopLevelKeys(t *testing.T) {
	var c jsonCursor
	c.write(`{"workstreams":[{"id":"a","module_paths":["x"],"depends_on":[]}`)
	if c.key != "workstreams" {
		t.Fatalf("key = %q, want workstreams: id and depends_on are nested", c.key)
	}
}

// Truncated or malformed input costs an empty answer, never a panic: the
// cursor reads a stream that may be cut off at any byte.
func TestCursorSurvivesTruncatedInput(t *testing.T) {
	for _, s := range []string{"", "{", `{"a`, `{"a":`, `{"a":[{`, `}}}]]]`, "\x00\xff"} {
		var c jsonCursor
		c.write(s)
	}
}

// The evidence note is what tells a reader whether a gap was cut or never
// existed, so it must name each kind of evidence the payload carries.
func TestContextNoteNamesEachKindOfEvidence(t *testing.T) {
	payload := map[string]any{
		"repository_graph": map[string]any{"symbols": map[string]any{
			"a.go": []any{}, "b.go": []any{}, "c.go": []any{},
		}},
		"project_context":     map[string]any{"hits": []any{1, 2}},
		"documentation_drift": []any{1, 2, 3, 4},
		"evidence_notes":      []string{"the graph was cut"},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	note := contextNote(raw)
	for _, want := range []string{"graph 3 files", "context 2 excerpts", "drift 4 items", "1 evidence cut"} {
		if !strings.Contains(note, want) {
			t.Errorf("note %q does not contain %q", note, want)
		}
	}
}

// A payload with no evidence says its size and nothing it does not have.
func TestContextNoteOfABarePayloadStatesOnlyItsSize(t *testing.T) {
	note := contextNote(json.RawMessage(`{"cycle":1}`))
	if !strings.HasSuffix(note, "KiB") {
		t.Fatalf("note = %q, want it to end in a size", note)
	}
	for _, absent := range []string{"graph", "context", "drift", "cut"} {
		if strings.Contains(note, absent) {
			t.Errorf("note %q claims %q the payload does not carry", note, absent)
		}
	}
}

// A payload that is not an object still gets a note rather than an error: the
// note is a label, and a missing label must not fail a stage.
func TestContextNoteOfUnreadablePayloadStillStatesASize(t *testing.T) {
	if note := contextNote(json.RawMessage(`not json`)); !strings.Contains(note, "evidence") {
		t.Fatalf("note = %q, want a size statement", note)
	}
}
