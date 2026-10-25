package synthesizer_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/synthesizer"
	"github.com/danielckv/agentic-plan-forge/internal/synthesizer/synthtest"
)

func compile(t *testing.T, in contracts.SynthesisInput, meta contracts.ApprovalMeta) synthesizer.CompiledPlan {
	t.Helper()
	plan, err := synthesizer.Compile(in, meta)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	return plan
}

func TestCompileIsByteIdentical(t *testing.T) {
	in := synthtest.Sample().Input()
	meta := contracts.ApprovalMeta{ApprovalID: "11111111-2222-4333-8444-555555555555", CreatedAt: time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)}
	a, b := compile(t, in, meta), compile(t, in, meta)
	if !bytes.Equal(a.SpecMarkdown, b.SpecMarkdown) {
		t.Fatal("spec Markdown differs between two compilations of the same input")
	}
	if !bytes.Equal(a.ManifestJSON, b.ManifestJSON) {
		t.Fatal("manifest differs between two compilations of the same input")
	}
	if bytes.Contains(a.SpecMarkdown, []byte("\r")) {
		t.Fatal("spec contains a carriage return; output must be canonical LF")
	}
	if !bytes.HasSuffix(a.SpecMarkdown, []byte("\n")) {
		t.Fatal("spec must end with a newline")
	}
}

func TestCompileSectionOrder(t *testing.T) {
	plan := compile(t, synthtest.Sample().Input(), contracts.ApprovalMeta{})
	spec := string(plan.SpecMarkdown)
	last := -1
	for _, heading := range synthesizer.SectionOrder {
		idx := strings.Index(spec, "\n## "+heading+"\n")
		if idx < 0 {
			t.Fatalf("section %q is missing", heading)
		}
		if idx <= last {
			t.Fatalf("section %q is out of order", heading)
		}
		last = idx
	}
	if !strings.HasPrefix(spec, "# Invoice export\n") {
		t.Fatalf("spec must start with the title, got %q", spec[:40])
	}
	if !strings.Contains(spec[:strings.Index(spec, "\n## ")], "Approval ID") {
		t.Fatal("title block must carry the approval metadata")
	}
}

func TestContentHashIgnoresApprovalMeta(t *testing.T) {
	in := synthtest.Sample().Input()
	h := synthesizer.ContentHash(in)
	if len(h) != 64 {
		t.Fatalf("ContentHash = %q, want 64 hex chars", h)
	}
	if h != synthesizer.ContentHash(in) {
		t.Fatal("ContentHash is not stable")
	}
	zero := compile(t, in, contracts.ApprovalMeta{})
	if h != zero.ContentSHA256() {
		t.Fatal("ContentHash must equal the hash of the output compiled with zeroed ApprovalMeta")
	}
	meta := contracts.ApprovalMeta{ApprovalID: "approval-1", CreatedAt: time.Unix(1_800_000_000, 0).UTC()}
	approved := compile(t, in, meta)
	if bytes.Equal(zero.SpecMarkdown, approved.SpecMarkdown) || bytes.Equal(zero.ManifestJSON, approved.ManifestJSON) {
		t.Fatal("ApprovalMeta must change the compiled bytes")
	}
	// Whatever the metadata, the hash the relay compares is unchanged.
	if synthesizer.ContentHash(in) != h {
		t.Fatal("ContentHash changed after compiling with approval metadata")
	}
	var m contracts.PlanManifest
	if err := json.Unmarshal(approved.ManifestJSON, &m); err != nil {
		t.Fatal(err)
	}
	if m.ApprovalID != "approval-1" || m.SpecSHA256 != approved.SpecSHA256 {
		t.Fatalf("manifest approval/spec hash mismatch: %+v", m)
	}
	changed := synthtest.Sample()
	changed.PRD.Product.Title = "Different title"
	if synthesizer.ContentHash(changed.Input()) == h {
		t.Fatal("ContentHash must change when the input changes")
	}
}

