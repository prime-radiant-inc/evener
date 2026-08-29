import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import {
  createRef,
  type ReactElement,
  type ReactNode,
  type RefObject,
} from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { StoreApi, UseBoundStore } from "zustand";
import { create } from "zustand";
import type {
  MobileConversation,
  MobileTimelineItem,
} from "../../conversation/model";
import type {
  ConversationAnchor,
  ConversationSkin,
} from "../../live-concepts/conversation/contract";
import {
  VirtualTranscript,
  type VirtualTranscriptHandle,
} from "../../live-concepts/conversation/VirtualTranscript";
import type {
  ConceptId,
  ConversationDisplayItem,
  NarrativeDisplayItem,
} from "../../live-concepts/model";
import type { ContentSizeCategory } from "../../native/contract";
import { ConversationScreen } from "../../screens/ConversationScreen";
import type { ConversationState } from "../../state/conversation";
import type { NavigationState } from "../../state/navigation";
import { createPreferencesStore } from "../../state/preferences";
import {
  VariableHeightVirtualList,
  type VariableHeightVirtualListHandle,
} from "./VariableHeightVirtualList";

interface Item {
  readonly key: string;
  readonly height: number;
}

const VIEWPORT_HEIGHT = 360;
let dispatchProgrammaticScrollEvents = true;
let fallbackVirtualRowHeight = 72;

class DeterministicResizeObserver implements ResizeObserver {
  static readonly instances = new Set<DeterministicResizeObserver>();
  readonly targets = new Set<Element>();
  private readonly callback: ResizeObserverCallback;

  constructor(callback: ResizeObserverCallback) {
    this.callback = callback;
    DeterministicResizeObserver.instances.add(this);
  }

  observe(target: Element): void {
    this.targets.add(target);
  }

  unobserve(target: Element): void {
    this.targets.delete(target);
  }

  disconnect(): void {
    this.targets.clear();
    DeterministicResizeObserver.instances.delete(this);
  }

  static emitAll(): void {
    DeterministicResizeObserver.emitWhere(() => true);
  }

  static emitWhere(predicate: (target: Element) => boolean): void {
    for (const observer of DeterministicResizeObserver.instances) {
      const entries = [...observer.targets].filter(predicate).map((target) => {
        const rect = target.getBoundingClientRect();
        return {
          target,
          contentRect: rect,
          borderBoxSize: [{ inlineSize: rect.width, blockSize: rect.height }],
          contentBoxSize: [{ inlineSize: rect.width, blockSize: rect.height }],
          devicePixelContentBoxSize: [
            { inlineSize: rect.width, blockSize: rect.height },
          ],
        } as unknown as ResizeObserverEntry;
      });
      if (entries.length > 0) observer.callback(entries, observer);
    }
  }
}

const originalResizeObserver = globalThis.ResizeObserver;
const originalRect = HTMLElement.prototype.getBoundingClientRect;
const originalScrollTo = HTMLElement.prototype.scrollTo;
const originalOffsetHeight = Object.getOwnPropertyDescriptor(
  HTMLElement.prototype,
  "offsetHeight",
);
const originalOffsetWidth = Object.getOwnPropertyDescriptor(
  HTMLElement.prototype,
  "offsetWidth",
);
const originalClientHeight = Object.getOwnPropertyDescriptor(
  HTMLElement.prototype,
  "clientHeight",
);
const originalScrollHeight = Object.getOwnPropertyDescriptor(
  HTMLElement.prototype,
  "scrollHeight",
);

beforeEach(() => {
  dispatchProgrammaticScrollEvents = true;
  fallbackVirtualRowHeight = 72;
  globalThis.ResizeObserver = DeterministicResizeObserver;
  Object.defineProperty(HTMLElement.prototype, "offsetHeight", {
    configurable: true,
    get() {
      return this.getAttribute("role") === "feed" ||
        this.hasAttribute("data-virtual-list-scroll")
        ? VIEWPORT_HEIGHT
        : 0;
    },
  });
  Object.defineProperty(HTMLElement.prototype, "offsetWidth", {
    configurable: true,
    get() {
      return 390;
    },
  });
  Object.defineProperty(HTMLElement.prototype, "clientHeight", {
    configurable: true,
    get() {
      return this.getAttribute("role") === "feed" ||
        this.hasAttribute("data-virtual-list-scroll")
        ? VIEWPORT_HEIGHT
        : 0;
    },
  });
  Object.defineProperty(HTMLElement.prototype, "scrollHeight", {
    configurable: true,
    get() {
      if (
        this.getAttribute("role") !== "feed" &&
        !this.hasAttribute("data-virtual-list-scroll")
      )
        return 0;
      return Number.parseFloat(
        (this.firstElementChild as HTMLElement | null)?.style.height ?? "0",
      );
    },
  });
  HTMLElement.prototype.getBoundingClientRect = function () {
    const article = this.matches('[data-testid="virtual-transcript-row"]')
      ? this.querySelector<HTMLElement>("[data-height]")
      : null;
    const height = article
      ? Number(article.dataset.height)
      : this.matches('[data-testid="virtual-transcript-row"]')
        ? fallbackVirtualRowHeight
        : this.getAttribute("role") === "feed" ||
            this.hasAttribute("data-virtual-list-scroll")
          ? VIEWPORT_HEIGHT
          : 0;
    return {
      x: 0,
      y: 0,
      top: 0,
      left: 0,
      right: 390,
      bottom: height,
      width: 390,
      height,
      toJSON: () => ({}),
    };
  };
  HTMLElement.prototype.scrollTo = function (
    optionsOrX?: ScrollToOptions | number,
    y?: number,
  ): void {
    const top =
      typeof optionsOrX === "number"
        ? (y ?? 0)
        : (optionsOrX?.top ?? this.scrollTop);
    this.scrollTop = top;
    if (dispatchProgrammaticScrollEvents) {
      this.dispatchEvent(new Event("scroll"));
    }
  };
});

