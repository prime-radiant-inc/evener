// @vitest-environment node
import { describe, expect, it } from "vitest";
import {
  type ActivityBranchState,
  type ActivityTree,
  activityNodeID,
  defaultExpandedIDs,
  parseActivityTree,
  reconcileActivityState,
} from "./activityData";

function branch(overrides: Partial<ActivityBranchState> = {}): ActivityBranchState {
  return { ...overrides };
}

function cloneWire<T>(value: T): T {
  return JSON.parse(JSON.stringify(value)) as T;
}

function assertDefined<T>(value: T | null | undefined, message: string): T {
  if (value == null) throw new Error(message);
  return value;
}

function getRootDelegateWire(tree: typeof VALID_TREE_WIRE) {
  const entry = assertDefined(tree.root.entries[1], "expected root delegate wire entry");
  if (entry.kind !== "delegate") throw new Error("expected root delegate wire entry");
  return assertDefined(entry.delegate, "expected root delegate payload");
}

function getChildSessionWire(tree: typeof VALID_TREE_WIRE) {
  return assertDefined(getRootDelegateWire(tree).child, "expected child session wire");
}

function getNestedDelegateWire(tree: typeof VALID_TREE_WIRE) {
  const entry = assertDefined(getChildSessionWire(tree).entries[1], "expected nested delegate wire entry");
  if (entry.kind !== "delegate") throw new Error("expected nested delegate wire entry");
  return assertDefined(entry.delegate, "expected nested delegate payload");
}

function getMalformedSiblingWire(tree: typeof VALID_TREE_WIRE) {
  const entry = assertDefined(getChildSessionWire(tree).entries[4], "expected malformed sibling wire entry");
  if (entry.kind !== "delegate") throw new Error("expected malformed sibling wire entry");
  return assertDefined(entry.delegate, "expected malformed sibling delegate payload");
}

function getRootShellWire(tree: typeof VALID_TREE_WIRE) {
  const entry = assertDefined(tree.root.entries[0], "expected root shell wire entry");
  if (entry.kind !== "shell") throw new Error("expected root shell wire entry");
  return assertDefined(entry.job, "expected root shell payload") as typeof entry.job & {
    transcriptRef?: string;
  };
}

function getDelegateEntry(tree: ActivityTree, ...path: number[]) {
  const firstIndex = path[0];
  if (typeof firstIndex !== "number") throw new Error("delegate path requires at least one index");
  let entry = tree.root.entries[firstIndex];
  for (let index = 1; index < path.length; index += 1) {
    const nextIndex = path[index];
    if (typeof nextIndex !== "number") return undefined;
    expect(entry?.kind).toBe("delegate");
    entry = entry?.kind === "delegate" ? entry.delegate.child?.entries[nextIndex] : undefined;
  }
  expect(entry?.kind).toBe("delegate");
  return entry?.kind === "delegate" ? entry : undefined;
}

