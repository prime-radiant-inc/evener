import {
  defaultRangeExtractor,
  type Range,
  useVirtualizer,
  type VirtualItem,
} from "@tanstack/react-virtual";
import {
  forwardRef,
  type ReactNode,
  useCallback,
  useImperativeHandle,
  useLayoutEffect,
  useMemo,
  useRef,
} from "react";
import {
  captureMeasuredAnchor,
  type MeasuredAnchor,
} from "../../conversation/paging";
import type { ConceptId } from "../../live-concepts/model";
import type { ContentSizeCategory } from "../../native/contract";

export interface VariableHeightVirtualListHandle {
  captureAnchor(): MeasuredAnchor | null;
  restoreAnchor(anchor: MeasuredAnchor): Promise<void>;
  scrollToEnd(behavior: "auto" | "smooth"): void;
  focusKey(key: string): void;
}

export interface VariableHeightVirtualListProps<T> {
  readonly items: readonly T[];
  readonly getItemKey: (item: T) => string;
  readonly estimateSize: (item: T) => number;
  readonly cacheScope: {
    readonly threadKey: string;
    readonly skinId: ConceptId;
    readonly contentSize: ContentSizeCategory;
  };
  readonly overscan: 6;
  readonly maxMountedRows: 48;
  readonly onScroll: (metrics: {
    offset: number;
    viewport: number;
    total: number;
  }) => void;
  readonly renderItem: (item: T, index: number) => ReactNode;
}

type ItemMeasurements = Map<string, number>;
type ContentSizeMeasurements = Map<ContentSizeCategory, ItemMeasurements>;
type SkinMeasurements = Map<ConceptId, ContentSizeMeasurements>;

/** Exact cache identity: threadKey -> skinId -> contentSize -> itemKey. */
const measurementCaches = new Map<string, SkinMeasurements>();

function measurementsFor(scope: {
  readonly threadKey: string;
  readonly skinId: ConceptId;
  readonly contentSize: ContentSizeCategory;
}): ItemMeasurements {
  let thread = measurementCaches.get(scope.threadKey);
  if (thread === undefined) {
    thread = new Map();
    measurementCaches.set(scope.threadKey, thread);
  }
  let skin = thread.get(scope.skinId);
  if (skin === undefined) {
    skin = new Map();
    thread.set(scope.skinId, skin);
  }
  let contentSize = skin.get(scope.contentSize);
  if (contentSize === undefined) {
    contentSize = new Map();
    skin.set(scope.contentSize, contentSize);
  }
  return contentSize;
}

function sameScope(
  left: VariableHeightVirtualListProps<unknown>["cacheScope"],
  right: VariableHeightVirtualListProps<unknown>["cacheScope"],
): boolean {
  return (
    left.threadKey === right.threadKey &&
    left.skinId === right.skinId &&
    left.contentSize === right.contentSize
  );
}

function clampedRangeExtractor(maxMountedRows: number) {
  return (range: Range): number[] => {
    const extracted = defaultRangeExtractor(range);
    if (extracted.length <= maxMountedRows) return extracted;
    const visibleMiddle = Math.floor((range.startIndex + range.endIndex) / 2);
    const first = Math.max(
      0,
      Math.min(
        range.count - maxMountedRows,
        visibleMiddle - Math.floor(maxMountedRows / 2),
      ),
    );
    return Array.from(
      { length: maxMountedRows },
      (_, offset) => first + offset,
    );
  };
}

function initialMeasurements<T>(
  items: readonly T[],
  getItemKey: (item: T) => string,
  estimateSize: (item: T) => number,
  cache: ItemMeasurements,
): VirtualItem[] {
  let start = 0;
  return items.map((item, index) => {
    const key = getItemKey(item);
    const size = cache.get(key) ?? estimateSize(item);
    const measurement: VirtualItem = {
      key,
      index,
      start,
      size,
      end: start + size,
      lane: 0,
    };
    start += size;
    return measurement;
  });
}

function measuredHeight(
  element: HTMLElement,
  entry: ResizeObserverEntry | undefined,
  fallback: number,
): number {
  const observed = entry?.borderBoxSize[0]?.blockSize;
  if (observed !== undefined && observed > 0) return Math.round(observed);
  const rectHeight = element.getBoundingClientRect().height;
  return rectHeight > 0 ? Math.round(rectHeight) : fallback;
}

