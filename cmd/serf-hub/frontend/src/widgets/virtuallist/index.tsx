import { type ScrollToOptions, useVirtualizer, type Virtualizer } from "@tanstack/react-virtual";
import { type ReactNode, type Ref, useImperativeHandle, useRef } from "react";
import { requireClass } from "../internal/requireClass";
import styles from "./virtuallist.module.css";

export interface VirtualListHandle {
  scrollToIndex: (index: number, options?: ScrollToOptions) => void;
  /**
   * The raw scrolling DOM node (this widget's own `root`, the one element
   * with `overflow-y: auto`) - an escape hatch for a consumer that needs
   * scroll-behavior concerns VirtualList deliberately doesn't own itself
   * (stick-to-bottom, near-top paging triggers, persisted scroll-position
   * restore; see the session transcript pane's flow/ hooks). Returns null
   * before mount, same as any ref.
   */
  getScrollElement: () => HTMLDivElement | null;
  /**
   * The index range currently rendered (including overscan - the same
   * window `renderRow` is called over, per this widget's own top-of-file
   * comment), derived from @tanstack/react-virtual's own getVirtualItems()
   * (already called by this widget's render; just never surfaced past it
   * before now). null when nothing has been measured/rendered yet (e.g.
   * before mount, or `count === 0`) - a consumer that needs "is index N
   * currently visible" (the session transcript's error-anchor pill - see
   * flow/useTranscriptScroll.ts) treats null as "don't know, assume not
   * visible" rather than crashing.
   */
  getVisibleRange: () => { startIndex: number; endIndex: number } | null;
}

export interface VirtualListProps {
  count: number;
  estimateSize: (index: number) => number;
  renderRow: (index: number) => ReactNode;
  /**
   * Opt in to post-mount remeasurement (@tanstack/react-virtual's
   * `measureElement`): each rendered row's real DOM height corrects its
   * cached size instead of `estimateSize` staying authoritative forever.
   * Off by default so every fixed/known-height consumer (e.g. the gallery
   * demo) is completely unaffected - only a consumer whose rows vary
   * unpredictably (e.g. the session transcript's turn list: one tool call
   * vs. a long streamed response) needs this.
   */
  dynamic?: boolean;
  /**
   * Keys each rendered row by a stable identity instead of react-virtual's
   * default (raw index). Needed by any consumer that PREPENDS rows (the
   * session transcript's older-turn paging): without it, an index shift
   * makes React reuse each row's DOM node - and dynamic mode's measured-
   * height cache, which is also keyed by this same identity - for a
   * DIFFERENT logical row than before, corrupting both live DOM state and
   * cached sizes. Omitted (the default) leaves every existing fixed-
   * identity consumer (e.g. the gallery demo) completely unaffected.
   */
  getItemKey?: (index: number) => string | number;
  /**
   * Called after react-virtual changes its rendered range or measurements.
   * Consumers can finish layout work that depends on a row becoming measured
   * without timers or polling. The arguments are exactly upstream's onChange
   * (the virtualizer instance and its sync flag); a consumer that only needs
   * "something changed" ignores both, as before.
   */
  onChange?: (instance: Virtualizer<HTMLDivElement, HTMLDivElement>, sync: boolean) => void;
  /**
   * Opt in to end-anchored following (virtual-core 3.17's anchorTo:"end" +
   * followOnAppend - the locked dependency tree already carries it, via
   * react-virtual 3.14.7's own @tanstack/virtual-core@3.17.5 pin). Meant for
   * live-append lists like the session transcript:
   *  - While the reader is within END_ANCHOR_THRESHOLD_PX of the end, row
   *    re-measurements (dynamic settling after mount, a streaming last row
   *    growing) keep the viewport pinned to the TRUE end - so "open at the
   *    latest turn" survives the estimate->measured correction instead of
   *    stranding the reader mid-transcript.
   *  - An append while the reader is at the end follows it; an append while
   *    the reader has scrolled back does NOT move the viewport (the at-end
   *    check reads real DOM geometry at append time, so it can't be fooled
   *    by a stale "was at bottom" flag).
   *  - A prepend (loadOlder paging) keeps the currently-visible row anchored
   *    instead of jumping - the compensation useTranscriptScroll used to do
   *    by hand, done by the virtualizer with per-item precision.
   * Off by default: fixed-height consumers (the gallery demo) want none of
   * this. Requires getItemKey to be meaningful (the follow/anchor decisions
   * are keyed) - both transcript consumers already pass one.
   */
  anchorToEnd?: boolean;
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

// How close to the true end (px) counts as "following the tail" when
// anchorToEnd is on. Matches the transcript scroll metrics' own
// AT_BOTTOM_THRESHOLD_PX (panes/session/transcript/flow/scrollMetrics.ts) -
// legacy renderer.js parity (docs/web-ui/parity/parity-m4-transcript.md
// §15): "within 50px of true bottom". Duplicated rather than imported
// because the import direction is panes -> widgets, never the reverse.
const END_ANCHOR_THRESHOLD_PX = 50;

/**
 * Windows `count` rows down to the visible range via @tanstack/react-virtual
 * (already a dependency): `renderRow(index)` is only called for rows near
 * the viewport, however large `count` is. Row sizes come from
 * `estimateSize` alone - no `measureElement` wiring, so it never re-reads
 * the DOM after mount, which is exactly right for the fixed/known-height
 * rows this wave's consumers have and keeps this a thin wrapper rather
 * than a dynamic-height layout engine.
 */
export function VirtualList({
  count,
  estimateSize,
  renderRow,
  dynamic,
  getItemKey,
  onChange,
  anchorToEnd,
  ref,
}: VirtualListProps) {
  const scrollRef = useRef<HTMLDivElement>(null);

  const virtualizer = useVirtualizer({
    count,
    getScrollElement: () => scrollRef.current,
    estimateSize,
    overscan: DEFAULT_OVERSCAN,
    getItemKey,
    onChange,
    ...(anchorToEnd
      ? { anchorTo: "end" as const, followOnAppend: true, scrollEndThreshold: END_ANCHOR_THRESHOLD_PX }
      : {}),
  });

  useImperativeHandle(
    ref,
    () => ({
      scrollToIndex: (index, options) => virtualizer.scrollToIndex(index, options),
      getScrollElement: () => scrollRef.current,
      getVisibleRange: () => {
        const items = virtualizer.getVirtualItems();
        const first = items[0];
        const last = items[items.length - 1];
        if (!first || !last) return null;
        return { startIndex: first.index, endIndex: last.index };
      },
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
            // dynamic mode never writes an inline height here: measureElement
            // reads this same element's offsetHeight (both on mount and via
            // ResizeObserver), so if the virtualizer's own item.size were
            // written back as this element's height, the read would always
            // just play back what was last written - no box-size change is
            // ever observable, and a row's real content height is never
            // adopted (T5b's live finding: a settled row stayed pinned at its
            // 96px estimate while its real content measured 337px, silently
            // overlapping the next row). Leaving height unset lets the box's
            // rendered size come from the content alone (the .item class's
            // position: absolute takes it out of flow, so this never affects
            // layout of anything else) - non-dynamic rows are unaffected,
            // keeping their known/fixed height exactly as before.
            style={
              dynamic
                ? { transform: `translateY(${item.start}px)` }
                : { height: item.size, transform: `translateY(${item.start}px)` }
            }
          >
            {renderRow(item.index)}
          </div>
        ))}
      </div>
    </div>
  );
}
