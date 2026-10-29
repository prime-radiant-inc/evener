// The model merge is where a refreshed job projection reaches the rail. These
// cases pin the merge itself: live turns join the summary without duplicating
// it, and the job lane plus its completeness come from the summary alone.
import type { TurnModel } from "@evener/appwire-client";
import { describe, expect, test } from "vitest";
import { railModelFromSummaryAndTurns } from "./railModel";
import type { RailJobInterval, RailSummary } from "./railSummary";

function job(overrides: Partial<RailJobInterval> = {}): RailJobInterval {
  return { jobId: "job_a", startedAt: 1_500, finishedAt: null, status: "running", ...overrides };
}

function summary(overrides: Partial<RailSummary> = {}): RailSummary {
  return {
    turns: [
      {
        turnId: "turn_1",
        startedAt: 1_000,
        userInput: true,
        steering: false,
        error: false,
        in: 10,
        out: 5,
        resultBytes: 0,
      },
    ],
    jobs: [job()],
    jobHistory: { status: "complete", revision: 3 },
    ...overrides,
  };
}

function turn(id: string, startedAt: number, usage?: { inputTokens: number; outputTokens: number }): TurnModel {
  return { id, status: "completed", items: [], startedAt: new Date(startedAt).toISOString(), usage };
}

describe("railModelFromSummaryAndTurns", () => {
  test("appends live turns the summary does not hold and never duplicates a shared turn", () => {
    const model = railModelFromSummaryAndTurns(summary(), [turn("turn_1", 1_000), turn("turn_2", 2_500)], {
      status: "complete",
      revision: 3,
    });

    expect(model.events.map((event) => event.startedAt)).toEqual([1_000, 2_500]);
    expect(model.events[0]?.userInput).toBe(true);
  });

  test("distinguishes two turns that started in the same millisecond by their ids", () => {
    const model = railModelFromSummaryAndTurns(summary(), [turn("turn_1", 1_000), turn("turn_2", 1_000)], {
      status: "complete",
      revision: 3,
    });

    expect(model.events).toHaveLength(2);
  });

  test("matches a summary turn without an id to a live turn by its start time", () => {
    const withoutId = summary({
      turns: [{ startedAt: 1_000, userInput: true, steering: false, error: false, in: 10, out: 5, resultBytes: 0 }],
    });
    const model = railModelFromSummaryAndTurns(withoutId, [turn("turn_9", 1_000)], {
      status: "complete",
      revision: 3,
    });

    expect(model.events).toHaveLength(1);
  });

  test("consumes an id-less summary match once so a second live turn at the same instant survives", () => {
    const withoutId = summary({
      turns: [{ startedAt: 1_000, userInput: true, steering: false, error: false, in: 10, out: 5, resultBytes: 0 }],
    });
    const model = railModelFromSummaryAndTurns(withoutId, [turn("turn_a", 1_000), turn("turn_b", 1_000)], {
      status: "complete",
      revision: 3,
    });

    expect(model.events).toHaveLength(2);
  });

  test("matches two id-less summary turns at one instant to two live turns one-to-one", () => {
    const tuple = { startedAt: 1_000, userInput: false, steering: false, error: false, in: 10, out: 5, resultBytes: 0 };
    const withoutIds = summary({ turns: [tuple, { ...tuple }] });
    const model = railModelFromSummaryAndTurns(withoutIds, [turn("turn_a", 1_000), turn("turn_b", 1_000)], {
      status: "complete",
      revision: 3,
    });

    expect(model.events).toHaveLength(2);
  });

  test("reconciles a matching live turn that has no timestamp of its own", () => {
    const live: TurnModel = {
      id: "turn_1",
      status: "completed",
      items: [],
      usage: { inputTokens: 100, outputTokens: 50 },
    };
    const model = railModelFromSummaryAndTurns(summary(), [live], { status: "complete", revision: 3 });

    expect(model.events).toHaveLength(1);
    expect(model.events[0]?.inTok).toBe(100);
  });

  test("reconciles a summary-captured turn with its live completion", () => {
    const live: TurnModel = {
      id: "turn_1",
      status: "completed",
      items: [],
      startedAt: new Date(1_000).toISOString(),
      usage: { inputTokens: 100, outputTokens: 50 },
      error: { message: "failed" },
    };
    const model = railModelFromSummaryAndTurns(summary(), [live], { status: "complete", revision: 3 });

    expect(model.events).toHaveLength(1);
    expect(model.events[0]).toMatchObject({ inTok: 100, outTok: 50, error: true, resultBytes: 0 });
  });

  test("uses the refreshed summary's jobs, so a finished job stops reading as running", () => {
    const refreshed = summary({
      jobs: [job({ finishedAt: 4_000, exitCode: 0, status: "finished" }), job({ jobId: "job_b", startedAt: 5_000 })],
      jobHistory: { status: "complete", revision: 4 },
    });
    const model = railModelFromSummaryAndTurns(refreshed, [turn("turn_1", 1_000)], {
      status: "complete",
      revision: 4,
    });

    expect(model.jobs.map((entry) => [entry.jobId, entry.finishedAt])).toEqual([
      ["job_a", 4_000],
      ["job_b", null],
    ]);
    expect(model.jobHistory.revision).toBe(4);
  });

  test("carries an incomplete job lane through the model instead of reporting it complete", () => {
    const degraded = { status: "degraded", revision: 9 } as const;
    const model = railModelFromSummaryAndTurns(summary(), [], degraded);
    expect(model.jobHistory).toEqual(degraded);
  });

  test("an incomplete lane stays incomplete through the merge", () => {
    const incomplete = { status: "incomplete", revision: null } as const;
    const model = railModelFromSummaryAndTurns(summary(), [], incomplete);
    expect(model.jobHistory.status).toBe("incomplete");
  });

  test("counts the widest overlap of job intervals as the lane count", () => {
    const model = railModelFromSummaryAndTurns(
      summary({
        jobs: [
          job({ jobId: "a", startedAt: 0, finishedAt: 1_000 }),
          job({ jobId: "b", startedAt: 500, finishedAt: 1_500 }),
          job({ jobId: "c", startedAt: 2_000, finishedAt: 2_500 }),
        ],
      }),
      [],
      { status: "complete", revision: 3 },
    );
    expect(model.jobLanes).toBe(2);
  });

  test("intervals that touch end to start do not share a lane", () => {
    const model = railModelFromSummaryAndTurns(
      summary({
        jobs: [
          job({ jobId: "a", startedAt: 0, finishedAt: 1_000 }),
          job({ jobId: "b", startedAt: 1_000, finishedAt: 2_000 }),
        ],
      }),
      [],
      { status: "complete", revision: 3 },
    );
    expect(model.jobLanes).toBe(1);
  });

  test("the span follows the job intervals as well as the turns", () => {
    const model = railModelFromSummaryAndTurns(summary({ jobs: [job({ startedAt: 500, finishedAt: 4_000 })] }), [], {
      status: "complete",
      revision: 3,
    });
    expect(model.startMs).toBe(500);
    expect(model.endMs).toBe(4_000);
  });

  test("an empty summary and no turns invent no ink and claim no complete history", () => {
    const model = railModelFromSummaryAndTurns(null, [], { status: "fatal", revision: null });
    expect(model.events).toEqual([]);
    expect(model.jobs).toEqual([]);
    expect(model.startMs).toBe(0);
    expect(model.endMs).toBe(0);
    expect(model.jobHistory.status).toBe("fatal");
  });

  test("live turns still render when the summary is absent, with the hook's lane state", () => {
    const model = railModelFromSummaryAndTurns(null, [turn("turn_1", 1_000)], { status: "fatal", revision: null });
    expect(model.events).toHaveLength(1);
    expect(model.jobs).toEqual([]);
    expect(model.jobHistory.status).toBe("fatal");
  });

  test("sorts the lane's jobs by start and then job id", () => {
    const model = railModelFromSummaryAndTurns(
      summary({
        jobs: [
          job({ jobId: "b", startedAt: 2_000, finishedAt: 3_000 }),
          job({ jobId: "a", startedAt: 2_000, finishedAt: 3_000 }),
          job({ jobId: "c", startedAt: 1_000, finishedAt: 1_500 }),
        ],
      }),
      [],
      { status: "complete", revision: 3 },
    );
    expect(model.jobs.map((entry) => entry.jobId)).toEqual(["c", "a", "b"]);
  });
});
