// @vitest-environment jsdom

// Pins the iOS half of the on-screen-keyboard fix: the hook mirrors the
// visual viewport's occluded bottom strip into --keyboard-inset, which the
// mobile .shell rule (AppShell.module.css) spends as padding-bottom. The fake
// visualViewport below is the external boundary (the browser's own object);
// everything under test is the real hook.
import { renderHook } from "@testing-library/react";
import { afterEach, describe, expect, test, vi } from "vitest";
import { keyboardInset, useKeyboardInset } from "./useKeyboardInset";

type Listener = () => void;

class FakeVisualViewport {
  height = 700;
  offsetTop = 0;
  scale = 1;
  private listeners = new Map<string, Set<Listener>>();

  addEventListener(type: string, listener: Listener): void {
    let set = this.listeners.get(type);
    if (!set) {
      set = new Set();
      this.listeners.set(type, set);
    }
    set.add(listener);
  }

  removeEventListener(type: string, listener: Listener): void {
    this.listeners.get(type)?.delete(listener);
  }

  listenerCount(type: string): number {
    return this.listeners.get(type)?.size ?? 0;
  }

  fire(type: "resize" | "scroll"): void {
    for (const listener of this.listeners.get(type) ?? []) listener();
  }
}

function installVisualViewport(): FakeVisualViewport {
  const fake = new FakeVisualViewport();
  vi.stubGlobal("visualViewport", fake);
  return fake;
}

function setInnerHeight(px: number): void {
  Object.defineProperty(window, "innerHeight", { value: px, configurable: true, writable: true });
}

afterEach(() => {
  vi.unstubAllGlobals();
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

  test("unmount removes the variable and every listener", () => {
    const vv = installVisualViewport();
    setInnerHeight(768);
    const { unmount } = renderHook(() => useKeyboardInset());
    expect(vv.listenerCount("resize")).toBe(1);
    expect(vv.listenerCount("scroll")).toBe(1);

    unmount();
    expect(vv.listenerCount("resize")).toBe(0);
    expect(vv.listenerCount("scroll")).toBe(0);
    expect(document.documentElement.style.getPropertyValue("--keyboard-inset")).toBe("");
  });

  test("is a no-op when the browser has no visualViewport", () => {
    vi.stubGlobal("visualViewport", null);
    const { unmount } = renderHook(() => useKeyboardInset());
    expect(document.documentElement.style.getPropertyValue("--keyboard-inset")).toBe("");
    unmount();
  });
});
