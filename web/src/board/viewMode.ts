export type BoardViewMode = "board" | "graph";

export const VIEW_STORAGE_KEY = "plan-forge.board.view";
export const COORDINATOR_STORAGE_KEY = "plan-forge.board.coordinator";

function storage(): Storage | null {
  try {
    return typeof window === "undefined" ? null : window.sessionStorage;
  } catch {
    // Storage can throw in locked-down webviews; the board then just forgets.
    return null;
  }
}

export function readViewMode(): BoardViewMode {
  return storage()?.getItem(VIEW_STORAGE_KEY) === "graph" ? "graph" : "board";
}

export function writeViewMode(mode: BoardViewMode): void {
  try {
    storage()?.setItem(VIEW_STORAGE_KEY, mode);
  } catch {
    /* quota or privacy mode: the choice simply is not remembered */
  }
}

/** The coordinator panel defaults to open. */
export function readCoordinatorOpen(): boolean {
  return storage()?.getItem(COORDINATOR_STORAGE_KEY) !== "closed";
}

export function writeCoordinatorOpen(open: boolean): void {
  try {
    storage()?.setItem(COORDINATOR_STORAGE_KEY, open ? "open" : "closed");
  } catch {
    /* not remembered */
  }
}
