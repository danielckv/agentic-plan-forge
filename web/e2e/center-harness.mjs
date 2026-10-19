/**
 * Drives the real command center daemon for the e2e suite.
 *
 * Unlike `bridge-harness.mjs`, which stands in for the Go bridge, this spawns
 * the actual binary: the command center's whole point is that several tabs
 * share one long-lived server, and a stand-in would prove nothing about the
 * fanout, the ticket handshake or the Origin rules that make a fixed port safe.
 *
 * Set `PLAN_FORGE_BIN` to a client build. The spec skips without it.
 */

import { spawn } from "node:child_process";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import path from "node:path";

const START_TIMEOUT_MS = 20_000;
const POLL_MS = 50;

/** Ask the OS for a free port, then hand it to the daemon. */
function freePort() {
  return new Promise((resolve, reject) => {
    const srv = createServer();
    srv.on("error", reject);
    srv.listen(0, "127.0.0.1", () => {
      const { port } = srv.address();
      srv.close(() => {
        resolve(port);
      });
    });
  });
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

/** Start the daemon and wait until it answers. */
export async function startCenterHarness() {
  const bin = process.env.PLAN_FORGE_BIN;
  if (!bin) throw new Error("PLAN_FORGE_BIN is not set");

  const home = await mkdtemp(path.join(tmpdir(), "plan-forge-center-e2e-"));
  const port = await freePort();
  const origin = `http://127.0.0.1:${String(port)}`;

  const child = spawn(bin, ["center", "serve", "--port", String(port)], {
    env: { ...process.env, PLAN_FORGE_HOME: home },
    stdio: ["ignore", "pipe", "pipe"],
  });
  let stderr = "";
  child.stderr.on("data", (c) => (stderr += String(c)));

  const deadline = Date.now() + START_TIMEOUT_MS;
  let secret = "";
  for (;;) {
    if (Date.now() > deadline) {
      child.kill("SIGKILL");
      throw new Error(`the command center did not start: ${stderr}`);
    }
    try {
      const res = await fetch(`${origin}/api/center/ping`);
      if (res.ok) {
        secret = (await readFile(path.join(home, "center", "daemon.token"), "utf8")).trim();
        break;
      }
    } catch {
      // not listening yet
    }
    await sleep(POLL_MS);
  }

  const daemonHeaders = { Authorization: `Bearer ${secret}`, "Content-Type": "application/json" };

  return {
    origin,
    /** Mint a single-use launch ticket and return the URL a tab should open. */
    async launchURL(route = "/live") {
      const res = await fetch(`${origin}/api/center/ticket`, {
        method: "POST",
        headers: daemonHeaders,
      });
      if (!res.ok) throw new Error(`minting a ticket failed: ${String(res.status)}`);
      const body = await res.json();
      return `${origin}${route}#t=${body.ticket}`;
    },
    /** Post one telemetry record the way a local hook does. */
    async ingest(record) {
      const res = await fetch(`${origin}/api/ingest`, {
        method: "POST",
        headers: daemonHeaders,
        body: JSON.stringify(record),
      });
      if (!res.ok) throw new Error(`ingest failed: ${String(res.status)} ${await res.text()}`);
    },
    async close() {
      child.kill("SIGTERM");
      await new Promise((resolve) => {
        child.on("exit", resolve);
        setTimeout(() => {
          child.kill("SIGKILL");
          resolve();
        }, 3000);
      });
      await rm(home, { recursive: true, force: true });
    },
  };
}
