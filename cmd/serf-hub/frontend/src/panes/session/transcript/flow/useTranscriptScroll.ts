// useTranscriptScroll: the transcript pane's one scroll-behavior hook -
// stick-to-bottom, the new-content pill's count/needs-you state, near-top
// loadOlder triggering, prepend scroll-anchor correction, and per-ref
// scroll-position persistence. Kept as a single hook (rather than several
// independently-attaching ones) because every one of these concerns reads
// and reacts to the SAME scroll element and the SAME "did the turn/item
// shape change" signal - splitting them would mean multiple native scroll
// listeners racing on one DOM node for no benefit.
//
// Design notes (see this task's report for the fuller reasoning):
//  - "At bottom before the mutation" is measured continuously by the native
//    scroll listener into wasAtBottomRef, NOT re-measured inside the
//    content-changed effect (which runs AFTER the DOM already committed the
//    new turns/items) - the ref always reflects the last REAL scroll
//    position, which is inherently "pre-mutation" relative to whatever
//    content change happens next.
//  - `measure` is the injectable seam this wave's binding constraints call
//    for (jsdom performs no real layout - see scrollMetrics.ts and
//    VirtualList's own test suite doc comment); production defaults to
//    reading the real DOM, tests substitute a controlled fake.
//  - A prepend is detected by diffing the FIRST turn's id across renders,
//    not an out-of-band "a loadOlder call is in flight" flag: a live append
//    can land while a loadOlder request is still in flight, and diffing the
//    data's own shape stays correct regardless of that interleaving.
import { useCallback, useEffect, useLayoutEffect, useRef, useState, type RefObject } from "react";
import type { ThreadModel } from "../../../../protocol/model";
import type { VirtualListHandle } from "../../../../widgets/virtuallist";
import { threadsStore } from "../../../../stores/threads";
import { isAtBottom, isNearTop, readScrollMetrics, type ScrollMetrics } from "./scrollMetrics";

export interface UseTranscriptScrollOptions {
  ref: string;
  model: ThreadModel | undefined;
  listRef: RefObject<VirtualListHandle | null>;
  loadOlder: () => Promise<void>;
  /** Injectable measurement seam - defaults to the real DOM (readScrollMetrics). */
  measure?: (el: HTMLElement) => ScrollMetrics;
}

export interface UseTranscriptScrollResult {
  /** Items rendered since the reader last was at (or returned to) the bottom. */
  pillCount: number;
  /** True while the pill is showing AND the thread is currently attention-worthy
   * (askPending, or a status the pane's own Cadence mapping treats as needs-you) -
   * recomputed live every render, so a later status flip upgrades the pill
   * in place even if it lands after the content that produced it. */
  pillNeedsYou: boolean;
  /** Scrolls to the last turn and clears the pill (also the target for a
   * manual click on NewContentPill). */
  jumpToBottom: () => void;
}

// A short coalescing window for the scroll-position persistence write only
// (never for the stick/pill/near-top decisions themselves, which react
// immediately) - nobody needs the stored value to be live-accurate every
// pixel, only eventually-accurate for the next mount, and every open pane
// sharing this store doesn't need a setState per scrolled pixel.
const PERSIST_DEBOUNCE_MS = 200;

function totalItemCount(model: ThreadModel | undefined): number {
  if (!model) return 0;
  let total = 0;
  for (const turn of model.turns) total += turn.items.length;
  return total;
}

// Mirrors Session.tsx's own cadenceStateForStatus mapping (awaiting/warning
// -> needs-you) rather than importing it: flow/ is composed BY Session.tsx,
// not the other way around (same "deliberately separate, parallel small
// mapping function" precedent Session.tsx itself follows relative to
// shell/rail/RailRow.tsx's cadenceStateFor - see its own comment). askPending
// is checked independently since it need not always coincide with status.type.
function isAttentionWorthy(model: ThreadModel | undefined): boolean {
  if (!model) return false;
  return model.askPending || model.status.type === "awaiting" || model.status.type === "warning";
}

