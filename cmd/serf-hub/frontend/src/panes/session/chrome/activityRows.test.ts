import { expect, test } from "vitest";
import type { ActivityTree } from "./activityData";
import { buildActivityRows, foldRowID } from "./activityRows";

function shell(jobId: string, terminal: boolean, status = terminal ? "completed" : "running") {
  return {
    kind: "shell" as const,
    job: {
      jobId,
      ownerSessionId: "sess_root",
      ownerRef: "ref_root",
      type: "shell",
      status,
      terminal,
      background: true,
      hasOutput: true,
      description: `job ${jobId}`,
      startedAt: "2026-08-05T15:00:00Z",
      outputBytes: 10,
    },
  };
}

function delegate(delegateId: string, opts: { active?: boolean; failed?: boolean; child?: unknown } = {}) {
  const status = opts.active ? "running" : "idle";
  const outcome = opts.failed ? "failed" : opts.active ? undefined : "completed";
  return {
    kind: "delegate" as const,
    delegate: {
      delegateId,
      ownerSessionId: "sess_root",
      rootSessionId: "sess_root",
      childSessionId: `sess_${delegateId}`,
      childRef: `ref_${delegateId}`,
      transcriptRef: `ref_${delegateId}`,
      type: "delegate",
      lifecycle: opts.active ? "running" : "idle",
      phase: opts.active ? "running" : "idle",
      status,
      outcome,
      projectionRevision: 1,
      terminal: !opts.active,
      resumable: true,
      runStartedAt: "2026-08-05T15:00:00Z",
      runEndedAt: opts.active ? undefined : "2026-08-05T15:01:00Z",
      latestActivityAt: "2026-08-05T15:01:00Z",
      branch: {},
      child: opts.child,
    },
  };
}

function session(entries: unknown[], counts = { active: 0, failed: 0, completed: 0, complete: true }) {
  return {
    kind: "session" as const,
    sessionId: "sess_root",
    ref: "ref_root",
    label: "Root",
    aggregate: "working",
    counts,
    entries,
    branch: {},
  };
}

function tree(entries: unknown[]): ActivityTree {
  return { revision: 1, root: session(entries) as ActivityTree["root"] };
}

test("stable delegate lineage stays one row and nests its ParentDelegateID shell", () => {
  const stable = {
    kind: "delegate" as const,
    delegate: {
      delegateId: "dlg_stable",
      ownerSessionId: "sess_root",
      rootSessionId: "sess_root",
      childSessionId: "sess_child",
      childRef: "local:sess_child",
      transcriptRef: "local:sess_child",
      type: "delegate",
      lifecycle: "active",
      phase: "running",
      status: "running",
      projectionRevision: 3,
      terminal: false,
      resumable: true,
      originTurnId: "turn_1",
      branch: {},
      child: {
        kind: "session" as const,
        sessionId: "sess_child",
        ref: "local:sess_child",
        label: "Child",
        aggregate: "running",
        counts: { active: 1, failed: 0, completed: 0, complete: true },
        entries: [
          {
            kind: "shell" as const,
            job: {
              jobId: "job_shell",
              ownerSessionId: "sess_child",
              ownerRef: "local:sess_child",
              parentDelegateId: "dlg_stable",
              transcriptRef: "job:job_shell",
              type: "shell",
              status: "running",
              terminal: false,
              background: true,
              hasOutput: true,
              description: "run checks",
              startedAt: "2026-08-15T10:00:00Z",
              outputBytes: 1,
            },
          },
        ],
        branch: {},
      },
    },
  };

  const rows = buildActivityRows(tree([stable]) as ActivityTree, new Set());
  expect(rows).toHaveLength(2);
  expect(rows[0]).toMatchObject({ kind: "delegate", id: "delegate:dlg_stable", live: true });
  expect(rows[1]).toMatchObject({
    kind: "job",
    id: "job:job_shell",
    parentID: "delegate:dlg_stable",
    level: 2,
    job: { parentDelegateId: "dlg_stable" },
  });
});

test("live entries render in order; terminal entries fold behind one row", () => {
  const rows = buildActivityRows(
    tree([shell("a", false), shell("b", true), shell("c", true), shell("d", false)]),
    new Set(),
  );
  expect(rows.map((r) => r.id)).toEqual(["job:a", "job:d", "session:sess_root:inactive-fold"]);
  const fold = rows[2];
  expect(fold?.kind === "fold" && fold.inactiveCount).toBe(2);
  // Top-level live rows open their detail strips by default.
  expect(rows[0]).toMatchObject({ defaultDetailOpen: true });
  expect(rows[1]).toMatchObject({ defaultDetailOpen: true });
});

test("fold row counts failures separately", () => {
  const rows = buildActivityRows(tree([shell("x", true, "failed"), shell("y", true)]), new Set());
  const fold = rows.find((r) => r.kind === "fold");
  expect(fold?.kind === "fold" && fold.failedCount).toBe(1);
});

test("fold row counts a terminal delegate outcome when lifecycle status is idle", () => {
  const rows = buildActivityRows(tree([delegate("dlg_failed", { failed: true })]), new Set());
  const fold = rows.find((row) => row.kind === "fold");
  expect(fold?.kind === "fold" && fold.failedCount).toBe(1);
});

test("set membership expands the fold and reveals terminal rows after the fold row", () => {
  const rows = buildActivityRows(
    tree([shell("a", false), shell("b", true)]),
    new Set([foldRowID("session:sess_root")]),
  );
  expect(rows.map((r) => r.id)).toEqual(["job:a", "session:sess_root:inactive-fold", "job:b"]);
  // A row revealed by opening the fold stays collapsed: the fold click means
  // "show the list", not "expand every child".
  expect(rows[2]).toMatchObject({ defaultDetailOpen: false });
});

test("delegate children nest one level deeper under the delegate row", () => {
  const child = session([shell("gc", false)], { active: 1, failed: 0, completed: 0, complete: false });
  const rows = buildActivityRows(tree([delegate("dlg_1", { active: true, child })]), new Set());
  const drow = rows[0];
  const crow = rows[1];
  expect(drow?.kind).toBe("delegate");
  expect(crow).toMatchObject({ kind: "job", level: 2, parentID: "delegate:dlg_1", defaultDetailOpen: false });
});

test("all-terminal delegate folds as one inactive entry", () => {
  const rows = buildActivityRows(tree([delegate("dlg_1", {})]), new Set());
  expect(rows.map((r) => r.id)).toEqual(["session:sess_root:inactive-fold"]);
});

test("no terminal entries renders no fold row", () => {
  const rows = buildActivityRows(tree([shell("a", false)]), new Set());
  expect(rows.every((r) => r.kind !== "fold")).toBe(true);
});
