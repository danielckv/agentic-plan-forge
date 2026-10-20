import assert from "node:assert/strict";
import { test } from "node:test";

import { createHook } from "./omp-hook-plan-forge.ts";
import type { ExecResult, HookDeps } from "./omp-hook-plan-forge.ts";

type ExecScript = (args: string[]) => unknown;

interface SessionResult {
  cancelled?: boolean;
}

interface CtxLike {
  cwd: string;
  ui: {
    notify(message: string, kind?: string): void;
    setStatus(key: string, text: string): void;
    setEditorText(text: string): void;
    confirm(title: string, message: string): Promise<boolean>;
  };
  isIdle(): boolean;
  waitForIdle(): Promise<void>;
  newSession(opts: {
    parentSession: unknown;
    setup(sm: { appendModeChange?(mode: string, data?: unknown): void }): Promise<void>;
  }): Promise<SessionResult | undefined>;
  switchSession?(sessionFile: string): Promise<SessionResult | undefined>;
  sessionManager: { getSessionFile(): string };
  setInterval(fn: () => void, ms: number): unknown;
  clearTimer(timer: unknown): void;
}

interface FakeCtxOptions {
  idle?: boolean;
  confirmYes?: boolean;
  order?: string[];
  newSessionThrows?: boolean;
  newSessionCancelled?: boolean;
  noAppendModeChange?: boolean;
  noSwitchSession?: boolean;
  switchSessionThrows?: boolean;
  switchSessionCancelled?: boolean;
}

const PARENT_FILE = "/sessions/abc123.jsonl";
const CHILD_FILE = "/sessions/new456.jsonl";

class FakeCtx implements CtxLike {
  cwd = "/repo";
  idle: boolean;
  notifications: { message: string; kind?: string }[] = [];
  statuses: [string, string][] = [];
  editorTexts: string[] = [];
  confirmCalls = 0;
  waitForIdleCalls = 0;
  newSessionCalls: { parentSession: unknown }[] = [];
  modeChanges: { mode: string; data: unknown }[] = [];
  switchCalls: string[] = [];
  intervals: { fn: () => void; ms: number }[] = [];
  clearedTimers: unknown[] = [];
  currentFile = PARENT_FILE;
  sessionManager = { getSessionFile: (): string => this.currentFile };
  switchSession?: (sessionFile: string) => Promise<SessionResult | undefined>;
  private readonly options: FakeCtxOptions;

  constructor(options: FakeCtxOptions = {}) {
    this.options = options;
    this.idle = options.idle ?? true;
    if (!options.noSwitchSession) {
      this.switchSession = async (sessionFile: string): Promise<SessionResult | undefined> => {
        this.options.order?.push("switchSession");
        if (this.options.switchSessionThrows) throw new Error("switchSession failed");
        this.switchCalls.push(sessionFile);
        return this.options.switchSessionCancelled ? { cancelled: true } : { cancelled: false };
      };
    }
  }

  ui = {
    notify: (message: string, kind?: string): void => {
      this.notifications.push({ message, kind });
    },
    setStatus: (key: string, text: string): void => {
      this.statuses.push([key, text]);
    },
    setEditorText: (text: string): void => {
      this.editorTexts.push(text);
    },
    confirm: async (_title: string, _message: string): Promise<boolean> => {
      this.confirmCalls++;
      return this.options.confirmYes ?? false;
    },
  };

  isIdle(): boolean {
    return this.idle;
  }

  async waitForIdle(): Promise<void> {
    this.waitForIdleCalls++;
  }

  async newSession(opts: {
    parentSession: unknown;
    setup(sm: { appendModeChange?(mode: string, data?: unknown): void }): Promise<void>;
  }): Promise<SessionResult | undefined> {
    if (this.options.newSessionThrows) throw new Error("newSession unavailable");
    const sm = this.options.noAppendModeChange
      ? {}
      : {
          appendModeChange: (mode: string, data?: unknown): void => {
            this.options.order?.push(`appendModeChange:${mode}`);
            this.modeChanges.push({ mode, data });
          },
        };
    await opts.setup(sm);
    this.options.order?.push("newSession");
    this.newSessionCalls.push({ parentSession: opts.parentSession });
    if (this.options.newSessionCancelled) return { cancelled: true };
    this.currentFile = CHILD_FILE;
    return { cancelled: false };
  }

  setInterval(fn: () => void, ms: number): unknown {
    this.intervals.push({ fn, ms });
    return this.intervals.length;
  }

  clearTimer(timer: unknown): void {
    this.clearedTimers.push(timer);
  }
}

interface FakePiOptions {
  order?: string[];
  sendThrows?: boolean;
  noSendUserMessage?: boolean;
}

class FakePi {
  execCalls: string[][] = [];
  messages: { message: unknown; options?: { deliverAs?: "nextTurn" | "steer" } }[] = [];
  userMessages: string[] = [];
  commands = new Map<string, { description: string; handler(args: string, ctx: CtxLike): Promise<void> }>();
  handlers = new Map<string, ((event: unknown, ctx: CtxLike) => unknown)[]>();
  sendUserMessage?: (text: string) => void;
  private readonly script: ExecScript;

  constructor(script: ExecScript, options: FakePiOptions = {}) {
    this.script = script;
    if (!options.noSendUserMessage) {
      this.sendUserMessage = (text: string): void => {
        options.order?.push("sendUserMessage");
        if (options.sendThrows) throw new Error("sendUserMessage failed");
        this.userMessages.push(text);
      };
    }
  }

