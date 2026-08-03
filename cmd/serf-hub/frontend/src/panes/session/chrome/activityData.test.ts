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
        kind: "job",
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
          childSessionId: "sess_child",
          childRef: "ref_child",
          mandate: "inspect branch",
          turns: [
            {
              jobId: "job_delegate_1",
              ownerSessionId: "sess_root",
              ownerRef: "ref_root",
              type: "delegate",
              status: "running",
              terminal: false,
              background: true,
              hasOutput: false,
              description: "delegate turn 1",
              startedAt: "2026-08-03T00:02:00Z",
              outputBytes: 0,
            },
            {
              jobId: "job_delegate_2",
              ownerSessionId: "sess_root",
              ownerRef: "ref_root",
              type: "delegate",
              status: "queuedForRetry",
              terminal: true,
              background: true,
              hasOutput: false,
              description: "delegate turn 2",
              startedAt: "2026-08-03T00:03:00Z",
              endedAt: "2026-08-03T00:04:00Z",
              outputBytes: 0,
            },
          ],
          child: {
            sessionId: "sess_child",
            ref: "ref_child",
            label: "Child session",
            aggregate: "running",
            counts: { active: 1, failed: 0, completed: 1, complete: false },
            entries: [
              {
                kind: "job",
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
                  childSessionId: "sess_leaf",
                  childRef: "ref_leaf",
                  turns: [
                    {
                      jobId: "job_nested_delegate",
                      ownerSessionId: "sess_child",
                      ownerRef: "ref_child",
                      type: "delegate",
                      status: "completed",
                      terminal: true,
                      background: true,
                      hasOutput: false,
                      description: "nested delegate turn",
                      startedAt: "2026-08-03T00:06:00Z",
                      endedAt: "2026-08-03T00:07:00Z",
                      outputBytes: 0,
                    },
                  ],
                  child: {
                    sessionId: "sess_leaf",
                    ref: "ref_leaf",
                    label: "Leaf session",
                    aggregate: "completed",
                    counts: { active: 0, failed: 0, completed: 1, complete: true },
                    entries: [
                      {
                        kind: "job",
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
                  childSessionId: "sess_missing",
                  childRef: "ref_missing",
                  turns: [],
                  branch: { error: "child unavailable" },
                },
              },
              {
                kind: "delegate",
                delegate: {
                  delegateId: "dlg_truncated",
                  childSessionId: "sess_truncated",
                  childRef: "ref_truncated",
                  turns: [],
                  branch: { truncated: true, continuation: "page-2" },
                },
              },
              {
                kind: "delegate",
                delegate: {
                  delegateId: "dlg_bad_sibling",
                  childSessionId: "sess_bad",
                  childRef: "ref_bad",
                  turns: [
                    {
                      jobId: 5,
                      ownerSessionId: "sess_child",
                      ownerRef: "ref_child",
                      type: "delegate",
                      status: "failed",
                      terminal: true,
                      background: true,
                      hasOutput: false,
                      description: "broken turn",
                      startedAt: "2026-08-03T00:10:00Z",
                      outputBytes: 0,
                    },
                  ],
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
            childSessionId: session.sessionId,
            childRef: session.ref,
            turns: [
              {
                jobId: `job_${level}`,
                ownerSessionId: `sess_${level}`,
                ownerRef: `ref_${level}`,
                type: "delegate",
                status: "running",
                terminal: false,
                background: true,
                hasOutput: false,
                description: `delegate ${level}`,
                startedAt: "2026-08-03T00:00:00Z",
                outputBytes: 0,
              },
            ],
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

  it("parses a recursive wire-true tree with delegates, unavailable children, and truncated branches", () => {
    const tree = parseActivityTree(VALID_TREE_WIRE) as ActivityTree;
    const rootDelegate = getDelegateEntry(tree, 1);
    const unavailable = getDelegateEntry(tree, 1, 2);
    const truncated = getDelegateEntry(tree, 1, 3);

    expect(tree?.revision).toBe(7);
    expect(rootDelegate?.delegate.delegateId).toBe("dlg_1");
    expect(rootDelegate?.delegate.turns).toHaveLength(2);
    expect(rootDelegate?.delegate.turns[1]?.status).toBe("queuedForRetry");
    expect(getDelegateEntry(tree, 1, 1)?.kind).toBe("delegate");
    expect(unavailable?.delegate.branch.error).toBe("child unavailable");
    expect(truncated?.delegate.branch).toEqual({
      truncated: true,
      continuation: "page-2",
    });
  });

  it("preserves unknown status text and authoritative terminal bits", () => {
    const tree = parseActivityTree(VALID_TREE_WIRE) as ActivityTree;
    const delegateTurn = getDelegateEntry(tree, 1)?.delegate.turns[1];
    expect(delegateTurn?.status).toBe("queuedForRetry");
    expect(delegateTurn?.terminal).toBe(true);
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
              childSessionId: "sess_keep",
              childRef: "ref_keep",
              turns: [],
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
              childSessionId: "sess_keep",
              childRef: "ref_keep",
              turns: [],
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
                      childSessionId: "sess_new",
                      childRef: "ref_new",
                      turns: [
                        {
                          jobId: "job_active",
                          ownerSessionId: "sess_keep",
                          ownerRef: "ref_keep",
                          type: "delegate",
                          status: "running",
                          terminal: false,
                          background: true,
                          hasOutput: false,
                          description: "active delegate turn",
                          startedAt: "2026-08-03T00:00:00Z",
                          outputBytes: 0,
                        },
                      ],
                      child: {
                        sessionId: "sess_new",
                        ref: "ref_new",
                        label: "New",
                        aggregate: "running",
                        counts: { active: 1, failed: 0, completed: 0, complete: false },
                        entries: [
                          {
                            kind: "job",
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
