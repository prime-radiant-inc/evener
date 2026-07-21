import { useImperativeHandle, useRef, type ReactNode, type Ref } from "react";
import { useVirtualizer, type ScrollToOptions } from "@tanstack/react-virtual";
import { requireClass } from "../internal/requireClass";
import styles from "./virtuallist.module.css";

export interface VirtualListHandle {
  scrollToIndex: (index: number, options?: ScrollToOptions) => void;
}

export interface VirtualListProps {
  count: number;
  estimateSize: (index: number) => number;
  renderRow: (index: number) => ReactNode;
  /**
   * Opt in to post-mount remeasurement (@tanstack/react-virtual's
   * `measureElement`): each rendered row's real DOM height corrects its
   * cached size instead of `estimateSize` staying authoritative forever.
   * Off by default so every fixed/known-height consumer (e.g. Rail's tree
   * rows) is completely unaffected - only a consumer whose rows vary
   * unpredictably (e.g. transcript turns: one tool call vs. a long streamed
   * response) needs this.
   */
  dynamic?: boolean;
  ref?: Ref<VirtualListHandle>;
}

const CLASS = {
  root: requireClass(styles.root, "virtuallist.module.css", "root"),
  sizer: requireClass(styles.sizer, "virtuallist.module.css", "sizer"),
  item: requireClass(styles.item, "virtuallist.module.css", "item"),
};

// A handful of rows above/below the viewport so fast scrolling and
// keyboard paging don't flash blank rows before the next frame paints.
const DEFAULT_OVERSCAN = 6;

/**
 * Windows `count` rows down to the visible range via @tanstack/react-virtual
 * (already a dependency): `renderRow(index)` is only called for rows near
 * the viewport, however large `count` is. Row sizes come from
 * `estimateSize` alone - no `measureElement` wiring, so it never re-reads
 * the DOM after mount, which is exactly right for the fixed/known-height
 * rows this wave's consumers have and keeps this a thin wrapper rather
 * than a dynamic-height layout engine.
 */
export function VirtualList({ count, estimateSize, renderRow, dynamic, ref }: VirtualListProps) {
  const scrollRef = useRef<HTMLDivElement>(null);

  const virtualizer = useVirtualizer({
    count,
    getScrollElement: () => scrollRef.current,
    estimateSize,
    overscan: DEFAULT_OVERSCAN,
  });

  useImperativeHandle(
    ref,
    () => ({
      scrollToIndex: (index, options) => virtualizer.scrollToIndex(index, options),
    }),
    [virtualizer],
  );

  return (
    <div ref={scrollRef} className={CLASS.root}>
      <div className={CLASS.sizer} style={{ height: virtualizer.getTotalSize() }}>
        {virtualizer.getVirtualItems().map((item) => (
          <div
            key={item.key}
            data-index={item.index}
            ref={dynamic ? virtualizer.measureElement : undefined}
            className={CLASS.item}
            style={{ height: item.size, transform: `translateY(${item.start}px)` }}
          >
            {renderRow(item.index)}
          </div>
        ))}
      </div>
    </div>
  );
}
