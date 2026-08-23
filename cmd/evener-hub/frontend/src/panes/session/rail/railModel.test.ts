// @vitest-environment node
import { describe, expect, test } from "vitest";
import type { TurnModel } from "../../../protocol/model";
import type { RailSummaryResponse } from "../../../protocol/types.gen";
import type { RailEvent } from "./axis";
import { liveOf, type RailJob, railModelFromSummary, railModelFromTurns } from "./railModel";

function makeTurn(overrides: Partial<TurnModel> = {}): TurnModel {
  return { id: "turn_1", status: "completed", items: [], ...overrides };
}

function makeTurns(count: number, startMs = 0, stepMs = 1000): TurnModel[] {
  const turns: TurnModel[] = [];
  for (let i = 0; i < count; i++) {
    turns.push(
      makeTurn({
        id: `turn_${i}`,
        startedAt: new Date(startMs + i * stepMs).toISOString(),
        items: [{ type: i === 0 ? "userMessage" : "assistantMessage", id: `item_${i}` } as never],
        usage: i > 0 ? { inputTokens: 100 * i, outputTokens: 50 * i, totalTokens: 150 * i } : undefined,
      }),
    );
  }
  return turns;
}

describe("railModelFromTurns", () => {
  test("empty turns produces empty model", () => {
    const model = railModelFromTurns([], Date.now());
    expect(model.events).toHaveLength(0);
    expect(model.prompts).toHaveLength(0);
    expect(model.live.n).toBe(0);
    expect(model.live.burn).toBe(0);
  });

  test("extracts events with timestamps and token usage", () => {
    const turns = makeTurns(5, 1000, 1000);
    const model = railModelFromTurns(turns, 10000);
    expect(model.events).toHaveLength(5);
    expect(model.events[0]?.ms).toBe(1000);
    expect(model.events[0]?.userInput).toBe(true);
    expect(model.events[1]?.inTok).toBe(100);
    expect(model.events[1]?.outTok).toBe(50);
  });

  test("skips turns without timestamps", () => {
    const turns = [
      makeTurn({ id: "t1", startedAt: "2026-01-01T00:00:00.000Z" }),
      makeTurn({ id: "t2" }), // no startedAt
      makeTurn({ id: "t3", startedAt: "2026-01-01T00:00:02.000Z" }),
    ];
    const model = railModelFromTurns(turns, Date.UTC(2026, 0, 1, 0, 0, 10));
    expect(model.events).toHaveLength(2);
  });
});

describe("liveOf — no-future-ink invariant", () => {
  test("for any nowMs, live.n contains no event with timestamp > nowMs", () => {
    const events: RailEvent[] = [];
    for (let i = 0; i < 20; i++) {
      events.push({ pos: i, ms: i * 1000, kind: "ASSISTANT", inTok: 100, outTok: 50 });
    }
    const jobs: RailJob[] = [];

    // Test at various "now" points
    for (let nowMs = 0; nowMs <= 20000; nowMs += 500) {
      const live = liveOf(events, jobs, nowMs);
      for (let i = 0; i < live.n; i++) {
        const ev = events[i];
        expect(ev).toBeDefined();
        expect(ev!.ms).toBeLessThanOrEqual(nowMs);
      }
    }
  });

  test("burn is cumulative sum of revealed tokens only", () => {
    const events: RailEvent[] = [
      { pos: 0, ms: 0, kind: "ASSISTANT", inTok: 100, outTok: 200 },
      { pos: 1, ms: 1000, kind: "ASSISTANT", inTok: 300, outTok: 400 },
      { pos: 2, ms: 2000, kind: "ASSISTANT", inTok: 500, outTok: 600 },
    ];
    const jobs: RailJob[] = [];

    expect(liveOf(events, jobs, 500).burn).toBe(300); // only first turn revealed
    expect(liveOf(events, jobs, 1500).burn).toBe(1000); // first two turns
    expect(liveOf(events, jobs, 2500).burn).toBe(2100); // all three turns
  });

  test("top5 ranks by cost so far", () => {
    const events: RailEvent[] = [
      { pos: 0, ms: 0, kind: "ASSISTANT", inTok: 100, outTok: 100 },
      { pos: 1, ms: 1000, kind: "ASSISTANT", inTok: 500, outTok: 500 },
      { pos: 2, ms: 2000, kind: "ASSISTANT", inTok: 200, outTok: 200 },
    ];
    const jobs: RailJob[] = [];

    const live = liveOf(events, jobs, 3000);
    expect(live.top5).toHaveLength(3);
    expect(live.top5[0]?._rankLive).toBe(1);
    expect(live.top5[0]?.inTok).toBe(500); // highest cost
  });

  test("promptsShown counts only revealed prompts", () => {
    const events: RailEvent[] = [
      { pos: 0, ms: 0, kind: "USER_INPUT", userInput: true },
      { pos: 1, ms: 1000, kind: "ASSISTANT", inTok: 10, outTok: 10 },
      { pos: 2, ms: 2000, kind: "USER_INPUT", userInput: true },
    ];
    const jobs: RailJob[] = [];

    expect(liveOf(events, jobs, 500).promptsShown).toBe(1);
    expect(liveOf(events, jobs, 1500).promptsShown).toBe(1);
    expect(liveOf(events, jobs, 2500).promptsShown).toBe(2);
  });

  test("jobsLive counts started-but-not-finished jobs", () => {
    const events: RailEvent[] = [];
    const jobs: RailJob[] = [
      { jobId: "j1", startedAt: 500, finishedAt: 2000, status: "completed" },
      { jobId: "j2", startedAt: 1500, finishedAt: 0, status: "running" },
    ];

    expect(liveOf(events, jobs, 1000).jobsStarted).toBe(1);
    expect(liveOf(events, jobs, 1000).jobsLive).toBe(1);
    expect(liveOf(events, jobs, 2500).jobsStarted).toBe(2);
    expect(liveOf(events, jobs, 2500).jobsLive).toBe(1); // j1 finished, j2 still running
  });
});

describe("railModelFromSummary", () => {
  test("converts summary turns to events", () => {
    const summary: RailSummaryResponse = {
      turns: [
        { startedAt: 1000, in: 0, out: 0, resultBytes: 0, error: false, userInput: true, steering: false },
        { startedAt: 2000, in: 100, out: 200, resultBytes: 0, error: false, userInput: false, steering: false },
        { startedAt: 3000, in: 0, out: 0, resultBytes: 500, error: false, userInput: false, steering: false },
      ],
      totalTokens: 300,
      resultBytes: 500,
      startedAt: 1000,
      endedAt: 3000,
    };
    const model = railModelFromSummary(summary, 3000);
    expect(model.events).toHaveLength(3);
    expect(model.events[0]?.userInput).toBe(true);
    expect(model.events[1]?.inTok).toBe(100);
    expect(model.events[2]?.resBytes).toBe(500);
  });
});
