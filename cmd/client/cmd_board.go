//go:build client && !server

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/danielckv/agentic-plan-forge/internal/app"
	"github.com/danielckv/agentic-plan-forge/internal/board"
	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/planstate"
	"github.com/danielckv/agentic-plan-forge/internal/sanitize"
)

func init() {
	register("board", "open the plan board window (board pull: print board edits for an agent)", runBoard)
	register("__board-child", "hidden: detached board window process", runBoardChild)
}

// boardPullBound is the total time `board pull` may take: it runs inside host
// hooks, so it must never hold the agent up.
const boardPullBound = 2 * time.Second

// printBoardMessage writes a board message to stderr, adding the "board: "
// prefix unless the message already carries it.
func printBoardMessage(msg string) {
	if msg == "" {
		return
	}
	if !strings.HasPrefix(msg, "board") {
		msg = "board: " + msg
	}
	fmt.Fprintln(os.Stderr, msg)
}

// runBoard implements `plan-forge board [--repo PATH] [--host claude|omp]
// [--detach]` and dispatches `board pull`.
func runBoard(ctx context.Context, args []string) int {
	if len(args) > 0 {
		switch args[0] {
		case "pull":
			return runBoardPull(ctx, args[1:])
		case "report":
			return runBoardReport(ctx, args[1:])
		case "discover":
			return runBoardDiscover(ctx, args[1:])
		case "activity":
			return runBoardActivity(ctx, args[1:])
		}
	}
	var (
		repoFlag string
		hostFlag string
		detach   bool
	)
	fs, err := parseFlags("board", args, func(fs *flag.FlagSet) {
		fs.StringVar(&repoFlag, "repo", "", "repository path (default: cwd's repository root)")
		fs.StringVar(&hostFlag, "host", "", "invoking host kind (claude, omp)")
		fs.BoolVar(&detach, "detach", false, "open in a background process and print {\"board\":\"open\",\"pid\":N}")
	})
	if err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "board takes no positional arguments (did you mean `board pull`?)")
		return exitUsage
	}
	host := strings.ToLower(strings.TrimSpace(hostFlag))
	if !app.ValidBoardHost(host) {
		fmt.Fprintln(os.Stderr, app.BoardMessageHost)
		return exitUsage
	}
	cfg, err := loadConfig()
	if err != nil {
		return fail(err)
	}
	opts := app.BoardOptions{Repo: repoFlag, Host: host, Config: cfg, Stdout: os.Stdout, Stderr: os.Stderr}
	if detach {
		res := app.StartBoardDetached(ctx, opts)
		if res.ExitCode != exitOK {
			printBoardMessage(res.Message)
			return res.ExitCode
		}
		state := "open"
		if res.AlreadyOpen {
			state = "already_open"
		}
		out, _ := json.Marshal(struct {
			Board string `json:"board"`
			PID   int    `json:"pid"`
		}{Board: state, PID: res.PID})
		fmt.Println(string(out))
		return exitOK
	}
	res := app.RunBoard(ctx, opts)
	printBoardMessage(res.Message)
	return res.ExitCode
}

func runBoardChild(ctx context.Context, args []string) int {
	var (
		repoFlag string
		hostFlag string
	)
	fs, err := parseFlags("__board-child", args, func(fs *flag.FlagSet) {
		fs.StringVar(&repoFlag, "repo", "", "repository path")
		fs.StringVar(&hostFlag, "host", "", "invoking host kind")
	})
	if err != nil || fs.NArg() != 0 {
		return exitUsage
	}
	host := strings.ToLower(strings.TrimSpace(hostFlag))
	if !app.ValidBoardHost(host) {
		return exitUsage
	}
	cfg, err := loadConfig()
	if err != nil {
		return fail(err)
	}
	// The parent redirected stdout and stderr to the board log.
	res := app.RunBoard(ctx, app.BoardOptions{
		Repo: repoFlag, Host: host, Config: cfg, Detached: true,
		Logger: slog.New(slog.NewTextHandler(os.Stderr, nil)),
	})
	printBoardMessage(res.Message)
	return res.ExitCode
}

