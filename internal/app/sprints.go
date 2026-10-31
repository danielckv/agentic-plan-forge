//go:build client && !server

package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/synthesizer"
)

// The sprint board is the plan arranged as work to commit to. It is computed
// from the task graph every time it is asked for, so it can never disagree
// with the plan; the owner's own moves are the only thing stored, and they are
// re-applied to the freshly computed board.
//
// Storing the board itself would mean two sources of truth for the same work,
// and the stored one would rot the moment the plan changed.

// sprintsRel is where the owner's moves live, beside the plan they arrange.
const sprintsRel = "docs/plans/.plan/sprints.json"

// sprintsSchemaVersion is bumped when the stored shape changes. A file from a
// newer version is ignored rather than guessed at.
const sprintsSchemaVersion = 1

// maxSprintsFileBytes bounds the read: this file is a few hundred placements
// at most, and it is untrusted input like every other repository file.
const maxSprintsFileBytes = 1 << 20

// sprintOverrides is the stored part of the board: the settings the owner
// chose and where they moved work to.
type sprintOverrides struct {
	SchemaVersion int    `json:"schema_version"`
	Basis         string `json:"basis,omitempty"`
	SprintDays    int    `json:"sprint_days,omitempty"`
	TeamSize      int    `json:"team_size,omitempty"`
	Horizon       int    `json:"horizon,omitempty"`
	// Placements maps a task id to the sprint it was moved to, where 0 is the
	// backlog. A task with no entry is placed by the auto-fill.
	Placements map[string]int `json:"placements,omitempty"`
	UpdatedAt  time.Time      `json:"updated_at"`
}

// SprintBoardView is what the page receives: the board, plus whether this
// session has a plan to arrange at all.
type SprintBoardView struct {
	Available bool `json:"available"`
	// Problem says why the board is empty, in words the page can show.
	Problem string                  `json:"problem,omitempty"`
	Board   synthesizer.SprintBoard `json:"board"`
}

func (s *appSession) sprintsPath() string {
	return filepath.Join(s.repo, filepath.FromSlash(sprintsRel))
}

// loadSprintOverrides reads the stored moves. A missing file is the normal
// first state, and an unreadable or unknown-version one is treated as absent:
// a board that recomputes is always better than a board that refuses to load.
func (s *appSession) loadSprintOverrides() sprintOverrides {
	empty := sprintOverrides{SchemaVersion: sprintsSchemaVersion, Placements: map[string]int{}}
	f, err := os.Open(s.sprintsPath())
	if err != nil {
		return empty
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, maxSprintsFileBytes))
	if err != nil {
		return empty
	}
	var stored sprintOverrides
	if err := json.Unmarshal(raw, &stored); err != nil {
		s.log.Warn("sprints file is not readable JSON; recomputing the board", "path", sprintsRel)
		return empty
	}
	if stored.SchemaVersion != sprintsSchemaVersion {
		s.log.Warn("sprints file has an unknown schema version; recomputing the board",
			"path", sprintsRel, "version", stored.SchemaVersion)
		return empty
	}
	if stored.Placements == nil {
		stored.Placements = map[string]int{}
	}
	return stored
}

// saveSprintOverrides writes the moves beside the plan. The directory already
// exists whenever a plan has been written; creating it here covers the board
// being arranged before the first artifact lands.
func (s *appSession) saveSprintOverrides(o sprintOverrides) error {
	o.SchemaVersion = sprintsSchemaVersion
	o.UpdatedAt = time.Now().UTC()
	if o.Placements == nil {
		o.Placements = map[string]int{}
	}
	raw, err := json.MarshalIndent(o, "", "  ")
	if err != nil {
		return err
	}
	path := s.sprintsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o600)
}

// sprintOptions turns the stored overrides and the session configuration into
// planner options. The workday comes from the configuration the estimates were
// made against, so the board and the schedule agree.
func (s *appSession) sprintOptions(o sprintOverrides) synthesizer.SprintOptions {
	return synthesizer.SprintOptions{
		Basis:        o.Basis,
		SprintDays:   o.SprintDays,
		WorkdayHours: s.cfg.HumanWorkdayHours,
		TeamSize:     o.TeamSize,
		MaxParallel:  s.cfg.MaxParallel,
		Horizon:      o.Horizon,
		Placements:   o.Placements,
	}
}

// sprintBoard computes the board for this session's plan.
func (s *appSession) sprintBoard() SprintBoardView {
	if s.exec == nil {
		return SprintBoardView{Problem: "this session has no deliberation to arrange"}
	}
	input, meta, ok := s.exec.approvedInput()
	if !ok {
		return SprintBoardView{Problem: "Nothing to arrange yet: the avatars have not produced a plan."}
	}
	plan, err := synthesizer.Compile(input, meta)
	if err != nil {
		return SprintBoardView{Problem: fmt.Sprintf("the plan does not compile: %v", err)}
	}
	overrides := s.loadSprintOverrides()
	board, err := synthesizer.BuildSprints(plan.Tasks, s.sprintOptions(overrides))
	if err != nil {
		return SprintBoardView{Problem: err.Error()}
	}
	return SprintBoardView{Available: true, Board: titled(board, plan)}
}

