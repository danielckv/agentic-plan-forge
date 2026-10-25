//go:build client && !server

package delivery

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/config"
)

// Deploying is the one step here that leaves the machine, so it is the one step
// that is never implicit: the window asks the owner to choose a target the
// first time, records the answer, and shows it as a toggle from then on. A plan
// is built and previewed without any of this; nothing is sent anywhere until
// the owner presses Deploy.

// DeployTimeout bounds one deploy.
const DeployTimeout = 20 * time.Minute

// ErrNoAgentBox reports that the AgentBox CLI is not on PATH.
var ErrNoAgentBox = errors.New("agentbox is not installed; run: npm -g i @madarco/agentbox && agentbox install")

// ErrNoTarget reports a deploy with no target chosen.
var ErrNoTarget = errors.New("no deploy target is configured")

// Deployment is the result of a deploy, for the window.
type Deployment struct {
	Target string `json:"target"`
	// URL is where the deployed app is reachable, when the target reports one.
	URL string `json:"url,omitempty"`
	// Detail is the target's own last line of output, shown verbatim.
	Detail   string    `json:"detail,omitempty"`
	At       time.Time `json:"at"`
	Image    string    `json:"image,omitempty"`
	Provider string    `json:"provider,omitempty"`
}

// Deploy publishes a built worktree to the configured target.
//
// docker runs the image that the preview already built, detached and named, on
// the local daemon. agent-box hands the worktree to the AgentBox CLI, which
// owns the provider choice itself (local Docker, SSH, Hetzner, DigitalOcean,
// Daytona, Vercel): Plan Forge does not re-implement that selection, it passes
// the provider through.
func Deploy(ctx context.Context, cfg config.Config, wt Worktree, box Box, approval string, onLine func(string)) (Deployment, error) {
	target := cfg.DeployTarget
	switch target {
	case "", config.DeployTargetNone:
		return Deployment{}, ErrNoTarget
	case config.DeployTargetDocker:
		return deployDocker(ctx, box, approval, onLine)
	case config.DeployTargetAgentBox:
		return deployAgentBox(ctx, cfg, wt, onLine)
	default:
		return Deployment{}, fmt.Errorf("deploy_target %q is not supported", target)
	}
}

// deployDocker runs the built image detached under a stable name, so a second
// deploy replaces the first rather than stacking containers.
func deployDocker(ctx context.Context, box Box, approval string, onLine func(string)) (Deployment, error) {
	if err := DockerAvailable(); err != nil {
		return Deployment{}, err
	}
	if box.Image == "" {
		return Deployment{}, errors.New("nothing has been built to deploy")
	}
	runCtx, cancel := context.WithTimeout(ctx, DeployTimeout)
	defer cancel()

	name := "plan-forge-deploy-" + strings.ToLower(approval)
	_, _ = docker(runCtx, "rm", "-f", name)

	port := box.Port
	if port <= 0 {
		port = PreviewPort
	}
	hostPort, err := freePort()
	if err != nil {
		return Deployment{}, err
	}
	publish := fmt.Sprintf("127.0.0.1:%d:%d", hostPort, port)
	out, err := docker(runCtx, "run", "-d", "--restart", "unless-stopped", "--name", name, "-p", publish, box.Image)
	if err != nil {
		return Deployment{}, fmt.Errorf("docker run: %w: %s", err, out)
	}
	if onLine != nil {
		onLine(strings.TrimSpace(out))
	}
	return Deployment{
		Target: config.DeployTargetDocker,
		Image:  box.Image,
		URL:    fmt.Sprintf("http://127.0.0.1:%d/", hostPort),
		Detail: "container " + shortID(strings.TrimSpace(out)),
		At:     time.Now(),
	}, nil
}

// deployAgentBox creates an AgentBox from the worktree and reports the URL the
// CLI gives back. The provider is whatever the owner configured in AgentBox
// itself; `agentbox create` uses its own default when none is named.
func deployAgentBox(ctx context.Context, cfg config.Config, wt Worktree, onLine func(string)) (Deployment, error) {
	if _, err := exec.LookPath("agentbox"); err != nil {
		return Deployment{}, ErrNoAgentBox
	}
	runCtx, cancel := context.WithTimeout(ctx, DeployTimeout)
	defer cancel()

	// `agentbox create` acts on the project in the working directory, so the
	// worktree is the cwd and the owner's checkout is never the thing boxed.
	if err := runStreaming(runCtx, wt.Path, "agentbox", []string{"create"}, onLine); err != nil {
		return Deployment{}, fmt.Errorf("agentbox create: %w", err)
	}
	deployment := Deployment{
		Target:   config.DeployTargetAgentBox,
		Provider: cfg.AgentBoxEndpoint(),
		At:       time.Now(),
	}
	// `agentbox url` prints the box's web URL. A box with no web app has no
	// URL to print, which is reported rather than treated as a failure: the
	// box is created either way.
	cmd := exec.CommandContext(runCtx, "agentbox", "url", "--print")
	cmd.Dir = wt.Path
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		deployment.Detail = "box created; agentbox url reported no address"
		return deployment, nil
	}
	if url := lastURL(text); url != "" {
		deployment.URL = url
	}
	deployment.Detail = lastLine(text)
	return deployment, nil
}

// lastURL returns the last http(s) token in text, which is how both CLIs print
// an address: on its own line, after whatever they logged first.
func lastURL(text string) string {
	var found string
	for _, field := range strings.Fields(text) {
		if strings.HasPrefix(field, "http://") || strings.HasPrefix(field, "https://") {
			found = strings.Trim(field, `"'<>,`)
		}
	}
	return found
}

func lastLine(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if trimmed := strings.TrimSpace(lines[i]); trimmed != "" {
			return clampStep(trimmed)
		}
	}
	return ""
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// TargetAvailable reports whether a target can run on this machine, so the
// window can grey out a choice instead of letting the owner pick one that
// fails minutes later.
func TargetAvailable(target string) error {
	switch target {
	case config.DeployTargetNone, "":
		return nil
	case config.DeployTargetDocker:
		return DockerAvailable()
	case config.DeployTargetAgentBox:
		if _, err := exec.LookPath("agentbox"); err != nil {
			return ErrNoAgentBox
		}
		return nil
	default:
		return fmt.Errorf("deploy_target %q is not supported", target)
	}
}