  execOptions: ({ timeout?: number; signal?: AbortSignal } | undefined)[] = [];

  exec(_bin: string, args: string[], options?: { timeout?: number; signal?: AbortSignal }): unknown {
    this.execCalls.push([...args]);
    this.execOptions.push(options);
    return this.script(args);
  }

  sendMessage(message: unknown, options?: { deliverAs?: "nextTurn" | "steer" }): void {
    this.messages.push({ message, options });
  }

  registerCommand(
    name: string,
    spec: { description: string; handler(args: string, ctx: CtxLike): Promise<void> },
  ): void {
    this.commands.set(name, spec);
  }

  on(event: string, handler: (event: unknown, ctx: CtxLike) => unknown): void {
    const list = this.handlers.get(event) ?? [];
    list.push(handler);
    this.handlers.set(event, list);
  }

  // The swarm protocol's event bus. channels stays empty when a test builds a
  // FakePi without one, which is how an older OMP looks to the hook.
  channels = new Map<string, ((payload: unknown) => void)[]>();
  events = {
    on: (channel: string, handler: (payload: unknown) => void): void => {
      const list = this.channels.get(channel) ?? [];
      list.push(handler);
      this.channels.set(channel, list);
    },
  };

  publish(channel: string, payload: unknown): void {
    for (const handler of this.channels.get(channel) ?? []) handler(payload);
  }
}

function makeDeps(): HookDeps {
  return {
    execTimeoutMs: 500,
    syncIntervalMs: 4000,
    waitTimeout: "1s",
    swarmCoalesceMs: 5,
    sleep: async () => {},
  };
}

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

const APPROVED_WAIT = JSON.stringify({
  session_id: "s1",
  state: "approved",
  artifact_paths: ["/repo/docs/plans/.plan/synthesized_spec.md"],
  validation: { valid: true, status: "UNSTARTED", verified: 0, total: 9, drift: [] },
});

const BOARD_DELTA =
  'Plan-Forge board update (revision 7). The user re-prioritised or added context to the approved plan.\n' +
  '- T-4 "Add export endpoint": priority -> HIGH\n' +
  "High-priority tasks, do these first: T-4";

function forgeScript(waitsBefore: { current: number }, finalWait: ExecResult): ExecScript {
  return (args) => {
    switch (args[0]) {
      case "config":
        return { code: 0, stdout: "{}", stderr: "" };
      case "run":
        return { code: 0, stdout: JSON.stringify({ session_id: "s1" }), stderr: "" };
      case "wait":
        waitsBefore.current++;
        return waitsBefore.current < 3 ? { code: 75, stdout: "", stderr: "" } : finalWait;
      default:
        return { code: 0, stdout: "", stderr: "" };
    }
  };
}

// flush drains the promise continuations queued behind the interval callback
// (two event-loop turns; a yield, not a wall-clock wait — no timers involved).
async function flush(): Promise<void> {
  await new Promise<void>((resolve) => setImmediate(resolve));
  await new Promise<void>((resolve) => setImmediate(resolve));
}

test("plan-forge command: wait loop through 75 to 0, then plan-mode session via mode_change + switchSession", async () => {
  const order: string[] = [];
  const waits = { current: 0 };
  const pi = new FakePi(forgeScript(waits, { code: 0, stdout: APPROVED_WAIT, stderr: "" }), { order });
  createHook(pi, makeDeps());
  const ctx = new FakeCtx({ order });

  const command = pi.commands.get("plan-forge");
  assert.ok(command, "plan-forge command registered");
  await command.handler("", ctx);

  assert.equal(waits.current, 3, "two 75 polls then exit 0");
  assert.deepEqual(
    pi.execCalls[1],
    ["run", "--repo", "/repo", "--host", "omp", "--mode", "solo", "--detach"],
  );
  assert.deepEqual(pi.execCalls[2], ["wait", "--session", "s1", "--timeout", "1s"]);
  assert.equal(ctx.waitForIdleCalls, 1);
  assert.deepEqual(
    order,
    ["appendModeChange:plan", "newSession", "switchSession", "sendUserMessage"],
    "mode_change recorded in setup, then reconcile via switchSession, then the turn starts",
  );
  assert.equal(ctx.newSessionCalls.length, 1);
  assert.equal(ctx.newSessionCalls[0].parentSession, PARENT_FILE);
  assert.deepEqual(ctx.modeChanges, [{ mode: "plan", data: {} }]);
  assert.deepEqual(ctx.switchCalls, [CHILD_FILE], "switchSession targets the new session file");
  assert.equal(pi.userMessages.length, 1);
  const prompt = pi.userMessages[0];
  assert.match(prompt, /docs\/plans\/\.plan\/synthesized_spec\.md/);
  assert.match(prompt, /valid=true status=UNSTARTED verified=0\/9/);
  assert.ok(
    prompt.includes(
      "Do not edit any file. Plan only: read the spec, review the validation, run plan-forge validate if needed, " +
        "verify AST targets, and present the execution checklist for approval.",
    ),
    "prompt is safe even when plan mode did not engage",
  );
  assert.ok(!prompt.startsWith("/plan"), "the session is already in plan mode");
  assert.equal(ctx.editorTexts.length, 0, "no fallback prefill on the happy path");
  assert.ok(ctx.statuses.some(([key, text]) => key === "plan-forge" && text.includes("waiting")));
  assert.ok(ctx.notifications.some((n) => n.message.includes("plan-mode session opened")));
});

