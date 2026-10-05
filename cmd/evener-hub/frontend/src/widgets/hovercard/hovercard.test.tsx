import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import { HoverCard, LONG_PRESS_MS } from ".";
import { installHoverlessMatchMedia } from "./hovercardTestUtils";
import { POINTER_CLICK_WINDOW_MS } from "./useFloatingLabel";

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
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