func TestManifestHasNoAbsolutePaths(t *testing.T) {
	f := synthtest.Sample()
	f.Graph.RootPath = "/Users/someone/secret-checkout"
	plan := compile(t, f.Input(), contracts.ApprovalMeta{})
	for _, b := range [][]byte{plan.SpecMarkdown, plan.ManifestJSON} {
		if bytes.Contains(b, []byte("secret-checkout")) {
			t.Fatal("compiled output leaks the local repository root")
		}
	}
	f.Architect.Summary = "see /Users/someone/secret-checkout/internal"
	if _, err := synthesizer.Compile(f.Input(), contracts.ApprovalMeta{}); err == nil {
		t.Fatal("Compile must refuse output that contains the local root path")
	}
}

func TestCompileRejectsInvalidDesigns(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*synthtest.Fixture)
		want   string
	}{
		{"open high critique", func(f *synthtest.Fixture) { f.Critiques[0].Status = contracts.CritiqueOpen }, "still open"},
		{"unaccepted medium", func(f *synthtest.Fixture) { f.Specialists[1].Accepted = nil }, "MEDIUM"},
		{"uncovered story", func(f *synthtest.Fixture) {
			f.Specialists[1].Tasks[0].StoryID = "S1"
			f.Specialists[0].Tasks[1].StoryID = "S1"
			f.Specialists[0].Tasks[1].EpicID = "E1"
		}, "uncovered"},
		{"missing doc task", func(f *synthtest.Fixture) { f.Architect.DocTasks = nil }, "DOC-1"},
		{"dependency cycle", func(f *synthtest.Fixture) { f.Specialists[0].Tasks[0].DependsOn = []string{"T-FE-1"} }, "cycle"},
		{"absolute target", func(f *synthtest.Fixture) { f.Specialists[0].Tasks[0].Targets[0].Path = "/etc/passwd" }, "repository-relative"},
		{"traversal target", func(f *synthtest.Fixture) { f.Specialists[0].Tasks[0].Targets[0].Path = "../outside.go" }, "repository-relative"},
		{"nonexistent symbol", func(f *synthtest.Fixture) { f.Specialists[0].Tasks[0].Targets[1].SymbolID = "sym-nope" }, "does not exist"},
		{"modify of unknown file", func(f *synthtest.Fixture) {
			f.Specialists[0].Tasks[0].Targets[1] = contracts.Target{Path: "internal/billing/ghost.go", Operation: contracts.OpModify}
		}, "must be operation add"},
		{"shared target ownership", func(f *synthtest.Fixture) {
			f.Specialists[1].Tasks[0].Targets = append(f.Specialists[1].Tasks[0].Targets,
				contracts.Target{Path: "internal/billing/export.go", Operation: contracts.OpModify})
		}, "is changed by workstreams"},
		{"missing consumer claim", func(f *synthtest.Fixture) { f.Specialists[1].Consumes = nil }, "does not declare"},
		{"stale contract version", func(f *synthtest.Fixture) { f.Specialists[1].Consumes[0].VersionHash = "stale" }, "version"},
		{"estimate without basis", func(f *synthtest.Fixture) { f.Specialists[0].Tasks[0].HumanEstimate.Basis = "" }, "basis"},
		{"estimate disorder", func(f *synthtest.Fixture) { f.Specialists[0].Tasks[0].AgentEstimate.Optimistic = 99 }, "optimistic <= likely"},
		{"shell verification", func(f *synthtest.Fixture) {
			f.Specialists[0].Tasks[0].Verification[0].Argv = []string{"sh", "-c", "go test"}
		}, "shell"},
		{"unresolved decision", func(f *synthtest.Fixture) {
			f.PM.Epics[0].Title = "Invoice export"
		}, ""},
	}
	for _, tc := range cases {
		if tc.want == "" {
			continue
		}
		f := synthtest.Sample()
		tc.mutate(&f)
		_, err := synthesizer.Compile(f.Input(), contracts.ApprovalMeta{})
		if err == nil {
			t.Errorf("%s: Compile succeeded, want an error", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %q does not mention %q", tc.name, err, tc.want)
		}
	}
	f := synthtest.Sample()
	in := f.Input()
	in.Decisions = []contracts.Decision{{ID: "D1", Question: "pick", Options: []contracts.DecisionOption{{ID: "a"}, {ID: "b"}}}}
	if _, err := synthesizer.Compile(in, contracts.ApprovalMeta{}); err == nil || !strings.Contains(err.Error(), "unresolved") {
		t.Fatalf("unresolved decision must block compilation, got %v", err)
	}
}

