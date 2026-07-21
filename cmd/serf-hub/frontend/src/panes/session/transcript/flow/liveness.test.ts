import { expect, test } from "vitest";
import {
  describeLiveness,
  formatExactGap,
  formatQuietBucket,
  QUIET_THRESHOLD_MS,
  STALL_THRESHOLD_MS,
} from "./liveness";

// Thresholds mirror the legacy renderer's own session-level liveness clock
// (docs/web-ui/parity/parity-m4-transcript.md §16: "quiet at gap>=20000ms,
// concern/stalled at gap>=180000ms") - NOT the separate per-subagent-row
// thresholds (10s/45s), which are a different feature area (T3's subagent
// module), explicitly not unified with this one per the doc's own Highlights.
test("QUIET_THRESHOLD_MS is 20s", () => {
  expect(QUIET_THRESHOLD_MS).toBe(20_000);
});

test("STALL_THRESHOLD_MS is 180s", () => {
  expect(STALL_THRESHOLD_MS).toBe(180_000);
});

test("describeLiveness: inactive thread shows nothing regardless of gap - liveness only evaluates while active", () => {
  expect(describeLiveness(999_999, false)).toEqual({ level: "none", text: null });
});

test("describeLiveness: active thread with a fresh frame (gap 0) shows nothing", () => {
  expect(describeLiveness(0, true)).toEqual({ level: "none", text: null });
});

test("describeLiveness: active thread just under the quiet threshold shows nothing", () => {
  expect(describeLiveness(19_999, true).level).toBe("none");
});

test("describeLiveness: active thread AT the quiet threshold enters the quiet level", () => {
  expect(describeLiveness(20_000, true).level).toBe("quiet");
});

test("describeLiveness: quiet level just under the stall threshold is still quiet, not stalled", () => {
  expect(describeLiveness(179_999, true).level).toBe("quiet");
});

test("describeLiveness: AT the stall threshold escalates to stalled", () => {
  expect(describeLiveness(180_000, true).level).toBe("stalled");
});

test("describeLiveness: quiet level's text contains the bucketed quiet phrase", () => {
  expect(describeLiveness(30_000, true).text).toContain(formatQuietBucket(30_000));
});

test("describeLiveness: stalled level's text names the exact-ish gap and reads as an honest concern, not a reassuring animation cue", () => {
  const { level, text } = describeLiveness(185_000, true);
  expect(level).toBe("stalled");
  expect(text).toContain(formatExactGap(185_000));
  expect(text!.toLowerCase()).toContain("stalled");
});

// formatQuietBucket: legacy parity bucketing (parity doc §16) - "<45s->
// '~30s'", "<90s->'~1m'", ">=90s->'~2m'" - evaluated against the gap's own
// raw seconds value, not seconds-since-the-quiet-threshold. The top bucket
// deliberately covers the WHOLE 90s-180s range, so the quiet line never
// shows "~3m" right before escalating.
test("formatQuietBucket: just under 45s reads ~30s", () => {
  expect(formatQuietBucket(44_999)).toBe("~30s");
});

test("formatQuietBucket: at 45s crosses into ~1m", () => {
  expect(formatQuietBucket(45_000)).toBe("~1m");
});

test("formatQuietBucket: just under 90s still reads ~1m", () => {
  expect(formatQuietBucket(89_999)).toBe("~1m");
});

test("formatQuietBucket: at 90s crosses into ~2m", () => {
  expect(formatQuietBucket(90_000)).toBe("~2m");
});

test("formatQuietBucket: just under the 180s stall threshold still reads ~2m, never ~3m", () => {
  expect(formatQuietBucket(179_999)).toBe("~2m");
});

test("formatQuietBucket: a gap at the very start of the quiet band (20s) reads ~30s", () => {
  expect(formatQuietBucket(20_000)).toBe("~30s");
});

// formatExactGap: legacy parity (parity doc §16) - "<60s->'Ns'", else 'Mm'
// plus ' Ss' only when the remainder is non-zero. Not reached via
// describeLiveness's own stalled path today (always >=180_000ms), but
// implemented and tested to the full pinned contract regardless, since it's
// a small, independently reusable formatter.
test("formatExactGap: under 60s reads as whole seconds", () => {
  expect(formatExactGap(5_000)).toBe("5s");
  expect(formatExactGap(59_000)).toBe("59s");
});

test("formatExactGap: an exact whole minute has no trailing seconds", () => {
  expect(formatExactGap(180_000)).toBe("3m");
});

test("formatExactGap: a non-zero remainder appends seconds", () => {
  expect(formatExactGap(185_000)).toBe("3m 5s");
});

test("formatExactGap: minutes and seconds both round down to whole units", () => {
  expect(formatExactGap(185_999)).toBe("3m 5s");
});
