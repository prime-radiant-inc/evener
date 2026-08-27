// Stable private live projector tests for createLiveActivityProjector —
// maps an ActivityView into a LiveActivityView for live-concept renderers.
// The projector instance owns a private registry: stable keys survive
// hierarchy insertion/reorder/patch. Labels are display-safe inputs only;
// raw diagnostic IDs live only in the private operational map. Duplicate/
// colliding source IDs are detected and produce distinct stable display keys.

import { describe, expect, it } from "vitest";
import type {
  ActivityView,
  RedactedDiagnostic,
  WorkEntry,
} from "../services/activity";
import type { LiveWorkItem } from "./model";
import { createLiveActivityProjector } from "./project-activity";

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
    const byStatus = new Map(live.tasks.map((t) => [t.status, t.count]));
    expect(byStatus.get("active")).toBe(1);
    expect(byStatus.get("open")).toBe(2);
    expect(byStatus.get("done")).toBe(3);
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
    expect(entry?.children[0]?.kind).toBe("job");
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
    const json = JSON.stringify(entry);
    expect(json).not.toContain("job-secret-id");
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
            {
              kind: "job",
              label: "child-job-1",
              tone: "terminal",
            },
            {
              kind: "watch",
              label: "child-watch-1",
              tone: "running",
            },
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

  // --- opaque collision-safe keys --------------------------------------------

  it("uses opaque collision-safe keys — all unique, not raw labels", () => {
    const p = createLiveActivityProjector();
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

  // --- tone mapping covers all values ----------------------------------------

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

  // --- stable keys across reorder / insertion / patch ------------------------

  it("keys are stable across re-projection of identical input", () => {
    const p = createLiveActivityProjector();
    const view = makeActivityView({
      work: [
        {
          kind: "delegate",
          label: "Agent",
          tone: "running",
          children: [{ kind: "job", label: "shell", tone: "terminal" }],
        },
      ],
      usage: { totalTokens: 100 },
    });
    const a = p.project(view);
    const b = p.project(view);
    expect(a.live).toEqual(b.live);
  });

  it("keys survive hierarchy reorder", () => {
    const p = createLiveActivityProjector();
    const original = makeActivityView({
      work: [
        { kind: "job", label: "job-A", tone: "running" },
        { kind: "job", label: "job-B", tone: "terminal" },
        { kind: "delegate", label: "del-C", tone: "running" },
      ],
    });
    const a = p.project(original);
    const keyA = a.live.work[0]?.key;
    const keyB = a.live.work[1]?.key;
    const keyC = a.live.work[2]?.key;

    // Reorder: B, C, A
    const reordered = makeActivityView({
      work: [
        { kind: "job", label: "job-B", tone: "terminal" },
        { kind: "delegate", label: "del-C", tone: "running" },
        { kind: "job", label: "job-A", tone: "running" },
      ],
    });
    const b = p.project(reordered);
    // job-A's key should now be at position 2
    expect(b.live.work[2]?.key).toBe(keyA);
    // job-B's key should now be at position 0
    expect(b.live.work[0]?.key).toBe(keyB);
    // del-C's key should now be at position 1
    expect(b.live.work[1]?.key).toBe(keyC);
  });

  it("keys survive hierarchy insertion (new entry prepended)", () => {
    const p = createLiveActivityProjector();
    const original = makeActivityView({
      work: [
        { kind: "job", label: "job-A", tone: "running" },
        { kind: "job", label: "job-B", tone: "terminal" },
      ],
    });
    const a = p.project(original);
    const keyA = a.live.work[0]?.key;
    const keyB = a.live.work[1]?.key;

    // Insert a new entry at the front
    const withInsert = makeActivityView({
      work: [
        { kind: "delegate", label: "new-del", tone: "running" },
        { kind: "job", label: "job-A", tone: "running" },
        { kind: "job", label: "job-B", tone: "terminal" },
      ],
    });
    const b = p.project(withInsert);
    expect(b.live.work[1]?.key).toBe(keyA);
    expect(b.live.work[2]?.key).toBe(keyB);
    // The new entry gets its own distinct key
    expect(b.live.work[0]?.key).not.toBe(keyA);
    expect(b.live.work[0]?.key).not.toBe(keyB);
  });

  it("keys survive patch (tone change on same entry)", () => {
    const p = createLiveActivityProjector();
    const original = makeActivityView({
      work: [{ kind: "job", label: "job-A", tone: "running" }],
    });
    const a = p.project(original);
    const keyA = a.live.work[0]?.key;

    // Patch: tone changes from running to terminal
    const patched = makeActivityView({
      work: [{ kind: "job", label: "job-A", tone: "terminal" }],
    });
    const b = p.project(patched);
    expect(b.live.work[0]?.key).toBe(keyA);
  });

  it("child keys survive parent reorder", () => {
    const p = createLiveActivityProjector();
    const original = makeActivityView({
      work: [
        {
          kind: "delegate",
          label: "parent",
          tone: "running",
          children: [
            { kind: "job", label: "child-A", tone: "running" },
            { kind: "job", label: "child-B", tone: "terminal" },
          ],
        },
      ],
    });
    const a = p.project(original);
    const childAKey = a.live.work[0]?.children[0]?.key;
    const childBKey = a.live.work[0]?.children[1]?.key;

    // Reorder children
    const reordered = makeActivityView({
      work: [
        {
          kind: "delegate",
          label: "parent",
          tone: "running",
          children: [
            { kind: "job", label: "child-B", tone: "terminal" },
            { kind: "job", label: "child-A", tone: "running" },
          ],
        },
      ],
    });
    const b = p.project(reordered);
    expect(b.live.work[0]?.children[0]?.key).toBe(childBKey);
    expect(b.live.work[0]?.children[1]?.key).toBe(childAKey);
  });

  // --- duplicate / colliding source IDs --------------------------------------

  it("detects duplicate source labels and returns distinct stable display keys", () => {
    // Two entries with the same kind+label (same source identity) but at
    // different positions. The projector must not collapse them — it must
    // produce distinct keys.
    const p = createLiveActivityProjector();
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
    // Keys are stable on re-projection
    const b = p.project(view);
    expect(b.live.work.map((w) => w.key)).toEqual(keys);
  });

  // --- operational map -------------------------------------------------------

  it("returns an operational map alongside the view", () => {
    const p = createLiveActivityProjector();
    const view = makeActivityView({
      work: [
        {
          kind: "delegate",
          label: "Agent",
          tone: "running",
          diagnostics: {
            rawId: "dlg-raw-1",
            operationName: "subagent",
            statusClass: "running",
          } as RedactedDiagnostic,
        },
      ],
    });
    const { live, operational } = p.project(view);
    expect(live).toBeDefined();
    expect(operational).toBeDefined();
    const opaqueKey = live.work[0]?.key;
    expect(opaqueKey).toBeDefined();
    // The operational map maps the opaque key back to the raw diagnostic ID
    expect(operational.keys.get(opaqueKey!)).toBe("dlg-raw-1");
  });

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
            operationName: "shell",
            statusClass: "running",
          } as RedactedDiagnostic,
        },
      ],
    });
    const { live, operational } = p.project(view);
    const liveJson = JSON.stringify(live);
    expect(liveJson).not.toContain("raw-123");
    // The operational map DOES contain the raw ID (it's the private map)
    const opJson = JSON.stringify(Array.from(operational.keys.entries()));
    expect(opJson).toContain("raw-123");
  });

  // --- determinism -----------------------------------------------------------

  it("is deterministic — same input on same instance produces same output", () => {
    const p = createLiveActivityProjector();
    const view = makeActivityView({
      tasks: [{ status: "done", count: 1 }],
      work: [
        {
          kind: "delegate",
          label: "Agent",
          tone: "running",
          children: [{ kind: "job", label: "shell", tone: "terminal" }],
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

  it("different instances produce different keys for the same input", () => {
    const p1 = createLiveActivityProjector();
    const p2 = createLiveActivityProjector();
    const view = makeActivityView({
      work: [{ kind: "job", label: "shell", tone: "running" }],
    });
    const a = p1.project(view);
    const b = p2.project(view);
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
