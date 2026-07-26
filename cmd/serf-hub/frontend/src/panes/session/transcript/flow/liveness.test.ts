// @vitest-environment node
import { expect, test } from "vitest";
import {
  describeLiveness,
  formatExactGap,
  formatQuietBucket,
  formatSubagentCount,
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

// runningSubagents is 0 in every test below except the dedicated "waiting"
// block further down - these pin the plain quiet/stalled decision exactly as
// before, for the ordinary case of an active turn with no delegated children
// at all.
test("describeLiveness: inactive thread shows nothing regardless of gap - liveness only evaluates while active", () => {
  expect(describeLiveness(999_999, false, 0)).toEqual({ level: "none", text: null });
});

test("describeLiveness: active thread with a fresh frame (gap 0) shows nothing", () => {
  expect(describeLiveness(0, true, 0)).toEqual({ level: "none", text: null });
});

test("describeLiveness: active thread just under the quiet threshold shows nothing", () => {
  expect(describeLiveness(19_999, true, 0).level).toBe("none");
});

test("describeLiveness: active thread AT the quiet threshold enters the quiet level", () => {
  expect(describeLiveness(20_000, true, 0).level).toBe("quiet");
});

test("describeLiveness: quiet level just under the stall threshold is still quiet, not stalled", () => {
  expect(describeLiveness(179_999, true, 0).level).toBe("quiet");
});

test("describeLiveness: AT the stall threshold escalates to stalled", () => {
  expect(describeLiveness(180_000, true, 0).level).toBe("stalled");
});

test("describeLiveness: quiet level's text contains the bucketed quiet phrase", () => {
  expect(describeLiveness(30_000, true, 0).text).toContain(formatQuietBucket(30_000));
});

test("describeLiveness: stalled level's text names the exact-ish gap and reads as an honest concern, not a reassuring animation cue", () => {
  const { level, text } = describeLiveness(185_000, true, 0);
  expect(level).toBe("stalled");
  expect(text).toContain(formatExactGap(185_000));
  expect(text!.toLowerCase()).toContain("stalled");
});

// --- runningSubagents: "waiting on N subagents" (design brief principle 6) -
// a wait explained by the active turn's own running children is not a stall
// and must not be reported as one (kata aep5: a parent blocked on its children
// looked identical to a genuinely stalled one, and the reader's correct action
// - wait vs. intervene - is opposite in the two cases).
//
// The asymmetry below is deliberate: running children suppress "quiet", but
// they do NOT suppress the stall report past the threshold. "A child is
// running" is a claim this client cannot verify - a row that lost contact with
// its child still reports running, because that is the honest default when
// nothing bad is known. Letting that claim silence the stall report forever
// would make the one case a reader most needs flagged - a parent wedged behind
// a child that died quietly - the one case the UI stayed confident about.

test("describeLiveness: a running child below the quiet threshold still shows nothing - the gap is the gate, not the child count", () => {
  expect(describeLiveness(19_999, true, 1).level).toBe("none");
});

test("describeLiveness: at the quiet threshold, a running child reads 'waiting' instead of 'quiet'", () => {
  const { level, text } = describeLiveness(20_000, true, 1);
  expect(level).toBe("waiting");
  expect(text).toBe("Waiting on 1 subagent");
});

test("describeLiveness: below the stall threshold, a running child reads 'waiting' with no gap figure - the wait is fully explained", () => {
  const { level, text } = describeLiveness(60_000, true, 2);
  expect(level).toBe("waiting");
  expect(text).toBe("Waiting on 2 subagents");
});

test("describeLiveness: past the stall threshold, a running child reports BOTH facts - believed running, and silent for a long time", () => {
  const { level, text } = describeLiveness(999_999, true, 2);
  expect(level).toBe("stalled");
  expect(text).toContain("Waiting on 2 subagents");
  expect(text).toContain(formatExactGap(999_999));
});

// The claim a stale row makes is exactly "running", so this is the shape the
// bug takes in the wild: one child believed running, nothing arriving for
// minutes. The line must not stay confidently quiet about it.
test("describeLiveness: a single believed-running child cannot suppress the stall report forever", () => {
  const { level, text } = describeLiveness(600_000, true, 1);
  expect(level).toBe("stalled");
  expect(text).toContain("no updates for");
});

test("describeLiveness: waiting text pluralizes the subagent count", () => {
  expect(describeLiveness(20_000, true, 1).text).toBe("Waiting on 1 subagent");
  expect(describeLiveness(20_000, true, 3).text).toBe("Waiting on 3 subagents");
});

test("describeLiveness: once no children are running, the plain quiet/stalled decision applies again", () => {
  expect(describeLiveness(30_000, true, 0).level).toBe("quiet");
  expect(describeLiveness(185_000, true, 0).level).toBe("stalled");
});

test("describeLiveness: an inactive thread with running children still shows nothing - the active gate still comes first", () => {
  expect(describeLiveness(999_999, false, 3)).toEqual({ level: "none", text: null });
});

// formatSubagentCount: pluralizes the running-children count for the
// "waiting" level's text - singular "1 subagent", plural otherwise (matching
// the design brief mockup's own worked example: "waiting on 1 subagent").
test("formatSubagentCount: exactly one reads the singular 'subagent'", () => {
  expect(formatSubagentCount(1)).toBe("1 subagent");
});

test("formatSubagentCount: more than one reads the plural 'subagents'", () => {
  expect(formatSubagentCount(2)).toBe("2 subagents");
  expect(formatSubagentCount(11)).toBe("11 subagents");
});

test("formatSubagentCount: zero reads the plural form too - not a real describeLiveness input (guarded by runningSubagents > 0), but pinned since it's a small, independently reusable formatter", () => {
  expect(formatSubagentCount(0)).toBe("0 subagents");
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
