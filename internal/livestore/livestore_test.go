//go:build client && !server

package livestore

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const testRepo = "/tmp/plan-forge-livestore-test-repo"

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenAt(filepath.Join(t.TempDir(), "live"), testRepo)
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	return s
}

// freeze pins the clock for the test and restores it afterwards, so eviction
// and retention are exercised without sleeping.
func freeze(t *testing.T, at time.Time) func(time.Time) {
	t.Helper()
	prev := now
	t.Cleanup(func() { now = prev })
	cur := at
	now = func() time.Time { return cur.UTC() }
	return func(next time.Time) { cur = next }
}

func beat(t *testing.T, s *Store, id, kind string) Doc {
	t.Helper()
	d, err := s.Record(Record{SessionID: id, Harness: "claude-code", Kind: kind})
	if err != nil {
		t.Fatalf("Record(%s, %s): %v", id, kind, err)
	}
	return d
}

func TestRecordUpsertsTheSessionAndAppendsOneEvent(t *testing.T) {
	s := newStore(t)
	d, err := s.Record(Record{
		SessionID: "claude:abc", Harness: "claude-code", Developer: "dev",
		Branch: "feat/x", Kind: KindSessionStart,
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	sess, ok := d.Sessions["claude:abc"]
	if !ok {
		t.Fatal("session not stored")
	}
	if sess.Branch != "feat/x" || sess.Developer != "dev" || sess.Mode != ModeUnplanned {
		t.Errorf("session = %+v", sess)
	}
	if len(d.Events) != 1 || d.Events[0].Kind != KindSessionStart {
		t.Errorf("events = %+v", d.Events)
	}

	// A later tool ping must not erase what session_start established.
	d = beat(t, s, "claude:abc", KindTool)
	if got := d.Sessions["claude:abc"]; got.Branch != "feat/x" || got.Developer != "dev" {
		t.Errorf("tool ping cleared session fields: %+v", got)
	}
	if len(d.Sessions) != 1 || len(d.Events) != 2 {
		t.Errorf("sessions=%d events=%d, want 1 and 2", len(d.Sessions), len(d.Events))
	}
}

func TestRecordRefusesInputItCannotStoreSafely(t *testing.T) {
	s := newStore(t)
	cases := map[string]Record{
		"empty id":        {SessionID: "", Harness: "h", Kind: KindTool},
		"unqualified id":  {SessionID: "abc", Harness: "h", Kind: KindTool},
		"unknown id prefix": {SessionID: "cursor:abc", Harness: "h", Kind: KindTool},
		"id too long":     {SessionID: "claude:" + strings.Repeat("a", 129), Harness: "h", Kind: KindTool},
		"empty harness":   {SessionID: "claude:abc", Harness: " ", Kind: KindTool},
		"unknown kind":    {SessionID: "claude:abc", Harness: "h", Kind: "nope"},
		"empty kind":      {SessionID: "claude:abc", Harness: "h", Kind: ""},
	}
	for name, rec := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := s.Record(rec); err == nil {
				t.Fatal("accepted")
			}
			if _, err := os.Stat(s.Path()); !os.IsNotExist(err) {
				t.Errorf("a refused record created the document: %v", err)
			}
		})
	}
}

func TestModeOnlyEverMovesUp(t *testing.T) {
	s := newStore(t)
	beat(t, s, "omp:1", KindSessionStart)
	d, err := s.Record(Record{SessionID: "omp:1", Harness: "omp", Mode: ModePlanned, TaskID: "T-1", Kind: KindTool})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if d.Sessions["omp:1"].Mode != ModePlanned {
		t.Fatal("session did not upgrade to PLANNED")
	}
	// A ping that knows nothing about the plan must not undo the upgrade.
	d = beat(t, s, "omp:1", KindTool)
	if got := d.Sessions["omp:1"]; got.Mode != ModePlanned || got.TaskID != "T-1" {
		t.Errorf("PLANNED was downgraded: %+v", got)
	}
}

