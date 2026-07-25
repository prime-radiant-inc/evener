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

// The panel animates in from scale(0.96) (popoverFadeScale), and the measuring
// layout effect runs in the same commit that mounts it - the frame where that
// `from` keyframe is still in force. getBoundingClientRect() reports the box
// AFTER transforms, so measuring the panel that way reads 96% of the settled
// size and places a panel that is about to be 4% wider. Measured live against
// a real hub at a 390px viewport: a 376px directory panel rected as 361px,
// clamped to left 21.03, and settled at right 397.03 - seven pixels past the
// viewport's own edge, with the same panel landing at left 8 / right 384 once
// the animation was disabled. offsetWidth/offsetHeight report the untransformed
// layout box, which is the size the panel will actually settle at.
//
// jsdom performs no layout, so both metrics have to be supplied here; the point
// of the test is which of the two the component reads, and the two are given
// deliberately different values so only one placement can result.
test("places the panel from its untransformed layout box, not its mid-animation rect", () => {
  const SETTLED_WIDTH = 376;
  const ANIMATING_WIDTH = Math.round(SETTLED_WIDTH * 0.96); // 361, scale(0.96)
  // A trigger hard against the right edge, so the placement flips and the
  // panel's own width lands in the arithmetic (a left-aligned placement would
  // read the same either way and prove nothing).
  const TRIGGER = { left: 900, right: 940, top: 100, bottom: 120 };

  const isPanel = (el: Element) => (el as HTMLElement).dataset?.testid === "panel";
  const originalRect = Element.prototype.getBoundingClientRect;
  const originalOffsetWidth = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "offsetWidth");
  const originalOffsetHeight = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "offsetHeight");

  Element.prototype.getBoundingClientRect = function stubbedRect(this: Element) {
    if (isPanel(this)) return { ...TRIGGER, width: ANIMATING_WIDTH, height: 100 } as DOMRect;
    if (this.tagName === "SPAN") return { ...TRIGGER, width: 40, height: 20 } as DOMRect;
    return originalRect.call(this);
  };
  Object.defineProperty(HTMLElement.prototype, "offsetWidth", {
    configurable: true,
    get(this: HTMLElement) {
      return isPanel(this) ? SETTLED_WIDTH : 0;
    },
  });
  Object.defineProperty(HTMLElement.prototype, "offsetHeight", {
    configurable: true,
    get(this: HTMLElement) {
      return isPanel(this) ? 100 : 0;
    },
  });

  try {
    render(
      <Popover open onClose={() => {}} trigger={<button type="button">open</button>} data-testid="panel">
        <div>panel body</div>
      </Popover>,
    );
    // Right-aligned to the trigger: 940 - 376 = 564. Measuring the animating
    // 361px box instead would have produced 940 - 361 = 579, and the panel
    // would then grow 15px past where it was placed.
    expect(screen.getByTestId("panel").style.left).toBe(`${TRIGGER.right - SETTLED_WIDTH}px`);
  } finally {
    Element.prototype.getBoundingClientRect = originalRect;
    restoreOwnProperty(HTMLElement.prototype, "offsetWidth", originalOffsetWidth);
    restoreOwnProperty(HTMLElement.prototype, "offsetHeight", originalOffsetHeight);
  }
});

// Puts back exactly what was there, including "nothing at all" - a stub left
// installed on HTMLElement.prototype would follow every later test file in the
// same worker.
function restoreOwnProperty(target: object, key: string, descriptor: PropertyDescriptor | undefined) {
  if (descriptor) Object.defineProperty(target, key, descriptor);
  else delete (target as Record<string, unknown>)[key];
}
