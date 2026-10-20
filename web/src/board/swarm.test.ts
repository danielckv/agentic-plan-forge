import { describe, expect, it } from "vitest";

import type { BoardSnapshot, BoardSwarmSession } from "./types";
import { validateBoardSnapshot } from "./types";

const BASE: Omit<BoardSnapshot, "swarm"> = {
  repo: "forge",
  title: "Plan",
  manifest_sha256: "abc",
  approved: true,
  status: "IN_PROGRESS",
  verified: 0,
  total: 1,
  revision: 1,
  host: "claude",
  evaluated_at: "2026-10-21T12:00:00Z",
  drift: [],
  tasks: [],
  consumers: [],
  discoveries: [],
  coordinator: { active: [], next_up: [], updated_at: "" },
};

function wire(swarm: unknown): Record<string, unknown> {
  return { ...BASE, swarm } as unknown as Record<string, unknown>;
}

const OMP_SESSION = {
  id: "omp:abc",
  harness: "omp",
  repo: "forge",
  branch: "main",
  this_repo: true,
  stale: false,
  channel: "live",
  swarm: {
    word: "WORKING",
    waiting_on_user: false,
    task_id: "T-12",
    stage: "limit retries",
    dispatch: "∥3 ready",
    epic: "ship the board",
    current_task: "wire the panel",
    active_agents: 2,
    agents: [
      { id: "worker-ada", agent: "coder", employee: "Ada", task: "wire the panel" },
      { id: "worker-bo", agent: "reviewer", task: "read the diff" },
    ],
    at: "2026-10-21T12:00:00Z",
  },
};

const CLAUDE_SESSION = {
  id: "claude:xyz",
  harness: "claude-code",
  repo: "forge",
  this_repo: true,
  stale: false,
  channel: "none",
};

function accepted(swarm: unknown): BoardSwarmSession[] {
  const result = validateBoardSnapshot(wire(swarm));
  if (!result.ok) throw new Error(`expected a valid snapshot, got: ${result.error}`);
  return result.value.swarm;
}

function first(swarm: unknown): BoardSwarmSession {
  const session = accepted(swarm)[0];
  if (!session) throw new Error("expected at least one session");
  return session;
}

describe("swarm snapshot validation", () => {
  it("accepts a live oh-my-pi session with its dispatched agents", () => {
    const session = first([OMP_SESSION]);
    expect(session.channel).toBe("live");
    expect(session.swarm?.active_agents).toBe(2);
    expect(session.swarm?.agents.map((agent) => agent.employee ?? agent.agent)).toEqual([
      "Ada",
      "reviewer",
    ]);
  });

  it("keeps a Claude session, with no swarm to show", () => {
    const session = first([CLAUDE_SESSION]);
    expect(session.channel).toBe("none");
    expect(session.swarm).toBeUndefined();
  });

  it("tells a harness with nothing dispatched apart from one it cannot ask", () => {
    const sessions = accepted([
      { ...OMP_SESSION, id: "omp:quiet", channel: "silent", swarm: undefined },
      CLAUDE_SESSION,
    ]);
    expect(sessions.map((session) => session.channel)).toEqual(["silent", "none"]);
  });

  it("reads a missing swarm key from an older server as no sessions", () => {
    const older = { ...BASE } as unknown as Record<string, unknown>;
    const result = validateBoardSnapshot(older);
    expect(result.ok).toBe(true);
    expect(result.ok && result.value.swarm).toEqual([]);
  });

  it("reads a null agent list as an empty one", () => {
    const session = first([{ ...OMP_SESSION, swarm: { ...OMP_SESSION.swarm, agents: null } }]);
    expect(session.swarm?.agents).toEqual([]);
  });

  it("refuses a malformed session rather than rendering it", () => {
    const bad: unknown[] = [
      { ...OMP_SESSION, channel: "maybe" },
      { ...OMP_SESSION, this_repo: "yes" },
      { ...OMP_SESSION, swarm: { ...OMP_SESSION.swarm, active_agents: -1 } },
      { ...OMP_SESSION, swarm: { ...OMP_SESSION.swarm, word: 7 } },
      { ...OMP_SESSION, swarm: { ...OMP_SESSION.swarm, agents: [{ id: "a" }] } },
      "not an object",
    ];
    for (const session of bad) {
      const result = validateBoardSnapshot(wire([session]));
      expect(result.ok, `expected ${JSON.stringify(session)} to be refused`).toBe(false);
    }
  });
});
