//go:build client && !server

package ast

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// goldenSymbol is one expected symbol. A nil Signature is not compared: it
// marks declarations whose exact header text is not part of the contract.
type goldenSymbol struct {
	QualifiedName string  `json:"qualified_name"`
	Kind          string  `json:"kind"`
	Visibility    string  `json:"visibility"`
	StartLine     int     `json:"start_line"`
	EndLine       int     `json:"end_line"`
	Signature     *string `json:"signature"`
}

type goldenRoute struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Line   int    `json:"line"`
}

type golden struct {
	File    string         `json:"file"`
	Symbols []goldenSymbol `json:"symbols"`
	Routes  []goldenRoute  `json:"routes"`
	Imports []string       `json:"imports"`
}

func loadGolden(t *testing.T, dir string) (golden, []byte) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", dir, "expected-symbols.json"))
	if err != nil {
		t.Fatal(err)
	}
	var g golden
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatalf("%s/expected-symbols.json: %v", dir, err)
	}
	src, err := os.ReadFile(filepath.Join("testdata", dir, g.File))
	if err != nil {
		t.Fatal(err)
	}
	return g, src
}

func TestSymbolExtractionMatchesGoldenPerLanguage(t *testing.T) {
	for _, dir := range []string{"go", "javascript", "typescript", "tsx", "python", "rust"} {
		t.Run(dir, func(t *testing.T) {
			g, src := loadGolden(t, dir)
			ex, errNodes := parseFixture(t, g.File, src)
			if errNodes != 0 {
				t.Fatalf("fixture has %d syntax error nodes", errNodes)
			}
			wantLang, ok := LanguageForPath(g.File)
			if !ok {
				t.Fatalf("no language for %s", g.File)
			}

			if len(ex.Symbols) != len(g.Symbols) {
				t.Fatalf("got %d symbols %v, want %d", len(ex.Symbols), symbolNames(ex.Symbols), len(g.Symbols))
			}
			ids := map[string]bool{}
			for i, want := range g.Symbols {
				got := ex.Symbols[i]
				if got.QualifiedName != want.QualifiedName || got.Kind != want.Kind || got.Visibility != want.Visibility ||
					got.StartLine != want.StartLine || got.EndLine != want.EndLine {
					t.Errorf("symbol %d = {%s %s %q lines %d-%d}, want {%s %s %q lines %d-%d}", i,
						got.QualifiedName, got.Kind, got.Visibility, got.StartLine, got.EndLine,
						want.QualifiedName, want.Kind, want.Visibility, want.StartLine, want.EndLine)
				}
				if want.Signature != nil && got.Signature != *want.Signature {
					t.Errorf("symbol %s signature = %q, want %q", want.QualifiedName, got.Signature, *want.Signature)
				}
				if got.Path != g.File || got.Language != wantLang {
					t.Errorf("symbol %s path/language = %s/%s, want %s/%s", want.QualifiedName, got.Path, got.Language, g.File, wantLang)
				}
				if got.ID == "" || ids[got.ID] {
					t.Errorf("symbol %s has an empty or duplicate ID %q", want.QualifiedName, got.ID)
				}
				ids[got.ID] = true
				if got.Signature != "" {
					sum := sha256.Sum256([]byte(got.Signature))
					if got.SignatureSHA256 != hex.EncodeToString(sum[:]) {
						t.Errorf("symbol %s signature hash does not match its signature", want.QualifiedName)
					}
				}
				if got.Name == "" || got.Evidence == "" {
					t.Errorf("symbol %s lacks a name or evidence: %+v", want.QualifiedName, got)
				}
			}

			if len(ex.Routes) != len(g.Routes) {
				t.Fatalf("got %d routes %+v, want %d", len(ex.Routes), ex.Routes, len(g.Routes))
			}
			for i, want := range g.Routes {
				got := ex.Routes[i]
				if got.Method != want.Method || got.Path != want.Path || got.Line != want.Line || got.Evidence != EvidenceRoute || got.FilePath != g.File {
					t.Errorf("route %d = %+v, want %+v with route_registration evidence", i, got, want)
				}
			}

			if !reflect.DeepEqual(nonNilStrings(ex.Imports), nonNilStrings(g.Imports)) {
				t.Errorf("imports = %v, want %v", ex.Imports, g.Imports)
			}
		})
	}
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func TestPythonDecoratorsAreKeptAndStartTheDeclaration(t *testing.T) {
	g, src := loadGolden(t, "python")
	ex, _ := parseFixture(t, g.File, src)
	for _, s := range ex.Symbols {
		if s.QualifiedName != "list_items" {
			continue
		}
		if s.StartLine != 21 || s.EndLine != 23 {
			t.Errorf("lines = %d-%d, want 21-23 (the decorator line starts the declaration)", s.StartLine, s.EndLine)
		}
		if s.Evidence != EvidenceDeclaration+"; decorators=@app.get" {
			t.Errorf("evidence = %q, want the decorator name recorded", s.Evidence)
		}
		return
	}
	t.Fatal("list_items was not extracted")
}

// TestSyntaxErrorYieldsGapAndKeepsValidSymbols pins the contract for a file
// with a syntax error: the scanner reports a PARSE_ERROR coverage gap for the
// file and still lists the symbols of the valid subtrees. A declaration that
// sits inside an error node is dropped when the grammar's recovery leaves no
// well-formed declaration node (extractor.addSymbol skips ERROR and MISSING
// nodes), but error recovery may also keep a partial declaration, so the test
// does not assert either outcome for Broken. Coverage gaps never mean "symbol
// absent".
func TestSyntaxErrorYieldsGapAndKeepsValidSymbols(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("testdata", "errors", "broken.go"))
	if err != nil {
		t.Fatal(err)
	}
	ex, errNodes := parseFixture(t, "broken.go", src)
	if errNodes == 0 {
		t.Fatal("broken.go parsed without error nodes")
	}
	names := symbolNames(ex.Symbols)
	if !hasString(names, "Valid") {
		t.Errorf("symbols = %v, want Valid listed despite the later syntax error", names)
	}

	// End to end: the scan records the file in Coverage.ParseGaps and keeps
	// the valid symbols in the graph.
	isolateGit(t)
	root := t.TempDir()
	writeFiles(t, root, map[string]string{"broken.go": string(src)})
	ageFiles(t, root, "broken.go")
	g, err := Scan(context.Background(), ScanOptions{Root: root, SkipDocDrift: true})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	gapped := false
	for _, gap := range g.Coverage.ParseGaps {
		if strings.HasPrefix(gap, "broken.go: "+GapParseError) {
			gapped = true
		}
	}
	if !gapped {
		t.Errorf("ParseGaps = %v, want a %s entry for broken.go", g.Coverage.ParseGaps, GapParseError)
	}
	if !hasString(symbolNames(g.Symbols["broken.go"]), "Valid") {
		t.Errorf("graph symbols = %v, want Valid listed", symbolNames(g.Symbols["broken.go"]))
	}
}

