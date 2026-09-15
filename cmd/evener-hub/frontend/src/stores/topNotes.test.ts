import { act, renderHook, waitFor } from "@testing-library/react";
import { beforeEach, expect, test } from "vitest";
import { resetWorkspaceStoreForTests, workspaceStore } from "../shell/workspace";
import { resetPanelStoreEvictionForTests } from "./panelStoreEviction";
import { topNotesStore, usePendingTopNotesFocus, useTopNotesExpanded } from "./topNotes";

beforeEach(() => {
  topNotesStore.getState().resetForTests();
  resetPanelStoreEvictionForTests();
  resetWorkspaceStoreForTests();
});

test("top notes is collapsed by default", () => {
  expect(topNotesStore.getState().isExpanded("ref_1")).toBe(false);
});

test("setExpanded updates state for a given session", () => {
  topNotesStore.getState().setExpanded("ref_1", true);
  expect(topNotesStore.getState().isExpanded("ref_1")).toBe(true);
  expect(topNotesStore.getState().isExpanded("ref_2")).toBe(false);

  topNotesStore.getState().setExpanded("ref_1", false);
  expect(topNotesStore.getState().isExpanded("ref_1")).toBe(false);
});

test("toggle inverts expanded state for a given session", () => {
  topNotesStore.getState().toggle("ref_1");
  expect(topNotesStore.getState().isExpanded("ref_1")).toBe(true);

  topNotesStore.getState().toggle("ref_1");
  expect(topNotesStore.getState().isExpanded("ref_1")).toBe(false);
});

test("openAndFocus expands and leaves exactly one servable focus request", () => {
  expect(topNotesStore.getState().hasPendingFocus("ref_1")).toBe(false);

  topNotesStore.getState().openAndFocus("ref_1");
  expect(topNotesStore.getState().isExpanded("ref_1")).toBe(true);
  expect(topNotesStore.getState().hasPendingFocus("ref_1")).toBe(true);

  // The request is served exactly once: the panel that takes it focuses,
  // and nothing re-serves a consumed request.
  expect(topNotesStore.getState().takePendingFocus("ref_1")).toBe(true);
  expect(topNotesStore.getState().hasPendingFocus("ref_1")).toBe(false);
  expect(topNotesStore.getState().takePendingFocus("ref_1")).toBe(false);
});

test("toggleAndFocus opens with a focus request, then collapses without one", () => {
  topNotesStore.getState().toggleAndFocus("ref_1");
  expect(topNotesStore.getState().isExpanded("ref_1")).toBe(true);
  expect(topNotesStore.getState().hasPendingFocus("ref_1")).toBe(true);

  topNotesStore.getState().toggleAndFocus("ref_1");
  expect(topNotesStore.getState().isExpanded("ref_1")).toBe(false);
  // Collapsing does not cancel the still-unserved request (no panel has
  // taken it), and the next open does not stack a second one.
  expect(topNotesStore.getState().hasPendingFocus("ref_1")).toBe(true);

  topNotesStore.getState().toggleAndFocus("ref_1");
  expect(topNotesStore.getState().isExpanded("ref_1")).toBe(true);
  expect(topNotesStore.getState().hasPendingFocus("ref_1")).toBe(true);
  expect(topNotesStore.getState().takePendingFocus("ref_1")).toBe(true);
  expect(topNotesStore.getState().hasPendingFocus("ref_1")).toBe(false);
});

test("focus requests are scoped per session", () => {
  topNotesStore.getState().openAndFocus("ref_1");
  expect(topNotesStore.getState().hasPendingFocus("ref_1")).toBe(true);
  expect(topNotesStore.getState().hasPendingFocus("ref_2")).toBe(false);

  // Serving one session's request leaves the other's untouched.
  expect(topNotesStore.getState().takePendingFocus("ref_2")).toBe(false);
  expect(topNotesStore.getState().hasPendingFocus("ref_1")).toBe(true);
});

// The hooks are what the panel renders from: their values - not the store
// getters behind them - are the contract.
test("useTopNotesExpanded and usePendingTopNotesFocus reflect store updates", () => {
  const { result: exp } = renderHook(() => useTopNotesExpanded("ref_1"));
  const { result: pending } = renderHook(() => usePendingTopNotesFocus("ref_1"));

  expect(exp.current).toBe(false);
  expect(pending.current).toBe(false);

  act(() => {
    topNotesStore.getState().openAndFocus("ref_1");
  });

  expect(exp.current).toBe(true);
  expect(pending.current).toBe(true);
});

// --- eviction: state lives inside the session pane -------------------------
//
// The top-notes panel only exists while a SESSION pane holds the ref.
// Companion panes (details/tasks/activity) keep the ref open in the
// workspace, but they cannot show the notes bar - an expanded flag that
// survives the session pane's close is invisible state, and the next
// /notes on the reopened session would TOGGLE it closed instead of opening.

test("expanded state does not outlive the session pane - companion panes do not keep it", async () => {
  act(() => {
    workspaceStore.setState({
      panes: [
        { id: "p_session", type: "session", params: { ref: "ref_1" }, slot: "main" },
        { id: "p_details", type: "sessionDetails", params: { ref: "ref_1" }, slot: "secondary" },
      ],
      focusedPaneId: "p_session",
    });
  });
  act(() => {
    topNotesStore.getState().openAndFocus("ref_1");
  });
  expect(topNotesStore.getState().isExpanded("ref_1")).toBe(true);

  // Close the session pane; only the details pane still holds the ref.
  act(() => {
    workspaceStore.setState({
      panes: [{ id: "p_details", type: "sessionDetails", params: { ref: "ref_1" }, slot: "main" }],
      focusedPaneId: "p_details",
    });
  });
  await waitFor(() => expect(topNotesStore.getState().isExpanded("ref_1")).toBe(false));
});

test("expanded state survives while the session pane holds the ref", async () => {
  act(() => {
    workspaceStore.setState({
      panes: [{ id: "p_session", type: "session", params: { ref: "ref_1" }, slot: "main" }],
      focusedPaneId: "p_session",
    });
  });
  act(() => {
    topNotesStore.getState().openAndFocus("ref_1");
  });

  // An unrelated workspace change runs the eviction sweep; the open session
  // pane keeps the state alive.
  act(() => {
    workspaceStore.setState({ focusedPaneId: "p_session" });
  });
  await act(async () => {
    await Promise.resolve();
  });
  expect(topNotesStore.getState().isExpanded("ref_1")).toBe(true);
});
