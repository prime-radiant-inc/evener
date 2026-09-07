import { expect, test } from "vitest";
import {
  type ActivityDelegateEntry,
  type ActivityEntry,
  type ActivitySessionNode,
  type ActivityShellEntry,
  type ActivityTree,
  activityNodeID,
} from "../../../protocol/activityData";
import { graftContinuationTree } from "../../../protocol/activityMerge";

const shell = (jobId: string, outputBytes = 0): ActivityShellEntry => ({
  kind: "shell",
  job: {
    jobId,
    ownerSessionId: "root",
    ownerRef: "local:root",
    type: "shell",
    status: "completed",
    terminal: true,
    background: false,
    hasOutput: true,
    description: jobId,
    startedAt: "2026-09-07T00:00:00Z",
    outputBytes,
  },
});
const session = (sessionId: string, entries: ActivityEntry[], continuation?: string): ActivitySessionNode => ({
  kind: "session",
  sessionId,
  ref: `local:${sessionId}`,
  label: sessionId,
  aggregate: continuation ? "unavailable" : "ended",
  counts: { active: 0, failed: 0, completed: entries.length, complete: !continuation },
  entries,
  branch: continuation ? { truncated: true, continuation } : {},
});
const tree = (entries: ActivityEntry[], continuation?: string): ActivityTree => ({
  revision: 4,
  root: session("root", entries, continuation),
});
const delegate = (child?: ActivitySessionNode, revision = 2): ActivityDelegateEntry => ({
  kind: "delegate",
  delegate: {
    delegateId: "delegate",
    childSessionId: "child",
    childRef: "local:child",
    projectionRevision: revision,
    type: "delegate",
    terminal: revision === 2,
    outcome: revision === 2 ? "completed" : undefined,
    status: revision === 2 ? "completed" : "running",
    child,
    branch: child ? {} : { truncated: true, continuation: "child-page" },
  },
});
const ids = (node: ActivitySessionNode) => node.entries.map(activityNodeID);

test("root continuation retains its prefix and deduplicates overlapping entries", () => {
  const current = tree([shell("a"), shell("b")], "next");
  const patch = tree([shell("b", 20), shell("c")]);
  const before = structuredClone(current);
  const result = graftContinuationTree(current, patch, "session:root");
  expect(ids(result.root)).toEqual(["job:a", "job:b", "job:c"]);
  expect(result.root.entries[1]).toEqual(shell("b", 20));
  expect(result.root.branch).toEqual({});
  expect(result.root.counts).toEqual({ active: 0, failed: 0, completed: 3, complete: true });
  expect(result.root.aggregate).toBe("ended");
  expect(current).toEqual(before);
  result.root.entries.splice(0, 1);
  expect(ids(current.root)).toEqual(["job:a", "job:b"]);
});

test("nested session continuation keeps earlier jobs and unrelated root siblings", () => {
  const current = tree([shell("sibling"), delegate(session("child", [shell("a")], "next"))]);
  const patch = tree([delegate(session("child", [shell("b")]))]);
  const result = graftContinuationTree(current, patch, "session:child");
  expect(ids(result.root)).toEqual(["job:sibling", "delegate:delegate"]);
  const entry = result.root.entries[1];
  if (entry?.kind !== "delegate" || !entry.delegate.child) throw new Error("missing child");
  expect(ids(entry.delegate.child)).toEqual(["job:a", "job:b"]);
  expect(entry.delegate.child.branch).toEqual({});
  expect(entry.delegate.child.counts).toEqual({ active: 0, failed: 0, completed: 2, complete: true });
  expect(result.root.counts).toEqual({ active: 0, failed: 0, completed: 4, complete: true });
});

test("delegate continuation expands child data at an unchanged projection revision", () => {
  const current = tree([delegate()]);
  const patch = tree([delegate(session("child", [shell("a")]))]);
  const result = graftContinuationTree(current, patch, "delegate:delegate");
  const entry = result.root.entries[0];
  if (entry?.kind !== "delegate" || !entry.delegate.child) throw new Error("missing child");
  expect(ids(entry.delegate.child)).toEqual(["job:a"]);
  expect(entry.delegate.branch).toEqual({});
  expect(entry.delegate.projectionRevision).toBe(2);
  expect(entry.delegate.status).toBe("completed");
});

