// railModel is the pure merge the rail renders: the summary's turn tuples and
// job intervals plus the live turns the transcript already holds. The job lane
// and its completeness always come from the summary; live turns never
// fabricate or refresh a job.
import { type TurnModel, turnUsageTokens } from "@evener/appwire-client";
import type { RailJobHistory, RailJobInterval, RailSummary, RailSummaryTurn } from "./railSummary";

/** The summary tuple with its token fields named for the renderer's IN/OUT
 * strata. */
export type RailEvent = Pick<RailSummaryTurn, "startedAt" | "userInput" | "steering" | "error" | "resultBytes"> & {
  inTok: number;
  outTok: number;
};

export type RailJob = RailJobInterval;

export interface RailModel {
  startMs: number;
  endMs: number;
  events: RailEvent[];
  jobs: RailJob[];
  jobLanes: number;
  jobHistory: RailJobHistory;
}

/** The lane's completeness and revision are the hook's state, never inferred
 * from the presence of jobs: an empty lane is not proof of a complete one. */
export function railModelFromSummaryAndTurns(
  summary: RailSummary | null,
  turns: readonly TurnModel[],
  jobHistory: RailJobHistory,
): RailModel {
  const events: RailEvent[] = [];
  const summaryIndexById = new Map<string, number>();
  const summaryIndicesByStart = new Map<number, number[]>();
  for (const turn of summary?.turns ?? []) {
    const index =
      events.push({
        startedAt: turn.startedAt,
        userInput: turn.userInput,
        steering: turn.steering,
        error: turn.error,
        inTok: turn.in,
        outTok: turn.out,
        resultBytes: turn.resultBytes,
      }) - 1;
    // A summary may carry the turn id; when it does not, the start timestamp is
    // the only identity left, and that turn can still be matched.
    if (turn.turnId) summaryIndexById.set(turn.turnId, index);
    else {
      const indices = summaryIndicesByStart.get(turn.startedAt);
      if (indices) indices.push(index);
      else summaryIndicesByStart.set(turn.startedAt, [index]);
    }
  }

  for (const turn of turns) {
    const usage = turnUsageTokens(turn);
    const error = turn.error != null;
    const first = turn.items[0];
    const startedAt = turn.startedAt ? Date.parse(turn.startedAt) : Number.NaN;
    // An id match needs no timestamp: the summary supplies the placement.
    let matched = summaryIndexById.get(turn.id);
    if (matched === undefined) {
      if (!Number.isFinite(startedAt)) continue;
      // The timestamp fallback is one-to-one: a second live turn at the same
      // instant is a distinct turn and must not overwrite the matched event.
      matched = summaryIndicesByStart.get(startedAt)?.shift();
    }
    if (matched !== undefined) {
      // The summary captured this turn earlier. Reconcile it with what the live
      // model knows now (usage, failure), keeping summary-only facts.
      const event = events[matched];
      if (event) {
        if (usage) {
          event.inTok = usage.inputTokens;
          event.outTok = usage.outputTokens;
        }
        event.error = error;
        if (first?.type === "userMessage") event.userInput = true;
        if (first?.type === "steering") event.steering = true;
      }
      continue;
    }
    if (!Number.isFinite(startedAt)) continue;
    events.push({
      startedAt,
      userInput: first?.type === "userMessage",
      steering: first?.type === "steering",
      error,
      inTok: usage?.inputTokens ?? 0,
      outTok: usage?.outputTokens ?? 0,
      resultBytes: 0,
    });
  }
  events.sort((a, b) => a.startedAt - b.startedAt);

  // Copy each interval: the model's jobs are the renderer's to annotate, and
  // the summary they came from is shared by every rail mount through the cache.
  const jobs: RailJob[] = [...(summary?.jobs ?? [])].map((entry) => ({ ...entry })).sort(compareJobs);

  let startMs = Number.POSITIVE_INFINITY;
  let endMs = Number.NEGATIVE_INFINITY;
  for (const event of events) {
    startMs = Math.min(startMs, event.startedAt);
    endMs = Math.max(endMs, event.startedAt);
  }
  for (const entry of jobs) {
    startMs = Math.min(startMs, entry.startedAt);
    endMs = Math.max(endMs, entry.startedAt);
    if (entry.finishedAt !== null) endMs = Math.max(endMs, entry.finishedAt);
  }
  if (!Number.isFinite(startMs) || !Number.isFinite(endMs)) {
    startMs = 0;
    endMs = 0;
  }

  return {
    startMs,
    endMs,
    events,
    jobs,
    jobLanes: widestOverlap(jobs),
    jobHistory,
  };
}

/** The lane timeline's contract: start ascending, then job id. */
function compareJobs(a: RailJob, b: RailJob): number {
  if (a.startedAt !== b.startedAt) return a.startedAt - b.startedAt;
  if (a.jobId === b.jobId) return 0;
  return a.jobId < b.jobId ? -1 : 1;
}

/** How many job lanes the rail needs: the widest overlap of the intervals. A
 * running job's interval never closes, so it keeps its lane to the end. */
function widestOverlap(jobs: readonly RailJob[]): number {
  const points: { at: number; delta: number }[] = [];
  for (const job of jobs) {
    points.push({ at: job.startedAt, delta: 1 });
    if (job.finishedAt !== null) points.push({ at: job.finishedAt, delta: -1 });
  }
  // An interval ending where the next starts does not overlap it.
  points.sort((a, b) => a.at - b.at || a.delta - b.delta);
  let widest = 0;
  let live = 0;
  for (const point of points) {
    live += point.delta;
    if (live > widest) widest = live;
  }
  // A zero-length interval (a job that started and finished in one
  // millisecond) still needs a lane, even though it overlaps nothing.
  return jobs.length > 0 ? Math.max(widest, 1) : 0;
}
