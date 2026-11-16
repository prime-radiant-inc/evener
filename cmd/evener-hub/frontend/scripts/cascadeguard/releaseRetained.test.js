// @vitest-environment node

// The native interruption release is judged by `retainsNewerReadingPoint`, and
// `run.mjs` calls that exact function. These cases pin the rule at the
// driver/test seam so a wrong release verdict fails here instead of in a
// browser flake that took down merge groups (#6395).
//
// `run.mjs` cannot be imported to check the call site itself: it reads the
// fixture from stdin at module load, so the last case pins that call site in
// its source text.
//
// The recorded constants are the CI failure's own geometry (run 43660905645).
// Reading it, the source grew 1099px between the release point and the
// post-release reading, and the reader was re-pinned to the end both times;
// only the entry crossing the viewport top changed, from `item_assistant_221_0`
// at -5351 to `item_assistant_228_0` at -14.42.
import { readFileSync } from "node:fs";
import { expect, test } from "vitest";
import { retainsNewerReadingPoint } from "./releaseRetained.mjs";

const RUN_MJS = new URL("./run.mjs", import.meta.url);

const RECORDED_RELEASE = {
  entry: "item_assistant_221_0", offset: -5351, followingBottom: true,
  scrollTop: 22468, scrollHeight: 23082, viewport: 614,
};
const RECORDED_AFTER = {
  entry: "item_assistant_228_0", offset: -14.421875, followingBottom: true,
  scrollTop: 23567, scrollHeight: 24181, viewport: 614,
};

// A reader parked inside a tall entry, taken from the same run's
// `prepared.point`: 5,739px from the end, so it is genuinely away from the end
// rather than a rounding case the classifier would call following.
const midEntry = (overrides) => ({
  entry: "item_assistant_221_0", offset: -226, followingBottom: false,
  scrollTop: 17331, scrollHeight: 23070, viewport: 614,
  ...overrides,
});

test("content growth across the release keeps a bottom follower's reading point", () => {
  // Proves the geometry is the CI failure's: the port grew, the end moved with
  // it, and the top-crossing entry changed under the reader.
  expect(RECORDED_AFTER.scrollHeight - RECORDED_RELEASE.scrollHeight).toBe(1099);
  expect(RECORDED_AFTER.scrollTop).toBeGreaterThan(RECORDED_RELEASE.scrollTop);
  expect(RECORDED_AFTER.entry).not.toBe(RECORDED_RELEASE.entry);
  expect(retainsNewerReadingPoint(RECORDED_RELEASE, RECORDED_AFTER)).toBe(true);
});

test("a release that moves nothing still retains a bottom follower", () => {
  // The release rule must agree with the classifier that chose it. A bottom
  // follower the guard itself identified, handed back unchanged, is retained.
  expect(retainsNewerReadingPoint(RECORDED_RELEASE, RECORDED_RELEASE)).toBe(true);
  expect(retainsNewerReadingPoint(RECORDED_AFTER, RECORDED_AFTER)).toBe(true);
});

test("a release that leaves the end is not retained", () => {
  const after = { ...RECORDED_AFTER, followingBottom: false };
  expect(retainsNewerReadingPoint(RECORDED_RELEASE, after)).toBe(false);
});

test("a release that replays the obsolete pre-trigger target is not retained", () => {
  // oldTarget from the failing run: 14.42px short of the end. Retaining means
  // the classifier still sees a bottom follower, so the stale replay fails.
  const stale = { ...RECORDED_AFTER, scrollTop: 23552.578125, followingBottom: false };
  expect(RECORDED_AFTER.scrollHeight - RECORDED_AFTER.viewport - stale.scrollTop).toBeCloseTo(14.42, 2);
  expect(retainsNewerReadingPoint(RECORDED_RELEASE, stale)).toBe(false);
});

test("a different entry fails a reader parked away from the end at the same alignment", () => {
  expect(retainsNewerReadingPoint(midEntry(), midEntry({ entry: "item_assistant_228_0" }))).toBe(false);
});

test("the same entry at a different alignment fails a reader away from the end", () => {
  expect(retainsNewerReadingPoint(midEntry(), midEntry({ offset: -2509 }))).toBe(false);
});

test("the same entry within committed-geometry tolerance keeps a reader away from the end", () => {
  expect(retainsNewerReadingPoint(midEntry(), midEntry({ offset: -224 }))).toBe(true);
  expect(retainsNewerReadingPoint(midEntry(), midEntry({ offset: -228 }))).toBe(true);
  expect(retainsNewerReadingPoint(midEntry(), midEntry({ offset: -229 }))).toBe(false);
});

test("the interruption release is judged by the rule this module defines", () => {
  // The verdict must come from this function, not a reintroduced inline
  // comparison, and it must be keyed on the release point's follow state.
  const source = readFileSync(RUN_MJS, "utf8");
  expect(source).toContain("import { retainsNewerReadingPoint } from \"./releaseRetained.mjs\";");
  expect(source).toContain("const retainedNewer = retainsNewerReadingPoint(releasePoint, after);");
  expect(source).not.toContain("after.entry === releasePoint.entry && Math.abs(after.offset - releasePoint.offset) <= 2");
});
