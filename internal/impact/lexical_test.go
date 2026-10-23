//go:build client && !server

package impact

import (
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

// fixture mirrors internal/app's plannedExecutor, so the ranking this package
// took over from the plan rail is compared against the same repository and the
// same two epics the rail's test has always used.
func fixture() Input {
	return Input{
		Repo: "/repo",
		Graph: &contracts.RepoContextGraph{
			WorktreeHash:  "wt-1",
			DetectedStack: []string{"go"},
			Symbols: map[string][]contracts.Symbol{
				"internal/billing/export.go": {
					sym("internal/billing/export.go", "ExportInvoices", contracts.SymbolFunction, 12, true),
					sym("internal/billing/export.go", "writeRow", contracts.SymbolFunction, 40, false),
				},
				"internal/billing/invoice.go": {sym("internal/billing/invoice.go", "Invoice", contracts.SymbolStruct, 5, true)},
				"internal/mail/send.go":       {sym("internal/mail/send.go", "SendDigest", contracts.SymbolFunction, 9, true)},
			},
		},
		PRD: contracts.NormalizedPRD{Revision: 3,
			Product: contracts.ProductPRD{
				Title:        "Billing export",
				Requirements: []string{"Export invoices for a date range"},
			},
			Epics: []contracts.DraftEpic{
				{ID: "E1", Title: "Invoice export", Goal: "Finance exports invoices.",
					Requirements: []string{"Export invoices for a date range"}, AcceptanceCriteria: []string{}},
				{ID: "E2", Title: "Customer onboarding tour", Requirements: []string{}, AcceptanceCriteria: []string{}},
			},
		},
		Context: contracts.ProjectContext{Hits: []contracts.ContextHit{
			{Path: "docs/adrs/0004-invoices.md", Heading: "Invoice export format", Text: "Invoices export as CSV.", Score: 4},
			{Path: "docs/plans/search.md", Heading: "Search", Text: "Ranking results.", Score: 1},
		}},
	}
}

func epicLinks(m contracts.ImpactMap, id string) []contracts.ImpactLink {
	for _, e := range m.Epics {
		if e.EpicID == id {
			return e.Links
		}
	}
	return nil
}

// TestLexicalReproducesTheRailRanking is the gate on moving rankComponents and
// rankNotes out of internal/app: the same repository and the same epics must
// still put ExportInvoices first and keep the unrelated search note out.
func TestLexicalReproducesTheRailRanking(t *testing.T) {
	m := Lexical(fixture())

	if m.State != contracts.ImpactLexical || m.PRDRevision != 3 || m.WorktreeHash != "wt-1" {
		t.Fatalf("map identity = %+v", m)
	}
	if len(m.Epics) != 2 {
		t.Fatalf("epics = %d, want 2", len(m.Epics))
	}

	e1 := epicLinks(m, "E1")
	if len(e1) == 0 || e1[0].SymbolName != "ExportInvoices" {
		t.Fatalf("E1 links = %+v, want ExportInvoices first", e1)
	}
	if e1[0].Path != "internal/billing/export.go" || e1[0].Line != 12 {
		t.Errorf("E1 first link location = %s:%d, want internal/billing/export.go:12", e1[0].Path, e1[0].Line)
	}

	var docs []contracts.ImpactLink
	for _, l := range e1 {
		if l.Kind != contracts.ImpactKindCode {
			docs = append(docs, l)
		}
		if l.SymbolName == "writeRow" {
			t.Error("a private declaration was linked")
		}
		if l.SymbolName == "SendDigest" {
			t.Error("an unrelated declaration was linked")
		}
	}
	if len(docs) != 1 || docs[0].Path != "docs/adrs/0004-invoices.md" {
		t.Errorf("E1 document links = %+v, want the invoice ADR only", docs)
	}
	if docs[0].Kind != contracts.ImpactKindDecision {
		t.Errorf("an ADR link kind = %q, want %q", docs[0].Kind, contracts.ImpactKindDecision)
	}

	if got := epicLinks(m, "E2"); len(got) != 0 {
		t.Errorf("E2 links = %+v, want none: nothing in the repository matches it", got)
	}
}

// TestLexicalClaimsNothingItCannotKnow guards the honesty of the first paint: a
// keyword match may name a part of the project, but never says what happens to
// it or why.
func TestLexicalClaimsNothingItCannotKnow(t *testing.T) {
	m := Lexical(fixture())
	for _, l := range allLinks(m) {
		if l.Source != contracts.ImpactSourceLexical {
			t.Errorf("link %s source = %q, want %q", l.ID, l.Source, contracts.ImpactSourceLexical)
		}
		if l.Verdict != "" || l.Reason != "" {
			t.Errorf("link %s asserts verdict %q reason %q from a keyword match", l.ID, l.Verdict, l.Reason)
		}
	}
	if len(m.Conflicts) != 0 {
		t.Errorf("conflicts = %+v, want none: a keyword match cannot find a contradiction", m.Conflicts)
	}
	if !strings.Contains(m.Summary, "matched by name") {
		t.Errorf("summary = %q, want it to say the match is by name alone", m.Summary)
	}
}

func TestLexicalIsDeterministicAndNeverFails(t *testing.T) {
	in := fixture()
	first, second := Lexical(in), Lexical(in)
	if len(allLinks(first)) != len(allLinks(second)) {
		t.Fatalf("two passes disagree: %d and %d links", len(allLinks(first)), len(allLinks(second)))
	}
	for i, a := range allLinks(first) {
		if b := allLinks(second)[i]; a.ID != b.ID {
			t.Errorf("link %d = %q then %q; the ranking is not stable", i, a.ID, b.ID)
		}
	}

	for name, in := range map[string]Input{
		"no graph":       {PRD: fixture().PRD},
		"no epics":       {Graph: fixture().Graph},
		"nothing at all": {},
	} {
		m := Lexical(in)
		if m.Whole == nil || m.Epics == nil || m.Conflicts == nil {
			t.Errorf("%s: a null array reached the window: %+v", name, m)
		}
		if m.Summary == "" {
			t.Errorf("%s: no summary", name)
		}
	}
}

func TestLexicalCutsOversizedPRDsAndSaysSo(t *testing.T) {
	in := fixture()
	in.PRD.Epics = nil
	for i := 0; i < maxEpics+3; i++ {
		in.PRD.Epics = append(in.PRD.Epics, contracts.DraftEpic{ID: "E" + string(rune('a'+i)), Title: "Invoice export"})
	}
	m := Lexical(in)
	if len(m.Epics) != maxEpics {
		t.Errorf("epics = %d, want the %d cap", len(m.Epics), maxEpics)
	}
	if len(m.Notes) == 0 || !strings.Contains(m.Notes[0], "only the first") {
		t.Errorf("notes = %v, want the cut stated", m.Notes)
	}
}

func TestDocKindSeparatesDecisionsFromNotes(t *testing.T) {
	for path, want := range map[string]string{
		"docs/adrs/0004-invoices.md": contracts.ImpactKindDecision,
		"docs/adr/0001-thing.md":     contracts.ImpactKindDecision,
		"specs/billing.md":           contracts.ImpactKindSpec,
		"docs/specs/billing.md":      contracts.ImpactKindSpec,
		"docs/plans/search.md":       contracts.ImpactKindDoc,
		".agents/STATE.md":           contracts.ImpactKindDoc,
	} {
		if got := docKind(path); got != want {
			t.Errorf("docKind(%q) = %q, want %q", path, got, want)
		}
	}
}
