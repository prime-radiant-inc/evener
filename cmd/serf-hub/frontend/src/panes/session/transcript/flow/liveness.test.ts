// @vitest-environment node
import { expect, test } from "vitest";
import {
  describeLiveness,
  formatExactGap,
  formatQuietBucket,
  formatSubagentCount,
  QUIET_THRESHOLD_MS,
  type RetryWait,
  STALL_THRESHOLD_MS,
} from "./liveness";

// Fills in RetryWait's non-attempt/delay fields with values these tests don't
// care about (a nominal 5s call so far, not yet past its wait) so each test
// only spells out what it's actually pinning.
function retryWait(overrides: Partial<RetryWait> & Pick<RetryWait, "attempt" | "attemptCap" | "delayMs">): RetryWait {
  return { errorClass: undefined, model: undefined, groupElapsedMs: 5_000, inProgress: false, ...overrides };
}

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

// kata 4zn8: a sustained provider rate limit rendered as "May be stalled — no
// updates for 10m 30s". Honest, and useless: the daemon knew it was on attempt
// 9 of 11 with 60s to the next try. A retry the daemon reports is a wait it can
// explain, so it pre-empts the guess — under the same asymmetry the subagent
// case already uses, because a retry claim can go stale too (a daemon that dies
// mid-retry leaves this state behind forever, kata 3h02).
test("describeLiveness: a pending retry explains the quiet instead of guessing", () => {
  const retry = retryWait({ attempt: 9, attemptCap: 11, delayMs: 60_000, errorClass: "rate_limit" });
  expect(describeLiveness(30_000, true, 0, retry)).toEqual({
    level: "retrying",
    text: "rate limited — attempt 9/11 — retrying in 60s — 5s on this call",
  });
});

test("describeLiveness: a non-rate-limit retryable error names itself generically", () => {
  const retry = retryWait({ attempt: 2, attemptCap: 11, delayMs: 4_000, errorClass: "server" });
  expect(describeLiveness(30_000, true, 0, retry)).toEqual({
    level: "retrying",
    text: "provider error — attempt 2/11 — retrying in 4s — 5s on this call",
  });
});

test("describeLiveness: past the stall threshold a retry reports both facts, never suppressing the stall", () => {
  const retry = retryWait({ attempt: 9, attemptCap: 11, delayMs: 60_000, errorClass: "rate_limit" });
  expect(describeLiveness(630_000, true, 0, retry)).toEqual({
    level: "stalled",
    text: "rate limited — attempt 9/11 — retrying in 60s — 5s on this call — no updates for 10m 30s",
  });
});

// A FIRST retry (attempt 1) specifically - not "every retry" - stays gated
// behind the ordinary 20s quiet clock. See the kata gw2c block below: from
// attempt 2 onward a retry bypasses this gate entirely, so this test's own
// invisibility depends on attempt being 1, not just on being under 20s.
test("describeLiveness: a first retry (attempt 1) under the quiet threshold stays invisible", () => {
  const retry = retryWait({ attempt: 1, attemptCap: 11, delayMs: 1_000, errorClass: "rate_limit" });
  expect(describeLiveness(5_000, true, 0, retry)).toEqual({ level: "none", text: null });
});

test("describeLiveness: a retry pre-empts the subagent wait — it is the more specific explanation", () => {
  const retry = retryWait({ attempt: 3, attemptCap: 11, delayMs: 8_000, errorClass: "rate_limit" });
  expect(describeLiveness(30_000, true, 2, retry)).toEqual({
    level: "retrying",
    text: "rate limited — attempt 3/11 — retrying in 8s — 5s on this call",
  });
});

// kata gw2c: the retry branch above shared the SAME 20s quiet clock as
// ordinary silence, so the faster a provider fails, the less likely the
// user was to ever see why - a fast retry storm (Retry-After: 2, 11
// attempts, ~22s total budget) burned almost its whole budget invisibly,
// flashing the explanation for its last ~2 seconds before the turn failed.
// The retry is KNOWN information the daemon reported, not an inference
// from absence, so it should not share silence's clock at all past the
// point the original no-flicker reasoning stops applying.
//
// Chosen trigger (recorded on the kata before implementing): retry.attempt
// >= 2 bypasses the 20s gate; attempt 1 stays fully gated (test above).
// attempt is already reported on every serf/thread/modelRetry notification
// (reducer.ts), so this needs no new wire field, no accumulator, and no
// new clock - describeLiveness stays a pure function of its existing four
// inputs. It also preserves the ORIGINAL reason the gate existed on this
// branch at all: a retry that never gets past attempt 1 (resolves or gives
// up on its first try) never bypasses the gate, so it can never flicker.
test("describeLiveness: a fast retry storm's second attempt becomes visible immediately, without waiting for the 20s quiet threshold", () => {
  const retry = retryWait({ attempt: 2, attemptCap: 11, delayMs: 2_000, errorClass: "rate_limit" });
  expect(describeLiveness(3_000, true, 0, retry)).toEqual({
    level: "retrying",
    text: "rate limited — attempt 2/11 — retrying in 2s — 5s on this call",
  });
});

test("describeLiveness: a single sub-second retry (attempt 1) still does not flicker into view under the quiet threshold", () => {
  const retry = retryWait({ attempt: 1, attemptCap: 11, delayMs: 500, errorClass: "rate_limit" });
  expect(describeLiveness(600, true, 0, retry)).toEqual({ level: "none", text: null });
});

