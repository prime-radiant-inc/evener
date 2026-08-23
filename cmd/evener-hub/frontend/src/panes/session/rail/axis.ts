// axis.ts — pure axis math for the Session Rail, ported from the reference
// implementation's verified math (proposals/transcript-viz/combined/index.html).
//
// The rail's core principle is live-faithfulness: the axis spans
// [session start, max(now, start+10min)] and re-scales continuously as now
// advances. Nothing is drawn before its time. Turn-index mode normalizes by
// turns-so-far. This module owns the mapping between timestamps / turn
// indices and pixel y-coordinates, plus tick generation.
//
// All functions are pure: no React, no DOM, no side effects. Unit-testable.

/** Clamp v to [a, b]. */
function clamp(v: number, a: number, b: number): number {
  return Math.max(a, Math.min(b, v));
}

/** 10-minute minimum window — the live time axis never zooms closer than this. */
export const MIN_WINDOW_MS = 10 * 60 * 1000;

/** 10-minute gap threshold — idle voids only hatch after this much real silence. */
export const GAP_MIN_MS = 10 * 60 * 1000;

/** 60-second hysteresis for comprehension recency reordering. */
export const REORDER_MARGIN_MS = 60 * 1000;

/** Row height for the transcript's fixed-height rows. */
export const ROW_H = 29;

/**
 * Axis kind: "time" (true elapsed time), "turn" (turn index), or "shared"
 * (comprehension mode's shared elapsed-since-start clock).
 */
export type AxisKind = "time" | "turn" | "shared";

/**
 * AxisParams — the resolved axis parameters for one view.
 * - time: `end` = max(now, start + MIN_WINDOW_MS) — the axis end.
 * - turn: `denom` = max(1, turns-so-far - 1) — the denominator for index normalization.
 * - shared: `D` = max(MIN_WINDOW_S, sharedElapsedS) — the shared clock's duration.
 */
export interface AxisParams {
  /** time axis only: the axis end (epoch ms). */
  end?: number;
  /** turn axis only: the denominator for index normalization. */
  denom?: number;
  /** shared axis only: the clock duration in seconds. */
  D?: number;
}

/**
 * RailView is the resolved view of a session at a given instant: the axis
 * kind, the "now" timestamp (epoch ms), and the live-derived summary
 * (RailLive). The axis math functions take a RailView plus a pixel height H
 * and return y-coordinates.
 */
export interface RailView {
  kind: AxisKind;
  nowMs: number;
  startMs: number;
  ap: AxisParams;
}

/**
 * Map a timestamp (epoch ms) to a y-pixel within height H, clamped to [0, H].
 * - time: fraction of [start, end].
 * - turn: fractional index (via the revealed turns' timestamps) normalized by denom.
 * - shared: fraction of [0, D seconds].
 */
export function vY(V: RailView, ms: number, H: number, events: RailEvent[]): number {
  const { kind, startMs, ap } = V;
  if (kind === "turn") {
    const f = fractionalIndexAtRev(events, ms, events.length) / (ap.denom ?? 1);
    return clamp(f, 0, 1) * H;
  }
  if (kind === "shared") {
    return clamp((ms - startMs) / 1000 / (ap.D ?? 1), 0, 1) * H;
  }
  const end = ap.end ?? startMs + MIN_WINDOW_MS;
  return clamp((ms - startMs) / (end - startMs), 0, 1) * H;
}

/**
 * Map a RailEvent to a y-pixel. In turn mode, uses the event's position index;
 * otherwise delegates to vY with the event's timestamp.
 */
export function vYev(V: RailView, ev: RailEvent, H: number, events: RailEvent[]): number {
  if (V.kind === "turn") {
    return clamp(ev.pos / (V.ap.denom ?? 1), 0, 1) * H;
  }
  return vY(V, ev.ms, H, events);
}

/**
 * Inverse of vY: map a y-pixel back to a timestamp (epoch ms).
 */
