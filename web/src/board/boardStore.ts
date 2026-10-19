import { useSyncExternalStore } from "react";

import {
  apiFetch,
  AUTH_OK,
  authFrame,
  bootstrap,
  bridgeWSURL,
  WS_SUBPROTOCOL,
} from "../protocol/bridge";
import type { BoardDiscovery, BoardNote, BoardPriority, BoardSnapshot, BoardTask } from "./types";
import { EMPTY_LIVE } from "./types";
import { validateBoardSnapshot } from "./types";

export type BoardConnection = "connecting" | "open" | "reconnecting" | "offline";
export type BoardMutation =
  | { priority: BoardPriority }
  | { note: string }
  | { delete_note: string };

export interface BoardToast {
  id: number;
  kind: "error";
  message: string;
}

export interface BoardState {
  snapshot: BoardSnapshot | null;
  connection: BoardConnection;
  loading: boolean;
  lastError: string | null;
  toast: BoardToast | null;
  pendingActions: number;
  /** True after the bridge closed the socket with 4403; reconnecting cannot help. */
  authFailed: boolean;
}
type TimerHandle = number | NodeJS.Timeout;


interface BoardStoreDependencies {
  bootstrap: () => Promise<string | null>;
  apiFetch: (path: string, init?: RequestInit) => Promise<Response>;
  socketURL: () => string;
  createSocket: (url: string, protocol: string) => WebSocket;
  schedule: (callback: () => void, delay: number) => TimerHandle;
  cancel: (timer: TimerHandle) => void;
  now: () => Date;
}

export interface BoardStore {
  subscribe: (listener: () => void) => () => void;
  getState: () => BoardState;
  start: () => void;
  stop: () => void;
  refresh: () => Promise<boolean>;
  acceptSnapshot: (value: unknown) => boolean;
  mutate: (taskID: string, action: BoardMutation) => Promise<boolean>;
  /** Accept (with a priority) or dismiss a harness discovery through `POST /api/board/discovery`. */
  resolveDiscovery: (id: string, ...resolution: DiscoveryResolution) => Promise<boolean>;
  clearToast: () => void;
}

const DEFAULT_STATE: BoardState = {
  snapshot: null,
  connection: "connecting",
  loading: true,
  lastError: null,
  toast: null,
  pendingActions: 0,
  authFailed: false,
};

export const AUTH_FAILED_MESSAGE = "authentication failed — reopen from the plan-forge CLI";

const DEFAULT_DEPENDENCIES: BoardStoreDependencies = {
  bootstrap,
  apiFetch,
  socketURL: bridgeWSURL,
  createSocket: (url, protocol) => new WebSocket(url, protocol),
  schedule: (callback, delay) => globalThis.setTimeout(callback, delay),
  cancel: (timer) => globalThis.clearTimeout(timer),
  now: () => new Date(),
};