// runBoardPull implements `plan-forge board pull --repo PATH --consumer ID
// [--format text|json] [--peek]`. It is hook-safe: apart from a usage error
// (an invalid consumer id or format) it exits 0 and prints nothing whenever it
// has nothing to report, including every failure.
func runBoardPull(ctx context.Context, args []string) int {
	var (
		repoFlag, consumer, format string
		peek                       bool
	)
	fs, err := parseFlags("board pull", args, func(fs *flag.FlagSet) {
		fs.StringVar(&repoFlag, "repo", "", "repository path (default: cwd)")
		fs.StringVar(&consumer, "consumer", "", "consumer id, claude:<session> or omp:<session>")
		fs.StringVar(&format, "format", "text", "output format: text|json")
		fs.BoolVar(&peek, "peek", false, "do not advance the consumer's cursor")
	})
	if err != nil || fs.NArg() != 0 {
		return exitUsage
	}
	if !board.ValidConsumer(consumer) {
		fmt.Fprintln(os.Stderr, "board pull: --consumer must match ^(claude|omp):[A-Za-z0-9._-]{1,128}$")
		return exitUsage
	}
	if format != "text" && format != "json" {
		fmt.Fprintln(os.Stderr, "board pull: --format must be text or json")
		return exitUsage
	}
	repo, code := resolveRepoArg(repoFlag)
	if code != exitOK {
		return exitOK
	}
	return boardPull(ctx, os.Stdout, repo, consumer, format, peek, boardPullBound)
}

// boardPull prints the events after consumer's cursor to w within bound. It
// always returns exitOK; anything that goes wrong prints nothing.
//
// Delivery is at-least-once: the delta is peeked, written to w, and only then
// is the cursor advanced, within what is left of bound. A failed write, an
// expired bound or a cancelled context leave the events to be delivered
// again; an ack that outlives the bound is abandoned and, being
// context-checked under the store lock, never advances the cursor later. The
// cursor advances even when the delta renders to nothing (events of an
// unknown kind, events every filter removes), or the same events would be
// peeked forever. With peek set the cursor is never advanced.
func boardPull(ctx context.Context, w io.Writer, repo, consumer, format string, peek bool, bound time.Duration) int {
	ctx, cancel := context.WithTimeout(ctx, bound)
	defer cancel()
	out, ack := boardDelta(ctx, repo, consumer, format, bound)
	if out != "" {
		if _, err := fmt.Fprintln(w, out); err != nil {
			return exitOK
		}
	}
	if !peek && ctx.Err() == nil {
		ackWithin(ctx, remaining(ctx), ack)
	}
	return exitOK
}

// remaining is the time left until ctx's deadline, or an hour without one.
func remaining(ctx context.Context) time.Duration {
	if dl, ok := ctx.Deadline(); ok {
		return time.Until(dl)
	}
	return time.Hour
}

// boardAck advances a consumer's cursor. It reports true only when this call
// moved the cursor; false covers a failure, an expired ctx and a cursor that
// another process already moved.
type boardAck func(ctx context.Context) bool

// ackWithin runs ack for at most bound. finished is false when bound or ctx
// ran out first: the ack is abandoned, its ctx expires, and the store then
// refuses to advance the cursor, so the events stay pullable. moved reports
// whether the cursor advanced.
func ackWithin(ctx context.Context, bound time.Duration, ack boardAck) (moved, finished bool) {
	if bound <= 0 || ctx.Err() != nil {
		return false, false
	}
	actx, cancel := context.WithTimeout(ctx, bound)
	defer cancel()
	done := make(chan bool, 1)
	go func() { done <- ack(actx) }()
	select {
	case moved = <-done:
		return moved, true
	case <-actx.Done():
		return false, false
	}
}

