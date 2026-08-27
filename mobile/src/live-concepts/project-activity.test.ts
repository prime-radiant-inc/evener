// Pure projection tests for projectLiveActivity — maps an ActivityView into a
// LiveActivityView for live-concept renderers. The projection is pure: same
// input → same output, no DOM, no network, no clock.
//
// Covers: task groups map directly; nested work (delegates, jobs, watches)
// projects without raw IDs, commands, paths, prompts, profile IDs, or refs.
// The live view carries only display-safe metadata: tone, title, detail
// summary — never raw identifiers or operational payloads.

import { describe, expect, it } from "vitest";
import type {
  ActivityView,
  RedactedDiagnostic,
  WorkEntry,
} from "../services/activity";
import type { LiveWorkItem } from "./model";
import { projectLiveActivity } from "./project-activity";

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

describe("projectLiveActivity", () => {
  it("maps task groups to live task groups", () => {
    const view = makeActivityView({
      tasks: [
        { status: "active", count: 1 },
        { status: "open", count: 2 },
        { status: "done", count: 3 },
      ],
    });
    const live = projectLiveActivity(view);
    expect(live.tasks).toHaveLength(3);
    const byStatus = new Map(live.tasks.map((t) => [t.status, t.count]));
    expect(byStatus.get("active")).toBe(1);
    expect(byStatus.get("open")).toBe(2);
    expect(byStatus.get("done")).toBe(3);
  });

  it("maps usage summary fields", () => {
    const view = makeActivityView({
      usage: {
        totalTokens: 500,
        cost: "$0.05",
        contextPressure: 0.75,
        durationMs: 120_000,
      },
    });
    const live = projectLiveActivity(view);
    expect(live.usage.totalTokens).toBe(500);
    expect(live.usage.cost).toBe("$0.05");
    expect(live.usage.contextPressure).toBe(0.75);
    expect(live.usage.durationMs).toBe(120_000);
  });

  it("maps a delegate work entry with children", () => {
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
    const live = projectLiveActivity(view);
    expect(live.work).toHaveLength(1);
    const entry = live.work[0];
    expect(entry?.kind).toBe("delegate");
    expect(entry?.title).toBe("Research subagent");
    expect(entry?.tone).toBe("running");
    expect(entry?.children).toHaveLength(1);
    expect(entry?.children[0]?.kind).toBe("job");
  });

  it("does not expose raw IDs from diagnostics in the live view", () => {
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
    const live = projectLiveActivity(view);
    const entry = live.work[0];
    // The live view should not carry raw IDs
    expect(entry).not.toHaveProperty("rawId");
    expect(entry).not.toHaveProperty("diagnostics");
    // Check that the serialized JSON does not contain the raw ID
    const json = JSON.stringify(entry);
    expect(json).not.toContain("job-secret-id");
    // C2: keys should be opaque — not the raw label
    expect(entry?.key).not.toBe("shell");
  });

  it("does not expose commands, paths, prompts, profile IDs, or refs", () => {
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
    const live = projectLiveActivity(view);
    const json = JSON.stringify(live);
    // None of these operational identifiers should leak
    expect(json).not.toContain("dlg-1");
    expect(json).not.toContain("profileId");
    expect(json).not.toContain("redacted");
  });

  it("maps nested work entries recursively", () => {
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
    const live = projectLiveActivity(view);
    const parent = live.work[0];
    expect(parent?.children).toHaveLength(2);
    expect(parent?.children[0]?.kind).toBe("job");
    expect(parent?.children[1]?.kind).toBe("watch");
  });

  it("uses opaque collision-safe keys (C2/I6)", () => {
    const view = makeActivityView({
      work: [
        { kind: "job", label: "shell", tone: "running" },
        { kind: "job", label: "shell", tone: "terminal" },
        { kind: "delegate", label: "shell", tone: "running" },
      ],
    });
    const live = projectLiveActivity(view);
    const keys = live.work.map((w) => w.key);
    // All keys must be unique (collision-safe)
    expect(new Set(keys).size).toBe(keys.length);
    // Keys must NOT be raw labels
    for (const key of keys) {
      expect(key).not.toBe("shell");
    }
  });

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
      const view = makeActivityView({
        work: [{ kind: "job", label: "test", tone: input }],
      });
      const live = projectLiveActivity(view);
      expect(live.work[0]?.tone).toBe(expected);
    }
  });

  it("is deterministic — same input produces same output", () => {
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
    const a = projectLiveActivity(view);
    const b = projectLiveActivity(view);
    expect(a).toEqual(b);
  });

  it("returns empty arrays for empty input", () => {
    const view = makeActivityView();
    const live = projectLiveActivity(view);
    expect(live.tasks).toEqual([]);
    expect(live.work).toEqual([]);
    expect(live.usage).toEqual({});
  });
});
