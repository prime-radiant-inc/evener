import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { LoadOlderRow } from "./LoadOlderRow";

// jsdom implements no ResizeObserver at all (the same gap the row's earlier
// IntersectionObserver sentinel needed stubbed). This stub records every
// observed element and exposes a trigger, so a test can say "the port's
// geometry changed" explicitly rather than depending on layout jsdom never
// performs.
class StubResizeObserver {
  static instances: StubResizeObserver[] = [];
  observed: Element[] = [];
  disconnected = false;
  constructor(private readonly callback: ResizeObserverCallback) {
    StubResizeObserver.instances.push(this);
  }
  observe(el: Element): void {
    this.observed.push(el);
  }
  unobserve(el: Element): void {
    this.observed = this.observed.filter((target) => target !== el);
  }
  disconnect(): void {
    this.disconnected = true;
  }
  /** Fires the callback as if every observed element changed size. */
  resize(): void {
    this.callback(
      this.observed.map((target) => ({ target }) as ResizeObserverEntry),
      this as unknown as ResizeObserver,
    );
  }
}

function latestObserver(): StubResizeObserver {
  const last = StubResizeObserver.instances[StubResizeObserver.instances.length - 1];
  if (!last) throw new Error("no ResizeObserver was constructed");
  return last;
}

// A stand-in for the transcript's scroll port and its content. jsdom computes
// no layout, so the geometry the row reads is defined outright; `set` changes
// it the way a pane resize or a settling virtualizer would.
function scrollPort(scrollHeight: number, clientHeight: number) {
  const el = document.createElement("div");
  const content = document.createElement("div");
  el.appendChild(content);
  const set = (nextScrollHeight: number, nextClientHeight: number) => {
    Object.defineProperty(el, "scrollHeight", { value: nextScrollHeight, configurable: true });
    Object.defineProperty(el, "clientHeight", { value: nextClientHeight, configurable: true });
  };
  set(scrollHeight, clientHeight);
  return { el, set };
}

// The row always has a port in production; these tests give one a scrollHeight
// that overflows by default so the render-only cases below never auto-load.
const OVERFLOWING = () => scrollPort(5000, 500);