afterEach(() => {
  cleanup();
  DeterministicResizeObserver.instances.clear();
  globalThis.ResizeObserver = originalResizeObserver;
  HTMLElement.prototype.getBoundingClientRect = originalRect;
  HTMLElement.prototype.scrollTo = originalScrollTo;
  if (originalOffsetHeight === undefined) {
    Reflect.deleteProperty(HTMLElement.prototype, "offsetHeight");
  } else {
    Object.defineProperty(
      HTMLElement.prototype,
      "offsetHeight",
      originalOffsetHeight,
    );
  }
  if (originalOffsetWidth === undefined) {
    Reflect.deleteProperty(HTMLElement.prototype, "offsetWidth");
  } else {
    Object.defineProperty(
      HTMLElement.prototype,
      "offsetWidth",
      originalOffsetWidth,
    );
  }
  if (originalClientHeight === undefined) {
    Reflect.deleteProperty(HTMLElement.prototype, "clientHeight");
  } else {
    Object.defineProperty(
      HTMLElement.prototype,
      "clientHeight",
      originalClientHeight,
    );
  }
  if (originalScrollHeight === undefined) {
    Reflect.deleteProperty(HTMLElement.prototype, "scrollHeight");
  } else {
    Object.defineProperty(
      HTMLElement.prototype,
      "scrollHeight",
      originalScrollHeight,
    );
  }
});

function items(count: number, height = 72): Item[] {
  return Array.from({ length: count }, (_, index) => ({
    key: `item-${index}`,
    height,
  }));
}

function bounded(text: string) {
  return { text, truncated: false, originalUtf8Bytes: text.length };
}

function displayNarrative(
  key: string,
  sourceKind: NarrativeDisplayItem["sourceKind"] = "assistant",
  body = key,
  streaming = false,
): NarrativeDisplayItem {
  return {
    key,
    sourceKind,
    body: bounded(body),
    label: null,
    tone: "idle",
    streaming,
    questionKey: null,
    evidenceKey: null,
    sequence: key,
  };
}

function displayMarker(
  key: string,
  state: "running" | "completed" = "running",
): ConversationDisplayItem {
  return {
    key,
    sourceKind: "tool",
    semanticKind: "tool",
    label: bounded(key),
    preview: null,
    duration: null,
    tone: state === "running" ? "running" : "success",
    state,
    evidenceKey: null,
    sequence: key,
  };
}

const measuredSkin: ConversationSkin = {
  id: "stillwater",
  className: "measured-skin",
  composerAppearance: { density: "compact", accent: "forest" },
  renderNarrativeItem: ({ item, body }) => (
    <div
      data-height={
        item.sourceKind === "user"
          ? 72
          : item.body.text.includes("grown")
            ? 178
            : 131
      }
    >
      {body}
    </div>
  ),
  renderActivityMarker: ({ item }) => (
    <div data-height={item.state === "completed" ? 252 : 219}>
      {item.label.text}
    </div>
  ),
  renderConversationChrome: () => null,
};

function renderList({
  values = items(500),
  threadKey = "thread-variable-list",
  skinId = "stillwater",
  contentSize = "large",
  ref = createRef<VariableHeightVirtualListHandle>(),
  estimate = 40,
  renderRow = (item: Item) => <div data-height={item.height}>{item.key}</div>,
}: {
  values?: readonly Item[];
  threadKey?: string;
  skinId?: ConceptId;
  contentSize?: ContentSizeCategory;
  ref?: RefObject<VariableHeightVirtualListHandle | null>;
  estimate?: number | ((item: Item) => number);
  renderRow?: (item: Item) => ReactNode;
} = {}): {
  readonly ref: RefObject<VariableHeightVirtualListHandle | null>;
  readonly rerenderList: (
    next: readonly Item[],
    nextSkin?: ConceptId,
    nextSize?: ContentSizeCategory,
    nextThread?: string,
  ) => void;
  readonly unmount: () => void;
} {
  let activeSkin = skinId;
  let activeSize = contentSize;
  let activeThread = threadKey;
  const element = (next: readonly Item[]): ReactElement => (
    <VariableHeightVirtualList
      ref={ref}
      items={next}
      getItemKey={(item) => item.key}
      estimateSize={(item) =>
        typeof estimate === "number" ? estimate : estimate(item)
      }
      cacheScope={{
        threadKey: activeThread,
        skinId: activeSkin,
        contentSize: activeSize,
      }}
      overscan={6}
      maxMountedRows={48}
      onScroll={() => {}}
      renderItem={renderRow}
    />
  );
  const view = render(element(values));
  return {
    ref,
    rerenderList(
      next,
      nextSkin = activeSkin,
      nextSize = activeSize,
      nextThread = activeThread,
    ) {
      activeSkin = nextSkin;
      activeSize = nextSize;
      activeThread = nextThread;
      view.rerender(element(next));
    },
    unmount: view.unmount,
  };
}

