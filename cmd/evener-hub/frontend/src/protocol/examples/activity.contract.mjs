import assert from "node:assert/strict";
import test from "node:test";
import { WireError } from "@evener/appwire-client";
import { runActivity, summarizeActivity } from "./activity-logic.mjs";

const job = (jobId) => ({
  kind: "shell",
  job: {
    jobId,
    ownerSessionId: "thread-1",
    ownerRef: "local:one",
    type: "shell",
    status: "completed",
    terminal: true,
    background: false,
    hasOutput: false,
    description: "job",
    startedAt: "2026-09-07T00:00:00Z",
    outputBytes: 0,
  },
});
const tree = (revision, entries = [job("job-1")], branch = {}) => ({
  revision,
  root: {
    kind: "session",
    sessionId: "thread-1",
    ref: "local:one",
    label: "Thread",
    aggregate: "ended",
    counts: { active: 0, failed: 0, completed: entries.length, complete: !branch.continuation },
    entries,
    branch,
  },
});
function fakeHub(responses) {
  const calls = [];
  return {
    calls,
    hub: {
      connect: async () => calls.push({ method: "connect" }),
      request: async (method, params) => {
        calls.push({ method, params });
        const response = responses.shift();
        if (response instanceof Error) throw response;
        return { data: response };
      },
      onNotification: () => () => {},
    },
  };
}

test("activity workflow validates before connection", async () => {
  const f = fakeHub([]);
  for (const input of [
    null,
    {},
    { ref: "local:one", threadId: "thread-1", extra: true },
    { ref: "local:one", threadId: "thread-1", maxPages: 0 },
    { ref: "local:one", threadId: "thread-1", maxPages: 101 },
    { ref: "local:one", threadId: "thread-1", maxPages: 1.5 },
    { ref: "local:one", threadId: "thread-1", maxPages: Number.NaN },
    { ref: "local:one", threadId: "thread-1", maxPages: "2" },
    { ref: "local:one", threadId: "thread-1", maxPages: Number.MAX_SAFE_INTEGER + 1 },
  ])
    await assert.rejects(runActivity(f.hub, input));
  assert.deepEqual(f.calls, []);
});
test("activity workflow reads advertised continuations within its page budget", async () => {
  const f = fakeHub([tree(1, [job("a")], { truncated: true, continuation: "cursor-a" }), tree(2, [job("b")])]);
  const result = await runActivity(f.hub, { ref: "local:one", threadId: "thread-1", maxPages: 2 });
  assert.equal(result.outcome, "read");
  assert.equal(result.pagesRead, 2);
  assert.equal(result.readback.root.entries.length, 2);
  assert.deepEqual(
    f.calls.map((call) => call.method),
    ["connect", "evener/jobs/list", "evener/jobs/list"],
  );
});
test("activity workflow preserves tree after a continuation failure", async () => {
  const f = fakeHub([tree(1, [job("a")], { truncated: true, continuation: "cursor-a" }), new Error("offline")]);
  const result = await runActivity(f.hub, { ref: "local:one", threadId: "thread-1" });
  assert.equal(result.outcome, "failed");
  assert.equal(result.readback.root.entries.length, 1);
  assert.equal(result.pagesRead, 2);
});
test("activity summary does not disclose tree labels, refs, cursors, or errors", () => {
  const summary = summarizeActivity({
    outcome: "failed",
    pagesRead: 2,
    remainingBranches: [
      { id: "private-id", label: "private-label", continuation: "private-cursor", error: "private-error" },
    ],
  });
  assert.deepEqual(summary, { outcome: "failed", pagesRead: 2, remainingBranches: 1 });
});

test("branch errors and truncated branches are incomplete or failed even below budget", async () => {
  const truncated = fakeHub([tree(1, [job("a")], { truncated: true })]);
  assert.equal(
    (await runActivity(truncated.hub, { ref: "local:one", threadId: "thread-1", maxPages: 4 })).outcome,
    "incomplete",
  );
  const errored = fakeHub([tree(1, [job("a")], { error: "server retained prefix" })]);
  assert.equal(
    (await runActivity(errored.hub, { ref: "local:one", threadId: "thread-1", maxPages: 4 })).outcome,
    "failed",
  );
});

test("nested branches use the advertised branch id and exact continuation", async () => {
  const token = "child cursor / λ";
  const nested = {
    kind: "delegate",
    delegate: {
      delegateId: "delegate-1",
      childSessionId: "child-1",
      childRef: "local:child",
      description: "child",
      type: "delegate",
      terminal: true,
      outcome: "completed",
      branch: { continuation: token, truncated: true },
      child: {
        kind: "session",
        sessionId: "child-1",
        ref: "local:child",
        label: "Child",
        aggregate: "ended",
        counts: { active: 0, failed: 0, completed: 0, complete: false },
        entries: [],
        branch: {},
      },
    },
  };
  const rootJob = job("root-job");
  const childPage = structuredClone(nested);
  childPage.delegate.branch = {};
  childPage.delegate.child.entries = [job("child-job-1"), job("child-job-2")];
  childPage.delegate.child.counts = { active: 0, failed: 0, completed: 2, complete: true };
  const f = fakeHub([tree(1, [rootJob, nested]), tree(2, [childPage])]);
  const result = await runActivity(f.hub, { ref: "local:one", threadId: "thread-1", maxPages: 2 });
  assert.equal(result.outcome, "read");
  assert.equal(result.pagesRead, 2);
  assert.deepEqual(f.calls[2].params, { ref: "local:one", continuation: token });
  assert.deepEqual(
    result.readback.root.entries.map((entry) =>
      entry.kind === "shell" ? entry.job.jobId : entry.delegate.child.entries.map((child) => child.job.jobId),
    ),
    ["root-job", ["child-job-1", "child-job-2"]],
  );
  assert.deepEqual(result.remainingBranches, []);
});

