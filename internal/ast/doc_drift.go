//go:build client && !server

package ast

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// Evidence kinds recorded in contracts.DocDriftItem.EvidenceKind.
const (
	// EvidenceDocSignature marks a documented call or signature compared with the
	// declaration the syntax tree holds today.
	EvidenceDocSignature = "doc_signature_vs_ast"
	// EvidenceDocDeclaration marks a documented declaration, or a documented
	// member of a known type, for which the syntax tree has no match.
	EvidenceDocDeclaration = "doc_declaration_vs_ast"
	// EvidenceDocPath marks a documented source path compared with the file list.
	EvidenceDocPath = "doc_path_vs_file_list"
	// EvidenceDocRoute marks a documented HTTP route compared with the route
	// registrations found in code.
	EvidenceDocRoute = "doc_route_vs_route_registration"
	// EvidenceUndocumented marks an exported symbol or route the doc never names.
	EvidenceUndocumented = "ast_symbol_absent_from_doc"
	// EvidenceCoverageGap marks a claim that could not be checked because the
	// scan did not cover the code it concerns.
	EvidenceCoverageGap = "coverage_gap"
)

const (
	// MaxMissingDocPerDoc caps the MISSING_DOC items reported for one document.
	MaxMissingDocPerDoc = 50
	// maxUnverifiablePerDoc caps the UNVERIFIABLE items reported for one document.
	maxUnverifiablePerDoc = 20
)

// DocFile is one documentation file offered to drift detection.
type DocFile struct {
	// Path is the repository-relative path with forward slashes.
	Path string
	// Content is the file text. It is treated as untrusted Markdown.
	Content []byte
}

// DriftInput is everything DetectDocDrift needs. It performs no file access.
type DriftInput struct {
	// Docs are README.md, ARCHITECTURE.md and docs/**/*.md, already selected
	// with the scanner's hard excludes applied.
	Docs []DocFile
	// Graph is the scanned repository. Its Coverage decides which claims are
	// refutable and which are only UNVERIFIABLE.
	Graph *contracts.RepoContextGraph
	// Files lists every repository-relative path considered part of the
	// repository, in the same selection the scan used. Path claims are checked
	// against it.
	Files []string
}

// DetectDocDrift compares documentation claims with the scanned repository and
// returns the advisory findings, ordered by document, line and type.
//
// Claims come from inline code spans and fenced code (symbol shapes and
// calls), from relative path mentions anywhere in the text, and from HTTP route
// mentions. Claims that match no rule are not reported. The rules are:
//
//   - STALE_SIGNATURE: the doc shows a call or signature of a function or method
//     the graph defines, but the parameter count differs (call form) or the
//     parameter count or names differ (signature form such as `func Foo(a int)`).
//   - MISSING_DOC: an exported symbol or route lives in a module the doc
//     describes (the doc names the module path) and the doc never mentions it;
//     at most MaxMissingDocPerDoc items per document, confidence MEDIUM.
//   - DEPRECATED_FLOW: the doc references a source path, an HTTP route, a
//     documented declaration or a member of a known type that no longer exists.
//     When coverage gaps exist for the module the doc concerns, the finding is
//     reported with DriftType UNVERIFIABLE instead: absence is never inferred
//     from an incomplete scan.
//
// Confidence follows extraction provenance: claims taken from inline code or a
// language-tagged fence start at HIGH, claims from prose or untagged fences at
// MEDIUM, and ambiguity (several declarations share the name) lowers a tier.
// Plain calls to names the graph does not define are deliberately not reported:
// they usually target third-party code.
func DetectDocDrift(in DriftInput) []contracts.DocDriftItem {
	items := []contracts.DocDriftItem{}
	if in.Graph == nil {
		return items
	}
	ix := newGraphIndex(in.Graph, in.Files)
	docs := append([]DocFile(nil), in.Docs...)
	sort.SliceStable(docs, func(i, j int) bool { return docs[i].Path < docs[j].Path })
	for _, doc := range docs {
		items = append(items, ix.analyzeDoc(doc)...)
	}
	order := map[string]int{
		contracts.DriftStaleSignature: 0,
		contracts.DriftDeprecatedFlow: 1,
		contracts.DriftMissingDoc:     2,
		contracts.DriftUnverifiable:   3,
	}
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.DocFilePath != b.DocFilePath {
			return a.DocFilePath < b.DocFilePath
		}
		if a.DocLine != b.DocLine {
			return a.DocLine < b.DocLine
		}
		if order[a.DriftType] != order[b.DriftType] {
			return order[a.DriftType] < order[b.DriftType]
		}
		if a.SymbolName != b.SymbolName {
			return a.SymbolName < b.SymbolName
		}
		return a.ID < b.ID
	})
	return items
}

// ---------------------------------------------------------------- provenance

// provenance records how a claim was extracted and therefore how far it is
// trusted.
type provenance int

const (
	provSpan provenance = iota
	provFenceTagged
	provFenceUntagged
	provProse
)

// tier is the starting confidence of a claim with this provenance.
func (p provenance) tier() string {
	switch p {
	case provSpan, provFenceTagged:
		return contracts.ConfidenceHigh
	}
	return contracts.ConfidenceMedium
}

func tierRank(t string) int {
	switch t {
	case contracts.ConfidenceHigh:
		return 3
	case contracts.ConfidenceMedium:
		return 2
	case contracts.ConfidenceLow:
		return 1
	}
	return 0
}

func minTier(a, b string) string {
	if tierRank(a) <= tierRank(b) {
		return a
	}
	return b
}

