import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeAll, beforeEach, expect, test } from "vitest";
import { resetWorkspaceStoreForTests, workspaceStore } from "../../../shell/workspace";
import { navigationStore } from "../../../stores/navigation/store";
import { keyID } from "../../../stores/navigation/types";
import { OpenTranscriptButton, openTranscript } from "./openTranscript";

beforeAll(async () => {
  await import("../");
});

beforeEach(() => {
  resetWorkspaceStoreForTests();
  navigationStore.setState({ resources: new Map() });
});

afterEach(() => {
  cleanup();
});

function transcriptPanes(ref: string) {
  return workspaceStore
    .getState()
    .panes.filter((pane) => pane.type === "transcript" && (pane.params as { ref?: unknown }).ref === ref);
}

function sessionPane(ref: string) {
  return workspaceStore
    .getState()
    .panes.find((pane) => pane.type === "session" && (pane.params as { ref?: unknown }).ref === ref);
}

// A location the navigation store has already fetched: the only source that
// can prove a nested session's top-level owner.
function seedLocation(ref: string, topLevelRef: string) {
  const key = { kind: "location", ref } as const;
  const resources = new Map(navigationStore.getState().resources);
  resources.set(keyID(key), {
    key,
    data: {
      generation_id: "generation_test",
      revision: 1,
      ref,
      top_level_ref: topLevelRef,
      top_level: ref === topLevelRef,
    },
    loadedRevision: 1,
    targetRevision: null,
    forceToken: 0,
    etag: "etag",
    loading: false,
    stale: false,
    error: null,
    generationID: "generation_test",
  });
  navigationStore.setState({ resources });
}

test("canonicalizes a child opened without a parent when its owning session is later known", () => {
  openTranscript("local:child");
  const first = transcriptPanes("local:child")[0];
  expect(first?.params).toEqual({ ref: "local:child" });

  openTranscript("local:child", "local:owner");

  const child = transcriptPanes("local:child");
  expect(child).toHaveLength(1);
  expect(child[0]?.params).toEqual({ ref: "local:child", parentRef: "local:owner" });
  expect(child[0]?.id).not.toBe(first?.id);
  expect(sessionPane("local:owner")?.slot).toBe("main");
  expect(child[0]?.slot).toBe("secondary");
  expect(workspaceStore.getState().focusedPaneId).toBe(child[0]?.id);
});

test("canonicalizes a child pane across parent contexts without disturbing a retained main session", () => {
  openTranscript("local:child", "local:owner-a");
  openTranscript("local:other", "local:other-owner");
  expect(transcriptPanes("local:other")).toHaveLength(1);

  openTranscript("local:child", "local:owner-b");

  // Neither parent's location is loaded, so no owner is ever proven: the
  // retained main session and the unrelated secondary pane survive every
  // open, and only the child's own pane is canonicalized to the new parent.
  expect(transcriptPanes("local:child")).toHaveLength(1);
  expect(transcriptPanes("local:child")[0]?.params).toEqual({
    ref: "local:child",
    parentRef: "local:owner-b",
  });
  expect(transcriptPanes("local:other")).toHaveLength(1);
  expect(sessionPane("local:owner-a")?.slot).toBe("main");
  expect(sessionPane("local:owner-b")).toBeUndefined();
  expect(workspaceStore.getState().focusedPaneId).toBe(transcriptPanes("local:child")[0]?.id);
});

test("preserves a restored workspace when the transcript owner cannot be proven", () => {
  const workspace = workspaceStore.getState();
  const mainId = workspace.replacePrimary("session", { ref: "local:owner" });
  const nestedId = workspace.openPane("session", { ref: "local:nested" });
  const unrelatedId = workspace.openPane("transcript", { ref: "local:unrelated" });
  workspace.focusPane(nestedId);

  openTranscript("local:child", "local:nested");

  const state = workspaceStore.getState();
  // No location is loaded, so the nested parent cannot be proven to be the
  // owner. Replacing the primary with the unproven guess would discard the
  // restored main session and every secondary pane.
  expect(state.mainPane()?.id).toBe(mainId);
  expect(state.mainPane()?.slot).toBe("main");
  expect(state.panes.some((pane) => pane.id === nestedId)).toBe(true);
  expect(state.panes.some((pane) => pane.id === unrelatedId)).toBe(true);
  expect(sessionPane("local:nested")?.id).toBe(nestedId);
  const child = transcriptPanes("local:child");
  expect(child).toHaveLength(1);
  expect(child[0]?.params).toEqual({ ref: "local:child", parentRef: "local:nested" });
  expect(child[0]?.slot).toBe("secondary");
  expect(state.focusedPaneId).toBe(child[0]?.id);
});

test("promotes the proven top-level owner from a loaded navigation location", () => {
  seedLocation("local:nested", "local:owner");
  const workspace = workspaceStore.getState();
  workspace.replacePrimary("session", { ref: "local:elsewhere" });
  const nestedId = workspace.openPane("session", { ref: "local:nested" });
  workspace.focusPane(nestedId);

  openTranscript("local:child", "local:nested");

  // Ownership is proven: the top-level owner moves into main, never the
  // immediate nested parent. The promotion replaces the whole pane set, so
  // the nested session pane yields to the opened child transcript.
  expect(sessionPane("local:owner")?.slot).toBe("main");
  expect(sessionPane("local:nested")).toBeUndefined();
  const child = transcriptPanes("local:child");
  expect(child).toHaveLength(1);
  expect(child[0]?.params).toEqual({ ref: "local:child", parentRef: "local:nested" });
  expect(workspaceStore.getState().focusedPaneId).toBe(child[0]?.id);
});

