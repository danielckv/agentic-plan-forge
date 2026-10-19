import { describe, expect, it, vi } from "vitest";

import { createBoardStore, discoveryRequestBody } from "./boardStore";
import { actorLabel, deriveCoordinatorView, formatAgo, nextReviewRequest } from "./coordinator";
import type { BoardDiscovery, BoardSnapshot, BoardTask } from "./types";
import { EMPTY_LIVE, validateBoardSnapshot } from "./types";

const NOW = Date.parse("2026-10-19T12:00:00Z");

function task(id: string, extra: Partial<BoardTask> = {}): BoardTask {
  return {
    id,
    epic_id: "E-1",
    story_id: "S-1",
    workstream_id: "W-1",
    title: `Task ${id}`,
    column: "todo",
    blocked: false,
    depends_on: [],
    priority: "normal",
    targets: [],
    verification: { total: 0, passed: 0 },
    acceptance_criteria: [],
    human_estimate: { likely: 1, basis: "x" },
    agent_estimate: { likely: 1, basis: "x" },
    notes: [],
    live: EMPTY_LIVE,
    awaiting_verification: false,
    discovered: false,
    ...extra,
  };
}

function discovery(id: string, extra: Partial<BoardDiscovery> = {}): BoardDiscovery {
  return {
    id,
    kind: "task",
    title: `Found ${id}`,
    paths: ["internal/x.go"],
    parent_task_id: "T-1",
    reason: "outside the plan",
    by: "claude:abc",
    at: "2026-10-19T11:59:00Z",
    status: "proposed",
    priority: "normal",
    ...extra,
  };
}

function snapshot(extra: Partial<BoardSnapshot> = {}): BoardSnapshot {
  return {
    repo: "repo",
    title: "Plan",
    manifest_sha256: "abc",
    approved: true,
    status: "IN_PROGRESS",
    verified: 0,
    total: 2,
    revision: 3,
    host: "claude",
    evaluated_at: "2026-10-19T11:59:00Z",
    drift: [],
    tasks: [task("T-1"), task("T-2")],
    consumers: [],
    discoveries: [discovery("D-1"), discovery("D-2", { kind: "path", status: "dismissed" })],
    coordinator: { active: [], next_up: ["T-1", "T-2"], updated_at: "2026-10-19T11:59:50Z" },
    ...extra,
  };
}

function jsonResponse(value: unknown, status = 200): Response {
  return new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } });
}

/** A snapshot as an older server sends it: no C11 keys anywhere. */
function legacyWire(): Record<string, unknown> {
  const wire = JSON.parse(JSON.stringify(snapshot())) as Record<string, unknown>;
  delete wire.discoveries;
  delete wire.coordinator;
  for (const entry of wire.tasks as Array<Record<string, unknown>>) {
    delete entry.live;
    delete entry.awaiting_verification;
    delete entry.discovered;
  }
  return wire;
}

