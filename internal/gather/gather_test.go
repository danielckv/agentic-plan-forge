//go:build client && !server

package gather

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// newRepo returns a symlink-free Git repository with one commit.
func newRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	write(t, repo, "README.md", "# demo\n")
	git("add", "README.md")
	git("commit", "-q", "-m", "Add invoice export skeleton")
	return repo
}

func write(t *testing.T, repo, rel, body string) {
	t.Helper()
	p := filepath.Join(repo, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// makeIndex writes a recall database. scoped selects the current schema (with
// a worktree column); otherwise the legacy one. rows are worktree, path, text.
func makeIndex(t *testing.T, path string, scoped bool, rows [][3]string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ddl := "CREATE VIRTUAL TABLE chunks USING fts5(path UNINDEXED, text, tokenize='porter unicode61')"
	if scoped {
		ddl = "CREATE VIRTUAL TABLE chunks USING fts5(worktree UNINDEXED, path UNINDEXED, text, tokenize='porter unicode61')"
	}
	if _, err := db.Exec(ddl); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if scoped {
			_, err = db.Exec("INSERT INTO chunks (worktree, path, text) VALUES (?, ?, ?)", r[0], r[1], r[2])
		} else {
			_, err = db.Exec("INSERT INTO chunks (path, text) VALUES (?, ?)", r[1], r[2])
		}
		if err != nil {
			t.Fatal(err)
		}
	}
}

func collect(t *testing.T, repo, text string) contracts.ProjectContext {
	t.Helper()
	got, err := Collect(context.Background(), Options{Repo: repo, Text: text})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	return got
}

const requirements = "Export invoices as CSV. Invoices export must include tax lines. Finance exports monthly."

func TestCollectReadsTheRecallIndexForThisWorktreeOnly(t *testing.T) {
	repo := newRepo(t)
	wt := filepath.Base(repo)
	makeIndex(t, filepath.Join(repo, ".git", "agents-local", "recall.db"), true, [][3]string{
		{wt, "docs/adrs/0004-invoice-export.md", "# Invoice export\n\nInvoices export to CSV with tax lines, decided 2026-09-01."},
		{wt, ".agents/STATE.md", "Lane billing: refunds are blocked on the ledger rewrite."},
		{"another-worktree", "docs/plans/other.md", "Invoices export to PDF in the other worktree."},
	})

	got := collect(t, repo, requirements)
	if got.Source != contracts.ContextSourceRecall {
		t.Fatalf("source = %q, want %q (notes %v)", got.Source, contracts.ContextSourceRecall, got.Notes)
	}
	if len(got.Hits) != 1 || got.Hits[0].Path != "docs/adrs/0004-invoice-export.md" {
		t.Fatalf("hits = %+v, want only this worktree's invoice ADR", got.Hits)
	}
	if got.Hits[0].Heading != "Invoice export" || got.Hits[0].Score <= 0 {
		t.Errorf("hit = %+v, want heading and a positive score", got.Hits[0])
	}
	if got.Branch != "main" || len(got.Head) != 12 || len(got.Recent) != 1 || got.Recent[0] != "Add invoice export skeleton" {
		t.Errorf("git state = branch %q head %q recent %v", got.Branch, got.Head, got.Recent)
	}
}

// Plan Forge only reads the index another tool owns: the file is
// byte-identical after a read.
func TestCollectLeavesTheRecallIndexUnchanged(t *testing.T) {
	repo := newRepo(t)
	db := filepath.Join(repo, ".git", "agents-local", "recall.db")
	makeIndex(t, db, true, [][3]string{{filepath.Base(repo), "docs/plans/p.md", "Invoices export as CSV."}})
	before, err := os.ReadFile(db)
	if err != nil {
		t.Fatal(err)
	}
	if got := collect(t, repo, requirements); got.Source != contracts.ContextSourceRecall || len(got.Hits) != 1 {
		t.Fatalf("context = %+v", got)
	}
	after, err := os.ReadFile(db)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("reading the recall index changed the file")
	}
	for _, side := range []string{"-wal", "-journal"} {
		if _, err := os.Stat(db + side); err == nil {
			t.Errorf("reading the recall index created %s", filepath.Base(db+side))
		}
	}
}

func TestCollectFallsBackToTheLegacyIndex(t *testing.T) {
	repo := newRepo(t)
	// A current index with rows for other worktrees only does not answer.
	makeIndex(t, filepath.Join(repo, ".git", "agents-local", "recall.db"), true, [][3]string{
		{"another-worktree", "docs/plans/other.md", "Invoices export elsewhere."},
	})
	makeIndex(t, filepath.Join(repo, ".agents", "index", "recall.db"), false, [][3]string{
		{"", "docs/plans/billing.md", "## Billing\n\nInvoices export nightly as CSV."},
	})

	got := collect(t, repo, requirements)
	if got.Source != contracts.ContextSourceLegacyRecall {
		t.Fatalf("source = %q, want %q (notes %v)", got.Source, contracts.ContextSourceLegacyRecall, got.Notes)
	}
	if len(got.Hits) != 1 || got.Hits[0].Path != "docs/plans/billing.md" || got.Hits[0].Heading != "Billing" {
		t.Fatalf("hits = %+v", got.Hits)
	}
}

