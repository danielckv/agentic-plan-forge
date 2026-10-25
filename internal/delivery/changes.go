//go:build client && !server

package delivery

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Budgets for what the Code tab is allowed to carry. The window renders every
// byte of this in one payload, so each list is bounded by count as well as by
// size: a byte budget alone does not bound the work a long list causes, which
// is the lesson ADR 0006 paid for with a 3.1 MB architect payload.
const (
	// MaxChangedFiles bounds the file list. A plan that rewrites more than
	// this is reported truncated rather than streamed in full.
	MaxChangedFiles = 400
	// MaxDiffBytes bounds the unified diff of the whole worktree.
	MaxDiffBytes = 1 << 20
	// MaxFileBytes bounds one file read back for the Code tab.
	MaxFileBytes = 512 << 10
)

// FileChange is one path the agent touched, with the line counts git reports.
type FileChange struct {
	Path string `json:"path"`
	// Status is git's letter: A added, M modified, D deleted, R renamed.
	Status  string `json:"status"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
	// Binary marks a file git counts no lines for.
	Binary bool `json:"binary"`
}

// Changes is everything the Code tab shows about a worktree.
type Changes struct {
	Branch string       `json:"branch"`
	Base   string       `json:"base"`
	Files  []FileChange `json:"files"`
	// Diff is the unified diff, trimmed to MaxDiffBytes.
	Diff string `json:"diff"`
	// Truncated names each budget that cut the payload, so the window can say
	// what is missing instead of showing a short list as if it were complete.
	Truncated []string `json:"truncated"`
	Added     int      `json:"added"`
	Removed   int      `json:"removed"`
}

// ReadChanges reports what the worktree holds against the commit it started
// from. It covers committed and uncommitted work alike: the agent may or may
// not commit, and the owner's review must not depend on which it chose.
func ReadChanges(ctx context.Context, wt Worktree) (Changes, error) {
	out := Changes{Branch: wt.Branch, Base: wt.Base, Files: []FileChange{}, Truncated: []string{}}
	if wt.Path == "" {
		return out, nil
	}
	// Staging everything is what makes one diff cover committed, staged and
	// untracked work. The worktree is Plan Forge's own, so the index is ours
	// to move; the owner's repository index is never touched.
	if res, err := git(ctx, wt.Path, "add", "-A"); err != nil {
		return out, fmt.Errorf("stage worktree changes: %w: %s", err, res)
	}
	numstat, err := git(ctx, wt.Path, "diff", "--numstat", "--find-renames", wt.Base)
	if err != nil {
		return out, fmt.Errorf("read change stats: %w: %s", err, numstat)
	}
	status, err := git(ctx, wt.Path, "diff", "--name-status", "--find-renames", wt.Base)
	if err != nil {
		return out, fmt.Errorf("read change status: %w: %s", err, status)
	}
	letters := statusLetters(status)

	for _, line := range strings.Split(strings.TrimSpace(numstat), "\n") {
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) != 3 {
			continue
		}
		change := FileChange{Path: fields[2], Status: letters[fields[2]]}
		if fields[0] == "-" || fields[1] == "-" {
			change.Binary = true
		} else {
			change.Added, _ = strconv.Atoi(fields[0])
			change.Removed, _ = strconv.Atoi(fields[1])
		}
		out.Files = append(out.Files, change)
		out.Added += change.Added
		out.Removed += change.Removed
	}
	sort.Slice(out.Files, func(i, j int) bool { return out.Files[i].Path < out.Files[j].Path })
	if len(out.Files) > MaxChangedFiles {
		out.Truncated = append(out.Truncated,
			fmt.Sprintf("file list cut to %d of %d paths", MaxChangedFiles, len(out.Files)))
		out.Files = out.Files[:MaxChangedFiles]
	}

	diff, err := git(ctx, wt.Path, "diff", "--find-renames", wt.Base)
	if err != nil {
		return out, fmt.Errorf("read diff: %w: %s", err, diff)
	}
	if len(diff) > MaxDiffBytes {
		out.Truncated = append(out.Truncated,
			fmt.Sprintf("diff cut to %d of %d bytes", MaxDiffBytes, len(diff)))
		diff = diff[:MaxDiffBytes]
	}
	out.Diff = diff
	return out, nil
}

// statusLetters maps path to git's status letter from --name-status output. A
// rename reports two paths; the destination is the one the Code tab lists.
func statusLetters(raw string) map[string]string {
	letters := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) < 2 {
			continue
		}
		letters[fields[len(fields)-1]] = fields[0][:1]
	}
	return letters
}

// FileContent is one file of the worktree, for the Code tab's file view.
type FileContent struct {
	Path      string `json:"path"`
	Text      string `json:"text"`
	Bytes     int    `json:"bytes"`
	Truncated bool   `json:"truncated"`
	Binary    bool   `json:"binary"`
}

// ReadFile returns one file of the worktree. rel is repository-relative and
// must stay inside the worktree: the window sends back a path the model's plan
// produced, and a path is the one thing a model is never trusted with (INV-4).
func ReadFile(wt Worktree, rel string) (FileContent, error) {
	if wt.Path == "" {
		return FileContent{}, fmt.Errorf("worktree is not open")
	}
	clean := filepath.Clean(filepath.FromSlash(rel))
	if clean == "." || filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") {
		return FileContent{}, fmt.Errorf("path %q is outside the worktree", rel)
	}
	full := filepath.Join(wt.Path, clean)
	// Join cleans, but a symlink inside the worktree can still point out of
	// it, so the resolved path is checked against the resolved root.
	root, err := filepath.EvalSymlinks(wt.Path)
	if err != nil {
		return FileContent{}, err
	}
	resolved, err := filepath.EvalSymlinks(full)
	if err != nil {
		return FileContent{}, err
	}
	if resolved != root && !strings.HasPrefix(resolved, root+string(filepath.Separator)) {
		return FileContent{}, fmt.Errorf("path %q resolves outside the worktree", rel)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return FileContent{}, err
	}
	if info.IsDir() {
		return FileContent{}, fmt.Errorf("path %q is a directory", rel)
	}
	body, err := os.ReadFile(resolved)
	if err != nil {
		return FileContent{}, err
	}
	out := FileContent{Path: filepath.ToSlash(clean), Bytes: len(body)}
	if isBinary(body) {
		out.Binary = true
		return out, nil
	}
	if len(body) > MaxFileBytes {
		body, out.Truncated = body[:MaxFileBytes], true
	}
	out.Text = string(body)
	return out, nil
}

// isBinary reports whether a NUL byte appears in the first 8 KiB, the same
// heuristic git uses to decide a file has no lines to count.
func isBinary(body []byte) bool {
	head := body
	if len(head) > 8<<10 {
		head = head[:8<<10]
	}
	for _, b := range head {
		if b == 0 {
			return true
		}
	}
	return false
}
