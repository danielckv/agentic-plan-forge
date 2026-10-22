import { ChangeSet, Text, type ChangeDesc } from "@codemirror/state";

import { sendableUpdates } from "./model";

/**
 * DocClient keeps one member's view of the shared `prd` document in step with
 * the Go OT authority.
 *
 * The authority accepts a push only at its current version, acknowledges it to
 * the sender with `TEXT_ACK`, and sends everyone else `TEXT_UPDATES`. So the
 * client keeps three layers:
 *
 * - `confirmed`: the text at `version`, exactly as the authority holds it;
 * - `inflight`: the one change sent and not yet acknowledged;
 * - `pending`: local edits made since, composed into one change.
 *
 * Local text is always confirmed + inflight + pending. A remote change is
 * applied to `confirmed` and rebased over the local layers, so neither side's
 * typing is lost. Every method is synchronous and pure apart from the `send`
 * and `onRemote` callbacks, so the protocol is unit-testable without a socket.
 */

/** One `TEXT_DELTA` payload, ready to send. */
export type DeltaPayload = ReturnType<typeof sendableUpdates>;

/** What the editor must do to stay equal to `localText`. */
export type RemoteEffect = { kind: "changes"; changes: ChangeSet } | { kind: "reset"; text: string };

export interface DocClientOptions {
  /** Sends one TEXT_DELTA payload. */
  send: (payload: DeltaPayload) => void;
  /** Tells the editor about text that changed under it. */
  onRemote: (effect: RemoteEffect) => void;
  /** Returns a fresh client ID; it must start with "<memberID>:". */
  clientId: () => string;
}

interface Inflight {
  clientId: string;
  changes: ChangeSet;
  /** The confirmed version the change was sent against. */
  base: number;
}

function toText(s: string): Text {
  return Text.of(s.split("\n"));
}

/**
 * diffChange returns the single replacement that turns `from` into `to`: the
 * common prefix and suffix stay, the middle is replaced.
 */
export function diffChange(from: string, to: string): ChangeSet {
  let start = 0;
  const max = Math.min(from.length, to.length);
  while (start < max && from.charCodeAt(start) === to.charCodeAt(start)) start++;
  let endFrom = from.length;
  let endTo = to.length;
  while (endFrom > start && endTo > start && from.charCodeAt(endFrom - 1) === to.charCodeAt(endTo - 1)) {
    endFrom--;
    endTo--;
  }
  return ChangeSet.of({ from: start, to: endFrom, insert: to.slice(start, endTo) }, from.length);
}

export class DocClient {
  private confirmed: Text = Text.empty;
  private version = 0;
  private inflight: Inflight | null = null;
  private pending: ChangeSet | null = null;
  private readonly opts: DocClientOptions;

  constructor(opts: DocClientOptions) {
    this.opts = opts;
  }

  /** The text the editor shows. */
  get localText(): string {
    let doc = this.confirmed;
    if (this.inflight !== null) doc = this.inflight.changes.apply(doc);
    if (this.pending !== null) doc = this.pending.apply(doc);
    return doc.toString();
  }

  /** The authority version `confirmed` is at. */
  get confirmedVersion(): number {
    return this.version;
  }

  /** True while local edits wait for the authority. */
  get hasUnconfirmed(): boolean {
    return this.inflight !== null || this.pending !== null;
  }

  /**
   * Adopt the authority's text, as a snapshot delivers it. Local edits not yet
   * confirmed are kept: they are re-expressed as one change against the new
   * text and sent again.
   */
  reset(text: string, version: number): void {
    const local = this.localText;
    const hadLocal = this.hasUnconfirmed;
    this.confirmed = toText(text);
    this.version = version;
    this.inflight = null;
    this.pending = null;
    if (hadLocal && local !== text) {
      this.pending = diffChange(text, local);
      this.flush();
      return;
    }
    this.opts.onRemote({ kind: "reset", text });
  }

  /** Record a local edit, expressed against the current local text. */
  edit(changes: ChangeSet): void {
    if (changes.empty) return;
    this.pending = this.pending === null ? changes : this.pending.compose(changes);
    this.flush();
  }

  /** The authority accepted the in-flight change. */
  ack(version: number): void {
    if (this.inflight === null) return;
    this.confirmed = this.inflight.changes.apply(this.confirmed);
    this.inflight = null;
    this.version = version;
    this.flush();
  }

  /**
   * Changes other members (or an upload) made, starting at `fromVersion`.
   * An update carrying the in-flight client ID is this client's own change
   * coming back, and counts as the acknowledgement. Updates this client
   * already holds (a stale rejection delivered them first) are skipped.
   * Returns false when the updates start past the confirmed version: the
   * caller must resync.
   */
  receive(fromVersion: number, updates: Array<{ client_id?: string; changes: ChangeDesc }>): boolean {
    if (fromVersion > this.version) return false;
    for (const u of updates.slice(this.version - fromVersion)) {
      if (this.inflight !== null && u.client_id === this.inflight.clientId) {
        this.ack(this.version + 1);
        continue;
      }
      this.applyRemote(ChangeSet.fromJSON(u.changes));
    }
    return true;
  }

  /**
   * The authority refused the in-flight change because its base was stale.
   * The updates it sends start at that base; those this client has not
   * received yet are applied as remote changes, and the local change is
   * rebased over them and sent again at the new version.
   */
  rejectStale(updates: Array<{ client_id?: string; changes: ChangeDesc }>): void {
    if (this.inflight === null) return;
    const mine = this.inflight;
    this.inflight = null;
    this.pending = this.pending === null ? mine.changes : mine.changes.compose(this.pending);
    for (const u of updates.slice(this.version - mine.base)) this.applyRemote(ChangeSet.fromJSON(u.changes));
    this.flush();
  }

  /**
   * The authority refused the in-flight change for good (the document is
   * frozen, or the change is invalid or too large). Unconfirmed edits are
   * dropped and the editor returns to the confirmed text.
   */
  discardUnconfirmed(): void {
    this.inflight = null;
    this.pending = null;
    this.opts.onRemote({ kind: "reset", text: this.confirmed.toString() });
  }

  /** Send the pending edits when nothing is in flight. */
  flush(): void {
    if (this.inflight !== null || this.pending === null) return;
    const clientId = this.opts.clientId();
    this.inflight = { clientId, changes: this.pending, base: this.version };
    this.pending = null;
    this.opts.send(sendableUpdates([{ clientID: clientId, changes: this.inflight.changes }], this.version));
  }

  /** Apply one remote change to confirmed and rebase the local layers over it. */
  private applyRemote(remote: ChangeSet): void {
    this.confirmed = remote.apply(this.confirmed);
    this.version += 1;
    let r = remote;
    if (this.inflight !== null) {
      const local = this.inflight.changes;
      this.inflight = { ...this.inflight, changes: local.map(r, true) };
      r = r.map(local);
    }
    if (this.pending !== null) {
      const local = this.pending;
      this.pending = local.map(r, true);
      r = r.map(local);
    }
    this.opts.onRemote({ kind: "changes", changes: r });
  }
}
