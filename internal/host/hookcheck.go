//go:build client && !server

package host

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/danielckv/agentic-plan-forge/integrations"
)

// Hook states, worst first. A reader should be able to sort on these.
const (
	HookMissing    = "missing"    // nothing of ours is installed
	HookStale      = "stale"      // installed, but not this binary or not every event
	HookModified   = "modified"   // installed and since changed by someone else
	HookUnreadable = "unreadable" // the file exists but cannot be parsed
	HookInstalled  = "installed"
)

// HookStatus is what `plan-forge doctor` reports about one host's managed
// hooks.
type HookStatus struct {
	Host    string   `json:"host"`
	Surface string   `json:"surface"`
	Path    string   `json:"path"`
	State   string   `json:"state"`
	Events  []string `json:"events,omitempty"`  // managed and current
	Missing []string `json:"missing,omitempty"` // expected, absent
	Stale   []string `json:"stale,omitempty"`   // managed, but pointing elsewhere
	Bin     string   `json:"bin,omitempty"`     // the binary a stale entry names
	Remedy  string   `json:"remedy,omitempty"`
}

// InspectHooks reports whether each host's managed hooks are installed and
// point at this binary.
//
// It matters most for oh-my-pi. Plan Forge installs no skills there, so the
// hook is the only thing that registers /plan-forge and /plan-forge-board: a
// missing hook does not degrade the commands, it removes them. Doctor has to
// say so rather than report a healthy-looking host with no way in.
//
// kinds is the set to inspect; repo "" skips project scope.
func (in *Installer) InspectHooks(kinds []string, repo string) ([]HookStatus, error) {
	home, err := homeDir()
	if err != nil {
		return nil, err
	}
	out := []HookStatus{}
	for _, kind := range kinds {
		switch kind {
		case string(KindClaude):
			out = append(out, in.claudeHookStatus(home))
		case string(KindOMP):
			out = append(out, in.ompHookStatus(home, repo))
		case string(KindAGY):
			out = append(out, in.agyHookStatus(home))
		}
	}
	return out, nil
}

// shortHome rewrites a path under the home directory to a ~ form, so a report
// never prints more of the user's filesystem than it needs to.
func shortHome(home, path string) string {
	if home != "" && strings.HasPrefix(path, home+string(os.PathSeparator)) {
		return "~" + strings.TrimPrefix(path, home)
	}
	return path
}

// claudeHookStatus parses settings.json and checks every managed event.
func (in *Installer) claudeHookStatus(home string) HookStatus {
	path := filepath.Join(home, ".claude", "settings.json")
	st := HookStatus{Host: string(KindClaude), Surface: "settings.json", Path: shortHome(home, path)}

	raw, err := os.ReadFile(path)
	if err != nil {
		st.State, st.Missing = HookMissing, append([]string(nil), claudeHookEvents...)
		st.Remedy = "run: plan-forge install --host claude"
		return st
	}
	var settings map[string]any
	if json.Unmarshal(raw, &settings) != nil {
		st.State = HookUnreadable
		st.Remedy = "settings.json is not valid JSON; fix it, then run: plan-forge install --host claude"
		return st
	}
	hooks, _ := settings["hooks"].(map[string]any)

	// The commands this binary would install, to compare against.
	want := map[string][]string{
		"SessionStart": {
			strings.ReplaceAll(claudeHookCommand, "{{PLAN_FORGE_BIN}}", in.bin),
			strings.ReplaceAll(claudeCenterCommand, "{{PLAN_FORGE_BIN}}", in.bin),
		},
		"PreToolUse":       {strings.ReplaceAll(claudeGuardCommand, "{{PLAN_FORGE_BIN}}", in.bin)},
		"PostToolUse":      {strings.ReplaceAll(claudeHandoffCommand, "{{PLAN_FORGE_BIN}}", in.bin)},
		"UserPromptSubmit": {strings.ReplaceAll(claudeHandoffCommand, "{{PLAN_FORGE_BIN}}", in.bin)},
	}
	for _, event := range claudeHookEvents {
		managed := managedCommands(hooks[event])
		if len(managed) == 0 {
			st.Missing = append(st.Missing, event)
			continue
		}
		if !sameCommands(managed, want[event]) {
			st.Stale = append(st.Stale, event)
			if st.Bin == "" {
				st.Bin = binaryOf(managed[0])
			}
			continue
		}
		st.Events = append(st.Events, event)
	}
	st.State = hookState(len(st.Missing), len(st.Stale))
	if st.State != HookInstalled {
		// The Claude merge refreshes a stale handler in place, so re-running
		// install is the whole remedy.
		st.Remedy = "run: plan-forge install --host claude"
	}
	return st
}

