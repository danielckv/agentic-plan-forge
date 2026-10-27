//go:build client && !server

package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// GitHub access settings. The token comes from the environment and never
// reaches the config file, a log line, or the window.
const (
	DefaultGitHubBaseURL = "https://api.github.com"
	DefaultGitHubTimeout = 15 * time.Second
	maxGitHubBytes       = 4 << 20
	// GitHubTokenEnv names the environment variable holding the token.
	GitHubAPIKeyEnv = "GITHUB_TOKEN"
)

// HTTPGitHub implements GitHub against the REST API. Every failure returns to
// the caller, which maps it to unknown: an API failure is never evidence of
// approval.
type HTTPGitHub struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// NewHTTPGitHub returns a client. An empty baseURL selects the public API.
func NewHTTPGitHub(baseURL, token string) (*HTTPGitHub, error) {
	if baseURL == "" {
		baseURL = DefaultGitHubBaseURL
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("github base url: %w", err)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return nil, errors.New("github base url must be http or https")
	}
	if u.Host == "" {
		return nil, errors.New("github base url has no host")
	}
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("github client needs a token in " + GitHubAPIKeyEnv)
	}
	return &HTTPGitHub{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   token,
		HTTP:    &http.Client{Timeout: DefaultGitHubTimeout},
	}, nil
}

func (c *HTTPGitHub) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: DefaultGitHubTimeout}
}

// get performs one GET and decodes JSON into out. The token is attached here
// and nowhere else, and a failure names the path and status only: a GitHub
// error body can echo scope information.
func (c *HTTPGitHub) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Authorization", "Bearer "+c.Token)
	res, err := c.client().Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("github %s answered %d", path, res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, maxGitHubBytes+1))
	if err != nil {
		return err
	}
	if int64(len(body)) > maxGitHubBytes {
		return errors.New("github response is too large")
	}
	return json.Unmarshal(body, out)
}

// validRepo accepts the owner/name form and nothing that could alter a path.
func validRepo(repo string) error {
	owner, name, ok := strings.Cut(strings.TrimSpace(repo), "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return fmt.Errorf("repository %q is not owner/name", repo)
	}
	for _, part := range []string{owner, name} {
		if part == "." || part == ".." || strings.ContainsAny(part, "?# \t") {
			return fmt.Errorf("repository %q has an invalid component", repo)
		}
	}
	return nil
}

// PullRequest reads the head commit, author, merge state, and whether the base
// branch is protected. Protection is a second call: the pull request payload
// does not state it.
func (c *HTTPGitHub) PullRequest(ctx context.Context, repo string, number int) (PR, error) {
	if err := validRepo(repo); err != nil {
		return PR{}, err
	}
	if number <= 0 {
		return PR{}, errors.New("github: pull request number must be positive")
	}
	var pull prWire
	if err := c.get(ctx, fmt.Sprintf("/repos/%s/pulls/%d", repo, number), &pull); err != nil {
		return PR{}, err
	}
	protected := false
	if pull.Base.Ref != "" {
		var branch struct {
			Protected bool `json:"protected"`
		}
		if err := c.get(ctx, "/repos/"+repo+"/branches/"+url.PathEscape(pull.Base.Ref), &branch); err != nil {
			return PR{}, err
		}
		protected = branch.Protected
	}
	return PR{
		Number:        number,
		HeadSHA:       pull.Head.SHA,
		Author:        pull.User.Login,
		Merged:        pull.Merged || pull.MergedAt != "",
		BaseProtected: protected,
	}, nil
}

// prWire is the pull-request fields acceptance depends on.
type prWire struct {
	Number   int    `json:"number"`
	Merged   bool   `json:"merged"`
	MergedAt string `json:"merged_at"`
	Head     struct {
		SHA string `json:"sha"`
	} `json:"head"`
	User struct {
		Login string `json:"login"`
	} `json:"user"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

// Reviews reads every review of one pull request. Dismissal is evidence: a
// dismissed approval never qualifies.
func (c *HTTPGitHub) Reviews(ctx context.Context, repo string, number int) ([]Review, error) {
	if err := validRepo(repo); err != nil {
		return nil, err
	}
	var raw []struct {
		User struct {
			Login string `json:"login"`
		} `json:"user"`
		State       string  `json:"state"`
		CommitID    string  `json:"commit_id"`
		DismissedAt *string `json:"dismissed_at"`
	}
	if err := c.get(ctx, fmt.Sprintf("/repos/%s/pulls/%d/reviews?per_page=100", repo, number), &raw); err != nil {
		return nil, err
	}
	out := make([]Review, 0, len(raw))
	for _, r := range raw {
		out = append(out, Review{
			Reviewer:  r.User.Login,
			State:     r.State,
			CommitSHA: r.CommitID,
			Dismissed: r.DismissedAt != nil,
		})
	}
	return out, nil
}

// Permission reads a collaborator's repository permission.
func (c *HTTPGitHub) Permission(ctx context.Context, repo, login string) (string, error) {
	if err := validRepo(repo); err != nil {
		return "", err
	}
	if strings.TrimSpace(login) == "" {
		return "", errors.New("github: empty reviewer login")
	}
	var out struct {
		Permission string `json:"permission"`
		RoleName   string `json:"role_name"`
	}
	if err := c.get(ctx, "/repos/"+repo+"/collaborators/"+url.PathEscape(login)+"/permission", &out); err != nil {
		return "", err
	}
	if out.Permission != "" {
		return out.Permission, nil
	}
	return strings.ToLower(out.RoleName), nil
}

// ParsePRNumber accepts the string a config or a request holds.
func ParsePRNumber(s string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("pull request %q is not a positive number", s)
	}
	return n, nil
}
