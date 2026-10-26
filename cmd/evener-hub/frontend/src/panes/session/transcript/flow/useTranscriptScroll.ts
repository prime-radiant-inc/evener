// useTranscriptScroll: the transcript pane's one scroll-behavior hook -
// stick-to-bottom, the new-content pill's count/needs-you state, near-top
// loadOlder triggering, and view-mode scroll-anchor capture/restore. Kept
// as a single hook (rather than several independently-attaching ones)
// because every one of these concerns reads and reacts to the SAME scroll
// element and the SAME "did the turn/item shape change" signal - splitting
// them would mean multiple native scroll listeners racing on one DOM node
// for no benefit.
//
// What this hook deliberately does NOT own anymore: raw scroll-position
// COMPENSATION. Prepend (loadOlder) anchoring, estimate->measured settle
// correction, and follow-on-append are the VirtualList's own job since it
// grew an end-anchor (widgets/virtuallist's anchorToEnd, backed by
// virtual-core 3.17's anchorTo:"end" + followOnAppend): the virtualizer
// reads REAL DOM geometry at the moment of the mutation, where this hook's
// hand-rolled scrollTop math had to trust estimated sizes and could strand
// a freshly-opened session mid-transcript (or mis-time a yank). Doing both
// would double-compensate every prepend.
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

import type { ThreadModel } from "@evener/appwire-client";
import { type RefObject, useCallback, useEffect, useLayoutEffect, useReducer, useRef, useState } from "react";
import { useStore } from "zustand";
import { isEditableTarget, isIMECompositionKeydown } from "../../../../keybindings/dispatcher";
import { isEntryTranscriptLocalOpening, threadsStore } from "../../../../stores/threads";
import type { CommittedVirtualListLayout, VirtualListHandle } from "../../../../widgets";
import type { TranscriptReadView } from "../transcriptReadView";
import { isDormantTranscript } from "../transcriptVisibility";
import {
  contentGrewBelowViewport,
  contentIsUnmeasured,
  isAtBottom,
  isEndBelowFold,
  isNearTop,
  portGeometryTargets,
  readScrollMetrics,
  type ScrollMetrics,
  shouldAutoLoadOlder,
} from "./scrollMetrics";
import {
  DRAG_BUTTON_BITS,
  failedTurnCount,
  isAttentionWorthy,
  isFailedTurn,
  MIDDLE_BUTTON_BIT,
  totalItemCount,
  verticalInputCanMovePort,
} from "./scrollPredicates";
import {
  type CapturedTranscriptView,
  completeTranscriptViewRestore,
  registerTranscriptView,
  retainedTranscriptCapture,
  retainTranscriptView,
} from "./transcriptViewRegistry";
import {
  anchorFromCapture,
  captureTopAnchor,
  captureTranscriptFocus,
  captureTranscriptView,
  focusCapturedEntry,
  type PendingTranscriptViewRestore,
  positionForAnchor,
  type RestoredViewAnchor,
  readAnchorPositions,
  readingPointOffset,
  restoreTopAnchor,
  retainedPlacements,
  topVisiblePosition,
  type ViewAnchor,
  type ViewAnchorPosition,
} from "./viewAnchor";

export interface UseTranscriptScrollOptions {
  ref: string;
  /** Mounted pane lifetime, including same-view model reloads. */
  viewId?: string;
  keepViewRemount?: () => boolean;
  model: ThreadModel | undefined;
  listRef: RefObject<VirtualListHandle | null>;
  loadOlder: () => Promise<void>;
  cancelOlder?: () => void;
  /** Injectable measurement seam - defaults to the real DOM (readScrollMetrics). */
  measure?: (el: HTMLElement) => ScrollMetrics;
  /** Identity of the currently rendered transcript representation. */
  viewKey?: string;
  /** A retained reader restores its measured view instead of landing fresh at the end. */
  initialViewCapture?: CapturedTranscriptView;
  onReaderIntent?: () => void;
  onReaderMovement?: (beforeOffset: number) => void;
  /** Injectable stable-entry geometry seam; production reads data-view-anchor elements. */
  measureAnchors?: (el: HTMLElement) => ViewAnchorPosition[];
  /** All entries in the active representation, including those in virtualized-out rows. */
  anchorEntries?: readonly Omit<ViewAnchorPosition, "offset" | "height">[];
  /** Number of rows in the active transformed representation. */
  renderedRowCount?: number;
  /** Source turn id to active transformed-row index. */
  sourceTurnRowIndexes?: ReadonlyMap<string, number>;
  /**
   * The session pane's pending-questions dock is a virtual row
   * (TranscriptBody's trailingRow), and an in-progress ask_user item
   * COMPLETING activates it without any turn/item shape change - neither
   * itemCount nor firstTurnId nor failedTurns moves, so the content-changed
   * effect never fires for it. This option carries the dock's own pending
   * signal (askDockStore via Session.tsx's useAskDockPending) so the hook
   * can treat its rising edge as new content: a reader who is not at the
   * bottom gets the new-content pill (needs-you styling comes free -
   * isAttentionWorthy reads the wire's askPending), and jumpToBottom lands
   * on the dock row because renderedRowCount counts it (same PR's count
   * fix). A reader at the bottom gets nothing: the end-anchored list
   * already followed the appended row into view.
   */
  askDockPending?: boolean;
  /**
   * The dock pending set's activation counter (askDockStore's
   * activationEpoch). The pill edge keys on THIS, not the boolean: a
   * snapshot resync can atomically replace the pending set (old batch
   * answered elsewhere, new one pending) without the boolean ever leaving
   * true, and that replacement is exactly the moment a scrolled-away reader
   * most needs the pill. Same-set additions don't bump it - the reader was
   * already told.
   */
  askDockActivationEpoch?: number;
  /** Whether a held-steering trailing row mounts an otherwise dormant transcript. */
  heldVisible?: boolean;
  /**
   * The held-steering ghost stack's arrival counter (Session's
   * useHeldSteerEpoch). Same role as askDockActivationEpoch: a held steer
   * APPEARING changes no turn/item shape, so this carries the arrival edge
   * for the pill. Removals never bump it (announced, not counted), so a
   * departure leaves the pill alone.
   */
  heldEpoch?: number;
}

// The end-anchored VirtualList owns the transcript's position; this is the one
// call that tells it "the end". The mount landing, an append's follow, and a
// leading-edge change's re-target all make exactly this request.
function scrollToLastRow(listRef: RefObject<VirtualListHandle | null>, count: number): void {
  if (count > 0) listRef.current?.scrollToIndex(count - 1, { align: "end" });
}

export interface UseTranscriptViewRegistrationOptions {
  enabled: boolean;
  id: string;
  logicalRef?: string;
  layout?: string;
  viewKey?: string;
  initialViewCapture?: CapturedTranscriptView;
  readView?: TranscriptReadView;
  listRef?: RefObject<VirtualListHandle | null>;
  measure?: (el: HTMLElement) => ScrollMetrics;
  measureAnchors?: (el: HTMLElement) => ViewAnchorPosition[];
  anchorEntries?: readonly Omit<ViewAnchorPosition, "offset" | "height">[];
  renderedRowCount?: number;
  /** Restore DOM focus only while this view still owns workspace focus. */
  canRestoreFocus?: () => boolean;
  focusFallback?: () => void;
  announce?: (summary: string) => void;
}

export interface UseTranscriptViewRegistrationResult {
  restoreAfterMeasurement(): void;
  hasRetainedPlacement(): boolean;
  isPlacementPending(): boolean;
  restoreAfterLayout(layout: CommittedVirtualListLayout): void;
}

function captureIsCurrent(captured: CapturedTranscriptView, readView?: TranscriptReadView): boolean {
  return (
    !readView ||
    (readView.alive &&
      (captured.positioningRevision === undefined || captured.positioningRevision === readView.positioningRevision))
  );
}

function viewportChanged(captured: CapturedTranscriptView | undefined, el: HTMLElement): boolean {
  const point = captured?.readingPoint;
  return !!point && (point.viewportWidth !== el.clientWidth || point.viewportHeight !== el.clientHeight);
}

/**
 * Registers the shared body with the transition registry without taking
 * ownership of ordinary append/prepend scrolling. The body calls the result
 * from VirtualList's measurement callback; the view-key layout effect covers
 * hosts whose row measurement callback does not fire for a config commit.
 */