// boardDelta peeks at the events after consumer's cursor and renders them as
// the text or JSON delta. It returns "" with a no-op ack when there is nothing
// to say, anything fails, or bound or ctx expires first. The returned ack
// advances the cursor to the last peeked event; the caller invokes it once the
// delta has actually been delivered. Events added after the peek are not
// covered by the ack. out may be "" with a live ack: the peek found events
// that render to nothing, and the ack still has to consume them.
func boardDelta(ctx context.Context, repo, consumer, format string, bound time.Duration) (out string, ack boardAck) {
	noop := func(context.Context) bool { return false }
	ctx, cancel := context.WithTimeout(ctx, bound)
	defer cancel()
	type result struct {
		out string
		ack boardAck
	}
	done := make(chan result, 1)
	go func() {
		out, ack := renderBoardDelta(repo, consumer, format)
		done <- result{out, ack}
	}()
	select {
	case r := <-done:
		if ctx.Err() != nil {
			return "", noop
		}
		return r.out, r.ack
	case <-ctx.Done():
		return "", noop
	}
}

// maxPullEvents is the most events the JSON pull returns; older ones are
// dropped and reported through "truncated".
const maxPullEvents = 50

// truncateRunes cuts s to at most max runes, ending in "…" when it cut.
func truncateRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max-1]) + "…"
}

// pullEvents bounds the events of a JSON pull the way the text delta bounds
// its lines: every text field passes sanitize.Terminal, values are cut to
// board.MaxDeltaNoteRunes and ids to board.MaxDeltaTitleRunes, and only the
// newest maxPullEvents are kept, oldest first. It reports whether it dropped
// any.
func pullEvents(events []board.Event) ([]board.Event, bool) {
	truncated := len(events) > maxPullEvents
	if truncated {
		events = events[len(events)-maxPullEvents:]
	}
	out := make([]board.Event, 0, len(events))
	for _, ev := range events {
		ev.TaskID = truncateRunes(sanitize.Terminal(ev.TaskID), board.MaxDeltaTitleRunes)
		ev.Kind = board.EventKind(sanitize.Terminal(string(ev.Kind)))
		ev.Value = truncateRunes(sanitize.Terminal(ev.Value), board.MaxDeltaNoteRunes)
		ev.By = truncateRunes(sanitize.Terminal(ev.By), board.MaxDeltaTitleRunes)
		out = append(out, ev)
	}
	return out, truncated
}

