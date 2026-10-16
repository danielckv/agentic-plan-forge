//go:build client && !server

// Package ast grounds the planning avatars in the repository. It parses source
// files with Tree-sitter, extracts symbols, imports and route registrations
// through declarative queries, selects files through git without ever running
// repository-controlled code, and compares README and docs claims against the
// resulting symbol graph.
//
// Every Tree-sitter parser, tree, cursor and query is closed explicitly: the
// bindings warn that finalizers and CGO do not mix.
package ast

import (
	"embed"
	"fmt"
	"path"
	"strings"

	ts "github.com/tree-sitter/go-tree-sitter"
	tsgo "github.com/tree-sitter/tree-sitter-go/bindings/go"
	tsjs "github.com/tree-sitter/tree-sitter-javascript/bindings/go"
	tspy "github.com/tree-sitter/tree-sitter-python/bindings/go"
	tsrs "github.com/tree-sitter/tree-sitter-rust/bindings/go"
	tsts "github.com/tree-sitter/tree-sitter-typescript/bindings/go"
)

//go:embed queries
var queryFS embed.FS

// Language names carried in contracts.Symbol.Language. TypeScript and TSX share
// one name; the file extension distinguishes them.
const (
	LangGo         = "go"
	LangJavaScript = "javascript"
	LangTypeScript = "typescript"
	LangPython     = "python"
	LangRust       = "rust"
)

// langSpec describes one grammar: the query file it loads, the Symbol language
// it reports and the extensions routed to it.
type langSpec struct {
	grammar     string
	name        string
	exts        []string
	newLanguage func() *ts.Language
}

var langSpecs = []langSpec{
	{grammar: "go", name: LangGo, exts: []string{".go"},
		newLanguage: func() *ts.Language { return ts.NewLanguage(tsgo.Language()) }},
	{grammar: "javascript", name: LangJavaScript, exts: []string{".js", ".jsx", ".mjs", ".cjs"},
		newLanguage: func() *ts.Language { return ts.NewLanguage(tsjs.Language()) }},
	{grammar: "typescript", name: LangTypeScript, exts: []string{".ts", ".mts", ".cts"},
		newLanguage: func() *ts.Language { return ts.NewLanguage(tsts.LanguageTypescript()) }},
	{grammar: "tsx", name: LangTypeScript, exts: []string{".tsx"},
		newLanguage: func() *ts.Language { return ts.NewLanguage(tsts.LanguageTSX()) }},
	{grammar: "python", name: LangPython, exts: []string{".py"},
		newLanguage: func() *ts.Language { return ts.NewLanguage(tspy.Language()) }},
	{grammar: "rust", name: LangRust, exts: []string{".rs"},
		newLanguage: func() *ts.Language { return ts.NewLanguage(tsrs.Language()) }},
}

// extToGrammar routes a lower-case extension to its grammar.
var extToGrammar = func() map[string]string {
	m := map[string]string{}
	for _, s := range langSpecs {
		for _, e := range s.exts {
			m[e] = s.grammar
		}
	}
	return m
}()

// otherSourceExts are source-code extensions for languages this scanner does
// not parse. Files with these extensions are counted as unsupported, and
// documentation drift treats their presence as a coverage gap because a
// symbol the docs mention might be defined in them.
var otherSourceExts = map[string]bool{
	".java": true, ".kt": true, ".kts": true, ".scala": true, ".c": true, ".h": true,
	".cc": true, ".cpp": true, ".cxx": true, ".hpp": true, ".cs": true, ".rb": true,
	".php": true, ".swift": true, ".m": true, ".mm": true, ".dart": true, ".lua": true,
	".ex": true, ".exs": true, ".erl": true, ".hs": true, ".clj": true, ".zig": true,
	".vue": true, ".svelte": true,
}

