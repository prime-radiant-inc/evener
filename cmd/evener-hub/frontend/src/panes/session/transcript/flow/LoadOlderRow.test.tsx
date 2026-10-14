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

  // The stub stands in for a browser's initial notification for each observed
  // target.
  latestObserver().resize();

  expect(onLoad).toHaveBeenCalledTimes(1);
});

// The real entry-slice opening: the pane paints the background-pruned model
// first, so the virtualizer renders rows that measure zero height until its
// measurement lands. The initial geometry check must not read that transient
// empty render as a short transcript and page in history over the network.
// Once measurement lands the SAME callback re-decides: the content really is
// short, so paging resumes - proving the guard suppresses only the transient,
// not short-page paging itself.
test("does not load while rendered rows are unmeasured, then loads once they measure short", () => {
  const port = scrollPort(300, 500);
  const rows = [0, 1, 2].map(() => {
    const row = document.createElement("div");
    port.el.firstElementChild?.appendChild(row);
    return row;
  });
  const onLoad = vi.fn();
  render(<LoadOlderRow onLoad={onLoad} loading={false} error={null} scrollElement={() => port.el} />);

  latestObserver().resize();
  expect(onLoad).not.toHaveBeenCalled();

  // The virtualizer's measurement lands: every rendered row now carries height.
  for (const row of rows) Object.defineProperty(row, "offsetHeight", { configurable: true, value: 40 });
  latestObserver().resize();

  expect(onLoad).toHaveBeenCalledTimes(1);
});

// The browser's real transient: one row has measured (53px) while thirteen are
// still zero, so the sizer totals far less than the transcript really is. A
// single measured row must not make the port look honestly short.
test("does not load while only some rendered rows have measured", () => {
  const port = scrollPort(300, 500);
  const rows = [0, 1, 2, 3].map(() => {
    const row = document.createElement("div");
    port.el.firstElementChild?.appendChild(row);
    return row;
  });
  Object.defineProperty(rows[3]!, "offsetHeight", { configurable: true, value: 53 });
  const onLoad = vi.fn();
  render(<LoadOlderRow onLoad={onLoad} loading={false} error={null} scrollElement={() => port.el} />);

  latestObserver().resize();

  expect(onLoad).not.toHaveBeenCalled();
});

// A transcript with no rendered rows at all (empty, or all rows hidden) is not
// the transient-measurement state: the short-page check still decides, so a
// genuinely empty page can still fill itself rather than being stranded.
test("an empty port with no rendered rows still auto-loads when short", () => {
  const port = scrollPort(300, 500);
  const onLoad = vi.fn();
  render(<LoadOlderRow onLoad={onLoad} loading={false} error={null} scrollElement={() => port.el} />);

  latestObserver().resize();

  expect(onLoad).toHaveBeenCalledTimes(1);
});

// N1: the real renderer keeps a virtual wrapper for a projected turn with no
// entries, measured zero. That measured-zero row beside visible content is not
// the transient unmeasured paint, so the short page still fills itself. The
// zero row is marked exactly as VirtualList marks a measured item.
test("auto-loads when a measured zero-height row sits beside visible rows", () => {
  const port = scrollPort(300, 500);
  const emptyTurn = document.createElement("div");
  emptyTurn.setAttribute("data-measured", "");
  port.el.firstElementChild?.appendChild(emptyTurn);
  const visible = document.createElement("div");
  Object.defineProperty(visible, "offsetHeight", { configurable: true, value: 40 });
  port.el.firstElementChild?.appendChild(visible);
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
  // The port the row just started watching is short, so the check it gets now
  // is what fills the page.
  latestObserver().resize();
  expect(onLoad).toHaveBeenCalledTimes(1);
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
  const row = screen.getByTestId("load-older-row");
  expect(row.getAttribute("data-state")).toBe("loading");
  expect(row.textContent).not.toBe("");
});

test("idle with more history to fetch, it shows no banner at all", () => {
  const port = OVERFLOWING();
  render(<LoadOlderRow onLoad={() => {}} loading={false} error={null} scrollElement={() => port.el} />);
  const row = screen.getByTestId("load-older-row");
  expect(row.getAttribute("data-state")).toBe("idle");
  expect(row.textContent).toBe("");
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
