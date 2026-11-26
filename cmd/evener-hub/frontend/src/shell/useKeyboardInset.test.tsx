// @vitest-environment jsdom

// Pins the hook half of the keyboard fix (see useKeyboardInset.ts's header
// for the full rationale): --keyboard-inset tracks the visual viewport's
// occluded bottom strip. The fake visualViewport below is the external
// boundary (the browser's own object); everything under test is the real hook.
import { renderHook } from "@testing-library/react";
import { afterEach, describe, expect, test, vi } from "vitest";
import { keyboardInset, useKeyboardInset } from "./useKeyboardInset";

class FakeVisualViewport extends EventTarget {
  height = 700;
  offsetTop = 0;
  scale = 1;

  fire(type: "resize" | "scroll"): void {
    this.dispatchEvent(new Event(type));
  }
}

function installVisualViewport(): FakeVisualViewport {
  const fake = new FakeVisualViewport();
  vi.stubGlobal("visualViewport", fake);
  return fake;
}

const originalInnerHeight = Object.getOwnPropertyDescriptor(window, "innerHeight");
const originalClientHeight = Object.getOwnPropertyDescriptor(document.documentElement, "clientHeight");

function setInnerHeight(px: number): void {
  Object.defineProperty(window, "innerHeight", { value: px, configurable: true, writable: true });
}

function setClientHeight(px: number): void {
  Object.defineProperty(document.documentElement, "clientHeight", { value: px, configurable: true });
}

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  if (originalInnerHeight) Object.defineProperty(window, "innerHeight", originalInnerHeight);
  if (originalClientHeight) Object.defineProperty(document.documentElement, "clientHeight", originalClientHeight);
  else Reflect.deleteProperty(document.documentElement, "clientHeight");
  document.documentElement.style.removeProperty("--keyboard-inset");
});

describe("keyboardInset", () => {
  test("is the layout-viewport strip hanging below the visual viewport", () => {
    expect(keyboardInset({ height: 400, offsetTop: 0, scale: 1 }, 768)).toBe(368);
  });

  test("subtracts the visual viewport's scroll offset", () => {
    expect(keyboardInset({ height: 400, offsetTop: 100, scale: 1 }, 768)).toBe(268);
  });

  test("never goes negative when the visual viewport covers the layout viewport", () => {
    expect(keyboardInset({ height: 800, offsetTop: 0, scale: 1 }, 768)).toBe(0);
  });

  test("reports 0 under pinch zoom, which occludes nothing", () => {
    expect(keyboardInset({ height: 400, offsetTop: 0, scale: 2 }, 768)).toBe(0);
  });
});

describe("useKeyboardInset", () => {
  test("sets --keyboard-inset on mount and tracks resize and scroll events", () => {
    const vv = installVisualViewport();
    setInnerHeight(768);
    vv.height = 768;
    const { unmount } = renderHook(() => useKeyboardInset());
    expect(document.documentElement.style.getPropertyValue("--keyboard-inset")).toBe("0px");

    vv.height = 400; // keyboard opened
    vv.fire("resize");
    expect(document.documentElement.style.getPropertyValue("--keyboard-inset")).toBe("368px");

    vv.offsetTop = 100; // Safari scrolled the visual viewport down
    vv.fire("scroll");
    expect(document.documentElement.style.getPropertyValue("--keyboard-inset")).toBe("268px");

    unmount();
  });

  test("reads the shrunk layout viewport when resizes-content leaves window.innerHeight stale", () => {
    const vv = installVisualViewport();
    setInnerHeight(580);
    setClientHeight(580);
    vv.height = 580;
    const { unmount } = renderHook(() => useKeyboardInset());
    expect(document.documentElement.style.getPropertyValue("--keyboard-inset")).toBe("0px");

    // Keyboard opens under interactive-widget=resizes-content: the layout
    // viewport and visualViewport both shrink to 374 while window.innerHeight
    // stays stale at 580.
    setClientHeight(374);
    vv.height = 374;
    vv.fire("resize");
    expect(document.documentElement.style.getPropertyValue("--keyboard-inset")).toBe("0px");

    // Keyboard closes, then opens with visualViewport shrinking before the
    // layout viewport commits: window resize settles --keyboard-inset back to 0.
    setClientHeight(580);
    vv.height = 580;
    vv.fire("resize");
    vv.height = 374;
    vv.fire("resize");
    expect(document.documentElement.style.getPropertyValue("--keyboard-inset")).toBe("206px");

    setClientHeight(374);
    window.dispatchEvent(new Event("resize"));
    expect(document.documentElement.style.getPropertyValue("--keyboard-inset")).toBe("0px");

    unmount();
  });

  test("unmount removes the variable and every listener it added", () => {
    const vv = installVisualViewport();
    setInnerHeight(768);
    const vvAddSpy = vi.spyOn(vv, "addEventListener");
    const vvRemoveSpy = vi.spyOn(vv, "removeEventListener");
    const winAddSpy = vi.spyOn(window, "addEventListener");
    const winRemoveSpy = vi.spyOn(window, "removeEventListener");
    const { unmount } = renderHook(() => useKeyboardInset());
    const vvAdded = vvAddSpy.mock.calls.map(([type, listener]) => ({ type, listener }));
    const winAdded = winAddSpy.mock.calls
      .filter(([type]) => type === "resize")
      .map(([type, listener]) => ({ type, listener }));
    expect(vvAdded.map(({ type }) => type).sort()).toEqual(["resize", "scroll"]);
    expect(winAdded.map(({ type }) => type)).toEqual(["resize"]);

    unmount();
    for (const { type, listener } of vvAdded) {
      expect(vvRemoveSpy).toHaveBeenCalledWith(type, listener);
    }
    for (const { type, listener } of winAdded) {
      expect(winRemoveSpy).toHaveBeenCalledWith(type, listener);
    }
    expect(document.documentElement.style.getPropertyValue("--keyboard-inset")).toBe("");
  });

  test("is a no-op when the browser has no visualViewport", () => {
    vi.stubGlobal("visualViewport", null);
    const { unmount } = renderHook(() => useKeyboardInset());
    expect(document.documentElement.style.getPropertyValue("--keyboard-inset")).toBe("");
    unmount();
  });
});
