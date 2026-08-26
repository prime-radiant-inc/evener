import { renderHook } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import {
  beginDisclosureBaseline,
  clearDisclosureScope,
  disclosureDefault,
  isDisclosureOpen,
  resetDisclosureStoreForTests,
  setDisclosureOpen,
  toggleDisclosure,
} from "./disclosureStore";

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

const scopedId = (scope: string, id: string): string => `${scope}\0${id}`;

test("Activity defaults eligible disclosures closed", () => {
  beginDisclosureBaseline("live:activity", ["tool", "thought"], false);

  expect(disclosureDefault("live:activity", "tool", true)).toBe(false);
  expect(disclosureDefault("live:activity", "thought", true)).toBe(false);
  expect(readOpen(scopedId("live:activity", "tool"), disclosureDefault("live:activity", "tool", false))).toBe(false);
});

test("entering Full clears closed overrides once and opens current eligible ids", () => {
  const scope = "live:full";
  beginDisclosureBaseline(scope, ["tool", "thought"], false);
  setDisclosureOpen(scopedId(scope, "tool"), false);

  beginDisclosureBaseline(scope, ["tool", "thought"], true);

  expect(readOpen(scopedId(scope, "tool"), disclosureDefault(scope, "tool", false))).toBe(true);
  expect(readOpen(scopedId(scope, "thought"), disclosureDefault(scope, "thought", false))).toBe(true);
});

test("a later manual collapse wins and a new eligible Full id opens by default", () => {
  const scope = "live:full-manual";
  beginDisclosureBaseline(scope, ["tool"], false);
  beginDisclosureBaseline(scope, ["tool"], true);
  toggleDisclosure(scopedId(scope, "tool"), disclosureDefault(scope, "tool", false));

  beginDisclosureBaseline(scope, ["tool", "new-tool"], true);

  expect(readOpen(scopedId(scope, "tool"), disclosureDefault(scope, "tool", false))).toBe(false);
  expect(readOpen(scopedId(scope, "new-tool"), disclosureDefault(scope, "new-tool", false))).toBe(true);
});

test("returning to Full starts a new baseline", () => {
  const scope = "live:full-again";
  beginDisclosureBaseline(scope, ["tool"], false);
  beginDisclosureBaseline(scope, ["tool"], true);
  toggleDisclosure(scopedId(scope, "tool"), disclosureDefault(scope, "tool", false));
  beginDisclosureBaseline(scope, ["tool"], false);
  beginDisclosureBaseline(scope, ["tool"], true);

  expect(readOpen(scopedId(scope, "tool"), disclosureDefault(scope, "tool", false))).toBe(true);
});

test("preview and live disclosure scopes never collide", () => {
  beginDisclosureBaseline("live:session", ["shared"], true);
  beginDisclosureBaseline("preview:test", ["shared"], false);
  setDisclosureOpen(scopedId("preview:test", "shared"), true);

  expect(readOpen(scopedId("live:session", "shared"), disclosureDefault("live:session", "shared", false))).toBe(true);
  expect(readOpen(scopedId("preview:test", "shared"), disclosureDefault("preview:test", "shared", false))).toBe(true);
});

test("clearing one disclosure scope leaves other scopes intact", () => {
  beginDisclosureBaseline("live:clear", ["shared"], true);
  beginDisclosureBaseline("preview:keep", ["shared"], true);
  setDisclosureOpen(scopedId("live:clear", "shared"), false);

  clearDisclosureScope("live:clear");

  expect(readOpen(scopedId("live:clear", "shared"), false)).toBe(false);
  expect(readOpen(scopedId("preview:keep", "shared"), disclosureDefault("preview:keep", "shared", false))).toBe(true);
});
