//go:build client && !server

package host

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// managedMarker identifies our entries inside a host's settings file. It is
// part of the JSON we write, so it is also what uninstall and JSON merge
// detect: an entry carrying this marker belongs to plan-forge.
const managedMarker = "plan-forge managed"

// claudeHookCommand is the SessionStart hook command template. The binary
// path is substituted before the entry is merged.
const claudeHookCommand = `"{{PLAN_FORGE_BIN}}" hook check-state --repo "$CLAUDE_PROJECT_DIR" --format claude`

// claudeHandoffCommand is the PostToolUse (matcher claudePostToolUseMatcher)
// and UserPromptSubmit hook command template: it turns an approved forge wait
// into plan mode and, on edit tools, reports harness activity to the board.
const claudeHandoffCommand = `"{{PLAN_FORGE_BIN}}" hook claude`

// claudePostToolUseMatcher is the managed PostToolUse matcher: Bash for the
// forge handoff, the edit tools for board activity capture.
const claudePostToolUseMatcher = "Bash|Edit|Write|MultiEdit|NotebookEdit"

// claudeHookEvents are the Claude Code settings events plan-forge manages.
var claudeHookEvents = []string{"SessionStart", "PostToolUse", "UserPromptSubmit"}

// claudeHookTimeout is the explicit per-handler timeout (seconds) applied to
// every managed Claude hook handler.
const claudeHookTimeout = 5

// settingsMergeError reports a settings file plan-forge refuses to touch.
type settingsMergeError struct{ msg string }

func (e *settingsMergeError) Error() string { return e.msg }

// mergeClaudeHook merges our managed entries into the parsed
// ~/.claude/settings.json: SessionStart (check-state), PostToolUse with
// matcher claudePostToolUseMatcher (hook claude) and UserPromptSubmit (hook claude). Each event
// is handled independently: an event without a managed entry gains one, and
// an existing managed entry is refreshed in place when any of its managed
// handlers differ from the desired handlers (e.g. a stale binary path after
// the binary moved), preserving entry order and foreign handlers. The merge
// is idempotent per event. Unrelated keys and foreign entries are never
// rewritten: unknown keys decode into the generic map and are written back
// unchanged.
func mergeClaudeHook(settings map[string]any, sessionStartCommand, handoffCommand string) (map[string]any, bool, error) {
	var hooks map[string]any
	switch v := settings["hooks"].(type) {
	case nil:
		hooks = map[string]any{}
	case map[string]any:
		hooks = v
	default:
		// A foreign shape is never overwritten: refuse and show the snippet.
		return nil, false, &settingsMergeError{
			msg: fmt.Sprintf("settings \"hooks\" is not an object\nmanual snippet:\n%s", manualHookSnippet),
		}
	}
	managed := func(command string) map[string]any {
		return map[string]any{
			"type":    "command",
			"command": command + " # " + managedMarker,
			"timeout": claudeHookTimeout,
		}
	}
	entries := map[string]map[string]any{
		"SessionStart":     {"hooks": []any{managed(sessionStartCommand)}},
		"PostToolUse":      {"matcher": claudePostToolUseMatcher, "hooks": []any{managed(handoffCommand)}},
		"UserPromptSubmit": {"hooks": []any{managed(handoffCommand)}},
	}
	changed := false
	for _, event := range claudeHookEvents {
		var existing []any
		switch v := hooks[event].(type) {
		case nil:
		case []any:
			existing = v
		default:
			return nil, false, &settingsMergeError{
				msg: fmt.Sprintf("settings hooks.%s is not an array\nmanual snippet:\n%s", event, manualHookSnippet),
			}
		}
		found := false
		// needSplit: a managed handler shared an entry with foreign handlers
		// under a stale matcher. The managed handler is removed from that
		// entry (foreign handlers keep their firing scope) and a fresh
		// managed entry with the desired matcher is appended instead.
		needSplit, haveCurrent := false, false
		wantMatcher, hasMatcher := entries[event]["matcher"].(string)
		for i, entry := range existing {
			kept, ours := stripManagedEntry(entry)
			if !ours {
				continue
			}
			found = true
			if em, _ := entry.(map[string]any); kept != nil && hasMatcher && em != nil && em["matcher"] != wantMatcher {
				existing[i] = kept
				needSplit = true
				changed = true
				continue
			}
			haveCurrent = true
			// A managed entry whose handlers differ from the desired ones
			// (e.g. a stale binary path after a move) is replaced in place,
			// preserving the entry's position and any foreign handlers it
			// shares the entry with.
			refreshed := replaceManagedHandlers(entry, entries[event])
			if !sameJSON(entry, refreshed) {
				existing[i] = refreshed
				changed = true
			}
		}
		if found && !(needSplit && !haveCurrent) {
			continue
		}
		hooks[event] = append(existing, any(entries[event]))
		changed = true
	}
	if !changed {
		return settings, false, nil
	}
	settings["hooks"] = hooks
	return settings, true, nil
}

