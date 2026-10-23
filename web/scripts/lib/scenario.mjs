// The sample planning session the preview and promo harnesses play: a team
// adding invoice CSV exports to a billing service. Every payload has the
// shape the Go side sends (internal/app, internal/avatars, internal/relay), so
// the window renders exactly what a real session would show.

export const RUN_ID = "run-2026-10-22-01";
export const MEMBERS = ["owner:dana", "editor:priya"];

export const STATUS = {
  ast: "ok",
  astDetail: "214 files parsed, 3,871 symbols indexed",
  docHealth: 1,
  ocr: "available",
  repo: "billing-service",
  repoPath: "/Users/dana/src/billing-service",
  branch: "feat/invoice-exports",
  requestedHost: "claude",
  mainHost: "claude",
  mainModel: "claude-opus-5-5",
  judgeHost: "claude",
  judgeModel: "claude-sonnet-5-5",
  criticSharesMain: false,
  hostDiffers: false,
  context: "ok",
  contextDetail: "4 related note(s) from the recall index",
  impact: "ok",
  impactDetail: "9 part(s) of the project, 1 clash(es) with a decision already made",
};

export const BRIEF = `# Invoice exports for finance

Finance closes every week by exporting invoices by hand. It takes about 40 minutes and fails without a word for ranges longer than a quarter.

## Who it is for

Finance analysts who close the books, and compliance officers who audit them.

## Requirements

- [ ] Export invoices as CSV for any date range.
- [ ] Exports over 10,000 rows finish without a timeout.
- [ ] Every export records who ran it, which range, and when.
- [ ] Finance can download the last 20 exports again.

## Done when

- One month of invoices downloads in under 30 seconds.
- An auditor can list every export for a quarter.

## Out of scope

- Scheduled exports, and XLSX output.

Reuse \`QueryInvoices\` in \`internal/reporting\` and keep the serializer streaming.
`;

const comp = (name, kind, path, line, signature) => ({
  id: `${path}#${kind}:${name}`,
  name,
  kind,
  path,
  line,
  signature,
  module: path.slice(0, path.lastIndexOf("/")),
  language: "go",
});

export const COMPONENTS = [
  comp("QueryInvoices", "function", "internal/reporting/query.go", 42, "func QueryInvoices(ctx context.Context, r Range) (*Rows, error)"),
  comp("Range", "struct", "internal/reporting/query.go", 18, "type Range struct"),
  comp("Rows", "struct", "internal/reporting/rows.go", 9, "type Rows struct"),
  comp("ExportHandler", "function", "internal/api/export.go", 27, "func ExportHandler(svc *reporting.Service) http.HandlerFunc"),
  comp("Invoice", "struct", "internal/billing/invoice.go", 12, "type Invoice struct"),
  comp("ListInvoices", "function", "internal/billing/store.go", 61, "func (s *Store) ListInvoices(ctx context.Context, f Filter) ([]Invoice, error)"),
  comp("RecordEvent", "function", "internal/audit/log.go", 33, "func RecordEvent(ctx context.Context, e Event) error"),
  comp("Event", "struct", "internal/audit/log.go", 14, "type Event struct"),
  comp("ObjectStore", "interface", "internal/storage/object.go", 8, "type ObjectStore interface"),
  comp("SignedURL", "function", "internal/storage/object.go", 40, "func SignedURL(key string, ttl time.Duration) (string, error)"),
  comp("RequireRole", "function", "internal/auth/roles.go", 22, "func RequireRole(role Role) func(http.Handler) http.Handler"),
  comp("RoleFinance", "constant", "internal/auth/roles.go", 9, "const RoleFinance Role = \"finance\""),
];

export const MODULES = (() => {
  const counts = new Map();
  for (const c of COMPONENTS) counts.set(c.module, (counts.get(c.module) ?? 0) + 1);
  return [...counts.entries()].sort(([a], [b]) => a.localeCompare(b)).map(([path, symbols]) => ({ path, language: "go", symbols }));
})();

export const CONTEXT = {
  source: "recall-index",
  branch: "feat/invoice-exports",
  head: "4e1a9c07d2b3",
  recent_commits: ["Add reporting query range filters", "Store invoices per tenant", "Audit log: record login events"],
  hits: [
    {
      path: "docs/adrs/0007-exports-stream.md",
      heading: "ADR 7: Exports stream, never buffer",
      text: "# ADR 7: Exports stream, never buffer\n\nEvery export writes rows to the response as the query yields them. Memory stays flat, so a quarter of invoices costs the same as a week. Decided 2026-08-14.",
      score: 7.4,
    },
    {
      path: "docs/specs/audit-log.md",
      heading: "Audit events",
      text: "## Audit events\n\nEach event records actor, action, target and time. Compliance reads them through the audit API; events are kept for seven years.",
      score: 5.1,
    },
    {
      path: ".agents/STATE.md",
      heading: "State",
      text: "# State\n\nreporting: range filters merged → exports next\nblocked: none",
      score: 3.3,
    },
    {
      path: "docs/plans/q4-finance.md",
      heading: "Q4 finance asks",
      text: "## Q4 finance asks\n\n1. Self-serve invoice exports\n2. Re-download of recent exports\n3. Monthly reconciliation report (later)",
      score: 2.9,
    },
  ],
};

