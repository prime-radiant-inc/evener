import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { createRef, type ReactElement, type RefObject } from "react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import type { ConceptId } from "../../live-concepts/model";
import type { ContentSizeCategory } from "../../native/contract";
import {
  VariableHeightVirtualList,
  type VariableHeightVirtualListHandle,
} from "./VariableHeightVirtualList";

interface Item {
  readonly key: string;
  readonly height: number;
}

const VIEWPORT_HEIGHT = 360;

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
    for (const observer of DeterministicResizeObserver.instances) {
      const entries = [...observer.targets].map((target) => {
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
  globalThis.ResizeObserver = DeterministicResizeObserver;
  Object.defineProperty(HTMLElement.prototype, "offsetHeight", {
    configurable: true,
    get() {
      return this.getAttribute("role") === "feed" ? VIEWPORT_HEIGHT : 0;
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
      return this.getAttribute("role") === "feed" ? VIEWPORT_HEIGHT : 0;
    },
  });
  Object.defineProperty(HTMLElement.prototype, "scrollHeight", {
    configurable: true,
    get() {
      if (this.getAttribute("role") !== "feed") return 0;
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
      : this.getAttribute("role") === "feed"
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
    this.dispatchEvent(new Event("scroll"));
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

function renderList({
  values = items(500),
  threadKey = "thread-variable-list",
  skinId = "stillwater",
  contentSize = "large",
  ref = createRef<VariableHeightVirtualListHandle>(),
}: {
  values?: readonly Item[];
  threadKey?: string;
  skinId?: ConceptId;
  contentSize?: ContentSizeCategory;
  ref?: RefObject<VariableHeightVirtualListHandle | null>;
} = {}): {
  readonly ref: RefObject<VariableHeightVirtualListHandle | null>;
  readonly rerenderList: (
    next: readonly Item[],
    nextSkin?: ConceptId,
    nextSize?: ContentSizeCategory,
  ) => void;
} {
  let activeSkin = skinId;
  let activeSize = contentSize;
  const element = (next: readonly Item[]): ReactElement => (
    <VariableHeightVirtualList
      ref={ref}
      items={next}
      getItemKey={(item) => item.key}
      estimateSize={(item) => item.height}
      cacheScope={{ threadKey, skinId: activeSkin, contentSize: activeSize }}
      overscan={6}
      maxMountedRows={48}
      onScroll={() => {}}
      renderItem={(item) => <div data-height={item.height}>{item.key}</div>}
    />
  );
  const view = render(element(values));
  return {
    ref,
    rerenderList(next, nextSkin = activeSkin, nextSize = activeSize) {
      activeSkin = nextSkin;
      activeSize = nextSize;
      view.rerender(element(next));
    },
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
    feed.scrollTop = 217 * 72 + 11;
    fireEvent.scroll(feed);
    const before = ref.current?.captureAnchor();

    expect(before?.key).toBe("item-217");
    expect(before).not.toBeNull();
    if (before === null || before === undefined)
      throw new Error("anchor missing");
    await act(() => ref.current?.restoreAnchor(before));
    const after = ref.current?.captureAnchor();
    expect(after?.key).toBe("item-217");
    expect(
      Math.abs((after?.offsetPx ?? 999) - before.offsetPx),
    ).toBeLessThanOrEqual(2);
  });

  it("keeps exact nested thread, skin, content-size measurement scopes isolated", async () => {
    const base = items(40, 72);
    const { ref, rerenderList } = renderList({
      values: base,
      threadKey: "thread-three-skins",
    });
    await emitMeasurements();
    await focusKey(ref, "item-10");
    const before = ref.current?.captureAnchor();
    expect(before).not.toBeNull();

    for (const [skinId, height] of [
      ["constellation", 131],
      ["field-notes", 219],
      ["stillwater", 72],
    ] as const) {
      rerenderList(items(40, height), skinId, "large");
      await emitMeasurements();
      if (before !== null && before !== undefined) {
        await act(() => ref.current?.restoreAnchor(before));
      }
      const anchor = ref.current?.captureAnchor();
      expect(anchor?.key).toBe(before?.key);
      expect(
        Math.abs((anchor?.offsetPx ?? 999) - (before?.offsetPx ?? 0)),
      ).toBeLessThanOrEqual(2);
    }
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
    });
    await emitMeasurements();
    await focusKey(ref, "item-10");

    for (const [contentSize, height] of [
      ["extraExtraLarge", 171],
      ["accessibilityExtraExtraExtraLarge", 241],
    ] as const) {
      const before = ref.current?.captureAnchor();
      expect(before).not.toBeNull();
      rerenderList(items(40, height), "constellation", contentSize);
      await emitMeasurements();
      await waitFor(() => {
        const after = ref.current?.captureAnchor();
        expect(after?.key).toBe(before?.key);
        expect(
          Math.abs((after?.offsetPx ?? 999) - (before?.offsetPx ?? 0)),
        ).toBeLessThanOrEqual(2);
      });
    }

    const beforeReturn = ref.current?.captureAnchor();
    rerenderList(items(40, 131), "constellation", "large");
    await emitMeasurements();
    const afterReturn = ref.current?.captureAnchor();
    expect(afterReturn?.key).toBe(beforeReturn?.key);
    expect(
      Math.abs((afterReturn?.offsetPx ?? 999) - (beforeReturn?.offsetPx ?? 0)),
    ).toBeLessThanOrEqual(2);
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
});
