import { configAtLevel, makeTranscriptDisplayConfig } from "@evener/appwire-client";
import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import {
  registerTranscriptView,
  resetTranscriptViewRegistryForTests,
} from "../panes/session/transcript/flow/transcriptViewRegistry";
import { installLocalStorage, MemoryStorage } from "../storageTestUtils";
import {
  configForSession,
  resetSessionDetailLevelsForTests,
  SESSION_DETAIL_LEVEL_KEY,
  sessionDetailLevel,
  setSessionDetailLevel,
  useSessionDetailLevel,
} from "./sessionDetailLevel";

// A default that differs from every preset in an advanced flag, so a result
// that kept the default's advanced settings is distinguishable from a bare
// preset.
const base = makeTranscriptDisplayConfig({ kind: "preset", level: "tools" }, { roundTimings: true });

beforeAll(() => {
  installLocalStorage(new MemoryStorage());
});

beforeEach(() => {
  localStorage.clear();
  resetSessionDetailLevelsForTests();
  resetTranscriptViewRegistryForTests();
});

afterEach(() => {
  localStorage.clear();
  resetSessionDetailLevelsForTests();
  resetTranscriptViewRegistryForTests();
});

test("a level is read back for its own session and not for another", () => {
  setSessionDetailLevel("ref_a", "full", base);
  expect(sessionDetailLevel("ref_a")).toBe("full");
  expect(sessionDetailLevel("ref_b")).toBeNull();
});

test("a chosen level is persisted and survives a reload of the store", () => {
  setSessionDetailLevel("ref_a", "chat", base);
  expect(localStorage.getItem(SESSION_DETAIL_LEVEL_KEY)).not.toBeNull();
  resetSessionDetailLevelsForTests();
  expect(sessionDetailLevel("ref_a")).toBe("chat");
});

test("returning to the default clears the level and the clearing is persisted", () => {
  setSessionDetailLevel("ref_a", "chat", base);
  setSessionDetailLevel("ref_b", "full", base);
  setSessionDetailLevel("ref_a", null, base);
  expect(sessionDetailLevel("ref_a")).toBeNull();
  resetSessionDetailLevelsForTests();
  expect(sessionDetailLevel("ref_a")).toBeNull();
  expect(sessionDetailLevel("ref_b")).toBe("full");
});

test("configForSession leaves the default unchanged without a level", () => {
  expect(configForSession(null, base)).toBe(base);
});

test("configForSession shows the session's level over the default's advanced settings", () => {
  const result = configForSession("full", base);
  expect(result).toEqual(configAtLevel("full", base));
  expect(result).not.toEqual(base);
  expect(result.advanced).toEqual(base.advanced);
});

test("a mounted reader re-renders when its session's level changes and ignores other sessions", () => {
  const { result } = renderHook(() => useSessionDetailLevel("ref_a"));
  expect(result.current).toBeNull();
  act(() => setSessionDetailLevel("ref_a", "activity", base));
  expect(result.current).toBe("activity");
  act(() => setSessionDetailLevel("ref_b", "chat", base));
  expect(result.current).toBe("activity");
  act(() => setSessionDetailLevel("ref_a", null, base));
  expect(result.current).toBeNull();
});

test("a reader without a session ref never has a level", () => {
  setSessionDetailLevel("ref_a", "full", base);
  const { result } = renderHook(() => useSessionDetailLevel(undefined));
  expect(result.current).toBeNull();
});

function fakeView() {
  const captured = { anchorOffset: 0, normalizedOffset: 0, followingBottom: false };
  const view = {
    id: "view_1",
    capture: vi.fn(() => captured),
    restore: vi.fn(),
    announce: vi.fn(),
  };
  return { view, captured };
}

test("changing a level captures and restores mounted views around the change and announces it", () => {
  const { view, captured } = fakeView();
  const order: string[] = [];
  view.capture.mockImplementation(() => {
    order.push("capture");
    return captured;
  });
  view.restore.mockImplementation(() => {
    order.push(`restore:${sessionDetailLevel("ref_a")}`);
  });
  registerTranscriptView(view);

  setSessionDetailLevel("ref_a", "full", base);

  expect(order).toEqual(["capture", "restore:full"]);
  expect(view.restore).toHaveBeenCalledWith(captured);
  expect(view.announce).toHaveBeenCalledOnce();
});

test("returning to the default also captures, restores and announces", () => {
  setSessionDetailLevel("ref_a", "full", base);
  const { view } = fakeView();
  registerTranscriptView(view);

  setSessionDetailLevel("ref_a", null, base);

  expect(view.capture).toHaveBeenCalledOnce();
  expect(view.restore).toHaveBeenCalledOnce();
  expect(view.announce).toHaveBeenCalledOnce();
});

test("setting the level a session already has does nothing", () => {
  setSessionDetailLevel("ref_a", "full", base);
  const { view } = fakeView();
  registerTranscriptView(view);

  setSessionDetailLevel("ref_a", "full", base);
  setSessionDetailLevel("ref_b", null, base);

  expect(view.capture).not.toHaveBeenCalled();
  expect(view.restore).not.toHaveBeenCalled();
  expect(view.announce).not.toHaveBeenCalled();
});
