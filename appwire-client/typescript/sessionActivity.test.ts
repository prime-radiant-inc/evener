import { expect, test } from "vitest";
import {
  decodeActivityRead,
  MAX_INTENT_CODE_POINTS,
  QUIET_AFTER_MS,
  quietState,
  STUCK_AFTER_MS,
} from "./sessionActivity";

const minutes = [0, 0, 1, 4, 9, 2, 0];

test("decodeActivityRead keeps well-formed sessions and drops keys it does not know", () => {
  expect(
    decodeActivityRead({
      sessions: [
        { ref: "local:a", minutes, runningSubagents: 0, quietForMs: 1_000, futureKey: { nested: true } },
        { ref: "paradise-park:b", minutes, runningSubagents: 2 },
      ],
      futureTopLevel: 1,
    }),
  ).toEqual([
    { ref: "local:a", minutes, runningSubagents: 0, quietForMs: 1_000 },
    { ref: "paradise-park:b", minutes, runningSubagents: 2 },
  ]);
});

test("decodeActivityRead drops a malformed session, never the whole read", () => {
  const good = { ref: "local:a", minutes, runningSubagents: 0 };
  expect(
    decodeActivityRead({
      sessions: [
        good,
        { ref: "", minutes, runningSubagents: 0 },
        { ref: "local:b", minutes: [1, -1], runningSubagents: 0 },
        { ref: "local:c", minutes: [], runningSubagents: 0 },
        { ref: "local:d", minutes, runningSubagents: 1.5 },
        { ref: "local:e", minutes, runningSubagents: 0, quietForMs: -5 },
        "not a session",
      ],
    }),
  ).toEqual([good]);
});

test("decodeActivityRead throws on a result that is not a session list", () => {
  for (const value of [null, [], {}, { sessions: "none" }]) {
    expect(() => decodeActivityRead(value)).toThrow("activity read: invalid response");
  }
});

// A Working row says what the session last set out to do, so the read keeps
// each session's latest tool intent, and a session whose daemon has stated none
// carries no key at all rather than an empty one.
test("decodeActivityRead keeps the latest tool intent", () => {
  expect(
    decodeActivityRead({
      sessions: [
        { ref: "local:a", minutes, runningSubagents: 0, latestIntent: "Reading the board's row tests." },
        { ref: "local:b", minutes, runningSubagents: 0 },
      ],
    }),
  ).toEqual([
    { ref: "local:a", minutes, runningSubagents: 0, latestIntent: "Reading the board's row tests." },
    { ref: "local:b", minutes, runningSubagents: 0 },
  ]);
});

// The read keeps when each session last moved, for comparing with its seen
// mark; a session that hasn't moved since its daemon began serving it carries
// no key, and a value that isn't a count drops that row alone.
test("decodeActivityRead keeps when a session last moved", () => {
  expect(
    decodeActivityRead({
      sessions: [
        { ref: "local:a", minutes, runningSubagents: 0, lastMovedAt: 1_800_000_060_000 },
        { ref: "local:b", minutes, runningSubagents: 0 },
        { ref: "local:c", minutes, runningSubagents: 0, lastMovedAt: "soon" },
      ],
    }),
  ).toEqual([
    { ref: "local:a", minutes, runningSubagents: 0, lastMovedAt: 1_800_000_060_000 },
    { ref: "local:b", minutes, runningSubagents: 0 },
  ]);
});

// An intent a hub could not have cut to the wire's bound is a malformed entry
// like any other, and drops its own row alone. The wire's bound counts Unicode
// code points, so an astral character must not smuggle a longer line past it.
test("decodeActivityRead drops a session whose intent is unusable", () => {
  const good = { ref: "local:a", minutes, runningSubagents: 0 };
  const astral = "\u{1F600}";
  expect(
    decodeActivityRead({
      sessions: [
        good,
        { ref: "local:b", minutes, runningSubagents: 0, latestIntent: 42 },
        { ref: "local:c", minutes, runningSubagents: 0, latestIntent: astral.repeat(MAX_INTENT_CODE_POINTS + 1) },
        { ref: "local:d", minutes, runningSubagents: 0, latestIntent: "x".repeat(MAX_INTENT_CODE_POINTS + 1) },
      ],
    }),
  ).toEqual([good]);
});

// The bound itself is not a runaway: an intent of exactly the wire's length,
// astral characters included, is kept.
test("decodeActivityRead keeps an intent of exactly the wire's bound", () => {
  const intent = "\u{1F600}".repeat(MAX_INTENT_CODE_POINTS);
  expect(
    decodeActivityRead({ sessions: [{ ref: "local:a", minutes, runningSubagents: 0, latestIntent: intent }] }),
  ).toEqual([{ ref: "local:a", minutes, runningSubagents: 0, latestIntent: intent }]);
});

test("a working session reads Quiet from three minutes and May be stuck from ten", () => {
  const quietFor = (ms: number) => quietState({ ref: "local:a", minutes, runningSubagents: 0, quietForMs: ms }, 0);
  expect(quietFor(QUIET_AFTER_MS - 1)).toBeNull();
  expect(quietFor(QUIET_AFTER_MS)).toEqual({ state: "quiet", forMs: QUIET_AFTER_MS });
  expect(quietFor(STUCK_AFTER_MS - 1)).toEqual({ state: "quiet", forMs: STUCK_AFTER_MS - 1 });
  expect(quietFor(STUCK_AFTER_MS)).toEqual({ state: "stuck", forMs: STUCK_AFTER_MS });
});

test("the time since the read counts toward the quiet time", () => {
  expect(quietState({ ref: "local:a", minutes, runningSubagents: 0, quietForMs: 2 * 60_000 }, 60_000)).toEqual({
    state: "quiet",
    forMs: QUIET_AFTER_MS,
  });
});

// Jesse's ruling: an agent waiting on subagents is never stuck. The hub sends
// no quiet time then; a session reporting a running subagent reads neither
// label even if one arrived, and a session that is not working has none.
test("a session waiting on subagents is never quiet or stuck", () => {
  expect(quietState({ ref: "local:a", minutes, runningSubagents: 1 }, 15 * 60_000)).toBeNull();
  expect(quietState({ ref: "local:a", minutes, runningSubagents: 1, quietForMs: 15 * 60_000 }, 0)).toBeNull();
  expect(quietState({ ref: "local:a", minutes, runningSubagents: 0 }, 15 * 60_000)).toBeNull();
});