function describeError(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

export type DiscoveryResolution = ["accept", BoardPriority] | ["dismiss"];

/** Build the C11 discovery request body; exactly one of accept (with priority) or dismiss. */
export function discoveryRequestBody(
  discoveryID: string,
  ...resolution: DiscoveryResolution
): Record<string, string> {
  if (discoveryID === "") throw new Error("discovery id is required");
  if (resolution[0] === "dismiss") return { id: discoveryID, action: "dismiss" };
  if (
    resolution[0] === "accept" &&
    (["high", "normal", "low"] as string[]).includes(resolution[1])
  ) {
    return { id: discoveryID, action: "accept", priority: resolution[1] };
  }
  throw new Error("invalid discovery action");
}

/** The accepted board task the server will create for a task discovery (C9/C11), used optimistically. */
function discoveredTask(discovery: BoardDiscovery, priority: BoardPriority): BoardTask {
  return {
    id: discovery.id,
    epic_id: "",
    story_id: "",
    workstream_id: "",
    title: discovery.title,
    column: "todo",
    blocked: false,
    depends_on: discovery.parent_task_id === "" ? [] : [discovery.parent_task_id],
    priority,
    targets: discovery.paths.map((path) => ({
      path,
      operation: "modify" as const,
      symbol_id: "",
      verified: false,
    })),
    verification: { total: 0, passed: 0 },
    acceptance_criteria: [],
    human_estimate: { likely: 0, basis: "" },
    agent_estimate: { likely: 0, basis: "" },
    notes: [],
    live: EMPTY_LIVE,
    awaiting_verification: false,
    discovered: true,
  };
}

function optimisticDiscoverySnapshot(
  snapshot: BoardSnapshot,
  discovery: BoardDiscovery,
  resolution: DiscoveryResolution,
): BoardSnapshot {
  const accepted = resolution[0] === "accept";
  const priority = resolution[0] === "accept" ? resolution[1] : discovery.priority;
  const discoveries = snapshot.discoveries.map((candidate) =>
    candidate.id === discovery.id
      ? { ...candidate, status: accepted ? ("accepted" as const) : ("dismissed" as const), priority }
      : candidate,
  );
  const addTask =
    accepted &&
    discovery.kind === "task" &&
    !snapshot.tasks.some((task) => task.id === discovery.id);
  return {
    ...snapshot,
    discoveries,
    tasks: addTask ? [...snapshot.tasks, discoveredTask(discovery, priority)] : snapshot.tasks,
  };
}

/** Build the C5 request body and reject any accidental multi-action object. */
export function actionRequestBody(taskID: string, action: BoardMutation): Record<string, string> {
  if (taskID === "") throw new Error("task id is required");
  const keys = Object.keys(action);
  if (keys.length !== 1) throw new Error("a board mutation must contain exactly one action");

  if ("priority" in action) {
    if (!(["high", "normal", "low"] as string[]).includes(action.priority)) {
      throw new Error("invalid priority");
    }
    return { id: taskID, priority: action.priority };
  }
  if ("note" in action) {
    const note = action.note.trim();
    if (note === "") throw new Error("context note cannot be empty");
    if (Array.from(note).length > 2000) throw new Error("context note is longer than 2000 characters");
    return { id: taskID, note };
  }
  if ("delete_note" in action && action.delete_note !== "") {
    return { id: taskID, delete_note: action.delete_note };
  }
  throw new Error("invalid board action");
}

export function createBoardStore(
  overrides: Partial<BoardStoreDependencies> = {},
): BoardStore {
  const dependencies: BoardStoreDependencies = { ...DEFAULT_DEPENDENCIES, ...overrides };
  let state: BoardState = { ...DEFAULT_STATE };
  const listeners = new Set<() => void>();
  let socket: WebSocket | null = null;
  let reconnectTimer: TimerHandle | null = null;
  let reconnectAttempt = 0;
  let active = false;
  let generation = 0;
  let sessionToken: string | null = null;
  let toastSequence = 0;
  let optimisticSequence = 0;

  function emit(): void {
    for (const listener of listeners) listener();
  }

  function update(patch: Partial<BoardState>): void {
    state = { ...state, ...patch };
    emit();
  }

  function errorToast(message: string): void {
    toastSequence += 1;
    update({ toast: { id: toastSequence, kind: "error", message }, lastError: message });
  }

  function acceptSnapshot(value: unknown): boolean {
    const result = validateBoardSnapshot(value);
    if (!result.ok) {
      errorToast(`Invalid board update ignored: ${result.error}`);
      return false;
    }
    const current = state.snapshot;
    if (
      current !== null &&
      current.manifest_sha256 === result.value.manifest_sha256 &&
      result.value.revision < current.revision
    ) {
      return true;
    }
    update({ snapshot: result.value, loading: false, lastError: null });
    return true;
  }

  async function refresh(): Promise<boolean> {
    try {
      const response = await dependencies.apiFetch("/api/board");
      if (!response.ok) throw new Error(`board request failed (${String(response.status)})`);
      const payload: unknown = await response.json();
      return acceptSnapshot(payload);
    } catch (error) {
      const message = `Unable to load the board: ${describeError(error)}`;
      update({
        loading: false,
        connection: state.snapshot === null ? "offline" : state.connection,
        lastError: message,
      });
      return false;
    }
  }

  function scheduleReconnect(token: string | null): void {
    if (!active || reconnectTimer !== null) return;
    const delay = Math.min(10_000, 1000 * 2 ** reconnectAttempt);
    reconnectAttempt += 1;
    update({ connection: state.snapshot === null ? "offline" : "reconnecting" });
    reconnectTimer = dependencies.schedule(() => {
      reconnectTimer = null;
      if (active) openSocket(token);
    }, delay);
  }

  function openSocket(token: string | null): void {
    if (!active) return;
    const openGeneration = generation;
    let nextSocket: WebSocket;
    try {
      nextSocket = dependencies.createSocket(dependencies.socketURL(), WS_SUBPROTOCOL);
      socket = nextSocket;
    } catch {
      update({ connection: state.snapshot === null ? "offline" : "reconnecting" });
      scheduleReconnect(token);
      return;
    }

    // The preview harness has no token and so no auth handshake: the socket is
    // usable as soon as it opens.
    let authenticated = token === null;
    nextSocket.onopen = () => {
      if (!active || openGeneration !== generation) return;
      if (token === null) {
        reconnectAttempt = 0;
        update({ connection: "open", lastError: null, authFailed: false });
        return;
      }
      // Authentication is the only frame Board sends; snapshots are server-push only.
      nextSocket.send(authFrame(token));
    };
    nextSocket.onmessage = (event: MessageEvent) => {
      if (!active || openGeneration !== generation || typeof event.data !== "string") return;
      if (event.data.length > 4 * 1024 * 1024) {
        nextSocket.close(4408, "board frame too large");
        return;
      }
      let frame: unknown;
      try {
        frame = JSON.parse(event.data) as unknown;
      } catch {
        nextSocket.close(4408, "invalid board frame");
        return;
      }
      if (typeof frame !== "object" || frame === null || Array.isArray(frame)) {
        nextSocket.close(4408, "invalid board frame");
        return;
      }
      const typedFrame = frame as { type?: unknown; snapshot?: unknown };
      if (typedFrame.type === AUTH_OK) {
        authenticated = true;
        reconnectAttempt = 0;
        update({ connection: "open", lastError: null, authFailed: false });
        return;
      }
      if (!authenticated) {
        nextSocket.close(4408, "board frame before authentication");
        return;
      }
      if (typedFrame.type === "board.snapshot") {
        acceptSnapshot(typedFrame.snapshot);
      }
    };
    nextSocket.onerror = () => {
      if (active && openGeneration === generation) {
        update({ connection: "offline", lastError: "The board connection is offline." });
      }
    };
    nextSocket.onclose = (event: CloseEvent) => {
      if (!active || openGeneration !== generation) return;
      if (socket === nextSocket) socket = null;
      // 4403 is the bridge refusing the auth frame: the token will not become
      // valid by retrying, so this is a standing state, never a reconnect.
      if (event.code === 4403) {
        update({ connection: "offline", lastError: AUTH_FAILED_MESSAGE, authFailed: true });
        return;
      }
      scheduleReconnect(token);
    };
  }

  function start(): void {
    if (active) return;
    active = true;
    generation += 1;
    const startGeneration = generation;
    update({
      connection: state.snapshot === null ? "connecting" : "reconnecting",
      loading: state.snapshot === null,
      lastError: null,
    });
    void dependencies.bootstrap().then(
      (token) => {
        if (!active || generation !== startGeneration) return;
        sessionToken = token;
        void refresh();
        openSocket(sessionToken);
      },
      (error: unknown) => {
        if (!active || generation !== startGeneration) return;
        update({
          connection: "offline",
          loading: false,
          lastError: `Session handshake failed: ${describeError(error)}`,
        });
      },
    );
  }

  function stop(): void {
    if (!active) return;
    active = false;
    generation += 1;
    if (reconnectTimer !== null) dependencies.cancel(reconnectTimer);
    reconnectTimer = null;
    if (socket !== null) {
      socket.onopen = null;
      socket.onmessage = null;
      socket.onerror = null;
      socket.onclose = null;
      socket.close(1000, "board closing");
    }
    socket = null;
  }

  function optimisticSnapshot(
    snapshot: BoardSnapshot,
    taskID: string,
    action: BoardMutation,
    requestBody: Record<string, string>,
  ): BoardSnapshot {
    const tasks = snapshot.tasks.map((task) => {
      if (task.id !== taskID) return task;
      if ("priority" in action) return { ...task, priority: action.priority };
      if ("note" in action) {
        optimisticSequence += 1;
        const note: BoardNote = {
          id: `optimistic-${String(optimisticSequence)}`,
          at: dependencies.now().toISOString(),
          text: requestBody.note ?? "",
        };
        return { ...task, notes: [...task.notes, note] };
      }
      return {
        ...task,
        notes: task.notes.filter((note) => note.id !== action.delete_note),
      };
    });
    return { ...snapshot, tasks };
  }

  /**
   * Apply `optimistic` immediately, POST `body`, and reconcile. Shared by task
   * and discovery mutations so both have identical 409 / error / rollback rules.
   */
  async function submit(
    path: string,
    body: Record<string, string>,
    previous: BoardSnapshot,
    optimistic: BoardSnapshot,
  ): Promise<boolean> {
    update({ snapshot: optimistic, pendingActions: state.pendingActions + 1 });

    try {
      const response = await dependencies.apiFetch(path, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      if (response.status === 409) {
        if (state.snapshot === optimistic) update({ snapshot: previous });
        errorToast("The plan changed — board reloaded");
        await refresh();
        return false;
      }
      if (!response.ok) {
        const payload: unknown = await response.json().catch(() => null);
        let serverError: unknown;
        if (
          typeof payload === "object" &&
          payload !== null &&
          !Array.isArray(payload) &&
          "error" in payload
        ) {
          serverError = payload.error;
        }
        throw new Error(
          typeof serverError === "string"
            ? serverError
            : `board update failed (${String(response.status)})`,
        );
      }
      const payload: unknown = await response.json();
      if (!acceptSnapshot(payload)) throw new Error("the server returned a malformed board snapshot");
      return true;
    } catch (error) {
      if (state.snapshot === optimistic) {
        update({ snapshot: previous });
        errorToast(`Board update failed: ${describeError(error)}`);
      } else {
        // Another mutation (or a server push) replaced the snapshot while this
        // request was in flight: restoring `previous` would erase that work, so
        // rebuild from the server's view instead.
        errorToast(`Board update failed: ${describeError(error)}`);
        await refresh();
      }
      return false;
    } finally {
      update({ pendingActions: Math.max(0, state.pendingActions - 1) });
    }
  }

  async function mutate(taskID: string, action: BoardMutation): Promise<boolean> {
    const previous = state.snapshot;
    if (previous === null || !previous.tasks.some((task) => task.id === taskID)) {
      errorToast(`Task ${taskID} is no longer on this board.`);
      return false;
    }

    let body: Record<string, string>;
    try {
      body = actionRequestBody(taskID, action);
    } catch (error) {
      errorToast(describeError(error));
      return false;
    }

    return submit("/api/board/task", body, previous, optimisticSnapshot(previous, taskID, action, body));
  }

  async function resolveDiscovery(id: string, ...resolution: DiscoveryResolution): Promise<boolean> {
    const previous = state.snapshot;
    const discovery = previous?.discoveries.find((candidate) => candidate.id === id);
    if (previous === null || discovery === undefined) {
      errorToast(`Discovery ${id} is no longer on this board.`);
      return false;
    }

    let body: Record<string, string>;
    try {
      body = discoveryRequestBody(id, ...resolution);
    } catch (error) {
      errorToast(describeError(error));
      return false;
    }

    return submit(
      "/api/board/discovery",
      body,
      previous,
      optimisticDiscoverySnapshot(previous, discovery, resolution),
    );
  }

  return {
    subscribe(listener) {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    getState: () => state,
    start,
    stop,
    refresh,
    acceptSnapshot,
    mutate,
    resolveDiscovery,
    clearToast: () => update({ toast: null }),
  };
}

export const boardStore = createBoardStore();

export function useBoardState(): BoardState {
  return useSyncExternalStore(boardStore.subscribe, boardStore.getState, boardStore.getState);
}
