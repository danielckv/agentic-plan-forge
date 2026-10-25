//go:build client && !server

package delivery

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// The preview box is a container built from the worktree and run on the local
// Docker daemon. It is the same image the docker deploy target publishes, so
// what the owner previews is what they ship, rather than a second build path
// that can disagree with the first.

const (
	// MaxBoxLogBytes bounds the log tail the Preview tab shows.
	MaxBoxLogBytes = 128 << 10
	// BuildTimeout bounds one image build.
	BuildTimeout = 30 * time.Minute
	// PreviewPort is the container port the preview is expected to listen on.
	// It is overridable per run; this is only the default guess.
	PreviewPort = 8080
	// ImagePrefix namespaces every image this package builds.
	ImagePrefix = "plan-forge/"
)

// ErrNoDocker reports that the Docker CLI is not on PATH.
var ErrNoDocker = errors.New("docker is not installed or not on PATH")

// ErrNoDockerfile reports a worktree with nothing to build.
var ErrNoDockerfile = errors.New("the plan produced no Dockerfile to build")

// Box is a built image and, once started, the container running it.
type Box struct {
	Image string `json:"image"`
	// Container is the running container id, empty before Start.
	Container string `json:"container,omitempty"`
	// URL is where the preview is reachable, empty before Start.
	URL string `json:"url,omitempty"`
	// HostPort is the port on the host the container is published on.
	HostPort int `json:"host_port,omitempty"`
	// Port is the container port that was published.
	Port int `json:"port,omitempty"`
}

// DockerAvailable reports whether the Docker CLI can be run. The window asks
// this before offering the Preview tab, so an owner without Docker is told
// once, up front, rather than after a build they waited for.
func DockerAvailable() error {
	if _, err := exec.LookPath("docker"); err != nil {
		return ErrNoDocker
	}
	return nil
}

// DockerfilePath returns the Dockerfile the worktree builds from, or
// ErrNoDockerfile. Only the repository root is considered: a Dockerfile deeper
// in the tree belongs to a component, and guessing which one to run is exactly
// the kind of invention that produces a preview of the wrong thing.
func DockerfilePath(wt Worktree) (string, error) {
	candidate := filepath.Join(wt.Path, "Dockerfile")
	if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
		return candidate, nil
	}
	return "", ErrNoDockerfile
}

// ImageTag is the image name for one approval. It is derived, not chosen, so a
// rebuild replaces its own image instead of leaving a pile behind.
func ImageTag(approval string) string {
	return ImagePrefix + strings.ToLower(approval)
}

// BuildBox builds the worktree into an image, reporting build output through
// onLine as it arrives.
func BuildBox(ctx context.Context, wt Worktree, approval string, onLine func(string)) (Box, error) {
	if err := DockerAvailable(); err != nil {
		return Box{}, err
	}
	if _, err := DockerfilePath(wt); err != nil {
		return Box{}, err
	}
	tag := ImageTag(approval)
	buildCtx, cancel := context.WithTimeout(ctx, BuildTimeout)
	defer cancel()
	// --pull keeps a stale cached base image from making the preview disagree
	// with what a clean deploy would produce.
	args := []string{"build", "--pull", "-t", tag, "-f", "Dockerfile", "."}
	if err := runStreaming(buildCtx, wt.Path, "docker", args, onLine); err != nil {
		return Box{}, fmt.Errorf("docker build: %w", err)
	}
	return Box{Image: tag}, nil
}

// StartBox runs the built image and publishes its port on the host. An existing
// container for the same approval is replaced, so pressing Preview twice does
// not leave two containers fighting over one port.
func StartBox(ctx context.Context, box Box, approval string, port int) (Box, error) {
	if err := DockerAvailable(); err != nil {
		return box, err
	}
	if box.Image == "" {
		return box, errors.New("no image has been built")
	}
	if port <= 0 {
		port = PreviewPort
	}
	name := containerName(approval)
	// Remove by name rather than by remembered id: a container left by a
	// previous process is still holding the name and the port.
	_, _ = docker(ctx, "rm", "-f", name)

	hostPort, err := freePort()
	if err != nil {
		return box, err
	}
	publish := fmt.Sprintf("127.0.0.1:%d:%d", hostPort, port)
	// Bound to loopback on purpose: a preview of code a model just wrote is
	// not something to put on the machine's network interfaces.
	out, err := docker(ctx, "run", "-d", "--name", name, "-p", publish, box.Image)
	if err != nil {
		return box, fmt.Errorf("docker run: %w: %s", err, out)
	}
	box.Container = strings.TrimSpace(out)
	box.Port, box.HostPort = port, hostPort
	box.URL = fmt.Sprintf("http://127.0.0.1:%d/", hostPort)
	return box, nil
}

// BoxLogs returns the tail of the container's output.
func BoxLogs(ctx context.Context, box Box) (string, error) {
	if box.Container == "" {
		return "", nil
	}
	out, err := docker(ctx, "logs", "--tail", "500", box.Container)
	if err != nil {
		return out, fmt.Errorf("docker logs: %w", err)
	}
	if len(out) > MaxBoxLogBytes {
		out = out[len(out)-MaxBoxLogBytes:]
	}
	return out, nil
}

// BoxRunning reports whether the container is still up, so the Preview tab can
// show a container that exited on its own rather than a dead link.
func BoxRunning(ctx context.Context, box Box) bool {
	if box.Container == "" {
		return false
	}
	out, err := docker(ctx, "inspect", "-f", "{{.State.Running}}", box.Container)
	return err == nil && strings.TrimSpace(out) == "true"
}

// StopBox removes the preview container. The image is left in place: it is what
// a docker deploy publishes, and rebuilding it costs minutes.
func StopBox(ctx context.Context, box Box) error {
	if box.Container == "" {
		return nil
	}
	if out, err := docker(ctx, "rm", "-f", box.Container); err != nil {
		return fmt.Errorf("docker rm: %w: %s", err, out)
	}
	return nil
}

func containerName(approval string) string {
	return "plan-forge-" + strings.ToLower(approval)
}

// docker runs one docker command and returns its combined output.
func docker(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// runStreaming runs a command and reports each output line as it arrives, so a
// long build shows progress instead of a spinner.
func runStreaming(ctx context.Context, dir, name string, args []string, onLine func(string)) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return err
	}
	scanLines(stdout, onLine)
	return cmd.Wait()
}

// freePort asks the kernel for an unused loopback port. Binding and closing is
// racy in principle; in practice the container starts immediately after and
// nothing else on a developer machine is scanning for the gap.
func freePort() (int, error) {
	listener, err := listenLoopback()
	if err != nil {
		return 0, err
	}
	defer func() { _ = listener.Close() }()
	_, portText, err := splitHostPort(listener.Addr().String())
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(portText)
}