const VERDICT_WORD = { change: "Change", add: "New", reuse: "Already there", conflicts_with: "Clashes" };

/** One code link, as the impact map resolves it from a candidate. */
function codeLink(epicID, component, verdict, reason) {
  return {
    id: `${epicID}:code:${component.path}#${component.name}`,
    area: component.module, kind: "code", path: component.path, line: component.line,
    symbol_id: component.id, symbol_name: component.name, symbol_kind: component.kind,
    verdict, reason, confidence: "HIGH", source: "model", provenance: component.signature ?? "",
  };
}

/** One documentation, specification or decision link. */
function docLink(epicID, hit, verdict, reason) {
  const kind = hit.path.includes("/adrs/") ? "decision" : hit.path.startsWith("specs/") ? "spec" : "doc";
  return {
    id: `${epicID}:${kind}:${hit.path}#${hit.heading}`,
    area: hit.heading, kind, path: hit.path, heading: hit.heading,
    verdict, reason, confidence: "HIGH", source: "model", provenance: hit.text,
  };
}

function planFile(id, title, goal, requirements, criteria, links) {
  const slug = title.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "");
  const touches = links.filter((l) => l.verdict !== "conflicts_with");
  const conflicts = links.filter((l) => l.verdict === "conflicts_with");
  const table = touches.length
    ? "| Part of the project | What happens | Where | Why |\n| :--- | :--- | :--- | :--- |\n" +
      touches
        .map((l) => {
          const what = l.symbol_name ? `\`${l.symbol_name}\`` : l.heading || l.area;
          const where = l.path ? `\`${l.path}${l.line ? `:${l.line}` : ""}\`` : "_nothing yet_";
          return `| ${what} | ${VERDICT_WORD[l.verdict] ?? "Possibly involved"} | ${where} | ${l.reason} |`;
        })
        .join("\n") + "\n"
    : "_Nothing in this project matches this epic. It looks like new work._\n";
  const markdown =
    `# ${id} · ${title}\n\n> ${goal}\n\n## Requirements\n\n${requirements.map((r) => `- [ ] ${r}`).join("\n")}\n\n` +
    `## Acceptance criteria\n\n${criteria.map((c) => `- ${c}`).join("\n")}\n\n## What this touches\n\n${table}` +
    (conflicts.length
      ? `\n## Conflicts with decisions already made\n\n${conflicts
          .map((l) => `- \`${l.path}\` — **${l.heading}**: ${l.reason}`)
          .join("\n")}\n`
      : "");
  return {
    id, title, goal, filename: `${id.toLowerCase()}-${slug}.md`, markdown,
    requirements: requirements.length, criteria: criteria.length, links,
    components: links
      .filter((l) => l.kind === "code")
      .map((l) => ({
        id: l.symbol_id, name: l.symbol_name, kind: l.symbol_kind, path: l.path, line: l.line,
        module: l.area, language: "go", verdict: l.verdict, reason: l.reason, confidence: l.confidence,
      })),
    notes: links
      .filter((l) => l.kind !== "code")
      .map((l) => ({ path: l.path, heading: l.heading, text: l.provenance, score: 0 })),
  };
}

const byName = (...names) => names.map((n) => COMPONENTS.find((c) => c.name === n));

const one = (name) => byName(name)[0];

export const PLAN_FILES = [
  planFile(
    "E1", "CSV invoice export", "Finance downloads any date range of invoices as a CSV file.",
    ["Export invoices as CSV for any date range.", "Exports over 10,000 rows finish without a timeout."],
    ["One month of invoices downloads in under 30 seconds."],
    [
      codeLink("E1", one("QueryInvoices"), "change", "Invoices are read here today, and this adds the date range finance asked for."),
      codeLink("E1", one("Range"), "reuse", "A date range is already described this way, so the export can use it unchanged."),
      codeLink("E1", one("ExportHandler"), "change", "This is what answers an export request, and it has to learn the new range."),
      codeLink("E1", one("Invoice"), "reuse", "The invoice record already holds every field the file needs."),
      docLink("E1", CONTEXT.hits[0], "change", "The notes describe the export as a one-click action, which this work finally delivers."),
    ],
  ),
  planFile(
    "E2", "Export audit trail", "Every export can be traced to a person, a range and a time.",
    ["Every export records who ran it, which range, and when."],
    ["An auditor can list every export for a quarter."],
    [
      codeLink("E2", one("RecordEvent"), "change", "Events are written here today; exports are not among them yet."),
      codeLink("E2", one("Event"), "change", "The record of an event has no room for a date range, so it gains one."),
      codeLink("E2", one("RequireRole"), "reuse", "Who may run an export is already decided here."),
      docLink("E2", CONTEXT.hits[1], "conflicts_with", "The recorded decision keeps the audit trail out of the request path, and these requirements write to it on every export."),
    ],
  ),
  planFile(
    "E3", "Export history", "Finance can download a recent export again without re-running it.",
    ["Finance can download the last 20 exports again."],
    ["A re-download returns the same file as the first one."],
    [
      codeLink("E3", one("ObjectStore"), "reuse", "Finished files are already kept here, so a re-download reads one back."),
      codeLink("E3", one("SignedURL"), "reuse", "A time-limited download link is already produced this way."),
      { id: "E3:new:history", area: "Export history list", kind: "code", verdict: "add", confidence: "MEDIUM", source: "model",
        reason: "Nothing in the project keeps a list of past exports for someone to pick from." },
      docLink("E3", CONTEXT.hits[3], "change", "The plan lists re-download as a later ask, and this brings it forward."),
    ],
  ),
];

/** GET /api/impact, as the model's pass would leave it. */
export const IMPACT = {
  schema_version: 1,
  state: "ready",
  revision: 2,
  prd_revision: 1,
  summary: "This work touches 9 parts of the project: 4 change, 1 is new, 4 are reused, 1 clashes with a decision already made.",
  whole: PLAN_FILES.flatMap((f) => f.links).filter((l) => l.verdict !== "conflicts_with").slice(0, 8),
  epics: PLAN_FILES.map((f) => ({ epic_id: f.id, title: f.title, links: f.links })),
  conflicts: PLAN_FILES.flatMap((f) => f.links).filter((l) => l.verdict === "conflicts_with"),
  notes: [],
};

// --- deliberation (Go-shaped AVATAR_* payloads) ------------------------------------------

export const PM_DOC = {
  epics: [
    { id: "E1", title: "CSV invoice export", goal: "Finance downloads any date range of invoices as a CSV file." },
    { id: "E2", title: "Export audit trail", goal: "Every export can be traced to a person, a range and a time." },
    { id: "E3", title: "Export history", goal: "Finance can download a recent export again without re-running it." },
  ],
  stories: [
    { id: "S1", epic_id: "E1", actor: "finance analyst", need: "export a date range of invoices as CSV", benefit: "I close the week without manual work", acceptance_criteria: ["One month downloads in under 30 seconds", "An empty range returns a file with a header row"], requirement_ids: ["REQ-1", "REQ-2"] },
    { id: "S2", epic_id: "E2", actor: "compliance officer", need: "list every export for a period", benefit: "I can answer an audit without asking engineering", acceptance_criteria: ["Each export shows who ran it, the range and the time"], requirement_ids: ["REQ-3"] },
    { id: "S3", epic_id: "E3", actor: "finance analyst", need: "download one of my last 20 exports again", benefit: "I do not wait for the same export twice", acceptance_criteria: ["A re-download returns the same file"], requirement_ids: ["REQ-4"] },
  ],
};

export const ARCHITECT_DOC = {
  summary: "Exports stream from QueryInvoices through a new CSV writer; each run writes one audit event and stores the file for re-download.",
  constraints: ["Exports stream; memory stays flat (ADR 7).", "Only the finance role can export."],
  workstreams: [
    { id: "reporting", role: "backend", module_paths: ["internal/reporting"], depends_on: [], contract_ids: ["export-csv"] },
    { id: "audit", role: "backend", module_paths: ["internal/audit"], depends_on: ["reporting"], contract_ids: ["record-export"] },
    { id: "web", role: "frontend", module_paths: ["web/src/exports"], depends_on: ["reporting"], contract_ids: [] },
  ],
  contracts: [],
  diagrams: {
    architecture: {
      nodes: [
        { id: "ui", label: "Finance UI" }, { id: "api", label: "ExportHandler" }, { id: "q", label: "QueryInvoices" },
        { id: "csv", label: "CSV writer" }, { id: "audit", label: "RecordEvent" }, { id: "store", label: "ObjectStore" },
      ],
      edges: [
        { from: "ui", to: "api", label: "range" }, { from: "api", to: "q", label: "" }, { from: "q", to: "csv", label: "rows" },
        { from: "api", to: "audit", label: "event" }, { from: "csv", to: "store", label: "file" },
      ],
    },
    sequence: { participants: [], messages: [] },
  },
  doc_tasks: [],
};

export const CRITIQUES = [
  { id: "C-1", severity: "CRITICAL", target_ids: ["reporting"], evidence: "A query timeout mid-export leaves a partial file with no marker.", impact: "Auditors would see phantom rows.", required_change: "Write a trailer row and discard files without it.", status: "resolved", resolution_ref: "reporting" },
  { id: "C-2", severity: "HIGH", target_ids: ["audit"], evidence: "A retried export re-runs the range without an idempotency key.", impact: "Duplicate audit events for one export.", required_change: "Key audit events by export ID.", status: "open" },
  { id: "C-3", severity: "MEDIUM", target_ids: ["web"], evidence: "The history list shows file names, not the range.", impact: "Analysts cannot tell exports apart.", required_change: "Show range and row count in the list.", status: "open" },
];

export const DECISION = {
  decision_id: "d-1",
  run_id: RUN_ID,
  input_revision: "4",
  question: "Where should a large export be written?",
  options: [
    { id: "a", label: "Stream the download", tradeoff: "No storage, but a dropped connection loses the export" },
    { id: "b", label: "Stage the file, then link it", tradeoff: "Survives a dropped connection and enables re-download; needs a cleanup job" },
  ],
  recommended_option_id: "b",
};

export const SPEC_MARKDOWN = `run_id: ${RUN_ID}

