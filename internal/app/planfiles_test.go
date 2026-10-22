//go:build client && !server

package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danielckv/agentic-plan-forge/internal/ast"
	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

func sym(path, name, kind string, line int, public bool) contracts.Symbol {
	vis := ast.VisibilityPrivate
	if public {
		vis = ast.VisibilityPublic
	}
	return contracts.Symbol{
		ID: path + "#" + kind + ":" + name, Name: name, Kind: kind, Language: "go", Path: path,
		StartLine: line, Visibility: vis, Signature: "func " + name + "()",
	}
}

// plannedExecutor holds a scanned graph and an interpretation with two epics.
func plannedExecutor(t *testing.T) *executor {
	t.Helper()
	exec := newExecutor(executorConfig{Repo: t.TempDir(), Logger: quietLogger()})
	exec.graph = &contracts.RepoContextGraph{
		DetectedStack: []string{"go"},
		Symbols: map[string][]contracts.Symbol{
			"internal/billing/export.go": {
				sym("internal/billing/export.go", "ExportInvoices", contracts.SymbolFunction, 12, true),
				sym("internal/billing/export.go", "writeRow", contracts.SymbolFunction, 40, false),
			},
			"internal/billing/invoice.go": {sym("internal/billing/invoice.go", "Invoice", contracts.SymbolStruct, 5, true)},
			"internal/mail/send.go":       {sym("internal/mail/send.go", "SendDigest", contracts.SymbolFunction, 9, true)},
		},
	}
	exec.norm = &contracts.NormalizedPRD{Revision: 3, Epics: []contracts.DraftEpic{
		{ID: "E1", Title: "Invoice export", Goal: "Finance exports | invoices.", Requirements: []string{"Export invoices for a date range"}, AcceptanceCriteria: []string{}},
		{ID: "E2", Title: "Customer onboarding tour", Requirements: []string{}, AcceptanceCriteria: []string{}},
	}}
	exec.projectCtx = &contracts.ProjectContext{Hits: []contracts.ContextHit{
		{Path: "docs/adrs/0004-invoices.md", Heading: "Invoice export format", Text: "Invoices export as CSV.", Score: 4},
		{Path: "docs/plans/search.md", Heading: "Search", Text: "Ranking results.", Score: 1},
	}}
	return exec
}

func TestComponentsListsBlocksThenSymbols(t *testing.T) {
	exec := plannedExecutor(t)

	blocks := exec.Components("", "")
	if !blocks.Ready || len(blocks.Symbols) != 0 || len(blocks.Modules) != 2 {
		t.Fatalf("blocks = %+v, want two modules and no symbols", blocks)
	}
	if m := blocks.Modules[0]; m.Path != "internal/billing" || m.Symbols != 2 {
		t.Errorf("first block = %+v, want internal/billing with its 2 public declarations", m)
	}

	billing := exec.Components("internal/billing", "")
	if len(billing.Symbols) != 2 || billing.Symbols[0].Name != "ExportInvoices" || billing.Symbols[1].Name != "Invoice" {
		t.Errorf("module listing = %+v, want the public declarations by path and line", billing.Symbols)
	}

	found := exec.Components("", "invoice")
	if len(found.Symbols) != 2 || found.Symbols[0].Name != "Invoice" {
		t.Errorf("search = %+v, want the exact name first", found.Symbols)
	}
	for _, c := range found.Symbols {
		if c.Name == "writeRow" {
			t.Error("a private declaration was offered")
		}
	}

	empty := newExecutor(executorConfig{Logger: quietLogger()}).Components("", "")
	if empty.Ready || empty.Modules == nil || empty.Symbols == nil {
		t.Errorf("before the scan = %+v, want not ready with empty lists", empty)
	}
}

func TestPlanFilesLinkEpicsToCodeAndNotes(t *testing.T) {
	v := plannedExecutor(t).PlanFiles()
	if v.Revision != 3 || len(v.Files) != 2 {
		t.Fatalf("plan files = %+v", v)
	}
	f := v.Files[0]
	if f.Filename != "e1-invoice-export.md" || f.Requirements != 1 || f.Criteria != 0 {
		t.Errorf("file = %+v", f)
	}
	if len(f.Components) == 0 || f.Components[0].Name != "ExportInvoices" {
		t.Errorf("components = %+v, want ExportInvoices first", f.Components)
	}
	if len(f.Notes) != 1 || f.Notes[0].Path != "docs/adrs/0004-invoices.md" {
		t.Errorf("notes = %+v, want the invoice ADR only", f.Notes)
	}
	for _, want := range []string{
		"# E1 · Invoice export", "> Finance exports | invoices.", "- [ ] Export invoices for a date range",
		"| `ExportInvoices` | function | `internal/billing/export.go:12` |", "`docs/adrs/0004-invoices.md` — Invoice export format",
	} {
		if !strings.Contains(f.Markdown, want) {
			t.Errorf("markdown lacks %q:\n%s", want, f.Markdown)
		}
	}
	if !strings.Contains(v.Files[1].Markdown, "No existing component matches") {
		t.Errorf("an epic with no match must say so:\n%s", v.Files[1].Markdown)
	}

	none := newExecutor(executorConfig{Logger: quietLogger()}).PlanFiles()
	if none.Revision != 0 || none.Files == nil || len(none.Files) != 0 {
		t.Errorf("before interpretation = %+v, want revision 0 and no files", none)
	}
}

func TestPlanFileTableCellsCannotBreakTheTable(t *testing.T) {
	md := renderPlanFile(contracts.DraftEpic{ID: "E1", Title: "T"}, []ComponentRef{
		{Name: "a|b`c", Kind: "function", Path: "x|y.go", Line: 1},
	}, nil)
	if !strings.Contains(md, "| `a\\|b'c` | function | `x\\|y.go:1` |") {
		t.Errorf("unsafe cell rendered as:\n%s", md)
	}
}

func TestComponentAndPlanFileRoutes(t *testing.T) {
	s := &appSession{exec: plannedExecutor(t)}
	for _, tc := range []struct {
		path    string
		handler http.HandlerFunc
		code    int
	}{
		{"/api/components?module=internal/billing", s.handleComponents, http.StatusOK},
		{"/api/components?q=" + strings.Repeat("x", 201), s.handleComponents, http.StatusBadRequest},
		{"/api/plan-files", s.handlePlanFiles, http.StatusOK},
	} {
		rec := httptest.NewRecorder()
		tc.handler(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if rec.Code != tc.code {
			t.Errorf("%s = %d, want %d", tc.path, rec.Code, tc.code)
		}
		if tc.code == http.StatusOK && !json.Valid(rec.Body.Bytes()) {
			t.Errorf("%s returned invalid JSON", tc.path)
		}
	}
	follower := &appSession{}
	rec := httptest.NewRecorder()
	follower.handleComponents(rec, httptest.NewRequest(http.MethodGet, "/api/components", nil))
	if !strings.Contains(rec.Body.String(), `"ready":false`) {
		t.Errorf("a follower must report not ready: %s", rec.Body.String())
	}
}
