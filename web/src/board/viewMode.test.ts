// @vitest-environment jsdom
import { afterEach, describe, expect, it } from "vitest";

import {
  readCoordinatorOpen,
  readViewMode,
  VIEW_STORAGE_KEY,
  writeCoordinatorOpen,
  writeViewMode,
} from "./viewMode";

describe("view and panel persistence", () => {
  afterEach(() => window.sessionStorage.clear());

  it("defaults to the board with the coordinator open", () => {
    expect(readViewMode()).toBe("board");
    expect(readCoordinatorOpen()).toBe(true);
  });

  it("remembers the view and the collapsed panel for the session", () => {
    writeViewMode("graph");
    writeCoordinatorOpen(false);
    expect(readViewMode()).toBe("graph");
    expect(readCoordinatorOpen()).toBe(false);
    writeViewMode("board");
    writeCoordinatorOpen(true);
    expect(readViewMode()).toBe("board");
    expect(readCoordinatorOpen()).toBe(true);
  });

  it("ignores junk in storage", () => {
    window.sessionStorage.setItem(VIEW_STORAGE_KEY, "banana");
    expect(readViewMode()).toBe("board");
  });
});