describe("C11 snapshot validation", () => {
  it("accepts a full C11 snapshot unchanged", () => {
    const result = validateBoardSnapshot(snapshot({
      tasks: [task("T-1", { live: { state: "active", by: "claude:abc", source: "file_edit", detail: "a.go", at: "2026-10-19T11:59:00Z" }, awaiting_verification: false })],
    }));
    expect(result.ok).toBe(true);
    if (!result.ok) return;
    expect(result.value.tasks[0]?.live.state).toBe("active");
    expect(result.value.discoveries).toHaveLength(2);
    expect(result.value.coordinator.next_up).toEqual(["T-1", "T-2"]);
  });

  it("defaults every C11 field when an older server omits them", () => {
    const result = validateBoardSnapshot(legacyWire());
    expect(result.ok).toBe(true);
    if (!result.ok) return;
    expect(result.value.discoveries).toEqual([]);
    expect(result.value.coordinator).toEqual({ active: [], next_up: [], updated_at: "" });
    for (const entry of result.value.tasks) {
      expect(entry.live).toEqual({ state: "", by: "", source: "", detail: "", at: "" });
      expect(entry.awaiting_verification).toBe(false);
      expect(entry.discovered).toBe(false);
    }
  });

  it("reads Go nil slices (null) as empty", () => {
    const wire = legacyWire();
    wire.discoveries = null;
    wire.coordinator = { active: null, next_up: null, updated_at: "" };
    const result = validateBoardSnapshot(wire);
    expect(result.ok).toBe(true);
    if (result.ok) expect(result.value.coordinator.next_up).toEqual([]);
  });

  it("rejects malformed C11 fields strictly", () => {
    const bad = (mutate: (wire: Record<string, unknown>) => void): string => {
      const wire = JSON.parse(JSON.stringify(snapshot())) as Record<string, unknown>;
      mutate(wire);
      const result = validateBoardSnapshot(wire);
      return result.ok ? "ok" : result.error;
    };
    expect(bad((wire) => { (wire.tasks as Array<Record<string, unknown>>)[0]!.live = { state: "busy", by: "", source: "", detail: "", at: "" }; }))
      .toBe("tasks are malformed");
    expect(bad((wire) => { (wire.tasks as Array<Record<string, unknown>>)[0]!.awaiting_verification = "yes"; }))
      .toBe("tasks are malformed");
    expect(bad((wire) => { (wire.discoveries as Array<Record<string, unknown>>)[0]!.status = "maybe"; }))
      .toBe("discoveries are malformed");
    expect(bad((wire) => { (wire.discoveries as Array<Record<string, unknown>>)[0]!.priority = "urgent"; }))
      .toBe("discoveries are malformed");
    expect(bad((wire) => { wire.discoveries = "none"; })).toBe("discoveries are malformed");
    expect(bad((wire) => { (wire.discoveries as Array<Record<string, unknown>>)[1]!.id = "D-1"; }))
      .toBe("discovery ids must be unique");
    expect(bad((wire) => { wire.coordinator = { active: [1], next_up: [], updated_at: "" }; }))
      .toBe("coordinator is malformed");
    expect(bad((wire) => { wire.coordinator = { active: [], next_up: [], updated_at: 5 }; }))
      .toBe("coordinator is malformed");
  });

  it("still renders from the store when the server is older", () => {
    const store = createBoardStore();
    expect(store.acceptSnapshot(legacyWire())).toBe(true);
    expect(store.getState().snapshot?.discoveries).toEqual([]);
  });
});

describe("resolveDiscovery", () => {
  it("builds exactly one action per request body", () => {
    expect(discoveryRequestBody("D-1", "accept", "high")).toEqual({ id: "D-1", action: "accept", priority: "high" });
    expect(discoveryRequestBody("D-1", "dismiss")).toEqual({ id: "D-1", action: "dismiss" });
    expect(() => discoveryRequestBody("", "dismiss")).toThrow("discovery id is required");
    expect(() => discoveryRequestBody("D-1", "accept", "urgent" as never)).toThrow("invalid discovery action");
  });

  it("applies accept optimistically (status, priority, new board task) and reconciles with the server", async () => {
    let release: (response: Response) => void = () => undefined;
    const pending = new Promise<Response>((resolve) => { release = resolve; });
    const apiFetch = vi.fn(() => pending);
    const store = createBoardStore({ apiFetch });
    store.acceptSnapshot(snapshot());

    const resolution = store.resolveDiscovery("D-1", "accept", "high");
    const optimistic = store.getState().snapshot;
    expect(optimistic?.discoveries.find((d) => d.id === "D-1")).toMatchObject({ status: "accepted", priority: "high" });
    const added = optimistic?.tasks.find((t) => t.id === "D-1");
    expect(added).toMatchObject({ discovered: true, column: "todo", priority: "high", depends_on: ["T-1"] });
    expect(store.getState().pendingActions).toBe(1);

    const serverView = snapshot({
      revision: 4,
      tasks: [task("T-1"), task("T-2"), task("D-1", { discovered: true, priority: "high", depends_on: ["T-1"], epic_id: "E-1" })],
      discoveries: [discovery("D-1", { status: "accepted", priority: "high" }), discovery("D-2", { kind: "path", status: "dismissed" })],
    });
    release(jsonResponse(serverView));
    await expect(resolution).resolves.toBe(true);

    const [path, init] = apiFetch.mock.calls[0] as unknown as [string, RequestInit];
    expect(path).toBe("/api/board/discovery");
    expect(init.method).toBe("POST");
    expect(JSON.parse(String(init.body))).toEqual({ id: "D-1", action: "accept", priority: "high" });
    expect(store.getState().snapshot?.revision).toBe(4);
    expect(store.getState().pendingActions).toBe(0);
  });

  it("applies dismiss optimistically without adding a task", async () => {
    const apiFetch = vi.fn(() => Promise.resolve(jsonResponse(snapshot({ revision: 4 }))));
    const store = createBoardStore({ apiFetch });
    store.acceptSnapshot(snapshot());
    const resolution = store.resolveDiscovery("D-1", "dismiss");
    expect(store.getState().snapshot?.discoveries.find((d) => d.id === "D-1")?.status).toBe("dismissed");
    expect(store.getState().snapshot?.tasks).toHaveLength(2);
    await resolution;
  });

  it("rolls back and toasts when the request fails", async () => {
    const before = snapshot();
    const store = createBoardStore({ apiFetch: () => Promise.reject(new Error("network down")) });
    store.acceptSnapshot(before);
    const accepted = store.getState().snapshot;

    const resolution = store.resolveDiscovery("D-1", "accept", "low");
    expect(store.getState().snapshot?.discoveries[0]?.status).toBe("accepted");
    await expect(resolution).resolves.toBe(false);
    expect(store.getState().snapshot).toBe(accepted);
    expect(store.getState().toast?.message).toContain("network down");
    expect(store.getState().pendingActions).toBe(0);
  });

  it("rolls back, reloads, and says the plan changed on 409", async () => {
    const fresh = snapshot({ revision: 9 });
    const apiFetch = vi.fn(async (path: string) =>
      path === "/api/board" ? jsonResponse(fresh) : jsonResponse({ error: "plan changed" }, 409),
    );
    const store = createBoardStore({ apiFetch });
    store.acceptSnapshot(snapshot());

    await expect(store.resolveDiscovery("D-1", "dismiss")).resolves.toBe(false);
    expect(store.getState().toast?.message).toBe("The plan changed — board reloaded");
    expect(apiFetch).toHaveBeenCalledWith("/api/board");
    expect(store.getState().snapshot?.revision).toBe(9);
    expect(store.getState().snapshot?.discoveries[0]?.status).toBe("proposed");
  });

  it("surfaces the server error message for 400/404", async () => {
    const store = createBoardStore({ apiFetch: () => Promise.resolve(jsonResponse({ error: "unknown discovery" }, 404)) });
    store.acceptSnapshot(snapshot());
    await expect(store.resolveDiscovery("D-1", "dismiss")).resolves.toBe(false);
    expect(store.getState().toast?.message).toContain("unknown discovery");
    expect(store.getState().snapshot?.discoveries[0]?.status).toBe("proposed");
  });

  it("refuses a discovery that is not on the board without a request", async () => {
    const apiFetch = vi.fn();
    const store = createBoardStore({ apiFetch });
    store.acceptSnapshot(snapshot());
    await expect(store.resolveDiscovery("D-99", "dismiss")).resolves.toBe(false);
    expect(apiFetch).not.toHaveBeenCalled();
    expect(store.getState().toast?.message).toContain("D-99");
  });
});

