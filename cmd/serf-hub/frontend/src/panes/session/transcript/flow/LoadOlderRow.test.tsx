import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { LoadOlderRow } from "./LoadOlderRow";

// jsdom implements no IntersectionObserver at all (same gap DockHost.test.tsx
// covers for ResizeObserver, and stubbed the same way). This stub records every
// observed element and exposes a trigger so a test can say "the sentinel came
// into view" explicitly, rather than depending on layout jsdom never performs.
class StubIntersectionObserver {
  static instances: StubIntersectionObserver[] = [];
  observed: Element[] = [];
  disconnected = false;
  constructor(
    private readonly callback: IntersectionObserverCallback,
    readonly options?: IntersectionObserverInit,
  ) {
    StubIntersectionObserver.instances.push(this);
  }
  observe(el: Element): void {
    this.observed.push(el);
  }
  unobserve(): void {}
  disconnect(): void {
    this.disconnected = true;
  }
  /** Fires the callback as if every observed element became visible. */
  enter(): void {
    this.callback(
      this.observed.map((target) => ({ target, isIntersecting: true }) as IntersectionObserverEntry),
      this as unknown as IntersectionObserver,
    );
  }
  /** Fires the callback with a non-intersecting entry (scrolled away). */
  leave(): void {
    this.callback(
      this.observed.map((target) => ({ target, isIntersecting: false }) as IntersectionObserverEntry),
      this as unknown as IntersectionObserver,
    );
  }
}

function latestObserver(): StubIntersectionObserver {
  const last = StubIntersectionObserver.instances[StubIntersectionObserver.instances.length - 1];
  if (!last) throw new Error("no IntersectionObserver was constructed");
  return last;
}

beforeEach(() => {
  StubIntersectionObserver.instances = [];
  vi.stubGlobal("IntersectionObserver", StubIntersectionObserver);
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

test("observes its own sentinel with a prefetch margin, so paging starts before the reader hits the top", () => {
  render(<LoadOlderRow onLoad={() => {}} loading={false} error={null} />);

  const observer = latestObserver();
  expect(observer.observed).toEqual([screen.getByTestId("load-older-sentinel")]);
  // A margin, not a bare 0 threshold: the fetch is in flight by the time the
  // reader arrives rather than starting when they get there.
  expect(observer.options?.rootMargin).toMatch(/\d+px/);
});

test("the sentinel coming into view loads older turns with no click at all", () => {
  const onLoad = vi.fn();
  render(<LoadOlderRow onLoad={onLoad} loading={false} error={null} />);

  latestObserver().enter();

  expect(onLoad).toHaveBeenCalledTimes(1);
});

test("a non-intersecting observation loads nothing", () => {
  const onLoad = vi.fn();
  render(<LoadOlderRow onLoad={onLoad} loading={false} error={null} />);

  latestObserver().leave();

  expect(onLoad).not.toHaveBeenCalled();
});

test("there is no 'load more' button to press - paging is automatic", () => {
  render(<LoadOlderRow onLoad={() => {}} loading={false} error={null} />);
  expect(screen.queryByRole("button")).toBeNull();
});

test("shows a quiet loading state while a page is in flight", () => {
  render(<LoadOlderRow onLoad={() => {}} loading={true} error={null} />);
  expect(screen.getByTestId("load-older-row").textContent).toMatch(/loading older turns/i);
});

test("idle with more history to fetch, it still says what it is - never an empty row", () => {
  render(<LoadOlderRow onLoad={() => {}} loading={false} error={null} />);
  expect(screen.getByTestId("load-older-row").textContent).toMatch(/older turns/i);
});

test("a failed fetch surfaces inline, announced, with a Retry - never silently", () => {
  render(<LoadOlderRow onLoad={() => {}} loading={false} error="Couldn't load older turns: network error" />);

  const alert = screen.getByRole("alert");
  expect(alert.textContent).toMatch(/couldn't load older turns/i);
  expect(alert.textContent).toMatch(/network error/);
  expect(screen.getByRole("button", { name: "Retry" })).toBeTruthy();
});

// The row renders the sentence it is handed and adds nothing: useTranscript
// composes it, because only useTranscript holds the rejection and can tell a
// failed page fetch from the failed session resume behind it. A label re-added
// here would talk over that.
test("the row shows the caller's own sentence verbatim, adding no label of its own", () => {
  render(<LoadOlderRow onLoad={() => {}} loading={false} error="Couldn't start this session: fork/exec serf" />);

  expect(screen.getByRole("alert").textContent).toBe("Couldn't start this session: fork/exec serf");
});

test("Retry calls onLoad", () => {
  const onLoad = vi.fn();
  render(<LoadOlderRow onLoad={onLoad} loading={false} error="network error" />);

  fireEvent.click(screen.getByTestId("load-older-retry"));

  expect(onLoad).toHaveBeenCalledTimes(1);
});

// Without this the still-visible sentinel would re-fire against a failing
// endpoint on every observation - an automatic retry loop nobody asked for.
test("while an error is showing, the sentinel stops auto-loading", () => {
  const onLoad = vi.fn();
  const { rerender } = render(<LoadOlderRow onLoad={onLoad} loading={false} error={null} />);
  latestObserver().enter();
  expect(onLoad).toHaveBeenCalledTimes(1);

  rerender(<LoadOlderRow onLoad={onLoad} loading={false} error="network error" />);
  latestObserver().enter();

  expect(onLoad).toHaveBeenCalledTimes(1); // still just the first, pre-failure call
});

test("clearing the error re-arms the automatic trigger", () => {
  const onLoad = vi.fn();
  const { rerender } = render(<LoadOlderRow onLoad={onLoad} loading={false} error="network error" />);
  latestObserver().enter();
  expect(onLoad).not.toHaveBeenCalled();

  rerender(<LoadOlderRow onLoad={onLoad} loading={false} error={null} />);
  latestObserver().enter();

  expect(onLoad).toHaveBeenCalledTimes(1);
});

test("the observer is torn down on unmount", () => {
  const { unmount } = render(<LoadOlderRow onLoad={() => {}} loading={false} error={null} />);
  const observer = latestObserver();
  expect(observer.disconnected).toBe(false);

  unmount();

  expect(observer.disconnected).toBe(true);
});

test("renders (without observing) in an environment that has no IntersectionObserver", () => {
  vi.unstubAllGlobals();
  vi.stubGlobal("IntersectionObserver", undefined);

  expect(() => render(<LoadOlderRow onLoad={() => {}} loading={false} error={null} />)).not.toThrow();
  expect(screen.getByTestId("load-older-row")).toBeTruthy();
});
