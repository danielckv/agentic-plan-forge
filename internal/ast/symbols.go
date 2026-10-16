//go:build client && !server

package ast

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	ts "github.com/tree-sitter/go-tree-sitter"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// KindImpl is the Symbol kind of a Rust impl block. It extends the kinds in
// contracts, which have no equivalent; the block records the implemented trait
// and target type in its signature.
const KindImpl = "impl"

// Evidence values recorded on symbols and routes.
const (
	// EvidenceDeclaration marks a symbol found as a declaration in the syntax tree.
	EvidenceDeclaration = "ast_declaration"
	// EvidenceRoute marks a route found as a registration call. It is evidence
	// that code registers the route, not proof of runtime behaviour.
	EvidenceRoute = "route_registration"
)

// Symbol visibility values.
const (
	VisibilityPublic     = "public"
	VisibilityPrivate    = "private"
	VisibilityProtected  = "protected"
	VisibilityRestricted = "restricted"
)

// maxSignatureBytes caps a stored signature so a pathological declaration cannot
// bloat the graph. The hash covers the capped text, so it always matches.
const maxSignatureBytes = 4096

var (
	goMuxPattern      = regexp.MustCompile(`^([A-Z]+)\s+(/\S*)$`)
	pyMethodsPattern  = regexp.MustCompile(`methods\s*=\s*[\[(]([^\])]*)[\])]`)
	quotedWordPattern = regexp.MustCompile(`["']([A-Za-z]+)["']`)
	rustVerbPattern   = regexp.MustCompile(`^(get|post|put|patch|delete)\s*\(`)
	rustOwnerPrefix   = regexp.MustCompile(`^[&*]+\s*('\w+\s+)?(mut\s+)?(dyn\s+)?`)
	stringPrefixChars = "rRbBuUfF"
)

// fileExtract is what one file contributes to the graph.
type fileExtract struct {
	Symbols []contracts.Symbol        `json:"symbols,omitempty"`
	Routes  []contracts.RouteEvidence `json:"routes,omitempty"`
	Imports []string                  `json:"imports,omitempty"`
}

// matchCaps is one query match grouped by capture role.
type matchCaps struct {
	defKind                                             string
	def, name, owner, trait, typ, value, decorated, imp *ts.Node
	rCall, rRecv, rMethod, rPath, rArgs, rArg2          *ts.Node
}

// extractor accumulates the symbols, routes and imports of one file.
type extractor struct {
	cl      *compiledLang
	src     []byte
	path    string
	symbols []contracts.Symbol
	routes  []contracts.RouteEvidence
	imports []string
}

// extractFile runs the language query over a parsed tree. Declarations come
// only from the query patterns; this code classifies, filters and describes
// what the patterns matched. Nodes are read before the cursor advances, because
// their capture storage belongs to the cursor.
func extractFile(cl *compiledLang, qc *ts.QueryCursor, root *ts.Node, src []byte, rel string) fileExtract {
	x := &extractor{cl: cl, src: src, path: rel}
	matches := qc.Matches(cl.query, root, src)
	for m := matches.Next(); m != nil; m = matches.Next() {
		mc := x.gather(m)
		switch {
		case mc.imp != nil:
			x.addImport(mc.imp)
		case mc.rCall != nil:
			x.addRoute(&mc)
		case mc.def != nil:
			x.addSymbol(&mc)
		}
	}
	return x.finish()
}

func (x *extractor) gather(m *ts.QueryMatch) matchCaps {
	var mc matchCaps
	for i := range m.Captures {
		c := m.Captures[i]
		n := c.Node
		role := x.cl.roles[c.Index]
		if role.kind != "" {
			mc.defKind, mc.def = role.kind, &n
			continue
		}
		switch role.field {
		case "name":
			mc.name = &n
		case "owner":
			mc.owner = &n
		case "trait":
			mc.trait = &n
		case "type":
			mc.typ = &n
		case "value":
			mc.value = &n
		case "decorated":
			mc.decorated = &n
		case "import":
			mc.imp = &n
		case "route.call":
			mc.rCall = &n
		case "route.recv":
			mc.rRecv = &n
		case "route.method":
			mc.rMethod = &n
		case "route.path":
			mc.rPath = &n
		case "route.args":
			mc.rArgs = &n
		case "route.arg2":
			mc.rArg2 = &n
		}
	}
	return mc
}

