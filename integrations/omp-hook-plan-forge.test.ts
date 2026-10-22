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
  waitForIdle(): Promise<void>;
  newSession(opts: {
    parentSession: unknown;
    setup(sm: { appendModeChange?(mode: string, data?: unknown): void }): Promise<void>;
  }): Promise<SessionResult | undefined>;
  switchSession?(sessionFile: string): Promise<SessionResult | undefined>;
  sessionManager: { getSessionFile(): string };
}

interface FakeCtxOptions {
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
  notifications: { message: string; kind?: string }[] = [];
  statuses: [string, string][] = [];
  editorTexts: string[] = [];
  confirmCalls = 0;
  waitForIdleCalls = 0;
  newSessionCalls: { parentSession: unknown }[] = [];
  modeChanges: { mode: string; data: unknown }[] = [];
  switchCalls: string[] = [];
  currentFile = PARENT_FILE;
  sessionManager = { getSessionFile: (): string => this.currentFile };
  switchSession?: (sessionFile: string) => Promise<SessionResult | undefined>;
  private readonly options: FakeCtxOptions;

  constructor(options: FakeCtxOptions = {}) {
    this.options = options;
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
}

interface FakePiOptions {
  order?: string[];
  sendThrows?: boolean;
  noSendUserMessage?: boolean;
}

class FakePi {
  execCalls: string[][] = [];
  messages: { message: unknown }[] = [];
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

  sendMessage(message: unknown): void {
    this.messages.push({ message });
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
}

function makeDeps(): HookDeps {
  return {
    execTimeoutMs: 500,
    waitTimeout: "1s",
    sleep: async () => {},
  };
}

const APPROVED_WAIT = JSON.stringify({
  session_id: "s1",
  state: "approved",
  artifact_paths: ["/repo/docs/plans/.plan/synthesized_spec.md"],
  validation: { valid: true, status: "UNSTARTED", verified: 0, total: 9, drift: [] },
});

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


// The command center, the plan board and swarm reporting were retired. The hook
// must not start, call or advertise any of them.
test("retired features: no board command, and session_start runs check-state only", async () => {
  const pi = new FakePi(() => ({ code: 0, stdout: "", stderr: "" }));
  createHook(pi, makeDeps());
  assert.deepEqual([...pi.commands.keys()], ["plan-forge"]);
  await pi.handlers.get("session_start")![0]({}, new FakeCtx());
  assert.deepEqual(pi.execCalls, [["hook", "check-state", "--repo", "/repo", "--format", "omp"]]);

  const ctx = new FakeCtx();
  await pi.handlers.get("tool_result")![0](
    { toolName: "edit", input: { path: "/repo/src/a.ts" }, content: "ok", isError: false },
    ctx,
  );
  assert.equal(pi.execCalls.length, 1, "an edit reports nowhere");
});

test("handoff prompt names the spec and validation, and nothing about a board", async () => {
  const pi = new FakePi(() => ({ code: 0, stdout: "", stderr: "" }));
  createHook(pi, makeDeps());
  const ctx = new FakeCtx();
  await pi.handlers.get("tool_result")![0](
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
  assert.match(prompt, /synthesized_spec\.md/);
  assert.match(prompt, /valid=true status=UNSTARTED verified=0\/9 drift=0/);
  assert.doesNotMatch(prompt, /board|Coordinator/);
});