const VALID_TREE_WIRE = {
  revision: 7,
  root: {
    sessionId: "sess_root",
    ref: "ref_root",
    label: "Root session",
    aggregate: "running",
    counts: { active: 2, failed: 1, completed: 3, complete: false },
    entries: [
      {
        kind: "shell",
        job: {
          jobId: "job_root_1",
          ownerSessionId: "sess_root",
          ownerRef: "ref_root",
          type: "shell",
          status: "completed",
          outcome: "success",
          terminal: true,
          background: false,
          hasOutput: true,
          description: "run root checks",
          command: "npm test",
          startedAt: "2026-08-03T00:00:00Z",
          endedAt: "2026-08-03T00:01:00Z",
          exitCode: 0,
          outputBytes: 10,
        },
      },
      {
        kind: "delegate",
        delegate: {
          delegateId: "dlg_1",
          ownerSessionId: "sess_root",
          rootSessionId: "sess_root",
          childSessionId: "sess_child",
          childRef: "ref_child",
          transcriptRef: "ref_child",
          type: "delegate",
          lifecycle: "retained",
          phase: "idle",
          status: "queuedForRetry",
          projectionRevision: 2,
          terminal: true,
          resumable: true,
          mandate: "inspect branch",
          runStartedAt: "2026-08-03T00:02:00Z",
          runEndedAt: "2026-08-03T00:04:00Z",
          latestActivityAt: "2026-08-03T00:04:00Z",
          child: {
            sessionId: "sess_child",
            ref: "ref_child",
            label: "Child session",
            aggregate: "running",
            counts: { active: 1, failed: 0, completed: 1, complete: false },
            entries: [
              {
                kind: "shell",
                job: {
                  jobId: "job_child_active",
                  ownerSessionId: "sess_child",
                  ownerRef: "ref_child",
                  type: "shell",
                  status: "running",
                  terminal: false,
                  background: false,
                  hasOutput: false,
                  description: "child active shell",
                  command: "make test",
                  startedAt: "2026-08-03T00:05:00Z",
                  outputBytes: 2,
                },
              },
              {
                kind: "delegate",
                delegate: {
                  delegateId: "dlg_nested",
                  ownerSessionId: "sess_child",
                  rootSessionId: "sess_root",
                  childSessionId: "sess_leaf",
                  childRef: "ref_leaf",
                  transcriptRef: "ref_leaf",
                  type: "delegate",
                  lifecycle: "retained",
                  phase: "idle",
                  status: "completed",
                  projectionRevision: 1,
                  terminal: true,
                  resumable: false,
                  runStartedAt: "2026-08-03T00:06:00Z",
                  runEndedAt: "2026-08-03T00:07:00Z",
                  latestActivityAt: "2026-08-03T00:07:00Z",
                  child: {
                    sessionId: "sess_leaf",
                    ref: "ref_leaf",
                    label: "Leaf session",
                    aggregate: "completed",
                    counts: { active: 0, failed: 0, completed: 1, complete: true },
                    entries: [
                      {
                        kind: "shell",
                        job: {
                          jobId: "job_leaf_done",
                          ownerSessionId: "sess_leaf",
                          ownerRef: "ref_leaf",
                          type: "shell",
                          status: "completed",
                          terminal: true,
                          background: false,
                          hasOutput: false,
                          description: "leaf done",
                          startedAt: "2026-08-03T00:08:00Z",
                          endedAt: "2026-08-03T00:09:00Z",
                          outputBytes: 0,
                        },
                      },
                    ],
                    branch: {},
                  },
                  branch: {},
                },
              },
              {
                kind: "delegate",
                delegate: {
                  delegateId: "dlg_unavailable",
                  ownerSessionId: "sess_child",
                  rootSessionId: "sess_root",
                  childSessionId: "sess_missing",
                  childRef: "ref_missing",
                  transcriptRef: "ref_missing",
                  type: "delegate",
                  lifecycle: "retained",
                  phase: "idle",
                  status: "completed",
                  projectionRevision: 1,
                  terminal: true,
                  resumable: false,
                  branch: { error: "child unavailable" },
                },
              },
              {
                kind: "delegate",
                delegate: {
                  delegateId: "dlg_truncated",
                  ownerSessionId: "sess_child",
                  rootSessionId: "sess_root",
                  childSessionId: "sess_truncated",
                  childRef: "ref_truncated",
                  transcriptRef: "ref_truncated",
                  type: "delegate",
                  lifecycle: "retained",
                  phase: "idle",
                  status: "completed",
                  projectionRevision: 1,
                  terminal: true,
                  resumable: false,
                  branch: { truncated: true, continuation: "page-2" },
                },
              },
              {
                kind: "delegate",
                delegate: {
                  delegateId: "dlg_bad_sibling",
                  ownerSessionId: "sess_child",
                  rootSessionId: "sess_root",
                  childSessionId: "sess_bad",
                  childRef: "ref_bad",
                  transcriptRef: "ref_bad",
                  type: "delegate",
                  lifecycle: "retained",
                  phase: "idle",
                  status: "failed",
                  projectionRevision: 1.5,
                  terminal: true,
                  resumable: false,
                  branch: {},
                },
              },
            ],
            branch: {},
          },
          branch: {},
        },
      },
    ],
    branch: {},
  },
};

