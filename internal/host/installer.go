//go:build client && !server

package host

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/danielckv/agentic-plan-forge/integrations"
	"github.com/danielckv/agentic-plan-forge/internal/config"
)

// Exit codes for install and uninstall. They are part of the documented
// interface: install.sh and install.ps1 propagate them verbatim.
const (
	// InstallExitNoHost is exit 3: auto mode found no supported host.
	InstallExitNoHost = 3
	// InstallExitRefused is exit 4: a path, ownership or settings merge was refused.
	InstallExitRefused = 4
	// InstallExitModified is exit 5: managed content diverged from the manifest.
	InstallExitModified = 5
)

// ErrInstallModified reports on-disk content that diverged from the recorded
// manifest hash. SuggestedIn holds the freshly rendered template written with
// O_EXCL for the user to compare (empty for merged settings files).
type ErrInstallModified struct {
	Path        string
	SuggestedIn string
}

func (e *ErrInstallModified) Error() string {
	if e.SuggestedIn != "" {
		return e.Path + " diverged from the recorded install manifest; new content written to " + e.SuggestedIn
	}
	return e.Path + " diverged from the recorded install manifest"
}

// InstallOptions are the install/uninstall flags from the plan.
type InstallOptions struct {
	Host    string // auto|claude|omp|agy|all
	Scope   string // user|project
	Repo    string // project scope / git hook repository
	GitHook bool
	DryRun  bool
	Yes     bool
	Force   bool
}

// HostReport is one rendered output row.
type HostReport struct {
	Host    string
	Found   bool
	Version string
	Auth    string // ready|unknown|missing
	AuthCmd string // login command printed for missing
	Targets []string
	Note    string // printed for the found-agy row instead of (skipped)
}

// manifestKind labels what a manifest row points at.
type manifestKind string

const (
	kindSkill       manifestKind = "skill"
	kindCommand     manifestKind = "command"
	kindHookFactory manifestKind = "hook-factory"
	kindSettings    manifestKind = "settings"
	kindGitHook     manifestKind = "git-hook"
)

// manifestRow is one installed artifact.
type manifestRow struct {
	Path    string       `json:"path"`
	Host    string       `json:"host"`
	Kind    manifestKind `json:"kind"`
	Scope   string       `json:"scope"`
	SHA256  string       `json:"sha256"`
	Patch   *patchRecord `json:"patch,omitempty"`
	Version string       `json:"version,omitempty"`
}

// patchRecord records a merge into pre-existing content.
type patchRecord struct {
	Anchor    string `json:"anchor"`
	PreSHA256 string `json:"pre_sha256"`
}

// installManifest is the idempotency record at
// PlanForgeHome()/install-manifest.json.
type installManifest struct {
	Rows []manifestRow `json:"rows"`
}

// artifact is one planned write. handoff carries the second Claude hook
// command (`hook claude`) for kindSettings artifacts; other kinds leave it
// empty.
type artifact struct {
	path    string
	host    string
	kind    manifestKind
	content string
	handoff string
	guard   string
	patch   *patchRecord
}

// Installer writes the integration artifacts. Plan computes them without
// touching the filesystem; Apply performs the manifest-checked writes.
type Installer struct {
	bin     string // absolute, symlink-resolved path of the running binary
	version string
}

// NewInstaller resolves the running binary path (os.Executable +
// filepath.EvalSymlinks) so skills and hooks keep working when the install
// directory is not on the agent's PATH.
func NewInstaller(version string) (*Installer, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve running executable: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return nil, fmt.Errorf("resolve executable symlinks: %w", err)
	}
	return &Installer{bin: resolved, version: version}, nil
}

// Bin returns the substituted {{PLAN_FORGE_BIN}} value.
func (in *Installer) Bin() string { return in.bin }

// selectedHosts maps the --host flag to concrete kinds. auto selects every
// agent found on this machine, in the order the owner wants them to outrank
// each other.
//
// Installing is not generating. Claude Code, Antigravity and oh-my-pi all get
// the skills and hooks, because all three are agents the owner types
// /plan-forge into. Which host generates the plan is a separate decision:
// oh-my-pi serves a pin only (ADR 0026), and the installer does not decide it.
func (in *Installer) selectedHosts(ctx context.Context, flag string) ([]string, error) {
	switch flag {
	case "claude", "omp", "agy":
		return []string{flag}, nil
	case "all":
		return []string{string(KindClaude), string(KindAGY), string(KindOMP)}, nil
	case "auto":
		var found []string
		for _, kind := range []string{string(KindClaude), string(KindAGY), string(KindOMP)} {
			if in.hostPresent(ctx, kind) {
				found = append(found, kind)
			}
		}
		return found, nil
	default:
		return nil, fmt.Errorf("unknown host %q (supported: auto, claude, agy, omp, all)", flag)
	}
}

// hostPresent reports detection without failing: claude/omp through their
// adapters' Detect, agy through the agy binary or its config directories.
func (in *Installer) hostPresent(ctx context.Context, kind string) bool {
	switch kind {
	case string(KindAGY):
		if _, err := lookPath("agy"); err == nil {
			return true
		}
		home, err := homeDir()
		if err != nil {
			return false
		}
		for _, dir := range []string{
			filepath.Join(home, ".gemini", "antigravity-cli"),
			filepath.Join(home, ".gemini", "config"),
		} {
			if info, err := os.Stat(dir); err == nil && info.IsDir() {
				return true
			}
		}
		return false
	case string(KindOMP):
		if _, err := lookPath("omp"); err == nil {
			return true
		}
		home, err := homeDir()
		if err != nil {
			return false
		}
		for _, dir := range []string{
			filepath.Join(home, ".omp", "agents"),
		} {
			if info, err := os.Stat(dir); err == nil && info.IsDir() {
				return true
			}
		}
		return false
	default:
		_, err := Version(ctx, Kind(kind))
		return err == nil
	}
}

// waitTimeout returns the per-host {{WAIT_TIMEOUT}} value, each below the
// host's tool-call timeout default.
func waitTimeout(kind string) string {
	if kind == string(KindClaude) {
		return "100s" // below the 120s BASH_DEFAULT_TIMEOUT_MS default
	}
	return "50s" // below the OMP 60s bash.autoBackground.thresholdMs default
}

// planModeHint returns the per-host {{PLAN_MODE_HINT}} value.
func planModeHint(kind string) string {
	switch kind {
	case string(KindClaude):
		return "press Shift+Tab until Plan Mode is shown"
	case string(KindOMP):
		return "run /plan"
	default:
		return "switch to Plan Mode"
	}
}