test("continuation data does not regress a newer delegate projection", () => {
  const current = tree([delegate(session("child", [shell("a")], "next"), 2)]);
  const patch = tree([delegate(session("child", [shell("b")]), 1)]);
  const result = graftContinuationTree(current, patch, "session:child");
  const entry = result.root.entries[0];
  if (entry?.kind !== "delegate" || !entry.delegate.child) throw new Error("missing child");
  expect(ids(entry.delegate.child)).toEqual(["job:a", "job:b"]);
  expect(entry.delegate.projectionRevision).toBe(2);
  expect(entry.delegate.status).toBe("completed");
});

test("merged summaries count failures and keep coverage separate from running state", () => {
  const failed = shell("failed");
  failed.job.status = "failed";
  failed.job.outcome = "failure";
  const running = shell("running");
  running.job.status = "running";
  running.job.terminal = false;
  const exhausted = delegate(session("child", [failed]));
  if (exhausted.delegate.child)
    exhausted.delegate.child.counts = { active: 0, failed: 1, completed: 0, complete: true };
  exhausted.delegate.outcome = "exhausted";
  const current = tree([shell("completed")], "next");
  const result = graftContinuationTree(current, tree([running, exhausted]), "session:root");
  expect(result.root.counts).toEqual({ active: 1, failed: 2, completed: 1, complete: true });
  expect(result.root.aggregate).toBe("working");
  running.job.terminal = true;
  const settled = graftContinuationTree(current, tree([running, exhausted]), "session:root");
  expect(settled.root.aggregate).toBe("failed");
});

test("partial descendant coverage stays incomplete until its last page loads", () => {
  const current = tree([shell("a")], "root-next");
  const partial = graftContinuationTree(
    current,
    tree([delegate(session("child", [shell("b")], "child-next"))]),
    "session:root",
  );
  expect(partial.root.counts).toEqual({ active: 0, failed: 0, completed: 3, complete: false });
  expect(partial.root.aggregate).toBe("unavailable");
  const final = graftContinuationTree(partial, tree([delegate(session("child", [shell("c")]))]), "session:child");
  expect(final.root.counts).toEqual({ active: 0, failed: 0, completed: 4, complete: true });
  expect(final.root.aggregate).toBe("ended");
});

test("branch errors and empty trees retain their coverage meaning", () => {
  const current = tree([], "next");
  const patch = tree([]);
  patch.root.branch = { error: "incomplete" };
  expect(graftContinuationTree(current, patch, "session:root").root).toMatchObject({
    aggregate: "unavailable",
    counts: { active: 0, failed: 0, completed: 0, complete: false },
  });
  expect(graftContinuationTree(current, tree([]), "session:root").root).toMatchObject({
    aggregate: "idle",
    counts: { active: 0, failed: 0, completed: 0, complete: true },
  });
});

test("loading a nested branch preserves an independently pending root continuation", () => {
  const current = tree([delegate(session("child", [shell("a")], "child-next"))], "root-next");
  const patch = tree([delegate(session("child", [shell("b")]))]);
  const result = graftContinuationTree(current, patch, "session:child");
  expect(result.root.branch).toEqual(current.root.branch);
  expect(result.root.counts.complete).toBe(false);
  const entry = result.root.entries[0];
  if (entry?.kind !== "delegate" || !entry.delegate.child) throw new Error("missing child");
  expect(entry.delegate.child.branch).toEqual({});
  expect(entry.delegate.child.counts.complete).toBe(true);
});

test("a continuation for a removed branch cannot change the retained tree", () => {
  const current = tree([shell("a")]);
  expect(graftContinuationTree(current, tree([shell("foreign")]), "session:missing")).toBe(current);
});