export function useTranscriptViewRegistration(
  options: UseTranscriptViewRegistrationOptions,
): UseTranscriptViewRegistrationResult {
  const { enabled, id, logicalRef, layout, readView } = options;
  const [, retryOpeningPlacement] = useReducer((revision: number) => revision + 1, 0);
  const optionsRef = useRef(options);
  optionsRef.current = options;
  const latestCaptureRef = useRef<CapturedTranscriptView | undefined>(undefined);
  const committedLayoutRef = useRef<CommittedVirtualListLayout | undefined>(undefined);
  const pendingRef = useRef<PendingTranscriptViewRestore | null>(
    options.initialViewCapture && captureIsCurrent(options.initialViewCapture, readView)
      ? {
          captured: options.initialViewCapture,
          readView,
          positioningRevision: readView?.positioningRevision,
          restoreFocus: true,
          scrollRequested: false,
          anchorRestored: false,
          focusScrollRequested: false,
          viewportReflow: false,
        }
      : null,
  );
  const placementIdentityRef = useRef({ id, logicalRef });
  if (placementIdentityRef.current.id !== id || placementIdentityRef.current.logicalRef !== logicalRef) {
    // Child measurement effects run before registration effects. A reused
    // pane must discard the old demand before those children can consume it.
    pendingRef.current = null;
    latestCaptureRef.current = undefined;
    committedLayoutRef.current = undefined;
    // Completed placement is also owned by that identity, not by the reused
    // DOM port. Keep it for same-view child-to-parent initialization only.
    const el = options.listRef?.current?.getScrollElement();
    if (el) retainedPlacements.delete(el);
    placementIdentityRef.current = { id, logicalRef };
  }

  const restoreAfterMeasurement = useCallback(() => {
    const pending = pendingRef.current;
    if (!pending) return;
    const currentOptions = optionsRef.current;
    const el = currentOptions.listRef?.current?.getScrollElement();
    const committed = committedLayoutRef.current;
    if (
      pending.readView !== currentOptions.readView ||
      pending.positioningRevision !== currentOptions.readView?.positioningRevision ||
      !captureIsCurrent(pending.captured, currentOptions.readView)
    ) {
      pendingRef.current = null;
      latestCaptureRef.current = undefined;
      if (el && committed?.virtualizer.scrollElement === el) committed.cancelPendingScroll();
      return;
    }
    if (!currentOptions.enabled || !el || !committed?.isCurrent() || committed.virtualizer.scrollElement !== el) return;
    if (currentOptions.logicalRef !== undefined && isEntryTranscriptLocalOpening(currentOptions.logicalRef)) return;

    const confirmScrollOffset = () => {
      if (Math.abs(el.scrollTop - (committed.virtualizer.scrollOffset ?? 0)) > 1.5) {
        committed.cancelPendingScroll();
      }
      return committed.isCurrent() && Math.abs(el.scrollTop - (committed.virtualizer.scrollOffset ?? 0)) <= 1.5;
    };
    const measure = currentOptions.measure ?? readScrollMetrics;
    const measureAnchors = currentOptions.measureAnchors ?? readAnchorPositions;
    const measured = measureAnchors(el);
    const candidates = currentOptions.anchorEntries?.map((entry) => ({ ...entry, offset: 0 })) ?? measured;
    if (pending.captured.followingBottom) {
      const count = currentOptions.renderedRowCount ?? 0;
      if (count > 0 && !pending.scrollRequested) {
        pending.scrollRequested = true;
        currentOptions.listRef?.current?.scrollToIndex(count - 1, { align: "end" });
      }
      if (!committed.isCurrent() || !isAtBottom(measure(el))) return;
      if (!confirmScrollOffset()) return;
      if (pending.restoreFocus && currentOptions.canRestoreFocus?.() !== false) {
        const focusResult = focusCapturedEntry(el, pending.captured, candidates, currentOptions.listRef, pending);
        if (focusResult === "waiting") return;
        if (focusResult === "missing") currentOptions.focusFallback?.();
      }
      pendingRef.current = null;
      latestCaptureRef.current = captureTranscriptView(
        el,
        measure,
        measureAnchors,
        currentOptions.readView?.positioningRevision,
      );
      completeTranscriptViewRestore(currentOptions.id, currentOptions.logicalRef);
      return;
    }

    const anchor = anchorFromCapture(pending.captured, candidates);
    if (!pending.anchorRestored) {
      const pendingTargetStillExists =
        pending.target === undefined || candidates.some((candidate) => candidate.id === pending.target?.id);
      if (!pendingTargetStillExists) {
        pending.target = undefined;
        pending.scrollRequested = false;
      }
      const restored = pending.target ?? (anchor ? restoreTopAnchor(anchor, candidates) : undefined);
      if (!restored) {
        const metrics = measure(el);
        const scrollable = Math.max(0, metrics.scrollHeight - metrics.clientHeight);
        const offset = Math.max(0, Math.min(1, pending.captured.normalizedOffset)) * scrollable;
        if (Math.abs(el.scrollTop - offset) > 1.5) {
          committed.scrollToOffset(offset);
          return;
        }
        if (!confirmScrollOffset()) return;
        pending.anchorRestored = true;
      } else {
        const current = measured.find((position) => position.id === restored.id);
        if (current) {
          if (!current.height || current.height <= 0) return;
          const sameSource = anchor && positionForAnchor(anchor, candidates)?.id === restored.id;
          let desired: number;
          if (!pending.viewportReflow) {
            desired = restored.offset;
          } else if (sameSource) {
            desired = readingPointOffset(pending.captured, current.height, el.clientHeight, el.clientWidth);
          } else {
            desired = Math.max(restored.offset, -Math.max(0, current.height - el.clientHeight));
          }
          const offset = Math.max(
            0,
            Math.min(el.scrollTop + current.offset - desired, el.scrollHeight - el.clientHeight),
          );
          if (Math.abs(el.scrollTop - offset) > 1.5) {
            committed.scrollToOffset(offset);
            return;
          }
          if (current.offset >= el.clientHeight || current.offset + current.height <= 0) return;
          if (!confirmScrollOffset()) return;
          pending.anchorRestored = true;
        } else if (!pending.scrollRequested) {
          pending.target = restored;
          pending.scrollRequested = true;
          currentOptions.listRef?.current?.scrollToIndex(restored.index, { align: "start" });
          return;
        } else {
          return;
        }
      }
    }

    if (pending.restoreFocus && currentOptions.canRestoreFocus?.() !== false) {
      const focusResult = focusCapturedEntry(el, pending.captured, candidates, currentOptions.listRef, pending);
      if (focusResult === "waiting") return;
      if (focusResult === "missing") currentOptions.focusFallback?.();
    }
    pendingRef.current = null;
    latestCaptureRef.current = captureTranscriptView(
      el,
      measure,
      measureAnchors,
      currentOptions.readView?.positioningRevision,
    );
    completeTranscriptViewRestore(currentOptions.id, currentOptions.logicalRef);
  }, []);

  const capture = useCallback((): CapturedTranscriptView => {
    const currentOptions = optionsRef.current;
    const el = currentOptions.listRef?.current?.getScrollElement();
    if (pendingRef.current)
      return el ? captureTranscriptFocus(el, pendingRef.current.captured) : pendingRef.current.captured;
    const before = latestCaptureRef.current;
    const committed = committedLayoutRef.current;
    const valid = !!el && !!committed?.isCurrent() && committed.virtualizer.scrollElement === el;
    if (before && (!valid || viewportChanged(before, el))) return el ? captureTranscriptFocus(el, before) : before;
    if (!el || !valid) return { anchorOffset: 0, normalizedOffset: 0, followingBottom: false };
    const next = captureTranscriptView(
      el,
      currentOptions.measure,
      currentOptions.measureAnchors,
      currentOptions.readView?.positioningRevision,
    );
    latestCaptureRef.current = next;
    return next;
  }, []);

  const restoreAfterLayout = useCallback(
    (committed: CommittedVirtualListLayout) => {
      const currentOptions = optionsRef.current;
      const el = currentOptions.listRef?.current?.getScrollElement();
      if (
        !currentOptions.enabled ||
        currentOptions.readView?.alive === false ||
        !el ||
        !committed.isCurrent() ||
        committed.virtualizer.scrollElement !== el
      )
        return;
      committedLayoutRef.current = committed;
      const before = pendingRef.current?.captured ?? latestCaptureRef.current;
      if (before && viewportChanged(before, el)) {
        if (pendingRef.current) pendingRef.current.viewportReflow = true;
        else
          pendingRef.current = {
            captured: before,
            readView: currentOptions.readView,
            positioningRevision: currentOptions.readView?.positioningRevision,
            restoreFocus: false,
            scrollRequested: false,
            anchorRestored: false,
            focusScrollRequested: false,
            viewportReflow: true,
          };
      }
      restoreAfterMeasurement();
      if (!pendingRef.current) capture();
    },
    [capture, restoreAfterMeasurement],
  );

  const restore = useCallback((captured: CapturedTranscriptView): void => {
    const readView = optionsRef.current.readView;
    if (!captureIsCurrent(captured, readView)) return;
    pendingRef.current = {
      captured,
      readView,
      positioningRevision: readView?.positioningRevision,
      restoreFocus: true,
      scrollRequested: false,
      anchorRestored: false,
      focusScrollRequested: false,
      viewportReflow: false,
    };
    const currentOptions = optionsRef.current;
    const el = currentOptions.listRef?.current?.getScrollElement();
    if (el) retainedPlacements.set(el, { logicalRef: currentOptions.logicalRef, captured });
  }, []);

  const announce = useCallback((summary: string) => {
    optionsRef.current.announce?.(summary);
  }, []);

  useLayoutEffect(() => {
    if (!enabled) return;
    const unregister = registerTranscriptView({
      id,
      logicalRef,
      layout,
      capture,
      restore: (captured) => {
        if (optionsRef.current.id === id && optionsRef.current.readView === readView) restore(captured);
      },
      announce,
    });
    return unregister;
  }, [announce, capture, enabled, id, logicalRef, layout, readView, restore]);

  // biome-ignore lint/correctness/useExhaustiveDependencies: id replaces the exact live port subscription
  useLayoutEffect(() => {
    if (!enabled || !readView) return;
    const el = optionsRef.current.listRef?.current?.getScrollElement();
    if (!el) return;
    let revision = readView.positioningRevision;
    return readView.subscribe((beforeOffset) => {
      const currentOptions = optionsRef.current;
      if (currentOptions.readView !== readView || currentOptions.listRef?.current?.getScrollElement() !== el) return;
      const committed = committedLayoutRef.current;
      if (!readView.alive || revision !== readView.positioningRevision) {
        revision = readView.positioningRevision;
        pendingRef.current = null;
        latestCaptureRef.current = undefined;
        if (committed?.virtualizer.scrollElement === el) committed.cancelPendingScroll();
      } else if (
        beforeOffset !== undefined &&
        Number.isFinite(beforeOffset) &&
        committed?.virtualizer.scrollElement === el
      ) {
        committed.syncReaderMovement(beforeOffset);
        // The reader can choose visible content before its new size is committed.
        latestCaptureRef.current = captureTranscriptView(
          el,
          currentOptions.measure,
          currentOptions.measureAnchors,
          readView.positioningRevision,
        );
      }
    });
  }, [capture, enabled, id, readView]);

  // biome-ignore lint/correctness/useExhaustiveDependencies: id is trigger-only, a reader identity change replaces its port listener
  useLayoutEffect(() => {
    if (!enabled) return;
    const el = optionsRef.current.listRef?.current?.getScrollElement();
    if (!el) return;
    const observeStablePosition = () => {
      const committed = committedLayoutRef.current;
      if (!committed?.isCurrent() || committed.virtualizer.scrollElement !== el) return;
      if (pendingRef.current) restoreAfterMeasurement();
      if (pendingRef.current) return;
      if (viewportChanged(latestCaptureRef.current, el)) return;
      capture();
    };
    el.addEventListener("scroll", observeStablePosition);
    return () => el.removeEventListener("scroll", observeStablePosition);
  }, [capture, enabled, id, restoreAfterMeasurement]);

  // Retry settlement through React's accepted projection, not synchronously
  // against the previous DOM in the store publication callback. Only pending
  // placement reads the current opening authority, keeping snapshot rendering
  // independent of store reads. Child layouts before this subscription and
  // settlement before subscription are covered by the current-state check in
  // restoreAfterMeasurement and the unconditional commit retry below.
  useLayoutEffect(() => {
    if (!enabled || logicalRef === undefined) return;
    return threadsStore.subscribe((state, previous) => {
      if (state.localOpenings.has(logicalRef) !== previous.localOpenings.has(logicalRef)) retryOpeningPlacement();
    });
  }, [enabled, logicalRef]);

  // A virtualizer notification can precede its sizer's DOM commit. Retry
  // after each commit as well as from the actual measurement callback.
  useLayoutEffect(() => {
    restoreAfterMeasurement();
  });

  return {
    restoreAfterMeasurement,
    restoreAfterLayout,
    isPlacementPending: () => pendingRef.current !== null,
    hasRetainedPlacement: () => {
      const captured = pendingRef.current?.captured ?? retainedTranscriptCapture(id, logicalRef);
      return captured !== undefined && !captured.followingBottom;
    },
  };
}

