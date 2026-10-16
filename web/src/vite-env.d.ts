/// <reference types="vite/client" />

/**
 * The local bridge injects the loopback WebSocket URL for this session before
 * the bundle loads; never a token (tokens live only in sessionStorage).
 */
declare global {
  // eslint-disable-next-line no-var
  var PLAN_FORGE_WS_URL: string | undefined;
}

export {};
