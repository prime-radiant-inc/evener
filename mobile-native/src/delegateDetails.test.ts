import { expect, test } from "vitest";
import type { ActivityDelegate } from "../../appwire-client/typescript/activityData";
import {
  delegateModel,
  delegatePacket,
  delegateTiming,
} from "./delegateDetails";

function delegate(overrides: Partial<ActivityDelegate> = {}): ActivityDelegate {
  return {
    delegateId: "d",
    childSessionId: "s",
    childRef: "local:s",
    branch: {},
    ...overrides,
  };
}

test("uses live timing for a running delegate and falls back from invalid quiet anchor", () => {
  const result = delegateTiming(
    delegate({
      runStartedAt: "2026-09-07T00:00:00Z",
      latestActivityAt: "not-a-date",
      runningForMs: 10,
      quietForMs: 10,
    }),
    Date.parse("2026-09-07T00:01:00Z"),
  );

  expect(result).toEqual({
    startedAt: "2026-09-07T00:00:00Z",
    durationMs: 60_000,
    quietForMs: 60_000,
    durationLive: true,
    quietLive: true,
    terminal: false,
  });
});

test("freezes a terminal delegate at valid end-start or snapshot duration", () => {
  expect(
    delegateTiming(
      delegate({
        terminal: true,
        runStartedAt: "2026-09-07T00:00:00Z",
        runEndedAt: "2026-09-07T00:00:05Z",
        durationMs: 99_000,
        latestActivityAt: "2026-09-07T00:00:04Z",
      }),
      Date.parse("2026-09-07T01:00:00Z"),
    ),
  ).toMatchObject({
    durationMs: 5_000,
    durationLive: false,
    quietLive: false,
    terminal: true,
  });
  expect(
    delegateTiming(
      delegate({ terminal: true, runStartedAt: "bad", durationMs: 0 }),
      1_000,
    ),
  ).toMatchObject({ durationMs: 0, durationLive: false, terminal: true });
  expect(
    delegateTiming(
      delegate({
        terminal: true,
        runStartedAt: "2026-09-07T00:00:05Z",
        runEndedAt: "2026-09-07T00:00:00Z",
        durationMs: 7_000,
      }),
      Date.parse("2026-09-07T01:00:00Z"),
    ),
  ).toMatchObject({ durationMs: 7_000 });
});

test("does not infer terminal state from a stale outcome on a resumed run", () => {
  const result = delegateTiming(
    delegate({
      outcome: "completed",
      status: "running",
      runStartedAt: "2026-09-07T00:00:00Z",
      runEndedAt: "bad",
    }),
    Date.parse("2026-09-07T00:00:02Z"),
  );

  expect(result.terminal).toBe(false);
  expect(result.durationMs).toBe(2_000);
  expect(result.durationLive).toBe(true);
  expect(result.endedAt).toBeUndefined();
});

test("uses frozen timing values only when live anchors are unavailable", () => {
  const result = delegateTiming(
    delegate({ runningForMs: 0, quietForMs: Number.POSITIVE_INFINITY }),
    1_000,
  );

  expect(result).toMatchObject({
    durationMs: 0,
    durationLive: false,
    terminal: false,
  });
  expect(result.quietForMs).toBeUndefined();
  expect(result.quietLive).toBe(false);
});

test("falls back to safe frozen values for invalid clocks and rejects unsafe snapshots", () => {
  const result = delegateTiming(
    delegate({
      runningForMs: 5,
      quietForMs: 6,
      runStartedAt: "2026-09-07T00:00:00Z",
    }),
    Number.NaN,
  );

  expect(result).toMatchObject({
    durationMs: 5,
    quietForMs: 6,
    durationLive: false,
    quietLive: false,
  });
  expect(
    delegateTiming(
      delegate({ runningForMs: Number.MAX_SAFE_INTEGER + 1, quietForMs: -1 }),
      Number.POSITIVE_INFINITY,
    ),
  ).toMatchObject({ durationMs: undefined, quietForMs: undefined });
  const future = delegateTiming(
    delegate({
      runStartedAt: "2026-09-07T00:01:00Z",
      latestActivityAt: "2026-09-07T00:02:00Z",
    }),
    Date.parse("2026-09-07T00:00:00Z"),
  );
  expect(future).toMatchObject({
    durationMs: 0,
    quietForMs: 0,
    durationLive: true,
    quietLive: true,
  });
});

test("anchors quiet age at the newer valid activity or start timestamp", () => {
  const result = delegateTiming(
    delegate({
      runStartedAt: "2026-09-07T00:01:00Z",
      latestActivityAt: "2026-09-07T00:00:10Z",
    }),
    Date.parse("2026-09-07T00:02:00Z"),
  );

  expect(result.quietForMs).toBe(60_000);
});

test("selects the resolved model and exposes requested model only when distinct", () => {
  expect(
    delegateModel(
      delegate({
        resolvedModel: "resolved",
        model: "model",
        requestedModel: "requested",
        reasoningEffort: " high ",
      }),
    ),
  ).toEqual({
    model: "resolved",
    requestedModel: "requested",
    reasoning: " high ",
  });
  expect(
    delegateModel(delegate({ model: " ", requestedModel: "requested" })),
  ).toEqual({ model: "requested" });
});

test("formats string packets as markdown and JSON values as pretty JSON", () => {
  expect(delegatePacket("")).toEqual({ text: "", format: "markdown" });
  expect(delegatePacket("null", true)).toEqual({
    text: '"null"',
    format: "json",
  });
  expect(delegatePacket(null)).toEqual({ text: "null", format: "json" });
  expect(delegatePacket(false)).toEqual({ text: "false", format: "json" });
  expect(delegatePacket(0)).toEqual({ text: "0", format: "json" });
  expect(delegatePacket({ ok: true })).toEqual({
    text: '{\n  "ok": true\n}',
    format: "json",
  });
  expect(delegatePacket(undefined)).toBeUndefined();
  const structured = delegatePacket({ nested: [1, false, null] }, true);
  expect(structured).toBeDefined();
  if (structured)
    expect(JSON.parse(structured.text)).toEqual({ nested: [1, false, null] });
});

test("omits packets that JSON cannot serialize", () => {
  const circular: Record<string, unknown> = {};
  circular.self = circular;
  expect(delegatePacket(circular)).toBeUndefined();
});
