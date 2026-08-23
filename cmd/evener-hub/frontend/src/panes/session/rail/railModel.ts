// railModel.ts — pure deriver: TurnModel[] (+ RailSummary) → RailModel.
//
// Ported from the reference implementation's liveOf() + buildDerived().
// Live-faithful by construction: the deriver only ever sees revealed data
// (TurnModel[] for live sessions, RailSummary for ended sessions), so it
// cannot produce "future ink" — no event with timestamp > now.
//
// No React, no DOM. Unit-testable.

import type { TurnModel } from "../../../protocol/model";
import type { RailSummaryResponse } from "../../../protocol/types.gen";
import type { RailEvent } from "./axis";

/**
 * RailModel is the derived summary of a session's revealed history, ready for
 * the canvas renderer. It contains only what a live observer at `nowMs` could
 * know.
 */
export interface RailModel {
  /** The session's start timestamp (epoch ms). */
  startMs: number;
  /** The session's end timestamp (epoch ms), or 0 if still live. */
  endMs: number;
  /** All revealed events, sorted by timestamp. */
  events: RailEvent[];
  /** Prompt anchors: events that are user input or user-sourced steering. */
  prompts: RailEvent[];
  /** Jobs with intervals. */
  jobs: RailJob[];
  /** Number of job lanes (for interval partitioning). */
  jobLanes: number;
  /** The live-derived summary at the current instant. */
  live: RailLive;
}

export interface RailJob {
  jobId: string;
  startedAt: number;
  finishedAt: number;
  exitCode?: number;
  status: string;
}

/**
 * RailLive is the computed "what an observer at nowMs can know" summary.
 * Updated every time the deriver runs with a new nowMs.
 */
export interface RailLive {
  /** Number of revealed events. */
  n: number;
  /** Running max input tokens (for log-width strata). */
  maxIn: number;
  /** Running max output tokens. */
  maxOut: number;
  /** Running max result bytes. */
  maxRes: number;
  /** Cumulative burn (Σ totalTokens so far). */
  burn: number;
  /** Cumulative result bytes. */
  resBytes: number;
  /** Jobs started so far. */
  jobsStarted: number;
  /** Jobs currently running (started but not finished). */
  jobsLive: number;
  /** Tools in flight (ASSISTANT → TOOL_RESULTS intervals not yet closed). */
  toolsInFlight: number;
  /** Delegate calls so far. */
  delegateCalls: number;
  /** Prompts shown so far. */
  promptsShown: number;
  /** Top-5 cost turns so far (with rankLive assigned). */
  top5: RailEvent[];
}

/**
 * Build a RailModel from live TurnModel[] data. Used for live sessions where
 * the data arrives via existing push (turn/started, turn/completed) and
 * client-side per-turn accumulation.
 */
export function railModelFromTurns(turns: TurnModel[], nowMs: number): RailModel {
  const events = turnsToRailEvents(turns);
  return buildRailModel(events, [], nowMs);
}

/**
 * Build a RailModel from a RailSummary (ended sessions). The summary carries
 * per-turn tuples and job intervals without turn text.
 */
export function railModelFromSummary(summary: RailSummaryResponse, nowMs: number): RailModel {
  const events = summaryToRailEvents(summary);
  const jobs = (summary.jobs ?? []).map((j) => ({
    jobId: j.jobId,
    startedAt: j.startedAt,
    finishedAt: j.finishedAt ?? 0,
    exitCode: j.exitCode ?? undefined,
    status: j.status,
  }));
  return buildRailModel(events, jobs, nowMs);
}

/**
 * Convert TurnModel[] to RailEvent[], extracting timestamps, token usage, and
 * event kind from each turn.
 */
function turnsToRailEvents(turns: TurnModel[]): RailEvent[] {
  const events: RailEvent[] = [];
  for (let i = 0; i < turns.length; i++) {
    const turn = turns[i];
    if (!turn) continue;
    const ms = turn.startedAt ? Date.parse(turn.startedAt) : 0;
    if (!ms) continue; // skip turns without timestamps
    events.push(turnToRailEvent(turn, i, ms));
  }
  return events;
}

/**
 * Convert a RailSummary to RailEvent[], extracting per-turn tuples.
 */
function summaryToRailEvents(summary: RailSummaryResponse): RailEvent[] {
  return summary.turns.map((t, i) => ({
    pos: i,
    ms: t.startedAt,
    kind: t.userInput
      ? "USER_INPUT"
      : t.steering
        ? "STEERING"
        : t.error
          ? "TURN_FAILURE"
          : t.resultBytes > 0
            ? "TOOL_RESULTS"
            : "ASSISTANT",
    inTok: t.in,
    outTok: t.out,
    resBytes: t.resultBytes,
    error: t.error,
    userInput: t.userInput,
    userSteer: t.steering,
  }));
}

function turnToRailEvent(turn: TurnModel, pos: number, ms: number): RailEvent {
  const usage = turn.usage;
  const kind = inferKind(turn);
  return {
    pos,
    ms,
    kind,
    inTok: usage?.inputTokens ?? 0,
    outTok: usage?.outputTokens ?? 0,
    resBytes: 0,
    error: turn.error != null,
    userInput: kind === "USER_INPUT",
    userSteer: kind === "STEERING",
    calls: extractCalls(turn),
  };
}

