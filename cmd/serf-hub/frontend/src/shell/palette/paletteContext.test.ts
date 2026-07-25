import { afterEach, beforeEach, expect, test } from "vitest";
import type { ThreadModel } from "../../protocol/model";
import { resetWorkspaceStoreForTests, workspaceStore } from "../workspace";
import { buildPaletteContext, hasActiveTurn, isSessionBusy, isSessionEnded } from "./paletteContext";

beforeEach(() => {
  resetWorkspaceStoreForTests();
});
afterEach(() => {
  resetWorkspaceStoreForTests();
});

function focus(type: string, params: unknown): void {
  workspaceStore.setState({ panes: [{ id: "p1", type: type as never, params }], focusedPaneId: "p1" });
}

test("buildPaletteContext reads the focused session pane's ref and page", () => {
  focus("session", { ref: "local:abc" });
  expect(buildPaletteContext()).toEqual({ sessionRef: "local:abc", onPage: "session" });
});

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

// --- model-derived predicates ---

function model(overrides: Partial<ThreadModel>): ThreadModel {
  return {
    status: { type: "idle" },
    activeTurnId: undefined,
    ...overrides,
  } as ThreadModel;
}

test("isSessionEnded is true only for ended/closed status", () => {
  expect(isSessionEnded(model({ status: { type: "ended" } }))).toBe(true);
  expect(isSessionEnded(model({ status: { type: "closed" } }))).toBe(true);
  expect(isSessionEnded(model({ status: { type: "active" } }))).toBe(false);
  expect(isSessionEnded(model({ status: { type: "idle" } }))).toBe(false);
});

test("isSessionBusy requires BOTH active status and a landed turn id", () => {
  expect(isSessionBusy(model({ status: { type: "active" }, activeTurnId: "t1" }))).toBe(true);
  // status active but the turn id hasn't landed yet: not busy (submitRouting note).
  expect(isSessionBusy(model({ status: { type: "active" }, activeTurnId: undefined }))).toBe(false);
  expect(isSessionBusy(model({ status: { type: "awaiting" }, activeTurnId: "t1" }))).toBe(false);
});

test("hasActiveTurn is the weaker turn-id-only guard", () => {
  expect(hasActiveTurn(model({ activeTurnId: "t1" }))).toBe(true);
  expect(hasActiveTurn(model({ activeTurnId: undefined }))).toBe(false);
  // awaiting-with-a-turn (mid-ask) still counts as having an active turn.
  expect(hasActiveTurn(model({ status: { type: "awaiting" }, activeTurnId: "t1" }))).toBe(true);
});