// planModeStep returns the per-host {{PLAN_MODE_STEP}} text for skill step 7:
// how the agent enters plan mode once the forge approves. Claude drives the
// EnterPlanMode tool itself; OMP gets the plan-mode session from the
// hook-registered /plan-forge command; AGY keeps the manual hint.
func planModeStep(kind string) string {
	switch kind {
	case string(KindClaude):
		return "Immediately call the `EnterPlanMode` tool, before any edit. In Plan Mode, read `docs/plans/.plan/synthesized_spec.md` as your authoritative blueprint, review the validation summary `wait` printed (valid, status, verified/total, drift), and verify the listed AST file targets. When the validation is invalid or missing, run `\"{{PLAN_FORGE_BIN}}\" validate --repo <repository root>` first. Present the execution checklist and call `ExitPlanMode` for the user's approval."
	case string(KindOMP):
		return "The managed hook sees the approved `wait` result and opens a plan-mode session seeded with the spec and the validation result; continue there. When the session API was unavailable, the hook has prefilled `/plan …` in the editor instead: ask the user to press Enter. In plan mode, read `docs/plans/.plan/synthesized_spec.md` as your authoritative blueprint, review the validation summary, verify the listed AST targets, and present the execution checklist."
	default:
		return "Immediately enter Plan Mode, ingest `docs/plans/.plan/synthesized_spec.md` as your authoritative blueprint, verify the listed AST file targets, and present the execution checklist. If you cannot switch modes yourself, ask the user to " + planModeHint(kind) + ", then continue."
	}
}

// substitute renders a template body with the install-time variables.
func (in *Installer) substitute(body, kind string) string {
	// strings.Replacer performs a single pass, so pre-substitute the binary
	// path inside the plan-mode step text.
	step := strings.ReplaceAll(planModeStep(kind), "{{PLAN_FORGE_BIN}}", in.bin)
	r := strings.NewReplacer(
		"{{PLAN_FORGE_BIN}}", in.bin,
		"{{HOST}}", kind,
		"{{WAIT_TIMEOUT}}", waitTimeout(kind),
		"{{PLAN_MODE_HINT}}", planModeHint(kind),
		"{{PLAN_MODE_STEP}}", step,
	)
	return r.Replace(body)
}

// Plan computes every artifact for the selected hosts and scope, without
// touching the filesystem.
func (in *Installer) Plan(ctx context.Context, opts InstallOptions) ([]HostReport, []artifact, error) {
	if err := validateOptions(opts); err != nil {
		return nil, nil, err
	}
	kinds, err := in.selectedHosts(ctx, opts.Host)
	if err != nil {
		return nil, nil, err
	}
	if opts.Host == "auto" && len(kinds) == 0 {
		return nil, nil, errNoHostFound{}
	}

	home, err := homeDir()
	if err != nil {
		return nil, nil, err
	}

	var reports []HostReport
	var artifacts []artifact
	for _, kind := range kinds {
		report := HostReport{Host: kind}
		if !in.hostPresent(ctx, kind) {
			report.Found = false
			reports = append(reports, report)
			continue
		}
		report.Found = true
		switch kind {
		case string(KindClaude):
			if v, err := Version(ctx, KindClaude); err == nil {
				report.Version = v
			}
			report.Auth, report.AuthCmd = in.authLabel(ctx, KindClaude)
			arts, err := in.planClaude(home, opts)
			if err != nil {
				return nil, nil, err
			}
			artifacts = append(artifacts, arts...)
			report.Targets = claudeTargets(opts.Scope)
		case string(KindOMP):
			if v, err := Version(ctx, KindOMP); err == nil {
				report.Version = v
			}
			report.Auth, report.AuthCmd = in.authLabel(ctx, KindOMP)
			arts, err := in.planOMP(home, opts)
			if err != nil {
				return nil, nil, err
			}
			artifacts = append(artifacts, arts...)
			report.Targets = ompTargets(opts.Scope)
		case string(KindAGY):
			// Generation signs in to the isolated profile, not the host's agy
			// (see the isolated agy profile section in docs/install.md), so the
			// host's own auth state says nothing here.
			report.Auth = "isolated profile"
			report.Note = "skills installed; generation signs in to the Plan Forge agy profile (plan-forge doctor shows how)"
			arts, err := in.planAGY(home, opts)
			if err != nil {
				return nil, nil, err
			}
			artifacts = append(artifacts, arts...)
			report.Targets = agyTargets(opts.Scope)
		}
		reports = append(reports, report)
	}

	if lines := ompDuplicateLines(home, ompProjectRepo(opts), artifacts); len(lines) > 0 {
		attachNotes(reports, lines)
	}
	if opts.GitHook {
		arts, err := in.planGitHook(opts)
		if err != nil {
			return nil, nil, err
		}
		artifacts = append(artifacts, arts...)
	}
	return reports, artifacts, nil
}

// validateOptions enforces the scope/repo rules before anything is planned.
func validateOptions(opts InstallOptions) error {
	switch opts.Scope {
	case "user", "project":
	default:
		return fmt.Errorf("unknown scope %q (supported: user, project)", opts.Scope)
	}
	if opts.Scope == "project" || opts.GitHook {
		if opts.Repo == "" {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			opts.Repo = cwd
		}
		abs, err := filepath.Abs(opts.Repo)
		if err != nil {
			return fmt.Errorf("resolve --repo: %w", err)
		}
		info, err := os.Stat(abs)
		if err != nil || !info.IsDir() {
			return &PathError{Path: abs, Reason: "is not an existing directory"}
		}
		if opts.Scope == "project" {
			if err := checkNoSymlinkComponents(abs); err != nil {
				return err
			}
		}
	}
	return nil
}

// authLabel renders the auth column: ready after a successful probe, unknown
// when the probe itself failed, missing with the login command.
func (in *Installer) authLabel(ctx context.Context, kind Kind) (label, cmd string) {
	adapter, ok := registeredMap(nil)[kind]
	if !ok {
		return "unknown", ""
	}
	state, err := adapter.Auth(ctx)
	if err != nil {
		return "unknown", ""
	}
	if state.Ready {
		return "ready", ""
	}
	return "missing", state.Detail
}

func claudeTargets(scope string) []string {
	if scope == "project" {
		return []string{".claude/skills/plan-forge, plan-config"}
	}
	return []string{"~/.claude/skills/plan-forge, plan-config, SessionStart/PreToolUse/PostToolUse hooks"}
}

func ompTargets(scope string) []string {
	if scope == "project" {
		return []string{".omp/skills/plan-forge, plan-config, .omp/hooks/pre/plan-forge.ts"}
	}
	return []string{"~/.omp/agent/skills/plan-forge, plan-config, ~/.omp/agent/hooks/pre/plan-forge.ts"}
}

func agyTargets(scope string) []string {
	if scope == "project" {
		return []string{".agents/skills/plan-forge, .agents/skills/plan-config"}
	}
	return []string{"~/.gemini/antigravity-cli/skills/..., ~/.gemini/config/skills/..."}
}

