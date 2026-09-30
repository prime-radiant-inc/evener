// Shared fixtures for the activity-surface tests (statusScope.test.ts,
// StatusBar.test.tsx, ActivitySidebar tests): one session tree at realistic
// shape - root A with an active subagent (B: running job, armed watch, 2/5
// tasks) and an idle one (C), plus 201 more direct children the wire has not
// sent (the page's `remaining`), and an unrelated root D. The wire's real
// shapes: list rows and locations are FLAT; the tree arrives through the
// paged subagents resource (SUBAGENTS_A below). Builders return fresh
// objects per call so tests never share mutable state.

import type { NavigationSessionSummary, NavigationWatchSummary } from "@evener/appwire-client";

export function summaryOf(
  partial: Partial<NavigationSessionSummary> & { ref: string; title: string },
): NavigationSessionSummary {
  return {
    host_id: "local",
    session_id: partial.ref,
    project: "evener",
    state: "idle",
    kind: "session",
    live: true,
    children: [],
    ...partial,
  } as NavigationSessionSummary;
}

export function watchOf(partial: Partial<NavigationWatchSummary>): NavigationWatchSummary {
  return {
    id: "w1",
    source: "self",
    deliveries: 0,
    created_at: "2026-09-29T10:00:00Z",
    active: true,
    ...partial,
  };
}

export function sampleTree() {
  const CHILD_B = summaryOf({
    ref: "local:b",
    title: "B",
    state: "active",
    kind: "subagent",
    running_jobs: [{ job_id: "j1", job_type: "shell", status: "running", command: "go test ./..." }],
    watches: [watchOf({})],
    tasks: { total: 5, done: 2, current: "doing the thing" },
  });
  const CHILD_C = summaryOf({ ref: "local:c", title: "C", state: "idle", kind: "subagent" });
  const ROOT_A = summaryOf({
    ref: "local:a",
    title: "A",
    state: "active",
  });
  const ROOT_D = summaryOf({ ref: "local:d", title: "D", state: "idle" });
  // The subagents resource's page-0 payload for local:a: the direct children
  // (each keeping its own nested subtree) and the wire's remainder.
  const SUBAGENTS_A = { sessions: [CHILD_B, CHILD_C], remaining: 201, truncated: false };
  return { ROOT_A, CHILD_B, CHILD_C, ROOT_D, SUBAGENTS_A };
}
