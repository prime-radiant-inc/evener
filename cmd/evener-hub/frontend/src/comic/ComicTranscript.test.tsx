import "fake-indexeddb/auto";
import {
  buildEntityView,
  hydrateThread,
  parseActivityTree,
  type ThreadTurnsListResponse,
} from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { wireThread } from "@evener/appwire-client/testing/notifications";
import {
  subagentOutcomesDelegatesResponse,
  subagentOutcomesResponse,
} from "@evener/appwire-client/testing/subagentWireFixtures";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { type ReactNode, useImperativeHandle, useRef } from "react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { pagingByRef } from "../panes/session/transcript/transcriptPaging";
import {
  resetTranscriptPagingForTests,
  type UseTranscriptResult,
  useTranscriptReader,
} from "../panes/session/transcript/useTranscript";
import { connectionStore } from "../stores/connection";
import { resetThreadsStoreForTests } from "../stores/threads";
import { useThreadModel } from "../stores/useThreadModel";
import * as widgets from "../widgets";
import { ComicTranscript } from "./ComicTranscript";
import * as comicRows from "./comicRows";
import { type ComicDelegateStrip, projectComicDelegates } from "./delegates";
import type { ComicEntry } from "./projection";
import { type ComicReadingPosition, readComicPosition, writeComicPosition } from "./readerPosition";
import { useComicReader } from "./useComicReader";
import { type ComicSubchatState, useComicSubchats } from "./useComicSubchats";

beforeEach(() => {
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    },
  );
  // jsdom has no layout. This seam exercises real VirtualList windowing, not geometry acceptance.
  vi.spyOn(HTMLElement.prototype, "offsetHeight", "get").mockReturnValue(500);
  vi.spyOn(HTMLElement.prototype, "offsetWidth", "get").mockReturnValue(760);
});
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  sessionStorage.clear();
  connectionStore.setState({ state: "idle", client: null });
  resetThreadsStoreForTests();
  resetTranscriptPagingForTests();
});

const entry: ComicEntry = {
  id: "m",
  sourceItemId: "source-m",
  sourceIndex: 0,
  kind: "dialogue",
  speaker: "human",
  sourceLabel: "FIXTURE_SOURCE",
  text: "literal fixture *text*",
  streaming: false,
};
function history(): UseTranscriptResult {
  return {
    model: undefined,
    hasOlder: false,
    loadOlder: vi.fn(async () => {}),
    loadingOlder: false,
    cancelOlder: vi.fn(),
    loadOlderReportingError: vi.fn(),
    olderError: null,
  };
}
function Reader({
  entries = [entry],
  mode = "comic",
  older,
  strips = [],
  subchats = { readers: new Map(), expandedPaths: [], expand() {}, collapse() {} },
}: {
  entries?: readonly ComicEntry[];
  mode?: "comic" | "plain";
  older: UseTranscriptResult;
  strips?: readonly ComicDelegateStrip[];
  subchats?: ComicSubchatState;
}) {
  const reader = useComicReader("local:test");
  return (
    <ComicTranscript
      root={{
        ref: "local:test",
        path: [],
        entries,
        history: older,
        activity: { snapshot: null, strips, entities: new Map(), context: {}, loadMore: async () => {} },
      }}
      subchats={subchats}
      reader={reader}
      mode={mode}
      onOpen={() => {}}
    />
  );
}
test("uses one scroll port through view changes and cancels restoration at latest", async () => {
  const older = history();
  const mounted = render(<Reader older={older} />);
  const port = mounted.container.querySelector("[data-comic-scroll]");
  expect(port).not.toBeNull();
  mounted.rerender(<Reader older={older} mode="plain" />);
  expect(mounted.container.querySelector("[data-comic-scroll]")).toBe(port);
  fireEvent.click(screen.getByRole("button", { name: /latest/i }));
  expect(older.cancelOlder).toHaveBeenCalled();
  mounted.unmount();
  expect(document.querySelector("[data-comic-probe]")).toBeNull();
});
test("looks up the end anchor once per render when presentation changes without changing rows", async () => {
  const entries = Array.from({ length: 40 }, (_, index) => ({ ...entry, id: `message-${index}`, sourceIndex: index }));
  const lookup = vi.spyOn(comicRows, "findComicFlowAnchorRow");
  const virtualList = vi.spyOn(widgets, "VirtualList");
  const mounted = render(<Reader entries={entries} older={history()} />);
  await waitFor(() => expect(mounted.container.querySelectorAll("[data-comic-row]").length).toBeGreaterThan(1));
  lookup.mockClear();
  virtualList.mockClear();

  fireEvent.change(screen.getByRole("combobox", { name: "Scene" }), { target: { value: "field" } });

  expect(virtualList).toHaveBeenCalled();
  expect(lookup).toHaveBeenCalledTimes(virtualList.mock.calls.length);
  expect(mounted.container.querySelectorAll("[data-comic-row]").length).toBeGreaterThan(1);
});

