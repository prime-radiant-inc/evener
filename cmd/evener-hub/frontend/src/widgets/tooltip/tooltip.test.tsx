import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import { Button } from "../button";
import { Tooltip } from "./index";

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

// The show-delay timer fires outside any React-tracked event, so advancing
// it must be wrapped in act() or the resulting setVisible(true) update
// isn't flushed before the next assertion reads the DOM.
function advance(ms: number) {
  act(() => {
    vi.advanceTimersByTime(ms);
  });
}

test("renders its trigger children", () => {
  render(
    <Tooltip label="Save your changes">
      <button type="button">Save</button>
    </Tooltip>,
  );
  expect(screen.getByRole("button", { name: "Save" })).toBeTruthy();
});

test("the tooltip is not shown initially", () => {
  render(
    <Tooltip label="Save your changes">
      <button type="button">Save</button>
    </Tooltip>,
  );
  expect(screen.queryByRole("tooltip")).toBeNull();
});

test("hovering shows the tooltip only after a 300ms delay", () => {
  vi.useFakeTimers();
  render(
    <Tooltip label="Save your changes">
      <button type="button">Save</button>
    </Tooltip>,
  );
  fireEvent.mouseEnter(screen.getByRole("button", { name: "Save" }));
  expect(screen.queryByRole("tooltip")).toBeNull();

  advance(299);
  expect(screen.queryByRole("tooltip")).toBeNull();

  advance(1);
  expect(screen.getByRole("tooltip").textContent).toBe("Save your changes");
});

test("moving the mouse away before the delay elapses cancels the show", () => {
  vi.useFakeTimers();
  render(
    <Tooltip label="Save your changes">
      <button type="button">Save</button>
    </Tooltip>,
  );
  const trigger = screen.getByRole("button", { name: "Save" });
  fireEvent.mouseEnter(trigger);
  advance(150);
  fireEvent.mouseLeave(trigger);
  advance(300);
  expect(screen.queryByRole("tooltip")).toBeNull();
});

test("moving the mouse away after the tooltip is showing hides it immediately", () => {
  vi.useFakeTimers();
  render(
    <Tooltip label="Save your changes">
      <button type="button">Save</button>
    </Tooltip>,
  );
  const trigger = screen.getByRole("button", { name: "Save" });
  fireEvent.mouseEnter(trigger);
  advance(300);
  expect(screen.getByRole("tooltip")).toBeTruthy();

  fireEvent.mouseLeave(trigger);
  expect(screen.queryByRole("tooltip")).toBeNull();
});

test("keyboard focus shows the tooltip after the same delay, blur hides it immediately", () => {
  vi.useFakeTimers();
  render(
    <Tooltip label="Save your changes">
      <button type="button">Save</button>
    </Tooltip>,
  );
  const trigger = screen.getByRole("button", { name: "Save" });
  fireEvent.focus(trigger);
  expect(screen.queryByRole("tooltip")).toBeNull();
  advance(300);
  expect(screen.getByRole("tooltip")).toBeTruthy();

  fireEvent.blur(trigger);
  expect(screen.queryByRole("tooltip")).toBeNull();
});

test("associates a single native-element trigger with the tooltip via aria-describedby while visible", () => {
  vi.useFakeTimers();
  render(
    <Tooltip label="Save your changes">
      <button type="button">Save</button>
    </Tooltip>,
  );
  const trigger = screen.getByRole("button", { name: "Save" });
  expect(trigger.getAttribute("aria-describedby")).toBeNull();

  fireEvent.focus(trigger);
  advance(300);
  const tooltip = screen.getByRole("tooltip");
  expect(trigger.getAttribute("aria-describedby")).toBe(tooltip.id);

  fireEvent.blur(trigger);
  expect(trigger.getAttribute("aria-describedby")).toBeNull();
});

// --- fix-wave: integration test with the real Button widget (Important) ---
// Before Button forwarded its ref and spread rest props onto its own
// <button> (see button.test.tsx), the cloneElement aria-describedby prop
// below still reached Button's props object but had nowhere to go once
// there - Button's render body only ever used its fixed, explicit prop
// list, so the description was silently dropped. This exercises the real
// cross-widget composition, not a synthetic stand-in, so a regression in
// either widget breaks it.

test("wrapping the real Button widget: focusing shows the tooltip and associates it via aria-describedby", () => {
  vi.useFakeTimers();
  render(
    <Tooltip label="Save your changes">
      <Button>Save</Button>
    </Tooltip>,
  );
  const trigger = screen.getByRole("button", { name: "Save" });
  expect(trigger.getAttribute("aria-describedby")).toBeNull();

  fireEvent.focus(trigger);
  advance(300);
  const tooltip = screen.getByRole("tooltip");
  expect(trigger.getAttribute("aria-describedby")).toBe(tooltip.id);

  fireEvent.blur(trigger);
  expect(trigger.getAttribute("aria-describedby")).toBeNull();
});