// renderBoardDelta does the work of boardDelta without a deadline. Its Pull is
// a peek, so abandoning it midway changes nothing on disk.
//
// The text is board.RenderDeltaFor for the pulling consumer: the events it
// wrote itself are skipped, discoveries are named by their own titles, and a
// "Coordinator:" line closes the delta when the head of the "Up next" queue
// moved since the consumer's previous delivery. A consumer with no cursor yet
// has no previous delivery, so the head it first sees is its baseline and is
// not announced. The ack stores the delivered head with the cursor.
//
// The JSON delta is bounded like the text: see pullEvents.
//
// The queue is computed from the manifest and the overlay alone: the hook
// path has a 2 s budget and the AST scan behind the structural evidence does
// not fit it, so a task only the scan knows to be done still counts as open
// until the harness reports it done.
func renderBoardDelta(repo, consumer, format string) (string, boardAck) {
	noop := func(context.Context) bool { return false }
	snap, err := planstate.Inspect(repo)
	if err != nil || snap.ManifestSHA256 == "" || !snap.Approved {
		return "", noop
	}
	store, err := board.Open(snap.ManifestSHA256)
	if err != nil {
		return "", noop
	}
	events, ov, err := store.Pull(consumer, true)
	if err != nil {
		return "", noop
	}
	titles, order := app.BoardTitles(snap.Manifest, ov)
	high := []string{}
	for _, id := range order {
		if ov.Priority(id) == board.PriorityHigh {
			high = append(high, id)
		}
	}
	for _, d := range ov.Discoveries {
		if d.Kind == board.DiscoveryTask && d.Status == board.DiscoveryAccepted && d.Priority == board.PriorityHigh {
			high = append(high, d.ID)
		}
	}
	head := ""
	if co := board.Coordinate(manifestEvidence(snap.Manifest), ov, order); len(co.NextUp) > 0 {
		head = co.NextUp[0]
	}
	cur, known := ov.Cursors[consumer]
	prevHead := cur.LastNextUp
	if !known {
		prevHead = head
	}
	ackSeq := cur.Seq
	if n := len(events); n > 0 {
		ackSeq = events[n-1].Seq
	}
	ack := func(ctx context.Context) bool {
		moved, _ := store.AckAdvanceNextUpCtx(ctx, consumer, ackSeq, head)
		return moved
	}
	// Events that render to nothing still have to be consumed, or every pull
	// would peek them again.
	emptyAck := noop
	if len(events) > 0 {
		emptyAck = ack
	}
	if format == "json" {
		changed := head != "" && head != prevHead
		if len(events) == 0 && !changed {
			return "", noop
		}
		shown, truncated := pullEvents(events)
		ids := make([]string, 0, len(high))
		for _, id := range high {
			if clean := sanitize.Terminal(id); clean != "" {
				ids = append(ids, clean)
			}
		}
		if len(ids) > board.MaxDeltaHighIDs {
			ids, truncated = ids[:board.MaxDeltaHighIDs], true
		}
		raw, err := json.Marshal(struct {
			Revision      uint64        `json:"revision"`
			Events        []board.Event `json:"events"`
			Truncated     bool          `json:"truncated"`
			HighPriority  []string      `json:"high_priority"`
			NextUp        string        `json:"next_up"`
			NextUpChanged bool          `json:"next_up_changed"`
		}{Revision: ov.Revision, Events: shown, Truncated: truncated, HighPriority: ids, NextUp: head, NextUpChanged: changed})
		if err != nil {
			return "", emptyAck
		}
		return string(raw), ack
	}
	text := board.RenderDeltaFor(consumer, ov.Revision, events, titles, high, head, prevHead)
	if text == "" {
		return "", emptyAck
	}
	return text, ack
}

// manifestEvidence turns the manifest's tasks into evidence rows with no
// structural progress (column todo), the input board.Coordinate needs when no
// AST scan ran.
func manifestEvidence(m contracts.PlanManifest) []planstate.TaskEvidence {
	rows := make([]planstate.TaskEvidence, 0, len(m.TaskDAG))
	for _, t := range m.TaskDAG {
		rows = append(rows, planstate.TaskEvidence{
			ID: t.ID, Title: t.Title, DependsOn: append([]string{}, t.DependsOn...), Column: planstate.ColumnTodo,
			Targets: []planstate.TargetEvidence{}, AcceptanceCriteria: []string{},
		})
	}
	return rows
}

// --- hook-safe board writes (contract C10) -----------------------------------------------------

// boardWriteBound is the total time `board report`, `board discover` and
// `board activity` may take: they run inside host hooks.
const boardWriteBound = 2 * time.Second

// errNoApprovedBoard reports a repository with no manifest, or one whose
// manifest is not approved on this machine.
var errNoApprovedBoard = errors.New("no approved plan in this repository")

// openApprovedBoard resolves repo to a directory, inspects it and opens the
// overlay store of its approved manifest. It returns the resolved root, the
// inspected plan and the store, or errNoApprovedBoard when there is no
// approved plan.
func openApprovedBoard(repo string) (string, planstate.Snapshot, *board.Store, error) {
	root, code := resolveRepoArg(repo)
	if code != exitOK {
		return "", planstate.Snapshot{}, nil, errors.New("cannot resolve the repository")
	}
	snap, err := planstate.Inspect(root)
	if err != nil {
		return "", planstate.Snapshot{}, nil, err
	}
	if snap.ManifestSHA256 == "" || !snap.Approved {
		return "", planstate.Snapshot{}, nil, errNoApprovedBoard
	}
	store, err := board.Open(snap.ManifestSHA256)
	if err != nil {
		return "", planstate.Snapshot{}, nil, err
	}
	return root, snap, store, nil
}

