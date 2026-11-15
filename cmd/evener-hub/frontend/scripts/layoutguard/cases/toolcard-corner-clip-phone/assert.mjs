// A one-element tool card must leave the Open control's whole touch target
// reachable and still keep the last row's evidence band inside its rounded
// lower corners.
export default function assert(m) {
  const failures = [];
  if (!m.openTopReachesControl)
    failures.push(
      `a tap inside the Open control's top edge lands on ${m.openTopHit} - the card clips its touch target`,
    );
  if (!m.openBottomReachesControl)
    failures.push(
      `a tap inside the Open control's bottom edge lands on ${m.openBottomHit} - the card clips its touch target`,
    );
  if (!(m.cardRadius > 0)) failures.push("the card has no rounded lower corner to keep the band inside");
  if (!m.bandReachesCardEdge) failures.push("the last row's evidence band does not run to the card's lower-left edge");
  if (m.bandCornerPainted)
    failures.push("the evidence band paints its square corner outside the card's rounded corner");
  if (failures.length > 0) return { pass: false, reason: failures.join("; ") };
  return {
    pass: true,
    reason: `the ${m.openHeight}px Open control is reachable at both edges, and the band runs to the card edge inside its ${m.cardRadius}px corner`,
  };
}
