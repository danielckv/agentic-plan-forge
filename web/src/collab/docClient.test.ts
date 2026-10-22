import { describe, expect, it } from "vitest";
import { ChangeSet, Text } from "@codemirror/state";

import { DocClient, diffChange, type DeltaPayload, type RemoteEffect } from "./docClient";

/**
 * A minimal stand-in for the Go authority (internal/collab): it accepts a push
 * only at its current version and records the applied updates, so the tests
 * exercise the same accept/stale rules the window meets in a real session.
 */
class Authority {
  text = "";
  version = 0;
  log: Array<{ client_id: string; changes: unknown }> = [];

  push(p: DeltaPayload): { ok: true; version: number } | { ok: false; updates: Array<{ client_id: string; changes: unknown }> } {
    const base = Number(p.base_version);
    if (base !== this.version) return { ok: false, updates: this.log.slice(base) };
    for (const u of p.updates) {
      this.text = ChangeSet.fromJSON(u.changes as never).apply(Text.of(this.text.split("\n"))).toString();
      this.log.push(u);
      this.version += 1;
    }
    return { ok: true, version: this.version };
  }

  /** Another member's edit (or an upload) applied directly. */
  remote(from: number, to: number, insert: string): { client_id: string; changes: unknown } {
    const u = { client_id: "other:1", changes: ChangeSet.of({ from, to, insert }, this.text.length).toJSON() };
    this.push({ document_id: "prd", base_version: String(this.version), updates: [u] });
    return u;
  }
}

/** A client wired to an editor model (a plain string kept equal to localText). */
function harness() {
  const sent: DeltaPayload[] = [];
  let editor = "";
  let seq = 0;
  const client = new DocClient({
    send: (p) => sent.push(p),
    clientId: () => `owner:${seq++}`,
    onRemote: (e: RemoteEffect) => {
      editor = e.kind === "reset" ? e.text : e.changes.apply(Text.of(editor.split("\n"))).toString();
    },
  });
  const type = (from: number, to: number, insert: string): void => {
    const cs = ChangeSet.of({ from, to, insert }, editor.length);
    editor = cs.apply(Text.of(editor.split("\n"))).toString();
    client.edit(cs);
  };
  return { client, sent, type, get editor() { return editor; } };
}

describe("DocClient", () => {
  it("keeps one change in flight and sends the rest on acknowledgement", () => {
    const auth = new Authority();
    const h = harness();
    h.client.reset("", 0);
    h.type(0, 0, "Hello");
    h.type(5, 5, " world");
    h.type(11, 11, "!");
    expect(h.sent).toHaveLength(1);
    const first = auth.push(h.sent[0]!);
    expect(first.ok).toBe(true);
    h.client.ack(1);
    expect(h.sent).toHaveLength(2); // the two later edits, composed into one
    auth.push(h.sent[1]!);
    h.client.ack(2);
    expect(auth.text).toBe("Hello world!");
    expect(h.client.localText).toBe("Hello world!");
    expect(h.client.hasUnconfirmed).toBe(false);
  });

  it("rebases local typing over a remote change instead of losing either", () => {
    const auth = new Authority();
    auth.push({ document_id: "prd", base_version: "0", updates: [{ client_id: "x:0", changes: ChangeSet.of({ from: 0, insert: "abc" }, 0).toJSON() }] });
    const h = harness();
    h.client.reset("abc", 1);
    h.type(3, 3, "XYZ"); // in flight: append
    const remote = auth.remote(0, 0, ">> "); // an upload lands first
    expect(h.client.receive(1, [remote as never])).toBe(true);
    expect(h.editor).toBe(">> abcXYZ");
    // The in-flight push is now stale; the authority answers with what it missed.
    const res = auth.push(h.sent[0]!);
    expect(res.ok).toBe(false);
    // The rejection repeats the remote update; it must not be applied twice.
    if (!res.ok) h.client.rejectStale(res.updates as never);
    expect(h.editor).toBe(">> abcXYZ");
    expect(h.sent).toHaveLength(2);
    expect(auth.push(h.sent[1]!).ok).toBe(true);
    h.client.ack(auth.version);
    expect(auth.text).toBe(">> abcXYZ");
    expect(h.client.localText).toBe(auth.text);
  });

  it("applies the updates a stale rejection carries", () => {
    const auth = new Authority();
    const h = harness();
    h.client.reset("", 0);
    const missed = auth.remote(0, 0, "# Brief\n");
    h.type(0, 0, "Note. ");
    const res = auth.push(h.sent[0]!);
    expect(res.ok).toBe(false);
    if (!res.ok) h.client.rejectStale(res.updates as never);
    expect(missed.client_id).toBe("other:1");
    expect(auth.push(h.sent[1]!).ok).toBe(true);
    h.client.ack(auth.version);
    expect(h.client.localText).toBe(auth.text);
    expect(h.editor).toBe(auth.text);
    expect(auth.text).toContain("Note. ");
    expect(auth.text).toContain("# Brief");
  });

  it("treats its own change echoed back as the acknowledgement", () => {
    const auth = new Authority();
    const h = harness();
    h.client.reset("", 0);
    h.type(0, 0, "hi");
    auth.push(h.sent[0]!);
    expect(h.client.receive(0, [auth.log[0] as never])).toBe(true);
    expect(h.client.hasUnconfirmed).toBe(false);
    expect(h.editor).toBe("hi");
  });

  it("asks for a resync when updates skip a version, and skips ones it holds", () => {
    const auth = new Authority();
    const h = harness();
    h.client.reset("", 0);
    expect(h.client.receive(3, [])).toBe(false);
    const u = auth.remote(0, 0, "x");
    expect(h.client.receive(0, [u as never])).toBe(true);
    expect(h.client.receive(0, [u as never])).toBe(true); // a repeat changes nothing
    expect(h.editor).toBe("x");
  });

  it("keeps unconfirmed typing across a snapshot", () => {
    const h = harness();
    h.client.reset("base", 0);
    h.type(4, 4, " + mine");
    h.client.reset("base", 0); // a reconnect snapshot: the push never landed
    expect(h.sent).toHaveLength(2);
    expect(h.client.localText).toBe("base + mine");
  });

  it("diffChange replaces only the middle", () => {
    const cs = diffChange("the quick fox", "the slow fox");
    expect(cs.apply(Text.of(["the quick fox"])).toString()).toBe("the slow fox");
    const ranges: Array<[number, number]> = [];
    cs.iterChangedRanges((fromA, toA) => ranges.push([fromA, toA]));
    expect(ranges).toEqual([[4, 9]]);
  });
});
