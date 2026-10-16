import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { fileURLToPath } from "node:url";
import fs from "node:fs";

const repoRoot = fileURLToPath(new URL("..", import.meta.url));

// Resolve the embedded JSON Schemas shared with the Go implementation.
// Vite reads these files directly out of internal/contracts/schema/, so the
// frontend and Go validate against the exact same bytes; nothing is copied or
// re-declared on the TypeScript side.
const schemaDir = fileURLToPath(new URL("../internal/contracts/schema/", import.meta.url));
const schemaFiles = fs.readdirSync(schemaDir).filter((f) => f.endsWith(".json"));

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: {
      "@pf": fileURLToPath(new URL("./src/", import.meta.url)),
      // Every schema JSON is addressable as a virtual module, e.g.
      // `@schema/envelope.v1.json`.
      ...Object.fromEntries(
        schemaFiles.map((f) => [
          `@schema/${f}`,
          `${schemaDir}${f}`,
        ]),
      ),
    },
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
  },
  test: {
    // Playwright specs under e2e/ run through `npm run test:e2e`, never vitest.
    include: ["src/**/*.test.{ts,tsx}"],
    exclude: ["e2e/**", "node_modules/**", "dist/**"],
  },
  server: {
    fs: {
      // tests/fixtures and internal/contracts live outside web/; the dev
      // server and vitest both need to serve them.
      allow: [repoRoot],
    },
  },
});
