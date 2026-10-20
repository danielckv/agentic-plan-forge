//go:build client && !server

package app

import (
	"path/filepath"
	"sort"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/livestore"
	"github.com/danielckv/agentic-plan-forge/internal/sanitize"
)

// Swarm channel states. They answer one question the panel must not fudge:
// why is this session showing no agents?
const (
	// SwarmChannelLive means snapshots are arriving from this session.
	SwarmChannelLive = "live"
	// SwarmChannelSilent means the harness speaks the swarm protocol but this
	// session has published nothing yet.
	SwarmChannelSilent = "silent"
	// SwarmChannelNone means the harness has no swarm channel to listen to.
	// Claude Code is the case: it reports tool telemetry, and has no bus that
	// says which subagents are live or what each one is working on, so the
	// panel says so rather than drawing an empty swarm that looks like idle.
	SwarmChannelNone = "none"
)

// swarmHarnesses are the harnesses that publish the swarm protocol.
var swarmHarnesses = map[string]bool{"omp": true, "oh-my-pi": true}

// BoardSwarmSession is one live harness session as the board shows it.
type BoardSwarmSession struct {
	ID      string `json:"id"`
	Harness string `json:"harness"`
	// Repo is the repository's base name, never its path: the board applies
	// the same rule to its own title.
	Repo   string `json:"repo"`
	Branch string `json:"branch,omitempty"`
	// ThisRepo marks the sessions working in the repository this board is for.
	ThisRepo bool `json:"this_repo"`
	// Stale means the session stopped reporting; it is shown dimmed rather
	// than dropped, because a session that went quiet is itself worth seeing.
	Stale bool `json:"stale"`
	// Channel is one of SwarmChannelLive, SwarmChannelSilent, SwarmChannelNone.
	Channel string `json:"channel"`
	// Swarm is the harness's last snapshot, present only when Channel is live.
	Swarm *livestore.Swarm `json:"swarm,omitempty"`
}

// buildBoardSwarm renders the live harness sessions across every repository on
// this machine, newest heartbeat first, with the board's own repository first
// within that order.
//
// It reads the live store directly rather than asking the command center: the
// board is already a separate process with its own window, and routing a read
// through a daemon that may not be running would make the panel disappear
// exactly when a session is least supervised.
func buildBoardSwarm(docs []livestore.Doc, repo string, at time.Time) []BoardSwarmSession {
	thisRepo := livestore.Key(resolveRepoKeyPath(repo))
	out := []BoardSwarmSession{}
	for _, doc := range docs {
		for _, sess := range doc.Sessions {
			entry := BoardSwarmSession{
				ID:       sanitize.Terminal(sess.ID),
				Harness:  sanitize.Terminal(sess.Harness),
				Repo:     sanitize.Terminal(doc.RepoName),
				Branch:   sanitize.Terminal(sess.Branch),
				ThisRepo: doc.RepoKey == thisRepo,
				Stale:    sess.Stale(at),
				Channel:  SwarmChannelNone,
			}
			if swarmHarnesses[harnessFamily(sess.Harness)] {
				entry.Channel = SwarmChannelSilent
				if sess.Swarm != nil {
					entry.Channel, entry.Swarm = SwarmChannelLive, sess.Swarm
				}
			}
			out = append(out, entry)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].ThisRepo != out[j].ThisRepo {
			return out[i].ThisRepo
		}
		if out[i].Stale != out[j].Stale {
			return out[j].Stale
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// harnessFamily maps a reported harness name onto the family that decides
// whether a swarm channel exists. Hooks report "omp" and "claude-code"; a
// variant spelling must not be read as a harness nobody knows.
func harnessFamily(harness string) string {
	switch harness {
	case "omp", "oh-my-pi":
		return "omp"
	case "claude", "claude-code":
		return "claude"
	}
	return harness
}

// resolveRepoKeyPath resolves repo the way livestore.Open does, so the key
// computed here matches the key the writer stored under.
func resolveRepoKeyPath(repo string) string {
	if repo == "" {
		return ""
	}
	real, err := filepath.EvalSymlinks(repo)
	if err != nil {
		return filepath.Clean(repo)
	}
	return real
}

// loadBoardSwarm reads every repository's live document. A missing or
// unreadable store is an empty swarm, never an error: the plan is what the
// board is for, and a panel beside it must not be able to stop it opening.
func loadBoardSwarm(repo string, at time.Time) []BoardSwarmSession {
	dir, err := livestore.Dir()
	if err != nil {
		return []BoardSwarmSession{}
	}
	docs, err := livestore.LoadAll(dir)
	if err != nil {
		return []BoardSwarmSession{}
	}
	return buildBoardSwarm(docs, repo, at)
}
