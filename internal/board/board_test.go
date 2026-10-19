//go:build client && !server

package board

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/config"
)

const testSHA = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenAt(filepath.Join(t.TempDir(), "board"), testSHA)
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	return s
}

func TestOpenValidatesManifestSHA(t *testing.T) {
	if _, err := OpenAt(t.TempDir(), strings.ToUpper(testSHA)); !errors.Is(err, ErrManifestSHA) {
		t.Fatalf("uppercase sha: %v", err)
	}
	if _, err := OpenAt(t.TempDir(), testSHA[:63]); !errors.Is(err, ErrManifestSHA) {
		t.Fatalf("short sha: %v", err)
	}
	if _, err := OpenAt(t.TempDir(), testSHA+"0"); !errors.Is(err, ErrManifestSHA) {
		t.Fatalf("long sha: %v", err)
	}
	if _, err := OpenAt(t.TempDir(), strings.Repeat("g", 64)); !errors.Is(err, ErrManifestSHA) {
		t.Fatalf("non-hex sha: %v", err)
	}
	if _, err := OpenAt("", testSHA); err == nil {
		t.Fatal("empty dir accepted")
	}
	if _, err := Open(testSHA); err != nil {
		t.Fatalf("Open: %v", err)
	}
}

func TestOpenUsesTheCacheBoardDirectory(t *testing.T) {
	s, err := Open(testSHA)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	base, err := config.Cache()
	if err != nil {
		t.Fatalf("Cache: %v", err)
	}
	if want := filepath.Join(base, "board", testSHA+".json"); s.Path() != want {
		t.Fatalf("Path() = %q, want %q", s.Path(), want)
	}
}

func TestLoadMissingFileIsEmptyAndCreatesNothing(t *testing.T) {
	s := newStore(t)
	o, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if o.Version != Version || o.Revision != 0 || len(o.Events) != 0 || len(o.Tasks) != 0 || len(o.Cursors) != 0 {
		t.Fatalf("unexpected empty overlay: %+v", o)
	}
	if _, err := os.Stat(s.Path()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Load created the file: %v", err)
	}
}

func TestSetPriority(t *testing.T) {
	s := newStore(t)

	// Setting the default on an unseen task is a no-op: no event, no file.
	o, err := s.SetPriority("T-1", PriorityNormal)
	if err != nil {
		t.Fatalf("SetPriority(normal): %v", err)
	}
	if o.Revision != 0 || len(o.Events) != 0 {
		t.Fatalf("no-op changed the overlay: %+v", o)
	}
	if _, err := os.Stat(s.Path()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("no-op wrote the file: %v", err)
	}

	o, err = s.SetPriority("T-1", PriorityHigh)
	if err != nil {
		t.Fatalf("SetPriority(high): %v", err)
	}
	if o.Revision != 1 || len(o.Events) != 1 {
		t.Fatalf("revision/events = %d/%d, want 1/1", o.Revision, len(o.Events))
	}
	ev := o.Events[0]
	if ev.Seq != 1 || ev.TaskID != "T-1" || ev.Kind != KindPriority || ev.Value != "high" {
		t.Fatalf("event = %+v", ev)
	}
	if o.Priority("T-1") != PriorityHigh {
		t.Fatalf("priority = %q", o.Priority("T-1"))
	}

	// Setting the current value again is a no-op.
	o, err = s.SetPriority("T-1", PriorityHigh)
	if err != nil {
		t.Fatalf("SetPriority(high) again: %v", err)
	}
	if o.Revision != 1 || len(o.Events) != 1 {
		t.Fatalf("repeat changed the overlay: revision %d, %d events", o.Revision, len(o.Events))
	}

	// Back to normal: stored as absent, but logged.
	o, err = s.SetPriority("T-1", PriorityNormal)
	if err != nil {
		t.Fatalf("SetPriority(normal): %v", err)
	}
	if o.Revision != 2 || o.Events[1].Value != "normal" {
		t.Fatalf("overlay = %+v", o)
	}
	if _, ok := o.Tasks["T-1"]; ok {
		t.Fatalf("empty task entry kept: %+v", o.Tasks)
	}
	if o.Priority("T-1") != PriorityNormal {
		t.Fatalf("effective priority = %q", o.Priority("T-1"))
	}
}

