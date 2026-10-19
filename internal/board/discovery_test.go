//go:build client && !server

package board

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// ageActivity rewrites the stored activity time of taskID, bypassing the
// refresh window.
func ageActivity(t *testing.T, s *Store, taskID string, by time.Duration) {
	t.Helper()
	if _, err := s.update(func(o *Overlay) (bool, error) {
		a := o.Activity[taskID]
		a.At = time.Now().UTC().Add(-by).Format(time.RFC3339)
		o.Activity[taskID] = a
		return true, nil
	}); err != nil {
		t.Fatalf("ageActivity: %v", err)
	}
}

// seedDiscoveries stores n proposed task discoveries D-1..D-n in one write, the
// oldest first, and returns the document.
func seedDiscoveries(t *testing.T, s *Store, n int) Overlay {
	t.Helper()
	base := time.Now().UTC().Add(-24 * time.Hour)
	ov, err := s.update(func(o *Overlay) (bool, error) {
		for i := 1; i <= n; i++ {
			o.Discoveries = append(o.Discoveries, Discovery{
				ID: fmt.Sprintf("D-%d", i), Kind: DiscoveryTask, Title: fmt.Sprintf("found %d", i),
				Paths: []string{}, By: "omp:seed", At: base.Add(time.Duration(i) * time.Second).Format(time.RFC3339),
				Status: DiscoveryProposed, Priority: PriorityNormal,
			})
		}
		o.DiscoverySeq = uint64(n)
		return true, nil
	})
	if err != nil {
		t.Fatalf("seedDiscoveries: %v", err)
	}
	return ov
}

func setStatus(t *testing.T, s *Store, id string, st DiscoveryStatus) {
	t.Helper()
	if _, err := s.update(func(o *Overlay) (bool, error) {
		i := o.discoveryIndex(id)
		if i < 0 {
			return false, ErrUnknownDiscovery
		}
		o.Discoveries[i].Status = st
		return true, nil
	}); err != nil {
		t.Fatalf("setStatus %s: %v", id, err)
	}
}