test("indexes each reader once and reuses its entries across visible rows and presentation changes", async () => {
  const model = hydrateThread({ thread: wireThread("local:root") }, "local:root", 0);
  const entities = buildEntityView({
    sessionRef: model.ref,
    tree: parseActivityTree(subagentOutcomesResponse().data) ?? undefined,
    turns: [],
    stale: false,
    ended: false,
  });
  const original = projectComicDelegates(model, entities)[0];
  if (!original) throw new Error("Fixture has no delegate");
  const strip = { ...original, ownerRef: "local:test" };
  const entries = [{ ...entry }, { ...entry, id: "second", sourceIndex: 1 }];
  const childEntry = { ...entry, text: "CHILD_FIRST" };
  const childSecond = { ...entry, id: "second", sourceIndex: 1, text: "CHILD_SECOND" };
  const childEntries = [childEntry, childSecond];
  const rootMap = vi.spyOn(entries, "map");
  const childMap = vi.spyOn(childEntries, "map");
  const child = {
    ref: strip.childRef,
    path: [strip.key],
    entries: childEntries,
    history: history(),
    activity: { snapshot: null, entities: new Map(), strips: [], context: {}, loadMore: async () => {} },
  };
  const subchats = {
    readers: new Map([[JSON.stringify(child.path), child]]),
    expandedPaths: [child.path],
    expand() {},
    collapse() {},
  };
  const older = history();
  const mounted = render(<Reader entries={entries} older={older} strips={[strip]} subchats={subchats} />);
  const checkContent = (rootText: string, childText: string) => {
    expect(
      mounted.container.querySelector('[data-comic-session="local:test"] [data-comic-entry="m"] [data-comic-text]')
        ?.textContent,
    ).toBe(rootText);
    expect(
      mounted.container.querySelector(
        `[data-comic-session="${strip.childRef}"] [data-comic-entry="m"] [data-comic-text]`,
      )?.textContent,
    ).toBe(childText);
  };
  await waitFor(() => checkContent(entry.text, childEntry.text));
  expect(rootMap).toHaveBeenCalledTimes(1);
  expect(childMap).toHaveBeenCalledTimes(1);
  rootMap.mockClear();
  childMap.mockClear();

  fireEvent.change(screen.getByRole("combobox", { name: "Scene" }), { target: { value: "field" } });

  expect(rootMap).not.toHaveBeenCalled();
  expect(childMap).not.toHaveBeenCalled();
  checkContent(entry.text, childEntry.text);

  const updatedEntry = { ...childEntry, text: "CHILD_UPDATED" };
  const changed = { ...child, entries: [updatedEntry, childSecond] };
  mounted.rerender(
    <Reader
      entries={entries}
      older={older}
      strips={[strip]}
      subchats={{ ...subchats, readers: new Map([[JSON.stringify(child.path), changed]]) }}
    />,
  );
  await waitFor(() => checkContent(entry.text, updatedEntry.text));
});

test("retains an absent saved anchor while shared pages advance, and stops on failure or user movement", async () => {
  writeComicPosition("local:test", {
    mode: "away",
    sessionRef: "local:test",
    path: [],
    entryId: "older",
    offset: 7,
    viewportOffset: 24,
  });
  const older = { ...history(), hasOlder: true };
  const mounted = render(<Reader older={older} />);
  await waitFor(() => expect(older.loadOlderReportingError).toHaveBeenCalledTimes(1));
  mounted.rerender(<Reader older={{ ...older, loadingOlder: true }} />);
  expect(older.loadOlderReportingError).toHaveBeenCalledTimes(1);
  mounted.rerender(
    <Reader older={{ ...older, olderError: "FIXTURE_FAILURE" }} entries={[{ ...entry, id: "page-two" }, entry]} />,
  );
  expect(older.loadOlderReportingError).toHaveBeenCalledTimes(1);
  fireEvent.click(screen.getByRole("button", { name: /retry/i }));
  expect(older.loadOlderReportingError).toHaveBeenCalledTimes(2);
  fireEvent.wheel(mounted.container.querySelector("[data-comic-scroll]")!.firstElementChild!);
  expect(older.cancelOlder).toHaveBeenCalled();
  mounted.unmount();
  sessionStorage.clear();
});

test("keeps itemless errors in distinct canonical groups through switching and prepending", async () => {
  const error: ComicEntry = {
    id: "turn-error:t0",
    sourceTurnId: "t0",
    kind: "caption",
    speaker: "source",
    sourceLabel: "FIXTURE_ERROR",
    text: "FIXTURE_FAILED_TURN",
    streaming: false,
  };
  const older = history();
  const mounted = render(<Reader entries={[entry, error]} older={older} />);
  await waitFor(() => expect(mounted.container.querySelector('[data-comic-turn-id="t0"]')).not.toBeNull());
  for (const mode of ["plain", "comic"] as const) {
    mounted.rerender(<Reader entries={[{ ...entry, id: "preceding" }, entry, error]} mode={mode} older={older} />);
    await waitFor(() =>
      expect(mounted.container.querySelector('[data-comic-entry="m"] [data-comic-text]')?.textContent).toBe(entry.text),
    );
    expect(
      mounted.container
        .querySelector('[data-comic-entry="turn-error:t0"]')
        ?.closest('section[aria-label="Turn errors"]'),
    ).not.toBeNull();
    expect(mounted.container.querySelector('[data-comic-entry="turn-error:t0"] [data-comic-text]')?.textContent).toBe(
      error.text,
    );
  }
  mounted.unmount();
});

