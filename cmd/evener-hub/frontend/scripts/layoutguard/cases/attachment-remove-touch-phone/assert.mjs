// The composer's staged-attachment remove control on a phone. Desktop hides it
// until the tile is hovered; a touch screen has no hover, and the IconButton
// grows to the 44px tap floor there, so a hidden 44px control inside an 80px
// thumbnail swallowed taps aimed at the image and removed the attachment
// instead of opening the lightbox.
//
// For each of two side-by-side tiles:
//   - a tap at the thumbnail's centre lands on that tile's open control
//   - the remove control is visible at rest (opacity 1) and at least 44x44
//   - every edge of the remove control's hit box reaches it, so neither the
//     neighbouring tile nor an ancestor's clip takes part of it
const TAP_MIN = 44;

function checkTile(prefix, t) {
  if (t.centreHit !== `${prefix}-open`) {
    return `a tap at the ${prefix} thumbnail's centre lands on ${t.centreHit}, not ${prefix}-open - tapping the image does not open the lightbox`;
  }
  if (t.slotOpacity !== 1) {
    return `the ${prefix} remove control is opacity ${t.slotOpacity} at rest - a touch screen has no hover to reveal it`;
  }
  if (t.removeWidth < TAP_MIN || t.removeHeight < TAP_MIN) {
    return `the ${prefix} remove control is ${t.removeWidth}x${t.removeHeight}, under the ${TAP_MIN}px tap floor`;
  }
  if (t.removeCentreHit !== `${prefix}-remove`) {
    return `a tap at the ${prefix} remove control's centre lands on ${t.removeCentreHit}, not ${prefix}-remove`;
  }
  const stray = t.edges.find((id) => id !== `${prefix}-remove`);
  if (stray !== undefined) {
    return `an edge of the ${prefix} remove control's hit box lands on ${stray} (top, right, bottom, left: ${t.edges.join(", ")}) - something covers or clips it`;
  }
  return null;
}

export default function assert(m) {
  if (m.viewportWidth >= 900) {
    return {
      pass: false,
      reason: `the viewport is ${m.viewportWidth}px wide - the case's phone emulation did not apply, so the touch rules were never exercised`,
    };
  }
  for (const prefix of ["first", "second"]) {
    const failure = checkTile(prefix, m[prefix]);
    if (failure) return { pass: false, reason: failure };
  }
  return {
    pass: true,
    reason: `at ${m.viewportWidth}px both thumbnail centres open their lightbox; both remove controls are visible at rest, ${m.first.removeWidth}x${m.first.removeHeight}, and hit-test whole`,
  };
}