export function vMsY(V: RailView, y: number, H: number, events: RailEvent[]): number {
  const { kind, startMs, ap } = V;
  const f = clamp(y / H, 0, 1);
  if (kind === "turn") {
    const denom = ap.denom ?? 1;
    const t = f * denom;
    const i = clamp(Math.floor(t), 0, events.length - 1);
    const n = Math.min(i + 1, events.length - 1);
    const p = clamp(t - i, 0, 1);
    const evI = events[i];
    const evN = events[n];
    if (!evI || !evN) return startMs;
    return evI.ms + (evN.ms - evI.ms) * p;
  }
  if (kind === "shared") {
    return startMs + f * (ap.D ?? 1) * 1000;
  }
  const end = ap.end ?? startMs + MIN_WINDOW_MS;
  return startMs + f * (end - startMs);
}

/**
 * Map a y-pixel to a fractional turn index (inverse of vYidx in turn mode,
 * fractional index lookup in time/shared mode).
 */
export function vIdxY(V: RailView, y: number, H: number, events: RailEvent[]): number {
  if (V.kind === "turn") {
    return clamp(y / H, 0, 1) * (V.ap.denom ?? 1);
  }
  return fractionalIndexAtRev(events, vMsY(V, y, H, events), events.length);
}

/**
 * Map a turn index to a y-pixel.
 */
export function vYidx(V: RailView, i: number, H: number, events: RailEvent[]): number {
  const { kind, ap } = V;
  i = clamp(i, 0, Math.max(0, events.length - 1));
  if (kind === "turn") {
    return events.length <= 1 ? 0 : (i / (ap.denom ?? 1)) * H;
  }
  const j = Math.floor(i);
  const n = Math.min(j + 1, events.length - 1);
  const p = i - j;
  const evJ = events[j];
  const evN = events[n];
  if (!evJ || !evN) return 0;
  const ms = evJ.ms + (evN.ms - evJ.ms) * p;
  return vY(V, ms, H, events);
}

/**
 * Binary search: the index of the last event with timestamp <= ms.
 * Returns -1 if ms is before the first event.
 */
export function indexAt(events: RailEvent[], ms: number): number {
  if (events.length === 0) return -1;
  const first = events[0];
  if (!first || ms < first.ms) return -1;
  let lo = 0;
  let hi = events.length - 1;
  while (lo < hi) {
    const mid = Math.ceil((lo + hi) / 2);
    const ev = events[mid];
    if (ev && ev.ms <= ms) lo = mid;
    else hi = mid - 1;
  }
  return lo;
}

/**
 * Fractional index at a timestamp, restricted to the first `revN` revealed
 * events. Returns a fractional index (e.g., 3.5 = halfway between events 3 and 4).
 */
export function fractionalIndexAtRev(events: RailEvent[], ms: number, revN: number): number {
  if (revN <= 1) return 0;
  const first = events[0];
  const last = events[revN - 1];
  if (!first || ms <= first.ms) return 0;
  if (!last || ms >= last.ms) return revN - 1;
  let lo = 0;
  let hi = revN - 1;
  while (lo < hi) {
    const mid = Math.ceil((lo + hi) / 2);
    const ev = events[mid];
    if (ev && ev.ms <= ms) lo = mid;
    else hi = mid - 1;
  }
  const i = lo;
  const n = Math.min(i + 1, revN - 1);
  const evI = events[i];
  const evN = events[n];
  if (!evI || !evN) return i;
  const den = evN.ms - evI.ms;
  return i + (den ? (ms - evI.ms) / den : 0);
}

/**
 * Resolve axis parameters for a given kind and "now" timestamp.
 */
export function makeAxisParams(
  kind: AxisKind,
  startMs: number,
  nowMs: number,
  revN: number,
  sharedElapsedS?: number,
): AxisParams {
  if (kind === "turn") return { denom: Math.max(1, revN - 1) };
  if (kind === "shared") return { D: Math.max(MIN_WINDOW_MS / 1000, sharedElapsedS ?? 0) };
  return { end: Math.max(nowMs, startMs + MIN_WINDOW_MS) };
}

/**
 * Generate axis tick positions and labels for the time axis.
 * Ticks are at round absolute hours (UTC), so labels never jump while the scale moves.
 */