func TestSetPriorityValidation(t *testing.T) {
	s := newStore(t)
	for _, id := range []string{"", "T 1", "T/1", strings.Repeat("T", 65), "T-1\n"} {
		if _, err := s.SetPriority(id, PriorityHigh); !errors.Is(err, ErrTaskID) {
			t.Fatalf("task id %q: %v", id, err)
		}
	}
	for _, p := range []Priority{"", "urgent", "HIGH", " normal"} {
		if _, err := s.SetPriority("T-1", p); !errors.Is(err, ErrPriority) {
			t.Fatalf("priority %q: %v", p, err)
		}
	}
	for _, id := range []string{"T-1", "T_1", "t.1", "0", strings.Repeat("T", 64)} {
		if !ValidTaskID(id) {
			t.Fatalf("ValidTaskID(%q) = false", id)
		}
	}
}

func TestAddNoteLimits(t *testing.T) {
	s := newStore(t)
	if _, err := s.AddNote("T-1", "   \n\t "); !errors.Is(err, ErrNoteEmpty) {
		t.Fatalf("empty note: %v", err)
	}
	if _, err := s.AddNote("T-1", strings.Repeat("x", MaxNoteRunes+1)); !errors.Is(err, ErrNoteTooLong) {
		t.Fatalf("long note: %v", err)
	}
	o, err := s.AddNote("T-1", "  "+strings.Repeat("x", MaxNoteRunes)+"  ")
	if err != nil {
		t.Fatalf("note at the rune cap: %v", err)
	}
	if len(o.Tasks["T-1"].Notes) != 1 {
		t.Fatalf("notes = %+v", o.Tasks["T-1"].Notes)
	}
	if got := len([]rune(o.Tasks["T-1"].Notes[0].Text)); got != MaxNoteRunes {
		t.Fatalf("stored %d runes, want %d", got, MaxNoteRunes)
	}

	// The rune cap counts runes, not bytes.
	multi := strings.Repeat("漢", MaxNoteRunes)
	if _, err := s.AddNote("T-2", multi); err != nil {
		t.Fatalf("multibyte note at the rune cap: %v", err)
	}
	if _, err := s.AddNote("T-2", multi+"漢"); !errors.Is(err, ErrNoteTooLong) {
		t.Fatalf("multibyte note over the rune cap: %v", err)
	}
}

func TestAddNoteLimitPerTask(t *testing.T) {
	s := newStore(t)
	for i := range MaxNotes {
		o, err := s.AddNote("T-1", fmt.Sprintf("note %d", i))
		if err != nil {
			t.Fatalf("note %d: %v", i, err)
		}
		if want := fmt.Sprintf("n-%d", i+1); o.Tasks["T-1"].Notes[i].ID != want {
			t.Fatalf("note %d id = %q, want %q", i, o.Tasks["T-1"].Notes[i].ID, want)
		}
	}
	if _, err := s.AddNote("T-1", "one too many"); !errors.Is(err, ErrNoteLimit) {
		t.Fatalf("note %d: %v", MaxNotes+1, err)
	}
	o, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(o.Tasks["T-1"].Notes) != MaxNotes {
		t.Fatalf("notes = %d, want %d", len(o.Tasks["T-1"].Notes), MaxNotes)
	}
	if o.Revision != MaxNotes {
		t.Fatalf("revision = %d, want %d", o.Revision, MaxNotes)
	}
}

func TestAddNoteTrimsAndLogs(t *testing.T) {
	s := newStore(t)
	o, err := s.AddNote("T-1", "  use the ledger table\n")
	if err != nil {
		t.Fatalf("AddNote: %v", err)
	}
	n := o.Tasks["T-1"].Notes[0]
	if n.ID != "n-1" || n.Text != "use the ledger table" || n.At == "" {
		t.Fatalf("note = %+v", n)
	}
	if ev := o.Events[0]; ev.Kind != KindContext || ev.Value != "use the ledger table" || ev.TaskID != "T-1" {
		t.Fatalf("event = %+v", ev)
	}
	if _, err := s.AddNote("T 1", "x"); !errors.Is(err, ErrTaskID) {
		t.Fatalf("bad task id: %v", err)
	}
}

