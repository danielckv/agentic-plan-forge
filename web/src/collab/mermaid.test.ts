// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import { readdirSync, existsSync, readFileSync } from "node:fs";
import { join, resolve } from "node:path";
import mermaid from "mermaid";

import { mermaidSourceRejected } from "../protocol/schema";

/**
 * `mermaid.parse` over the compiler-generated fixtures in
 * `tests/fixtures/mermaid/*.mmd` (written by Step 3b). When the directory is
 * absent the suite skips with a logged notice; once the fixtures land, every
 * file must pass both the shared statement gate and `mermaid.parse`.
 */

const fixtureDir = resolve(import.meta.dirname, "../../../tests/fixtures/mermaid");

// Initialize once from constants; never pass user/LLM data to initialize.
mermaid.initialize({
  startOnLoad: false,
  securityLevel: "strict",
  suppressErrorRendering: true,
  htmlLabels: false,
  maxTextSize: 50000,
  maxEdges: 500,
});

describe("mermaid.parse over tests/fixtures/mermaid/*.mmd", () => {
  it("parses every fixture (or skips with a notice when absent)", async () => {
    if (!existsSync(fixtureDir)) {
      console.log("[mermaid] tests/fixtures/mermaid/ absent; skipping fixture parse");
      expect(true).toBe(true);
      return;
    }
    const files = readdirSync(fixtureDir).filter((f) => f.endsWith(".mmd"));
    expect(files.length).toBeGreaterThanOrEqual(1);
    for (const file of files) {
      const src = readFileSync(join(fixtureDir, file), "utf8");
      expect(mermaidSourceRejected(src), `${file}: shared gate`).toBeNull();
      // No suppressErrors: an invalid fixture must throw and fail this test.
      await expect(mermaid.parse(src), `${file}: mermaid.parse`).resolves.toBeTruthy();
    }
  });

  it("rejects denied statements the compiler must never emit", async () => {
    const denied = [
      'flowchart TD\nA-->B\nclick A href "https://evil.example"',
      "sequenceDiagram\nA->>B: x\nlink A: y @ https://evil.example",
      "flowchart TD\nstyle A fill:#f00",
    ];
    for (const src of denied) {
      expect(mermaidSourceRejected(src)).not.toBeNull();
    }
  });
});
