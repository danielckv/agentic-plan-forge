//go:build client && !server

package ast

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// scanTree scans a tree the test wrote. The tree is not a Git work tree, so the
// scanner walks it with the root .gitignore, which is what the evidence rules
// apply to either way.
func scanTree(t *testing.T, files map[string]string) *contracts.RepoContextGraph {
	t.Helper()
	root := t.TempDir()
	writeFiles(t, root, files)
	rels := make([]string, 0, len(files))
	for rel := range files {
		rels = append(rels, rel)
	}
	ageFiles(t, root, rels...)
	g, err := Scan(context.Background(), ScanOptions{Root: root, SkipDocDrift: true})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	return g
}

func gapMatching(g *contracts.RepoContextGraph, rel, reason string) bool {
	for _, gap := range g.Coverage.ParseGaps {
		if strings.HasPrefix(gap, rel+": "+reason) {
			return true
		}
	}
	return false
}

func noteMatching(g *contracts.RepoContextGraph, want string) bool {
	for _, n := range g.Coverage.Notes {
		if strings.Contains(n, want) {
			return true
		}
	}
	return false
}

func TestTheClassifierRoutesEveryExtensionToItsEvidenceKind(t *testing.T) {
	cases := map[string]contentKind{
		"main.go":                 contentCode,
		"web/src/App.tsx":         contentCode,
		"scripts/tool.py":         contentCode,
		"app/Main.JAVA":           contentUnsupportedCode,
		"app/view.vue":            contentUnsupportedCode,
		"docs/README.md":          contentMarkdown,
		"notes/MARKDOWN.Markdown": contentMarkdown,
		"docs/logo.svg":           contentSVG,
		"web/public/ICON.SVG":     contentSVG,
		"assets/logo.png":         contentExcluded,
		"docs/images/shot.webp":   contentExcluded,
		"package.json":            contentExcluded,
		"go.mod":                  contentExcluded,
		"docs/style.css":          contentExcluded,
		"tools/tool":              contentExcluded,
		"bin/app":                 contentExcluded,
	}
	for path, want := range cases {
		if got := classifyContent(path); got != want {
			t.Errorf("classifyContent(%q) = %d, want %d", path, got, want)
		}
	}
	if lang, ok := LanguageForPath("main.go"); !ok || lang != LangGo {
		t.Errorf("LanguageForPath(main.go) = %q, %t; the exported routing must not change", lang, ok)
	}
}

func TestAnExcludedFileLeavesTheWorktreeHashAlone(t *testing.T) {
	base := map[string]string{
		"main.go":       "package main\n\nfunc main() {}\n",
		"notes.md":      "# Notes\n\nThe plan lives in main.go.\n",
		"docs/logo.svg": `<svg xmlns="http://www.w3.org/2000/svg"><title>Logo</title></svg>`,
	}
	baseline := scanTree(t, base)

	withBinary := map[string]string{"assets/logo.png": "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"}
	for rel, body := range base {
		withBinary[rel] = body
	}
	got := scanTree(t, withBinary)

	if got.WorktreeHash != baseline.WorktreeHash {
		t.Errorf("WorktreeHash changed when only an excluded PNG was added: got %s, want %s", got.WorktreeHash, baseline.WorktreeHash)
	}
	if got.Coverage.FilesSkipped != baseline.Coverage.FilesSkipped+1 {
		t.Errorf("FilesSkipped = %d, want %d: an excluded path is still counted", got.Coverage.FilesSkipped, baseline.Coverage.FilesSkipped+1)
	}
	if got.Coverage.FilesScanned != baseline.Coverage.FilesScanned {
		t.Errorf("FilesScanned = %d, want %d: an excluded file is never scanned", got.Coverage.FilesScanned, baseline.Coverage.FilesScanned)
	}
	if _, ok := got.Symbols["assets/logo.png"]; ok {
		t.Error("the excluded file reached the symbol map")
	}
}