it("parses stable delegate lineage and ParentDelegateID shell ancestry without activation turns", () => {
  const parsed = parseActivityTree({
    revision: 12,
    root: {
      sessionId: "sess_root",
      ref: "ref_root",
      label: "Root",
      aggregate: "running",
      counts: { active: 2, failed: 0, completed: 0, complete: true },
      entries: [
        {
          kind: "delegate",
          delegate: {
            delegateId: "dlg_parent",
            ownerSessionId: "sess_root",
            rootSessionId: "sess_root",
            childSessionId: "sess_child",
            childRef: "local:sess_child",
            transcriptRef: "local:sess_child",
            type: "delegate",
            lifecycle: "active",
            phase: "running",
            status: "running",
            projectionRevision: 4,
            terminal: false,
            resumable: true,
            originTurnId: "turn_1",
            runStartedAt: "2026-08-15T10:00:00Z",
            latestActivityAt: "2026-08-15T10:00:03Z",
            quietForMs: 3000,
            delegationAllowance: 1,
            parentWatchGranted: true,
            branch: {},
            child: {
              sessionId: "sess_child",
              ref: "local:sess_child",
              label: "Child",
              aggregate: "running",
              counts: { active: 1, failed: 0, completed: 0, complete: true },
              entries: [
                {
                  kind: "shell",
                  job: {
                    jobId: "job_shell",
                    ownerSessionId: "sess_child",
                    ownerRef: "local:sess_child",
                    parentDelegateId: "dlg_parent",
                    transcriptRef: "job:job_shell",
                    type: "shell",
                    status: "running",
                    terminal: false,
                    background: true,
                    hasOutput: true,
                    description: "run checks",
                    startedAt: "2026-08-15T10:00:01Z",
                    outputBytes: 12,
                  },
                },
              ],
              branch: {},
            },
          },
        },
      ],
      branch: {},
    },
  });

  const entry = parsed?.root.entries[0];
  expect(entry?.kind).toBe("delegate");
  if (entry?.kind !== "delegate") throw new Error("expected stable delegate");
  expect(entry.delegate).toMatchObject({
    delegateId: "dlg_parent",
    ownerSessionId: "sess_root",
    rootSessionId: "sess_root",
    status: "running",
    projectionRevision: 4,
    parentWatchGranted: true,
  });
  const shell = entry.delegate.child?.entries[0];
  expect(shell?.kind).toBe("shell");
  if (shell?.kind !== "shell") throw new Error("expected child shell");
  expect(shell.job.parentDelegateId).toBe("dlg_parent");
});

function makeDepthWire(depth: number): unknown {
  let session = {
    sessionId: `sess_${depth}`,
    ref: `ref_${depth}`,
    label: `Session ${depth}`,
    aggregate: depth === 0 ? "running" : "completed",
    counts: { active: depth === 0 ? 1 : 0, failed: 0, completed: depth, complete: depth !== 0 },
    entries: [] as unknown[],
    branch: {},
  };
  for (let level = depth - 1; level >= 0; level -= 1) {
    session = {
      sessionId: `sess_${level}`,
      ref: `ref_${level}`,
      label: `Session ${level}`,
      aggregate: "running",
      counts: { active: 1, failed: 0, completed: level, complete: false },
      entries: [
        {
          kind: "delegate",
          delegate: {
            delegateId: `dlg_${level}`,
            ownerSessionId: `sess_${level}`,
            rootSessionId: "sess_0",
            childSessionId: session.sessionId,
            childRef: session.ref,
            transcriptRef: session.ref,
            type: "delegate",
            lifecycle: "active",
            phase: "running",
            status: "running",
            projectionRevision: 1,
            terminal: false,
            resumable: true,
            runStartedAt: "2026-08-03T00:00:00Z",
            latestActivityAt: "2026-08-03T00:00:00Z",
            child: session,
            branch: {},
          },
        },
      ],
      branch: {},
    };
  }
  return { revision: 1, root: session };
}

