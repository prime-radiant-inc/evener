import { useVirtualizer } from "@tanstack/react-virtual";
import { type JSX, useRef } from "react";
import type { MobileTimelineItem } from "../../conversation/model";
import {
  adjustAfterPrepend,
  createPagingState,
  type PagingState,
  recordPrepend,
} from "../../conversation/paging";
import { NewActivityButton } from "./NewActivityButton";
import { TimelineItem } from "./TimelineItem";
import "./Timeline.css";

export interface TimelineProps {
  /** The ordered conversation items (oldest first, newest last). */
  readonly items: MobileTimelineItem[];
  /** Whether follow mode is active (auto-scroll to bottom). */
  readonly following: boolean;
  /** Number of unseen new items (drives the new-activity pill). */
  readonly unseen: number;
  /** Invoked when the user taps the new-activity pill. */
  readonly onTapNewActivity?: () => void;
  /** Invoked when the user scrolls near the top to load older items. */
  readonly loadOlder?: () => void;
  /** Invoked when the user taps an external link in an assistant message. */
  readonly onExternalLink?: (url: string) => void;
  /** Height of each timeline item estimate for virtualization (default 64). */
  readonly estimateItemHeight?: number;
  /** Scroll threshold from the top (in px) that triggers loadOlder (default 120). */
  readonly loadOlderThreshold?: number;
}

/**
 * Virtualized conversation timeline. Uses `@tanstack/react-virtual` for
 * windowing: only visible items render DOM nodes. Items are keyed by stable
 * identity (`item.id`), never by index, so prepend/insert does not destroy
 * component state. One scroller element owns all scrolling.
 *
 * Prepend-anchor preservation: before `loadOlder` prepends older items, the
 * timeline records the topmost visible item and its scroll offset. After the
 * virtualizer re-renders with the new items, the scroll offset is adjusted by
 * the measured height of the prepended content, keeping the visible content
 * within a 2 CSS pixel tolerance.
 *
 * The new-activity pill appears when `unseen > 0 && !following`.
 */
export function Timeline({
  items,
  following,
  unseen,
  onTapNewActivity,
  loadOlder,
  onExternalLink,
  estimateItemHeight = 64,
  loadOlderThreshold = 120,
}: TimelineProps): JSX.Element {
  const scrollRef = useRef<HTMLDivElement>(null);
  const pagingRef = useRef<PagingState>(createPagingState());
  const itemCountRef = useRef(items.length);

  const virtualizer = useVirtualizer({
    count: items.length,
    estimateSize: () => estimateItemHeight,
    getItemKey: (index) => items[index]?.id ?? index,
    getScrollElement: () => scrollRef.current,
    overscan: 8,
  });

  const virtualItems = virtualizer.getVirtualItems();
  const totalHeight = virtualizer.getTotalSize();

  function handleScroll(): void {
    const el = scrollRef.current;
    if (el === null) return;

    // Load older when scrolled near the top.
    if (loadOlder !== undefined && el.scrollTop <= loadOlderThreshold) {
      // Record the anchor before prepending, then trigger loadOlder.
      pagingRef.current = recordPrepend(pagingRef.current, items, el.scrollTop);
      loadOlder();
    }
  }

  // Adjust scroll offset after items were prepended (item count grew at the
  // top). We detect this by comparing the previous item count to the current;
  // if it grew and we have a recorded anchor, adjust.
  const prevCount = itemCountRef.current;
  if (items.length > prevCount && pagingRef.current.anchorId !== null) {
    const prependedCount = items.length - prevCount;
    const el = scrollRef.current;
    const currentOffset = el ? el.scrollTop : 0;
    const result = adjustAfterPrepend(
      { ...pagingRef.current, pendingPrependCount: prependedCount },
      currentOffset,
      estimateItemHeight,
    );
    pagingRef.current = result.nextState;
    if (el !== null) {
      el.scrollTop = result.offset;
    }
  }
  itemCountRef.current = items.length;

  // When following, keep the scroller pinned to the bottom.
  if (following && scrollRef.current !== null) {
    const el = scrollRef.current;
    // Defer to next frame so the new total height is committed.
    queueMicrotask(() => {
      if (el !== null) {
        el.scrollTop = el.scrollHeight;
      }
    });
  }

  return (
    <div className="evener-timeline">
      <div
        ref={scrollRef}
        className="evener-timeline__scroll"
        onScroll={handleScroll}
        role="feed"
      >
        <div
          className="evener-timeline__inner"
          style={{ height: `${totalHeight}px`, position: "relative" }}
        >
          {virtualItems.map((virtualItem) => {
            const item = items[virtualItem.index];
            if (item === undefined) return null;
            return (
              <div
                key={virtualItem.key}
                className="evener-timeline__item"
                style={{
                  position: "absolute",
                  top: 0,
                  left: 0,
                  width: "100%",
                  transform: `translateY(${virtualItem.start}px)`,
                }}
              >
                <TimelineItem item={item} onExternalLink={onExternalLink} />
              </div>
            );
          })}
        </div>
      </div>
      {unseen > 0 && !following ? (
        <NewActivityButton
          unseen={unseen}
          onTap={onTapNewActivity ?? (() => {})}
        />
      ) : null}
    </div>
  );
}
