// Stable private live projector tests for createLiveActivityProjector —
// maps an ActivityView into a LiveActivityView for live-concept renderers.
// The projector instance owns a private registry: stable keys survive
// hierarchy insertion/reorder/patch. Labels are display-safe inputs only;
// raw diagnostic IDs live only in the private operational map. Duplicate/
// colliding source IDs are detected and produce a documented safe error.
//
// Uses a deterministic key allocator to avoid probabilistic assertions.

import { describe, expect, it } from "vitest";
import type {
  ActivityView,
  RedactedDiagnostic,
  WorkEntry,
} from "../services/activity";
import type { LiveWorkItem } from "./model";
import {
  createLiveActivityProjector,
  type OpaqueKeyAllocator,
} from "./project-activity";

// --- fixture helpers ---------------------------------------------------------

const ALL_TRUE_CAPS = {
  send: true,
  steer: true,
  interrupt: true,
  compact: true,
  clear: true,
  forkFromTurn: true,
  shutdown: true,
  changeModel: true,
  queue: true,
  goal: true,
  rename: true,
};

function makeActivityView(over: Partial<ActivityView> = {}): ActivityView {
  return {
    tasks: [],
    work: [],
    usage: {},
    capabilities: ALL_TRUE_CAPS,
    ...over,
  };
}

function deterministicAllocator(prefix: string): OpaqueKeyAllocator {
  let n = 0;
  return () => {
    n += 1;
    return `${prefix}${n}`;
  };
}

// --- tests -------------------------------------------------------------------

