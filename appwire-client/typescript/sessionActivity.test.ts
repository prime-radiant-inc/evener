import { expect, test } from "vitest";
import { decodeActivityRead, QUIET_AFTER_MS, quietState, STUCK_AFTER_MS } from "./sessionActivity";

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