func (x *extractor) text(n *ts.Node) string {
	return string(x.src[n.StartByte():n.EndByte()])
}

func (x *extractor) grammar() string { return x.cl.spec.grammar }

func (x *extractor) isJS() bool {
	switch x.grammar() {
	case "javascript", "typescript", "tsx":
		return true
	}
	return false
}

// ---------------------------------------------------------------- symbols

func (x *extractor) addSymbol(mc *matchCaps) {
	def := mc.def
	kind := mc.defKind
	if def.IsError() || def.IsMissing() {
		return
	}
	name := ""
	if mc.name != nil {
		if mc.name.IsMissing() {
			return
		}
		name = x.text(mc.name)
	}
	owner := ""
	if mc.owner != nil {
		owner = ownerName(x.text(mc.owner))
	}
	if kind == "impl" {
		name = owner
	}
	if name == "" {
		return
	}

	switch {
	case x.isJS():
		if !jsTopLevel(def, kind) {
			return
		}
	case x.grammar() == "python":
		if !pyTopLevel(mc) {
			return
		}
	}

	symKind := x.symbolKind(mc)
	qualified := name
	switch {
	case kind == "impl":
		qualified = "impl " + owner
		if mc.trait != nil {
			qualified = "impl " + compact(x.text(mc.trait)) + " for " + owner
		}
	case kind == "method" && owner != "":
		qualified = owner + "." + name
	}

	sig := x.signature(mc, name)
	sig = normalizeSignature(sig)
	if len(sig) > maxSignatureBytes {
		cut := maxSignatureBytes
		for cut > 0 && !utf8.RuneStart(sig[cut]) {
			cut--
		}
		sig = sig[:cut] + "…"
	}
	sum := sha256.Sum256([]byte(sig))

	start := def
	if mc.decorated != nil {
		start = mc.decorated
	}
	evidence := EvidenceDeclaration
	if mc.decorated != nil {
		if decs := x.decorators(mc.decorated); len(decs) > 0 {
			evidence += "; decorators=" + strings.Join(decs, ",")
		}
	}

	x.symbols = append(x.symbols, contracts.Symbol{
		Name:            name,
		QualifiedName:   qualified,
		Kind:            symKind,
		Language:        x.cl.spec.name,
		Path:            x.path,
		StartLine:       int(start.StartPosition().Row) + 1,
		EndLine:         int(def.EndPosition().Row) + 1,
		Signature:       sig,
		SignatureSHA256: hex.EncodeToString(sum[:]),
		Visibility:      x.visibility(mc, name),
		Evidence:        evidence,
	})
}

// symbolKind maps a declaration capture to a contracts symbol kind. Go type
// declarations are refined by the type they declare.
func (x *extractor) symbolKind(mc *matchCaps) string {
	switch mc.defKind {
	case "function":
		return contracts.SymbolFunction
	case "method":
		return contracts.SymbolMethod
	case "struct":
		return contracts.SymbolStruct
	case "interface":
		return contracts.SymbolInterface
	case "class":
		return contracts.SymbolClass
	case "enum":
		return contracts.SymbolEnum
	case "trait":
		return contracts.SymbolTrait
	case "const":
		return contracts.SymbolConstant
	case "var":
		return contracts.SymbolVariable
	case "impl":
		return KindImpl
	}
	if x.grammar() == "go" && mc.typ != nil {
		switch mc.typ.Kind() {
		case "struct_type":
			return contracts.SymbolStruct
		case "interface_type":
			return contracts.SymbolInterface
		}
	}
	return contracts.SymbolType
}

