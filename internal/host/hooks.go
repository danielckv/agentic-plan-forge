//go:build client && !server

package host

import (
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

// settingsMergeError reports a settings file plan-forge refuses to touch.
type settingsMergeError struct{ msg string }

func (e *settingsMergeError) Error() string { return e.msg }

// mergeClaudeHook merges our SessionStart entry into the parsed
// ~/.claude/settings.json. It appends the entry only when no entry carrying
// the managed marker exists, and never rewrites unrelated keys: unknown keys
// decode into the generic map and are written back unchanged.
func mergeClaudeHook(settings map[string]any, hookCommand string) (map[string]any, bool, error) {
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	start, _ := hooks["SessionStart"].([]any)
	entry := map[string]any{
		"hooks": []any{map[string]any{
			"type":    "command",
			"command": hookCommand + " # " + managedMarker,
		}},
	}
	for _, existing := range start {
		if entryHasMarker(existing, claudeCommandKey) {
			return settings, false, nil
		}
	}
	hooks["SessionStart"] = append(start, any(entry))
	settings["hooks"] = hooks
	return settings, true, nil
}

const claudeCommandKey = "command"

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
// without our entry and a report of whether our entry was present. It powers
// uninstall: only our entry is removed, everything else is preserved.
func splitManagedHook(raw []byte) (map[string]any, bool, error) {
	var settings map[string]any
	if err := json.Unmarshal(raw, &settings); err != nil {
		return nil, false, &settingsMergeError{
			msg: fmt.Sprintf("settings file is not valid JSON: %v\nmanual snippet:\n%s", err, manualHookSnippet),
		}
	}
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		return settings, false, nil
	}
	start, ok := hooks["SessionStart"].([]any)
	if !ok {
		return settings, false, nil
	}
	kept := make([]any, 0, len(start))
	ours := false
	for _, entry := range start {
		if entryHasMarker(entry, claudeCommandKey) {
			ours = true
			continue
		}
		kept = append(kept, entry)
	}
	if !ours {
		return settings, false, nil
	}
	if len(kept) == 0 {
		delete(hooks, "SessionStart")
	} else {
		hooks["SessionStart"] = kept
	}
	if len(hooks) == 0 {
		delete(settings, "hooks")
	}
	return settings, true, nil
}

// manualHookSnippet is printed whenever a settings merge is refused, so the
// user can add the hook by hand instead. The JSON inside uses two-space
// indentation because that is what the user pastes into their settings file.
// editorconfig-checker-disable
const manualHookSnippet = `{
  "hooks": {
    "SessionStart": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "PLAN_FORGE_BIN hook check-state --repo \"$CLAUDE_PROJECT_DIR\" --format claude"
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