// Proves the trigger is attempt count, not delay duration or elapsed gap: a
// 46s per-attempt delay (the sustained/legacy-incident cadence, median 46s
// per rejection) still becomes visible the instant attempt 2 arrives, well
// under the 20s gap that would previously have hidden it.
test("describeLiveness: a sustained-cadence retry (long per-attempt delay) also becomes visible on its second attempt, before the quiet threshold", () => {
  const retry = retryWait({ attempt: 2, attemptCap: 11, delayMs: 46_000, errorClass: "rate_limit" });
  expect(describeLiveness(3_000, true, 0, retry)).toEqual({
    level: "retrying",
    text: "rate limited — attempt 2/11 — retrying in 46s — 5s on this call",
  });
});

// Component 1's two honesty rules (design doc): the denominator is the
// effective bound (AttemptCap), not the raw policy budget, and the model
// renders only when a chain walk actually switched it - RetryWait.model is
// already pre-narrowed by the caller (LivenessLine.tsx) to be set ONLY when
// it differs from the session's primary model, so describeLiveness/
// formatRetryWait render whatever it is given with no comparison of their own.
test("describeLiveness: the denominator is attemptCap, not the raw retry policy budget", () => {
  // A consume-phase failure has dropped the effective bound to 4 well before
  // maxAttempts' own 11 would suggest more retries remain - rendering 9/11
  // here would promise retries the early-stop rule will not spend.
  const retry = retryWait({ attempt: 3, attemptCap: 4, delayMs: 5_000, errorClass: "rate_limit" });
  expect(describeLiveness(30_000, true, 0, retry).text).toBe(
    "rate limited — attempt 3/4 — retrying in 5s — 5s on this call",
  );
});

test("describeLiveness: names the model when a fallback chain walk switched it", () => {
  const retry = retryWait({ attempt: 1, attemptCap: 4, delayMs: 5_000, errorClass: "rate_limit", model: "gpt-5" });
  expect(describeLiveness(30_000, true, 0, retry).text).toBe(
    "rate limited (gpt-5) — attempt 1/4 — retrying in 5s — 5s on this call",
  );
});

test("describeLiveness: shows the retry group's elapsed time, per the current call rather than the whole turn", () => {
  const retry = retryWait({
    attempt: 9,
    attemptCap: 11,
    delayMs: 60_000,
    errorClass: "rate_limit",
    groupElapsedMs: 14 * 60_000,
  });
  expect(describeLiveness(30_000, true, 0, retry).text).toBe(
    "rate limited — attempt 9/11 — retrying in 60s — 14m on this call",
  );
});

// Web-only difference from the TUI (task 11 brief): while deltas flow, the
// wait is over and clearing the indicator would re-create the vanishing-chip
// bug, so it renders "in progress" instead of a stale delay countdown.
// RetryWait.inProgress is pre-derived by the caller (spec: "clients can
// derive waiting-vs-in-progress locally: delay expired, or a delta arrived").
test("describeLiveness: renders 'in progress' instead of a delay countdown once the wait is over", () => {
  const retry = retryWait({ attempt: 9, attemptCap: 11, delayMs: 60_000, errorClass: "rate_limit", inProgress: true });
  expect(describeLiveness(30_000, true, 0, retry).text).toBe(
    "rate limited — attempt 9/11 — in progress — 5s on this call",
  );
});

// Design doc Component 1 (Jesse 2026-08-08 rendering note) pins the chip
// string character for character: "provider error — attempt 3/4 — retrying
// in 32s — 14m on this call" (waiting) / "... — in progress — 14m on this
// call" (streaming). This is the literal contract, not a paraphrase.
test("describeLiveness: matches the spec's exact waiting chip string, character for character", () => {
  const retry = retryWait({
    attempt: 3,
    attemptCap: 4,
    delayMs: 32_000,
    errorClass: "server",
    groupElapsedMs: 14 * 60_000,
  });
  expect(describeLiveness(30_000, true, 0, retry).text).toBe(
    "provider error — attempt 3/4 — retrying in 32s — 14m on this call",
  );
});

test("describeLiveness: matches the spec's exact streaming ('in progress') chip string, character for character", () => {
  const retry = retryWait({
    attempt: 3,
    attemptCap: 4,
    delayMs: 32_000,
    errorClass: "server",
    groupElapsedMs: 14 * 60_000,
    inProgress: true,
  });
  expect(describeLiveness(30_000, true, 0, retry).text).toBe(
    "provider error — attempt 3/4 — in progress — 14m on this call",
  );
});

// A hub too old to send AttemptCap sends the zero value; rendering "attempt
// 3/0" would be exactly the dishonest denominator AttemptCap exists to
// eliminate, so the web chip omits the denominator entirely rather than
// falling back to a different budget.
test("describeLiveness: attemptCap 0 renders the attempt number without a denominator", () => {
  const retry = retryWait({ attempt: 3, attemptCap: 0, delayMs: 5_000, errorClass: "server" });
  expect(describeLiveness(30_000, true, 0, retry).text).toBe(
    "provider error — attempt 3 — retrying in 5s — 5s on this call",
  );
});