const FALLBACK_CASES: {
  name: string;
  ctx: FakeCtxOptions;
  pi: FakePiOptions;
  expectedOrder: string[];
}[] = [
  {
    name: "newSession throws",
    ctx: { newSessionThrows: true },
    pi: {},
    expectedOrder: [],
  },
  {
    name: "newSession is cancelled",
    ctx: { newSessionCancelled: true },
    pi: {},
    expectedOrder: ["appendModeChange:plan", "newSession"],
  },
  {
    name: "appendModeChange is missing in setup",
    ctx: { noAppendModeChange: true },
    pi: {},
    expectedOrder: [],
  },
  {
    name: "switchSession is unavailable",
    ctx: { noSwitchSession: true },
    pi: {},
    expectedOrder: [],
  },
  {
    name: "switchSession throws",
    ctx: { switchSessionThrows: true },
    pi: {},
    expectedOrder: ["appendModeChange:plan", "newSession", "switchSession"],
  },
  {
    name: "switchSession is cancelled",
    ctx: { switchSessionCancelled: true },
    pi: {},
    expectedOrder: ["appendModeChange:plan", "newSession", "switchSession"],
  },
  {
    name: "sendUserMessage is unavailable",
    ctx: {},
    pi: { noSendUserMessage: true },
    expectedOrder: [],
  },
];

for (const c of FALLBACK_CASES) {
  test(`plan-forge command: ${c.name} -> falls back to the /plan prefill`, async () => {
    const order: string[] = [];
    const pi = new FakePi(forgeScript({ current: 0 }, { code: 0, stdout: APPROVED_WAIT, stderr: "" }), {
      ...c.pi,
      order,
    });
    createHook(pi, makeDeps());
    const ctx = new FakeCtx({ ...c.ctx, order });

    await pi.commands.get("plan-forge")!.handler("", ctx);

    assert.deepEqual(order, c.expectedOrder, "no step runs after the failing one");
    assert.equal(pi.userMessages.length, 0, "no turn started through sendUserMessage");
    assert.equal(ctx.editorTexts.length, 1);
    assert.ok(ctx.editorTexts[0].startsWith("/plan "), "editor prefilled with /plan");
    assert.match(ctx.editorTexts[0], /synthesized_spec\.md/);
    assert.match(ctx.editorTexts[0], /Do not edit any file\. Plan only:/);
    assert.ok(ctx.notifications.some((n) => n.message.includes("press Enter")));
  });
}

test("plan-forge command: exit 130 notifies and never opens a session", async () => {
  const order: string[] = [];
  const pi = new FakePi(forgeScript({ current: 0 }, { code: 130, stdout: "", stderr: "" }), { order });
  createHook(pi, makeDeps());
  const ctx = new FakeCtx({ order });

  await pi.commands.get("plan-forge")!.handler("", ctx);

  assert.ok(ctx.notifications.some((n) => n.message.includes("closed without approval")));
  assert.equal(ctx.newSessionCalls.length, 0);
  assert.equal(ctx.editorTexts.length, 0);
  assert.equal(pi.userMessages.length, 0);
  assert.deepEqual(order, []);
});

test("board command and sync timer: deliverAs nextTurn when idle, steer when busy", async () => {
  const pi = new FakePi((args) => {
    if (args[0] === "board" && args[1] === "pull") {
      return { code: 0, stdout: BOARD_DELTA + "\n", stderr: "" };
    }
    if (args[0] === "board") {
      return { code: 0, stdout: JSON.stringify({ board: "open", pid: 42 }), stderr: "" };
    }
    return { code: 0, stdout: "", stderr: "" };
  });
  createHook(pi, makeDeps());
  const ctx = new FakeCtx({ idle: true });

  const command = pi.commands.get("plan-forge-board");
  assert.ok(command, "plan-forge-board command registered");
  await command.handler("", ctx);

  assert.ok(ctx.notifications.some((n) => n.message.includes("board is open")));
  assert.equal(ctx.intervals.length, 1, "sync timer started");
  assert.equal(ctx.intervals[0].ms, 4000);

  ctx.intervals[0].fn();
  await flush();
  assert.equal(pi.messages.length, 1);
  const first = pi.messages[0].message as { customType?: string; content?: string; display?: boolean };
  assert.equal(first.customType, "plan-forge-board");
  assert.equal(first.content, BOARD_DELTA);
  assert.equal(first.display, true);
  assert.equal(pi.messages[0].options?.deliverAs, "nextTurn");
  assert.deepEqual(
    pi.execCalls.find((call) => call[1] === "pull"),
    ["board", "pull", "--repo", "/repo", "--consumer", "omp:abc123", "--format", "text"],
  );
  assert.ok(ctx.statuses.some(([key, text]) => key === "plan-forge" && text.includes("r7")));

  ctx.idle = false;
  ctx.intervals[0].fn();
  await flush();
  assert.equal(pi.messages.length, 2);
  assert.equal(pi.messages[1].options?.deliverAs, "steer");
});

test("board sync: empty pull delivers no message", async () => {
  const pi = new FakePi((args) => {
    if (args[0] === "board" && args[1] === "pull") {
      return { code: 0, stdout: "", stderr: "" };
    }
    return { code: 0, stdout: JSON.stringify({ board: "open", pid: 42 }), stderr: "" };
  });
  createHook(pi, makeDeps());
  const ctx = new FakeCtx();

  await pi.commands.get("plan-forge-board")!.handler("", ctx);
  ctx.intervals[0].fn();
  await flush();

  assert.equal(pi.messages.length, 0);
});

