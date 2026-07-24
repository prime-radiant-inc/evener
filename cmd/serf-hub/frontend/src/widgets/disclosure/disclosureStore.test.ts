import { renderHook } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { isDisclosureOpen, resetDisclosureStoreForTests, setDisclosureOpen, toggleDisclosure } from "./disclosureStore";

afterEach(() => resetDisclosureStoreForTests());

// isDisclosureOpen is a reactive hook (it rides useStore, mirroring
// subagentModuleStore.ts's useSubagentRows), so it must be read inside a
// render context - renderHook is exactly how that sibling store's own test
// reads its reactive selector. setDisclosureOpen/toggleDisclosure are plain
// getState/setState mutators and are called directly.
const readOpen = (id: string, fallback: boolean): boolean =>
  renderHook(() => isDisclosureOpen(id, fallback)).result.current;

test("unset id reports the fallback", () => {
  expect(readOpen("a", false)).toBe(false);
  expect(readOpen("a", true)).toBe(true);
});

test("setDisclosureOpen overrides the fallback and persists", () => {
  setDisclosureOpen("a", true);
  expect(readOpen("a", false)).toBe(true);
  setDisclosureOpen("a", false);
  expect(readOpen("a", true)).toBe(false);
});

test("toggle flips from the fallback then from stored state", () => {
  toggleDisclosure("a", false); // fallback false -> true
  expect(readOpen("a", false)).toBe(true);
  toggleDisclosure("a", false); // stored true -> false
  expect(readOpen("a", false)).toBe(false);
});
