import { act, renderHook } from "@testing-library/react";
import { beforeEach, expect, test } from "vitest";
import { topNotesStore, usePendingTopNotesFocus, useTopNotesExpanded } from "./topNotes";

beforeEach(() => {
  topNotesStore.getState().resetForTests();
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
