//go:build client && !server

package ast

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// maxGitOutput bounds what one git invocation may print.
const maxGitOutput = 256 << 20

// hardenedConfig is prepended to every git invocation. It disables the two
// settings that make git spawn a repository-configured helper (a file system
// monitor hook) or write cache files while merely listing files.
var hardenedConfig = []string{"-c", "core.fsmonitor=false", "-c", "core.untrackedCache=false"}

// permittedGit lists the only argument vectors this package will run. Anything
// that could execute repository-controlled code — status, diff, anything with a
// pathspec that selects attribute filters — is deliberately absent.
var permittedGit = [][]string{
	{"rev-parse", "--show-toplevel"},
	{"rev-parse", "--is-inside-work-tree"},
	{"rev-parse", "--verify", "--quiet", "HEAD"},
	{"rev-parse", "--abbrev-ref", "HEAD"},
	{"ls-files", "-z", "--cached", "--others", "--exclude-standard"},
	{"ls-files", "-s", "-z"},
	{"hash-object", "--stdin"},
	{"config", "--get", "core.hooksPath"},
}

var (
	gitPathName = regexp.MustCompile(`^[a-z][a-z-]*$`)
	objectID    = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
)

func permittedGitArgs(args []string) bool {
	for _, p := range permittedGit {
		if equalStrings(p, args) {
			return true
		}
	}
	return len(args) == 3 && args[0] == "rev-parse" && args[1] == "--git-path" && gitPathName.MatchString(args[2])
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// GitError reports a failed git invocation. Stderr holds the first line git
// printed, which is enough to explain refusals such as "dubious ownership".
type GitError struct {
	Args   []string
	Err    error
	Stderr string
}

func (e *GitError) Error() string {
	msg := fmt.Sprintf("git %s: %v", strings.Join(e.Args, " "), e.Err)
	if e.Stderr != "" {
		msg += ": " + e.Stderr
	}
	return msg
}

// Unwrap returns the underlying process error.
func (e *GitError) Unwrap() error { return e.Err }

// ExitCode returns the process exit status, or -1 when git did not run to
// completion.
func (e *GitError) ExitCode() int {
	var ee *exec.ExitError
	if errors.As(e.Err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// Git runs a closed set of read-only git subcommands against one directory.
// Every invocation passes `-c core.fsmonitor=false -c core.untrackedCache=false`,
// sets GIT_OPTIONAL_LOCKS=0 and GIT_TERMINAL_PROMPT=0, drops every inherited
// GIT_* variable, never passes `safe.directory`, and refuses any argument vector
// outside the permitted list. It never runs `git status` or `git diff`, which
// can invoke external diff drivers, text conversion or filters.
type Git struct {
	bin string
	dir string
}

// NewGit returns a Git bound to dir. It fails when no git executable is on PATH.
func NewGit(dir string) (*Git, error) {
	bin, err := exec.LookPath("git")
	if err != nil {
		return nil, fmt.Errorf("git is not installed: %w", err)
	}
	return &Git{bin: bin, dir: dir}, nil
}

// RepoRoot returns the Git top-level directory containing dir.
func RepoRoot(ctx context.Context, dir string) (string, error) {
	g, err := NewGit(dir)
	if err != nil {
		return "", err
	}
	return g.TopLevel(ctx)
}

// gitEnv returns the child environment: the parent's without GIT_* variables,
// plus the settings that stop git prompting or taking optional locks.
func gitEnv(parent []string) []string {
	env := make([]string, 0, len(parent)+3)
	for _, kv := range parent {
		if strings.HasPrefix(kv, "GIT_") {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
}

func (g *Git) run(ctx context.Context, stdin io.Reader, args ...string) ([]byte, error) {
	if !permittedGitArgs(args) {
		return nil, fmt.Errorf("ast: git %q is not a permitted command", strings.Join(args, " "))
	}
	full := make([]string, 0, len(hardenedConfig)+len(args))
	full = append(full, hardenedConfig...)
	full = append(full, args...)

	cmd := exec.CommandContext(ctx, g.bin, full...)
	cmd.Dir = g.dir
	cmd.Env = gitEnv(os.Environ())
	cmd.Stdin = stdin
	cmd.WaitDelay = 2 * time.Second
	out := &capBuffer{limit: maxGitOutput}
	errBuf := &capBuffer{limit: 64 << 10}
	cmd.Stdout, cmd.Stderr = out, errBuf
	if err := cmd.Run(); err != nil {
		line, _, _ := strings.Cut(strings.TrimSpace(errBuf.buf.String()), "\n")
		return nil, &GitError{Args: args, Err: err, Stderr: line}
	}
	if out.overflow {
		return nil, fmt.Errorf("ast: git %s output exceeds %d bytes", args[0], maxGitOutput)
	}
	return out.buf.Bytes(), nil
}

// capBuffer collects output up to a limit and records overflow.
type capBuffer struct {
	buf      bytes.Buffer
	limit    int
	overflow bool
}

func (b *capBuffer) Write(p []byte) (int, error) {
	if room := b.limit - b.buf.Len(); len(p) > room {
		b.overflow = true
		if room > 0 {
			b.buf.Write(p[:room])
		}
		return len(p), nil
	}
	return b.buf.Write(p)
}

// TopLevel returns the root of the working tree that contains the directory.
func (g *Git) TopLevel(ctx context.Context) (string, error) {
	out, err := g.run(ctx, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	return filepath.FromSlash(strings.TrimSpace(string(out))), nil
}

// InsideWorkTree reports whether the directory is inside a Git working tree.
func (g *Git) InsideWorkTree(ctx context.Context) bool {
	out, err := g.run(ctx, nil, "rev-parse", "--is-inside-work-tree")
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

// Head returns the commit hash HEAD points at. ok is false in a repository
// without commits.
func (g *Git) Head(ctx context.Context) (hash string, ok bool) {
	out, err := g.run(ctx, nil, "rev-parse", "--verify", "--quiet", "HEAD")
	if err != nil {
		return "", false
	}
	h := strings.TrimSpace(string(out))
	return h, objectID.MatchString(h)
}

// Branch returns the current branch name, or "HEAD" when detached. ok is false
// in a repository without commits.
func (g *Git) Branch(ctx context.Context) (name string, ok bool) {
	out, err := g.run(ctx, nil, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}

// ListFiles returns the repository-relative paths git considers part of the
// working tree: tracked files plus untracked files that no ignore rule (the
// repository's .gitignore files, .git/info/exclude and the global excludes)
// hides. Paths use forward slashes and are sorted by git.
func (g *Git) ListFiles(ctx context.Context) ([]string, error) {
	out, err := g.run(ctx, nil, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	var files []string
	seen := map[string]bool{}
	for _, p := range strings.Split(string(out), "\x00") {
		if p == "" || seen[p] || !safeRelPath(p) {
			continue
		}
		seen[p] = true
		files = append(files, p)
	}
	return files, nil
}

// safeRelPath rejects absolute paths and parent references.
func safeRelPath(p string) bool {
	if strings.HasPrefix(p, "/") || filepath.IsAbs(p) {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return false
		}
	}
	return true
}

// IndexEntry is one staged file from `git ls-files -s`.
type IndexEntry struct {
	// Mode is the octal file mode, such as 100644.
	Mode string
	// OID is the staged blob object ID.
	OID string
	// Stage is 0 for a normal entry and 1-3 for an unmerged path.
	Stage int
}

// IndexEntries returns the staged blob of every tracked path. For an unmerged
// path the first non-zero stage is reported.
func (g *Git) IndexEntries(ctx context.Context) (map[string]IndexEntry, error) {
	out, err := g.run(ctx, nil, "ls-files", "-s", "-z")
	if err != nil {
		return nil, err
	}
	entries := map[string]IndexEntry{}
	for _, rec := range strings.Split(string(out), "\x00") {
		meta, path, ok := strings.Cut(rec, "\t")
		if !ok {
			continue
		}
		f := strings.Fields(meta)
		if len(f) != 3 || !objectID.MatchString(f[1]) {
			continue
		}
		stage, err := strconv.Atoi(f[2])
		if err != nil {
			continue
		}
		if _, dup := entries[path]; dup {
			continue
		}
		entries[path] = IndexEntry{Mode: f[0], OID: f[1], Stage: stage}
	}
	return entries, nil
}

// HashObjectStdin returns the blob object ID git computes for the bytes read
// from r. Because the bytes arrive on standard input with no path, git applies
// no attribute filters, so no repository-configured clean filter can run.
func (g *Git) HashObjectStdin(ctx context.Context, r io.Reader) (string, error) {
	out, err := g.run(ctx, r, "hash-object", "--stdin")
	if err != nil {
		return "", err
	}
	oid := strings.TrimSpace(string(out))
	if !objectID.MatchString(oid) {
		return "", fmt.Errorf("ast: git hash-object returned %q", oid)
	}
	return oid, nil
}

// HooksPath returns the configured core.hooksPath. set is false when the key is
// not configured.
func (g *Git) HooksPath(ctx context.Context) (value string, set bool, err error) {
	out, err := g.run(ctx, nil, "config", "--get", "core.hooksPath")
	if err != nil {
		var ge *GitError
		if errors.As(err, &ge) && ge.ExitCode() == 1 {
			return "", false, nil
		}
		return "", false, err
	}
	return strings.TrimSpace(string(out)), true, nil
}

// GitPath resolves a path inside the git directory, such as "hooks". name must
// be lower-case letters and hyphens.
func (g *Git) GitPath(ctx context.Context, name string) (string, error) {
	out, err := g.run(ctx, nil, "rev-parse", "--git-path", name)
	if err != nil {
		return "", err
	}
	p := strings.TrimSpace(string(out))
	if !filepath.IsAbs(p) {
		p = filepath.Join(g.dir, filepath.FromSlash(p))
	}
	return p, nil
}