test("keeps an item-backed point fixed with itemless errors across prepend, resize and views", async ({
  onTestFinished,
}) => {
  // Deterministic layout seam, NOT browser geometry evidence. These independently
  // chosen content coordinates change while the expected viewport point stays 24.
  let contentTop = 120;
  let width = 760;
  let port: HTMLElement | undefined;
  const registeredListeners = vi.spyOn(HTMLElement.prototype, "addEventListener");
  const observers = new Map<ResizeObserverCallback, Element>();
  vi.stubGlobal(
    "ResizeObserver",
    class {
      constructor(private callback: ResizeObserverCallback) {}
      observe(target: Element) {
        observers.set(this.callback, target);
      }
      unobserve() {}
      disconnect() {
        observers.delete(this.callback);
      }
    },
  );
  vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockImplementation(() => width);
  vi.spyOn(HTMLElement.prototype, "offsetWidth", "get").mockImplementation(() => width);
  vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockReturnValue(500);
  vi.spyOn(HTMLElement.prototype, "scrollHeight", "get").mockImplementation(function (this: HTMLElement) {
    return this.parentElement?.hasAttribute("data-comic-scroll")
      ? Number.parseFloat((this.firstElementChild as HTMLElement | null)?.style.height ?? "0")
      : 20;
  });
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    const height = this.style.height
      ? Number.parseFloat(this.style.height)
      : this.hasAttribute("data-index") || this.parentElement?.hasAttribute("data-comic-scroll")
        ? 500
        : 20;
    return new DOMRect(0, 0, width, height);
  });
  const originalScrollTo = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "scrollTo");
  Object.defineProperty(HTMLElement.prototype, "scrollTo", {
    configurable: true,
    value(this: HTMLElement, options: ScrollToOptions) {
      this.scrollTop = options.top ?? this.scrollTop;
    },
  });
  onTestFinished(() => {
    if (originalScrollTo) Object.defineProperty(HTMLElement.prototype, "scrollTo", originalScrollTo);
    else Reflect.deleteProperty(HTMLElement.prototype, "scrollTo");
  });
  const measuredOffsets: number[] = [];
  vi.spyOn(document, "createRange").mockImplementation(
    () =>
      ({
        setStart(_node: Node, offset: number) {
          measuredOffsets.push(offset);
        },
        setEnd() {},
        getBoundingClientRect: () => new DOMRect(0, contentTop - (port?.scrollTop ?? 0), 8, 20),
      }) as unknown as Range,
  );
  const position = {
    mode: "away" as const,
    sessionRef: "local:test",
    path: [],
    entryId: "m",
    offset: 7,
    viewportOffset: 24,
  };
  writeComicPosition("local:test", position);
  const error: ComicEntry = {
    id: "turn:t0:error",
    sourceTurnId: "t0",
    kind: "caption",
    speaker: "source",
    sourceLabel: "Turn error",
    text: "FIXTURE_ITEMLESS_FAILURE",
    streaming: false,
  };
  const older = history();
  const mounted = render(<Reader entries={[entry, error]} older={older} />);
  port = mounted.container.querySelector("[data-comic-scroll]")!.firstElementChild as HTMLElement;
  const check = async () => {
    await waitFor(() => expect(contentTop - port!.scrollTop).toBe(24));
    expect(readComicPosition("local:test")).toEqual(position);
    expect(measuredOffsets.at(-1)).toBe(7);
    const group = mounted.container.querySelector('section[aria-label="Turn errors"]');
    expect(group?.querySelector('[data-comic-entry="turn:t0:error"]')).not.toBeNull();
    expect(group?.querySelector('[data-comic-entry="m"]')).toBeNull();
  };
  await check();
  const prepended = [{ ...entry, id: "preceding", sourceItemId: "preceding", sourceIndex: -1 }, entry, error];
  contentTop = 320;
  mounted.rerender(<Reader entries={prepended} older={older} />);
  await check();
  contentTop = 440;
  width = 320;
  await act(async () => {
    for (const [callback, target] of observers) {
      callback(
        [{ target, borderBoxSize: [{ inlineSize: width, blockSize: 500 }] } as unknown as ResizeObserverEntry],
        {} as ResizeObserver,
      );
    }
  });
  await check();
  contentTop = 200;
  mounted.rerender(<Reader entries={prepended} mode="plain" older={older} />);
  await check();
  contentTop = 440;
  mounted.rerender(<Reader entries={prepended} mode="comic" older={older} />);
  await check();
  fireEvent.click(screen.getByRole("button", { name: /latest/i }));
  expect(readComicPosition("local:test")?.mode).toBe("end");
  // A resize can clamp the DOM offset before the next committed layout. It is
  // not a reader gesture and must not convert end-following into an away anchor.
  contentTop = port.scrollTop + 124;
  port.scrollTop -= 100;
  // Deliver the reader's scroll observation before VirtualList's next commit,
  // matching the real resize failure rather than jsdom's synchronous act flush.
  const listenerIndex = registeredListeners.mock.calls.findIndex(
    ([type, listener], index) =>
      type === "scroll" &&
      registeredListeners.mock.contexts[index] === port &&
      typeof listener === "function" &&
      listener.name === "scroll",
  );
  expect(listenerIndex).toBeGreaterThanOrEqual(0);
  const onScroll = registeredListeners.mock.calls[listenerIndex]![1] as EventListener;
  act(() => onScroll.call(port!, new Event("scroll")));
  expect(readComicPosition("local:test")?.mode).toBe("end");
  fireEvent.wheel(port);
  port.scrollTop -= 10;
  act(() => onScroll.call(port!, new Event("scroll")));
  expect(readComicPosition("local:test")?.mode).toBe("away");
  mounted.unmount();
});

test("mounts bounded source rows rather than the whole loaded history", async () => {
  const entries = Array.from({ length: 500 }, (_, index) => ({ ...entry, id: `m-${index}`, sourceIndex: index }));
  const mounted = render(<Reader entries={entries} older={history()} />);
  await waitFor(() => expect(mounted.container.querySelectorAll("[data-comic-entry]").length).toBeGreaterThan(0));
  expect(mounted.container.querySelectorAll("[data-comic-entry]").length).toBeLessThan(30);
  mounted.unmount();
});

