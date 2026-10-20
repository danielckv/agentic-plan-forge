//go:build client && !server

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/app"
	"github.com/danielckv/agentic-plan-forge/internal/livestore"
)

func init() {
	register("swarm", "report a harness swarm snapshot to the command center", runSwarm)
}

const swarmUsage = "swarm takes one subcommand: report"

// swarmReportBound keeps a report well inside the budget a host hook allows.
// The swarm panel is the least important thing happening in that process.
const swarmReportBound = 400 * time.Millisecond

// maxSwarmJSON bounds the snapshot read from --json. The protocol's payload is
// a few hundred bytes; anything approaching this is not one.
const maxSwarmJSON = 64 << 10

func runSwarm(ctx context.Context, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, swarmUsage)
		return exitUsage
	}
	if args[0] == "report" {
		return runSwarmReport(ctx, args[1:])
	}
	fmt.Fprintln(os.Stderr, swarmUsage)
	return exitUsage
}

// swarmStatus is the oh-my-pi `assistant:status` payload, in the field names
// the protocol uses. Plan-Forge consumes this protocol; it does not define it,
// so the shape is mirrored here rather than restated in Plan-Forge's own
// vocabulary, and an unknown field is ignored instead of refused — a harness
// that adds one must not break a hook.
type swarmStatus struct {
	Word          string `json:"word"`
	WaitingOnUser bool   `json:"waitingOnUser"`
	TaskID        string `json:"taskId"`
	Stage         string `json:"stage"`
	Dispatch      string `json:"dispatch"`
	Epic          string `json:"epic"`
	CurrentTask   string `json:"currentTask"`
	ActiveAgents  int    `json:"activeAgents"`
	Subtasks      []struct {
		ID       string `json:"id"`
		Agent    string `json:"agent"`
		Employee string `json:"employee"`
		Task     string `json:"task"`
	} `json:"parallelSubtasks"`
}

// runSwarmReport implements `plan-forge swarm report --repo PATH --session ID
// --json PAYLOAD`. It runs inside an OMP extension's event handler, so it
// always exits 0 and prints nothing: a swarm panel that failed to update must
// never surface to the user as a failure of the work the agent was doing.
//
// The payload is untrusted. It is parsed leniently and every string in it is
// sanitized and bounded by the store before it is written.
func runSwarmReport(ctx context.Context, args []string) int {
	var (
		repo, session, harness, raw string
	)
	if _, err := parseFlags("swarm report", args, func(fs *flag.FlagSet) {
		fs.StringVar(&repo, "repo", "", "repository path")
		fs.StringVar(&session, "session", "", "harness-qualified session id, e.g. omp:abc")
		fs.StringVar(&harness, "harness", "omp", "harness name")
		fs.StringVar(&raw, "json", "", "the assistant:status payload as compact JSON")
	}); err != nil {
		return exitOK
	}
	if strings.TrimSpace(repo) == "" || strings.TrimSpace(session) == "" || len(raw) > maxSwarmJSON {
		return exitOK
	}
	var in swarmStatus
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		return exitOK // a payload this process cannot read is not worth a word
	}
	swarm := livestore.Swarm{
		Word: in.Word, WaitingOnUser: in.WaitingOnUser, TaskID: in.TaskID,
		Stage: in.Stage, Dispatch: in.Dispatch, Epic: in.Epic,
		CurrentTask: in.CurrentTask, ActiveAgents: in.ActiveAgents,
	}
	for _, sub := range in.Subtasks {
		swarm.Agents = append(swarm.Agents, livestore.SwarmAgent{
			ID: sub.ID, Agent: sub.Agent, Employee: sub.Employee, Task: sub.Task,
		})
	}
	// A harness that names no subtasks may still report a count; trust its
	// count over the length of the list it chose not to send.
	if swarm.ActiveAgents == 0 {
		swarm.ActiveAgents = len(swarm.Agents)
	}
	ctx, cancel := context.WithTimeout(ctx, swarmReportBound)
	defer cancel()
	_ = app.CenterReport(ctx, app.CenterRecord{
		Repo: repo, SessionID: session, Harness: harness, Swarm: &swarm,
	})
	return exitOK
}
