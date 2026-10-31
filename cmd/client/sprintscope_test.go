//go:build client && !server

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/danielckv/agentic-plan-forge/internal/config"
	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// scopeRepo writes the two files `wait --sprint` reads: the approved manifest
// and, when placements are given, the owner's arrangement.
func scopeRepo(t *testing.T, tasks []contracts.TechnicalTask, placements map[string]int) string {
	t.Helper()
	repo := t.TempDir()
	dir := filepath.Join(repo, filepath.FromSlash("docs/plans/.plan"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(contracts.PlanManifest{SchemaVersion: 1, TaskDAG: tasks})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plan_manifest.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if placements != nil {
		stored, err := json.Marshal(storedSprints{
			SchemaVersion: 1, SprintDays: 2, TeamSize: 1, Horizon: 3, Placements: placements,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "sprints.json"), stored, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return repo
}

func scopeTask(id string, hours float64, deps ...string) contracts.TechnicalTask {
	return contracts.TechnicalTask{
		ID: id, EpicID: "E1", WorkstreamID: "ws-a", Title: "task " + id, DependsOn: deps,
		HumanEstimate: contracts.Estimate{Optimistic: hours, Likely: hours, Pessimistic: hours, Basis: "b"},
		AgentEstimate: contracts.Estimate{Optimistic: hours, Likely: hours, Pessimistic: hours, Basis: "b"},
	}
}

var scopeConfig = config.Config{HumanWorkdayHours: 10, MaxParallel: 4}

// The handoff scope names exactly the sprint's tasks and says how much of the
// plan it is leaving out, so the agent can state what it is not doing.
func TestSprintScopeNamesTheCommittedTasks(t *testing.T) {
	tasks := []contracts.TechnicalTask{
		scopeTask("T-1", 8), scopeTask("T-2", 8), scopeTask("T-3", 8), scopeTask("T-4", 8),
	}
	repo := scopeRepo(t, tasks, map[string]int{})
	scope, err := sprintScope(repo, 1, scopeConfig)
	if err != nil {
		t.Fatal(err)
	}
	if scope.Number != 1 || scope.Name != "Sprint 1" {
		t.Errorf("scope = %d %q, want sprint 1", scope.Number, scope.Name)
	}
	if len(scope.TaskIDs) == 0 {
		t.Fatal("sprint 1 committed nothing")
	}
	if scope.Remaining != len(tasks)-len(scope.TaskIDs) {
		t.Errorf("remaining = %d, want %d", scope.Remaining, len(tasks)-len(scope.TaskIDs))
	}
	if scope.HumanHours != float64(len(scope.TaskIDs))*8 {
		t.Errorf("human hours = %v for %d tasks of 8", scope.HumanHours, len(scope.TaskIDs))
	}
}

// The owner's arrangement is what the handoff follows: a task moved to another
// sprint is not in sprint 1's scope.
func TestSprintScopeFollowsTheOwnersArrangement(t *testing.T) {
	tasks := []contracts.TechnicalTask{scopeTask("T-1", 4), scopeTask("T-2", 4)}
	repo := scopeRepo(t, tasks, map[string]int{"T-2": 2})
	scope, err := sprintScope(repo, 1, scopeConfig)
	if err != nil {
		t.Fatal(err)
	}
	if len(scope.TaskIDs) != 1 || scope.TaskIDs[0] != "T-1" {
		t.Errorf("sprint 1 = %v, want only T-1: T-2 was moved to sprint 2", scope.TaskIDs)
	}
	second, err := sprintScope(repo, 2, scopeConfig)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.TaskIDs) != 1 || second.TaskIDs[0] != "T-2" {
		t.Errorf("sprint 2 = %v, want T-2", second.TaskIDs)
	}
}

// With no arrangement stored, the scope is the auto-filled board: a plan
// nobody has touched still hands over a first sprint.
func TestSprintScopeWorksWithNoStoredBoard(t *testing.T) {
	repo := scopeRepo(t, []contracts.TechnicalTask{scopeTask("T-1", 4)}, nil)
	scope, err := sprintScope(repo, 1, scopeConfig)
	if err != nil {
		t.Fatal(err)
	}
	if len(scope.TaskIDs) != 1 || scope.TaskIDs[0] != "T-1" {
		t.Errorf("sprint 1 = %v, want T-1", scope.TaskIDs)
	}
}

// Every refusal says what is wrong in words the agent can relay.
func TestSprintScopeRefusalsAreReadable(t *testing.T) {
	repo := scopeRepo(t, []contracts.TechnicalTask{scopeTask("T-1", 4)}, nil)
	for _, tc := range []struct {
		name   string
		repo   string
		number int
		want   string
	}{
		{"zero", repo, 0, "between 1 and"},
		{"too high", repo, 99, "between 1 and"},
		{"no manifest", t.TempDir(), 1, "no approved plan manifest"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := sprintScope(tc.repo, tc.number, scopeConfig)
			if err == nil {
				t.Fatal("want a refusal")
			}
			if !contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

// A plan with no tasks is refused rather than handed over as an empty sprint.
func TestSprintScopeRefusesAPlanWithNoTasks(t *testing.T) {
	repo := scopeRepo(t, nil, nil)
	if _, err := sprintScope(repo, 1, scopeConfig); err == nil {
		t.Fatal("a plan with no tasks must be refused")
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