export const VariableHeightVirtualList = forwardRef(
  function VariableHeightVirtualListInner<T>(
    {
      items,
      getItemKey,
      estimateSize,
      cacheScope,
      overscan,
      maxMountedRows,
      onScroll,
      renderItem,
    }: VariableHeightVirtualListProps<T>,
    forwardedRef: React.ForwardedRef<VariableHeightVirtualListHandle>,
  ) {
    const scrollRef = useRef<HTMLDivElement>(null);
    const focusedKeyRef = useRef<string | null>(null);
    const pendingFocusRef = useRef(false);
    const pendingResizeRef = useRef<{
      readonly anchor: MeasuredAnchor | null;
      readonly following: boolean;
    } | null>(null);
    const restoreWaitersRef = useRef<Array<() => void>>([]);
    const previousScopeRef = useRef(cacheScope);
    const previousAnchorRef = useRef<MeasuredAnchor | null>(null);
    const activeCacheRef = useRef(measurementsFor(cacheScope));
    const itemKeys = useMemo(
      () => items.map((item) => getItemKey(item)),
      [getItemKey, items],
    );
    const initialCache = useMemo(
      () =>
        initialMeasurements(
          items,
          getItemKey,
          estimateSize,
          activeCacheRef.current,
        ),
      [estimateSize, getItemKey, items],
    );

    const rangeExtractor = useMemo(
      () => clampedRangeExtractor(maxMountedRows),
      [maxMountedRows],
    );

    const virtualizer = useVirtualizer<HTMLDivElement, HTMLElement>({
      useFlushSync: false,
      count: items.length,
      getScrollElement: () => scrollRef.current,
      getItemKey: (index) => {
        const item = items[index];
        if (item === undefined)
          throw new Error("Virtual item index is invalid");
        return getItemKey(item);
      },
      estimateSize: (index) => {
        const item = items[index];
        if (item === undefined)
          throw new Error("Virtual item index is invalid");
        return estimateSize(item);
      },
      initialMeasurementsCache: initialCache,
      overscan,
      rangeExtractor,
      anchorTo: "end",
      measureElement: (element, entry, instance) => {
        const index = instance.indexFromElement(element);
        const item = items[index];
        if (item === undefined) return 0;
        const key = getItemKey(item);
        const size = measuredHeight(element, entry, estimateSize(item));
        if (
          activeCacheRef.current.get(key) !== size &&
          pendingResizeRef.current === null
        ) {
          const scrollElement = scrollRef.current;
          const offset = instance.scrollOffset ?? scrollElement?.scrollTop ?? 0;
          const viewport =
            instance.scrollRect?.height ??
            scrollElement?.clientHeight ??
            scrollElement?.offsetHeight ??
            0;
          pendingResizeRef.current = {
            anchor: captureMeasuredAnchor(instance.getVirtualItems(), offset),
            following: instance.getTotalSize() - (offset + viewport) <= 48,
          };
        }
        activeCacheRef.current.set(key, size);
        return size;
      },
      onChange: (instance) => {
        const element = scrollRef.current;
        const offset = instance.scrollOffset ?? element?.scrollTop ?? 0;
        const viewport =
          instance.scrollRect?.height ??
          element?.clientHeight ??
          element?.offsetHeight ??
          0;
        const resized = pendingResizeRef.current;
        pendingResizeRef.current = null;
        if (resized?.following) {
          instance.scrollToEnd({ behavior: "auto" });
        } else if (resized?.anchor !== null && resized?.anchor !== undefined) {
          const measured = instance
            .getVirtualItems()
            .find((item) => String(item.key) === resized.anchor?.key);
          if (measured !== undefined) {
            const restoredOffset = measured.start - resized.anchor.offsetPx;
            if (restoredOffset !== offset) {
              instance.scrollToOffset(restoredOffset, { behavior: "auto" });
            }
          }
        }
        const finalOffset =
          instance.scrollOffset ?? element?.scrollTop ?? offset;
        previousAnchorRef.current = captureMeasuredAnchor(
          instance.getVirtualItems(),
          finalOffset,
        );
        onScroll({
          offset: finalOffset,
          viewport,
          total: instance.getTotalSize(),
        });

        const active = document.activeElement?.closest<HTMLElement>(
          '[data-testid="virtual-transcript-row"]',
        );
        const activeKey = active?.dataset.itemKey;
        if (
          activeKey !== undefined &&
          !instance
            .getVirtualItems()
            .some((candidate) => String(candidate.key) === activeKey)
        ) {
          focusedKeyRef.current = activeKey;
          element?.focus({ preventScroll: true });
        }

        const waiters = restoreWaitersRef.current.splice(0);
        for (const resolve of waiters) resolve();
      },
    });
    virtualizer.shouldAdjustScrollPositionOnItemSizeChange = (
      virtualItem,
      _delta,
      instance,
    ) => virtualItem.start < (instance.scrollOffset ?? 0);

    const captureAnchor = useCallback((): MeasuredAnchor | null => {
      const element = scrollRef.current;
      if (element === null) return null;
      const anchor = captureMeasuredAnchor(
        virtualizer.getVirtualItems(),
        virtualizer.scrollOffset ?? element.scrollTop,
      );
      previousAnchorRef.current = anchor;
      return anchor;
    }, [virtualizer]);

    const nextChange = useCallback(
      () =>
        new Promise<void>((resolve) => {
          restoreWaitersRef.current.push(resolve);
        }),
      [],
    );

    const restoreAnchor = useCallback(
      async (anchor: MeasuredAnchor): Promise<void> => {
        const index = itemKeys.indexOf(anchor.key);
        if (index < 0) return;
        const mounted = virtualizer
          .getVirtualItems()
          .find((item) => String(item.key) === anchor.key);
        if (mounted !== undefined && activeCacheRef.current.has(anchor.key)) {
          virtualizer.scrollToOffset(mounted.start - anchor.offsetPx, {
            behavior: "auto",
          });
          return;
        }
        const changed = nextChange();
        virtualizer.scrollToIndex(index, { align: "start", behavior: "auto" });
        await changed;
        const measured = virtualizer
          .getVirtualItems()
          .find((item) => String(item.key) === anchor.key);
        if (measured !== undefined) {
          virtualizer.scrollToOffset(measured.start - anchor.offsetPx, {
            behavior: "auto",
          });
        }
      },
      [itemKeys, nextChange, virtualizer],
    );

    const focusKey = useCallback(
      (key: string): void => {
        const index = itemKeys.indexOf(key);
        if (index < 0) return;
        focusedKeyRef.current = key;
        pendingFocusRef.current = true;
        const mounted = [
          ...(scrollRef.current?.querySelectorAll<HTMLElement>(
            '[data-testid="virtual-transcript-row"]',
          ) ?? []),
        ].find((row) => row.dataset.itemKey === key);
        if (mounted !== null && mounted !== undefined) {
          mounted.focus({ preventScroll: true });
          pendingFocusRef.current = false;
          return;
        }
        scrollRef.current?.focus({ preventScroll: true });
        virtualizer.scrollToIndex(index, { align: "center", behavior: "auto" });
      },
      [itemKeys, virtualizer],
    );

    useImperativeHandle(
      forwardedRef,
      () => ({
        captureAnchor,
        restoreAnchor,
        scrollToEnd(behavior) {
          virtualizer.scrollToEnd({ behavior });
        },
        focusKey,
      }),
      [captureAnchor, focusKey, restoreAnchor, virtualizer],
    );

    useLayoutEffect(() => {
      const previousScope = previousScopeRef.current;
      if (sameScope(previousScope, cacheScope)) return;

      const outgoingAnchor = previousAnchorRef.current;
      const incoming = measurementsFor(cacheScope);
      if (
        previousScope.threadKey === cacheScope.threadKey &&
        previousScope.skinId === cacheScope.skinId &&
        previousScope.contentSize !== cacheScope.contentSize
      ) {
        incoming.clear();
      }
      activeCacheRef.current = incoming;
      previousScopeRef.current = cacheScope;
      virtualizer.measure();
      for (const [key, size] of incoming) {
        const index = itemKeys.indexOf(key);
        if (index >= 0) virtualizer.resizeItem(index, size);
      }
      if (outgoingAnchor !== null) void restoreAnchor(outgoingAnchor);
    }, [cacheScope, itemKeys, restoreAnchor, virtualizer]);

    useLayoutEffect(() => {
      previousAnchorRef.current = captureAnchor();
      const key = focusedKeyRef.current;
      if (key === null || !pendingFocusRef.current) return;
      const mounted = [
        ...(scrollRef.current?.querySelectorAll<HTMLElement>(
          '[data-testid="virtual-transcript-row"]',
        ) ?? []),
      ].find((row) => row.dataset.itemKey === key);
      if (mounted !== null && mounted !== undefined) {
        mounted.focus({ preventScroll: true });
        pendingFocusRef.current = false;
      }
    });

    const virtualItems = virtualizer.getVirtualItems();
    return (
      <div
        ref={scrollRef}
        className="evener-timeline__scroll"
        role="feed"
        aria-label="Conversation transcript"
        data-page-scroll-owner="true"
        tabIndex={-1}
        onScrollCapture={() => {
          const active = document.activeElement?.closest<HTMLElement>(
            '[data-testid="virtual-transcript-row"]',
          );
          if (active !== null && active !== undefined) {
            focusedKeyRef.current = active.dataset.itemKey ?? null;
            scrollRef.current?.focus({ preventScroll: true });
          }
        }}
      >
        <div
          style={{
            height: `${virtualizer.getTotalSize()}px`,
            position: "relative",
            width: "100%",
          }}
        >
          {virtualItems.map((virtualItem) => {
            const item = items[virtualItem.index];
            if (item === undefined) return null;
            const key = getItemKey(item);
            return (
              <article
                key={key}
                ref={virtualizer.measureElement}
                data-index={virtualItem.index}
                data-item-key={key}
                data-testid="virtual-transcript-row"
                aria-posinset={virtualItem.index + 1}
                aria-setsize={items.length}
                tabIndex={-1}
                style={{
                  position: "absolute",
                  top: 0,
                  left: 0,
                  width: "100%",
                  transform: `translateY(${virtualItem.start}px)`,
                }}
              >
                {renderItem(item, virtualItem.index)}
              </article>
            );
          })}
        </div>
      </div>
    );
  },
) as <T>(
  props: VariableHeightVirtualListProps<T> & {
    readonly ref?: React.ForwardedRef<VariableHeightVirtualListHandle>;
  },
) => React.ReactElement;