func TestV1FileUpgradesInMemoryAndPersistsV2(t *testing.T) {
	s := newStore(t)
	v1 := `{"version":1,"manifest_sha256":"` + testSHA + `","revision":2,
"tasks":{"T-1":{"priority":"high","notes":[{"id":"n-1","at":"2026-01-01T00:00:00Z","text":"keep"}]}},
"events":[{"seq":1,"at":"2026-01-01T00:00:00Z","task_id":"T-1","kind":"priority","value":"high"},
{"seq":2,"at":"2026-01-01T00:00:01Z","task_id":"T-1","kind":"context","value":"keep"}],
"cursors":{"claude:abc":{"seq":2,"seen_at":"2026-01-01T00:00:02Z"}}}`
	if err := os.WriteFile(s.Path(), []byte(v1), 0o600); err != nil {
		t.Fatal(err)
	}
	o, err := s.Load()
	if err != nil {
		t.Fatalf("Load v1: %v", err)
	}
	if o.Version != 2 || o.Revision != 2 || o.Priority("T-1") != PriorityHigh || len(o.Tasks["T-1"].Notes) != 1 {
		t.Fatalf("upgraded overlay = %+v", o)
	}
	if o.Activity == nil || o.Discoveries == nil || len(o.Activity) != 0 || len(o.Discoveries) != 0 {
		t.Fatalf("v2 collections missing: %+v", o)
	}
	if o.Cursors["claude:abc"].Seq != 2 || o.Cursors["claude:abc"].LastNextUp != "" {
		t.Fatalf("cursor = %+v", o.Cursors)
	}
	// Load never rewrites the file.
	raw, _ := os.ReadFile(s.Path())
	if string(raw) != v1 {
		t.Fatalf("Load rewrote the v1 file: %s", raw)
	}
	// The next write persists v2 and keeps the old data.
	if _, err := s.RecordActivity("omp:x", "T-1", ActivityActive, SourceReport, "go"); err != nil {
		t.Fatalf("RecordActivity: %v", err)
	}
	raw, _ = os.ReadFile(s.Path())
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["version"] != float64(2) {
		t.Fatalf("version = %v in %s", doc["version"], raw)
	}
	for _, k := range []string{"activity", "discoveries"} {
		if _, ok := doc[k]; !ok {
			t.Fatalf("missing %q in %s", k, raw)
		}
	}
	o, err = s.Load()
	if err != nil || o.Revision != 3 || o.Priority("T-1") != PriorityHigh || o.Events[2].By != "omp:x" || o.Events[0].By != "" {
		t.Fatalf("after write: %+v %v", o, err)
	}

	// A future schema is refused, never silently downgraded.
	if err := os.WriteFile(s.Path(), []byte(`{"version":3,"manifest_sha256":"`+testSHA+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(); err == nil {
		t.Fatal("version 3 accepted")
	}
}

func TestRecordActivity(t *testing.T) {
	s := newStore(t)
	o, err := s.RecordActivity("claude:abc", "T-1", ActivityActive, SourceFileEdit, "internal/x.go")
	if err != nil {
		t.Fatalf("RecordActivity: %v", err)
	}
	a := o.Activity["T-1"]
	if a.State != ActivityActive || a.By != "claude:abc" || a.Source != SourceFileEdit || a.Detail != "internal/x.go" || a.At == "" {
		t.Fatalf("activity = %+v", a)
	}
	if o.Revision != 1 || len(o.Events) != 1 {
		t.Fatalf("revision %d events %+v", o.Revision, o.Events)
	}
	ev := o.Events[0]
	if ev.Kind != KindActivity || ev.Value != "active" || ev.By != "claude:abc" || ev.TaskID != "T-1" || ev.Seq != 1 {
		t.Fatalf("event = %+v", ev)
	}

	// Same state and source within 30 s: no event, no write, even from another
	// consumer or with another detail.
	before, _ := os.ReadFile(s.Path())
	o, err = s.RecordActivity("omp:other", "T-1", ActivityActive, SourceFileEdit, "internal/y.go")
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(s.Path())
	if o.Revision != 1 || !bytes.Equal(before, after) || o.Activity["T-1"].Detail != "internal/x.go" {
		t.Fatalf("duplicate wrote: revision %d, %+v", o.Revision, o.Activity["T-1"])
	}

	// A different source or state records.
	if o, err = s.RecordActivity("claude:abc", "T-1", ActivityActive, SourceReport, ""); err != nil || o.Revision != 2 {
		t.Fatalf("new source: revision %d, %v", o.Revision, err)
	}
	if o, err = s.RecordActivity("claude:abc", "T-1", ActivityDone, SourceReport, "all green"); err != nil || o.Revision != 3 {
		t.Fatalf("new state: revision %d, %v", o.Revision, err)
	}
	if o.Activity["T-1"].State != ActivityDone {
		t.Fatalf("latest must win: %+v", o.Activity["T-1"])
	}

	// The same record older than 30 s is written again, with a new event.
	ageActivity(t, s, "T-1", 45*time.Second)
	o, err = s.RecordActivity("claude:abc", "T-1", ActivityDone, SourceReport, "again")
	if err != nil || o.Revision != 4 { // ageActivity is a raw update: no event of its own
		t.Fatalf("stale refresh: revision %d, %v", o.Revision, err)
	}
	if o.Activity["T-1"].Detail != "again" {
		t.Fatalf("stale refresh kept the old record: %+v", o.Activity["T-1"])
	}

	// Another task is independent.
	if o, err = s.RecordActivity("claude:abc", "T-2", ActivityBlocked, SourceVerify, ""); err != nil || o.Activity["T-2"].State != ActivityBlocked {
		t.Fatalf("T-2: %+v %v", o.Activity, err)
	}
}

func TestRecordActivityDetailAndValidation(t *testing.T) {
	s := newStore(t)
	long := strings.Repeat("é", 300)
	o, err := s.RecordActivity("omp:x", "T-1", ActivityActive, SourceReport, "a\x1b[31m\nb "+long)
	if err != nil {
		t.Fatal(err)
	}
	d := o.Activity["T-1"].Detail
	if strings.ContainsAny(d, "\x1b\n") || len([]rune(d)) != 200 || !strings.HasPrefix(d, "a [31m b ") {
		t.Fatalf("detail = %q (%d runes)", d, len([]rune(d)))
	}
	cases := []struct {
		name     string
		consumer string
		task     string
		state    ActivityState
		source   string
		want     error
	}{
		{"consumer", "someone", "T-1", ActivityActive, SourceReport, ErrConsumerID},
		{"task", "omp:x", "T 1", ActivityActive, SourceReport, ErrTaskID},
		{"state", "omp:x", "T-1", "paused", SourceReport, ErrActivityState},
		{"source", "omp:x", "T-1", ActivityDone, "guess", ErrActivitySource},
	}
	for _, c := range cases {
		if _, err := s.RecordActivity(c.consumer, c.task, c.state, c.source, ""); !errors.Is(err, c.want) {
			t.Fatalf("%s: err = %v, want %v", c.name, err, c.want)
		}
	}
}

func TestActivityRetentionIsBounded(t *testing.T) {
	s := newStore(t)
	base := time.Now().UTC().Add(-time.Hour)
	if _, err := s.update(func(o *Overlay) (bool, error) {
		for i := range MaxActivity {
			id := fmt.Sprintf("T-%d", i)
			o.Activity[id] = Activity{
				State: ActivityActive, By: "omp:x", Source: SourceReport,
				At: base.Add(time.Duration(i) * time.Second).Format(time.RFC3339),
			}
		}
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	o, err := s.RecordActivity("omp:x", "T-new", ActivityActive, SourceReport, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(o.Activity) != MaxActivity {
		t.Fatalf("activity = %d entries", len(o.Activity))
	}
	if _, ok := o.Activity["T-0"]; ok {
		t.Fatal("the stalest record survived")
	}
	if _, ok := o.Activity["T-new"]; !ok {
		t.Fatal("the new record is missing")
	}
}

func TestAddDiscoveryTask(t *testing.T) {
	s := newStore(t)
	d, o, err := s.AddDiscovery("omp:abc", DiscoveryInput{
		Kind: DiscoveryTask, Title: "  Handle \x1b[31mempty\n input ", Paths: []string{"./pkg//a.go", "pkg/a.go", "pkg/b"},
		ParentTaskID: "T-1", Reason: "found\twhile\nreading",
	})
	if err != nil {
		t.Fatalf("AddDiscovery: %v", err)
	}
	want := Discovery{
		ID: "D-1", Kind: DiscoveryTask, Title: "Handle [31mempty input", Paths: []string{"pkg/a.go", "pkg/b"},
		ParentTaskID: "T-1", Reason: "found while reading", By: "omp:abc", Status: DiscoveryProposed, Priority: PriorityNormal,
	}
	if d.At == "" {
		t.Fatal("no timestamp")
	}
	want.At = d.At
	if !reflect.DeepEqual(d, want) {
		t.Fatalf("discovery = %+v\nwant %+v", d, want)
	}
	if !reflect.DeepEqual(o.Discoveries, []Discovery{want}) {
		t.Fatalf("stored = %+v", o.Discoveries)
	}
	if o.Revision != 1 || len(o.Events) != 1 {
		t.Fatalf("events = %+v", o.Events)
	}
	ev := o.Events[0]
	if ev.Kind != KindDiscovered || ev.Value != "D-1" || ev.TaskID != "D-1" || ev.By != "omp:abc" {
		t.Fatalf("event = %+v", ev)
	}
	// IDs are monotonic.
	d2, _, err := s.AddDiscovery("omp:abc", DiscoveryInput{Kind: DiscoveryTask, Title: "second"})
	if err != nil || d2.ID != "D-2" || d2.Paths == nil || len(d2.Paths) != 0 {
		t.Fatalf("second = %+v, %v", d2, err)
	}
	raw, _ := os.ReadFile(s.Path())
	if !strings.Contains(string(raw), `"paths":[]`) || !strings.Contains(string(raw), `"parent_task_id":""`) {
		t.Fatalf("shape: %s", raw)
	}
}

func TestAddDiscoveryLimitsAndSanitizing(t *testing.T) {
	s := newStore(t)
	d, _, err := s.AddDiscovery("claude:s1", DiscoveryInput{
		Kind: DiscoveryTask, Title: strings.Repeat("t", 200), Reason: strings.Repeat("ü", 900),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len([]rune(d.Title)) != 120 || len([]rune(d.Reason)) != 500 {
		t.Fatalf("title %d reason %d runes", len([]rune(d.Title)), len([]rune(d.Reason)))
	}
}

func TestAddDiscoveryValidation(t *testing.T) {
	s := newStore(t)
	base := DiscoveryInput{Kind: DiscoveryTask, Title: "ok"}
	cases := []struct {
		name     string
		consumer string
		mut      func(*DiscoveryInput)
		want     error
	}{
		{"consumer", "bob", func(*DiscoveryInput) {}, ErrConsumerID},
		{"kind", "omp:x", func(d *DiscoveryInput) { d.Kind = "epic" }, ErrDiscoveryKind},
		{"empty kind", "omp:x", func(d *DiscoveryInput) { d.Kind = "" }, ErrDiscoveryKind},
		{"title required", "omp:x", func(d *DiscoveryInput) { d.Title = " \n\x1b " }, ErrDiscoveryTitle},
		{"parent", "omp:x", func(d *DiscoveryInput) { d.ParentTaskID = "T 1" }, ErrTaskID},
		{"absolute", "omp:x", func(d *DiscoveryInput) { d.Paths = []string{"/etc/passwd"} }, ErrDiscoveryPath},
		{"dotdot", "omp:x", func(d *DiscoveryInput) { d.Paths = []string{"../x"} }, ErrDiscoveryPath},
		{"inner dotdot", "omp:x", func(d *DiscoveryInput) { d.Paths = []string{"a/../b"} }, ErrDiscoveryPath},
		{"git", "omp:x", func(d *DiscoveryInput) { d.Paths = []string{".git/config"} }, ErrDiscoveryPath},
		{"git dir", "omp:x", func(d *DiscoveryInput) { d.Paths = []string{".git"} }, ErrDiscoveryPath},
		{"git case", "omp:x", func(d *DiscoveryInput) { d.Paths = []string{".GIT/hooks/x"} }, ErrDiscoveryPath},
		{"plan state", "omp:x", func(d *DiscoveryInput) { d.Paths = []string{"docs/plans/.plan/manifest.json"} }, ErrDiscoveryPath},
		{"plan state dir", "omp:x", func(d *DiscoveryInput) { d.Paths = []string{"./docs/plans/.plan/"} }, ErrDiscoveryPath},
		{"backslash", "omp:x", func(d *DiscoveryInput) { d.Paths = []string{`a\b.go`} }, ErrDiscoveryPath},
		{"drive", "omp:x", func(d *DiscoveryInput) { d.Paths = []string{"C:/x"} }, ErrDiscoveryPath},
		{"empty path", "omp:x", func(d *DiscoveryInput) { d.Paths = []string{""} }, ErrDiscoveryPath},
		{"root", "omp:x", func(d *DiscoveryInput) { d.Paths = []string{"."} }, ErrDiscoveryPath},
		{"control", "omp:x", func(d *DiscoveryInput) { d.Paths = []string{"a\x1b[0m.go"} }, ErrDiscoveryPath},
		{"too long", "omp:x", func(d *DiscoveryInput) { d.Paths = []string{strings.Repeat("a", MaxPathBytes+1)} }, ErrDiscoveryPath},
		{"too many", "omp:x", func(d *DiscoveryInput) {
			for i := range MaxDiscoveryPaths + 1 {
				d.Paths = append(d.Paths, fmt.Sprintf("f%d.go", i))
			}
		}, ErrDiscoveryPath},
		{"path kind without path", "omp:x", func(d *DiscoveryInput) { d.Kind = DiscoveryPath }, ErrDiscoveryPath},
		{"path kind with two", "omp:x", func(d *DiscoveryInput) { d.Kind, d.Paths = DiscoveryPath, []string{"a", "b"} }, ErrDiscoveryPath},
	}
	for _, c := range cases {
		in := base
		c.mut(&in)
		if _, _, err := s.AddDiscovery(c.consumer, in); !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", c.name, err, c.want)
		}
	}
	o, err := s.Load()
	if err != nil || len(o.Discoveries) != 0 || o.Revision != 0 {
		t.Fatalf("a refused discovery was stored: %+v %v", o, err)
	}
	// Dots that are not traversal are fine.
	if _, _, err := s.AddDiscovery("omp:x", DiscoveryInput{Kind: DiscoveryPath, Paths: []string{"..hidden/.gitignore"}}); err != nil {
		t.Fatalf("..hidden/.gitignore: %v", err)
	}
	if d, _, err := s.AddDiscovery("omp:x", DiscoveryInput{Kind: DiscoveryPath, Paths: []string{"./.github//ci.yml"}}); err != nil || d.Paths[0] != ".github/ci.yml" || d.Title != ".github/ci.yml" {
		t.Fatalf("normalization: %+v %v", d, err)
	}
}

func TestPathDiscoveryDedupe(t *testing.T) {
	s := newStore(t)
	d1, o, err := s.AddDiscovery("omp:a", DiscoveryInput{Kind: DiscoveryPath, Paths: []string{"pkg/new.go"}, ParentTaskID: "T-1"})
	if err != nil || d1.ID != "D-1" || o.Revision != 1 {
		t.Fatalf("first: %+v %d %v", d1, o.Revision, err)
	}
	// Age the record so the refresh shows.
	old := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	if _, err := s.update(func(o *Overlay) (bool, error) { o.Discoveries[0].At = old; return true, nil }); err != nil {
		t.Fatal(err)
	}
	d2, o, err := s.AddDiscovery("claude:b", DiscoveryInput{Kind: DiscoveryPath, Paths: []string{"./pkg/new.go"}})
	if err != nil {
		t.Fatal(err)
	}
	if d2.ID != "D-1" || len(o.Discoveries) != 1 || o.Revision != 1 || len(o.Events) != 1 {
		t.Fatalf("not deduplicated: %+v revision %d events %d", o.Discoveries, o.Revision, len(o.Events))
	}
	if o.Discoveries[0].At == old || o.Discoveries[0].By != "omp:a" || o.Discoveries[0].ParentTaskID != "T-1" {
		t.Fatalf("only at may change: %+v", o.Discoveries[0])
	}
	// Any status dedupes, and the status stays.
	if _, err := s.ResolveDiscovery("D-1", false, ""); err != nil {
		t.Fatal(err)
	}
	d3, o, err := s.AddDiscovery("omp:a", DiscoveryInput{Kind: DiscoveryPath, Paths: []string{"pkg/new.go"}})
	if err != nil || d3.ID != "D-1" || d3.Status != DiscoveryDismissed || len(o.Discoveries) != 1 {
		t.Fatalf("dismissed path duplicated: %+v %v", o.Discoveries, err)
	}
	// A task discovery with the same path is not a path discovery.
	d4, _, err := s.AddDiscovery("omp:a", DiscoveryInput{Kind: DiscoveryTask, Title: "x", Paths: []string{"pkg/new.go"}})
	if err != nil || d4.ID != "D-2" {
		t.Fatalf("task discovery: %+v %v", d4, err)
	}
	// Same-second repeat does not write.
	before, _ := os.ReadFile(s.Path())
	if _, _, err := s.AddDiscovery("omp:a", DiscoveryInput{Kind: DiscoveryPath, Paths: []string{"pkg/new.go"}}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(s.Path())
	if !bytes.Equal(before, after) {
		t.Fatal("a same-second duplicate rewrote the file")
	}
}

func TestDiscoveryEvictionAndLimit(t *testing.T) {
	s := newStore(t)
	seedDiscoveries(t, s, MaxDiscoveries)

	// All proposed: refused, and nothing changes.
	before, _ := os.ReadFile(s.Path())
	_, o, err := s.AddDiscovery("omp:x", DiscoveryInput{Kind: DiscoveryTask, Title: "one too many"})
	if !errors.Is(err, ErrDiscoveryLimit) {
		t.Fatalf("err = %v, want ErrDiscoveryLimit", err)
	}
	after, _ := os.ReadFile(s.Path())
	if len(o.Discoveries) != MaxDiscoveries || !bytes.Equal(before, after) {
		t.Fatalf("a refused add changed the store: %d discoveries", len(o.Discoveries))
	}

	// Dismissed goes first, oldest first, even when an older accepted exists.
	setStatus(t, s, "D-3", DiscoveryAccepted)
	setStatus(t, s, "D-9", DiscoveryDismissed)
	setStatus(t, s, "D-7", DiscoveryDismissed)
	d, o, err := s.AddDiscovery("omp:x", DiscoveryInput{Kind: DiscoveryTask, Title: "fresh 1"})
	if err != nil || d.ID != "D-201" || len(o.Discoveries) != MaxDiscoveries {
		t.Fatalf("add 1: %+v %d %v", d, len(o.Discoveries), err)
	}
	if o.discoveryIndex("D-7") >= 0 || o.discoveryIndex("D-9") < 0 || o.discoveryIndex("D-3") < 0 {
		t.Fatal("expected the oldest dismissed (D-7) to be evicted first")
	}
	if _, _, err := s.AddDiscovery("omp:x", DiscoveryInput{Kind: DiscoveryTask, Title: "fresh 2"}); err != nil {
		t.Fatal(err)
	}
	o, _ = s.Load()
	if o.discoveryIndex("D-9") >= 0 || o.discoveryIndex("D-3") < 0 {
		t.Fatal("expected D-9 to go before the accepted D-3")
	}
	// Then the oldest accepted.
	d, o, err = s.AddDiscovery("omp:x", DiscoveryInput{Kind: DiscoveryTask, Title: "fresh 3"})
	if err != nil || d.ID != "D-203" || o.discoveryIndex("D-3") >= 0 || len(o.Discoveries) != MaxDiscoveries {
		t.Fatalf("add 3: %+v %v", d, err)
	}
	// Every proposed again: refused.
	if _, _, err := s.AddDiscovery("omp:x", DiscoveryInput{Kind: DiscoveryTask, Title: "fresh 4"}); !errors.Is(err, ErrDiscoveryLimit) {
		t.Fatalf("err = %v", err)
	}
}

func TestDiscoveryIDsSurviveEvictingTheNewest(t *testing.T) {
	s := newStore(t)
	d1, _, err := s.AddDiscovery("omp:x", DiscoveryInput{Kind: DiscoveryTask, Title: "a"})
	if err != nil || d1.ID != "D-1" {
		t.Fatal(d1, err)
	}
	// Force the newest out of the list, as an eviction would.
	if _, err := s.update(func(o *Overlay) (bool, error) { o.Discoveries = nil; return true, nil }); err != nil {
		t.Fatal(err)
	}
	d2, _, err := s.AddDiscovery("omp:x", DiscoveryInput{Kind: DiscoveryTask, Title: "b"})
	if err != nil || d2.ID != "D-2" {
		t.Fatalf("id reused: %+v %v", d2, err)
	}
}

func TestResolveDiscovery(t *testing.T) {
	s := newStore(t)
	if _, _, err := s.AddDiscovery("omp:a", DiscoveryInput{Kind: DiscoveryTask, Title: "Handle empty input", ParentTaskID: "T-1"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AddDiscovery("omp:a", DiscoveryInput{Kind: DiscoveryPath, Paths: []string{"pkg/n.go"}}); err != nil {
		t.Fatal(err)
	}

	o, err := s.ResolveDiscovery("D-1", true, PriorityHigh)
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	d := o.Discoveries[0]
	if d.Status != DiscoveryAccepted || d.Priority != PriorityHigh || o.Priority("D-1") != PriorityHigh {
		t.Fatalf("accepted = %+v, task priority %q", d, o.Priority("D-1"))
	}
	ev := o.Events[len(o.Events)-1]
	if ev.Kind != KindDiscoveryAccepted || ev.Value != "D-1:high" || ev.By != "user" || ev.TaskID != "D-1" || o.Revision != 3 {
		t.Fatalf("event = %+v revision %d", ev, o.Revision)
	}

	// Same state: no-op.
	if o, err = s.ResolveDiscovery("D-1", true, PriorityHigh); err != nil || o.Revision != 3 {
		t.Fatalf("re-accept: revision %d, %v", o.Revision, err)
	}
	// A new priority is a change.
	if o, err = s.ResolveDiscovery("D-1", true, PriorityLow); err != nil || o.Revision != 4 || o.Discoveries[0].Priority != PriorityLow || o.Priority("D-1") != PriorityLow {
		t.Fatalf("re-prioritise: %+v %v", o, err)
	}
	// A user priority edit of the accepted task keeps the discovery in step.
	if o, err = s.SetPriority("D-1", PriorityHigh); err != nil || o.Discoveries[0].Priority != PriorityHigh {
		t.Fatalf("SetPriority sync: %+v %v", o.Discoveries[0], err)
	}

	// Dismiss, twice.
	o, err = s.ResolveDiscovery("D-1", false, "")
	if err != nil || o.Discoveries[0].Status != DiscoveryDismissed {
		t.Fatalf("dismiss: %+v %v", o.Discoveries[0], err)
	}
	ev = o.Events[len(o.Events)-1]
	rev := o.Revision
	if ev.Kind != KindDiscoveryDismissed || ev.Value != "D-1" || ev.By != "user" || ev.TaskID != "D-1" {
		t.Fatalf("event = %+v", ev)
	}
	if o, err = s.ResolveDiscovery("D-1", false, ""); err != nil || o.Revision != rev {
		t.Fatalf("re-dismiss: revision %d, %v", o.Revision, err)
	}
	// A dismissed discovery can still be accepted; an empty priority is normal.
	o, err = s.ResolveDiscovery("D-1", true, "")
	if err != nil || o.Discoveries[0].Status != DiscoveryAccepted || o.Discoveries[0].Priority != PriorityNormal {
		t.Fatalf("accept after dismiss: %+v %v", o.Discoveries[0], err)
	}
	if ev = o.Events[len(o.Events)-1]; ev.Value != "D-1:normal" {
		t.Fatalf("event = %+v", ev)
	}
	if _, ok := o.Tasks["D-1"]; ok {
		t.Fatalf("normal priority must not be stored: %+v", o.Tasks)
	}

	// A path discovery is not a task: no task overlay.
	if o, err = s.ResolveDiscovery("D-2", true, PriorityHigh); err != nil || o.Discoveries[1].Status != DiscoveryAccepted {
		t.Fatalf("accept path: %v", err)
	}
	if _, ok := o.Tasks["D-2"]; ok {
		t.Fatalf("a path discovery became a task: %+v", o.Tasks)
	}

	// Errors.
	for _, id := range []string{"D-99", "D-0", "D-", "D-01", "x", "", "T-1", "D-1;rm"} {
		if _, err := s.ResolveDiscovery(id, true, PriorityHigh); !errors.Is(err, ErrUnknownDiscovery) {
			t.Errorf("%q: err = %v, want ErrUnknownDiscovery", id, err)
		}
	}
	if _, err := s.ResolveDiscovery("D-1", true, "urgent"); !errors.Is(err, ErrPriority) {
		t.Fatalf("bad priority: %v", err)
	}
	// A dismissal ignores the priority.
	if _, err := s.ResolveDiscovery("D-1", false, "urgent"); err != nil {
		t.Fatalf("dismiss with junk priority: %v", err)
	}
}

func TestMapPaths(t *testing.T) {
	m := contracts.PlanManifest{TaskDAG: []contracts.TechnicalTask{
		{ID: "T-1", Targets: []contracts.Target{{Path: "internal/board/board.go"}, {Path: "cmd/client"}}},
		{ID: "T-2", Targets: []contracts.Target{{Path: "internal/board/"}, {Path: "web/src/./app.tsx"}}},
		{ID: "T-3", Targets: []contracts.Target{{Path: "/abs/never"}, {Path: "../escape"}, {Path: "."}, {Path: ""}}},
		{ID: "T-4"},
	}}
	by, un := MapPaths(m, []string{
		"internal/board/board.go",    // T-1 exact, T-2 prefix
		"internal/board/delta.go",    // T-2 prefix only
		"cmd/client/main.go",         // T-1 directory prefix
		"cmd/clientele/main.go",      // not a prefix: segment boundary
		"web/src/app.tsx",            // T-2 after Clean of the target
		"./web//src/app.tsx",         // same path after Clean, deduplicated
		"README.md",                  // unplanned
		"docs/z.md",                  // unplanned
		"docs/a.md",                  // unplanned, sorts first
		"/abs/never/x.go",            // invalid
		"../escape/x.go",             // invalid
		".git/config",                // invalid
		"docs/plans/.plan/m.json",    // invalid
		"",                           // invalid
		"README.md",                  // duplicate
		"internal/board",             // equals the T-2 directory target
		"a/../internal/board/x.go",   // invalid: dotdot
		"abs/never",                  // T-3's absolute target never matches
		`internal\board\board.go`,    // invalid: backslash
		"internal/boardgame/main.go", // not under internal/board
	})
	wantBy := map[string][]string{
		"T-1": {"cmd/client/main.go", "internal/board/board.go"},
		"T-2": {"internal/board", "internal/board/board.go", "internal/board/delta.go", "web/src/app.tsx"},
	}
	wantUn := []string{"README.md", "abs/never", "cmd/clientele/main.go", "docs/a.md", "docs/z.md", "internal/boardgame/main.go"}
	if !reflect.DeepEqual(by, wantBy) {
		t.Fatalf("byTask = %#v\nwant %#v", by, wantBy)
	}
	if !reflect.DeepEqual(un, wantUn) {
		t.Fatalf("unplanned = %#v\nwant %#v", un, wantUn)
	}

	// Deterministic whatever the input order.
	rev := []string{"x/y.go", "internal/board/delta.go", "cmd/client/a.go"}
	by1, un1 := MapPaths(m, rev)
	by2, un2 := MapPaths(m, []string{rev[2], rev[1], rev[0]})
	if !reflect.DeepEqual(by1, by2) || !reflect.DeepEqual(un1, un2) {
		t.Fatalf("order-dependent: %v %v / %v %v", by1, un1, by2, un2)
	}
	// Empty input yields empty, non-nil results.
	by, un = MapPaths(m, nil)
	if by == nil || un == nil || len(by) != 0 || len(un) != 0 {
		t.Fatalf("empty = %#v %#v", by, un)
	}
	by, un = MapPaths(contracts.PlanManifest{}, []string{"a.go"})
	if len(by) != 0 || !reflect.DeepEqual(un, []string{"a.go"}) {
		t.Fatalf("no tasks = %#v %#v", by, un)
	}
}

func TestConcurrentMixedWritesLoseNoUpdate(t *testing.T) {
	s := newStore(t)
	const workers = 12
	const perWorker = 4
	var wg sync.WaitGroup
	errs := make(chan error, workers*perWorker*3)
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st, err := OpenAt(strings.TrimSuffix(s.Path(), "/"+testSHA+".json"), testSHA) // own Store value, same file
			if err != nil {
				errs <- err
				return
			}
			consumer := fmt.Sprintf("omp:w%d", w)
			for i := range perWorker {
				task := fmt.Sprintf("T-%d-%d", w, i)
				if _, err := st.RecordActivity(consumer, task, ActivityActive, SourceFileEdit, "x"); err != nil {
					errs <- fmt.Errorf("activity: %w", err)
				}
				if _, _, err := st.AddDiscovery(consumer, DiscoveryInput{Kind: DiscoveryTask, Title: "found " + task, ParentTaskID: task}); err != nil {
					errs <- fmt.Errorf("discovery: %w", err)
				}
				if _, err := st.SetPriority(task, PriorityHigh); err != nil {
					errs <- fmt.Errorf("priority: %w", err)
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	o, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	total := workers * perWorker
	if len(o.Activity) != total || len(o.Discoveries) != total || len(o.Tasks) != total {
		t.Fatalf("lost updates: activity %d discoveries %d tasks %d, want %d", len(o.Activity), len(o.Discoveries), len(o.Tasks), total)
	}
	if o.Revision != uint64(3*total) || len(o.Events) != 3*total {
		t.Fatalf("revision %d events %d, want %d", o.Revision, len(o.Events), 3*total)
	}
	ids := map[string]bool{}
	for _, d := range o.Discoveries {
		if ids[d.ID] {
			t.Fatalf("duplicate discovery id %s", d.ID)
		}
		ids[d.ID] = true
	}
	for i, ev := range o.Events {
		if ev.Seq != uint64(i+1) {
			t.Fatalf("event %d has seq %d", i, ev.Seq)
		}
	}
}
