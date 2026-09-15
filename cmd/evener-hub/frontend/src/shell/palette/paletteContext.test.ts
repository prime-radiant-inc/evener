// @vitest-environment node

import { afterEach, beforeEach, expect, test } from "vitest";
import type { ThreadModel } from "../../protocol/model";
import { resetWorkspaceStoreForTests, workspaceStore } from "../workspace";
import { buildPaletteContext, isSessionBusy } from "./paletteContext";

beforeEach(() => {
  resetWorkspaceStoreForTests();
});
afterEach(() => {
  resetWorkspaceStoreForTests();
});

function focus(type: string, params: unknown): void {
  workspaceStore.setState({ panes: [{ id: "p1", type: type as never, params, slot: "main" }], focusedPaneId: "p1" });
}

test("buildPaletteContext reads the focused session pane's ref and page", () => {
  focus("session", { ref: "local:abc" });
  expect(buildPaletteContext()).toEqual({ sessionRef: "local:abc", onPage: "session" });
});

test.each(["sessionTasks", "sessionActivity", "sessionDetails", "sessionNotes"])(
  "buildPaletteContext derives session scope from focused %s pane",
  (type) => {
    focus(type, { ref: "local:panel" });
    expect(buildPaletteContext()).toEqual({ sessionRef: "local:panel", onPage: "session" });
  },
);

test("buildPaletteContext yields a null sessionRef when the focused pane is not a session", () => {
  focus("spawn", {});
  expect(buildPaletteContext()).toEqual({ sessionRef: null, onPage: "spawn" });
  focus("settings", { section: "theme" });
  expect(buildPaletteContext()).toEqual({ sessionRef: null, onPage: "settings" });
});

test("buildPaletteContext treats a transcript pane as non-session (no interactive context)", () => {
  focus("transcript", { ref: "local:abc" });
  expect(buildPaletteContext()).toEqual({ sessionRef: null, onPage: "other" });
});

test("buildPaletteContext returns a null sessionRef when nothing is focused", () => {
  expect(buildPaletteContext()).toEqual({ sessionRef: null, onPage: "other" });
});

// --- the one model-derived predicate ---

function model(overrides: Partial<ThreadModel>): ThreadModel {
  return {
    status: { type: "idle" },
    activeTurnId: undefined,
    ...overrides,
  } as ThreadModel;
}

test("isSessionBusy reads the thread status, not the transcript's open turn row", () => {
  expect(isSessionBusy(model({ status: { type: "active" }, activeTurnId: "t1" }))).toBe(true);
  // Between turn/completed and turn/started of an inline turn boundary the
  // id is gone while the session is still working.
  expect(isSessionBusy(model({ status: { type: "active" }, activeTurnId: undefined }))).toBe(true);
  expect(isSessionBusy(model({ status: { type: "idle" }, activeTurnId: "t1" }))).toBe(false);
  // Mid-ask the daemon advertises no steer or queue (appCapabilitiesLocked
  // derives both from an active status), and the palette agrees.
  expect(isSessionBusy(model({ status: { type: "awaiting" }, activeTurnId: "t1" }))).toBe(false);
});
