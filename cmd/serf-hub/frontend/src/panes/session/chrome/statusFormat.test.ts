// @vitest-environment node
import { expect, test } from "vitest";
import { formatWorkDuration, modelLabel, totalWorkMillis } from "./statusFormat";

// formatWorkDuration mirrors the daemon's own compactDuration/formatWorkMillis
// convention verbatim (cmd/serf-hub/web_format.go:79-102): a session's
// accumulated work time reads "Ns" under a minute, "Nm" under an hour, "Nh Nm"
// above - the SAME bucketing the rest of the hub's duration displays use, so
// the status row's work-time clock doesn't invent a new convention. This is
// deliberately NOT transcript/messages/format.ts's formatDurationMs: that one
// is scoped to short tool-call durations (sub-second precision, no hour
// bucket) - work time can span a whole session, hours included.
test("clamps a zero or negative duration up to the honest minimum of 1 second", () => {
  expect(formatWorkDuration(0)).toBe("1s");
  expect(formatWorkDuration(-500)).toBe("1s");
});

test("sub-minute durations render as whole seconds, floored", () => {
  expect(formatWorkDuration(500)).toBe("1s"); // floor(0.5s) = 0, clamped to 1
  expect(formatWorkDuration(1000)).toBe("1s");
  expect(formatWorkDuration(59_999)).toBe("59s");
});

test("a duration of exactly one minute rolls over to the minutes bucket, not 60s", () => {
  expect(formatWorkDuration(60_000)).toBe("1m");
});

test("sub-hour durations render as whole minutes, floored", () => {
  expect(formatWorkDuration(90_000)).toBe("1m"); // 1.5 minutes
  expect(formatWorkDuration(59 * 60_000)).toBe("59m");
});

test("a duration of exactly one hour rolls over to the hours bucket", () => {
  expect(formatWorkDuration(60 * 60_000)).toBe("1h 0m");
});

test("hour-scale durations render as 'Nh Nm', with minutes modulo 60", () => {
  expect(formatWorkDuration(60 * 60_000 + 60_000)).toBe("1h 1m");
  expect(formatWorkDuration(2 * 60 * 60_000 + 5 * 60_000)).toBe("2h 5m");
});

// modelLabel: Thread has no separate "model id" field on the cold-hydrate
// wire snapshot (reducer.ts's own hydrateThread comment) - only
// ModelProvider, which the reducer also copies into ThreadModel.model
// verbatim until a live thread/model/changed actually splits them apart.
// Showing "X/X" for that cold-hydrate shape would be a visible artifact of
// an implementation detail leaking into the UI, so this collapses to a
// single label whenever they're still identical.
test("modelLabel collapses to a single label when provider and model are still identical (cold-hydrate shape)", () => {
  expect(modelLabel("anthropic/claude-sonnet-4-5", "anthropic/claude-sonnet-4-5")).toBe("anthropic/claude-sonnet-4-5");
});

test("modelLabel shows provider/model once they've actually been split apart (post thread/model/changed)", () => {
  expect(modelLabel("anthropic", "claude-opus-5")).toBe("anthropic/claude-opus-5");
});

// totalWorkMillis: workMillis is the cumulative time already banked for
// completed turns; activeTurnStartedAt (present only while a turn is
// in-flight - see ThreadModel's own doc comment) adds the still-running
// turn's live elapsed time on top, so the status row's clock keeps ticking
// during an active turn instead of freezing at the last completed total.
test("totalWorkMillis is just workMillis when no turn is currently active", () => {
  expect(totalWorkMillis(90_000, undefined, 1_000_000)).toBe(90_000);
});

test("totalWorkMillis adds the in-flight turn's live elapsed time on top of workMillis", () => {
  const startedAt = new Date(1_000_000).toISOString();
  expect(totalWorkMillis(60_000, startedAt, 1_030_000)).toBe(90_000);
});

// A wire ActiveTurnStartedAt of 0 (the daemon's zero value) does NOT reach the
// reducer omitted - when it arrives present-but-zero, epochMsToISO (reducer.ts:
// 78-80, which guards only `undefined`, never 0) turns it into
// "1970-01-01T00:00:00.000Z". Trusting that as a real turn start makes the
// in-flight term now-minus-epoch - an absurd ~500000h clock. The honest
// reading: an anchor at or before the Unix epoch is the wire's "unset"
// sentinel leaking through, not a turn that has run since 1970, so fall back
// to the banked total. No clock beats an absurd one.
test("ignores an at-epoch activeTurnStartedAt (the epochMsToISO(0) sentinel) instead of clocking now-minus-epoch", () => {
  const epochAnchor = new Date(0).toISOString(); // exactly what epochMsToISO(0) produces
  expect(totalWorkMillis(45_000, epochAnchor, 1_800_000_000_000)).toBe(45_000);
});

test("ignores a pre-epoch (Go zero-time) or unparseable activeTurnStartedAt, falling back to the banked total", () => {
  const preEpoch = new Date(-6_000_000_000).toISOString(); // parses to a negative epoch-ms
  expect(totalWorkMillis(45_000, preEpoch, 1_800_000_000_000)).toBe(45_000);
  expect(totalWorkMillis(45_000, "not-a-timestamp", 1_800_000_000_000)).toBe(45_000);
});
