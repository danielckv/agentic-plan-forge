//go:build client && !server

package app

import (
	"os"
	"path/filepath"
	"testing"
)

// The work view is the window's only reader of the repository's planning files.
// These tests pin the contract the page relies on: a real inventory, relative
// paths only, and an open that refuses content that changed underneath it.
func TestRepositoryWorkReadsThePlanningFiles(t *testing.T) {
	repo := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		path := filepath.Join(repo, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".agents/STATE.md", "# State\n\n## Next\n\n- first thing\n- second thing\n")
	write("docs/plans/a.md", "# A plan\n\n- [ ] open task\n- [x] done task\n")
	write("FOLLOW_UPS.md", "# Follow ups\n\n- a follow-up\n")

	s := &appSession{repo: repo}
	inv := s.RepositoryWork(t.Context())
	if len(inv.Documents) == 0 {
		t.Fatal("the work view found no documents")
	}
	if len(inv.Next) == 0 {
		t.Fatal("the active state file's next items are missing")
	}
	for _, doc := range inv.Documents {
		if filepath.IsAbs(doc.Path) {
			t.Errorf("document path %q is absolute", doc.Path)
		}
		if doc.Path == "" {
			t.Error("a document has no path")
		}
	}
}

// A document that changed since the inventory was taken is refused, so the page
// never shows stale bytes under a fresh heading.
func TestRepositoryWorkRefusesAChangedDocument(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, "docs", "plans", "a.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("# A plan\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &appSession{repo: repo}
	inv := s.RepositoryWork(t.Context())
	if len(inv.Documents) == 0 {
		t.Fatal("no document to open")
	}
	id := inv.Documents[0].ID
	if err := os.WriteFile(path, []byte("# A plan, rewritten\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.RepositoryWorkDocument(t.Context(), id); err == nil {
		t.Fatal("a changed document was served under its old identity")
	}
}

// A session with no repository answers with an empty view, never an error page.
func TestRepositoryWorkWithoutARepository(t *testing.T) {
	inv := (&appSession{}).RepositoryWork(t.Context())
	if len(inv.Documents) != 0 {
		t.Errorf("a session with no repository reported %d documents", len(inv.Documents))
	}
}