// jsTopLevel reports whether a JavaScript or TypeScript declaration sits at
// module level, directly or under an export statement. Class members qualify
// when their class does.
func jsTopLevel(def *ts.Node, kind string) bool {
	decl := def
	if kind == "method" {
		body := def.Parent()
		if body == nil {
			return false
		}
		if decl = body.Parent(); decl == nil {
			return false
		}
	}
	p := decl.Parent()
	if p == nil {
		return false
	}
	if p.Kind() == "export_statement" {
		if p = p.Parent(); p == nil {
			return false
		}
	}
	return p.Kind() == "program"
}

// jsExported reports whether a declaration is wrapped by an export statement.
func jsExported(decl *ts.Node) bool {
	p := decl.Parent()
	return p != nil && p.Kind() == "export_statement"
}

// pyTopLevel reports whether a Python declaration is module level. Methods
// qualify when their class does, so nested classes and local functions are not
// part of the API surface.
func pyTopLevel(mc *matchCaps) bool {
	if mc.defKind != "method" {
		return true
	}
	if mc.owner == nil {
		return false
	}
	cls := mc.owner.Parent()
	if cls == nil {
		return false
	}
	p := cls.Parent()
	if p != nil && p.Kind() == "decorated_definition" {
		p = p.Parent()
	}
	return p != nil && p.Kind() == "module"
}

// ownerName reduces a receiver or impl target such as `*Server`, `Wrapper<T>` or
// `&'a mut Foo` to its bare type name.
func ownerName(s string) string {
	s = strings.TrimSpace(s)
	s = rustOwnerPrefix.ReplaceAllString(s, "")
	if i := strings.IndexAny(s, "[<("); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	if i := strings.LastIndexAny(s, ".:"); i >= 0 {
		s = s[i+1:]
	}
	return s
}

func compact(s string) string { return strings.Join(strings.Fields(s), " ") }

// decorators lists the decorator names of a Python decorated definition.
func (x *extractor) decorators(dec *ts.Node) []string {
	var out []string
	for i := range dec.NamedChildCount() {
		c := dec.NamedChild(i)
		if c.Kind() != "decorator" {
			continue
		}
		expr := c.NamedChild(0)
		if expr == nil {
			continue
		}
		if expr.Kind() == "call" {
			if f := expr.ChildByFieldName("function"); f != nil {
				expr = f
			}
		}
		out = append(out, "@"+strings.Join(strings.Fields(x.text(expr)), ""))
	}
	return out
}

// ------------------------------------------------------------- visibility

func (x *extractor) visibility(mc *matchCaps, name string) string {
	switch x.grammar() {
	case "go":
		if r, _ := utf8.DecodeRuneInString(name); unicode.IsUpper(r) {
			return VisibilityPublic
		}
		return VisibilityPrivate
	case "python":
		if strings.HasPrefix(name, "_") && !(strings.HasPrefix(name, "__") && strings.HasSuffix(name, "__")) {
			return VisibilityPrivate
		}
		return VisibilityPublic
	case "rust":
		return x.rustVisibility(mc)
	}
	return x.jsVisibility(mc, name)
}

func (x *extractor) jsVisibility(mc *matchCaps, name string) string {
	def := mc.def
	decl := def
	if mc.defKind == "method" {
		for i := range def.NamedChildCount() {
			if c := def.NamedChild(i); c.Kind() == "accessibility_modifier" {
				switch strings.TrimSpace(x.text(c)) {
				case "private":
					return VisibilityPrivate
				case "protected":
					return VisibilityProtected
				case "public":
					return VisibilityPublic
				}
			}
		}
		if strings.HasPrefix(name, "#") {
			return VisibilityPrivate
		}
		if body := def.Parent(); body != nil && body.Parent() != nil {
			decl = body.Parent()
		}
	}
	if jsExported(decl) {
		return VisibilityPublic
	}
	return VisibilityPrivate
}

