import { describe, expect, it, vi } from "vitest";

import { actionRequestBody, AUTH_FAILED_MESSAGE, createBoardStore } from "./boardStore";
import type { BoardMutation } from "./boardStore";
import {
  consumerHostLabel,
  deriveDeliveryChip,
  EMPTY_FILTERS,
  filterBoardTasks,
  sortBoardTasks,
} from "./boardUtils";
import type { BoardSnapshot, BoardTask } from "./types";
import { validateBoardSnapshot } from "./types";

type LegacyTask = Omit<BoardTask, "live" | "awaiting_verification" | "discovered">;

const withC11 = (task: LegacyTask): BoardTask => ({
  ...task,
  live: { state: "", by: "", source: "", detail: "", at: "" },
  awaiting_verification: false,
  discovered: false,
});

const TASKS: BoardTask[] = ([
  {
    id: "T-1",
    epic_id: "E-1",
    story_id: "S-1",
    workstream_id: "W-api",
    title: "Normal manifest-first task",
    column: "todo",
    blocked: false,
    depends_on: [],
    priority: "normal",
    targets: [{ path: "internal/first.go", operation: "add", symbol_id: "First", verified: false }],
    verification: { total: 1, passed: 0 },
    acceptance_criteria: ["First criterion"],
    human_estimate: { likely: 4, basis: "implementation" },
    agent_estimate: { likely: 8, basis: "grounded edit" },
    notes: [],
  },
  {
    id: "T-2",
    epic_id: "E-2",
    story_id: "S-2",
    workstream_id: "W-web",
    title: "Low task with searchable drawer",
    column: "todo",
    blocked: true,
    depends_on: ["T-1"],
    priority: "low",
    targets: [{ path: "web/src/board/BoardDrawer.tsx", operation: "add", symbol_id: "", verified: false }],
    verification: { total: 2, passed: 0 },
    acceptance_criteria: ["Drawer criterion"],
    human_estimate: { likely: 5, basis: "implementation" },
    agent_estimate: { likely: 9, basis: "grounded edit" },
    notes: [],
  },
  {
    id: "T-3",
    epic_id: "E-1",
    story_id: "S-3",
    workstream_id: "W-api",
    title: "High later task",
    column: "todo",
    blocked: false,
    depends_on: [],
    priority: "high",
    targets: [{ path: "internal/high.go", operation: "modify", symbol_id: "High", verified: true }],
    verification: { total: 1, passed: 1 },
    acceptance_criteria: ["High criterion"],
    human_estimate: { likely: 2, basis: "implementation" },
    agent_estimate: { likely: 3, basis: "grounded edit" },
    notes: [{ id: "n-1", at: "2026-10-19T12:00:00Z", text: "Keep this first." }],
  },
  {
    id: "T-4",
    epic_id: "E-2",
    story_id: "S-4",
    workstream_id: "W-web",
    title: "Second high task",
    column: "in_progress",
    blocked: false,
    depends_on: [],
    priority: "high",
    targets: [{ path: "web/src/board/BoardApp.tsx", operation: "add", symbol_id: "BoardApp", verified: true }],
    verification: { total: 2, passed: 1 },
    acceptance_criteria: ["Board criterion"],
    human_estimate: { likely: 6, basis: "implementation" },
    agent_estimate: { likely: 12, basis: "grounded edit" },
    notes: [],
  },
] as LegacyTask[]).map(withC11);

const GOOD_SNAPSHOT: BoardSnapshot = {
  repo: "agentic-plan-forge",
  title: "Board contract",
  manifest_sha256: "abc123",
  approved: true,
  status: "IN_PROGRESS",
  verified: 2,
  total: 4,
  revision: 7,
  host: "claude",
  evaluated_at: "2026-10-19T12:00:00Z",
  drift: [],
  tasks: TASKS,
  consumers: [{ id: "claude:test", delivered_revision: 6, seen_at: "2026-10-19T12:00:00Z" }],
  discoveries: [],
  swarm: [],
  coordinator: { active: [], next_up: [], updated_at: "" },
};

