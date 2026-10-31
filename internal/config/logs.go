package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// LogDirName is the subdirectory of the Plan Forge home that holds log files.
const LogDirName = "logs"

// LogRetention keeps log files for two days: today's file, yesterday's, and the
// one before it until the second midnight passes. Older files are deleted on
// every open, so nothing grows without bound and no extra job is needed.
const LogRetention = 2 * 24 * time.Hour

// LogDir returns the Plan Forge home log directory, creating it when missing:
// $PLAN_FORGE_HOME/logs or ~/.plan-forge/logs — the same directory that holds
// config.json, so one path covers everything a user inspects.
func LogDir() (string, error) {
	home, err := EnsureHome()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, LogDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// LogFileName is the per-day log file name for a session process: the date
// makes rotation automatic — a new day writes a new file and leaves the old
// ones to the retention sweep.
func LogFileName(now time.Time) string {
	return "plan-forge-" + now.Format("2006-01-02") + ".log"
}

// OpenLogFile opens (appending) today's log file in the home log directory and
// deletes log files older than LogRetention before returning. The caller owns
// closing the file. 0600 keeps the contents readable by the owner only: log
// lines carry absolute paths and model names.
func OpenLogFile(now time.Time) (*os.File, error) {
	dir, err := LogDir()
	if err != nil {
		return nil, err
	}
	if err := removeExpiredLogs(dir, now); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, LogFileName(now)), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return f, nil
}

// removeExpiredLogs deletes plan-forge-*.log files whose file name date is
// older than LogRetention before now. The date parsed from the name decides,
// not the file mtime, so a file that was not written to still expires. Files
// that do not match the pattern are left alone: other tools may keep their own
// files here.
func removeExpiredLogs(dir string, now time.Time) error {
	matches, err := filepath.Glob(filepath.Join(dir, "plan-forge-*.log"))
	if err != nil {
		return err
	}
	cutoff := now.Add(-LogRetention)
	for _, path := range matches {
		stamp := strings.TrimSuffix(filepath.Base(path), ".log")
		stamp = strings.TrimPrefix(stamp, "plan-forge-")
		day, parseErr := time.ParseInLocation("2006-01-02", stamp, time.Local)
		if parseErr != nil {
			continue
		}
		if day.Before(time.Date(cutoff.Year(), cutoff.Month(), cutoff.Day(), 0, 0, 0, 0, time.Local)) {
			if rmErr := os.Remove(path); rmErr != nil && !os.IsNotExist(rmErr) {
				return fmt.Errorf("remove expired log %s: %w", path, rmErr)
			}
		}
	}
	return nil
}

// LatestLogs lists the surviving log files, oldest first, for `plan-forge logs`
// style diagnostics and for doctor to point at.
func LatestLogs() ([]string, error) {
	dir, err := LogDir()
	if err != nil {
		return nil, err
	}
	matches, err := filepath.Glob(filepath.Join(dir, "plan-forge-*.log"))
	if err != nil {
		return nil, err
	}
	sort.Strings(matches)
	return matches, nil
}
