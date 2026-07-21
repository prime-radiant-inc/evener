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
import { type RefObject, useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import type { ThreadModel, TurnModel } from "../../../../protocol/model";
import { threadsStore } from "../../../../stores/threads";
import type { VirtualListHandle } from "../../../../widgets/virtuallist";
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
  /** True while a failed turn the reader hasn't seen yet is anchored (see
   * "the error anchor" below) - outranks pillNeedsYou per the pinned
   * contract (contracts-transcript-scroll-liveness.md §5: "error outranks
   * a simultaneous needs-you state"). Exposed independently of
   * pillNeedsYou rather than pre-resolved here: precedence is a rendering
   * decision for whoever consumes both (NewContentPill), not this hook's
   * job to collapse into one value. */
  pillError: boolean;
  /** Scrolls to the last turn and clears the pill - unless an error anchor
   * is active, in which case it jumps to THAT turn's index instead (see
   * "the error anchor" below). Also the target for a manual click on
   * NewContentPill. */
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

// The error-anchor's failure signal is TURN-level (this rewrite's own
// TurnModel.status/error, stamped by turn/completed - see reducer.test.ts's
// EventError-shape tests), not legacy's tool-call-level "did this one call
// fail" (contracts-transcript-scroll-liveness.md §5 line 114 talks about a
// finalized tool call; the w4 fix-round report's item-9 writeup already
// established turn.status === "failed" as this rewrite's equivalent
// signal, confirmed against appwire/types.go's TurnStatusFailed). error is
// `unknown` on TurnModel, so this only ever checks presence, never shape.
function isFailedTurn(turn: TurnModel): boolean {
  return turn.status === "failed" || turn.error !== undefined;
}

// A COUNT (not a boolean, not "the first failed turn's index/id") - failed-
// ness is terminal (a turn never un-fails) and turns only ever append or
// get prepended, never removed, so this only ever goes UP. Deliberately a
// count rather than the first-failed index: a turn's own failure can be
// resolved (seen live at the bottom, or anchored-then-cleared) while an
// EARLIER-index turn stays the "first" failed one by array position -
// pinning the dependency to "the first index" would then never change
// again and a genuinely new, later failure would never get a chance to be
// evaluated. Recomputed every render (same O(turns) cost class as
// totalItemCount above) but its VALUE is identical across a pure streaming
// delta (which touches item text/pendingText, never turn.status/error), so
// it doesn't defeat the "per-delta work never re-runs the content-changed
// effect" property - see that effect's own dependency-array comment.
function failedTurnCount(model: ThreadModel | undefined): number {
  if (!model) return 0;
  let n = 0;
  for (const turn of model.turns) if (isFailedTurn(turn)) n++;
  return n;
}

export function useTranscriptScroll({
  ref,
  model,
  listRef,
  loadOlder,
  measure = readScrollMetrics,
}: UseTranscriptScrollOptions): UseTranscriptScrollResult {
  const [pillCount, setPillCount] = useState(0);
  // The first failed turn's index, while the reader hasn't seen it yet
  // (null = no active anchor). State (not just a ref) because clearing it
  // from inside the scroll listener (see handleScroll below) must trigger
  // a re-render so pillError updates live, exactly like pillCount already
  // does for the same reason.
  const [errorAnchorIndex, setErrorAnchorIndex] = useState<number | null>(null);

  const wasAtBottomRef = useRef(true);
  const prevScrollHeightRef = useRef<number | null>(null);
  const firstTurnIdRef = useRef<string | undefined>(undefined);
  const baselineItemCountRef = useRef(0);
  const initializedRef = useRef(false);
  const debounceTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const pendingSaveRef = useRef<number | null>(null);
  // Turn IDs whose failure (if any) has already been accounted for - seen
  // live while at the bottom, already anchored-and-cleared, or currently
  // the active anchor (added the moment it's chosen - see the
  // content-changed effect below). Keyed by ID rather than a scan
  // POSITION/watermark deliberately: a turn can be observed as
  // not-yet-failed by one effect run (e.g. triggered by an unrelated
  // item's growth) and only fail on a LATER run - a position-based "how
  // far have I scanned" cutoff would already have advanced past it by
  // then and could never find it again, even after the dependency array
  // is fixed to notice the failure at all (this was the actual review
  // finding: the wire's real turn/completed EventError path streams items
  // normally, THEN settles via a bare stamp with no new items - see
  // failedTurnCount's own comment for the trigger half of that fix). IDs
  // are also prepend-safe for free: unlike errorAnchorIndex (a position,
  // shifted explicitly below), a turn's identity doesn't change when
  // older turns are prepended in front of it.
  const resolvedFailedTurnIdsRef = useRef<Set<string>>(new Set());
  // Latest-ref mirror of errorAnchorIndex (state) for the same reason
  // itemCountRef/turnsLengthRef/modelRef exist: handleScroll is a
  // long-lived closure (attached once per mount/hasContent transition, not
  // every render - see that effect's own comment) and the content-changed
  // effect's dependency array deliberately excludes it, so both must read
  // the CURRENT value through a ref rather than close over a stale one.
  const errorAnchorIndexRef = useRef<number | null>(null);
  errorAnchorIndexRef.current = errorAnchorIndex;

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
  // Content-changed effect trigger (see that effect's own dependency-array
  // comment for why itemCount/firstTurnId alone can't reach a bare-stamp
  // turn failure).
  const failedTurns = failedTurnCount(model);
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
    // The reader is caught up (this fires both on a manual scroll-to-bottom
    // and on jumpToBottom below) - any pending error anchor is resolved
    // too, the same "they saw it" reasoning the at-bottom append path
    // already uses when deciding whether to set one in the first place.
    // Synchronous ref assignment alongside the state setter, exactly like
    // baselineItemCountRef above: a caller that immediately re-reads
    // errorAnchorIndexRef in the same tick (handleScroll does) must see
    // the cleared value without waiting for the next render.
    setErrorAnchorIndex(null);
    errorAnchorIndexRef.current = null;
  }, []);

  const jumpToBottom = useCallback(() => {
    const anchor = errorAnchorIndexRef.current;
    if (anchor !== null) {
      // Jumps INTO the transcript, not to the bottom - wasAtBottomRef must
      // stay false, or the very next mutation's stick-to-bottom check would
      // yank the reader away from the row they just navigated to (see
      // "the error anchor" - the whole point of an anchor is to land THERE).
      listRef.current?.scrollToIndex(anchor, { align: "start" });
      wasAtBottomRef.current = false;
    } else {
      const count = turnsLengthRef.current;
      if (count > 0) listRef.current?.scrollToIndex(count - 1, { align: "end" });
      wasAtBottomRef.current = true;
    }
    clearPill();
  }, [listRef, clearPill]);

  // Mount / (re)attach. Guarded by initializedRef so the one-time restore-
  // or-default-to-bottom positioning and baseline recording happen exactly
  // once per "the scroll element became available" transition (initial
  // mount, or VirtualList mounting for the first time once a previously-
  // empty thread gets its first turn) - hasContent is what makes this rerun
  // for that later transition, since a plain RefObject mutation alone
  // triggers no rerun.
  // biome-ignore lint/correctness/useExhaustiveDependencies: both flags below are deliberate, re-verified against this rule - see the two comments inside
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
      // Turns present at mount (e.g. a cold-opened session whose history
      // already contains a failed turn) are not "newly appended" - see the
      // content-changed effect's failed-turn scan below. model is read
      // directly (not through modelRef) since this block runs exactly once,
      // at whichever render actually mounts - same reasoning as firstTurnId
      // just above, and the same pattern the mount effect's own closing
      // comment already documents for that field.
      for (const t of model?.turns ?? []) {
        if (isFailedTurn(t)) resolvedFailedTurnIdsRef.current.add(t.id);
      }
      initializedRef.current = true;
    }

    function handleScroll() {
      // el is already narrowed non-null above, but that narrowing doesn't
      // carry into this nested closure's own type - it's the same `const`,
      // never reassigned, so re-checking it here is a formality, not a real
      // possibility.
      if (!el) return;
      const m = measure(el);
      wasAtBottomRef.current = isAtBottom(m);
      if (wasAtBottomRef.current) clearPill();
      // The error anchor also clears on its own once its failed turn
      // scrolls into the rendered range - narrower than clearPill above
      // (only the anchor, not the rest of the pill: other still-unseen
      // content below it is unrelated and must stay counted). A1's
      // getVisibleRange() is exactly this widget-level lever; null (nothing
      // measured/rendered) reads as "don't know, assume not visible".
      const anchor = errorAnchorIndexRef.current;
      if (anchor !== null) {
        const range = listRef.current?.getVisibleRange();
        if (range && anchor >= range.startIndex && anchor <= range.endIndex) {
          setErrorAnchorIndex(null);
        }
      }
      // useTranscript.ts's own loadOlder has no internal catch - a rejected
      // thread/turns/list request propagates through its returned promise
      // uncaught unless the caller handles it; best-effort here, matching
      // Session.tsx's own ensureThread(ref).catch(() => {}) precedent for
      // the exact same shape of gap.
      // useTranscript.ts's own loadOlder has no internal catch - a rejected
      // thread/turns/list request propagates through its returned promise
      // uncaught unless the caller handles it; best-effort here, matching
      // Session.tsx's own ensureThread(ref).catch(() => {}) precedent for
      // the exact same shape of gap. (A dedicated unit test asserting "no
      // unhandledRejection fires" was attempted and abandoned - vitest's
      // own runner appears to intercept process-level unhandledRejection
      // dispatch in a way a per-test process.on listener can't reliably
      // observe here, so it couldn't discriminate the buggy state from the
      // fixed one; the full-suite exit-code check DOES catch this class of
      // regression - confirmed empirically: a genuinely uncaught rejection
      // exits 1 even though the individual test that triggered it "passes".)
      if (isNearTop(m.scrollTop)) loadOlderRef.current().catch(() => {});
      schedulePersist(m.scrollTop);
    }

    el.addEventListener("scroll", handleScroll);
    return () => el.removeEventListener("scroll", handleScroll);
    // firstTurnId is intentionally NOT a dependency: it's only read inside
    // the initializedRef-guarded one-time block above, which - since
    // initializedRef never resets - executes exactly once per mount, at
    // whichever render actually flips hasContent (or the first render, if
    // content is there from the start). That render's closure already has
    // the fresh firstTurnId; every later change to firstTurnId (e.g. a
    // loadOlder prepend) happens after initializedRef.current is already
    // true, when this gated block no longer runs at all - so a later
    // effect re-run with a stale firstTurnId in its closure, if one ever
    // happened, still wouldn't read it. The listener itself also needs no
    // re-attachment when content changes: schedulePersist/clearPill/
    // loadOlderRef.current all read fresh state at call time regardless.
    //
    // hasContent is listed despite not being read in this effect's body at
    // all - it exists purely to force a re-run at the "VirtualList mounts
    // for the first time" transition (a ref becoming non-null triggers no
    // re-render/effect on its own; hasContent flipping does).
  }, [ref, listRef, measure, clearPill, schedulePersist, hasContent]);

  // Flushes any pending debounced persistence write on true unmount (a fast
  // tab-switch right after scrolling must not lose it) - a dedicated,
  // dep-stable effect so this ONLY fires on unmount, not on every content
  // change the listener-attach effect above might otherwise re-run for.
  useEffect(() => {
    return () => flushPendingSave();
  }, [flushPendingSave]);

  // Content-changed reaction: fires only when the turn/item SHAPE actually
  // changes (item count, the first turn's identity, or the failed-turn
  // count, all primitives) - never on a pure streaming-text delta, which
  // changes model.turns's object reference but none of those values, so
  // React skips re-running an effect whose primitive deps didn't change.
  // biome-ignore lint/correctness/useExhaustiveDependencies: failedTurns is deliberately trigger-only (never read in the body below) - see its own doc comment above for why a bare-stamp turn failure needs it anyway
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
        // misattribute; re-baseline entirely rather than guess. An existing
        // anchor's index can't be trusted to still mean anything, and
        // neither can the resolved-turn bookkeeping (a turn's very identity
        // may not be stable across an unpredictable resync) - drop it
        // wholesale rather than risk stale entries.
        baselineItemCountRef.current = itemCount;
        resolvedFailedTurnIdsRef.current = new Set();
        setErrorAnchorIndex(null);
      } else {
        const prependedCount = currentModel.turns.slice(0, prevIndex).reduce((sum, t) => sum + t.items.length, 0);
        // Prepended history is backfill, not "new" - advance the baseline by
        // exactly what loadOlder added so the pill count stays unaffected.
        baselineItemCountRef.current += prependedCount;
        // prevIndex IS the count of turns just prepended (it's where the
        // old first turn now sits) - an active anchor's INDEX shifts by
        // that same amount, or it'd silently point at the wrong turn from
        // here on (resolvedFailedTurnIdsRef needs no such shift - it's
        // keyed by turn ID, not position).
        if (errorAnchorIndexRef.current !== null) {
          setErrorAnchorIndex(errorAnchorIndexRef.current + prevIndex);
        }
        // Prepended (historical) turns are backfill, not new - same
        // "backfill, not new" reasoning as baselineItemCountRef just above,
        // applied to failure tracking: a failed turn the reader is only
        // now paging UP into is already-known history, not a live event to
        // anchor on. Without this, a later append-triggered scan could find
        // it as "the first unresolved failed turn" and wrongly anchor on
        // stale history instead of (or ahead of) a genuinely new failure.
        for (const t of currentModel.turns.slice(0, prevIndex)) {
          if (isFailedTurn(t)) resolvedFailedTurnIdsRef.current.add(t.id);
        }

        const prevScrollHeight = prevScrollHeightRef.current;
        if (prevScrollHeight !== null) {
          const m = measure(el);
          el.scrollTop = m.scrollTop + (m.scrollHeight - prevScrollHeight);
        }
      }
    } else {
      // Failed-turn tracking runs independent of item growth - the real
      // wire's turn/completed EventError path settles with a BARE stamp
      // (no items - see isFailedTurn's own comment), so a failure can
      // arrive without ever moving `unseen` off zero below. failedTurns
      // (the dependency that gets this effect to fire at all for that
      // case) is what makes this reachable; wasAtBottomRef alone then
      // decides the outcome: at the bottom, every currently-unresolved
      // failure is "seen" and resolved in bulk (matching "a failed turn
      // arriving at the bottom never creates an anchor"); scrolled away,
      // the FIRST unresolved one becomes the anchor, but only while none is
      // already active (contracts §5's anchor points at a single row - a
      // later failure doesn't steal it, it stays pending for its own turn
      // once this one clears).
      if (currentModel) {
        if (wasAtBottomRef.current) {
          for (const t of currentModel.turns) {
            if (isFailedTurn(t)) resolvedFailedTurnIdsRef.current.add(t.id);
          }
        } else if (errorAnchorIndexRef.current === null) {
          const firstUnresolved = currentModel.turns.find(
            (t) => isFailedTurn(t) && !resolvedFailedTurnIdsRef.current.has(t.id),
          );
          if (firstUnresolved) {
            resolvedFailedTurnIdsRef.current.add(firstUnresolved.id);
            setErrorAnchorIndex(currentModel.turns.indexOf(firstUnresolved));
          }
        }
      }

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
    // failedTurns is the one exception to "primitives derived from model
    // don't need model itself in this list" being sufficient: a turn's
    // failure can flip with NEITHER itemCount NOR firstTurnId changing (the
    // bare-stamp settle above), so without it this whole failed-turn branch
    // would silently never run for that real wire shape.
  }, [itemCount, firstTurnId, failedTurns, listRef, measure]);

  return {
    pillCount,
    pillNeedsYou: pillCount > 0 && isAttentionWorthy(model),
    pillError: errorAnchorIndex !== null,
    jumpToBottom,
  };
}
