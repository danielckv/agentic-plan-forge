//go:build client && !server

package board

// Contract C9: harness activity per task and discoveries (work the harness found
// outside the plan). Both live in the same overlay document as the priorities
// and notes, so every edit below runs under the store lock, lands through the
// same atomic rename and is size-trimmed by the same writeTrim.

import (
	"errors"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/sanitize"
)

// Limits from contract C9.
const (
	// MaxDiscoveries is the number of discoveries the overlay holds.
	MaxDiscoveries = 200
	// MaxDiscoveryTitleRunes is the longest discovery title.
	MaxDiscoveryTitleRunes = 120
	// MaxDiscoveryReasonRunes is the longest discovery reason.
	MaxDiscoveryReasonRunes = 500
	// MaxDiscoveryPaths is the number of paths one task discovery may carry.
	MaxDiscoveryPaths = 10
	// MaxPathBytes is the longest repository path the store records.
	MaxPathBytes = 512
	// MaxActivityDetailRunes is the longest activity detail.
	MaxActivityDetailRunes = 200
	// MaxActivity is the number of tasks whose activity is retained; beyond it
	// the stalest record is dropped.
	MaxActivity = 500
	// ActivityRefresh is how long an unchanged activity record counts as
	// current: recording the same state and source again within it is a no-op.
	ActivityRefresh = 30 * time.Second
)

// Event kinds added by schema v2. Each carries the author in Event.By. For the
// three discovery kinds TaskID is the discovery id (D-<n>).
const (
	// KindActivity has the activity state as its value.
	KindActivity EventKind = "activity"
	// KindDiscovered has the discovery id as its value.
	KindDiscovered EventKind = "discovered"
	// KindDiscoveryAccepted has "D-<n>:<priority>" as its value.
	KindDiscoveryAccepted EventKind = "discovery_accepted"
	// KindDiscoveryDismissed has the discovery id as its value.
	KindDiscoveryDismissed EventKind = "discovery_dismissed"
)

// Errors added by schema v2.
var (
	// ErrActivityState reports an activity state other than active, done or blocked.
	ErrActivityState = errors.New("board: invalid activity state")
	// ErrActivitySource reports an activity source other than file_edit, report or verify.
	ErrActivitySource = errors.New("board: invalid activity source")
	// ErrDiscoveryKind reports a discovery kind other than task or path.
	ErrDiscoveryKind = errors.New("board: invalid discovery kind")
	// ErrDiscoveryTitle reports a task discovery without a title.
	ErrDiscoveryTitle = errors.New("board: empty discovery title")
	// ErrDiscoveryPath reports an invalid, forbidden or missing discovery path.
	ErrDiscoveryPath = errors.New("board: invalid discovery path")
	// ErrDiscoveryLimit reports a store whose MaxDiscoveries discoveries are all proposed.
	ErrDiscoveryLimit = errors.New("board: discovery limit reached; resolve a proposed discovery first")
	// ErrUnknownDiscovery reports a discovery id the overlay does not hold.
	ErrUnknownDiscovery = errors.New("board: unknown discovery")
)

// ActivityState is what the harness is doing with a task.
type ActivityState string

// The three activity states.
const (
	ActivityActive  ActivityState = "active"
	ActivityDone    ActivityState = "done"
	ActivityBlocked ActivityState = "blocked"
)

func (s ActivityState) valid() bool {
	return s == ActivityActive || s == ActivityDone || s == ActivityBlocked
}

// The three activity sources: an edit to a task's file, an explicit
// `board report`, and a verification run.
const (
	SourceFileEdit = "file_edit"
	SourceReport   = "report"
	SourceVerify   = "verify"
)

func validSource(s string) bool {
	return s == SourceFileEdit || s == SourceReport || s == SourceVerify
}

// Activity is the latest harness activity on one task.
type Activity struct {
	State  ActivityState `json:"state"`
	By     string        `json:"by"`
	Source string        `json:"source"`
	Detail string        `json:"detail"`
	At     string        `json:"at"`
}

// recent reports whether the record is younger than d.
func (a Activity) recent(d time.Duration) bool {
	t, err := time.Parse(time.RFC3339, a.At)
	if err != nil {
		return false
	}
	age := time.Since(t)
	return age >= 0 && age < d
}

// DiscoveryKind is the kind of a discovery.
type DiscoveryKind string

// The two discovery kinds: work not in the plan, and a repository path touched
// outside every planned target.
const (
	DiscoveryTask DiscoveryKind = "task"
	DiscoveryPath DiscoveryKind = "path"
)

// DiscoveryStatus is where a discovery stands.
type DiscoveryStatus string

