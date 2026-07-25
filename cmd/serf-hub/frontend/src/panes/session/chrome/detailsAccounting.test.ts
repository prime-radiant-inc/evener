// @vitest-environment node
import { expect, test } from "vitest";
import type { ThreadModel } from "../../../protocol/model";
import { formatTimestamp, sessionTokens } from "./detailsAccounting";

function model(overrides: Partial<ThreadModel> = {}): ThreadModel {
  return { turns: [], usage: null, ...overrides } as unknown as ThreadModel;
}

function turn(inputTokens: number, outputTokens: number) {
  return { id: `t${inputTokens}`, status: "completed", items: [], usage: { inputTokens, outputTokens } };
}

// The thread-level cumulative total is the authoritative figure: the daemon
// accumulated it across the whole session, including turns this client never
// loaded. When it is present it wins outright and the scope is the session.
test("the thread's own cumulative usage is reported as a full-session total", () => {
  const tokens = sessionTokens(model({ usage: { inputTokens: 47_466, outputTokens: 514 }, turns: [turn(1, 1)] }));
  expect(tokens).toEqual({ inputTokens: 47_466, outputTokens: 514, scope: "session" });
});

// A session whose persisted meta carries no cumulative total (a fork child -
// agent/fork.go's writeForkChild never stamps CumulativeUsage) still has real
// per-turn usage in every turn the client loaded, so summing those recovers
// the figure. These are the two turns of one of Jesse's real fork children.
test("absent thread usage falls back to the sum over the turns the client loaded", () => {
  const tokens = sessionTokens(model({ turns: [turn(6961, 73), turn(1276, 47)] }));
  expect(tokens).toEqual({ inputTokens: 8237, outputTokens: 120, scope: "session" });
});

// thread/read windows turns via turnLimit and reports the truncation with
// olderCursor. A sum over a truncated window is NOT the session total, so the
// scope has to say so - never label a partial figure as a full session.
test("a derived sum over a truncated turn window is scoped to the loaded turns only", () => {
  const tokens = sessionTokens(model({ turns: [turn(100, 10)], olderCursor: "cursor_1" }));
  expect(tokens?.scope).toBe("loaded");
});

// With no olderCursor the loaded turns ARE the whole transcript, so the sum
// over them is a genuine full-session total and may be labelled as one.
test("a derived sum over a complete, untruncated transcript is a real session total", () => {
  const tokens = sessionTokens(model({ turns: [turn(100, 10), turn(200, 20)], olderCursor: undefined }));
  expect(tokens).toEqual({ inputTokens: 300, outputTokens: 30, scope: "session" });
});

// Turns with no usage at all (a USER_INPUT or TOOL_RESULTS turn, and every
// turn of a session whose transcript recorded none) contribute nothing. A sum
// of nothing is not "0 tokens" - it is no data, which must render no row.
test("turns carrying no usage sum to no data at all rather than a zero total", () => {
  expect(
    sessionTokens(model({ turns: [{ id: "t", status: "completed", items: [] }] } as Partial<ThreadModel>)),
  ).toBeNull();
  expect(sessionTokens(model())).toBeNull();
});

test("a thread usage object whose counts are all zero is no data, not a zero total", () => {
  expect(sessionTokens(model({ usage: { inputTokens: 0, outputTokens: 0 } }))).toBeNull();
});

test("formatTimestamp renders an ISO instant in the reader's own locale", () => {
  const iso = "2026-07-23T18:02:46.000Z";
  expect(formatTimestamp(iso)).toBe(new Date(iso).toLocaleString());
});

test("formatTimestamp reports an unparseable instant as absent rather than 'Invalid Date'", () => {
  expect(formatTimestamp("not-a-date")).toBeUndefined();
  expect(formatTimestamp(undefined)).toBeUndefined();
});
