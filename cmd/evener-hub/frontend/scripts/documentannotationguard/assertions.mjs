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

// The READABLE text of a marker is visible only when the clip itself has
// positive area and EVERY positive-area fragment of the marker's own DOM Range
// lies fully inside that clip, within the existing 1px tolerance. Mere
// intersection is not enough: a sliver of one fragment inside the clip, or a
// wrapped marker with one offscreen fragment, is REJECTED, as is a zero-area or
// inverted clip. The tall note itself is never constrained this way - only its
// short beginning/end marker fragments must fit.
export function markerVisible(clip, fragments, tolerance = 1) {
  if (!rectHasArea(clip)) return false;
  const visible = fragments.filter(rectHasArea);
  if (visible.length === 0) return false;
  return visible.every((fragment) => rectInside(clip, fragment, tolerance));
}
