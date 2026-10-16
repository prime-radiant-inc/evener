import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { StrictMode, useRef } from "react";
import { afterEach, expect, test, vi } from "vitest";
import { POINTER_CLICK_WINDOW_MS, SHOW_DELAY_MS, useFloatingLabel } from "./useFloatingLabel";

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

function advance(ms: number) {
  act(() => {
    vi.advanceTimersByTime(ms);
  });
}

function Harness({ measure = () => {} }: { measure?: () => void }) {
  const observe = useRef<HTMLSpanElement>(null);
  const { visible, wrapperRef, triggerProps } = useFloatingLabel({ measure, observe });

  return (
    <span ref={wrapperRef}>
      <button type="button" {...triggerProps}>
        Trigger
      </button>
      {visible && (
        <span ref={observe} data-testid="observed-label">
          Label
        </span>
      )}
    </span>
  );
}

function installResizeObserver() {
  const observe = vi.fn();
  const disconnect = vi.fn();
  let callback: ResizeObserverCallback | undefined;
  let instance: StubResizeObserver | undefined;

  class StubResizeObserver {
    constructor(nextCallback: ResizeObserverCallback) {
      callback = nextCallback;
      instance = this;
    }

    observe = observe;
    unobserve = vi.fn();
    disconnect = disconnect;
  }

  vi.stubGlobal("ResizeObserver", StubResizeObserver);

  return {
    observe,
    disconnect,
    fire() {
      if (!callback || !instance) throw new Error("ResizeObserver was not constructed");
      callback([], instance as unknown as ResizeObserver);
    },
  };
}

test("shows after 300ms and hides immediately", () => {
  vi.useFakeTimers();
  render(<Harness />);
  const trigger = screen.getByRole("button", { name: "Trigger" });

  fireEvent.mouseEnter(trigger);
  advance(299);
  expect(screen.queryByTestId("observed-label")).toBeNull();

  advance(1);
  expect(screen.getByTestId("observed-label")).toBeTruthy();

  fireEvent.mouseLeave(trigger);
  expect(screen.queryByTestId("observed-label")).toBeNull();
});

test("focus uses the same delay and blur hides immediately", () => {
  vi.useFakeTimers();
  render(<Harness />);
  const trigger = screen.getByRole("button", { name: "Trigger" });

  fireEvent.focus(trigger);
  advance(299);
  expect(screen.queryByTestId("observed-label")).toBeNull();

  advance(1);
  expect(screen.getByTestId("observed-label")).toBeTruthy();

  fireEvent.blur(trigger);
  expect(screen.queryByTestId("observed-label")).toBeNull();
});

test("blur cancels a pending focus show", () => {
  vi.useFakeTimers();
  render(<Harness />);
  const trigger = screen.getByRole("button", { name: "Trigger" });

  fireEvent.focus(trigger);
  advance(150);
  fireEvent.blur(trigger);
  advance(300);

  expect(screen.queryByTestId("observed-label")).toBeNull();
});

test("scroll cancels a pending show", () => {
  vi.useFakeTimers();
  render(<Harness />);
  const trigger = screen.getByRole("button", { name: "Trigger" });

  fireEvent.mouseEnter(trigger);
  advance(150);
  act(() => window.dispatchEvent(new Event("scroll")));
  advance(300);

  expect(screen.queryByTestId("observed-label")).toBeNull();
});

test("viewport resize cancels a pending show", () => {
  vi.useFakeTimers();
  render(<Harness />);
  const trigger = screen.getByRole("button", { name: "Trigger" });

  fireEvent.mouseEnter(trigger);
  advance(150);
  act(() => window.dispatchEvent(new Event("resize")));
  advance(300);

  expect(screen.queryByTestId("observed-label")).toBeNull();
});

test("scroll dismisses a visible label and cancels a re-armed show", () => {
  vi.useFakeTimers();
  render(<Harness />);
  const trigger = screen.getByRole("button", { name: "Trigger" });

  fireEvent.mouseEnter(trigger);
  advance(300);
  expect(screen.getByTestId("observed-label")).toBeTruthy();

  fireEvent.mouseEnter(trigger);
  act(() => window.dispatchEvent(new Event("scroll")));
  expect(screen.queryByTestId("observed-label")).toBeNull();

  advance(300);
  expect(screen.queryByTestId("observed-label")).toBeNull();
});