function jsonResponse(value: unknown, status = 200): Response {
  return new Response(JSON.stringify(value), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

describe("board snapshot validation", () => {
  it("accepts a complete C5 snapshot", () => {
    const result = validateBoardSnapshot(GOOD_SNAPSHOT);
    expect(result.ok).toBe(true);
    if (result.ok) expect(result.value.tasks).toHaveLength(4);
  });

  it("rejects malformed nested fields", () => {
    const malformed = {
      ...GOOD_SNAPSHOT,
      tasks: [{ ...TASKS[0]!, priority: "urgent" }],
    };
    const result = validateBoardSnapshot(malformed);
    expect(result).toEqual({ ok: false, error: "tasks are malformed" });
  });

  it("keeps the last good snapshot when a malformed update arrives", () => {
    const store = createBoardStore();
    expect(store.acceptSnapshot(GOOD_SNAPSHOT)).toBe(true);
    const accepted = store.getState().snapshot;
    expect(store.acceptSnapshot({ ...GOOD_SNAPSHOT, verified: "two" })).toBe(false);
    expect(store.getState().snapshot).toBe(accepted);
  });
});

describe("board task derivations", () => {
  it("sorts HIGH first, keeps manifest order within tiers, and puts LOW last", () => {
    const scrambled = [TASKS[1]!, TASKS[0]!, TASKS[3]!, TASKS[2]!];
    expect(sortBoardTasks(scrambled, ["T-1", "T-2", "T-3", "T-4"]).map((task) => task.id))
      .toEqual(["T-3", "T-4", "T-1", "T-2"]);
  });

  it("filters across id, title, target path, facets, and blocked state", () => {
    expect(filterBoardTasks(TASKS, { ...EMPTY_FILTERS, search: "boarddrawer" }).map((task) => task.id))
      .toEqual(["T-2"]);
    expect(filterBoardTasks(TASKS, { ...EMPTY_FILTERS, search: "T-3" }).map((task) => task.id))
      .toEqual(["T-3"]);
    expect(filterBoardTasks(TASKS, {
      ...EMPTY_FILTERS,
      epic: "E-2",
      workstream: "W-web",
      priority: "low",
      blockedOnly: true,
    }).map((task) => task.id)).toEqual(["T-2"]);
  });

  it("derives waiting and delivered copy from consumer revisions", () => {
    expect(deriveDeliveryChip(GOOD_SNAPSHOT)).toEqual({
      delivered: false,
      label: "Waiting for the agent's next step",
    });
    expect(deriveDeliveryChip({
      ...GOOD_SNAPSHOT,
      consumers: [{ ...GOOD_SNAPSHOT.consumers[0]!, delivered_revision: 7 }],
    })).toEqual({
      delivered: true,
      label: "Delivered to Claude Code · rev 7",
    });
  });

  it("names the delivering host from the consumer id prefix", () => {
    const delivered = (id: string) =>
      deriveDeliveryChip({
        ...GOOD_SNAPSHOT,
        consumers: [{ id, delivered_revision: 7, seen_at: "2026-10-19T12:00:00Z" }],
      }).label;
    expect(delivered("omp:session-1")).toBe("Delivered to oh-my-pi · rev 7");
    expect(delivered("claude:abc")).toBe("Delivered to Claude Code · rev 7");
    expect(delivered("opaque")).toBe("Delivered to the agent · rev 7");
    expect(consumerHostLabel("agy:1")).toBe("agy");
  });
});

describe("board mutations", () => {
  it("applies an optimistic priority and rolls it back on request failure", async () => {
    let rejectRequest: (reason?: unknown) => void = () => undefined;
    const pending = new Promise<Response>((_resolve, reject) => {
      rejectRequest = reject;
    });
    const apiFetchMock = vi.fn(() => pending);
    const store = createBoardStore({ apiFetch: apiFetchMock });
    store.acceptSnapshot(GOOD_SNAPSHOT);

    const mutation = store.mutate("T-1", { priority: "high" });
    expect(store.getState().snapshot?.tasks[0]?.priority).toBe("high");
    rejectRequest(new Error("network down"));

    await expect(mutation).resolves.toBe(false);
    expect(store.getState().snapshot?.tasks[0]?.priority).toBe("normal");
    expect(store.getState().toast?.message).toContain("network down");
  });

  it("sends exactly one action in each POST body", async () => {
    const actions: BoardMutation[] = [
      { priority: "high" },
      { note: "ship behind the existing gate" },
      { delete_note: "n-1" },
    ];
    for (const action of actions) {
      const body = actionRequestBody("T-1", action);
      const actionKeys = Object.keys(body).filter((key) => key !== "id");
      expect(actionKeys).toHaveLength(1);
    }

    const responseSnapshot: BoardSnapshot = {
      ...GOOD_SNAPSHOT,
      revision: 8,
      tasks: GOOD_SNAPSHOT.tasks.map((task) =>
        task.id === "T-1" ? { ...task, priority: "high" } : task,
      ),
    };
    const apiFetchMock = vi.fn(async (path: string, init?: RequestInit) => {
      expect(path).toBe("/api/board/task");
      expect(init?.method).toBe("POST");
      return jsonResponse(responseSnapshot);
    });
    const store = createBoardStore({ apiFetch: apiFetchMock });
    store.acceptSnapshot(GOOD_SNAPSHOT);
    await expect(store.mutate("T-1", { priority: "high" })).resolves.toBe(true);

    expect(apiFetchMock).toHaveBeenCalledTimes(1);
    const request = apiFetchMock.mock.calls[0]!;
    expect(request[0]).toBe("/api/board/task");
    const init = request[1];
    expect(init?.method).toBe("POST");
    expect(JSON.parse(String(init?.body))).toEqual({ id: "T-1", priority: "high" });
  });

  it("rejects accidental multi-action values before issuing a request", () => {
    const invalid = { priority: "high", note: "also a note" } as unknown as BoardMutation;
    expect(() => actionRequestBody("T-1", invalid)).toThrow("exactly one action");
  });
});

interface Deferred<T> {
  promise: Promise<T>;
  resolve: (value: T) => void;
  reject: (reason?: unknown) => void;
}

function deferred<T>(): Deferred<T> {
  let resolve: (value: T) => void = () => undefined;
  let reject: (reason?: unknown) => void = () => undefined;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

describe("concurrent board mutations", () => {
  it("rebuilds from the server when a failed mutation cannot roll back cleanly", async () => {
    const first = deferred<Response>();
    const second = deferred<Response>();
    const apiFetchMock = vi.fn(async (path: string, init?: RequestInit) => {
      if (path === "/api/board") return jsonResponse(GOOD_SNAPSHOT);
      const body = JSON.parse(String(init?.body)) as { id: string };
      return body.id === "T-1" ? first.promise : second.promise;
    });
    const store = createBoardStore({ apiFetch: apiFetchMock });
    store.acceptSnapshot(GOOD_SNAPSHOT);

    const firstMutation = store.mutate("T-1", { priority: "high" });
    const secondMutation = store.mutate("T-2", { priority: "normal" });
    expect(store.getState().snapshot?.tasks[0]?.priority).toBe("high");
    expect(store.getState().snapshot?.tasks[1]?.priority).toBe("normal");

    // The first request fails while the second is still in flight: the
    // snapshot is no longer the one the first mutation produced.
    first.reject(new Error("boom"));
    await expect(firstMutation).resolves.toBe(false);
    expect(store.getState().snapshot?.tasks[0]?.priority).toBe("normal");
    expect(store.getState().toast?.message).toContain("boom");

    second.resolve(jsonResponse({
      ...GOOD_SNAPSHOT,
      revision: 8,
      tasks: GOOD_SNAPSHOT.tasks.map((task) =>
        task.id === "T-2" ? { ...task, priority: "normal" } : task,
      ),
    }));
    await expect(secondMutation).resolves.toBe(true);
    const tasks = store.getState().snapshot?.tasks ?? [];
    expect(tasks.find((task) => task.id === "T-1")?.priority).toBe("normal");
    expect(tasks.find((task) => task.id === "T-2")?.priority).toBe("normal");
    expect(store.getState().pendingActions).toBe(0);
  });
});

class FakeSocket {
  onopen: (() => void) | null = null;
  onmessage: ((event: { data: string }) => void) | null = null;
  onerror: (() => void) | null = null;
  onclose: ((event: { code: number }) => void) | null = null;
  sent: string[] = [];
  send(data: string): void {
    this.sent.push(data);
  }
  close(): void {
    /* closing is driven explicitly by each test */
  }
}

function socketHarness(token: string | null) {
  const sockets: FakeSocket[] = [];
  const delays: number[] = [];
  let scheduled: (() => void) | null = null;
  const store = createBoardStore({
    bootstrap: () => Promise.resolve(token),
    apiFetch: () => Promise.resolve(jsonResponse(GOOD_SNAPSHOT)),
    socketURL: () => "ws://127.0.0.1/ws",
    createSocket: () => {
      const socket = new FakeSocket();
      sockets.push(socket);
      return socket as unknown as WebSocket;
    },
    schedule: (callback, delay) => {
      delays.push(delay);
      scheduled = callback;
      return 1;
    },
    cancel: () => undefined,
  });
  return {
    store,
    sockets,
    delays,
    runScheduled: () => {
      const callback = scheduled;
      scheduled = null;
      callback?.();
    },
  };
}

describe("board socket lifecycle", () => {
  it("opens immediately without an auth frame when there is no token (preview)", async () => {
    const harness = socketHarness(null);
    harness.store.start();
    await vi.waitFor(() => expect(harness.sockets).toHaveLength(1));

    // One failed connection pushes the backoff up...
    harness.sockets[0]!.onopen?.();
    expect(harness.sockets[0]!.sent).toEqual([]);
    expect(harness.store.getState().connection).toBe("open");
    harness.sockets[0]!.onclose?.({ code: 1006 });
    expect(harness.delays).toEqual([1000]);

    // ...and a token-less open resets it, so the next drop starts over.
    harness.runScheduled();
    harness.sockets[1]!.onopen?.();
    harness.sockets[1]!.onmessage?.({
      data: JSON.stringify({ type: "board.snapshot", snapshot: { ...GOOD_SNAPSHOT, revision: 9 } }),
    });
    expect(harness.store.getState().snapshot?.revision).toBe(9);
    harness.sockets[1]!.onclose?.({ code: 1006 });
    expect(harness.delays).toEqual([1000, 1000]);
    harness.store.stop();
  });

  it("does not reconnect after close code 4403 and reports an auth failure", async () => {
    const harness = socketHarness("token");
    harness.store.start();
    await vi.waitFor(() => expect(harness.sockets).toHaveLength(1));
    harness.sockets[0]!.onopen?.();
    expect(harness.sockets[0]!.sent).toHaveLength(1);

    harness.sockets[0]!.onclose?.({ code: 4403 });
    expect(harness.delays).toEqual([]);
    const state = harness.store.getState();
    expect(state.connection).toBe("offline");
    expect(state.lastError).toBe(AUTH_FAILED_MESSAGE);
    expect(state.authFailed).toBe(true);
    expect(AUTH_FAILED_MESSAGE).toBe("authentication failed — reopen from the plan-forge CLI");
    harness.store.stop();
  });
});