func lowerTier(t string) string {
	switch t {
	case contracts.ConfidenceHigh:
		return contracts.ConfidenceMedium
	case contracts.ConfidenceMedium:
		return contracts.ConfidenceLow
	}
	return t
}

// ------------------------------------------------------------------- claims

type callClaim struct {
	line    int
	name    string // possibly dotted: Foo, Owner.Method, pkg.Func
	args    []string
	raw     string // the call as written, whitespace normalised
	sigForm bool   // preceded by func, def, fn or function
	lang    string // language implied by the fence tag or declaration keyword
	prov    provenance
}

type pathClaim struct {
	line int
	p    string
	prov provenance
}

type routeClaim struct {
	line   int
	method string
	path   string
}

type docLine struct {
	n       int
	text    string
	fence   bool
	lang    string // normalised language of a tagged fence
	tagged  bool
	symbols bool // whether symbol claims are taken from this line
}

var (
	callRe       = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)*\s*\(`)
	declPrefixRe = regexp.MustCompile(`\b(func|def|fn|function)\s+(?:\([^()]*\)\s*)?$`)
	pathRe       = regexp.MustCompile(`[\w./-]+\.(?:tsx|ts|jsx|js|mjs|cjs|mts|cts|go|py|rs)\b`)
	routeRe      = regexp.MustCompile("\\b(GET|POST|PUT|PATCH|DELETE)\\s+(/[^\\s`\"'<>)\\]]*)")
	routeTokenRe = regexp.MustCompile(`/[A-Za-z0-9_:{}<>\[\].~%-]+(?:/[A-Za-z0-9_:{}<>\[\].~%-]+)*`)
	identSplitRe = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)
	jsDefaultRe  = regexp.MustCompile(`(^|[^=!<>])=([^=>]|$)`)
	rustSelfRe   = regexp.MustCompile(`^(?:&\s*(?:'\w+\s+)?)?(?:mut\s+)?self\b`)
)

// callKeywords are names that precede a parenthesis without being calls.
var callKeywords = map[string]bool{
	"if": true, "for": true, "while": true, "switch": true, "return": true, "catch": true,
	"with": true, "elif": true, "match": true, "func": true, "def": true, "fn": true,
	"function": true, "await": true, "typeof": true, "sizeof": true, "defer": true,
	"go": true, "select": true, "case": true, "in": true, "not": true, "and": true,
	"or": true, "assert": true, "yield": true, "except": true, "lambda": true,
}

// externalQualifiers are receivers and packages that never denote code from
// the scanned repository.
var externalQualifiers = map[string]bool{
	"console": true, "Math": true, "JSON": true, "Object": true, "Array": true,
	"Promise": true, "Date": true, "String": true, "Number": true, "Boolean": true,
	"Map": true, "Set": true, "Symbol": true, "Reflect": true, "process": true,
	"window": true, "document": true, "os": true, "sys": true, "re": true,
	"json": true, "time": true, "math": true, "fmt": true, "strings": true,
	"strconv": true, "errors": true, "context": true, "http": true, "io": true,
	"bytes": true, "std": true, "self": true, "this": true, "super": true,
	"cls": true, "t": true, "b": true, "ctx": true, "err": true,
}

// fenceLanguage normalises a fence info string. scan reports whether symbol
// claims are taken from such a fence; tagged reports whether the fence named a
// language this scanner understands.
func fenceLanguage(info string) (lang string, scan, tagged bool) {
	tag := strings.ToLower(strings.TrimSpace(info))
	if i := strings.IndexAny(tag, " \t{,"); i >= 0 {
		tag = tag[:i]
	}
	switch tag {
	case "":
		return "", true, false
	case "go", "golang":
		return LangGo, true, true
	case "js", "javascript", "jsx", "mjs", "cjs", "node":
		return LangJavaScript, true, true
	case "ts", "typescript", "tsx":
		return LangTypeScript, true, true
	case "py", "python", "python3":
		return LangPython, true, true
	case "rs", "rust":
		return LangRust, true, true
	}
	return "", false, false
}

func keywordLanguage(kw string) string {
	switch kw {
	case "func":
		return LangGo
	case "def":
		return LangPython
	case "fn":
		return LangRust
	case "function":
		return LangJavaScript
	}
	return ""
}

// langCompatible reports whether a documented language can describe a symbol
// of the given language. JavaScript and TypeScript are interchangeable.
func langCompatible(doc, sym string) bool {
	if doc == "" || doc == sym {
		return true
	}
	js := func(l string) bool { return l == LangJavaScript || l == LangTypeScript }
	return js(doc) && js(sym)
}

