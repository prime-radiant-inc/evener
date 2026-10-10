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

  useEffect(() => {
    const measure = (): void => {
      const clip = scrollElement.getBoundingClientRect();
      const targets = annotations.map((annotation) => ({
        operationId: annotation.operation.operationId,
        sourceRange: annotation.operation.capture.sourceRange,
      }));
      setFragments(measureDocumentHighlights(map, targets, clip).slice());
    };
    // Active-passage selection is delegated from the scroll owner so one capture
    // listener covers hover, focus and click: `focus` does not bubble, so only a
    // capture-phase listener sees it.
    const activate = (event: Event): void => {
      const target = event.target;
      if (!(target instanceof Element)) return;
      const operationId = target.closest("[data-document-highlight]")?.getAttribute("data-operation-id");
      if (operationId) setActiveOperationId(operationId);
    };

    const ownerDocument = scrollElement.ownerDocument;
    const view = ownerDocument.defaultView;
    const fonts = (ownerDocument as unknown as { fonts?: FontFaceSet }).fonts;
    const visualViewport = view?.visualViewport ?? null;
    const onScroll = (): void => measure();
    const onResize = (): void => measure();

    measure();
    scrollElement.addEventListener("scroll", onScroll);
    view?.addEventListener("resize", onResize);
    fonts?.addEventListener("loadingdone", onScroll);
    visualViewport?.addEventListener("resize", onScroll);
    visualViewport?.addEventListener("scroll", onScroll);
    scrollElement.addEventListener("mouseover", activate, true);
    scrollElement.addEventListener("click", activate, true);
    scrollElement.addEventListener("focus", activate, true);

    return () => {
      scrollElement.removeEventListener("scroll", onScroll);
      view?.removeEventListener("resize", onResize);
      fonts?.removeEventListener("loadingdone", onScroll);
      visualViewport?.removeEventListener("resize", onScroll);
      visualViewport?.removeEventListener("scroll", onScroll);
      scrollElement.removeEventListener("mouseover", activate, true);
      scrollElement.removeEventListener("click", activate, true);
      scrollElement.removeEventListener("focus", activate, true);
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
        <div className={CLASS.notes} style={notesStyle}>
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