test("tool_result: approved wait prefills /plan in the editor", async () => {
  const pi = new FakePi(() => ({ code: 0, stdout: "", stderr: "" }));
  createHook(pi, makeDeps());
  const ctx = new FakeCtx();

  const handlers = pi.handlers.get("tool_result") ?? [];
  assert.equal(handlers.length, 1);
  await handlers[0](
    {
      toolName: "bash",
      input: { command: '"/usr/local/bin/plan-forge" wait --session s1 --timeout 50s' },
      content: [{ type: "text", text: APPROVED_WAIT }],
      isError: false,
    },
    ctx,
  );

  assert.equal(ctx.editorTexts.length, 1);
  assert.ok(ctx.editorTexts[0].startsWith("/plan "));
  assert.match(ctx.editorTexts[0], /synthesized_spec\.md/);
  assert.ok(ctx.notifications.some((n) => n.message.includes("press Enter")));

  // A non-wait bash result and an error result stay untouched.
  await handlers[0](
    { toolName: "bash", input: { command: "ls -la" }, content: [{ type: "text", text: "ok" }], isError: false },
    ctx,
  );
  await handlers[0](
    {
      toolName: "bash",
      input: { command: "plan-forge wait --session s1" },
      content: [{ type: "text", text: APPROVED_WAIT }],
      isError: true,
    },
    ctx,
  );
  assert.equal(ctx.editorTexts.length, 1);
});

test("session_start: banner sent only for non-empty stdout", async () => {
  const pi = new FakePi((args) =>
    args[0] === "hook" ? { code: 0, stdout: "plan approved 3 days ago\n", stderr: "" } : { code: 0, stdout: "", stderr: "" },
  );
  createHook(pi, makeDeps());
  const ctx = new FakeCtx();

  const handlers = pi.handlers.get("session_start") ?? [];
  assert.equal(handlers.length, 1);
  await handlers[0]({}, ctx);
  assert.deepEqual(pi.messages.map((m) => m.message), ["plan approved 3 days ago"]);
});

// ---- review-fix tests ----

function shutdown(pi: FakePi, ctx: FakeCtx): void {
  const handlers = pi.handlers.get("session_shutdown") ?? [];
  assert.equal(handlers.length, 1, "session_shutdown handler registered");
  handlers[0]({}, ctx);
}

function deferred<T>(): { promise: Promise<T>; resolve(value: T): void } {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((r) => {
    resolve = r;
  });
  return { promise, resolve };
}

test("pi.exec receives timeout + signal; a hung child is aborted and the race timer is cleared", async () => {
  const handles: unknown[] = [];
  const cleared: unknown[] = [];
  const realSet = globalThis.setTimeout;
  const realClear = globalThis.clearTimeout;
  globalThis.setTimeout = ((fn: () => void, ms?: number, ...rest: unknown[]) => {
    const h = realSet(fn, ms, ...rest);
    if (ms === 12345) handles.push(h);
    return h;
  }) as typeof setTimeout;
  globalThis.clearTimeout = ((h: Parameters<typeof clearTimeout>[0]) => {
    cleared.push(h);
    return realClear(h);
  }) as typeof clearTimeout;
  try {
    // Fast child: the race timer must be cleared in finally.
    const fast = new FakePi(() => ({ code: 0, stdout: "banner", stderr: "" }));
    createHook(fast, { ...makeDeps(), execTimeoutMs: 12345 });
    await fast.handlers.get("session_start")![0]({}, new FakeCtx());
    assert.equal(handles.length, 1);
    assert.ok(cleared.includes(handles[0]), "race timer cleared after exec settles");
    // session_start also fires `center ensure` on its own budget, so find the
    // check-state call rather than assuming it is the first exec.
    const checkState = fast.execCalls.findIndex((a) => a[0] === "hook" && a[1] === "check-state");
    assert.ok(checkState >= 0, "check-state was not invoked");
    assert.equal(fast.execOptions[checkState]?.timeout, 12345);
    assert.ok(fast.execOptions[checkState]?.signal instanceof AbortSignal);
  } finally {
    globalThis.setTimeout = realSet;
    globalThis.clearTimeout = realClear;
  }

  // Hung child: the signal is aborted when the bound elapses.
  const hung = new FakePi(() => new Promise<never>(() => {}));
  createHook(hung, { ...makeDeps(), execTimeoutMs: 20 });
  await hung.handlers.get("session_start")![0]({}, new FakeCtx());
  const hungIdx = hung.execCalls.findIndex((a) => a[0] === "hook" && a[1] === "check-state");
  assert.ok(hungIdx >= 0, "check-state was not invoked");
  assert.equal(hung.execOptions[hungIdx]?.timeout, 20);
  assert.equal(hung.execOptions[hungIdx]?.signal?.aborted, true, "hung child aborted on timeout");
});

test("board sync: overlapping ticks are skipped while a pull is in flight", async () => {
  const gate = deferred<ExecResult>();
  let pulls = 0;
  const pi = new FakePi((args) => {
    if (args[0] === "board" && args[1] === "pull") {
      pulls++;
      return pulls === 1 ? gate.promise : { code: 0, stdout: "", stderr: "" };
    }
    return { code: 0, stdout: "{}", stderr: "" };
  });
  createHook(pi, makeDeps());
  const ctx = new FakeCtx();
  await pi.commands.get("plan-forge-board")!.handler("", ctx);

  ctx.intervals[0].fn();
  ctx.intervals[0].fn();
  ctx.intervals[0].fn();
  await flush();
  assert.equal(pulls, 1, "no overlapping pull while one is in flight");
  gate.resolve({ code: 0, stdout: "", stderr: "" });
  await flush();
  ctx.intervals[0].fn();
  await flush();
  assert.equal(pulls, 2, "next tick runs once the pull settled");
});