test("viewport resize dismisses a visible label", () => {
  vi.useFakeTimers();
  render(<Harness />);
  const trigger = screen.getByRole("button", { name: "Trigger" });

  fireEvent.mouseEnter(trigger);
  advance(300);
  expect(screen.getByTestId("observed-label")).toBeTruthy();

  act(() => window.dispatchEvent(new Event("resize")));
  expect(screen.queryByTestId("observed-label")).toBeNull();
});

test("unmount clears a pending show timer", () => {
  vi.useFakeTimers();
  const { unmount } = render(<Harness />);

  fireEvent.mouseEnter(screen.getByRole("button", { name: "Trigger" }));
  expect(vi.getTimerCount()).toBe(1);

  unmount();
  expect(vi.getTimerCount()).toBe(0);
});

test("observes the shown label, re-measures box changes, and disconnects on cleanup", () => {
  vi.useFakeTimers();
  const observer = installResizeObserver();
  const measure = vi.fn();
  const { unmount } = render(<Harness measure={measure} />);

  fireEvent.mouseEnter(screen.getByRole("button", { name: "Trigger" }));
  advance(300);
  const observedLabel = screen.getByTestId("observed-label");
  expect(observer.observe).toHaveBeenCalledWith(observedLabel);
  expect(measure).not.toHaveBeenCalled();

  act(() => observer.fire());
  expect(measure).toHaveBeenCalledTimes(1);

  unmount();
  expect(observer.disconnect).toHaveBeenCalledTimes(1);
});

// Counts live callback identities registered on a Document, keyed by event type
// and capture flag. The native methods still run, so the listeners counted are
// the ones actually installed. Two consumers with distinct callbacks count
// twice; one shared callback counts once. The pre-existing listeners present
// before the render are a baseline, subtracted from every reading.
function tallyDocumentListeners(target: Document) {
  const proto = EventTarget.prototype;
  const originalAdd = Object.getOwnPropertyDescriptor(proto, "addEventListener");
  const originalRemove = Object.getOwnPropertyDescriptor(proto, "removeEventListener");
  if (!originalAdd || !originalRemove) throw new Error("EventTarget.prototype has no own listener methods");
  const nativeAdd = originalAdd.value as EventTarget["addEventListener"];
  const nativeRemove = originalRemove.value as EventTarget["removeEventListener"];

  const live = new Map<string, Set<EventListenerOrEventListenerObject>>();
  const keyOf = (type: string, capture: boolean) => `${type}|${capture}`;
  const captureOf = (options?: boolean | AddEventListenerOptions) =>
    typeof options === "boolean" ? options : options?.capture === true;

  function trackedAdd(
    this: EventTarget,
    type: string,
    listener: EventListenerOrEventListenerObject | null,
    options?: boolean | AddEventListenerOptions,
  ) {
    if (this === target && listener !== null) {
      const key = keyOf(type, captureOf(options));
      let identities = live.get(key);
      if (!identities) {
        identities = new Set();
        live.set(key, identities);
      }
      identities.add(listener);
    }
    return nativeAdd.call(this, type, listener, options);
  }

  function trackedRemove(
    this: EventTarget,
    type: string,
    listener: EventListenerOrEventListenerObject | null,
    options?: boolean | AddEventListenerOptions,
  ) {
    if (this === target && listener !== null) {
      live.get(keyOf(type, captureOf(options)))?.delete(listener);
    }
    return nativeRemove.call(this, type, listener, options);
  }

  Object.defineProperty(proto, "addEventListener", { configurable: true, writable: true, value: trackedAdd });
  Object.defineProperty(proto, "removeEventListener", { configurable: true, writable: true, value: trackedRemove });

  const baseline = new Map<string, number>();
  for (const [key, identities] of live) baseline.set(key, identities.size);

  return {
    net(type: string, capture: boolean) {
      return (live.get(keyOf(type, capture))?.size ?? 0) - (baseline.get(keyOf(type, capture)) ?? 0);
    },
    restore() {
      Object.defineProperty(proto, "addEventListener", originalAdd);
      Object.defineProperty(proto, "removeEventListener", originalRemove);
    },
  };
}

const CONSUMER_IDS = Array.from({ length: 100 }, (_, index) => `consumer-${index}`);

function ConsumerSet({ count }: { count: number }) {
  const ids = CONSUMER_IDS.slice(0, count);
  return (
    <div>
      {ids.map((id) => (
        <Harness key={id} />
      ))}
    </div>
  );
}

