import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { useRef, useState } from "react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { SIDEBAR_WIDTH_DEFAULT, SIDEBAR_WIDTH_MAX, SIDEBAR_WIDTH_MIN } from "../../stores/prefs";
import { RAIL_WIDTH_PROPERTY, RailResizeHandle } from "./RailResizeHandle";

// jsdom implements neither the Pointer Capture API nor any layout, so the two
// browser capabilities this component leans on are stubbed here (the same
// gap-filling shell/AppShell.test.tsx documents for ResizeObserver):
//   - setPointerCapture/releasePointerCapture are recorded so the tests can
//     assert the drag actually takes and gives back capture, which is what
//     makes a fast drag survive leaving the handle.
//   - the resized element's getBoundingClientRect is fixed at left=0 with a
//     width driven by whatever --rail-width the component last painted, so
//     "pointer at clientX=N" means "rail N pixels wide", exactly as in a
//     browser.
const captured: number[] = [];
const released: number[] = [];

beforeEach(() => {
  captured.length = 0;
  released.length = 0;
  Element.prototype.setPointerCapture = function setPointerCapture(pointerId: number) {
    captured.push(pointerId);
  };
  Element.prototype.releasePointerCapture = function releasePointerCapture(pointerId: number) {
    released.push(pointerId);
  };
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

// A minimal stand-in for Rail's own wrapper: the same railRef/--rail-width
// contract, plus the committed width surfaced for assertions. Deliberately not
// the real Rail - this file tests the handle's mechanics, and Rail.test.tsx
// covers the wiring.
function Harness({ initialWidth = SIDEBAR_WIDTH_DEFAULT }: { initialWidth?: number }) {
  const railRef = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(initialWidth);
  return (
    <div
      data-testid="fake-rail"
      ref={railRef}
      // biome-ignore lint/suspicious/noExplicitAny: a custom property is not in CSSProperties' key set
      style={{ [RAIL_WIDTH_PROPERTY]: `${width}px` } as any}
    >
      <span data-testid="committed">{width}</span>
      <RailResizeHandle width={width} onCommit={setWidth} railRef={railRef} />
    </div>
  );
}

function stubRailLayout(): void {
  const rail = screen.getByTestId("fake-rail");
  vi.spyOn(rail, "getBoundingClientRect").mockImplementation(() => {
    const painted = rail.style.getPropertyValue(RAIL_WIDTH_PROPERTY);
    const width = Number.parseFloat(painted) || SIDEBAR_WIDTH_DEFAULT;
    return { left: 0, right: width, width, top: 0, bottom: 0, height: 0, x: 0, y: 0, toJSON: () => ({}) } as DOMRect;
  });
}

function handle(): HTMLElement {
  return screen.getByTestId("rail-resize-handle");
}

function paintedWidth(): number {
  return Number.parseFloat(screen.getByTestId("fake-rail").style.getPropertyValue(RAIL_WIDTH_PROPERTY));
}

function committedWidth(): number {
  return Number(screen.getByTestId("committed").textContent);
}

function drag(...xs: number[]): void {
  fireEvent.pointerDown(handle(), { button: 0, pointerId: 7, clientX: paintedWidth() });
  for (const clientX of xs) fireEvent.pointerMove(handle(), { pointerId: 7, clientX });
  fireEvent.pointerUp(handle(), { pointerId: 7, clientX: xs[xs.length - 1] });
}

describe("accessibility contract", () => {
  // A resize separator has no HTML element of its own, so the whole contract is
  // ARIA + tabIndex - deliberately asserted attribute by attribute: a screen
  // reader user's only description of this control is what's below.
  test("is a focusable vertical separator with a name and a live value in its bounds", () => {
    render(<Harness />);
    const separator = handle();
    expect(separator.getAttribute("role")).toBe("separator");
    expect(separator.getAttribute("aria-orientation")).toBe("vertical");
    expect(separator.getAttribute("aria-label")).toBe("Resize sidebar");
    expect(separator.getAttribute("aria-valuenow")).toBe(String(SIDEBAR_WIDTH_DEFAULT));
    expect(separator.getAttribute("aria-valuemin")).toBe(String(SIDEBAR_WIDTH_MIN));
    expect(separator.getAttribute("aria-valuemax")).toBe(String(SIDEBAR_WIDTH_MAX));
    expect(separator.tabIndex).toBe(0);
  });

  test("is reachable by role and accessible name, not only by test id", () => {
    render(<Harness />);
    expect(screen.getByRole("separator", { name: "Resize sidebar" })).toBe(handle());
  });

  test("aria-valuenow follows the drag live, then settles on the released width", () => {
    render(<Harness />);
    stubRailLayout();
    fireEvent.pointerDown(handle(), { button: 0, pointerId: 1, clientX: 280 });
    fireEvent.pointerMove(handle(), { pointerId: 1, clientX: 360 });
    expect(handle().getAttribute("aria-valuenow")).toBe("360");
    fireEvent.pointerUp(handle(), { pointerId: 1, clientX: 360 });
    expect(handle().getAttribute("aria-valuenow")).toBe("360");
  });
});

describe("pointer drag", () => {
  test("widens the rail and commits the released width", () => {
    render(<Harness />);
    stubRailLayout();
    drag(340, 420);
    expect(paintedWidth()).toBe(420);
    expect(committedWidth()).toBe(420);
  });

  test("narrows the rail", () => {
    render(<Harness initialWidth={400} />);
    stubRailLayout();
    drag(240);
    expect(committedWidth()).toBe(240);
  });

  test("takes and releases pointer capture so a fast drag survives leaving the handle", () => {
    render(<Harness />);
    stubRailLayout();
    drag(330);
    expect(captured).toEqual([7]);
    expect(released).toEqual([7]);
  });

  test("clamps at both bounds no matter how far the pointer travels", () => {
    render(<Harness />);
    stubRailLayout();
    drag(4000);
    expect(committedWidth()).toBe(SIDEBAR_WIDTH_MAX);
    drag(-500);
    expect(committedWidth()).toBe(SIDEBAR_WIDTH_MIN);
  });

  test("commits once per gesture, not once per move", () => {
    const onCommit = vi.fn();
    function CountingHarness() {
      const railRef = useRef<HTMLDivElement>(null);
      return (
        <div data-testid="fake-rail" ref={railRef}>
          <RailResizeHandle width={SIDEBAR_WIDTH_DEFAULT} onCommit={onCommit} railRef={railRef} />
        </div>
      );
    }
    render(<CountingHarness />);
    stubRailLayout();
    fireEvent.pointerDown(handle(), { button: 0, pointerId: 2, clientX: 280 });
    for (const clientX of [300, 310, 320, 330]) fireEvent.pointerMove(handle(), { pointerId: 2, clientX });
    expect(onCommit).not.toHaveBeenCalled();
    fireEvent.pointerUp(handle(), { pointerId: 2, clientX: 330 });
    expect(onCommit).toHaveBeenCalledTimes(1);
    expect(onCommit).toHaveBeenCalledWith(330);
  });

  test("a move with no drag in progress is inert", () => {
    render(<Harness />);
    stubRailLayout();
    fireEvent.pointerMove(handle(), { pointerId: 3, clientX: 500 });
    expect(committedWidth()).toBe(SIDEBAR_WIDTH_DEFAULT);
  });

  test("ignores a non-primary button so a right-click still reaches the context menu", () => {
    render(<Harness />);
    stubRailLayout();
    fireEvent.pointerDown(handle(), { button: 2, pointerId: 4, clientX: 280 });
    fireEvent.pointerMove(handle(), { pointerId: 4, clientX: 500 });
    expect(committedWidth()).toBe(SIDEBAR_WIDTH_DEFAULT);
    expect(captured).toEqual([]);
  });

  test("a cancelled pointer (e.g. an OS gesture takeover) commits where the drag got to", () => {
    render(<Harness />);
    stubRailLayout();
    fireEvent.pointerDown(handle(), { button: 0, pointerId: 5, clientX: 280 });
    fireEvent.pointerMove(handle(), { pointerId: 5, clientX: 350 });
    fireEvent.pointerCancel(handle(), { pointerId: 5 });
    expect(committedWidth()).toBe(350);
    expect(released).toEqual([5]);
  });
});

describe("keyboard operation", () => {
  test("ArrowRight widens and ArrowLeft narrows by one step", () => {
    render(<Harness />);
    fireEvent.keyDown(handle(), { key: "ArrowRight" });
    expect(committedWidth()).toBe(SIDEBAR_WIDTH_DEFAULT + 16);
    fireEvent.keyDown(handle(), { key: "ArrowLeft" });
    fireEvent.keyDown(handle(), { key: "ArrowLeft" });
    expect(committedWidth()).toBe(SIDEBAR_WIDTH_DEFAULT - 16);
  });

  test("Shift+Arrow takes a coarse step", () => {
    render(<Harness />);
    fireEvent.keyDown(handle(), { key: "ArrowRight", shiftKey: true });
    expect(committedWidth()).toBe(SIDEBAR_WIDTH_DEFAULT + 64);
  });

  test("Home and End jump to the two bounds", () => {
    render(<Harness />);
    fireEvent.keyDown(handle(), { key: "End" });
    expect(committedWidth()).toBe(SIDEBAR_WIDTH_MAX);
    fireEvent.keyDown(handle(), { key: "Home" });
    expect(committedWidth()).toBe(SIDEBAR_WIDTH_MIN);
  });

  test("stepping past a bound clamps rather than overshooting", () => {
    render(<Harness initialWidth={SIDEBAR_WIDTH_MIN} />);
    fireEvent.keyDown(handle(), { key: "ArrowLeft" });
    expect(committedWidth()).toBe(SIDEBAR_WIDTH_MIN);
  });

  test("an unrelated key changes nothing and keeps its own default behaviour", () => {
    render(<Harness />);
    const event = fireEvent.keyDown(handle(), { key: "Tab" });
    expect(event).toBe(true); // fireEvent returns false only when the handler preventDefaulted
    expect(committedWidth()).toBe(SIDEBAR_WIDTH_DEFAULT);
  });

  test("a resizing key is preventDefaulted so the page never scrolls instead", () => {
    render(<Harness />);
    expect(fireEvent.keyDown(handle(), { key: "ArrowRight" })).toBe(false);
  });
});

test("double-click resets to the default width", () => {
  render(<Harness initialWidth={SIDEBAR_WIDTH_MAX} />);
  fireEvent.doubleClick(handle());
  expect(committedWidth()).toBe(SIDEBAR_WIDTH_DEFAULT);
});