async function emitMeasurements(): Promise<void> {
  act(() => DeterministicResizeObserver.emitAll());
  await waitFor(() =>
    expect(
      screen.getAllByTestId("virtual-transcript-row").length,
    ).toBeGreaterThan(0),
  );
}

function emitMeasurementWhere(predicate: (target: Element) => boolean): void {
  act(() => DeterministicResizeObserver.emitWhere(predicate));
}

function rowStart(key: string): number {
  const row = screen
    .getByText(key)
    .closest<HTMLElement>('[data-testid="virtual-transcript-row"]');
  if (row === null) throw new Error(`missing row ${key}`);
  const match = row.style.transform.match(/translateY\(([-\d.]+)px\)/);
  if (match?.[1] === undefined) throw new Error(`missing transform for ${key}`);
  return Number(match[1]);
}

async function navigateToMountedRow(
  feed: HTMLElement,
  key: string,
  estimatedStart: number,
): Promise<void> {
  act(() => {
    feed.scrollTop = estimatedStart;
    fireEvent.scroll(feed);
  });
  await waitFor(() => expect(screen.getByText(key)).toBeInTheDocument());
}

async function promiseSettled(promise: Promise<unknown>): Promise<boolean> {
  return Promise.race([promise.then(() => true), Promise.resolve(false)]);
}

async function focusKey(
  ref: RefObject<VariableHeightVirtualListHandle | null>,
  key: string,
): Promise<void> {
  act(() => ref.current?.focusKey(key));
  await waitFor(() =>
    expect(
      screen.getByText(key).closest('[data-testid="virtual-transcript-row"]'),
    ).toHaveFocus(),
  );
}