// ompHookStatus compares the installed hook factory against what this binary
// would write. Comparing bytes rather than scraping the binary path also
// catches a hook left behind by an older template.
func (in *Installer) ompHookStatus(home, repo string) HookStatus {
	path := filepath.Join(home, ".omp", "agent", "hooks", "pre", "plan-forge.ts")
	if repo != "" {
		if project := filepath.Join(repo, ".omp", "hooks", "pre", "plan-forge.ts"); fileExists(project) {
			path = project
		}
	}
	st := HookStatus{Host: string(KindOMP), Surface: "hooks/pre/plan-forge.ts", Path: shortHome(home, path)}
	st.Remedy = "without this hook oh-my-pi has no /plan-forge or /plan-forge-board at all, " +
		"because Plan Forge installs no skills there; run: plan-forge install --host omp"

	raw, err := os.ReadFile(path)
	if err != nil {
		st.State = HookMissing
		return st
	}
	wantBody := in.substitute(integrations.OMPHook(), string(KindOMP))
	if string(raw) == wantBody {
		st.State, st.Events, st.Remedy = HookInstalled, []string{"session_start", "tool_result"}, ""
		return st
	}
	// Owned and unchanged since install, but not what this binary writes: an
	// older template or a moved binary. Owned and changed: someone edited it.
	if manifest, _, err := loadManifest(); err == nil {
		for _, row := range manifest.Rows {
			if row.Path != path || row.Kind != kindHookFactory {
				continue
			}
			if sha256Hex(raw) == row.SHA256 {
				st.State, st.Bin = HookStale, binaryOf(string(raw))
				return st
			}
			st.State = HookModified
			st.Remedy = "the installed hook was edited after install; " +
				"re-run with --force to replace it: plan-forge install --host omp --force"
			return st
		}
	}
	st.State = HookStale
	return st
}

// agyHookStatus checks the flat SessionStart array Antigravity reads.
func (in *Installer) agyHookStatus(home string) HookStatus {
	path := filepath.Join(home, ".gemini", "config", "hooks.json")
	st := HookStatus{Host: string(KindAGY), Surface: "hooks.json", Path: shortHome(home, path)}

	raw, err := os.ReadFile(path)
	if err != nil {
		st.State, st.Missing = HookMissing, []string{"SessionStart"}
		st.Remedy = "run: plan-forge install --host agy"
		return st
	}
	var hooks map[string]any
	if json.Unmarshal(raw, &hooks) != nil {
		st.State = HookUnreadable
		st.Remedy = "hooks.json is not valid JSON; fix it, then run: plan-forge install --host agy"
		return st
	}
	managed := managedCommands(hooks["SessionStart"])
	switch {
	case len(managed) == 0:
		st.State, st.Missing = HookMissing, []string{"SessionStart"}
		st.Remedy = "run: plan-forge install --host agy"
	case !sameCommands(managed, []string{in.bin + " hook check-state --format agy"}):
		st.State, st.Stale, st.Bin = HookStale, []string{"SessionStart"}, binaryOf(managed[0])
		// mergeAGYHook returns early on a marker match, so re-running install
		// does not refresh a stale path. Say what actually works.
		st.Remedy = "re-running install does not refresh this entry; " +
			"run plan-forge uninstall --host agy, then plan-forge install --host agy"
	default:
		st.State, st.Events = HookInstalled, []string{"SessionStart"}
	}
	return st
}

func hookState(missing, stale int) string {
	switch {
	case stale > 0:
		return HookStale
	case missing > 0:
		return HookMissing
	}
	return HookInstalled
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// managedCommands collects the command strings of every managed handler under
// one event, in either the Claude nested shape or the AGY flat one.
func managedCommands(node any) []string {
	entries, ok := node.([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, entry := range entries {
		m, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		if cmd, _ := m[claudeCommandKey].(string); strings.Contains(cmd, managedMarker) {
			out = append(out, cmd)
			continue
		}
		handlers, _ := m["hooks"].([]any)
		for _, h := range handlers {
			hm, ok := h.(map[string]any)
			if !ok {
				continue
			}
			if cmd, _ := hm[claudeCommandKey].(string); strings.Contains(cmd, managedMarker) {
				out = append(out, cmd)
			}
		}
	}
	return out
}

// sameCommands compares installed commands against the desired set, ignoring
// order and the managed marker suffix.
func sameCommands(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	strip := func(in []string) []string {
		out := make([]string, 0, len(in))
		for _, c := range in {
			out = append(out, strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(c), "# "+managedMarker)))
		}
		sort.Strings(out)
		return out
	}
	a, b := strip(got), strip(want)
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// binaryOf extracts the binary path a managed command names, for a report that
// can say which install a stale entry still points at.
func binaryOf(command string) string {
	command = strings.TrimSpace(command)
	if command == "" {
		return ""
	}
	if strings.HasPrefix(command, `"`) {
		if end := strings.Index(command[1:], `"`); end >= 0 {
			return command[1 : end+1]
		}
		return ""
	}
	if i := strings.IndexAny(command, " \t"); i > 0 {
		return command[:i]
	}
	return command
}

// HookHosts is the set of hosts worth inspecting: those detected on this
// machine, plus any the install manifest already owns a hook for. A host whose
// binary has gone missing but whose hook is still installed must not drop out
// of the report — that is precisely the state a user needs told.
func HookHosts(detected []string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(kind string) {
		if kind == "" || seen[kind] {
			return
		}
		seen[kind] = true
		out = append(out, kind)
	}
	for _, kind := range detected {
		add(kind)
	}
	if manifest, _, err := loadManifest(); err == nil {
		for _, row := range manifest.Rows {
			if row.Kind == kindSettings || row.Kind == kindHookFactory {
				add(row.Host)
			}
		}
	}
	sort.Strings(out)
	return out
}