func TestMarkdownAndSVGChangesInvalidateTheWorktreeHash(t *testing.T) {
	base := map[string]string{
		"main.go":       "package main\n\nfunc main() {}\n",
		"notes.md":      "# Notes\n\nOne.\n",
		"docs/logo.svg": `<svg xmlns="http://www.w3.org/2000/svg"><title>Logo</title></svg>`,
	}
	baseline := scanTree(t, base)

	editedMarkdown := map[string]string{
		"main.go":       "package main\n\nfunc main() {}\n",
		"notes.md":      "# Notes\n\nTwo.\n",
		"docs/logo.svg": base["docs/logo.svg"],
	}
	if got := scanTree(t, editedMarkdown); got.WorktreeHash == baseline.WorktreeHash {
		t.Error("a Markdown edit left WorktreeHash unchanged")
	}

	editedSVG := map[string]string{
		"main.go":       "package main\n\nfunc main() {}\n",
		"notes.md":      base["notes.md"],
		"docs/logo.svg": `<svg xmlns="http://www.w3.org/2000/svg"><rect width="8" height="8"/></svg>`,
	}
	if got := scanTree(t, editedSVG); got.WorktreeHash == baseline.WorktreeHash {
		t.Error("an SVG edit left WorktreeHash unchanged")
	}

	addedElsewhere := map[string]string{"internal/x/guide.md": "# Guide\n"}
	for rel, body := range base {
		addedElsewhere[rel] = body
	}
	if got := scanTree(t, addedElsewhere); got.WorktreeHash == baseline.WorktreeHash {
		t.Error("Markdown outside README and docs/ did not reach the hash")
	}
}

func TestBinaryDisguisedAsMarkdownIsHashedAndSkippedWithAGap(t *testing.T) {
	g := scanTree(t, map[string]string{
		"main.go": "package main\n\nfunc main() {}\n",
		"fake.md": "\xff\xfe not text \x00\n",
		"real.md": "# Real\n\nText.\n",
	})
	if !gapMatching(g, "fake.md", GapTextInvalid) {
		t.Errorf("ParseGaps = %v, want a %s entry for fake.md", g.Coverage.ParseGaps, GapTextInvalid)
	}
	if gapMatching(g, "real.md", GapTextInvalid) {
		t.Errorf("ParseGaps = %v: valid Markdown must not be gapped", g.Coverage.ParseGaps)
	}
	if g.Symbols["fake.md"] != nil {
		t.Error("a skipped text file contributed symbols")
	}
}

func TestAnOversizedSVGIsSkippedWithItsOwnGap(t *testing.T) {
	big := `<svg xmlns="http://www.w3.org/2000/svg"><desc>` +
		strings.Repeat("a", int(maxSVGBytes)+1024) + `</desc></svg>`
	g := scanTree(t, map[string]string{
		"main.go":   "package main\n\nfunc main() {}\n",
		"huge.svg":  big,
		"small.svg": `<svg xmlns="http://www.w3.org/2000/svg"></svg>`,
	})
	if !gapMatching(g, "huge.svg", GapTextTooLarge) {
		t.Errorf("ParseGaps = %v, want a %s entry for huge.svg", g.Coverage.ParseGaps, GapTextTooLarge)
	}
	if gapMatching(g, "small.svg", GapTextTooLarge) {
		t.Errorf("ParseGaps = %v: the small SVG is under the cap", g.Coverage.ParseGaps)
	}
}