describe("VariableHeightVirtualList", () => {
  it("preserves default feed, page owner, scoped cache, and semantic article rows", async () => {
    renderList({ values: items(3) });
    await emitMeasurements();

    const feed = screen.getByRole("feed", { name: "Conversation transcript" });
    expect(feed).toHaveAttribute("data-page-scroll-owner", "true");
    expect(feed).not.toHaveAttribute("inert");
    expect(screen.getAllByRole("article")).toHaveLength(3);
    expect(screen.getAllByRole("article")[2]).toHaveAttribute(
      "aria-posinset",
      "3",
    );
    expect(screen.getAllByRole("article")[2]).toHaveAttribute(
      "aria-setsize",
      "3",
    );
  });

  it("supports explicit caller-owned rows and custom locked scroll semantics", async () => {
    render(
      <VariableHeightVirtualList
        items={items(3)}
        getItemKey={(item) => item.key}
        estimateSize={(item) => item.height}
        measurementCache={{ mode: "ephemeral" }}
        rowSemantics={{ mode: "caller-owned" }}
        scrollSemantics={{
          mode: "custom",
          role: "region",
          ariaLabel: "Evidence sections",
          pageScrollOwner: false,
          locked: true,
        }}
        overscan={6}
        maxMountedRows={48}
        onScroll={() => {}}
        renderItem={(item) => (
          <section aria-label={`Section ${item.key}`} data-height={item.height}>
            {item.key}
          </section>
        )}
      />,
    );
    await emitMeasurements();

    const detail = document.querySelector<HTMLElement>(
      '[data-virtual-list-scroll="true"]',
    );
    expect(detail).not.toBeNull();
    if (detail === null) throw new Error("custom scroller missing");
    expect(detail).toHaveAttribute("aria-label", "Evidence sections");
    expect(detail).not.toHaveAttribute("data-page-scroll-owner");
    expect(detail).toHaveAttribute("inert");
    expect(detail).toHaveAttribute("aria-hidden", "true");
    expect(detail).toHaveAttribute("data-scroll-locked", "true");
    expect(screen.queryAllByRole("article")).toHaveLength(0);
    expect(detail.querySelectorAll("section")).toHaveLength(3);
  });

  it("keeps ephemeral measurements private to each mounted list instance", async () => {
    const element = (height: number) => (
      <VariableHeightVirtualList
        items={items(20, height)}
        getItemKey={(item) => item.key}
        estimateSize={() => 40}
        measurementCache={{ mode: "ephemeral" }}
        rowSemantics={{ mode: "caller-owned" }}
        scrollSemantics={{
          mode: "custom",
          role: "region",
          ariaLabel: "Ephemeral evidence",
          pageScrollOwner: true,
          locked: false,
        }}
        overscan={6}
        maxMountedRows={48}
        onScroll={() => {}}
        renderItem={(item) => <div data-height={item.height}>{item.key}</div>}
      />
    );
    const first = render(element(90));
    await emitMeasurements();
    expect(rowStart("item-5")).toBe(5 * 90);
    first.unmount();

    render(element(130));
    await waitFor(() => expect(rowStart("item-5")).toBe(5 * 40));
    await emitMeasurements();
    expect(rowStart("item-5")).toBe(5 * 130);
  });

  it("cancels an ephemeral pending restore when its list instance unmounts", async () => {
    const ref = createRef<VariableHeightVirtualListHandle>();
    const view = render(
      <VariableHeightVirtualList
        ref={ref}
        items={items(100, 72)}
        getItemKey={(item) => item.key}
        estimateSize={() => 40}
        measurementCache={{ mode: "ephemeral" }}
        rowSemantics={{ mode: "caller-owned" }}
        scrollSemantics={{
          mode: "custom",
          role: "region",
          ariaLabel: "Ephemeral cancellation",
          pageScrollOwner: true,
          locked: false,
        }}
        overscan={6}
        maxMountedRows={48}
        onScroll={() => {}}
        renderItem={(item) => <div data-height={item.height}>{item.key}</div>}
      />,
    );
    await emitMeasurements();
    const restoration = ref.current?.restoreAnchor({
      key: "item-50",
      offsetPx: -7,
      priorIndex: 50,
    });
    if (restoration === undefined) throw new Error("restore handle missing");
    let settled = false;
    void restoration.then(() => {
      settled = true;
    });

    await act(async () => view.unmount());
    expect(settled).toBe(true);
  });

  it("mounts at most 48 rows while exposing logical set positions", async () => {
    renderList();
    await emitMeasurements();

    expect(
      screen.getAllByTestId("virtual-transcript-row").length,
    ).toBeLessThanOrEqual(48);
    expect(screen.queryAllByRole("article")).not.toHaveLength(500);
    const first = screen.getAllByTestId("virtual-transcript-row")[0];
    expect(first).toHaveAttribute("aria-setsize", "500");
    expect(Number(first?.getAttribute("aria-posinset"))).toBeGreaterThan(0);
  });

  it("makes every one of 500 stable logical keys reachable without breaking the mount ceiling", async () => {
    const { ref } = renderList();
    await emitMeasurements();
    const reached = new Set<string>();

    for (const item of items(500)) {
      act(() => ref.current?.focusKey(item.key));
      expect(screen.getByText(item.key)).toBeVisible();
      reached.add(item.key);
      expect(
        screen.getAllByTestId("virtual-transcript-row").length,
      ).toBeLessThanOrEqual(48);
    }

    expect(reached).toHaveLength(500);
  });

  it("captures item-217 and restores its measured offset within two pixels", async () => {
    const { ref } = renderList();
    await emitMeasurements();
    await focusKey(ref, "item-217");
    const feed = screen.getByRole("feed");
    feed.scrollTop = rowStart("item-217") + 11;
    fireEvent.scroll(feed);
    const before = ref.current?.captureAnchor();

    expect(before?.key).toBe("item-217");
    expect(before).not.toBeNull();
    if (before === null || before === undefined)
      throw new Error("anchor missing");
    const restoration = ref.current?.restoreAnchor(before);
    emitMeasurementWhere(
      (target) => (target as HTMLElement).dataset.itemKey === "item-217",
    );
    await act(async () => restoration);
    const after = ref.current?.captureAnchor();
    expect(after?.key).toBe("item-217");
    expect(
      Math.abs((after?.offsetPx ?? 999) - before.offsetPx),
    ).toBeLessThanOrEqual(2);
  });

  it("waits through estimated scroll changes for the target measurement and adjusted offset", async () => {
    const { ref, rerenderList } = renderList({
      values: items(500, 72),
      threadKey: "thread-target-handshake",
      estimate: 40,
    });
    await emitMeasurements();
    const restoration = ref.current?.restoreAnchor({
      key: "item-217",
      offsetPx: -9,
      priorIndex: 217,
    });
    if (restoration === undefined) throw new Error("restore handle missing");
    let restored = false;
    void restoration.then(() => {
      restored = true;
    });

    await act(async () => {
      rerenderList(
        items(500, 72).map((item) =>
          item.key === "item-216" ? { ...item, height: 73 } : item,
        ),
      );
      DeterministicResizeObserver.emitWhere(
        (target) => (target as HTMLElement).dataset.itemKey === "item-216",
      );
    });
    expect(restored).toBe(false);
    emitMeasurementWhere((target) => target.getAttribute("role") === "feed");
    expect(await promiseSettled(restoration)).toBe(false);

    emitMeasurementWhere(
      (target) => (target as HTMLElement).dataset.itemKey === "item-217",
    );
    await act(async () => restoration);
    const restoredAnchor = ref.current?.captureAnchor();
    expect(restoredAnchor?.key).toBe("item-217");
    expect(
      Math.abs((restoredAnchor?.offsetPx ?? 999) - -9),
    ).toBeLessThanOrEqual(2);
  });

  it("completes no-op restoration immediately for an already measured target", async () => {
    const { ref } = renderList({ values: items(40, 72), estimate: 40 });
    await emitMeasurements();
    const anchor = ref.current?.captureAnchor();
    if (anchor === null || anchor === undefined)
      throw new Error("anchor missing");
    const restoration = ref.current?.restoreAnchor(anchor);
    if (restoration === undefined) throw new Error("restore handle missing");
    await expect(restoration).resolves.toBeUndefined();
    expect(ref.current?.captureAnchor()).toEqual(anchor);
  });

  it("settles restoration when the target is removed before measurement", async () => {
    const values = items(100, 72);
    const { ref, rerenderList } = renderList({
      values,
      threadKey: "cancel-removed-target",
    });
    await emitMeasurements();
    const restoration = ref.current?.restoreAnchor({
      key: "item-50",
      offsetPx: -7,
      priorIndex: 50,
    });
    if (restoration === undefined) throw new Error("restore handle missing");
    let settled = false;
    void restoration.then(() => {
      settled = true;
    });

    await act(async () => {
      rerenderList(values.filter((item) => item.key !== "item-50"));
    });
    expect(settled).toBe(true);
  });

  it("cancels old-thread same-key restoration without a stale adjustment", async () => {
    const values = items(100, 72);
    const { ref, rerenderList } = renderList({
      values,
      threadKey: "cancel-thread-a",
    });
    await emitMeasurements();
    const restoration = ref.current?.restoreAnchor({
      key: "item-50",
      offsetPx: -17,
      priorIndex: 50,
    });
    if (restoration === undefined) throw new Error("restore handle missing");
    let settled = false;
    void restoration.then(() => {
      settled = true;
    });

    await act(async () => {
      rerenderList(values, "stillwater", "large", "cancel-thread-b");
    });
    expect(settled).toBe(true);
    const offsetAfterSwitch = screen.getByRole("feed").scrollTop;
    emitMeasurementWhere(
      (target) => (target as HTMLElement).dataset.itemKey === "item-50",
    );
    expect(screen.getByRole("feed").scrollTop).toBe(offsetAfterSwitch);
  });

  it("settles target-measurement restoration when the list unmounts", async () => {
    const { ref, unmount } = renderList({
      values: items(100, 72),
      threadKey: "cancel-unmount-measurement",
    });
    await emitMeasurements();
    const restoration = ref.current?.restoreAnchor({
      key: "item-50",
      offsetPx: -7,
      priorIndex: 50,
    });
    if (restoration === undefined) throw new Error("restore handle missing");
    let settled = false;
    void restoration.then(() => {
      settled = true;
    });
    await act(async () => unmount());
    expect(settled).toBe(true);
  });

  it("settles a pending offset correction on unmount", async () => {
    const { ref, unmount } = renderList({
      values: items(40, 72),
      threadKey: "cancel-unmount-offset",
    });
    await emitMeasurements();
    const anchor = ref.current?.captureAnchor();
    if (anchor === null || anchor === undefined)
      throw new Error("anchor missing");
    dispatchProgrammaticScrollEvents = false;
    const restoration = ref.current?.restoreAnchor({
      ...anchor,
      offsetPx: anchor.offsetPx - 20,
    });
    if (restoration === undefined) throw new Error("restore handle missing");
    let settled = false;
    void restoration.then(() => {
      settled = true;
    });
    await act(async () => unmount());
    expect(settled).toBe(true);
  });

  it("keeps exact nested thread, skin, content-size measurement scopes isolated", async () => {
    const base = items(40, 72);
    const { ref, rerenderList } = renderList({
      values: base,
      threadKey: "thread-three-skins",
      estimate: 40,
    });
    await emitMeasurements();
    expect(rowStart("item-5")).toBe(5 * 72);
    await focusKey(ref, "item-5");
    const before = ref.current?.captureAnchor();
    expect(before).not.toBeNull();

    for (const [skinId, height] of [
      ["constellation", 131],
      ["field-notes", 219],
    ] as const) {
      rerenderList(items(40, height), skinId, "large");
      await emitMeasurements();
      expect(rowStart("item-5")).toBe(5 * height);
      const anchor = ref.current?.captureAnchor();
      expect(anchor?.key).toBe(before?.key);
      expect(
        Math.abs((anchor?.offsetPx ?? 999) - (before?.offsetPx ?? 0)),
      ).toBeLessThanOrEqual(2);
    }

    // Revisit Stillwater without another ResizeObserver delivery. Only the
    // retained nested leaf can recover 72px geometry instead of the 40px estimate.
    rerenderList(items(40, 72), "stillwater", "large");
    await waitFor(() => expect(rowStart("item-5")).toBe(5 * 72));
  });

  it("preserves the measured anchor through streaming and marker growth in every skin", async () => {
    const skinHeights = [
      ["stillwater", 72],
      ["constellation", 131],
      ["field-notes", 219],
    ] as const;
    const { ref, rerenderList } = renderList({
      values: items(40, 72),
      threadKey: "thread-resizing-skins",
    });
    await emitMeasurements();

    for (const [skinId, height] of skinHeights) {
      rerenderList(items(40, height), skinId, "large");
      await emitMeasurements();
      await focusKey(ref, "item-12");
      const before = ref.current?.captureAnchor();
      expect(before).not.toBeNull();
      const grown = items(40, height).map((item, index) =>
        index === 7
          ? { ...item, height: height + 47 }
          : index === 9
            ? { ...item, height: height + 33 }
            : item,
      );
      rerenderList(grown, skinId, "large");
      await emitMeasurements();
      await waitFor(() => {
        const after = ref.current?.captureAnchor();
        expect(after?.key).toBe(before?.key);
        expect(
          Math.abs((after?.offsetPx ?? 999) - (before?.offsetPx ?? 0)),
        ).toBeLessThanOrEqual(2);
      });
    }
  });

  it("recomputes only the active skin/content-size scope across Dynamic Type transitions", async () => {
    const { ref, rerenderList } = renderList({
      values: items(40, 131),
      threadKey: "thread-dynamic-type",
      skinId: "constellation",
      estimate: 40,
    });
    await emitMeasurements();
    expect(rowStart("item-5")).toBe(5 * 131);

    // Seed two unrelated exact leaves with geometry unlike the estimate.
    rerenderList(items(40, 82), "stillwater", "extraExtraLarge");
    await emitMeasurements();
    expect(rowStart("item-5")).toBe(5 * 82);
    rerenderList(items(40, 171), "constellation", "extraExtraLarge");
    await emitMeasurements();
    expect(rowStart("item-5")).toBe(5 * 171);

    // Revisit large first, then make a same-skin Dynamic Type transition into
    // the pre-seeded XXL leaf. That active target alone must be invalidated.
    rerenderList(items(40, 90), "field-notes", "large");
    await emitMeasurements();
    rerenderList(items(40, 131), "constellation", "large");
    await waitFor(() => expect(rowStart("item-5")).toBe(5 * 131));
    await focusKey(ref, "item-5");
    const before = ref.current?.captureAnchor();
    rerenderList(items(40, 171), "constellation", "extraExtraLarge");
    await waitFor(() => expect(rowStart("item-5")).toBe(5 * 40));
    await emitMeasurements();
    await waitFor(() => {
      const after = ref.current?.captureAnchor();
      expect(after?.key).toBe(before?.key);
      expect(
        Math.abs((after?.offsetPx ?? 999) - (before?.offsetPx ?? 0)),
      ).toBeLessThanOrEqual(2);
    });

    // The unrelated Stillwater/XXL leaf remains intact without remeasurement.
    rerenderList(items(40, 82), "stillwater", "extraExtraLarge");
    await waitFor(() => expect(rowStart("item-5")).toBe(5 * 82));
  });

  it("moves focus to the feed before eviction and restores only on intentional revisit", async () => {
    const { ref } = renderList();
    await emitMeasurements();
    await focusKey(ref, "item-12");
    expect(
      screen
        .getByText("item-12")
        .closest('[data-testid="virtual-transcript-row"]'),
    ).toHaveFocus();

    const feed = screen.getByRole("feed");
    act(() => {
      feed.scrollTop = 400 * 72;
      fireEvent.scroll(feed);
    });
    await waitFor(() => expect(feed).toHaveFocus());
    expect(screen.queryByText("item-12")).toBeNull();

    await focusKey(ref, "item-12");
    expect(
      screen
        .getByText("item-12")
        .closest('[data-testid="virtual-transcript-row"]'),
    ).toHaveFocus();
  });

  it("preserves descendant focus for retained rows and moves it before same-count removal", async () => {
    const values = items(20, 72);
    const { rerenderList } = renderList({
      values,
      estimate: 40,
      renderRow: (item) => (
        <button type="button" data-height={item.height}>
          Action {item.key}
        </button>
      ),
    });
    await emitMeasurements();
    const action = screen.getByRole("button", { name: "Action item-5" });
    action.focus();
    const feed = screen.getByRole("feed");

    act(() => {
      feed.scrollTop += 1;
      fireEvent.scroll(feed);
    });
    expect(action).toHaveFocus();
    emitMeasurementWhere(
      (target) => (target as HTMLElement).dataset.itemKey === "item-4",
    );
    expect(action).toHaveFocus();

    rerenderList(
      values.map((item) =>
        item.key === "item-5" ? { ...item, key: "replacement-5" } : item,
      ),
    );
    expect(feed).toHaveFocus();
    expect(screen.queryByRole("button", { name: "Action item-5" })).toBeNull();
  });
});