// LanguageForPath reports the Symbol language for a source path, and whether the
// extension is one the scanner parses. Routing is by extension only and is
// case-insensitive.
func LanguageForPath(p string) (language string, ok bool) {
	g, ok := extToGrammar[strings.ToLower(path.Ext(p))]
	if !ok {
		return "", false
	}
	for _, s := range langSpecs {
		if s.grammar == g {
			return s.name, true
		}
	}
	return "", false
}

// captureRole is what one query capture name means to the extractor.
type captureRole struct {
	// kind is set for @def.<kind> captures.
	kind string
	// field is the capture name for every other capture.
	field string
}

var (
	// defKinds are the declaration kinds a @def.<kind> capture may carry.
	defKinds = map[string]bool{
		"function": true, "method": true, "struct": true, "interface": true, "class": true,
		"type": true, "enum": true, "trait": true, "const": true, "var": true, "impl": true,
	}
	// captureFields are the non-declaration capture names queries may use.
	captureFields = map[string]bool{
		"name": true, "owner": true, "trait": true, "type": true, "value": true, "decorated": true,
		"import": true, "route.call": true, "route.recv": true, "route.method": true,
		"route.path": true, "route.args": true, "route.arg2": true,
	}
)

// compiledLang is a grammar with its compiled query.
type compiledLang struct {
	spec  *langSpec
	lang  *ts.Language
	query *ts.Query
	roles []captureRole
}

// languageSet holds every compiled grammar. Close releases the queries.
type languageSet struct {
	byGrammar map[string]*compiledLang
}

// compileLanguages compiles every embedded query. Any failure — a missing query
// file, a syntax error, a grammar ABI the runtime rejects, or a capture name
// the extractor does not understand — fails the whole scan loudly rather than
// producing a graph with silent holes.
func compileLanguages() (*languageSet, error) {
	set := &languageSet{byGrammar: map[string]*compiledLang{}}
	for i := range langSpecs {
		spec := &langSpecs[i]
		src, err := queryFS.ReadFile("queries/" + spec.grammar + ".scm")
		if err != nil {
			set.Close()
			return nil, fmt.Errorf("ast: read %s query: %w", spec.grammar, err)
		}
		lang := spec.newLanguage()
		probe := ts.NewParser()
		serr := probe.SetLanguage(lang)
		probe.Close()
		if serr != nil {
			set.Close()
			return nil, fmt.Errorf("ast: grammar %s is incompatible with the runtime: %w", spec.grammar, serr)
		}
		q, qerr := ts.NewQuery(lang, string(src))
		if qerr != nil {
			set.Close()
			return nil, fmt.Errorf("ast: compile %s query: %v", spec.grammar, qerr)
		}
		roles, err := rolesFor(q)
		if err != nil {
			q.Close()
			set.Close()
			return nil, fmt.Errorf("ast: %s query: %w", spec.grammar, err)
		}
		set.byGrammar[spec.grammar] = &compiledLang{spec: spec, lang: lang, query: q, roles: roles}
	}
	return set, nil
}

// rolesFor maps every capture index of a compiled query to its role.
func rolesFor(q *ts.Query) ([]captureRole, error) {
	names := q.CaptureNames()
	roles := make([]captureRole, len(names))
	for i, n := range names {
		switch {
		case strings.HasPrefix(n, "def."):
			kind := strings.TrimPrefix(n, "def.")
			if !defKinds[kind] {
				return nil, fmt.Errorf("unknown declaration capture @%s", n)
			}
			roles[i] = captureRole{kind: kind}
		case captureFields[n]:
			roles[i] = captureRole{field: n}
		default:
			return nil, fmt.Errorf("unknown capture @%s", n)
		}
	}
	return roles, nil
}

// Close releases every compiled query.
func (s *languageSet) Close() {
	for _, cl := range s.byGrammar {
		cl.query.Close()
	}
	s.byGrammar = nil
}

// forPath returns the compiled grammar for a path, or nil when unsupported.
func (s *languageSet) forPath(p string) *compiledLang {
	g, ok := extToGrammar[strings.ToLower(path.Ext(p))]
	if !ok {
		return nil
	}
	return s.byGrammar[g]
}
