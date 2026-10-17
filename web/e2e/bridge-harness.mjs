/**
 * A stand-in for the Go loopback bridge, built to the same contract as
 * `internal/relay/local_ws.go`, so the e2e suite exercises the embedded bundle
 * under the conditions the dedicated window actually runs in:
 *
 * - every response carries the production Content-Security-Policy, read from
 *   `e2e/bridge-csp.txt` and pinned against `relay.BridgeCSP` by
 *   `internal/relay/local_ws_test.go`;
 * - `POST /api/session` redeems a single-use launch nonce for a token;
 * - `GET /ws` negotiates `plan-forge.v1` and reads an auth frame first;
 * - `/api/*` requires `Authorization: Bearer <token>`.
 *
 * The preview harness in `scripts/preview.mjs` deliberately does none of this:
 * it injects a relay URL and plays the authority. Both are needed — that one
 * drives the pages, this one proves the window can reach them at all.
 */

import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { randomBytes } from "node:crypto";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { WebSocketServer } from "ws";

const DIST = fileURLToPath(new URL("../dist/", import.meta.url));
const CSP_FILE = fileURLToPath(new URL("./bridge-csp.txt", import.meta.url));

const CONTENT_TYPES = {
  ".css": "text/css; charset=utf-8",
  ".html": "text/html; charset=utf-8",
  ".js": "text/javascript; charset=utf-8",
  ".json": "application/json; charset=utf-8",
  ".map": "application/json; charset=utf-8",
  ".svg": "image/svg+xml",
  ".woff2": "font/woff2",
};

/** Serve one file out of dist/, refusing any path that escapes it. */
async function readAsset(urlPath) {
  const clean = path.posix.normalize(decodeURIComponent(urlPath));
  const target = path.join(DIST, clean === "/" ? "index.html" : clean.replace(/^\/+/, ""));
  if (!target.startsWith(DIST)) return null;
  try {
    return { body: await readFile(target), ext: path.extname(target) };
  } catch {
    return null;
  }
}

function readBody(req, limit = 1024) {
  return new Promise((resolve, reject) => {
    const chunks = [];
    let size = 0;
    req.on("data", (c) => {
      size += c.length;
      if (size > limit) {
        reject(new Error("request body too large"));
        req.destroy();
        return;
      }
      chunks.push(c);
    });
    req.on("end", () => resolve(Buffer.concat(chunks).toString("utf8")));
    req.on("error", reject);
  });
}

/**
 * Start the harness. Returns the origin, the launch nonce the window would
 * find in its URL fragment, and a close function.
 */
export async function startBridgeHarness() {
  const csp = (await readFile(CSP_FILE, "utf8")).trim();
  let nonce = randomBytes(16).toString("hex");
  let token = null;
  let nonceUsed = false;
  /** Frames the UI sent after authenticating; the specs assert on these. */
  const received = [];

  const server = createServer((req, res) => {
    res.setHeader("Content-Security-Policy", csp);
    res.setHeader("X-Content-Type-Options", "nosniff");
    res.setHeader("Referrer-Policy", "no-referrer");
    res.setHeader("Cross-Origin-Opener-Policy", "same-origin");
    res.setHeader("Cache-Control", "no-store");

    const url = new URL(req.url, "http://127.0.0.1");

    if (req.method === "POST" && url.pathname === "/api/session") {
      void readBody(req).then(
        (raw) => {
          let parsed = {};
          try {
            parsed = JSON.parse(raw);
          } catch {
            res.writeHead(400, { "Content-Type": CONTENT_TYPES[".json"] });
            res.end(JSON.stringify({ error: "nonce required" }));
            return;
          }
          if (parsed.nonce !== nonce || nonceUsed) {
            res.writeHead(403, { "Content-Type": CONTENT_TYPES[".json"] });
            res.end(JSON.stringify({ error: "unknown nonce" }));
            return;
          }
          nonceUsed = true;
          token = randomBytes(32).toString("hex");
          res.writeHead(200, { "Content-Type": CONTENT_TYPES[".json"] });
          res.end(JSON.stringify({ token }));
        },
        () => {
          res.writeHead(400, { "Content-Type": CONTENT_TYPES[".json"] });
          res.end(JSON.stringify({ error: "nonce required" }));
        },
      );
      return;
    }

    if (url.pathname.startsWith("/api/")) {
      const auth = req.headers.authorization ?? "";
      if (token === null || auth !== `Bearer ${token}`) {
        res.writeHead(401, { "Content-Type": CONTENT_TYPES[".json"] });
        res.end(JSON.stringify({ error: "session token required" }));
        return;
      }
      if (url.pathname === "/api/status") {
        res.writeHead(200, { "Content-Type": CONTENT_TYPES[".json"] });
        res.end(
          JSON.stringify({
            ast: "ok",
            astDetail: "12 files parsed",
            docHealth: 0,
            ocr: "available",
            repo: "billing-service",
            repoPath: "/Users/dev/src/billing-service",
          }),
        );
        return;
      }
      res.writeHead(404, { "Content-Type": CONTENT_TYPES[".json"] });
      res.end(JSON.stringify({ error: "not found" }));
      return;
    }

    void readAsset(url.pathname).then(async (asset) => {
      // Client-side routes have no extension and fall back to index.html,
      // exactly as the bridge's handleStatic does.
      const resolved = asset ?? (path.extname(url.pathname) === "" ? await readAsset("/") : null);
      if (resolved === null) {
        res.writeHead(404, { "Content-Type": CONTENT_TYPES[".json"] });
        res.end(JSON.stringify({ error: "not found" }));
        return;
      }
      res.writeHead(200, {
        "Content-Type": CONTENT_TYPES[resolved.ext] ?? "application/octet-stream",
      });
      res.end(resolved.body);
    });
  });

  const wss = new WebSocketServer({
    server,
    path: "/ws",
    handleProtocols: (protocols) => (protocols.has("plan-forge.v1") ? "plan-forge.v1" : false),
  });

  wss.on("connection", (socket) => {
    let authenticated = false;
    socket.on("message", (data) => {
      const text = data.toString("utf8");
      if (!authenticated) {
        let frame = {};
        try {
          frame = JSON.parse(text);
        } catch {
          socket.close(4403, "authentication failed");
          return;
        }
        if (frame.type !== "auth" || token === null || frame.token !== token) {
          socket.close(4403, "authentication failed");
          return;
        }
        authenticated = true;
        // The bridge acknowledges with a control frame before any envelope.
        socket.send(JSON.stringify({ type: "auth.ok" }));
        socket.send(
          JSON.stringify({
            version: 1,
            session_id: "e2e",
            sender_id: "authority",
            event_id: "e2e-snapshot",
            event_type: "STATE_SNAPSHOT",
            seq: "1",
            revision: "1",
            epoch: "1",
            timestamp: new Date().toISOString(),
            payload: {
              phase: "INGESTION",
              doc_version: "1",
              doc_text: "# Export invoices\n",
              role: "owner",
              members: ["owner"],
            },
          }),
        );
        return;
      }
      received.push(text);
    });
  });

  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  const { port } = server.address();

  return {
    origin: `http://127.0.0.1:${port}`,
    csp,
    received,
    // Bridge.LaunchURL() mints a fresh single-use nonce per window; each test
    // opens its own window, so each one asks for its own nonce.
    mintNonce() {
      nonce = randomBytes(16).toString("hex");
      nonceUsed = false;
      token = null;
      received.length = 0;
      return nonce;
    },
    async close() {
      wss.close();
      for (const client of wss.clients) client.terminate();
      await new Promise((resolve) => server.close(resolve));
    },
  };
}