describe("parseActivityTree", () => {
  it("returns null for the old flat array payload", () => {
    expect(parseActivityTree([{ jobId: "old-flat-row" }])).toBeNull();
  });

  it("parses authoritative shell kind rows for shell-only and nested mixed trees", () => {
    const shellOnly = {
      revision: 3,
      root: {
        sessionId: "sess_shell_only",
        ref: "ref_shell_only",
        label: "Shell only",
        aggregate: "working",
        counts: { active: 1, failed: 0, completed: 0, complete: true },
        entries: [
          {
            kind: "shell",
            job: {
              jobId: "job_shell_only",
              ownerSessionId: "sess_shell_only",
              ownerRef: "ref_shell_only",
              type: "shell",
              status: "running",
              terminal: false,
              background: false,
              hasOutput: false,
              description: "shell only",
              startedAt: "2026-08-03T00:00:00Z",
              outputBytes: 0,
            },
          },
        ],
        branch: {},
      },
    };
    expect(parseActivityTree(shellOnly)?.root.entries).toMatchObject([
      { kind: "shell", job: { jobId: "job_shell_only" } },
    ]);

    const tree = parseActivityTree(VALID_TREE_WIRE) as ActivityTree;
    expect(tree.root.entries[0]).toMatchObject({ kind: "shell", job: { jobId: "job_root_1" } });
    expect(getDelegateEntry(tree, 1)?.delegate.child?.entries[0]).toMatchObject({
      kind: "shell",
      job: { jobId: "job_child_active" },
    });
    expect(getDelegateEntry(tree, 1, 1)?.delegate.child?.entries[0]).toMatchObject({
      kind: "shell",
      job: { jobId: "job_leaf_done" },
    });
  });

  it("retains a valid transcriptRef on shell jobs", () => {
    const tree = cloneWire(VALID_TREE_WIRE);
    getRootShellWire(tree).transcriptRef = "job:job_activity";

    expect(parseActivityTree(tree)?.root.entries[0]).toMatchObject({
      kind: "shell",
      job: { transcriptRef: "job:job_activity" },
    });
  });

  it("rejects an empty transcriptRef as an incomplete shell job", () => {
    const malformed = cloneWire(VALID_TREE_WIRE);
    getRootShellWire(malformed).transcriptRef = "";

    const tree = parseActivityTree(malformed) as ActivityTree;
    expect(tree.root.entries[0]).toMatchObject({
      kind: "shell",
      job: { jobId: "job_root_1" },
    });
    expect(
      tree.root.entries[0] && "job" in tree.root.entries[0] ? tree.root.entries[0].job.transcriptRef : undefined,
    ).toBeUndefined();
  });

  it("rejects a non-string transcriptRef as an incomplete shell job", () => {
    const malformed = cloneWire(VALID_TREE_WIRE);
    const rootShell = getRootShellWire(malformed) as {
      transcriptRef?: unknown;
    };
    rootShell.transcriptRef = 7;

    const tree = parseActivityTree(malformed) as ActivityTree;
    expect(tree.root.entries[0]).toMatchObject({
      kind: "delegate",
      delegate: { delegateId: "dlg_1" },
    });
    expect(tree.root.branch.error).toBe("incomplete");
  });

  it("parses a recursive wire-true tree with delegates, unavailable children, and truncated branches", () => {
    const tree = parseActivityTree(VALID_TREE_WIRE) as ActivityTree;
    const rootDelegate = getDelegateEntry(tree, 1);
    const unavailable = getDelegateEntry(tree, 1, 2);
    const truncated = getDelegateEntry(tree, 1, 3);

    expect(tree?.revision).toBe(7);
    expect(rootDelegate?.delegate.delegateId).toBe("dlg_1");
    expect(rootDelegate?.delegate.status).toBe("queuedForRetry");
    expect(rootDelegate?.delegate.projectionRevision).toBe(2);
    expect(getDelegateEntry(tree, 1, 1)?.kind).toBe("delegate");
    expect(unavailable?.delegate.branch.error).toBe("child unavailable");
    expect(truncated?.delegate.branch).toEqual({
      truncated: true,
      continuation: "page-2",
    });
  });

  it("preserves unknown status text and authoritative terminal bits", () => {
    const tree = parseActivityTree(VALID_TREE_WIRE) as ActivityTree;
    const delegate = getDelegateEntry(tree, 1)?.delegate;
    expect(delegate?.status).toBe("queuedForRetry");
    expect(delegate?.terminal).toBe(true);
  });

  it("drops malformed siblings while preserving valid siblings and marking the owning session incomplete", () => {
    const tree = parseActivityTree(VALID_TREE_WIRE) as ActivityTree;
    const child = getDelegateEntry(tree, 1)?.delegate.child;
    expect(child?.entries.map((entry: (typeof child.entries)[number]) => entry.kind)).toEqual([
      "shell",
      "delegate",
      "delegate",
      "delegate",
    ]);
    expect(child?.branch.error).toBe("incomplete");
  });

  it("keeps wire-true shell siblings while a malformed sibling still degrades the branch", () => {
    const tree = parseActivityTree(VALID_TREE_WIRE) as ActivityTree;
    const child = getDelegateEntry(tree, 1)?.delegate.child;
    expect(child?.entries.map((entry: (typeof child.entries)[number]) => entry.kind)).toEqual([
      "shell",
      "delegate",
      "delegate",
      "delegate",
    ]);
    expect(child?.branch.error).toBe("incomplete");
  });

  it("returns null when required root identity is invalid", () => {
    const invalid = {
      revision: 7,
      root: {
        ...VALID_TREE_WIRE.root,
        sessionId: 5,
      },
    };
    expect(parseActivityTree(invalid)).toBeNull();
  });

  it("caps recursion at depth 64 and degrades depth 65 to a truncated unavailable branch", () => {
    const tree = parseActivityTree(makeDepthWire(65));
    let session = tree?.root;
    for (let i = 0; i < 64; i += 1) {
      const entry = session?.entries[0];
      expect(entry?.kind).toBe("delegate");
      if (i === 63) {
        const delegate = entry?.kind === "delegate" ? entry.delegate : undefined;
        expect(delegate?.child).toBeUndefined();
        expect(delegate?.branch.truncated).toBe(true);
        expect(delegate?.branch.error).toBe("depth limit exceeded");
      }
      session = entry?.kind === "delegate" ? entry.delegate.child : undefined;
    }
  });

  it("rejects a fractional or negative root revision", () => {
    const fractional = cloneWire(VALID_TREE_WIRE);
    fractional.revision = 7.5;
    expect(parseActivityTree(fractional)).toBeNull();

    const negative = cloneWire(VALID_TREE_WIRE);
    negative.revision = -1;
    expect(parseActivityTree(negative)).toBeNull();
  });

  it("treats fractional nested counts as a malformed branch without erasing valid siblings", () => {
    const malformed = cloneWire(VALID_TREE_WIRE);
    const nestedChild = assertDefined(getNestedDelegateWire(malformed).child, "expected nested child wire session");
    nestedChild.counts.completed = 1.5;

    const tree = parseActivityTree(malformed) as ActivityTree;
    const nested = getDelegateEntry(tree, 1, 1);
    expect(nested?.delegate.child).toBeUndefined();
    expect(nested?.delegate.branch.error).toBe("incomplete");
    expect(getDelegateEntry(tree, 1, 2)?.delegate.branch.error).toBe("child unavailable");
  });

  it("drops a sibling whose stable integer field is fractional", () => {
    const fractional = cloneWire(VALID_TREE_WIRE);
    getMalformedSiblingWire(fractional).projectionRevision = 2.5;
    const fractionalTree = parseActivityTree(fractional) as ActivityTree;
    const fractionalChild = getDelegateEntry(fractionalTree, 1)?.delegate.child;
    expect(fractionalChild?.entries.map((entry: (typeof fractionalChild.entries)[number]) => entry.kind)).toEqual([
      "shell",
      "delegate",
      "delegate",
      "delegate",
    ]);
    expect(fractionalChild?.branch.error).toBe("incomplete");
  });

  it("accepts a signed integer exitCode but rejects a fractional one", () => {
    const signed = cloneWire(VALID_TREE_WIRE);
    getRootShellWire(signed).exitCode = -9;
    expect(parseActivityTree(signed)?.root.entries[0]).toMatchObject({
      kind: "shell",
      job: { exitCode: -9 },
    });

    const malformed = cloneWire(VALID_TREE_WIRE);
    getRootShellWire(malformed).exitCode = 1.5;
    const tree = parseActivityTree(malformed) as ActivityTree;
    expect(tree.root.entries[0]).toMatchObject({ kind: "delegate" });
    expect(tree.root.branch.error).toBe("incomplete");
  });
});

