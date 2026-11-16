// The rule the native interruption release is judged by: did the released
// target measurements keep the reading point the reader held when the release
// began?
//
// `run.mjs` calls this after `nativePositioningInterruption` releases the held
// measurements, and the regression test imports the same module. No test can
// import run.mjs itself: it reads the fixture from stdin at module load.
//
// Both arguments are reading points: the geometry `readingPointExpr` in run.mjs
// returns, as plain objects with entry, offset and followingBottom.
//
// A bottom follower's reading point is the end, not an entry. The fixture's
// live source keeps growing while the hold is up, and rows arriving below a
// reader parked on the end move whichever entry crosses the viewport top
// without moving the reader off where its input put it (#6395). Entry identity
// therefore cannot hold for a bottom follower across the release.
//
// `followingBottom` is the guard's own classification of that state
// (readingPointExpr, at the product's own at-bottom threshold), and it is the
// same field the pill's live-result assertion reads afterwards. Judging the
// release by a tighter band than the one that classified it would reject a
// release that moved nothing at all.
export function retainsNewerReadingPoint(releasePoint, after) {
  if (releasePoint.followingBottom) return after.followingBottom;
  return after.entry === releasePoint.entry && Math.abs(after.offset - releasePoint.offset) <= 2;
}
