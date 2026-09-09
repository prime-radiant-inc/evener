import type { NavigationSessionSummary } from "../protocol/types.gen";
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
}

export function snapshotFromNavigation(rows: readonly NavigationSessionSummary[] | null): Map<string, AttentionEntry> {
  const snapshot = new Map<string, AttentionEntry>();
  if (!rows) return snapshot;
  for (const row of rows) {
    const level = levelFromState(row.state);
    if (level !== "needs_you" && level !== "error") continue;
    snapshot.set(row.ref, { ref: row.ref, title: row.title, level, askPending: row.ask_pending === true });
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

export function detectFires(
  prev: Map<string, AttentionEntry>,
  next: Map<string, AttentionEntry>,
  loudScope: NotificationsLoudScopePref,
): AttentionEntry[] {
  const fires: AttentionEntry[] = [];
  for (const [ref, entry] of next) {
    if (prev.has(ref)) continue;
    if (loudScope === "all" || entry.askPending || entry.level === "error") fires.push(entry);
  }
  return fires;
}