// runBounded runs fn and waits at most bound for it. A call that outlives the
// bound or ctx reports false and is abandoned: the process exits right after,
// and every store write is atomic.
func runBounded[T any](ctx context.Context, bound time.Duration, fn func() T) (T, bool) {
	ctx, cancel := context.WithTimeout(ctx, bound)
	defer cancel()
	done := make(chan T, 1)
	go func() { done <- fn() }()
	select {
	case v := <-done:
		return v, true
	case <-ctx.Done():
		var zero T
		return zero, false
	}
}

// stringList is a repeatable string flag.
type stringList []string

func (l *stringList) String() string { return strings.Join(*l, ",") }

func (l *stringList) Set(v string) error {
	*l = append(*l, v)
	return nil
}

// evalExisting resolves symlinks in the deepest existing ancestor of p and
// re-appends the rest, so a path whose file does not exist yet still compares
// with a symlink-free repository root.
func evalExisting(p string) string {
	var tail []string
	for cur := p; ; {
		if real, err := filepath.EvalSymlinks(cur); err == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				real = filepath.Join(real, tail[i])
			}
			return real
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
		}
		tail = append(tail, filepath.Base(cur))
		cur = parent
	}
}

// repoRelPath returns p as a clean, slash-separated path relative to repo, or
// false when p is empty or lies outside the repository (or is the repository
// itself). A relative p is taken relative to the repository root.
func repoRelPath(repo, p string) (string, bool) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", false
	}
	root := evalExisting(repo)
	abs := p
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(root, abs)
	}
	rel, err := filepath.Rel(root, evalExisting(filepath.Clean(abs)))
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// repoRelPaths maps every path through repoRelPath and drops the ones outside
// the repository.
func repoRelPaths(repo string, in []string) []string {
	out := make([]string, 0, len(in))
	for _, p := range in {
		if rel, ok := repoRelPath(repo, p); ok {
			out = append(out, rel)
		}
	}
	return out
}

// boardTaskKnown reports whether id is a task of the manifest or a task
// discovery that is not dismissed.
func boardTaskKnown(m contracts.PlanManifest, ov board.Overlay, id string) bool {
	for _, t := range m.TaskDAG {
		if t.ID == id {
			return true
		}
	}
	for _, d := range ov.Discoveries {
		if d.ID == id && d.Kind == board.DiscoveryTask && d.Status != board.DiscoveryDismissed {
			return true
		}
	}
	return false
}

// boardActivityState maps the --state values of `board report` to activity.
func boardActivityState(state string) (board.ActivityState, bool) {
	switch state {
	case "started":
		return board.ActivityActive, true
	case "done":
		return board.ActivityDone, true
	case "blocked":
		return board.ActivityBlocked, true
	}
	return "", false
}

// runBoardReport implements `plan-forge board report --repo PATH --consumer ID
// --task ID --state started|done|blocked [--note TEXT]`. It is hook-safe: only
// an invalid flag exits 2; everything else exits 0, printing nothing, and no
// approved plan, an unknown task or a store failure is a silent no-op.
func runBoardReport(ctx context.Context, args []string) int {
	var repoFlag, consumer, task, state, note string
	fs, err := parseFlags("board report", args, func(fs *flag.FlagSet) {
		fs.StringVar(&repoFlag, "repo", "", "repository path (default: cwd)")
		fs.StringVar(&consumer, "consumer", "", "consumer id, claude:<session> or omp:<session>")
		fs.StringVar(&task, "task", "", "task id, such as T-1")
		fs.StringVar(&state, "state", "", "started, done or blocked")
		fs.StringVar(&note, "note", "", "optional context note added to the task")
	})
	if err != nil || fs.NArg() != 0 {
		return exitUsage
	}
	if !board.ValidConsumer(consumer) {
		fmt.Fprintln(os.Stderr, "board report: --consumer must match ^(claude|omp):[A-Za-z0-9._-]{1,128}$")
		return exitUsage
	}
	if !board.ValidTaskID(task) {
		fmt.Fprintln(os.Stderr, "board report: --task must be a task id such as T-1")
		return exitUsage
	}
	act, ok := boardActivityState(state)
	if !ok {
		fmt.Fprintln(os.Stderr, "board report: --state must be started, done or blocked")
		return exitUsage
	}
	runBounded(ctx, boardWriteBound, func() struct{} {
		boardReport(repoFlag, consumer, task, act, note)
		return struct{}{}
	})
	return exitOK
}

