import { act, renderHook } from "@testing-library/react";
import { expect, test } from "vitest";
import { consumeQuoteInsert, requestQuoteInsert, useQuoteInsertRequest } from "./quoteInsert";

test("useQuoteInsertRequest: reports nothing for a ref with no pending request", () => {
  const { result } = renderHook(() => useQuoteInsertRequest("ref-none"));
  expect(result.current).toBeUndefined();
});

test("requestQuoteInsert: a request becomes visible to that ref's hook", () => {
  const { result } = renderHook(() => useQuoteInsertRequest("ref-a"));
  act(() => requestQuoteInsert("ref-a", "> quoted\n\n"));
  expect(result.current?.text).toBe("> quoted\n\n");
});

test("requestQuoteInsert: does not leak into a different ref's hook", () => {
  const { result } = renderHook(() => useQuoteInsertRequest("ref-b-other"));
  act(() => requestQuoteInsert("ref-b-target", "> quoted\n\n"));
  expect(result.current).toBeUndefined();
});

test("consumeQuoteInsert: clears the pending request for that ref", () => {
  const { result } = renderHook(() => useQuoteInsertRequest("ref-c"));
  act(() => requestQuoteInsert("ref-c", "> first\n\n"));
  expect(result.current?.text).toBe("> first\n\n");
  act(() => consumeQuoteInsert("ref-c"));
  expect(result.current).toBeUndefined();
});

test("requestQuoteInsert: placement defaults to append when omitted", () => {
  const { result } = renderHook(() => useQuoteInsertRequest("ref-placement-default"));
  act(() => requestQuoteInsert("ref-placement-default", "> quoted\n\n"));
  expect(result.current?.placement).toBe("append");
});

test("requestQuoteInsert: an explicit placement is recorded on the request", () => {
  const { result } = renderHook(() => useQuoteInsertRequest("ref-placement-prefix"));
  act(() => requestQuoteInsert("ref-placement-prefix", "/plugin:review ", "prefix"));
  expect(result.current?.placement).toBe("prefix");
  expect(result.current?.text).toBe("/plugin:review ");
});

test("two requests for the same ref carry distinct ids, so a subscriber can tell them apart even if the text repeats", () => {
  const { result } = renderHook(() => useQuoteInsertRequest("ref-d"));
  act(() => requestQuoteInsert("ref-d", "> same\n\n"));
  const firstId = result.current?.id;
  act(() => consumeQuoteInsert("ref-d"));
  act(() => requestQuoteInsert("ref-d", "> same\n\n"));
  expect(result.current?.id).not.toBe(firstId);
});
