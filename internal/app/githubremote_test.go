//go:build client && !server

package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func initRepoWithRemote(t *testing.T, remote string) string {
	t.Helper()
	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git is unavailable here: %v (%s)", err, out)
		}
	}
	run("init", "-q")
	run("remote", "add", "origin", remote)
	return repo
}

// The review client must find the owner/name pair from the repository itself.
// A guessed name would read someone else's pull request.
func TestGithubSlugFromTheOriginRemote(t *testing.T) {
	for _, tc := range []struct {
		remote string
		want   string
	}{
		{"git@github.com:acme/widget.git", "acme/widget"},
		{"https://github.com/acme/widget.git", "acme/widget"},
		{"https://github.com/acme/widget", "acme/widget"},
		{"ssh://git@github.com/acme/widget.git", "acme/widget"},
	} {
		slug, err := slugFromRemote(tc.remote)
		if err != nil {
			t.Errorf("%s: %v", tc.remote, err)
			continue
		}
		if slug != tc.want {
			t.Errorf("%s = %q, want %q", tc.remote, slug, tc.want)
		}
	}
}

// A remote that is not GitHub is reported, not silently reshaped into a name.
func TestSlugFromANonGithubRemoteIsReported(t *testing.T) {
	for _, remote := range []string{"https://gitlab.com/acme/widget.git", "not a url", ""} {
		if slug, err := slugFromRemote(remote); err == nil {
			t.Errorf("%q produced %q", remote, slug)
		}
	}
}

// An error never echoes a credential that a remote URL may carry.
func TestRemoteErrorsNeverEchoCredentials(t *testing.T) {
	_, err := slugFromRemote("https://user:secret-token@example.com/acme/widget.git")
	if err == nil {
		t.Fatal("a non-GitHub remote was accepted")
	}
	if strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("error leaked a credential: %v", err)
	}
}

// A repository with no origin reports that instead of an empty name.
func TestMissingOriginIsReported(t *testing.T) {
	repo := t.TempDir()
	if _, err := githubSlugFromRemote(context.Background(), repo); err == nil {
		t.Fatal("a repository with no origin produced a slug")
	}
}

// The slug comes from the repository when one is present.
func TestSlugComesFromTheRepository(t *testing.T) {
	repo := initRepoWithRemote(t, "git@github.com:acme/widget.git")
	slug, err := githubSlugFromRemote(context.Background(), repo)
	if err != nil {
		t.Fatalf("githubSlugFromRemote: %v", err)
	}
	if slug != "acme/widget" {
		t.Errorf("slug = %q, want acme/widget", slug)
	}
	if info, err := os.Stat(filepath.Join(repo, ".git")); err != nil || !info.IsDir() {
		t.Fatal("the fixture repository was not created")
	}
}
