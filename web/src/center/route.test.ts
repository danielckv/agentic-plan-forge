import { describe, expect, it } from "vitest";

import { CENTER_ROUTES, DEFAULT_ROUTE, isCenterPath, routeFromPath } from "./route";

describe("routeFromPath", () => {
  it("resolves every known route", () => {
    for (const route of CENTER_ROUTES) {
      expect(routeFromPath(route)).toBe(route);
    }
  });

  it("ignores a trailing slash", () => {
    expect(routeFromPath("/live/")).toBe("/live");
    expect(routeFromPath("/sprint//")).toBe("/sprint");
  });

  it("falls back to the default rather than throwing", () => {
    for (const path of ["/", "", "/nope", "/live/extra", "/LIVE"]) {
      expect(routeFromPath(path)).toBe(DEFAULT_ROUTE);
    }
  });
});

describe("isCenterPath", () => {
  it("claims only the command center's own paths", () => {
    expect(isCenterPath("/live")).toBe(true);
    expect(isCenterPath("/setup/")).toBe(true);
    expect(isCenterPath("/board")).toBe(false);
    expect(isCenterPath("/ingest")).toBe(false);
    expect(isCenterPath("/")).toBe(false);
  });
});