func TestDeleteNote(t *testing.T) {
	s := newStore(t)
	if _, err := s.DeleteNote("T-1", "n-1"); !errors.Is(err, ErrUnknownTask) {
		t.Fatalf("unknown task: %v", err)
	}
	if _, err := s.AddNote("T-1", "keep me"); err != nil {
		t.Fatalf("AddNote: %v", err)
	}
	if _, err := s.DeleteNote("T-1", "n-9"); !errors.Is(err, ErrNoteNotFound) {
		t.Fatalf("unknown note: %v", err)
	}
	o, err := s.DeleteNote("T-1", "n-1")
	if err != nil {
		t.Fatalf("DeleteNote: %v", err)
	}
	if len(o.Tasks["T-1"].Notes) != 0 {
		t.Fatalf("notes = %+v", o.Tasks["T-1"].Notes)
	}
	if _, ok := o.Tasks["T-1"]; ok {
		t.Fatalf("empty task kept: %+v", o.Tasks)
	}
	ev := o.Events[len(o.Events)-1]
	if ev.Kind != KindContextRemoved || ev.Value != "n-1" || ev.Seq != 2 {
		t.Fatalf("event = %+v", ev)
	}
	// The removed id stays visible in the log, so the next note does not reuse it.
	o, err = s.AddNote("T-1", "another")
	if err != nil {
		t.Fatalf("AddNote: %v", err)
	}
	if got := o.Tasks["T-1"].Notes[0].ID; got != "n-2" {
		t.Fatalf("reused id %q", got)
	}
}

func TestEventCapKeepsNewest(t *testing.T) {
	s := newStore(t)
	total := MaxEvents + 20
	for i := range total {
		p := PriorityHigh
		if i%2 == 1 {
			p = PriorityLow
		}
		if _, err := s.SetPriority("T-1", p); err != nil {
			t.Fatalf("edit %d: %v", i, err)
		}
	}
	o, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if o.Revision != uint64(total) {
		t.Fatalf("revision = %d, want %d", o.Revision, total)
	}
	if len(o.Events) != MaxEvents {
		t.Fatalf("events = %d, want %d", len(o.Events), MaxEvents)
	}
	if first := o.Events[0].Seq; first != uint64(total-MaxEvents+1) {
		t.Fatalf("oldest seq = %d, want %d", first, total-MaxEvents+1)
	}
	if last := o.Events[len(o.Events)-1].Seq; last != uint64(total) {
		t.Fatalf("newest seq = %d, want %d", last, total)
	}
}

func TestPullCursorAdvanceAndPeek(t *testing.T) {
	s := newStore(t)
	const consumer = "claude:abc-123"

	// The first pull on an empty store registers the consumer and returns nothing.
	evs, o, err := s.Pull(consumer, false)
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if len(evs) != 0 || o.Revision != 0 {
		t.Fatalf("empty pull = %d events, revision %d", len(evs), o.Revision)
	}
	if _, ok := o.Cursors[consumer]; !ok {
		t.Fatalf("consumer not registered: %+v", o.Cursors)
	}

	if _, err := s.AddNote("T-1", "first"); err != nil {
		t.Fatalf("AddNote: %v", err)
	}
	if _, err := s.SetPriority("T-2", PriorityHigh); err != nil {
		t.Fatalf("SetPriority: %v", err)
	}

	evs, o, err = s.Pull(consumer, false)
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if len(evs) != 2 || evs[0].Seq != 1 || evs[1].Seq != 2 {
		t.Fatalf("events = %+v", evs)
	}
	if got := o.Cursors[consumer].Seq; got != 2 {
		t.Fatalf("cursor = %d, want 2", got)
	}

	evs, _, err = s.Pull(consumer, false)
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if len(evs) != 0 {
		t.Fatalf("second pull returned %+v", evs)
	}

	if _, err := s.AddNote("T-1", "third"); err != nil {
		t.Fatalf("AddNote: %v", err)
	}
	evs, o, err = s.Pull(consumer, true)
	if err != nil {
		t.Fatalf("peek: %v", err)
	}
	if len(evs) != 1 || evs[0].Seq != 3 {
		t.Fatalf("peek events = %+v", evs)
	}
	if got := o.Cursors[consumer].Seq; got != 2 {
		t.Fatalf("peek advanced the cursor to %d", got)
	}
	evs, _, err = s.Pull(consumer, false)
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if len(evs) != 1 || evs[0].Seq != 3 {
		t.Fatalf("events after peek = %+v", evs)
	}
}