function installTogglingMatchMedia(initialHoverless: boolean) {
  let hoverless = initialHoverless;
  vi.stubGlobal("matchMedia", (query: string) => ({
    matches: hoverless && query === "(hover: none)",
    media: query,
    onchange: null,
    addListener: () => {},
    removeListener: () => {},
    addEventListener: () => {},
    removeEventListener: () => {},
    dispatchEvent: () => false,
  }));
  return {
    set(next: boolean) {
      hoverless = next;
    },
  };
}

test("shares pointer tracking across any number of mounted consumers", () => {
  const tally = tallyDocumentListeners(document);
  try {
    for (const count of [1, 10, 100]) {
      const view = render(<ConsumerSet count={count} />);
      expect(tally.net("pointerdown", true)).toBe(1);
      expect(tally.net("pointerup", true)).toBe(1);
      expect(tally.net("pointercancel", true)).toBe(1);

      view.unmount();
      expect(tally.net("pointerdown", true)).toBe(0);
      expect(tally.net("pointerup", true)).toBe(0);
      expect(tally.net("pointercancel", true)).toBe(0);
    }
  } finally {
    tally.restore();
  }
});

test("keeps one listener through partial unmount and starts clean on remount", () => {
  vi.useFakeTimers();
  const tally = tallyDocumentListeners(document);
  try {
    const view = render(<ConsumerSet count={3} />);
    expect(tally.net("pointerdown", true)).toBe(1);

    view.rerender(<ConsumerSet count={2} />);
    expect(tally.net("pointerdown", true)).toBe(1);
    view.rerender(<ConsumerSet count={1} />);
    expect(tally.net("pointerdown", true)).toBe(1);
    view.rerender(<ConsumerSet count={0} />);
    expect(tally.net("pointerdown", true)).toBe(0);
    expect(tally.net("pointerup", true)).toBe(0);
    expect(tally.net("pointercancel", true)).toBe(0);

    // Remounting starts a fresh owner, and keyboard focus still reveals.
    view.rerender(<ConsumerSet count={1} />);
    expect(tally.net("pointerdown", true)).toBe(1);
    fireEvent.focus(screen.getByRole("button", { name: "Trigger" }));
    advance(SHOW_DELAY_MS);
    expect(screen.getByTestId("observed-label")).toBeTruthy();
  } finally {
    tally.restore();
  }
});

test("a StrictMode remount settles on one shared owner", () => {
  const tally = tallyDocumentListeners(document);
  try {
    const view = render(
      <StrictMode>
        <ConsumerSet count={2} />
      </StrictMode>,
    );
    expect(tally.net("pointerdown", true)).toBe(1);
    expect(tally.net("pointerup", true)).toBe(1);
    expect(tally.net("pointercancel", true)).toBe(1);

    view.unmount();
    expect(tally.net("pointerdown", true)).toBe(0);
    expect(tally.net("pointerup", true)).toBe(0);
    expect(tally.net("pointercancel", true)).toBe(0);
  } finally {
    tally.restore();
  }
});

test("pointer-focus suppression follows the current hoverless mode while keyboard focus still reveals", () => {
  vi.useFakeTimers();
  vi.setSystemTime(1_000_000);
  const media = installTogglingMatchMedia(true);
  render(<Harness />);
  const trigger = screen.getByRole("button", { name: "Trigger" });

  // Hoverless: the focus a press takes reveals nothing.
  fireEvent.pointerDown(trigger, { pointerId: 1, button: 0 });
  fireEvent.focus(trigger);
  advance(SHOW_DELAY_MS);
  expect(screen.queryByTestId("observed-label")).toBeNull();

  // Hoverless keyboard focus, with no pointer in flight and none recent, reveals.
  fireEvent.pointerUp(trigger, { pointerId: 1 });
  advance(POINTER_CLICK_WINDOW_MS + 1);
  fireEvent.focus(trigger);
  advance(SHOW_DELAY_MS);
  expect(screen.getByTestId("observed-label")).toBeTruthy();

  // Hover-capable: the same press-then-focus reveals.
  media.set(false);
  fireEvent.blur(trigger);
  fireEvent.pointerDown(trigger, { pointerId: 2, button: 0 });
  fireEvent.focus(trigger);
  advance(SHOW_DELAY_MS);
  expect(screen.getByTestId("observed-label")).toBeTruthy();
});
