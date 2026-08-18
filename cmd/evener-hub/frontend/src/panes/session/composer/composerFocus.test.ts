import { act, renderHook } from "@testing-library/react";
import { expect, test } from "vitest";
import { consumeComposerFocus, requestComposerFocus, useComposerFocusRequest } from "./composerFocus";

test("useComposerFocusRequest: reports nothing for a ref with no pending request", () => {
  const { result } = renderHook(() => useComposerFocusRequest("ref-none"));
  expect(result.current).toBeUndefined();
});

test("requestComposerFocus: a request becomes visible to that ref's hook", () => {
  const { result } = renderHook(() => useComposerFocusRequest("ref-a"));
  act(() => requestComposerFocus("ref-a"));
  expect(result.current).not.toBeUndefined();
});

test("requestComposerFocus: does not leak into a different ref's hook", () => {
  const { result } = renderHook(() => useComposerFocusRequest("ref-b-other"));
  act(() => requestComposerFocus("ref-b-target"));
  expect(result.current).toBeUndefined();
});

test("consumeComposerFocus: clears the pending request for that ref", () => {
  const { result } = renderHook(() => useComposerFocusRequest("ref-c"));
  act(() => requestComposerFocus("ref-c"));
  expect(result.current).not.toBeUndefined();
  act(() => consumeComposerFocus("ref-c"));
  expect(result.current).toBeUndefined();
});

test("two requests for the same ref carry distinct ids, so a subscriber can tell them apart", () => {
  const { result } = renderHook(() => useComposerFocusRequest("ref-d"));
  act(() => requestComposerFocus("ref-d"));
  const firstId = result.current?.id;
  act(() => consumeComposerFocus("ref-d"));
  act(() => requestComposerFocus("ref-d"));
  expect(result.current?.id).not.toBe(firstId);
});