export function timeTicks(
  startMs: number,
  endMs: number,
  H: number,
  vYFn: (ms: number) => number,
): { y: number; label: string }[] {
  const winMs = endMs - startMs;
  const stepMs = (winMs <= 8 * 3600_000 ? 1 : winMs <= 26 * 3600_000 ? 2 : 6) * 3600_000;
  const ticks: { y: number; label: string }[] = [];
  // Start tick
  ticks.push({ y: 0, label: utcLabel(startMs) });
  for (let ms = Math.ceil(startMs / stepMs) * stepMs; ms <= endMs; ms += stepMs) {
    const y = vYFn(ms);
    if (y > 10) {
      ticks.push({ y: clamp(y, 11, H - 5), label: utcLabel(ms) });
    }
  }
  return ticks;
}

/**
 * Generate axis tick positions and labels for the turn-index axis.
 */
export function turnTicks(revN: number, H: number): { y: number; label: string }[] {
  const max = revN - 1;
  if (max < 1) return [];
  const step = Math.max(1, Math.ceil(max / 12 / 25) * 25);
  const denom = Math.max(1, max);
  const ticks: { y: number; label: string }[] = [];
  for (let i = 0; i <= max; i += step) {
    const y = (i / denom) * H;
    ticks.push({ y: clamp(y, 5, H - 5), label: `#${i}` });
  }
  return ticks;
}

/**
 * Generate axis ticks for the shared elapsed-since-start clock.
 */
export function sharedTicks(D: number, H: number, vYFn: (s: number) => number): { y: number; label: string }[] {
  const stepS = D <= 10 * 3600 ? 3600 : 7200;
  const ticks: { y: number; label: string }[] = [{ y: 0, label: "+0h" }];
  for (let s = stepS; s <= D; s += stepS) {
    const y = vYFn(s);
    ticks.push({ y: clamp(y, 11, H - 5), label: `+${s / 3600}h` });
  }
  return ticks;
}

/** Format an epoch ms as HH:MM UTC. */
export function utcLabel(ms: number): string {
  const d = new Date(ms);
  return `${pad2(d.getUTCHours())}:${pad2(d.getUTCMinutes())}`;
}

/** Format an epoch ms as HH:MM:SS UTC. */
export function utcLabelSec(ms: number): string {
  const d = new Date(ms);
  return `${pad2(d.getUTCHours())}:${pad2(d.getUTCMinutes())}:${pad2(d.getUTCSeconds())}`;
}

/** Format a duration in ms as "Hh MMm" or "MMm" or "M:SS". */
export function durFmt(ms: number, sec = false): string {
  const t = Math.max(0, Math.round(ms / 1000));
  const h = Math.floor(t / 3600);
  const m = Math.floor((t % 3600) / 60);
  const s = t % 60;
  return h ? `${h}h ${pad2(m)}m` : sec ? `${m}:${pad2(s)}` : `${m}m`;
}

/** Format elapsed seconds as "H:MM". */
export function fmtElapsed(s: number): string {
  s = Math.max(0, Math.round(s));
  return `${Math.floor(s / 3600)}:${pad2(Math.floor(s / 60) % 60)}`;
}

// --- types shared with railModel ---

export interface RailEvent {
  /** Position index (0-based) in the session's event stream. */
  pos: number;
  /** Timestamp (epoch ms). */
  ms: number;
  /** Event kind — mirrors the reference's kind strings. */
  kind: RailEventKind;
  /** Input tokens (assistant turns only). */
  inTok?: number;
  /** Output tokens (assistant turns only). */
  outTok?: number;
  /** Result bytes (tool-results turns only). */
  resBytes?: number;
  /** True if this turn has an error. */
  error?: boolean;
  /** True if this is a user-input prompt. */
  userInput?: boolean;
  /** True if this is a user-sourced steering prompt. */
  userSteer?: boolean;
  /** Tool calls on this turn (for in-flight and delegate detection). */
  calls?: { name: string; error?: boolean }[];
}

export type RailEventKind =
  | "HOOK_COMPLETED"
  | "ENVIRONMENT"
  | "USER_INPUT"
  | "STEERING"
  | "ASSISTANT"
  | "TOOL_RESULTS"
  | "ATTENTION_RESOLUTION"
  | "TURN_FAILURE";

function pad2(n: number): string {
  return String(n).padStart(2, "0");
}
