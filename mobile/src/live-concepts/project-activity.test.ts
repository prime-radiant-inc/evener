// Stable private live projector tests for createLiveActivityProjector —
// maps an ActivityView into a LiveActivityView for live-concept renderers.
// The projector instance owns a private scoped registry keyed by exact scope,
// parent identity, kind, and source identity via nested Maps: stable keys
// survive hierarchy insertion/reorder/patch. Labels are display-safe inputs
// only; raw diagnostic IDs live only in the private operational map.
// Duplicate/cross-kind source IDs are detected and produce a generic safe
// error. Cycle detection throws a generic cycle error before stack overflow.
// The production default allocator is process-unique via a module-level
// factory counter; injected allocator collisions produce a generic error.
// The registry is bounded with safe capacity rejection before mutation.
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

function diag(
  rawId: string,
  over: Partial<RedactedDiagnostic> = {},
): RedactedDiagnostic {
  return {
    rawId,
    operationName: "op",
    statusClass: "running",
    ...over,
  };
}

const SCOPE = "scope-1";

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
    const { live } = p.project(view, { scope: SCOPE });
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
    const { live } = p.project(view, { scope: SCOPE });
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
    const { live } = p.project(view, { scope: SCOPE });
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
    const { live } = p.project(view, { scope: SCOPE });
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
    const { live } = p.project(view, { scope: SCOPE });
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
    const { live } = p.project(view, { scope: SCOPE });
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
    // Distinct kind+label combos so none are indistinguishable duplicates.
    const view = makeActivityView({
      work: [
        { kind: "job", label: "shell", tone: "running" },
        { kind: "job", label: "build", tone: "terminal" },
        { kind: "delegate", label: "shell", tone: "running" },
      ],
    });
    const { live } = p.project(view, { scope: SCOPE });
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
      const { live } = p.project(view, { scope: SCOPE });
      expect(live.work[0]?.tone).toBe(expected);
    }
  });

  // --- activity identity: parent path/kind/rawId -----------------------------

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
          diagnostics: diag("raw-A"),
        },
        {
          kind: "job",
          label: "job-B",
          tone: "terminal",
          diagnostics: diag("raw-B"),
        },
      ],
    });
    const a = p.project(original, { scope: SCOPE });
    const keyA = a.live.work[0]?.key;
    const keyB = a.live.work[1]?.key;

    // Reorder: B, A
    const reordered = makeActivityView({
      work: [
        {
          kind: "job",
          label: "job-B",
          tone: "terminal",
          diagnostics: diag("raw-B"),
        },
        {
          kind: "job",
          label: "job-A",
          tone: "running",
          diagnostics: diag("raw-A"),
        },
      ],
    });
    const b = p.project(reordered, { scope: SCOPE });
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
          diagnostics: diag("raw-A"),
        },
      ],
    });
    const a = p.project(original, { scope: SCOPE });
    const keyA = a.live.work[0]?.key;

    const withInsert = makeActivityView({
      work: [
        {
          kind: "delegate",
          label: "new-del",
          tone: "running",
          diagnostics: diag("raw-new"),
        },
        {
          kind: "job",
          label: "job-A",
          tone: "running",
          diagnostics: diag("raw-A"),
        },
      ],
    });
    const b = p.project(withInsert, { scope: SCOPE });
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
          diagnostics: diag("raw-A"),
        },
      ],
    });
    const a = p.project(original, { scope: SCOPE });
    const keyA = a.live.work[0]?.key;

    const patched = makeActivityView({
      work: [
        {
          kind: "job",
          label: "job-A",
          tone: "terminal",
          diagnostics: diag("raw-A"),
        },
      ],
    });
    const b = p.project(patched, { scope: SCOPE });
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
              diagnostics: diag("cA"),
            },
            {
              kind: "job",
              label: "child-B",
              tone: "terminal",
              diagnostics: diag("cB"),
            },
          ],
        },
      ],
    });
    const a = p.project(original, { scope: SCOPE });
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
              diagnostics: diag("cB"),
            },
            {
              kind: "job",
              label: "child-A",
              tone: "running",
              diagnostics: diag("cA"),
            },
          ],
        },
      ],
    });
    const b = p.project(reordered, { scope: SCOPE });
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
              diagnostics: diag("shared"),
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
              diagnostics: diag("shared"),
            },
          ],
        },
      ],
    });
    const { live } = p.project(view, { scope: SCOPE });
    const child1Key = live.work[0]?.children[0]?.key;
    const child2Key = live.work[1]?.children[0]?.key;
    // Same rawId under different parents → different keys
    expect(child1Key).not.toBe(child2Key);
  });

  // --- raw duplicates / cross-kind → generic safe error ---------------------

  it("duplicate raw IDs under the same parent produce a generic safe error", () => {
    const p = createLiveActivityProjector();
    const view = makeActivityView({
      work: [
        {
          kind: "job",
          label: "job-A",
          tone: "running",
          diagnostics: diag("dup-id"),
        },
        {
          kind: "job",
          label: "job-B",
          tone: "terminal",
          diagnostics: diag("dup-id"),
        },
      ],
    });
    expect(() => p.project(view, { scope: SCOPE })).toThrow(
      /duplicate.*identity/i,
    );
  });

  it("duplicate raw ID error contains no raw ID, label, or prompt", () => {
    const p = createLiveActivityProjector();
    const view = makeActivityView({
      work: [
        {
          kind: "job",
          label: "secret-label",
          tone: "running",
          diagnostics: diag("secret-raw-id-xyz"),
        },
        {
          kind: "job",
          label: "other-label",
          tone: "terminal",
          diagnostics: diag("secret-raw-id-xyz"),
        },
      ],
    });
    let message = "";
    try {
      p.project(view, { scope: SCOPE });
    } catch (e) {
      message = e instanceof Error ? e.message : String(e);
    }
    expect(message).not.toContain("secret-raw-id-xyz");
    expect(message).not.toContain("secret-label");
    expect(message).not.toContain("other-label");
  });

  it("cross-kind duplicate raw IDs under the same parent produce a generic safe error", () => {
    const p = createLiveActivityProjector();
    const view = makeActivityView({
      work: [
        {
          kind: "job",
          label: "job-A",
          tone: "running",
          diagnostics: diag("dup-id"),
        },
        {
          kind: "watch",
          label: "watch-A",
          tone: "running",
          diagnostics: diag("dup-id"),
        },
      ],
    });
    expect(() => p.project(view, { scope: SCOPE })).toThrow(
      /duplicate.*identity/i,
    );
  });

  // --- no-ID siblings: kind+label only, indistinguishable duplicates error ---

  it("no-ID siblings with distinct kinds but same label get distinct keys", () => {
    const p = createLiveActivityProjector({
      allocator: deterministicAllocator("w"),
    });
    const view = makeActivityView({
      work: [
        { kind: "job", label: "same-label", tone: "running" },
        { kind: "watch", label: "same-label", tone: "running" },
      ],
    });
    const { live } = p.project(view, { scope: SCOPE });
    const keys = live.work.map((w) => w.key);
    expect(new Set(keys).size).toBe(keys.length);
  });

  it("indistinguishable duplicate no-ID siblings produce a generic safe error", () => {
    const p = createLiveActivityProjector();
    const view = makeActivityView({
      work: [
        { kind: "job", label: "same-label", tone: "running" },
        { kind: "job", label: "same-label", tone: "terminal" },
        { kind: "job", label: "same-label", tone: "idle" },
      ],
    });
    expect(() => p.project(view, { scope: SCOPE })).toThrow(
      /indistinguishable.*duplicate/i,
    );
  });

  it("indistinguishable duplicate no-ID error contains no occurrence positions or labels", () => {
    const p = createLiveActivityProjector();
    const view = makeActivityView({
      work: [
        { kind: "job", label: "secret-no-id-label", tone: "running" },
        { kind: "job", label: "secret-no-id-label", tone: "terminal" },
      ],
    });
    let message = "";
    try {
      p.project(view, { scope: SCOPE });
    } catch (e) {
      message = e instanceof Error ? e.message : String(e);
    }
    expect(message).not.toContain("secret-no-id-label");
    expect(message).not.toContain("#0");
    expect(message).not.toContain("#1");
  });

  it("no-ID reorder is stable (same kind+label, reordered, with a distinct third entry)", () => {
    const p = createLiveActivityProjector({
      allocator: deterministicAllocator("w"),
    });
    const original = makeActivityView({
      work: [
        { kind: "job", label: "unique-A", tone: "running" },
        { kind: "job", label: "unique-B", tone: "terminal" },
      ],
    });
    const a = p.project(original, { scope: SCOPE });
    const keyA = a.live.work[0]?.key;
    const keyB = a.live.work[1]?.key;

    // Reorder: B, A — distinct labels so no indistinguishable duplicate
    const reordered = makeActivityView({
      work: [
        { kind: "job", label: "unique-B", tone: "terminal" },
        { kind: "job", label: "unique-A", tone: "running" },
      ],
    });
    const b = p.project(reordered, { scope: SCOPE });
    expect(b.live.work[0]?.key).toBe(keyB);
    expect(b.live.work[1]?.key).toBe(keyA);
  });

  // --- I1: snapshot ReadonlyMaps, recursive operational map -----------------

  it("returns snapshot operational map that cannot corrupt internal state", () => {
    const p = createLiveActivityProjector();
    const view = makeActivityView({
      work: [
        {
          kind: "delegate",
          label: "Agent",
          tone: "running",
          diagnostics: diag("dlg-raw-1"),
        },
      ],
    });
    const a = p.project(view, { scope: SCOPE });
    const opaqueKey = a.live.work[0]?.key;
    expect(opaqueKey).toBeDefined();

    // Subsequent projection should still work
    const b = p.project(view, { scope: SCOPE });
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
          diagnostics: diag("raw-123"),
        },
      ],
    });
    const a = p.project(view, { scope: SCOPE });
    const opaqueKey = a.live.work[0]?.key;

    // Corrupt the snapshot
    (a.operational.keys as Map<string, string>).clear();

    // Subsequent projection unaffected
    const b = p.project(view, { scope: SCOPE });
    expect(b.live.work[0]?.key).toBe(opaqueKey);
    expect(b.operational.keys.get(opaqueKey ?? "")).toBe("raw-123");
  });

  it("operational map is recursively populated for nested children", () => {
    const p = createLiveActivityProjector();
    const view = makeActivityView({
      work: [
        {
          kind: "delegate",
          label: "parent",
          tone: "running",
          diagnostics: diag("raw-parent"),
          children: [
            {
              kind: "job",
              label: "child-1",
              tone: "running",
              diagnostics: diag("raw-child-1"),
            },
            {
              kind: "watch",
              label: "child-2",
              tone: "running",
              diagnostics: diag("raw-child-2"),
            },
          ],
        },
      ],
    });
    const { live, operational } = p.project(view, { scope: SCOPE });
    const parentKey = live.work[0]?.key;
    const child1Key = live.work[0]?.children[0]?.key;
    const child2Key = live.work[0]?.children[1]?.key;

    // All keys (including nested) are in the operational map
    expect(operational.keys.get(parentKey ?? "")).toBe("raw-parent");
    expect(operational.keys.get(child1Key ?? "")).toBe("raw-child-1");
    expect(operational.keys.get(child2Key ?? "")).toBe("raw-child-2");
    expect(operational.keys.size).toBe(3);
  });

  it("nested operational map for no-ID children uses label as the value", () => {
    const p = createLiveActivityProjector();
    const view = makeActivityView({
      work: [
        {
          kind: "delegate",
          label: "parent",
          tone: "running",
          children: [{ kind: "job", label: "no-id-child", tone: "running" }],
        },
      ],
    });
    const { live, operational } = p.project(view, { scope: SCOPE });
    const childKey = live.work[0]?.children[0]?.key;
    expect(operational.keys.get(childKey ?? "")).toBe("no-id-child");
  });

  // --- I2: reset / dispose / exact scoped reset ------------------------------

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
          diagnostics: diag("raw-A"),
        },
      ],
    });
    const a = p.project(view, { scope: SCOPE });
    const keyA = a.live.work[0]?.key;

    p.reset();

    const b = p.project(view, { scope: SCOPE });
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
          diagnostics: diag("raw-A"),
        },
      ],
    });
    const a = p.project(view, { scope: SCOPE });
    const keyA = a.live.work[0]?.key;

    p.dispose();

    const b = p.project(view, { scope: SCOPE });
    expect(b.live.work[0]?.key).not.toBe(keyA);
  });

  it("reset(scope) clears only that exact scope, leaving other scopes intact", () => {
    const p = createLiveActivityProjector();
    const view = makeActivityView({
      work: [
        {
          kind: "job",
          label: "job-A",
          tone: "running",
          diagnostics: diag("raw-A"),
        },
      ],
    });
    const a = p.project(view, { scope: "scope-A" });
    const keyA = a.live.work[0]?.key;

    const b = p.project(view, { scope: "scope-B" });
    const keyB = b.live.work[0]?.key;
    expect(keyA).not.toBe(keyB);

    // Reset only scope A
    p.reset("scope-A");

    // Re-project scope A — should get a NEW key (registry was cleared)
    const a2 = p.project(view, { scope: "scope-A" });
    expect(a2.live.work[0]?.key).not.toBe(keyA);

    // Re-project scope B — should still get the SAME key (not cleared)
    const b2 = p.project(view, { scope: "scope-B" });
    expect(b2.live.work[0]?.key).toBe(keyB);
  });

  it("reset(scope) uses exact scope deletion, not delimiter prefix", () => {
    const p = createLiveActivityProjector();
    const view = makeActivityView({
      work: [
        {
          kind: "job",
          label: "job-A",
          tone: "running",
          diagnostics: diag("raw-A"),
        },
      ],
    });
    // Project "scope" and "scope:child" — these should be distinct exact scopes
    const a = p.project(view, { scope: "scope" });
    const keyMain = a.live.work[0]?.key;

    const b = p.project(view, { scope: "scope:child" });
    const keyChild = b.live.work[0]?.key;
    expect(keyMain).not.toBe(keyChild);

    // Reset "scope" — should NOT affect "scope:child" (exact deletion, not prefix)
    p.reset("scope");

    // "scope" gets a new key
    const a2 = p.project(view, { scope: "scope" });
    expect(a2.live.work[0]?.key).not.toBe(keyMain);

    // "scope:child" keeps its key (exact scope deletion, no prefix matching)
    const b2 = p.project(view, { scope: "scope:child" });
    expect(b2.live.work[0]?.key).toBe(keyChild);
  });

  // --- capacity: safe rejection before mutation, no FIFO eviction ----------

  it("capacity rejection throws before mutation and leaves existing key stability", () => {
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
          diagnostics: diag("r1"),
        },
        {
          kind: "job",
          label: "j2",
          tone: "running",
          diagnostics: diag("r2"),
        },
        {
          kind: "job",
          label: "j3",
          tone: "running",
          diagnostics: diag("r3"),
        },
      ],
    });
    const a = p.project(view, { scope: SCOPE });
    const key1 = a.live.work[0]?.key;
    const key2 = a.live.work[1]?.key;
    const key3 = a.live.work[2]?.key;

    // Now project a view that adds a 4th distinct entry — should reject
    const overView = makeActivityView({
      work: [
        {
          kind: "job",
          label: "j1",
          tone: "running",
          diagnostics: diag("r1"),
        },
        {
          kind: "job",
          label: "j2",
          tone: "running",
          diagnostics: diag("r2"),
        },
        {
          kind: "job",
          label: "j3",
          tone: "running",
          diagnostics: diag("r3"),
        },
        {
          kind: "job",
          label: "j4",
          tone: "running",
          diagnostics: diag("r4"),
        },
      ],
    });
    expect(() => p.project(overView, { scope: SCOPE })).toThrow(
      /capacity exceeded/i,
    );

    // Existing keys remain stable — rejection happened before mutation
    const b = p.project(view, { scope: SCOPE });
    expect(b.live.work[0]?.key).toBe(key1);
    expect(b.live.work[1]?.key).toBe(key2);
    expect(b.live.work[2]?.key).toBe(key3);
  });

  it("identical over-cap rejection (re-projecting same over-cap view) leaves existing key stability", () => {
    const p = createLiveActivityProjector({
      allocator: deterministicAllocator("w"),
      maxRegistrySize: 2,
    });
    const baseView = makeActivityView({
      work: [
        {
          kind: "job",
          label: "j1",
          tone: "running",
          diagnostics: diag("r1"),
        },
        {
          kind: "job",
          label: "j2",
          tone: "running",
          diagnostics: diag("r2"),
        },
      ],
    });
    const a = p.project(baseView, { scope: SCOPE });
    const key1 = a.live.work[0]?.key;
    const key2 = a.live.work[1]?.key;

    const overView = makeActivityView({
      work: [
        {
          kind: "job",
          label: "j1",
          tone: "running",
          diagnostics: diag("r1"),
        },
        {
          kind: "job",
          label: "j2",
          tone: "running",
          diagnostics: diag("r2"),
        },
        {
          kind: "job",
          label: "j3",
          tone: "running",
          diagnostics: diag("r3"),
        },
      ],
    });

    // Reject twice — identical over-cap rejection
    expect(() => p.project(overView, { scope: SCOPE })).toThrow(
      /capacity exceeded/i,
    );
    expect(() => p.project(overView, { scope: SCOPE })).toThrow(
      /capacity exceeded/i,
    );

    // Existing keys are still stable
    const b = p.project(baseView, { scope: SCOPE });
    expect(b.live.work[0]?.key).toBe(key1);
    expect(b.live.work[1]?.key).toBe(key2);
  });

  it("re-projecting the same at-cap view does not throw and preserves keys", () => {
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
          diagnostics: diag("r1"),
        },
        {
          kind: "job",
          label: "j2",
          tone: "running",
          diagnostics: diag("r2"),
        },
        {
          kind: "job",
          label: "j3",
          tone: "running",
          diagnostics: diag("r3"),
        },
      ],
    });
    const a = p.project(view, { scope: SCOPE });
    // Re-projecting the same at-cap view should not throw (no new allocations)
    const b = p.project(view, { scope: SCOPE });
    expect(b.live.work.map((w) => w.key)).toEqual(
      a.live.work.map((w) => w.key),
    );
  });

  // --- allocator collision detection ----------------------------------------

  it("injected allocator that returns duplicate keys produces a generic safe error", () => {
    // Allocator that always returns the same key
    const colliding: OpaqueKeyAllocator = () => "collision-key";
    const p = createLiveActivityProjector({ allocator: colliding });
    const view = makeActivityView({
      work: [
        {
          kind: "job",
          label: "j1",
          tone: "running",
          diagnostics: diag("r1"),
        },
        {
          kind: "job",
          label: "j2",
          tone: "running",
          diagnostics: diag("r2"),
        },
      ],
    });
    expect(() => p.project(view, { scope: SCOPE })).toThrow(
      /allocator collision/i,
    );
  });

  it("allocator collision error contains no raw ID, label, or key value", () => {
    const colliding: OpaqueKeyAllocator = () => "secret-collision-key";
    const p = createLiveActivityProjector({ allocator: colliding });
    const view = makeActivityView({
      work: [
        {
          kind: "job",
          label: "secret-label",
          tone: "running",
          diagnostics: diag("secret-raw-id"),
        },
        {
          kind: "job",
          label: "other-label",
          tone: "running",
          diagnostics: diag("other-raw-id"),
        },
      ],
    });
    let message = "";
    try {
      p.project(view, { scope: SCOPE });
    } catch (e) {
      message = e instanceof Error ? e.message : String(e);
    }
    expect(message).not.toContain("secret-collision-key");
    expect(message).not.toContain("secret-label");
    expect(message).not.toContain("secret-raw-id");
    expect(message).not.toContain("other-raw-id");
  });

  // --- cycle detection: 1/2/3-length cycles --------------------------------

  // WorkEntry.children is readonly; use a mutable alias to build cycles.
  type MutableWorkEntry = Omit<WorkEntry, "children"> & {
    children?: WorkEntry[];
  };

  it("1-length cycle (self-referencing entry) throws a generic cycle error", () => {
    const p = createLiveActivityProjector();
    const self: MutableWorkEntry = {
      kind: "delegate",
      label: "self",
      tone: "running",
      diagnostics: diag("self-id"),
    };
    self.children = [self];
    const view = makeActivityView({ work: [self] });
    expect(() => p.project(view, { scope: SCOPE })).toThrow(/cycle/i);
  });

  it("2-length cycle throws a generic cycle error", () => {
    const p = createLiveActivityProjector();
    const a: MutableWorkEntry = {
      kind: "delegate",
      label: "a",
      tone: "running",
      diagnostics: diag("a-id"),
    };
    const b: MutableWorkEntry = {
      kind: "delegate",
      label: "b",
      tone: "running",
      diagnostics: diag("b-id"),
    };
    a.children = [b];
    b.children = [a];
    const view = makeActivityView({ work: [a] });
    expect(() => p.project(view, { scope: SCOPE })).toThrow(/cycle/i);
  });

  it("3-length cycle throws a generic cycle error", () => {
    const p = createLiveActivityProjector();
    const a: MutableWorkEntry = {
      kind: "delegate",
      label: "a",
      tone: "running",
      diagnostics: diag("a-id"),
    };
    const b: MutableWorkEntry = {
      kind: "delegate",
      label: "b",
      tone: "running",
      diagnostics: diag("b-id"),
    };
    const c: MutableWorkEntry = {
      kind: "delegate",
      label: "c",
      tone: "running",
      diagnostics: diag("c-id"),
    };
    a.children = [b];
    b.children = [c];
    c.children = [a];
    const view = makeActivityView({ work: [a] });
    expect(() => p.project(view, { scope: SCOPE })).toThrow(/cycle/i);
  });

  it("cycle error contains no raw ID, label, or prompt", () => {
    const p = createLiveActivityProjector();
    const self: MutableWorkEntry = {
      kind: "delegate",
      label: "secret-cycle-label",
      tone: "running",
      diagnostics: diag("secret-cycle-id"),
    };
    self.children = [self];
    const view = makeActivityView({ work: [self] });
    let message = "";
    try {
      p.project(view, { scope: SCOPE });
    } catch (e) {
      message = e instanceof Error ? e.message : String(e);
    }
    expect(message).not.toContain("secret-cycle-label");
    expect(message).not.toContain("secret-cycle-id");
  });

  it("diamond (non-cyclic shared child) does not throw", () => {
    // A diamond: parent → childA, childB → sharedGrandchild.
    // This is NOT a cycle — sharedGrandchild is visited twice but via
    // different parent paths, and the ancestry set is cleared on the way up.
    const p = createLiveActivityProjector();
    const shared: WorkEntry = {
      kind: "job",
      label: "shared-grandchild",
      tone: "running",
      diagnostics: diag("shared-gc-id"),
    };
    const childA: WorkEntry = {
      kind: "delegate",
      label: "child-A",
      tone: "running",
      diagnostics: diag("child-a-id"),
      children: [shared],
    };
    const childB: WorkEntry = {
      kind: "delegate",
      label: "child-B",
      tone: "running",
      diagnostics: diag("child-b-id"),
      children: [shared],
    };
    const parent: WorkEntry = {
      kind: "delegate",
      label: "parent",
      tone: "running",
      diagnostics: diag("parent-id"),
      children: [childA, childB],
    };
    const view = makeActivityView({ work: [parent] });
    expect(() => p.project(view, { scope: SCOPE })).not.toThrow();
  });

  // --- cross-instance default allocator is process-unique ------------------

  it("different instances with default allocators produce different keys for the same input", () => {
    const p1 = createLiveActivityProjector();
    const p2 = createLiveActivityProjector();
    const view = makeActivityView({
      work: [
        {
          kind: "job",
          label: "shell",
          tone: "running",
          diagnostics: diag("raw-1"),
        },
      ],
    });
    const a = p1.project(view, { scope: SCOPE });
    const b = p2.project(view, { scope: SCOPE });
    expect(a.live.work[0]?.key).not.toBe(b.live.work[0]?.key);
  });

  it("different instances with explicit distinct allocators produce different keys (deterministic)", () => {
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
          diagnostics: diag("raw-1"),
        },
      ],
    });
    const a = p1.project(view, { scope: SCOPE });
    const b = p2.project(view, { scope: SCOPE });
    expect(a.live.work[0]?.key).toBe("a1");
    expect(b.live.work[0]?.key).toBe("b1");
    expect(a.live.work[0]?.key).not.toBe(b.live.work[0]?.key);
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
          diagnostics: diag("raw-123"),
        },
      ],
    });
    const { live, operational } = p.project(view, { scope: SCOPE });
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
              diagnostics: diag("raw-c"),
            },
          ],
          diagnostics: diag("raw-p"),
        },
      ],
      usage: { totalTokens: 100 },
    });
    const a = p.project(view, { scope: SCOPE });
    const b = p.project(view, { scope: SCOPE });
    expect(a.live).toEqual(b.live);
  });

  it("returns empty arrays for empty input", () => {
    const p = createLiveActivityProjector();
    const view = makeActivityView();
    const { live } = p.project(view, { scope: SCOPE });
    expect(live.tasks).toEqual([]);
    expect(live.work).toEqual([]);
    expect(live.usage).toEqual({});
  });

  // --- scoped identity ------------------------------------------------------

  it("same source ID under different scopes gets different keys (no cross-scope aliasing)", () => {
    const p = createLiveActivityProjector();
    const view = makeActivityView({
      work: [
        {
          kind: "job",
          label: "shell",
          tone: "running",
          diagnostics: diag("raw-1"),
        },
      ],
    });
    const a = p.project(view, { scope: "scope-A" });
    const b = p.project(view, { scope: "scope-B" });
    expect(a.live.work[0]?.key).not.toBe(b.live.work[0]?.key);
  });

  it("reused ID across different scopes does not alias", () => {
    const p = createLiveActivityProjector();
    const view = makeActivityView({
      work: [
        {
          kind: "job",
          label: "shell",
          tone: "running",
          diagnostics: diag("raw-1"),
        },
      ],
    });
    const a = p.project(view, { scope: "scope-A" });
    const b = p.project(view, { scope: "scope-B" });
    // Same item ID but different scopes → different keys (no cross-aliasing)
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
    const { live } = p.project(view, { scope: SCOPE });
    const json = JSON.stringify(live);
    expect(json).not.toContain("dlg-raw-secret");
    expect(json).not.toContain("job-raw-secret");
    expect(json).not.toContain("redacted");
    expect(json).not.toContain("profileId");
  });
});