test("session_shutdown nulls ctx: a pull finishing afterwards sends nothing", async () => {
  const gate = deferred<ExecResult>();
  const pi = new FakePi((args) =>
    args[0] === "board" && args[1] === "pull" ? gate.promise : { code: 0, stdout: "{}", stderr: "" },
  );
  createHook(pi, makeDeps());
  const ctx = new FakeCtx();
  await pi.commands.get("plan-forge-board")!.handler("", ctx);
  ctx.intervals[0].fn();
  await flush();
  shutdown(pi, ctx);
  assert.equal(ctx.clearedTimers.length, 1, "timer cleared on shutdown");
  gate.resolve({ code: 0, stdout: BOARD_DELTA, stderr: "" });
  await flush();
  assert.equal(pi.messages.length, 0, "no sendMessage after shutdown");
});

test("plan-forge command: wait exit 0 without an approved plan never hands off", async () => {
  for (const stdout of ["", JSON.stringify({ state: "rejected" }), "not json"]) {
    const order: string[] = [];
    const pi = new FakePi(forgeScript({ current: 0 }, { code: 0, stdout, stderr: "" }), { order });
    createHook(pi, makeDeps());
    const ctx = new FakeCtx({ order });
    await pi.commands.get("plan-forge")!.handler("", ctx);
    assert.ok(
      ctx.notifications.some((n) => n.message === "plan-forge wait ended without an approved plan"),
      `notified for stdout=${JSON.stringify(stdout)}`,
    );
    assert.deepEqual(order, []);
    assert.equal(ctx.editorTexts.length, 0);
    assert.equal(pi.userMessages.length, 0);
  }
});

test("plan-forge command: missing exit code and killed:true are failures, not approvals", async () => {
  const cases: unknown[] = [
    { stdout: APPROVED_WAIT, stderr: "" },
    { code: 0, killed: true, stdout: APPROVED_WAIT, stderr: "" },
  ];
  for (const finalWait of cases) {
    const order: string[] = [];
    const pi = new FakePi(forgeScript({ current: 0 }, finalWait), { order });
    createHook(pi, makeDeps());
    const ctx = new FakeCtx({ order });
    await pi.commands.get("plan-forge")!.handler("", ctx);
    assert.ok(ctx.notifications.some((n) => n.message.startsWith("plan-forge wait failed")));
    assert.deepEqual(order, []);
    assert.equal(ctx.editorTexts.length, 0);
  }
});

test("notifications strip C0/C1 controls and cap stderr at 200 chars", async () => {
  const pi = new FakePi((args) =>
    args[0] === "run"
      ? { code: 1, stdout: "", stderr: "bad\u0085\u009bthing" + "x".repeat(400) + "\nsecond line" }
      : { code: 0, stdout: "{}", stderr: "" },
  );
  createHook(pi, makeDeps());
  const ctx = new FakeCtx();
  await pi.commands.get("plan-forge")!.handler("", ctx);
  const prefix = "plan-forge run failed: ";
  const note = ctx.notifications.find((n) => n.message.startsWith(prefix));
  assert.ok(note);
  assert.ok(!/[\u0000-\u001f\u007f-\u009f]/.test(note.message), "no control characters");
  assert.ok(note.message.length <= prefix.length + 200, "capped at 200");
  assert.ok(!note.message.includes("second line"));
});

test("plan-forge command: sendUserMessage failing after it was invoked notifies, never prefills", async () => {
  const order: string[] = [];
  const pi = new FakePi(forgeScript({ current: 0 }, { code: 0, stdout: APPROVED_WAIT, stderr: "" }), {
    order,
    sendThrows: true,
  });
  createHook(pi, makeDeps());
  const ctx = new FakeCtx({ order });
  await pi.commands.get("plan-forge")!.handler("", ctx);
  assert.deepEqual(order, ["appendModeChange:plan", "newSession", "switchSession", "sendUserMessage"]);
  assert.equal(ctx.editorTexts.length, 0, "no duplicate prefill");
  assert.ok(ctx.notifications.some((n) => n.kind === "error" && n.message.includes("sendUserMessage failed")));
  assert.ok(!ctx.notifications.some((n) => n.message.includes("press Enter")));
});

test("board command: sync starts only when the board exits 0", async () => {
  for (const code of [1, 3]) {
    const pi = new FakePi(() => ({ code, stdout: "", stderr: "nope" }));
    createHook(pi, makeDeps());
    const ctx = new FakeCtx();
    await pi.commands.get("plan-forge-board")!.handler("", ctx);
    assert.equal(ctx.intervals.length, 0, `no sync timer after board exit ${code}`);
  }
});

test("board command: no managed timers -> sync-unavailable notice", async () => {
  const pi = new FakePi(() => ({ code: 0, stdout: "{}", stderr: "" }));
  createHook(pi, makeDeps());
  const ctx = new FakeCtx();
  (ctx as unknown as { setInterval?: unknown }).setInterval = undefined;
  await pi.commands.get("plan-forge-board")!.handler("", ctx);
  assert.ok(
    ctx.notifications.some(
      (n) =>
        n.message === "board sync unavailable in this OMP version; edits reach the agent when you ask it",
    ),
  );
});

