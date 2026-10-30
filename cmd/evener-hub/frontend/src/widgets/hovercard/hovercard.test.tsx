import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createPortal } from "react-dom";
import { createRoot } from "react-dom/client";
import { afterEach, expect, test, vi } from "vitest";
import { HoverCard as FromBarrel } from "..";
import { appendPopout } from "../internal/popoutTestUtils";
import { HoverCard, LONG_PRESS_MS } from ".";
import { installHoverlessMatchMedia } from "./hovercardTestUtils";
import { POINTER_CLICK_WINDOW_MS, SHOW_DELAY_MS } from "./useFloatingLabel";

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

test("it is exported from the widget barrel", () => {
  expect(FromBarrel).toBe(HoverCard);
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

// The same mount-time branch in a popout: the opener document's activeElement
// is the iframe, so asking it could never find the row that already has focus
// in the popout.
test("mounting under an already-focused target inside a popout opens the card there", () => {
  const { frame, doc } = appendPopout();
  const rowTarget = () => doc.querySelector<HTMLElement>('[data-testid="popout-row"]');
  function Fixture({ showCard }: { showCard: boolean }) {
    return (
      <div role="treeitem" tabIndex={0} data-testid="popout-row">
        {showCard && (
          <HoverCard label={<div>Project prime-radiant</div>} focusTarget={rowTarget}>
            <button type="button">Row action</button>
          </HoverCard>
        )}
      </div>
    );
  }
  const view = render(createPortal(<Fixture showCard={false} />, doc.body));
  try {
    const row = rowTarget();
    if (!row) throw new Error("popout row missing");
    act(() => row.focus());
    expect(doc.activeElement).toBe(row);

    view.rerender(createPortal(<Fixture showCard />, doc.body));
    const card = doc.querySelector('[role="tooltip"]');
    expect(card).not.toBeNull();
    expect(card?.parentElement).toBe(doc.body);
    expect(row.getAttribute("aria-describedby")).toBe(card?.getAttribute("id"));
  } finally {
    view.unmount();
    frame.remove();
  }
});

test("a hoverless tap activates the row and opens no card", () => {
  installHoverlessMatchMedia();
  const activate = vi.fn();

  render(
    // biome-ignore lint/a11y/noStaticElementInteractions: this fixture observes whether the nested trigger bubbles
    // biome-ignore lint/a11y/useKeyWithClickEvents: keyboard activation is exercised through the external focus test
    <div onClick={activate}>
      <HoverCard label={<div>Project prime-radiant</div>} longPressEnabled>
        <button type="button" tabIndex={-1}>
          Fix flaky test
        </button>
      </HoverCard>
    </div>,
  );

  fireEvent.click(screen.getByRole("button", { name: "Fix flaky test" }));

  expect(screen.queryByRole("tooltip")).toBeNull();
  expect(activate).toHaveBeenCalledOnce();
});

test("a hoverless long press opens the card and swallows the tap that follows", () => {
  vi.useFakeTimers();
  installHoverlessMatchMedia();
  const activate = vi.fn();

  render(
    // biome-ignore lint/a11y/noStaticElementInteractions: this fixture observes whether the nested trigger bubbles
    // biome-ignore lint/a11y/useKeyWithClickEvents: keyboard activation is exercised through the external focus test
    <div onClick={activate}>
      <HoverCard label={<div>Project prime-radiant</div>} longPressEnabled>
        <button type="button" tabIndex={-1}>
          Fix flaky test
        </button>
      </HoverCard>
    </div>,
  );
  const trigger = screen.getByRole("button", { name: "Fix flaky test" });

  fireEvent.pointerDown(trigger, { button: 0, clientX: 10, clientY: 10 });
  act(() => vi.advanceTimersByTime(LONG_PRESS_MS));

  const card = screen.getByRole("tooltip");
  expect(card.getAttribute("data-long-press-enabled")).toBe("true");

  fireEvent.pointerUp(trigger, { clientX: 10, clientY: 10 });
  fireEvent.click(trigger);

  expect(activate).not.toHaveBeenCalled();
  expect(screen.getByRole("tooltip")).toBe(card);
});

test.each(["scroll", "resize"])("%s cancels a pending long press without swallowing the next click", (event) => {
  vi.useFakeTimers();
  installHoverlessMatchMedia();
  const activate = vi.fn();
  render(
    <HoverCard label={<div>Project prime-radiant</div>} longPressEnabled>
      <button type="button" onClick={activate}>
        Fix flaky test
      </button>
    </HoverCard>,
  );
  const trigger = screen.getByRole("button", { name: "Fix flaky test" });
  fireEvent.pointerDown(trigger, { button: 0 });
  act(() => vi.advanceTimersByTime(100));
  // Scroll does not bubble, so exercise the ancestor capture listener.
  fireEvent(event === "scroll" ? document.body : window, new Event(event));
  act(() => vi.advanceTimersByTime(LONG_PRESS_MS));
  expect(screen.queryByRole("tooltip")).toBeNull();
  fireEvent.pointerUp(trigger);
  fireEvent.click(trigger);
  expect(activate).toHaveBeenCalledOnce();

  fireEvent.pointerDown(trigger, { button: 0 });
  act(() => vi.advanceTimersByTime(LONG_PRESS_MS));
  expect(screen.getByRole("tooltip")).toBeTruthy();
  fireEvent.pointerUp(trigger);
  fireEvent.click(trigger);
  expect(activate).toHaveBeenCalledOnce();
});

test("a hoverless press that moves or is cancelled opens no card", () => {
  vi.useFakeTimers();
  installHoverlessMatchMedia();

  render(
    <div>
      <HoverCard label={<div>Project prime-radiant</div>} longPressEnabled>
        <button type="button">Fix flaky test</button>
      </HoverCard>
    </div>,
  );
  const trigger = screen.getByRole("button", { name: "Fix flaky test" });

  fireEvent.pointerDown(trigger, { button: 0, clientX: 10, clientY: 10 });
  act(() => vi.advanceTimersByTime(100));
  fireEvent.pointerMove(trigger, { clientX: 80, clientY: 80 });
  act(() => vi.advanceTimersByTime(LONG_PRESS_MS));
  expect(screen.queryByRole("tooltip")).toBeNull();

  fireEvent.pointerDown(trigger, { button: 0, clientX: 10, clientY: 10 });
  act(() => vi.advanceTimersByTime(100));
  fireEvent.pointerCancel(trigger);
  act(() => vi.advanceTimersByTime(LONG_PRESS_MS));
  expect(screen.queryByRole("tooltip")).toBeNull();
});

test("long-press-enabled cards still activate when the browser has no matchMedia API", () => {
  vi.useFakeTimers();
  vi.stubGlobal("matchMedia", undefined);
  const activate = vi.fn();
  render(
    // biome-ignore lint/a11y/noStaticElementInteractions: this fixture observes whether the nested trigger bubbles
    // biome-ignore lint/a11y/useKeyWithClickEvents: keyboard behavior is covered by the external focus tests
    <div onClick={activate}>
      <HoverCard label={<div>Project prime-radiant</div>} longPressEnabled>
        <button type="button">Fix flaky test</button>
      </HoverCard>
    </div>,
  );

  const trigger = screen.getByRole("button", { name: "Fix flaky test" });
  expect(() => {
    fireEvent.focus(trigger);
    act(() => vi.advanceTimersByTime(300));
  }).not.toThrow();
  expect(screen.getByRole("tooltip")).toBeTruthy();

  expect(() => fireEvent.click(trigger)).not.toThrow();
  expect(activate).toHaveBeenCalledOnce();
});

test("the focus a hoverless tap takes opens no card, and the tap still activates the row", async () => {
  installHoverlessMatchMedia();
  const activate = vi.fn();
  const user = userEvent.setup();

  render(
    // biome-ignore lint/a11y/noStaticElementInteractions: this fixture observes whether the nested trigger bubbles
    // biome-ignore lint/a11y/useKeyWithClickEvents: the tap is the activation under test
    <div onClick={activate}>
      <div role="treeitem" tabIndex={0} data-testid="row">
        <HoverCard
          label={<div>Project prime-radiant</div>}
          focusTarget={() => screen.queryByTestId("row")}
          longPressEnabled
        >
          <button type="button">Fix flaky test</button>
        </HoverCard>
      </div>
    </div>,
  );
  const trigger = screen.getByRole("button", { name: "Fix flaky test" });
  const row = screen.getByTestId("row");

  // Real touch order: the compatibility mousedown and the focus it takes come
  // after the pointer lifts, so the suppression must outlive the press.
  fireEvent.pointerDown(row, { button: 0 });
  fireEvent.pointerUp(row, { button: 0 });
  fireEvent.mouseDown(row, { button: 0 });
  fireEvent.focus(row);
  expect(screen.queryByRole("tooltip")).toBeNull();

  await user.click(trigger);
  expect(screen.queryByRole("tooltip")).toBeNull();
  expect(activate).toHaveBeenCalledOnce();
});

test("a long-press-opened card closes when the user presses outside it", () => {
  vi.useFakeTimers();
  installHoverlessMatchMedia();

  render(
    <div>
      <HoverCard label={<div>Project prime-radiant</div>} longPressEnabled>
        <button type="button">Fix flaky test</button>
      </HoverCard>
      <button type="button">Elsewhere</button>
    </div>,
  );
  const trigger = screen.getByRole("button", { name: "Fix flaky test" });

  fireEvent.pointerDown(trigger, { button: 0, clientX: 10, clientY: 10 });
  act(() => vi.advanceTimersByTime(LONG_PRESS_MS));
  expect(screen.getByRole("tooltip")).toBeTruthy();

  fireEvent.pointerDown(screen.getByRole("button", { name: "Elsewhere" }));
  expect(screen.queryByRole("tooltip")).toBeNull();
});

test("a long press swallows the click even when the trigger itself owns it", () => {
  vi.useFakeTimers();
  installHoverlessMatchMedia();
  const activate = vi.fn();
  render(
    <HoverCard label={<div>Project prime-radiant</div>} longPressEnabled>
      <button type="button" onClick={activate}>
        Fix flaky test
      </button>
    </HoverCard>,
  );
  const trigger = screen.getByRole("button", { name: "Fix flaky test" });

  fireEvent.pointerDown(trigger, { button: 0, clientX: 10, clientY: 10 });
  act(() => vi.advanceTimersByTime(LONG_PRESS_MS));
  expect(screen.getByRole("tooltip")).toBeTruthy();

  fireEvent.pointerUp(trigger, { clientX: 10, clientY: 10 });
  fireEvent.click(trigger);

  expect(activate).not.toHaveBeenCalled();
});

test("a plain tap on an open card dismisses it and still activates the row", () => {
  vi.useFakeTimers();
  installHoverlessMatchMedia();
  const activate = vi.fn();
  render(
    // biome-ignore lint/a11y/noStaticElementInteractions: this fixture observes whether the nested trigger bubbles
    // biome-ignore lint/a11y/useKeyWithClickEvents: keyboard activation is exercised through the external focus test
    <div onClick={activate}>
      <HoverCard label={<div>Project prime-radiant</div>} longPressEnabled>
        <button type="button" tabIndex={-1}>
          Fix flaky test
        </button>
      </HoverCard>
    </div>,
  );
  const trigger = screen.getByRole("button", { name: "Fix flaky test" });

  fireEvent.pointerDown(trigger, { button: 0, clientX: 10, clientY: 10 });
  act(() => vi.advanceTimersByTime(LONG_PRESS_MS));
  expect(screen.getByRole("tooltip")).toBeTruthy();
  fireEvent.pointerUp(trigger, { clientX: 10, clientY: 10 });
  fireEvent.click(trigger);
  expect(activate).not.toHaveBeenCalled();

  fireEvent.click(trigger);
  expect(screen.queryByRole("tooltip")).toBeNull();
  expect(activate).toHaveBeenCalledOnce();
});

test("hoverless keyboard focus still opens the card", () => {
  installHoverlessMatchMedia();
  render(
    <div role="treeitem" tabIndex={0} data-testid="row">
      <HoverCard
        label={<div>Project prime-radiant</div>}
        focusTarget={() => screen.queryByTestId("row")}
        longPressEnabled
      >
        <button type="button">Fix flaky test</button>
      </HoverCard>
    </div>,
  );

  fireEvent.focus(screen.getByTestId("row"));
  expect(screen.getByRole("tooltip")).toBeTruthy();
});

test("a cancelled long press does not leave the next click swallowed", () => {
  vi.useFakeTimers();
  installHoverlessMatchMedia();
  const activate = vi.fn();
  render(
    // biome-ignore lint/a11y/noStaticElementInteractions: this fixture observes whether the nested trigger bubbles
    // biome-ignore lint/a11y/useKeyWithClickEvents: keyboard activation is exercised through the external focus test
    <div onClick={activate}>
      <HoverCard label={<div>Project prime-radiant</div>} longPressEnabled>
        <button type="button" tabIndex={-1}>
          Fix flaky test
        </button>
      </HoverCard>
    </div>,
  );
  const trigger = screen.getByRole("button", { name: "Fix flaky test" });

  fireEvent.pointerDown(trigger, { button: 0, clientX: 10, clientY: 10 });
  act(() => vi.advanceTimersByTime(LONG_PRESS_MS));
  expect(screen.getByRole("tooltip")).toBeTruthy();

  fireEvent.pointerCancel(trigger);
  fireEvent.click(trigger);

  expect(activate).toHaveBeenCalledOnce();
});

function LongPressToggleFixture({
  longPressEnabled,
  onActivate,
}: {
  longPressEnabled: boolean;
  onActivate: () => void;
}) {
  return (
    <div>
      <HoverCard label={<div>Project prime-radiant</div>} longPressEnabled={longPressEnabled}>
        <button type="button" onClick={onActivate}>
          Fix flaky test
        </button>
      </HoverCard>
    </div>
  );
}

function endPress(trigger: HTMLElement, endingEvent: string) {
  if (endingEvent === "pointerup") fireEvent.pointerUp(trigger, { pointerId: 1, clientX: 10, clientY: 10 });
  else fireEvent.pointerCancel(trigger, { pointerId: 1 });
}

test.each(["pointerup", "pointercancel"])(
  "disabling long-press mode during a pending press does not swallow a later activation (%s ends it)",
  (endingEvent) => {
    vi.useFakeTimers();
    installHoverlessMatchMedia();
    const activate = vi.fn();
    const view = render(<LongPressToggleFixture longPressEnabled onActivate={activate} />);
    const trigger = screen.getByRole("button", { name: "Fix flaky test" });

    fireEvent.pointerDown(trigger, { pointerId: 1, button: 0, clientX: 10, clientY: 10 });
    act(() => vi.advanceTimersByTime(100));
    expect(screen.queryByRole("tooltip")).toBeNull();

    view.rerender(<LongPressToggleFixture longPressEnabled={false} onActivate={activate} />);
    endPress(trigger, endingEvent);
    view.rerender(<LongPressToggleFixture longPressEnabled onActivate={activate} />);

    // No further pointerdown resets the gesture: this is the control's own
    // activation, the click a keyboard or assistive technology sends.
    fireEvent.click(trigger);
    expect(activate).toHaveBeenCalledOnce();
  },
);

test.each(["pointerup", "pointercancel"])(
  "disabling long-press mode after the card is revealed does not swallow a later activation (%s ends it)",
  (endingEvent) => {
    vi.useFakeTimers();
    installHoverlessMatchMedia();
    const activate = vi.fn();
    const view = render(<LongPressToggleFixture longPressEnabled onActivate={activate} />);
    const trigger = screen.getByRole("button", { name: "Fix flaky test" });

    fireEvent.pointerDown(trigger, { pointerId: 1, button: 0, clientX: 10, clientY: 10 });
    act(() => vi.advanceTimersByTime(LONG_PRESS_MS));
    expect(screen.getByRole("tooltip")).toBeTruthy();

    view.rerender(<LongPressToggleFixture longPressEnabled={false} onActivate={activate} />);
    endPress(trigger, endingEvent);
    view.rerender(<LongPressToggleFixture longPressEnabled onActivate={activate} />);

    fireEvent.click(trigger);
    expect(activate).toHaveBeenCalledOnce();
  },
);

test("a press that leaves the trigger before the click does not swallow a later one", () => {
  vi.useFakeTimers();
  installHoverlessMatchMedia();
  const activate = vi.fn();
  render(
    // biome-ignore lint/a11y/noStaticElementInteractions: this fixture observes whether the nested trigger bubbles
    // biome-ignore lint/a11y/useKeyWithClickEvents: keyboard activation is exercised through the external focus test
    <div onClick={activate}>
      <HoverCard label={<div>Project prime-radiant</div>} longPressEnabled>
        <button type="button" tabIndex={-1}>
          Fix flaky test
        </button>
      </HoverCard>
    </div>,
  );
  const trigger = screen.getByRole("button", { name: "Fix flaky test" });

  fireEvent.pointerDown(trigger, { button: 0, clientX: 10, clientY: 10 });
  act(() => vi.advanceTimersByTime(LONG_PRESS_MS));
  expect(screen.getByRole("tooltip")).toBeTruthy();

  // The finger drifts off and lifts outside, so no click arrives for the
  // swallow; the bound must drop it.
  fireEvent.pointerLeave(trigger);
  act(() => vi.advanceTimersByTime(POINTER_CLICK_WINDOW_MS + 1));
  fireEvent.click(trigger);

  expect(activate).toHaveBeenCalledOnce();
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

test.each([
  {
    name: "beside the sidebar, centered on the row",
    right: 280,
    top: 300,
    height: 40,
    viewportWidth: 1000,
    viewportHeight: 800,
    wantLeft: "292px",
    wantTop: "272px",
  },
  {
    name: "at the top viewport margin",
    right: 280,
    top: 0,
    height: 40,
    viewportWidth: 1000,
    viewportHeight: 800,
    wantLeft: "292px",
    wantTop: "8px",
  },
  {
    name: "at the bottom viewport margin",
    right: 360,
    top: 770,
    height: 30,
    viewportWidth: 1000,
    viewportHeight: 800,
    wantLeft: "372px",
    wantTop: "696px",
  },
  {
    name: "above when right does not fit",
    right: 360,
    top: 300,
    height: 40,
    viewportWidth: 500,
    viewportHeight: 800,
    wantLeft: "30px",
    wantTop: "192px",
  },
  {
    name: "below when above does not fit",
    right: 360,
    top: 40,
    height: 40,
    viewportWidth: 500,
    viewportHeight: 800,
    wantLeft: "30px",
    wantTop: "92px",
  },
  {
    name: "clamped when neither side fits",
    right: 360,
    top: 40,
    height: 40,
    viewportWidth: 500,
    viewportHeight: 150,
    wantLeft: "30px",
    wantTop: "8px",
  },
  {
    name: "horizontally clamped fallback",
    right: 360,
    top: 300,
    height: 40,
    viewportWidth: 270,
    viewportHeight: 800,
    wantLeft: "22px",
    wantTop: "192px",
  },
  {
    name: "right fitting exactly at the viewport margin",
    right: 280,
    top: 300,
    height: 40,
    viewportWidth: 540,
    viewportHeight: 800,
    wantLeft: "292px",
    wantTop: "272px",
  },
])(
  "places a side-anchored production card $name",
  ({ right, top, height, viewportWidth, viewportHeight, wantLeft, wantTop }) => {
    vi.useFakeTimers();
    vi.stubGlobal("innerWidth", viewportWidth);
    vi.stubGlobal("innerHeight", viewportHeight);
    const width = vi.spyOn(HTMLElement.prototype, "offsetWidth", "get").mockReturnValue(240);
    const cardHeight = vi.spyOn(HTMLElement.prototype, "offsetHeight", "get").mockReturnValue(96);
    try {
      render(
        <HoverCard
          label={<div>Complete context</div>}
          sideAnchor={() => ({ rowRect: new DOMRect(20, top, 260, height), sideRight: right })}
        >
          <button type="button">Short title</button>
        </HoverCard>,
      );
      fireEvent.focus(screen.getByRole("button"));
      act(() => vi.advanceTimersByTime(300));
      const card = screen.getByRole("tooltip");
      expect(card.style.left).toBe(wantLeft);
      expect(card.style.top).toBe(wantTop);
      expect(card.parentElement).toBe(document.body);
      expect(screen.getByRole("button").getAttribute("aria-describedby")).toBe(card.id);
    } finally {
      width.mockRestore();
      cardHeight.mockRestore();
    }
  },
);

test("remeasures a side-anchored card's untransformed size after its content changes", () => {
  vi.useFakeTimers();
  vi.stubGlobal("innerWidth", 1000);
  vi.stubGlobal("innerHeight", 800);
  let resize: () => void = () => {};
  vi.stubGlobal(
    "ResizeObserver",
    class {
      constructor(callback: () => void) {
        resize = callback;
      }
      observe() {}
      disconnect() {}
    },
  );
  const width = vi.spyOn(HTMLElement.prototype, "offsetWidth", "get").mockReturnValue(240);
  const height = vi.spyOn(HTMLElement.prototype, "offsetHeight", "get").mockReturnValue(96);
  // The transform-distorted box must never drive card collision decisions.
  const rect = vi.spyOn(Element.prototype, "getBoundingClientRect").mockReturnValue(new DOMRect(0, 0, 1, 1));
  try {
    render(
      <HoverCard
        label={<div>Complete context</div>}
        sideAnchor={() => ({ rowRect: new DOMRect(20, 300, 260, 40), sideRight: 280 })}
      >
        <button type="button">Short title</button>
      </HoverCard>,
    );
    fireEvent.focus(screen.getByRole("button"));
    act(() => vi.advanceTimersByTime(300));
    const card = screen.getByRole("tooltip");
    expect(card.style.top).toBe("272px");
    height.mockReturnValue(200);
    act(() => resize());
    expect(card.style.top).toBe("220px");
    width.mockReturnValue(720);
    act(() => resize());
    expect(card.style.left).toBe("8px");
    expect(card.style.top).toBe("88px");
  } finally {
    width.mockRestore();
    height.mockRestore();
    rect.mockRestore();
  }
});

test("keeps an open side-anchored card beside its resized owning row", () => {
  vi.stubGlobal("innerWidth", 1200);
  vi.stubGlobal("innerHeight", 800);
  const resizeCallbacks = new Map<Element, () => void>();
  vi.stubGlobal(
    "ResizeObserver",
    class {
      private readonly targets = new Set<Element>();
      constructor(private readonly callback: () => void) {}
      observe(target: Element) {
        this.targets.add(target);
        resizeCallbacks.set(target, this.callback);
      }
      disconnect() {
        for (const target of this.targets) resizeCallbacks.delete(target);
      }
    },
  );
  let sidebarRight = 280;
  const width = vi.spyOn(HTMLElement.prototype, "offsetWidth", "get").mockReturnValue(240);
  const height = vi.spyOn(HTMLElement.prototype, "offsetHeight", "get").mockReturnValue(96);
  try {
    render(
      <div role="treeitem" tabIndex={0} data-testid="owning-row">
        <HoverCard
          label={<div>Complete context</div>}
          focusTarget={() => document.querySelector('[data-testid="owning-row"]')}
          sideAnchor={() => ({ rowRect: new DOMRect(20, 300, sidebarRight - 20, 40), sideRight: sidebarRight })}
        >
          <button type="button">Short title</button>
        </HoverCard>
      </div>,
    );
    const row = screen.getByTestId("owning-row");
    act(() => row.focus());
    const card = screen.getByRole("tooltip");
    expect(card.style.left).toBe("292px");
    expect(card.style.top).toBe("272px");
    sidebarRight = 560;
    act(() => resizeCallbacks.get(row)?.());
    expect(screen.getByRole("tooltip")).toBe(card);
    expect(card.style.left).toBe("572px");
    expect(card.style.top).toBe("272px");
    expect(document.activeElement).toBe(row);
    expect(row.getAttribute("aria-describedby")).toBe(card.id);
  } finally {
    width.mockRestore();
    height.mockRestore();
  }
});

// Puts back exactly what was there, including "nothing at all" - a stub left
// installed on HTMLElement.prototype would follow every later test file in the
// same worker.
function restoreOwnProperty(target: object, key: string, descriptor: PropertyDescriptor | undefined) {
  if (descriptor) Object.defineProperty(target, key, descriptor);
  else delete (target as Record<string, unknown>)[key];
}

// Counts live callback identities registered on an EventTarget, keyed by event
// type and capture flag. The native methods still run, so the listeners counted
// are the ones actually installed. The pre-existing listeners present before the
// count starts are a baseline, subtracted from every reading. window is the
// target here: the gesture's cancellation pair and the floating-label hook's
// dismissal pair both live on window, and only the callback identity tells them
// apart. window carries its OWN addEventListener, which shadows
// EventTarget.prototype's, so the override lands on the target itself.
function tallyEventTarget(target: EventTarget) {
  const originalAdd = Object.getOwnPropertyDescriptor(target, "addEventListener");
  const originalRemove = Object.getOwnPropertyDescriptor(target, "removeEventListener");
  const nativeAdd = (originalAdd?.value ?? target.addEventListener) as EventTarget["addEventListener"];
  const nativeRemove = (originalRemove?.value ?? target.removeEventListener) as EventTarget["removeEventListener"];

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

  Object.defineProperty(target, "addEventListener", { configurable: true, writable: true, value: trackedAdd });
  Object.defineProperty(target, "removeEventListener", { configurable: true, writable: true, value: trackedRemove });

  const baseline = new Map<string, number>();
  for (const [key, identities] of live) baseline.set(key, identities.size);

  return {
    net(type: string, capture: boolean) {
      return (live.get(keyOf(type, capture))?.size ?? 0) - (baseline.get(keyOf(type, capture)) ?? 0);
    },
    restore() {
      if (originalAdd) Object.defineProperty(target, "addEventListener", originalAdd);
      else Reflect.deleteProperty(target, "addEventListener");
      if (originalRemove) Object.defineProperty(target, "removeEventListener", originalRemove);
      else Reflect.deleteProperty(target, "removeEventListener");
    },
  };
}

const TALLY_IDS = Array.from({ length: 100 }, (_, index) => `hovercard-${index}`);

function LongPressCards({ count }: { count: number }) {
  return (
    <div>
      {TALLY_IDS.slice(0, count).map((id) => (
        <HoverCard key={id} label={<div>Project prime-radiant</div>} longPressEnabled>
          <button type="button">Fix flaky test {id}</button>
        </HoverCard>
      ))}
    </div>
  );
}

test("owns window cancellation only during its gesture", () => {
  vi.useFakeTimers();
  installHoverlessMatchMedia();
  const tally = tallyEventTarget(window);
  try {
    for (const count of [1, 10, 100]) {
      const view = render(<LongPressCards count={count} />);
      // An idle long-press card owns no window scroll/resize listener, and the
      // count does not grow with the number of mounted cards.
      expect(tally.net("scroll", true)).toBe(0);
      expect(tally.net("resize", false)).toBe(0);
      view.unmount();
      expect(tally.net("scroll", true)).toBe(0);
      expect(tally.net("resize", false)).toBe(0);
    }

    const view = render(<LongPressCards count={1} />);
    const trigger = screen.getByRole("button", { name: `Fix flaky test ${TALLY_IDS[0]}` });

    // Starting a gesture owns exactly one scroll/resize cancellation pair.
    fireEvent.pointerDown(trigger, { pointerId: 7, button: 0, clientX: 10, clientY: 10 });
    expect(tally.net("scroll", true)).toBe(1);
    expect(tally.net("resize", false)).toBe(1);

    // Showing the card adds the floating-label hook's own dismissal pair; the
    // gesture pair is distinct and still owned by the gesture.
    act(() => vi.advanceTimersByTime(LONG_PRESS_MS));
    expect(screen.getByRole("tooltip")).toBeTruthy();
    expect(tally.net("scroll", true)).toBe(2);
    expect(tally.net("resize", false)).toBe(2);

    // Lifting ends the gesture and disposes its pair, leaving the visible
    // card's own hook pair (which goes when the card is dismissed).
    fireEvent.pointerUp(trigger, { pointerId: 7 });
    expect(tally.net("scroll", true)).toBe(1);
    expect(tally.net("resize", false)).toBe(1);

    // A cancelled gesture disposes its pair.
    fireEvent.pointerDown(trigger, { pointerId: 8, button: 0, clientX: 10, clientY: 10 });
    expect(tally.net("resize", false)).toBe(2);
    fireEvent.pointerCancel(trigger, { pointerId: 8 });
    expect(tally.net("resize", false)).toBe(1);

    // Leaving the trigger ends the gesture without a lift there.
    fireEvent.pointerDown(trigger, { pointerId: 9, button: 0, clientX: 10, clientY: 10 });
    expect(tally.net("resize", false)).toBe(2);
    fireEvent.pointerLeave(trigger, { pointerId: 9 });
    expect(tally.net("resize", false)).toBe(1);

    // Unmounting disposes a gesture pair still owed.
    fireEvent.pointerDown(trigger, { pointerId: 10, button: 0, clientX: 10, clientY: 10 });
    expect(tally.net("resize", false)).toBe(2);
    view.unmount();
    expect(tally.net("scroll", true)).toBe(0);
    expect(tally.net("resize", false)).toBe(0);
  } finally {
    tally.restore();
  }
});

test.each(["touch", "pen"])("ignores an unrelated %s pointer and retains the initiating gesture", (pointerType) => {
  vi.useFakeTimers();
  installHoverlessMatchMedia();
  const activate = vi.fn();
  const tally = tallyEventTarget(window);
  try {
    render(
      // biome-ignore lint/a11y/noStaticElementInteractions: this fixture observes whether the nested trigger bubbles
      // biome-ignore lint/a11y/useKeyWithClickEvents: keyboard activation is exercised through the external focus test
      <div onClick={activate}>
        <HoverCard label={<div>Project prime-radiant</div>} longPressEnabled>
          <button type="button" tabIndex={-1}>
            Fix flaky test
          </button>
        </HoverCard>
      </div>,
    );
    const trigger = screen.getByRole("button", { name: "Fix flaky test" });

    // Pointer 7 starts the gesture.
    fireEvent.pointerDown(trigger, { pointerId: 7, pointerType, button: 0, clientX: 10, clientY: 10 });
    expect(tally.net("scroll", true)).toBe(1);
    expect(tally.net("resize", false)).toBe(1);

    // Pointer 8 presses, moves, lifts and cancels on the same trigger; none of
    // it may replace pointer 7's start record, timer, listeners or suppression.
    fireEvent.pointerDown(trigger, { pointerId: 8, pointerType, button: 0, clientX: 12, clientY: 12 });
    fireEvent.pointerMove(trigger, { pointerId: 8, pointerType, clientX: 90, clientY: 90 });
    fireEvent.pointerUp(trigger, { pointerId: 8, pointerType, clientX: 90, clientY: 90 });
    fireEvent.pointerCancel(trigger, { pointerId: 8, pointerType });
    expect(tally.net("scroll", true)).toBe(1);
    expect(tally.net("resize", false)).toBe(1);
    expect(screen.queryByRole("tooltip")).toBeNull();

    // Pointer 7 still reveals the card at its original deadline.
    act(() => vi.advanceTimersByTime(LONG_PRESS_MS));
    expect(screen.getByRole("tooltip")).toBeTruthy();
    expect(tally.net("scroll", true)).toBe(2);
    expect(tally.net("resize", false)).toBe(2);

    // Its OWN end disposes the gesture pair and arms exactly one suppressed
    // compatibility click; the later ordinary activation still fires.
    fireEvent.pointerUp(trigger, { pointerId: 7, pointerType, clientX: 10, clientY: 10 });
    expect(tally.net("scroll", true)).toBe(1);
    fireEvent.click(trigger);
    expect(activate).not.toHaveBeenCalled();
    fireEvent.click(trigger);
    expect(activate).toHaveBeenCalledOnce();

    // A fresh gesture after the cancellation works, and a cancelled one
    // disposes its own pair.
    fireEvent.pointerDown(trigger, { pointerId: 7, pointerType, button: 0, clientX: 10, clientY: 10 });
    expect(tally.net("scroll", true)).toBe(1);
    fireEvent.pointerCancel(trigger, { pointerId: 7, pointerType });
    expect(tally.net("scroll", true)).toBe(0);
    fireEvent.pointerDown(trigger, { pointerId: 7, pointerType, button: 0, clientX: 10, clientY: 10 });
    expect(tally.net("scroll", true)).toBe(1);
    act(() => vi.advanceTimersByTime(LONG_PRESS_MS));
    expect(screen.getByRole("tooltip")).toBeTruthy();
    fireEvent.pointerUp(trigger, { pointerId: 7, pointerType });
    expect(tally.net("scroll", true)).toBe(1);
  } finally {
    tally.restore();
  }
});

test("a disabled long-press mode owns no cancellation listeners", () => {
  vi.useFakeTimers();
  installHoverlessMatchMedia();
  const tally = tallyEventTarget(window);
  try {
    render(
      <HoverCard label={<div>Project prime-radiant</div>}>
        <button type="button">Fix flaky test</button>
      </HoverCard>,
    );
    const trigger = screen.getByRole("button", { name: "Fix flaky test" });
    expect(tally.net("scroll", true)).toBe(0);
    expect(tally.net("resize", false)).toBe(0);

    fireEvent.pointerDown(trigger, { pointerId: 3, pointerType: "touch", button: 0, clientX: 10, clientY: 10 });
    expect(tally.net("scroll", true)).toBe(0);
    expect(tally.net("resize", false)).toBe(0);
    act(() => vi.advanceTimersByTime(LONG_PRESS_MS));
    expect(screen.queryByRole("tooltip")).toBeNull();
  } finally {
    tally.restore();
  }
});

test("unmounting during the press disposes the pending gesture listeners", () => {
  vi.useFakeTimers();
  installHoverlessMatchMedia();
  const tally = tallyEventTarget(window);
  try {
    const view = render(<LongPressCards count={1} />);
    const trigger = screen.getByRole("button", { name: `Fix flaky test ${TALLY_IDS[0]}` });
    fireEvent.pointerDown(trigger, { pointerId: 4, pointerType: "touch", button: 0, clientX: 10, clientY: 10 });
    expect(tally.net("scroll", true)).toBe(1);
    expect(tally.net("resize", false)).toBe(1);

    view.unmount();
    expect(tally.net("scroll", true)).toBe(0);
    expect(tally.net("resize", false)).toBe(0);
    act(() => vi.advanceTimersByTime(LONG_PRESS_MS));
    expect(screen.queryByRole("tooltip")).toBeNull();
  } finally {
    tally.restore();
  }
});

// --- popout realm (Dockview) ---------------------------------------------
// A Dockview popout is a separate Window with its own Document and its own
// constructors. jsdom's only real second realm is an iframe (a document from
// createHTMLDocument shares the opener's constructors and cannot catch a realm
// defect), so these cases render the card through a portal into the iframe's
// body: every element's ownerDocument is the popout document.

test("a long-press card in a popout document portals its bubble there and dismisses on a pointerdown in that document", () => {
  vi.useFakeTimers();
  installHoverlessMatchMedia();
  const { frame, doc } = appendPopout();
  const view = render(
    createPortal(
      <HoverCard label={<div>Project prime-radiant</div>} longPressEnabled>
        <button type="button">Fix flaky test</button>
      </HoverCard>,
      doc.body,
    ),
  );
  try {
    const trigger = doc.querySelector("button")!;
    fireEvent.pointerDown(trigger, { button: 0, clientX: 10, clientY: 10 });
    act(() => vi.advanceTimersByTime(LONG_PRESS_MS));

    const bubble = doc.querySelector('[role="tooltip"]');
    expect(bubble).not.toBeNull();
    expect(bubble?.parentElement).toBe(doc.body);
    expect(document.body.contains(bubble)).toBe(false);

    fireEvent.pointerDown(doc.body);

    expect(doc.querySelector('[role="tooltip"]')).toBeNull();
  } finally {
    view.unmount();
    frame.remove();
  }
});

test("places a popout card from the popout window's own viewport, not the opener's", () => {
  vi.useFakeTimers();
  const { frame, doc, win } = appendPopout();
  const CARD_WIDTH = 240;
  const CARD_HEIGHT = 96;
  // Deliberately different from the opener's 1024x768. 600x150 makes both
  // axes observable: 600 - 240 - 8 = 352 left, and a trigger at top 50 has
  // no room either side of it in a 150px-tall viewport, so the card clamps
  // to the 8px edge margin (the opener's 768px height would place it below
  // at 78px instead).
  const POPOUT_WIDTH = 600;
  const POPOUT_HEIGHT = 150;
  Object.defineProperty(win, "innerWidth", { configurable: true, value: POPOUT_WIDTH });
  Object.defineProperty(win, "innerHeight", { configurable: true, value: POPOUT_HEIGHT });

  const htmlProto = win.HTMLElement.prototype;
  const originalOffsetWidth = Object.getOwnPropertyDescriptor(htmlProto, "offsetWidth");
  const originalOffsetHeight = Object.getOwnPropertyDescriptor(htmlProto, "offsetHeight");
  const isBubble = (el: Element) => el.getAttribute("role") === "tooltip";
  Object.defineProperty(htmlProto, "offsetWidth", {
    configurable: true,
    get(this: HTMLElement) {
      return isBubble(this) ? CARD_WIDTH : 0;
    },
  });
  Object.defineProperty(htmlProto, "offsetHeight", {
    configurable: true,
    get(this: HTMLElement) {
      return isBubble(this) ? CARD_HEIGHT : 0;
    },
  });

  const view = render(
    createPortal(
      <HoverCard label={<div>Project prime-radiant</div>}>
        <button type="button">Fix flaky test</button>
      </HoverCard>,
      doc.body,
    ),
  );
  try {
    const trigger = doc.querySelector("button")!;
    // The wrapper span is what the card measures as its anchor; jsdom lays
    // nothing out, so supply the rect.
    (trigger.parentElement as HTMLElement).getBoundingClientRect = () => new DOMRect(950, 50, 40, 20);

    fireEvent.focus(trigger);
    act(() => vi.advanceTimersByTime(300));

    // Fall back to the opener's document so the pre-fix run reports the math
    // defect (the placement computed from the wrong viewport) rather than
    // just the missing portal.
    const bubble = (doc.querySelector('[role="tooltip"]') ??
      document.querySelector('[role="tooltip"]')) as HTMLElement | null;
    expect(bubble).not.toBeNull();
    expect(bubble?.style.left).toBe(`${POPOUT_WIDTH - CARD_WIDTH - 8}px`);
    expect(bubble?.style.top).toBe("8px");
    expect(bubble?.parentElement).toBe(doc.body);
  } finally {
    restoreOwnProperty(htmlProto, "offsetWidth", originalOffsetWidth);
    restoreOwnProperty(htmlProto, "offsetHeight", originalOffsetHeight);
    view.unmount();
    frame.remove();
  }
});

// Dockview can move a mounted card between windows without a render. A visible
// bubble has to follow: it belongs to the document its trigger is in now.
test("a visible card follows a move into another document", async () => {
  vi.useFakeTimers();
  installHoverlessMatchMedia();
  const { frame, doc, win } = appendPopout();
  Object.defineProperty(win, "innerWidth", { configurable: true, value: 600 });
  Object.defineProperty(win, "innerHeight", { configurable: true, value: 150 });
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  try {
    act(() =>
      root.render(
        <HoverCard label={<div>Project prime-radiant</div>} longPressEnabled>
          <button type="button">Fix flaky test</button>
        </HoverCard>,
      ),
    );
    const trigger = host.querySelector("button");
    if (!trigger) throw new Error("hovercard trigger missing");
    fireEvent.pointerDown(trigger, { pointerId: 4, pointerType: "touch", button: 0, clientX: 10, clientY: 10 });
    act(() => vi.advanceTimersByTime(LONG_PRESS_MS));
    expect(document.querySelector('[role="tooltip"]')).not.toBeNull();

    act(() => {
      doc.body.appendChild(host);
    });
    await act(async () => {});

    const bubble = doc.querySelector('[role="tooltip"]');
    expect(bubble).not.toBeNull();
    expect(bubble?.parentElement).toBe(doc.body);
    expect(document.querySelector('[role="tooltip"]')).toBeNull();
  } finally {
    act(() => root.unmount());
    frame.remove();
    host.remove();
  }
});

// The shared pointer tracker is acquired per document. After a move, a tap in
// the destination document must still read as a tap: otherwise the focus it
// takes is treated as keyboard focus and a hoverless card pops up unasked.
test("a hoverless tap after adoption is still a tap, not keyboard focus", async () => {
  vi.useFakeTimers();
  installHoverlessMatchMedia();
  const { frame, doc } = appendPopout();
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  try {
    act(() =>
      root.render(
        <HoverCard label={<div>Project prime-radiant</div>} longPressEnabled>
          <button type="button">Fix flaky test</button>
        </HoverCard>,
      ),
    );
    act(() => {
      doc.body.appendChild(host);
    });
    await act(async () => {});

    const trigger = doc.querySelector("button");
    if (!trigger) throw new Error("popout trigger missing");
    fireEvent.pointerDown(trigger, { pointerId: 7, pointerType: "touch", button: 0 });
    fireEvent.pointerUp(trigger, { pointerId: 7, pointerType: "touch", button: 0 });
    fireEvent.focus(trigger);
    act(() => vi.advanceTimersByTime(SHOW_DELAY_MS));

    expect(doc.querySelector('[role="tooltip"]')).toBeNull();
  } finally {
    act(() => root.unmount());
    frame.remove();
    host.remove();
  }
});

// A card can mount in a popout in the same instant as the tap that focused its
// row. Its mount-time focus check has to read that popout's pointer tracker,
// not the opener's, or the tap reads as keyboard focus and the card opens.
test("a card mounted in a popout by a tap does not open on that tap's focus", async () => {
  vi.useFakeTimers();
  installHoverlessMatchMedia();
  const { frame, doc } = appendPopout();
  const firstHost = document.createElement("div");
  const secondHost = document.createElement("div");
  doc.body.append(firstHost, secondHost);
  const firstRoot = createRoot(firstHost);
  const secondRoot = createRoot(secondHost);
  try {
    // One card already owns this document's pointer tracker.
    act(() =>
      firstRoot.render(
        <HoverCard label={<div>first</div>} longPressEnabled>
          <button type="button">First</button>
        </HoverCard>,
      ),
    );
    const outside = doc.createElement("div");
    doc.body.append(outside);
    fireEvent.pointerDown(outside, { pointerId: 9, pointerType: "touch", button: 0 });
    fireEvent.pointerUp(outside, { pointerId: 9, pointerType: "touch", button: 0 });
    const row = doc.createElement("div");
    row.tabIndex = 0;
    row.setAttribute("data-testid", "tap-row");
    doc.body.append(row);
    act(() => row.focus());

    act(() =>
      secondRoot.render(
        <HoverCard
          label={<div>second</div>}
          focusTarget={() => doc.querySelector<HTMLElement>('[data-testid="tap-row"]')}
          longPressEnabled
        >
          <button type="button">Second</button>
        </HoverCard>,
      ),
    );
    act(() => vi.advanceTimersByTime(SHOW_DELAY_MS));

    expect(doc.querySelector('[role="tooltip"]')).toBeNull();
  } finally {
    act(() => firstRoot.unmount());
    act(() => secondRoot.unmount());
    frame.remove();
  }
});

// A long press can outlive a document move. Its cancellation pair has to move
// with the card, or a scroll in the destination window never ends the press
// and the timer opens a card the person scrolled away from.
test("a scroll in the destination window cancels a press that crossed a move", async () => {
  vi.useFakeTimers();
  installHoverlessMatchMedia();
  const { frame, doc, win } = appendPopout();
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  try {
    act(() =>
      root.render(
        <HoverCard label={<div>Project prime-radiant</div>} longPressEnabled>
          <button type="button">Fix flaky test</button>
        </HoverCard>,
      ),
    );
    const trigger = host.querySelector("button");
    if (!trigger) throw new Error("hovercard trigger missing");
    fireEvent.pointerDown(trigger, { pointerId: 5, pointerType: "touch", button: 0, clientX: 5, clientY: 5 });

    act(() => {
      doc.body.appendChild(host);
    });
    await act(async () => {});

    act(() => win.dispatchEvent(new win.Event("scroll")));
    act(() => vi.advanceTimersByTime(LONG_PRESS_MS));

    expect(doc.querySelector('[role="tooltip"]')).toBeNull();
  } finally {
    act(() => root.unmount());
    frame.remove();
    host.remove();
  }
});

// A card mounted directly in a popout resolves its document after mount. Its
// very first press must already arm cancellation in that document.
test("the first press after mounting in a popout is cancelled by a scroll there", async () => {
  vi.useFakeTimers();
  installHoverlessMatchMedia();
  const { frame, doc, win } = appendPopout();
  const view = render(
    createPortal(
      <HoverCard label={<div>Project prime-radiant</div>} longPressEnabled>
        <button type="button">Fix flaky test</button>
      </HoverCard>,
      doc.body,
    ),
  );
  try {
    const trigger = doc.querySelector("button");
    if (!trigger) throw new Error("popout trigger missing");
    fireEvent.pointerDown(trigger, { pointerId: 6, pointerType: "touch", button: 0 });
    act(() => win.dispatchEvent(new win.Event("scroll")));
    act(() => vi.advanceTimersByTime(LONG_PRESS_MS));
    expect(doc.querySelector('[role="tooltip"]')).toBeNull();
  } finally {
    view.unmount();
    frame.remove();
  }
});