// The three statuses. Discoveries enter as proposed; only the user resolves
// them.
const (
	DiscoveryProposed  DiscoveryStatus = "proposed"
	DiscoveryAccepted  DiscoveryStatus = "accepted"
	DiscoveryDismissed DiscoveryStatus = "dismissed"
)

// Discovery is one harness finding. Paths is never nil. Priority is the
// priority the user gave on accepting it (normal until then).
type Discovery struct {
	ID           string          `json:"id"`
	Kind         DiscoveryKind   `json:"kind"`
	Title        string          `json:"title"`
	Paths        []string        `json:"paths"`
	ParentTaskID string          `json:"parent_task_id"`
	Reason       string          `json:"reason"`
	By           string          `json:"by"`
	At           string          `json:"at"`
	Status       DiscoveryStatus `json:"status"`
	Priority     Priority        `json:"priority"`
}

// DiscoveryInput is what a consumer reports. A path discovery takes exactly one
// path and defaults its title to that path; a task discovery takes a title and
// up to MaxDiscoveryPaths optional path hints.
type DiscoveryInput struct {
	Kind         DiscoveryKind
	Title        string
	Paths        []string
	ParentTaskID string
	Reason       string
}

// validBy reports whether by may author an event: empty, "user" or a consumer.
func validBy(by string) bool {
	return by == "" || by == "user" || ValidConsumer(by)
}

// RecordActivity records that consumer is doing state to taskID. The latest
// record per task wins. detail is sanitized and cut to MaxActivityDetailRunes.
// When the task's record already holds the same state and source and is less
// than ActivityRefresh old, nothing is written: no event, no revision bump.
func (s *Store) RecordActivity(consumer, taskID string, state ActivityState, source, detail string) (Overlay, error) {
	if !ValidConsumer(consumer) {
		return Overlay{}, ErrConsumerID
	}
	if !ValidTaskID(taskID) {
		return Overlay{}, ErrTaskID
	}
	if !state.valid() {
		return Overlay{}, ErrActivityState
	}
	if !validSource(source) {
		return Overlay{}, ErrActivitySource
	}
	detail = sanitize.Truncate(sanitize.Terminal(detail), MaxActivityDetailRunes)
	return s.update(func(o *Overlay) (bool, error) {
		prev, had := o.Activity[taskID]
		if had && prev.State == state && prev.Source == source && prev.recent(ActivityRefresh) {
			return false, nil
		}
		if !had && len(o.Activity) >= MaxActivity {
			o.evictActivity()
		}
		o.Activity[taskID] = Activity{State: state, By: consumer, Source: source, Detail: detail, At: now()}
		o.appendEventBy(taskID, KindActivity, string(state), consumer)
		return true, nil
	})
}

// evictActivity drops the stalest activity record; equal times drop the
// smaller task id, so the result is deterministic.
func (o *Overlay) evictActivity() {
	victim, at := "", ""
	for id, a := range o.Activity {
		if victim == "" || a.At < at || (a.At == at && id < victim) {
			victim, at = id, a.At
		}
	}
	if victim != "" {
		delete(o.Activity, victim)
	}
}

// forbiddenPrefixes are never recorded as discovery paths: repository
// internals and the plan's own state.
var forbiddenPrefixes = []string{".git", "docs/plans/.plan"}

// cleanRelPath normalizes p into a clean, repo-relative, slash-separated path.
// It refuses an empty, over-long or non-UTF-8 path, control characters,
// backslashes, absolute paths (including drive letters), any ".." segment, the
// root itself and anything under .git/ or docs/plans/.plan/.
func cleanRelPath(p string) (string, bool) {
	if p == "" || len(p) > MaxPathBytes || !utf8.ValidString(p) || sanitize.Terminal(p) != p {
		return "", false
	}
	if strings.ContainsRune(p, '\\') || strings.HasPrefix(p, "/") {
		return "", false
	}
	if len(p) >= 2 && p[1] == ':' {
		return "", false // Windows drive letter
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return "", false
		}
	}
	c := path.Clean(p)
	if c == "." || c == "" {
		return "", false
	}
	lower := strings.ToLower(c)
	for _, f := range forbiddenPrefixes {
		if lower == f || strings.HasPrefix(lower, f+"/") {
			return "", false
		}
	}
	return c, true
}

// discoveryNum returns n for an id of the form D-<n>, n >= 1.
func discoveryNum(id string) (uint64, bool) {
	if !strings.HasPrefix(id, "D-") {
		return 0, false
	}
	n, err := strconv.ParseUint(id[2:], 10, 64)
	if err != nil || n == 0 || strconv.FormatUint(n, 10) != id[2:] {
		return 0, false
	}
	return n, true
}