// claudeCommandKey is the settings key holding a handler's command.
const claudeCommandKey = "command"

// replaceManagedHandlers swaps every managed handler in one hook entry for
// the handlers of the desired entry, keeping foreign handlers and entry
// order. When the desired entry carries a matcher and the existing entry's
// matcher differs, the matcher is updated too so existing installs upgrade.
// Shapes follow stripManagedEntry: the flat AGY/Claude handler and the
// Claude entry with a "hooks" array.
func replaceManagedHandlers(entry any, desiredEntry map[string]any) any {
	desired, _ := desiredEntry["hooks"].([]any)
	wantMatcher, hasMatcher := desiredEntry["matcher"].(string)
	m, ok := entry.(map[string]any)
	if !ok {
		return entry
	}
	if entryHasMarker(m, claudeCommandKey) {
		if len(desired) == 1 {
			return desired[0]
		}
		return entry
	}
	handlers, ok := m["hooks"].([]any)
	if !ok {
		return entry
	}
	kept := make([]any, 0, len(handlers)+len(desired))
	inserted := false
	for _, handler := range handlers {
		if entryHasMarker(handler, claudeCommandKey) {
			if !inserted {
				kept = append(kept, desired...)
				inserted = true
			}
			continue
		}
		kept = append(kept, handler)
	}
	if !inserted {
		return entry
	}
	rest := make(map[string]any, len(m))
	for k, v := range m {
		rest[k] = v
	}
	rest["hooks"] = kept
	if hasMatcher && len(kept) == len(desired) { // never retarget foreign handlers
		rest["matcher"] = wantMatcher
	}
	return rest
}

// entryHasMarker reports whether a SessionStart entry's command carries the
// managed marker. Unknown entry shapes are simply not ours.
func entryHasMarker(entry any, commandKey string) bool {
	m, ok := entry.(map[string]any)
	if !ok {
		return false
	}
	cmd, _ := m[commandKey].(string)
	return strings.Contains(cmd, managedMarker)
}

// stripManagedEntry removes the handlers carrying the managed marker from one
// hook entry. It returns the entry to keep (nil when nothing is left) and
// whether any managed handler was present. Two shapes are recognised: the
// Claude entry ({"matcher":…, "hooks":[{"command":…}]}) and the flat AGY
// handler ({"type":"command","command":…}). A foreign handler that shares an
// entry with ours survives; entries of any other shape are never ours.
func stripManagedEntry(entry any) (any, bool) {
	m, ok := entry.(map[string]any)
	if !ok {
		return entry, false
	}
	if entryHasMarker(m, claudeCommandKey) {
		return nil, true
	}
	handlers, ok := m["hooks"].([]any)
	if !ok {
		return entry, false
	}
	kept := make([]any, 0, len(handlers))
	removed := false
	for _, handler := range handlers {
		if entryHasMarker(handler, claudeCommandKey) {
			removed = true
			continue
		}
		kept = append(kept, handler)
	}
	if !removed {
		return entry, false
	}
	if len(kept) == 0 {
		return nil, true
	}
	rest := make(map[string]any, len(m))
	for k, v := range m {
		rest[k] = v
	}
	rest["hooks"] = kept
	return rest, true
}

// sameJSON compares two decoded-JSON values canonically. A direct
// reflect.DeepEqual would be type-sensitive (JSON numbers decode to float64,
// while freshly built handlers carry ints), so both sides are re-encoded.
func sameJSON(a, b any) bool {
	ab, err := json.Marshal(a)
	if err != nil {
		return false
	}
	bb, err := json.Marshal(b)
	if err != nil {
		return false
	}
	return bytes.Equal(ab, bb)
}

