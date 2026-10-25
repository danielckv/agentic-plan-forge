import { describe, expect, it } from "vitest";
import { readdirSync, readFileSync, existsSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { join } from "node:path";

import {
  validate,
  mermaidStatementDenied,
  mermaidSourceRejected,
  mermaidDenylistTokens,
} from "./schema";
import { decodeFrame } from "./wire";

/**
 * The schema conformance corpus is the committed `tests/fixtures/schema/`
 * directory: the same fixtures the Go side validates against. The frontend
 * must reach them through the repo root so no copy exists here.
 */

const fixturesRoot = fileURLToPath(new URL("../../../tests/fixtures/", import.meta.url));
const schemaFixtureDir = join(fixturesRoot, "schema");

type SchemaId =
  | "envelope.v1"
  | "normalized_prd.v1"
  | "impact_map.v1"
  | "synthesis.v1"
  | "research_finding.v1";

const SCHEMA_IDS: readonly SchemaId[] = [
  "envelope.v1",
  "normalized_prd.v1",
  "impact_map.v1",
  "synthesis.v1",
  "research_finding.v1",
];

/** File name convention: `<schemaId>__<case>.json` — narrows to a schema id. */
function schemaIdFor(file: string): SchemaId {
  const id = file.split("__")[0] as SchemaId;
  if (!SCHEMA_IDS.includes(id)) {
    throw new Error(`fixture ${file} does not name a schema id`);
  }
  return id;
}

function readJSON(path: string): unknown {
  return JSON.parse(readFileSync(path, "utf8")) as unknown;
}

describe("shared JSON Schema fixtures (tests/fixtures/schema)", () => {
  it("loads the fixture directory", () => {
    expect(existsSync(schemaFixtureDir)).toBe(true);
  });

  it("accepts every valid fixture", () => {
    const dir = join(schemaFixtureDir, "valid");
    for (const file of readdirSync(dir)) {
      if (!file.endsWith(".json")) continue;
      const result = validate(schemaIdFor(file), readJSON(join(dir, file)));
      expect(result.ok, `${file}: ${JSON.stringify(result.errors)}`).toBe(true);
    }
  });

  it("rejects every invalid fixture (unknown keys, numeric counters, unknown event types)", () => {
    const dir = join(schemaFixtureDir, "invalid");
    for (const file of readdirSync(dir)) {
      if (!file.endsWith(".json")) continue;
      expect(validate(schemaIdFor(file), readJSON(join(dir, file))).ok, `${file} must be rejected`).toBe(false);
    }
  });

  it("rejects an unknown schema_version in normalized_prd.v1", () => {
    const result = validate("normalized_prd.v1", {
      schema_version: 2,
      product: {
        goals: [], actors: [], flows: [], requirements: [],
        acceptance_criteria: [], constraints: [], open_questions: [],
      },
      ambiguities: [],
    });
    expect(result.ok).toBe(false);
  });
});

describe("wire frame strict decode", () => {
  const validFrame = () => JSON.stringify({
    version: 1, session_id: "s", sender_id: "m", event_id: "e",
    event_type: "PRESENCE", seq: "1", revision: "1", epoch: "1",
    timestamp: "2026-10-17T10:15:00Z",
    payload: {},
  });

  it("rejects duplicate JSON keys", () => {
    const duped = validFrame().replace("{", '{"session_id":"x",');
    expect(decodeFrame(duped).ok).toBe(false);
  });

  it("rejects trailing JSON", () => {
    expect(decodeFrame(`${validFrame()} {}`).ok).toBe(false);
  });

  it("rejects frames beyond the 4 MiB decode cap", () => {
    const frame = JSON.stringify({
      version: 1, session_id: "s", sender_id: "m", event_id: "e",
      event_type: "PRESENCE", seq: "1", revision: "1", epoch: "1",
      timestamp: "2026-10-17T10:15:00Z",
      payload: { note: "x".repeat(4 * 1024 * 1024 + 1) },
    });
    expect(frame.length).toBeGreaterThan(4 * 1024 * 1024);
    expect(decodeFrame(frame).ok).toBe(false);
  });
});

describe("mermaid denylist (shared with Go via mermaid-denylist.json)", () => {
  it("loads the shared denylist tokens", () => {
    expect(mermaidDenylistTokens.includes("click")).toBe(true);
    expect(mermaidDenylistTokens.includes("href")).toBe(true);
  });

  it("denies click/href statements split on newline and semicolon", () => {
    expect(mermaidStatementDenied('Click t1 href "https://evil.example"')).toBe("click");
    expect(mermaidStatementDenied("A->>B: x\nlink A: y @ https://evil.example")).toBe("link");
    expect(mermaidStatementDenied("A->>B: x; call A: y")).toBe("call");
    expect(mermaidStatementDenied("graph TD\nA-->B")).toBeNull();
  });

  it("rejects frontmatter, directives and themeCSS before any render", () => {
    expect(mermaidSourceRejected("---\ntitle: x\n---\ngraph TD")).toBe("frontmatter block");
    expect(mermaidSourceRejected("%%{init: {}}%%\ngraph TD")).toBe("directive (%%)");
    expect(mermaidSourceRejected("graph TD\nthemeCSS: x")).toBe("themeCSS");
    expect(mermaidSourceRejected("graph TD\nA-->B")).toBeNull();
  });
});
