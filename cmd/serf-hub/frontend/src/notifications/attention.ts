// The pure heart of the notifications engine: deriving per-thread attention
// transitions from successive treeStore snapshots, with no DOM or store
// access of its own. The engine (index.ts) owns the stateful "which snapshot
// was the baseline" bookkeeping; this module just answers "given two
// snapshots, what should fire?".
//
// Data source is treeStore's own `needs_you` tier array — the uncapped,
// top-level, non-archived, non-subagent set of sessions at level
// needs_you/error (cmd/serf-hub/internal/hubcore/tree.go:792-852). That is
// EXACTLY the daemon's tier-eligible attention population (the same
// definition the AttentionSummary counts over, attention.go:27-36), so a
// ref newly present in the tier is precisely a transition INTO the alarming
// set from outside it — the legacy engine's `into && !was` gate
// (notifications.js:264-267), reconstructed from snapshots instead of the
// per-broadcast prevLevel the old wire carried.
import type { NotificationsLoudScopePref } from "../stores/prefs";
import type { TreeResponse } from "../stores/tree";

export type AttentionLevel = "working" | "needs_you" | "error" | "idle";

// Mirrors the daemon's attentionLevel(NormalizeState(status)) exactly
// (attention.go:53-64 over tree.go:236-259): the tree node's `state` is
// already the normalized UI state, so the client re-derives the same level
// the server bucketed the badge counts by.
export function levelFromState(state: string): AttentionLevel {
  switch (state) {
    case "active":
      return "working";
    case "awaiting":
    case "warning":
      return "needs_you";
    case "errored":
      return "error";
    default:
      return "idle";
  }
}

// One thread's presence in the needs_you tier. `level` is only ever
// needs_you or error (the tier holds nothing else); `askPending` and `level`
// together decide loudScope narrowing below.
export interface AttentionEntry {
  ref: string;
  title: string;
  level: "needs_you" | "error";
  askPending: boolean;
}

// Snapshot the needs_you tier, keyed by the qualified `ref` (stable session
// identity). A null tree (nothing loaded yet) is an empty snapshot. The
// level guard is defensive: the tier is built server-side from exactly
// awaiting/warning/errored, so anything else would be a wire contract break.
export function snapshotFromTree(tree: TreeResponse | null): Map<string, AttentionEntry> {
  const snap = new Map<string, AttentionEntry>();
  if (!tree) return snap;
  for (const n of tree.needs_you) {
    const level = levelFromState(n.state);
    if (level !== "needs_you" && level !== "error") continue;
    snap.set(n.ref, { ref: n.ref, title: n.title, level, askPending: n.ask_pending === true });
  }
  return snap;
}

// The refs that just transitioned INTO the alarming set (present in `next`,
// absent from `prev`), narrowed by loudScope: "asks" (the default) fires
// only for an ask_pending transition or an error; "all" fires for every
// qualifying transition (notifications.js:268). error<->needs_you shuffles
// within the tier, and drops out of it, produce nothing — matching the
// legacy's `into && !was` outer gate.
export function detectFires(
  prev: Map<string, AttentionEntry>,
  next: Map<string, AttentionEntry>,
  loudScope: NotificationsLoudScopePref,
): AttentionEntry[] {
  const fires: AttentionEntry[] = [];
  for (const [ref, entry] of next) {
    if (prev.has(ref)) continue;
    const loud = loudScope === "all" || entry.askPending || entry.level === "error";
    if (loud) fires.push(entry);
  }
  return fires;
}
