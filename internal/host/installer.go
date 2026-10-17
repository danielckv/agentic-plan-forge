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

// artifact is one planned write.
type artifact struct {
	path    string
	host    string
	kind    manifestKind
	content string
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
// host whose binary is found or (for agy) whose config directories exist.
func (in *Installer) selectedHosts(ctx context.Context, flag string) ([]string, error) {
	switch flag {
	case "claude", "omp", "agy":
		return []string{flag}, nil
	case "all":
		return []string{string(KindClaude), string(KindOMP), string(KindAGY)}, nil
	case "auto":
		var found []string
		for _, kind := range []string{string(KindClaude), string(KindOMP), string(KindAGY)} {
			if in.hostPresent(ctx, kind) {
				found = append(found, kind)
			}
		}
		return found, nil
	default:
		return nil, fmt.Errorf("unknown host %q (supported: auto, claude, omp, agy, all)", flag)
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

// substitute renders a template body with the install-time variables.
func (in *Installer) substitute(body, kind string) string {
	r := strings.NewReplacer(
		"{{PLAN_FORGE_BIN}}", in.bin,
		"{{HOST}}", kind,
		"{{WAIT_TIMEOUT}}", waitTimeout(kind),
		"{{PLAN_MODE_HINT}}", planModeHint(kind),
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
			// No verified auth probe exists for Antigravity generation.
			report.Auth = "unknown"
			report.Note = "skills installed; avatar generation needs Claude Code or OMP"
			arts, err := in.planAGY(home, opts)
			if err != nil {
				return nil, nil, err
			}
			artifacts = append(artifacts, arts...)
			report.Targets = agyTargets(opts.Scope)
		}
		reports = append(reports, report)
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
		return []string{".claude/skills/plan-forge, .claude/skills/plan-config"}
	}
	return []string{"~/.claude/skills/plan-forge, ~/.claude/skills/plan-config, SessionStart hook"}
}

func ompTargets(scope string) []string {
	if scope == "project" {
		return []string{".omp/skills/..., .omp/commands/plan-forge.md, plan-config.md, hooks/pre/plan-forge.ts"}
	}
	return []string{"~/.omp/agent/skills/..., ~/.omp/agent/commands/plan-forge.md, plan-config.md, hooks/pre/plan-forge.ts"}
}

func agyTargets(scope string) []string {
	if scope == "project" {
		return []string{".agents/skills/plan-forge, .agents/skills/plan-config"}
	}
	return []string{"~/.gemini/antigravity-cli/skills/..., ~/.gemini/config/skills/..."}
}

// planClaude plans the two SKILL.md files and the SessionStart settings merge.
func (in *Installer) planClaude(home string, opts InstallOptions) ([]artifact, error) {
	root := filepath.Join(home, ".claude")
	if opts.Scope == "project" {
		root = filepath.Join(opts.Repo, ".claude")
	} else if err := in.checkHostRoot(root); err != nil {
		return nil, err
	}
	forge := in.substitute(integrations.SkillPlanForge(), string(KindClaude))
	conf := in.substitute(integrations.SkillPlanConfig(), string(KindClaude))
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
		arts = append(arts, artifact{
			path:    filepath.Join(root, "settings.json"),
			host:    string(KindClaude),
			kind:    kindSettings,
			content: hookCommand,
			patch:   &patchRecord{Anchor: "hooks.SessionStart"},
		})
	}
	return arts, nil
}

// planOMP plans two skills, two slash commands and the hook factory.
func (in *Installer) planOMP(home string, opts InstallOptions) ([]artifact, error) {
	var skills, commands, hooks string
	if opts.Scope == "project" {
		skills = filepath.Join(opts.Repo, ".omp", "skills")
		commands = filepath.Join(opts.Repo, ".omp", "commands")
		hooks = filepath.Join(opts.Repo, ".omp", "hooks", "pre")
	} else {
		root := filepath.Join(home, ".omp", "agent")
		if err := in.checkHostRoot(root); err != nil {
			return nil, err
		}
		skills = filepath.Join(root, "skills")
		commands = filepath.Join(root, "commands")
		hooks = filepath.Join(root, "hooks", "pre")
	}
	forge := in.substitute(integrations.SkillPlanForge(), string(KindOMP))
	conf := in.substitute(integrations.SkillPlanConfig(), string(KindOMP))
	hookBody := in.substitute(integrations.OMPHook(), string(KindOMP))
	return []artifact{
		{path: filepath.Join(skills, "plan-forge", "SKILL.md"), host: string(KindOMP), kind: kindSkill, content: forge},
		{path: filepath.Join(skills, "plan-config", "SKILL.md"), host: string(KindOMP), kind: kindSkill, content: conf},
		{path: filepath.Join(commands, "plan-forge.md"), host: string(KindOMP), kind: kindCommand, content: forge},
		{path: filepath.Join(commands, "plan-config.md"), host: string(KindOMP), kind: kindCommand, content: conf},
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
	// Rebuild: current artifacts' rows plus rows from other scopes we leave
	// alone (uninstall --host all handles them).
	kept := make([]manifestRow, 0, len(manifest.Rows))
	for _, row := range manifest.Rows {
		if rowReplaced(row.Path, arts) {
			continue
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
		} else if prev.Path != art.path {
			// Unowned pre-existing file at a template target: refuse.
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
func (in *Installer) applyArtifactForce(art artifact, opts InstallOptions) (manifestRow, error) {
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
		merged, changed, err = mergeClaudeHook(settings, art.content)
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
func backup(raw []byte) error {
	dir, err := backupPath(sha256Hex(raw))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return writeExcl(filepath.Join(dir, "pre"), raw)
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
// our hook entries/managed blocks. It returns the removal report lines.
func (in *Installer) Uninstall(opts InstallOptions) ([]string, error) {
	manifest, _, err := loadManifest()
	if err != nil {
		return nil, err
	}
	var removed []string
	for _, row := range manifest.Rows {
		if opts.Host != "all" && row.Host != opts.Host {
			continue
		}
		raw, err := os.ReadFile(row.Path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return removed, err
		}
		switch row.Kind {
		case kindSettings:
			stripped, ours, err := splitManagedHook(raw)
			if err != nil || !ours {
				continue
			}
			encoded, err := encodeSettings(stripped)
			if err != nil {
				return removed, err
			}
			if err := backup(raw); err != nil {
				return removed, err
			}
			if err := writeAtomicPrivate(row.Path, encoded, 0o600); err != nil {
				return removed, err
			}
			removed = append(removed, row.Path+" (hook entry removed)")
		case kindGitHook:
			stripped, ours := stripManagedGitHook(string(raw))
			if !ours {
				continue
			}
			if err := backup(raw); err != nil {
				return removed, err
			}
			if strings.TrimSpace(stripped) == "#!/bin/sh" {
				if err := os.Remove(row.Path); err != nil {
					return removed, err
				}
			} else if err := writeAtomicPrivate(row.Path, []byte(stripped), 0o755); err != nil {
				return removed, err
			}
			removed = append(removed, row.Path+" (managed block removed)")
		default:
			if sha256Hex(raw) != row.SHA256 {
				// Diverged after install: do not delete what we cannot prove
				// we own byte-for-byte.
				continue
			}
			if err := os.Remove(row.Path); err != nil {
				return removed, err
			}
			removed = append(removed, row.Path)
		}
	}
	return removed, nil
}

// homeDir resolves the user's home directory for host roots.
func homeDir() (string, error) {
	return os.UserHomeDir()
}

// GitHookTimeout bounds nothing here; it documents the 2s hook budget shared
// with check-state. Reserved for future use by the hook command.
const GitHookTimeout = 2 * time.Second