describe("server-shaped discovered tasks", () => {
  const serverTask = (operation: string | undefined): BoardTask => {
    const target: Record<string, unknown> = { path: "internal/x.go", symbol_id: "", verified: false };
    if (operation !== undefined) target.operation = operation;
    return task("D-1", { discovered: true, targets: [target as unknown as BoardTask["targets"][number]] });
  };

  it("accepts an empty operation and normalizes it to modify", () => {
    const result = validateBoardSnapshot(snapshot({ tasks: [serverTask(""), task("T-2")] }));
    expect(result.ok).toBe(true);
    if (!result.ok) return;
    expect(result.value.tasks[0]?.discovered).toBe(true);
    expect(result.value.tasks[0]?.targets[0]?.operation).toBe("modify");
  });

  it("accepts a missing operation and still rejects an unknown one", () => {
    const missing = validateBoardSnapshot(snapshot({ tasks: [serverTask(undefined)] }));
    expect(missing.ok).toBe(true);
    if (missing.ok) expect(missing.value.tasks[0]?.targets[0]?.operation).toBe("modify");
    expect(validateBoardSnapshot(snapshot({ tasks: [serverTask("explode")] })).ok).toBe(false);
    expect(validateBoardSnapshot(snapshot({ tasks: [serverTask("toString")] })).ok).toBe(false);
  });

  it("keeps the store accepting later pushes after such a task", () => {
    const store = createBoardStore();
    expect(store.acceptSnapshot(snapshot({ revision: 4, tasks: [serverTask("")] }))).toBe(true);
    expect(store.acceptSnapshot(snapshot({ revision: 5, tasks: [serverTask("")] }))).toBe(true);
    expect(store.getState().snapshot?.tasks[0]?.targets[0]?.operation).toBe("modify");
  });
});

describe("finished tasks are not active", () => {
  const working = { state: "active", by: "claude:abc", source: "file_edit", detail: "a.go", at: "2026-10-19T11:59:00Z" } as const;

  it("leaves an active-but-done task out of Active now", () => {
    const view = deriveCoordinatorView(
      snapshot({
        tasks: [task("T-1", { column: "done", live: working }), task("T-2", { column: "in_progress", live: working })],
        coordinator: { active: ["T-1", "T-2"], next_up: [], updated_at: "" },
      }),
      NOW,
    );
    expect(view.active.map((entry) => entry.task.id)).toEqual(["T-2"]);
  });
});