export interface UseTranscriptScrollResult {
  /** Items rendered since the reader last was at (or returned to) the bottom. */
  pillCount: number;
  /** True whenever the jump-to-latest pill should be on offer: the reader is
   * away from the bottom (even with nothing new - the pill is a scroll-
   * position affordance first, per docs/web-ui/decisions.md's "a
   * jump-to-latest pill when scrolled up"), OR unseen items/failures are
   * pending. Sourced from real DOM geometry in the scroll listener, so a
   * jump that lands short leaves it true instead of stranding the reader. */
  pillVisible: boolean;
  /** True while the pill is showing AND the thread is currently attention-worthy
   * (askPending, or a status the pane's own Cadence mapping treats as needs-you) -
   * recomputed live every render, so a later status flip upgrades the pill
   * in place even if it lands after the reader scrolled away or after the
   * content that produced it. */
  pillNeedsYou: boolean;
  /** True while a failed turn the reader hasn't seen yet is anchored (see
   * "the error anchor" below) - outranks pillNeedsYou per the pinned
   * contract (contracts-transcript-scroll-liveness.md §5: "error outranks
   * a simultaneous needs-you state"). Exposed independently of
   * pillNeedsYou rather than pre-resolved here: precedence is a rendering
   * decision for whoever consumes both (NewContentPill), not this hook's
   * job to collapse into one value. */
  pillError: boolean;
  /** Direction for the NewContentPill's chevron arrow. "up" when the error
   * anchor (if active) is above the current viewport, "down" otherwise
   * (normal case: new content below, or no error anchor). */
  pillArrowDirection: "up" | "down";
  /** Scrolls to the last turn and clears the pill's count/error state -
   * unless an error anchor is active, in which case it jumps to THAT turn's
   * index instead (see "the error anchor" below). The pill's VISIBILITY is
   * not cleared by the click itself: it stays on offer until the landing's
   * scroll event (or an at-bottom measurement at click time) confirms
   * arrival. Also the target for a manual click on NewContentPill. */
  jumpToBottom: () => void;
  /** Announce that the READER is moving the transcript right now, for scroll
   * actions this hook cannot observe from the scroll port: the transcript's
   * keyboard chords are dispatched from `window` and write `scrollTop`
   * directly (useTranscriptScrollKeys), so no port listener ever sees them.
   * Call it immediately BEFORE the scroll write. The bottom-hold correction
   * refuses to re-pin while a gesture is pending in the same frame - see the
   * scroll listener. Stable identity; safe to call from a long-lived handler. */
  markGesture: (readerIntent?: boolean, beforeOffset?: number) => void;
  /** Capture the top stable row immediately before changing view mode. */
  captureViewAnchor: () => void;
  /** Finish a pending restore after VirtualList reports new measurements. */
  restoreViewAnchorAfterMeasurement: (layout?: CommittedVirtualListLayout) => void;
}