test.each(["invalid capture", "delayed backward key"] as const)(
  "does not revive a cancelled anchor after %s",
  async (ordering) => {
    // Consumer protocol seam: control validity and delivery of committed layouts,
    // not the widget implementation. Existing tests above retain the real widget.
    let valid = true;
    let publish: (() => void) | undefined;
    let port: HTMLDivElement;
    const place = vi.fn((offset: number) => {
      port.scrollTop = offset;
    });
    vi.spyOn(widgets, "VirtualList").mockImplementation(function ControlledList(props) {
      const element = useRef<HTMLDivElement>(null);
      useImperativeHandle(props.ref, () => ({
        getScrollElement: () => element.current,
        isLayoutCurrent: () => valid,
        getVisibleRange: () => ({ startIndex: 0, endIndex: props.count - 1 }),
        scrollToIndex: vi.fn(),
      }));
      publish = () => {
        port = element.current!;
        props.onLayout?.({
          virtualizer: { scrollElement: port } as widgets.CommittedVirtualListLayout["virtualizer"],
          isCurrent: () => valid,
          scrollToOffset: place,
          cancelPendingScroll: vi.fn(),
          syncReaderMovement: vi.fn(),
        });
      };
      return (
        <div ref={element}>
          {Array.from({ length: props.count }, (_, index) => (
            <div key={props.getItemKey?.(index) ?? index}>{props.renderRow(index)}</div>
          ))}
        </div>
      );
    });
    vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockReturnValue(500);
    vi.spyOn(HTMLElement.prototype, "scrollHeight", "get").mockReturnValue(2000);
    const tops: Record<string, number> = { m: 120, older: 900 };
    vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
      return new DOMRect(0, this.dataset.comicEntry ? tops[this.dataset.comicEntry]! - port.scrollTop : 0, 760, 20);
    });
    vi.spyOn(document, "createRange").mockImplementation(() => {
      let source: HTMLElement;
      return {
        setStart(node: Node) {
          source = node.parentElement!.closest<HTMLElement>("[data-comic-entry]")!;
        },
        setEnd() {},
        getBoundingClientRect: () => source.getBoundingClientRect(),
      } as unknown as Range;
    });
    if (ordering === "invalid capture") {
      writeComicPosition("local:test", {
        mode: "away",
        sessionRef: "local:test",
        path: [],
        entryId: "older",
        offset: 7,
        viewportOffset: 24,
      });
    } else {
      writeComicPosition("local:test", { mode: "end", sessionRef: "local:test", path: [] });
    }
    const older = { ...history(), hasOlder: true };
    const mounted = render(<Reader older={older} />);
    port = mounted.container.querySelector("[data-comic-scroll]")!.firstElementChild as HTMLDivElement;
    await waitFor(() => expect(port.querySelector('[data-comic-entry="m"]')).not.toBeNull());
    act(() => publish!());
    port.scrollTop = ordering === "invalid capture" ? 80 : 1500;
    place.mockClear();
    const frames = new Map<number, FrameRequestCallback>();
    let frameID = 0;
    vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => {
      frames.set(++frameID, callback);
      return frameID;
    });
    vi.stubGlobal("cancelAnimationFrame", (id: number) => frames.delete(id));
    const frame = () =>
      act(() => {
        const pending = [...frames.values()];
        frames.clear();
        for (const callback of pending) callback(0);
      });
    if (ordering === "invalid capture") {
      valid = false;
      fireEvent.wheel(port);
      port.scrollTop = 100;
    } else {
      fireEvent.keyDown(port, { key: "ArrowUp" });
    }
    frame(); // No replacement geometry, or no native movement yet.
    expect(older.cancelOlder).toHaveBeenCalledTimes(1);
    valid = true;
    mounted.rerender(<Reader older={older} entries={[entry, { ...entry, id: "older" }]} />);
    act(() => publish!()); // Late valid rows include the cancelled source.
    if (ordering === "delayed backward key") {
      port.scrollTop = 100; // Native key movement starts AFTER that frame/layout.
      fireEvent.scroll(port);
      act(() => publish!());
    }
    expect(place).not.toHaveBeenCalled();
    expect(port.scrollTop).toBe(100);
    expect(readComicPosition("local:test")).toMatchObject({ mode: "away", entryId: "m", viewportOffset: 20 });
    // A replacement point still participates in ordinary reflow restoration.
    tops.m = 220;
    act(() => publish!());
    expect(port.scrollTop).toBe(200);
    fireEvent.keyDown(port, { key: "ArrowUp" });
    frame(); // Still no native movement when explicit navigation supersedes it.
    fireEvent.click(screen.getByRole("button", { name: /latest/i }));
    expect(port.scrollTop).toBe(1500);
    expect(readComicPosition("local:test")?.mode).toBe("end");
    mounted.unmount();
  },
);

async function mountGestureReader(
  atTop = false,
  nested?: { node: ReactNode; position: Extract<ComicReadingPosition, { mode: "away" }> },
) {
  // Deterministic consumer-protocol geometry, not native browser layout.
  let port: HTMLDivElement;
  const geometry = { sourceY: atTop ? 24 : 120, valid: true, height: 2000 };
  let publish: (() => void) | undefined;
  const place = vi.fn((offset: number) => {
    port.scrollTop = offset;
  });
  vi.spyOn(widgets, "VirtualList").mockImplementation(function ControlledList(props) {
    const element = useRef<HTMLDivElement>(null);
    useImperativeHandle(props.ref, () => ({
      getScrollElement: () => element.current,
      isLayoutCurrent: () => geometry.valid,
      getVisibleRange: () => ({ startIndex: 0, endIndex: props.count - 1 }),
      scrollToIndex: vi.fn(),
    }));
    publish = () => {
      port = element.current!;
      props.onLayout?.({
        virtualizer: { scrollElement: port } as widgets.CommittedVirtualListLayout["virtualizer"],
        isCurrent: () => geometry.valid,
        scrollToOffset: place,
        cancelPendingScroll: vi.fn(),
        syncReaderMovement: vi.fn(),
      });
    };
    return (
      <div ref={element}>
        {Array.from({ length: props.count }, (_, index) => (
          <div key={props.getItemKey?.(index) ?? index}>{props.renderRow(index)}</div>
        ))}
      </div>
    );
  });
  vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockReturnValue(500);
  vi.spyOn(HTMLElement.prototype, "scrollHeight", "get").mockImplementation(() => geometry.height);
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    return new DOMRect(0, this.dataset.comicEntry ? geometry.sourceY - port.scrollTop : 0, 760, 20);
  });
  vi.spyOn(document, "createRange").mockImplementation(() => {
    let source: HTMLElement;
    return {
      setStart(node: Node) {
        source = node.parentElement!.closest<HTMLElement>("[data-comic-entry]")!;
      },
      setEnd() {},
      getBoundingClientRect: () => source.getBoundingClientRect(),
    } as unknown as Range;
  });
  const position = nested?.position ?? {
    mode: "away" as const,
    sessionRef: "local:test",
    path: [],
    entryId: "m",
    offset: 7,
    viewportOffset: 24,
  };
  writeComicPosition("local:test", position);
  const older = history();
  const mounted = render(nested?.node ?? <Reader older={older} />);
  port = mounted.container.querySelector("[data-comic-scroll]")!.firstElementChild as HTMLDivElement;
  await waitFor(() =>
    expect(
      port.querySelector(`[data-comic-entry="${position.mode === "away" ? position.entryId : "m"}"]`),
    ).not.toBeNull(),
  );
  act(() => publish!());
  const frames = new Map<number, FrameRequestCallback>();
  let frameID = 0;
  vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => {
    frames.set(++frameID, callback);
    return frameID;
  });
  vi.stubGlobal("cancelAnimationFrame", (id: number) => frames.delete(id));
  const frame = () =>
    act(() => {
      const pending = [...frames.values()];
      frames.clear();
      for (const callback of pending) callback(0);
    });
  return { port, geometry, publish: () => act(() => publish!()), place, frame, older, mounted, position };
}

