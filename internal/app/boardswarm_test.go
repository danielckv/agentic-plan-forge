//go:build client && !server

package app

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/livestore"
)

func swarmAt() time.Time { return time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC) }

func beat(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func findSwarm(t *testing.T, list []BoardSwarmSession, id string) BoardSwarmSession {
	t.Helper()
	for _, s := range list {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("session %s is missing from %+v", id, list)
	return BoardSwarmSession{}
}

// The panel must not draw an empty swarm for a harness that has no swarm
// channel: "no agents running" and "no way to ask" are different facts, and
// only one of them is a reason to go looking for the agents.
func TestBoardSwarmSeparatesASilentHarnessFromAnUnreachableOne(t *testing.T) {
	at := swarmAt()
	docs := []livestore.Doc{{
		RepoKey: "k1", RepoName: "forge",
		Sessions: map[string]livestore.Session{
			"omp:live": {
				ID: "omp:live", Harness: "omp", LastBeat: beat(at),
				Swarm: &livestore.Swarm{Word: "WORKING", ActiveAgents: 2, Agents: []livestore.SwarmAgent{
					{ID: "worker-ada", Agent: "coder", Employee: "Ada", Task: "wire the panel"},
				}},
			},
			"omp:quiet":     {ID: "omp:quiet", Harness: "omp", LastBeat: beat(at)},
			"claude:abc":    {ID: "claude:abc", Harness: "claude-code", LastBeat: beat(at)},
			"claude:legacy": {ID: "claude:legacy", Harness: "claude", LastBeat: beat(at)},
		},
	}}
	got := buildBoardSwarm(docs, "/nowhere", at)
	if len(got) != 4 {
		t.Fatalf("sessions = %d, want 4", len(got))
	}
	live := findSwarm(t, got, "omp:live")
	if live.Channel != SwarmChannelLive || live.Swarm == nil || len(live.Swarm.Agents) != 1 {
		t.Fatalf("a reporting omp session must be live with its agents, got %+v", live)
	}
	if quiet := findSwarm(t, got, "omp:quiet"); quiet.Channel != SwarmChannelSilent || quiet.Swarm != nil {
		t.Fatalf("an omp session that has published nothing is silent, got %+v", quiet)
	}
	for _, id := range []string{"claude:abc", "claude:legacy"} {
		if c := findSwarm(t, got, id); c.Channel != SwarmChannelNone || c.Swarm != nil {
			t.Fatalf("%s: Claude Code has no swarm channel, got %+v", id, c)
		}
	}
}

func TestBoardSwarmPutsThisRepoAndLiveSessionsFirst(t *testing.T) {
	at := swarmAt()
	repo := t.TempDir()
	real, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	docs := []livestore.Doc{
		{RepoKey: "other", RepoName: "elsewhere", Sessions: map[string]livestore.Session{
			"omp:far": {ID: "omp:far", Harness: "omp", LastBeat: beat(at)},
		}},
		{RepoKey: livestore.Key(real), RepoName: filepath.Base(real), Sessions: map[string]livestore.Session{
			"omp:here":  {ID: "omp:here", Harness: "omp", LastBeat: beat(at)},
			"omp:stale": {ID: "omp:stale", Harness: "omp", LastBeat: beat(at.Add(-time.Hour))},
		}},
	}
	got := buildBoardSwarm(docs, repo, at)
	order := make([]string, 0, len(got))
	for _, s := range got {
		order = append(order, s.ID)
	}
	want := []string{"omp:here", "omp:stale", "omp:far"}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order = %v, want %v", order, want)
		}
	}
	if !findSwarm(t, got, "omp:here").ThisRepo {
		t.Fatal("a session in the board's own repository must be marked as such")
	}
	if findSwarm(t, got, "omp:far").ThisRepo {
		t.Fatal("a session in another repository must not be")
	}
	// A session that went quiet is shown dimmed, not dropped: going quiet is
	// itself the thing worth noticing.
	if !findSwarm(t, got, "omp:stale").Stale {
		t.Fatal("a session past the stale window must be marked stale")
	}
}

func TestBoardSwarmIsEmptyWithoutAnyLiveDocuments(t *testing.T) {
	got := buildBoardSwarm(nil, "/nowhere", swarmAt())
	if got == nil || len(got) != 0 {
		t.Fatalf("got %+v, want an empty slice rather than a null", got)
	}
}