describe("activityNodeID", () => {
  it("builds stable typed ids for sessions, delegates, and jobs", () => {
    expect(activityNodeID({ kind: "session", sessionId: "root" })).toBe("session:root");
    expect(activityNodeID({ kind: "delegate", delegateId: "dlg_1" })).toBe("delegate:dlg_1");
    expect(activityNodeID({ kind: "shell", jobId: "job_1" })).toBe("job:job_1");
  });
});

describe("defaultExpandedIDs", () => {
  it("expands the root and every ancestor of active work, but not completed-only branches", () => {
    const tree = parseActivityTree(VALID_TREE_WIRE) as ActivityTree;
    expect(defaultExpandedIDs(tree)).toEqual(["session:sess_root", "delegate:dlg_1", "session:sess_child"]);
  });
});

describe("reconcileActivityState", () => {
  it("preserves an explicit collapse when an already-active branch stays active", () => {
    const previous = parseActivityTree(VALID_TREE_WIRE) as ActivityTree;
    const next = parseActivityTree({ ...cloneWire(VALID_TREE_WIRE), revision: 8 }) as ActivityTree;

    expect(
      reconcileActivityState(
        {
          expandedIDs: ["session:sess_root"],
          selectedID: undefined,
          selectionPruned: false,
          tree: previous,
        },
        next,
      ),
    ).toEqual({
      expandedIDs: ["session:sess_root"],
      selectedID: undefined,
      selectionPruned: false,
    });
  });

  it("auto-opens only a branch that transitions from inactive to active", () => {
    const previousWire = cloneWire(VALID_TREE_WIRE);
    previousWire.root.aggregate = "completed";
    previousWire.root.counts = { active: 0, failed: 0, completed: 5, complete: true };
    const previousRootDelegate = getRootDelegateWire(previousWire);
    previousRootDelegate.status = "completed";
    previousRootDelegate.terminal = true;
    const previousChild = getChildSessionWire(previousWire);
    previousChild.aggregate = "completed";
    previousChild.counts = { active: 0, failed: 0, completed: 4, complete: true };
    const childEntry = assertDefined(previousChild.entries[0], "expected child shell wire entry");
    if (childEntry.kind !== "shell") throw new Error("expected child shell wire entry");
    const childJob = assertDefined(childEntry.job, "expected child shell job payload");
    childJob.status = "completed";
    childJob.terminal = true;

    const previous = parseActivityTree(previousWire) as ActivityTree;
    const next = parseActivityTree(VALID_TREE_WIRE) as ActivityTree;

    expect(
      reconcileActivityState(
        {
          expandedIDs: ["session:sess_root"],
          selectedID: undefined,
          selectionPruned: false,
          tree: previous,
        },
        next,
      ),
    ).toEqual({
      expandedIDs: ["session:sess_root", "delegate:dlg_1", "session:sess_child"],
      selectedID: undefined,
      selectionPruned: false,
    });
  });

  it("preserves surviving explicit disclosure choices, auto-opens newly active ancestors, and preserves surviving selection", () => {
    const previous = parseActivityTree({
      revision: 1,
      root: {
        sessionId: "sess_root",
        ref: "ref_root",
        label: "Root",
        aggregate: "completed",
        counts: { active: 0, failed: 0, completed: 1, complete: true },
        entries: [
          {
            kind: "delegate",
            delegate: {
              delegateId: "dlg_keep",
              ownerSessionId: "sess_root",
              rootSessionId: "sess_root",
              childSessionId: "sess_keep",
              childRef: "ref_keep",
              transcriptRef: "ref_keep",
              type: "delegate",
              lifecycle: "retained",
              phase: "idle",
              status: "completed",
              projectionRevision: 1,
              terminal: true,
              resumable: false,
              child: {
                sessionId: "sess_keep",
                ref: "ref_keep",
                label: "Keep",
                aggregate: "completed",
                counts: { active: 0, failed: 0, completed: 1, complete: true },
                entries: [],
                branch: branch(),
              },
              branch: branch(),
            },
          },
        ],
        branch: branch(),
      },
    }) as ActivityTree;
    const next = parseActivityTree({
      revision: 2,
      root: {
        sessionId: "sess_root",
        ref: "ref_root",
        label: "Root",
        aggregate: "running",
        counts: { active: 1, failed: 0, completed: 1, complete: false },
        entries: [
          {
            kind: "delegate",
            delegate: {
              delegateId: "dlg_keep",
              ownerSessionId: "sess_root",
              rootSessionId: "sess_root",
              childSessionId: "sess_keep",
              childRef: "ref_keep",
              transcriptRef: "ref_keep",
              type: "delegate",
              lifecycle: "active",
              phase: "running",
              status: "running",
              projectionRevision: 2,
              terminal: false,
              resumable: true,
              child: {
                sessionId: "sess_keep",
                ref: "ref_keep",
                label: "Keep",
                aggregate: "running",
                counts: { active: 1, failed: 0, completed: 1, complete: false },
                entries: [
                  {
                    kind: "delegate",
                    delegate: {
                      delegateId: "dlg_new_active",
                      ownerSessionId: "sess_keep",
                      rootSessionId: "sess_root",
                      childSessionId: "sess_new",
                      childRef: "ref_new",
                      transcriptRef: "ref_new",
                      type: "delegate",
                      lifecycle: "active",
                      phase: "running",
                      status: "running",
                      projectionRevision: 1,
                      terminal: false,
                      resumable: true,
                      runStartedAt: "2026-08-03T00:00:00Z",
                      latestActivityAt: "2026-08-03T00:00:00Z",
                      child: {
                        sessionId: "sess_new",
                        ref: "ref_new",
                        label: "New",
                        aggregate: "running",
                        counts: { active: 1, failed: 0, completed: 0, complete: false },
                        entries: [
                          {
                            kind: "shell",
                            job: {
                              jobId: "job_active_shell",
                              ownerSessionId: "sess_new",
                              ownerRef: "ref_new",
                              type: "shell",
                              status: "running",
                              terminal: false,
                              background: false,
                              hasOutput: false,
                              description: "leaf active shell",
                              startedAt: "2026-08-03T00:01:00Z",
                              outputBytes: 0,
                            },
                          },
                        ],
                        branch: branch(),
                      },
                      branch: branch(),
                    },
                  },
                ],
                branch: branch(),
              },
              branch: branch(),
            },
          },
        ],
        branch: branch(),
      },
    }) as ActivityTree;

    expect(
      reconcileActivityState(
        {
          expandedIDs: ["session:sess_root", "delegate:dlg_keep"],
          selectedID: "session:sess_keep",
          selectionPruned: false,
        },
        next,
      ),
    ).toEqual({
      expandedIDs: [
        "session:sess_root",
        "delegate:dlg_keep",
        "session:sess_keep",
        "delegate:dlg_new_active",
        "session:sess_new",
      ],
      selectedID: "session:sess_keep",
      selectionPruned: false,
    });

    expect(defaultExpandedIDs(previous)).toEqual(["session:sess_root"]);
  });

  it("falls a pruned selection back to the nearest surviving owner and marks selectionPruned", () => {
    const next = parseActivityTree(VALID_TREE_WIRE) as ActivityTree;
    expect(
      reconcileActivityState(
        {
          expandedIDs: ["session:sess_root", "delegate:dlg_1", "session:sess_child"],
          selectedID: "job:job_missing",
          selectionPruned: false,
        },
        next,
      ),
    ).toEqual({
      expandedIDs: ["session:sess_root", "delegate:dlg_1", "session:sess_child"],
      selectedID: "session:sess_root",
      selectionPruned: true,
    });
  });
});

