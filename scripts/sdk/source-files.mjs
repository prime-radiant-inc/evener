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

// Every extension Metro and Vite resolve as a source module, and the directory
// names none of these tools should descend into. Both lists are shared for the
// same reason the loader-call set is: the rewriter, the value-import derivation
// and the grep gate each sweep the app trees, and a file one of them reads and
// another skips is a file that reaches the package by path in silence. The
// grep gate is a shell script, so it carries its own copy of both, and a Go
// audit asserts the two stay equal (like it does for the loader calls).
export const SOURCE_EXTENSIONS = [".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs"];

// A JavaScript module reaches the package by the same relative path a
// TypeScript one does; the target it lands on is still TypeScript.
export const SKIPPED_DIRS = new Set(["node_modules", "dist", "build", "ios", "android", "__snapshots__", ".git"]);