test.each([false, true])(
  "retains stationary pointer source across a layout publication: %s",
  async (publishDuringClick) => {
    const { port, geometry, publish, frame, older, mounted, position } = await mountGestureReader();
    expect(geometry.sourceY - port.scrollTop).toBe(position.viewportOffset);
    expect(readComicPosition("local:test")).toEqual(position);
    // The restored semantic offset is 7, but this committed line's first visible
    // offset is 0. A stationary gesture must retain the former, not recapture it.
    fireEvent.pointerDown(port);
    if (publishDuringClick) publish();
    fireEvent.pointerUp(port);
    frame();
    expect(older.cancelOlder).toHaveBeenCalledTimes(1);
    expect(port.scrollTop).toBe(96);
    expect(readComicPosition("local:test")).toEqual(position);
    geometry.valid = true;
    geometry.sourceY = 220;
    publish();
    expect(geometry.sourceY - port.scrollTop).toBe(position.viewportOffset);
    expect(readComicPosition("local:test")).toEqual(position);
    mounted.unmount();
  },
);

test("keeps touch pan cancellation pending until scrolling after an invalid-to-valid layout", async () => {
  const { port, geometry, publish, place, frame, older, mounted, position } = await mountGestureReader();
  place.mockClear();
  geometry.valid = false;
  fireEvent.pointerDown(port, { pointerType: "touch", pointerId: 7 });
  fireEvent.touchStart(port);
  fireEvent.pointerCancel(port, { pointerType: "touch", pointerId: 7 });
  frame(); // A browser may start panning after the first attempted capture.
  expect(older.cancelOlder).toHaveBeenCalled();
  expect(place).not.toHaveBeenCalled();
  geometry.sourceY = 220;
  geometry.valid = true;
  publish(); // No scroll yet: neither place the cancelled anchor nor capture this reflowed point.
  expect(place).not.toHaveBeenCalled();
  expect(readComicPosition("local:test")).toEqual(position);
  port.scrollTop = 76;
  fireEvent.scroll(port);
  publish();
  expect(readComicPosition("local:test")).toMatchObject({ mode: "away", entryId: "m", viewportOffset: 144 });
  geometry.sourceY = 320;
  publish();
  expect(port.scrollTop).toBe(176);
  mounted.unmount();
});

test.each(["wheel up", "touch no-op", "backward key"] as const)(
  "settles a completed %s at the top boundary before reflow",
  async (input) => {
    const { port, geometry, publish, frame, mounted, position } = await mountGestureReader(true);
    expect(port.scrollTop).toBe(0);
    expect(geometry.sourceY - port.scrollTop).toBe(position.viewportOffset);
    expect(readComicPosition("local:test")).toEqual(position);
    if (input === "wheel up") fireEvent.wheel(port, { deltaY: -120 });
    if (input === "touch no-op") {
      fireEvent.pointerDown(port, { pointerType: "touch", pointerId: 7 });
      fireEvent.touchStart(port);
      fireEvent.touchEnd(port);
      fireEvent.pointerUp(port, { pointerType: "touch", pointerId: 7 });
    }
    if (input === "backward key") {
      fireEvent.keyDown(port, { key: "ArrowUp" });
      fireEvent.keyUp(port, { key: "ArrowUp" });
    }
    frame();
    geometry.sourceY = 124;
    publish();
    expect(geometry.sourceY - port.scrollTop).toBe(position.viewportOffset);
    mounted.unmount();
  },
);

test("captures a completed pointer on the next valid layout without placing its cancelled source", async () => {
  const { port, geometry, publish, place, frame, mounted } = await mountGestureReader();
  fireEvent.pointerDown(port, { pointerId: 9, pointerType: "mouse" });
  geometry.valid = false;
  fireEvent.pointerUp(document.body, { pointerId: 9, pointerType: "mouse" });
  frame();
  place.mockClear();
  geometry.sourceY = 220;
  publish();
  expect(place).not.toHaveBeenCalled();
  geometry.valid = true;
  publish();
  expect(place).not.toHaveBeenCalled();
  expect(readComicPosition("local:test")).toMatchObject({ mode: "away", entryId: "m", viewportOffset: 124 });
  geometry.sourceY = 320;
  publish();
  expect(port.scrollTop).toBe(196);
  mounted.unmount();
});

test.each(["wheel", "key"])("ignores stale pointer release after newer %s intent", async (input) => {
  const { port, geometry, publish, place, frame, mounted, position } = await mountGestureReader();
  fireEvent.pointerDown(port, { pointerId: 9, pointerType: "mouse" });
  if (input === "wheel") fireEvent.wheel(port, { deltaY: -120 });
  else fireEvent.keyDown(port, { key: "PageUp" });
  fireEvent.pointerUp(document.body, { pointerId: 9, pointerType: "mouse" });
  frame();
  place.mockClear();
  geometry.sourceY = 220;
  publish();
  expect(place).not.toHaveBeenCalled();
  expect(readComicPosition("local:test")).toEqual(position);
  port.scrollTop = 76;
  fireEvent.scroll(port);
  geometry.sourceY = 320;
  publish();
  expect(port.scrollTop).toBe(176);
  mounted.unmount();
});

test.each(["wheel", "ArrowDown", "PageDown", "End", " "])(
  "settles bottom-boundary %s intent before content grows",
  async (input) => {
    const { port, geometry, publish, frame, mounted } = await mountGestureReader();
    fireEvent.click(screen.getByRole("button", { name: /latest/i }));
    expect(port.scrollTop).toBe(1500);
    if (input === "wheel") fireEvent.wheel(port, { deltaY: 120 });
    else fireEvent.keyDown(port, { key: input });
    frame();
    geometry.height = 2100;
    publish();
    expect(port.scrollTop).toBe(1600);
    expect(readComicPosition("local:test")).toMatchObject({ mode: "end" });
    mounted.unmount();
  },
);

test.each(["PageUp", "Home", " "])("settles top-boundary %s intent before reflow", async (key) => {
  const { port, geometry, publish, mounted } = await mountGestureReader(true);
  fireEvent.keyDown(port, { key, shiftKey: key === " " });
  geometry.sourceY = 124;
  publish();
  expect(port.scrollTop).toBe(100);
  mounted.unmount();
});

