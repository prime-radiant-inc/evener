// Saved-highlight fragment measurement for the browser reader (Task 5).
//
// Measures the map's OWN verified Ranges (`rangesFor`) using the rectangles the
// browser reports (`Range.getClientRects`) intersected with the supplied real
// scroll clip. Only positive intersections survive, with fractional CSS pixels
// preserved exactly; a stale/disposed map or an unavailable range yields none.
// It never paints whole blocks for a partial word and never mutates prose.
import type { SourceRange } from "@evener/appwire-client";
import type { BoundDocumentMap } from "../../widgets/markdown/documentSourceMap";

export interface HighlightTarget {
  operationId: string;
  sourceRange: SourceRange;
}

export interface HighlightFragment {
  operationId: string;
  // This piece's slot in its annotation's measured rect sequence. The slot is
  // stable while the rendered content keeps its layout structure: scrolling,
  // resize and reflow move a fragment's rect but keep its enumeration, so a
  // band's React key survives geometry-only updates and keyboard focus is not
  // dropped. Clipped-out rects still consume their slot, so survivors keep
  // their ordinals when a scroll moves pieces in and out of the clip.
  ordinal: number;
  rect: DOMRectReadOnly;
}

function clippedRect(left: number, top: number, right: number, bottom: number): DOMRectReadOnly {
  return {
    left,
    top,
    right,
    bottom,
    width: right - left,
    height: bottom - top,
    x: left,
    y: top,
    toJSON: () => ({}),
  } as DOMRectReadOnly;
}

export function measureDocumentHighlights(
  map: BoundDocumentMap,
  targets: readonly HighlightTarget[],
  clip: DOMRectReadOnly,
): readonly HighlightFragment[] {
  const fragments: HighlightFragment[] = [];
  for (const target of targets) {
    let slot = 0;
    for (const range of map.rangesFor(target.sourceRange)) {
      const rects = range.getClientRects();
      for (let index = 0; index < rects.length; index += 1) {
        const ordinal = slot;
        slot += 1;
        const rect = rects[index];
        if (!rect) continue;
        const left = Math.max(rect.left, clip.left);
        const top = Math.max(rect.top, clip.top);
        const right = Math.min(rect.right, clip.right);
        const bottom = Math.min(rect.bottom, clip.bottom);
        if (right > left && bottom > top) {
          fragments.push({ operationId: target.operationId, ordinal, rect: clippedRect(left, top, right, bottom) });
        }
      }
    }
  }
  return fragments;
}
