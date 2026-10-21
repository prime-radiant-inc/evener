// Independent, pure containment oracle for the document-annotation browser
// guard. Deliberately free of DOM or product imports so its Node regression can
// exercise the geometry rules directly. The guard measures TWO distinct roots:
// the visible PANE (which legally contains a footer) governs footer controls,
// while the document's actual SCROLL BODY governs the revealed note stack that
// is clipped by it.

// A rectangle with no positive area is never "contained".
export function rectHasArea(rect) {
  return (
    rect.right > rect.left &&
    rect.bottom > rect.top &&
    Number.isFinite(rect.left) &&
    Number.isFinite(rect.top) &&
    Number.isFinite(rect.right) &&
    Number.isFinite(rect.bottom)
  );
}

// `inner` fully inside `outer`, within the approved 1 CSS pixel tolerance.
export function rectInside(outer, inner, tolerance = 1) {
  return (
    rectHasArea(inner) &&
    inner.left >= outer.left - tolerance &&
    inner.top >= outer.top - tolerance &&
    inner.right <= outer.right + tolerance &&
    inner.bottom <= outer.bottom + tolerance
  );
}

// The revealed note stack is clipped by the document's actual scroll body, so it
// must be measured against `scrollRect`, never the larger pane root that also
// contains the legal footer. `paneRect` is accepted for call-site symmetry and
// receipts only.
export function noteStackContained({ paneRect, scrollRect }, stackRect, tolerance = 1) {
  void paneRect;
  return rectInside(scrollRect, stackRect, tolerance);
}

// The real durable-storage store list the driver reads through `readStorage`. It
// is the collection obligation the browser journey's native IndexedDB read must
// honor. It omits `annotationNamespaces`; adding that store is a separately
// granted runtime change, not this unit.
export const STORES = [
  "composerDrafts",
  "annotationPayloads",
  "annotationOperations",
  "annotationHeads",
  "annotationEvidence",
  "annotationSequences",
  "outbox",
  "optimistic",
  "recovery",
  "sequences",
];
