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
  unobserve(): void {}
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

beforeEach(() => {
  StubResizeObserver.instances = [];
  vi.stubGlobal("ResizeObserver", StubResizeObserver);
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

test("watches the transcript's scroll port and its content, not a sentinel of its own", () => {
  const port = scrollPort(5000, 500);
  render(<LoadOlderRow onLoad={() => {}} loading={false} error={null} scrollElement={() => port.el} />);

  const observer = latestObserver();
  expect(observer.observed).toEqual([port.el, port.el.firstElementChild]);
});

test("loads older turns when the port is not full", () => {
  const port = scrollPort(300, 500);
  const onLoad = vi.fn();
  render(<LoadOlderRow onLoad={onLoad} loading={false} error={null} scrollElement={() => port.el} />);

  latestObserver().resize();

  expect(onLoad).toHaveBeenCalledTimes(1);
});

// The reported bug: the row renders into FlowOverlay's non-scrolling top slot,
// so a sentinel of its own was inside the viewport at every scroll position
// and loaded a page on every open, however far the reader was from the top of
// history.
test("does not load while the transcript already overflows its port", () => {
  const port = scrollPort(5000, 500);
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
  const port = scrollPort(5000, 500);
  const onLoad = vi.fn();
  render(<LoadOlderRow onLoad={onLoad} loading={false} error={null} scrollElement={() => port.el} />);

  latestObserver().resize();
  expect(onLoad).not.toHaveBeenCalled();

  port.set(300, 500);
  latestObserver().resize();

  expect(onLoad).toHaveBeenCalledTimes(1);
});

test("with no scroll element accessor it observes nothing and never auto-loads", () => {
  const onLoad = vi.fn();
  render(<LoadOlderRow onLoad={onLoad} loading={false} error={null} />);

  expect(StubResizeObserver.instances).toHaveLength(0);
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

test("idle with more history to fetch, it shows no banner at all", () => {
  render(<LoadOlderRow onLoad={() => {}} loading={false} error={null} />);
  expect(screen.queryByText(/older turns/i)).toBeNull();
});

test("a failed fetch surfaces inline, announced, with a Retry - never silently", () => {
  render(<LoadOlderRow onLoad={() => {}} loading={false} error="Couldn't load older turns: network error" />);
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
  render(<LoadOlderRow onLoad={() => {}} loading={false} error="Couldn't start this session: fork/exec evener" />);
  expect(screen.getByRole("alert").textContent).toBe("Couldn't start this session: fork/exec evener");
});

test("Retry calls onLoad", () => {
  const onLoad = vi.fn();
  render(<LoadOlderRow onLoad={onLoad} loading={false} error="network error" />);

  fireEvent.click(screen.getByTestId("load-older-retry"));

  expect(onLoad).toHaveBeenCalledTimes(1);
});

// Without this the still-too-short port would re-fire against a failing
// endpoint on every observation - an automatic retry loop nobody asked for.
test("while an error is showing, the geometry check stops auto-loading", () => {
  const port = scrollPort(300, 500);
  const onLoad = vi.fn();
  const { rerender } = render(
    <LoadOlderRow onLoad={onLoad} loading={false} error={null} scrollElement={() => port.el} />,
  );
  latestObserver().resize();
  expect(onLoad).toHaveBeenCalledTimes(1);

  rerender(<LoadOlderRow onLoad={onLoad} loading={false} error="network error" scrollElement={() => port.el} />);
  latestObserver().resize();

  expect(onLoad).toHaveBeenCalledTimes(1); // still just the first, pre-failure call
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

test("renders (without observing) in an environment that has no ResizeObserver", () => {
  vi.unstubAllGlobals();
  vi.stubGlobal("ResizeObserver", undefined);

  expect(() =>
    render(
      <LoadOlderRow
        onLoad={() => {}}
        loading={false}
        error={null}
        scrollElement={() => document.createElement("div")}
      />,
    ),
  ).not.toThrow();
  expect(screen.getByTestId("load-older-row")).toBeTruthy();
});
