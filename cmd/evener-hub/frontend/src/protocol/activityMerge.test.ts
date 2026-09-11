import { expect, test } from "vitest";
import {
  type ActivityDelegateEntry,
  type ActivityEntry,
  type ActivitySessionNode,
  type ActivityShellEntry,
  type ActivityTree,
  activityNodeID,
  parseActivityTree,
} from "./activityData";
import { fenceRootSession, graftContinuationTree } from "./activityMerge";

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
  const result = graftContinuationTree(current, "session:root", patch);
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
  const result = graftContinuationTree(current, "session:child", patch);
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
  const result = graftContinuationTree(current, "delegate:delegate", patch);
  const entry = result.root.entries[0];
  if (entry?.kind !== "delegate" || !entry.delegate.child) throw new Error("missing child");
  expect(ids(entry.delegate.child)).toEqual(["job:a"]);
  expect(entry.delegate.branch).toEqual({});
  expect(entry.delegate.projectionRevision).toBe(2);
  expect(entry.delegate.status).toBe("completed");
});

test("turn-based delegates aggregate their work without becoming unavailable", () => {
  const current = tree([shell("earlier")], "next");
  const withTurns = delegate(undefined, 2);
  delete withTurns.delegate.type;
  withTurns.delegate.branch = {};
  withTurns.delegate.turns = [shell("turn").job];
  const parsed = parseActivityTree(tree([withTurns]));
  if (!parsed) throw new Error("turn-based activity tree did not parse");
  const result = graftContinuationTree(current, "session:root", parsed);
  expect(result.root.counts).toEqual({ active: 0, failed: 0, completed: 2, complete: true });
  expect(result.root.aggregate).toBe("ended");
});

test("turn-container continuation refresh is not blocked by stable projection fencing", () => {
  const currentEntry = delegate();
  delete currentEntry.delegate.type;
  currentEntry.delegate.turns = [shell("old-turn").job];
  const patchEntry = delegate();
  delete patchEntry.delegate.type;
  patchEntry.delegate.turns = [shell("new-turn").job];
  const result = graftContinuationTree(tree([currentEntry], "next"), "session:root", tree([patchEntry]));
  const entry = result.root.entries[0];
  if (entry?.kind !== "delegate") throw new Error("missing turn container");
  expect(entry.delegate.turns?.map((turn) => turn.jobId)).toEqual(["new-turn"]);
});

test("turn-container refresh retains the latest activity timestamp", () => {
  const currentEntry = delegate();
  delete currentEntry.delegate.type;
  currentEntry.delegate.latestActivityAt = "2026-09-10T00:02:00Z";
  currentEntry.delegate.turns = [shell("old-turn").job];
  const patchEntry = delegate();
  delete patchEntry.delegate.type;
  patchEntry.delegate.latestActivityAt = "2026-09-10T00:01:00Z";
  patchEntry.delegate.turns = [shell("new-turn").job];
  const result = graftContinuationTree(tree([currentEntry], "next"), "session:root", tree([patchEntry]));
  const entry = result.root.entries[0];
  if (entry?.kind !== "delegate") throw new Error("missing turn container");
  expect(entry.delegate.latestActivityAt).toBe("2026-09-10T00:02:00Z");
});

test("a child-session continuation keeps the turn container's own newer turns", () => {
  const currentEntry = delegate(session("child", [shell("a")], "next"));
  delete currentEntry.delegate.type;
  currentEntry.delegate.latestActivityAt = "2026-09-10T00:01:00Z";
  currentEntry.delegate.turns = [shell("turn-old").job, shell("turn-new").job];
  const patchEntry = delegate(session("child", [shell("b")]));
  delete patchEntry.delegate.type;
  patchEntry.delegate.latestActivityAt = "2026-09-10T00:03:00Z";
  patchEntry.delegate.turns = [shell("turn-old").job];
  const result = graftContinuationTree(tree([currentEntry]), "session:child", tree([patchEntry]));
  const entry = result.root.entries[0];
  if (entry?.kind !== "delegate" || !entry.delegate.child) throw new Error("missing turn container");
  expect(entry.delegate.turns?.map((turn) => turn.jobId)).toEqual(["turn-old", "turn-new"]);
  expect(ids(entry.delegate.child)).toEqual(["job:a", "job:b"]);
  expect(entry.delegate.latestActivityAt).toBe("2026-09-10T00:03:00Z");
});