test("wait loop: stops when session_shutdown fires", async () => {
  let waits = 0;
  const holder: { pi?: FakePi; ctx?: FakeCtx } = {};
  const pi = new FakePi((args) => {
    if (args[0] === "wait") {
      waits++;
      if (waits === 1) shutdown(holder.pi!, holder.ctx!);
      return { code: waits > 5 ? 130 : 75, stdout: "", stderr: "" };
    }
    return args[0] === "run"
      ? { code: 0, stdout: JSON.stringify({ session_id: "s1" }), stderr: "" }
      : { code: 0, stdout: "{}", stderr: "" };
  });
  const ctx = new FakeCtx();
  holder.pi = pi;
  holder.ctx = ctx;
  createHook(pi, makeDeps());
  await pi.commands.get("plan-forge")!.handler("", ctx);
  assert.equal(waits, 1, "no wait polls after shutdown");
  assert.equal(ctx.newSessionCalls.length, 0);
});

test("wait loop: 1s delay only when exit 75 returns in under 5s", async () => {
  const sleeps: number[] = [];
  const run = async (now?: () => number): Promise<void> => {
    const pi = new FakePi(forgeScript({ current: 0 }, { code: 0, stdout: APPROVED_WAIT, stderr: "" }));
    createHook(pi, {
      ...makeDeps(),
      now,
      sleep: async (ms: number) => {
        sleeps.push(ms);
      },
    });
    await pi.commands.get("plan-forge")!.handler("", new FakeCtx());
  };
  await run();
  assert.deepEqual(sleeps, [1000, 1000], "fast 75 polls are delayed");
  sleeps.length = 0;
  let t = 0;
  await run(() => (t += 6000));
  assert.deepEqual(sleeps, [], "slow 75 polls are not delayed");
});

test("wait loop: capped, then notifies and stops", async () => {
  let waits = 0;
  const pi = new FakePi((args) => {
    if (args[0] === "wait") {
      waits++;
      return { code: waits > 50 ? 130 : 75, stdout: "", stderr: "" };
    }
    return args[0] === "run"
      ? { code: 0, stdout: JSON.stringify({ session_id: "s1" }), stderr: "" }
      : { code: 0, stdout: "{}", stderr: "" };
  });
  let t = 0;
  createHook(pi, { ...makeDeps(), waitMaxMs: 5000, now: () => (t += 1000), sleep: async () => {} });
  const ctx = new FakeCtx();
  await pi.commands.get("plan-forge")!.handler("", ctx);
  assert.ok(waits < 10, "loop stopped by the cap");
  assert.ok(ctx.notifications.some((n) => n.message.includes("6 hour limit")));
  assert.equal(ctx.newSessionCalls.length, 0);
});

test("sessionIdOf: raw ids . and .. fall back to session", async () => {
  for (const raw of [".", ".."]) {
    const pi = new FakePi((args) =>
      args[0] === "board" && args[1] === "pull"
        ? { code: 0, stdout: "", stderr: "" }
        : { code: 0, stdout: "{}", stderr: "" },
    );
    createHook(pi, makeDeps());
    const ctx = new FakeCtx();
    Object.assign(ctx.sessionManager, { getSessionId: () => raw });
    await pi.commands.get("plan-forge-board")!.handler("", ctx);
    ctx.intervals[0].fn();
    await flush();
    const pull = pi.execCalls.find((call) => call[1] === "pull");
    assert.ok(pull);
    assert.equal(pull[pull.indexOf("--consumer") + 1], "omp:session");
  }
});

// ---- board activity capture ----

function activityCalls(pi: FakePi): string[][] {
  return pi.execCalls.filter((args) => args[0] === "board" && args[1] === "activity");
}

test("tool_result: edit/write/ast_edit trigger a fire-and-forget board activity exec", async () => {
  const pi = new FakePi(() => ({ code: 0, stdout: "", stderr: "" }));
  createHook(pi, makeDeps());
  const ctx = new FakeCtx();
  const handler = (pi.handlers.get("tool_result") ?? [])[0];

  await handler({ toolName: "edit", input: { path: "internal/a.go" }, content: [], isError: false }, ctx);
  await handler({ toolName: "write", input: { path: "/repo/web/b.ts" }, content: [], isError: false }, ctx);
  await handler({ toolName: "ast_edit", input: { file_path: "c.py" }, content: [], isError: false }, ctx);
  await handler({ toolName: "custom_patch", input: { path: "d.md" }, content: [], isError: false }, ctx);

  assert.deepEqual(activityCalls(pi), [
    ["board", "activity", "--repo", "/repo", "--consumer", "omp:abc123", "--path", "internal/a.go"],
    ["board", "activity", "--repo", "/repo", "--consumer", "omp:abc123", "--path", "web/b.ts"],
    ["board", "activity", "--repo", "/repo", "--consumer", "omp:abc123", "--path", "c.py"],
    ["board", "activity", "--repo", "/repo", "--consumer", "omp:abc123", "--path", "d.md"],
  ]);
  assert.equal(pi.execOptions[0]?.timeout, 500, "bounded by execTimeoutMs");
  assert.equal(ctx.editorTexts.length, 0);
  assert.equal(ctx.notifications.length, 0);
  assert.equal(pi.messages.length, 0, "capture sends no message");
});