test("focuses an already exact child pane without remounting it or duplicating it", () => {
  const owner = workspaceStore.getState().openPane("session", { ref: "local:owner" });
  const exact = workspaceStore.getState().openPane("transcript", { ref: "local:child", parentRef: "local:owner" });
  const unrelated = workspaceStore.getState().openPane("transcript", { ref: "local:other" });
  workspaceStore.getState().focusPane(unrelated);

  openTranscript("local:child", "local:owner");

  expect(transcriptPanes("local:child")).toHaveLength(1);
  expect(transcriptPanes("local:child")[0]?.id).toBe(exact);
  expect(sessionPane("local:owner")?.id).toBe(owner);
  expect(sessionPane("local:owner")?.slot).toBe("main");
  expect(workspaceStore.getState().focusedPaneId).toBe(exact);
  expect(transcriptPanes("local:other")).toHaveLength(1);
});

test("keeps no-parent opening deduped and usable without a desktop host", () => {
  openTranscript("remote:child");
  const first = transcriptPanes("remote:child")[0];

  openTranscript("remote:child");

  expect(transcriptPanes("remote:child")).toHaveLength(1);
  expect(transcriptPanes("remote:child")[0]?.id).toBe(first?.id);
  expect(transcriptPanes("remote:child")[0]?.slot).toBe("main");
  expect(workspaceStore.getState().focusedPaneId).toBe(first?.id);
});

test("OpenTranscriptButton renders the glyph with no visible label, tooltip 'Open', and opens on click", () => {
  render(<OpenTranscriptButton transcriptRef="local:child" parentRef="local:owner" />);

  const button = screen.getByRole("button", { name: "Open transcript" });
  // Icon-only: the accessible name comes from aria-label, not visible text.
  expect(button.textContent).toBe("");
  expect(button.getAttribute("title")).toBe("Open");

  fireEvent.click(button);
  expect(transcriptPanes("local:child")).toHaveLength(1);
  expect(transcriptPanes("local:child")[0]?.params).toEqual({ ref: "local:child", parentRef: "local:owner" });
});

test("exact Open origin: canonical reuse replaces session and transcript origins without changing panes", async () => {
  const { transcriptOpenOrigin } = await import("../../../shell/workspace");
  const workspace = workspaceStore.getState();
  const owner = workspace.openPane("session", { ref: "local:owner" });
  openTranscript("local:child", "local:owner");
  const child = transcriptPanes("local:child")[0]!;
  openTranscript("local:leaf", "local:child");
  const leaf = transcriptPanes("local:leaf")[0]!;
  expect(transcriptOpenOrigin(leaf)).toBe(child);
  expect(transcriptOpenOrigin(child)).toBe(sessionPane("local:owner"));
  const session = workspace.openPane("session", { ref: "local:child" });
  const retained = workspaceStore.getState().panes;

  openTranscript("local:leaf", "local:child");
  expect(transcriptOpenOrigin(leaf)).toBe(sessionPane("local:child"));
  expect(transcriptOpenOrigin(leaf)?.id).toBe(session);
  expect(workspaceStore.getState().panes).toEqual(retained);
  expect(transcriptPanes("local:leaf")[0]).toBe(leaf);

  workspace.focusPane(child.id);
  openTranscript("local:leaf", "local:child");
  expect(transcriptOpenOrigin(leaf)).toBe(child);
  expect(workspaceStore.getState().focusedPaneId).toBe(leaf.id);
  expect(workspaceStore.getState().mainPane()?.id).toBe(owner);
  expect(workspaceStore.getState().panes).toEqual(retained);
});

test.each([
  { context: "unrelated session", type: "session" as const, params: { ref: "local:unrelated" } },
  { context: "unrelated transcript", type: "transcript" as const, params: { ref: "local:unrelated" } },
  { context: "same-ref document", type: "doc" as const, params: { ref: "local:owner" } },
  { context: "no focused pane", type: null, params: {} },
])("exact Open origin: $context cannot become the canonical child's return context", async ({ type, params }) => {
  const { transcriptOpenOrigin } = await import("../../../shell/workspace");
  if (type === "doc") await import("../../doc");
  const workspace = workspaceStore.getState();
  const owner = workspace.openPane("session", { ref: "local:owner" });
  openTranscript("local:leaf", "local:owner");
  const leaf = transcriptPanes("local:leaf")[0]!;
  expect(transcriptOpenOrigin(leaf)?.id).toBe(owner);
  if (type) workspace.openPane(type, params);
  else workspaceStore.setState({ focusedPaneId: null });
  const retained = workspaceStore.getState().panes;

  openTranscript("local:leaf", "local:owner");

  expect(transcriptOpenOrigin(leaf)).toBeUndefined();
  expect(transcriptPanes("local:leaf")[0]).toBe(leaf);
  expect(workspaceStore.getState().panes).toEqual(retained);
});