test("wrapping the real Button widget: hovering shows the tooltip and associates it via aria-describedby", () => {
  vi.useFakeTimers();
  render(
    <Tooltip label="Save your changes">
      <Button>Save</Button>
    </Tooltip>,
  );
  const trigger = screen.getByRole("button", { name: "Save" });

  fireEvent.mouseEnter(trigger);
  advance(300);
  const tooltip = screen.getByRole("tooltip");
  expect(trigger.getAttribute("aria-describedby")).toBe(tooltip.id);

  fireEvent.mouseLeave(trigger);
  expect(trigger.getAttribute("aria-describedby")).toBeNull();
});

test("degrades gracefully (no crash, no aria wiring) when children is not a single element", () => {
  vi.useFakeTimers();
  render(
    <Tooltip label="Info">
      <span>Text</span>
      <span>More text</span>
    </Tooltip>,
  );
  fireEvent.mouseEnter(screen.getByText("Text").parentElement!);
  advance(300);
  expect(screen.getByRole("tooltip")).toBeTruthy();
});

test("never traps focus: Tab moves through and past the trigger normally", async () => {
  const user = userEvent.setup();
  render(
    <div>
      <button type="button">Before</button>
      <Tooltip label="Save your changes">
        <button type="button">Save</button>
      </Tooltip>
      <button type="button">After</button>
    </div>,
  );
  screen.getByRole("button", { name: "Before" }).focus();
  await user.tab();
  expect(document.activeElement).toBe(screen.getByRole("button", { name: "Save" }));
  await user.tab();
  expect(document.activeElement).toBe(screen.getByRole("button", { name: "After" }));
});

test("is hidden from touch (no hover capability) via CSS, using only tokens", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "tooltip.module.css"), "utf8");
  expect(css).toMatch(/@media \(hover: none\)/);
});

// A bubble with no intrinsic width request shrink-to-fits into the space its
// own offsets leave inside the containing block, and `left: 50%` leaves only
// half the TRIGGER's width - so on a small trigger (a 24px icon button, a 48px
// "Send") the label collapsed to a column one word per line and max-width was
// never reached at all. Reproduced live in Chrome against a real hub: the
// composer's "Queue until the agent stops · ⌘+Enter" rendered as six stacked
// lines and spilled over the queue strip above it. Checked against the
// stylesheet because jsdom performs no layout.
test("the bubble asks for its label's natural single-line width, so max-width is what actually caps it", () => {
  const css = readFileSync(join(dirname(fileURLToPath(import.meta.url)), "tooltip.module.css"), "utf8");
  const rule = /\.bubble\s*\{([^}]*)\}/.exec(css)?.[1] ?? "";
  expect(rule).toMatch(/width:\s*max-content/);
  expect(rule).toMatch(/max-width:\s*240px/);
});

// --- collision handling -------------------------------------------------
// Laid out inside the trigger's own subtree, the bubble is cut by any ancestor
// with overflow: hidden. The panes are exactly that: measured live at 1440px,
// the composer's Send tooltip had a right edge of 1439.27 - two pixels of
// viewport margin, so it read as nearly safe - while the pane clipping it ended
// at x = 1424, leaving 15.27px of the bubble unpainted. It rendered on screen as
// "Send now · ⌘+Ente". No amount of shifting against the VIEWPORT fixes that;
// only leaving the clipping subtree does, which is what the portal is for.

test("the bubble is portaled to document.body, not nested inside the trigger's wrapper", () => {
  vi.useFakeTimers();
  render(
    <Tooltip label="Save your changes">
      <button type="button">Save</button>
    </Tooltip>,
  );
  const trigger = screen.getByRole("button", { name: "Save" });
  fireEvent.mouseEnter(trigger);
  advance(300);

  const wrapper = trigger.parentElement;
  expect(wrapper).not.toBeNull();
  const bubble = screen.getByRole("tooltip");
  for (let el = bubble.parentElement; el; el = el.parentElement) {
    expect(el).not.toBe(wrapper);
  }
  expect(bubble.parentElement).toBe(document.body);
});