// titled fills each item's epic and story titles from the compiled plan, so
// the page can group and label without a second request.
func titled(board synthesizer.SprintBoard, plan synthesizer.CompiledPlan) synthesizer.SprintBoard {
	epics := make(map[string]string, len(plan.Epics))
	for _, e := range plan.Epics {
		epics[e.ID] = e.Title
	}
	stories := make(map[string]string, len(plan.Stories))
	for _, st := range plan.Stories {
		stories[st.ID] = storyLabel(st)
	}
	fill := func(items []synthesizer.SprintItem) {
		for i := range items {
			items[i].EpicTitle = epics[items[i].EpicID]
			items[i].StoryTitle = stories[items[i].StoryID]
		}
	}
	for i := range board.Sprints {
		fill(board.Sprints[i].Items)
	}
	fill(board.Backlog.Items)
	return board
}

// storyLabel renders a story as the one line a card has room for.
func storyLabel(st contracts.Story) string {
	if st.Need == "" {
		return st.ID
	}
	if st.Actor == "" {
		return st.Need
	}
	return st.Actor + ": " + st.Need
}

func (s *appSession) handleSprints(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.sprintBoard())
}

// sprintMoveRequest moves one task. Sprint 0 is the backlog.
type sprintMoveRequest struct {
	TaskID string `json:"task_id"`
	Sprint *int   `json:"sprint"`
}

func (s *appSession) handleSprintMove(w http.ResponseWriter, r *http.Request) {
	var req sprintMoveRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.TaskID == "" || req.Sprint == nil {
		writeAPIError(w, http.StatusBadRequest, "a move needs task_id and sprint")
		return
	}
	overrides := s.loadSprintOverrides()
	if overrides.Placements == nil {
		overrides.Placements = map[string]int{}
	}
	target := *req.Sprint
	if target < synthesizer.BacklogSprint || target > synthesizer.MaxHorizon {
		writeAPIError(w, http.StatusBadRequest,
			fmt.Sprintf("sprint must be between %d and %d", synthesizer.BacklogSprint, synthesizer.MaxHorizon))
		return
	}
	overrides.Placements[req.TaskID] = target
	if err := s.saveSprintOverrides(overrides); err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.sprintBoard())
}

// sprintSettingsRequest changes how the board is computed. Every field is
// optional; an absent one keeps its stored value.
type sprintSettingsRequest struct {
	Basis      *string `json:"basis"`
	SprintDays *int    `json:"sprint_days"`
	TeamSize   *int    `json:"team_size"`
	Horizon    *int    `json:"horizon"`
}

func (s *appSession) handleSprintSettings(w http.ResponseWriter, r *http.Request) {
	var req sprintSettingsRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	overrides := s.loadSprintOverrides()
	if req.Basis != nil {
		if *req.Basis != synthesizer.BasisHuman && *req.Basis != synthesizer.BasisAgent {
			writeAPIError(w, http.StatusBadRequest, "basis must be human or agent")
			return
		}
		overrides.Basis = *req.Basis
	}
	if req.SprintDays != nil {
		if *req.SprintDays < 1 || *req.SprintDays > 30 {
			writeAPIError(w, http.StatusBadRequest, "sprint_days must be between 1 and 30")
			return
		}
		overrides.SprintDays = *req.SprintDays
	}
	if req.TeamSize != nil {
		if *req.TeamSize < 1 || *req.TeamSize > 50 {
			writeAPIError(w, http.StatusBadRequest, "team_size must be between 1 and 50")
			return
		}
		overrides.TeamSize = *req.TeamSize
	}
	if req.Horizon != nil {
		if *req.Horizon < 1 || *req.Horizon > synthesizer.MaxHorizon {
			writeAPIError(w, http.StatusBadRequest,
				fmt.Sprintf("horizon must be between 1 and %d", synthesizer.MaxHorizon))
			return
		}
		overrides.Horizon = *req.Horizon
		// A sprint that no longer exists cannot hold work: those items return
		// to the backlog rather than vanish.
		for id, at := range overrides.Placements {
			if at > *req.Horizon {
				overrides.Placements[id] = synthesizer.BacklogSprint
			}
		}
	}
	if err := s.saveSprintOverrides(overrides); err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.sprintBoard())
}

// handleSprintReset drops every move and returns the auto-filled board. The
// settings are kept: resetting the arrangement is not resetting the team.
func (s *appSession) handleSprintReset(w http.ResponseWriter, _ *http.Request) {
	overrides := s.loadSprintOverrides()
	overrides.Placements = map[string]int{}
	if err := s.saveSprintOverrides(overrides); err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.sprintBoard())
}

// decodeJSONBody reads a bounded request body and refuses unknown fields, so a
// typo in a field name is an error rather than a silently ignored setting.
func decodeJSONBody(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, maxSprintsFileBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			return errors.New("the request body is empty")
		}
		return err
	}
	return nil
}
