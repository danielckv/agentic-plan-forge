//go:build client && !server

package host

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/danielckv/agentic-plan-forge/internal/config"
	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// agyProfileDir is the isolated agy home: $PLAN_FORGE_HOME/profiles/agy. The
// directory is the HOME every agy child process runs against, so its sign-in
// state (~/.gemini/oauth_creds.json and friends) and its settings stay inside
// the Plan Forge home and never touch the host's real ~/.gemini. No XDG_*
// override is needed: unset, those variables derive from HOME.
func agyProfileDir() (string, error) {
	home, err := config.Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "profiles", "agy"), nil
}

// agyProfileSettingsPath is where the agy CLI reads its settings when HOME is
// the profile directory: <profile>/.gemini/antigravity-cli/settings.json.
func agyProfileSettingsPath(profileDir string) string {
	return filepath.Join(profileDir, ".gemini", "antigravity-cli", "settings.json")
}

// agyManagedSettings is the settings document Plan Forge writes into a fresh
// profile. The deny rules fence generation off from every tool family, which
// is what makes the tool-free receipt enforceable. validateAGYIsolation
// re-reads and re-checks this policy before every spawn, so an edit weakens
// nothing: it fails closed instead.
func agyManagedSettings() []byte {
	rules := make([]byte, 0, 256)
	rules = append(rules, []byte("{\n  \"permissions\": {\n    \"deny\": [\n")...)
	for i, rule := range requiredAGYDenyRules {
		sep := ",\n"
		if i == len(requiredAGYDenyRules)-1 {
			sep = "\n"
		}
		rules = append(rules, []byte("      "+strconv.Quote(rule)+sep)...)
	}
	return append(rules, []byte("    ]\n  }\n}\n")...)
}

// ensureAGYProfile provisions the isolated profile and returns the verified
// isolation selection. Provisioning creates only what is missing: an existing
// settings file is left alone (Preflight's validateAGYIsolation enforces the
// deny policy on it and fails closed on any drift). Everything created lives
// under the Plan Forge home, mode 0700 for directories and 0600 for settings.
func ensureAGYProfile() (*agyIsolation, error) {
	dir, err := agyProfileDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create agy profile %s: %w", dir, err)
	}
	if info, err := os.Stat(dir); err != nil {
		return nil, err
	} else if !info.IsDir() {
		return nil, fmt.Errorf("agy profile %s is not a directory", dir)
	} else if info.Mode().Perm()&0o077 != 0 {
		// The profile holds sign-in credentials; a directory another user can
		// read leaks them. Plan Forge owns this path, so tighten it.
		if err := os.Chmod(dir, 0o700); err != nil {
			return nil, fmt.Errorf("tighten agy profile %s: %w", dir, err)
		}
	}
	settingsPath := agyProfileSettingsPath(dir)
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o700); err != nil {
		return nil, fmt.Errorf("create agy settings directory: %w", err)
	}
	if _, err := os.Lstat(settingsPath); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(settingsPath, agyManagedSettings(), 0o600); err != nil {
			return nil, fmt.Errorf("write agy isolated settings: %w", err)
		}
	} else if err != nil {
		return nil, err
	}
	return &agyIsolation{homeDir: dir, settingsPath: settingsPath, selectionVerified: true}, nil
}

// ensureIsolation attaches the verified isolated profile to the adapter,
// provisioning it on first use. Failure keeps the host fail-closed: the
// capability error names what could not be prepared instead of falling back
// to the host's real home.
func (a *AGY) ensureIsolation() error {
	if a.isolation != nil {
		return nil
	}
	isolation, err := ensureAGYProfile()
	if err != nil {
		return &contracts.ErrCapability{
			Host:   string(KindAGY),
			Reason: "could not prepare the isolated agy profile: " + err.Error(),
			Remedy: "check permissions on the Plan Forge home, or use Claude Code or OMP",
		}
	}
	a.isolation = isolation
	return nil
}

// isolatedEnvironment rewrites HOME and USERPROFILE at the profile directory,
// the narrowest override that keeps every agy read and write inside it. XDG
// variables need no entry: unset, they derive from HOME, so they resolve
// inside the profile too.
func isolatedEnvironment(environment []string, homeDir string) []string {
	environment = replaceEnvironment(environment, "HOME", homeDir)
	return replaceEnvironment(environment, "USERPROFILE", homeDir)
}

// childEnv assembles the environment every agy child process runs with: the
// common allowlist plus the isolated HOME. It is the single seam Generate and
// the auth/models probes share, so a probe answers for the same sign-in state
// a generation run sees.
func (a *AGY) childEnv(cwd string) ([]string, error) {
	if err := a.ensureIsolation(); err != nil {
		return nil, err
	}
	return isolatedEnvironment(childEnvironment(cwd, a, a.extraEnv), a.isolation.homeDir), nil
}

// runIsolatedProbe is runProbe against the isolated profile.
func (a *AGY) runIsolatedProbe(ctx context.Context, args ...string) ([]byte, []byte, error) {
	path, err := lookPath(a.binary)
	if err != nil {
		return nil, nil, err
	}
	dir, err := newRunDir()
	if err != nil {
		return nil, nil, err
	}
	defer os.RemoveAll(dir)
	env, err := a.childEnv(dir)
	if err != nil {
		return nil, nil, err
	}
	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	return runCaptured(probeCtx, path, args, dir, env, nil)
}

// agySignInHint is the remedy a user runs to sign in to the isolated profile.
// Signing in to the host's own agy does nothing for Plan Forge: generation
// only ever sees the profile.
func agySignInHint(homeDir string) string {
	return fmt.Sprintf("sign in to the Plan Forge agy profile: HOME=%q agy", homeDir)
}