test("tool_result: read-like tools, errors and path-less inputs do not trigger activity", async () => {
  const pi = new FakePi(() => ({ code: 0, stdout: "", stderr: "" }));
  createHook(pi, makeDeps());
  const ctx = new FakeCtx();
  const handler = (pi.handlers.get("tool_result") ?? [])[0];

  for (const toolName of ["read", "grep", "glob", "find", "bash"]) {
    await handler({ toolName, input: { path: "x.go", command: "ls" }, content: [], isError: false }, ctx);
  }
  await handler({ toolName: "edit", input: { path: "x.go" }, content: [], isError: true }, ctx);
  await handler({ toolName: "edit", input: {}, content: [], isError: false }, ctx);
  await handler({ toolName: "edit", input: { path: 7 }, content: [], isError: false }, ctx);
  assert.deepEqual(activityCalls(pi), []);
});

test("tool_result: the same path is reported once per 10 s window", async () => {
  const pi = new FakePi(() => ({ code: 0, stdout: "", stderr: "" }));
  let nowMs = 1_000_000;
  createHook(pi, { ...makeDeps(), now: () => nowMs });
  const ctx = new FakeCtx();
  const handler = (pi.handlers.get("tool_result") ?? [])[0];
  const edit = (path: string) => handler({ toolName: "edit", input: { path }, content: [], isError: false }, ctx);

  await edit("a.go");
  nowMs += 9_999;
  await edit("a.go");
  await edit("b.go");
  assert.equal(activityCalls(pi).length, 2, "a.go deduped within 10 s, b.go distinct");
  nowMs += 1;
  await edit("a.go");
  assert.equal(activityCalls(pi).length, 3, "a.go reported again after 10 s");
});

test("tool_result: an activity exec failure is swallowed", async () => {
  const throwing = new FakePi(() => {
    throw new Error("spawn failed");
  });
  createHook(throwing, makeDeps());
  const rejecting = new FakePi(() => Promise.reject(new Error("exec rejected")));
  createHook(rejecting, makeDeps());
  const ctx = new FakeCtx();
  for (const pi of [throwing, rejecting]) {
    const handler = (pi.handlers.get("tool_result") ?? [])[0];
    await handler({ toolName: "write", input: { path: "a.go" }, content: [], isError: false }, ctx);
    await flush();
    assert.equal(activityCalls(pi).length, 1);
  }
  assert.equal(ctx.notifications.length, 0);
  assert.equal(ctx.editorTexts.length, 0);
});

test("tool_result: activity exec is not awaited by the handler", async () => {
  const gate = deferred<ExecResult>();
  const pi = new FakePi(() => gate.promise);
  createHook(pi, makeDeps());
  const ctx = new FakeCtx();
  const handler = (pi.handlers.get("tool_result") ?? [])[0];
  await handler({ toolName: "edit", input: { path: "a.go" }, content: [], isError: false }, ctx);
  assert.equal(activityCalls(pi).length, 1);
  gate.resolve({ code: 0, stdout: "", stderr: "" });
  await flush();
});

test("handoff prompt carries the board report/discover/Coordinator instructions", async () => {
  const pi = new FakePi(() => ({ code: 0, stdout: "", stderr: "" }));
  createHook(pi, makeDeps());
  const ctx = new FakeCtx();
  const handler = (pi.handlers.get("tool_result") ?? [])[0];
  await handler(
    {
      toolName: "bash",
      input: { command: "plan-forge wait --session s1" },
      content: [{ type: "text", text: APPROVED_WAIT }],
      isError: false,
    },
    ctx,
  );
  assert.equal(ctx.editorTexts.length, 1);
  const prompt = ctx.editorTexts[0];
  assert.match(prompt, /board report --repo <root> --consumer omp:abc123 --task <ID> --state started/);
  assert.match(prompt, /--state done/);
  assert.match(prompt, /board discover --title/);
  assert.match(prompt, /`Coordinator:` line/);
  assert.equal(activityCalls(pi).length, 0, "a bash wait is not an edit");
});

test("session_start: the command center daemon is ensured, and never opens a tab", async () => {
  const pi = new FakePi((args) =>
    args[0] === "center"
      ? { code: 0, stdout: "", stderr: "" }
      : { code: 0, stdout: "banner", stderr: "" },
  );
  createHook(pi, makeDeps());
  await pi.handlers.get("session_start")![0]({}, new FakeCtx());

  const ensure = pi.execCalls.find((a) => a[0] === "center");
  assert.deepEqual(ensure, ["center", "ensure", "--repo", "/repo"]);
  assert.ok(!ensure!.includes("--open"), "starting a session must not open a dashboard tab");
  // The banner still arrives: the launch runs beside it, not before it.
  assert.equal(pi.messages.length, 1);
});

test("session_start: a failing command center never blocks the banner", async () => {
  const pi = new FakePi((args) => {
    if (args[0] === "center") return Promise.reject(new Error("no daemon"));
    return { code: 0, stdout: "banner", stderr: "" };
  });
  createHook(pi, makeDeps());
  // An unhandled rejection here would fail the run, so the assertion is that
  // this resolves at all.
  await pi.handlers.get("session_start")![0]({}, new FakeCtx());
  await new Promise((r) => setImmediate(r));
  assert.equal(pi.messages.length, 1);
});

test("session_start: the command center launch is not awaited", async () => {
  // A launch that never settles must not hold the session start open.
  const pi = new FakePi((args) => {
    if (args[0] === "center") return new Promise<never>(() => {});
    return { code: 0, stdout: "banner", stderr: "" };
  });
  createHook(pi, makeDeps());
  await pi.handlers.get("session_start")![0]({}, new FakeCtx());
  assert.equal(pi.messages.length, 1, "the banner was blocked by the launch");
});

