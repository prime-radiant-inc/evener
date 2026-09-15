import { renderHook } from "@testing-library/react";
import { beforeEach, expect, test } from "vitest";
import { topNotesStore, useTopNotesExpanded, useTopNotesFocusEpoch } from "./topNotes";

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

test("openAndFocus expands and increments focus epoch", () => {
  expect(topNotesStore.getState().getFocusEpoch("ref_1")).toBe(0);

  topNotesStore.getState().openAndFocus("ref_1");
  expect(topNotesStore.getState().isExpanded("ref_1")).toBe(true);
  expect(topNotesStore.getState().getFocusEpoch("ref_1")).toBe(1);

  topNotesStore.getState().openAndFocus("ref_1");
  expect(topNotesStore.getState().isExpanded("ref_1")).toBe(true);
  expect(topNotesStore.getState().getFocusEpoch("ref_1")).toBe(2);
});

test("useTopNotesExpanded and useTopNotesFocusEpoch reflect store state", () => {
  const { result: exp } = renderHook(() => useTopNotesExpanded("ref_1"));
  const { result: epoch } = renderHook(() => useTopNotesFocusEpoch("ref_1"));

  expect(exp.current).toBe(false);
  expect(epoch.current).toBe(0);

  topNotesStore.getState().openAndFocus("ref_1");

  expect(topNotesStore.getState().isExpanded("ref_1")).toBe(true);
  expect(topNotesStore.getState().getFocusEpoch("ref_1")).toBe(1);
});
