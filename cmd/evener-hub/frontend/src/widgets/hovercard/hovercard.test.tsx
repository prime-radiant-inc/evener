import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { HoverCard } from ".";

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

test("the plain-node gallery child keeps the automatic description association", () => {
  vi.useFakeTimers();
  render(
    <HoverCard label={<div>Tests passed in 14 seconds</div>}>
      <button type="button">job_01HZX7P9</button>
    </HoverCard>,
  );
  const trigger = screen.getByRole("button", { name: "job_01HZX7P9" });
  expect(trigger.getAttribute("aria-describedby")).toBeNull();

  fireEvent.focus(trigger);
  act(() => vi.advanceTimersByTime(300));

  const card = screen.getByRole("tooltip");
  expect(trigger.getAttribute("aria-describedby")).toBe(card.id);
  fireEvent.blur(trigger);
  expect(screen.queryByRole("tooltip")).toBeNull();
  expect(trigger.getAttribute("aria-describedby")).toBeNull();
});

// The card renders the shared scaffold's div bubble, where the tooltip renders
// its span: only this proves the shared measure path reads the trigger's rect
// and this bubble's own box, and writes the placement out. jsdom performs no
// layout, so both boxes have to be supplied.
test("places the portaled card from the trigger's rect and the card's own box", () => {
  vi.useFakeTimers();
  const CARD_WIDTH = 240;
  const CARD_HEIGHT = 96;
  // jsdom's window is 1024x768; centring a 240px card on a trigger whose centre
  // is at 970 would end at 1090, 66px past the viewport.
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
      return isBubble(this) ? CARD_WIDTH : 0;
    },
  });
  Object.defineProperty(HTMLElement.prototype, "offsetHeight", {
    configurable: true,
    get(this: HTMLElement) {
      return isBubble(this) ? CARD_HEIGHT : 0;
    },
  });

  try {
    render(
      <HoverCard label={<div>Tests passed in 14 seconds</div>}>
        <button type="button">job_01HZX7P9</button>
      </HoverCard>,
    );
    fireEvent.focus(screen.getByRole("button", { name: "job_01HZX7P9" }));
    act(() => vi.advanceTimersByTime(300));

    const card = screen.getByRole("tooltip");
    expect(card.tagName).toBe("DIV");
    expect(card.parentElement).toBe(document.body);
    // 1024 - 240 - 8: flush against the reserved margin, not against 850.
    expect(card.style.left).toBe(`${window.innerWidth - CARD_WIDTH - 8}px`);
    // Above the trigger: 300 - 8 (gap) - 96 (card).
    expect(card.style.top).toBe(`${TRIGGER.top - 8 - CARD_HEIGHT}px`);
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
