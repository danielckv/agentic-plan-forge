//go:build client && !server

package repowork

import (
	"testing"
)

// A project's decision records and plans do not all live in the three
// directories Plan Forge happens to name. Walking docs whole means a plan a
// folder deeper, or decisions under docs/adr or docs/decisions, are found
// without the owner configuring anything.
func TestScanFindsNestedAndAlternateDocDirs(t *testing.T) {
	files := map[string]string{
		"docs/plans/p.md":                  "# Plan\n",
		"docs/plans/q4/export.md":          "# Nested plan\n",
		"docs/plans/q4/q1/archive.md":      "# Deeper still\n",
		"docs/adr/0007-choose-json.md":     "# Decision\n",
		"docs/decisions/0008-pick-wire.md": "# Decision, other spelling\n",
		"docs/specs/s.md":                  "# Spec\n",
		"docs/specifications/api.md":       "# Spec, other spelling\n",
		"adrs/0001-root.md":                "# Decision at the root\n",
		"plans/roadmap.md":                 "# Plan at the root\n",
		"specs/interface.md":               "# Spec at the root\n",
		"docs/guides/how-to.md":            "# A guide, not work\n",
		"docs/images/diagram.png":          "binary",
		"web/src/app.go":                   "package app\n",
	}
	root := writeRepo(t, files)
	inv := mustScan(t, root, Options{})

	want := map[string]string{
		"docs/plans/p.md":                  kindPlan,
		"docs/plans/q4/export.md":          kindPlan,
		"docs/plans/q4/q1/archive.md":      kindPlan,
		"docs/adr/0007-choose-json.md":     kindADR,
		"docs/decisions/0008-pick-wire.md": kindADR,
		"docs/specs/s.md":                  kindSpec,
		"docs/specifications/api.md":       kindSpec,
		"adrs/0001-root.md":                kindADR,
		"plans/roadmap.md":                 kindPlan,
		"specs/interface.md":               kindSpec,
	}
	for path, kind := range want {
		doc := findDoc(t, inv, path)
		if doc.Kind != kind {
			t.Errorf("%s kind = %q, want %q", path, kind, doc.Kind)
		}
	}
	// A guide under docs is indexed as a plain document, not as work.
	if d := findDoc(t, inv, "docs/guides/how-to.md"); d.Kind != kindDocument {
		t.Errorf("docs/guides/how-to.md kind = %q, want a plain document", d.Kind)
	}
	// Code is never a planning document.
	for _, d := range inv.Documents {
		if d.Path == "web/src/app.go" {
			t.Fatal("a Go file was indexed as a planning document")
		}
	}
}

// The nearest directory that names a kind decides it, so a nested folder does
// not relabel everything under docs.
func TestClassifyUsesTheNearestKindDirectory(t *testing.T) {
	for _, tc := range []struct {
		rel  string
		want string
	}{
		{"docs/plans/p.md", kindPlan},
		{"docs/plans/q4/p.md", kindPlan},
		{"docs/specs/q4/s.md", kindSpec},
		{"docs/adr/0001-x.md", kindADR},
		{"docs/decisions/0002-y.md", kindADR},
		{"docs/specifications/api.md", kindSpec},
		{"plans/roadmap.md", kindPlan},
		{"adrs/0001-x.md", kindADR},
		// A kind noun in a file name still wins over an unhelpful directory.
		{"docs/notes/epic-launch.md", kindEpic},
		{"docs/notes/follow-ups.md", kindFollowUp},
		{"docs/notes/tasks.md", kindTask},
		// A guide is a document, whatever sits above it.
		{"docs/guides/how-to.md", kindDocument},
		// Session memory is not planning work, and a kind noun below it does
		// not make it work.
		{".agents/sessions/2026-01-01.md", kindDocument},
	} {
		if got := classify(tc.rel); got != tc.want {
			t.Errorf("classify(%q) = %q, want %q", tc.rel, got, tc.want)
		}
	}
}

// Media directories are not walked, so a project full of screenshots does not
// spend its document budget rejecting images one at a time.
func TestScanSkipsMediaDirectories(t *testing.T) {
	files := map[string]string{
		"docs/plans/p.md":       "# Plan\n",
		"docs/images/shot1.png": "binary",
		"docs/images/shot2.png": "binary",
		"docs/assets/logo.svg":  "<svg/>",
	}
	root := writeRepo(t, files)
	inv := mustScan(t, root, Options{})
	findDoc(t, inv, "docs/plans/p.md")
	for _, skip := range inv.Skipped {
		if assetDir(lastSegment(baseDir(skip.Path))) {
			t.Errorf("media was walked and skipped individually: %+v", skip)
		}
	}
}

// lastSegment is the final directory name of a path.
func lastSegment(rel string) string {
	for i := len(rel) - 1; i >= 0; i-- {
		if rel[i] == '/' {
			return rel[i+1:]
		}
	}
	return rel
}

// baseDir is the directory a path sits in, for the media test.
func baseDir(rel string) string {
	for i := len(rel) - 1; i >= 0; i-- {
		if rel[i] == '/' {
			return rel[:i]
		}
	}
	return "."
}
