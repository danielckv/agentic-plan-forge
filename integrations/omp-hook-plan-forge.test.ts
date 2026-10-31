import assert from "node:assert/strict";
import { test } from "node:test";

import { createHook } from "./omp-hook-plan-forge.ts";
import type { HookDeps } from "./omp-hook-plan-forge.ts";

type ExecScript = (args: string[]) => unknown;

interface SessionResult {
  cancelled?: boolean;
}

interface CtxLike {
  cwd: string;
  ui: {
    notify(message: string, kind?: string): void;
    setEditorText(text: string): void;
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
  editorTexts: string[] = [];
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
    setEditorText: (text: string): void => {
      this.editorTexts.push(text);
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
  registeredCommands: string[] = [];
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

  // The hook must not register commands any more: the skill is the entry
  // point, and a skill named plan-forge would shadow a command of that name.
  registerCommand(name: string): void {
    this.registeredCommands.push(name);
  }

  on(event: string, handler: (event: unknown, ctx: CtxLike) => unknown): void {
    const list = this.handlers.get(event) ?? [];
    list.push(handler);
    this.handlers.set(event, list);
  }
}

function makeDeps(): HookDeps {
  return { execTimeoutMs: 500 };
}

const APPROVED_WAIT = JSON.stringify({
  session_id: "s1",
  state: "approved",
  artifact_paths: ["/repo/docs/plans/.plan/synthesized_spec.md"],
  validation: { valid: true, status: "UNSTARTED", verified: 0, total: 9, drift: [] },
});

const QUIET: ExecScript = () => ({ code: 0, stdout: "", stderr: "" });

// approve fires the tool_result the skill produces: a bash result for
// `plan-forge wait` whose output carries an approved plan.
async function approve(pi: FakePi, ctx: CtxLike, text: string = APPROVED_WAIT): Promise<void> {
  const handlers = pi.handlers.get("tool_result") ?? [];
  assert.equal(handlers.length, 1);
  await handlers[0](
    {
      toolName: "bash",
      input: { command: '"/usr/local/bin/plan-forge" wait --session s1 --timeout 50s' },
      content: [{ type: "text", text }],
      isError: false,
    },
    ctx,
  );
}

test("approved wait opens a plan-mode session: mode_change in setup, then switchSession, then the turn", async () => {
  const order: string[] = [];
  const pi = new FakePi(QUIET, { order });
  createHook(pi, makeDeps());
  const ctx = new FakeCtx({ order });

  await approve(pi, ctx);

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
  assert.equal(ctx.editorTexts.length, 0, "the session opened, so nothing is prefilled");
  assert.equal(pi.execCalls.length, 0, "the skill already ran the binary; the hook runs nothing");
});

test("the hook registers no commands: the skill owns /plan-forge", () => {
  const pi = new FakePi(QUIET);
  createHook(pi, makeDeps());
  assert.deepEqual(pi.registeredCommands, []);
  assert.deepEqual([...pi.handlers.keys()].sort(), ["session_start", "tool_result"]);
});

const FALLBACKS: [string, FakeCtxOptions, FakePiOptions][] = [
  ["newSession throws", { newSessionThrows: true }, {}],
  ["newSession is cancelled", { newSessionCancelled: true }, {}],
  ["appendModeChange is missing", { noAppendModeChange: true }, {}],
  ["switchSession is unavailable", { noSwitchSession: true }, {}],
  ["switchSession throws", { switchSessionThrows: true }, {}],
  ["switchSession is cancelled", { switchSessionCancelled: true }, {}],
  ["sendUserMessage is unavailable", {}, { noSendUserMessage: true }],
];

for (const [name, ctxOptions, piOptions] of FALLBACKS) {
  test(`${name} -> the /plan prefill`, async () => {
    const pi = new FakePi(QUIET, piOptions);
    createHook(pi, makeDeps());
    const ctx = new FakeCtx(ctxOptions);

    await approve(pi, ctx);

    assert.equal(ctx.editorTexts.length, 1);
    assert.ok(ctx.editorTexts[0].startsWith("/plan "));
    assert.match(ctx.editorTexts[0], /synthesized_spec\.md/);
    assert.ok(ctx.notifications.some((n) => n.message.includes("press Enter")));
    assert.equal(pi.userMessages.length, 0);
  });
}

test("sendUserMessage failing after it was invoked notifies, never prefills", async () => {
  const pi = new FakePi(QUIET, { sendThrows: true });
  createHook(pi, makeDeps());
  const ctx = new FakeCtx();

  await approve(pi, ctx);

  assert.equal(ctx.editorTexts.length, 0, "the turn may have started: a prefill would duplicate it");
  assert.ok(
    ctx.notifications.some((n) => n.kind === "error" && n.message.includes("may not have started")),
  );
});

test("a notification strips C0/C1 controls and caps the field at 200 chars", async () => {
  const pi = new FakePi(QUIET, { noSendUserMessage: true });
  createHook(pi, makeDeps());
  const ctx = new FakeCtx();

  await approve(
    pi,
    ctx,
    JSON.stringify({
      state: "approved",
      validation: { status: "A\u0007B\u009bC", spec_path: "x".repeat(400) },
    }),
  );

  const prefill = ctx.editorTexts[0];
  assert.ok(!/[\u0000-\u001f\u007f-\u009f]/.test(prefill), "no control characters reach the prompt");
  assert.match(prefill, /status=A B C/);
  assert.ok(!prefill.includes("x".repeat(201)), "the spec path is capped at 200 characters");
});

test("only an approved plan-forge wait hands off", async () => {
  const pi = new FakePi(QUIET);
  createHook(pi, makeDeps());
  const ctx = new FakeCtx();
  const handler = (pi.handlers.get("tool_result") ?? [])[0];

  // A different command, an error result, a non-bash tool, and a wait that
  // ended without an approved plan: all silent.
  await handler(
    { toolName: "bash", input: { command: "ls -la" }, content: [{ type: "text", text: APPROVED_WAIT }], isError: false },
    ctx,
  );
  await handler(
    {
      toolName: "bash",
      input: { command: "plan-forge wait --session s1" },
      content: [{ type: "text", text: APPROVED_WAIT }],
      isError: true,
    },
    ctx,
  );
  await handler({ toolName: "edit", input: { path: "/repo/src/a.ts" }, content: "ok", isError: false }, ctx);
  await handler(
    {
      toolName: "bash",
      input: { command: "plan-forge wait --session s1" },
      content: [{ type: "text", text: JSON.stringify({ state: "cancelled" }) }],
      isError: false,
    },
    ctx,
  );

  assert.equal(ctx.newSessionCalls.length, 0);
  assert.equal(ctx.editorTexts.length, 0);
  assert.equal(pi.userMessages.length, 0);
});

test("the handoff prompt names the spec, the validation and the no-edit rule", async () => {
  const pi = new FakePi(QUIET);
  createHook(pi, makeDeps());
  const ctx = new FakeCtx();

  await approve(pi, ctx);

  const prompt = pi.userMessages[0];
  assert.match(prompt, /synthesized_spec\.md/);
  assert.match(prompt, /valid=true status=UNSTARTED verified=0\/9 drift=0/);
  assert.ok(
    prompt.includes(
      "Do not edit any file. Plan only: read the spec, review the validation, run plan-forge validate if needed, " +
        "verify AST targets, and present the execution checklist for approval.",
    ),
  );
  assert.ok(!/board/i.test(prompt), "the plan board was retired in 0.4.0");
});

test("session_start sends the banner only for non-empty stdout, and runs check-state only", async () => {
  const outputs = ["  plan: 3 tasks  ", "   ", ""];
  let i = 0;
  const pi = new FakePi(() => ({ code: 0, stdout: outputs[i++] ?? "", stderr: "" }));
  createHook(pi, makeDeps());
  const ctx = new FakeCtx();
  const handler = (pi.handlers.get("session_start") ?? [])[0];

  await handler({}, ctx);
  await handler({}, ctx);
  await handler({}, ctx);

  assert.deepEqual(pi.messages, [{ message: "plan: 3 tasks" }]);
  for (const call of pi.execCalls) {
    assert.deepEqual(call, ["hook", "check-state", "--repo", "/repo", "--format", "omp"]);
  }
});

test("pi.exec receives timeout + signal; a hung child is aborted and the race timer is cleared", async () => {
  let captured: AbortSignal | undefined;
  const pi = new FakePi(
    () =>
      new Promise(() => {
        // Never settles: the hook's own bound must fire.
      }),
  );
  createHook(pi, { execTimeoutMs: 20 });
  const ctx = new FakeCtx();

  await (pi.handlers.get("session_start") ?? [])[0]({}, ctx);

  assert.equal(pi.execOptions.length, 1);
  assert.equal(pi.execOptions[0]?.timeout, 20);
  captured = pi.execOptions[0]?.signal;
  assert.ok(captured, "a signal is passed");
  assert.equal(captured.aborted, true, "the hung child is aborted when the bound fires");
  assert.deepEqual(pi.messages, [], "a timed-out check-state sends no banner");
});
