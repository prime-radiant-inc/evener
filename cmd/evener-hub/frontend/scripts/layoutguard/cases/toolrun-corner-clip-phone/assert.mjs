// A folded tool run that is a whole card must leave an overhanging control in
// its last row reachable, everything that paints to the card's edge must stay
// inside the card's rounded corners, and the Disclosure body wrapper's top gap
// must be cancelled so the first row sits on the body's hairline.
export default function assert(m) {
  const failures = [];
  if (!(m.cardRadius > 0)) failures.push("the run's card has no rounded lower corner");
  if (!m.controlBottomReachesControl)
    failures.push(
      `a tap inside the last row's control at its bottom edge lands on ${m.controlBottomHit} - the card clips its touch target`,
    );
  if (m.closedSummaryCornerPainted)
    failures.push("the closed run's summary paints its square corner outside the card's rounded corner");
  if (m.openSummaryRadius !== 0)
    failures.push("the open run's summary rounds its lower corners above the rows it heads");
  if (!(m.firstRowGap >= 0.5 && m.firstRowGap <= 1.5))
    failures.push(
      `the open run's first row starts ${m.firstRowGap}px below the summary - expected the body's 1px hairline, not Disclosure's uncancelled --space-1 gap`,
    );
  if (!m.bandReachesCardEdge) failures.push("the last row's evidence band does not run to the card's lower-left edge");
  if (m.bandCornerPainted)
    failures.push("the evidence band paints its square corner outside the card's rounded corner");
  if (failures.length > 0) return { pass: false, reason: failures.join("; ") };
  return {
    pass: true,
    reason: `the ${m.controlHeight}px control in the last row is reachable at its bottom edge, the summary and the band stay inside the card's ${m.cardRadius}px corners, and the first row is seated on the body's hairline`,
  };
}
