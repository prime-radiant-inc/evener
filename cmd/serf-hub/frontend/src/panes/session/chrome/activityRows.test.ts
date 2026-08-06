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
  const status = opts.failed ? "failed" : opts.active ? "running" : "completed";
  return {
    kind: "delegate" as const,
    delegate: {
      delegateId,
      childSessionId: `sess_${delegateId}`,
      childRef: `ref_${delegateId}`,
      turns: [
        {
          jobId: `job_${delegateId}_t1`,
          ownerSessionId: "sess_root",
          ownerRef: "ref_root",
          type: "delegate",
          status,
          terminal: !opts.active,
          background: true,
          hasOutput: false,
          description: `turn of ${delegateId}`,
          startedAt: "2026-08-05T15:00:00Z",
          outputBytes: 0,
        },
      ],
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
