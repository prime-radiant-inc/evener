// What counts as a test file, in one place.
//
// The package-test gate and the value-import derivation both need the answer,
// and they had two: one matched Vitest's own default glob, the other looked
// for the substring ".test." and so missed every .spec. file. A file that one
// tool treats as a test and the other as ordinary source is a gap in whichever
// is stricter, so the predicate is shared rather than restated.

// The same shape as Vitest's default test glob.
const TEST_FILE = /\.(test|spec)\.[cm]?[jt]sx?$/;

export function isTestFile(name) {
  return TEST_FILE.test(name);
}