func TestSVGNamingAnOutsideResourceIsNeverResolved(t *testing.T) {
	cases := map[string]string{
		"linked.svg":   `<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink"><image xlink:href="https://example.invalid/remote.png"/></svg>`,
		"doctype.svg":  `<?xml version="1.0"?><!DOCTYPE svg SYSTEM "https://example.invalid/svg.dtd"><svg/>`,
		"entity.svg":   `<?xml version="1.0"?><!DOCTYPE svg [<!ENTITY web SYSTEM "https://example.invalid/x">]><svg>&web;</svg>`,
		"scripted.svg": `<svg xmlns="http://www.w3.org/2000/svg"><script>fetch("https://example.invalid")</script></svg>`,
	}
	files := map[string]string{"main.go": "package main\n\nfunc main() {}\n"}
	for rel, body := range cases {
		files[rel] = body
	}
	g := scanTree(t, files)
	for rel := range cases {
		if got := classifyContent(rel); got != contentSVG {
			t.Fatalf("classifyContent(%q) = %d, want contentSVG", rel, got)
		}
		if !gapMatching(g, rel, GapTextExternalRef) {
			t.Errorf("ParseGaps = %v, want a %s entry for %s", g.Coverage.ParseGaps, GapTextExternalRef, rel)
		}
	}
	if !noteMatching(g, "hashed but not read as text") {
		t.Errorf("Coverage.Notes = %v, want a note about skipped SVG text", g.Coverage.Notes)
	}
}

func TestUnsupportedSourceIsCountedWithoutSymbols(t *testing.T) {
	g := scanTree(t, map[string]string{
		"main.go":       "package main\n\nfunc main() {}\n",
		"app/Main.java": "public final class Main { public static void main(String[] a) {} }\n",
	})
	if g.Coverage.UnsupportedFiles != 1 {
		t.Errorf("UnsupportedFiles = %d, want 1", g.Coverage.UnsupportedFiles)
	}
	if found := hasString(symbolNames(g.Symbols["app/Main.java"]), "Main"); found {
		t.Error("symbols were claimed from an unsupported language")
	}
	if g.Symbols["app/Main.java"] != nil {
		t.Error("an unsupported language file reached the symbol map")
	}
}

func TestDetectStackReadsNoManifestContent(t *testing.T) {
	manifest := `{"name":"app","dependencies":{"react":"19","vite":"8"}}`
	g := scanTree(t, map[string]string{
		"web/package.json": manifest,
		"go.mod":           "module example.invalid/app\n",
		"main.go":          "package main\n\nfunc main() {}\n",
	})
	got := strings.Join(g.DetectedStack, ",")
	for _, want := range []string{"go-modules", "node"} {
		if !strings.Contains(got, want) {
			t.Errorf("DetectedStack = %v, want %s from the file name alone", g.DetectedStack, want)
		}
	}
	for _, banned := range []string{"react", "vite"} {
		if strings.Contains(got, banned) {
			t.Errorf("DetectedStack = %v: %s can only be known by reading the manifest", g.DetectedStack, banned)
		}
	}
	if !noteMatching(g, "inferred") {
		t.Errorf("Coverage.Notes = %v, want the stack marked inferred", g.Coverage.Notes)
	}
}

func TestTheExcludedCountIsStatedInTheNotes(t *testing.T) {
	g := scanTree(t, map[string]string{
		"main.go":         "package main\n\nfunc main() {}\n",
		"assets/logo.png": "\x89PNG\r\n\x1a\n",
	})
	if !noteMatching(g, "keeps source code, Markdown and SVG only") {
		t.Errorf("Coverage.Notes = %v, want the excluded count stated", g.Coverage.Notes)
	}
}

func TestTheExtractorVersionInvalidatesOldCaches(t *testing.T) {
	if extractorVersion < 2 {
		t.Fatalf("extractorVersion = %d, want at least 2: the hashing semantics changed", extractorVersion)
	}
}