// boardReport records the harness activity act on task and, for a non-empty
// note, adds it as context authored by consumer. Failures are dropped.
func boardReport(repo, consumer, task string, act board.ActivityState, note string) {
	_, snap, store, err := openApprovedBoard(repo)
	if err != nil {
		return
	}
	ov, err := store.Load()
	if err != nil || !boardTaskKnown(snap.Manifest, ov, task) {
		return
	}
	_, _ = store.RecordActivity(consumer, task, act, board.SourceReport, "")
	if strings.TrimSpace(note) != "" {
		_, _ = store.AddNoteBy(consumer, task, note)
	}
}

// runBoardDiscover implements `plan-forge board discover --repo PATH
// --consumer ID --title TEXT [--parent ID] [--path P]... [--reason TEXT]`. It
// records a proposed task discovery and prints {"id":"D-n"}. It is hook-safe
// like `board report`.
func runBoardDiscover(ctx context.Context, args []string) int {
	var (
		repoFlag, consumer, title, parent, reason string
		paths                                     stringList
	)
	fs, err := parseFlags("board discover", args, func(fs *flag.FlagSet) {
		fs.StringVar(&repoFlag, "repo", "", "repository path (default: cwd)")
		fs.StringVar(&consumer, "consumer", "", "consumer id, claude:<session> or omp:<session>")
		fs.StringVar(&title, "title", "", "title of the work that is not in the plan")
		fs.StringVar(&parent, "parent", "", "task the work belongs to, such as T-1")
		fs.Var(&paths, "path", "repository path the work touches (repeatable)")
		fs.StringVar(&reason, "reason", "", "why the work is needed")
	})
	if err != nil || fs.NArg() != 0 {
		return exitUsage
	}
	if !board.ValidConsumer(consumer) {
		fmt.Fprintln(os.Stderr, "board discover: --consumer must match ^(claude|omp):[A-Za-z0-9._-]{1,128}$")
		return exitUsage
	}
	if strings.TrimSpace(title) == "" {
		fmt.Fprintln(os.Stderr, "board discover: --title is required")
		return exitUsage
	}
	if parent != "" && !board.ValidTaskID(parent) {
		fmt.Fprintln(os.Stderr, "board discover: --parent must be a task id such as T-1")
		return exitUsage
	}
	id, ok := runBounded(ctx, boardWriteBound, func() string {
		return boardDiscover(repoFlag, consumer, title, parent, reason, paths)
	})
	if !ok || id == "" {
		return exitOK
	}
	out, err := json.Marshal(struct {
		ID string `json:"id"`
	}{ID: id})
	if err != nil {
		return exitOK
	}
	fmt.Fprintln(os.Stdout, string(out))
	return exitOK
}

