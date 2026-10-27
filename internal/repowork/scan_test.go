package repowork

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeRepo builds a repository fixture from a path->content map and returns
// its root.
func writeRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, body := range files {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func mustScan(t *testing.T, root string, opts Options) Inventory {
	t.Helper()
	opts.Root = root
	inv, err := Scan(context.Background(), opts)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	return inv
}

func findDoc(t *testing.T, inv Inventory, path string) Document {
	t.Helper()
	for _, d := range inv.Documents {
		if d.Path == path {
			return d
		}
	}
	t.Fatalf("document %q not indexed", path)
	return Document{}
}

func TestScanEmptyRepo(t *testing.T) {
	inv := mustScan(t, t.TempDir(), Options{})
	if len(inv.Documents) != 0 || len(inv.Next) != 0 {
		t.Fatalf("empty repo returned %d documents", len(inv.Documents))
	}
	if inv.Partial {
		t.Fatal("empty repo must not be partial")
	}
	if inv.Revision == "" {
		t.Fatal("revision empty")
	}
}

func TestScanStateNextOrderAndExtraction(t *testing.T) {
	state := "# State\n\n## Next\n\n- plan the launch\n- second item depends on: `plan-the-launch`\n- done thought goes elsewhere\n\n## Done\n\n- retired item\n"
	root := writeRepo(t, map[string]string{statePath: state})
	inv := mustScan(t, root, Options{})
	if len(inv.Next) != 3 {
		t.Fatalf("got %d next items, want 3", len(inv.Next))
	}
	// The bullet is line 5: the file is "# State", blank, "## Next", blank, then
	// the first item.
	if inv.Next[0].Text != "plan the launch" || inv.Next[0].Status != statusOpen || inv.Next[0].Line != 5 {
		t.Fatalf("first next wrong: %+v", inv.Next[0])
	}
	if inv.Next[0].EndLine != 5 {
		t.Fatalf("plain bullet EndLine = %d, want 5", inv.Next[0].EndLine)
	}
	// Source-order preservation: ## Done bullets must not reorder or leak in.
	for _, n := range inv.Next {
		if n.Text == "retired item" {
			t.Fatal("retired item leaked into Next")
		}
	}
	// Nested indented lines extend EndLine but are not separate items.
	state2 := "# State\n\n## Next\n\n- parent item\n  - nested detail\n- second\n"
	root2 := writeRepo(t, map[string]string{statePath: state2})
	inv2 := mustScan(t, root2, Options{})
	if len(inv2.Next) != 2 {
		t.Fatalf("nested line became an item: %d items", len(inv2.Next))
	}
	if inv2.Next[0].EndLine != 6 || !strings.Contains(inv2.Next[0].Source, "nested detail") {
		t.Fatalf("continuation not merged: %+v", inv2.Next[0])
	}
}

func TestScanEpicCheckboxStatuses(t *testing.T) {
	epic := "# Epic\n\n- [ ] open task\n- [x] done task\n- [X] done again\n- plain bullet no status\n"
	root := writeRepo(t, map[string]string{"docs/plans/epic-launch.md": epic})
	inv := mustScan(t, root, Options{})
	doc := findDoc(t, inv, "docs/plans/epic-launch.md")
	if doc.Kind != kindEpic {
		t.Fatalf("kind = %q, want epic", doc.Kind)
	}
	if len(doc.Items) != 3 {
		t.Fatalf("plain bullet emitted without explicit status: %d items", len(doc.Items))
	}
	if doc.Items[0].Status != statusOpen || doc.Items[1].Status != statusDone || doc.Items[2].Status != statusDone {
		t.Fatalf("checkbox statuses wrong: %v", doc.Items)
	}
	if doc.Items[0].DependsOn != nil {
		t.Fatalf("deps inferred without a clause: %v", doc.Items[0].DependsOn)
	}
}

func TestScanRootFollowUpsAndKinds(t *testing.T) {
	files := map[string]string{
		"FOLLOW_UPS.md":       "# Follow-ups\n\n- [ ] chase review\n",
		"docs/plans/p.md":     "# Plan\n",
		"docs/specs/s.md":     "# Spec\n",
		"docs/adrs/0001-x.md": "# ADR\n",
		"docs/notes/n.md":     "# an indexed document, not work\n",
		"web/src/README.md":   "# not indexed\n",
		"PLAN.md":             "# root plan\n",
		"TASKS.md":            "# tasks\n",
		"EPICS.md":            "# epics\n",
		"STATE.md":            "# state\n",
		"README.md":           "# not matched prefix\n",
	}
	root := writeRepo(t, files)
	inv := mustScan(t, root, Options{})
	want := map[string]string{
		"FOLLOW_UPS.md":       kindFollowUp,
		"PLAN.md":             kindPlan,
		"TASKS.md":            kindTask,
		"EPICS.md":            kindEpic,
		"STATE.md":            kindState,
		"docs/plans/p.md":     kindPlan,
		"docs/specs/s.md":     kindSpec,
		"docs/adrs/0001-x.md": kindADR,
	}
	for path, kind := range want {
		d := findDoc(t, inv, path)
		if d.Kind != kind {
			t.Fatalf("%s kind = %q, want %q", path, d.Kind, kind)
		}
	}
	if d := findDoc(t, inv, "docs/notes/n.md"); d.Kind != kindDocument {
		t.Fatalf("docs/notes/n.md kind = %q, want a plain document", d.Kind)
	}
	for _, absent := range []string{"web/src/README.md", "README.md"} {
		for _, d := range inv.Documents {
			if d.Path == absent {
				t.Fatalf("%s must not be indexed", absent)
			}
		}
	}
}

func TestScanDeterministicAndStable(t *testing.T) {
	files := map[string]string{
		statePath:         "# State\n\n## Next\n\n- a\n",
		"docs/plans/b.md": "# B\n",
		"docs/plans/a.md": "# A\n",
		"docs/adrs/z.md":  "# Z\n",
	}
	root := writeRepo(t, files)
	first := mustScan(t, root, Options{})
	second := mustScan(t, root, Options{})
	if first.Revision != second.Revision {
		t.Fatalf("revision unstable: %s vs %s", first.Revision, second.Revision)
	}
	for i := 1; i < len(first.Documents); i++ {
		if first.Documents[i-1].Path >= first.Documents[i].Path {
			t.Fatalf("documents not sorted at %d", i)
		}
	}
}

func TestScanSymlinkEscapeRefused(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.md"), []byte("# secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := writeRepo(t, map[string]string{statePath: "# State\n\n## Next\n\n- a\n"})
	if err := os.Symlink(filepath.Join(outside, "secret.md"), filepath.Join(root, "docs", "plans", "leak.md")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	// A symlinked directory pointing outside must also be refused.
	if err := os.Symlink(outside, filepath.Join(root, "docs", "plans", "out")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	inv := mustScan(t, root, Options{})
	for _, d := range inv.Documents {
		if d.Path == "docs/plans/leak.md" {
			t.Fatal("symlinked file indexed")
		}
		if strings.HasPrefix(d.Path, "docs/plans/out/") {
			t.Fatal("symlinked directory traversed")
		}
	}
	reasons := map[string]string{}
	for _, s := range inv.Skipped {
		reasons[s.Path] = s.Reason
	}
	if reasons["docs/plans/leak.md"] != "symbolic link refused" {
		t.Fatalf("file skip reason = %q", reasons["docs/plans/leak.md"])
	}
	if reasons["docs/plans/out"] != "symbolic link refused" {
		t.Fatalf("dir skip reason = %q", reasons["docs/plans/out"])
	}
}

func TestScanExtraRootsValidation(t *testing.T) {
	root := writeRepo(t, map[string]string{statePath: "# State\n"})
	for _, bad := range []string{"../outside", "/abs", ".git/hooks", "a/../../b"} {
		if _, err := Scan(context.Background(), Options{Root: root, ExtraRoots: []string{bad}}); err == nil {
			t.Fatalf("extra root %q accepted", bad)
		}
	}
	writeRepo2 := writeRepo(t, map[string]string{
		statePath:              "# State\n",
		"team/notes/plan-x.md": "# Team plan\n",
	})
	inv, err := Scan(context.Background(), Options{Root: writeRepo2, ExtraRoots: []string{"team/notes"}})
	if err != nil {
		t.Fatalf("valid extra root: %v", err)
	}
	findDoc(t, inv, "team/notes/plan-x.md")
}

func TestScanOversizeAndBinarySkipped(t *testing.T) {
	root := writeRepo(t, map[string]string{
		statePath:              "# State\n",
		"docs/plans/big.md":    strings.Repeat("a", 4096),
		"docs/plans/binary.md": "\xff\xfe\x00bad",
	})
	inv := mustScan(t, root, Options{MaxFileBytes: 1024})
	findDoc(t, inv, statePath)
	if !inv.Partial {
		t.Fatal("oversize skip must set Partial")
	}
	found := false
	for _, s := range inv.Skipped {
		if s.Path == "docs/plans/big.md" && s.Reason == "file exceeds per-file size limit" {
			found = true
		}
	}
	if !found {
		t.Fatalf("oversize skip missing: %+v", inv.Skipped)
	}
	// Binary: invalid UTF-8 is its own reason.
	root2 := writeRepo(t, map[string]string{
		statePath:           "# State\n",
		"docs/plans/bad.md": "\xff\xfe\x00bad",
	})
	inv2 := mustScan(t, root2, Options{})
	reason := ""
	for _, s := range inv2.Skipped {
		if s.Path == "docs/plans/bad.md" {
			reason = s.Reason
		}
	}
	if reason != "invalid UTF-8" {
		t.Fatalf("binary skip reason = %q", reason)
	}
}

func TestScanBudgetExpiryByContext(t *testing.T) {
	files := map[string]string{statePath: "# State\n"}
	for i := range 50 {
		files["docs/plans/p"+string(rune('a'+i%26))+string(rune('a'+i/26))+".md"] = "# p\n"
	}
	root := writeRepo(t, files)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	inv, err := Scan(ctx, Options{Root: root, Budget: 0})
	if err != nil {
		t.Fatalf("canceled scan errored: %v", err)
	}
	if !inv.Partial {
		t.Fatal("canceled scan must be partial")
	}
	found := false
	for _, s := range inv.Skipped {
		if s.Reason == "budget exhausted" {
			found = true
		}
	}
	if !found {
		t.Fatalf("budget exhausted reason missing: %+v", inv.Skipped)
	}
}

func TestScanRootValidation(t *testing.T) {
	if _, err := Scan(context.Background(), Options{Root: ""}); err == nil {
		t.Fatal("empty root accepted")
	}
	if _, err := Scan(context.Background(), Options{Root: filepath.Join(t.TempDir(), "missing")}); err == nil {
		t.Fatal("missing root accepted")
	}
}

func TestDocumentIDsUnique(t *testing.T) {
	files := map[string]string{
		statePath:         "# State\n",
		"docs/plans/a.md": "# A\n",
		"docs/specs/a.md": "# A\n",
		"docs/adrs/a.md":  "# A\n",
	}
	root := writeRepo(t, files)
	inv := mustScan(t, root, Options{})
	seen := map[string]bool{}
	for _, d := range inv.Documents {
		if seen[d.ID] {
			t.Fatalf("duplicate id %s", d.ID)
		}
		seen[d.ID] = true
		if len(d.ID) != idHexLen {
			t.Fatalf("id %q wrong length", d.ID)
		}
	}
	if len(seen) != 4 {
		t.Fatalf("indexed %d documents, want 4", len(seen))
	}
	// A forged traversal-shaped ID is rejected as unknown, not passed through.
	if _, _, err := Open(context.Background(), root, "../../../etc/passwd"); err != ErrNotFound {
		t.Fatalf("forged id error = %v, want ErrNotFound", err)
	}
}
