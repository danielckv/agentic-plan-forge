package collab

import (
	"encoding/json"
	"strings"
	"testing"
)

func mustInsert(t *testing.T, docLen int, text string) Update {
	t.Helper()
	raw, err := insertAtJSON(docLen, text)
	if err != nil {
		t.Fatalf("insertAtJSON(%d, %q): %v", docLen, text, err)
	}
	return Update{ClientID: "m1:a", Changes: raw}
}

// coveredLength sums the retain and delete lengths of a ChangeSet JSON array,
// which must equal the length of the document the ChangeSet applies to.
func coveredLength(t *testing.T, raw json.RawMessage) int {
	t.Helper()
	var parts []json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil {
		t.Fatalf("changes %s: %v", raw, err)
	}
	total := 0
	for _, p := range parts {
		var n int
		if err := json.Unmarshal(p, &n); err == nil {
			total += n
			continue
		}
		var arr []json.RawMessage
		if err := json.Unmarshal(p, &arr); err != nil || len(arr) == 0 {
			t.Fatalf("bad part %s in %s", p, raw)
		}
		var del int
		if err := json.Unmarshal(arr[0], &del); err != nil {
			t.Fatalf("bad delete length in %s: %v", p, err)
		}
		total += del
	}
	return total
}

func TestPushTwoUpdateBatchAdvancesVersionByTwo(t *testing.T) {
	a := NewAuthority("")
	batch := []Update{mustInsert(t, 0, "ab"), mustInsert(t, 2, "c")}

	res, rej := a.Push("m1", 0, batch)
	if rej != nil {
		t.Fatalf("push rejected: %+v", rej)
	}
	if res.NewVersion != 2 {
		t.Fatalf("NewVersion = %d, want 2", res.NewVersion)
	}
	if len(res.Applied) != 2 {
		t.Fatalf("Applied = %d updates, want 2", len(res.Applied))
	}
	snap := a.Snapshot()
	if snap.Version != 2 || snap.Text != "abc" {
		t.Fatalf("snapshot = (%d, %q), want (2, %q)", snap.Version, snap.Text, "abc")
	}
	if got := len(a.UpdatesSince(0)); got != 2 {
		t.Fatalf("UpdatesSince(0) = %d, want 2", got)
	}
	if got := len(a.UpdatesSince(1)); got != 1 {
		t.Fatalf("UpdatesSince(1) = %d, want 1", got)
	}

	// The next push at the new head advances by exactly one more.
	res, rej = a.Push("m1", 2, []Update{mustInsert(t, 3, "d")})
	if rej != nil {
		t.Fatalf("second push rejected: %+v", rej)
	}
	if res.NewVersion != 3 {
		t.Fatalf("NewVersion = %d, want 3", res.NewVersion)
	}
}

func TestAppendNonEmptyDocumentRetainsExistingPrefix(t *testing.T) {
	raw, err := insertAtJSON(5, "x\ny")
	if err != nil {
		t.Fatal(err)
	}
	if want := `[5,[0,"x","y"]]`; string(raw) != want {
		t.Fatalf("changes = %s, want %s", raw, want)
	}
	if got := coveredLength(t, raw); got != 5 {
		t.Fatalf("retain+delete = %d, want 5", got)
	}
	doc := encodeUTF16("hello")
	out, rej := applyChangeSet(doc, raw)
	if rej != nil {
		t.Fatalf("applyChangeSet rejected append: %+v", rej)
	}
	if got := decodeUTF16(out); got != "hellox\ny" {
		t.Fatalf("applied = %q, want %q", got, "hellox\ny")
	}

	// End to end: the logged update must replay against the previous text.
	a := NewAuthority("hello")
	res, rej := a.Append("sys:ingest:1", "x\ny")
	if rej != nil {
		t.Fatalf("Append rejected: %+v", rej)
	}
	if res.NewVersion != 1 {
		t.Fatalf("NewVersion = %d, want 1", res.NewVersion)
	}
	logged := a.UpdatesSince(0)
	if len(logged) != 1 {
		t.Fatalf("logged updates = %d, want 1", len(logged))
	}
	replayed, rej := applyChangeSet(encodeUTF16("hello"), logged[0].Changes)
	if rej != nil {
		t.Fatalf("logged append does not apply to a non-empty document: %+v", rej)
	}
	if got := decodeUTF16(replayed); got != a.Snapshot().Text {
		t.Fatalf("replayed = %q, head = %q", got, a.Snapshot().Text)
	}
}