func (x *extractor) rustVisibility(mc *matchCaps) string {
	def := mc.def
	if mc.defKind == "impl" {
		return ""
	}
	// Methods of a trait impl are as visible as the trait. Methods declared in a
	// trait take the trait's visibility.
	if mc.defKind == "method" {
		if mc.trait != nil {
			return VisibilityPublic
		}
		if body := def.Parent(); body != nil {
			if holder := body.Parent(); holder != nil && holder.Kind() == "trait_item" {
				return rustVisibilityOf(x, holder)
			}
		}
	}
	return rustVisibilityOf(x, def)
}

func rustVisibilityOf(x *extractor, n *ts.Node) string {
	for i := range n.NamedChildCount() {
		c := n.NamedChild(i)
		if c.Kind() != "visibility_modifier" {
			continue
		}
		if strings.TrimSpace(x.text(c)) == "pub" {
			return VisibilityPublic
		}
		return VisibilityRestricted
	}
	return VisibilityPrivate
}

// -------------------------------------------------------------- signatures

// signature returns the declaration text from its start through the parameter
// list and result type, excluding bodies. Structs, classes and interfaces
// report their header only.
func (x *extractor) signature(mc *matchCaps, name string) string {
	def := mc.def
	switch x.grammar() {
	case "go":
		return x.goSignature(mc, name)
	case "python":
		return x.headerBeforeBody(def)
	case "rust":
		return x.rustSignature(mc)
	}
	return x.jsSignature(mc)
}

// headerBeforeBody returns a declaration's text up to its body. A declaration
// without a body field is returned whole. Trailing statement punctuation is
// dropped.
func (x *extractor) headerBeforeBody(def *ts.Node) string {
	end := def.EndByte()
	if b := def.ChildByFieldName("body"); b != nil {
		end = b.StartByte()
	}
	return strings.TrimRight(string(x.src[def.StartByte():end]), " \t\r\n;:,")
}

func (x *extractor) goSignature(mc *matchCaps, name string) string {
	def := mc.def
	switch mc.defKind {
	case "type":
		if mc.typ == nil {
			return x.headerBeforeBody(def)
		}
		prefix := string(x.src[def.StartByte():mc.typ.StartByte()])
		summary := x.text(mc.typ)
		switch mc.typ.Kind() {
		case "struct_type":
			summary = "struct"
		case "interface_type":
			summary = "interface"
		}
		return "type " + prefix + summary
	case "const", "var":
		s := mc.defKind + " " + name
		if t := def.ChildByFieldName("type"); t != nil {
			s += " " + x.text(t)
		}
		return s
	}
	return x.headerBeforeBody(def)
}

func (x *extractor) jsSignature(mc *matchCaps) string {
	def := mc.def
	switch mc.defKind {
	case "function":
		if mc.value == nil || mc.name == nil {
			return x.headerBeforeBody(def)
		}
		// const f = (a, b): R => ...  keeps everything up to the result type.
		end := mc.value.StartByte()
		for _, field := range []string{"parameters", "parameter", "return_type"} {
			if p := mc.value.ChildByFieldName(field); p != nil && p.EndByte() > end {
				end = p.EndByte()
			}
		}
		kw := ""
		if c := def.Child(0); c != nil {
			kw = x.text(c) + " "
		}
		return kw + string(x.src[mc.name.StartByte():end])
	case "type":
		return strings.TrimRight(x.text(def), " \t\r\n;")
	}
	return x.headerBeforeBody(def)
}

func (x *extractor) rustSignature(mc *matchCaps) string {
	def := mc.def
	switch mc.defKind {
	case "const", "var":
		if t := def.ChildByFieldName("type"); t != nil {
			return string(x.src[def.StartByte():t.EndByte()])
		}
	case "type":
		return strings.TrimRight(x.text(def), " \t\r\n;")
	}
	return x.headerBeforeBody(def)
}

// normalizeSignature canonicalises whitespace so reformatting a declaration does
// not change its signature or hash: runs of whitespace collapse to one space,
// padding inside brackets and trailing commas before a closing bracket are
// removed.
func normalizeSignature(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	return signatureReplacer.Replace(s)
}

