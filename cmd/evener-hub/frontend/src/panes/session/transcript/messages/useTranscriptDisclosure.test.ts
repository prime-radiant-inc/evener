import { scopedDisclosureId } from "@evener/appwire-client";
import { act, cleanup, renderHook } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import {
  beginDisclosureBaseline,
  resetDisclosureStoreForTests,
  setDisclosureOpen,
} from "../../../../widgets/disclosure/disclosureStore";
import { useTranscriptDisclosure } from "./useTranscriptDisclosure";

const SCOPE = "transcript:readOnly:ref-1";
const ID = "item_1";

afterEach(() => {
  cleanup();
  resetDisclosureStoreForTests();
});

test("scopes the id and applies the caller's default", () => {
  const { result } = renderHook(() => useTranscriptDisclosure({ scope: SCOPE, id: ID, defaultOpen: true }));
  expect(result.current.key).toBe(scopedDisclosureId(SCOPE, ID));
  expect(result.current.fallback).toBe(true);
  expect(result.current.open).toBe(true);
});

test("an open shared baseline raises a closed caller default", () => {
  act(() => beginDisclosureBaseline(SCOPE, [ID], true));
  const { result } = renderHook(() => useTranscriptDisclosure({ scope: SCOPE, id: ID }));
  expect(result.current.fallback).toBe(true);
  expect(result.current.open).toBe(true);
});

test("a reader's stored choice wins over both defaults", () => {
  act(() => beginDisclosureBaseline(SCOPE, [ID], true));
  act(() => setDisclosureOpen(scopedDisclosureId(SCOPE, ID), false));
  const { result } = renderHook(() => useTranscriptDisclosure({ scope: SCOPE, id: ID, defaultOpen: true }));
  expect(result.current.fallback).toBe(true);
  expect(result.current.open).toBe(false);
});