describe("review requests", () => {
  it("gives every request a new sequence number, even across view changes", () => {
    const counter = { current: 0 };
    const first = nextReviewRequest(counter, "D-1");
    const second = nextReviewRequest(counter, "D-1");
    expect(second.seq).not.toBe(first.seq);
    expect(second.seq).toBeGreaterThan(first.seq);
  });
});

describe("coordinator panel derivations", () => {
  const live = (by: string, at: string, detail = "internal/x.go") =>
    ({ state: "active", by, source: "file_edit", detail, at }) as const;

  it("lists active tasks in coordinator order with actor, age, and detail", () => {
    const view = deriveCoordinatorView(
      snapshot({
        tasks: [
          task("T-1", { live: live("omp:s1", "2026-10-19T11:59:30Z", "web/a.tsx") }),
          task("T-2", { live: live("claude:abc", "2026-10-19T11:59:55Z") }),
          task("T-3", { live: { state: "done", by: "claude:abc", source: "report", detail: "", at: "2026-10-19T11:50:00Z" } }),
        ],
        coordinator: { active: ["T-2", "T-1"], next_up: [], updated_at: "" },
      }),
      NOW,
    );
    expect(view.active.map((entry) => entry.task.id)).toEqual(["T-2", "T-1"]);
    expect(view.active[0]).toMatchObject({ actor: "Claude Code", ago: "5s ago" });
    expect(view.active[1]).toMatchObject({ actor: "oh-my-pi", ago: "30s ago", detail: "web/a.tsx" });
  });

  it("orders unlisted active tasks by newest activity", () => {
    const view = deriveCoordinatorView(
      snapshot({
        tasks: [
          task("T-1", { live: live("claude:a", "2026-10-19T11:50:00Z") }),
          task("T-2", { live: live("claude:a", "2026-10-19T11:58:00Z") }),
        ],
        coordinator: { active: [], next_up: [], updated_at: "" },
      }),
      NOW,
    );
    expect(view.active.map((entry) => entry.task.id)).toEqual(["T-2", "T-1"]);
  });

  it("keeps next_up order, ranks from 1, and flags blocked entries", () => {
    const view = deriveCoordinatorView(
      snapshot({
        tasks: [task("T-1", { priority: "high" }), task("T-2", { blocked: true }), task("T-3")],
        coordinator: { active: [], next_up: ["T-3", "gone", "T-1", "T-2"], updated_at: "2026-10-19T11:59:00Z" },
      }),
      NOW,
    );
    expect(view.nextUp.map((entry) => [entry.task.id, entry.rank, entry.unblocked])).toEqual([
      ["T-3", 1, true],
      ["T-1", 2, true],
      ["T-2", 3, false],
    ]);
    expect(view.nextUp[0]?.hint).toBe("Unblocked · steering the agent");
    expect(view.nextUp[2]?.hint).toBe("Blocked");
    expect(view.updatedAgo).toBe("1m ago");
  });

  it("counts only proposed discoveries, split by kind", () => {
    const view = deriveCoordinatorView(
      snapshot({
        discoveries: [
          discovery("D-1"),
          discovery("D-2", { kind: "path" }),
          discovery("D-3", { kind: "path" }),
          discovery("D-4", { status: "accepted" }),
          discovery("D-5", { status: "dismissed" }),
        ],
      }),
      NOW,
    );
    expect(view.proposed.map((d) => d.id)).toEqual(["D-1", "D-2", "D-3"]);
    expect(view.proposedTasks).toBe(1);
    expect(view.proposedPaths).toBe(2);
  });

  it("formats ages and actors", () => {
    expect(formatAgo("2026-10-19T11:59:58Z", NOW)).toBe("just now");
    expect(formatAgo("2026-10-19T11:15:00Z", NOW)).toBe("45m ago");
    expect(formatAgo("2026-10-19T09:00:00Z", NOW)).toBe("3h ago");
    expect(formatAgo("2026-10-15T12:00:00Z", NOW)).toBe("4d ago");
    expect(formatAgo("not a date", NOW)).toBe("");
    expect(formatAgo("2026-10-19T12:00:30Z", NOW)).toBe("just now");
    expect(actorLabel("user")).toBe("You");
    expect(actorLabel("omp:1")).toBe("oh-my-pi");
    expect(actorLabel("")).toBe("the agent");
  });
});