// planClaude plans the two SKILL.md files and the managed settings merge
// (SessionStart, PreToolUse, PostToolUse).
func (in *Installer) planClaude(home string, opts InstallOptions) ([]artifact, error) {
	root := filepath.Join(home, ".claude")
	if opts.Scope == "project" {
		root = filepath.Join(opts.Repo, ".claude")
	} else if err := in.checkHostRoot(root); err != nil {
		return nil, err
	}
	// The Claude copies carry `enabled: false`, and that marker is now the only
	// thing keeping them out of OMP: OMP installs no skills of its own, yet it
	// still reads ~/.claude/skills on opt-in and <repo>/.claude/skills by
	// default. An unmarked copy would surface the Claude-rendered body, whose
	// plan-mode step orders `EnterPlanMode` — a tool OMP does not have — beside
	// the /plan-forge command the hook registers. OMP's conventional scanners
	// skip a skill with that key (omp://skills.md, "SKILL.md frontmatter"), and
	// Claude Code ignores unknown frontmatter keys, so its own loading is
	// unaffected. The AGY copies are NOT marked: AGY reads neither key.
	forge := markHiddenFromOMP(in.substitute(integrations.SkillPlanForge(), string(KindClaude)))
	conf := markHiddenFromOMP(in.substitute(integrations.SkillPlanConfig(), string(KindClaude)))
	arts := []artifact{
		{
			path: filepath.Join(root, "skills", "plan-forge", "SKILL.md"),
			host: string(KindClaude), kind: kindSkill, content: forge,
		},
		{
			path: filepath.Join(root, "skills", "plan-config", "SKILL.md"),
			host: string(KindClaude), kind: kindSkill, content: conf,
		},
	}
	if opts.Scope == "user" {
		hookCommand := strings.ReplaceAll(claudeHookCommand, "{{PLAN_FORGE_BIN}}", in.bin)
		handoffCommand := strings.ReplaceAll(claudeHandoffCommand, "{{PLAN_FORGE_BIN}}", in.bin)
		guardCommand := strings.ReplaceAll(claudeGuardCommand, "{{PLAN_FORGE_BIN}}", in.bin)
		arts = append(arts, artifact{
			path:    filepath.Join(root, "settings.json"),
			host:    string(KindClaude),
			kind:    kindSettings,
			content: hookCommand,
			handoff: handoffCommand,
			guard:   guardCommand,
			patch:   &patchRecord{Anchor: "hooks.SessionStart"},
		})
	}
	return arts, nil
}

// planOMP plans the two SKILL.md files and the hook factory. OMP reads its own
// skill root, so it gets the same two skills as the other hosts, rendered for
// OMP. The hook adds what a skill cannot do there: the session-start status
// banner, and the plan-mode session once a run is approved. The slash commands
// an earlier version wrote under commands/ are removed by Apply.
func (in *Installer) planOMP(home string, opts InstallOptions) ([]artifact, error) {
	var skills, hooks string
	if opts.Scope == "project" {
		skills = filepath.Join(opts.Repo, ".omp", "skills")
		hooks = filepath.Join(opts.Repo, ".omp", "hooks", "pre")
	} else {
		root := filepath.Join(home, ".omp", "agent")
		if err := in.checkHostRoot(root); err != nil {
			return nil, err
		}
		skills = filepath.Join(root, "skills")
		hooks = filepath.Join(root, "hooks", "pre")
	}
	forge := in.substitute(integrations.SkillPlanForge(), string(KindOMP))
	conf := in.substitute(integrations.SkillPlanConfig(), string(KindOMP))
	hookBody := in.substitute(integrations.OMPHook(), string(KindOMP))
	return []artifact{
		{path: filepath.Join(skills, "plan-forge", "SKILL.md"), host: string(KindOMP), kind: kindSkill, content: forge},
		{path: filepath.Join(skills, "plan-config", "SKILL.md"), host: string(KindOMP), kind: kindSkill, content: conf},
		{path: filepath.Join(hooks, "plan-forge.ts"), host: string(KindOMP), kind: kindHookFactory, content: hookBody},
	}, nil
}

// planAGY plans both documented global skill locations (project scope writes
// .agents/skills only) and the global hooks.json SessionStart merge, whose
// schema is confirmed: {<name>: {"SessionStart": [{"type":"command","command":...,"timeout":...}]}}.
func (in *Installer) planAGY(home string, opts InstallOptions) ([]artifact, error) {
	forge := in.substitute(integrations.SkillPlanForge(), string(KindAGY))
	conf := in.substitute(integrations.SkillPlanConfig(), string(KindAGY))
	if opts.Scope == "project" {
		skills := filepath.Join(opts.Repo, ".agents", "skills")
		return []artifact{
			{path: filepath.Join(skills, "plan-forge", "SKILL.md"), host: string(KindAGY), kind: kindSkill, content: forge},
			{path: filepath.Join(skills, "plan-config", "SKILL.md"), host: string(KindAGY), kind: kindSkill, content: conf},
		}, nil
	}
	cliRoot := filepath.Join(home, ".gemini", "antigravity-cli")
	appRoot := filepath.Join(home, ".gemini", "config")
	if err := in.checkHostRoot(cliRoot); err != nil {
		return nil, err
	}
	if err := in.checkHostRoot(appRoot); err != nil {
		return nil, err
	}
	return []artifact{
		{path: filepath.Join(cliRoot, "skills", "plan-forge", "SKILL.md"), host: string(KindAGY), kind: kindSkill, content: forge},
		{path: filepath.Join(cliRoot, "skills", "plan-config", "SKILL.md"), host: string(KindAGY), kind: kindSkill, content: conf},
		{path: filepath.Join(appRoot, "skills", "plan-forge", "SKILL.md"), host: string(KindAGY), kind: kindSkill, content: forge},
		{path: filepath.Join(appRoot, "skills", "plan-config", "SKILL.md"), host: string(KindAGY), kind: kindSkill, content: conf},
		{
			path: filepath.Join(appRoot, "hooks.json"), host: string(KindAGY), kind: kindSettings,
			content: in.bin + " hook check-state --format agy", patch: &patchRecord{Anchor: "SessionStart"},
		},
	}, nil
}

// planGitHook plans the post-merge managed block. core.hooksPath resolving
// inside the worktree (tracked hooks, e.g. husky) or outside this repository's
// git dir (shared hooks) refuses with the manual snippet.
func (in *Installer) planGitHook(opts InstallOptions) ([]artifact, error) {
	repo := opts.Repo
	if repo == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		repo = cwd
	}
	hooksDir, gitDir, topLevel, err := gitHookPaths(repo)
	if err != nil {
		return nil, err
	}
	insideWorktree := topLevel != "" && pathWithin(hooksDir, topLevel) && !pathWithin(hooksDir, gitDir)
	if hooksDir != filepath.Join(gitDir, "hooks") && (insideWorktree || !pathWithin(hooksDir, gitDir)) {
		return nil, fmt.Errorf("%w\n%s", &PathError{
			Path:   hooksDir,
			Reason: "git hooks directory is tracked in the worktree or shared outside this repository's git dir",
		},
			gitHookSnippet(in.bin))
	}
	return []artifact{{
		path: filepath.Join(hooksDir, "post-merge"), kind: kindGitHook,
		content: renderGitHookBlock(in.bin),
	}}, nil
}

// gitHookPaths resolves the hooks directory, the absolute git dir and the
// worktree top-level through read-only git plumbing.
func gitHookPaths(repo string) (hooksDir, gitDir, topLevel string, err error) {
	run := func(args ...string) (string, error) {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		var out, errOut bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &errOut
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("git %s: %s", strings.Join(args, " "),
				strings.TrimSpace(errOut.String()))
		}
		return strings.TrimSpace(out.String()), nil
	}
	hooksRel, err := run("rev-parse", "--git-path", "hooks")
	if err != nil {
		return "", "", "", err
	}
	gitDir, err = run("rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", "", "", err
	}
	topLevel, topErr := run("rev-parse", "--show-toplevel")
	if topErr != nil {
		topLevel = ""
	}
	hooksDir = hooksRel
	if !filepath.IsAbs(hooksDir) {
		hooksDir = filepath.Join(gitDir, hooksRel)
	}
	return hooksDir, gitDir, topLevel, nil
}