func TestAppendEmptyDocumentUsesEmptyDocumentShape(t *testing.T) {
	raw, err := insertAtJSON(0, "abc")
	if err != nil {
		t.Fatal(err)
	}
	if want := `[[0,"abc"]]`; string(raw) != want {
		t.Fatalf("changes = %s, want %s", raw, want)
	}
	if got := coveredLength(t, raw); got != 0 {
		t.Fatalf("retain+delete = %d, want 0", got)
	}
	out, rej := applyChangeSet(nil, raw)
	if rej != nil {
		t.Fatalf("applyChangeSet rejected: %+v", rej)
	}
	if got := decodeUTF16(out); got != "abc" {
		t.Fatalf("applied = %q, want %q", got, "abc")
	}
}

// A terminal client appends to the PRD through the same Push path the browser
// uses. This pins the two things that made the first attempt fail: the change
// must be the canonical append encoding, and the client id must start with the
// pushing member's id followed by a colon.
func TestTerminalAppendIsAcceptedByPush(t *testing.T) {
	a := NewAuthority("Existing requirement.\n")
	const member = "owner"
	changes, err := AppendChange(22, "Add MFA.")
	if err != nil {
		t.Fatal(err)
	}
	updates := []Update{{ClientID: member + ":terminal", Changes: changes}}
	res, rej := a.Push(member, 0, updates)
	if rej != nil {
		t.Fatalf("the terminal's append was rejected: %+v", rej)
	}
	if res.NewVersion != 1 {
		t.Fatalf("NewVersion = %d, want 1", res.NewVersion)
	}
	if want := "Existing requirement.\nAdd MFA."; a.Snapshot().Text != want {
		t.Fatalf("document = %q, want %q", a.Snapshot().Text, want)
	}

	// A client id that does not start with the member id is refused, which is
	// what the terminal sent before the prefix was corrected.
	if _, rej := a.Push(member, 1, []Update{{ClientID: "terminal:owner", Changes: changes}}); rej == nil {
		t.Fatal("a foreign client id was accepted")
	}
}

func TestPushStaleBaseVersionRejectedWithHead(t *testing.T) {
	a := NewAuthority("")
	if _, rej := a.Push("m1", 0, []Update{mustInsert(t, 0, "ab"), mustInsert(t, 2, "c")}); rej != nil {
		t.Fatalf("setup push rejected: %+v", rej)
	}
	other := mustInsert(t, 0, "z")
	other.ClientID = "m2:a"
	_, rej := a.Push("m2", 0, []Update{other})
	if rej == nil {
		t.Fatal("stale push accepted")
	}
	if rej.Reason != rejectReasonStale {
		t.Fatalf("reason = %q, want %q", rej.Reason, rejectReasonStale)
	}
	if rej.HeadVersion != 2 {
		t.Fatalf("HeadVersion = %d, want 2", rej.HeadVersion)
	}
	if len(rej.Updates) != 2 {
		t.Fatalf("missed updates = %d, want 2", len(rej.Updates))
	}
	if got := a.Snapshot().Text; got != "abc" {
		t.Fatalf("document changed by rejected push: %q", got)
	}
}

func TestPushOversizeDocumentRejectedAllOrNothing(t *testing.T) {
	chunk := strings.Repeat("a", 200<<10)
	a := NewAuthority("")
	batch := []Update{
		mustInsert(t, 0, chunk),
		mustInsert(t, len(chunk), chunk),
		mustInsert(t, 2*len(chunk), chunk),
	}
	_, rej := a.Push("m1", 0, batch)
	if rej == nil {
		t.Fatal("oversize document accepted")
	}
	if rej.Reason != rejectReasonLarge {
		t.Fatalf("reason = %q, want %q", rej.Reason, rejectReasonLarge)
	}
	if rej.HeadVersion != 0 {
		t.Fatalf("HeadVersion = %d, want 0", rej.HeadVersion)
	}
	snap := a.Snapshot()
	if snap.Version != 0 || snap.Text != "" {
		t.Fatalf("rejected batch changed state: version %d, %d bytes", snap.Version, len(snap.Text))
	}
}

func TestPushBatchBeyondLogBoundCompactsWithoutDeadlock(t *testing.T) {
	a := NewAuthority("")
	n := logMaxEntries + 1
	batch := make([]Update, n)
	for i := range batch {
		batch[i] = mustInsert(t, i, "a")
	}
	res, rej := a.Push("m1", 0, batch)
	if rej != nil {
		t.Fatalf("push rejected: %+v", rej)
	}
	if res.NewVersion != uint64(n) {
		t.Fatalf("NewVersion = %d, want %d", res.NewVersion, n)
	}
	if got := a.UpdatesSince(0); got != nil {
		t.Fatalf("UpdatesSince(0) returned %d updates after compaction, want resync (nil)", len(got))
	}
	if got := len(a.UpdatesSince(uint64(n) - 1)); got != 1 {
		t.Fatalf("UpdatesSince(head-1) = %d, want 1", got)
	}
}
