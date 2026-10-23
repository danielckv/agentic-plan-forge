//go:build client && !server

package avatars_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/avatars"
	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/synthesizer"
	"github.com/danielckv/agentic-plan-forge/internal/synthesizer/synthtest"
)

// driftItems returns n confirmed drift items, each in its own document.
func driftItems(n int) []contracts.DocDriftItem {
	out := make([]contracts.DocDriftItem, 0, n)
	for i := range n {
		id := "DOC-" + strconv.Itoa(i+1)
		out = append(out, contracts.DocDriftItem{
			ID: id, DocFilePath: fmt.Sprintf("docs/page%d.md", i+1), DocLine: i + 1,
			CodeFilePath: "internal/billing/invoice.go", SymbolName: "ListInvoices",
			DriftType: contracts.DriftStaleSignature, Expected: "ListInvoices(org)",
			Observed: "ListInvoices(ctx, org)", EvidenceKind: "signature",
			Confidence: contracts.ConfidenceHigh, RemediationTask: "Update " + id,
		})
	}
	return out
}

// batchOf reads the batch number out of a documentation-task payload.
func batchOf(t *testing.T, in input) int {
	t.Helper()
	var n int
	if err := json.Unmarshal(in.Fields["batch"], &n); err != nil {
		t.Fatalf("documentation payload has no batch number: %v", err)
	}
	return n
}

// payloadHas reports whether a request payload carries a top-level field.
func payloadHas(t *testing.T, raw json.RawMessage, field string) bool {
	t.Helper()
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatalf("payload is not an object: %v", err)
	}
	_, ok := top[field]
	return ok
}

// One architect call had to read the graph, the drift and the context and then
// write the DAG, the contracts, two diagrams and a task per drift item; on a
// repository with real drift it outran the call timeout. The stage now answers
// in parts, and each part reads only what it answers.
func TestTheArchitectAnswersInParts(t *testing.T) {
	h := newHarness(t, synthtest.Sample())
	if _, err := h.run(context.Background(), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}

	for key, want := range map[string]int{"architect": 1, keyArchDiagrams: 1, keyArchDocs: 1} {
		if got := h.stub.count(key); got != want {
			t.Errorf("%s calls = %d, want %d", key, got, want)
		}
	}
	design := h.stub.callsOf("architect")[0].Req
	if payloadHas(t, design.UserPayload, "documentation_drift") {
		t.Error("the design part carries the documentation drift it does not answer")
	}
	docs := h.stub.callsOf(keyArchDocs)[0].Req
	if payloadHas(t, docs.UserPayload, "repository_graph") {
		t.Error("the documentation part carries the repository graph it does not need")
	}
	if !payloadHas(t, docs.UserPayload, "id_prefix") {
		t.Error("the documentation part was not told which id prefix keeps its tasks unique")
	}
	diagrams := h.stub.callsOf(keyArchDiagrams)[0].Req
	if payloadHas(t, diagrams.UserPayload, "repository_graph") {
		t.Error("the diagram part carries the repository graph; it draws the decided design")
	}
}