export function useTranscriptScroll({
  ref,
  model,
  listRef,
  loadOlder,
  measure = readScrollMetrics,
}: UseTranscriptScrollOptions): UseTranscriptScrollResult {
  const [pillCount, setPillCount] = useState(0);

  const wasAtBottomRef = useRef(true);
  const prevScrollHeightRef = useRef<number | null>(null);
  const firstTurnIdRef = useRef<string | undefined>(undefined);
  const baselineItemCountRef = useRef(0);
  const initializedRef = useRef(false);
  const debounceTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const pendingSaveRef = useRef<number | null>(null);

  // "Latest" ref so the scroll listener - attached far less often than every
  // render - never invokes a stale loadOlder closure. Necessary specifically
  // because useTranscript.ts's own loadOlder callback changes identity on
  // every loadingOlder flip (its de-dupe guard reads loadingOlder from that
  // same closure); calling a stale one could read a stale de-dupe flag.
  const loadOlderRef = useRef(loadOlder);
  loadOlderRef.current = loadOlder;

  const itemCount = totalItemCount(model);
  const turnsLength = model?.turns.length ?? 0;
  const firstTurnId = model?.turns[0]?.id;
  const hasContent = turnsLength > 0;
  // Also kept in refs for the scroll listener/jumpToBottom, which are not
  // re-created on every render (see the effects below).
  const itemCountRef = useRef(itemCount);
  itemCountRef.current = itemCount;
  const turnsLengthRef = useRef(turnsLength);
  turnsLengthRef.current = turnsLength;
  // Latest ref for model itself: the content-changed effect below needs the
  // full turns array (to size a detected prepend), but must NOT re-run on
  // every streaming delta just because `model` is a fresh object reference -
  // its dependency array is itemCount/firstTurnId (primitives) precisely so
  // a pure text-delta re-render (which changes neither) is free, matching
  // the wave's own "per-delta work never re-renders the settled transcript"
  // constraint in spirit even though this hook's own work is cheap either way.
  const modelRef = useRef(model);
  modelRef.current = model;

  const flushPendingSave = useCallback(() => {
    if (debounceTimerRef.current !== null) {
      clearTimeout(debounceTimerRef.current);
      debounceTimerRef.current = null;
    }
    if (pendingSaveRef.current !== null) {
      threadsStore.getState().setScrollPosition(ref, pendingSaveRef.current);
      pendingSaveRef.current = null;
    }
  }, [ref]);

  const schedulePersist = useCallback(
    (position: number) => {
      pendingSaveRef.current = position;
      if (debounceTimerRef.current !== null) clearTimeout(debounceTimerRef.current);
      debounceTimerRef.current = setTimeout(flushPendingSave, PERSIST_DEBOUNCE_MS);
    },
    [flushPendingSave],
  );

  const clearPill = useCallback(() => {
    setPillCount(0);
    baselineItemCountRef.current = itemCountRef.current;
  }, []);

  const jumpToBottom = useCallback(() => {
    const count = turnsLengthRef.current;
    if (count > 0) listRef.current?.scrollToIndex(count - 1, { align: "end" });
    wasAtBottomRef.current = true;
    clearPill();
  }, [listRef, clearPill]);

  // Mount / (re)attach. Guarded by initializedRef so the one-time restore-
  // or-default-to-bottom positioning and baseline recording happen exactly
  // once per "the scroll element became available" transition (initial
  // mount, or VirtualList mounting for the first time once a previously-
  // empty thread gets its first turn) - hasContent is what makes this rerun
  // for that later transition, since a plain RefObject mutation alone
  // triggers no rerun.
  useLayoutEffect(() => {
    const el = listRef.current?.getScrollElement();
    if (!el) return;

    if (!initializedRef.current) {
      const saved = threadsStore.getState().scrollPositions.get(ref);
      if (saved !== undefined) {
        el.scrollTop = saved;
      } else {
        const count = turnsLengthRef.current;
        if (count > 0) listRef.current?.scrollToIndex(count - 1, { align: "end" });
      }
      const m = measure(el);
      wasAtBottomRef.current = isAtBottom(m);
      prevScrollHeightRef.current = m.scrollHeight;
      firstTurnIdRef.current = firstTurnId;
      baselineItemCountRef.current = itemCountRef.current;
      initializedRef.current = true;
    }

    function handleScroll() {
      const m = measure(el!);
      wasAtBottomRef.current = isAtBottom(m);
      if (wasAtBottomRef.current) clearPill();
      if (isNearTop(m.scrollTop)) void loadOlderRef.current();
      schedulePersist(m.scrollTop);
    }

    el.addEventListener("scroll", handleScroll);
    return () => el.removeEventListener("scroll", handleScroll);
    // firstTurnId is intentionally NOT a dependency: it's only read inside
    // the initializedRef-guarded one-time block above, and the listener
    // itself needs no re-attachment when content changes (schedulePersist/
    // clearPill/loadOlderRef.current all read fresh state at call time).
    // (No react-hooks/exhaustive-deps lint rule is configured in this
    // project - this deliberate omission is only documented here, not
    // suppressed.)
  }, [ref, listRef, measure, clearPill, schedulePersist, hasContent]);

  // Flushes any pending debounced persistence write on true unmount (a fast
  // tab-switch right after scrolling must not lose it) - a dedicated,
  // dep-stable effect so this ONLY fires on unmount, not on every content
  // change the listener-attach effect above might otherwise re-run for.
  useEffect(() => {
    return () => flushPendingSave();
  }, [flushPendingSave]);

  // Content-changed reaction: fires only when the turn/item SHAPE actually
  // changes (item count or the first turn's identity, both primitives) -
  // never on a pure streaming-text delta, which changes model.turns's
  // object reference but neither of these values, so React skips re-running
  // an effect whose primitive deps didn't change.
  useLayoutEffect(() => {
    if (!initializedRef.current) return;
    const el = listRef.current?.getScrollElement();
    if (!el) return;

    const currentModel = modelRef.current;
    const prevFirstTurnId = firstTurnIdRef.current;
    const isPrepend = prevFirstTurnId !== undefined && firstTurnId !== undefined && firstTurnId !== prevFirstTurnId;

    if (isPrepend && currentModel) {
      const prevIndex = currentModel.turns.findIndex((t) => t.id === prevFirstTurnId);
      if (prevIndex === -1) {
        // Not a simple prepend (e.g. a full resync after reconnect) - don't
        // misattribute; re-baseline entirely rather than guess.
        baselineItemCountRef.current = itemCount;
      } else {
        const prependedCount = currentModel.turns.slice(0, prevIndex).reduce((sum, t) => sum + t.items.length, 0);
        // Prepended history is backfill, not "new" - advance the baseline by
        // exactly what loadOlder added so the pill count stays unaffected.
        baselineItemCountRef.current += prependedCount;

        const prevScrollHeight = prevScrollHeightRef.current;
        if (prevScrollHeight !== null) {
          const m = measure(el);
          el.scrollTop = m.scrollTop + (m.scrollHeight - prevScrollHeight);
        }
      }
    } else {
      const unseen = itemCount - baselineItemCountRef.current;
      if (unseen > 0) {
        if (wasAtBottomRef.current) {
          const count = turnsLengthRef.current;
          if (count > 0) listRef.current?.scrollToIndex(count - 1, { align: "end" });
          wasAtBottomRef.current = true;
          baselineItemCountRef.current = itemCount;
        } else {
          setPillCount(unseen);
        }
      }
    }

    firstTurnIdRef.current = firstTurnId;
    prevScrollHeightRef.current = measure(el).scrollHeight;
    // model is read via modelRef.current (see above), not closed over here,
    // specifically so this effect does NOT re-run on every streaming delta.
  }, [itemCount, firstTurnId, listRef, measure]);

  return {
    pillCount,
    pillNeedsYou: pillCount > 0 && isAttentionWorthy(model),
    jumpToBottom,
  };
}
