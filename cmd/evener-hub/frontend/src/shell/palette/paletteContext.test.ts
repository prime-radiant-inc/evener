// @vitest-environment node

import { afterEach, beforeEach, expect, test } from "vitest";
import { presentAttentionTarget } from "../attentionPlacement";
import { resetWorkspaceStoreForTests, workspaceStore } from "../workspace";
import { buildPaletteContext } from "./paletteContext";
import "../../panes/session";
import "../../panes/sessionPanels";

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
  expect(buildPaletteContext()).toEqual({ sessionRef: "local:abc", sessionPaneId: "p1", onPage: "session" });
});

test.each(["sessionTasks", "sessionDetails"])(
  "buildPaletteContext derives session scope from focused %s pane",
  (type) => {
    focus(type, { ref: "local:panel" });
    expect(buildPaletteContext()).toEqual({ sessionRef: "local:panel", sessionPaneId: null, onPage: "session" });
  },
);

test("a focused session panel pins the open session pane for its ref as the addressed seat", () => {
  // The handoff inserts into the session's composer, so with the panel
  // focused the destination is the ref's open session pane - the unique
  // seat - not the panel. With no session pane open there is no unique
  // destination and the pin stays null.
  workspaceStore.setState({
    panes: [
      { id: "p1", type: "sessionTasks" as never, params: { ref: "local:panel" }, slot: "main" },
      { id: "p2", type: "session" as never, params: { ref: "local:panel" }, slot: "secondary" },
    ],
    focusedPaneId: "p1",
  });
  expect(buildPaletteContext()).toEqual({
    sessionRef: "local:panel",
    sessionPaneId: "p2",
    onPage: "session",
  });
});

test.each([false, true])("a focused panel skips a parked composer with a presented duplicate: %s", (duplicate) => {
  const parked = workspaceStore.getState().openPane("session", { ref: "local:panel" });
  presentAttentionTarget("local:target");
  const visible = duplicate ? workspaceStore.getState().openPane("session", { ref: "local:panel" }) : null;
  workspaceStore.getState().openPane("sessionTasks", { ref: "local:panel" });
  expect(buildPaletteContext()).toEqual({ sessionRef: "local:panel", sessionPaneId: visible, onPage: "session" });
  expect(workspaceStore.getState().panes.find((pane) => pane.id === parked)?.slot).toBe("parked");
});

test("a retired Activity descriptor carries no interactive palette context", () => {
  focus("sessionActivity", { ref: "local:retired" });
  expect(buildPaletteContext()).toEqual({ sessionRef: null, sessionPaneId: null, onPage: "other" });
});

test("buildPaletteContext yields a null sessionRef when the focused pane is not a session", () => {
  focus("spawn", {});
  expect(buildPaletteContext()).toEqual({ sessionRef: null, sessionPaneId: null, onPage: "spawn" });
  focus("settings", { section: "theme" });
  expect(buildPaletteContext()).toEqual({ sessionRef: null, sessionPaneId: null, onPage: "settings" });
});

test("buildPaletteContext treats a transcript pane as non-session (no interactive context)", () => {
  focus("transcript", { ref: "local:abc" });
  expect(buildPaletteContext()).toEqual({ sessionRef: null, sessionPaneId: null, onPage: "other" });
});

test("buildPaletteContext addresses a focused cascade's leaf conversation under its own pane", () => {
  // A cascade column is a live conversation seat: session commands hand off to
  // the leaf conversation, addressed to the pane hosting the column.
  focus("sessionZoom", {
    ref: "local:leaf",
    source: { type: "session", params: { ref: "local:root" } },
    edges: [{ ownerRef: "local:parent", childRef: "local:leaf", delegateId: "d1" }],
  });
  expect(buildPaletteContext()).toEqual({
    sessionRef: "local:leaf",
    sessionPaneId: "p1",
    onPage: "session",
  });
});

test("buildPaletteContext returns a null sessionRef when nothing is focused", () => {
  expect(buildPaletteContext()).toEqual({ sessionRef: null, sessionPaneId: null, onPage: "other" });
});
