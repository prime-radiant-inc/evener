// Pure "honest liveness" logic: quiet ~Nm -> "may be stalled", driven by
// gap = now - ThreadModel.lastFrameAt. Display-only - no self-heal/reconnect
// side effect (that's a connection.ts/threads.ts concern, not this pane's),
// no idle animation (Cadence already carries live activity via its trace -
// see widgets/cadence). Session-level thresholds only; the legacy renderer
// also runs a SEPARATE per-subagent-row liveness clock (10s/45s) - that's a
// distinct feature area (T3's subagent module) this deliberately does not
// unify with, per parity doc's own Highlights section.

export type LivenessLevel = "none" | "quiet" | "stalled";

export interface LivenessInfo {
  level: LivenessLevel;
  text: string | null;
}

// docs/web-ui/parity/parity-m4-transcript.md §16.
export const QUIET_THRESHOLD_MS = 20_000;
export const STALL_THRESHOLD_MS = 180_000;

const SECOND_MS = 1_000;
const MINUTE_MS = 60 * SECOND_MS;

/**
 * Bucketed, quantized "calm quiet" phrasing - never an exact rising
 * per-second counter, matching the legacy renderer's own
 * formatLivenessQuiet breakpoints exactly (parity doc §16): <45s -> "~30s",
 * <90s -> "~1m", >=90s -> "~2m". The top bucket covers the ENTIRE 90s-180s
 * span (not just up to some smaller ceiling), so the quiet line never
 * climbs to "~3m" in the moment before crossing into stalled.
 */
export function formatQuietBucket(gapMs: number): string {
  if (gapMs < 45_000) return "~30s";
  if (gapMs < 90_000) return "~1m";
  return "~2m";
}

/**
 * Exact-ish (not bucketed) gap phrasing for the stalled/concern level,
 * matching the legacy renderer's own formatLivenessGap (parity doc §16):
 * under 60s as whole seconds, otherwise whole minutes plus a trailing
 * " Ss" only when the remainder is non-zero.
 */
export function formatExactGap(gapMs: number): string {
  const totalSeconds = Math.floor(gapMs / SECOND_MS);
  if (totalSeconds < 60) return `${totalSeconds}s`;
  const minutes = Math.floor(gapMs / MINUTE_MS);
  const remainderSeconds = totalSeconds - minutes * 60;
  return remainderSeconds === 0 ? `${minutes}m` : `${minutes}m ${remainderSeconds}s`;
}

/**
 * The single entry point LivenessLine.tsx renders from: given the gap since
 * the model's last frame and whether the thread is currently "active"
 * (liveness only ever evaluates while active - any other status always
 * reads as level "none", matching the legacy renderer's own gate), decides
 * the quiet/stalled level and its display text.
 */
export function describeLiveness(gapMs: number, active: boolean): LivenessInfo {
  if (!active || gapMs < QUIET_THRESHOLD_MS) return { level: "none", text: null };
  if (gapMs < STALL_THRESHOLD_MS) return { level: "quiet", text: `Quiet ${formatQuietBucket(gapMs)}` };
  return { level: "stalled", text: `May be stalled — no updates for ${formatExactGap(gapMs)}` };
}
