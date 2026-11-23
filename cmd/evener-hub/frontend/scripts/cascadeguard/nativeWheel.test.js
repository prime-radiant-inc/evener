// @vitest-environment node

// `readInsideTallEntry` repositions the source reader with a genuine native
// wheel before an interruption measurement. An upward wheel needs a full step
// of room to apply; a reader closer to the top than that clamps at the
// boundary and leaves the reading line at the entry's edge. These cases pin
// the rule that a repositioning wheel always points at the end that has room.
//
// `run.mjs` cannot be imported to check the call site itself: it reads the
// fixture from stdin at module load, so the last case pins that call site in
// its source text.
import { readFileSync } from "node:fs";
import { expect, test } from "vitest";
import { readerWheelDelta } from "./nativeWheel.mjs";

const RUN_MJS = new URL("./run.mjs", import.meta.url);

// The failing run's own settled point (`reading-attempt.json`): the reader is
// at the top of a scrollport with 11,755px of content.
const RECORDED_TOP = { scrollTop: 0, scrollHeight: 11755, clientHeight: 657 };
// A re-render had parked the same reader 92px down when its wheel was chosen;
// the virtualizer reset it to the top before the wheel event landed.
const RECORDED_NEAR_TOP = { scrollTop: 92, scrollHeight: 10569, clientHeight: 657 };
// The same run's preceding wheel interruption, parked inside a tall entry.
const RECORDED_INTERIOR = { scrollTop: 4707, scrollHeight: 24026, clientHeight: 644 };
// A bottom follower at the end of the same scrollport.
const RECORDED_END = { scrollTop: 11098, scrollHeight: 11755, clientHeight: 657 };

test("a reader with less than a full wheel step above it scrolls down", () => {
  expect(readerWheelDelta(RECORDED_TOP)).toBe(900);
  expect(readerWheelDelta(RECORDED_NEAR_TOP)).toBe(900);
});

test("a reader away from the top keeps the established upward gesture", () => {
  expect(readerWheelDelta(RECORDED_INTERIOR)).toBe(-900);
  expect(readerWheelDelta(RECORDED_END)).toBe(-900);
});

test("the tall-entry repositioning wheel is chosen by this module's rule", () => {
  const source = readFileSync(RUN_MJS, "utf8");
  expect(source).toContain('import { readerWheelDelta } from "./nativeWheel.mjs";');
  expect(source).toContain("await nativeReaderWheel(portExpr, readerWheelDelta);");
  expect(source).not.toContain("await nativeReaderWheel(portExpr, -900);");
});