var signatureReplacer = strings.NewReplacer(
	", )", ")", ", ]", "]", ",)", ")", ",]", "]",
	"( ", "(", " )", ")", "[ ", "[", " ]", "]", "{ ", "{", " }", "}",
)

// ---------------------------------------------------------------- imports

func (x *extractor) addImport(n *ts.Node) {
	var out []string
	switch n.Kind() {
	case "import_spec":
		if p := n.ChildByFieldName("path"); p != nil {
			out = append(out, unquote(x.text(p)))
		}
	case "import_statement":
		if x.grammar() == "python" {
			out = x.pyImport(n)
		} else if s := n.ChildByFieldName("source"); s != nil {
			out = append(out, unquote(x.text(s)))
		}
	case "import_from_statement":
		out = x.pyFromImport(n)
	case "use_declaration":
		if a := n.ChildByFieldName("argument"); a != nil {
			out = append(out, strings.Join(strings.Fields(x.text(a)), ""))
		}
	}
	for _, imp := range out {
		if imp != "" {
			x.imports = append(x.imports, imp)
		}
	}
}

func (x *extractor) pyImport(n *ts.Node) []string {
	var out []string
	for i := range n.NamedChildCount() {
		if n.FieldNameForNamedChild(uint32(i)) != "name" {
			continue
		}
		c := n.NamedChild(i)
		switch c.Kind() {
		case "dotted_name":
			out = append(out, x.text(c))
		case "aliased_import":
			if nn := c.ChildByFieldName("name"); nn != nil {
				out = append(out, x.text(nn))
			}
		}
	}
	return out
}

func (x *extractor) pyFromImport(n *ts.Node) []string {
	mod := n.ChildByFieldName("module_name")
	if mod == nil {
		return nil
	}
	module := strings.TrimSpace(x.text(mod))
	if strings.Trim(module, ".") != "" {
		return []string{module}
	}
	// `from . import x` imports the sibling module x.
	var out []string
	for i := range n.NamedChildCount() {
		if n.FieldNameForNamedChild(uint32(i)) != "name" {
			continue
		}
		c := n.NamedChild(i)
		if c.Kind() == "aliased_import" {
			if nn := c.ChildByFieldName("name"); nn != nil {
				c = nn
			}
		}
		out = append(out, module+x.text(c))
	}
	return out
}

// unquote strips the quotes of a string literal, including language prefixes
// such as r"" and triple quotes. Text that is not quoted is returned trimmed.
func unquote(s string) string {
	s = strings.TrimSpace(s)
	i := strings.IndexAny(s, "\"'`")
	if i < 0 || strings.Trim(s[:i], stringPrefixChars) != "" {
		return s
	}
	s = s[i:]
	for _, q := range []string{`"""`, `'''`, "`", `"`, "'"} {
		if len(s) >= 2*len(q) && strings.HasPrefix(s, q) && strings.HasSuffix(s, q) {
			return s[len(q) : len(s)-len(q)]
		}
	}
	return s
}

// ----------------------------------------------------------------- routes

func (x *extractor) addRoute(mc *matchCaps) {
	if mc.rPath == nil || mc.rMethod == nil && x.grammar() != "rust" {
		return
	}
	var method, p string
	var ok bool
	switch x.grammar() {
	case "go":
		method, p, ok = x.goRoute(mc)
	case "python":
		method, p, ok = x.pyRoute(mc)
	case "rust":
		method, p, ok = x.rustRoute(mc)
	default:
		method, p, ok = x.jsRoute(mc)
	}
	if !ok {
		return
	}
	x.routes = append(x.routes, contracts.RouteEvidence{
		Method:   method,
		Path:     p,
		FilePath: x.path,
		Line:     int(mc.rPath.StartPosition().Row) + 1,
		Evidence: EvidenceRoute,
	})
}

