//go:build client && !server

package acceptance

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The GitHub client is the only place acceptance trusts for reviewer identity.
// These tests pin the parts a wrong implementation would get silently wrong:
// the token never appears in an error, a dismissed review is recognised, the
// head commit comes from the API, and a failure is an error the caller turns
// into unknown rather than approval.
func TestHTTPGitHubReadsEvidenceAndNeverLeaksTheToken(t *testing.T) {
	const token = "ghp_example_secret_value"
	var gotAuth []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = append(gotAuth, r.Header.Get("Authorization"))
		switch {
		case r.URL.Path == "/repos/acme/widget/pulls/7":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"number": 7,
				"merged": false,
				"head":   map[string]string{"sha": "abc123"},
				"user":   map[string]string{"login": "author"},
				"base":   map[string]string{"ref": "main"},
			})
		case r.URL.Path == "/repos/acme/widget/branches/main":
			_ = json.NewEncoder(w).Encode(map[string]bool{"protected": true})
		case r.URL.Path == "/repos/acme/widget/pulls/7/reviews":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"user": map[string]string{"login": "dev"}, "state": "APPROVED", "commit_id": "abc123"},
				{"user": map[string]string{"login": "old"}, "state": "APPROVED", "commit_id": "abc123", "dismissed_at": "2026-01-01T00:00:00Z"},
			})
		case r.URL.Path == "/repos/acme/widget/collaborators/dev/permission":
			_ = json.NewEncoder(w).Encode(map[string]string{"permission": "write"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c, err := NewHTTPGitHub(srv.URL, token)
	if err != nil {
		t.Fatalf("NewHTTPGitHub: %v", err)
	}
	ctx := context.Background()
	pr, err := c.PullRequest(ctx, "acme/widget", 7)
	if err != nil {
		t.Fatalf("PullRequest: %v", err)
	}
	if pr.HeadSHA != "abc123" || pr.Author != "author" || !pr.BaseProtected || pr.Merged {
		t.Errorf("PR = %+v, want the API's head, author and protection", pr)
	}
	reviews, err := c.Reviews(ctx, "acme/widget", 7)
	if err != nil {
		t.Fatalf("Reviews: %v", err)
	}
	if len(reviews) != 2 {
		t.Fatalf("reviews = %d, want 2", len(reviews))
	}
	if reviews[0].State != ReviewStateApproved || reviews[0].Dismissed {
		t.Errorf("first review = %+v, want a live approval", reviews[0])
	}
	if !reviews[1].Dismissed {
		t.Error("a dismissed review was reported as live")
	}
	perm, err := c.Permission(ctx, "acme/widget", "dev")
	if err != nil || perm != "write" {
		t.Errorf("Permission = %q, %v, want write", perm, err)
	}
	for _, auth := range gotAuth {
		if auth != "Bearer "+token {
			t.Errorf("Authorization = %q, want the bearer token", auth)
		}
	}

	// A failure must not echo the credential, whatever the endpoint says.
	srv.Close()
	_, err = c.PullRequest(ctx, "acme/widget", 7)
	if err == nil {
		t.Fatal("a closed server produced no error")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("error leaked the token: %v", err)
	}
}

// A reviewer-shaped repository string must never build a different API path.
func TestHTTPGitHubRejectsHostileRepositoryAndLogin(t *testing.T) {
	c, err := NewHTTPGitHub("https://api.github.com", "token")
	if err != nil {
		t.Fatalf("NewHTTPGitHub: %v", err)
	}
	ctx := context.Background()
	for _, repo := range []string{"", "acme", "acme/", "/widget", "acme/widget/extra", "../acme/widget", "acme/wid get"} {
		if _, err := c.PullRequest(ctx, repo, 1); err == nil {
			t.Errorf("repository %q was accepted", repo)
		}
	}
	if _, err := c.PullRequest(ctx, "acme/widget", 0); err == nil {
		t.Error("pull request 0 was accepted")
	}
	if _, err := c.Permission(ctx, "acme/widget", ""); err == nil {
		t.Error("an empty login was accepted")
	}
}

// A token is required: without one the client refuses to exist rather than
// making anonymous calls whose 404 the caller would read as "no approval".
func TestHTTPGitHubRequiresAToken(t *testing.T) {
	if _, err := NewHTTPGitHub("https://api.github.com", ""); err == nil {
		t.Fatal("a client without a token was created")
	}
	if _, err := NewHTTPGitHub("ftp://api.github.com", "token"); err == nil {
		t.Fatal("a non-http base url was accepted")
	}
}

func TestParsePRNumber(t *testing.T) {
	if n, err := ParsePRNumber(" 12 "); err != nil || n != 12 {
		t.Errorf("ParsePRNumber = %d, %v, want 12", n, err)
	}
	for _, bad := range []string{"", "0", "-3", "x"} {
		if _, err := ParsePRNumber(bad); err == nil {
			t.Errorf("ParsePRNumber(%q) was accepted", bad)
		}
	}
}