func TestLanguageRouting(t *testing.T) {
	cases := map[string]struct {
		lang string
		ok   bool
	}{
		"a.go": {LangGo, true}, "a.js": {LangJavaScript, true}, "a.jsx": {LangJavaScript, true},
		"a.mjs": {LangJavaScript, true}, "a.cjs": {LangJavaScript, true},
		"a.ts": {LangTypeScript, true}, "a.mts": {LangTypeScript, true}, "a.cts": {LangTypeScript, true},
		"a.tsx": {LangTypeScript, true}, "a.py": {LangPython, true}, "a.rs": {LangRust, true},
		"A.GO": {LangGo, true}, "a.java": {"", false}, "a.md": {"", false}, "Makefile": {"", false},
	}
	for p, want := range cases {
		lang, ok := LanguageForPath(p)
		if lang != want.lang || ok != want.ok {
			t.Errorf("LanguageForPath(%q) = (%q, %v), want (%q, %v)", p, lang, ok, want.lang, want.ok)
		}
	}
}

func TestEverySymbolKindIsAContractKind(t *testing.T) {
	known := map[string]bool{
		contracts.SymbolFunction: true, contracts.SymbolMethod: true, contracts.SymbolStruct: true,
		contracts.SymbolInterface: true, contracts.SymbolClass: true, contracts.SymbolType: true,
		contracts.SymbolEnum: true, contracts.SymbolTrait: true, contracts.SymbolConstant: true,
		contracts.SymbolVariable: true, KindImpl: true,
	}
	for _, dir := range []string{"go", "javascript", "typescript", "tsx", "python", "rust"} {
		g, src := loadGolden(t, dir)
		ex, _ := parseFixture(t, g.File, src)
		for _, s := range ex.Symbols {
			if !known[s.Kind] {
				t.Errorf("%s: symbol %s has unknown kind %q", dir, s.QualifiedName, s.Kind)
			}
		}
	}
}