func TestPMMustNotReferenceTechnicalDetail(t *testing.T) {
	f := synthtest.Sample()
	f.PM.Stories[0].Need = "to call ListInvoices in internal/billing/invoice.go"
	_, err := synthesizer.Compile(f.Input(), contracts.ApprovalMeta{})
	if err == nil || !strings.Contains(err.Error(), "technical detail") {
		t.Fatalf("PM output with a path or symbol must be rejected, got %v", err)
	}
}

// Repositories name things with ordinary words: a helper called `article`, a
// callback called `load`. The Product Manager never reads the repository, so it
// cannot avoid those words, and matching them rejected drafts it had no way to
// write differently. Only a code-shaped name is evidence of a leak.
func TestOrdinaryWordsThatAreAlsoSymbolsAreNotTechnicalDetail(t *testing.T) {
	g := contracts.RepoContextGraph{Symbols: map[string][]contracts.Symbol{
		"src/articles.py": {
			{Name: "article", QualifiedName: "articles.article"},
			{Name: "load", QualifiedName: "articles.load"},
			{Name: "connected"},
			{Name: "Article"},
			{Name: "load_article_cache"},
			{Name: "ListInvoices"},
		},
	}}
	symbols := synthesizer.SymbolNames(g)
	for _, keep := range []string{"load_article_cache", "ListInvoices", "articles.article", "articles.load"} {
		if _, ok := symbols[keep]; !ok {
			t.Errorf("SymbolNames dropped the code-shaped name %q", keep)
		}
	}
	for _, drop := range []string{"article", "load", "connected", "Article"} {
		if _, ok := symbols[drop]; ok {
			t.Errorf("SymbolNames kept %q, a word product language needs", drop)
		}
	}
	prose := "A researcher opens an article, waits for the load, and stays connected while Article pages render."
	if ref := synthesizer.TechnicalReference(prose, symbols); ref != "" {
		t.Errorf("product prose was rejected over %q", ref)
	}
	for _, leak := range []string{
		"the system calls ListInvoices",
		"clear load_article_cache every night",
		"read articles.article for the body",
		"open internal/billing/invoice.go",
	} {
		if synthesizer.TechnicalReference(leak, symbols) == "" {
			t.Errorf("no technical reference found in %q", leak)
		}
	}
}

func TestCompareInterfacesDistinguishesBrokenFromConflict(t *testing.T) {
	f := synthtest.Sample()
	if issues := synthesizer.CompareInterfaces(f.Architect, f.Specialists); len(issues) != 0 {
		t.Fatalf("a consistent design has no issues, got %+v", issues)
	}
	// Both claims name the right version but disagree on the output shape:
	// two valid alternatives, which need a human decision.
	f.Specialists[1].Consumes[0].OutputSchema = "application/json"
	issues := synthesizer.CompareInterfaces(f.Architect, f.Specialists)
	if len(issues) != 1 || issues[0].Kind != synthesizer.IssueConflict || issues[0].ContractID != "export-api" {
		t.Fatalf("want one conflict on export-api, got %+v", issues)
	}
	// A wrong version hash is a broken draft that only needs repair.
	f = synthtest.Sample()
	f.Specialists[1].Consumes[0].VersionHash = "0000"
	issues = synthesizer.CompareInterfaces(f.Architect, f.Specialists)
	if len(issues) != 1 || issues[0].Kind != synthesizer.IssueBroken || issues[0].Workstreams[0] != "web-ui" {
		t.Fatalf("want one broken issue for web-ui, got %+v", issues)
	}
	// Order independence.
	f = synthtest.Sample()
	rev := []synthesizer.SpecialistDoc{f.Specialists[1], f.Specialists[0]}
	if fmt.Sprint(synthesizer.CompareInterfaces(f.Architect, f.Specialists)) != fmt.Sprint(synthesizer.CompareInterfaces(f.Architect, rev)) {
		t.Fatal("CompareInterfaces depends on specialist order")
	}
}