export function useTranscriptScroll({
  ref,
  viewId,
  keepViewRemount,
  model,
  listRef,
  loadOlder,
  cancelOlder,
  measure = readScrollMetrics,
  viewKey = "everything",
  initialViewCapture,
  measureAnchors = readAnchorPositions,
  anchorEntries,
  renderedRowCount: renderedRowCountInput,
  sourceTurnRowIndexes,
  askDockPending = false,
  askDockActivationEpoch = 0,
  heldVisible = false,
  heldEpoch = 0,
  onReaderIntent,
  onReaderMovement,
}: UseTranscriptScrollOptions): UseTranscriptScrollResult {
  const keepViewRemountRef = useRef(keepViewRemount);
  keepViewRemountRef.current = keepViewRemount;
  useLayoutEffect(() => {
    if (viewId === undefined) return;
    return retainTranscriptView(viewId, ref, () => keepViewRemountRef.current?.() ?? true);
  }, [viewId, ref]);
  const [pillCount, setPillCount] = useState(0);
  // The first failed turn's index, while the reader hasn't seen it yet
  // (null = no active anchor). State (not just a ref) because clearing it
  // from inside the scroll listener (see handleScroll below) must trigger
  // a re-render so pillError updates live, exactly like pillCount already
  // does for the same reason.
  const [errorAnchorIndex, setErrorAnchorIndex] = useState<number | null>(null);
  // Arrow direction for the pill: "up" when the anchor is above the visible
  // range, "down" otherwise (normal case or no anchor). Updated whenever
  // scroll position changes (in handleScroll), so it's always in sync with
  // the current viewport state.
  const [pillArrowDirection, setPillArrowDirection] = useState<"up" | "down">("down");
  // "The reader is away from the bottom" as RENDER state (wasAtBottomRef is
  // the same fact as a ref, for the long-lived scroll closure). This is what
  // lets the pill be a scroll-position affordance - visible whenever the
  // reader has scrolled back, even with zero new items - rather than only a
  // new-content counter. Updated everywhere wasAtBottomRef is written:
  // handleScroll (the common path), the one-time mount init, and the per-ref
  // reset. jumpToBottom SETS it in the error-anchor branch (the landing is
  // deliberately not the bottom) and, in the bottom-seeking branch, clears
  // it ONLY from a measurement that already reads at-bottom (where no scroll
  // - and so no landing event - will happen); otherwise the scroll triggered
  // by the jump fires handleScroll on landing, which clears it only once the
  // reader has ACTUALLY arrived - so a jump that lands short leaves the pill
  // on offer instead of vanishing into a stranded mid-transcript position.
  const [awayFromBottom, setAwayFromBottom] = useState(false);

  const wasAtBottomRef = useRef(true);
  // Reader-gesture state for the bottom-hold correction (see the scroll
  // listener). Hook-level rather than effect-local so markGesture can be handed
  // out with a stable identity.
  //
  // A marker vetoes the event its own gesture caused, and nothing else. A
  // session switch cannot be that event: the first scroll a newly-opened session
  // gets is its own mount scroll-to-end, which no gesture aimed at the previous
  // transcript is responsible for, and which is the landing this correction
  // exists to get right. So the per-ref reset below clears them all - the
  // pieces of state and the pending clearing frame's handle, whose null is what
  // lets the new session's first gesture schedule a frame of its own - alongside
  // every other piece of per-session state.
  // Frames the pending marker has left before its clearing frame clears it; a
  // marker is pending while this is above zero.
  const gestureFramesLeftRef = useRef(0);
  const gestureClearFrameRef = useRef<number | null>(null);
  const pointerDraggingRef = useRef(false);
  const middleButtonHeldRef = useRef(false);
  const lastTouchYRef = useRef<number | null>(null);
  const readerGestureRef = useRef<{ beforeOffset: number; admitted: boolean } | undefined>(undefined);
  const pendingBottomJumpRef = useRef<{
    element: HTMLElement;
    scrollHeight: number;
    clientHeight: number;
  } | null>(null);
  const readerCallbacksRef = useRef({ onReaderIntent, onReaderMovement });
  readerCallbacksRef.current = { onReaderIntent, onReaderMovement };
  // The geometry the previous measurement saw. Read only to classify the NEXT
  // scroll event (see handleScroll). Deliberately NOT reset alongside the other
  // per-ref state below: the mount block reseeds it from a fresh measurement in
  // the same effect pass, before the scroll listener is attached, so no handler
  // can ever read the previous session's geometry.
  const lastScrollGeometryRef = useRef<ScrollMetrics>({ scrollTop: 0, scrollHeight: 0, clientHeight: 0 });
  // Announce that the reader is moving the transcript right now. Cleared on the
  // next animation frame, which is exactly "this frame": a frame's scroll steps
  // run BEFORE its requestAnimationFrame callbacks, so every scroll event the
  // gesture can be responsible for is delivered while the flag is up, and none
  // of the next frame's corrections see it.
  //
  // That frame may never arrive - requestAnimationFrame does not run while the
  // tab is hidden, and the first frame after it becomes visible again can be
  // arbitrarily far away - so the scroll listener also CONSUMES the flag on the
  // event it explains (one whose content and viewport heights held), and the
  // document going hidden clears it outright. Between the three, a gesture
  // vetoes the event it caused and, within its frames, the measurement
  // corrections that land before that event - nothing later.
  //
  // Over-marking is the dangerous direction, not under-marking. A veto falls
  // through to the ordinary path below, which records the reader as away from
  // the bottom, so the at-bottom clause fails for every later correction until
  // the reader actually returns to the bottom: one false veto reinstates the
  // mount strand permanently, rather than costing a single frame.
  //
  // A native scrolling key's marker lasts three frames. The browser's smooth
  // scroll for the key starts a frame or two after it, so its first scroll
  // event arrives after one or two frame clears (Chrome: usually 20ms after
  // Shift-Space's keydown, #3880, and sometimes 37.8ms, #4065). An instant key
  // scroll consumes the marker in the first frame.
  const markGesture = useCallback(
    (readerIntent = true, beforeOffset?: number, frames = 1) => {
      const el = listRef.current?.getScrollElement();
      if (readerIntent && el) {
        pendingViewAnchorRef.current = null;
        pendingBottomJumpRef.current = null;
        readerCallbacksRef.current.onReaderIntent?.();
        if (beforeOffset !== undefined && Number.isFinite(beforeOffset) && el.scrollTop !== beforeOffset)
          readerCallbacksRef.current.onReaderMovement?.(beforeOffset);
        readerGestureRef.current = { beforeOffset: el.scrollTop, admitted: true };
      }
      pendingInitialEndRef.current = null;
      gestureFramesLeftRef.current = Math.max(gestureFramesLeftRef.current, frames);
      if (gestureClearFrameRef.current !== null) return;
      const clearOnFrame = () => {
        gestureClearFrameRef.current = requestAnimationFrame(() => {
          gestureFramesLeftRef.current -= 1;
          if (gestureFramesLeftRef.current > 0) return clearOnFrame();
          gestureFramesLeftRef.current = 0;
          gestureClearFrameRef.current = null;
          readerGestureRef.current = undefined;
        });
      };
      clearOnFrame();
    },
    [listRef],
  );
  // Drops a pending marker and its clearing frame, for when no scroll it could
  // explain is still to come: the document went hidden, or the session changed.
  const forgetPendingMarker = useCallback(() => {
    gestureFramesLeftRef.current = 0;
    readerGestureRef.current = undefined;
    if (gestureClearFrameRef.current !== null) {
      cancelAnimationFrame(gestureClearFrameRef.current);
      gestureClearFrameRef.current = null;
    }
  }, []);
  // How exact each marker is, since over-marking is the harmful direction:
  //
  //   scroll chords - EXACT. useTranscriptScrollKeys writes the offset itself
  //     and marks only when the write moved it (its scrollPortBy).
  //   wheel         - a sideways wheel scrolls nothing here and never marks,
  //     whether it says so with a zero deltaY or with shiftKey; nor does a zoom
  //     gesture (ctrl-wheel) or one a descendant has already claimed via
  //     preventDefault. A vertical one marks only when
  //     verticalInputCanMovePort says it can reach and move the port: not into a
  //     limit the port is already at, and not when a nested scroller will answer
  //     it instead.
  //   touch         - the same predicate, on real vertical movement only, so a
  //     sideways swipe and a swipe a nested scroller answers are both ignored.
  //   native keys   - the wheel's predicate, by key direction. The one marker
  //     that lasts three frames, since the key's smooth scroll starts a frame or
  //     two late, so a key whose scroll never starts can veto a correction in
  //     each of them.
  //   pointer drag  - the LEAST exact, and deliberately kept: a selection drag
  //     that moves without scrolling marks a gesture, which no handler can tell
  //     from a scrollbar drag that does scroll. The PRIMARY button marks only
  //     while the pointer moves, so a held one is never a standing veto; a held
  //     MIDDLE button is an autoscroll in progress and vetoes for as long as it
  //     is down, stationary pointer included. Ruled out are a finger (it has the
  //     touch path, which is more exact), a secondary button, a drag that is
  //     over (no button held), and one that has left the port.
  const startPointerDrag = useCallback((event: PointerEvent) => {
    // A finger produces BOTH event streams. The touch path knows about
    // direction and nested scrollers; adopting the same finger here as a drag
    // would mark every sideways swipe and undo that exactness.
    //
    // The primary button drags a scrollbar or a selection; the middle button is
    // native autoscroll, which scrolls the port continuously for as long as it
    // is held - so leaving it out costs a re-pin on every correction that lands
    // while it is moving, not a single frame. The secondary button opens a menu
    // and never scrolls. Where the middle button does nothing at all, this marks
    // a drag that moves nothing: the same trade the path already makes for a
    // selection drag.
    //
    // The two buttons are tracked differently, because their held states mean
    // opposite things.
    //
    // A mark comes from pointermove and lasts one frame, which is the whole
    // story for the PRIMARY button: a selection drag holds it for as long as the
    // reader is choosing text and scrolls nothing while they pause, so a held
    // primary button must never be a standing veto - that is the permanent false
    // veto this design treats as the harmful direction.
    //
    // A held MIDDLE button is the opposite: native autoscroll scrolls the port
    // continuously while the pointer sits still, producing a stream of scroll
    // events with no pointermove to mark any of them. So it is tracked as a
    // state rather than an event, and the scroll listener reads it. Being a
    // state, it is bounded by its clears rather than by any clock: the button
    // coming up, a move without it, the pointer leaving the port, the pane
    // switching session, and focus or visibility going away.
    //
    // Two trades come with it. On a platform where a middle-button hold does
    // nothing at all it is a standing veto over a transcript that is not moving,
    // which needs the reader to press and hold the middle button over the
    // transcript while content grows. And because pointerleave is one of the
    // clears, an autoscroll that carries on with the cursor outside the port
    // goes unmarked from then on - under-marking, the safe side, and the same
    // trade pointerleave already makes for a drag.
    if (event.pointerType === "touch") return;
    if ((event.button !== 0 && event.button !== 1) || !event.isPrimary) return;
    if (event.button === 1) middleButtonHeldRef.current = true;
    pointerDraggingRef.current = true;
    const port = event.currentTarget;
    if (port instanceof HTMLElement) readerGestureRef.current = { beforeOffset: port.scrollTop, admitted: false };
  }, []);
  // A mouse pointer gets no implicit capture, so a drag released outside the
  // port never delivers pointerup to it, and a drag that started elsewhere can
  // wander in with its button still down. Two answers, neither alone complete:
  // the held button ends a drag wherever it was released, and leaving the port
  // ends it too, so a foreign drag entering later is not adopted. The trade is
  // that a drag which leaves the port and returns - an edge auto-scroll - stops
  // marking; under-marking is the safe direction here.
  const continuePointerDrag = useCallback(
    (event: PointerEvent) => {
      // Ahead of both reading AND writing the flag: a finger must neither mark
      // a mouse drag nor end one.
      if (event.pointerType === "touch") return;
      // The middle bit going away ends the autoscroll even when another button
      // is still down, and even when the release never reaches this port.
      if ((event.buttons & MIDDLE_BUTTON_BIT) === 0) middleButtonHeldRef.current = false;
      // A move carrying neither button a drag is begun for ends the drag rather
      // than continuing it. Testing the mask, not just "some button is down",
      // is what stops a secondary drag inheriting a primary one the port never
      // saw end: the flag survives a blur, and every later right-button move
      // would otherwise mark from a button the drag path never accepted.
      if ((event.buttons & DRAG_BUTTON_BITS) === 0) {
        pointerDraggingRef.current = false;
        return;
      }
      if (pointerDraggingRef.current) markGesture(false);
    },
    [markGesture],
  );
  // Deliberately NOT filtered by pointerType, unlike the two above: ending a
  // drag is the under-marking direction, so a stray touch pointerup clearing a
  // mouse drag costs an unmarked scroll. Ignoring it would keep the drag marked
  // only until the mouse's own pointerup, its next button-free move, or a
  // pointerleave - a short window, not a latch, but still the over-marking side
  // of the trade.
  const endPointerDrag = useCallback(() => {
    pointerDraggingRef.current = false;
    middleButtonHeldRef.current = false;
  }, []);
  // The autoscroll state is the only gesture state with no clock and nothing to
  // consume it, so everything that ends it has to be named. A middle press over
  // the transcript can be released anywhere - once focus leaves, the port sees
  // no pointerup at all - and until something says otherwise this would keep
  // vetoing, with every vetoed correction disarming the re-pin. Losing focus and
  // going hidden are both "the release, wherever it happens, is not ours to
  // see". pointerDraggingRef needs no equivalent: it only acts on a later
  // pointermove, which carries its own buttons.
  const endAutoscrollOnFocusLoss = useCallback(() => {
    middleButtonHeldRef.current = false;
  }, []);
  // A hidden document stops the clearing frame as well as hiding the scroll, so
  // it bounds the pending MARKER too, not just the autoscroll: a drag marks
  // without producing any scroll event of its own, and that marker would
  // otherwise survive until the first scroll after return - which, if content
  // grew meanwhile, is the measurement correction it would then veto. Blur needs
  // no equivalent: a blurred window is still rendering, so the frame boundary is
  // still the marker's bound there.
  const forgetGesturesWhenHidden = useCallback(() => {
    if (document.visibilityState !== "hidden") return;
    middleButtonHeldRef.current = false;
    forgetPendingMarker();
  }, [forgetPendingMarker]);
  const markWheel = useCallback(
    (event: WheelEvent) => {
      // None of these move this port. ctrl-wheel is the browser's zoom gesture,
      // and a macOS trackpad pinch arrives as one. shift-wheel is horizontal,
      // and the port is overflow-x:clip (virtuallist.module.css) so it pans in
      // no engine - engines disagree on whether they zero deltaY or leave it set
      // while scrolling sideways, and reading shiftKey needs no such guess. A
      // wheel a descendant already claimed will not reach the port either.
      if (event.ctrlKey || event.shiftKey || event.defaultPrevented) return;
      if (event.deltaY === 0) return;
      const port = event.currentTarget;
      if (!(port instanceof HTMLElement)) return;
      if (!verticalInputCanMovePort(port, measure(port), event.target, event.deltaY > 0 ? 1 : -1)) return;
      markGesture(true, lastScrollGeometryRef.current.scrollTop);
    },
    [markGesture, measure],
  );
  const startTouch = useCallback((event: TouchEvent) => {
    lastTouchYRef.current = event.touches[0]?.clientY ?? null;
  }, []);
  const continueTouch = useCallback(
    (event: TouchEvent) => {
      const y = event.touches[0]?.clientY ?? null;
      const last = lastTouchYRef.current;
      lastTouchYRef.current = y;
      // A first move with no recorded start (the touch began outside the port)
      // is not marked; the next one with real movement is.
      if (y === null || last === null || y === last) return;
      const port = event.currentTarget;
      if (!(port instanceof HTMLElement)) return;
      // A finger moving UP - clientY decreasing - pushes content down.
      if (!verticalInputCanMovePort(port, measure(port), event.target, y < last ? 1 : -1)) return;
      markGesture();
    },
    [markGesture, measure],
  );
  // A finished touch has to clear the recorded Y, or the "no recorded start"
  // guard above is true exactly once per mount and every later outside-start
  // move compares against a stale value.
  const endTouch = useCallback(() => {
    lastTouchYRef.current = null;
  }, []);
  const markNativeKey = useCallback(
    (event: KeyboardEvent) => {
      const space = event.key === " ";
      if (
        event.defaultPrevented ||
        event.altKey ||
        event.ctrlKey ||
        event.metaKey ||
        (event.shiftKey && !space) ||
        isIMECompositionKeydown(event) ||
        isEditableTarget(event.target) ||
        (space && event.target instanceof Element && event.target.closest("button, summary"))
      )
        return;
      let direction: -1 | 1 | undefined;
      if (space) {
        direction = event.shiftKey ? -1 : 1;
      } else if (event.key === "ArrowUp" || event.key === "PageUp" || event.key === "Home") {
        direction = -1;
      } else if (event.key === "ArrowDown" || event.key === "PageDown" || event.key === "End") {
        direction = 1;
      }
      const port = event.currentTarget;
      if (
        direction === undefined ||
        !(port instanceof HTMLElement) ||
        !verticalInputCanMovePort(port, measure(port), event.target, direction)
      )
        return;
      markGesture(true, undefined, 3);
      // markGesture's reader intent supersedes the pending capture - and, with
      // it, the movement record - so the key's own backward direction has to be
      // armed AFTER it. A forward key keeps waiting for its event, as before.
      if (direction === -1 && listRef.current?.getScrollElement() === port) listRef.current.noteBackwardMovement?.();
    },
    [listRef, markGesture, measure],
  );
  useEffect(
    () => () => {
      if (gestureClearFrameRef.current !== null) cancelAnimationFrame(gestureClearFrameRef.current);
    },
    [],
  );

  const firstTurnIdRef = useRef<string | undefined>(undefined);
  const baselineItemCountRef = useRef(0);
  const initializedRef = useRef(false);
  // The ref this hook's per-ref state was initialized for. The Session pane
  // is NOT keyed by ref (DockHost's PaneHost renders <Component params=...>
  // with no key), so clicking a different session in the sidebar updates
  // params.ref on the SAME mounted component instance - useTranscriptScroll's
  // own refs (initializedRef, wasAtBottomRef, baselineItemCountRef,
  // firstTurnIdRef, resolvedFailedTurnIdsRef, errorAnchorIndex) all persist
  // across that change. Without a reset, the new session opens wherever the
  // virtualizer defaults (initializedRef is already true, so the mount
  // effect's scroll-to-bottom is skipped) and stick-to-bottom / pill counts
  // are computed against the PREVIOUS session's scroll state. refForInitRef
  // detects the change and re-initializes every per-ref piece of state so
  // the new session is treated as a fresh open (always land at the bottom).
  const refForInitRef = useRef<string | undefined>(ref);
  const pendingInitialEndRef = useRef<string | null>(null);
  // True while the end position we asked for is still the reader's wish. A
  // reader's own scroll replaces it; an index target the caller asked for (a
  // failed-turn jump) is never replaced by this flag.
  const pendingEndTargetRef = useRef(false);
  const pendingViewAnchorRef = useRef<{
    anchor: ViewAnchor | undefined;
    proportion: number;
    target?: RestoredViewAnchor;
    scrollRequested?: boolean;
  } | null>(null);
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
  // older turns are prepended in front of it. The active target's row index is
  // resolved from its source turn id on every transformed-row update.
  const resolvedFailedTurnIdsRef = useRef<Set<string>>(new Set());
  // Latest-ref mirror of errorAnchorIndex (state) for the same reason
  // itemCountRef/renderedRowCountRef/modelRef exist: handleScroll is a
  // long-lived closure (attached once per mount/hasContent transition, not
  // every render - see that effect's own comment) and the content-changed
  // effect's dependency array deliberately excludes it, so both must read
  // the CURRENT value through a ref rather than close over a stale one.
  const errorAnchorIndexRef = useRef<number | null>(null);
  errorAnchorIndexRef.current = errorAnchorIndex;
  const errorAnchorTurnIdRef = useRef<string | undefined>(undefined);

  // "Latest" ref so the scroll listener - attached far less often than every
  // render - never invokes a stale loadOlder closure. Necessary specifically
  // because useTranscript.ts's own loadOlder callback changes identity on
  // every loadingOlder flip (its de-dupe guard reads loadingOlder from that
  // same closure); calling a stale one could read a stale de-dupe flag.
  const loadOlderRef = useRef(loadOlder);
  loadOlderRef.current = loadOlder;

  const itemCount = totalItemCount(model);
  const turnsLength = model?.turns.length ?? 0;
  const renderedRowCount = renderedRowCountInput ?? turnsLength;
  const firstTurnId = model?.turns[0]?.id;
  // Content-changed effect trigger (see that effect's own dependency-array
  // comment for why itemCount/firstTurnId alone can't reach a bare-stamp
  // turn failure).
  const failedTurns = failedTurnCount(model);
  // NOT turnsLength > 0 (kata cmjb): a real evener session's transcript
  // always carries at least the synthetic prelude turn (isDormantTranscript's
  // own comment), so turnsLength is already 1 - and this would already be
  // true - before the VirtualList this hook depends on has ever mounted;
  // Session.tsx renders EmptyTranscript, not the real transcript, for that
  // exact state. A dormant session's turns.length going 1 -> 2 (the prelude
  // gains its first real turn) would then leave hasContent unchanged, so the
  // mount effect below would silently never re-run at the one render where
  // VirtualList actually appears - initializedRef stuck false, no
  // scroll-to-bottom, no scroll listener, no stick-to-bottom, for the rest
  // of the pane's mounted life. isDormantTranscript plus heldVisible mirrors
  // Session.tsx's own render condition exactly, so this flips at the SAME
  // transition VirtualList actually mounts at. Ask-only dormant behavior is
  // intentionally unchanged.
  const hasContent = heldVisible || !isDormantTranscript(model?.turns ?? []);
  // Track hasContent transitions so a false->true flip (VirtualList remounts
  // after the model briefly went undefined - e.g. a store resync that clears
  // the thread, or the same ref re-hydrating) re-runs the one-time
  // scroll-to-bottom. initializedRef alone can't see this: it was set true on
  // the first hydration and never resets, so a remount on the SAME ref would
  // skip the scroll-to-bottom and strand the reader at the virtualizer's
  // default top position. (A ref CHANGE is handled separately by
  // refForInitRef below; this handles the same-ref remount.)
  const prevHasContentRef = useRef(hasContent);
  // Also kept in refs for the scroll listener/jumpToBottom, which are not
  // re-created on every render (see the effects below).
  const itemCountRef = useRef(itemCount);
  itemCountRef.current = itemCount;
  const renderedRowCountRef = useRef(renderedRowCount);
  renderedRowCountRef.current = renderedRowCount;
  const sourceTurnRowIndexesRef = useRef(sourceTurnRowIndexes);
  sourceTurnRowIndexesRef.current = sourceTurnRowIndexes;
  // Latest ref for model itself: the content-changed effect below needs the
  // full turns array (to size a detected prepend), but must NOT re-run on
  // every streaming delta just because `model` is a fresh object reference -
  // its dependency array is itemCount/firstTurnId (primitives) precisely so
  // a pure text-delta re-render (which changes neither) is free, matching
  // the wave's own "per-delta work never re-renders the settled transcript"
  // constraint in spirit even though this hook's own work is cheap either way.
  const modelRef = useRef(model);
  modelRef.current = model;
  // The store owns whether this ref's recorded history is still being restored
  // from disk (the entry-backed local opening). While it is, the pane's first
  // paint shows the evicted snapshot - rows the virtualizer measures at zero -
  // and paging must hold regardless of that geometry.
  const openingPending = useStore(threadsStore, (state) => state.localOpenings.has(ref));
  const openingPendingRef = useRef(openingPending);
  openingPendingRef.current = openingPending;
  // The previous-pending transition is scoped to the ref AND the port it was
  // observed on. Session persists across ref changes (see refForInitRef below),
  // so an unkeyed boolean would read the NEXT session's ordinary state as THIS
  // session's opening settling and submit the next session's older-page demand
  // before its own initial scroll placement. A ref or port change is
  // navigation, not settlement: reseed and take no load action.
  const openingTransitionRef = useRef<{ ref: string; port: HTMLElement | null; pending: boolean }>({
    ref,
    port: null,
    pending: openingPending,
  });

  const resumeOpening = useCallback(
    (layout?: CommittedVirtualListLayout) => {
      const list = listRef.current;
      const port = list?.getScrollElement();
      const previous = openingTransitionRef.current;
      if (!port || openingPendingRef.current || previous.ref !== ref || previous.port !== port || !previous.pending)
        return;
      // Real readers resume only after child registration and our own placement.
      // Older handles without committed geometry retain their immediate check.
      if (
        list?.isMeasurementReady &&
        (!layout?.isCurrent() ||
          layout.virtualizer.scrollElement !== port ||
          pendingInitialEndRef.current === ref ||
          pendingViewAnchorRef.current !== null)
      )
        return;
      openingTransitionRef.current = { ref, port, pending: false };
      port.removeAttribute("data-opening-hold");
      const metrics = measure(port);
      if (isNearTop(metrics.scrollTop) && !contentIsUnmeasured(port)) loadOlderRef.current().catch(() => {});
      else if (shouldAutoLoadOlder(port)) loadOlderRef.current().catch(() => {});
    },
    [ref, listRef, measure],
  );

  // While the recorded opening is pending, hold BOTH paging triggers: stamp the
  // scroll port so the short-page row's own geometry check holds (that row owns
  // its own observer), and gate the near-top trigger above. When the opening
  // settles - success, failure or no restored model - the store publishes the
  // change, this effect runs, the stamp lifts, and a one-shot re-check lets a
  // genuinely short or near-top page fill without waiting for a resize or
  // scroll event that may never arrive. Disposal clears the store hold the same
  // way, so a torn-down opening cannot strand the row watching a stale stamp.
  useLayoutEffect(() => {
    const port = listRef.current?.getScrollElement();
    if (!port) return;
    const previous = openingTransitionRef.current;
    if (previous.ref !== ref || previous.port !== port) {
      openingTransitionRef.current = { ref, port, pending: openingPending };
      if (openingPending) port.setAttribute("data-opening-hold", "");
      else port.removeAttribute("data-opening-hold");
      return;
    }
    if (openingPending) {
      port.setAttribute("data-opening-hold", "");
      openingTransitionRef.current = { ref, port, pending: true };
      return;
    }
    if (!previous.pending) {
      port.removeAttribute("data-opening-hold");
      return;
    }
    resumeOpening();
  });

  const rowIndexForTurn = useCallback((turnId: string, sourceIndex: number): number => {
    const mapped = sourceTurnRowIndexesRef.current?.get(turnId) ?? sourceIndex;
    return Math.max(0, Math.min(mapped, Math.max(0, renderedRowCountRef.current - 1)));
  }, []);

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
    errorAnchorTurnIdRef.current = undefined;
    // With the anchor gone the pill's next jump heads for the bottom, never
    // up - and because the pill now STAYS VISIBLE after an anchor click, a
    // stale "up" arrow would otherwise render on the plain "latest" pill
    // until the next scroll event recomputes it.
    setPillArrowDirection("down");
  }, []);

  const jumpToBottom = useCallback(() => {
    pendingViewAnchorRef.current = null;
    pendingBottomJumpRef.current = null;
    readerGestureRef.current = undefined;
    readerCallbacksRef.current.onReaderIntent?.();
    const anchor = errorAnchorIndexRef.current;
    if (anchor === null) cancelOlder?.();
    if (anchor !== null) {
      // Jumps INTO the transcript, not to the bottom - wasAtBottomRef must
      // stay false, or the very next mutation's stick-to-bottom check would
      // yank the reader away from the row they just navigated to (see
      // "the error anchor" - the whole point of an anchor is to land THERE).
      listRef.current?.scrollToIndex(anchor, { align: "start" });
      wasAtBottomRef.current = false;
      // Mirror into render state like every other wasAtBottomRef write site:
      // the landing is not the bottom, so the pill stays on offer.
      setAwayFromBottom(true);
    } else {
      const count = renderedRowCountRef.current;
      // Bottom state is NEVER set optimistically: wasAtBottomRef and
      // awayFromBottom only come from measured geometry. So measure BEFORE
      // requesting any scroll - scrollToIndex can synchronously move the DOM
      // to its estimate-derived end, which would make an after-the-fact
      // measurement describe the jump's own unconfirmed landing rather than
      // the reader's position at click time.
      const el = listRef.current?.getScrollElement();
      const m = el ? measure(el) : undefined;
      if (m && isAtBottom(m)) {
        // Already at the true bottom by measurement: no scroll will happen,
        // so no landing event will fire - confirm arrival from the
        // measurement itself.
        wasAtBottomRef.current = true;
        setAwayFromBottom(false);
      } else {
        // The pre-jump measurement is authoritative for the reader's CURRENT
        // position: they are away from the bottom. Write that into both
        // trackers before requesting the jump - the DOM can move without a
        // scroll event (content growth above the viewport, measurement
        // corrections), leaving the trackers stale at at-bottom, and a stale
        // at-bottom would let an append in the landing window auto-stick and
        // would hide the pill the moment clearPill() runs below.
        wasAtBottomRef.current = false;
        setAwayFromBottom(true);
        // scrollToIndex engages the virtualizer's own machinery (scrollState
        // -> measurement during the scroll, the reconcile loop, and the
        // anchorToEnd pinning that holds the end across later
        // estimate->measured corrections). But its target offset derives from
        // the measurement cache - ESTIMATES for every row between here and
        // the end that has never been rendered - so the landing it computes
        // is not guaranteed to be the true bottom, and whether a correction
        // arrives afterward is timing-dependent (the reconcile loop settles
        // after one stable frame; ResizeObserver delivery is async). That
        // shortfall was the unreliable jump: the pill had already been
        // cleared, and with no new content arriving there was no affordance
        // left to recover with.
        //
        // So after engaging the virtualizer, pin the scroll element to its
        // true DOM maximum directly - exact by construction, whatever the
        // estimates say. Later measurement corrections only ever change
        // scrollHeight, and the end-anchor (threshold 4px; the pin leaves the
        // distance at 0) keeps the viewport pinned to the new true end.
        //
        // Arrival is confirmed only by the landing's own scroll event
        // (handleScroll): until then the reader is still away, so an append
        // in that window increments the pill instead of auto-sticking on an
        // unconfirmed jump, and a landing that later corrections leave short
        // keeps the pill on offer.
        pendingEndTargetRef.current = false;
        if (count > 0) listRef.current?.scrollToIndex(count - 1, { align: "end" });
        if (el) {
          // Pin from LIVE geometry, re-measured after scrollToIndex: the
          // pre-jump measurement m is only for the at-bottom classification
          // above - engaging the virtualizer may already have corrected the
          // DOM maximum, and a pin computed from the stale value could land
          // short.
          const live = measure(el);
          pendingBottomJumpRef.current = {
            element: el,
            scrollHeight: live.scrollHeight,
            clientHeight: live.clientHeight,
          };
          el.scrollTop = Math.max(0, live.scrollHeight - live.clientHeight);
        }
      }
    }
    clearPill();
  }, [listRef, clearPill, measure, cancelOlder]);

  const captureViewAnchor = useCallback(() => {
    pendingInitialEndRef.current = null;
    const el = listRef.current?.getScrollElement();
    if (!el) return;
    const m = measure(el);
    const scrollable = Math.max(0, m.scrollHeight - m.clientHeight);
    const positions = measureAnchors(el);
    const firstVisible = topVisiblePosition(positions);
    pendingViewAnchorRef.current = {
      anchor: firstVisible ? captureTopAnchor(firstVisible) : undefined,
      proportion: scrollable > 0 ? m.scrollTop / scrollable : 0,
    };
  }, [listRef, measure, measureAnchors]);

  const restorePendingViewAnchor = useCallback(
    (layout?: CommittedVirtualListLayout) => {
      if (pendingInitialEndRef.current === ref) {
        const list = listRef.current;
        const el = list?.getScrollElement();
        if (!el || (list?.isMeasurementReady?.() === false && !layout?.isCurrent())) return;
        pendingInitialEndRef.current = null;
        // An end request made against the initial sizer can be clamped to zero.
        // Finish that fresh-open default once the real measured sizer commits.
        const retained = retainedPlacements.get(el);
        if (retained?.logicalRef !== ref && gestureFramesLeftRef.current <= 0) {
          pendingEndTargetRef.current = true;
          scrollToLastRow(listRef, renderedRowCountRef.current);
          const metrics = measure(el);
          el.scrollTop = Math.max(0, metrics.scrollHeight - metrics.clientHeight);
        }
      }
      const pending = pendingViewAnchorRef.current;
      if (!pending) return;
      const el = listRef.current?.getScrollElement();
      if (!el) return;

      const measured = measureAnchors(el);
      const positions = anchorEntries?.map((entry) => ({ ...entry, offset: 0 })) ?? measured;
      const restored = pending.target ?? (pending.anchor ? restoreTopAnchor(pending.anchor, positions) : undefined);
      if (!restored) {
        const m = measure(el);
        el.scrollTop = pending.proportion * Math.max(0, m.scrollHeight - m.clientHeight);
        pendingViewAnchorRef.current = null;
        return;
      }

      const current = measured.find((position) => position.id === restored.id);
      if (current) {
        el.scrollTop += current.offset - restored.offset;
        pendingViewAnchorRef.current = null;
        return;
      }

      // Keep the target and desired offset pending. VirtualList's onChange seam
      // calls this function again after scrollToIndex renders/measures the row.
      // Mark first because react-virtual can notify synchronously from the call.
      if (!pending.scrollRequested) {
        pending.target = restored;
        pending.scrollRequested = true;
        listRef.current?.scrollToIndex(restored.index, { align: "start" });
      }
    },
    [ref, listRef, measure, measureAnchors, anchorEntries],
  );

  const restoreViewAnchorAfterMeasurement = useCallback(
    (layout?: CommittedVirtualListLayout) => {
      restorePendingViewAnchor(layout);
      if (layout) resumeOpening(layout);
    },
    [restorePendingViewAnchor, resumeOpening],
  );

  // Mount / (re)attach. Guarded by initializedRef so the one-time restore-
  // or-default-to-bottom positioning and baseline recording happen exactly
  // once per "the scroll element became available" transition (initial
  // mount, or VirtualList mounting for the first time once a previously-
  // empty thread gets its first turn) - hasContent is what makes this rerun
  // for that later transition, since a plain RefObject mutation alone
  // triggers no rerun.
  // biome-ignore lint/correctness/useExhaustiveDependencies: both flags below are deliberate, re-verified against this rule - see the two comments inside
  useLayoutEffect(() => {
    // Ref change on a persistent Session instance (sidebar click to a
    // different session reuses the pane - see refForInitRef's own comment):
    // reset every per-ref piece of state so the new session is treated as a
    // fresh open. This runs before the initializedRef guard below so that
    // guard re-executes its scroll-to-bottom for the new ref.
    //
    // A same-ref remount (hasContent false->true: the model briefly went
    // undefined and VirtualList unmounted, then re-hydrated) is caught here
    // too: initializedRef was left true from the first hydration, so without
    // this reset the remount would skip the scroll-to-bottom and strand the
    // reader at the top.
    //
    // This block runs BEFORE the scroll-element null check below: a ref
    // change or remount can land on a render where VirtualList hasn't mounted
    // yet (model undefined), so `el` is null - the reset must still fire so
    // the LATER render where VirtualList appears treats it as a fresh open.
    const hasContentRemounted = !prevHasContentRef.current && hasContent;
    if (refForInitRef.current !== ref || hasContentRemounted) {
      refForInitRef.current = ref;
      initializedRef.current = false;
      wasAtBottomRef.current = true;
      setAwayFromBottom(false);
      firstTurnIdRef.current = undefined;
      baselineItemCountRef.current = 0;
      pendingInitialEndRef.current = null;
      pendingViewAnchorRef.current = null;
      pendingBottomJumpRef.current = null;
      resolvedFailedTurnIdsRef.current = new Set();
      setErrorAnchorIndex(null);
      errorAnchorIndexRef.current = null;
      errorAnchorTurnIdRef.current = undefined;
      setPillCount(0);
      // The next event any of these would meet is the mount scroll below, which
      // belongs to the new session and to no gesture.
      forgetPendingMarker();
      pointerDraggingRef.current = false;
      middleButtonHeldRef.current = false;
      lastTouchYRef.current = null;
    }
    prevHasContentRef.current = hasContent;

    const el = listRef.current?.getScrollElement();
    if (!el) return;

    if (!initializedRef.current) {
      // Opening a session always lands at the end (kata cmjb, Jesse's call):
      // the latest content is what a reader clicks in for. This deliberately
      // replaced the earlier per-ref restore of a stored scroll offset — the
      // whole persistence (threads.ts scrollPositions + the debounced writer
      // that lived below) was removed with it, not just bypassed.
      const retained = retainedPlacements.get(el);
      const captured = retained?.logicalRef === ref ? retained.captured : undefined;
      if (!captured && (!initialViewCapture || initialViewCapture.followingBottom)) {
        pendingEndTargetRef.current = true;
        scrollToLastRow(listRef, renderedRowCountRef.current);
        pendingInitialEndRef.current = listRef.current?.isMeasurementReady?.() === false ? ref : null;
      }
      const m = measure(el);
      lastScrollGeometryRef.current = m;
      // A retained placement lands after its rows measure, so this geometry
      // can still be estimates that fit the port; its capture says where the reader is.
      wasAtBottomRef.current = (captured ?? initialViewCapture)?.followingBottom ?? isAtBottom(m);
      setAwayFromBottom(!wasAtBottomRef.current);
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
      const jump = pendingBottomJumpRef.current;
      pendingBottomJumpRef.current = null;
      const jumpLanded =
        jump?.element === el && jump.scrollHeight === m.scrollHeight && jump.clientHeight === m.clientHeight;
      // Consumed here, not merely read: see markGesture on why the frame
      // boundary alone cannot be trusted to clear it. A held middle button is
      // read rather than consumed - native autoscroll scrolls the port for as
      // long as it is down, so every scroll event it produces is the reader's.
      //
      // Only an event the gesture could have caused consumes the marker: one
      // whose content and viewport heights held. A measurement correction
      // (heights changed) can land between a native key and its smooth scroll,
      // a frame or two later; it is still vetoed, but leaves the marker for the key's
      // own scroll, measured from the corrected offset.
      const gestured = gestureFramesLeftRef.current > 0 || middleButtonHeldRef.current;
      const previous = lastScrollGeometryRef.current;
      const heightsHeld = m.scrollHeight === previous.scrollHeight && m.clientHeight === previous.clientHeight;
      if (heightsHeld) {
        gestureFramesLeftRef.current = 0;
      } else if (readerGestureRef.current) {
        readerGestureRef.current = { ...readerGestureRef.current, beforeOffset: m.scrollTop };
      }
      // Content measured in BELOW a transcript that was already at the true
      // bottom, in the SAME scroll port, with the offset never moving
      // backwards: the virtualizer correcting its own estimates, not the reader
      // leaving.
      //
      // The offset clause alone does not carry that premise - the virtualizer
      // writes scrollTop too (applyScrollAdjustment), and writes it BACKWARDS
      // when a row above the viewport shrinks. What excludes a shrink is the
      // scrollHeight clause: there is strictly MORE content than the last
      // measurement saw. And a correction only ever changes how much content
      // there is, never the size of the box holding it, so a clientHeight that
      // moved is a resized (or first-ever-measured) viewport, which is not this
      // case and keeps the pill it would otherwise suppress.
      //
      // One narrow window where this misreads the reader: an upward flick begun
      // from the EXACT bottom during a mount-time measurement storm can have its
      // first frame swallowed, when a forward correction coalesced into the same
      // frame exceeds the reader's own delta and the net offset reads as
      // advanced. It self-releases on the very next event, where their continued
      // scroll moves the offset back with no further growth to hide it.
      //
      // The end-anchor is meant to hold the end across those corrections, but
      // its follow can under-count one that lands after the scroll-to-end
      // reconcile has already torn down (virtual-core's reconcileScroll settles
      // on the first frame whose target stops moving) - measured in the
      // transcript scroll guard as scrollHeight 17076 -> 17221 against an offset
      // that moved only 16374 -> 16432. Past the anchor's own 4px threshold it
      // disengages, and nothing moves the transcript again: a session the reader
      // has never touched is stranded short of the latest content, offering a
      // jump-to-latest pill at mount.
      //
      // So re-pin to the new true bottom - exact from the geometry in hand,
      // whatever the estimates say, the same pin jumpToBottom lands on - and
      // leave the trackers describing the bottom they already described. Arrival
      // is confirmed by the pin's own scroll event, like every other landing
      // here; a pin that a further correction leaves short is simply corrected
      // again by the next one.
      //
      // The early return defers the REST of this listener - the error anchor's
      // own clear, the pill's arrow direction, and the near-top loadOlder
      // trigger - to that same pin event. It always arrives: this branch is only
      // taken when the gap is already past the at-bottom threshold, so the
      // assignment genuinely moves scrollTop and the browser dispatches for it.
      const gesture = readerGestureRef.current;
      const beforeOffset = gesture?.beforeOffset ?? previous.scrollTop;
      if ((gestured || pointerDraggingRef.current) && m.scrollTop !== beforeOffset && heightsHeld) {
        if (!gesture?.admitted) {
          pendingViewAnchorRef.current = null;
          readerCallbacksRef.current.onReaderIntent?.();
        }
        readerCallbacksRef.current.onReaderMovement?.(beforeOffset);
        readerGestureRef.current = { beforeOffset: m.scrollTop, admitted: true };
      }
      lastScrollGeometryRef.current = m;
      // A reader's own movement replaces an END target the virtualizer is still
      // reconciling. A mount's or a restore's scrollToIndex lands a frame or two
      // later (react-virtual reconciles in an animation frame), and re-applying
      // it would throw away the position the reader just took. Only end targets
      // are replaced: an index target the caller asked for (a failed-turn jump)
      // must still land on its row.
      if (isAtBottom(m)) {
        pendingEndTargetRef.current = false;
      } else if (pendingEndTargetRef.current && m.scrollTop !== previous.scrollTop) {
        pendingEndTargetRef.current = false;
        listRef.current?.scrollToOffset?.(m.scrollTop);
      }
      if (!gestured && wasAtBottomRef.current && contentGrewBelowViewport(previous, m)) {
        el.scrollTop = Math.max(0, m.scrollHeight - m.clientHeight);
        return;
      }
      // A measurement write can reach the old sizer's end before it commits.
      // Passive admission needs committed geometry. An explicit jump instead
      // confirms its first landing against the live geometry it pinned.
      const layoutCurrent = listRef.current?.isLayoutCurrent() === true;
      // A backward measurement can also leave the old sizer's end before
      // commit. Keep established end-follow intent through that passive gap,
      // but let the reader leave immediately, including during reflow.
      const preserveEndFollow =
        wasAtBottomRef.current &&
        !gestured &&
        !pointerDraggingRef.current &&
        m.clientHeight === previous.clientHeight &&
        !layoutCurrent;
      wasAtBottomRef.current =
        preserveEndFollow || (isAtBottom(m) && (wasAtBottomRef.current || jumpLanded || layoutCurrent));
      setAwayFromBottom(!wasAtBottomRef.current);
      if (wasAtBottomRef.current) clearPill();
      // The error anchor also clears on its own once its failed turn
      // scrolls into the rendered range - narrower than clearPill above
      // (only the anchor, not the rest of the pill: other still-unseen
      // content below it is unrelated and must stay counted). A1's
      // getVisibleRange() is exactly this widget-level lever; null (nothing
      // measured/rendered) reads as "don't know, assume not visible".
      const anchor = errorAnchorIndexRef.current;
      const range = listRef.current?.getVisibleRange();
      if (anchor !== null) {
        if (range && anchor >= range.startIndex && anchor <= range.endIndex) {
          setErrorAnchorIndex(null);
          errorAnchorTurnIdRef.current = undefined;
        }
        // Update arrow direction: point up if anchor is above the visible
        // range, down otherwise. When no range is yet available (before
        // mount), assume down (the normal case).
        if (range && anchor < range.startIndex) {
          setPillArrowDirection("up");
        } else {
          setPillArrowDirection("down");
        }
      } else {
        // No error anchor - always point down (new content is below).
        setPillArrowDirection("down");
      }
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
      // The mount/measurement scroll of a transcript that is still painting its
      // rows sits at scrollTop 0 with nothing measured, which reads as a reader
      // at the top of a short transcript. That is the same transient empty
      // render the geometry fill guards against; paging it would issue a network
      // history request during an opening that must be local-only. The trigger
      // resumes once any rendered row has a measured height.
      //
      // The store's local-opening state holds this trigger through the whole
      // recorded restore, including rows react-virtual has already MEASURED at
      // zero (the evicted first paint): measurement readiness alone cannot tell
      // those rows from a settled transcript's legitimately empty turn.
      if (isNearTop(m.scrollTop) && !contentIsUnmeasured(el) && !openingPendingRef.current)
        loadOlderRef.current().catch(() => {});
    }

    // A geometry change that never produces a scroll event: a webfont swaps in
    // after the mount's landing (scrollHeight grows while the offset stays
    // pinned - measured 11466 -> 11487 at document.fonts.ready), the
    // virtualizer adopts newly-measured row heights without moving scrollTop,
    // or the port itself shrinks because something outside the transcript grew
    // (the pane header's cadence trace appearing - measured clientHeight
    // 671 -> 667 - or the composer gaining a line). Each leaves the reader
    // short of the true bottom with wasAtBottomRef still true: no pill, nothing
    // to click. Re-pin to the true bottom from live geometry.
    //
    // No growth-since-baseline test here, unlike the scroll listener's
    // contentGrewBelowViewport: a scroll event may be the reader's own
    // movement, so that path has to prove the change was not them, but these
    // triggers never are (and the gesture veto below covers a reader mid-way
    // through one). "Was following the bottom, and the true end is out of
    // view" is the whole condition, which also holds when a scroll event in
    // the same frame already took the shrunk geometry as its baseline (scroll
    // events dispatch before ResizeObserver delivery), and for a shortfall
    // inside isAtBottom's rounding tolerance, which nothing else would ever
    // correct. A reader scrolled away has wasAtBottomRef false and is never
    // moved.
    let reanchorRetryFrame: number | null = null;
    // Re-run once the frame boundary clears a pending gesture marker (the same
    // frame markGesture schedules its own clear on). Only armed for a growth
    // the gesture actually vetoed, so it is not a poll: for a drag or wheel the
    // marker is gone by the next frame (a native key's within three), and
    // for the one unbounded case (a stationary middle-button hold) it stops the
    // moment the hold does.
    function scheduleReanchorRetry() {
      if (reanchorRetryFrame !== null) return;
      reanchorRetryFrame = requestAnimationFrame(() => {
        reanchorRetryFrame = null;
        reanchorIfEndLeftView();
      });
    }
    function reanchorIfEndLeftView() {
      if (!el) return;
      const m = measure(el);
      // The same gesture veto the scroll listener applies, but READ rather than
      // consumed: this is not the event a pending gesture caused (content
      // growth fires none), so the marker must survive for the scroll event
      // that the gesture's own movement still delivers.
      const gestured = gestureFramesLeftRef.current > 0 || middleButtonHeldRef.current;
      const endLeftView = isEndBelowFold(m);
      // A vetoed correction MUST be retried: the marker can outlive this
      // trigger with no further resize or font event (a stationary
      // middle-button hold, a selection drag), and a dropped correction would
      // leave the reader permanently short of the bottom with wasAtBottomRef
      // still true and no pill to recover with.
      if (wasAtBottomRef.current && endLeftView && gestured) {
        scheduleReanchorRetry();
        return;
      }
      lastScrollGeometryRef.current = m;
      if (wasAtBottomRef.current && endLeftView) {
        el.scrollTop = Math.max(0, m.scrollHeight - m.clientHeight);
      }
    }

    let disposed = false;
    // The fonts trigger covers the swap landing before the virtualizer has
    // re-measured the rows; the observer covers the row measurement itself
    // (the content) and the port's own resize (the scroll element).
    const fonts = document.fonts;
    if (fonts) {
      void fonts.ready
        .then(() => {
          if (!disposed) reanchorIfEndLeftView();
        })
        .catch(() => {});
    }
    let geometryObserver: ResizeObserver | undefined;
    if (typeof ResizeObserver !== "undefined") {
      geometryObserver = new ResizeObserver(() => {
        if (!disposed) reanchorIfEndLeftView();
      });
      for (const target of portGeometryTargets(el)) geometryObserver.observe(target);
    }

    function cancelInitialEndOnReaderScroll() {
      if (!el || pendingInitialEndRef.current !== ref) return;
      const current = measure(el);
      const previous = lastScrollGeometryRef.current;
      if (
        current.scrollTop < previous.scrollTop ||
        current.clientHeight !== previous.clientHeight ||
        current.scrollHeight <= previous.scrollHeight
      ) {
        pendingInitialEndRef.current = null;
      }
    }
    // Native scrollbar movement has no wheel event. Classify it before the
    // virtualizer's bubble listener can finish a still-pending mount default.
    el.addEventListener("scroll", cancelInitialEndOnReaderScroll, true);
    // Classify real movement before the body's capture-only scroll listener.
    el.addEventListener("scroll", handleScroll, { capture: true });
    el.addEventListener("keydown", markNativeKey);
    el.addEventListener("wheel", markWheel, { passive: true });
    el.addEventListener("touchstart", startTouch, { passive: true });
    el.addEventListener("touchmove", continueTouch, { passive: true });
    el.addEventListener("touchend", endTouch, { passive: true });
    el.addEventListener("touchcancel", endTouch, { passive: true });
    el.addEventListener("pointerdown", startPointerDrag, { passive: true });
    el.addEventListener("pointermove", continuePointerDrag, { passive: true });
    el.addEventListener("pointerup", endPointerDrag, { passive: true });
    el.addEventListener("pointercancel", endPointerDrag, { passive: true });
    el.addEventListener("pointerleave", endPointerDrag, { passive: true });
    window.addEventListener("blur", endAutoscrollOnFocusLoss, { passive: true });
    document.addEventListener("visibilitychange", forgetGesturesWhenHidden, { passive: true });
    return () => {
      disposed = true;
      if (reanchorRetryFrame !== null) cancelAnimationFrame(reanchorRetryFrame);
      geometryObserver?.disconnect();
      el.removeEventListener("scroll", cancelInitialEndOnReaderScroll, true);
      el.removeEventListener("scroll", handleScroll, { capture: true });
      el.removeEventListener("keydown", markNativeKey);
      el.removeEventListener("wheel", markWheel);
      el.removeEventListener("touchstart", startTouch);
      el.removeEventListener("touchmove", continueTouch);
      el.removeEventListener("touchend", endTouch);
      el.removeEventListener("touchcancel", endTouch);
      el.removeEventListener("pointerdown", startPointerDrag);
      el.removeEventListener("pointermove", continuePointerDrag);
      el.removeEventListener("pointerup", endPointerDrag);
      el.removeEventListener("pointercancel", endPointerDrag);
      el.removeEventListener("pointerleave", endPointerDrag);
      window.removeEventListener("blur", endAutoscrollOnFocusLoss);
      document.removeEventListener("visibilitychange", forgetGesturesWhenHidden);
    };
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
    // re-attachment when content changes: clearPill/loadOlderRef.current
    // read fresh state at call time regardless.
    //
    // hasContent is listed despite not being read in this effect's body at
    // all - it exists purely to force a re-run at the "VirtualList mounts
    // for the first time" transition (a ref becoming non-null triggers no
    // re-render/effect on its own; hasContent flipping does).
    //
    // The gesture handlers ARE listed, rather than left to the ignore above:
    // every one is useCallback-stable, so naming them costs no extra effect
    // runs, and it keeps this rule's silence scoped to the two flags it was
    // written for.
  }, [
    ref,
    listRef,
    measure,
    clearPill,
    hasContent,
    markNativeKey,
    markWheel,
    startTouch,
    continueTouch,
    endTouch,
    startPointerDrag,
    continuePointerDrag,
    endPointerDrag,
    endAutoscrollOnFocusLoss,
    forgetGesturesWhenHidden,
    forgetPendingMarker,
  ]);

  // A mode change commits a different row set into the same VirtualList. This
  // layout effect runs after that commit and after the list's own layout work,
  // so the stable row can first be brought into the measured window, then have
  // its exact viewport offset corrected synchronously before paint.
  // biome-ignore lint/correctness/useExhaustiveDependencies: viewKey is deliberately trigger-only
  useLayoutEffect(() => {
    restoreViewAnchorAfterMeasurement();
  }, [viewKey, restoreViewAnchorAfterMeasurement]);

  // The ask dock's activation edge (see the options' own doc comments): new
  // answerable content appeared below without any transcript shape change.
  // Keyed on the activation EPOCH, not the pending boolean, so an atomic
  // pending-set replacement (a resync swapping an answered-elsewhere batch
  // for a new one) re-fires it while the boolean never left true. Declared
  // AFTER the mount effect above so a ref change (which resets
  // wasAtBottomRef to true for the fresh open) is already reflected when
  // this edge evaluates, and a session OPENED with an already-pending ask
  // never fires it - initial mount scrolls to the end, so the dock starts
  // visible and there is nothing unseen to count.
  const prevAskDockEpochRef = useRef(askDockActivationEpoch);
  useLayoutEffect(() => {
    const previous = prevAskDockEpochRef.current;
    prevAskDockEpochRef.current = askDockActivationEpoch;
    if (askDockActivationEpoch === previous || askDockActivationEpoch === 0) return;
    if (!initializedRef.current || wasAtBottomRef.current) return;
    setPillCount((count) => count + 1);
  }, [askDockActivationEpoch]);

  // The held-steer stack's arrival edge (see the option's doc comment):
  // keyed on the epoch, never on stack presence, and a pane opened with a
  // hold already in flight never fires it (initial mount scrolls to the
  // end; the first observation baselines without a bump).
  const prevHeldEpochRef = useRef(heldEpoch);
  useLayoutEffect(() => {
    const previous = prevHeldEpochRef.current;
    prevHeldEpochRef.current = heldEpoch;
    if (heldEpoch === previous || heldEpoch === 0) return;
    if (!initializedRef.current || wasAtBottomRef.current) return;
    setPillCount((count) => count + 1);
  }, [heldEpoch]);

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
        errorAnchorTurnIdRef.current = undefined;
      } else {
        const prependedCount = currentModel.turns.slice(0, prevIndex).reduce((sum, t) => sum + t.items.length, 0);
        // Prepended history is backfill, not "new" - advance the baseline by
        // exactly what loadOlder added so the pill count stays unaffected.
        baselineItemCountRef.current += prependedCount;
        // The active error target is keyed by source turn id. Re-resolve its
        // transformed row below instead of adding the source prepend count;
        // several source turns may occupy one rendered row.
        const activeTurnId = errorAnchorTurnIdRef.current;
        if (activeTurnId !== undefined) {
          const activeSourceIndex = currentModel.turns.findIndex((turn) => turn.id === activeTurnId);
          if (activeSourceIndex >= 0) setErrorAnchorIndex(rowIndexForTurn(activeTurnId, activeSourceIndex));
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
      }
      // The visible row is anchored by the end-anchored VirtualList
      // (anchorToEnd) with real per-item geometry, and this hook still never
      // writes scrollTop itself. But the mount's (or a jump's)
      // scrollToIndex(count-1, {align:"end"}) leaves the virtualizer's
      // reconcile loop holding THAT row index, and a leading-edge change
      // shifts every row's index - so the stale index now names a row a page
      // above the true end, and the reconcile drags the viewport up to it,
      // leaving a reader who opened at the bottom a page short of the latest
      // with the jump-to-latest pill showing. When the reader is following
      // the bottom, re-issuing the end target with the NEW last row keeps that
      // loop pointed at the bottom; a reader scrolled away is left alone.
      if (wasAtBottomRef.current) {
        pendingEndTargetRef.current = true;
        scrollToLastRow(listRef, renderedRowCountRef.current);
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
            errorAnchorTurnIdRef.current = firstUnresolved.id;
            setErrorAnchorIndex(rowIndexForTurn(firstUnresolved.id, currentModel.turns.indexOf(firstUnresolved)));
          }
        }
      }

      const unseen = itemCount - baselineItemCountRef.current;
      if (unseen > 0) {
        if (wasAtBottomRef.current) {
          pendingEndTargetRef.current = true;
          scrollToLastRow(listRef, renderedRowCountRef.current);
          wasAtBottomRef.current = true;
          baselineItemCountRef.current = itemCount;
        } else {
          setPillCount(unseen);
        }
      }
    }

    firstTurnIdRef.current = firstTurnId;
    // model is read via modelRef.current (see above), not closed over here,
    // specifically so this effect does NOT re-run on every streaming delta.
    // failedTurns is the one exception to "primitives derived from model
    // don't need model itself in this list" being sufficient: a turn's
    // failure can flip with NEITHER itemCount NOR firstTurnId changing (the
    // bare-stamp settle above), so without it this whole failed-turn branch
    // would silently never run for that real wire shape.
  }, [itemCount, firstTurnId, failedTurns, listRef, measure, renderedRowCount, sourceTurnRowIndexes, rowIndexForTurn]);

  // A transformed row set can change without changing the source turn/item
  // shape (for example, an Intent run coalescing three turns into one row).
  // Keep an active failure target coupled to its source turn id, not its old
  // source-turn index.
  // biome-ignore lint/correctness/useExhaustiveDependencies: row count/map are deliberate trigger dependencies for ref-backed target remapping
  useLayoutEffect(() => {
    const turnId = errorAnchorTurnIdRef.current;
    if (turnId === undefined || modelRef.current === undefined) return;
    const sourceIndex = modelRef.current.turns.findIndex((turn) => turn.id === turnId);
    if (sourceIndex < 0) return;
    const rowIndex = rowIndexForTurn(turnId, sourceIndex);
    if (errorAnchorIndexRef.current !== rowIndex) setErrorAnchorIndex(rowIndex);
  }, [renderedRowCount, sourceTurnRowIndexes, rowIndexForTurn]);

  // pillVisible is the union of every reason to offer the jump: scrolled
  // back (the always-on case), unseen items pending, or an unseen failure
  // anchored. The latter two imply the former in practice (both are only
  // ever set while wasAtBottomRef is false, which handleScroll had already
  // mirrored into awayFromBottom) - listing them just keeps the value right
  // even in the gap between a content change and the next scroll event.
  const pillVisible = awayFromBottom || pillCount > 0 || errorAnchorIndex !== null;

  return {
    pillCount,
    markGesture,
    pillVisible,
    // needs-you is gated on VISIBILITY, not on a nonzero count: an awaiting
    // flip that lands after the reader scrolled away (no new items at all)
    // still upgrades the on-offer pill in place. The dock's own pending
    // signal is part of the predicate, not just the model: model.askPending
    // is snapshot-authoritative (only hydrateThread sets it - no
    // notification carries it), so a live-arriving ask would otherwise read
    // as generic "new" content until the next snapshot.
    pillNeedsYou: pillVisible && (isAttentionWorthy(model) || askDockPending),
    pillError: errorAnchorIndex !== null,
    pillArrowDirection,
    jumpToBottom,
    captureViewAnchor,
    restoreViewAnchorAfterMeasurement,
  };
}
