// Saved-highlight overlay for the browser reader (Task 5).
//
// Bands are non-text decorations: each is a positioned, empty control with no
// text of its own, so rendering or revealing highlights never changes authored
// prose, text-node parentage or the browser Selection string. Every saved note
// is rendered as literal, inert text under `data-annotation-note`, revealed on
// hover, keyboard focus or a click/tap for EVERY annotation overlapping the
// touched passage. Bands are real buttons, so they are natively keyboard
// reachable in the document's sequential navigation order. Geometry is
// recomputed from the real scroll owner after scroll, resize,
// font-set/visual-viewport changes and map replacement; React owns the rendered
// DOM and every listener is removed on unmount.
import { type AnnotationView, formatAnnotationReference, type SourceRange } from "@evener/appwire-client";
import { Fragment, useEffect, useRef, useState } from "react";
import { requireClass } from "../../widgets/internal/requireClass";
import type { BoundDocumentMap } from "../../widgets/markdown/documentSourceMap";
import styles from "./documentAnnotations.module.css";
import { type HighlightFragment, measureDocumentHighlights } from "./documentHighlights";

export interface DocumentHighlightsProps {
  map: BoundDocumentMap;
  annotations: readonly AnnotationView[];
  scrollElement: HTMLElement;
}

const CLASS = {
  overlay: requireClass(styles.highlightOverlay, "documentAnnotations.module.css", "highlightOverlay"),
  band: requireClass(styles.highlightBand, "documentAnnotations.module.css", "highlightBand"),
  notes: requireClass(styles.highlightNotes, "documentAnnotations.module.css", "highlightNotes"),
  note: requireClass(styles.annotationNote, "documentAnnotations.module.css", "annotationNote"),
  reference: requireClass(styles.annotationReference, "documentAnnotations.module.css", "annotationReference"),
};

function overlaps(a: SourceRange, b: SourceRange): boolean {
  return Math.max(a.start, b.start) < Math.min(a.end, b.end);
}

