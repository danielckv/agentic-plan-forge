//go:build client && !server

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/danielckv/agentic-plan-forge/internal/config"
	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/synthesizer"
)

// `plan-forge wait --sprint N` narrows the handoff to one sprint.
//
// Without it the agent receives the whole approved plan, which is the
// behaviour every release so far has had and stays the default. With it the
// agent is told which tasks are committed now, so a plan of forty tasks is
// implemented a sprint at a time instead of in one sitting.
//
// The scope is read from the two files the approval already wrote into the
// repository — the manifest for the task graph, sprints.json for the owner's
// arrangement — so `wait` needs nothing from the session that produced them.

// sprintsRel is where the board's overrides live, beside the plan.
const sprintsRel = "docs/plans/.plan/sprints.json"

// maxSprintsFileBytes bounds the read of an untrusted repository file.
const maxSprintsFileBytes = 1 << 20

// SprintScope is what `wait --sprint` adds to the handoff JSON.
type SprintScope struct {
	Number       int      `json:"number"`
	Name         string   `json:"name"`
	TaskIDs      []string `json:"task_ids"`
	HumanHours   float64  `json:"human_hours"`
	AgentMinutes float64  `json:"agent_minutes"`
	// Remaining is how many tasks the plan holds outside this sprint, so the
	// agent can say what it is not doing.
	Remaining int `json:"remaining"`
}

// storedSprints is the subset of sprints.json this command reads. The window
// owns the file; unknown fields here are ignored rather than refused, because
// a newer window must not break an older binary's handoff.
type storedSprints struct {
	SchemaVersion int            `json:"schema_version"`
	Basis         string         `json:"basis"`
	SprintDays    int            `json:"sprint_days"`
	TeamSize      int            `json:"team_size"`
	Horizon       int            `json:"horizon"`
	Placements    map[string]int `json:"placements"`
}

// sprintScope builds the scope for one sprint number from the repository's own
// approved artifacts. An error names what is missing, in words the agent can
// relay.
func sprintScope(repo string, number int, cfg config.Config) (SprintScope, error) {
	if number < 1 || number > synthesizer.MaxHorizon {
		return SprintScope{}, fmt.Errorf("--sprint must be between 1 and %d", synthesizer.MaxHorizon)
	}
	manifest, err := readManifest(repo)
	if err != nil {
		return SprintScope{}, err
	}
	if len(manifest.TaskDAG) == 0 {
		return SprintScope{}, errors.New("the approved plan has no tasks to arrange")
	}
	stored := readStoredSprints(repo)
	board, err := synthesizer.BuildSprints(manifest.TaskDAG, synthesizer.SprintOptions{
		Basis:        stored.Basis,
		SprintDays:   stored.SprintDays,
		WorkdayHours: cfg.HumanWorkdayHours,
		TeamSize:     stored.TeamSize,
		MaxParallel:  cfg.MaxParallel,
		Horizon:      stored.Horizon,
		Placements:   stored.Placements,
	})
	if err != nil {
		return SprintScope{}, err
	}
	for _, s := range board.Sprints {
		if s.Number != number {
			continue
		}
		ids := make([]string, 0, len(s.Items))
		for _, it := range s.Items {
			ids = append(ids, it.TaskID)
		}
		return SprintScope{
			Number: s.Number, Name: s.Name, TaskIDs: ids,
			HumanHours: s.HumanHours, AgentMinutes: s.AgentMinutes,
			Remaining: len(manifest.TaskDAG) - len(ids),
		}, nil
	}
	return SprintScope{}, fmt.Errorf("the board has no sprint %d; it holds %d", number, len(board.Sprints))
}

// readManifest reads the approved plan manifest from the repository.
func readManifest(repo string) (contracts.PlanManifest, error) {
	path := filepath.Join(repo, filepath.FromSlash("docs/plans/.plan/plan_manifest.json"))
	f, err := os.Open(path)
	if err != nil {
		return contracts.PlanManifest{}, errors.New("no approved plan manifest in docs/plans/.plan/")
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, 64<<20))
	if err != nil {
		return contracts.PlanManifest{}, err
	}
	var m contracts.PlanManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return contracts.PlanManifest{}, errors.New("the plan manifest is not readable JSON")
	}
	return m, nil
}

// readStoredSprints reads the owner's arrangement. A missing or unreadable
// file means the auto-filled board, which is the right answer for a plan
// nobody has arranged yet.
func readStoredSprints(repo string) storedSprints {
	f, err := os.Open(filepath.Join(repo, filepath.FromSlash(sprintsRel)))
	if err != nil {
		return storedSprints{}
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, maxSprintsFileBytes))
	if err != nil {
		return storedSprints{}
	}
	var stored storedSprints
	if err := json.Unmarshal(raw, &stored); err != nil {
		return storedSprints{}
	}
	return stored
}
