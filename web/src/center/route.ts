/**
 * The command center's router.
 *
 * It is deliberately a few lines rather than a dependency. The server serves
 * index.html for any extensionless path, so a deep link already works; all the
 * client needs is to decide which page a path means and to keep the address
 * bar honest when the user navigates. The forge's own router cannot be reused
 * because it is phase-locked by design: it blocks jumps to a route the session
 * has not reached, and the command center has no phase to lock to.
 */

export const CENTER_ROUTES = ["/live", "/sprint", "/setup"] as const;

export type CenterRoute = (typeof CENTER_ROUTES)[number];

export const DEFAULT_ROUTE: CenterRoute = "/live";

/** The label shown in the navigation for each route. */
export const ROUTE_LABEL: Record<CenterRoute, string> = {
  "/live": "Live",
  "/sprint": "Sprint",
  "/setup": "Setup",
};

/**
 * Resolve a pathname to a route. Anything unrecognised falls back to the
 * default rather than erroring: a stale bookmark should land somewhere useful.
 */
export function routeFromPath(pathname: string): CenterRoute {
  const trimmed = pathname.replace(/\/+$/, "") || "/";
  const match = CENTER_ROUTES.find((r) => r === trimmed);
  return match ?? DEFAULT_ROUTE;
}

/** True when this pathname belongs to the command center. */
export function isCenterPath(pathname: string): boolean {
  const trimmed = pathname.replace(/\/+$/, "") || "/";
  return CENTER_ROUTES.some((r) => r === trimmed);
}