function productionConversation(
  id: string,
  prefix: string,
  count = 30,
): MobileConversation {
  const timelineItems: MobileTimelineItem[] = Array.from(
    { length: count },
    (_, index) => ({
      kind: "user",
      id: `shared-${index}`,
      text: `${prefix}-${index}`,
    }),
  );
  return {
    id,
    sessionId: `${id}-session`,
    name: id,
    preview: "",
    modelProvider: "test",
    status: "ready",
    items: timelineItems,
    capabilities: {
      send: true,
      steer: true,
      interrupt: true,
      compact: true,
      clear: true,
      forkFromTurn: true,
      shutdown: true,
      changeModel: true,
      changeVisionModel: true,
      queue: true,
      goal: true,
      rename: true,
    },
    queue: { depth: 0, preview: [] },
    usage: {},
    askPending: false,
  };
}

function productionConversationStore(
  conversation: MobileConversation,
): UseBoundStore<StoreApi<ConversationState>> {
  return create<ConversationState>(() => ({
    ref: conversation.id,
    profileId: "profile",
    connectionGeneration: 0,
    conversationGeneration: 1,
    conversation,
    olderCursor: null,
    loadingOlder: false,
    status: "open",
    error: null,
    draft: "",
    pendingSend: null,
    open: vi.fn(),
    loadOlder: vi.fn(async () => ({ status: "ignored" as const })),
    setDraft: vi.fn(),
    send: vi.fn(),
    steer: vi.fn(),
    queue: vi.fn(),
    interrupt: vi.fn(),
    close: vi.fn(),
    applyNotification: vi.fn(),
    reset: vi.fn(),
  }));
}

