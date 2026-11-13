import { act, render, screen } from "@testing-library/react";
import { useRef } from "react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { usePresence } from "./usePresence";

// jsdom implements no Web Animations, so each test decides what
// Element.getAnimations reports for the layer: nothing (reduced motion, or no
// animation API), a finite exit, or a loop.
interface FakeAnimation {
  animation: Animation;
  finish: () => void;
  cancel: () => void;
}

function fakeAnimation(endTime: number, currentTime: number | null = 0): FakeAnimation {
  let resolve: () => void = () => {};
  let reject: (reason: unknown) => void = () => {};
  const finished = new Promise<void>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  // A rejection nobody awaits yet must not surface as an unhandled one.
  finished.catch(() => {});
  const animation = {
    playState: "running",
    currentTime,
    effect: { getComputedTiming: () => ({ endTime }) },
    finished,
  } as unknown as Animation;
  return { animation, finish: () => resolve(), cancel: () => reject(new DOMException("aborted", "AbortError")) };
}

let running: Animation[] = [];
const originalGetAnimations = Object.getOwnPropertyDescriptor(Element.prototype, "getAnimations");

beforeEach(() => {
  running = [];
  Object.defineProperty(Element.prototype, "getAnimations", {
    configurable: true,
    value(this: Element) {
      return this.getAttribute("data-motion") === "closing" ? running : [];
    },
  });
});

afterEach(() => {
  vi.useRealTimers();
  if (originalGetAnimations) Object.defineProperty(Element.prototype, "getAnimations", originalGetAnimations);
  else delete (Element.prototype as { getAnimations?: unknown }).getAnimations;
});

function Layer({ open, onExited }: { open: boolean; onExited?: () => void }) {
  const ref = useRef<HTMLDivElement>(null);
  const { mounted, phase } = usePresence(open, ref, { onExited });
  if (!mounted) return null;
  return <div ref={ref} data-testid="layer" data-motion={phase} />;
}

const layer = () => screen.queryByTestId("layer");

test("a layer with no exit animation leaves in the same commit it closes in", () => {
  const onExited = vi.fn();
  const { rerender } = render(<Layer open onExited={onExited} />);
  expect(layer()?.getAttribute("data-motion")).toBe("open");
  rerender(<Layer open={false} onExited={onExited} />);
  expect(layer()).toBeNull();
  expect(onExited).toHaveBeenCalledTimes(1);
});

test("without the Web Animations API the layer leaves at once", () => {
  delete (Element.prototype as { getAnimations?: unknown }).getAnimations;
  const { rerender } = render(<Layer open />);
  rerender(<Layer open={false} />);
  expect(layer()).toBeNull();
});

test("a running exit keeps the layer, marked closing, until it finishes", async () => {
  const exit = fakeAnimation(80);
  running = [exit.animation];
  const onExited = vi.fn();
  const { rerender } = render(<Layer open onExited={onExited} />);
  rerender(<Layer open={false} onExited={onExited} />);
  expect(layer()?.getAttribute("data-motion")).toBe("closing");
  expect(onExited).not.toHaveBeenCalled();

  await act(async () => exit.finish());
  expect(layer()).toBeNull();
  expect(onExited).toHaveBeenCalledTimes(1);
});

test("a looping animation inside the layer is not waited on", () => {
  running = [fakeAnimation(Number.POSITIVE_INFINITY).animation];
  const { rerender } = render(<Layer open />);
  rerender(<Layer open={false} />);
  expect(layer()).toBeNull();
});

test("when the exit's end never arrives, the exit token bounds the removal", () => {
  vi.useFakeTimers();
  // The animation has run its whole length, but its end is never reported.
  running = [fakeAnimation(80, 80).animation];
  const { rerender } = render(<Layer open />);
  rerender(<Layer open={false} />);
  // The token's own length is not enough to give up on the animation...
  act(() => vi.advanceTimersByTime(80));
  expect(layer()?.getAttribute("data-motion")).toBe("closing");
  // ...but a few frames past it the layer is removed regardless.
  act(() => vi.advanceTimersByTime(100));
  expect(layer()).toBeNull();
});

test("an exit the main thread kept from starting gets its length once more, and no longer", () => {
  vi.useFakeTimers();
  // Still at its first frame when the token's bound passes: a heavy commit held
  // the thread, so the user has not seen the exit yet.
  running = [fakeAnimation(80, 0).animation];
  const { rerender } = render(<Layer open />);
  rerender(<Layer open={false} />);
  act(() => vi.advanceTimersByTime(180));
  expect(layer()?.getAttribute("data-motion")).toBe("closing");
  act(() => vi.advanceTimersByTime(180));
  expect(layer()).toBeNull();
});

test("reopening during the exit cancels the removal and returns the layer to open", async () => {
  vi.useFakeTimers();
  const exit = fakeAnimation(80);
  running = [exit.animation];
  const onExited = vi.fn();
  const { rerender } = render(<Layer open onExited={onExited} />);
  rerender(<Layer open={false} onExited={onExited} />);
  const closing = layer();
  expect(closing?.getAttribute("data-motion")).toBe("closing");

  rerender(<Layer open onExited={onExited} />);
  // The browser cancels the exit when the attribute flips back; neither that
  // rejection nor the fallback timer may count as the layer having left.
  await act(async () => exit.cancel());
  act(() => vi.advanceTimersByTime(1000));
  expect(layer()).toBe(closing);
  expect(layer()?.getAttribute("data-motion")).toBe("open");
  expect(onExited).not.toHaveBeenCalled();
});
