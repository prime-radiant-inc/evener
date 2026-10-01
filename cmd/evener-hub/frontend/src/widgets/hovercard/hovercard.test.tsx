import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
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

test("external focus and pointer hover independently keep the card open", () => {
  render(
    <div role="treeitem" tabIndex={0} data-testid="row">
      <HoverCard label={<div>Project prime-radiant</div>} focusTarget={() => screen.queryByTestId("row")}>
        <button type="button" tabIndex={-1}>
          Fix flaky test
        </button>
      </HoverCard>
    </div>,
  );
  const row = screen.getByRole("treeitem");

  fireEvent.focus(row);

  const card = screen.getByRole("tooltip");
  expect(row.getAttribute("aria-describedby")).toBe(card.id);
  const trigger = screen.getByRole("button", { name: "Fix flaky test" });
  expect(trigger.tabIndex).toBe(-1);

  fireEvent.mouseEnter(trigger);
  fireEvent.mouseLeave(trigger);
  expect(screen.getByRole("tooltip")).toBe(card);

  fireEvent.mouseEnter(trigger);
  fireEvent.blur(row);
  expect(screen.getByRole("tooltip")).toBe(card);

  fireEvent.mouseLeave(trigger);
  expect(screen.queryByRole("tooltip")).toBeNull();
  expect(row.getAttribute("aria-describedby")).toBeNull();
});

test("external focus closes after focus travels through a child and leaves the target", () => {
  render(
    <>
      <div role="treeitem" tabIndex={0} data-testid="row">
        <HoverCard label={<div>Project prime-radiant</div>} focusTarget={() => screen.queryByTestId("row")}>
          <button type="button">Row action</button>
        </HoverCard>
      </div>
      <button type="button">Outside</button>
    </>,
  );
  const row = screen.getByRole("treeitem");
  const rowAction = screen.getByRole("button", { name: "Row action" });
  const outside = screen.getByRole("button", { name: "Outside" });

  fireEvent.focus(row);
  expect(screen.getByRole("tooltip")).toBeTruthy();
  fireEvent.blur(row, { relatedTarget: rowAction });
  fireEvent.focus(rowAction, { relatedTarget: row });
  expect(screen.getByRole("tooltip")).toBeTruthy();

  fireEvent.blur(rowAction, { relatedTarget: outside });
  fireEvent.focus(outside, { relatedTarget: rowAction });
  expect(screen.queryByRole("tooltip")).toBeNull();
  expect(row.getAttribute("aria-describedby")).toBeNull();
});

test("mounting under an already-focused external target opens the card", () => {
  function Fixture({ showCard }: { showCard: boolean }) {
    return (
      <div role="treeitem" tabIndex={0} data-testid="row">
        {showCard && (
          <HoverCard label={<div>Project prime-radiant</div>} focusTarget={() => screen.queryByTestId("row")}>
            <button type="button">Row action</button>
          </HoverCard>
        )}
      </div>
    );
  }

  const view = render(<Fixture showCard={false} />);
  const row = screen.getByRole("treeitem");
  act(() => row.focus());
  expect(document.activeElement).toBe(row);

  view.rerender(<Fixture showCard />);
  const card = screen.getByRole("tooltip");
  expect(row.getAttribute("aria-describedby")).toBe(card.id);
});

test("the first hoverless tap opens an enabled card and the second activates its containing row", () => {
  const originalMatchMedia = installHoverlessMatchMedia();
  const activate = vi.fn();

  try {
    render(
      // biome-ignore lint/a11y/noStaticElementInteractions: this fixture observes whether the nested trigger bubbles
      // biome-ignore lint/a11y/useKeyWithClickEvents: keyboard activation is exercised through the external focus test
      <div onClick={activate}>
        <HoverCard label={<div>Project prime-radiant</div>} tapEnabled>
          <button type="button" tabIndex={-1}>
            Fix flaky test
          </button>
        </HoverCard>
      </div>,
    );

    fireEvent.click(screen.getByRole("button", { name: "Fix flaky test" }));

    const card = screen.getByRole("tooltip");
    expect(card.getAttribute("data-tap-enabled")).toBe("true");
    expect(activate).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "Fix flaky test" }));
    expect(activate).toHaveBeenCalledOnce();
    expect(screen.queryByRole("tooltip")).toBeNull();
  } finally {
    window.matchMedia = originalMatchMedia;
  }
});

test("focus before a hoverless click still treats that click as the first tap", async () => {
  const originalMatchMedia = installHoverlessMatchMedia();
  const activate = vi.fn();
  const user = userEvent.setup();

  try {
    render(
      // biome-ignore lint/a11y/noStaticElementInteractions: this fixture observes whether the nested trigger bubbles
      // biome-ignore lint/a11y/useKeyWithClickEvents: keyboard behavior is covered by the external focus tests
      <div onClick={activate}>
        <div role="treeitem" tabIndex={0} data-testid="row">
          <HoverCard
            label={<div>Project prime-radiant</div>}
            focusTarget={() => screen.queryByTestId("row")}
            tapEnabled
          >
            <button type="button">Fix flaky test</button>
          </HoverCard>
        </div>
      </div>,
    );
    const trigger = screen.getByRole("button", { name: "Fix flaky test" });

    await user.click(trigger);
    expect(screen.getByRole("tooltip")).toBeTruthy();
    expect(activate).not.toHaveBeenCalled();

    await user.click(trigger);
    expect(screen.queryByRole("tooltip")).toBeNull();
    expect(activate).toHaveBeenCalledOnce();
  } finally {
    window.matchMedia = originalMatchMedia;
  }
});

test("a tap-opened card closes when the user presses outside it", () => {
  const originalMatchMedia = installHoverlessMatchMedia();

  try {
    render(
      <div>
        <HoverCard label={<div>Project prime-radiant</div>} tapEnabled>
          <button type="button">Fix flaky test</button>
        </HoverCard>
        <button type="button">Elsewhere</button>
      </div>,
    );

    fireEvent.click(screen.getByRole("button", { name: "Fix flaky test" }));
    expect(screen.getByRole("tooltip")).toBeTruthy();

    fireEvent.pointerDown(screen.getByRole("button", { name: "Elsewhere" }));
    expect(screen.queryByRole("tooltip")).toBeNull();
  } finally {
    window.matchMedia = originalMatchMedia;
  }
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

function installHoverlessMatchMedia() {
  const originalMatchMedia = window.matchMedia;
  window.matchMedia = vi.fn().mockImplementation((query: string) => ({
    matches: query === "(hover: none)",
    media: query,
    onchange: null,
    addListener: vi.fn(),
    removeListener: vi.fn(),
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
    dispatchEvent: vi.fn(),
  }));
  return originalMatchMedia;
}
