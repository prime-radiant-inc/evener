// The composer's narrow-layout gate: below the phone-width boundary the verb
// cluster leaves the control row and rides in the session menu instead of
// wrapping below the status row (Jesse's 2026-09-28 design ruling on the
// #1339 phone-width wrap, which superseded the provisional three-verb wrap
// rule this boundary was measured for).
//
// Why a ResizeObserver and not the viewport: a docked pane squeezed to phone
// width inside a desktop viewport must compact exactly like the phone does
// (composer.module.css states that invariant for its own container queries),
// so the gate reads the composer root's own box, the same box the retired
// wrap rule's @container query resolved against. And why JS and not CSS: the
// session menu's items render through a portal to document.body
// (widgets/menu), outside every container the pane's queries can reach, so
// the verbs' relocation between two DOM homes cannot be a pure style change.
//
// The observer also can't be CSS for the buttons alone: hiding the row
// buttons below the boundary while the menu shows the same verbs would leave
// two DOM owners for one control - the inline-controls design's own rule is
// one DOM location, one tab order (2026-08-10-composer-controls-inline-design.md).

import { useLayoutEffect, useState } from "react";

/** The composer content width at and below which the narrow layout answers:
 * the boundary the retired three-verb wrap rule was measured against
 * (promptcard's former @container (max-width: 399px), issue #1339). The
 * browser guard sweeps this exact edge (scripts/overflowguard's
 * VERB_WRAP_WIDTHS brackets it at 399/400). */
export const COMPOSER_PHONE_MAX_WIDTH = 399;

/** True while the element the returned callback ref attaches to is as
 * narrow as a phone; the tuple's second member is that ref. The gate
 * follows the element through mounts and unmounts - a state-backed ref,
 * not a ref object's identity - so a host whose element first appears
 * after the initial render (Composer renders null until its model
 * hydrates) is observed from the moment it exists. Wide is the default in
 * every environment without a live ResizeObserver - jsdom ships none and
 * lays out no cascade, so its boxes read zero-width and the seed below
 * skips too; the whole jsdom suite keeps the wide layout unless a test
 * drives the narrow side through its own stub. */
export function useNarrowComposer(): [boolean, (element: HTMLElement | null) => void] {
  const [narrow, setNarrow] = useState(false);
  const [element, setElement] = useState<HTMLElement | null>(null);
  useLayoutEffect(() => {
    if (element === null || typeof ResizeObserver === "undefined") return;
    // Seed from the live box before the first paint so a phone-width mount
    // never flashes the wide row - the wrap CSS is retired, so wide genuinely
    // overflows there. Zero-width boxes (jsdom, an undisplayed ancestor)
    // skip the seed and wait for the observer's own initial delivery instead.
    const seedWidth = element.getBoundingClientRect().width;
    if (seedWidth > 0) setNarrow(seedWidth <= COMPOSER_PHONE_MAX_WIDTH);
    const observer = new ResizeObserver((entries) => {
      const entry = entries[0];
      if (entry !== undefined) setNarrow(entry.contentRect.width <= COMPOSER_PHONE_MAX_WIDTH);
    });
    observer.observe(element);
    return () => observer.disconnect();
  }, [element]);
  return [narrow, setElement];
}
