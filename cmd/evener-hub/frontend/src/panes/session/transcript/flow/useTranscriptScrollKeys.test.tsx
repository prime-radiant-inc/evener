// Tests for useTranscriptScrollKeys (webui-keybindings-p3 Task 1): the
// session transcript's keyboard scroll seam. Each mounted Session pane
// registers its own transcript.* handlers against the shared keybindings
// registry; the multi-instance rule (a handler returns false when its pane
// is not workspaceStore.focusedPaneId) is the SelectionQuote clobber class
// from Phase 2a, pinned here with a two-pane test. Chord dispatch goes
// through the REAL dispatcher/defaults (installKeybindings) so these tests
// also pin the Alt+Arrow chords themselves, the editable-target suppression,
// and mobile inertness (rail.toggle's no-registration pattern).
import { cleanup, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { ACTIONS } from "../../../../keybindings/actions";
import { DEFAULT_BINDINGS } from "../../../../keybindings/defaults";
import { keybindingsRegistry } from "../../../../keybindings/registry";
import { installKeybindings } from "../../../../shell/installKeybindings";
import { resetMobileViewportForTests } from "../../../../shell/useIsMobile";
import { resetWorkspaceStoreForTests, workspaceStore } from "../../../../shell/workspace";
import type { VirtualListHandle } from "../../../../widgets/virtuallist";
import { TRANSCRIPT_LINE_SCROLL_PX, useTranscriptScrollKeys } from "./useTranscriptScrollKeys";

function keydown(init: KeyboardEventInit): KeyboardEvent {
  return new KeyboardEvent("keydown", { bubbles: true, cancelable: true, ...init });
}

// A VirtualListHandle double whose scroll element is a real (layout-free)
// jsdom div: scrollTop assignment sticks in jsdom, and clientHeight is
// defined explicitly (500px) so the page-scroll step has geometry to read.
function renderPaneKeys(paneId: string, { mounted = true }: { mounted?: boolean } = {}) {
  const el = document.createElement("div");
  Object.defineProperty(el, "clientHeight", { value: 500, configurable: true });
  document.body.appendChild(el);
  const scrollToIndex = vi.fn();
  const listRef = {
    current: {
      scrollToIndex,
      getScrollElement: () => (mounted ? el : null),
      getVisibleRange: () => null,
    } as VirtualListHandle,
  };
  const jumpToBottom = vi.fn();
  // Records the offset AT THE MOMENT the marker is called, so a test can prove
  // the gesture is marked before the scroll write rather than after it.
  const markedAt: number[] = [];
  const markGesture = vi.fn(() => {
    markedAt.push(el.scrollTop);
  });
  const hook = renderHook(() => useTranscriptScrollKeys({ paneId, listRef, jumpToBottom, markGesture }));
  return { el, scrollToIndex, jumpToBottom, markGesture, markedAt, unmount: hook.unmount };
}

// jsdom's scrollTop is an unclamped slot, so a chord that a real browser would
// refuse (scrolling down at the bottom) still "moves" it. These tests need the
// browser's own clamp to exist, since the gesture marker now turns on whether
// the write moved anything.
function clampScrollTop(el: HTMLElement, scrollHeight: number): void {
  let value = 0;
  Object.defineProperty(el, "scrollHeight", { value: scrollHeight, configurable: true });
  Object.defineProperty(el, "scrollTop", {
    configurable: true,
    get: () => value,
    set: (next: number) => {
      value = Math.max(0, Math.min(next, scrollHeight - el.clientHeight));
    },
  });
}

beforeEach(() => {
  resetWorkspaceStoreForTests();
  installKeybindings();
});

afterEach(() => {
  cleanup();
  document.body.innerHTML = "";
  vi.unstubAllGlobals();
  resetMobileViewportForTests();
});

test("Alt+ArrowDown scrolls only the focused pane's transcript (two-pane multi-instance pin)", () => {
  const a = renderPaneKeys("pane_a");
  const b = renderPaneKeys("pane_b");
  workspaceStore.setState({ focusedPaneId: "pane_a" });

  window.dispatchEvent(keydown({ key: "ArrowDown", code: "ArrowDown", altKey: true }));
  expect(a.el.scrollTop).toBe(TRANSCRIPT_LINE_SCROLL_PX);
  expect(b.el.scrollTop).toBe(0);

  workspaceStore.setState({ focusedPaneId: "pane_b" });
  window.dispatchEvent(keydown({ key: "ArrowDown", code: "ArrowDown", altKey: true }));
  expect(a.el.scrollTop).toBe(TRANSCRIPT_LINE_SCROLL_PX);
  expect(b.el.scrollTop).toBe(TRANSCRIPT_LINE_SCROLL_PX);
});

test("Alt+ArrowUp scrolls the focused transcript up one line step", () => {
  const a = renderPaneKeys("pane_a");
  workspaceStore.setState({ focusedPaneId: "pane_a" });
  a.el.scrollTop = 100;

  window.dispatchEvent(keydown({ key: "ArrowUp", code: "ArrowUp", altKey: true }));
  expect(a.el.scrollTop).toBe(100 - TRANSCRIPT_LINE_SCROLL_PX);
});

test("Alt+Shift+ArrowDown/Up page the focused transcript by ~0.9 of the viewport", () => {
  const a = renderPaneKeys("pane_a");
  workspaceStore.setState({ focusedPaneId: "pane_a" });

  window.dispatchEvent(keydown({ key: "ArrowDown", code: "ArrowDown", altKey: true, shiftKey: true }));
  expect(a.el.scrollTop).toBe(450);

  window.dispatchEvent(keydown({ key: "ArrowUp", code: "ArrowUp", altKey: true, shiftKey: true }));
  expect(a.el.scrollTop).toBe(0);
});

test("the Alt+Shift page chords do NOT also fire the plain line-scroll bindings", () => {
  const a = renderPaneKeys("pane_a");
  workspaceStore.setState({ focusedPaneId: "pane_a" });

  window.dispatchEvent(keydown({ key: "ArrowDown", code: "ArrowDown", altKey: true, shiftKey: true }));
  // Exactly the page step, not page+line (the line binding requires Shift to
  // be absent).
  expect(a.el.scrollTop).toBe(450);
});

test("Alt+Arrow scroll chords are suppressed from editable targets", () => {
  const a = renderPaneKeys("pane_a");
  workspaceStore.setState({ focusedPaneId: "pane_a" });
  const input = document.createElement("input");
  document.body.appendChild(input);

  input.dispatchEvent(keydown({ key: "ArrowDown", code: "ArrowDown", altKey: true }));
  expect(a.el.scrollTop).toBe(0);

  window.dispatchEvent(keydown({ key: "ArrowDown", code: "ArrowDown", altKey: true }));
  expect(a.el.scrollTop).toBe(TRANSCRIPT_LINE_SCROLL_PX);
});

test("a keydown no pane accepts is not preventDefaulted (unfocused panes decline, unmounted lists decline)", () => {
  const a = renderPaneKeys("pane_a");
  // pane_b registers handlers too, but its VirtualList has no scroll element.
  renderPaneKeys("pane_b", { mounted: false });
  // Nobody focused: both handlers decline.
  const unfocused = keydown({ key: "ArrowDown", code: "ArrowDown", altKey: true });
  window.dispatchEvent(unfocused);
  expect(unfocused.defaultPrevented).toBe(false);
  expect(a.el.scrollTop).toBe(0);

  // Focused but the transcript's VirtualList has not mounted a scroll
  // element yet (a dormant session renders EmptyTranscript, no list).
  workspaceStore.setState({ focusedPaneId: "pane_b" });
  const unmounted = keydown({ key: "ArrowDown", code: "ArrowDown", altKey: true });
  window.dispatchEvent(unmounted);
  expect(unmounted.defaultPrevented).toBe(false);
});

test("unmounting a pane unregisters only that pane's handlers", () => {
  const a = renderPaneKeys("pane_a");
  const b = renderPaneKeys("pane_b");
  workspaceStore.setState({ focusedPaneId: "pane_b" });
  b.unmount();

  const event = keydown({ key: "ArrowDown", code: "ArrowDown", altKey: true });
  window.dispatchEvent(event);
  expect(event.defaultPrevented).toBe(false);
  expect(a.el.scrollTop).toBe(0);

  workspaceStore.setState({ focusedPaneId: "pane_a" });
  window.dispatchEvent(keydown({ key: "ArrowDown", code: "ArrowDown", altKey: true }));
  expect(a.el.scrollTop).toBe(TRANSCRIPT_LINE_SCROLL_PX);
});

// Phase 4a assigned the default chords (the p4 plan's Design decision 2):
// Alt+Home/Alt+End complete the Alt-arrow scroll family, same plain policy.
test("transcript.scrollTop/scrollBottom default to Alt+Home/Alt+End", () => {
  expect(DEFAULT_BINDINGS.find((b) => b.actionId === ACTIONS.transcriptScrollTop)?.chord).toBe("Alt+Home");
  expect(DEFAULT_BINDINGS.find((b) => b.actionId === ACTIONS.transcriptScrollBottom)?.chord).toBe("Alt+End");
});

test("Alt+Home/Alt+End drive the focused pane's virtualizer (the real default chords)", () => {
  const a = renderPaneKeys("pane_a");
  const b = renderPaneKeys("pane_b");
  workspaceStore.setState({ focusedPaneId: "pane_a" });

  window.dispatchEvent(keydown({ key: "Home", code: "Home", altKey: true }));
  expect(a.scrollToIndex).toHaveBeenCalledWith(0, { align: "start" });
  expect(b.scrollToIndex).not.toHaveBeenCalled();

  window.dispatchEvent(keydown({ key: "End", code: "End", altKey: true }));
  expect(a.jumpToBottom).toHaveBeenCalledOnce();
  expect(b.jumpToBottom).not.toHaveBeenCalled();
});

test("Alt+Home/Alt+End are suppressed from editable targets (Home/End keep their caret meaning)", () => {
  const a = renderPaneKeys("pane_a");
  workspaceStore.setState({ focusedPaneId: "pane_a" });
  const input = document.createElement("input");
  document.body.appendChild(input);

  input.dispatchEvent(keydown({ key: "Home", code: "Home", altKey: true }));
  input.dispatchEvent(keydown({ key: "End", code: "End", altKey: true }));
  expect(a.scrollToIndex).not.toHaveBeenCalled();
  expect(a.jumpToBottom).not.toHaveBeenCalled();
});

// jsdom has no matchMedia at all (useIsMobile.test.ts's header documents the
// probe); a matching stub puts every useIsMobile consumer on mobile.
function installMobileViewport(): void {
  vi.stubGlobal(
    "matchMedia",
    vi.fn((media: string) => ({
      media,
      matches: media === "(max-width: 899px)",
      addEventListener: () => {},
      removeEventListener: () => {},
    })),
  );
}

test("mobile: no transcript scroll handlers register at all (the rail.toggle inertness pattern)", () => {
  installMobileViewport();
  const a = renderPaneKeys("pane_a");
  workspaceStore.setState({ focusedPaneId: "pane_a" });

  expect(keybindingsRegistry.getState().actions.get(ACTIONS.transcriptLineDown)).toBeUndefined();
  const event = keydown({ key: "ArrowDown", code: "ArrowDown", altKey: true });
  window.dispatchEvent(event);
  expect(a.el.scrollTop).toBe(0);
  expect(event.defaultPrevented).toBe(false);
});

// useTranscriptScroll's bottom-hold correction refuses to re-pin a transcript
// while the reader is gesturing at it. These chords scroll by writing scrollTop
// straight to the element, dispatched from `window` - a listener on the scroll
// port never sees them (roborev medium 1 on f998582) - so they announce
// themselves through the marker instead.
test("the line and page scroll chords mark a reader gesture when the write moves the port", () => {
  const a = renderPaneKeys("pane_a");
  workspaceStore.setState({ focusedPaneId: "pane_a" });
  clampScrollTop(a.el, 4000);
  a.el.scrollTop = 500;

  document.body.dispatchEvent(keydown({ key: "ArrowUp", altKey: true }));

  expect(a.el.scrollTop).toBe(500 - TRANSCRIPT_LINE_SCROLL_PX);
  expect(a.markGesture).toHaveBeenCalledTimes(1);

  document.body.dispatchEvent(keydown({ key: "ArrowUp", altKey: true, shiftKey: true }));

  expect(a.markGesture).toHaveBeenCalledTimes(2);
});

// roborev medium on fb07321. scrollTop assignments clamp, so a chord aimed past
// an edge writes nothing - and marking a gesture there vetoes a late measurement
// correction landing in the same frame, which (a veto records the reader as away
// from the bottom) disarms the re-pin until they return to the bottom.
test.each([
  ["Alt+ArrowDown", { key: "ArrowDown", altKey: true }],
  ["Alt+Shift+ArrowDown", { key: "ArrowDown", altKey: true, shiftKey: true }],
])("%s at the bottom writes nothing and marks no gesture", (_label, chord) => {
  const a = renderPaneKeys("pane_a");
  workspaceStore.setState({ focusedPaneId: "pane_a" });
  clampScrollTop(a.el, 2000);
  a.el.scrollTop = 4000; // clamped to the true bottom, 2000 - 500
  expect(a.el.scrollTop).toBe(1500);

  document.body.dispatchEvent(keydown(chord));

  expect(a.el.scrollTop).toBe(1500);
  expect(a.markGesture).not.toHaveBeenCalled();
});

test.each([
  ["Alt+ArrowUp", { key: "ArrowUp", altKey: true }],
  ["Alt+Shift+ArrowUp", { key: "ArrowUp", altKey: true, shiftKey: true }],
])("%s at the top writes nothing and marks no gesture", (_label, chord) => {
  const a = renderPaneKeys("pane_a");
  workspaceStore.setState({ focusedPaneId: "pane_a" });
  clampScrollTop(a.el, 2000);
  a.el.scrollTop = 0;

  document.body.dispatchEvent(keydown(chord));

  expect(a.el.scrollTop).toBe(0);
  expect(a.markGesture).not.toHaveBeenCalled();
});

test("a chord an unfocused pane declines marks no gesture on it", () => {
  // The marker must not fire for a pane that did not scroll: a false gesture
  // vetoes a legitimate bottom correction, and that veto is not recoverable
  // until the reader returns to the bottom.
  const a = renderPaneKeys("pane_a");
  const b = renderPaneKeys("pane_b");
  workspaceStore.setState({ focusedPaneId: "pane_a" });

  document.body.dispatchEvent(keydown({ key: "ArrowUp", altKey: true }));

  expect(a.markGesture).toHaveBeenCalledTimes(1);
  expect(b.markGesture).not.toHaveBeenCalled();
});