// discoveryOlder orders by time, then by id number.
func discoveryOlder(a, b Discovery) bool {
	if a.At != b.At {
		return a.At < b.At
	}
	na, _ := discoveryNum(a.ID)
	nb, _ := discoveryNum(b.ID)
	return na < nb
}

func (o *Overlay) discoveryIndex(id string) int {
	for i := range o.Discoveries {
		if o.Discoveries[i].ID == id {
			return i
		}
	}
	return -1
}

// nextDiscoveryID returns the next D-<n>. It never reuses an id: the counter
// survives evictions.
func (o *Overlay) nextDiscoveryID() string {
	top := o.DiscoverySeq
	for _, d := range o.Discoveries {
		if n, ok := discoveryNum(d.ID); ok && n > top {
			top = n
		}
	}
	top++
	o.DiscoverySeq = top
	return "D-" + strconv.FormatUint(top, 10)
}

// evictDiscovery makes room for one more discovery. It drops the oldest
// dismissed one, else the oldest accepted one, and never a proposed one. It
// reports false when every discovery is proposed.
func (o *Overlay) evictDiscovery() bool {
	for _, status := range []DiscoveryStatus{DiscoveryDismissed, DiscoveryAccepted} {
		victim := -1
		for i := range o.Discoveries {
			if o.Discoveries[i].Status != status {
				continue
			}
			if victim < 0 || discoveryOlder(o.Discoveries[i], o.Discoveries[victim]) {
				victim = i
			}
		}
		if victim >= 0 {
			o.Discoveries = append(o.Discoveries[:victim:victim], o.Discoveries[victim+1:]...)
			return true
		}
	}
	return false
}

// syncDiscoveryPriority mirrors a user priority edit of an accepted task
// discovery onto the discovery itself.
func (o *Overlay) syncDiscoveryPriority(taskID string, p Priority) {
	if i := o.discoveryIndex(taskID); i >= 0 && o.Discoveries[i].Kind == DiscoveryTask && o.Discoveries[i].Status == DiscoveryAccepted {
		o.Discoveries[i].Priority = p
	}
}

// AddDiscovery records a harness finding as proposed and logs a discovered
// event authored by consumer. Title and reason are sanitized and cut to 120 and
// 500 runes. A path discovery for a path the store already holds, in any
// status, is not duplicated: only its time is refreshed, and the existing
// discovery is returned without an event. At most MaxDiscoveries are kept;
// when full the oldest dismissed, then the oldest accepted, is dropped, and a
// store whose discoveries are all proposed returns ErrDiscoveryLimit.
func (s *Store) AddDiscovery(consumer string, d DiscoveryInput) (Discovery, Overlay, error) {
	if !ValidConsumer(consumer) {
		return Discovery{}, Overlay{}, ErrConsumerID
	}
	if d.Kind != DiscoveryTask && d.Kind != DiscoveryPath {
		return Discovery{}, Overlay{}, ErrDiscoveryKind
	}
	if d.ParentTaskID != "" && !ValidTaskID(d.ParentTaskID) {
		return Discovery{}, Overlay{}, ErrTaskID
	}
	paths := make([]string, 0, len(d.Paths))
	seen := map[string]bool{}
	for _, p := range d.Paths {
		c, ok := cleanRelPath(p)
		if !ok {
			return Discovery{}, Overlay{}, ErrDiscoveryPath
		}
		if !seen[c] {
			seen[c] = true
			paths = append(paths, c)
		}
	}
	if len(paths) > MaxDiscoveryPaths {
		return Discovery{}, Overlay{}, ErrDiscoveryPath
	}
	title := sanitize.Truncate(sanitize.Terminal(d.Title), MaxDiscoveryTitleRunes)
	switch d.Kind {
	case DiscoveryPath:
		if len(paths) != 1 {
			return Discovery{}, Overlay{}, ErrDiscoveryPath
		}
		if title == "" {
			title = sanitize.Truncate(paths[0], MaxDiscoveryTitleRunes)
		}
	case DiscoveryTask:
		if title == "" {
			return Discovery{}, Overlay{}, ErrDiscoveryTitle
		}
	}
	reason := sanitize.Truncate(sanitize.Terminal(d.Reason), MaxDiscoveryReasonRunes)

	var out Discovery
	ov, err := s.update(func(o *Overlay) (bool, error) {
		if d.Kind == DiscoveryPath {
			for i := range o.Discoveries {
				ex := &o.Discoveries[i]
				if ex.Kind == DiscoveryPath && len(ex.Paths) == 1 && ex.Paths[0] == paths[0] {
					changed := false
					if t := now(); ex.At != t {
						ex.At, changed = t, true
					}
					out = *ex
					return changed, nil
				}
			}
		}
		if len(o.Discoveries) >= MaxDiscoveries && !o.evictDiscovery() {
			return false, ErrDiscoveryLimit
		}
		out = Discovery{
			ID:           o.nextDiscoveryID(),
			Kind:         d.Kind,
			Title:        title,
			Paths:        paths,
			ParentTaskID: d.ParentTaskID,
			Reason:       reason,
			By:           consumer,
			At:           now(),
			Status:       DiscoveryProposed,
			Priority:     PriorityNormal,
		}
		o.Discoveries = append(o.Discoveries, out)
		o.appendEventBy(out.ID, KindDiscovered, out.ID, consumer)
		return true, nil
	})
	if err != nil {
		return Discovery{}, ov, err
	}
	return out, ov, nil
}