func TestDirtyPathsAreSanitizedBoundedAndRelative(t *testing.T) {
	s := newStore(t)
	in := []string{"b.go", "a.go", "b.go", "/etc/passwd", "   ", "x\x00y.go"}
	for i := 0; i < MaxDirtyFiles+10; i++ {
		in = append(in, fmt.Sprintf("pkg/f%03d.go", i))
	}
	d, err := s.Record(Record{SessionID: "claude:p", Harness: "claude-code", Kind: KindTool, DirtyFiles: in})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	got := d.Sessions["claude:p"].DirtyFiles
	if len(got) != MaxDirtyFiles {
		t.Fatalf("len = %d, want %d", len(got), MaxDirtyFiles)
	}
	for i, p := range got {
		if filepath.IsAbs(p) {
			t.Errorf("absolute path survived: %q", p)
		}
		if strings.ContainsRune(p, 0) {
			t.Errorf("control character survived: %q", p)
		}
		if i > 0 && got[i-1] >= p {
			t.Errorf("not sorted or deduplicated at %d: %q then %q", i, got[i-1], p)
		}
	}
}

func TestUntrustedStringsAreBoundedOnWrite(t *testing.T) {
	s := newStore(t)
	d, err := s.Record(Record{
		SessionID: "claude:x", Harness: "claude-code",
		Branch:  strings.Repeat("b", MaxBranchRunes*2),
		Command: strings.Repeat("c", MaxCommandRunes*2),
		Tool:    strings.Repeat("t", MaxToolRunes*2),
		Detail:  strings.Repeat("d", MaxDetailRunes*2),
		Kind:    KindCommand,
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	sess := d.Sessions["claude:x"]
	for _, c := range []struct {
		name string
		got  string
		max  int
	}{
		{"branch", sess.Branch, MaxBranchRunes},
		{"command", sess.LastCommand, MaxCommandRunes},
		{"tool", sess.LastTool, MaxToolRunes},
		{"detail", d.Events[0].Detail, MaxDetailRunes},
	} {
		if len([]rune(c.got)) > c.max {
			t.Errorf("%s = %d runes, want at most %d", c.name, len([]rune(c.got)), c.max)
		}
	}
}

func TestSessionsAreCappedAndEvictedOldestHeartbeatFirst(t *testing.T) {
	s := newStore(t)
	set := freeze(t, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	for i := 0; i < MaxSessions+5; i++ {
		set(time.Date(2026, 1, 1, 0, i, 0, 0, time.UTC))
		beat(t, s, fmt.Sprintf("claude:s%03d", i), KindTool)
	}
	d, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(d.Sessions) != MaxSessions {
		t.Fatalf("sessions = %d, want %d", len(d.Sessions), MaxSessions)
	}
	// The five oldest heartbeats are the ones that went.
	for i := 0; i < 5; i++ {
		if _, ok := d.Sessions[fmt.Sprintf("claude:s%03d", i)]; ok {
			t.Errorf("s%03d survived although it had the oldest heartbeat", i)
		}
	}
	if _, ok := d.Sessions[fmt.Sprintf("claude:s%03d", MaxSessions+4)]; !ok {
		t.Error("the newest session was evicted")
	}
}

func TestSessionsAndEventsExpire(t *testing.T) {
	s := newStore(t)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	set := freeze(t, start)
	beat(t, s, "claude:old", KindTool)

	// Still inside EvictAfter: present, but stale enough to render idle.
	set(start.Add(StaleAfter + time.Minute))
	d, err := s.Sweep()
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	sess, ok := d.Sessions["claude:old"]
	if !ok {
		t.Fatal("session evicted too early")
	}
	if !sess.Stale(now()) {
		t.Error("session should render stale")
	}

	// Past EvictAfter: the session goes. Past Retention: the event goes too.
	set(start.Add(Retention + time.Hour))
	if d, err = s.Sweep(); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(d.Sessions) != 0 {
		t.Errorf("sessions = %+v, want none", d.Sessions)
	}
	if len(d.Events) != 0 {
		t.Errorf("events = %+v, want none past retention", d.Events)
	}
}

func TestEventsAreCapped(t *testing.T) {
	s := newStore(t)
	for i := 0; i < MaxEvents+20; i++ {
		beat(t, s, "claude:e", KindTool)
	}
	d, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(d.Events) != MaxEvents {
		t.Fatalf("events = %d, want %d", len(d.Events), MaxEvents)
	}
	// The log keeps the newest, and seq never rewinds.
	if d.Events[len(d.Events)-1].Seq != d.Seq {
		t.Errorf("last event seq %d != doc seq %d", d.Events[len(d.Events)-1].Seq, d.Seq)
	}
	for i := 1; i < len(d.Events); i++ {
		if d.Events[i].Seq <= d.Events[i-1].Seq {
			t.Fatalf("seq not increasing at %d", i)
		}
	}
}

func TestConcurrentRecordsLoseNoUpdate(t *testing.T) {
	s := newStore(t)
	const writers = 8
	const each = 12
	var wg sync.WaitGroup
	errs := make(chan error, writers*each)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				if _, err := s.Record(Record{
					SessionID: fmt.Sprintf("claude:w%d", w), Harness: "claude-code", Kind: KindTool,
				}); err != nil {
					errs <- err
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent Record: %v", err)
	}
	d, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if d.Seq != writers*each {
		t.Errorf("seq = %d, want %d: an update was lost", d.Seq, writers*each)
	}
	if len(d.Sessions) != writers {
		t.Errorf("sessions = %d, want %d", len(d.Sessions), writers)
	}
}

func TestDocumentAndDirectoryArePrivate(t *testing.T) {
	s := newStore(t)
	beat(t, s, "claude:m", KindTool)
	fi, err := os.Stat(s.Path())
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("document mode = %v, want 0600", got)
	}
	di, err := os.Stat(filepath.Dir(s.Path()))
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if got := di.Mode().Perm(); got != 0o700 {
		t.Errorf("directory mode = %v, want 0700", got)
	}
}

func TestLoadRejectsAnotherRepositorysDocumentAndAnUnknownVersion(t *testing.T) {
	s := newStore(t)
	for name, body := range map[string]string{
		"foreign repo":    `{"version":1,"repo_key":"deadbeef","sessions":{},"events":[]}`,
		"unknown version": `{"version":99,"sessions":{},"events":[]}`,
		"not json":        `{`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(s.Path(), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Load(); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func TestMissingDocumentLoadsEmptyWithoutCreatingIt(t *testing.T) {
	s := newStore(t)
	d, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(d.Sessions) != 0 || len(d.Events) != 0 || d.Version != Version {
		t.Errorf("doc = %+v", d)
	}
	if _, err := os.Stat(s.Path()); !os.IsNotExist(err) {
		t.Errorf("Load created the document: %v", err)
	}
}

func TestWriteTrimDropsOldEventsBeforeFailing(t *testing.T) {
	s := newStore(t)
	d := s.empty()
	d.Sessions["claude:big"] = Session{ID: "claude:big", Harness: "claude-code", LastBeat: stamp(now())}
	for i := 0; i < 400; i++ {
		d.Events = append(d.Events, Event{
			Seq: uint64(i + 1), At: stamp(now()), SessionID: "claude:big",
			Kind: KindCommand, Detail: strings.Repeat("x", MaxDetailRunes),
		})
	}
	if err := s.writeTrim(&d); err != nil {
		t.Fatalf("writeTrim: %v", err)
	}
	if len(d.Events) == 0 {
		t.Fatal("every event was dropped")
	}

	// A single oversized session alone cannot fit: that is the only ErrTooLarge.
	huge := s.empty()
	for i := 0; i < MaxSessions; i++ {
		huge.Sessions[fmt.Sprintf("claude:h%03d", i)] = Session{
			ID: fmt.Sprintf("claude:h%03d", i), Harness: strings.Repeat("h", 60),
			DirtyFiles: make([]string, 0), LastBeat: stamp(now()),
		}
	}
	for i := range huge.Sessions {
		sess := huge.Sessions[i]
		for j := 0; j < MaxDirtyFiles; j++ {
			sess.DirtyFiles = append(sess.DirtyFiles, strings.Repeat("p", MaxPathRunes))
		}
		huge.Sessions[i] = sess
	}
	if err := s.writeTrim(&huge); err != nil && !errors.Is(err, ErrTooLarge) {
		t.Fatalf("want nil or ErrTooLarge, got %v", err)
	}
}
