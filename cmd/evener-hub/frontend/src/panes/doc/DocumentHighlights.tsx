// Saved-highlight overlay for the browser reader (Task 5).
//
// Bands are non-text decorations: each is a positioned, empty control with no
// text of its own, so rendering or revealing highlights never changes authored
// prose, text-node parentage or the browser Selection string. Every saved note
// is rendered as literal, inert text under `data-annotation-note`, revealed on
// hover, keyboard focus or a click/tap for EVERY annotation overlapping the
// touched passage, and dismissed by a repeated tap, a tap outside every band,
// or focus leaving the bands and the revealed surface — so the underlying
// prose is always reachable again. Bands are real buttons, so they are natively
// keyboard
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
  // The rendered content's own root (the markdown host). Mermaid renders
  // diagrams asynchronously: a diagram can grow the content after mount while
  // the scroll owner's box is unchanged, moving annotated paragraphs with no
  // scroll, resize or font event. Observing the content root catches that
  // growth; omit it when the caller cannot name the content root.
  contentElement?: HTMLElement | null;
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

export function DocumentHighlights({ map, annotations, scrollElement, contentElement }: DocumentHighlightsProps) {
  const [fragments, setFragments] = useState<readonly HighlightFragment[]>([]);
  const [activeOperationId, setActiveOperationId] = useState<string | null>(null);
  // The delegated listeners live for the effect's whole lifetime, so they read
  // the currently revealed note through this ref rather than a stale closure.
  const activeOperationIdRef = useRef<string | null>(null);
  const overlayRef = useRef<HTMLDivElement | null>(null);
  const notesRef = useRef<HTMLDivElement | null>(null);
  // The selection/tap gesture must survive the overlay effect's own rebinds:
  // a revalidation landing mid-drag re-runs the effect on a changed annotations
  // list or map, and handlers rebound over empty closure state would ignore the
  // armed drag's release, leaving its trailing click free to toggle the note it
  // happens to land on. One ref carries the gesture across rebinds.
  const gestureRef = useRef({
    pointerId: null as number | null,
    start: null as { x: number; y: number } | null,
    operationId: null as string | null,
    moved: false,
    inNotes: false,
    handledTap: null as { x: number; y: number; at: number } | null,
  });

  useEffect(() => {
    const setActive = (operationId: string | null): void => {
      activeOperationIdRef.current = operationId;
      setActiveOperationId(operationId);
    };
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
      // An interaction on the note stack's own interactive surface (the
      // copyable pending reference) belongs to that surface: it never
      // reveals, switches or dismisses, so the reference's selection and
      // copy path stay usable.
      if (target instanceof Node && notesRef.current?.contains(target) === true) return;
      let operationId =
        target instanceof Element
          ? (target.closest("[data-document-highlight]")?.getAttribute("data-operation-id") ?? null)
          : null;
      if (!operationId && event instanceof MouseEvent) {
        const hover = (event.type === "mouseover" || event.type === "mousemove") && event.buttons === 0;
        if (hover || event.type === "click") operationId = bandAtPoint(event.clientX, event.clientY);
      }
      // A click is also the dismissal path, so the revealed note never traps the
      // underlying prose: pressing the same band again toggles the note away, and
      // a press that lands on no band clears whatever was revealed. A click that
      // merely trails an already-handled pointer press is consumed here. Hover
      // keeps its reveal-only semantics.
      if (event.type === "click") {
        // A selection-modified click is the selection's, never the note's:
        // the tap machinery already refuses Shift presses as selection
        // adjustments, and the trailing click of such a press carries the
        // same modifier - extending a text selection across a band must
        // neither reveal nor dismiss the note it crosses.
        if (event instanceof MouseEvent && event.shiftKey) return;
        const handledTap = gestureRef.current.handledTap;
        if (
          event instanceof MouseEvent &&
          handledTap !== null &&
          Math.abs(event.clientX - handledTap.x) < 5 &&
          Math.abs(event.clientY - handledTap.y) < 5 &&
          Date.now() - handledTap.at < 1500
        ) {
          gestureRef.current.handledTap = null;
          return;
        }
        setActive(operationId !== null && operationId === activeOperationIdRef.current ? null : operationId);
        return;
      }
      if (operationId) setActive(operationId);
    };
    // A genuine tap is a stationary primary press over a band's rectangle,
    // completed by the SAME uncancelled pointer's release — never a Shift
    // release and never a drag. Cancellation resets without revealing, and an
    // unrelated pointer's release is ignored because the arming pointer id must
    // match. The armed fields live in gestureRef so a rebind mid-gesture keeps
    // them; only the reset helper and the handlers are per-binding.
    const resetArmedGesture = (): void => {
      const gesture = gestureRef.current;
      // A consumed-click record deliberately survives the reset: the trailing
      // click arrives after pointerup, once no pointer is armed.
      gesture.pointerId = null;
      gesture.start = null;
      gesture.operationId = null;
      gesture.moved = false;
      gesture.inNotes = false;
    };
    const onPointerDown = (event: PointerEvent): void => {
      if (event.button !== 0 || event.isPrimary === false || event.shiftKey) return;
      const gesture = gestureRef.current;
      gesture.pointerId = event.pointerId;
      gesture.start = { x: event.clientX, y: event.clientY };
      gesture.moved = false;
      // A press on the note's own interactive surface never arms a reveal or
      // a dismissal: its release and trailing click belong to that surface.
      gesture.inNotes = event.target instanceof Node && notesRef.current?.contains(event.target) === true;
      gesture.operationId = gesture.inNotes ? null : bandAtPoint(event.clientX, event.clientY);
    };
    const onPointerMove = (event: PointerEvent): void => {
      const gesture = gestureRef.current;
      if (event.pointerId !== gesture.pointerId || !gesture.start) return;
      if (Math.abs(event.clientX - gesture.start.x) > 3 || Math.abs(event.clientY - gesture.start.y) > 3)
        gesture.moved = true;
    };
    const onPointerUp = (event: PointerEvent): void => {
      const gesture = gestureRef.current;
      if (event.pointerId !== gesture.pointerId) return;
      if (!gesture.moved && !event.shiftKey && !gesture.inNotes) {
        // The tap mirrors the click's dismissal contract: a repeated stationary
        // tap on the same band toggles the note away, and a stationary tap that
        // lands on no band clears the revealed note so touch users get the
        // underlying prose back. A moved tap is a selection drag and reveals
        // or dismisses nothing.
        if (gesture.operationId === null) {
          if (activeOperationIdRef.current !== null) setActive(null);
        } else {
          setActive(gesture.operationId === activeOperationIdRef.current ? null : gesture.operationId);
        }
        gesture.handledTap = { x: event.clientX, y: event.clientY, at: Date.now() };
      } else if (gesture.moved && !event.shiftKey) {
        // A drag is a selection gesture: it reveals nothing here, and the
        // compatibility click the browser fires at the release point must not
        // reach activate and toggle the note it happened to land on. Consume
        // it exactly like a tap's trailing click; keyboard-activated clicks
        // have no preceding pointer press and keep the full toggle path.
        gesture.handledTap = { x: event.clientX, y: event.clientY, at: Date.now() };
      }
      resetArmedGesture();
    };
    const onPointerCancel = (event: PointerEvent): void => {
      if (event.pointerId !== gestureRef.current.pointerId) return;
      resetArmedGesture();
    };
    // Keyboard reachability is symmetric: when focus leaves the bands and the
    // revealed surface, the note goes with it. Focus moving between the bands
    // or into the note stack's own controls keeps the note.
    const onFocusOut = (event: FocusEvent): void => {
      const next = event.relatedTarget;
      if (
        next instanceof Element &&
        (next.closest("[data-document-highlight]") !== null || notesRef.current?.contains(next) === true)
      ) {
        return;
      }
      setActive(null);
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
    // Async content growth (a mermaid diagram rendering late) moves annotated
    // paragraphs without changing the scroll owner's own box, so the observer
    // above never fires: observe the rendered content's root as well, and its
    // layout growth re-measures the same verified ranges at their new
    // geometry.
    if (contentElement) sizeObserver?.observe(contentElement);
    scrollElement.addEventListener("scroll", onScroll);
    view?.addEventListener("resize", onResize);
    fonts?.addEventListener("loadingdone", onScroll);
    visualViewport?.addEventListener("resize", onScroll);
    visualViewport?.addEventListener("scroll", onScroll);
    scrollElement.addEventListener("mouseover", activate, true);
    scrollElement.addEventListener("mousemove", activate, true);
    scrollElement.addEventListener("click", activate, true);
    scrollElement.addEventListener("focus", activate, true);
    scrollElement.addEventListener("focusout", onFocusOut, true);
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
      scrollElement.removeEventListener("focusout", onFocusOut, true);
      scrollElement.removeEventListener("pointerdown", onPointerDown, true);
      scrollElement.removeEventListener("wheel", onWheel);
      ownerDocument.removeEventListener("pointermove", onPointerMove);
      ownerDocument.removeEventListener("pointerup", onPointerUp);
      ownerDocument.removeEventListener("pointercancel", onPointerCancel);
    };
  }, [map, annotations, scrollElement, contentElement]);

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
