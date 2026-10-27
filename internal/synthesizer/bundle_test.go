package synthesizer_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/synthesizer"
	"github.com/danielckv/agentic-plan-forge/internal/synthesizer/synthtest"
)

// The bundle turns an approved plan into repository files. It must be
// deterministic, never overwrite a document a person wrote, and never invent an
// architecture decision to have a decision record.

func buildSample(t *testing.T, opts synthesizer.BundleOptions) synthesizer.Bundle {
	t.Helper()
	in := synthtest.Sample().Input()
	bundle, err := synthesizer.BuildBundle(in, contracts.ApprovalMeta{ApprovalID: "a-1"}, opts)
	if err != nil {
		t.Fatalf("BuildBundle: %v", err)
	}
	return bundle
}

func TestBundleIsDeterministic(t *testing.T) {
	first := buildSample(t, synthesizer.BundleOptions{})
	second := buildSample(t, synthesizer.BundleOptions{})
	if len(first.Entries) != len(second.Entries) {
		t.Fatalf("two builds differ in size: %d vs %d", len(first.Entries), len(second.Entries))
	}
	for i := range first.Entries {
		if first.Entries[i].Path != second.Entries[i].Path {
			t.Errorf("entry %d path = %q, want %q", i, second.Entries[i].Path, first.Entries[i].Path)
		}
		if string(first.Entries[i].Body) != string(second.Entries[i].Body) {
			t.Errorf("entry %d body differs between two builds", i)
		}
		if first.Entries[i].SHA256 != second.Entries[i].SHA256 {
			t.Errorf("entry %d hash differs between two builds", i)
		}
	}
}

// Every generated file is a Markdown document under a planning directory, and
// no entry names a host path.
func TestBundlePathsAreRepositoryRelative(t *testing.T) {
	bundle := buildSample(t, synthesizer.BundleOptions{})
	if len(bundle.Entries) < 2 {
		t.Fatalf("bundle has %d entries, want at least a plan and a spec", len(bundle.Entries))
	}
	for _, entry := range bundle.Entries {
		if strings.HasPrefix(entry.Path, "/") || strings.Contains(entry.Path, "..") {
			t.Errorf("path %q is not repository-relative", entry.Path)
		}
		if !strings.HasSuffix(entry.Path, ".md") {
			t.Errorf("path %q is not Markdown", entry.Path)
		}
		if len(entry.Body) == 0 {
			t.Errorf("%q is empty", entry.Path)
		}
		if strings.Contains(string(entry.Body), "/Users/") {
			t.Errorf("%q carries a host path", entry.Path)
		}
	}
}

// A decision record is written only for a decision that was made.
func TestBundleWritesNoAdrWithoutADecision(t *testing.T) {
	bundle := buildSample(t, synthesizer.BundleOptions{})
	for _, entry := range bundle.Entries {
		if entry.Kind == synthesizer.KindADR {
			t.Fatal("a bundle with no resolved decision wrote a decision record")
		}
	}
}

// A resolved decision becomes one record, numbered from the caller's next free
// number.
func TestBundleNumbersDecisionRecords(t *testing.T) {
	in := synthtest.Sample().Input()
	in.Decisions = []contracts.Decision{{
		ID: "d-1", Question: "Which export format?",
		Options:          []contracts.DecisionOption{{ID: "csv", Label: "CSV"}, {ID: "xlsx", Label: "XLSX"}},
		ResolvedOptionID: "csv",
	}}
	bundle, err := synthesizer.BuildBundle(in, contracts.ApprovalMeta{ApprovalID: "a-1"},
		synthesizer.BundleOptions{NextADRNumber: 7})
	if err != nil {
		t.Fatalf("BuildBundle: %v", err)
	}
	var adr *synthesizer.BundleEntry
	for i := range bundle.Entries {
		if bundle.Entries[i].Kind == synthesizer.KindADR {
			adr = &bundle.Entries[i]
		}
	}
	if adr == nil {
		t.Fatal("a resolved decision produced no record")
	}
	if !strings.HasPrefix(adr.Path, "docs/adrs/0007-") {
		t.Errorf("record path = %q, want docs/adrs/0007-…", adr.Path)
	}
	if !strings.Contains(string(adr.Body), "CSV") {
		t.Error("the record does not name the chosen option")
	}
}

// An existing destination is refused: a generated file never overwrites a
// document a person wrote.
func TestBundleRefusesAnExistingDestination(t *testing.T) {
	bundle := buildSample(t, synthesizer.BundleOptions{})
	opts := synthesizer.BundleOptions{
		ExistingFiles: map[string]string{bundle.Entries[0].Path: "abc"},
	}
	_, err := synthesizer.BuildBundle(synthtest.Sample().Input(), contracts.ApprovalMeta{ApprovalID: "a-1"}, opts)
	var collision *synthesizer.BundleCollision
	if !errors.As(err, &collision) {
		t.Fatalf("error = %v, want BundleCollision", err)
	}
	if collision.Path != bundle.Entries[0].Path {
		t.Errorf("collision names %q, want %q", collision.Path, bundle.Entries[0].Path)
	}
}

// A decision-record number already used is skipped, never reused.
func TestBundleNeverReusesAnAdrNumber(t *testing.T) {
	in := synthtest.Sample().Input()
	in.Decisions = []contracts.Decision{{
		ID: "d-1", Question: "Which format?", ResolvedOptionID: "csv",
		Options: []contracts.DecisionOption{{ID: "csv", Label: "CSV"}},
	}}
	opts := synthesizer.BundleOptions{
		NextADRNumber: 3,
		ExistingFiles: map[string]string{"docs/adrs/0003-which-format.md": "abc"},
	}
	bundle, err := synthesizer.BuildBundle(in, contracts.ApprovalMeta{ApprovalID: "a-1"}, opts)
	if err != nil {
		t.Fatalf("BuildBundle: %v", err)
	}
	for _, entry := range bundle.Entries {
		if entry.Kind != synthesizer.KindADR {
			continue
		}
		if strings.Contains(entry.Path, "0003-") {
			t.Errorf("record %q reused a taken number", entry.Path)
		}
	}
}

// A project's own directories win over the defaults.
func TestBundleHonoursProjectDirectories(t *testing.T) {
	bundle := buildSample(t, synthesizer.BundleOptions{
		ExistingDirs: map[synthesizer.DocumentKind][]string{synthesizer.KindPlan: {"planning/plans"}},
	})
	found := false
	for _, entry := range bundle.Entries {
		if entry.Kind == synthesizer.KindPlan && strings.HasPrefix(entry.Path, "planning/plans/") {
			found = true
		}
	}
	if !found {
		t.Error("the project's own plan directory was ignored")
	}
}