describe("createLiveActivityProjector", () => {
  it("maps task groups to live task groups", () => {
    const p = createLiveActivityProjector();
    const view = makeActivityView({
      tasks: [
        { status: "active", count: 1 },
        { status: "open", count: 2 },
        { status: "done", count: 3 },
      ],
    });
    const { live } = p.project(view);
    expect(live.tasks).toHaveLength(3);
  });

  it("maps usage summary fields", () => {
    const p = createLiveActivityProjector();
    const view = makeActivityView({
      usage: {
        totalTokens: 500,
        cost: "$0.05",
        contextPressure: 0.75,
        durationMs: 120_000,
      },
    });
    const { live } = p.project(view);
    expect(live.usage.totalTokens).toBe(500);
    expect(live.usage.cost).toBe("$0.05");
    expect(live.usage.contextPressure).toBe(0.75);
    expect(live.usage.durationMs).toBe(120_000);
  });

  it("maps a delegate work entry with children", () => {
    const p = createLiveActivityProjector();
    const view = makeActivityView({
      work: [
        {
          kind: "delegate",
          label: "Research subagent",
          tone: "running",
          durationMs: 5000,
          children: [
            {
              kind: "job",
              label: "shell",
              tone: "running",
              outputSummary: "2.0 KB",
            },
          ],
          diagnostics: {
            rawId: "dlg-secret-123",
            operationName: "subagent",
            statusClass: "running",
          } as RedactedDiagnostic,
        },
      ],
    });
    const { live } = p.project(view);
    expect(live.work).toHaveLength(1);
    const entry = live.work[0];
    expect(entry?.kind).toBe("delegate");
    expect(entry?.title).toBe("Research subagent");
    expect(entry?.tone).toBe("running");
    expect(entry?.children).toHaveLength(1);
  });

  it("does not expose raw IDs from diagnostics in the live view", () => {
    const p = createLiveActivityProjector();
    const view = makeActivityView({
      work: [
        {
          kind: "job",
          label: "shell",
          tone: "running",
          diagnostics: {
            rawId: "job-secret-id",
            operationName: "shell",
            statusClass: "running",
          } as RedactedDiagnostic,
        },
      ],
    });
    const { live } = p.project(view);
    const entry = live.work[0];
    expect(entry).not.toHaveProperty("rawId");
    expect(entry).not.toHaveProperty("diagnostics");
    expect(JSON.stringify(entry)).not.toContain("job-secret-id");
    expect(entry?.key).not.toBe("shell");
  });

  it("does not expose commands, paths, prompts, profile IDs, or refs", () => {
    const p = createLiveActivityProjector();
    const view = makeActivityView({
      work: [
        {
          kind: "delegate",
          label: "Agent",
          tone: "running",
          diagnostics: {
            rawId: "dlg-1",
            operationName: "subagent",
            statusClass: "running",
            profileId: "redacted",
          } as RedactedDiagnostic,
        },
      ],
    });
    const { live } = p.project(view);
    const json = JSON.stringify(live);
    expect(json).not.toContain("dlg-1");
    expect(json).not.toContain("profileId");
    expect(json).not.toContain("redacted");
  });

  it("maps nested work entries recursively", () => {
    const p = createLiveActivityProjector();
    const view = makeActivityView({
      work: [
        {
          kind: "delegate",
          label: "Parent",
          tone: "running",
          children: [
            { kind: "job", label: "child-job-1", tone: "terminal" },
            { kind: "watch", label: "child-watch-1", tone: "running" },
          ],
        },
      ],
    });
    const { live } = p.project(view);
    const parent = live.work[0];
    expect(parent?.children).toHaveLength(2);
    expect(parent?.children[0]?.kind).toBe("job");
    expect(parent?.children[1]?.kind).toBe("watch");
  });

  // --- opaque collision-safe keys (deterministic) ----------------------------

  it("uses opaque collision-safe keys — all unique, not raw labels", () => {
    const p = createLiveActivityProjector({
      allocator: deterministicAllocator("w"),
    });
    const view = makeActivityView({
      work: [
        { kind: "job", label: "shell", tone: "running" },
        { kind: "job", label: "shell", tone: "terminal" },
        { kind: "delegate", label: "shell", tone: "running" },
      ],
    });
    const { live } = p.project(view);
    const keys = live.work.map((w) => w.key);
    expect(new Set(keys).size).toBe(keys.length);
    for (const key of keys) {
      expect(key).not.toBe("shell");
    }
  });

  // --- tone mapping ----------------------------------------------------------

  it("maps tone values correctly", () => {
    const tones: {
      input: WorkEntry["tone"];
      expected: LiveWorkItem["tone"];
    }[] = [
      { input: "running", expected: "running" },
      { input: "failed", expected: "failed" },
      { input: "terminal", expected: "success" },
      { input: "idle", expected: "idle" },
      { input: "unknown", expected: "unknown" },
    ];
    for (const { input, expected } of tones) {
      const p = createLiveActivityProjector();
      const view = makeActivityView({
        work: [{ kind: "job", label: "test", tone: input }],
      });
      const { live } = p.project(view);
      expect(live.work[0]?.tone).toBe(expected);
    }
  });

  // --- C2: activity identity includes parent path/kind/rawId -----------------

  it("keys survive hierarchy reorder (with diagnostics)", () => {
    const p = createLiveActivityProjector({
      allocator: deterministicAllocator("w"),
    });
    const original = makeActivityView({
      work: [
        {
          kind: "job",
          label: "job-A",
          tone: "running",
          diagnostics: {
            rawId: "raw-A",
            operationName: "j",
            statusClass: "r",
          } as RedactedDiagnostic,
        },
        {
          kind: "job",
          label: "job-B",
          tone: "terminal",
          diagnostics: {
            rawId: "raw-B",
            operationName: "j",
            statusClass: "t",
          } as RedactedDiagnostic,
        },
      ],
    });
    const a = p.project(original);
    const keyA = a.live.work[0]?.key;
    const keyB = a.live.work[1]?.key;

    // Reorder: B, A
    const reordered = makeActivityView({
      work: [
        {
          kind: "job",
          label: "job-B",
          tone: "terminal",
          diagnostics: {
            rawId: "raw-B",
            operationName: "j",
            statusClass: "t",
          } as RedactedDiagnostic,
        },
        {
          kind: "job",
          label: "job-A",
          tone: "running",
          diagnostics: {
            rawId: "raw-A",
            operationName: "j",
            statusClass: "r",
          } as RedactedDiagnostic,
        },
      ],
    });
    const b = p.project(reordered);
    expect(b.live.work[0]?.key).toBe(keyB);
    expect(b.live.work[1]?.key).toBe(keyA);
  });

  it("keys survive hierarchy insertion (new entry prepended)", () => {
    const p = createLiveActivityProjector();
    const original = makeActivityView({
      work: [
        {
          kind: "job",
          label: "job-A",
          tone: "running",
          diagnostics: {
            rawId: "raw-A",
            operationName: "j",
            statusClass: "r",
          } as RedactedDiagnostic,
        },
      ],
    });
    const a = p.project(original);
    const keyA = a.live.work[0]?.key;

    const withInsert = makeActivityView({
      work: [
        {
          kind: "delegate",
          label: "new-del",
          tone: "running",
          diagnostics: {
            rawId: "raw-new",
            operationName: "d",
            statusClass: "r",
          } as RedactedDiagnostic,
        },
        {
          kind: "job",
          label: "job-A",
          tone: "running",
          diagnostics: {
            rawId: "raw-A",
            operationName: "j",
            statusClass: "r",
          } as RedactedDiagnostic,
        },
      ],
    });
    const b = p.project(withInsert);
    expect(b.live.work[1]?.key).toBe(keyA);
    expect(b.live.work[0]?.key).not.toBe(keyA);
  });

  it("keys survive patch (tone change on same entry)", () => {
    const p = createLiveActivityProjector();
    const original = makeActivityView({
      work: [
        {
          kind: "job",
          label: "job-A",
          tone: "running",
          diagnostics: {
            rawId: "raw-A",
            operationName: "j",
            statusClass: "r",
          } as RedactedDiagnostic,
        },
      ],
    });
    const a = p.project(original);
    const keyA = a.live.work[0]?.key;

    const patched = makeActivityView({
      work: [
        {
          kind: "job",
          label: "job-A",
          tone: "terminal",
          diagnostics: {
            rawId: "raw-A",
            operationName: "j",
            statusClass: "t",
          } as RedactedDiagnostic,
        },
      ],
    });
    const b = p.project(patched);
    expect(b.live.work[0]?.key).toBe(keyA);
  });

  it("child keys survive parent reorder", () => {
    const p = createLiveActivityProjector({
      allocator: deterministicAllocator("w"),
    });
    const original = makeActivityView({
      work: [
        {
          kind: "delegate",
          label: "parent",
          tone: "running",
          children: [
            {
              kind: "job",
              label: "child-A",
              tone: "running",
              diagnostics: {
                rawId: "cA",
                operationName: "j",
                statusClass: "r",
              } as RedactedDiagnostic,
            },
            {
              kind: "job",
              label: "child-B",
              tone: "terminal",
              diagnostics: {
                rawId: "cB",
                operationName: "j",
                statusClass: "t",
              } as RedactedDiagnostic,
            },
          ],
        },
      ],
    });
    const a = p.project(original);
    const childAKey = a.live.work[0]?.children[0]?.key;
    const childBKey = a.live.work[0]?.children[1]?.key;

    const reordered = makeActivityView({
      work: [
        {
          kind: "delegate",
          label: "parent",
          tone: "running",
          children: [
            {
              kind: "job",
              label: "child-B",
              tone: "terminal",
              diagnostics: {
                rawId: "cB",
                operationName: "j",
                statusClass: "t",
              } as RedactedDiagnostic,
            },
            {
              kind: "job",
              label: "child-A",
              tone: "running",
              diagnostics: {
                rawId: "cA",
                operationName: "j",
                statusClass: "r",
              } as RedactedDiagnostic,
            },
          ],
        },
      ],
    });
    const b = p.project(reordered);
    expect(b.live.work[0]?.children[0]?.key).toBe(childBKey);
    expect(b.live.work[0]?.children[1]?.key).toBe(childAKey);
  });

  it("same child under different parents gets different keys (no cross-parent aliasing)", () => {
    const p = createLiveActivityProjector({
      allocator: deterministicAllocator("w"),
    });
    const view = makeActivityView({
      work: [
        {
          kind: "delegate",
          label: "parent-1",
          tone: "running",
          children: [
            {
              kind: "job",
              label: "shared-child",
              tone: "running",
              diagnostics: {
                rawId: "shared",
                operationName: "j",
                statusClass: "r",
              } as RedactedDiagnostic,
            },
          ],
        },
        {
          kind: "delegate",
          label: "parent-2",
          tone: "running",
          children: [
            {
              kind: "job",
              label: "shared-child",
              tone: "running",
              diagnostics: {
                rawId: "shared",
                operationName: "j",
                statusClass: "r",
              } as RedactedDiagnostic,
            },
          ],
        },
      ],
    });
    const { live } = p.project(view);
    const child1Key = live.work[0]?.children[0]?.key;
    const child2Key = live.work[1]?.children[0]?.key;
    // Same rawId under different parents → different keys
    expect(child1Key).not.toBe(child2Key);
  });

  // --- C2: duplicate raw IDs → safe error -----------------------------------

  it("duplicate raw IDs produce a safe error", () => {
    const p = createLiveActivityProjector();
    const view = makeActivityView({
      work: [
        {
          kind: "job",
          label: "job-A",
          tone: "running",
          diagnostics: {
            rawId: "dup-id",
            operationName: "j",
            statusClass: "r",
          } as RedactedDiagnostic,
        },
        {
          kind: "job",
          label: "job-B",
          tone: "terminal",
          diagnostics: {
            rawId: "dup-id",
            operationName: "j",
            statusClass: "t",
          } as RedactedDiagnostic,
        },
      ],
    });
    expect(() => p.project(view)).toThrow(/duplicate.*dup-id/i);
  });

  it("indistinguishable duplicate no-ID siblings produce distinct keys", () => {
    const p = createLiveActivityProjector({
      allocator: deterministicAllocator("w"),
    });
    const view = makeActivityView({
      work: [
        { kind: "job", label: "same-label", tone: "running" },
        { kind: "job", label: "same-label", tone: "terminal" },
        { kind: "job", label: "same-label", tone: "idle" },
      ],
    });
    const { live } = p.project(view);
    const keys = live.work.map((w) => w.key);
    expect(new Set(keys).size).toBe(keys.length);
    // Keys are stable on re-projection (same order)
    const b = p.project(view);
    expect(b.live.work.map((w) => w.key)).toEqual(keys);
  });

  // --- I1: snapshot ReadonlyMaps ----------------------------------------------

  it("returns snapshot operational map that cannot corrupt internal state", () => {
    const p = createLiveActivityProjector();
    const view = makeActivityView({
      work: [
        {
          kind: "delegate",
          label: "Agent",
          tone: "running",
          diagnostics: {
            rawId: "dlg-raw-1",
            operationName: "d",
            statusClass: "r",
          } as RedactedDiagnostic,
        },
      ],
    });
    const a = p.project(view);
    const opaqueKey = a.live.work[0]?.key;
    expect(opaqueKey).toBeDefined();

    // Subsequent projection should still work
    const b = p.project(view);
    expect(b.live.work[0]?.key).toBe(opaqueKey);
    expect(b.operational.keys.get(opaqueKey ?? "")).toBe("dlg-raw-1");
  });

  it("mutating returned operational map does not affect subsequent projection", () => {
    const p = createLiveActivityProjector();
    const view = makeActivityView({
      work: [
        {
          kind: "job",
          label: "shell",
          tone: "running",
          diagnostics: {
            rawId: "raw-123",
            operationName: "j",
            statusClass: "r",
          } as RedactedDiagnostic,
        },
      ],
    });
    const a = p.project(view);
    const opaqueKey = a.live.work[0]?.key;

    // Corrupt the snapshot
    (a.operational.keys as Map<string, string>).clear();

    // Subsequent projection unaffected
    const b = p.project(view);
    expect(b.live.work[0]?.key).toBe(opaqueKey);
    expect(b.operational.keys.get(opaqueKey ?? "")).toBe("raw-123");
  });

  // --- I2: reset / dispose ---------------------------------------------------

  it("reset() clears everything", () => {
    const p = createLiveActivityProjector({
      allocator: deterministicAllocator("w"),
    });
    const view = makeActivityView({
      work: [
        {
          kind: "job",
          label: "job-A",
          tone: "running",
          diagnostics: {
            rawId: "raw-A",
            operationName: "j",
            statusClass: "r",
          } as RedactedDiagnostic,
        },
      ],
    });
    const a = p.project(view);
    const keyA = a.live.work[0]?.key;

    p.reset();

    const b = p.project(view);
    expect(b.live.work[0]?.key).not.toBe(keyA);
  });

  it("dispose clears everything", () => {
    const p = createLiveActivityProjector();
    const view = makeActivityView({
      work: [
        {
          kind: "job",
          label: "job-A",
          tone: "running",
          diagnostics: {
            rawId: "raw-A",
            operationName: "j",
            statusClass: "r",
          } as RedactedDiagnostic,
        },
      ],
    });
    const a = p.project(view);
    const keyA = a.live.work[0]?.key;

    p.dispose();

    const b = p.project(view);
    expect(b.live.work[0]?.key).not.toBe(keyA);
  });

  it("registry has safe overflow with bounded size", () => {
    const p = createLiveActivityProjector({
      allocator: deterministicAllocator("w"),
      maxRegistrySize: 3,
    });
    const view = makeActivityView({
      work: [
        {
          kind: "job",
          label: "j1",
          tone: "running",
          diagnostics: {
            rawId: "r1",
            operationName: "j",
            statusClass: "r",
          } as RedactedDiagnostic,
        },
        {
          kind: "job",
          label: "j2",
          tone: "running",
          diagnostics: {
            rawId: "r2",
            operationName: "j",
            statusClass: "r",
          } as RedactedDiagnostic,
        },
        {
          kind: "job",
          label: "j3",
          tone: "running",
          diagnostics: {
            rawId: "r3",
            operationName: "j",
            statusClass: "r",
          } as RedactedDiagnostic,
        },
        {
          kind: "job",
          label: "j4",
          tone: "running",
          diagnostics: {
            rawId: "r4",
            operationName: "j",
            statusClass: "r",
          } as RedactedDiagnostic,
        },
      ],
    });
    // Should not throw — overflow is handled with eviction
    const { live } = p.project(view);
    expect(live.work).toHaveLength(4);
  });

  // --- operational map -------------------------------------------------------

  it("operational map is not serialized with the view", () => {
    const p = createLiveActivityProjector();
    const view = makeActivityView({
      work: [
        {
          kind: "job",
          label: "shell",
          tone: "running",
          diagnostics: {
            rawId: "raw-123",
            operationName: "j",
            statusClass: "r",
          } as RedactedDiagnostic,
        },
      ],
    });
    const { live, operational } = p.project(view);
    const liveJson = JSON.stringify(live);
    expect(liveJson).not.toContain("raw-123");
    const opJson = JSON.stringify(Array.from(operational.keys.entries()));
    expect(opJson).toContain("raw-123");
  });

  // --- determinism / edge cases ---------------------------------------------

  it("is deterministic — same input on same instance produces same output", () => {
    const p = createLiveActivityProjector();
    const view = makeActivityView({
      tasks: [{ status: "done", count: 1 }],
      work: [
        {
          kind: "delegate",
          label: "Agent",
          tone: "running",
          children: [
            {
              kind: "job",
              label: "shell",
              tone: "terminal",
              diagnostics: {
                rawId: "raw-c",
                operationName: "j",
                statusClass: "t",
              } as RedactedDiagnostic,
            },
          ],
          diagnostics: {
            rawId: "raw-p",
            operationName: "d",
            statusClass: "r",
          } as RedactedDiagnostic,
        },
      ],
      usage: { totalTokens: 100 },
    });
    const a = p.project(view);
    const b = p.project(view);
    expect(a.live).toEqual(b.live);
  });

  it("returns empty arrays for empty input", () => {
    const p = createLiveActivityProjector();
    const view = makeActivityView();
    const { live } = p.project(view);
    expect(live.tasks).toEqual([]);
    expect(live.work).toEqual([]);
    expect(live.usage).toEqual({});
  });

  it("different instances produce different keys for the same input (deterministic)", () => {
    const p1 = createLiveActivityProjector({
      allocator: deterministicAllocator("a"),
    });
    const p2 = createLiveActivityProjector({
      allocator: deterministicAllocator("b"),
    });
    const view = makeActivityView({
      work: [
        {
          kind: "job",
          label: "shell",
          tone: "running",
          diagnostics: {
            rawId: "raw-1",
            operationName: "j",
            statusClass: "r",
          } as RedactedDiagnostic,
        },
      ],
    });
    const a = p1.project(view);
    const b = p2.project(view);
    expect(a.live.work[0]?.key).toBe("a1");
    expect(b.live.work[0]?.key).toBe("b1");
    expect(a.live.work[0]?.key).not.toBe(b.live.work[0]?.key);
  });

  // --- serialization safety --------------------------------------------------

  it("serialized view JSON contains no raw operational strings", () => {
    const p = createLiveActivityProjector();
    const view = makeActivityView({
      work: [
        {
          kind: "delegate",
          label: "Agent",
          tone: "running",
          children: [
            {
              kind: "job",
              label: "shell",
              tone: "running",
              diagnostics: {
                rawId: "job-raw-secret",
                operationName: "shell",
                statusClass: "running",
              } as RedactedDiagnostic,
            },
          ],
          diagnostics: {
            rawId: "dlg-raw-secret",
            operationName: "subagent",
            statusClass: "running",
            profileId: "redacted",
          } as RedactedDiagnostic,
        },
      ],
    });
    const { live } = p.project(view);
    const json = JSON.stringify(live);
    expect(json).not.toContain("dlg-raw-secret");
    expect(json).not.toContain("job-raw-secret");
    expect(json).not.toContain("redacted");
    expect(json).not.toContain("profileId");
  });
});