## Executive summary

Invoice CSV exports ship in one sprint. Exports stream from \`QueryInvoices\` through a new CSV writer, every run writes one audit event keyed by export ID, and the file is staged in object storage so finance can download it again. The critique closed one CRITICAL (partial files) and one HIGH (duplicate audit events) before convergence.

## Immutable constraints

- Exports stream; memory stays flat regardless of row count.
- Every export writes exactly one audit event, including failures.
- Only the finance role can export.

## AST targets

- \`internal/reporting/query.go\` — extend \`QueryInvoices\` with streaming rows
- \`internal/reporting/csv.go\` — new streaming writer (\`WriteCSV\`)
- \`internal/audit/log.go\` — \`RecordEvent\` keyed by export ID

## Documentation Sync

- [ ] docs/reporting.md: the CSV contract and limits
- [ ] CHANGELOG: user-facing entry for exports
`;

export const MANIFEST = {
  architecture_diagram: `graph LR
    UI[Finance UI] --> API[ExportHandler]
    API --> Q[QueryInvoices]
    Q --> CSV[CSV writer]
    API --> AUD[RecordEvent]
    CSV --> OS[ObjectStore]`,
  sequence_diagram: `sequenceDiagram
    participant F as Finance analyst
    participant A as ExportHandler
    participant Q as QueryInvoices
    F->>A: Export range
    A->>Q: Stream rows
    Q-->>A: Rows
    A-->>F: CSV file`,
  human_gantt: `gantt
    dateFormat YYYY-MM-DD
    axisFormat %b %d
    section Sprint
    Streaming CSV writer :2026-10-26, 2d
    Audit events by export ID :2026-10-28, 1d
    Staged files and history :2026-10-29, 2d`,
  agent_gantt: `gantt
    dateFormat HH:mm
    axisFormat %H:%M
    section Agent run
    Reporting :00:00, 14m
    Audit :00:14, 7m
    Web :00:21, 12m`,
  gantt_chips: { human: "5 days (45 hrs)", agent: "~33 mins" },
  epics: PM_DOC.epics.map((e) => ({
    id: e.id,
    title: e.title,
    goal: e.goal,
    stories: PM_DOC.stories
      .filter((s) => s.epic_id === e.id)
      .map((s) => ({
        id: `${e.id}-${s.id}`,
        need: `As a ${s.actor}, I ${s.need}, so ${s.benefit}.`,
        subtasks: [
          {
            id: `${e.id}-${s.id}-T1`,
            title: s.need.charAt(0).toUpperCase() + s.need.slice(1),
            severity: e.id === "E1" ? "HIGH" : "MEDIUM",
            acceptance: s.acceptance_criteria,
            targets: e.id === "E1" ? ["internal/reporting/csv.go"] : e.id === "E2" ? ["internal/audit/log.go"] : ["internal/storage/object.go"],
            verification: [e.id === "E1" ? "go test ./internal/reporting/..." : e.id === "E2" ? "go test ./internal/audit/..." : "go test ./internal/storage/..."],
            humanEstimate: "1 day (9 hrs)",
            agentEstimate: "~10 mins",
          },
        ],
      })),
  })),
};