beforeEach(() => {
  StubResizeObserver.instances = [];
  vi.stubGlobal("ResizeObserver", StubResizeObserver);
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

test("watches the transcript's scroll port and its content, not a sentinel of its own", () => {
  const port = OVERFLOWING();
  render(<LoadOlderRow onLoad={() => {}} loading={false} error={null} scrollElement={() => port.el} />);

  const observer = latestObserver();
  expect(observer.observed).toEqual([port.el, port.el.firstElementChild]);
});

test("loads older turns when the port is not full", () => {
  const port = scrollPort(300, 500);
  const onLoad = vi.fn();
  render(<LoadOlderRow onLoad={onLoad} loading={false} error={null} scrollElement={() => port.el} />);

  // The mount check (a browser's ResizeObserver delivers this same one as its
  // initial notification for the observed targets).
  expect(onLoad).toHaveBeenCalledTimes(1);
});

// The reported bug: the row renders into FlowOverlay's non-scrolling top slot,
// so a sentinel of its own was inside the viewport at every scroll position
// and loaded a page on every open, however far the reader was from the top of
// history.
test("does not load while the transcript already overflows its port", () => {
  const port = OVERFLOWING();
  const onLoad = vi.fn();
  render(<LoadOlderRow onLoad={onLoad} loading={false} error={null} scrollElement={() => port.el} />);

  latestObserver().resize();

  expect(onLoad).not.toHaveBeenCalled();
});

// A transcript that overflows at mount can later fit: the pane grows, or the
// virtualizer's estimates settle to shorter real rows. Nothing else fires then
// - there is no scroll event to drive the near-top trigger - so the re-check
// is what keeps the older history reachable.
test("re-checks on a later geometry change and loads once the port is no longer full", () => {
  const port = OVERFLOWING();
  const onLoad = vi.fn();
  render(<LoadOlderRow onLoad={onLoad} loading={false} error={null} scrollElement={() => port.el} />);

  latestObserver().resize();
  expect(onLoad).not.toHaveBeenCalled();

  port.set(300, 500);
  latestObserver().resize();

  expect(onLoad).toHaveBeenCalledTimes(1);
});

test("a null scroll element never loads", () => {
  const onLoad = vi.fn();
  render(<LoadOlderRow onLoad={onLoad} loading={false} error={null} scrollElement={() => null} />);

  latestObserver().resize();

  expect(onLoad).not.toHaveBeenCalled();
});

// A port that is not there on the first read (a late-mounting list) changes no
// observed border box, so the observer alone would never notice it: the render
// effect is what re-points the observation.
test("re-points the observation at a port that appears on a later render", () => {
  const port = scrollPort(300, 500);
  const onLoad = vi.fn();
  let mounted = false;
  const { rerender } = render(
    <LoadOlderRow onLoad={onLoad} loading={false} error={null} scrollElement={() => (mounted ? port.el : null)} />,
  );
  expect(latestObserver().observed).toEqual([]);
  expect(onLoad).not.toHaveBeenCalled();

  mounted = true;
  rerender(
    <LoadOlderRow onLoad={onLoad} loading={false} error={null} scrollElement={() => (mounted ? port.el : null)} />,
  );

  expect(latestObserver().observed).toEqual([port.el, port.el.firstElementChild]);
});

// A transcript can hand back a different scroll element; watching the old one
// would stop the geometry re-checks silently.
test("re-points the observation when the transcript hands back a different port", () => {
  const first = scrollPort(5000, 500);
  const second = scrollPort(300, 500);
  const onLoad = vi.fn();
  let port = first.el;
  const { rerender } = render(<LoadOlderRow onLoad={onLoad} loading={false} error={null} scrollElement={() => port} />);
  expect(latestObserver().observed).toEqual([first.el, first.el.firstElementChild]);

  port = second.el;
  rerender(<LoadOlderRow onLoad={onLoad} loading={false} error={null} scrollElement={() => port} />);

  expect(latestObserver().observed).toEqual([second.el, second.el.firstElementChild]);
  latestObserver().resize();
  // The first port overflowed, this one does not: the load comes from the port
  // the row is watching NOW.
  expect(onLoad).toHaveBeenCalledTimes(1);
});

test("there is no 'load more' button to press - paging is automatic", () => {
  const port = OVERFLOWING();
  render(<LoadOlderRow onLoad={() => {}} loading={false} error={null} scrollElement={() => port.el} />);
  expect(screen.queryByRole("button")).toBeNull();
});

test("shows a quiet loading state while a page is in flight", () => {
  const port = OVERFLOWING();
  render(<LoadOlderRow onLoad={() => {}} loading={true} error={null} scrollElement={() => port.el} />);
  expect(screen.getByTestId("load-older-row").textContent).toMatch(/loading older turns/i);
});

test("idle with more history to fetch, it shows no banner at all", () => {
  const port = OVERFLOWING();
  render(<LoadOlderRow onLoad={() => {}} loading={false} error={null} scrollElement={() => port.el} />);
  expect(screen.queryByText(/older turns/i)).toBeNull();
});

test("a failed fetch surfaces inline, announced, with a Retry - never silently", () => {
  const port = OVERFLOWING();
  render(
    <LoadOlderRow
      onLoad={() => {}}
      loading={false}
      error="Couldn't load older turns: network error"
      scrollElement={() => port.el}
    />,
  );
  const alert = screen.getByRole("alert");
  expect(alert.textContent).toMatch(/couldn't load older turns/i);
  expect(alert.textContent).toMatch(/network error/i);
  expect(screen.getByRole("button", { name: "Retry" })).toBeTruthy();
});

// The row renders the sentence it is handed and adds nothing: useTranscript
// composes it, because only useTranscript holds the rejection and can tell a
// failed page fetch from the failed session resume behind it. A label re-added
// here would talk over that.
test("the row shows the caller's own sentence verbatim, adding no label of its own", () => {
  const port = OVERFLOWING();
  render(
    <LoadOlderRow
      onLoad={() => {}}
      loading={false}
      error="Couldn't start this session: fork/exec evener"
      scrollElement={() => port.el}
    />,
  );
  expect(screen.getByRole("alert").textContent).toBe("Couldn't start this session: fork/exec evener");
});

test("Retry calls onLoad", () => {
  const port = OVERFLOWING();
  const onLoad = vi.fn();
  render(<LoadOlderRow onLoad={onLoad} loading={false} error="network error" scrollElement={() => port.el} />);

  fireEvent.click(screen.getByTestId("load-older-retry"));

  expect(onLoad).toHaveBeenCalledTimes(1);
});

// Without this the still-too-short port would re-fire against a failing
// endpoint on every observation - an automatic retry loop nobody asked for.
test("while an error is showing, the geometry check stops auto-loading", () => {
  const port = scrollPort(300, 500);
  const onLoad = vi.fn();
  render(<LoadOlderRow onLoad={onLoad} loading={false} error="network error" scrollElement={() => port.el} />);
  latestObserver().resize();

  expect(onLoad).not.toHaveBeenCalled();
});

test("clearing the error re-arms the automatic trigger", () => {
  const port = scrollPort(300, 500);
  const onLoad = vi.fn();
  const { rerender } = render(
    <LoadOlderRow onLoad={onLoad} loading={false} error="network error" scrollElement={() => port.el} />,
  );
  latestObserver().resize();
  expect(onLoad).not.toHaveBeenCalled();

  rerender(<LoadOlderRow onLoad={onLoad} loading={false} error={null} scrollElement={() => port.el} />);
  latestObserver().resize();

  expect(onLoad).toHaveBeenCalledTimes(1);
});

test("the observer is torn down on unmount", () => {
  const port = scrollPort(300, 500);
  const { unmount } = render(
    <LoadOlderRow onLoad={() => {}} loading={false} error={null} scrollElement={() => port.el} />,
  );
  const observer = latestObserver();
  expect(observer.disconnected).toBe(false);

  unmount();

  expect(observer.disconnected).toBe(true);
});

// jsdom's shape: no ResizeObserver, so the one geometry check is all there is.
// This is what makes the pane suites page on their own.
test("checks once and still renders in an environment that has no ResizeObserver", () => {
  const port = scrollPort(300, 500);
  const onLoad = vi.fn();
  vi.unstubAllGlobals();
  vi.stubGlobal("ResizeObserver", undefined);

  render(<LoadOlderRow onLoad={onLoad} loading={false} error={null} scrollElement={() => port.el} />);

  expect(screen.getByTestId("load-older-row")).toBeTruthy();
  expect(onLoad).toHaveBeenCalledTimes(1);
});