// splitDoc splits a document into lines and marks fenced code.
func splitDoc(content []byte) []docLine {
	text := strings.ReplaceAll(string(content), "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	raw := strings.Split(text, "\n")
	out := make([]docLine, 0, len(raw))
	var fenceChar byte
	fenceLen := 0
	var cur docLine
	for i, ln := range raw {
		trimmed := strings.TrimLeft(ln, " \t")
		if fenceLen == 0 {
			if run, ch := fenceRun(trimmed); run >= 3 && !(ch == '`' && strings.Contains(trimmed[run:], "`")) {
				fenceChar, fenceLen = ch, run
				lang, scan, tagged := fenceLanguage(trimmed[run:])
				cur = docLine{fence: true, lang: lang, tagged: tagged, symbols: scan}
				continue
			}
			out = append(out, docLine{n: i + 1, text: ln})
			continue
		}
		if run, ch := fenceRun(trimmed); ch == fenceChar && run >= fenceLen && strings.TrimSpace(trimmed[run:]) == "" {
			fenceLen = 0
			continue
		}
		dl := cur
		dl.n, dl.text = i+1, ln
		out = append(out, dl)
	}
	return out
}

// fenceRun returns the length and character of a leading run of backticks or
// tildes.
func fenceRun(s string) (int, byte) {
	if s == "" || (s[0] != '`' && s[0] != '~') {
		return 0, 0
	}
	n := 0
	for n < len(s) && s[n] == s[0] {
		n++
	}
	return n, s[0]
}

// codeSpanRanges returns the content ranges of the inline code spans of a line.
func codeSpanRanges(line string) [][2]int {
	var out [][2]int
	i := 0
	for i < len(line) {
		if line[i] != '`' {
			i++
			continue
		}
		j := i
		for j < len(line) && line[j] == '`' {
			j++
		}
		n := j - i
		k := j
		found := false
		for k < len(line) {
			if line[k] != '`' {
				k++
				continue
			}
			e := k
			for e < len(line) && line[e] == '`' {
				e++
			}
			if e-k == n {
				out = append(out, [2]int{j, k})
				i = e
				found = true
				break
			}
			k = e
		}
		if !found {
			i = j
		}
	}
	return out
}

func isIdentByte(c byte) bool {
	return c == '_' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isPathByte(c byte) bool {
	return isIdentByte(c) || c == '.' || c == '-' || c == '/'
}

const defaultQuotes = "\"'`"

func quotesFor(lang string) string {
	if lang == LangRust {
		return "\""
	}
	return defaultQuotes
}

func anglesFor(lang string) bool {
	return lang == LangRust || lang == LangTypeScript || lang == LangJavaScript || lang == ""
}

// balancedArgs returns the text between the parenthesis at s[open] and its
// match.
func balancedArgs(s string, open int, quotes string) (inner string, closeIdx int, ok bool) {
	depth := 0
	var quote byte
	for i := open; i < len(s); i++ {
		c := s[i]
		if quote != 0 {
			if c == '\\' {
				i++
				continue
			}
			if c == quote {
				quote = 0
			}
			continue
		}
		switch {
		case strings.IndexByte(quotes, c) >= 0:
			quote = c
		case c == '(' || c == '[' || c == '{':
			depth++
		case c == ')' || c == ']' || c == '}':
			depth--
			if depth == 0 {
				return s[open+1 : i], i, true
			}
			if depth < 0 {
				return "", 0, false
			}
		}
	}
	return "", 0, false
}

// splitTopLevel splits a parameter or argument list at top-level commas.
func splitTopLevel(s, quotes string, angles bool) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []string
	depth, angle, start := 0, 0, 0
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quote != 0 {
			if c == '\\' {
				i++
				continue
			}
			if c == quote {
				quote = 0
			}
			continue
		}
		switch {
		case strings.IndexByte(quotes, c) >= 0:
			quote = c
		case c == '(' || c == '[' || c == '{':
			depth++
		case c == ')' || c == ']' || c == '}':
			depth--
		case angles && c == '<' && i > 0 && isIdentByte(s[i-1]):
			angle++
		case angles && c == '>' && angle > 0 && i > 0 && s[i-1] != '-' && s[i-1] != '=':
			angle--
		case c == ',' && depth == 0 && angle == 0:
			out = append(out, strings.TrimSpace(s[start:i]))
			start = i + 1
		}
	}
	out = append(out, strings.TrimSpace(s[start:]))
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

// extractCalls finds the calls and signatures in one piece of code text.
func extractCalls(seg string, line int, lang string, prov provenance) []callClaim {
	var out []callClaim
	for _, m := range callRe.FindAllStringIndex(seg, -1) {
		start, end := m[0], m[1]
		if start > 0 {
			if prev := seg[start-1]; prev == '.' || prev == '$' || isIdentByte(prev) {
				continue
			}
		}
		name := strings.TrimRight(strings.TrimSuffix(seg[start:end], "("), " \t")
		if callKeywords[name] {
			continue
		}
		inner, closeIdx, ok := balancedArgs(seg, end-1, quotesFor(lang))
		if !ok {
			continue
		}
		cc := callClaim{
			line: line,
			name: name,
			args: splitTopLevel(inner, quotesFor(lang), anglesFor(lang)),
			raw:  strings.Join(strings.Fields(seg[start:closeIdx+1]), " "),
			lang: lang,
			prov: prov,
		}
		if pm := declPrefixRe.FindStringSubmatch(seg[:start]); pm != nil {
			cc.sigForm = true
			if cc.lang == "" {
				cc.lang = keywordLanguage(pm[1])
			}
		}
		out = append(out, cc)
	}
	return out
}

// ------------------------------------------------------------- parameters

type param struct {
	name     string
	optional bool
	variadic bool
}

// signatureArgs returns the parameter list text of a declaration signature.
func signatureArgs(sig, name, lang string) (string, bool) {
	quotes := quotesFor(lang)
	from := 0
	for from < len(sig) {
		i := strings.Index(sig[from:], name)
		if i < 0 {
			return "", false
		}
		i += from
		j := i + len(name)
		from = j
		if i > 0 && isIdentByte(sig[i-1]) {
			continue
		}
		if j < len(sig) && isIdentByte(sig[j]) {
			continue
		}
		k := skipSpaces(sig, j)
		if k < len(sig) && (sig[k] == '[' || sig[k] == '<') {
			if e := matchAngleOrSquare(sig, k); e > 0 {
				k = skipSpaces(sig, e+1)
			}
		}
		if k < len(sig) && (sig[k] == '?' || sig[k] == '!') {
			k = skipSpaces(sig, k+1)
		}
		if k < len(sig) && sig[k] == '(' {
			inner, _, ok := balancedArgs(sig, k, quotes)
			return inner, ok
		}
		if k < len(sig) && sig[k] == '=' {
			if open := strings.IndexByte(sig[k:], '('); open >= 0 {
				inner, _, ok := balancedArgs(sig, k+open, quotes)
				return inner, ok
			}
			rest := strings.TrimSpace(strings.TrimPrefix(sig[k:], "="))
			rest = strings.TrimSpace(strings.TrimPrefix(rest, "async "))
			if identSplitRe.FindString(rest) == rest && rest != "" {
				return rest, true
			}
		}
	}
	return "", false
}

func skipSpaces(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	return i
}

// matchAngleOrSquare returns the index of the bracket closing the one at open,
// or -1.
func matchAngleOrSquare(s string, open int) int {
	o := s[open]
	c := byte(']')
	if o == '<' {
		c = '>'
	}
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case o:
			depth++
		case c:
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// symbolParams parses the parameters a callable symbol declares. ok is false
// when the signature could not be read, in which case no comparison is made.
func symbolParams(sym contracts.Symbol) ([]param, bool) {
	inner, ok := signatureArgs(sym.Signature, sym.Name, sym.Language)
	if !ok {
		return nil, false
	}
	return parseParams(sym.Language, splitTopLevel(inner, quotesFor(sym.Language), anglesFor(sym.Language))), true
}

func parseParams(lang string, segs []string) []param {
	switch lang {
	case LangGo:
		return goParams(segs)
	case LangPython:
		return pyParams(segs)
	case LangRust:
		return rustParams(segs)
	}
	return jsParams(segs)
}

func goParams(segs []string) []param {
	named := false
	for _, s := range segs {
		if strings.ContainsAny(strings.TrimSpace(s), " \t") {
			named = true
			break
		}
	}
	out := make([]param, 0, len(segs))
	for _, s := range segs {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		p := param{variadic: strings.Contains(s, "...")}
		if named {
			p.name = strings.Fields(s)[0]
		}
		out = append(out, p)
	}
	return out
}

func pyParams(segs []string) []param {
	var out []param
	for _, s := range segs {
		s = strings.TrimSpace(s)
		if s == "" || s == "*" || s == "/" {
			continue
		}
		p := param{variadic: strings.HasPrefix(s, "*")}
		core := strings.TrimLeft(s, "*")
		p.name = identSplitRe.FindString(core)
		p.optional = p.variadic || strings.Contains(core, "=")
		out = append(out, p)
	}
	if len(out) > 0 && (out[0].name == "self" || out[0].name == "cls") {
		out = out[1:]
	}
	return out
}

func rustParams(segs []string) []param {
	var out []param
	for _, s := range segs {
		s = strings.TrimSpace(s)
		if s == "" || rustSelfRe.MatchString(s) {
			continue
		}
		name := s
		if i := strings.Index(name, ":"); i >= 0 {
			name = name[:i]
		}
		name = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(name), "mut "))
		out = append(out, param{name: name})
	}
	return out
}