test("ignores another pointer's release and a cancelled touch's finger lift", async () => {
  const { port, geometry, publish, place, frame, mounted, position } = await mountGestureReader();
  fireEvent.pointerDown(port, { pointerId: 7, pointerType: "touch" });
  fireEvent.touchStart(port);
  fireEvent.pointerUp(document.body, { pointerId: 8, pointerType: "touch" });
  fireEvent.pointerCancel(port, { pointerId: 7, pointerType: "touch" });
  fireEvent.touchEnd(port);
  frame();
  place.mockClear();
  geometry.sourceY = 220;
  publish();
  expect(place).not.toHaveBeenCalled();
  expect(readComicPosition("local:test")).toEqual(position);
  port.scrollTop = 76;
  fireEvent.scroll(port);
  geometry.sourceY = 320;
  publish();
  expect(port.scrollTop).toBe(176);
  mounted.unmount();
});

test("fences pointer completion after latest and after reader retirement", async () => {
  const { port, geometry, publish, frame, mounted } = await mountGestureReader();
  fireEvent.pointerDown(port, { pointerId: 7, pointerType: "mouse" });
  fireEvent.click(screen.getByRole("button", { name: /latest/i }));
  geometry.height = 2100;
  fireEvent.pointerUp(document.body, { pointerId: 7, pointerType: "mouse" });
  frame();
  publish();
  expect(port.scrollTop).toBe(1600);
  expect(readComicPosition("local:test")).toMatchObject({ mode: "end" });
  fireEvent.pointerDown(port, { pointerId: 8, pointerType: "mouse" });
  mounted.unmount();
  writeComicPosition("local:test", { mode: "end", sessionRef: "local:test", path: [] });
  fireEvent.pointerUp(document.body, { pointerId: 8, pointerType: "mouse" });
  frame();
  expect(readComicPosition("local:test")).toEqual({ mode: "end", sessionRef: "local:test", path: [] });
});

test("settles an unavailable saved source when history is exhausted", async () => {
  writeComicPosition("local:test", {
    mode: "away",
    sessionRef: "local:test",
    path: [],
    entryId: "deleted",
    offset: 12,
    viewportOffset: 24,
  });
  const older = history();
  const mounted = render(<Reader older={older} />);
  await waitFor(() =>
    expect(readComicPosition("local:test")).toEqual({
      mode: "away",
      sessionRef: "local:test",
      path: [],
      entryId: "m",
      offset: 0,
      viewportOffset: 24,
    }),
  );
  expect(older.loadOlderReportingError).not.toHaveBeenCalled();
  mounted.unmount();
});

test("selection cancels missing-source demand before another page can be requested", async () => {
  writeComicPosition("local:test", {
    mode: "away",
    sessionRef: "local:test",
    path: [],
    entryId: "missing",
    offset: 12,
    viewportOffset: 24,
  });
  const older = { ...history(), hasOlder: true };
  const mounted = render(<Reader older={older} />);
  await waitFor(() => expect(older.loadOlderReportingError).toHaveBeenCalledTimes(1));
  const text = mounted.container.querySelector("[data-comic-text]")!;
  const range = document.createRange();
  range.selectNodeContents(text);
  document.getSelection()?.addRange(range);
  fireEvent(document, new Event("selectionchange"));
  expect(older.cancelOlder).toHaveBeenCalledTimes(1);
  mounted.rerender(<Reader older={older} entries={[{ ...entry, id: "page-two" }, entry]} />);
  expect(older.loadOlderReportingError).toHaveBeenCalledTimes(1);
  document.getSelection()?.removeAllRanges();
  mounted.unmount();
});

test("does not attach parent end to trailing child output", async () => {
  const model = hydrateThread({ thread: wireThread("local:root") }, "local:root", 0);
  const entities = buildEntityView({
    sessionRef: model.ref,
    tree: parseActivityTree(subagentOutcomesResponse().data) ?? undefined,
    turns: [],
    stale: false,
    ended: false,
  });
  const original = projectComicDelegates(model, entities)[0]!;
  const strip = { ...original, ownerRef: "local:test" };
  let follow: boolean | undefined;
  const real = widgets.VirtualList;
  vi.spyOn(widgets, "VirtualList").mockImplementation((props) => {
    follow = props.anchorToEnd;
    return real(props);
  });
  const child = {
    ref: strip.childRef,
    path: [strip.key],
    entries: [{ ...entry, id: "child" }],
    history: history(),
    activity: { snapshot: null, entities: new Map(), strips: [], context: {}, loadMore: async () => {} },
  };
  const subchats = {
    readers: new Map([[JSON.stringify(child.path), child]]),
    expandedPaths: [child.path],
    expand() {},
    collapse() {},
  };
  const mounted = render(<Reader older={history()} strips={[strip]} subchats={subchats} />);
  await waitFor(() => expect(follow).toBe(false));
  mounted.unmount();
});

test("restores only the saved child path and pages its absent source, not the parent", async () => {
  const model = hydrateThread({ thread: wireThread("local:root") }, "local:root", 0);
  const entities = buildEntityView({
    sessionRef: model.ref,
    tree: parseActivityTree(subagentOutcomesResponse().data) ?? undefined,
    turns: [],
    stale: false,
    ended: false,
  });
  const strips = projectComicDelegates(model, entities).map((strip) => ({ ...strip, ownerRef: "local:test" }));
  const target = strips[0]!;
  const position = {
    mode: "away" as const,
    sessionRef: target.childRef,
    path: [target.key],
    entryId: entry.id,
    offset: 7,
    viewportOffset: 24,
  };
  writeComicPosition("local:test", position);
  const parentHistory = { ...history(), hasOlder: true };
  const childHistory = { ...history(), hasOlder: true };
  const expand = vi.fn();
  const subchats: ComicSubchatState = { readers: new Map(), expandedPaths: [], expand, collapse: vi.fn() };
  const mounted = render(<Reader older={parentHistory} strips={strips} subchats={subchats} />);
  await waitFor(() => expect(expand).toHaveBeenCalledWith([], target));
  expect(expand.mock.calls.every(([, strip]) => strip.key === target.key)).toBe(true);
  const child = {
    ref: target.childRef,
    path: [target.key],
    entries: [],
    history: childHistory,
    activity: { snapshot: null, strips: [], entities: new Map(), context: {}, loadMore: async () => {} },
  };
  mounted.rerender(
    <Reader
      older={parentHistory}
      strips={strips}
      subchats={{ ...subchats, expandedPaths: [child.path], readers: new Map([[JSON.stringify(child.path), child]]) }}
    />,
  );
  await waitFor(() => expect(childHistory.loadOlderReportingError).toHaveBeenCalledTimes(1));
  expect(parentHistory.loadOlderReportingError).not.toHaveBeenCalled();
  const loaded = { ...child, entries: [entry], history: { ...childHistory, hasOlder: false } };
  mounted.rerender(
    <Reader
      older={parentHistory}
      strips={strips}
      subchats={{ ...subchats, expandedPaths: [child.path], readers: new Map([[JSON.stringify(child.path), loaded]]) }}
    />,
  );
  expect(readComicPosition("local:test")).toEqual(position);
  mounted.unmount();
});