// checkHostRoot enforces the user-scope symlink policy on a host root: an
// existing symlinked root must resolve to a user-owned, non-world-writable
// directory.
func (in *Installer) checkHostRoot(root string) error {
	info, err := os.Lstat(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil // absent root is created by the installer
		}
		return err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return nil
	}
	return checkSymlinkedRoot(root)
}

// ErrNoHostFound is the exit-3 sentinel for the command layer: auto mode
// found no supported host.
var ErrNoHostFound = errNoHostFound{}

// errNoHostFound is exit 3: auto mode found no supported host.
type errNoHostFound struct{}

func (errNoHostFound) Error() string {
	return "no supported agent host found. Supported hosts: claude, omp, agy. " +
		"Install one, or pass --host explicitly"
}

// manifestPath returns PlanForgeHome()/install-manifest.json.
func manifestPath() (string, error) {
	home, err := config.EnsureHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "install-manifest.json"), nil
}

// loadManifest reads the install manifest; a missing file is an empty one.
func loadManifest() (installManifest, string, error) {
	path, err := manifestPath()
	if err != nil {
		return installManifest{}, "", err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return installManifest{}, path, nil
	}
	if err != nil {
		return installManifest{}, path, err
	}
	var m installManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return installManifest{}, path, fmt.Errorf("decode %s: %w", path, err)
	}
	return m, path, nil
}

// saveManifest writes the manifest atomically with owner-only permissions.
func saveManifest(m installManifest, path string) error {
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomicPrivate(path, append(raw, '\n'), 0o600)
}

// writeAtomicPrivate writes content to path via a same-directory temp file
// and rename, with the given mode. The parent directory must exist. When path
// is a symlink within the user's home directory, its target is updated in
// place so dotfiles symlinks are preserved.
func writeAtomicPrivate(path string, content []byte, mode os.FileMode) error {
	target := path
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		resolved, rerr := filepath.EvalSymlinks(path)
		if rerr != nil {
			return rerr
		}
		if cerr := checkSymlinkedRoot(filepath.Dir(resolved)); cerr != nil {
			return cerr
		}
		target = resolved
	}
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, filepath.Base(target)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, target)
}

// sha256Hex hashes content.
func sha256Hex(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// backupPath returns PlanForgeHome()/backups/<sha256>/.
func backupPath(sum string) (string, error) {
	home, err := config.EnsureHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "backups", sum), nil
}

// Apply performs the planned writes with the manifest, backup and divergence
// rules. DryRun performs none of them.
func (in *Installer) Apply(arts []artifact, opts InstallOptions) error {
	manifest, manifestFile, err := loadManifest()
	if err != nil {
		return err
	}
	rowsByPath := make(map[string]manifestRow, len(manifest.Rows))
	for _, row := range manifest.Rows {
		rowsByPath[row.Path] = row
	}

	var newRows []manifestRow
	var modified *ErrInstallModified
	for _, art := range arts {
		row, writeErr := in.applyArtifact(art, rowsByPath[art.path], opts)
		if writeErr != nil {
			var modErr *ErrInstallModified
			if errors.As(writeErr, &modErr) {
				if opts.Force {
					row, forceErr := in.applyArtifactForce(art, opts)
					if forceErr != nil {
						return forceErr
					}
					newRows = append(newRows, row)
					continue
				}
				modified = modErr
				continue
			}
			return writeErr
		}
		if row.Path != "" {
			newRows = append(newRows, row)
		}
	}
	if modified != nil {
		return modified
	}
	if opts.DryRun {
		return nil
	}

	// Rows for paths this run no longer manages (another scope) survive;
	// only the current artifacts' rows are rewritten (uninstall --host all
	// removes the rest).
	kept := make([]manifestRow, 0, len(manifest.Rows))
	// OMP reached Plan Forge through commands/plan-forge.md and
	// commands/plan-config.md in earlier versions. Both are obsolete now that
	// OMP has the skills, and are swept only when this run plans OMP
	// artifacts, and only while the bytes still match the recorded hash: a copy
	// the user edited keeps its row and stays on disk.
	ompPlanned := false
	for _, art := range arts {
		if art.host == string(KindOMP) {
			ompPlanned = true
			break
		}
	}
	staleCommands := staleOMPCommands(arts)
	for _, row := range manifest.Rows {
		if rowReplaced(row.Path, arts) {
			continue
		}
		// The plan-forge-board skill was retired in 0.4.0. Its copy goes on the
		// next install, under the same hash check as every other sweep.
		if row.Kind == kindSkill && retiredSkill(row.Path) {
			gone, err := removeOwnedRow(row)
			if err != nil {
				return err
			}
			if gone {
				continue
			}
		}
		if ompPlanned && row.Host == string(KindOMP) &&
			row.Kind == kindCommand && staleCommands[row.Path] {
			gone, err := removeOwnedRow(row)
			if err != nil {
				return err
			}
			if gone {
				continue
			}
		}
		kept = append(kept, row)
	}
	newRows = append(newRows, kept...)
	sort.Slice(newRows, func(i, j int) bool { return newRows[i].Path < newRows[j].Path })
	manifest.Rows = newRows
	return saveManifest(manifest, manifestFile)
}

func rowReplaced(path string, arts []artifact) bool {
	for _, art := range arts {
		if art.path == path {
			return true
		}
	}
	return false
}

// staleOMPCommands lists the commands/*.md paths an OMP install from before
// the skills existed may have left behind: one pair per OMP root this run
// writes into. The root is derived from the hook artifact, whose path is
// always <root>/hooks/pre/plan-forge.ts.
func staleOMPCommands(arts []artifact) map[string]bool {
	stale := map[string]bool{}
	for _, art := range arts {
		if art.host != string(KindOMP) || art.kind != kindHookFactory {
			continue
		}
		root := filepath.Dir(filepath.Dir(filepath.Dir(art.path)))
		stale[filepath.Join(root, "commands", "plan-forge.md")] = true
		stale[filepath.Join(root, "commands", "plan-config.md")] = true
	}
	return stale
}

// removeOwnedRow deletes a manifest-owned file, but only while its content
// still matches the recorded hash. It reports whether the file is gone
// (removed now or already absent); a diverged file is left alone.
func removeOwnedRow(row manifestRow) (bool, error) {
	raw, err := os.ReadFile(row.Path)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if sha256Hex(raw) != row.SHA256 {
		return false, nil
	}
	if err := os.Remove(row.Path); err != nil {
		return false, err
	}
	// A skill lives in its own directory. Removing the SKILL.md leaves that
	// directory empty, so drop it too; os.Remove refuses a non-empty one, which
	// is exactly the guard wanted when the user keeps files of their own there.
	if row.Kind == kindSkill {
		_ = os.Remove(filepath.Dir(row.Path))
	}
	return true, nil
}