function inferKind(turn: TurnModel): RailEvent["kind"] {
  // The wire's Turn doesn't carry an explicit kind field; infer from items.
  const items = turn.items;
  if (!items || items.length === 0) return "ASSISTANT";
  const first = items[0];
  if (!first) return "ASSISTANT";
  switch (first.type) {
    case "userMessage":
      return "USER_INPUT";
    case "steering":
      return "STEERING";
    case "commandExecution":
    case "toolResult":
      return "TOOL_RESULTS";
    default:
      return "ASSISTANT";
  }
}

function extractCalls(_turn: TurnModel): { name: string; error?: boolean }[] | undefined {
  // Tool calls are on assistant turns' items; extraction from the wire items
  // is handled by the reducer already. For the rail's purposes, we detect
  // delegate calls and errors at the event level. Full call extraction is a
  // P1 concern when the rail needs in-flight reconstruction.
  return undefined;
}

/**
 * Build the RailModel from events and jobs, computing the live summary at nowMs.
 * This is the core live-faithful deriver — ported from liveOf() in the reference.
 */
function buildRailModel(events: RailEvent[], jobs: RailJob[], nowMs: number): RailModel {
  const sorted = [...events].sort((a, b) => a.ms - b.ms);
  // Re-index positions after sort
  sorted.forEach((e, i) => {
    e.pos = i;
  });

  const firstEv = sorted[0];
  const lastEv = sorted[sorted.length - 1];
  const startMs = firstEv ? firstEv.ms : 0;
  const endMs = jobs.length > 0 ? jobs.reduce((max, j) => Math.max(max, j.finishedAt), 0) : lastEv ? lastEv.ms : 0;

  const prompts = sorted.filter((e) => e.userInput || e.userSteer);

  // Compute job lanes (interval partitioning)
  const laneEnds: number[] = [];
  let jobLanes = 0;
  for (const j of jobs) {
    let lane = laneEnds.findIndex((t) => t <= j.startedAt);
    if (lane < 0) lane = laneEnds.length;
    laneEnds[lane] = j.finishedAt;
    jobLanes = Math.max(jobLanes, lane + 1);
  }

  const live = liveOf(sorted, jobs, nowMs);

  return { startMs, endMs, events: sorted, prompts, jobs, jobLanes: Math.max(1, jobLanes), live };
}

/**
 * Compute the live summary at nowMs — what a live observer can know.
 * Ported from liveOf() in the reference.
 */
export function liveOf(events: RailEvent[], jobs: RailJob[], nowMs: number): RailLive {
  const n = indexAt(events, nowMs) + 1;
  let maxIn = 1,
    maxOut = 1,
    maxRes = 1,
    burn = 0,
    resBytes = 0;
  let jobsStarted = 0,
    jobsLive = 0,
    toolsInFlight = 0,
    delegateCalls = 0,
    promptsShown = 0;
  const assistants: RailEvent[] = [];

  for (let i = 0; i < n; i++) {
    const e = events[i];
    if (!e) continue;
    if (e.kind === "ASSISTANT") {
      const tot = (e.inTok ?? 0) + (e.outTok ?? 0);
      maxIn = Math.max(maxIn, e.inTok ?? 0);
      maxOut = Math.max(maxOut, e.outTok ?? 0);
      burn += tot;
      assistants.push(e);
    } else if (e.kind === "TOOL_RESULTS") {
      maxRes = Math.max(maxRes, e.resBytes ?? 0);
      resBytes += e.resBytes ?? 0;
    }
    if (e.userInput || e.userSteer) promptsShown++;
  }

  for (const j of jobs) {
    if (j.startedAt > nowMs) break;
    jobsStarted++;
    if (j.finishedAt === 0 || j.finishedAt > nowMs) jobsLive++;
  }

  // Tools in flight: ASSISTANT → TOOL_RESULTS intervals where the assistant
  // has been revealed but the results haven't. For P0 this is approximate;
  // full in-flight reconstruction is P1.
  for (let i = 0; i < n; i++) {
    const ev = events[i];
    const next = events[i + 1];
    if (!ev || !next) continue;
    if (ev.kind === "ASSISTANT" && next.kind !== "TOOL_RESULTS") {
      if (next.ms > nowMs) toolsInFlight++;
    }
  }

  const top5 = [...assistants]
    .filter((e) => (e.inTok ?? 0) + (e.outTok ?? 0) > 0)
    .sort((a, b) => (b.inTok ?? 0) + (b.outTok ?? 0) - (a.inTok ?? 0) - (a.outTok ?? 0))
    .slice(0, 5);
  top5.forEach((e, i) => {
    e._rankLive = i + 1;
  });

  return {
    n,
    maxIn,
    maxOut,
    maxRes,
    burn,
    resBytes,
    jobsStarted,
    jobsLive,
    toolsInFlight,
    delegateCalls,
    promptsShown,
    top5,
  };
}

/** Binary search: last index with ms <= nowMs. */
function indexAt(events: RailEvent[], ms: number): number {
  if (events.length === 0) return -1;
  const first = events[0];
  if (!first || ms < first.ms) return -1;
  let lo = 0,
    hi = events.length - 1;
  while (lo < hi) {
    const mid = Math.ceil((lo + hi) / 2);
    const ev = events[mid];
    if (ev && ev.ms <= ms) lo = mid;
    else hi = mid - 1;
  }
  return lo;
}

// Extend RailEvent with _rankLive for top-5 cost ranking.
declare module "./axis" {
  interface RailEvent {
    _rankLive?: number;
  }
}
