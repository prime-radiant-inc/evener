import { type RefObject, useCallback, useLayoutEffect, useReducer, useRef } from "react";
import { isEntryTranscriptLocalOpening } from "../../../../stores/entryTranscriptRoutes";
import { threadsStore } from "../../../../stores/threads";
import type { CommittedVirtualListLayout, VirtualListHandle } from "../../../../widgets";
import type { TranscriptReadView } from "../transcriptReadView";
import { isAtBottom, readScrollMetrics, type ScrollMetrics } from "./scrollMetrics";
import {
  type CapturedTranscriptView,
  completeTranscriptViewRestore,
  registerTranscriptView,
  retainedTranscriptCapture,
} from "./transcriptViewRegistry";
import {
  anchorFromCapture,
  captureElementView,
  captureTranscriptFocus,
  focusCapturedEntry,
  type PendingTranscriptViewRestore,
  positionForAnchor,
  readAnchorPositions,
  readingPointOffset,
  restoreTopAnchor,
  retainedPlacements,
  type ViewAnchorPosition,
} from "./viewAnchor";

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
      latestCaptureRef.current = captureElementView(
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
    latestCaptureRef.current = captureElementView(
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
    const next = captureElementView(
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
        latestCaptureRef.current = captureElementView(
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