// applyArtifact writes one artifact following the manifest rules. A zero
// Path in the returned row means "identical content, skipped".
func (in *Installer) applyArtifact(art artifact, prev manifestRow, opts InstallOptions) (manifestRow, error) {
	switch art.kind {
	case kindSettings:
		return in.applySettings(art, prev, opts)
	case kindGitHook:
		return in.applyGitHook(art, prev, opts)
	}

	existing, err := os.ReadFile(art.path)
	switch {
	case err == nil:
	case errors.Is(err, os.ErrNotExist):
		existing = nil
	default:
		return manifestRow{}, err
	}

	if existing != nil {
		if string(existing) == art.content {
			return manifestRow{
				Path: art.path, Host: art.host, Kind: art.kind, Scope: opts.Scope,
				SHA256: sha256Hex(existing), Version: in.version,
			}, nil
		}
		if prev.Path == art.path && prev.SHA256 != "" {
			if sha256Hex(existing) != prev.SHA256 {
				suggested := art.path + ".plan-forge-new"
				if werr := writeExcl(suggested, []byte(art.content)); werr != nil && !errors.Is(werr, os.ErrExist) {
					return manifestRow{}, werr
				}
				return manifestRow{}, &ErrInstallModified{Path: art.path, SuggestedIn: suggested}
			}
		} else if prev.Path != art.path && !opts.Force {
			// Unowned pre-existing file at a template target: refuse unless --force.
			return manifestRow{}, &PathError{
				Path:   art.path,
				Reason: "exists and is not owned by plan-forge; pass --force to overwrite",
			}
		}
	}

	if opts.DryRun {
		return manifestRow{
			Path: art.path, Host: art.host, Kind: art.kind, Scope: opts.Scope,
			SHA256: sha256Hex([]byte(art.content)),
		}, nil
	}
	if err := ensureDir(filepath.Dir(art.path)); err != nil {
		return manifestRow{}, err
	}
	if err := writeAtomicPrivate(art.path, []byte(art.content), 0o644); err != nil {
		return manifestRow{}, err
	}
	return manifestRow{
		Path: art.path, Host: art.host, Kind: art.kind, Scope: opts.Scope,
		SHA256: sha256Hex([]byte(art.content)), Version: in.version,
	}, nil
}

// applyArtifactForce overwrites a diverged template artifact after --force.
// Merged files (settings, git hook) are re-merged instead: the divergence
// check is skipped but the user's other content is never overwritten.
func (in *Installer) applyArtifactForce(art artifact, opts InstallOptions) (manifestRow, error) {
	switch art.kind {
	case kindSettings:
		return in.applySettings(art, manifestRow{}, opts)
	case kindGitHook:
		return in.applyGitHook(art, manifestRow{}, opts)
	}
	if err := ensureDir(filepath.Dir(art.path)); err != nil {
		return manifestRow{}, err
	}
	if err := writeAtomicPrivate(art.path, []byte(art.content), 0o644); err != nil {
		return manifestRow{}, err
	}
	return manifestRow{
		Path: art.path, Host: art.host, Kind: art.kind, Scope: opts.Scope,
		SHA256: sha256Hex([]byte(art.content)), Version: in.version,
	}, nil
}

// applySettings merges our hook entry into a JSON settings file (Claude
// settings.json or AGY hooks.json), following the backup and divergence rules.
func (in *Installer) applySettings(art artifact, prev manifestRow, opts InstallOptions) (manifestRow, error) {
	raw, err := os.ReadFile(art.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return manifestRow{}, err
	}
	if errors.Is(err, os.ErrNotExist) {
		raw = nil
	}
	if raw != nil && prev.Path == art.path && prev.SHA256 != "" && prev.SHA256 != sha256Hex(raw) {
		return manifestRow{}, &ErrInstallModified{Path: art.path}
	}

	var settings map[string]any
	if raw != nil {
		if err := json.Unmarshal(raw, &settings); err != nil {
			return manifestRow{}, &settingsMergeError{msg: fmt.Sprintf(
				"%s is not valid JSON: %v\nmanual snippet:\n%s", art.path, err, manualHookSnippet,
			)}
		}
	} else {
		settings = map[string]any{}
	}

	var merged map[string]any
	var changed bool
	if art.host == string(KindClaude) {
		merged, changed, err = mergeClaudeHook(settings, art.content, art.guard, art.handoff)
	} else {
		merged, changed, err = mergeAGYHook(settings, art.content)
	}
	if err != nil {
		return manifestRow{}, err
	}
	if !changed {
		if raw == nil {
			return manifestRow{}, nil // nothing to merge into and nothing written
		}
		return manifestRow{
			Path: art.path, Host: art.host, Kind: kindSettings, Scope: opts.Scope,
			SHA256: sha256Hex(raw),
		}, nil
	}

	encoded, err := encodeSettings(merged)
	if err != nil {
		return manifestRow{}, err
	}
	if opts.DryRun {
		return manifestRow{
			Path: art.path, Host: art.host, Kind: kindSettings, Scope: opts.Scope,
			SHA256: sha256Hex(encoded), Patch: &patchRecord{
				Anchor:    art.patch.Anchor,
				PreSHA256: sha256Hex(raw),
			},
		}, nil
	}
	if raw != nil {
		if err := backup(raw); err != nil {
			return manifestRow{}, err
		}
		if err := writeAtomicPrivate(art.path, encoded, 0o600); err != nil {
			return manifestRow{}, err
		}
	} else if err := writeExcl(art.path, encoded); err != nil {
		return manifestRow{}, err
	}
	return manifestRow{
		Path: art.path, Host: art.host, Kind: kindSettings, Scope: opts.Scope,
		SHA256: sha256Hex(encoded), Patch: &patchRecord{
			Anchor:    art.patch.Anchor,
			PreSHA256: sha256Hex(raw),
		}, Version: in.version,
	}, nil
}

// writeExcl creates path exclusively (O_EXCL) with owner-only permissions.
func writeExcl(path string, content []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(content); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// backup stores pre-image content under PlanForgeHome()/backups/<sha256>/.
//
// The path is the content hash of the pre-image, so preserving the same
// content twice names the same file. A second preservation is therefore
// success, not a conflict: an uninstall or install that runs again over an
// unchanged settings file keeps the pre-image it already has. Any other
// existing file is a real conflict and fails closed.
func backup(raw []byte) error {
	dir, err := backupPath(sha256Hex(raw))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dir, "pre")
	err = writeExcl(path, raw)
	if errors.Is(err, os.ErrExist) {
		existing, readErr := os.ReadFile(path)
		if readErr == nil && bytes.Equal(existing, raw) {
			return nil
		}
		return err
	}
	return err
}