func jsParams(segs []string) []param {
	var out []param
	for _, s := range segs {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		p := param{variadic: strings.HasPrefix(s, "...")}
		core := strings.TrimSpace(strings.TrimPrefix(s, "..."))
		if core == "this" || strings.HasPrefix(core, "this:") || strings.HasPrefix(core, "this ") {
			continue
		}
		switch {
		case strings.HasPrefix(core, "{") || strings.HasPrefix(core, "["):
			p.name = leadingGroup(core)
		default:
			p.name = identSplitRe.FindString(core)
		}
		rest := strings.TrimSpace(strings.TrimPrefix(core, p.name))
		p.optional = p.variadic || strings.HasPrefix(rest, "?") || jsDefaultRe.MatchString(core)
		out = append(out, p)
	}
	return out
}

// leadingGroup returns the bracketed pattern at the start of s, such as the
// destructuring pattern of a JavaScript parameter.
func leadingGroup(s string) string {
	depth := 0
	for i := range len(s) {
		switch s[i] {
		case '{', '[', '(':
			depth++
		case '}', ']', ')':
			depth--
			if depth == 0 {
				return s[:i+1]
			}
		}
	}
	return s
}

// argsCompatible reports whether the documented arguments fit the declared
// parameters. A signature-form claim must match the parameter list exactly; a
// call-form claim only has to be a legal number of arguments.
func argsCompatible(declared []param, cc callClaim, symLang string) bool {
	for _, a := range cc.args {
		if a == "..." || a == "…" || strings.Contains(a, "…") {
			return true // elided in the documentation; nothing to compare
		}
	}
	required, variadic := 0, false
	for _, p := range declared {
		if p.variadic {
			variadic = true
		}
		if !p.optional && !p.variadic {
			required++
		}
	}
	if cc.sigForm {
		lang := cc.lang
		if lang == "" {
			lang = symLang
		}
		doc := parseParams(lang, cc.args)
		if len(doc) != len(declared) {
			return false
		}
		for i := range doc {
			if doc[i].name != "" && declared[i].name != "" && doc[i].name != declared[i].name {
				return false
			}
		}
		return true
	}
	n := len(cc.args)
	if symLang == LangGo && n == 1 && strings.Contains(cc.args[0], "(") && len(declared) > 1 {
		return true // f(g()) may spread g's multiple results
	}
	if n < required {
		return false
	}
	return variadic || n <= len(declared)
}

// ----------------------------------------------------------------- the index

type routeKey struct {
	method string
	norm   string
}

