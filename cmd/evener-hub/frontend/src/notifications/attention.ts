import { approvalWaiting, type NavigationSessionSummary, needsResponseRest } from "@evener/appwire-client";
import type { NotificationsLoudScopePref } from "../stores/prefs";

export type AttentionLevel = "working" | "needs_you" | "error" | "idle";

export function levelFromState(state: string): AttentionLevel {
  switch (state) {
    case "active":
      return "working";
    case "awaiting":
    case "warning":
    case "restartRequired":
      return "needs_you";
    case "errored":
      return "error";
    default:
      return "idle";
  }
}

export interface AttentionEntry {
  ref: string;
  title: string;
  level: "needs_you" | "error";
  askPending: boolean;
  approvalPending: boolean;
  // The session rests awaiting with no question: its turn ended asking for a
  // reply (end_reason needs_response). A plain reply rests idle instead.
  needsResponse: boolean;
}

export function snapshotFromNavigation(rows: readonly NavigationSessionSummary[] | null): Map<string, AttentionEntry> {
  const snapshot = new Map<string, AttentionEntry>();
  if (!rows) return snapshot;
  for (const row of rows) {
    const approvalPending = row.approval_pending === true;
    // An approval blocks its turn mid-tool, so the row still reports
    // "active"; like the hub's promotedAttentionLevel, a pending approval
    // makes any row short of a failure need you.
    const level = approvalWaiting(row.state, approvalPending) ? "needs_you" : levelFromState(row.state);
    if (level !== "needs_you" && level !== "error") continue;
    snapshot.set(row.ref, {
      ref: row.ref,
      title: row.title,
      level,
      askPending: row.ask_pending === true,
      approvalPending,
      needsResponse: needsResponseRest(row.state, row.ask_pending === true),
    });
  }
  return snapshot;
}

/** Compatibility seam for the notification owner during the migration. */
export function snapshotFromTree(input: unknown): Map<string, AttentionEntry> {
  if (Array.isArray(input)) return snapshotFromNavigation(input as NavigationSessionSummary[]);
  if (input && typeof input === "object" && "needs_you" in input) {
    const rows = (input as { needs_you?: unknown }).needs_you;
    return Array.isArray(rows) ? snapshotFromNavigation(rows as NavigationSessionSummary[]) : new Map();
  }
  return new Map();
}

function isLoud(entry: AttentionEntry, loudScope: NotificationsLoudScopePref): boolean {
  return (
    loudScope === "all" || entry.askPending || entry.approvalPending || entry.needsResponse || entry.level === "error"
  );
}

// A session fires when it becomes loud: on entering the tier, or when a row
// already in it turns into something the scope alerts for (a warning that
// settles into a reply request). A row that stays loud never re-fires.
export function detectFires(
  prev: Map<string, AttentionEntry>,
  next: Map<string, AttentionEntry>,
  loudScope: NotificationsLoudScopePref,
): AttentionEntry[] {
  const fires: AttentionEntry[] = [];
  for (const [ref, entry] of next) {
    if (!isLoud(entry, loudScope)) continue;
    const before = prev.get(ref);
    if (before && isLoud(before, loudScope)) continue;
    fires.push(entry);
  }
  return fires;
}
