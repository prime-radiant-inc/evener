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
  useReducer,
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
    measured: boolean;
  }) => void;
  readonly renderItem: (item: T, index: number) => ReactNode;
}

interface RestoreOperation {
  readonly key: string;
  cancelled: boolean;
  measurementResolver: (() => void) | null;
  offsetWaiter: OffsetWaiter | null;
}

interface OffsetWaiter {
  readonly target: number;
  readonly resolve: () => void;
  readonly operation: RestoreOperation;
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
): { readonly size: number; readonly measured: boolean } {
  const observed =
    entry?.borderBoxSize[0]?.blockSize ?? entry?.contentRect.height;
  if (observed !== undefined && observed > 0) {
    return { size: Math.round(observed), measured: true };
  }
  const rectHeight = element.getBoundingClientRect().height;
  return {
    size: rectHeight > 0 ? Math.round(rectHeight) : fallback,
    measured: false,
  };
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
    const onScrollRef = useRef(onScroll);
    onScrollRef.current = onScroll;
    const pendingResizeRef = useRef<{
      readonly anchor: MeasuredAnchor | null;
      readonly following: boolean;
    } | null>(null);
    const measurementWaitersRef = useRef(new Map<string, Set<() => void>>());
    const pendingMeasuredKeysRef = useRef(new Set<string>());
    const offsetWaitersRef = useRef<OffsetWaiter[]>([]);
    const restoreOperationsRef = useRef(new Set<RestoreOperation>());
    const rowElementsRef = useRef(new Map<string, HTMLElement>());
    const rowRefCallbacksRef = useRef(
      new Map<string, (node: HTMLElement | null) => void>(),
    );
    const previousScopeRef = useRef(cacheScope);
    const previousAnchorRef = useRef<MeasuredAnchor | null>(null);
    const activeCacheRef = useRef(measurementsFor(cacheScope));
    const [measurementRevision, notifyMeasurement] = useReducer(
      (revision: number) => revision + 1,
      0,
    );
    const cancelRestore = useCallback((operation: RestoreOperation): void => {
      if (operation.cancelled) return;
      operation.cancelled = true;
      if (operation.measurementResolver !== null) {
        const waiters = measurementWaitersRef.current.get(operation.key);
        waiters?.delete(operation.measurementResolver);
        if (waiters?.size === 0) {
          measurementWaitersRef.current.delete(operation.key);
        }
        const resolve = operation.measurementResolver;
        operation.measurementResolver = null;
        resolve();
      }
      if (operation.offsetWaiter !== null) {
        const waiter = operation.offsetWaiter;
        offsetWaitersRef.current = offsetWaitersRef.current.filter(
          (candidate) => candidate !== waiter,
        );
        operation.offsetWaiter = null;
        waiter.resolve();
      }
      restoreOperationsRef.current.delete(operation);
    }, []);
    const cancelAllRestores = useCallback((): void => {
      for (const operation of [...restoreOperationsRef.current]) {
        cancelRestore(operation);
      }
    }, [cancelRestore]);
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
        const measurement = measuredHeight(element, entry, estimateSize(item));
        const { size } = measurement;
        if (
          instance.itemSizeCache.get(key) !== size &&
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
        if (measurement.measured) {
          activeCacheRef.current.set(key, size);
          pendingMeasuredKeysRef.current.add(key);
          notifyMeasurement();
        }
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
        onScrollRef.current({
          offset: finalOffset,
          viewport,
          total: instance.getTotalSize(),
          measured: false,
        });
        const pendingOffsets = offsetWaitersRef.current;
        offsetWaitersRef.current = [];
        let retryOffset: number | null = null;
        for (const waiter of pendingOffsets) {
          if (waiter.operation.cancelled) continue;
          if (Math.abs(finalOffset - waiter.target) <= 2) {
            waiter.operation.offsetWaiter = null;
            waiter.resolve();
          } else {
            offsetWaitersRef.current.push(waiter);
            retryOffset = waiter.target;
          }
        }
        if (retryOffset !== null) {
          instance.scrollToOffset(retryOffset, { behavior: "auto" });
        }
      },
    });
    virtualizer.shouldAdjustScrollPositionOnItemSizeChange = (
      virtualItem,
      _delta,
      instance,
    ) => virtualItem.start < (instance.scrollOffset ?? 0);

    useLayoutEffect(() => {
      if (measurementRevision === 0) return;
      const element = scrollRef.current;
      if (element === null) return;
      const measuredKeys = [...pendingMeasuredKeysRef.current];
      pendingMeasuredKeysRef.current.clear();
      for (const key of measuredKeys) {
        const waiters = measurementWaitersRef.current.get(key);
        if (waiters === undefined) continue;
        measurementWaitersRef.current.delete(key);
        for (const resolve of waiters) resolve();
      }
      onScrollRef.current({
        offset: virtualizer.scrollOffset ?? element.scrollTop,
        viewport:
          virtualizer.scrollRect?.height ??
          element.clientHeight ??
          element.offsetHeight,
        total: virtualizer.getTotalSize(),
        measured: true,
      });
    }, [measurementRevision, virtualizer]);

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

    const restoreAnchor = useCallback(
      async (anchor: MeasuredAnchor): Promise<void> => {
        const index = itemKeys.indexOf(anchor.key);
        if (index < 0) return;
        const operation: RestoreOperation = {
          key: anchor.key,
          cancelled: false,
          measurementResolver: null,
          offsetWaiter: null,
        };
        restoreOperationsRef.current.add(operation);
        try {
          let target = virtualizer
            .getVirtualItems()
            .find((item) => String(item.key) === anchor.key);
          if (target === undefined || !activeCacheRef.current.has(anchor.key)) {
            const measured = new Promise<void>((resolve) => {
              const wake = (): void => {
                operation.measurementResolver = null;
                resolve();
              };
              operation.measurementResolver = wake;
              const waiters = measurementWaitersRef.current.get(anchor.key);
              if (waiters === undefined) {
                measurementWaitersRef.current.set(anchor.key, new Set([wake]));
              } else {
                waiters.add(wake);
              }
            });
            virtualizer.scrollToIndex(index, {
              align: "start",
              behavior: "auto",
            });
            await measured;
            if (operation.cancelled) return;
            target = virtualizer
              .getVirtualItems()
              .find((item) => String(item.key) === anchor.key);
          }

          if (target === undefined || operation.cancelled) return;
          const targetOffset = target.start - anchor.offsetPx;
          const currentOffset =
            virtualizer.scrollOffset ?? scrollRef.current?.scrollTop ?? 0;
          if (Math.abs(currentOffset - targetOffset) <= 2) return;
          const adjusted = new Promise<void>((resolve) => {
            const waiter: OffsetWaiter = {
              target: targetOffset,
              resolve,
              operation,
            };
            operation.offsetWaiter = waiter;
            offsetWaitersRef.current.push(waiter);
          });
          virtualizer.scrollToOffset(targetOffset, { behavior: "auto" });
          await adjusted;
        } finally {
          restoreOperationsRef.current.delete(operation);
        }
      },
      [itemKeys, virtualizer],
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

    const rowRefFor = useCallback(
      (key: string): ((node: HTMLElement | null) => void) => {
        const existing = rowRefCallbacksRef.current.get(key);
        if (existing !== undefined) return existing;
        const callback = (node: HTMLElement | null): void => {
          if (node !== null) {
            rowElementsRef.current.set(key, node);
            virtualizer.measureElement(node);
            return;
          }
          const outgoing = rowElementsRef.current.get(key);
          if (
            outgoing !== undefined &&
            document.activeElement !== null &&
            outgoing.contains(document.activeElement)
          ) {
            focusedKeyRef.current = key;
            pendingFocusRef.current = false;
            scrollRef.current?.focus({ preventScroll: true });
          }
          rowElementsRef.current.delete(key);
          rowRefCallbacksRef.current.delete(key);
          virtualizer.measureElement(null);
        };
        rowRefCallbacksRef.current.set(key, callback);
        return callback;
      },
      [virtualizer],
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
      for (const operation of [...restoreOperationsRef.current]) {
        if (!itemKeys.includes(operation.key)) cancelRestore(operation);
      }
    }, [cancelRestore, itemKeys]);

    useLayoutEffect(
      () => () => {
        cancelAllRestores();
      },
      [cancelAllRestores],
    );

    useLayoutEffect(() => {
      const previousScope = previousScopeRef.current;
      if (sameScope(previousScope, cacheScope)) return;

      cancelAllRestores();
      const threadChanged = previousScope.threadKey !== cacheScope.threadKey;
      const outgoingAnchor = threadChanged ? null : captureAnchor();
      const incoming = measurementsFor(cacheScope);
      if (
        previousScope.threadKey === cacheScope.threadKey &&
        previousScope.contentSize !== cacheScope.contentSize
      ) {
        incoming.clear();
      }
      activeCacheRef.current = incoming;
      previousScopeRef.current = cacheScope;
      if (threadChanged) previousAnchorRef.current = null;
      virtualizer.measure();
      for (const [key, size] of incoming) {
        const index = itemKeys.indexOf(key);
        if (index >= 0) virtualizer.resizeItem(index, size);
      }
      if (outgoingAnchor !== null) void restoreAnchor(outgoingAnchor);
    }, [
      cacheScope,
      cancelAllRestores,
      captureAnchor,
      itemKeys,
      restoreAnchor,
      virtualizer,
    ]);

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
                ref={rowRefFor(key)}
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