test("tool_result: an edit reports to both the board and the command center", async () => {
  const pi = new FakePi(() => ({ code: 0, stdout: "", stderr: "" }));
  createHook(pi, makeDeps());
  await pi.handlers.get("tool_result")![0](
    { toolName: "edit", input: { file_path: "/repo/src/a.ts" } },
    new FakeCtx(),
  );
  const board = pi.execCalls.find((a) => a[0] === "board" && a[1] === "activity");
  const center = pi.execCalls.find((a) => a[0] === "center" && a[1] === "report");
  assert.ok(board, "the board capture was lost");
  assert.ok(center, "the command center was not told");
  const session = center[center.indexOf("--session") + 1];
  assert.ok(session.startsWith("omp:"), `session = ${session}`);
  assert.ok(center.includes("src/a.ts"), `detail missing: ${center.join(" ")}`);
  // The board and the command center agree on who is reporting.
  assert.equal(session, board[board.indexOf("--consumer") + 1]);
});

test("tool_result: a bash command reaches the command center, but a wait poll does not", async () => {
  const pi = new FakePi(() => ({ code: 0, stdout: "", stderr: "" }));
  createHook(pi, makeDeps());
  await pi.handlers.get("tool_result")![0](
    { toolName: "bash", input: { command: "go test ./..." } },
    new FakeCtx(),
  );
  const reported = pi.execCalls.filter((a) => a[0] === "center" && a[1] === "report");
  assert.equal(reported.length, 1);
  assert.ok(reported[0].includes("go test ./..."));

  // A `plan-forge wait` poll is the forge talking to itself.
  const quiet = new FakePi(() => ({ code: 0, stdout: "", stderr: "" }));
  createHook(quiet, makeDeps());
  await quiet.handlers.get("tool_result")![0](
    { toolName: "bash", input: { command: '"/bin/plan-forge" wait --session s1 --timeout 50s' } },
    new FakeCtx(),
  );
  assert.equal(quiet.execCalls.filter((a) => a[0] === "center" && a[1] === "report").length, 0);
});

// --- the oh-my-pi swarm protocol ------------------------------------------

const swarmSnapshot = {
  word: "WORKING",
  waitingOnUser: false,
  taskId: "T-12",
  stage: "limit retries",
  dispatch: "∥3 ready",
  activeAgents: 2,
  epic: "ship the board",
  currentTask: "wire the panel",
  parallelSubtasks: [
    { id: "worker-ada", agent: "coder", employee: "Ada", task: "wire the panel" },
    { id: "worker-bo", agent: "reviewer", employee: "Bo", task: "read the diff" },
  ],
};

async function startedHook(): Promise<FakePi> {
  const pi = new FakePi(() => ({ code: 0, stdout: "", stderr: "" }));
  createHook(pi, makeDeps());
  await pi.handlers.get("session_start")![0]({}, new FakeCtx());
  return pi;
}

function swarmReports(pi: FakePi): string[][] {
  return pi.execCalls.filter((a) => a[0] === "swarm" && a[1] === "report");
}

test("swarm: an assistant:status snapshot is forwarded verbatim", async () => {
  const pi = await startedHook();
  pi.publish("assistant:status", swarmSnapshot);
  await delay(40);

  const reports = swarmReports(pi);
  assert.equal(reports.length, 1);
  const args = reports[0];
  assert.deepEqual(args.slice(0, 8), [
    "swarm", "report", "--repo", "/repo", "--session", "omp:abc123", "--harness", "omp",
  ]);
  assert.equal(args[8], "--json");
  // Forwarded as the protocol wrote it: Plan-Forge consumes this payload, it
  // does not own it, so no field is renamed on the way through.
  assert.deepEqual(JSON.parse(args[9]), swarmSnapshot);
});

test("swarm: a burst coalesces into one report carrying the newest snapshot", async () => {
  const pi = await startedHook();
  pi.publish("assistant:status", { ...swarmSnapshot, activeAgents: 1 });
  pi.publish("assistant:status", { ...swarmSnapshot, activeAgents: 2 });
  pi.publish("assistant:status", { ...swarmSnapshot, activeAgents: 3 });
  await delay(40);

  const reports = swarmReports(pi);
  assert.equal(reports.length, 1, "a dispatch burst must not become a burst of subprocesses");
  assert.equal(JSON.parse(reports[0][9]).activeAgents, 3, "a panel must show what is running now");
});

test("swarm: an unusable payload is dropped rather than forwarded", async () => {
  const pi = await startedHook();
  for (const bad of [null, "a string", ["an", "array"], 42]) {
    pi.publish("assistant:status", bad);
  }
  await delay(40);
  assert.equal(swarmReports(pi).length, 0);
});

test("swarm: nothing is reported after the session shuts down", async () => {
  const pi = await startedHook();
  pi.publish("assistant:status", swarmSnapshot);
  await pi.handlers.get("session_shutdown")![0]({}, new FakeCtx());
  await delay(40);
  assert.equal(swarmReports(pi).length, 0, "a queued snapshot must not outlive its session");
});

test("swarm: an OMP without the event bus simply has no swarm panel", async () => {
  const pi = new FakePi(() => ({ code: 0, stdout: "", stderr: "" }));
  (pi as { events?: unknown }).events = undefined;
  assert.doesNotThrow(() => createHook(pi, makeDeps()));
  await pi.handlers.get("session_start")![0]({}, new FakeCtx());
  assert.equal(swarmReports(pi).length, 0);
});