func (x *extractor) goRoute(mc *matchCaps) (method, p string, ok bool) {
	m := x.text(mc.rMethod)
	p = unquote(x.text(mc.rPath))
	switch m {
	case "GET", "POST", "PUT", "PATCH", "DELETE":
		return m, p, strings.HasPrefix(p, "/")
	case "Get", "Post", "Put", "Patch", "Delete":
		return strings.ToUpper(m), p, strings.HasPrefix(p, "/")
	case "Handle", "HandleFunc":
		if mm := goMuxPattern.FindStringSubmatch(p); mm != nil {
			return mm[1], mm[2], true
		}
		return "", p, strings.HasPrefix(p, "/")
	}
	return "", "", false
}

func (x *extractor) jsRoute(mc *matchCaps) (method, p string, ok bool) {
	p = unquote(x.text(mc.rPath))
	return strings.ToUpper(x.text(mc.rMethod)), p, strings.HasPrefix(p, "/")
}

func (x *extractor) pyRoute(mc *matchCaps) (method, p string, ok bool) {
	p = unquote(x.text(mc.rPath))
	if !strings.HasPrefix(p, "/") {
		return "", "", false
	}
	m := x.text(mc.rMethod)
	if m != "route" && m != "api_route" {
		return strings.ToUpper(m), p, true
	}
	var verbs []string
	if mc.rArgs != nil {
		if mm := pyMethodsPattern.FindStringSubmatch(x.text(mc.rArgs)); mm != nil {
			for _, q := range quotedWordPattern.FindAllStringSubmatch(mm[1], -1) {
				verbs = append(verbs, strings.ToUpper(q[1]))
			}
		}
	}
	if len(verbs) == 0 {
		return "GET", p, true
	}
	return strings.Join(verbs, ","), p, true
}

func (x *extractor) rustRoute(mc *matchCaps) (method, p string, ok bool) {
	p = unquote(x.text(mc.rPath))
	if !strings.HasPrefix(p, "/") {
		return "", "", false
	}
	if mc.rArg2 != nil && mc.rArg2.Kind() == "call_expression" {
		if m := rustVerbPattern.FindStringSubmatch(x.text(mc.rArg2)); m != nil {
			method = strings.ToUpper(m[1])
		}
	}
	return method, p, true
}

// ----------------------------------------------------------------- finish

// finish orders the results deterministically, assigns stable IDs and removes
// duplicates.
func (x *extractor) finish() fileExtract {
	sort.SliceStable(x.symbols, func(i, j int) bool {
		a, b := x.symbols[i], x.symbols[j]
		if a.StartLine != b.StartLine {
			return a.StartLine < b.StartLine
		}
		if a.QualifiedName != b.QualifiedName {
			return a.QualifiedName < b.QualifiedName
		}
		return a.Kind < b.Kind
	})
	seen := map[string]int{}
	out := make([]contracts.Symbol, 0, len(x.symbols))
	for _, s := range x.symbols {
		base := fmt.Sprintf("%s#%s:%s", s.Path, s.Kind, s.QualifiedName)
		key := fmt.Sprintf("%s@%d", base, s.StartLine)
		if seen[key] > 0 {
			continue
		}
		seen[key]++
		seen[base]++
		s.ID = base
		if seen[base] > 1 {
			s.ID = fmt.Sprintf("%s@%d", base, s.StartLine)
		}
		out = append(out, s)
	}

	sort.SliceStable(x.routes, func(i, j int) bool {
		a, b := x.routes[i], x.routes[j]
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return a.Method < b.Method
	})
	routes := make([]contracts.RouteEvidence, 0, len(x.routes))
	rseen := map[string]int{}
	for _, r := range x.routes {
		base := fmt.Sprintf("route:%s:%d:%s:%s", r.FilePath, r.Line, r.Method, r.Path)
		rseen[base]++
		r.ID = base
		if rseen[base] > 1 {
			r.ID = fmt.Sprintf("%s#%d", base, rseen[base])
		}
		routes = append(routes, r)
	}

	var imports []string
	iseen := map[string]bool{}
	for _, imp := range x.imports {
		if !iseen[imp] {
			iseen[imp] = true
			imports = append(imports, imp)
		}
	}
	return fileExtract{Symbols: out, Routes: routes, Imports: imports}
}
