// Pure formatting helpers for the dense activity tree rows. Kept React-free so
// each is trivially unit-testable (same contract as transcript/messages/format).
import { formatTokenCount } from "../transcript/messages/format";
import type { ActivityUsage } from "./activityData";

// formatUsagePair renders a delegate row's token cluster ("↑41k ↓6k"), or null
// when the daemon sent no usage (old daemon, shell-only work) so the row hides
// the cluster instead of rendering ↑0 ↓0.
export function formatUsagePair(usage: ActivityUsage | undefined): string | null {
  if (!usage) return null;
  return `↑${formatTokenCount(usage.inputTokens)} ↓${formatTokenCount(usage.outputTokens)}`;
}

// formatQuietAge buckets a millisecond age into the rail's compact stamps
// ("12s", "1m", "13h", "2d"). Negative input (clock skew) clamps to 0s.
export function formatQuietAge(ms: number): string {
  const seconds = Math.max(0, Math.floor(ms / 1000));
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h`;
  return `${Math.floor(hours / 24)}d`;
}

// quietAnchorMillis is the instant a live row's quiet clock measures from:
// the last observed output when known, else the job's start. 0 when neither
// parses, which renders the dishonest-huge-age case as a visible bug rather
// than a hidden one.
export function quietAnchorMillis(job: { lastOutputAt?: string; startedAt: string }): number {
  const anchor = job.lastOutputAt ?? job.startedAt;
  const parsed = Date.parse(anchor);
  return Number.isNaN(parsed) ? 0 : parsed;
}

// isFailedStatus is the single source for the danger set, shared by row dots,
// fold-row failure counts, and terminal meta text.
export function isFailedStatus(status: string): boolean {
  const normalized = status.trim().toLowerCase();
  return normalized === "failed" || normalized === "exhausted" || normalized === "error";
}

export function jobStatusDotState(
  status: string,
  terminal?: boolean,
): "idle" | "working" | "needs-you" | "failed" | "ended" {
  const normalized = status.trim().toLowerCase();
  if (
    normalized === "running" ||
    normalized === "working" ||
    normalized === "queued" ||
    normalized === "starting" ||
    normalized === "resuming"
  ) {
    return "working";
  }
  if (isFailedStatus(normalized)) return "failed";
  if (normalized === "needs-you" || normalized === "blocked") return "needs-you";
  if (terminal === true || normalized === "completed" || normalized === "cancelled" || normalized === "stopped")
    return "ended";
  return "idle";
}