func chain(likely float64) []contracts.TechnicalTask {
	est := contracts.Estimate{Optimistic: likely, Likely: likely, Pessimistic: likely, Basis: "test basis"}
	agent := contracts.Estimate{Optimistic: 10, Likely: 10, Pessimistic: 10, Basis: "test basis"}
	return []contracts.TechnicalTask{
		{ID: "T1", WorkstreamID: "a", Title: "one", HumanEstimate: est, AgentEstimate: agent},
		{ID: "T2", WorkstreamID: "b", Title: "two", DependsOn: []string{"T1"}, HumanEstimate: est, AgentEstimate: agent},
		{ID: "T3", WorkstreamID: "c", Title: "three", DependsOn: []string{"T2"}, HumanEstimate: est, AgentEstimate: agent},
	}
}

func TestBuildScheduleThreeTaskChain(t *testing.T) {
	s, err := synthesizer.BuildSchedule(chain(13.5), 9)
	if err != nil {
		t.Fatal(err)
	}
	if s.Human.Days != 4.5 || s.Human.Effort != 40.5 {
		t.Fatalf("human = %v days / %v hrs, want 4.5 / 40.5", s.Human.Days, s.Human.Effort)
	}
	if got := s.HumanHeadline(); got != "Human: 4.5 days (40.5 hrs)" {
		t.Fatalf("headline = %q", got)
	}
	if s.Human.Makespan != 40.5 {
		t.Fatalf("one developer: makespan %v must equal effort 40.5", s.Human.Makespan)
	}
	// The chain forbids any overlap, so the agent makespan equals its effort.
	if s.Agent.Effort != 30 || s.Agent.Makespan != 30 {
		t.Fatalf("agent chain effort/makespan = %v/%v, want 30/30", s.Agent.Effort, s.Agent.Makespan)
	}
	joined := strings.Join(s.Assumptions, "\n")
	for _, want := range []string{"09:00-18:00 Monday-Friday", "no implied holidays", "model estimate, not measured", "No fixed human/agent speed ratio"} {
		if !strings.Contains(joined, want) {
			t.Errorf("assumptions lack %q", want)
		}
	}
}

