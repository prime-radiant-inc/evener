import { afterEach, expect, test, vi } from "vitest";
import { acquirePointerActivity } from "./pointerActivity";
import { POINTER_CLICK_WINDOW_MS } from "./useFloatingLabel";

afterEach(() => {
  vi.useRealTimers();
});

// A second, real Document in the same jsdom realm: a fresh event target that
// shares nothing with the top-level document but dispatches real PointerEvents.
function secondDocument(): Document {
  return document.implementation.createHTMLDocument("second");
}

function dispatchPointer(owner: Document, type: string, pointerId: number): void {
  owner.dispatchEvent(new PointerEvent(type, { pointerId, bubbles: true }));
}

// Two clocks: the timer queue a real test would use, and the Date clock the
// compatibility window is measured against. jsdom needs both advanced together.
function startClock(): { advance: (ms: number) => void } {
  vi.useFakeTimers();
  let current = 1_000_000;
  vi.setSystemTime(current);
  return {
    advance(ms: number) {
      current += ms;
      vi.advanceTimersByTime(ms);
      vi.setSystemTime(current);
    },
  };
}

test("keeps pointer state isolated between documents", () => {
  const first = document;
  const second = secondDocument();
  const firstHandle = acquirePointerActivity(first);
  const secondHandle = acquirePointerActivity(second);
  try {
    expect(firstHandle.isPointerFocus(POINTER_CLICK_WINDOW_MS)).toBe(false);
    expect(secondHandle.isPointerFocus(POINTER_CLICK_WINDOW_MS)).toBe(false);

    dispatchPointer(first, "pointerdown", 1);
    expect(firstHandle.isPointerFocus(POINTER_CLICK_WINDOW_MS)).toBe(true);
    // The press on the first document left the second's state untouched.
    expect(secondHandle.isPointerFocus(POINTER_CLICK_WINDOW_MS)).toBe(false);

    dispatchPointer(second, "pointerdown", 2);
    expect(secondHandle.isPointerFocus(POINTER_CLICK_WINDOW_MS)).toBe(true);

    dispatchPointer(second, "pointerup", 2);
    dispatchPointer(first, "pointerup", 1);
  } finally {
    firstHandle.release();
    secondHandle.release();
  }
});

test("tracks each pointer separately, so releasing one leaves the other pressed", () => {
  const clock = startClock();
  const owner = secondDocument();
  const handle = acquirePointerActivity(owner);
  try {
    dispatchPointer(owner, "pointerdown", 11);
    dispatchPointer(owner, "pointerdown", 22);

    // Lift one finger while the other stays down: focus is still the pointer's,
    // however long the window has passed.
    dispatchPointer(owner, "pointerup", 11);
    clock.advance(POINTER_CLICK_WINDOW_MS * 4);
    expect(handle.isPointerFocus(POINTER_CLICK_WINDOW_MS)).toBe(true);

    dispatchPointer(owner, "pointerup", 22);
    expect(handle.isPointerFocus(POINTER_CLICK_WINDOW_MS)).toBe(true);
    clock.advance(POINTER_CLICK_WINDOW_MS + 1);
    expect(handle.isPointerFocus(POINTER_CLICK_WINDOW_MS)).toBe(false);
  } finally {
    handle.release();
  }
});

test("a cancelled pointer releases its own id just like a lifted one", () => {
  const clock = startClock();
  const owner = secondDocument();
  const handle = acquirePointerActivity(owner);
  try {
    dispatchPointer(owner, "pointerdown", 7);
    dispatchPointer(owner, "pointercancel", 7);
    // Cancellation sets the same compatibility timestamp a lift would.
    expect(handle.isPointerFocus(POINTER_CLICK_WINDOW_MS)).toBe(true);
    clock.advance(POINTER_CLICK_WINDOW_MS + 1);
    expect(handle.isPointerFocus(POINTER_CLICK_WINDOW_MS)).toBe(false);
  } finally {
    handle.release();
  }
});

test("holds the compatibility window to exactly 350ms", () => {
  const clock = startClock();
  const owner = secondDocument();
  const handle = acquirePointerActivity(owner);
  try {
    dispatchPointer(owner, "pointerup", 5);
    clock.advance(POINTER_CLICK_WINDOW_MS);
    expect(handle.isPointerFocus(POINTER_CLICK_WINDOW_MS)).toBe(true);
    clock.advance(1);
    expect(handle.isPointerFocus(POINTER_CLICK_WINDOW_MS)).toBe(false);
  } finally {
    handle.release();
  }
});

test("releases the last reference once and only once", () => {
  const owner = secondDocument();
  const first = acquirePointerActivity(owner);
  const second = acquirePointerActivity(owner);
  // The owner outlives the first handle while a second holds a reference.
  first.release();
  dispatchPointer(owner, "pointerdown", 3);
  expect(second.isPointerFocus(POINTER_CLICK_WINDOW_MS)).toBe(true);
  dispatchPointer(owner, "pointerup", 3);

  second.release();
  // A repeated release is a no-op, not a second teardown.
  second.release();
  first.release();

  // The listeners are gone, so a press on the now-unowned document is inert.
  dispatchPointer(owner, "pointerdown", 4);
  const reacquired = acquirePointerActivity(owner);
  expect(reacquired.isPointerFocus(POINTER_CLICK_WINDOW_MS)).toBe(false);
  reacquired.release();
});