test.each(["collapse", "reopen"] as const)(
  "nested %s supersedes a pointer awaiting a valid layout",
  async (boundary) => {
    const fixture = subagentOutcomesDelegatesResponse();
    const model = hydrateThread({ thread: wireThread("local:root") }, "local:root", 0);
    const entities = buildEntityView({
      sessionRef: model.ref,
      tree: parseActivityTree(subagentOutcomesResponse().data) ?? undefined,
      turns: [],
      stale: false,
      ended: false,
    });
    const original = projectComicDelegates(model, entities)[0]!;
    const strip = { ...original, ownerRef: "local:test", childRef: `local:toggle-child-${boundary}` };
    const leafRef = `local:toggle-leaf-${boundary}`;
    const leafKey = JSON.stringify([strip.childRef, "dlg_nested"]);
    const client = new FakeClient("ready");
    client.on("thread/read", (p) => ({
      thread: wireThread(p.ref, {
        turns: [
          {
            id: "t",
            status: "completed",
            itemsView: "full",
            items: [
              {
                id: p.ref === leafRef ? "leaf-answer" : "child-answer",
                type: "agentMessage",
                text: "LITERAL_NESTED_SOURCE",
              },
            ],
          },
        ],
      }),
    }));
    client.on("thread/unsubscribe", () => ({}));
    client.on("evener/thread/activity/read", (p) => ({
      context: { ...fixture.context, ref: p.ref },
      scope: "session",
      delegates: { known: true, total: p.ref === strip.childRef ? 1 : 0, active: 0, failed: 0, completed: 0 },
      jobs: { known: true, total: 0, active: 0, failed: 0, completed: 0 },
      watches: { known: true, total: 0, active: 0, failed: 0, completed: 0 },
    }));
    client.on("evener/thread/delegates/list", (p) => ({
      ...fixture,
      scope: "session",
      context: { ...fixture.context, ref: p.ref },
      delegates:
        p.ref === strip.childRef
          ? [{ ...fixture.delegates[0]!, ownerRef: strip.childRef, delegateId: "dlg_nested", childRef: leafRef }]
          : [],
    }));
    connectionStore.getState().connect(client);
    let children!: ComicSubchatState;
    const older = history();
    function Root() {
      const subchats = useComicSubchats("local:test", undefined);
      children = subchats.state;
      return (
        <>
          {subchats.lifetimes}
          <Reader older={older} strips={[strip]} subchats={subchats.state} />
        </>
      );
    }
    const position = {
      mode: "away" as const,
      sessionRef: leafRef,
      path: [strip.key, leafKey],
      entryId: "leaf-answer",
      offset: 7,
      viewportOffset: 24,
    };
    const { geometry, publish, frame, port, mounted } = await mountGestureReader(false, { node: <Root />, position });
    expect(children.readers.size).toBe(2);
    expect(readComicPosition("local:test")).toEqual(position);
    expect(geometry.sourceY - port.scrollTop).toBe(24);
    const button = () =>
      mounted.container.querySelector<HTMLButtonElement>(`[data-comic-child-ref="${leafRef}"] [aria-expanded]`)!;
    const click = (invalid: boolean) => {
      const target = button();
      fireEvent.pointerDown(target, { pointerId: 7, pointerType: "mouse" });
      geometry.valid = !invalid;
      fireEvent.pointerUp(target, { pointerId: 7, pointerType: "mouse" });
      fireEvent.click(target);
      geometry.valid = true;
      publish();
      frame();
    };
    click(boundary === "collapse");
    expect(children.readers.size).toBe(1);
    expect(readComicPosition("local:test")).toEqual({
      mode: "away",
      sessionRef: strip.childRef,
      path: [strip.key],
      entryId: leafKey,
      offset: 0,
      viewportOffset: 0,
    });
    expect(geometry.sourceY - port.scrollTop).toBe(0);
    click(boundary === "reopen");
    await waitFor(() => expect(children.readers.size).toBe(2));
    // Native focus leaves a collapsed selection in the disclosure, not selected
    // source text. Exercise its queued notification after the explicit toggle.
    expect(document.getSelection()?.isCollapsed).toBe(true);
    expect(document.getSelection()?.anchorNode).toBe(button());
    fireEvent(document, new Event("selectionchange"));
    publish();
    expect(readComicPosition("local:test")).toEqual(position);
    expect(geometry.sourceY - port.scrollTop).toBe(24);
    mounted.unmount();
  },
);