function productionNavigationStore(): UseBoundStore<StoreApi<NavigationState>> {
  return create<NavigationState>((_, get) => ({
    tab: "sessions",
    conversationStack: [{ sessionId: "thread-a", title: "Thread" }],
    activeConversation: { sessionId: "thread-a", title: "Thread" },
    setTab: vi.fn(),
    pushConversation: vi.fn(),
    popConversation: vi.fn(),
    popAllConversations: vi.fn(),
    clearConversations: vi.fn(),
    canGoBack: () => get().conversationStack.length > 0,
  }));
}

function visibleVirtualAnchor(): { key: string; offsetPx: number } {
  const feed = screen.getByRole("feed", { name: "Conversation transcript" });
  const rows = screen
    .getAllByTestId("virtual-transcript-row")
    .map((row) => {
      const start = Number(
        (row as HTMLElement).style.transform.match(
          /translateY\(([-\d.]+)px\)/,
        )?.[1],
      );
      return {
        key: (row as HTMLElement).dataset.itemKey ?? "",
        start,
        height: row.getBoundingClientRect().height,
      };
    })
    .sort((left, right) => left.start - right.start);
  const row = rows.find(
    (candidate) => candidate.start + candidate.height > feed.scrollTop,
  );
  if (row === undefined) throw new Error("no visible virtual anchor");
  return { key: row.key, offsetPx: row.start - feed.scrollTop };
}