// The wire always sends turns (appwire/types.go has no omitempty on the field),
// so an empty list is the daemon saying "none", the same standing as absent.
test.each([
  { held: "no turn list", turns: undefined },
  { held: "an empty turn list", turns: [] },
])("a child-session continuation adopts turns a client holding $held had never seen", ({ turns }) => {
  const currentEntry = delegate(session("child", [shell("a")], "next"));
  delete currentEntry.delegate.type;
  currentEntry.delegate.turns = turns;
  const patchEntry = delegate(session("child", [shell("b")]));
  delete patchEntry.delegate.type;
  patchEntry.delegate.turns = [shell("turn-first").job];
  const result = graftContinuationTree(tree([currentEntry]), "session:child", tree([patchEntry]));
  const entry = result.root.entries[0];
  if (entry?.kind !== "delegate") throw new Error("missing turn container");
  expect(entry.delegate.turns?.map((turn) => turn.jobId)).toEqual(["turn-first"]);
});

test("a child-session continuation cannot regress its container's own state", () => {
  const currentEntry = delegate(session("child", [shell("a")], "next"));
  delete currentEntry.delegate.type;
  currentEntry.delegate.status = "completed";
  currentEntry.delegate.outcome = "completed";
  currentEntry.delegate.terminal = true;
  currentEntry.delegate.turns = [shell("turn-1").job];
  const patchEntry = delegate(session("child", [shell("b")]));
  delete patchEntry.delegate.type;
  patchEntry.delegate.status = "running";
  patchEntry.delegate.outcome = undefined;
  patchEntry.delegate.terminal = false;
  patchEntry.delegate.turns = [shell("turn-1").job, shell("turn-2").job];
  const result = graftContinuationTree(tree([currentEntry]), "session:child", tree([patchEntry]));
  const entry = result.root.entries[0];
  if (entry?.kind !== "delegate") throw new Error("missing turn container");
  expect(entry.delegate).toMatchObject({ status: "completed", outcome: "completed", terminal: true });
  expect(entry.delegate.turns?.map((turn) => turn.jobId)).toEqual(["turn-1", "turn-2"]);
});

test("a bounded refresh keeps the turns a continuation loaded into a container", () => {
  const currentEntry = delegate(session("child", [shell("a")]));
  delete currentEntry.delegate.type;
  currentEntry.delegate.branch = { truncated: true, continuation: "delegate-next" };
  currentEntry.delegate.turns = [shell("turn-1").job, shell("turn-2").job];
  const incoming = delegate(session("child", [shell("a")]));
  delete incoming.delegate.type;
  incoming.delegate.branch = { truncated: true, continuation: "delegate-next" };
  incoming.delegate.turns = [shell("turn-1").job];
  const fenced = fenceRootSession(tree([currentEntry]).root, tree([incoming]).root);
  const entry = fenced.entries[0];
  if (entry?.kind !== "delegate") throw new Error("missing turn container");
  expect(entry.delegate.turns?.map((turn) => turn.jobId)).toEqual(["turn-1", "turn-2"]);
  // A retained turn is work the session still holds, so it has to be counted.
  expect(fenced.counts).toEqual({ active: 0, failed: 0, completed: 3, complete: false });
});