// Every confirmed drift item still needs its own documentation task, so the
// work is split into batches that each fit one call. The assembled draft covers
// every item exactly once.
func TestDocumentationTasksAreBatched(t *testing.T) {
	fx := synthtest.Sample()
	fx.DocDrift = driftItems(45)
	h := newHarness(t, fx)
	h.in.DocDrift = fx.DocDrift

	si, err := h.run(context.Background(), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	batches := h.stub.callsOf(keyArchDocs)
	if len(batches) != 3 {
		t.Fatalf("documentation calls = %d, want 3 batches of at most 20 items", len(batches))
	}
	for i, c := range batches {
		var p struct {
			Items []contracts.DocDriftItem `json:"documentation_drift"`
		}
		if err := json.Unmarshal(c.Req.UserPayload, &p); err != nil {
			t.Fatal(err)
		}
		if len(p.Items) == 0 || len(p.Items) > 20 {
			t.Errorf("batch %d carries %d items, want 1..20", i+1, len(p.Items))
		}
	}

	var arch synthesizer.ArchitectDoc
	if err := json.Unmarshal(si.Architecture, &arch); err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	ids := map[string]bool{}
	for _, task := range arch.DocTasks {
		if ids[task.ID] {
			t.Errorf("duplicate documentation task id %q across batches", task.ID)
		}
		ids[task.ID] = true
		for _, d := range task.DocDriftIDs {
			seen[d]++
		}
	}
	for _, item := range fx.DocDrift {
		if seen[item.ID] != 1 {
			t.Errorf("drift item %s is covered by %d tasks, want exactly 1", item.ID, seen[item.ID])
		}
	}
}

// An item that could not be verified against the repository graph needs no
// task, so it is not batched; the parts are told how many there were.
func TestUnverifiableDriftIsNotBatched(t *testing.T) {
	fx := synthtest.Sample()
	fx.DocDrift = append(driftItems(2), contracts.DocDriftItem{
		ID: "DOC-U", DocFilePath: "docs/unknown.md", DocLine: 1,
		DriftType: contracts.DriftUnverifiable, EvidenceKind: "prose", Confidence: contracts.ConfidenceLow,
		RemediationTask: "read docs/unknown.md",
	})
	h := newHarness(t, fx)
	h.in.DocDrift = fx.DocDrift

	if _, err := h.run(context.Background(), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	calls := h.stub.callsOf(keyArchDocs)
	if len(calls) != 1 {
		t.Fatalf("documentation calls = %d, want 1", len(calls))
	}
	var p struct {
		Items        []contracts.DocDriftItem `json:"documentation_drift"`
		Unverifiable int                      `json:"unverifiable_drift_items"`
	}
	if err := json.Unmarshal(calls[0].Req.UserPayload, &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Items) != 2 {
		t.Errorf("batched items = %d, want the 2 confirmed ones", len(p.Items))
	}
	if p.Unverifiable != 1 {
		t.Errorf("unverifiable_drift_items = %d, want 1", p.Unverifiable)
	}
}

// Each part is validated on its own, and the assembled draft is validated as a
// whole: two batches that each pass alone can still contradict each other, and
// the stage must say so and start over rather than publish the contradiction or
// retry a part that cannot fix it.
func TestPartsThatContradictEachOtherFailTheStageAndAreDropped(t *testing.T) {
	fx := synthtest.Sample()
	fx.DocDrift = driftItems(25)
	h := newHarness(t, fx)
	h.in.DocDrift = fx.DocDrift
	// Batch 1 waits for a task of batch 2 and batch 2 for a task of batch 1.
	// Neither batch holds a cycle; the assembled draft does.
	h.stub.on(keyArchDocs, func(c *call, in input) (reply, bool) {
		tasks := h.stub.docTasks(in)
		if len(tasks) == 0 {
			return reply{}, false
		}
		switch batchOf(t, in) {
		case 1:
			tasks[0].DependsOn = []string{"T-DOC-2-DOC-21"}
		case 2:
			tasks[0].DependsOn = []string{"T-DOC-1-DOC-1"}
		default:
			return reply{}, false
		}
		return reply{Content: raw(synthesizer.ArchitectDoc{DocTasks: tasks})}, true
	})

	_, err := h.run(context.Background(), nil)
	var stage *avatars.StageError
	if !errors.As(err, &stage) || stage.Stage != "architect" {
		t.Fatalf("Run error = %v, want an architect StageError", err)
	}
	if !strings.Contains(err.Error(), "do not fit together") {
		t.Errorf("error = %v, want it to name the disagreement between the parts", err)
	}

	// The retry asks for every part again: keeping them would re-assemble the
	// same draft and fail the same way without calling a model at all.
	design := h.stub.count("architect")
	_, _ = h.run(context.Background(), nil)
	if got := h.stub.count("architect"); got <= design {
		t.Errorf("design calls = %d, want the retry to ask for the design again", got)
	}
}

// A part that failed costs its own call and no other: the retry re-runs it and
// adopts the parts that already finished.
func TestARetryReRunsOnlyTheFailedPart(t *testing.T) {
	fx := synthtest.Sample()
	fx.DocDrift = driftItems(25)
	h := newHarness(t, fx)
	h.in.DocDrift = fx.DocDrift
	// Batch 2 refuses twice: the call and the one transient retry generate is
	// allowed. The batches run concurrently, so the failure is keyed on the
	// batch number in the payload, never on the order the calls arrive in.
	var mu sync.Mutex
	tries := 0
	h.stub.on(keyArchDocs, func(c *call, in input) (reply, bool) {
		if batchOf(t, in) != 2 {
			return reply{}, false
		}
		mu.Lock()
		tries++
		failing := tries <= 2
		mu.Unlock()
		if failing {
			return reply{Err: errors.New("the provider refused")}, true
		}
		return reply{}, false
	})

	_, err := h.run(context.Background(), nil)
	var stage *avatars.StageError
	if !errors.As(err, &stage) || stage.Stage != "architect" {
		t.Fatalf("Run error = %v, want an architect StageError", err)
	}
	firstDesign, firstDiagrams := h.stub.count("architect"), h.stub.count(keyArchDiagrams)

	// The retry is the same run id with the same checkpoint store.
	if _, err := h.run(context.Background(), nil); err != nil {
		t.Fatalf("the retry failed: %v", err)
	}
	if got := h.stub.count("architect"); got != firstDesign {
		t.Errorf("design calls = %d, want the checkpointed design kept at %d", got, firstDesign)
	}
	if got := h.stub.count(keyArchDiagrams); got != firstDiagrams {
		t.Errorf("diagram calls = %d, want the checkpointed diagrams kept at %d", got, firstDiagrams)
	}
	var batch1, batch2 int
	for _, c := range h.stub.callsOf(keyArchDocs) {
		switch batchOf(t, decodeInput(c.Req)) {
		case 1:
			batch1++
		case 2:
			batch2++
		}
	}
	if batch1 != 1 {
		t.Errorf("batch 1 ran %d times, want once: it had finished", batch1)
	}
	if batch2 < 2 {
		t.Errorf("batch 2 ran %d times, want the failure and the retry", batch2)
	}
}

// ADR 0004's rule, applied to a stage: a call that outran the clock was too
// large, so it runs again with less evidence rather than being repeated.
func TestATimedOutStageRetriesWithLessEvidence(t *testing.T) {
	fx := synthtest.Sample()
	h := newHarness(t, fx)
	hits := make([]contracts.ContextHit, 0, 64)
	for i := range 64 {
		hits = append(hits, contracts.ContextHit{
			Path: fmt.Sprintf("docs/note%d.md", i), Text: strings.Repeat("c", 2000), Score: float64(64 - i),
		})
	}
	h.in.Context = contracts.ProjectContext{Source: "files", Branch: "main", Hits: hits}
	h.in.CallTimeout = 50 * time.Millisecond
	h.stub.on("architect", func(c *call, in input) (reply, bool) {
		if c.Nth == 1 {
			return reply{Err: context.DeadlineExceeded}, true
		}
		return reply{}, false
	})

	if _, err := h.run(context.Background(), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	calls := h.stub.callsOf("architect")
	if len(calls) != 2 {
		t.Fatalf("design calls = %d, want the timed-out call and one smaller retry", len(calls))
	}
	first, second := len(calls[0].Req.UserPayload), len(calls[1].Req.UserPayload)
	if second > first/2 {
		t.Errorf("the retry payload is %d bytes against %d: it must be at most half", second, first)
	}
	var p struct {
		Notes []string `json:"evidence_notes"`
	}
	if err := json.Unmarshal(calls[1].Req.UserPayload, &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Notes) == 0 {
		t.Error("the smaller payload cut evidence without saying so")
	}
}
