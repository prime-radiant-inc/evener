import { describe, expect, test } from "vitest";
import {
  isFreshActivityRead,
  RAIL_ACTIVITY_STALE_AFTER_MS,
  type RailActivityRead,
  workingStatus,
} from "./railActivity";
import type { RailSession } from "./railNodes";

function session(overrides: Partial<RailSession> = {}): RailSession {
  return {
    row_id: "project:p1:local:a",
    ref: "local:a",
    host_id: "local",
    session_id: "a",
    title: "Fix flaky test",
    project: "Proj",
    state: "active",
    kind: "session",
    live: true,
    children: [],
    ...overrides,
  };
}

function read(activity: Partial<RailActivityRead["activity"]>, msSinceRead = 0): RailActivityRead {
  return {
    activity: {
      ref: "local:a",
      minutes: [0, 0, 0, 0, 0, 0, 0],
      runningSubagents: 0,
      ...activity,
    },
    msSinceRead,
  };
}

describe("workingStatus", () => {
  test("running subagents outrank the latest intent", () => {
    expect(workingStatus(session(), read({ runningSubagents: 2, latestIntent: "Reading the row tests." })).kind).toBe(
      "subagents",
    );
  });

  test("a quiet session reads quiet before its intent", () => {
    expect(workingStatus(session(), read({ quietForMs: 4 * 60_000, latestIntent: "Reading." })).kind).toBe("quiet");
  });

  test("a stuck session reads stuck before its intent", () => {
    expect(workingStatus(session(), read({ quietForMs: 11 * 60_000, latestIntent: "Reading." })).kind).toBe("stuck");
  });

  test("the latest intent is the status when nothing louder applies", () => {
    expect(workingStatus(session(), read({ latestIntent: "Reading the board's row tests." })).kind).toBe("intent");
  });

  test("a running job is the fallback before the bare state word", () => {
    expect(workingStatus(session({ running_job_command: "go test ./..." }), null).kind).toBe("job");
  });

  test("a running job's own intent is its status text, not the command", () => {
    expect(
      workingStatus(
        session({ running_job_command: "go test ./agent/...", running_job_intent: "Checking the rows." }),
        null,
      ),
    ).toEqual({ kind: "job", text: "Checking the rows." });
  });

  test("a running job with no stated intent keeps the command", () => {
    expect(workingStatus(session({ running_job_command: "go test ./agent/..." }), null)).toEqual({
      kind: "job",
      text: "Running go test ./agent/...",
    });
  });

  test("an activity read's latest intent still leads a running job's own", () => {
    expect(
      workingStatus(session({ running_job_intent: "Checking the rows." }), read({ latestIntent: "Fixing it." })),
    ).toEqual({ kind: "intent", text: "Fixing it." });
  });

  test("a fresh read's zero subagent count answers outright, ignoring the row's tally", () => {
    expect(
      workingStatus(session({ subagents: { running: 3, failed: 0, done: 0 } }), read({ latestIntent: "Reading." }))
        .kind,
    ).toBe("intent");
  });

  test("without a read, the row's tally does not invent a subagent line", () => {
    expect(workingStatus(session({ subagents: { running: 1, failed: 0, done: 0 } }), null).kind).toBe("state");
  });

  test("with no read, intent, or job, the bare state word remains", () => {
    expect(workingStatus(session(), null).kind).toBe("state");
  });
});

describe("isFreshActivityRead", () => {
  test("a read inside two poll intervals is fresh, older is not", () => {
    expect(isFreshActivityRead(0)).toBe(true);
    expect(isFreshActivityRead(RAIL_ACTIVITY_STALE_AFTER_MS - 1)).toBe(true);
    expect(isFreshActivityRead(RAIL_ACTIVITY_STALE_AFTER_MS)).toBe(false);
  });
});