type graphIndex struct {
	g                 *contracts.RepoContextGraph
	byName            map[string][]contracts.Symbol
	byQual            map[string][]contracts.Symbol
	allNames          map[string]bool
	typeNames         map[string]bool
	files             []string
	fileSet           map[string]bool
	dirSet            map[string]bool
	routes            []routeKey
	imports           map[string]bool
	docSyms           map[string][]contracts.Symbol
	docRoutes         map[string][]contracts.RouteEvidence
	moduleDirs        []string
	gapPaths          []string
	unsupported       []string
	timedOut          bool
	fileListTruncated bool
}

func isCallableKind(k string) bool {
	return k == contracts.SymbolFunction || k == contracts.SymbolMethod
}

func isTypeKind(k string) bool {
	switch k {
	case contracts.SymbolStruct, contracts.SymbolInterface, contracts.SymbolClass,
		contracts.SymbolType, contracts.SymbolEnum, contracts.SymbolTrait:
		return true
	}
	return false
}

func newGraphIndex(g *contracts.RepoContextGraph, files []string) *graphIndex {
	ix := &graphIndex{
		g:         g,
		byName:    map[string][]contracts.Symbol{},
		byQual:    map[string][]contracts.Symbol{},
		allNames:  map[string]bool{},
		typeNames: map[string]bool{},
		fileSet:   map[string]bool{},
		dirSet:    map[string]bool{},
		imports:   map[string]bool{},
		docSyms:   map[string][]contracts.Symbol{},
		docRoutes: map[string][]contracts.RouteEvidence{},
		timedOut:  g.Coverage.TimedOut,
	}
	for _, f := range files {
		ix.files = append(ix.files, f)
		ix.fileSet[f] = true
		for d := path.Dir(f); d != "." && d != "/" && d != ""; d = path.Dir(d) {
			if ix.dirSet[d] {
				break
			}
			ix.dirSet[d] = true
		}
		if otherSourceExts[strings.ToLower(path.Ext(f))] {
			ix.unsupported = append(ix.unsupported, f)
		}
	}
	sort.Strings(ix.unsupported)

	paths := make([]string, 0, len(g.Symbols))
	for p := range g.Symbols {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	modSet := map[string]bool{}
	for _, p := range paths {
		dir := path.Dir(p)
		for _, s := range g.Symbols[p] {
			if s.Kind == KindImpl {
				continue
			}
			ix.allNames[s.Name] = true
			qual := s.QualifiedName
			if qual == "" {
				qual = s.Name
			}
			switch {
			case isCallableKind(s.Kind):
				ix.byName[s.Name] = append(ix.byName[s.Name], s)
				ix.byQual[qual] = append(ix.byQual[qual], s)
			case isTypeKind(s.Kind):
				ix.typeNames[s.Name] = true
			}
			for _, imp := range s.Imports {
				for _, seg := range strings.FieldsFunc(imp, func(r rune) bool {
					return r == '/' || r == '.' || r == ':' || r == '@' || r == '\\'
				}) {
					ix.imports[seg] = true
				}
			}
			if s.Visibility == VisibilityPublic && documentableKind(s.Kind) && !isTestPath(p) && dir != "." {
				ix.docSyms[dir] = append(ix.docSyms[dir], s)
				modSet[dir] = true
			}
		}
	}
	for _, r := range g.Routes {
		ix.routes = append(ix.routes, routeKey{method: r.Method, norm: normRoute(r.Path)})
		dir := path.Dir(r.FilePath)
		if dir != "." && !isTestPath(r.FilePath) {
			ix.docRoutes[dir] = append(ix.docRoutes[dir], r)
			modSet[dir] = true
		}
	}
	for d := range modSet {
		ix.moduleDirs = append(ix.moduleDirs, d)
	}
	sort.Strings(ix.moduleDirs)

	for _, gp := range g.Coverage.ParseGaps {
		if i := strings.Index(gp, ": "); i > 0 {
			ix.gapPaths = append(ix.gapPaths, gp[:i])
		}
	}
	sort.Strings(ix.gapPaths)
	for _, n := range g.Coverage.Notes {
		if strings.HasPrefix(n, NoteFileListTruncated) {
			ix.fileListTruncated = true
		}
	}
	return ix
}

func documentableKind(k string) bool {
	return isCallableKind(k) || isTypeKind(k)
}

func isTestPath(p string) bool {
	lower := strings.ToLower(p)
	base := path.Base(lower)
	if strings.HasSuffix(base, "_test.go") || strings.HasSuffix(base, "_test.py") ||
		(strings.HasPrefix(base, "test_") && strings.HasSuffix(base, ".py")) {
		return true
	}
	for _, suf := range []string{".test.ts", ".test.tsx", ".test.js", ".test.jsx", ".spec.ts", ".spec.tsx", ".spec.js", ".spec.jsx"} {
		if strings.HasSuffix(base, suf) {
			return true
		}
	}
	for _, seg := range strings.Split(path.Dir(lower), "/") {
		switch seg {
		case "test", "tests", "testdata", "__tests__", "fixtures", "e2e":
			return true
		}
	}
	return false
}

// normRoute canonicalises a route path so that `/users/:id`, `/users/{id}` and
// `/users/<id>` compare equal.
func normRoute(p string) string {
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	}
	if len(p) > 1 {
		p = strings.TrimRight(p, "/")
		if p == "" {
			p = "/"
		}
	}
	segs := strings.Split(p, "/")
	for i, s := range segs {
		switch {
		case strings.HasPrefix(s, ":"),
			strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}"),
			strings.HasPrefix(s, "<") && strings.HasSuffix(s, ">"),
			strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]"):
			segs[i] = ":param"
		}
	}
	return strings.Join(segs, "/")
}

func methodCompatible(routeMethods, claim string) bool {
	if routeMethods == "" {
		return true
	}
	for _, m := range strings.Split(routeMethods, ",") {
		if strings.TrimSpace(m) == claim {
			return true
		}
	}
	return false
}