test("an incomplete root summary remains incomplete without a branch token", async () => {
  const incomplete = tree(1, [job("a")]);
  incomplete.root.counts.complete = false;
  const result = await runActivity(fakeHub([incomplete]).hub, { ref: "local:one", threadId: "thread-1" });
  assert.equal(result.outcome, "incomplete");
});

test("wrong root and stale revisions preserve the already retained tree", async () => {
  const wrong = fakeHub([]);
  const wrongTree = tree(1);
  wrongTree.root.ref = "local:other";
  wrong.hub.request = async (method, params) => {
    wrong.calls.push({ method, params });
    return { data: wrongTree };
  };
  const result = await runActivity(wrong.hub, { ref: "local:one", threadId: "thread-1" });
  assert.equal(result.outcome, "failed");
  const wrongThread = fakeHub([]);
  const wrongThreadTree = tree(1);
  wrongThreadTree.root.sessionId = "thread:other";
  wrongThread.hub.request = async (method, params) => {
    wrongThread.calls.push({ method, params });
    return { data: wrongThreadTree };
  };
  assert.equal((await runActivity(wrongThread.hub, { ref: "local:one", threadId: "thread-1" })).outcome, "failed");
  const stale = fakeHub([tree(2, [job("a")], { continuation: "cursor" }), tree(1, [job("b")])]);
  const staleResult = await runActivity(stale.hub, { ref: "local:one", threadId: "thread-1" });
  assert.equal(staleResult.outcome, "failed");
  assert.equal(staleResult.readback.root.entries[0].job.jobId, "a");
});

test("bounded pages, cursor stalls, empty reads, and unavailable or ended roots are typed", async () => {
  const budget = fakeHub([tree(1, [job("a")], { continuation: "cursor", truncated: true })]);
  const budgetResult = await runActivity(budget.hub, { ref: "local:one", threadId: "thread-1", maxPages: 1 });
  assert.equal(budgetResult.outcome, "incomplete");
  assert.equal(budget.calls.filter((call) => call.method === "evener/jobs/list").length, 1);
  const stall = fakeHub([
    tree(1, [job("a")], { continuation: "cursor", truncated: true }),
    tree(2, [job("b")], { continuation: "cursor", truncated: true }),
  ]);
  const stallResult = await runActivity(stall.hub, { ref: "local:one", threadId: "thread-1", maxPages: 10 });
  assert.equal(stallResult.outcome, "incomplete");
  assert.equal(stallResult.pagesRead, 2);
  assert.equal(stall.calls.filter((call) => call.method === "evener/jobs/list").length, 2);
  const erroredStall = fakeHub([
    tree(1, [job("a")], { continuation: "cursor", truncated: true }),
    tree(2, [job("b")], { error: "retained prefix" }),
  ]);
  assert.equal(
    (await runActivity(erroredStall.hub, { ref: "local:one", threadId: "thread-1", maxPages: 10 })).outcome,
    "failed",
  );
  const empty = fakeHub([tree(1, [], {})]);
  assert.equal((await runActivity(empty.hub, { ref: "local:one", threadId: "thread-1" })).outcome, "read");
  for (const [error, outcome] of [
    [new WireError("unsupported", -32014, { evenerErrorInfo: "actionUnavailable" }), "unavailable"],
    [new WireError("thread not found: thread-1", -32014, { evenerErrorInfo: "sessionUnavailable" }), "ended"],
  ]) {
    const f = fakeHub([error]);
    const result = await runActivity(f.hub, { ref: "local:one", threadId: "thread-1" });
    assert.equal(result.outcome, outcome);
    assert.equal(result.readback, null);
  }
});

test("input is captured before connect and failed connect makes no request", async () => {
  const f = fakeHub([tree(1)]);
  const input = { ref: "local:one", threadId: "thread-1" };
  f.hub.connect = async () => {
    f.calls.push({ method: "connect" });
    input.ref = "local:changed";
  };
  const result = await runActivity(f.hub, input);
  assert.equal(result.outcome, "read");
  assert.deepEqual(f.calls[1].params, { ref: "local:one" });
  const failed = fakeHub([]);
  failed.hub.connect = async () => {
    throw new Error("connect failed");
  };
  const failure = await runActivity(failed.hub, { ref: "local:one", threadId: "thread-1" });
  assert.equal(failure.outcome, "failed");
  assert.deepEqual(failed.calls, []);
});