func TestPullRejectsInvalidConsumer(t *testing.T) {
	s := newStore(t)
	bad := []string{
		"", "claude", "claude:", ":abc", "agy:abc", "claude:ab cd", "claude:a/b",
		"claude:" + strings.Repeat("a", 129), "omp:" + strings.Repeat("a", 129), "claude:a\n",
	}
	for _, id := range bad {
		if _, _, err := s.Pull(id, false); !errors.Is(err, ErrConsumerID) {
			t.Fatalf("consumer %q: %v", id, err)
		}
	}
	good := []string{"claude:abc", "omp:x", "claude:" + strings.Repeat("a", 128), "omp:a.b_c-d"}
	for _, id := range good {
		if !ValidConsumer(id) {
			t.Fatalf("ValidConsumer(%q) = false", id)
		}
		if _, _, err := s.Pull(id, true); err != nil {
			t.Fatalf("consumer %q: %v", id, err)
		}
	}
}

func TestPullKeepsSixtyFourCursors(t *testing.T) {
	s := newStore(t)
	for i := range 70 {
		if _, _, err := s.Pull(fmt.Sprintf("omp:c%02d", i), false); err != nil {
			t.Fatalf("pull %d: %v", i, err)
		}
	}
	o, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(o.Cursors) != MaxCursors {
		t.Fatalf("cursors = %d, want %d", len(o.Cursors), MaxCursors)
	}
	if _, ok := o.Cursors["omp:c69"]; !ok {
		t.Fatalf("newest cursor evicted: %+v", o.Cursors)
	}
}

func TestPruneCursorsIsDeterministic(t *testing.T) {
	in := map[string]Cursor{}
	for i := range 70 {
		in[fmt.Sprintf("claude:c%02d", i)] = Cursor{Seq: uint64(i), SeenAt: fmt.Sprintf("2026-01-01T00:%02d:00Z", i)}
	}
	out := pruneCursors(in)
	if len(out) != MaxCursors {
		t.Fatalf("cursors = %d, want %d", len(out), MaxCursors)
	}
	for i := 0; i < 6; i++ {
		if _, ok := out[fmt.Sprintf("claude:c%02d", i)]; ok {
			t.Fatalf("oldest cursor %d kept", i)
		}
	}
	if _, ok := out["claude:c69"]; !ok {
		t.Fatal("newest cursor dropped")
	}
	// On a tie the greater id wins, so omp:t06 … omp:t69 survive.
	tied := map[string]Cursor{}
	for i := range 70 {
		tied[fmt.Sprintf("omp:t%02d", i)] = Cursor{SeenAt: "2026-01-01T00:00:00Z"}
	}
	out = pruneCursors(tied)
	if _, ok := out["omp:t05"]; ok {
		t.Fatal("tie kept the lowest id")
	}
	if _, ok := out["omp:t06"]; !ok {
		t.Fatal("tie dropped the highest id")
	}
}

func TestOverlayJSONSchemaV1(t *testing.T) {
	s := newStore(t)
	if _, err := s.SetPriority("T-1", PriorityHigh); err != nil {
		t.Fatalf("SetPriority: %v", err)
	}
	if _, err := s.AddNote("T-1", "context"); err != nil {
		t.Fatalf("AddNote: %v", err)
	}
	if _, _, err := s.Pull("claude:abc", false); err != nil {
		t.Fatalf("Pull: %v", err)
	}
	raw, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	for _, k := range []string{"version", "manifest_sha256", "revision", "tasks", "events", "cursors"} {
		if _, ok := doc[k]; !ok {
			t.Fatalf("missing key %q in %s", k, raw)
		}
	}
	if doc["version"] != float64(Version) || doc["manifest_sha256"] != testSHA {
		t.Fatalf("header = %+v", doc)
	}
	task := doc["tasks"].(map[string]any)["T-1"].(map[string]any)
	if task["priority"] != "high" {
		t.Fatalf("task = %+v", task)
	}
	note := task["notes"].([]any)[0].(map[string]any)
	for _, k := range []string{"id", "at", "text"} {
		if _, ok := note[k]; !ok {
			t.Fatalf("missing note key %q: %+v", k, note)
		}
	}
	event := doc["events"].([]any)[0].(map[string]any)
	for _, k := range []string{"seq", "at", "task_id", "kind", "value"} {
		if _, ok := event[k]; !ok {
			t.Fatalf("missing event key %q: %+v", k, event)
		}
	}
	cursor := doc["cursors"].(map[string]any)["claude:abc"].(map[string]any)
	for _, k := range []string{"seq", "seen_at"} {
		if _, ok := cursor[k]; !ok {
			t.Fatalf("missing cursor key %q: %+v", k, cursor)
		}
	}
}

func TestFileAndDirectoryModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix file modes")
	}
	dir := filepath.Join(t.TempDir(), "board")
	s, err := OpenAt(dir, testSHA)
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	if _, err := s.SetPriority("T-1", PriorityHigh); err != nil {
		t.Fatalf("SetPriority: %v", err)
	}
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat dir: %v", err)
	}
	if got := di.Mode().Perm(); got != 0o700 {
		t.Fatalf("dir mode = %o, want 700", got)
	}
	fi, err := os.Stat(s.Path())
	if err != nil {
		t.Fatalf("Stat file: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Fatalf("file mode = %o, want 600", got)
	}
	li, err := os.Stat(s.lockPath())
	if err != nil {
		t.Fatalf("Stat lock: %v", err)
	}
	if got := li.Mode().Perm(); got != 0o600 {
		t.Fatalf("lock mode = %o, want 600", got)
	}
}

func TestWriteRefusesOverOneMiB(t *testing.T) {
	s := newStore(t)
	build := func(n int) Overlay {
		o := empty(testSHA)
		o.Tasks["T-1"] = TaskOverlay{Notes: []Note{{ID: "n-1", At: "2026-01-01T00:00:00Z", Text: strings.Repeat("a", n)}}}
		return o
	}
	size := func(n int) int {
		t.Helper()
		b, err := json.Marshal(build(n))
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		return len(b)
	}
	// The marshalled size is linear in n with slope 1, so this lands exactly on
	// the cap: the largest document the store accepts.
	n := MaxFileBytes - size(0)
	if got := size(n); got != MaxFileBytes {
		t.Fatalf("probe = %d bytes, want %d", got, MaxFileBytes)
	}
	if err := s.write(build(n)); err != nil {
		t.Fatalf("write at the cap: %v", err)
	}
	fi, err := os.Stat(s.Path())
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if fi.Size() != MaxFileBytes {
		t.Fatalf("file = %d bytes, want %d", fi.Size(), MaxFileBytes)
	}
	if err := s.write(build(n + 1)); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("write over the cap: %v", err)
	}
}

func TestEditRefusedOverOneMiBKeepsTheOldFile(t *testing.T) {
	s := newStore(t)
	// 2000 runes of three-byte characters: 6 KiB per note, in both the task and
	// the event, so roughly 90 notes reach the cap.
	text := strings.Repeat("漢", MaxNoteRunes)
	var lastRev uint64
	refused := false
	for i := range 400 {
		o, err := s.AddNote(fmt.Sprintf("T-%d", i/MaxNotes), text)
		if errors.Is(err, ErrTooLarge) {
			refused = true
			break
		}
		if err != nil {
			t.Fatalf("note %d: %v", i, err)
		}
		lastRev = o.Revision
	}
	if !refused {
		t.Fatal("the size limit never triggered")
	}
	fi, err := os.Stat(s.Path())
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if fi.Size() > MaxFileBytes {
		t.Fatalf("file = %d bytes, over the cap", fi.Size())
	}
	o, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if o.Revision != lastRev {
		t.Fatalf("revision = %d, want the last accepted %d", o.Revision, lastRev)
	}
}