// applyGitHook appends the managed block to the post-merge hook, creating the
// file with #!/bin/sh and mode 0755 when absent.
func (in *Installer) applyGitHook(art artifact, prev manifestRow, opts InstallOptions) (manifestRow, error) {
	existing, err := os.ReadFile(art.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return manifestRow{}, err
	}
	if errors.Is(err, os.ErrNotExist) {
		existing = nil
	}
	if existing != nil && strings.Contains(string(existing), gitHookManagedHead) {
		return manifestRow{
			Path: art.path, Kind: kindGitHook, Scope: opts.Scope,
			SHA256: sha256Hex(existing),
		}, nil
	}
	if existing != nil && prev.Path == art.path && prev.SHA256 != "" && sha256Hex(existing) != prev.SHA256 {
		suggested := art.path + ".plan-forge-new"
		next := []byte(appendGitShebang(string(existing), art.content))
		if werr := writeExcl(suggested, next); werr != nil && !errors.Is(werr, os.ErrExist) {
			return manifestRow{}, werr
		}
		return manifestRow{}, &ErrInstallModified{Path: art.path, SuggestedIn: suggested}
	}

	content := appendGitShebang(string(existing), art.content)
	if opts.DryRun {
		return manifestRow{
			Path: art.path, Kind: kindGitHook, Scope: opts.Scope,
			SHA256: sha256Hex([]byte(content)),
		}, nil
	}
	if err := ensureDir(filepath.Dir(art.path)); err != nil {
		return manifestRow{}, err
	}
	if existing != nil {
		if err := backup(existing); err != nil {
			return manifestRow{}, err
		}
	}
	mode := os.FileMode(0o644)
	if existing == nil {
		mode = 0o755
	}
	if err := writeAtomicPrivate(art.path, []byte(content), mode); err != nil {
		return manifestRow{}, err
	}
	return manifestRow{
		Path: art.path, Kind: kindGitHook, Scope: opts.Scope,
		SHA256: sha256Hex([]byte(content)), Version: in.version,
	}, nil
}

// appendGitShebang prepends the shell header for a newly created hook file
// and otherwise appends the managed block after existing content.
func appendGitShebang(existing, block string) string {
	if existing == "" {
		return "#!/bin/sh\n" + gitHookManagedHead + "\n" + block + "\n" + gitHookManagedTail + "\n"
	}
	return appendManagedGitHook(existing, block)
}

// Uninstall removes only manifest-owned files with matching hashes and only
// our hook entries/managed blocks. It returns the removal report lines. With
// DryRun nothing is written or deleted; otherwise the manifest rows that no
// longer have anything to remove are pruned, so a later install starts clean.
func (in *Installer) Uninstall(opts InstallOptions) ([]string, error) {
	manifest, manifestFile, err := loadManifest()
	if err != nil {
		return nil, err
	}
	var removed []string
	kept := make([]manifestRow, 0, len(manifest.Rows))
	for _, row := range manifest.Rows {
		if opts.Host != "all" && row.Host != opts.Host {
			kept = append(kept, row)
			continue
		}
		line, keep, err := uninstallRow(row, opts.DryRun)
		if err != nil {
			return removed, err
		}
		if keep {
			kept = append(kept, row)
		}
		if line != "" {
			removed = append(removed, line)
		}
	}
	if opts.DryRun || len(kept) == len(manifest.Rows) {
		return removed, nil
	}
	manifest.Rows = kept
	return removed, saveManifest(manifest, manifestFile)
}

// uninstallRow removes one manifest row's managed content. It returns the
// report line (empty when nothing was removed) and whether the row must stay
// in the manifest (the file is still ours but could not be touched).
func uninstallRow(row manifestRow, dryRun bool) (line string, keep bool, err error) {
	raw, err := os.ReadFile(row.Path)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", true, err
	}
	switch row.Kind {
	case kindSettings:
		stripped, ours, splitErr := splitManagedHook(raw)
		if splitErr != nil {
			return "", true, nil // unparseable settings: leave file and row alone
		}
		if !ours {
			return "", false, nil
		}
		if !dryRun {
			encoded, err := encodeSettings(stripped)
			if err != nil {
				return "", true, err
			}
			if err := backup(raw); err != nil {
				return "", true, err
			}
			if err := writeAtomicPrivate(row.Path, encoded, 0o600); err != nil {
				return "", true, err
			}
		}
		return row.Path + " (hook entry removed)", false, nil
	case kindGitHook:
		stripped, ours := stripManagedGitHook(string(raw))
		if !ours {
			return "", false, nil
		}
		if !dryRun {
			if err := backup(raw); err != nil {
				return "", true, err
			}
			if strings.TrimSpace(stripped) == "#!/bin/sh" {
				if err := os.Remove(row.Path); err != nil {
					return "", true, err
				}
			} else if err := writeAtomicPrivate(row.Path, []byte(stripped), 0o755); err != nil {
				return "", true, err
			}
		}
		return row.Path + " (managed block removed)", false, nil
	default:
		if sha256Hex(raw) != row.SHA256 {
			// Diverged after install: do not delete what we cannot prove
			// we own byte-for-byte.
			return "", true, nil
		}
		if !dryRun {
			if err := os.Remove(row.Path); err != nil {
				return "", true, err
			}
		}
		return row.Path, false, nil
	}
}

// retiredSkills are skill directory names earlier versions installed and this
// version removes on install.
var retiredSkills = []string{"plan-forge-board"}

// retiredSkill reports whether path is the SKILL.md of a retired skill.
func retiredSkill(path string) bool {
	if filepath.Base(path) != "SKILL.md" {
		return false
	}
	dir := filepath.Base(filepath.Dir(path))
	for _, name := range retiredSkills {
		if dir == name {
			return true
		}
	}
	return false
}

// managedSkillNames are the skill directory names Plan Forge installs.
var managedSkillNames = []string{"plan-forge", "plan-config"}

// SkillRoot is one directory a host reads skills from.
type SkillRoot struct {
	Path   string   `json:"path"`
	Via    string   `json:"via"`
	Skills []string `json:"skills,omitempty"` // managed skill names OMP/the host lists from here
	Hidden []string `json:"hidden,omitempty"` // names OMP reads here but skips (enabled: false)
}

// SkillDuplicate is one managed skill name present in more than one root the
// same host surface reads.
type SkillDuplicate struct {
	Name      string   `json:"name"`
	Paths     []string `json:"paths"`
	Identical bool     `json:"identical"` // byte-identical bodies: OMP collapses them silently
}

// SkillRootView is what one host surface reads. AGY has two surfaces (CLI
// and app) that read different global roots; they are never compared.
type SkillRootView struct {
	Host       string           `json:"host"`
	Surface    string           `json:"surface,omitempty"`
	Roots      []SkillRoot      `json:"roots"`
	Duplicates []SkillDuplicate `json:"duplicates,omitempty"`
	OptIn      []string         `json:"opt_in_sources,omitempty"` // OMP settings files that enabled a foreign root
}

// SkillDuplicateRemedy is the advice printed next to every visible duplicate.
// Plan Forge never edits the user's OMP settings itself.
const SkillDuplicateRemedy = `add ignoredSkills: ["plan-forge*"] to your OMP settings, or remove the foreign provider from enabledProviders (or set its enableClaudeUser/enableAgentsUser/enableClaudeProject/enableAgentsProject toggle to false); plan-forge does not edit that file`

// ompSkillSettings is the part of the user's OMP configuration that decides
// which foreign skill roots OMP reads.
type ompSkillSettings struct {
	ClaudeUser, AgentsUser, CodexUser, PiUser bool // opt-in user roots
	ClaudeProject, AgentsProject              bool // default true; false when explicitly disabled
	CustomDirs                                []string
	Sources                                   []string // files that enabled something
}

// rawSkillCfg is one parsed settings file.
type rawSkillCfg struct {
	enabledProviders []string
	flags            map[string]bool
	customDirs       []string
}

