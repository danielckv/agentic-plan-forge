/** Types for `bridge-harness.mjs`; the harness itself runs under plain Node. */

export interface BridgeHarness {
  /** Origin the window loads, `http://127.0.0.1:<port>`. */
  origin: string;
  /** Content-Security-Policy sent with every response. */
  csp: string;
  /** Text frames the UI sent after authenticating, in arrival order. */
  received: string[];
  /** Mint a fresh single-use launch nonce and reset the session state. */
  mintNonce(): string;
  close(): Promise<void>;
}

export declare function startBridgeHarness(): Promise<BridgeHarness>;