// ResolveDiscovery accepts or dismisses the discovery id on the user's behalf
// and logs a discovery_accepted or discovery_dismissed event authored by
// "user". Accepting needs a priority (empty means normal); accepting a task
// discovery also stores that priority for its board task. Resolving a
// discovery to the state it already has — for an accept, the same priority
// too — is a no-op. An id the overlay does not hold, malformed or not, returns
// ErrUnknownDiscovery.
func (s *Store) ResolveDiscovery(id string, accept bool, p Priority) (Overlay, error) {
	if _, ok := discoveryNum(id); !ok {
		return Overlay{}, ErrUnknownDiscovery
	}
	if accept {
		if p == "" {
			p = PriorityNormal
		}
		if _, ok := ParsePriority(string(p)); !ok {
			return Overlay{}, ErrPriority
		}
	}
	return s.update(func(o *Overlay) (bool, error) {
		i := o.discoveryIndex(id)
		if i < 0 {
			return false, ErrUnknownDiscovery
		}
		d := &o.Discoveries[i]
		if !accept {
			if d.Status == DiscoveryDismissed {
				return false, nil
			}
			d.Status = DiscoveryDismissed
			o.appendEventBy(id, KindDiscoveryDismissed, id, "user")
			return true, nil
		}
		if d.Status == DiscoveryAccepted && d.Priority == p {
			return false, nil
		}
		d.Status, d.Priority = DiscoveryAccepted, p
		if d.Kind == DiscoveryTask {
			t := o.Tasks[id]
			if p == PriorityNormal {
				t.Priority = ""
			} else {
				t.Priority = p
			}
			o.setTask(id, t)
		}
		o.appendEventBy(id, KindDiscoveryAccepted, id+":"+string(p), "user")
		return true, nil
	})
}

// MapPaths matches repository-relative paths against the task targets of m. A
// path maps to a task when it equals one of that task's target paths, or sits
// under one that is a directory prefix of it; it may map to several tasks. A
// path that maps to no task is unplanned. Paths are normalized the way
// discovery paths are; invalid ones (absolute, with "..", under .git/ or the
// plan state, over-long, …) are dropped. Both results are deduplicated and
// sorted, and byTask only holds tasks that matched; neither result is nil.
func MapPaths(m contracts.PlanManifest, paths []string) (byTask map[string][]string, unplanned []string) {
	byTask = map[string][]string{}
	unplanned = []string{}
	targets := make(map[string][]string, len(m.TaskDAG))
	for _, t := range m.TaskDAG {
		for _, tg := range t.Targets {
			c := path.Clean(strings.TrimSpace(tg.Path))
			if c == "." || c == "" || strings.HasPrefix(c, "/") || c == ".." || strings.HasPrefix(c, "../") {
				continue
			}
			targets[t.ID] = append(targets[t.ID], c)
		}
	}
	taskIDs := make([]string, 0, len(targets))
	for id := range targets {
		taskIDs = append(taskIDs, id)
	}
	sort.Strings(taskIDs)

	seen := map[string]bool{}
	for _, raw := range paths {
		p, ok := cleanRelPath(raw)
		if !ok || seen[p] {
			continue
		}
		seen[p] = true
		matched := false
		for _, id := range taskIDs {
			for _, tp := range targets[id] {
				if p == tp || strings.HasPrefix(p, tp+"/") {
					byTask[id] = append(byTask[id], p)
					matched = true
					break
				}
			}
		}
		if !matched {
			unplanned = append(unplanned, p)
		}
	}
	for id := range byTask {
		sort.Strings(byTask[id])
	}
	sort.Strings(unplanned)
	return byTask, unplanned
}
