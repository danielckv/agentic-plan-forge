//go:build client && !server

package app

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// The repository's remote supplies the owner/name pair the review client reads.
// It is read from git's own configuration rather than guessed, and a remote
// that is not GitHub is reported instead of silently producing a wrong name.

// remoteSlugPatterns accepts the remote forms git actually stores: an ssh URL,
// a scp-like shorthand and an https URL. The host must be github.com: a GitLab
// or self-hosted remote has an owner/name pair that looks identical, and using
// it would read a different server's pull request.
var remoteSlugPatterns = []*regexp.Regexp{
	regexp.MustCompile(`^(?:ssh://)?git@github\.com[:/]([^/]+/[^/]+?)(?:\.git)?/?$`),
	regexp.MustCompile(`^(?:ssh://git@|https?://|git://)(?:[^@/]+@)?(?:www\.)?github\.com/([^/]+/[^/]+?)(?:\.git)?/?$`),
}

// githubSlugTimeout bounds one read of the repository's remote configuration.
const githubSlugTimeout = 5 * time.Second

// githubSlugFromRemote reads the origin remote of repo and returns owner/name.
func githubSlugFromRemote(ctx context.Context, repo string) (string, error) {
	if strings.TrimSpace(repo) == "" {
		return "", errors.New("this session has no repository")
	}
	ctx, cancel := context.WithTimeout(ctx, githubSlugTimeout)
	defer cancel()
	// git's allowlisted configuration is already hardened elsewhere; this call
	// only reads one config value and never runs a repository-provided tool.
	cmd := exec.CommandContext(ctx, "git", "-C", repo, "config", "--get", "remote.origin.url")
	out, err := cmd.Output()
	if err != nil {
		return "", errors.New("this repository has no origin remote, so its GitHub pull request cannot be found")
	}
	return slugFromRemote(strings.TrimSpace(string(out)))
}

// slugFromRemote extracts owner/name from one remote URL.
func slugFromRemote(remote string) (string, error) {
	for _, pattern := range remoteSlugPatterns {
		if m := pattern.FindStringSubmatch(remote); m != nil {
			slug := strings.TrimSuffix(m[1], ".git")
			if strings.Count(slug, "/") != 1 {
				break
			}
			return slug, nil
		}
	}
	return "", fmt.Errorf("the origin remote %q is not a GitHub repository", redactRemote(remote))
}

// redactRemote keeps a remote readable in an error without echoing anything that
// could carry a credential.
func redactRemote(remote string) string {
	if at := strings.LastIndex(remote, "@"); at >= 0 {
		return "…" + remote[at:]
	}
	return remote
}