describe("ConversationScreen authoritative real Timeline boundary", () => {
  it("isolates thread scope, restores same-thread Dynamic Type, and reports 48/49px follow", async () => {
    const conversationStore = productionConversationStore(
      productionConversation("thread-a", "A"),
    );
    const navigationStore = productionNavigationStore();
    const preferencesStore = createPreferencesStore();
    render(
      <ConversationScreen
        conversationStore={conversationStore}
        navigationStore={navigationStore}
        preferencesStore={preferencesStore}
      />,
    );
    await emitMeasurements();
    const feed = screen.getByRole("feed", { name: "Conversation transcript" });
    await waitFor(() => expect(rowStart("A-26") - rowStart("A-25")).toBe(72));
    await waitFor(() =>
      expect(
        feed.scrollHeight - (feed.scrollTop + feed.clientHeight),
      ).toBeLessThanOrEqual(48),
    );
    await navigateToMountedRow(feed, "A-5", 5 * 64);
    await emitMeasurements();
    await navigateToMountedRow(feed, "A-5", 5 * 72);
    expect(rowStart("A-5")).toBe(5 * 72);
    act(() => {
      feed.scrollTop = rowStart("A-5") + 11;
      fireEvent.scroll(feed);
    });
    const beforeScale = visibleVirtualAnchor();
    fallbackVirtualRowHeight = 131;
    act(() => preferencesStore.getState().setContentSize("extraExtraLarge"));
    await emitMeasurements();
    expect(rowStart("A-5")).toBe(5 * 131);
    const afterScale = visibleVirtualAnchor();
    expect(afterScale.key).toBe(beforeScale.key);
    expect(
      Math.abs(afterScale.offsetPx - beforeScale.offsetPx),
    ).toBeLessThanOrEqual(2);
    const beforeReturn = visibleVirtualAnchor();
    fallbackVirtualRowHeight = 72;
    act(() => preferencesStore.getState().setContentSize("large"));
    await waitFor(() => expect(rowStart("A-5")).toBe(5 * 64));
    await emitMeasurements();
    expect(rowStart("A-5")).toBe(5 * 72);
    const afterReturn = visibleVirtualAnchor();
    expect(afterReturn.key).toBe(beforeReturn.key);
    expect(
      Math.abs(afterReturn.offsetPx - beforeReturn.offsetPx),
    ).toBeLessThanOrEqual(2);

    act(() => {
      feed.scrollTop = feed.scrollHeight - feed.clientHeight - 49;
      fireEvent.scroll(feed);
      conversationStore.setState({
        conversation: productionConversation("thread-a", "A", 31),
      });
    });
    expect(screen.getByRole("button", { name: "1 new" })).toBeVisible();
    act(() => {
      feed.scrollTop = feed.scrollHeight - feed.clientHeight - 48;
      fireEvent.scroll(feed);
    });
    expect(screen.queryByRole("button", { name: "1 new" })).toBeNull();

    await navigateToMountedRow(feed, "A-5", 5 * 64);
    await emitMeasurements();
    act(() => {
      feed.scrollTop = rowStart("A-5") + 11;
      fireEvent.scroll(feed);
    });
    fallbackVirtualRowHeight = 219;
    act(() => {
      conversationStore.setState({
        ref: "thread-b",
        conversationGeneration: 2,
        conversation: productionConversation("thread-b", "B"),
      });
    });
    await emitMeasurements();
    await waitFor(() =>
      expect(
        feed.scrollHeight - (feed.scrollTop + feed.clientHeight),
      ).toBeLessThanOrEqual(48),
    );
    await navigateToMountedRow(feed, "B-5", 5 * 64);
    await emitMeasurements();
    await navigateToMountedRow(feed, "B-5", 5 * 219);
    expect(rowStart("B-5")).toBe(5 * 219);
  });
});