// encodeSettings renders a settings map as 2-space-indented JSON with a
// trailing newline.
func encodeSettings(settings map[string]any) ([]byte, error) {
	out, err := json.MarshalIndent(orderedSettings(settings), "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// orderedSettings re-marshals with sorted top-level keys so output is stable.
func orderedSettings(settings map[string]any) map[string]any {
	keys := make([]string, 0, len(settings))
	for k := range settings {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	ordered := make(map[string]any, len(settings))
	for _, k := range keys {
		ordered[k] = settings[k]
	}
	return ordered
}

// mergeAGYHook merges our SessionStart handler into the parsed
// ~/.gemini/config/hooks.json, whose documented schema maps hook names to
// event objects whose hooks arrays carry {type, command, timeout} handlers.
func mergeAGYHook(hooks map[string]any, hookCommand string) (map[string]any, bool, error) {
	if hooks == nil {
		hooks = map[string]any{}
	}
	start, _ := hooks["SessionStart"].([]any)
	handler := map[string]any{
		"type":    "command",
		"command": hookCommand + " # " + managedMarker,
		"timeout": 2,
	}
	for _, existing := range start {
		if entryHasMarker(existing, claudeCommandKey) {
			return hooks, false, nil
		}
	}
	hooks["SessionStart"] = append(start, any(handler))
	return hooks, true, nil
}

// splitManagedHook separates a settings/hooks JSON file into the content
// without our entries and a report of whether any were present. It powers
// uninstall: only entries carrying the managed marker are removed, everything
// else is preserved. Two layouts are recognised: the Claude shape
// ({"hooks": {<event>: [entries]}}) and the AGY shape ({<event>: [entries]}
// at the top level).
func splitManagedHook(raw []byte) (map[string]any, bool, error) {
	var settings map[string]any
	if err := json.Unmarshal(raw, &settings); err != nil {
		return nil, false, &settingsMergeError{
			msg: fmt.Sprintf("settings file is not valid JSON: %v\nmanual snippet:\n%s", err, manualHookSnippet),
		}
	}
	ours := false
	strip := func(hooks map[string]any) bool {
		stripped := false
		for _, event := range claudeHookEvents {
			entries, ok := hooks[event].([]any)
			if !ok {
				continue
			}
			kept := make([]any, 0, len(entries))
			found := false
			for _, entry := range entries {
				rest, ours := stripManagedEntry(entry)
				if !ours {
					kept = append(kept, entry)
					continue
				}
				found = true
				if rest != nil {
					kept = append(kept, rest)
				}
			}
			if !found {
				continue
			}
			stripped = true
			if len(kept) == 0 {
				delete(hooks, event)
			} else {
				hooks[event] = kept
			}
		}
		return stripped
	}
	if hooks, ok := settings["hooks"].(map[string]any); ok && strip(hooks) {
		ours = true
		if len(hooks) == 0 {
			delete(settings, "hooks")
		}
	}
	// AGY hooks.json keeps the event arrays at the top level.
	if strip(settings) {
		ours = true
	}
	if !ours {
		return settings, false, nil
	}
	return settings, true, nil
}

// manualHookSnippet is printed whenever a settings merge is refused, so the
// user can add the hooks by hand instead. The JSON inside uses two-space
// indentation because that is what the user pastes into their settings file.
// editorconfig-checker-disable
const manualHookSnippet = `{
  "hooks": {
    "SessionStart": [
      {
        "hooks": [
          {
            "command": "PLAN_FORGE_BIN hook check-state --repo \"$CLAUDE_PROJECT_DIR\" --format claude",
            "timeout": 5
          }
        ]
      }
    ],
    "PostToolUse": [
      {
        "matcher": "Bash|Edit|Write|MultiEdit|NotebookEdit",
        "hooks": [
          {
            "command": "PLAN_FORGE_BIN hook claude",
            "timeout": 5
          }
        ]
      }
    ],
    "UserPromptSubmit": [
      {
        "hooks": [
          {
            "command": "PLAN_FORGE_BIN hook claude",
            "timeout": 5
          }
        ]
      }
    ]
  }
}` // editorconfig-checker-enable

// gitHookManagedHead and gitHookManagedTail bound the managed block appended
// to a repository's post-merge hook.
const (
	gitHookManagedHead = "# >>> plan-forge (managed) >>>"
	gitHookManagedTail = "# <<< plan-forge <<<"
)

// appendManagedGitHook appends the managed block to existing post-merge
// content, preserving that content and running after it. When the file is
// absent the caller creates it with a #!/bin/sh header and mode 0755.
func appendManagedGitHook(existing, block string) string {
	if strings.Contains(existing, gitHookManagedHead) {
		return existing
	}
	content := existing
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	if content != "" {
		content += "\n"
	}
	return content + gitHookManagedHead + "\n" + block + gitHookManagedTail + "\n"
}

// stripManagedGitHook removes our managed block from post-merge content,
// returning the remainder and whether the block was present.
func stripManagedGitHook(content string) (string, bool) {
	start := strings.Index(content, gitHookManagedHead)
	if start < 0 {
		return content, false
	}
	end := strings.Index(content, gitHookManagedTail)
	if end < start {
		return content, false
	}
	remainder := content[:start] + content[end+len(gitHookManagedTail):]
	remainder = strings.TrimRight(remainder, "\n")
	if remainder != "" {
		remainder += "\n"
	}
	return remainder, true
}

// gitHookSnippet is printed when the git-hook install is refused (tracked or
// shared hooks directories) so the user can add the block by hand.
func gitHookSnippet(binPath string) string {
	return fmt.Sprintf("Add this block to your post-merge hook manually:\n%s\n%s\n%s\n", gitHookManagedHead, renderGitHookBlock(binPath), gitHookManagedTail)
}

// renderGitHookBlock produces the guarded one-line hook body with the binary
// path substituted.
func renderGitHookBlock(binPath string) string {
	return fmt.Sprintf("PF=%q; if [ -x \"$PF\" ]; then \"$PF\" hook check-state --repo \"$(git rev-parse --show-toplevel)\" --format text || true; fi", binPath)
}

// ensureDir creates dir (and parents) when missing. It returns the error the
// plan requires for a path component that is not a directory.
func ensureDir(dir string) error {
	info, err := os.Lstat(dir)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return checkSymlinkedRoot(dir)
		}
		if !info.IsDir() {
			return &PathError{Path: dir, Reason: "exists and is not a directory"}
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return nil
}

// checkNoSymlinkComponents (below) is the path policy used by the installer;
// the host-root symlink rule is checkSymlinkedRoot.

// checkSymlinkedRoot enforces the user-scope rule for a host root that is a
// symlink: the resolved target must be a directory, not world-writable, and
// owned by the current user. Ownership is verified portably: the resolved
// root must be inside the user's home directory, the one directory the user
// provably owns on every supported platform (all plan host roots live under
// it). Anything else fails closed with exit 4.
func checkSymlinkedRoot(root string) error {
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return &PathError{Path: resolved, Reason: "host root resolves to a non-directory"}
	}
	if info.Mode().Perm()&0o002 != 0 {
		return &PathError{Path: resolved, Reason: "host root resolves to a world-writable directory"}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	if !pathWithin(resolved, home) {
		return &PathError{Path: resolved, Reason: "host root resolves outside the current user's home directory"}
	}
	return nil
}

// checkNoSymlinkComponents enforces the project-scope path policy: every
// component of the absolute path must be a real file or directory (Lstat;
// Windows reparse points count as symlinks via the Lstat mode check).
func checkNoSymlinkComponents(abs string) error {
	volume := filepath.VolumeName(abs)
	cur := abs
	var ancestors []string
	for {
		ancestors = append(ancestors, cur)
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}
	for i := len(ancestors) - 1; i >= 0; i-- {
		path := ancestors[i]
		if path == volume || path == string(filepath.Separator) || path == "." {
			continue
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return &PathError{Path: path, Reason: "is a symlink component in the project path"}
		}
		if !info.Mode().IsRegular() && !info.IsDir() {
			return &PathError{Path: path, Reason: "is neither a regular file nor a directory"}
		}
	}
	return nil
}

// PathError reports a refused path with the reason. The installer maps it to
// exit 4.
type PathError struct {
	Path   string
	Reason string
}

func (e *PathError) Error() string {
	return e.Path + ": " + e.Reason
}

// pathWithin reports whether target is inside dir (equal or below).
func pathWithin(target, dir string) bool {
	rel, err := filepath.Rel(dir, target)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