function jobFixture(overrides: Record<string, unknown> = {}) {
  return {
    jobId: "job_1",
    ownerSessionId: "sess_root",
    ownerRef: "ref_root",
    type: "shell",
    status: "running",
    terminal: false,
    background: true,
    hasOutput: true,
    description: "make test-web",
    startedAt: "2026-08-05T15:00:00Z",
    outputBytes: 18432,
    ...overrides,
  };
}

function treeFixture(entries: unknown[]) {
  return {
    revision: 1,
    root: {
      sessionId: "sess_root",
      ref: "ref_root",
      label: "Root",
      aggregate: "working",
      counts: { active: 1, failed: 0, completed: 0, complete: true },
      entries,
      branch: {},
    },
  };
}

describe("lastOutputAt and usage wire fields", () => {
  it("parses lastOutputAt on jobs", () => {
    const tree = parseActivityTree(
      treeFixture([{ kind: "shell", job: jobFixture({ lastOutputAt: "2026-08-05T15:02:11Z" }) }]),
    );
    expect(tree?.root.entries[0]).toMatchObject({
      kind: "shell",
      job: { lastOutputAt: "2026-08-05T15:02:11Z" },
    });
  });

  it("parses usage on delegates", () => {
    const tree = parseActivityTree(
      treeFixture([
        {
          kind: "delegate",
          delegate: {
            delegateId: "dlg_1",
            ownerSessionId: "sess_root",
            rootSessionId: "sess_root",
            childSessionId: "sess_child",
            childRef: "ref_child",
            transcriptRef: "ref_child",
            type: "delegate",
            lifecycle: "retained",
            phase: "idle",
            status: "completed",
            projectionRevision: 1,
            terminal: true,
            resumable: false,
            branch: {},
            usage: { inputTokens: 41200, outputTokens: 6100 },
          },
        },
      ]),
    );
    expect(tree?.root.entries[0]).toMatchObject({
      kind: "delegate",
      delegate: { usage: { inputTokens: 41200, outputTokens: 6100 } },
    });
  });

  it("omits optional activity fields when absent", () => {
    const tree = parseActivityTree(treeFixture([{ kind: "shell", job: jobFixture() }]));
    const entry = tree?.root.entries[0];
    expect(entry?.kind === "shell" && entry.job.lastOutputAt).toBeUndefined();
  });

  it("rejects malformed usage (non-numeric inputTokens) as incomplete", () => {
    const tree = parseActivityTree(
      treeFixture([
        {
          kind: "delegate",
          delegate: {
            delegateId: "dlg_1",
            ownerSessionId: "sess_root",
            rootSessionId: "sess_root",
            childSessionId: "sess_child",
            childRef: "ref_child",
            transcriptRef: "ref_child",
            type: "delegate",
            lifecycle: "retained",
            phase: "idle",
            status: "completed",
            projectionRevision: 1,
            terminal: true,
            resumable: false,
            branch: {},
            usage: { inputTokens: "many", outputTokens: 1 },
          },
        },
      ]),
    );
    // Malformed delegate drops from entries and flags the branch incomplete,
    // matching how other malformed fields behave.
    expect(tree?.root.entries).toHaveLength(0);
    expect(tree?.root.branch.error).toBeDefined();
  });
});
