import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { Popover } from "./index";

afterEach(() => cleanup());

// The §3.4 guarantee: the panel is an out-of-flow overlay portaled to
// document.body, so opening it never pushes page content down (never reflows).

test("the panel is not in the document while closed", () => {
  render(
    <Popover open={false} onClose={() => {}} trigger={<button type="button">open</button>} data-testid="panel">
      <div>panel body</div>
    </Popover>,
  );
  expect(screen.queryByTestId("panel")).toBeNull();
});

test("when open, the panel is portaled to document.body, not nested under the trigger wrapper", () => {
  render(
    <Popover open onClose={() => {}} trigger={<button type="button">open</button>} data-testid="panel">
      <div>panel body</div>
    </Popover>,
  );
  const panel = screen.getByTestId("panel");
  expect(panel).toBeTruthy();

  // The trigger button lives inside Popover's in-flow wrapper <span>; the
  // panel must NOT share that wrapper (that would reflow). Walk the panel's
  // ancestry: it reaches document.body without passing through the trigger's
  // own wrapper span.
  const triggerButton = screen.getByRole("button", { name: "open" });
  const triggerWrapper = triggerButton.parentElement;
  expect(triggerWrapper).not.toBeNull();

  let ancestor: HTMLElement | null = panel.parentElement;
  let reachedBody = false;
  while (ancestor) {
    expect(ancestor).not.toBe(triggerWrapper);
    if (ancestor === document.body) {
      reachedBody = true;
      break;
    }
    ancestor = ancestor.parentElement;
  }
  expect(reachedBody).toBe(true);
});

// closeOnScroll: the model picker's own list scrolls, and a page scroll behind
// the panel must not dismiss a picker mid-interaction - so the scroll/resize
// close pair is an opt-out. Menu-shaped consumers keep the default.
test("by default a window scroll closes the panel", () => {
  const onClose = vi.fn();
  render(
    <Popover open onClose={onClose} trigger={<button type="button">open</button>} data-testid="panel">
      <div>panel body</div>
    </Popover>,
  );

  window.dispatchEvent(new Event("scroll"));

  expect(onClose).toHaveBeenCalled();
});

test("closeOnScroll={false} keeps the panel open through a window scroll and a resize", () => {
  const onClose = vi.fn();
  render(
    <Popover
      open
      onClose={onClose}
      closeOnScroll={false}
      trigger={<button type="button">open</button>}
      data-testid="panel"
    >
      <div>panel body</div>
    </Popover>,
  );

  window.dispatchEvent(new Event("scroll"));
  window.dispatchEvent(new Event("resize"));

  expect(onClose).not.toHaveBeenCalled();
  expect(screen.getByTestId("panel")).toBeTruthy();
});

// A panel whose content arrives asynchronously (the model picker's catalog
// fetch: a narrow loading skeleton, then a full-width list) changes size after
// the open-time measure. Placement must follow, or a flipped panel computed
// off the small size hangs off the viewport edge - observed live at 390px: a
// 98px skeleton right-aligned to its trigger, grown to 368px, 6px off-screen.
test("re-measures placement when the panel's own size changes after opening", async () => {
  // jsdom ships no ResizeObserver; a manual stub lets the test drive the
  // observation callback the same way a real size change would.
  const callbacks: ResizeObserverCallback[] = [];
  class StubResizeObserver {
    constructor(cb: ResizeObserverCallback) {
      callbacks.push(cb);
    }
    observe() {}
    unobserve() {}
    disconnect() {}
  }
  const original = globalThis.ResizeObserver;
  globalThis.ResizeObserver = StubResizeObserver as unknown as typeof ResizeObserver;
  // Every element reports the same rect in jsdom (all zeros), so assert on the
  // re-measure happening at all: a second computePopoverPosition pass runs and
  // the panel keeps a concrete inline placement.
  try {
    render(
      <Popover open onClose={() => {}} trigger={<button type="button">open</button>} data-testid="panel">
        <div>panel body</div>
      </Popover>,
    );
    const panel = screen.getByTestId("panel");
    expect(callbacks).toHaveLength(1);

    const grow = callbacks[0];
    if (!grow) throw new Error("expected Popover to observe its panel for size changes");
    grow([], {} as ResizeObserver);

    expect(panel.style.top).not.toBe("");
    expect(panel.style.left).not.toBe("");
  } finally {
    globalThis.ResizeObserver = original;
  }
});
