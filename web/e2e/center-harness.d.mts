/** Types for `center-harness.mjs`; the harness itself runs under plain Node. */

/** One telemetry record, matching `center.IngestRequest`. */
export interface CenterRecord {
  repo: string;
  session_id: string;
  harness: string;
  developer?: string;
  branch?: string;
  mode?: "PLANNED" | "UNPLANNED";
  task_id?: string;
  tool?: string;
  command?: string;
  dirty_files?: string[];
  kind: string;
  detail?: string;
}

export interface CenterHarness {
  /** Origin the daemon bound, `http://127.0.0.1:<port>`. */
  origin: string;
  /** Mint a single-use launch ticket and return the URL a tab should open. */
  launchURL(route?: string): Promise<string>;
  /** Post one telemetry record the way a local hook does. */
  ingest(record: CenterRecord): Promise<void>;
  close(): Promise<void>;
}

export declare function startCenterHarness(): Promise<CenterHarness>;
