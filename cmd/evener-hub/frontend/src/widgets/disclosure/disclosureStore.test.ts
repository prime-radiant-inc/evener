import { act, cleanup, renderHook } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { isDisclosureOpen, resetDisclosureStoreForTests, setDisclosureOpen, toggleDisclosure } from "./disclosureStore";

afterEach(() => {
  cleanup();
  resetDisclosureStoreForTests();
});

// The store's baseline semantics live with the store, in the package's own
// disclosure.test.ts, against a fresh createDisclosureStore() with no view
// layer. This file owns only the one job the adapter adds: isDisclosureOpen is
// a reactive hook over zustand's useStore, so it re-renders its caller when
// that store changes.
test("the hook re-renders when the store changes", async () => {
  const { result } = renderHook(() => isDisclosureOpen("a", false));
  expect(result.current).toBe(false);

  await act(async () => {
    setDisclosureOpen("a", true);
  });
  expect(result.current).toBe(true);

  await act(async () => {
    toggleDisclosure("a", false);
  });
  expect(result.current).toBe(false);
});