test.each(["continue", "cancel"] as const)(
  "saved child restoration owns the next accepted page only while active: %s",
  async (intent) => {
    const model = hydrateThread({ thread: wireThread("local:root") }, "local:root", 0);
    const entities = buildEntityView({
      sessionRef: model.ref,
      tree: parseActivityTree(subagentOutcomesResponse().data) ?? undefined,
      turns: [],
      stale: false,
      ended: false,
    });
    const strip = {
      ...projectComicDelegates(model, entities)[0]!,
      ownerRef: "local:test",
      childRef: `local:continuation-${intent}`,
    };
    const turn = (id: string) => ({
      id,
      status: "completed" as const,
      itemsView: "full" as const,
      items: [{ id, type: "agentMessage" as const, text: `SOURCE_${id}` }],
    });
    const client = new FakeClient("ready");
    client.on("thread/read", (p) => ({
      thread: wireThread(p.ref, { turns: [turn("current")] }),
      olderCursor: "96",
    }));
    client.on("thread/unsubscribe", () => ({}));
    const requests: string[] = [];
    let acceptFirst!: (page: ThreadTurnsListResponse) => void;
    client.on("thread/turns/list", (p) => {
      requests.push(p.cursor!);
      return requests.length === 1
        ? new Promise<ThreadTurnsListResponse>((resolve) => {
            acceptFirst = resolve;
          })
        : { data: [turn("saved-child")] };
    });
    connectionStore.getState().connect(client);
    const parentHistory = history();
    let childState!: ComicSubchatState;
    function Root() {
      const subchats = useComicSubchats("local:test", undefined);
      childState = subchats.state;
      return (
        <>
          {subchats.lifetimes}
          <Reader older={parentHistory} strips={[strip]} subchats={subchats.state} />
        </>
      );
    }
    const position = {
      mode: "away" as const,
      sessionRef: strip.childRef,
      path: [strip.key],
      entryId: "saved-child",
      offset: 4,
      viewportOffset: 24,
    };
    writeComicPosition("local:test", position);
    const mounted = render(<Root />);
    await waitFor(() => expect(requests).toEqual(["96"]));
    await waitFor(() => expect(childState.readers.get(JSON.stringify(position.path))?.history.loadingOlder).toBe(true));
    if (intent === "cancel")
      fireEvent.wheel(mounted.container.querySelector("[data-comic-scroll]")!.firstElementChild!, { deltaY: -100 });
    await act(async () => acceptFirst({ data: [turn("middle")], nextCursor: "56" }));
    await waitFor(() =>
      expect(childState.readers.get(JSON.stringify(position.path))?.history.loadingOlder).toBe(false),
    );
    expect(requests).toEqual(intent === "continue" ? ["96", "56"] : ["96"]);
    const child = childState.readers.get(JSON.stringify(position.path))!;
    expect(child.entries.some((value) => value.id === "middle")).toBe(true);
    expect(child.entries.some((value) => value.id === "saved-child")).toBe(intent === "continue");
    expect(parentHistory.loadOlderReportingError).not.toHaveBeenCalled();
    if (intent === "continue") expect(readComicPosition("local:test")).toEqual(position);
    mounted.unmount();
  },
);

test.each(["failure", "success"] as const)(
  "latest retires only Comic child demand before delayed %s",
  async (settlement) => {
    const model = hydrateThread({ thread: wireThread("local:root") }, "local:root", 0);
    const entities = buildEntityView({
      sessionRef: model.ref,
      tree: parseActivityTree(subagentOutcomesResponse().data) ?? undefined,
      turns: [],
      stale: false,
      ended: false,
    });
    const strips = projectComicDelegates(model, entities).map((strip) => ({
      ...strip,
      ownerRef: "local:test",
      childRef: `${strip.childRef}-${settlement}`,
    }));
    const strip = strips[0]!;
    const client = new FakeClient("ready");
    client.on("thread/read", (p) => ({
      thread: wireThread(p.ref, {
        turns: [
          {
            id: "current",
            status: "completed",
            itemsView: "full",
            items: [{ id: "child-loaded", type: "agentMessage", text: "CHILD_LOADED" }],
          },
        ],
      }),
      olderCursor: "child-older",
    }));
    client.on("thread/unsubscribe", () => ({}));
    let resolve!: (value: ThreadTurnsListResponse) => void;
    let reject!: (error: Error) => void;
    let attempts = 0;
    const page: ThreadTurnsListResponse = {
      data: [
        {
          id: "old",
          status: "completed",
          itemsView: "full",
          items: [{ id: "saved-child", type: "agentMessage", text: "SAVED_CHILD" }],
        },
      ],
    };
    client.on("thread/turns/list", () => {
      attempts++;
      return attempts === 1
        ? new Promise<ThreadTurnsListResponse>((yes, no) => {
            resolve = yes;
            reject = no;
          })
        : page;
    });
    connectionStore.getState().connect(client);
    let independent!: UseTranscriptResult;
    let independentID!: string;
    function Independent() {
      useThreadModel(strip.childRef);
      const reader = useComicReader(strip.childRef);
      independentID = reader.reader.id;
      independent = useTranscriptReader(strip.childRef, reader.reader);
      return null;
    }
    const parentHistory = history();
    function Root() {
      const subchats = useComicSubchats("local:test", undefined);
      return (
        <>
          {subchats.lifetimes}
          <Reader older={parentHistory} strips={strips} subchats={subchats.state} />
        </>
      );
    }
    writeComicPosition("local:test", {
      mode: "away",
      sessionRef: strip.childRef,
      path: [strip.key],
      entryId: "saved-child",
      offset: 4,
      viewportOffset: 24,
    });
    const other = render(<Independent />);
    const mounted = render(<Root />);
    await waitFor(() => expect(attempts).toBe(1));
    act(() => independent.loadOlderReportingError());
    const paging = pagingByRef.get(strip.childRef)!;
    expect(paging.paneConsumers.size).toBe(2);
    fireEvent.click(screen.getByRole("button", { name: /latest/i }));
    await act(async () => {
      if (settlement === "failure") reject(new Error("DELAYED_CHILD_FAILURE"));
      else resolve(page);
    });
    expect([...paging.paneConsumers.keys()]).toEqual([independentID]);
    expect(readComicPosition("local:test")).toEqual({ mode: "end", sessionRef: "local:test", path: [] });
    if (settlement === "failure") {
      expect(independent.olderError).not.toBeNull();
      act(() => independent.loadOlderReportingError());
      await waitFor(() => expect(independent.hasOlder).toBe(false));
    }
    expect(independent.model?.turns.some((turn) => turn.items.some((item) => item.id === "saved-child"))).toBe(true);
    mounted.rerender(<Root />);
    expect(attempts).toBe(settlement === "failure" ? 2 : 1);
    expect(parentHistory.loadOlderReportingError).not.toHaveBeenCalled();
    mounted.unmount();
    other.unmount();
  },
);