// TestACachedScanKeepsTheSameEvidence scans twice with the cache enabled. The
// second run must reach the same hash, the same text evidence and the same gap
// for a rejected SVG, so a cached text verdict is not lost.
func TestACachedScanKeepsTheSameEvidence(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"main.go":         "package main\n\nfunc main() {}\n",
		"notes.md":        "# Notes\n\nOne.\n",
		"logo.svg":        `<svg xmlns="http://www.w3.org/2000/svg"><title>Logo</title></svg>`,
		"linked.svg":      `<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink"><image xlink:href="https://example.invalid/x.png"/></svg>`,
		"fake.md":         "\xff\xfe\x00 binary",
		"assets/logo.png": "\x89PNG\r\n\x1a\n",
	}
	writeFiles(t, root, files)
	rels := make([]string, 0, len(files))
	for rel := range files {
		rels = append(rels, rel)
	}
	ageFiles(t, root, rels...)

	opts := ScanOptions{Root: root, SkipDocDrift: true, ReadCache: true, WriteCache: true}
	first, err := Scan(context.Background(), opts)
	if err != nil {
		t.Fatalf("first Scan: %v", err)
	}
	second, err := Scan(context.Background(), opts)
	if err != nil {
		t.Fatalf("second Scan: %v", err)
	}
	if first.WorktreeHash != second.WorktreeHash {
		t.Errorf("WorktreeHash = %s on the cached run, want %s", second.WorktreeHash, first.WorktreeHash)
	}
	if first.Coverage.FilesScanned != second.Coverage.FilesScanned || first.Coverage.FilesSkipped != second.Coverage.FilesSkipped {
		t.Errorf("coverage = %d scanned/%d skipped on the cached run, want %d/%d",
			second.Coverage.FilesScanned, second.Coverage.FilesSkipped, first.Coverage.FilesScanned, first.Coverage.FilesSkipped)
	}
	for _, rel := range []string{"linked.svg", "fake.md"} {
		if !gapMatching(second, rel, GapTextExternalRef) && !gapMatching(second, rel, GapTextInvalid) {
			t.Errorf("ParseGaps = %v, want the text gap for %s restored from the cache", second.Coverage.ParseGaps, rel)
		}
	}
}

// TestIgnoredAndSymlinkedPathsStayOut confirms the evidence restriction did not
// relax the older protections: an ignored path is not listed, and a symbolic
// link is counted and skipped.
func TestIgnoredAndSymlinkedPathsStayOut(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		".gitignore":   "secret.md\n*.log\n",
		"main.go":      "package main\n\nfunc main() {}\n",
		"secret.md":    "# Secret\n",
		"notes.md":     "# Notes\n",
		"assets/a.png": "\x89PNG\r\n\x1a\n",
	})
	ageFiles(t, root, ".gitignore", "main.go", "secret.md", "notes.md", "assets/a.png")
	if err := os.Symlink(filepath.Join(root, "notes.md"), filepath.Join(root, "link.md")); err != nil {
		t.Skipf("symbolic links are unavailable: %v", err)
	}

	g, err := Scan(context.Background(), ScanOptions{Root: root, SkipDocDrift: true})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	for _, rel := range []string{"secret.md", "link.md", "assets/a.png"} {
		if _, ok := g.Symbols[rel]; ok {
			t.Errorf("%s reached the symbol map", rel)
		}
	}
	if g.Coverage.FilesSkipped == 0 {
		t.Error("FilesSkipped = 0, want the ignored and linked paths counted")
	}
}

// TestMarkdownIsReadOutsideDocs confirms a Markdown file in any directory is
// text evidence, not only README and docs/.
func TestMarkdownIsReadOutsideDocs(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"main.go":                   "package main\n\nfunc main() {}\n",
		"internal/billing/GUIDE.md": "# Billing\n\nThe `Charge` call lives in main.go.\n",
	})
	ageFiles(t, root, "main.go", "internal/billing/GUIDE.md")
	g, err := Scan(context.Background(), ScanOptions{Root: root})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if !isDocPath("internal/billing/GUIDE.md") {
		t.Error("a Markdown file outside docs/ is not read as documentation")
	}
	if !noteMatching(g, "contributed text evidence") {
		t.Errorf("Coverage.Notes = %v, want the text evidence count", g.Coverage.Notes)
	}
}