test("a bounded refresh still updates the container state it does list", () => {
  const currentEntry = delegate(session("child", [shell("a")]));
  delete currentEntry.delegate.type;
  currentEntry.delegate.branch = { truncated: true, continuation: "delegate-next" };
  currentEntry.delegate.status = "running";
  currentEntry.delegate.outcome = undefined;
  currentEntry.delegate.terminal = false;
  currentEntry.delegate.turns = [shell("turn-1").job, shell("turn-2").job];
  const incoming = delegate(session("child", [shell("a")]));
  delete incoming.delegate.type;
  incoming.delegate.branch = { truncated: true, continuation: "delegate-next" };
  incoming.delegate.status = "completed";
  incoming.delegate.outcome = "completed";
  incoming.delegate.terminal = true;
  incoming.delegate.turns = [shell("turn-1").job];
  const fenced = fenceRootSession(tree([currentEntry]).root, tree([incoming]).root);
  const entry = fenced.entries[0];
  if (entry?.kind !== "delegate") throw new Error("missing turn container");
  expect(entry.delegate).toMatchObject({ status: "completed", outcome: "completed", terminal: true });
  expect(entry.delegate.turns?.map((turn) => turn.jobId)).toEqual(["turn-1", "turn-2"]);
});

test("a child-session continuation adopts unseen turns beside the ones on screen", () => {
  const currentEntry = delegate(session("child", [shell("a")], "next"));
  delete currentEntry.delegate.type;
  currentEntry.delegate.turns = [shell("turn-1").job];
  const patchEntry = delegate(session("child", [shell("b")]));
  delete patchEntry.delegate.type;
  patchEntry.delegate.turns = [shell("turn-1").job, shell("turn-2").job];
  const result = graftContinuationTree(tree([currentEntry]), "session:child", tree([patchEntry]));
  const entry = result.root.entries[0];
  if (entry?.kind !== "delegate") throw new Error("missing turn container");
  expect(entry.delegate.turns?.map((turn) => turn.jobId)).toEqual(["turn-1", "turn-2"]);
});

// A bounded page is a prefix of the session's entry order and says nothing
// about what lies past its cutoff, so pages already loaded from beyond it stay.
test("a bounded root refresh keeps the pages already loaded past its window", () => {
  const paged = graftContinuationTree(tree([shell("a")], "next"), "session:root", tree([shell("b")]));
  const fenced = fenceRootSession(paged.root, tree([shell("a", 40)], "next-2").root);
  expect(ids(fenced)).toEqual(["job:a", "job:b"]);
  expect(fenced.entries[0]).toEqual(shell("a", 40));
  expect(fenced.branch).toEqual({ truncated: true, continuation: "next-2" });
  expect(fenced.counts).toEqual({ active: 0, failed: 0, completed: 2, complete: false });
});

// Every projectStableActivityDelegate path that returns without a child marks
// the delegate's own branch first - a child-unavailable or link-mismatch error,
// or the depth cut-off's truncation - so an omission here says "could not
// render it", never "there is none".
test("a refresh that could not render a child keeps the subtree already loaded", () => {
  const paged = graftContinuationTree(
    tree([delegate(session("child", [shell("a")], "child-next"))]),
    "session:child",
    tree([delegate(session("child", [shell("b")]))]),
  );
  const fenced = fenceRootSession(paged.root, tree([delegate()]).root);
  const entry = fenced.entries[0];
  if (entry?.kind !== "delegate" || !entry.delegate.child) throw new Error("the loaded child subtree was dropped");
  expect(ids(entry.delegate.child)).toEqual(["job:a", "job:b"]);
  expect(fenced.counts).toEqual({ active: 0, failed: 0, completed: 3, complete: false });
});

test("a complete refresh drops a child the delegate no longer carries", () => {
  const paged = graftContinuationTree(
    tree([delegate(session("child", [shell("a")], "child-next"))]),
    "session:child",
    tree([delegate(session("child", [shell("b")]))]),
  );
  const childless = delegate();
  childless.delegate.branch = {};
  const fenced = fenceRootSession(paged.root, tree([childless]).root);
  const entry = fenced.entries[0];
  if (entry?.kind !== "delegate") throw new Error("missing delegate");
  expect(entry.delegate.child).toBeUndefined();
  expect(fenced.counts).toEqual({ active: 0, failed: 0, completed: 1, complete: true });
});