// readOMPSkillSettings reads ~/.omp/agent/{settings.json,config.yml,config.yaml}
// and, when repo is set, <repo>/.omp/ the same way. It only reads.
// Keys: enabledProviders (claude|agents|codex), enableClaudeUser,
// enableAgentsUser, enableCodexUser, enablePiUser, enableClaudeProject,
// enableAgentsProject and skills.customDirectories, each either top level or
// under skills:.
func readOMPSkillSettings(home, repo string) ompSkillSettings {
	s := ompSkillSettings{ClaudeProject: true, AgentsProject: true}
	dirs := []string{filepath.Join(home, ".omp", "agent")}
	if repo != "" {
		dirs = append(dirs, filepath.Join(repo, ".omp"))
	}
	for _, dir := range dirs {
		for _, name := range []string{"settings.json", "config.yml", "config.yaml"} {
			path := filepath.Join(dir, name)
			info, err := os.Stat(path)
			if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
				continue
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			var cfg rawSkillCfg
			if strings.HasSuffix(name, ".json") {
				cfg = parseOMPSkillJSON(raw)
			} else {
				cfg = parseOMPSkillYAML(raw)
			}
			s.apply(cfg, path, home, repo)
		}
	}
	return s
}

func (s *ompSkillSettings) apply(cfg rawSkillCfg, path, home, repo string) {
	enabled := false
	for _, p := range cfg.enabledProviders {
		switch strings.ToLower(strings.TrimSpace(p)) {
		case "claude":
			s.ClaudeUser, enabled = true, true
		case "agents":
			s.AgentsUser, enabled = true, true
		case "codex":
			s.CodexUser, enabled = true, true
		}
	}
	for key, on := range cfg.flags {
		switch key {
		case "enableClaudeUser":
			if on {
				s.ClaudeUser, enabled = true, true
			}
		case "enableAgentsUser":
			if on {
				s.AgentsUser, enabled = true, true
			}
		case "enableCodexUser":
			if on {
				s.CodexUser, enabled = true, true
			}
		case "enablePiUser":
			if on {
				s.PiUser, enabled = true, true
			}
		case "enableClaudeProject":
			s.ClaudeProject = on
		case "enableAgentsProject":
			s.AgentsProject = on
		}
	}
	for _, dir := range cfg.customDirs {
		if resolved := resolveSkillDir(dir, home, repo); resolved != "" {
			s.CustomDirs = append(s.CustomDirs, resolved)
			enabled = true
		}
	}
	if enabled {
		s.Sources = append(s.Sources, path)
	}
}

func resolveSkillDir(dir, home, repo string) string {
	dir = strings.TrimSpace(dir)
	switch {
	case dir == "":
		return ""
	case dir == "~" || strings.HasPrefix(dir, "~/"):
		return filepath.Join(home, strings.TrimPrefix(dir, "~"))
	case filepath.IsAbs(dir):
		return filepath.Clean(dir)
	case repo != "":
		return filepath.Join(repo, dir)
	}
	return ""
}

var skillSettingFlags = map[string]bool{
	"enableClaudeUser": true, "enableAgentsUser": true, "enableCodexUser": true, "enablePiUser": true,
	"enableClaudeProject": true, "enableAgentsProject": true,
}

func parseOMPSkillJSON(raw []byte) rawSkillCfg {
	cfg := rawSkillCfg{flags: map[string]bool{}}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return cfg
	}
	strs := func(v any) []string {
		var out []string
		if list, ok := v.([]any); ok {
			for _, item := range list {
				if s, ok := item.(string); ok {
					out = append(out, s)
				}
			}
		}
		return out
	}
	cfg.enabledProviders = strs(root["enabledProviders"])
	scopes := []map[string]any{root}
	if skills, ok := root["skills"].(map[string]any); ok {
		scopes = append(scopes, skills)
	}
	for _, m := range scopes {
		for key := range skillSettingFlags {
			if b, ok := m[key].(bool); ok {
				cfg.flags[key] = b
			}
		}
		cfg.customDirs = append(cfg.customDirs, strs(m["customDirectories"])...)
	}
	return cfg
}

// parseOMPSkillYAML is a line scanner for the handful of keys above. A
// nested key counts only at the top level or under skills:, so the unrelated
// commands.enableClaudeUser toggle is ignored.
func parseOMPSkillYAML(raw []byte) rawSkillCfg {
	cfg := rawSkillCfg{flags: map[string]bool{}}
	section, list, listIndent := "", "", 0
	scalar := func(v string) string { return strings.Trim(strings.TrimSpace(v), `"'`) }
	add := func(key, v string) {
		switch key {
		case "enabledProviders":
			cfg.enabledProviders = append(cfg.enabledProviders, scalar(v))
		case "customDirectories":
			cfg.customDirs = append(cfg.customDirs, scalar(v))
		}
	}
	for _, line := range strings.Split(string(raw), "\n") {
		text := strings.TrimRight(line, " \t\r")
		trimmed := strings.TrimSpace(text)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		trimmed = strings.TrimSpace(strings.SplitN(trimmed, " #", 2)[0])
		indent := len(text) - len(strings.TrimLeft(text, " "))
		if list != "" && indent >= listIndent && strings.HasPrefix(trimmed, "-") {
			add(list, strings.TrimPrefix(trimmed, "-"))
			continue
		}
		list = ""
		key, val, ok := strings.Cut(trimmed, ":")
		if !ok {
			continue
		}
		key, val = strings.TrimSpace(key), strings.TrimSpace(val)
		if indent == 0 {
			section = ""
			if val == "" {
				section = key
			}
		}
		if indent != 0 && section != "skills" {
			continue
		}
		switch {
		case key == "enabledProviders" || key == "customDirectories":
			if val == "" {
				list, listIndent = key, indent
			} else if strings.HasPrefix(val, "[") && strings.HasSuffix(val, "]") {
				for _, item := range strings.Split(val[1:len(val)-1], ",") {
					if strings.TrimSpace(item) != "" {
						add(key, item)
					}
				}
			}
		case skillSettingFlags[key]:
			if val == "true" || val == "false" {
				cfg.flags[key] = val == "true"
			}
		}
	}
	return cfg
}

type readRoot struct{ Path, Via string }

type skillSurface struct {
	Name  string
	Roots []readRoot
}

// hostSurfaces lists the skill roots each surface of a host reads. repo ""
// means no project scope. Claude reads only .claude roots. OMP reads its own
// .omp roots, the Claude and Agents project roots by default, and the user
// roots of a foreign provider only after the user opted in. AGY's CLI and
// app surfaces read different global roots and share the project root.
func hostSurfaces(kind, home, repo string, s ompSkillSettings) []skillSurface {
	var roots []readRoot
	add := func(path, via string) { roots = append(roots, readRoot{filepath.Join(path), via}) }
	switch kind {
	case string(KindClaude):
		add(filepath.Join(home, ".claude", "skills"), "user")
		if repo != "" {
			add(filepath.Join(repo, ".claude", "skills"), "project")
		}
	case string(KindOMP):
		add(filepath.Join(home, ".omp", "agent", "skills"), "native user")
		if repo != "" {
			add(filepath.Join(repo, ".omp", "skills"), "native project")
			if s.ClaudeProject {
				add(filepath.Join(repo, ".claude", "skills"), "claude project (read by default)")
			}
			if s.AgentsProject {
				add(filepath.Join(repo, ".agents", "skills"), "agents project (read by default)")
			}
		}
		if s.ClaudeUser {
			add(filepath.Join(home, ".claude", "skills"), "claude user (opted in)")
		}
		if s.AgentsUser {
			add(filepath.Join(home, ".agents", "skills"), "agents user (opted in)")
		}
		if s.CodexUser {
			add(filepath.Join(home, ".codex", "skills"), "codex user (opted in)")
		}
		for _, dir := range s.CustomDirs {
			add(dir, "skills.customDirectories")
		}
	case string(KindAGY):
		var project []readRoot
		if repo != "" {
			project = []readRoot{{filepath.Join(repo, ".agents", "skills"), "project"}}
		}
		return []skillSurface{
			{"cli", append([]readRoot{{filepath.Join(home, ".gemini", "antigravity-cli", "skills"), "user"}}, project...)},
			{"app", append([]readRoot{{filepath.Join(home, ".gemini", "config", "skills"), "user"}}, project...)},
		}
	}
	return []skillSurface{{Roots: roots}}
}

