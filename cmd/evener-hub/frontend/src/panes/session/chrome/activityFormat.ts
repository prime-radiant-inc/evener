// Pure formatting helpers for the activity surfaces: the Overview sidebar's
// rows, the session chrome's Overview label, and the transcript's entity
// views. Kept React-free so each is trivially unit-testable (same contract as
// protocol/displayFormat).

import {
  type ActivityUsage,
  formatTokenCount,
  isActivityFailure,
  type SessionActivitySummary,
} from "@evener/appwire-client";

/** "Overview · N active": subagents running at every depth (the subtree
 * count, useSubagentCounts) plus the session's own running jobs, once both
 * are known. */
export function activityActionLabel(
  summary: SessionActivitySummary | null | undefined,
  subagents: SessionActivitySummary["delegates"] | null,
): string {
  return subagents?.known && summary?.jobs.known
    ? `Overview · ${subagents.active + summary.jobs.active} active`
    : "Overview";
}

// formatUsagePair renders a delegate row's token cluster ("↑41.2K ↓6.1K"), or null
// when the daemon sent no usage (old daemon, shell-only work) so the row hides
// the cluster instead of rendering ↑0 ↓0.
export function formatUsagePair(usage: ActivityUsage | undefined): string | null {
  if (!usage) return null;
  const parts = [];
  if (usage.inputTokens !== undefined) parts.push(`↑${formatTokenCount(usage.inputTokens)}`);
  if (usage.outputTokens !== undefined) parts.push(`↓${formatTokenCount(usage.outputTokens)}`);
  return parts.length ? parts.join(" ") : null;
}

// isFailedStatus is the danger set for the status dot: it reads a displayed
// status string, not an entry's verdict. Terminal failure - a settled row's
// glyph - comes from the outcome instead, through jobIsFailed.
export function isFailedStatus(status: string): boolean {
  return isActivityFailure(undefined, status);
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

// The shared package owns the job-status display words; re-export keeps the
// web importers (entity refs, job tools) on their existing path.
export { jobStatusDisplay } from "@evener/appwire-client";