test("a complete root refresh drops the entries it no longer lists", () => {
  const paged = graftContinuationTree(tree([shell("a")], "next"), "session:root", tree([shell("b")]));
  const fenced = fenceRootSession(paged.root, tree([shell("a")]).root);
  expect(ids(fenced)).toEqual(["job:a"]);
  expect(fenced.counts).toEqual({ active: 0, failed: 0, completed: 1, complete: true });
});

test("a bounded root refresh keeps the pages loaded inside a delegate's child", () => {
  const paged = graftContinuationTree(
    tree([delegate(session("child", [shell("a")], "child-next"))]),
    "session:child",
    tree([delegate(session("child", [shell("b")]))]),
  );
  const fenced = fenceRootSession(paged.root, tree([delegate(session("child", [shell("a")], "child-next-2"))]).root);
  const entry = fenced.entries[0];
  if (entry?.kind !== "delegate" || !entry.delegate.child) throw new Error("missing child");
  expect(ids(entry.delegate.child)).toEqual(["job:a", "job:b"]);
});

test.each([null, []])("delegate turns accept the wire array value %j", (turns) => {
  const entry = delegate();
  const raw = tree([entry]);
  const parsed = parseActivityTree({
    ...raw,
    root: { ...raw.root, entries: [{ ...entry, delegate: { ...entry.delegate, turns } }] },
  });
  expect(parsed?.root.entries[0]?.kind).toBe("delegate");
});

test("continuation data does not regress a newer delegate projection", () => {
  const current = tree([delegate(session("child", [shell("a")], "next"), 2)]);
  const patch = tree([delegate(session("child", [shell("b")]), 1)]);
  const result = graftContinuationTree(current, "session:child", patch);
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
  const result = graftContinuationTree(current, "session:root", tree([running, exhausted]));
  expect(result.root.counts).toEqual({ active: 1, failed: 2, completed: 1, complete: true });
  expect(result.root.aggregate).toBe("working");
  running.job.terminal = true;
  const settled = graftContinuationTree(current, "session:root", tree([running, exhausted]));
  expect(settled.root.aggregate).toBe("failed");
});

test.each([
  ["shell", "failure", 1],
  ["shell", "failed", 0],
  ["shell", "exhausted", 0],
  ["stable delegate", "failure", 0],
  ["stable delegate", "failed", 1],
  ["stable delegate", "exhausted", 1],
  ["turn container", "failure", 1],
  ["turn container", "failed", 0],
  ["turn container", "exhausted", 0],
])("matches backend counts for %s outcome %s", (kind, outcome, failed) => {
  const entry = kind === "shell" ? shell("failed") : delegate();
  if (kind === "shell") {
    if (entry.kind !== "shell") throw new Error("unexpected shell fixture");
    entry.job.outcome = outcome;
    entry.job.terminal = true;
  } else if (kind === "stable delegate") {
    if (entry.kind !== "delegate") throw new Error("unexpected delegate fixture");
    entry.delegate.outcome = outcome;
    entry.delegate.terminal = true;
  } else {
    if (entry.kind !== "delegate") throw new Error("unexpected turn fixture");
    delete entry.delegate.type;
    entry.delegate.turns = [{ ...shell("failed").job, outcome, terminal: true }];
  }
  if (entry.kind === "delegate") entry.delegate.branch = {};
  const result = graftContinuationTree(tree([]), "session:root", tree([entry]));
  expect(result.root.counts).toMatchObject({ active: 0, failed, completed: 1 - failed });
  expect(result.root.aggregate).toBe(failed ? "failed" : "ended");
});