describe("VirtualTranscript real measured boundary", () => {
  it("does not lose initial tail initialization when layout notifies before the handle is installed", async () => {
    const ref = { current: null as VirtualTranscriptHandle | null };
    const values = Array.from({ length: 80 }, (_, index) =>
      displayNarrative(`live-${index}`),
    );
    render(
      <VirtualTranscript
        ref={ref}
        threadKey="real-initial-race"
        skinId="stillwater"
        contentSize="large"
        items={values}
        skin={measuredSkin}
        savedAnchor={null}
        unseen={0}
        onAnchorChange={() => {}}
        onUnseenChange={() => {}}
        onFocusIntentChange={() => {}}
      />,
    );

    await emitMeasurements();
    await waitFor(() => {
      const key = ref.current?.captureAnchor()?.itemKey;
      expect(Number(key?.replace("live-", ""))).toBeGreaterThanOrEqual(70);
    });
  });

  it("waits for the real saved-anchor target measurement during initial ordering", async () => {
    const ref = { current: null as VirtualTranscriptHandle | null };
    const values = Array.from({ length: 80 }, (_, index) =>
      displayNarrative(`saved-${index}`),
    );
    const saved: ConversationAnchor = {
      threadKey: "real-saved-race",
      itemKey: "saved-40",
      offsetPx: -7,
      following: false,
    };
    render(
      <VirtualTranscript
        ref={ref}
        threadKey="real-saved-race"
        skinId="stillwater"
        contentSize="large"
        items={values}
        skin={measuredSkin}
        savedAnchor={saved}
        unseen={0}
        onAnchorChange={() => {}}
        onUnseenChange={() => {}}
        onFocusIntentChange={() => {}}
      />,
    );
    await emitMeasurements();
    emitMeasurementWhere(
      (target) => (target as HTMLElement).dataset.itemKey === "saved-40",
    );
    await waitFor(() => {
      const restored = ref.current?.captureAnchor();
      expect(restored?.itemKey).toBe("saved-40");
      expect(Math.abs((restored?.offsetPx ?? 999) - -7)).toBeLessThanOrEqual(2);
    });
  });

  it("uses the real family estimates but restores measured user, streaming, and marker growth", async () => {
    const ref = { current: null as VirtualTranscriptHandle | null };
    const base: ConversationDisplayItem[] = Array.from(
      { length: 20 },
      (_, index) =>
        index === 7
          ? displayNarrative("family-7", "assistant", "streaming", true)
          : index === 9
            ? displayMarker("family-9")
            : displayNarrative(
                `family-${index}`,
                index % 3 === 0 ? "user" : "assistant",
              ),
    );
    const callbacks = {
      onAnchorChange: vi.fn(),
      onUnseenChange: vi.fn(),
      onFocusIntentChange: vi.fn(),
    };
    const element = (values: readonly ConversationDisplayItem[]) => (
      <VirtualTranscript
        ref={ref}
        threadKey="real-display-families"
        skinId="stillwater"
        contentSize="large"
        items={values}
        skin={measuredSkin}
        savedAnchor={null}
        unseen={0}
        {...callbacks}
      />
    );
    const view = render(element(base));
    await emitMeasurements();
    act(() => ref.current?.focusKey("family-12"));
    await emitMeasurements();
    const feed = screen.getByRole("feed", { name: "Conversation transcript" });
    act(() => {
      feed.scrollTop = rowStart("family-12") + 11;
      fireEvent.scroll(feed);
    });
    const before = ref.current?.captureAnchor();
    expect(before).not.toBeNull();

    view.rerender(
      element(
        base.map((item) =>
          item.key === "family-7" && "body" in item
            ? { ...item, body: bounded("grown streaming") }
            : item.key === "family-9" && "semanticKind" in item
              ? { ...item, state: "completed", tone: "success" }
              : item,
        ),
      ),
    );
    emitMeasurementWhere((target) =>
      ["family-7", "family-9"].includes(
        (target as HTMLElement).dataset.itemKey ?? "",
      ),
    );
    await waitFor(() => {
      const after = ref.current?.captureAnchor();
      expect(after?.itemKey).toBe(before?.itemKey);
      expect(
        Math.abs((after?.offsetPx ?? 999) - (before?.offsetPx ?? 0)),
      ).toBeLessThanOrEqual(2);
    });
  });
});