func TestBuildSchedulePERT(t *testing.T) {
	task := contracts.TechnicalTask{
		ID: "T1", WorkstreamID: "a", Title: "x",
		HumanEstimate: contracts.Estimate{Optimistic: 2, Likely: 4, Pessimistic: 12, Basis: "b"},
		AgentEstimate: contracts.Estimate{Optimistic: 6, Likely: 6, Pessimistic: 6, Basis: "b"},
	}
	s, err := synthesizer.BuildSchedule([]contracts.TechnicalTask{task}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if s.HoursPerDay != 9 {
		t.Fatalf("hours per day default = %v, want 9", s.HoursPerDay)
	}
	if s.Human.Effort != 5.0 { // (2 + 16 + 12) / 6
		t.Fatalf("PERT expected = %v, want 5", s.Human.Effort)
	}
	if s.Human.EffortOptimistic != 2 || s.Human.EffortPessimistic != 12 {
		t.Fatalf("range = %v-%v", s.Human.EffortOptimistic, s.Human.EffortPessimistic)
	}
}

func TestBuildScheduleRejectsBadEstimates(t *testing.T) {
	bad := func(mut func(*contracts.TechnicalTask)) error {
		tasks := chain(1)
		mut(&tasks[0])
		_, err := synthesizer.BuildSchedule(tasks, 9)
		return err
	}
	cases := map[string]func(*contracts.TechnicalTask){
		"negative":     func(x *contracts.TechnicalTask) { x.HumanEstimate.Optimistic = -1 },
		"disordered":   func(x *contracts.TechnicalTask) { x.HumanEstimate.Likely = 99 },
		"no basis":     func(x *contracts.TechnicalTask) { x.AgentEstimate.Basis = " " },
		"agent badnan": func(x *contracts.TechnicalTask) { x.AgentEstimate.Pessimistic = nan() },
	}
	for name, mut := range cases {
		if err := bad(mut); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	tasks := chain(1)
	tasks[0].DependsOn = []string{"T3"}
	if _, err := synthesizer.BuildSchedule(tasks, 9); err == nil {
		t.Error("a dependency cycle must be rejected")
	}
	if _, err := synthesizer.BuildSchedule(chain(1), 25); err == nil {
		t.Error("hours per day above 24 must be rejected")
	}
}

func nan() float64 { var z float64; return z / z }

func TestAgentScheduleSerializesWorkstreamsAndParallelizesOthers(t *testing.T) {
	agent := contracts.Estimate{Optimistic: 10, Likely: 10, Pessimistic: 10, Basis: "b"}
	human := contracts.Estimate{Optimistic: 1, Likely: 1, Pessimistic: 1, Basis: "b"}
	mk := func(id, ws string) contracts.TechnicalTask {
		return contracts.TechnicalTask{ID: id, WorkstreamID: ws, Title: id, HumanEstimate: human, AgentEstimate: agent}
	}
	// Two tasks of one workstream and two of another, no dependencies.
	s, err := synthesizer.BuildScheduleWith([]contracts.TechnicalTask{mk("A1", "x"), mk("A2", "x"), mk("B1", "y"), mk("B2", "y")},
		synthesizer.ScheduleOptions{HoursPerDay: 9, MaxParallel: 4})
	if err != nil {
		t.Fatal(err)
	}
	if s.Agent.Effort != 40 {
		t.Fatalf("summed agent effort = %v, want 40", s.Agent.Effort)
	}
	if s.Agent.Makespan != 20 {
		t.Fatalf("agent makespan = %v, want 20 (two workstreams in parallel, each serial)", s.Agent.Makespan)
	}
	if s.Human.Makespan != 4 {
		t.Fatalf("one developer does tasks serially: makespan = %v, want 4", s.Human.Makespan)
	}
	byWS := map[string][][2]float64{}
	for _, task := range s.Agent.Tasks {
		for _, iv := range byWS[task.WorkstreamID] {
			if task.Start < iv[1] && iv[0] < task.End {
				t.Fatalf("workstream %s has overlapping tasks", task.WorkstreamID)
			}
		}
		byWS[task.WorkstreamID] = append(byWS[task.WorkstreamID], [2]float64{task.Start, task.End})
	}
	// max_parallel 1 serializes everything.
	s1, _ := synthesizer.BuildScheduleWith([]contracts.TechnicalTask{mk("A1", "x"), mk("B1", "y")}, synthesizer.ScheduleOptions{MaxParallel: 1})
	if s1.Agent.Makespan != 20 {
		t.Fatalf("max_parallel 1 makespan = %v, want 20", s1.Agent.Makespan)
	}
}

func TestHumanScheduleTopologicalTieBreak(t *testing.T) {
	est := contracts.Estimate{Optimistic: 1, Likely: 1, Pessimistic: 1, Basis: "b"}
	mk := func(id string, deps ...string) contracts.TechnicalTask {
		return contracts.TechnicalTask{ID: id, WorkstreamID: id, Title: id, DependsOn: deps, HumanEstimate: est, AgentEstimate: est}
	}
	s, err := synthesizer.BuildSchedule([]contracts.TechnicalTask{mk("C"), mk("B", "C"), mk("A", "C"), mk("D")}, 9)
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, task := range s.Human.Tasks {
		order = append(order, task.ID)
	}
	// Ready set {C,D} yields C; then {A,B,D} yields A, B, D.
	if got := strings.Join(order, ","); got != "C,A,B,D" {
		t.Fatalf("human order = %s, want C,A,B,D", got)
	}
}

func TestQuoteLabelRules(t *testing.T) {
	got := synthesizer.QuoteLabel("a & b < c > d \" e ' f # g % h ; i \\ j ` k | l\nm\tn")
	if !strings.HasPrefix(got, `"`) || !strings.HasSuffix(got, `"`) {
		t.Fatalf("label must be double-quoted: %s", got)
	}
	for _, raw := range []string{"&", "<", ">", "'", "%", "`", "|", "\\"} {
		inner := got[1 : len(got)-1]
		// Raw specials never survive except inside entities (# and ;).
		for _, ent := range []string{"#38;", "#60;", "#62;", "#39;", "#35;", "#37;", "#59;", "#92;", "#96;", "#124;", "#34;"} {
			inner = strings.ReplaceAll(inner, ent, "")
		}
		if strings.Contains(inner, raw) {
			t.Fatalf("raw %q survived in %s", raw, got)
		}
	}
	for _, ent := range []string{"#38;", "#60;", "#62;", "#34;", "#39;", "#35;", "#37;", "#59;", "#92;", "#96;", "#124;"} {
		if !strings.Contains(got, ent) {
			t.Errorf("label lacks entity %s: %s", ent, got)
		}
	}
	if strings.ContainsAny(got, "\n\t\r") {
		t.Fatalf("control characters survived: %q", got)
	}
	long := synthesizer.QuoteLabel(strings.Repeat("é", 500))
	if n := utf8.RuneCountInString(long); n != 122 {
		t.Fatalf("a long label must be cut to 120 runes plus quotes, got %d runes", n)
	}
	if synthesizer.QuoteLabel("\x1b\u202e\u200b") != `"unnamed"` {
		t.Fatal("a label of only control characters becomes unnamed")
	}
}

func TestGanttLabelRules(t *testing.T) {
	got := synthesizer.GanttLabel("Backend: add API; call x # y %z%\n(a/b)+c_d-e.f")
	want := "Backend add API call x y z (a/b)+c_d-e.f"
	if got != want {
		t.Fatalf("GanttLabel = %q, want %q", got, want)
	}
	if n := utf8.RuneCountInString(synthesizer.GanttLabel(strings.Repeat("a", 200))); n != 60 {
		t.Fatalf("GanttLabel length = %d, want 60", n)
	}
	if synthesizer.GanttLabel("::;;##") != "task" {
		t.Fatal("an all-special label becomes the placeholder")
	}
}

// denialCorpus covers the statements that the frontend gate denies.
var denialCorpus = []string{"Backend: add API; call x # y", "title x", "click a href", "STYLE a fill:red", "link a", "href x", "call x", "classDef a", "linkStyle 0", "links x", "%%{init}", "x\nclick y", "a;style b"}

func TestCheckMermaidDeniesEveryDenylistedStatement(t *testing.T) {
	tokens, err := contracts.MermaidDenylistTokens()
	if err != nil || len(tokens) == 0 {
		t.Fatalf("denylist: %v %v", tokens, err)
	}
	for _, tok := range tokens {
		for _, src := range []string{
			"graph TD\n" + tok + " a b",
			"graph TD\n    " + strings.ToUpper(tok) + " a",
			"graph TD\n a --> b; " + tok + " x",
		} {
			if err := synthesizer.CheckMermaid(src); err == nil {
				t.Errorf("CheckMermaid accepted %q", src)
			}
		}
	}
	for _, src := range []string{"---\ntitle: x\n---\ngraph TD", "graph TD\n%% comment", "graph TD\n themeCSS", "graph TD\n a --> `b`", "graph TD\n a<b"} {
		if err := synthesizer.CheckMermaid(src); err == nil {
			t.Errorf("CheckMermaid accepted %q", src)
		}
	}
	if err := synthesizer.CheckMermaid("graph TD\n    n0[\"a\"]\n    n0 --> n1\n"); err != nil {
		t.Errorf("a clean graph was rejected: %v", err)
	}
}

func renderCorpusDiagrams(t testing.TB, labels []string) (string, string, string) {
	t.Helper()
	var g synthesizer.GraphDiagram
	var s synthesizer.SequenceDiagram
	for i, l := range labels {
		id := fmt.Sprintf("x%d", i)
		g.Nodes = append(g.Nodes, synthesizer.DiagramNode{ID: id, Label: l})
		s.Participants = append(s.Participants, synthesizer.DiagramNode{ID: id, Label: l})
		if i > 0 {
			prev := fmt.Sprintf("x%d", i-1)
			g.Edges = append(g.Edges, synthesizer.DiagramEdge{From: prev, To: id, Label: l})
			kind := synthesizer.MessageCall
			if i%2 == 0 {
				kind = synthesizer.MessageReturn
			}
			s.Messages = append(s.Messages, synthesizer.SequenceMessage{From: prev, To: id, Label: l, Kind: kind})
		}
	}
	gs, err := synthesizer.RenderGraph(g)
	if err != nil {
		t.Fatalf("RenderGraph: %v", err)
	}
	ss, err := synthesizer.RenderSequence(s)
	if err != nil {
		t.Fatalf("RenderSequence: %v", err)
	}
	tasks := make([]contracts.TechnicalTask, 0, len(labels))
	est := contracts.Estimate{Optimistic: 1, Likely: 2, Pessimistic: 3, Basis: "b"}
	for i, l := range labels {
		tasks = append(tasks, contracts.TechnicalTask{ID: fmt.Sprintf("T%02d", i), WorkstreamID: l, Title: l, HumanEstimate: est, AgentEstimate: est})
	}
	sched, err := synthesizer.BuildSchedule(tasks, 9)
	if err != nil {
		t.Fatalf("BuildSchedule: %v", err)
	}
	hg, err := synthesizer.RenderGantt(synthesizer.GanttHuman, sched.Human, sched)
	if err != nil {
		t.Fatalf("RenderGantt human: %v", err)
	}
	ag, err := synthesizer.RenderGantt(synthesizer.GanttAgent, sched.Agent, sched)
	if err != nil {
		t.Fatalf("RenderGantt agent: %v", err)
	}
	return gs + "\n" + ss, hg, ag
}

// frontendRejects mirrors web/src/protocol/schema.ts mermaidSourceRejected: the
// source is split on every newline and ';' and each statement's first
// whitespace-delimited token is compared with the denylist.
func frontendRejects(t testing.TB, src string) string {
	t.Helper()
	tokens, _ := contracts.MermaidDenylistTokens()
	if strings.HasPrefix(strings.TrimLeft(strings.TrimPrefix(src, "\ufeff"), " \t\r\n\f\v"), "---") {
		return "frontmatter"
	}
	if strings.Contains(src, "%%") {
		return "directive"
	}
	if strings.Contains(src, "themeCSS") {
		return "themeCSS"
	}
	for _, stmt := range strings.FieldsFunc(src, func(r rune) bool { return r == '\n' || r == ';' }) {
		fields := strings.Fields(stmt)
		if len(fields) == 0 {
			continue
		}
		first := strings.ToLower(fields[0])
		for _, tok := range tokens {
			if first == strings.ToLower(tok) {
				return "denied keyword " + first
			}
		}
	}
	return ""
}

func TestGeneratedMermaidSurvivesTheDenialCorpus(t *testing.T) {
	flow, human, agent := renderCorpusDiagrams(t, denialCorpus)
	for name, src := range map[string]string{"flow+sequence": flow, "human": human, "agent": agent} {
		if reason := frontendRejects(t, src); reason != "" {
			t.Errorf("%s: frontend gate would reject generated output (%s):\n%s", name, reason, src)
		}
		if err := synthesizer.CheckMermaid(src); err != nil {
			t.Errorf("%s: %v", name, err)
		}
		for _, bad := range []string{"`", "%%", "themeCSS", "<"} {
			if strings.Contains(src, bad) {
				t.Errorf("%s contains %q", name, bad)
			}
		}
	}
}

func FuzzMermaidRendering(f *testing.F) {
	for _, s := range denialCorpus {
		f.Add(s, s)
	}
	f.Add("a;click b", "x#59;style y")
	f.Add("\u202e\x1b[0m call", "click\u00a0x")
	f.Add("a\nlink b", "\ufeffhref")
	f.Fuzz(func(t *testing.T, a, b string) {
		if !utf8.ValidString(a) || !utf8.ValidString(b) {
			return
		}
		flow, human, agent := renderCorpusDiagrams(t, []string{a, b, a + ";" + b})
		for _, src := range []string{flow, human, agent} {
			if reason := frontendRejects(t, src); reason != "" {
				t.Fatalf("rejected token in generated output (%s) for %q %q:\n%s", reason, a, b, src)
			}
			if err := synthesizer.CheckMermaid(src); err != nil {
				t.Fatalf("%v for %q %q:\n%s", err, a, b, src)
			}
		}
	})
}

func TestMermaidFixturesMatchGeneratedSamples(t *testing.T) {
	plan := compile(t, synthtest.Sample().Input(), contracts.ApprovalMeta{})
	samples := map[string]string{
		"architecture.mmd": plan.Diagrams.Architecture,
		"sequence.mmd":     plan.Diagrams.Sequence,
		"gantt.mmd":        plan.Diagrams.HumanGantt,
		"gantt-agent.mmd":  plan.Diagrams.AgentGantt,
	}
	dir := filepath.Join("..", "..", "tests", "fixtures", "mermaid")
	update := os.Getenv("PLAN_FORGE_UPDATE_FIXTURES") == "1"
	if update {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, src := range samples {
		path := filepath.Join(dir, name)
		if update {
			if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		have, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v (regenerate with PLAN_FORGE_UPDATE_FIXTURES=1)", name, err)
		}
		if string(have) != src {
			t.Errorf("%s is out of date; regenerate with PLAN_FORGE_UPDATE_FIXTURES=1 go test ./internal/synthesizer/", name)
		}
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "*.mmd"))
	if len(matches) < 3 {
		t.Fatalf("need at least three .mmd fixtures, have %d", len(matches))
	}
}

func TestDraftContentMatchesSchema(t *testing.T) {
	plan := compile(t, synthtest.Sample().Input(), contracts.ApprovalMeta{})
	raw, err := plan.DraftContent()
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		Spec     string `json:"spec_markdown"`
		Manifest string `json:"manifest_json"`
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	if c.Spec != string(plan.SpecMarkdown) || c.Manifest != string(plan.ManifestJSON) {
		t.Fatal("draft content differs from the compiled artifacts")
	}
}

func TestSpecEscapesUntrustedMarkdown(t *testing.T) {
	f := synthtest.Sample()
	f.Architect.Summary = "<script>alert(1)</script> `tick` **bold** [x](http://evil) \x1b]0;t\x07"
	plan := compile(t, f.Input(), contracts.ApprovalMeta{})
	spec := string(plan.SpecMarkdown)
	for _, bad := range []string{"<script>", "[x](http", "\x1b", "\x07"} {
		if strings.Contains(spec, bad) {
			t.Fatalf("spec contains unescaped %q", bad)
		}
	}
}

// TestWriteCorpusDiagrams writes the denial-corpus diagrams to
// $PLAN_FORGE_CORPUS_DIR so a developer can run mermaid.parse over them. It is
// skipped unless the variable is set.
func TestWriteCorpusDiagrams(t *testing.T) {
	dir := os.Getenv("PLAN_FORGE_CORPUS_DIR")
	if dir == "" {
		t.Skip("PLAN_FORGE_CORPUS_DIR not set")
	}
	flow, human, agent := renderCorpusDiagrams(t, denialCorpus)
	parts := strings.SplitN(flow, "\nsequenceDiagram\n", 2)
	files := map[string]string{
		"corpus-graph.mmd":    parts[0],
		"corpus-sequence.mmd": "sequenceDiagram\n" + parts[1],
		"corpus-human.mmd":    human,
		"corpus-agent.mmd":    agent,
	}
	for name, src := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// A Domain Specialist drafts exactly one workstream, and the payload names it.
// Models drop the per-task repetition of that name; the stage used to fail the
// whole draft for it. The task inherits the draft's workstream instead.
func TestSpecialistTasksInheritTheDraftWorkstream(t *testing.T) {
	f := synthtest.Sample()
	for i := range f.Specialists {
		for j := range f.Specialists[i].Tasks {
			f.Specialists[i].Tasks[j].WorkstreamID = ""
		}
	}
	raw, err := json.Marshal(f.Specialists[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	d, err := synthesizer.DecodeSpecialist(raw)
	if err != nil {
		t.Fatalf("DecodeSpecialist: %v", err)
	}
	for _, task := range d.Tasks {
		if task.WorkstreamID != d.WorkstreamID {
			t.Fatalf("task %s workstream is %q, want %q", task.ID, task.WorkstreamID, d.WorkstreamID)
		}
	}
	ctx := synthesizer.ContextOf(f.PM, f.Graph, f.DocDrift)
	if err := d.Validate(f.Architect, ctx); err != nil {
		t.Fatalf("Validate after inheriting the workstream: %v", err)
	}
	if _, err := synthesizer.Compile(f.Input(), contracts.ApprovalMeta{}); err != nil {
		t.Fatalf("Compile: %v", err)
	}
}

// A task that names a different workstream is a disagreement, not an omission.
func TestSpecialistTaskOfAnotherWorkstreamIsStillRejected(t *testing.T) {
	f := synthtest.Sample()
	f.Specialists[0].Tasks[0].WorkstreamID = "web-ui"
	_, err := synthesizer.Compile(f.Input(), contracts.ApprovalMeta{})
	if err == nil || !strings.Contains(err.Error(), "belongs to workstream") {
		t.Fatalf("Compile error %v, want one naming the wrong workstream", err)
	}
}
