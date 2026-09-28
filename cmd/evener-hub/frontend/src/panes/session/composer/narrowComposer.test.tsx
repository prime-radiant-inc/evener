// useNarrowComposer gates the composer's narrow layout (Jesse's 2026-09-28
// design ruling on the #1339 phone-width verb wrap): the control row that
// used to wrap a three-verb cluster below the phone-width boundary holds
// Send alone there, with Stop and Steer riding in the session menu instead.
// The boundary is the one the retired wrap rule measured (promptcard.module.css,
// issue #1339), so a docked pane squeezed to phone width inside a desktop
// viewport answers exactly like the phone does - the same invariant
// composer.module.css states for its own container queries.
//
// jsdom lays out no cascade and ships no ResizeObserver, so the wide default
// the suite relies on (every existing composer test renders the inline verbs)
// is pinned here explicitly, and the narrow side is driven through a
// callback-capturing stub - the sanctioned pattern (resizeObserverTestUtils'
// own header: "a test that needs to drive size changes keeps its own
// callback-capturing stub").
import { act, cleanup, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { COMPOSER_PHONE_MAX_WIDTH, useNarrowComposer } from "./narrowComposer";

// The hook's real host is Composer's root div; the probe is the same shape
// (a div the ref points at) without the rest of the composer.
function Probe({ mounted = true }: { mounted?: boolean }) {
  const [narrow, setElement] = useNarrowComposer();
  return mounted ? <div ref={setElement} data-testid="probe" data-narrow={narrow ? "1" : "0"} /> : null;
}

let fire: (width: number) => void;
let observed: Element | null;
const disconnectSpy = vi.fn();

beforeEach(() => {
  fire = () => {};
  observed = null;
  disconnectSpy.mockClear();
  class CapturingResizeObserver implements ResizeObserver {
    constructor(private readonly callback: ResizeObserverCallback) {}
    observe(target: Element): void {
      observed = target;
      fire = (width: number) => this.callback([{ contentRect: { width } } as ResizeObserverEntry], this);
    }
    unobserve(): void {}
    disconnect(): void {
      disconnectSpy();
    }
  }
  globalThis.ResizeObserver = CapturingResizeObserver as unknown as typeof ResizeObserver;
});

afterEach(() => {
  cleanup();
  // jsdom has no ResizeObserver of its own; hand the next test the same
  // baseline this file's default-state test asserts.
  delete (globalThis as { ResizeObserver?: typeof ResizeObserver }).ResizeObserver;
});

test("stays wide without any ResizeObserver, the default every existing composer test runs under", () => {
  delete (globalThis as { ResizeObserver?: typeof ResizeObserver }).ResizeObserver;
  render(<Probe />);
  expect(screen.getByTestId("probe").dataset.narrow).toBe("0");
});

test("observes the ref's own element, not some other box", () => {
  render(<Probe />);
  expect(observed).toBe(screen.getByTestId("probe"));
});

test("goes narrow at the phone-width boundary and wide again above it", () => {
  render(<Probe />);
  act(() => fire(COMPOSER_PHONE_MAX_WIDTH));
  expect(screen.getByTestId("probe").dataset.narrow).toBe("1");
  act(() => fire(COMPOSER_PHONE_MAX_WIDTH + 1));
  expect(screen.getByTestId("probe").dataset.narrow).toBe("0");
  act(() => fire(320));
  expect(screen.getByTestId("probe").dataset.narrow).toBe("1");
});

test("stops observing when its host unmounts", () => {
  const view = render(<Probe />);
  view.unmount();
  expect(disconnectSpy).toHaveBeenCalledOnce();
});

test("a zero-width box defers the gate to the observer's own delivery", () => {
  render(<Probe />);
  expect(screen.getByTestId("probe").dataset.narrow).toBe("0");
  act(() => fire(COMPOSER_PHONE_MAX_WIDTH));
  expect(screen.getByTestId("probe").dataset.narrow).toBe("1");
});

test("a measurable box seeds the gate before the observer's first delivery", () => {
  const rect = vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockReturnValue({ width: 320 } as DOMRect);
  render(<Probe />);
  expect(screen.getByTestId("probe").dataset.narrow).toBe("1");
  rect.mockRestore();
});

test("engages when the element first appears after the initial render", () => {
  const view = render(<Probe mounted={false} />);
  expect(screen.queryByTestId("probe")).toBeNull();
  view.rerender(<Probe mounted={true} />);
  act(() => fire(320));
  expect(screen.getByTestId("probe").dataset.narrow).toBe("1");
});