func (ix *graphIndex) routeKnown(method, p string) bool {
	np := normRoute(p)
	for _, r := range ix.routes {
		if !methodCompatible(r.method, method) {
			continue
		}
		switch {
		case r.norm == np:
			return true
		case r.norm != "/" && strings.HasSuffix(np, r.norm):
			return true
		case np != "/" && strings.HasSuffix(r.norm, np):
			return true
		}
	}
	return false
}

// pathExists reports whether a documented path names a repository file. The
// path may be written relative to the repository root, relative to the document
// or as a trailing portion of a longer path. ok is false when the path cannot
// be judged (it escapes the repository).
func (ix *graphIndex) pathExists(docDir, p string) (exists, ok bool) {
	rel := strings.HasPrefix(p, "./") || strings.HasPrefix(p, "../")
	clean := path.Clean(p)
	docRel := path.Join(docDir, p)
	if docRel == ".." || strings.HasPrefix(docRel, "../") {
		return false, false
	}
	if rel {
		return ix.fileSet[docRel], true
	}
	if ix.fileSet[clean] || ix.fileSet[docRel] {
		return true, true
	}
	suffix := "/" + clean
	for _, f := range ix.files {
		if f == clean || strings.HasSuffix(f, suffix) {
			return true, true
		}
	}
	return false, true
}

// gapReason reports why absence cannot be established for code under the given
// scopes ("" means the whole repository).
func (ix *graphIndex) gapReason(scopes []string) (string, bool) {
	if ix.timedOut {
		return "the scan timed out before the whole repository was read", true
	}
	in := func(p, scope string) bool {
		return scope == "" || p == scope || strings.HasPrefix(p, scope+"/")
	}
	for _, scope := range scopes {
		for _, gp := range ix.gapPaths {
			if in(gp, scope) {
				return "parse gap in " + gp, true
			}
		}
		for _, up := range ix.unsupported {
			if in(up, scope) {
				return "unsupported language file " + up, true
			}
		}
	}
	return "", false
}

// ----------------------------------------------------------- per document

type described struct {
	dir  string
	line int
}

type driftBuilder struct {
	doc          string
	items        []contracts.DocDriftItem
	seen         map[string]bool
	missing      int
	unverifiable int
}

func tick(s string) string {
	return strings.ReplaceAll(strings.Join(strings.Fields(s), " "), "`", "'")
}

func remediation(doc string, line int, expected, observed string) string {
	return fmt.Sprintf("Update `%s` line %d: `%s` → `%s`", doc, line, tick(expected), tick(observed))
}

func (b *driftBuilder) emit(it contracts.DocDriftItem) {
	key := strings.Join([]string{it.DriftType, it.DocFilePath, it.SymbolName, it.Expected, it.Observed}, "\x00")
	if b.seen[key] {
		return
	}
	b.seen[key] = true
	it.DocFilePath = b.doc
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d\x00%s\x00%s", it.DriftType, b.doc, it.DocLine, it.SymbolName, it.Expected)))
	it.ID = "drift-" + hex.EncodeToString(sum[:])[:12]
	if it.RemediationTask == "" {
		it.RemediationTask = remediation(b.doc, it.DocLine, it.Expected, it.Observed)
	}
	b.items = append(b.items, it)
}

func (b *driftBuilder) unverified(line int, symbol, expected, reason string) {
	if b.unverifiable >= maxUnverifiablePerDoc {
		return
	}
	b.unverifiable++
	b.emit(contracts.DocDriftItem{
		DocLine:         line,
		SymbolName:      symbol,
		DriftType:       contracts.DriftUnverifiable,
		Expected:        expected,
		Observed:        "(cannot verify: " + reason + ")",
		EvidenceKind:    EvidenceCoverageGap,
		Confidence:      contracts.ConfidenceLow,
		RemediationTask: fmt.Sprintf("Check `%s` line %d by hand: `%s` could not be verified because %s", b.doc, line, tick(expected), reason),
	})
}

func (ix *graphIndex) analyzeDoc(doc DocFile) []contracts.DocDriftItem {
	lines := splitDoc(doc.Content)
	docDir := path.Dir(doc.Path)
	var calls []callClaim
	var paths []pathClaim
	var routes []routeClaim

	for _, dl := range lines {
		spans := codeSpanRanges(dl.text)
		if dl.fence {
			prov := provFenceUntagged
			if dl.tagged {
				prov = provFenceTagged
			}
			if dl.symbols {
				calls = append(calls, extractCalls(dl.text, dl.n, dl.lang, prov)...)
			}
			paths = append(paths, pathClaims(dl.text, dl.n, nil, prov)...)
		} else {
			for _, sp := range spans {
				calls = append(calls, extractCalls(dl.text[sp[0]:sp[1]], dl.n, "", provSpan)...)
			}
			paths = append(paths, pathClaims(dl.text, dl.n, spans, provProse)...)
		}
		routes = append(routes, routeClaims(dl.text, dl.n)...)
	}

	desc := ix.describedModules(lines)
	scopes := make([]string, 0, len(desc)+1)
	for _, d := range desc {
		scopes = append(scopes, d.dir)
	}
	if docDir != "." {
		scopes = append(scopes, docDir)
	}
	if len(scopes) == 0 {
		scopes = []string{""}
	}

	b := &driftBuilder{doc: doc.Path, seen: map[string]bool{}}
	ix.checkCalls(b, calls, scopes)
	ix.checkPaths(b, paths, docDir)
	ix.checkRoutes(b, routes, scopes)
	ix.checkMissing(b, lines, desc)
	return b.items
}

