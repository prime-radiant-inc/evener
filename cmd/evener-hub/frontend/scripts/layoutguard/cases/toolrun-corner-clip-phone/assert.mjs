// A folded tool run that is a whole card must leave an overhanging control in
// its last row reachable, and everything that paints to the card's edge must
// stay inside the card's rounded corners.
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
  if (!m.bandReachesCardEdge) failures.push("the last row's evidence band does not run to the card's lower-left edge");
  if (m.bandCornerPainted) failures.push("the evidence band paints its square corner outside the card's rounded corner");
  if (failures.length > 0) return { pass: false, reason: failures.join("; ") };
  return {
    pass: true,
    reason: `the ${m.controlHeight}px control in the last row is reachable at its bottom edge, and the summary and the band stay inside the card's ${m.cardRadius}px corners`,
  };
}