// boardDiscover records a proposed task discovery and returns its id, or ""
// when anything fails. A parent the plan does not know is dropped, so the card
// cannot wait forever on a task that is not there; paths outside the
// repository are dropped too.
func boardDiscover(repo, consumer, title, parent, reason string, paths []string) string {
	root, snap, store, err := openApprovedBoard(repo)
	if err != nil {
		return ""
	}
	if parent != "" {
		ov, err := store.Load()
		if err != nil || !boardTaskKnown(snap.Manifest, ov, parent) {
			parent = ""
		}
	}
	d, _, err := store.AddDiscovery(consumer, board.DiscoveryInput{
		Kind: board.DiscoveryTask, Title: title, Paths: repoRelPaths(root, paths), ParentTaskID: parent, Reason: reason,
	})
	if err != nil {
		return ""
	}
	return d.ID
}

// runBoardActivity implements `plan-forge board activity --repo PATH
// --consumer ID --path P...`: the harness capture path. Paths come from
// repeated --path flags; arguments after the flags are paths too. It is
// hook-safe like `board report`.
func runBoardActivity(ctx context.Context, args []string) int {
	var (
		repoFlag, consumer string
		paths              stringList
	)
	fs, err := parseFlags("board activity", args, func(fs *flag.FlagSet) {
		fs.StringVar(&repoFlag, "repo", "", "repository path (default: cwd)")
		fs.StringVar(&consumer, "consumer", "", "consumer id, claude:<session> or omp:<session>")
		fs.Var(&paths, "path", "repository path the harness touched (repeatable)")
	})
	if err != nil {
		return exitUsage
	}
	paths = append(paths, fs.Args()...)
	if !board.ValidConsumer(consumer) {
		fmt.Fprintln(os.Stderr, "board activity: --consumer must match ^(claude|omp):[A-Za-z0-9._-]{1,128}$")
		return exitUsage
	}
	if len(paths) == 0 {
		fmt.Fprintln(os.Stderr, "board activity: at least one --path is required")
		return exitUsage
	}
	runBounded(ctx, boardWriteBound, func() struct{} {
		_ = boardActivity(repoFlag, consumer, paths)
		return struct{}{}
	})
	return exitOK
}

// boardActivity is the C10 `activity` logic, shared with the Claude hook. It
// maps paths (repository-relative, or absolute inside the repository) onto the
// plan's task targets: each mapped task the consumer's board shows neither
// done nor active is recorded active with source file_edit and the path as
// detail, and each unplanned path becomes a path discovery whose parent is the
// consumer's most recently active task. It returns errNoApprovedBoard when the
// repository has no approved plan; a path outside the repository is ignored.
func boardActivity(repo, consumer string, paths []string) error {
	root, snap, store, err := openApprovedBoard(repo)
	if err != nil {
		return err
	}
	rel := repoRelPaths(root, paths)
	if len(rel) == 0 {
		return nil
	}
	byTask, unplanned := board.MapPaths(snap.Manifest, rel)
	ov, err := store.Load()
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(byTask))
	for id := range byTask {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if a, ok := ov.Activity[id]; ok && (a.State == board.ActivityDone || a.State == board.ActivityActive) {
			continue
		}
		if _, err := store.RecordActivity(consumer, id, board.ActivityActive, board.SourceFileEdit, byTask[id][0]); err != nil {
			return err
		}
	}
	if len(unplanned) == 0 {
		return nil
	}
	if ov, err = store.Load(); err != nil {
		return err
	}
	parent := latestActiveTask(ov, consumer)
	for _, p := range unplanned {
		if _, _, err := store.AddDiscovery(consumer, board.DiscoveryInput{
			Kind: board.DiscoveryPath, Paths: []string{p}, ParentTaskID: parent,
		}); err != nil {
			return err
		}
	}
	return nil
}

// latestActiveTask returns the task whose activity is active, was recorded by
// consumer and is the newest; equal times pick the smaller id. It is "" when
// the consumer has none.
func latestActiveTask(ov board.Overlay, consumer string) string {
	best := ""
	for id, a := range ov.Activity {
		if a.State != board.ActivityActive || a.By != consumer {
			continue
		}
		if best == "" || a.At > ov.Activity[best].At || (a.At == ov.Activity[best].At && id < best) {
			best = id
		}
	}
	return best
}