func pathClaims(text string, line int, spans [][2]int, plain provenance) []pathClaim {
	var out []pathClaim
	for _, m := range pathRe.FindAllStringIndex(text, -1) {
		start, end := m[0], m[1]
		if end < len(text) && text[end] == '.' && end+1 < len(text) && isIdentByte(text[end+1]) {
			continue // x.go.bak, x.ts.map
		}
		p := text[start:end]
		ws := strings.LastIndexAny(text[:start], " \t\"'`(<[") + 1
		we := strings.IndexAny(text[end:], " \t\"'`)>]")
		if we < 0 {
			we = len(text) - end
		}
		word := text[ws : end+we]
		if strings.Contains(word, "://") || strings.HasPrefix(word, "www.") || strings.Contains(word, "@") {
			continue
		}
		if strings.HasPrefix(p, "/") || strings.Contains(p, "path/to/") || IsHardExcluded(path.Clean(p)) {
			continue
		}
		prov := plain
		for _, sp := range spans {
			if start >= sp[0] && end <= sp[1] {
				prov = provSpan
				break
			}
		}
		out = append(out, pathClaim{line: line, p: p, prov: prov})
	}
	return out
}

func routeClaims(text string, line int) []routeClaim {
	var out []routeClaim
	for _, m := range routeRe.FindAllStringSubmatch(text, -1) {
		p := strings.TrimRight(m[2], ".,;:*")
		if len(p) == 0 {
			continue
		}
		out = append(out, routeClaim{line: line, method: m[1], path: p})
	}
	return out
}

// describedModules lists the module directories the document names, with the
// first line naming each.
func (ix *graphIndex) describedModules(lines []docLine) []described {
	var out []described
	for _, dir := range ix.moduleDirs {
		if ln := mentionLine(lines, dir); ln > 0 {
			out = append(out, described{dir: dir, line: ln})
		}
	}
	return out
}

// mentionLine returns the first line that names the directory as a path, or 0.
// A single-segment directory counts only when written with a trailing slash or
// alone in an inline code span, because a bare word such as "web" says nothing
// about a directory.
func mentionLine(lines []docLine, dir string) int {
	single := !strings.Contains(dir, "/")
	for _, dl := range lines {
		text := dl.text
		from := 0
		for from < len(text) {
			i := strings.Index(text[from:], dir)
			if i < 0 {
				break
			}
			i += from
			end := i + len(dir)
			from = i + 1
			if !pathBoundaryBefore(text, i) {
				continue
			}
			if end < len(text) {
				c := text[end]
				if isIdentByte(c) || c == '-' {
					continue
				}
				if c == '.' && end+1 < len(text) && isIdentByte(text[end+1]) {
					continue
				}
				if single && c != '/' {
					continue
				}
			} else if single {
				continue
			}
			return dl.n
		}
		if single && !dl.fence {
			for _, sp := range codeSpanRanges(text) {
				if strings.TrimSuffix(strings.TrimSpace(text[sp[0]:sp[1]]), "/") == dir {
					return dl.n
				}
			}
		}
	}
	return 0
}

func pathBoundaryBefore(text string, i int) bool {
	if i == 0 {
		return true
	}
	prev := text[i-1]
	if !isPathByte(prev) {
		return true
	}
	return prev == '/' && i >= 2 && text[i-2] == '.' && (i == 2 || !isPathByte(text[i-3]))
}

// ------------------------------------------------------------ claim checks

type candidate struct {
	sym contracts.Symbol
}

// candidates returns the callable symbols a call claim may refer to.
func (ix *graphIndex) candidates(cc callClaim) []candidate {
	var out []candidate
	add := func(list []contracts.Symbol) {
		for _, s := range list {
			if langCompatible(cc.lang, s.Language) {
				out = append(out, candidate{sym: s})
			}
		}
	}
	i := strings.LastIndexByte(cc.name, '.')
	if i < 0 {
		add(ix.byName[cc.name])
		return out
	}
	add(ix.byQual[cc.name])
	if len(out) > 0 {
		return out
	}
	q, last := cc.name[:i], cc.name[i+1:]
	if j := strings.LastIndexByte(q, '.'); j >= 0 {
		q = q[j+1:]
	}
	for _, s := range ix.byName[last] {
		if s.Kind != contracts.SymbolFunction || !langCompatible(cc.lang, s.Language) {
			continue
		}
		stem := strings.TrimSuffix(path.Base(s.Path), path.Ext(s.Path))
		if path.Base(path.Dir(s.Path)) == q || stem == q {
			out = append(out, candidate{sym: s})
		}
	}
	return out
}

func symbolLabel(s contracts.Symbol) string {
	if s.QualifiedName != "" {
		return s.QualifiedName
	}
	return s.Name
}

func (ix *graphIndex) checkCalls(b *driftBuilder, calls []callClaim, scopes []string) {
	for _, cc := range calls {
		cands := ix.candidates(cc)
		if len(cands) > 0 {
			ix.checkSignature(b, cc, cands)
			continue
		}
		ix.checkAbsentDeclaration(b, cc, scopes)
	}
}

func (ix *graphIndex) checkSignature(b *driftBuilder, cc callClaim, cands []candidate) {
	var cite *contracts.Symbol
	for i := range cands {
		declared, ok := symbolParams(cands[i].sym)
		if !ok || argsCompatible(declared, cc, cands[i].sym.Language) {
			return
		}
		if cite == nil {
			cite = &cands[i].sym
		}
	}
	conf := cc.prov.tier()
	if len(cands) > 1 {
		conf = lowerTier(conf)
	}
	b.emit(contracts.DocDriftItem{
		DocLine:      cc.line,
		CodeFilePath: cite.Path,
		SymbolName:   symbolLabel(*cite),
		DriftType:    contracts.DriftStaleSignature,
		Expected:     cc.raw,
		Observed:     cite.Signature,
		EvidenceKind: EvidenceDocSignature,
		Confidence:   conf,
	})
}