test.each(["error", "failed", "exhausted"])(
  'summary counts terminal status "%s" as completed without an outcome',
  (status) => {
    const job = shell(`job-${status}`);
    job.job.status = status;
    job.job.outcome = undefined;
    const stable = delegate();
    stable.delegate.branch = {};
    stable.delegate.status = status;
    stable.delegate.outcome = undefined;
    stable.delegate.terminal = true;
    const turns = delegate();
    delete turns.delegate.type;
    turns.delegate.branch = {};
    turns.delegate.turns = [{ ...shell(`turn-${status}`).job, status, outcome: undefined }];
    const result = graftContinuationTree(tree([]), "session:root", tree([job, stable, turns]));
    expect(result.root.counts).toMatchObject({ active: 0, failed: 0, completed: 3 });
    expect(result.root.aggregate).toBe("ended");
  },
);

test("merged summaries count empty turn-container delegates as one entry", () => {
  const emptyTurns = delegate();
  if (emptyTurns.delegate.type !== "delegate") throw new Error("unexpected delegate type");
  delete emptyTurns.delegate.type;
  emptyTurns.delegate.status = "completed";
  emptyTurns.delegate.outcome = "completed";
  emptyTurns.delegate.branch = {};
  const result = graftContinuationTree(tree([]), "session:root", tree([emptyTurns]));
  expect(result.root.counts).toMatchObject({ active: 0, failed: 0, completed: 0 });
  expect(result.root.aggregate).toBe("idle");
});

test("partial descendant coverage stays incomplete until its last page loads", () => {
  const current = tree([shell("a")], "root-next");
  const partial = graftContinuationTree(
    current,
    "session:root",
    tree([delegate(session("child", [shell("b")], "child-next"))]),
  );
  expect(partial.root.counts).toEqual({ active: 0, failed: 0, completed: 3, complete: false });
  expect(partial.root.aggregate).toBe("unavailable");
  const final = graftContinuationTree(partial, "session:child", tree([delegate(session("child", [shell("c")]))]));
  expect(final.root.counts).toEqual({ active: 0, failed: 0, completed: 4, complete: true });
  expect(final.root.aggregate).toBe("ended");
});

test("branch errors and empty trees retain their coverage meaning", () => {
  const current = tree([], "next");
  const patch = tree([]);
  patch.root.branch = { error: "incomplete" };
  expect(graftContinuationTree(current, "session:root", patch).root).toMatchObject({
    aggregate: "unavailable",
    counts: { active: 0, failed: 0, completed: 0, complete: false },
  });
  expect(graftContinuationTree(current, "session:root", tree([])).root).toMatchObject({
    aggregate: "idle",
    counts: { active: 0, failed: 0, completed: 0, complete: true },
  });
});

test("loading a nested branch preserves an independently pending root continuation", () => {
  const current = tree([delegate(session("child", [shell("a")], "child-next"))], "root-next");
  const patch = tree([delegate(session("child", [shell("b")]))]);
  const result = graftContinuationTree(current, "session:child", patch);
  expect(result.root.branch).toEqual(current.root.branch);
  expect(result.root.counts.complete).toBe(false);
  const entry = result.root.entries[0];
  if (entry?.kind !== "delegate" || !entry.delegate.child) throw new Error("missing child");
  expect(entry.delegate.child.branch).toEqual({});
  expect(entry.delegate.child.counts.complete).toBe(true);
});

test("a continuation for a removed branch cannot change the retained tree", () => {
  const current = tree([shell("a")]);
  expect(graftContinuationTree(current, "session:missing", tree([shell("foreign")]))).toBe(current);
});

test("a nested page cannot replace or add coverage on an unrelated branch", () => {
  const current = tree([delegate(session("child", [shell("a")], "child-next"))], "root-next");
  const patch = tree([delegate(session("child", [shell("b")]))]);
  patch.root.branch = { error: "unrelated-page-error" };
  const result = graftContinuationTree(current, "session:child", patch);
  expect(result.root.branch).toEqual({ truncated: true, continuation: "root-next" });
  const child = result.root.entries[0];
  if (child?.kind !== "delegate" || !child.delegate.child) throw new Error("missing child");
  expect(ids(child.delegate.child)).toEqual(["job:a", "job:b"]);
  expect(child.delegate.child.branch).toEqual({});
});