export function DocumentHighlights({ map, annotations, scrollElement }: DocumentHighlightsProps) {
  const [fragments, setFragments] = useState<readonly HighlightFragment[]>([]);
  const [activeOperationId, setActiveOperationId] = useState<string | null>(null);
  const overlayRef = useRef<HTMLDivElement | null>(null);
  const notesRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    const measure = (): void => {
      const clip = scrollElement.getBoundingClientRect();
      const targets = annotations.map((annotation) => ({
        operationId: annotation.operation.operationId,
        sourceRange: annotation.operation.capture.sourceRange,
      }));
      setFragments(measureDocumentHighlights(map, targets, clip));
    };
    // Saved bands are pointer-events:none decorations, so a real pointer targets
    // the prose beneath them. Reveal is resolved BOTH from the event target
    // (keyboard focus and a direct band click, which still target the band) AND
    // from the pointer's own coordinates against the measured band rectangles
    // (ordinary hover and a coordinate click on the prose).
    const bandAtPoint = (x: number, y: number): string | null => {
      const hit = [...scrollElement.querySelectorAll<HTMLElement>("[data-document-highlight]")].find((band) => {
        const rect = band.getBoundingClientRect();
        return x >= rect.left && x <= rect.right && y >= rect.top && y <= rect.bottom;
      });
      return hit?.getAttribute("data-operation-id") ?? null;
    };
    // Active-passage selection is delegated from the scroll owner so one capture
    // listener covers hover, focus and click: `focus` does not bubble, so only a
    // capture-phase listener sees it.
    const activate = (event: Event): void => {
      const target = event.target;
      let operationId =
        target instanceof Element
          ? (target.closest("[data-document-highlight]")?.getAttribute("data-operation-id") ?? null)
          : null;
      if (!operationId && event instanceof MouseEvent) {
        const hover = (event.type === "mouseover" || event.type === "mousemove") && event.buttons === 0;
        if (hover || event.type === "click") operationId = bandAtPoint(event.clientX, event.clientY);
      }
      if (operationId) setActiveOperationId(operationId);
    };
    // A genuine tap is a stationary primary press over a band's rectangle,
    // completed by the SAME uncancelled pointer's release — never a Shift
    // release and never a drag. Cancellation resets without revealing, and an
    // unrelated pointer's release is ignored because the arming pointer id must
    // match.
    let tapPointerId: number | null = null;
    let tapStart: { x: number; y: number } | null = null;
    let tapOperationId: string | null = null;
    let tapMoved = false;
    const onPointerDown = (event: PointerEvent): void => {
      if (event.button !== 0 || event.isPrimary === false || event.shiftKey) return;
      tapPointerId = event.pointerId;
      tapStart = { x: event.clientX, y: event.clientY };
      tapMoved = false;
      tapOperationId = bandAtPoint(event.clientX, event.clientY);
    };
    const onPointerMove = (event: PointerEvent): void => {
      if (event.pointerId !== tapPointerId || !tapStart) return;
      if (Math.abs(event.clientX - tapStart.x) > 3 || Math.abs(event.clientY - tapStart.y) > 3) tapMoved = true;
    };
    const onPointerUp = (event: PointerEvent): void => {
      if (event.pointerId !== tapPointerId) return;
      if (tapOperationId && !tapMoved && !event.shiftKey) setActiveOperationId(tapOperationId);
      tapPointerId = null;
      tapStart = null;
      tapOperationId = null;
      tapMoved = false;
    };
    const onPointerCancel = (event: PointerEvent): void => {
      if (event.pointerId !== tapPointerId) return;
      tapPointerId = null;
      tapStart = null;
      tapOperationId = null;
      tapMoved = false;
    };
    // The revealed note surface is pointer-events:none so it never intercepts a
    // selection press or release; that also makes its scrollbar non-hit-testable,
    // so a genuine wheel over the stack must still scroll it. Forward the wheel by
    // coordinates while the pointer is inside the stack and the stack can move.
    const onWheel = (event: WheelEvent): void => {
      const notes = notesRef.current;
      if (notes === null || !notes.isConnected || !scrollElement.contains(notes)) return;
      // A nested note control (the pending reference re-enables pointer events
      // for itself) must keep a wheel it can consume natively: only forward a
      // wheel that targets the non-hit-testable stack region, never one that
      // targets an interactive descendant of the stack.
      if (event.target instanceof Node && notes.contains(event.target)) return;
      const rect = notes.getBoundingClientRect();
      if (
        event.clientX < rect.left ||
        event.clientX > rect.right ||
        event.clientY < rect.top ||
        event.clientY > rect.bottom
      )
        return;
      const before = notes.scrollTop;
      notes.scrollTop = before + event.deltaY;
      if (notes.scrollTop !== before) event.preventDefault();
    };

    const ownerDocument = scrollElement.ownerDocument;
    const view = ownerDocument.defaultView;
    const fonts = (ownerDocument as unknown as { fonts?: FontFaceSet }).fonts;
    const visualViewport = view?.visualViewport ?? null;
    const onScroll = (): void => measure();
    const onResize = (): void => measure();
    // A pane-only width change reflows the prose without any browser viewport
    // resize, so the window/visualViewport listeners never fire and the saved
    // bands would keep stale geometry. Observe the scroll owner's own box so a
    // layout change to the pane re-measures the same verified ranges.
    const sizeObserver = view?.ResizeObserver ? new view.ResizeObserver(() => measure()) : null;

    measure();
    sizeObserver?.observe(scrollElement);
    scrollElement.addEventListener("scroll", onScroll);
    view?.addEventListener("resize", onResize);
    fonts?.addEventListener("loadingdone", onScroll);
    visualViewport?.addEventListener("resize", onScroll);
    visualViewport?.addEventListener("scroll", onScroll);
    scrollElement.addEventListener("mouseover", activate, true);
    scrollElement.addEventListener("mousemove", activate, true);
    scrollElement.addEventListener("click", activate, true);
    scrollElement.addEventListener("focus", activate, true);
    scrollElement.addEventListener("pointerdown", onPointerDown, true);
    scrollElement.addEventListener("wheel", onWheel, { passive: false });
    ownerDocument.addEventListener("pointermove", onPointerMove);
    ownerDocument.addEventListener("pointerup", onPointerUp);
    ownerDocument.addEventListener("pointercancel", onPointerCancel);

    return () => {
      sizeObserver?.disconnect();
      scrollElement.removeEventListener("scroll", onScroll);
      view?.removeEventListener("resize", onResize);
      fonts?.removeEventListener("loadingdone", onScroll);
      visualViewport?.removeEventListener("resize", onScroll);
      visualViewport?.removeEventListener("scroll", onScroll);
      scrollElement.removeEventListener("mouseover", activate, true);
      scrollElement.removeEventListener("mousemove", activate, true);
      scrollElement.removeEventListener("click", activate, true);
      scrollElement.removeEventListener("focus", activate, true);
      scrollElement.removeEventListener("pointerdown", onPointerDown, true);
      scrollElement.removeEventListener("wheel", onWheel);
      ownerDocument.removeEventListener("pointermove", onPointerMove);
      ownerDocument.removeEventListener("pointerup", onPointerUp);
      ownerDocument.removeEventListener("pointercancel", onPointerCancel);
    };
  }, [map, annotations, scrollElement]);

  // A band is `position: absolute`, so its `left`/`top` are measured from THIS
  // overlay's padding box, not the scroll owner's border box. The overlay travels
  // with the scrolled content, so reading its own rectangle each render gives the
  // real containing-block relationship (recomputed after every scroll) instead of
  // adding the scroll owner's offsets a second time. The scroll owner still owns
  // the clipping and the events.
  const overlayRect = overlayRef.current?.getBoundingClientRect();
  // The bands keep their independent containing-block/clipping geometry above.
  // Only the REVEALED NOTE SURFACE is placed at the scroll owner's visible
  // top-left, so a note revealed for an early passage stays where the reader is
  // looking instead of being pinned to the bottom of the whole document. It is
  // bounded to the visible clip on BOTH axes: width via maxWidth, and height via
  // maxHeight, so the stack owns its own vertical scrolling rather than growing
  // past the actual scroll body (`.highlightNotes` carries `overflow-y: auto`).
  // Expressing it in the same overlay coordinate space keeps it working after
  // scroll (the owner re-measures) without a second positioning system.
  const visibleClip = overlayRect ? scrollElement.getBoundingClientRect() : null;
  const notesStyle =
    overlayRect && visibleClip
      ? {
          left: visibleClip.left - overlayRect.left + 12,
          top: visibleClip.top - overlayRect.top + 12,
          right: "auto",
          bottom: "auto",
          maxWidth: Math.max(visibleClip.width - 24, 0),
          maxHeight: Math.max(visibleClip.height - 24, 0),
        }
      : undefined;
  const active = activeOperationId
    ? (annotations.find((annotation) => annotation.operation.operationId === activeOperationId) ?? null)
    : null;
  const revealed = active
    ? annotations.filter((annotation) =>
        overlaps(annotation.operation.capture.sourceRange, active.operation.capture.sourceRange),
      )
    : [];

  return (
    <div className={CLASS.overlay} ref={overlayRef}>
      {overlayRect &&
        fragments.map((fragment) => (
          <button
            key={`${fragment.operationId}:${fragment.rect.left}:${fragment.rect.top}:${fragment.rect.right}:${fragment.rect.bottom}`}
            type="button"
            // The band is a decoration for opening its saved note, not a control
            // whose label is the note text.
            aria-label="Saved annotation"
            data-document-highlight=""
            data-operation-id={fragment.operationId}
            className={CLASS.band}
            style={{
              left: fragment.rect.left - overlayRect.left,
              top: fragment.rect.top - overlayRect.top,
              width: fragment.rect.right - fragment.rect.left,
              height: fragment.rect.bottom - fragment.rect.top,
            }}
          />
        ))}
      {revealed.length > 0 && (
        <div className={CLASS.notes} style={notesStyle} ref={notesRef}>
          {revealed.map((annotation) => (
            <Fragment key={annotation.operation.operationId}>
              <div data-annotation-note="" className={CLASS.note}>
                {annotation.operation.note}
              </div>
              {/* A saved operation whose captured recipient cannot be resolved
                  stays pending, so its reference never reaches a draft. Expose
                  the shared formatter's complete snapshot reference as inert,
                  selectable text so the user can copy it into a chosen draft;
                  it never chooses a substitute recipient or appends itself. */}
              {annotation.handoff.state === "pending" && (
                <div data-annotation-reference="" data-handoff-state="pending" className={CLASS.reference}>
                  {formatAnnotationReference(
                    annotation.operation.capture,
                    annotation.operation.lease.key.path,
                    annotation.operation.note,
                  )}
                </div>
              )}
            </Fragment>
          ))}
        </div>
      )}
    </div>
  );
}
