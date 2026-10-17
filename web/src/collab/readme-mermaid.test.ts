// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import mermaid from "mermaid";

/**
 * Every ```mermaid block in README.md must parse. GitHub renders these blocks
 * directly, so a syntax error would publish a broken diagram. `mermaid.parse`
 * runs WITHOUT `suppressErrors`, so an invalid diagram throws and fails here.
 */

const readmePath = resolve(import.meta.dirname, "../../../README.md");

mermaid.initialize({ startOnLoad: false, securityLevel: "strict", suppressErrorRendering: true });

function mermaidBlocks(markdown: string): string[] {
  const blocks: string[] = [];
  const fence = /```mermaid\n([\s\S]*?)```/g;
  for (let m = fence.exec(markdown); m !== null; m = fence.exec(markdown)) {
    blocks.push(m[1] ?? "");
  }
  return blocks;
}

describe("README.md mermaid blocks", () => {
  const blocks = mermaidBlocks(readFileSync(readmePath, "utf8"));

  it("contains the How it works diagram", () => {
    expect(blocks.length).toBeGreaterThanOrEqual(1);
  });

  it.each(blocks.map((src, i) => [i + 1, src] as const))("block %i parses", async (_i, src) => {
    const result = await mermaid.parse(src);
    expect(result).toBeTruthy();
  });

  it("rejects a malformed diagram (the test can fail)", async () => {
    await expect(mermaid.parse("flowchart TD\n  A -->")).rejects.toBeDefined();
  });
});