// skillBodies resolves the SKILL.md a root would hold for a name: the planned
// content when an artifact targets that path, otherwise what is on disk.
func skillBody(root, name string, planned map[string][]byte) ([]byte, bool) {
	path := filepath.Join(root, name, "SKILL.md")
	if body, ok := planned[path]; ok {
		return body, true
	}
	body, err := os.ReadFile(path)
	return body, err == nil
}

// markHiddenFromOMP adds `enabled: false` as the last frontmatter key. A body
// without frontmatter, or one that already sets `enabled`, is returned as is.
func markHiddenFromOMP(body string) string {
	if !strings.HasPrefix(body, "---\n") {
		return body
	}
	end := strings.Index(body[4:], "\n---\n")
	if end < 0 {
		return body
	}
	front := body[4 : 4+end+1]
	for _, line := range strings.Split(front, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "enabled:") {
			return body
		}
	}
	at := 4 + end + 1
	return body[:at] + "enabled: false\n" + body[at:]
}

// hiddenFromOMP reports whether a SKILL.md frontmatter carries `enabled: false`.
func hiddenFromOMP(body []byte) bool {
	text := string(body)
	if !strings.HasPrefix(text, "---\n") {
		return false
	}
	end := strings.Index(text[4:], "\n---")
	if end < 0 {
		return false
	}
	for _, line := range strings.Split(text[4:4+end], "\n") {
		if strings.TrimSpace(line) == "enabled: false" {
			return true
		}
	}
	return false
}

// inspectSurface lists a surface's roots and the managed names present in
// more than one of them. For OMP, a copy marked `enabled: false` is skipped
// by OMP's scanners, so it is reported as hidden and never as a duplicate.
func inspectSurface(host string, sf skillSurface, planned map[string][]byte) SkillRootView {
	view := SkillRootView{Host: host, Surface: sf.Name, Roots: []SkillRoot{}}
	seen := map[string]bool{}
	type copyOf struct {
		path string
		body []byte
	}
	copies := map[string][]copyOf{}
	for _, r := range sf.Roots {
		key := r.Path
		if resolved, err := filepath.EvalSymlinks(r.Path); err == nil {
			key = resolved
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		root := SkillRoot{Path: r.Path, Via: r.Via}
		for _, name := range managedSkillNames {
			if body, ok := skillBody(r.Path, name, planned); ok {
				if host == string(KindOMP) && hiddenFromOMP(body) {
					root.Hidden = append(root.Hidden, name)
					continue
				}
				root.Skills = append(root.Skills, name)
				path := filepath.Join(r.Path, name, "SKILL.md")
				copies[name] = append(copies[name], copyOf{path, body})
			}
		}
		view.Roots = append(view.Roots, root)
	}
	for _, name := range managedSkillNames {
		list := copies[name]
		if len(list) < 2 {
			continue
		}
		dup := SkillDuplicate{Name: name, Identical: true}
		for _, c := range list {
			dup.Paths = append(dup.Paths, c.path)
			if !bytes.Equal(c.body, list[0].body) {
				dup.Identical = false
			}
		}
		view.Duplicates = append(view.Duplicates, dup)
	}
	return view
}

// InspectSkillRoots is the read-only view behind `plan-forge doctor`: the
// skill roots each given host kind reads and the managed skill names present
// in more than one root of the same surface. repo "" skips project roots.
func InspectSkillRoots(kinds []string, repo string) ([]SkillRootView, error) {
	home, err := homeDir()
	if err != nil {
		return nil, err
	}
	settings := readOMPSkillSettings(home, repo)
	var views []SkillRootView
	for _, kind := range kinds {
		for _, sf := range hostSurfaces(kind, home, repo, settings) {
			view := inspectSurface(kind, sf, nil)
			if kind == string(KindOMP) {
				view.OptIn = settings.Sources
			}
			views = append(views, view)
		}
	}
	return views, nil
}

func ompProjectRepo(opts InstallOptions) string {
	if opts.Scope == "project" {
		return opts.Repo
	}
	return ""
}

func plannedSkillBodies(arts []artifact) map[string][]byte {
	planned := map[string][]byte{}
	for _, art := range arts {
		if art.kind == kindSkill {
			planned[art.path] = []byte(art.content)
		}
	}
	return planned
}

// ompDuplicateLines returns one warning per managed skill that OMP would list
// twice with differing bodies once the planned artifacts are written. Two
// identical bodies collapse silently, so they are not reported. A foreign user
// root counts only when the user opted in; nothing is edited.
func ompDuplicateLines(home, repo string, arts []artifact) []string {
	settings := readOMPSkillSettings(home, repo)
	planned := plannedSkillBodies(arts)
	var lines []string
	for _, sf := range hostSurfaces(string(KindOMP), home, repo, settings) {
		for _, d := range inspectSurface(string(KindOMP), sf, planned).Duplicates {
			if d.Identical {
				continue
			}
			lines = append(lines, fmt.Sprintf("warning: OMP would list skill %q from %s with different bodies; %s", d.Name, strings.Join(d.Paths, " and "), SkillDuplicateRemedy))
		}
	}
	return lines
}

// attachNotes puts report lines on the OMP row, else on the first found row,
// so they print in both real and --dry-run output.
func attachNotes(reports []HostReport, lines []string) {
	target := -1
	for i, r := range reports {
		if !r.Found {
			continue
		}
		if target < 0 {
			target = i
		}
		if r.Host == string(KindOMP) {
			target = i
			break
		}
	}
	if target < 0 {
		return
	}
	note := reports[target].Note
	for _, l := range lines {
		if note != "" {
			note += "\n    "
		}
		note += l
	}
	reports[target].Note = note
}

// homeDir resolves the user's home directory for host roots.
//
// $HOME wins when it is set. os.UserHomeDir reads the passwd entry on some
// platforms, which would reach outside a sandbox: a test, a container or a
// PLAN_FORGE_HOME-scoped run must see the home it was given, not the
// machine's. Host agents themselves honour $HOME, so using it here keeps the
// two in agreement.
func homeDir() (string, error) {
	if home, ok := os.LookupEnv("HOME"); ok && home != "" {
		return home, nil
	}
	return os.UserHomeDir()
}

// GitHookTimeout bounds nothing here; it documents the 2s hook budget shared
// with check-state. Reserved for future use by the hook command.
const GitHookTimeout = 2 * time.Second
