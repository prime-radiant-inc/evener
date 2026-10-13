// The composer's staged-attachment remove control on a wide touch screen (a
// tablet in landscape, a touch laptop). The phone case reaches the touch rules
// through max-width: 899px; this one is above that breakpoint, so only the
// (any-pointer: coarse) half of the media query can apply them. Same per-tile
// checks as the phone case.
import { checkTile } from "../attachment-remove-touch-phone/assert.mjs";

export default function assert(m) {
  if (m.viewportWidth < 900) {
    return {
      pass: false,
      reason: `the viewport is ${m.viewportWidth}px wide - under the 900px breakpoint the width alone applies the touch rules, so the coarse-pointer query is never exercised`,
    };
  }
  if (!m.anyPointerCoarse) {
    return {
      pass: false,
      reason: "(any-pointer: coarse) does not match - the case's touch emulation did not apply",
    };
  }
  for (const prefix of ["first", "second"]) {
    const failure = checkTile(prefix, m[prefix]);
    if (failure) return { pass: false, reason: failure };
  }
  return {
    pass: true,
    reason: `at ${m.viewportWidth}px with a coarse pointer both thumbnail centres open their lightbox; both remove controls are visible at rest, ${m.first.removeWidth}x${m.first.removeHeight}, and hit-test whole`,
  };
}
