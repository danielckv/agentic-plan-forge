package config

import (
	"errors"
	"os"
	"path/filepath"
)

// markerName is written when Plan Forge first creates its home directory. It is
// the guard that stops `uninstall --purge` from deleting a directory Plan Forge
// does not own, such as a user's home directory when PLAN_FORGE_HOME=$HOME.
const markerName = ".plan-forge"

// ErrNotOwned reports that a directory lacks the Plan Forge marker file.
var ErrNotOwned = errors.New("directory is not owned by plan-forge (missing " + markerName + ")")

// ErrHomeMustBeAbsolute reports a relative PLAN_FORGE_HOME.
var ErrHomeMustBeAbsolute = errors.New("PLAN_FORGE_HOME must be an absolute path")

// Home returns the Plan Forge home directory: $PLAN_FORGE_HOME when set, else
// ~/.plan-forge. The override exists for test and install isolation, so it must
// be absolute; a relative value would silently place state in the working tree.
func Home() (string, error) {
	if v := os.Getenv("PLAN_FORGE_HOME"); v != "" {
		if !filepath.IsAbs(v) {
			return "", ErrHomeMustBeAbsolute
		}
		return filepath.Clean(v), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".plan-forge"), nil
}

// Cache returns the Plan Forge cache directory: $PLAN_FORGE_HOME/cache when the
// override is set, else os.UserCacheDir()/plan-forge.
func Cache() (string, error) {
	if v := os.Getenv("PLAN_FORGE_HOME"); v != "" {
		if !filepath.IsAbs(v) {
			return "", ErrHomeMustBeAbsolute
		}
		return filepath.Join(filepath.Clean(v), "cache"), nil
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "plan-forge"), nil
}

// EnsureHome creates the home directory and its marker file when missing, and
// returns the directory. The marker is what makes the directory provably ours.
func EnsureHome() (string, error) {
	dir, err := Home()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	marker := filepath.Join(dir, markerName)
	if _, err := os.Stat(marker); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(marker, []byte("plan-forge\n"), 0o600); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	}
	return dir, nil
}

// EnsureCache creates the cache directory when missing.
func EnsureCache() (string, error) {
	dir, err := Cache()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// VerifyOwned reports whether dir is a Plan Forge home directory. Purge refuses
// to delete anything when this fails.
func VerifyOwned(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return ErrNotOwned
	}
	if _, err := os.Stat(filepath.Join(dir, markerName)); err != nil {
		return ErrNotOwned
	}
	return nil
}

// IsUnsafePurgeTarget reports whether purging dir would touch a location that is
// never Plan Forge's to delete: the filesystem root, the user's home directory,
// or an ancestor of the working directory.
func IsUnsafePurgeTarget(dir string) bool {
	clean := filepath.Clean(dir)
	if clean == string(filepath.Separator) || clean == "." {
		return true
	}
	if home, err := os.UserHomeDir(); err == nil && filepath.Clean(home) == clean {
		return true
	}
	wd, err := os.Getwd()
	if err != nil {
		return false
	}
	wd = filepath.Clean(wd)
	for {
		if wd == clean {
			return true
		}
		parent := filepath.Dir(wd)
		if parent == wd {
			return false
		}
		wd = parent
	}
}

// PurgeTargets lists the directories and files `uninstall --purge` may remove.
// Both roots are enumerated explicitly so a purge never deletes a directory
// recursively, whatever it happens to contain.
func PurgeTargets() ([]string, error) {
	home, err := Home()
	if err != nil {
		return nil, err
	}
	cache, err := Cache()
	if err != nil {
		return nil, err
	}
	targets := []string{
		filepath.Join(home, "config.json"),
		filepath.Join(home, "relay-token.json"),
		filepath.Join(home, "install-manifest.json"),
		filepath.Join(home, "backups"),
		filepath.Join(home, "approvals.json"),
		filepath.Join(home, "sessions"),
		filepath.Join(cache, "run"),
		filepath.Join(cache, "uploads"),
		filepath.Join(cache, "sessions"),
		filepath.Join(cache, "recovery"),
		filepath.Join(cache, "notices"),
		// State of the plan board, live store and command center, retired in
		// 0.4.0. Purged for one more release so an upgrade leaves nothing behind.
		filepath.Join(cache, "board"),
		filepath.Join(cache, "live"),
		filepath.Join(cache, "center"),
		filepath.Join(home, "center"),
	}
	return targets, nil
}
