package repowork

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenRoundTrip(t *testing.T) {
	body := "# Plan\n\n- [ ] first task\n"
	root := writeRepo(t, map[string]string{
		statePath:         "# State\n\n## Next\n\n- work\n",
		"docs/plans/p.md": body,
	})
	inv := mustScan(t, root, Options{})
	doc := findDoc(t, inv, "docs/plans/p.md")
	got, content, err := Open(context.Background(), root, doc.ID)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got.Path != "docs/plans/p.md" || string(content) != body {
		t.Fatalf("Open returned wrong document/content: %q %q", got.Path, content)
	}
}

func TestOpenUnknownAndForgedIDs(t *testing.T) {
	root := writeRepo(t, map[string]string{statePath: "# State\n\n## Next\n\n- work\n"})
	for _, id := range []string{
		"0123456789abcdef", // well-formed hex, no such document
		"NOPE",             // malformed
		"../../../../etc/passwd",
		"",
	} {
		if _, _, err := Open(context.Background(), root, id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Open(%q) error = %v, want ErrNotFound", id, err)
		}
	}
}

func TestOpenDetectsChangedContent(t *testing.T) {
	root := writeRepo(t, map[string]string{"docs/plans/p.md": "# Plan\n"})
	inv := mustScan(t, root, Options{})
	doc := findDoc(t, inv, "docs/plans/p.md")
	if err := os.WriteFile(filepath.Join(root, "docs", "plans", "p.md"), []byte("# Plan changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Open(context.Background(), root, doc.ID); !errors.Is(err, ErrChanged) {
		t.Fatalf("changed content error = %v, want ErrChanged", err)
	}
}

// A document's identity carries its bytes, so the same path with different
// content is a different document, not a renamed one.
func TestDocumentIdentityChangesWithContent(t *testing.T) {
	root := writeRepo(t, map[string]string{"docs/plans/p.md": "# Plan\n"})
	first := findDoc(t, mustScan(t, root, Options{}), "docs/plans/p.md")

	if err := os.WriteFile(filepath.Join(root, "docs", "plans", "p.md"), []byte("# Plan changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	second := findDoc(t, mustScan(t, root, Options{}), "docs/plans/p.md")
	if first.ID == second.ID {
		t.Fatal("a changed document kept the identity of its old content")
	}
	if first.SHA256 == second.SHA256 {
		t.Fatal("a changed document kept the hash of its old content")
	}

	// Unchanged bytes keep their identity, so a page can be re-rendered without
	// every document looking stale.
	third := findDoc(t, mustScan(t, root, Options{}), "docs/plans/p.md")
	if second.ID != third.ID {
		t.Fatal("an unchanged document changed identity between two scans")
	}
}

func TestOpenRefusesSymlinkSwap(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "elsewhere.md"), []byte("# elsewhere"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := writeRepo(t, map[string]string{"docs/plans/p.md": "# Plan\n"})
	inv := mustScan(t, root, Options{})
	doc := findDoc(t, inv, "docs/plans/p.md")
	if err := os.Remove(filepath.Join(root, "docs", "plans", "p.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "elsewhere.md"), filepath.Join(root, "docs", "plans", "p.md")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	// A symlink where the document used to be is refused either as a changed
	// document or as an explicit refusal; what must never happen is serving the
	// bytes it points at.
	_, content, err := Open(context.Background(), root, doc.ID)
	if err == nil {
		t.Fatalf("a symlink swap was served: %q", content)
	}
	if errors.Is(err, ErrNotFound) {
		return
	}
	if !strings.Contains(err.Error(), "symbolic link refused") {
		t.Fatalf("symlink swap error = %v, want a refusal", err)
	}
}

func TestScanErrorsLeakNoHostPaths(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission test unreliable as root")
	}
	root := writeRepo(t, map[string]string{
		statePath:         "# State\n",
		"docs/plans/p.md": "# Plan\n",
		"docs/plans/s.md": "# Secret\n",
	})
	if err := os.Chmod(filepath.Join(root, "docs", "plans", "s.md"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(filepath.Join(root, "docs", "plans", "s.md"), 0o644)
	})
	inv, err := Scan(context.Background(), Options{Root: root})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if !inv.Partial {
		t.Fatal("unreadable file must set Partial")
	}
	found := false
	for _, s := range inv.Skipped {
		if s.Path == "docs/plans/s.md" {
			if s.Reason != "permission denied" {
				t.Fatalf("reason = %q", s.Reason)
			}
			found = true
		}
		if strings.Contains(s.Reason, root) || strings.Contains(s.Path, root) {
			t.Fatalf("host path leaked: %+v", s)
		}
	}
	if !found {
		t.Fatalf("unreadable skip missing: %+v", inv.Skipped)
	}
	// Errors from Open must not carry the host root either.
	_, _, oerr := Open(context.Background(), filepath.Join(root, "missing"), "0123456789abcdef")
	if oerr != nil && strings.Contains(oerr.Error(), root) {
		t.Fatalf("host path leaked in Open error: %v", oerr)
	}
}

func TestScanPartialOnDepthLimit(t *testing.T) {
	// Build docs/plans/a/b/c/d/e/f.md: depth 5 under the root exceeds maxDepth 4.
	deep := "docs/plans/a/b/c/d/e/f.md"
	root := writeRepo(t, map[string]string{
		statePath: "# State\n",
		deep:      "# Deep\n",
	})
	inv := mustScan(t, root, Options{})
	for _, d := range inv.Documents {
		if d.Path == deep {
			t.Fatal("depth limit not enforced")
		}
	}
	if !inv.Partial {
		t.Fatal("depth skip must set Partial")
	}
	found := false
	for _, s := range inv.Skipped {
		if s.Reason == "depth limit reached" {
			found = true
		}
	}
	if !found {
		t.Fatalf("depth limit reason missing: %+v", inv.Skipped)
	}
	// Shallow nesting still works.
	shallow := "docs/plans/a/b/c/d.md"
	root2 := writeRepo(t, map[string]string{statePath: "# State\n", shallow: "# OK\n"})
	inv2 := mustScan(t, root2, Options{})
	findDoc(t, inv2, shallow)
}
