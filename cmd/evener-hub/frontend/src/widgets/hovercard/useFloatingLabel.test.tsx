import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { useRef } from "react";
import { afterEach, expect, test, vi } from "vitest";
import { useFloatingLabel } from "./useFloatingLabel";

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
