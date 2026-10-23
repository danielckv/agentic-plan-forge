// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import { readFileSync, readdirSync } from "node:fs";
import { resolve } from "node:path";
import mermaid from "mermaid";

/**
 * Every ```mermaid block in the repository's Markdown must parse. GitHub
 * renders these blocks directly, so a syntax error would publish a broken
 * diagram. `mermaid.parse` runs WITHOUT `suppressErrors`, so an invalid
 * diagram throws and fails here.
 *
 * The whole documentation tree is scanned, not README.md alone: a diagram that
 * moves between pages must not take its test with it.
 */

const repoRoot = resolve(import.meta.dirname, "../../..");
const skipDirs = new Set(["node_modules", "dist", ".git", ".claude", ".plan"]);

mermaid.initialize({ startOnLoad: false, securityLevel: "strict", suppressErrorRendering: true });

function markdownFiles(dir: string): string[] {
  const out: string[] = [];
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    if (entry.name.startsWith(".") && entry.name !== ".github") continue;
    const full = resolve(dir, entry.name);
    if (entry.isDirectory()) {
      if (skipDirs.has(entry.name)) continue;
      out.push(...markdownFiles(full));
      continue;
    }
    if (entry.name.endsWith(".md")) out.push(full);
  }
  return out.sort();
}

function mermaidBlocks(markdown: string): string[] {
  const blocks: string[] = [];
  const fence = /```mermaid\n([\s\S]*?)```/g;
  for (let m = fence.exec(markdown); m !== null; m = fence.exec(markdown)) {
    blocks.push(m[1] ?? "");
  }
  return blocks;
}

describe("Markdown mermaid blocks", () => {
  const found: Array<readonly [string, string]> = [];
  for (const file of markdownFiles(repoRoot)) {
    for (const src of mermaidBlocks(readFileSync(file, "utf8"))) {
      found.push([file.slice(repoRoot.length + 1), src] as const);
    }
  }

  it("the documentation still carries a diagram", () => {
    expect(found.length).toBeGreaterThanOrEqual(1);
  });

  it.each(found)("%s parses", async (_file, src) => {
    const result = await mermaid.parse(src);
    expect(result).toBeTruthy();
  });

  it("rejects a malformed diagram (the test can fail)", async () => {
    await expect(mermaid.parse("flowchart TD\n  A -->")).rejects.toBeDefined();
  });
});