func TestCollectReadsFilesWhenNoIndexAnswers(t *testing.T) {
	repo := newRepo(t)
	write(t, repo, ".agents/STATE.md", "# State\n\nnext: ship invoices export with tax lines\n")
	write(t, repo, "docs/adrs/0002-csv.md", "# ADR 2: CSV\n\n## Decision\n\nInvoices export as CSV, never XLSX.\n")
	write(t, repo, "docs/specs/unrelated.md", "# Search\n\nThe search box ranks results.\n")
	// A corrupt index is reported and skipped, not fatal.
	write(t, repo, ".agents/index/recall.db", "not a database")

	got := collect(t, repo, requirements)
	if got.Source != contracts.ContextSourceFiles {
		t.Fatalf("source = %q, want %q", got.Source, contracts.ContextSourceFiles)
	}
	paths := map[string]string{}
	for _, h := range got.Hits {
		paths[h.Path] = h.Heading
	}
	if len(paths) != 2 || paths["docs/adrs/0002-csv.md"] != "ADR 2: CSV" || paths[".agents/STATE.md"] != "State" {
		t.Errorf("hits = %+v, want the ADR and STATE.md under their titles", got.Hits)
	}
	if len(got.Notes) == 0 || !strings.Contains(got.Notes[0], "legacy recall index unreadable") {
		t.Errorf("notes = %v, want the corrupt legacy index reported", got.Notes)
	}
}

func TestCollectWithoutSourcesOrText(t *testing.T) {
	repo := newRepo(t)
	if got := collect(t, repo, requirements); got.Source != contracts.ContextSourceNone || len(got.Hits) != 0 {
		t.Errorf("empty repo = %+v, want source none and no hits", got)
	}
	write(t, repo, "docs/adrs/0001.md", "# Invoices export\n")
	got := collect(t, repo, "  ")
	if got.Source != contracts.ContextSourceNone || len(got.Hits) != 0 || got.Branch != "main" {
		t.Errorf("no text = %+v, want git state only", got)
	}
	if _, err := Collect(context.Background(), Options{}); err == nil {
		t.Error("an empty repository path must be refused")
	}
}

func TestCollectNeverLeaksMachinePaths(t *testing.T) {
	repo := newRepo(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	write(t, repo, "docs/plans/p.md",
		"# Invoices export\n\nSee "+filepath.Join(repo, "internal", "billing")+" and "+filepath.Join(home, "notes.md")+" for invoices export.\n")
	got := collect(t, repo, requirements)
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), repo) || strings.Contains(string(raw), home+string(filepath.Separator)) {
		t.Errorf("an absolute path survived: %s", raw)
	}
	if len(got.Hits) != 1 || !strings.Contains(got.Hits[0].Text, "internal/billing") || !strings.Contains(got.Hits[0].Text, "~/notes.md") {
		t.Errorf("hits = %+v, want the paths rewritten, not dropped", got.Hits)
	}
}

func TestCollectSkipsSymlinksOutOfTheRepository(t *testing.T) {
	repo := newRepo(t)
	outside := filepath.Join(t.TempDir(), "secret.md")
	if err := os.WriteFile(outside, []byte("# Invoices export from outside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "docs", "plans"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(repo, "docs", "plans", "link.md")); err != nil {
		t.Skip("symlinks unavailable")
	}
	write(t, repo, "docs/plans/real.md", "# Invoices export\n")
	got := collect(t, repo, requirements)
	for _, h := range got.Hits {
		if h.Path == "docs/plans/link.md" {
			t.Fatalf("a symlink was followed: %+v", got.Hits)
		}
	}
	if len(got.Hits) != 1 {
		t.Errorf("hits = %+v, want only the real file", got.Hits)
	}
}

func TestCollectStaysWithinTheBudget(t *testing.T) {
	repo := newRepo(t)
	para := strings.Repeat("Invoices export with tax lines for finance. ", 25)
	for i := range 20 {
		write(t, repo, filepath.Join("docs", "plans", "p"+string(rune('a'+i))+".md"), "# Plan\n\n"+para+"\n\n"+para)
	}
	const budget = 8 << 10
	got, err := Collect(context.Background(), Options{Repo: repo, Text: requirements, Budget: budget, MaxHits: 50})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(got)
	if len(raw) > budget || !got.Truncated || len(got.Hits) == 0 {
		t.Errorf("size %d (budget %d), truncated %v, hits %d", len(raw), budget, got.Truncated, len(got.Hits))
	}
	for _, h := range got.Hits {
		if r := []rune(h.Text); len(r) > chunkChars+1 {
			t.Errorf("excerpt of %d runes exceeds %d", len(r), chunkChars)
		}
	}
}

func TestQueryTermsRanksByFrequency(t *testing.T) {
	got := QueryTerms("Invoices export. Export invoices. Export CSV with the tax lines.", 3)
	want := []string{"export", "invoices", "lines"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("QueryTerms = %v, want %v", got, want)
	}
	if QueryTerms("a an the", 5) != nil {
		t.Error("stop words alone must give no terms")
	}
	if q := matchQuery([]string{"one", `t"wo`}); q != `"one" OR "two"` {
		t.Errorf("matchQuery = %s", q)
	}
}
