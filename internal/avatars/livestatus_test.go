//go:build client && !server

package avatars_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/avatars"
	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/relay"
	"github.com/danielckv/agentic-plan-forge/internal/synthesizer/synthtest"
)

// The room shows which host and model serve each avatar. The placement
// arrives before any output, so every thought after the first names it.
func TestEveryAvatarReportsItsHostAndModel(t *testing.T) {
	h := newHarness(t, synthtest.Sample())
	if _, err := h.run(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	for avatar, wantHost := range map[string]string{
		relay.StagePM:        "claude",
		relay.StageArchitect: "claude",
		relay.StageCritic:    "agy",
	} {
		named := 0
		for _, p := range h.sink.thoughts(avatar) {
			if p.Host == "" {
				continue
			}
			named++
			if p.Host != wantHost {
				t.Errorf("%s ran on %q, want %q", avatar, p.Host, wantHost)
			}
			if p.Model == "" {
				t.Errorf("%s named a host but no model", avatar)
			}
		}
		if named == 0 {
			t.Errorf("no thought of %s named a host", avatar)
		}
	}
	spec := h.sink.thoughts(relay.StageSpecialistPrefix + "billing-api")
	if len(spec) == 0 {
		t.Fatal("the specialist emitted no thought")
	}
	if spec[len(spec)-1].Host != "omp" {
		t.Errorf("specialist ran on %q, want omp: the roles must not all land on one host",
			spec[len(spec)-1].Host)
	}
}

// Each avatar says which part of its stage is running. The architect answers
// in three parts, and a reader must be able to tell which one is in flight.
func TestEveryAvatarReportsTheStepItIsOn(t *testing.T) {
	h := newHarness(t, synthtest.Sample())
	if _, err := h.run(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	steps := map[string]bool{}
	for _, p := range h.sink.thoughts(relay.StageArchitect) {
		if p.Step != "" {
			steps[p.Step] = true
		}
	}
	for _, want := range []string{"design 1/3", "diagrams 2/3"} {
		if !steps[want] {
			t.Errorf("the architect never reported step %q; reported %v", want, keysOf(steps))
		}
	}
	docs := false
	for step := range steps {
		if strings.HasPrefix(step, "docs 3/3 · batch ") {
			docs = true
		}
	}
	if !docs {
		t.Errorf("the architect never reported a documentation batch; reported %v", keysOf(steps))
	}
	if got := lastStep(h, relay.StagePM); got != "epics and stories" {
		t.Errorf("pm step = %q", got)
	}
	if got := lastStep(h, relay.StageCritic); got != "barrier review" && got != "early review" {
		t.Errorf("critic step = %q", got)
	}
}

// A card must say what its call can see. Without it a reader cannot tell
// evidence that was cut to fit from evidence that never existed.
func TestEveryAvatarReportsTheEvidenceItCanSee(t *testing.T) {
	h := newHarness(t, synthtest.Sample())
	if _, err := h.run(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	var notes []string
	for _, p := range h.sink.thoughts(relay.StageArchitect) {
		if p.ContextNote != "" {
			notes = append(notes, p.ContextNote)
		}
	}
	if len(notes) == 0 {
		t.Fatal("no architect thought stated the evidence it can see")
	}
	// The design part is the one that reads the repository graph; the
	// documentation batches read drift items and say so instead.
	design := notes[0]
	for _, want := range []string{"graph ", " files", "KiB"} {
		if !strings.Contains(design, want) {
			t.Errorf("design evidence note %q does not mention %q", design, want)
		}
	}
	drift := false
	for _, note := range notes {
		if strings.Contains(note, "drift ") && strings.Contains(note, " items") {
			drift = true
		}
	}
	if !drift {
		t.Errorf("no documentation batch stated its drift items; notes = %v", notes)
	}
	for _, note := range notes {
		if !strings.Contains(note, "KiB") {
			t.Errorf("evidence note %q does not state its size", note)
		}
	}
}

// A stage call now has minutes to run. Without a heartbeat the card holds one
// sentence for the whole stage and a slow run is indistinguishable from a
// hung one, which is how the architect timeout went unexplained.
func TestALongCallReportsItsProgressWhileItRuns(t *testing.T) {
	h := newHarness(t, synthtest.Sample())
	h.in.HeartbeatEvery = 10 * time.Millisecond
	h.stub.delay = 120 * time.Millisecond
	if _, err := h.run(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	beats := 0
	for _, p := range h.sink.thoughts(relay.StagePM) {
		if strings.HasPrefix(p.PublicSummary, "Running for ") {
			beats++
			if p.ElapsedMS <= 0 {
				t.Errorf("heartbeat %q reported no elapsed time", p.PublicSummary)
			}
		}
	}
	if beats < 2 {
		t.Fatalf("heartbeats during a %v call = %d, want at least 2", h.stub.delay, beats)
	}
}

// A heartbeat must never be mistaken for the end of a stage: a run that
// finishes still ends every avatar on a terminal status.
func TestHeartbeatsDoNotDisturbTheFinalStatus(t *testing.T) {
	h := newHarness(t, synthtest.Sample())
	h.in.HeartbeatEvery = 10 * time.Millisecond
	h.stub.delay = 60 * time.Millisecond
	if _, err := h.run(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	for _, avatar := range []string{relay.StagePM, relay.StageArchitect, relay.StageCritic} {
		got := h.sink.thoughts(avatar)
		if len(got) == 0 {
			t.Fatalf("%s emitted no thought", avatar)
		}
		last := got[len(got)-1].Status
		if last != "Synced" && last != "Critiquing" {
			t.Errorf("%s ended on %q, want a settled status", avatar, last)
		}
	}
}

// The narrator reads the other avatars' steps and publishes one live line. It
// is not an avatar of the cycle: it must never be counted as still working,
// or the room would never let a finished cycle synthesize.
func TestTheNarratorPublishesALiveStatusAndGatesNothing(t *testing.T) {
	h := newHarness(t, synthtest.Sample())
	h.in.NarrateEvery = 10 * time.Millisecond
	h.in.HeartbeatEvery = 10 * time.Millisecond
	h.stub.delay = 40 * time.Millisecond
	h.stub.on("narrator", func(c *call, in input) (reply, bool) {
		return reply{Content: raw(map[string]string{"status": "The architect is mapping the export onto internal/billing."})}, true
	})
	if _, err := h.run(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	said := h.sink.thoughts(relay.StageNarrator)
	if len(said) == 0 {
		t.Fatal("the narrator published nothing")
	}
	for _, p := range said {
		if p.Status != "Narrating" {
			t.Errorf("narrator status = %q, want Narrating: any working status would gate synthesis", p.Status)
		}
		if p.PublicSummary == "" {
			t.Error("the narrator published an empty line")
		}
	}
	// It reads steps, never drafts: the payload it was given carries steps
	// and nothing else.
	calls := h.stub.callsOf("narrator")
	if len(calls) == 0 {
		t.Fatal("the narrator made no call")
	}
	body := string(calls[0].Req.UserPayload)
	if !strings.Contains(body, `"steps"`) {
		t.Errorf("narrator payload has no steps: %s", truncate(body))
	}
	for _, forbidden := range []string{`"workstreams"`, `"contracts"`, `"repository_graph"`} {
		if strings.Contains(body, forbidden) {
			t.Errorf("narrator payload carries %s; it must read steps, not drafts", forbidden)
		}
	}
}

// A narrator that fails is a lost status line, never a lost run.
func TestANarratorFailureNeverFailsTheRun(t *testing.T) {
	h := newHarness(t, synthtest.Sample())
	h.in.NarrateEvery = 10 * time.Millisecond
	h.stub.delay = 40 * time.Millisecond
	h.stub.on("narrator", func(c *call, in input) (reply, bool) {
		return reply{Err: context.DeadlineExceeded}, true
	})
	if _, err := h.run(context.Background(), nil); err != nil {
		t.Fatalf("a failing narrator ended the run: %v", err)
	}
	if got := h.sink.thoughts(relay.StageNarrator); len(got) != 0 {
		t.Errorf("a failed narration was published anyway: %+v", got)
	}
}

// Turning the narrator off costs nothing else.
func TestTheNarratorCanBeTurnedOff(t *testing.T) {
	h := newHarness(t, synthtest.Sample())
	h.in.NoNarrator = true
	h.in.NarrateEvery = 10 * time.Millisecond
	h.stub.delay = 40 * time.Millisecond
	if _, err := h.run(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if got := h.stub.count("narrator"); got != 0 {
		t.Errorf("narrator calls with the narrator off = %d, want 0", got)
	}
	if got := h.sink.thoughts(relay.StageNarrator); len(got) != 0 {
		t.Errorf("the narrator published %d lines while off", len(got))
	}
}

func lastStep(h *harness, avatar string) string {
	step := ""
	for _, p := range h.sink.thoughts(avatar) {
		if p.Step != "" {
			step = p.Step
		}
	}
	return step
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func truncate(s string) string {
	if len(s) <= 200 {
		return s
	}
	return s[:200] + "…"
}

var _ = contracts.RoleNarrator
var _ = avatars.DefaultStageTimeout