func TestConcurrentStoresLoseNoUpdate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "board")
	const workers, edits = 8, 25

	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for w := range workers {
		s, err := OpenAt(dir, testSHA)
		if err != nil {
			t.Fatalf("OpenAt: %v", err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range edits {
				p := PriorityHigh
				if i%2 == 1 {
					p = PriorityLow
				}
				if _, err := s.SetPriority(fmt.Sprintf("G%d", w), p); err != nil {
					errs <- fmt.Errorf("worker %d edit %d: %w", w, i, err)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	s, err := OpenAt(dir, testSHA)
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	o, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := uint64(workers * edits)
	if o.Revision != want {
		t.Fatalf("revision = %d, want %d", o.Revision, want)
	}
	if len(o.Events) != int(want) {
		t.Fatalf("events = %d, want %d", len(o.Events), want)
	}
	seen := make(map[uint64]bool, len(o.Events))
	for i, ev := range o.Events {
		if ev.Seq != uint64(i+1) {
			t.Fatalf("event %d has seq %d", i, ev.Seq)
		}
		if seen[ev.Seq] {
			t.Fatalf("duplicate seq %d", ev.Seq)
		}
		seen[ev.Seq] = true
	}
}

func TestConcurrentAddNoteKeepsEveryNote(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "board")
	const workers = 8

	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for w := range workers {
		s, err := OpenAt(dir, testSHA)
		if err != nil {
			t.Fatalf("OpenAt: %v", err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range MaxNotes {
				if _, err := s.AddNote(fmt.Sprintf("W%d", w), fmt.Sprintf("note %d", i)); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	s, err := OpenAt(dir, testSHA)
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	o, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if o.Revision != workers*MaxNotes {
		t.Fatalf("revision = %d, want %d", o.Revision, workers*MaxNotes)
	}
	for w := range workers {
		id := fmt.Sprintf("W%d", w)
		notes := o.Tasks[id].Notes
		if len(notes) != MaxNotes {
			t.Fatalf("%s has %d notes, want %d", id, len(notes), MaxNotes)
		}
		ids := map[string]bool{}
		for i, n := range notes {
			if ids[n.ID] {
				t.Fatalf("%s: duplicate note id %q", id, n.ID)
			}
			ids[n.ID] = true
			if want := fmt.Sprintf("n-%d", i+1); n.ID != want {
				t.Fatalf("%s note %d id = %q, want %q", id, i, n.ID, want)
			}
		}
	}
}

func TestAckAdvancesAndNeverRewinds(t *testing.T) {
	s := newStore(t)
	const c = "claude:ack"
	for _, id := range []string{"T-1", "T-2", "T-3"} {
		if _, err := s.SetPriority(id, PriorityHigh); err != nil {
			t.Fatal(err)
		}
	}
	cursor := func() uint64 {
		t.Helper()
		o, err := s.Load()
		if err != nil {
			t.Fatal(err)
		}
		return o.Cursors[c].Seq
	}
	if err := s.Ack(c, 2); err != nil {
		t.Fatalf("Ack: %v", err)
	}
	if got := cursor(); got != 2 {
		t.Fatalf("cursor = %d, want 2", got)
	}
	evs, _, err := s.Pull(c, true)
	if err != nil || len(evs) != 1 || evs[0].Seq != 3 {
		t.Fatalf("peek after ack = %+v, %v", evs, err)
	}
	if err := s.Ack(c, 1); err != nil {
		t.Fatalf("Ack: %v", err)
	}
	if got := cursor(); got != 2 {
		t.Fatalf("a lower ack rewound the cursor to %d", got)
	}
	// An ack beyond the revision cannot skip events that do not exist yet.
	if err := s.Ack(c, 99); err != nil {
		t.Fatalf("Ack: %v", err)
	}
	if got := cursor(); got != 3 {
		t.Fatalf("cursor = %d, want clamp to the revision 3", got)
	}
	if err := s.Ack("agy:nope", 1); !errors.Is(err, ErrConsumerID) {
		t.Fatalf("Ack with a bad consumer: %v", err)
	}
}

func TestCursorWritesSkipNoOps(t *testing.T) {
	s := newStore(t)
	const c = "claude:noop"
	if _, err := s.SetPriority("T-1", PriorityHigh); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Pull(c, false); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour)
	touch := func() {
		t.Helper()
		if err := os.Chtimes(s.Path(), past, past); err != nil {
			t.Fatal(err)
		}
	}
	modified := func() bool {
		t.Helper()
		fi, err := os.Stat(s.Path())
		if err != nil {
			t.Fatal(err)
		}
		return !fi.ModTime().Equal(past.Truncate(time.Second)) && !fi.ModTime().Equal(past)
	}
	touch()
	if _, _, err := s.Pull(c, false); err != nil {
		t.Fatal(err)
	}
	if err := s.Ack(c, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.Ack(c, 0); err != nil {
		t.Fatal(err)
	}
	if modified() {
		t.Fatal("a no-op pull or ack rewrote the file")
	}

	// A stale seen_at is refreshed, even though the sequence is unchanged.
	o, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	o.Cursors[c] = Cursor{Seq: 1, SeenAt: time.Now().Add(-2 * time.Minute).UTC().Format(time.RFC3339)}
	if err := s.write(o); err != nil {
		t.Fatal(err)
	}
	touch()
	if err := s.Ack(c, 1); err != nil {
		t.Fatal(err)
	}
	if !modified() {
		t.Fatal("a stale cursor was not refreshed")
	}
}

// fillNearCap adds 2000-rune three-byte notes across tasks until the log alone
// would push the document past MaxFileBytes. It returns the number of notes.
func fillNearCap(t *testing.T, s *Store) int {
	t.Helper()
	text := strings.Repeat("漢", MaxNoteRunes)
	n := 0
	// 6 tasks x 20 notes x 6 KiB = 720 KiB of task notes fit on their own, but
	// the same text again in the event log does not.
	for i := range 6 * MaxNotes {
		if _, err := s.AddNote(fmt.Sprintf("T-%d", i/MaxNotes), text); err != nil {
			t.Fatalf("note %d: %v", i, err)
		}
		n++
	}
	return n
}

func TestSizeCapTrimsOldEventsInsteadOfBlockingWrites(t *testing.T) {
	s := newStore(t)
	const c = "claude:big"
	n := fillNearCap(t, s) // events (n x 6 KiB) plus the same text in the tasks
	o, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(o.Events) >= n {
		t.Fatalf("no event was trimmed: %d events for %d notes", len(o.Events), n)
	}
	if o.Revision != uint64(n) || o.Events[len(o.Events)-1].Seq != o.Revision {
		t.Fatalf("revision %d, last event %+v", o.Revision, o.Events[len(o.Events)-1])
	}
	if fi, _ := os.Stat(s.Path()); fi.Size() > MaxFileBytes {
		t.Fatalf("file is %d bytes", fi.Size())
	}

	if _, _, err := s.Pull(c, false); err != nil {
		t.Fatalf("Pull at the cap: %v", err)
	}
	if err := s.Ack(c, o.Revision); err != nil {
		t.Fatalf("Ack at the cap: %v", err)
	}
	if _, err := s.SetPriority("T-0", PriorityHigh); err != nil {
		t.Fatalf("SetPriority at the cap: %v", err)
	}
	if _, err := s.AddNote("T-new", "one more"); err != nil {
		t.Fatalf("AddNote at the cap: %v", err)
	}
	// Other consumers still get an event-log tail, never an error.
	evs, o, err := s.Pull("omp:late", true)
	if err != nil || len(evs) == 0 || len(evs) != len(o.Events) {
		t.Fatalf("late consumer: %d events, %v", len(evs), err)
	}
}

func TestSizeCapRefusesOnlyWhenTheTasksThemselvesDoNotFit(t *testing.T) {
	s := newStore(t)
	text := strings.Repeat("漢", MaxNoteRunes)
	var lastOK Overlay
	refused := false
	for i := range 60 * MaxNotes { // 60 full tasks x 120 KiB = 7 MiB of notes
		o, err := s.AddNote(fmt.Sprintf("T-%d", i/MaxNotes), text)
		if errors.Is(err, ErrTooLarge) {
			refused = true
			break
		}
		if err != nil {
			t.Fatalf("note %d: %v", i, err)
		}
		lastOK = o
	}
	if !refused {
		t.Fatal("notes beyond the cap were accepted")
	}
	// The refused edit left the file intact, and the tasks alone fill most of it.
	got, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision != lastOK.Revision || len(got.Events) < 1 {
		t.Fatalf("revision %d / %d events after a refusal, want %d", got.Revision, len(got.Events), lastOK.Revision)
	}
	notes := 0
	for _, task := range got.Tasks {
		notes += len(task.Notes)
	}
	if notes*3*MaxNoteRunes < MaxFileBytes/2 {
		t.Fatalf("refused with only %d notes stored", notes)
	}
	// Cursor writes still land.
	if _, _, err := s.Pull("claude:x", false); err != nil {
		t.Fatalf("Pull: %v", err)
	}
}

func TestRefusedEditReturnsThePreEditOverlay(t *testing.T) {
	s := newStore(t)
	// A document sitting exactly at the cap with no events: any edit adds an
	// event and a note, which no trimming can make fit.
	o := empty(testSHA)
	o.Tasks["T-1"] = TaskOverlay{Notes: []Note{{ID: "n-1", At: "2026-01-01T00:00:00Z", Text: "a"}}}
	b, err := json.Marshal(o)
	if err != nil {
		t.Fatal(err)
	}
	o.Tasks["T-1"].Notes[0].Text = strings.Repeat("a", MaxFileBytes-len(b)+1)
	if err := s.write(o); err != nil {
		t.Fatalf("seed write: %v", err)
	}
	got, err := s.AddNote("T-2", "one more")
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("AddNote = %v, want ErrTooLarge", err)
	}
	if got.Revision != 0 || len(got.Events) != 0 || len(got.Tasks["T-2"].Notes) != 0 {
		t.Fatalf("refused edit returned a mutated overlay: revision %d, %d events, T-2 %+v", got.Revision, len(got.Events), got.Tasks["T-2"])
	}
	disk, err := s.Load()
	if err != nil || disk.Revision != 0 || len(disk.Events) != 0 {
		t.Fatalf("disk changed: %+v, %v", disk, err)
	}
}

func TestAckAdvanceClaimsEachSequenceOnce(t *testing.T) {
	s := newStore(t)
	if _, err := s.SetPriority("T-1", PriorityHigh); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetPriority("T-2", PriorityHigh); err != nil {
		t.Fatal(err)
	}
	const c = "claude:s1"
	moved, err := s.AckAdvance(c, 2)
	if err != nil || !moved {
		t.Fatalf("first AckAdvance = %v, %v, want true", moved, err)
	}
	if moved, err = s.AckAdvance(c, 2); err != nil || moved {
		t.Fatalf("repeat AckAdvance = %v, %v, want false", moved, err)
	}
	if moved, _ = s.AckAdvance(c, 1); moved {
		t.Fatal("an older seq must not move the cursor")
	}
	if _, err := s.AckAdvance("bad consumer", 1); !errors.Is(err, ErrConsumerID) {
		t.Fatalf("invalid consumer: %v", err)
	}
	if ov, _ := s.Load(); ov.Cursors[c].Seq != 2 {
		t.Fatalf("cursor = %+v", ov.Cursors[c])
	}
}

func TestAckAdvanceIsExclusiveUnderConcurrency(t *testing.T) {
	s := newStore(t)
	if _, err := s.SetPriority("T-1", PriorityHigh); err != nil {
		t.Fatal(err)
	}
	wins, n := 0, 8
	var mu sync.Mutex
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if moved, err := s.AckAdvance("omp:x", 1); err == nil && moved {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("%d callers claimed the same events, want 1", wins)
	}
}

func TestAckAdvanceNextUpCountsAHeadChange(t *testing.T) {
	s := newStore(t)
	if _, err := s.SetPriority("T-1", PriorityHigh); err != nil {
		t.Fatal(err)
	}
	const c = "omp:me"
	if moved, err := s.AckAdvanceNextUp(c, 1, "T-4"); err != nil || !moved {
		t.Fatalf("first = %v, %v", moved, err)
	}
	if moved, _ := s.AckAdvanceNextUp(c, 1, "T-4"); moved {
		t.Fatal("same seq and head must not move")
	}
	if moved, _ := s.AckAdvanceNextUp(c, 1, "T-5"); !moved {
		t.Fatal("a new head at the same seq must move")
	}
	if _, err := s.AckAdvanceNextUp(c, 1, "bad id!"); !errors.Is(err, ErrTaskID) {
		t.Fatalf("bad head: %v", err)
	}
}