// A fixed-position bubble is placed once per show, so a scroll behind it would
// leave it hanging in space, visually detached from the control it describes.
// Same trade-off Popover documents for its own panel; cheaper here, because
// re-hovering brings the tooltip straight back.
test("a scroll anywhere hides the bubble rather than leaving it detached", () => {
  vi.useFakeTimers();
  render(
    <Tooltip label="Save your changes">
      <button type="button">Save</button>
    </Tooltip>,
  );
  fireEvent.mouseEnter(screen.getByRole("button", { name: "Save" }));
  advance(300);
  expect(screen.getByRole("tooltip")).toBeTruthy();

  act(() => {
    window.dispatchEvent(new Event("scroll"));
  });
  expect(screen.queryByRole("tooltip")).toBeNull();
});

test("a viewport resize hides the bubble, whose placement it would invalidate", () => {
  vi.useFakeTimers();
  render(
    <Tooltip label="Save your changes">
      <button type="button">Save</button>
    </Tooltip>,
  );
  fireEvent.mouseEnter(screen.getByRole("button", { name: "Save" }));
  advance(300);
  expect(screen.getByRole("tooltip")).toBeTruthy();

  act(() => {
    window.dispatchEvent(new Event("resize"));
  });
  expect(screen.queryByRole("tooltip")).toBeNull();
});

test("the portaled bubble still carries the aria-describedby association to its trigger", () => {
  vi.useFakeTimers();
  render(
    <Tooltip label="Save your changes">
      <button type="button">Save</button>
    </Tooltip>,
  );
  const trigger = screen.getByRole("button", { name: "Save" });
  fireEvent.focus(trigger);
  advance(300);
  const bubble = screen.getByRole("tooltip");
  // The id association is what survives the move out of the DOM subtree - it
  // is the only thing tying the two together now.
  expect(bubble.parentElement).toBe(document.body);
  expect(trigger.getAttribute("aria-describedby")).toBe(bubble.id);
});

test("the bubble is positioned as fixed, which is what a portaled placement requires", () => {
  const css = readFileSync(join(dirname(fileURLToPath(import.meta.url)), "tooltip.module.css"), "utf8");
  const rule = /\.bubble\s*\{([^}]*)\}/.exec(css)?.[1] ?? "";
  expect(rule).toMatch(/position:\s*fixed/);
  // The old centring pair would fight the computed left outright.
  expect(rule).not.toMatch(/left:\s*50%/);
  expect(rule).not.toMatch(/transform:/);
});

// The component-level half of the collision fix: the pure math is covered in
// computePosition.test.ts, but only this proves the component feeds it the
// trigger's real rect and the bubble's real size, and writes the answer out.
// jsdom performs no layout, so both have to be supplied.
test("shifts a right-edge bubble inside the viewport rather than writing the centred placement", () => {
  vi.useFakeTimers();
  const BUBBLE_WIDTH = 200;
  const BUBBLE_HEIGHT = 28;
  // jsdom's window is 1024x768; this trigger's centre is at 970, so a centred
  // bubble would start at 870 and end at 1070 - 46px past the viewport.
  const TRIGGER = { left: 950, right: 990, top: 300, bottom: 320, width: 40, height: 20 };

  const isBubble = (el: Element) => el.getAttribute("role") === "tooltip";
  const originalRect = Element.prototype.getBoundingClientRect;
  const originalOffsetWidth = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "offsetWidth");
  const originalOffsetHeight = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "offsetHeight");

  Element.prototype.getBoundingClientRect = function stubbedRect(this: Element) {
    // The wrapper span is what the component measures as its anchor.
    if (this.tagName === "SPAN" && !isBubble(this)) return TRIGGER as DOMRect;
    return originalRect.call(this);
  };
  Object.defineProperty(HTMLElement.prototype, "offsetWidth", {
    configurable: true,
    get(this: HTMLElement) {
      return isBubble(this) ? BUBBLE_WIDTH : 0;
    },
  });
  Object.defineProperty(HTMLElement.prototype, "offsetHeight", {
    configurable: true,
    get(this: HTMLElement) {
      return isBubble(this) ? BUBBLE_HEIGHT : 0;
    },
  });

  try {
    render(
      <Tooltip label="Save your changes">
        <button type="button">Save</button>
      </Tooltip>,
    );
    fireEvent.mouseEnter(screen.getByRole("button", { name: "Save" }));
    advance(300);
    const bubble = screen.getByRole("tooltip");
    // 1024 - 200 - 8: flush against the reserved margin, not against 870.
    expect(bubble.style.left).toBe(`${window.innerWidth - BUBBLE_WIDTH - 8}px`);
    // Above the trigger: 300 - 8 (gap) - 28 (bubble).
    expect(bubble.style.top).toBe(`${TRIGGER.top - 8 - BUBBLE_HEIGHT}px`);
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
