// Package synthtest builds a small, fully valid plan used by the synthesizer,
// avatar and application tests. It is data only: nothing here runs in the
// shipped binaries.
package synthtest

import (
	"encoding/json"
	"strings"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/synthesizer"
)

// Fixture is one consistent set of stage documents.
type Fixture struct {
	PRD         contracts.NormalizedPRD
	Graph       contracts.RepoContextGraph
	DocDrift    []contracts.DocDriftItem
	PM          synthesizer.PMDoc
	Architect   synthesizer.ArchitectDoc
	Specialists []synthesizer.SpecialistDoc
	Critiques   []contracts.Critique
}

func hex64(c string) string { return strings.Repeat(c, 64) }

func est(o, l, p float64, basis string) contracts.Estimate {
	return contracts.Estimate{Optimistic: o, Likely: l, Pessimistic: p, Basis: basis}
}

// Sample returns the invoice-export plan: two workstreams sharing one contract,
// a documentation task for one stale doc claim and resolved critiques.
func Sample() Fixture {
	graph := contracts.RepoContextGraph{
		SchemaVersion: contracts.SchemaVersion,
		RootPath:      "",
		GitCommitHash: strings.Repeat("a", 40),
		WorktreeHash:  hex64("b"),
		DetectedStack: []string{"go", "typescript"},
		Modules: []contracts.ModuleBoundary{
			{ID: "m-billing", Path: "internal/billing", Language: "go", SymbolIDs: []string{"sym-list"}},
			{ID: "m-web", Path: "web/src", Language: "typescript"},
		},
		Symbols: map[string][]contracts.Symbol{
			"internal/billing/invoice.go": {{
				ID: "sym-list", Name: "ListInvoices", Kind: contracts.SymbolFunction, Language: "go",
				Path: "internal/billing/invoice.go", StartLine: 10, EndLine: 30,
				Signature: "func ListInvoices(ctx context.Context, org string) ([]Invoice, error)", SignatureSHA256: hex64("c"),
				Visibility: "exported",
			}},
			"web/src/InvoiceTable.tsx": {{
				ID: "sym-table", Name: "InvoiceTable", Kind: contracts.SymbolFunction, Language: "typescript",
				Path: "web/src/InvoiceTable.tsx", StartLine: 1, EndLine: 40,
				Signature: "export function InvoiceTable(props: Props)", SignatureSHA256: hex64("d"), Visibility: "exported",
			}},
		},
		DocDriftReports: []contracts.DocDriftItem{},
		Coverage:        contracts.Coverage{FilesScanned: 12, FilesSkipped: 1},
	}
	drift := []contracts.DocDriftItem{{
		ID: "DOC-1", DocFilePath: "docs/billing.md", DocLine: 14, CodeFilePath: "internal/billing/invoice.go",
		SymbolName: "ListInvoices", DriftType: contracts.DriftStaleSignature,
		Expected: "ListInvoices(org)", Observed: "ListInvoices(ctx, org)",
		EvidenceKind: "signature", Confidence: contracts.ConfidenceHigh,
		RemediationTask: "Update `docs/billing.md` line 14: `ListInvoices(org)` -> `ListInvoices(ctx, org)`",
	}}
	prd := contracts.NormalizedPRD{
		SchemaVersion: contracts.SchemaVersion, Revision: 3,
		Product: contracts.ProductPRD{
			Title:              "Invoice export",
			Problem:            "Finance staff cannot take invoices out of the product for month-end reconciliation.",
			Goals:              []string{"Finance can reconcile invoices in a spreadsheet"},
			Actors:             []string{"Finance analyst"},
			Flows:              []string{"Analyst picks a date range and downloads a CSV"},
			Requirements:       []string{"Finance can export invoices as CSV", "Exports are limited to the signed-in organisation"},
			AcceptanceCriteria: []string{"An export contains every invoice in the selected date range", "An export never contains another organisation's invoices"},
			Constraints:        []string{"Exports must never include another organisation's invoices"},
			OpenQuestions:      []string{},
		},
		TechnicalAppendix: "Use the existing billing package.",
		SourceRefs:        []contracts.SourceRef{},
		Ambiguities:       []contracts.Ambiguity{},
	}
	pm := synthesizer.PMDoc{
		Epics: []contracts.Epic{{ID: "E1", Title: "Invoice export", Goal: "Finance can download invoices as a spreadsheet."}},
		Stories: []contracts.Story{
			{
				ID: "S1", EpicID: "E1", Actor: "Finance analyst", Need: "to download invoices for a date range as CSV",
				Benefit:            "month-end reconciliation takes minutes",
				AcceptanceCriteria: []string{"The CSV contains every invoice in the range"}, RequirementIDs: []string{"REQ-1"},
			},
			{
				ID: "S2", EpicID: "E1", Actor: "Finance analyst", Need: "to see only my organisation's invoices in exports",
				AcceptanceCriteria: []string{"No other organisation's invoice appears in the CSV"}, RequirementIDs: []string{"REQ-2"},
			},
		},
	}
	pm.Normalize()

	arch := synthesizer.ArchitectDoc{
		Summary:     "A billing export function feeds a download control in the web client.",
		Constraints: []string{"The export function must scope every query to the caller's organisation."},
		Workstreams: []contracts.Workstream{
			{ID: "billing-api", Role: contracts.RoleBackend, ModulePaths: []string{"internal/billing"}, ContractIDs: []string{"export-api"}},
			{ID: "web-ui", Role: contracts.RoleFrontend, ModulePaths: []string{"web/src"}, DependsOn: []string{"billing-api"}},
		},
		Contracts: []contracts.InterfaceContract{{
			ID: "export-api", OwnerWorkstream: "billing-api", Language: "go",
			Signature:           "func ExportInvoices(ctx context.Context, req ExportRequest) (io.Reader, error)",
			InputSchema:         "ExportRequest{org_id string, from date, to date}",
			OutputSchema:        "text/csv",
			Errors:              []string{"ORG_FORBIDDEN", "RANGE_INVALID"},
			ConsumerWorkstreams: []string{"web-ui"},
		}},
		Diagrams: synthesizer.DiagramSet{
			Architecture: synthesizer.GraphDiagram{
				Nodes: []synthesizer.DiagramNode{{ID: "ui", Label: "Web client"}, {ID: "api", Label: "Billing API"}, {ID: "db", Label: "Invoice store"}},
				Edges: []synthesizer.DiagramEdge{{From: "ui", To: "api", Label: "export request"}, {From: "api", To: "db", Label: "query invoices"}},
			},
			Sequence: synthesizer.SequenceDiagram{
				Participants: []synthesizer.DiagramNode{{ID: "u", Label: "Analyst"}, {ID: "c", Label: "Web client"}, {ID: "a", Label: "Billing API"}},
				Messages: []synthesizer.SequenceMessage{
					{From: "u", To: "c", Label: "choose range", Kind: synthesizer.MessageCall},
					{From: "c", To: "a", Label: "ExportInvoices", Kind: synthesizer.MessageCall},
					{From: "a", To: "c", Label: "csv stream", Kind: synthesizer.MessageReturn},
				},
			},
		},
		DocTasks: []contracts.TechnicalTask{{
			ID: "T-DOC-1", EpicID: "E1", WorkstreamID: "docs", Title: "Refresh the billing documentation",
			Steps:              []string{"Update the ListInvoices call in docs/billing.md"},
			Targets:            []contracts.Target{{Path: "docs/billing.md", Operation: contracts.OpDocument}},
			AcceptanceCriteria: []string{"docs/billing.md shows the current ListInvoices signature"},
			Verification:       []contracts.VerificationCommand{},
			DocDriftIDs:        []string{"DOC-1"},
			HumanEstimate:      est(0.25, 0.5, 1, "small documentation edit"),
			AgentEstimate:      est(2, 3, 6, "single file edit"),
		}},
	}
	synthesizer.NormalizeContracts(&arch)
	hash := arch.Contracts[0].VersionHash
	claim := synthesizer.InterfaceClaim{
		ContractID: "export-api", VersionHash: hash,
		InputSchema: "ExportRequest{org_id string, from date, to date}", OutputSchema: "text/csv",
		Errors: []string{"ORG_FORBIDDEN", "RANGE_INVALID"},
	}
	be := synthesizer.SpecialistDoc{
		WorkstreamID: "billing-api",
		Summary:      "Add an organisation-scoped CSV export beside the existing invoice listing.",
		DataModels:   []string{"ExportRequest{org_id, from, to}"},
		Implements:   []synthesizer.InterfaceClaim{claim},
		Tasks: []contracts.TechnicalTask{{
			ID: "T-BE-1", EpicID: "E1", StoryID: "S1", WorkstreamID: "billing-api", Title: "Implement ExportInvoices",
			Steps: []string{"Create internal/billing/export.go", "Stream CSV rows from ListInvoices"},
			Targets: []contracts.Target{
				{Path: "internal/billing/export.go", Operation: contracts.OpAdd},
				{Path: "internal/billing/invoice.go", Operation: contracts.OpModify, SymbolID: "sym-list"},
			},
			AcceptanceCriteria: []string{"The CSV contains every invoice in the range"},
			Verification: []contracts.VerificationCommand{
				{ID: "V-BE-1", Argv: []string{"go", "test", "./internal/billing/..."}, Cwd: ".", TimeoutSeconds: 120, ExpectedExitCode: 0},
			},
			HumanEstimate: est(2, 4, 8, "similar export endpoints took half a day"),
			AgentEstimate: est(8, 12, 25, "one new file and one edit"),
		}, {
			ID: "T-BE-2", EpicID: "E1", StoryID: "S2", WorkstreamID: "billing-api", Title: "Scope exports to the organisation",
			Steps:              []string{"Reject requests whose org_id differs from the caller", "Add a regression test"},
			DependsOn:          []string{"T-BE-1"},
			Targets:            []contracts.Target{{Path: "internal/billing/export_test.go", Operation: contracts.OpAdd}},
			AcceptanceCriteria: []string{"No other organisation's invoice appears in the CSV"},
			Verification: []contracts.VerificationCommand{
				{ID: "V-BE-2", Argv: []string{"go", "test", "./internal/billing/...", "-run", "Scope"}, Cwd: ".", TimeoutSeconds: 120, ExpectedExitCode: 0},
			},
			HumanEstimate: est(1, 2, 4, "one guard and one test"),
			AgentEstimate: est(4, 6, 12, "one test file"),
		}},
	}
	fe := synthesizer.SpecialistDoc{
		WorkstreamID: "web-ui",
		Summary:      "Add a download control that calls the export function.",
		Consumes:     []synthesizer.InterfaceClaim{claim},
		Tasks: []contracts.TechnicalTask{{
			ID: "T-FE-1", EpicID: "E1", StoryID: "S1", WorkstreamID: "web-ui", Title: "Add the export button",
			Steps:     []string{"Add a download button to the invoice table", "Call the export function with the chosen range"},
			DependsOn: []string{"T-BE-1"},
			Targets: []contracts.Target{
				{Path: "web/src/InvoiceTable.tsx", Operation: contracts.OpModify, SymbolID: "sym-table"},
			},
			AcceptanceCriteria: []string{"The button downloads a CSV for the chosen range"},
			Verification:       []contracts.VerificationCommand{},
			HumanEstimate:      est(1, 3, 5, "one component change"),
			AgentEstimate:      est(5, 8, 15, "one component edit"),
		}},
	}
	crit := []contracts.Critique{
		{
			ID: "C-1", Severity: contracts.SeverityHigh, TargetIDs: []string{"billing-api"}, Evidence: "The export did not check the caller's organisation.",
			RequiredChange: "Reject mismatched org_id.", Status: contracts.CritiqueResolved, ResolutionRef: "T-BE-2",
		},
		{
			ID: "C-2", Severity: contracts.SeverityMedium, TargetIDs: []string{"web-ui"}, Evidence: "Large exports may time out in the browser.",
			RequiredChange: "Stream the download.", Status: contracts.CritiqueOpen,
		},
	}
	fe.Accepted = []synthesizer.AcceptedTradeoff{{CritiqueID: "C-2", Rationale: "Expected exports are small; revisit if timeouts are reported."}}

	return Fixture{
		PRD: prd, Graph: graph, DocDrift: drift, PM: pm, Architect: arch,
		Specialists: []synthesizer.SpecialistDoc{be, fe}, Critiques: crit,
	}
}

func mustRaw(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return raw
}

// Input assembles the SynthesisInput for the fixture.
func (f Fixture) Input() contracts.SynthesisInput {
	specs := make([]json.RawMessage, len(f.Specialists))
	for i, s := range f.Specialists {
		specs[i] = mustRaw(s)
	}
	return contracts.SynthesisInput{
		PRD: f.PRD, PM: mustRaw(f.PM), Architecture: mustRaw(f.Architect), Specialists: specs,
		Critiques: f.Critiques, Graph: f.Graph, DocDrift: f.DocDrift,
	}
}
