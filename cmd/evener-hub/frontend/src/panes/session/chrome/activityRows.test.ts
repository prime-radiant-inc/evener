import { expect, test } from "vitest";
import type {
  ActivityJob,
  ActivitySessionNode,
  ActivityShellEntry,
  ActivityTree,
} from "../../../protocol/activityData";
import { activityDelegateState, buildActivityRows, foldRowID } from "./activityRows";

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

function delegate(
  delegateId: string,
  opts: { active?: boolean; failed?: boolean; child?: ActivitySessionNode; type?: string; turns?: ActivityJob[] } = {},
) {
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
      type: opts.type ?? "delegate",
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
      turns: opts.turns,
    },
  };
}

function turn(jobId: string, terminal: boolean, status: string, outcome?: string): ActivityJob {
  return {
    jobId,
    ownerSessionId: "sess_root",
    ownerRef: "ref_root",
    type: "turn",
    status,
    outcome,
    terminal,
    background: false,
    hasOutput: true,
    description: jobId,
    startedAt: "2026-08-05T15:00:00Z",
    outputBytes: 0,
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

test("row failure state follows outcome when status is non-failure", () => {
  const failed = shell("outcome-failed", true, "completed") as ActivityShellEntry;
  failed.job.outcome = "failure";
  const rows = buildActivityRows(tree([failed]), new Set());
  expect(rows.find((row) => row.kind === "fold")).toMatchObject({ failedCount: 1 });
});

test("fold row counts a terminal delegate outcome when lifecycle status is idle", () => {
  const rows = buildActivityRows(tree([delegate("dlg_failed", { failed: true })]), new Set());
  const fold = rows.find((row) => row.kind === "fold");
  expect(fold?.kind === "fold" && fold.failedCount).toBe(1);
});

test("turn-based activity derives completion from turns even when delegate fields disagree", () => {
  const rows = buildActivityRows(
    tree([
      delegate("dlg_completed", {
        active: true,
        type: "agent",
        turns: [turn("turn_done", true, "completed")],
      }),
      delegate("dlg_active", {
        type: "agent",
        turns: [turn("turn_live", false, "running")],
      }),
    ]),
    new Set(),
  );
  expect(rows.map((row) => row.id)).toEqual(["delegate:dlg_active", "session:sess_root:inactive-fold"]);
  expect(rows.find((row) => row.kind === "fold")).toMatchObject({ inactiveCount: 1 });
});

test("turn-based failure contributes to the inactive fold and row status", () => {
  const entry = delegate("dlg_failed", {
    type: "agent",
    turns: [turn("turn_failed", true, "completed", "failure")],
  });
  const rows = buildActivityRows(tree([entry]), new Set());
  expect(rows.find((row) => row.kind === "fold")).toMatchObject({ failedCount: 1 });
  expect(activityDelegateState(entry.delegate)).toMatchObject({ active: false, failed: true, status: "failed" });
});

test("turn state gives active work precedence over earlier failures and later completion", () => {
  const state = activityDelegateState(
    delegate("dlg_mixed", {
      active: true,
      type: "agent",
      turns: [turn("turn_failed", true, "completed", "failure"), turn("turn_done", true, "completed")],
    }).delegate,
  );
  expect(state).toMatchObject({ active: false, failed: true, status: "failed" });

  const active = activityDelegateState(
    delegate("dlg_active_last", {
      type: "agent",
      turns: [turn("turn_live", false, "running"), turn("turn_done", true, "completed")],
    }).delegate,
  );
  expect(active).toMatchObject({ active: true, status: "running" });
});

test("turn-based activity remains live while its child session is active", () => {
  const child = session([shell("child", false)], {
    active: 1,
    failed: 0,
    completed: 0,
    complete: false,
  }) as ActivitySessionNode;
  const state = activityDelegateState(delegate("dlg_child", { type: "agent", child }).delegate);
  expect(state).toMatchObject({ active: true });
});

test("stable delegate keeps its resource status while an active child makes the row live", () => {
  const child = session([shell("child", false)], {
    active: 1,
    failed: 0,
    completed: 0,
    complete: true,
  }) as ActivitySessionNode;
  const entry = delegate("dlg_stable_child", { child });
  entry.delegate.terminal = true;
  entry.delegate.outcome = "completed";
  entry.delegate.status = "completed";
  expect(activityDelegateState(entry.delegate)).toMatchObject({ active: true, status: "completed" });
  expect(buildActivityRows(tree([entry]), new Set())[0]).toMatchObject({ kind: "delegate", live: true });
});

test("running stable delegate ignores stale failure outcome", () => {
  const entry = delegate("dlg_resumed", {});
  entry.delegate.terminal = false;
  entry.delegate.status = "running";
  entry.delegate.outcome = "failure";
  expect(activityDelegateState(entry.delegate)).toMatchObject({ active: true, failed: false, status: "running" });

  entry.delegate.terminal = true;
  expect(activityDelegateState(entry.delegate)).toMatchObject({ active: false, failed: true, status: "failure" });

  entry.delegate.terminal = false;
  entry.delegate.status = "error";
  expect(activityDelegateState(entry.delegate)).toMatchObject({ active: true, failed: true, status: "error" });
});

test("empty turn-container rows follow child activity rather than container metadata", () => {
  const active = delegate("dlg_empty_active", { type: "agent" });
  active.delegate.terminal = false;
  active.delegate.status = "running";
  active.delegate.outcome = undefined;
  const failed = delegate("dlg_empty_failed", { type: "agent" });
  failed.delegate.terminal = true;
  failed.delegate.status = "completed";
  failed.delegate.outcome = "failure";
  const rows = buildActivityRows(tree([active, failed]), new Set());
  expect(rows.map((row) => row.id)).toEqual(["session:sess_root:inactive-fold"]);
  expect(rows.find((row) => row.kind === "fold")).toMatchObject({ inactiveCount: 2, failedCount: 0 });
  expect(activityDelegateState(active.delegate)).toMatchObject({ active: false, failed: false, status: "unknown" });
  expect(activityDelegateState(failed.delegate)).toMatchObject({ active: false, failed: false, status: "unknown" });
});

test("child activity takes status precedence over prior own failures and child failures surface", () => {
  const activeChild = session([shell("child", false)], {
    active: 1,
    failed: 0,
    completed: 0,
    complete: false,
  }) as ActivitySessionNode;
  expect(
    activityDelegateState(
      delegate("dlg_active_child", {
        type: "agent",
        turns: [turn("turn_failed", true, "completed", "failure")],
        child: activeChild,
      }).delegate,
    ),
  ).toMatchObject({ active: true, failed: true, status: "working" });

  const failedChild = session([], { active: 0, failed: 1, completed: 0, complete: true }) as ActivitySessionNode;
  expect(
    activityDelegateState(
      delegate("dlg_failed_child", {
        type: "agent",
        turns: [turn("turn_done", true, "completed")],
        child: failedChild,
      }).delegate,
    ),
  ).toMatchObject({ active: false, failed: true, status: "failed" });
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
  const rows = buildActivityRows(
    tree([delegate("dlg_1", { active: true, child: child as ActivitySessionNode })]),
    new Set(),
  );
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