// checkAbsentDeclaration reports a documented declaration or type member the
// graph does not have. Plain calls to unknown names are never reported.
func (ix *graphIndex) checkAbsentDeclaration(b *driftBuilder, cc callClaim, scopes []string) {
	name := cc.name
	conf := ""
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		q := name[:i]
		if strings.Contains(q, ".") || externalQualifiers[q] || ix.imports[q] || !ix.typeNames[q] || len(ix.byQual[name]) > 0 {
			return
		}
		conf = lowerTier(cc.prov.tier())
	} else {
		if !cc.sigForm || name == "main" || name == "init" || ix.allNames[name] {
			return
		}
		conf = cc.prov.tier()
		if cc.prov != provSpan {
			conf = lowerTier(conf)
		}
	}
	if reason, gap := ix.gapReason(scopes); gap {
		b.unverified(cc.line, name, cc.raw, reason)
		return
	}
	b.emit(contracts.DocDriftItem{
		DocLine:      cc.line,
		SymbolName:   name,
		DriftType:    contracts.DriftDeprecatedFlow,
		Expected:     cc.raw,
		Observed:     "(no matching declaration in the scanned repository)",
		EvidenceKind: EvidenceDocDeclaration,
		Confidence:   conf,
	})
}

func (ix *graphIndex) checkPaths(b *driftBuilder, claims []pathClaim, docDir string) {
	for _, pc := range claims {
		exists, ok := ix.pathExists(docDir, pc.p)
		if !ok || exists {
			continue
		}
		if ix.fileListTruncated {
			b.unverified(pc.line, pc.p, pc.p, "the repository file list was truncated")
			continue
		}
		conf := contracts.ConfidenceMedium
		if ix.dirSet[path.Dir(path.Clean(pc.p))] {
			conf = contracts.ConfidenceHigh
		}
		b.emit(contracts.DocDriftItem{
			DocLine:      pc.line,
			DriftType:    contracts.DriftDeprecatedFlow,
			Expected:     pc.p,
			Observed:     "(file not found in the repository)",
			EvidenceKind: EvidenceDocPath,
			Confidence:   minTier(conf, pc.prov.tier()),
		})
	}
}

func (ix *graphIndex) checkRoutes(b *driftBuilder, claims []routeClaim, scopes []string) {
	if len(ix.routes) == 0 {
		return // route detection found nothing here, so no route can be refuted
	}
	for _, rc := range claims {
		if ix.routeKnown(rc.method, rc.path) {
			continue
		}
		expected := rc.method + " " + rc.path
		if reason, gap := ix.gapReason(scopes); gap {
			b.unverified(rc.line, expected, expected, reason)
			continue
		}
		b.emit(contracts.DocDriftItem{
			DocLine:      rc.line,
			SymbolName:   expected,
			DriftType:    contracts.DriftDeprecatedFlow,
			Expected:     expected,
			Observed:     "(no matching route registration found)",
			EvidenceKind: EvidenceDocRoute,
			Confidence:   contracts.ConfidenceMedium,
		})
	}
}

// checkMissing reports exported symbols and routes of described modules that
// the document never names.
func (ix *graphIndex) checkMissing(b *driftBuilder, lines []docLine, desc []described) {
	if len(desc) == 0 {
		return
	}
	words := map[string]bool{}
	var docText strings.Builder
	for _, dl := range lines {
		for _, w := range identSplitRe.FindAllString(dl.text, -1) {
			words[w] = true
		}
		docText.WriteString(dl.text)
		docText.WriteByte('\n')
	}
	docRoutes := map[string]bool{}
	for _, r := range routeTokenRe.FindAllString(docText.String(), -1) {
		docRoutes[normRoute(r)] = true
	}

	for _, d := range desc {
		for _, s := range ix.docSyms[d.dir] {
			if b.missing >= MaxMissingDocPerDoc {
				return
			}
			if words[s.Name] {
				continue
			}
			if s.Kind == contracts.SymbolMethod {
				if i := strings.IndexByte(s.QualifiedName, '.'); i > 0 && words[s.QualifiedName[:i]] {
					continue
				}
			}
			b.missing++
			observed := s.Signature
			if observed == "" {
				observed = s.Kind + " " + s.Name
			}
			b.emit(contracts.DocDriftItem{
				DocLine:      d.line,
				CodeFilePath: s.Path,
				SymbolName:   symbolLabel(s),
				DriftType:    contracts.DriftMissingDoc,
				Expected:     "(not documented)",
				Observed:     observed,
				EvidenceKind: EvidenceUndocumented,
				Confidence:   contracts.ConfidenceMedium,
			})
		}
		for _, r := range ix.docRoutes[d.dir] {
			if b.missing >= MaxMissingDocPerDoc {
				return
			}
			if docRoutes[normRoute(r.Path)] {
				continue
			}
			b.missing++
			label := strings.TrimSpace(r.Method + " " + r.Path)
			b.emit(contracts.DocDriftItem{
				DocLine:      d.line,
				CodeFilePath: r.FilePath,
				SymbolName:   label,
				DriftType:    contracts.DriftMissingDoc,
				Expected:     "(not documented)",
				Observed:     label,
				EvidenceKind: EvidenceUndocumented,
				Confidence:   contracts.ConfidenceMedium,
			})
		}
	}
}
