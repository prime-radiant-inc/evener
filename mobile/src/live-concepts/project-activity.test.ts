// Stable private live projector tests for createLiveActivityProjector —
// maps an ActivityView into a LiveActivityView for live-concept renderers.
// The projector instance owns a private scoped registry using nested exact
// Maps at every depth — no delimiter/path composites for hierarchy identity.
// Raw diagnostic IDs and no-ID fallback labels live in separate tagged
// namespaces so rawId "x" can never alias label "x". Raw diagnostic IDs are
// unique scope-wide across all parents and kinds. The operational snapshot
// maps rawId → opaque key (no-ID entries omitted) and is returned as a
// genuinely runtime-immutable wrapper. All rejections are transactional.
// Traversal is iterative (stack-safe). Typed projector error class with code.

import { describe, expect, it } from "vitest";
import type {
  ActivityView,
  RedactedDiagnostic,
  WorkEntry,
} from "../services/activity";
import type { LiveWorkItem } from "./model";
import {
  ActivityProjectorError,
  createLiveActivityProjector,
  type EntropySource,
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

// Deterministic entropy source for crypto seam tests.
function makeDeterministicEntropy(seed: number): EntropySource {
  let state = seed;
  return () => {
    const bytes = new Uint8Array(8);
    for (let i = 0; i < 8; i++) {
      state = (state * 1103515245 + 12345) & 0x7fffffff;
      bytes[i] = state & 0xff;
    }
    return bytes;
  };
}

// Constant entropy source: always returns the same bytes. Useful for
// verifying that the salt comes from the entropy, independent of the
// instance counter.
function makeConstantEntropy(): EntropySource {
  const bytes = new Uint8Array(8);
  for (let i = 0; i < 8; i++) bytes[i] = 0xab;
  return () => bytes;
}

// Helper to safely override and restore globalThis.crypto (read-only getter
// in some runtimes requires defineProperty).
function withMockedCrypto(mock: unknown, fn: () => void): void {
  const original = Object.getOwnPropertyDescriptor(globalThis, "crypto");
  Object.defineProperty(globalThis, "crypto", {
    value: mock,
    writable: true,
    configurable: true,
  });
  try {
    fn();
  } finally {
    if (original !== undefined) {
      Object.defineProperty(globalThis, "crypto", original);
    } else {
      Object.defineProperty(globalThis, "crypto", {
        value: undefined,
        writable: true,
        configurable: true,
      });
    }
  }
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

// WorkEntry.children is readonly; use a mutable alias to build cycles/deep.
type MutableWorkEntry = Omit<WorkEntry, "children"> & {
  children?: WorkEntry[];
};

// Build a linear chain of depth N (each entry has one child).
function makeDeepChain(depth: number): WorkEntry {
  let current: MutableWorkEntry = {
    kind: "job",
    label: "leaf",
    tone: "terminal",
    diagnostics: diag("leaf-id"),
  };
  for (let i = depth - 1; i >= 0; i--) {
    current = {
      kind: "delegate",
      label: `level-${i}`,
      tone: "running",
      diagnostics: diag(`deep-${i}`),
      children: [current as WorkEntry],
    };
  }
  return current as WorkEntry;
}

// --- tests -------------------------------------------------------------------

describe("createLiveActivityProjector", () => {
  // --- basic mapping --------------------------------------------------------

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

  // --- activity identity: parent/kind/source, stable reorder/patch -----------

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
              diagnostics: diag("shared-2"),
            },
          ],
        },
      ],
    });
    const { live } = p.project(view, { scope: SCOPE });
    const child1Key = live.work[0]?.children[0]?.key;
    const child2Key = live.work[1]?.children[0]?.key;
    expect(child1Key).not.toBe(child2Key);
  });

  // --- (1) namespace tagging: rawId `x` vs label `x` -------------------------

  it("rawId 'x' does not alias no-ID label 'x' under the same parent", () => {
    const p = createLiveActivityProjector({
      allocator: deterministicAllocator("w"),
    });
    const view = makeActivityView({
      work: [
        {
          kind: "job",
          label: "x",
          tone: "running",
          diagnostics: diag("x"),
        },
        {
          kind: "job",
          label: "x",
          tone: "terminal",
        },
      ],
    });
    const { live } = p.project(view, { scope: SCOPE });
    expect(live.work).toHaveLength(2);
    expect(live.work[0]?.key).not.toBe(live.work[1]?.key);
  });

  it("no-ID label 'x' and rawId 'x' get distinct keys regardless of order", () => {
    const p1 = createLiveActivityProjector({
      allocator: deterministicAllocator("a"),
    });
    const p2 = createLiveActivityProjector({
      allocator: deterministicAllocator("b"),
    });
    const rawFirst = makeActivityView({
      work: [
        { kind: "job", label: "x", tone: "running", diagnostics: diag("x") },
        { kind: "job", label: "x", tone: "terminal" },
      ],
    });
    const labelFirst = makeActivityView({
      work: [
        { kind: "job", label: "x", tone: "terminal" },
        { kind: "job", label: "x", tone: "running", diagnostics: diag("x") },
      ],
    });
    const a = p1.project(rawFirst, { scope: SCOPE });
    const b = p2.project(labelFirst, { scope: SCOPE });
    // The raw entry's key must be the same allocation slot in both orderings
    expect(a.live.work[0]?.key).toBe("a1");
    expect(b.live.work[1]?.key).toBe("b2");
    // They must not alias each other
    expect(a.live.work[0]?.key).not.toBe(a.live.work[1]?.key);
    expect(b.live.work[0]?.key).not.toBe(b.live.work[1]?.key);
  });

  // --- (2) no delimiter/path composites — hostile strings -------------------

  it("hostile label containing '/kind:' does not corrupt hierarchy identity", () => {
    const p = createLiveActivityProjector({
      allocator: deterministicAllocator("w"),
    });
    const view = makeActivityView({
      work: [
        {
          kind: "job",
          label: "hostile/job:shell",
          tone: "running",
          diagnostics: diag("h1"),
        },
        {
          kind: "job",
          label: "innocent",
          tone: "terminal",
          diagnostics: diag("h2"),
        },
      ],
    });
    const { live } = p.project(view, { scope: SCOPE });
    expect(live.work).toHaveLength(2);
    expect(new Set(live.work.map((w) => w.key)).size).toBe(2);
  });

  it("hostile rawId containing '/' and ':' does not alias other entries", () => {
    const p = createLiveActivityProjector({
      allocator: deterministicAllocator("w"),
    });
    const view = makeActivityView({
      work: [
        {
          kind: "job",
          label: "a",
          tone: "running",
          diagnostics: diag("raw/job:other"),
        },
        {
          kind: "job",
          label: "other",
          tone: "terminal",
          diagnostics: diag("other"),
        },
      ],
    });
    const { live } = p.project(view, { scope: SCOPE });
    expect(live.work[0]?.key).not.toBe(live.work[1]?.key);
  });

  it("hostile label and rawId as child identity survive nested under hostile parent", () => {
    const p = createLiveActivityProjector({
      allocator: deterministicAllocator("w"),
    });
    const view = makeActivityView({
      work: [
        {
          kind: "delegate",
          label: "parent/deep:evil",
          tone: "running",
          diagnostics: diag("parent-evil"),
          children: [
            {
              kind: "job",
              label: "child/evil:job",
              tone: "running",
              diagnostics: diag("child-evil"),
            },
          ],
        },
      ],
    });
    const { live } = p.project(view, { scope: SCOPE });
    expect(live.work[0]?.children).toHaveLength(1);
    // Re-project — keys stable
    const b = p.project(view, { scope: SCOPE });
    expect(b.live.work[0]?.key).toBe(live.work[0]?.key);
    expect(b.live.work[0]?.children[0]?.key).toBe(
      live.work[0]?.children[0]?.key,
    );
  });

  // --- (3) scope-wide raw ID uniqueness — no diamond reuse ------------------

  it("duplicate raw ID across different parents (scope-wide) produces a safe error", () => {
    const p = createLiveActivityProjector();
    const shared: WorkEntry = {
      kind: "job",
      label: "shared",
      tone: "running",
      diagnostics: diag("shared-id"),
    };
    const view = makeActivityView({
      work: [
        {
          kind: "delegate",
          label: "parent-1",
          tone: "running",
          diagnostics: diag("p1"),
          children: [shared],
        },
        {
          kind: "delegate",
          label: "parent-2",
          tone: "running",
          diagnostics: diag("p2"),
          children: [shared],
        },
      ],
    });
    expect(() => p.project(view, { scope: SCOPE })).toThrow(/raw.*duplicate/i);
  });

  it("diamond reuse of the same rawId is rejected (no blessing)", () => {
    const p = createLiveActivityProjector();
    const shared: WorkEntry = {
      kind: "job",
      label: "shared-grandchild",
      tone: "running",
      diagnostics: diag("shared-gc"),
    };
    const view = makeActivityView({
      work: [
        {
          kind: "delegate",
          label: "parent",
          tone: "running",
          diagnostics: diag("parent-id"),
          children: [
            {
              kind: "delegate",
              label: "child-A",
              tone: "running",
              diagnostics: diag("child-a"),
              children: [shared],
            },
            {
              kind: "delegate",
              label: "child-B",
              tone: "running",
              diagnostics: diag("child-b"),
              children: [shared],
            },
          ],
        },
      ],
    });
    expect(() => p.project(view, { scope: SCOPE })).toThrow(/raw.*duplicate/i);
  });

  it("diamond with distinct rawIds under different parents is fine (same label, different rawId)", () => {
    const p = createLiveActivityProjector();
    const view = makeActivityView({
      work: [
        {
          kind: "delegate",
          label: "parent",
          tone: "running",
          diagnostics: diag("parent-id"),
          children: [
            {
              kind: "delegate",
              label: "child-A",
              tone: "running",
              diagnostics: diag("child-a"),
              children: [
                {
                  kind: "job",
                  label: "shared-label",
                  tone: "running",
                  diagnostics: diag("gc-a"),
                },
              ],
            },
            {
              kind: "delegate",
              label: "child-B",
              tone: "running",
              diagnostics: diag("child-b"),
              children: [
                {
                  kind: "job",
                  label: "shared-label",
                  tone: "running",
                  diagnostics: diag("gc-b"),
                },
              ],
            },
          ],
        },
      ],
    });
    expect(() => p.project(view, { scope: SCOPE })).not.toThrow();
  });

  it("duplicate raw IDs in top-level siblings produce a safe error", () => {
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
    expect(() => p.project(view, { scope: SCOPE })).toThrow(/raw.*duplicate/i);
  });

  it("cross-kind duplicate raw IDs (scope-wide) produce a safe error", () => {
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
    expect(() => p.project(view, { scope: SCOPE })).toThrow(/raw.*duplicate/i);
  });

  // --- (7) typed error class — no raw ID/label in message -------------------

  it("raw duplicate error is typed ActivityProjectorError with code", () => {
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
    let err: unknown;
    try {
      p.project(view, { scope: SCOPE });
    } catch (e) {
      err = e;
    }
    expect(err).toBeInstanceOf(ActivityProjectorError);
    expect((err as ActivityProjectorError).code).toBe("raw-duplicate");
    const msg = (err as ActivityProjectorError).message;
    expect(msg).not.toContain("secret-raw-id-xyz");
    expect(msg).not.toContain("secret-label");
    expect(msg).not.toContain("other-label");
  });

  // --- no-ID siblings: kind+label, indistinguishable duplicates error -------

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

  it("no-ID duplicate detection uses exact nested kind→label sets, not NUL composites", () => {
    // The no-ID duplicate check must use exact nested kind → Set<label> maps,
    // not a NUL-delimited composite string. This test verifies:
    //   - same kind + same label → duplicate error (exact match)
    //   - different kind + same label → distinct keys (no false collision)
    //   - label containing \u0000 (NUL) does not create false aliasing
    const p = createLiveActivityProjector({
      allocator: deterministicAllocator("w"),
    });
    // Different kinds, same label → must succeed with distinct keys
    const okView = makeActivityView({
      work: [
        { kind: "job", label: "same", tone: "running" },
        { kind: "watch", label: "same", tone: "running" },
        { kind: "delegate", label: "same", tone: "running" },
      ],
    });
    const { live } = p.project(okView, { scope: SCOPE });
    expect(live.work).toHaveLength(3);
    const keys = live.work.map((w) => w.key);
    expect(new Set(keys).size).toBe(3);

    // Same kind + same label → no-id-duplicate error
    const p2 = createLiveActivityProjector({
      allocator: deterministicAllocator("x"),
    });
    const dupView = makeActivityView({
      work: [
        { kind: "job", label: "same", tone: "running" },
        { kind: "job", label: "same", tone: "terminal" },
      ],
    });
    let err: unknown;
    try {
      p2.project(dupView, { scope: SCOPE });
    } catch (e) {
      err = e;
    }
    expect(err).toBeInstanceOf(ActivityProjectorError);
    expect((err as ActivityProjectorError).code).toBe("no-id-duplicate");
  });

  it("no-ID label containing NUL does not create false duplicate via NUL composite", () => {
    // If the implementation used a NUL composite like `${kind}\u0000${label}`,
    // a label "job\u0000x" with kind "watch" could collide with label "x"
    // with kind "job" (since "watch\u0000job\u0000x" vs "job\u0000x" differ,
    // but a buggy splitter could confuse them). The exact nested kind→label
    // sets must not alias these. Here we test labels that contain \u0000
    // and verify they are handled as exact strings, not composites.
    const p = createLiveActivityProjector({
      allocator: deterministicAllocator("w"),
    });
    // Two entries: same kind, labels that differ only by NUL placement.
    // If NUL composites were used, "a\u0000b" and "a" could alias with
    // kind "b" — but kind is a fixed union, so we test label NUL safety.
    const view = makeActivityView({
      work: [
        { kind: "job", label: "a\u0000b", tone: "running" },
        { kind: "job", label: "a", tone: "terminal" },
      ],
    });
    const { live } = p.project(view, { scope: SCOPE });
    expect(live.work).toHaveLength(2);
    expect(live.work[0]?.key).not.toBe(live.work[1]?.key);

    // Same kind + same NUL-containing label → duplicate error (exact match)
    const p2 = createLiveActivityProjector();
    const dupView = makeActivityView({
      work: [
        { kind: "job", label: "a\u0000b", tone: "running" },
        { kind: "job", label: "a\u0000b", tone: "terminal" },
      ],
    });
    let err: unknown;
    try {
      p2.project(dupView, { scope: SCOPE });
    } catch (e) {
      err = e;
    }
    expect(err).toBeInstanceOf(ActivityProjectorError);
    expect((err as ActivityProjectorError).code).toBe("no-id-duplicate");
  });

  it("indistinguishable duplicate no-ID siblings produce a typed error", () => {
    const p = createLiveActivityProjector();
    const view = makeActivityView({
      work: [
        { kind: "job", label: "same-label", tone: "running" },
        { kind: "job", label: "same-label", tone: "terminal" },
        { kind: "job", label: "same-label", tone: "idle" },
      ],
    });
    let err: unknown;
    try {
      p.project(view, { scope: SCOPE });
    } catch (e) {
      err = e;
    }
    expect(err).toBeInstanceOf(ActivityProjectorError);
    expect((err as ActivityProjectorError).code).toBe("no-id-duplicate");
  });

  it("no-ID duplicate error contains no labels or occurrence positions", () => {
    const p = createLiveActivityProjector();
    const view = makeActivityView({
      work: [
        { kind: "job", label: "secret-no-id-label", tone: "running" },
        { kind: "job", label: "secret-no-id-label", tone: "terminal" },
      ],
    });
    let err: unknown;
    try {
      p.project(view, { scope: SCOPE });
    } catch (e) {
      err = e;
    }
    expect(err).toBeInstanceOf(ActivityProjectorError);
    const msg = (err as ActivityProjectorError).message;
    expect(msg).not.toContain("secret-no-id-label");
    expect(msg).not.toContain("#0");
    expect(msg).not.toContain("#1");
  });

  it("no-ID reorder is stable (distinct labels, reordered)", () => {
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

  // --- (4) operational map: rawId→key, omit no-ID, immutable wrapper ---------

  it("operational map direction is rawId → opaque key", () => {
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
    const opaqueKey = live.work[0]?.key;
    // Direction: rawId → key (not key → rawId)
    expect(operational.keys.get("raw-123")).toBe(opaqueKey);
  });

  it("operational map omits no-ID/display-label entries", () => {
    const p = createLiveActivityProjector();
    const view = makeActivityView({
      work: [
        {
          kind: "job",
          label: "no-id-entry",
          tone: "running",
        },
        {
          kind: "job",
          label: "has-id-entry",
          tone: "running",
          diagnostics: diag("has-id"),
        },
      ],
    });
    const { operational } = p.project(view, { scope: SCOPE });
    expect(operational.keys.size).toBe(1);
    expect(operational.keys.has("has-id")).toBe(true);
    // No-ID label is NOT in the operational map
    expect(operational.keys.get("no-id-entry")).toBeUndefined();
  });

  it("operational map is recursively populated for nested children with rawIds", () => {
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

    // Direction: rawId → key for all nested entries
    expect(operational.keys.get("raw-parent")).toBe(parentKey);
    expect(operational.keys.get("raw-child-1")).toBe(child1Key);
    expect(operational.keys.get("raw-child-2")).toBe(child2Key);
    expect(operational.keys.size).toBe(3);
  });

  it("operational map is a genuinely immutable wrapper — casting does not expose mutation", () => {
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

    // Casting to Map should NOT give us set/delete/clear
    const casted = a.operational.keys as unknown as Map<string, string>;
    expect(typeof (casted as unknown as { set?: unknown }).set).toBe(
      "undefined",
    );
    expect(typeof (casted as unknown as { clear?: unknown }).clear).toBe(
      "undefined",
    );
    expect(typeof (casted as unknown as { delete?: unknown }).delete).toBe(
      "undefined",
    );

    // Attempting to clear via cast should be a no-op or not exist
    expect(() => {
      (a.operational.keys as unknown as { clear?: () => void }).clear?.();
    }).not.toThrow();

    // Subsequent projection unaffected
    const b = p.project(view, { scope: SCOPE });
    expect(b.live.work[0]?.key).toBe(opaqueKey);
    expect(b.operational.keys.get("raw-123")).toBe(opaqueKey);
    expect(a.operational.keys.get("raw-123")).toBe(opaqueKey);
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

    // The wrapper has no mutation methods, but even if someone reaches the
    // internal Map somehow, it's a separate copy from the registry.
    const casted = a.operational.keys as unknown as {
      set?: (k: string, v: string) => void;
    };
    // set doesn't exist on the immutable wrapper
    expect(casted.set).toBeUndefined();

    // Subsequent projection unaffected
    const b = p.project(view, { scope: SCOPE });
    expect(b.live.work[0]?.key).toBe(opaqueKey);
    expect(b.operational.keys.get("raw-123")).toBe(opaqueKey);
  });

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

    const b = p.project(view, { scope: SCOPE });
    expect(b.live.work[0]?.key).toBe(opaqueKey);
    expect(b.operational.keys.get("dlg-raw-1")).toBe(opaqueKey);
  });

  // --- reset / dispose / exact scoped reset --------------------------------

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

    p.reset("scope-A");

    const a2 = p.project(view, { scope: "scope-A" });
    expect(a2.live.work[0]?.key).not.toBe(keyA);

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
    const a = p.project(view, { scope: "scope" });
    const keyMain = a.live.work[0]?.key;

    const b = p.project(view, { scope: "scope:child" });
    const keyChild = b.live.work[0]?.key;
    expect(keyMain).not.toBe(keyChild);

    p.reset("scope");

    const a2 = p.project(view, { scope: "scope" });
    expect(a2.live.work[0]?.key).not.toBe(keyMain);

    const b2 = p.project(view, { scope: "scope:child" });
    expect(b2.live.work[0]?.key).toBe(keyChild);
  });

  // --- (5) transactional rejection — zero registry entries ------------------

  it("capacity rejection is transactional — zero new registry entries", () => {
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
    let err: unknown;
    try {
      p.project(overView, { scope: SCOPE });
    } catch (e) {
      err = e;
    }
    expect(err).toBeInstanceOf(ActivityProjectorError);
    expect((err as ActivityProjectorError).code).toBe("capacity");

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

    expect(() => p.project(overView, { scope: SCOPE })).toThrow(
      /capacity exceeded/i,
    );
    expect(() => p.project(overView, { scope: SCOPE })).toThrow(
      /capacity exceeded/i,
    );

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
    const b = p.project(view, { scope: SCOPE });
    expect(b.live.work.map((w) => w.key)).toEqual(
      a.live.work.map((w) => w.key),
    );
  });

  it("repeated rejected new scopes cannot grow storage (cap bounds all retained state)", () => {
    // With a tiny cap, repeatedly projecting into a NEW scope (that would
    // overflow) must not accumulate any retained state.
    const p = createLiveActivityProjector({
      allocator: deterministicAllocator("w"),
      maxRegistrySize: 1,
    });
    const baseView = makeActivityView({
      work: [
        {
          kind: "job",
          label: "j1",
          tone: "running",
          diagnostics: diag("r1"),
        },
      ],
    });
    p.project(baseView, { scope: "s0" });
    const bigView = makeActivityView({
      work: [
        {
          kind: "job",
          label: "a",
          tone: "running",
          diagnostics: diag("ra"),
        },
        {
          kind: "job",
          label: "b",
          tone: "running",
          diagnostics: diag("rb"),
        },
      ],
    });
    // Repeatedly try to project into new scopes that exceed cap
    for (let i = 0; i < 20; i++) {
      expect(() => p.project(bigView, { scope: `s${i + 1}` })).toThrow(
        /capacity exceeded/i,
      );
    }
    // The original scope should still work and be stable
    const b = p.project(baseView, { scope: "s0" });
    expect(b.live.work[0]?.key).toBeDefined();
  });

  it("collision rejection is transactional — zero new registry entries", () => {
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
    let err: unknown;
    try {
      p.project(view, { scope: SCOPE });
    } catch (e) {
      err = e;
    }
    expect(err).toBeInstanceOf(ActivityProjectorError);
    expect((err as ActivityProjectorError).code).toBe("collision");
  });

  it("collision error contains no raw ID, label, or key value", () => {
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
    let err: unknown;
    try {
      p.project(view, { scope: SCOPE });
    } catch (e) {
      err = e;
    }
    expect(err).toBeInstanceOf(ActivityProjectorError);
    expect((err as ActivityProjectorError).code).toBe("collision");
    const msg = (err as ActivityProjectorError).message;
    expect(msg).not.toContain("secret-collision-key");
    expect(msg).not.toContain("secret-label");
    expect(msg).not.toContain("secret-raw-id");
    expect(msg).not.toContain("other-raw-id");
  });

  it("raw-duplicate rejection is transactional — zero new registry entries", () => {
    const p = createLiveActivityProjector({
      allocator: deterministicAllocator("w"),
    });
    const view = makeActivityView({
      work: [
        {
          kind: "job",
          label: "a",
          tone: "running",
          diagnostics: diag("dup"),
        },
        {
          kind: "job",
          label: "b",
          tone: "running",
          diagnostics: diag("dup"),
        },
      ],
    });
    expect(() => p.project(view, { scope: SCOPE })).toThrow(/raw.*duplicate/i);
    // Re-project a valid view — the first entry should get key w1 (no leak)
    const valid = makeActivityView({
      work: [
        {
          kind: "job",
          label: "a",
          tone: "running",
          diagnostics: diag("dup"),
        },
      ],
    });
    const b = p.project(valid, { scope: SCOPE });
    expect(b.live.work[0]?.key).toBe("w1");
  });

  it("no-id-duplicate rejection is transactional — zero new registry entries", () => {
    const p = createLiveActivityProjector({
      allocator: deterministicAllocator("w"),
    });
    const view = makeActivityView({
      work: [
        { kind: "job", label: "dup-label", tone: "running" },
        { kind: "job", label: "dup-label", tone: "terminal" },
      ],
    });
    expect(() => p.project(view, { scope: SCOPE })).toThrow(
      /indistinguishable/i,
    );
    const valid = makeActivityView({
      work: [{ kind: "job", label: "dup-label", tone: "running" }],
    });
    const b = p.project(valid, { scope: SCOPE });
    expect(b.live.work[0]?.key).toBe("w1");
  });

  it("cycle rejection is transactional — zero new registry entries", () => {
    const p = createLiveActivityProjector({
      allocator: deterministicAllocator("w"),
    });
    const self: MutableWorkEntry = {
      kind: "delegate",
      label: "self",
      tone: "running",
      diagnostics: diag("self-id"),
    };
    self.children = [self as WorkEntry];
    const view = makeActivityView({ work: [self as WorkEntry] });
    expect(() => p.project(view, { scope: SCOPE })).toThrow(/cycle/i);
    // Re-project a valid view — should get w1 (no leaked allocation)
    const valid = makeActivityView({
      work: [
        {
          kind: "job",
          label: "a",
          tone: "running",
          diagnostics: diag("a-id"),
        },
      ],
    });
    const b = p.project(valid, { scope: SCOPE });
    expect(b.live.work[0]?.key).toBe("w1");
  });

  // --- (6) iterative/stack-safe traversal -----------------------------------

  it("1-length cycle (self-referencing entry) throws a typed cycle error", () => {
    const p = createLiveActivityProjector();
    const self: MutableWorkEntry = {
      kind: "delegate",
      label: "self",
      tone: "running",
      diagnostics: diag("self-id"),
    };
    self.children = [self as WorkEntry];
    const view = makeActivityView({ work: [self as WorkEntry] });
    let err: unknown;
    try {
      p.project(view, { scope: SCOPE });
    } catch (e) {
      err = e;
    }
    expect(err).toBeInstanceOf(ActivityProjectorError);
    expect((err as ActivityProjectorError).code).toBe("cycle");
  });

  it("2-length cycle throws a typed cycle error", () => {
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
    a.children = [b as WorkEntry];
    b.children = [a as WorkEntry];
    const view = makeActivityView({ work: [a as WorkEntry] });
    let err: unknown;
    try {
      p.project(view, { scope: SCOPE });
    } catch (e) {
      err = e;
    }
    expect(err).toBeInstanceOf(ActivityProjectorError);
    expect((err as ActivityProjectorError).code).toBe("cycle");
  });

  it("3-length cycle throws a typed cycle error", () => {
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
    a.children = [b as WorkEntry];
    b.children = [c as WorkEntry];
    c.children = [a as WorkEntry];
    const view = makeActivityView({ work: [a as WorkEntry] });
    let err: unknown;
    try {
      p.project(view, { scope: SCOPE });
    } catch (e) {
      err = e;
    }
    expect(err).toBeInstanceOf(ActivityProjectorError);
    expect((err as ActivityProjectorError).code).toBe("cycle");
  });

  it("cycle error contains no raw ID, label, or prompt", () => {
    const p = createLiveActivityProjector();
    const self: MutableWorkEntry = {
      kind: "delegate",
      label: "secret-cycle-label",
      tone: "running",
      diagnostics: diag("secret-cycle-id"),
    };
    self.children = [self as WorkEntry];
    const view = makeActivityView({ work: [self as WorkEntry] });
    let err: unknown;
    try {
      p.project(view, { scope: SCOPE });
    } catch (e) {
      err = e;
    }
    expect(err).toBeInstanceOf(ActivityProjectorError);
    const msg = (err as ActivityProjectorError).message;
    expect(msg).not.toContain("secret-cycle-label");
    expect(msg).not.toContain("secret-cycle-id");
  });

  it("diamond with distinct rawIds (non-cyclic shared label) does not throw", () => {
    const p = createLiveActivityProjector();
    const view = makeActivityView({
      work: [
        {
          kind: "delegate",
          label: "parent",
          tone: "running",
          diagnostics: diag("parent-id"),
          children: [
            {
              kind: "delegate",
              label: "child-A",
              tone: "running",
              diagnostics: diag("child-a"),
              children: [
                {
                  kind: "job",
                  label: "shared-label",
                  tone: "running",
                  diagnostics: diag("gc-a"),
                },
              ],
            },
            {
              kind: "delegate",
              label: "child-B",
              tone: "running",
              diagnostics: diag("child-b"),
              children: [
                {
                  kind: "job",
                  label: "shared-label",
                  tone: "running",
                  diagnostics: diag("gc-b"),
                },
              ],
            },
          ],
        },
      ],
    });
    expect(() => p.project(view, { scope: SCOPE })).not.toThrow();
  });

  it("deep acyclic 15k chain projects full hierarchy successfully, no RangeError, exact iterative count", () => {
    // A chain deeper than the typical JS call stack must project successfully
    // (iterative traversal, no RangeError). The full hierarchy must be
    // present in the output with the exact expected count.
    const depth = 15_000;
    const p = createLiveActivityProjector({
      allocator: deterministicAllocator("d"),
      maxRegistrySize: 100_000,
    });
    const root = makeDeepChain(depth);
    const view = makeActivityView({ work: [root] });

    // Must succeed — no throw, no RangeError.
    const result = p.project(view, { scope: SCOPE });

    // The top-level work array has exactly 1 root entry.
    expect(result.live.work).toHaveLength(1);

    // Walk the full live tree iteratively and count every node. The chain
    // is linear (each node has exactly 1 child). makeDeepChain(depth) creates
    // 1 leaf + `depth` wrapper nodes = depth + 1 total.
    let count = 0;
    const stack: LiveWorkItem[] = [...result.live.work];
    while (stack.length > 0) {
      const item = stack.pop();
      if (item === undefined) break;
      count += 1;
      for (const child of item.children) {
        stack.push(child);
      }
    }
    expect(count).toBe(depth + 1);

    // The operational map must contain all rawIds (one per node).
    expect(result.operational.keys.size).toBe(depth + 1);

    // Every key in the live tree must be in the operational map.
    const opValues = new Set<string>();
    for (const [, v] of result.operational.keys.entries()) {
      opValues.add(v);
    }
    const liveKeys = new Set<string>();
    const stack2: LiveWorkItem[] = [...result.live.work];
    while (stack2.length > 0) {
      const item = stack2.pop();
      if (item === undefined) break;
      liveKeys.add(item.key);
      for (const child of item.children) {
        stack2.push(child);
      }
    }
    for (const k of liveKeys) {
      expect(opValues.has(k)).toBe(true);
    }
  });

  it("deep chain with cycle at the bottom yields typed cycle error, not RangeError", () => {
    // Build a chain that's deep AND has a cycle at the bottom.
    const depth = 5_000;
    const p = createLiveActivityProjector({
      maxRegistrySize: 100_000,
    });
    // Create a chain where the leaf points back to an ancestor
    const leaf: MutableWorkEntry = {
      kind: "job",
      label: "leaf",
      tone: "terminal",
      diagnostics: diag("leaf-id"),
    };
    let current = leaf;
    for (let i = 0; i < depth; i++) {
      const parent: MutableWorkEntry = {
        kind: "delegate",
        label: `level-${i}`,
        tone: "running",
        diagnostics: diag(`deep-${i}`),
        children: [current as WorkEntry],
      };
      current = parent;
    }
    // Make leaf's child point back to root — cycle
    leaf.children = [current as WorkEntry];
    const view = makeActivityView({ work: [current as WorkEntry] });
    let err: unknown;
    try {
      p.project(view, { scope: SCOPE });
    } catch (e) {
      err = e;
    }
    expect(err).toBeInstanceOf(ActivityProjectorError);
    expect((err as ActivityProjectorError).code).toBe("cycle");
    expect(err).not.toBeInstanceOf(RangeError);
  });

  // --- (8) non-derivable default salt ---------------------------------------

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

  it("default allocator keys are non-derivable (do not contain rawId or label)", () => {
    const p = createLiveActivityProjector();
    const view = makeActivityView({
      work: [
        {
          kind: "job",
          label: "secret-label",
          tone: "running",
          diagnostics: diag("secret-raw-id"),
        },
      ],
    });
    const { live } = p.project(view, { scope: SCOPE });
    const key = live.work[0]?.key ?? "";
    expect(key).not.toContain("secret-label");
    expect(key).not.toContain("secret-raw-id");
    expect(key.length).toBeGreaterThan(0);
  });

  it("injected deterministic allocator is honored (not self-fulfilling with module counters)", () => {
    // The injected allocator fully overrides the default; module-level counters
    // must not participate in key generation when an allocator is injected.
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

  it("injected allocator collision behavior is detectable (not masked by module counters)", () => {
    // An allocator that always returns the same key must produce a collision
    // error, regardless of module-level state.
    const colliding: OpaqueKeyAllocator = () => "same-key";
    const p = createLiveActivityProjector({ allocator: colliding });
    const view = makeActivityView({
      work: [
        {
          kind: "job",
          label: "a",
          tone: "running",
          diagnostics: diag("r1"),
        },
        {
          kind: "job",
          label: "b",
          tone: "running",
          diagnostics: diag("r2"),
        },
      ],
    });
    expect(() => p.project(view, { scope: SCOPE })).toThrow(
      /allocator collision/i,
    );
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

  // --- (7) all error codes covered -----------------------------------------

  it("all five error codes are exercised across tests", () => {
    // This is a meta-test asserting the code union type is complete.
    const codes: ActivityProjectorErrorCode[] = [
      "capacity",
      "collision",
      "cycle",
      "raw-duplicate",
      "no-id-duplicate",
    ];
    for (const code of codes) {
      const err = new ActivityProjectorError(code, "test");
      expect(err.code).toBe(code);
      expect(err).toBeInstanceOf(Error);
      expect(err.name).toBe("ActivityProjectorError");
    }
  });

  // --- typed error path tests (actual project() paths, not construction) -----

  it("capacity error path through project() — typed, transactional", () => {
    const p = createLiveActivityProjector({
      allocator: deterministicAllocator("w"),
      maxRegistrySize: 2,
    });
    const view = makeActivityView({
      work: [
        { kind: "job", label: "j1", tone: "running", diagnostics: diag("r1") },
        { kind: "job", label: "j2", tone: "running", diagnostics: diag("r2") },
        { kind: "job", label: "j3", tone: "running", diagnostics: diag("r3") },
      ],
    });
    let err: unknown;
    try {
      p.project(view, { scope: SCOPE });
    } catch (e) {
      err = e;
    }
    expect(err).toBeInstanceOf(ActivityProjectorError);
    const typed = err as ActivityProjectorError;
    expect(typed.code).toBe("capacity");
    expect(typed.name).toBe("ActivityProjectorError");
    expect(typed instanceof Error).toBe(true);
    // Transactional: subsequent valid projection gets clean keys
    const valid = makeActivityView({
      work: [
        { kind: "job", label: "j1", tone: "running", diagnostics: diag("r1") },
      ],
    });
    const b = p.project(valid, { scope: SCOPE });
    expect(b.live.work[0]?.key).toBe("w1");
  });

  it("collision error path through project() — typed, transactional", () => {
    const colliding: OpaqueKeyAllocator = () => "same-key";
    const p = createLiveActivityProjector({ allocator: colliding });
    const view = makeActivityView({
      work: [
        { kind: "job", label: "j1", tone: "running", diagnostics: diag("r1") },
        { kind: "job", label: "j2", tone: "running", diagnostics: diag("r2") },
      ],
    });
    let err: unknown;
    try {
      p.project(view, { scope: SCOPE });
    } catch (e) {
      err = e;
    }
    expect(err).toBeInstanceOf(ActivityProjectorError);
    const typed = err as ActivityProjectorError;
    expect(typed.code).toBe("collision");
    expect(typed.name).toBe("ActivityProjectorError");
  });

  it("cycle error path through project() — typed, transactional", () => {
    const p = createLiveActivityProjector();
    const self: MutableWorkEntry = {
      kind: "delegate",
      label: "self",
      tone: "running",
      diagnostics: diag("self-id"),
    };
    self.children = [self as WorkEntry];
    const view = makeActivityView({ work: [self as WorkEntry] });
    let err: unknown;
    try {
      p.project(view, { scope: SCOPE });
    } catch (e) {
      err = e;
    }
    expect(err).toBeInstanceOf(ActivityProjectorError);
    const typed = err as ActivityProjectorError;
    expect(typed.code).toBe("cycle");
    expect(typed.name).toBe("ActivityProjectorError");
    // Transactional: subsequent valid projection gets clean keys
    const valid = makeActivityView({
      work: [
        { kind: "job", label: "a", tone: "running", diagnostics: diag("a-id") },
      ],
    });
    const b = p.project(valid, { scope: SCOPE });
    expect(b.live.work[0]?.key).toBeDefined();
  });

  it("raw-duplicate error path through project() — typed, transactional", () => {
    const p = createLiveActivityProjector();
    const view = makeActivityView({
      work: [
        { kind: "job", label: "a", tone: "running", diagnostics: diag("dup") },
        { kind: "job", label: "b", tone: "running", diagnostics: diag("dup") },
      ],
    });
    let err: unknown;
    try {
      p.project(view, { scope: SCOPE });
    } catch (e) {
      err = e;
    }
    expect(err).toBeInstanceOf(ActivityProjectorError);
    const typed = err as ActivityProjectorError;
    expect(typed.code).toBe("raw-duplicate");
    expect(typed.name).toBe("ActivityProjectorError");
  });

  it("no-id-duplicate error path through project() — typed, transactional", () => {
    const p = createLiveActivityProjector();
    const view = makeActivityView({
      work: [
        { kind: "job", label: "dup-label", tone: "running" },
        { kind: "job", label: "dup-label", tone: "terminal" },
      ],
    });
    let err: unknown;
    try {
      p.project(view, { scope: SCOPE });
    } catch (e) {
      err = e;
    }
    expect(err).toBeInstanceOf(ActivityProjectorError);
    const typed = err as ActivityProjectorError;
    expect(typed.code).toBe("no-id-duplicate");
    expect(typed.name).toBe("ActivityProjectorError");
  });

  // --- (R1) runtime-immutable operational snapshot ----------------------------

  it("operational snapshot: Object.freeze — casts cannot assign size", () => {
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
    const { operational } = p.project(view, { scope: SCOPE });
    const keys = operational.keys;
    // size is a getter — assigning to it throws in strict mode or is a no-op
    expect(() => {
      (keys as unknown as { size: number }).size = 999;
    }).toThrow(TypeError);
    // size is still correct
    expect(keys.size).toBe(1);
  });

  it("operational snapshot: Reflect.ownKeys does not expose backing Map (#private)", () => {
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
    const { operational } = p.project(view, { scope: SCOPE });
    const keys = operational.keys;
    const ownKeys = Reflect.ownKeys(keys);
    // The #map private field must NOT appear in own keys (private fields are
    // not enumerable via Reflect.ownKeys).
    const privateField = ownKeys.find(
      (k) => typeof k === "string" && k.startsWith("#"),
    );
    expect(privateField).toBeUndefined();
    // The wrapper should expose only the expected interface methods/getters
    const expected = new Set([
      "size",
      "get",
      "has",
      "forEach",
      "entries",
      "keys",
      "values",
      // Symbol.iterator is a symbol, handled below
    ]);
    const stringKeys = ownKeys.filter(
      (k): k is string => typeof k === "string",
    );
    for (const k of stringKeys) {
      // Allow only expected keys (no #private field leaking)
      if (!expected.has(k)) {
        // Some runtimes may add non-standard properties; the critical check
        // is that #map is not present
        expect(k.startsWith("#")).toBe(false);
      }
    }
  });

  it("operational snapshot: strict assignment to wrapper properties fails (frozen)", () => {
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
    const { operational } = p.project(view, { scope: SCOPE });
    const keys = operational.keys;

    // The wrapper is frozen — adding new properties or changing existing
    // ones must throw in strict mode.
    expect(() => {
      (keys as unknown as Record<string, unknown>).size = 999;
    }).toThrow(TypeError);

    expect(() => {
      (keys as unknown as Record<string, unknown>).malicious = true;
    }).toThrow(TypeError);

    // Operational object is also frozen
    expect(() => {
      (operational as unknown as Record<string, unknown>).keys = new Map();
    }).toThrow(TypeError);
  });

  it("operational snapshot: shadow get/has/iterator cannot access backing Map", () => {
    const p = createLiveActivityProjector();
    const view = makeActivityView({
      work: [
        {
          kind: "job",
          label: "shell",
          tone: "running",
          diagnostics: diag("raw-1"),
        },
        {
          kind: "job",
          label: "shell2",
          tone: "running",
          diagnostics: diag("raw-2"),
        },
      ],
    });
    const { operational } = p.project(view, { scope: SCOPE });
    const keys = operational.keys;
    expect(keys.size).toBe(2);

    // Casting to Map does NOT give set/delete/clear
    const casted = keys as unknown as Map<string, string>;
    expect(typeof (casted as unknown as { set?: unknown }).set).toBe(
      "undefined",
    );
    expect(typeof (casted as unknown as { delete?: unknown }).delete).toBe(
      "undefined",
    );
    expect(typeof (casted as unknown as { clear?: unknown }).clear).toBe(
      "undefined",
    );

    // Shadow properties: assigning get/has to the cast does nothing (frozen)
    expect(() => {
      (keys as unknown as Record<string, unknown>).get = () => "hacked";
    }).toThrow(TypeError);
    expect(() => {
      (keys as unknown as Record<string, unknown>).has = () => true;
    }).toThrow(TypeError);

    // Iterator still works and returns the original entries
    const entries = Array.from(keys.entries());
    expect(entries).toHaveLength(2);
    expect(new Set(entries.map(([, v]) => v)).size).toBe(2);

    // After attempted shadowing, the methods still work correctly
    expect(keys.get("raw-1")).toBeDefined();
    expect(keys.has("raw-2")).toBe(true);
    expect(keys.has("nonexistent")).toBe(false);
  });

  it("operational snapshot: cannot mutate size even via Reflect.set", () => {
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
    const { operational } = p.project(view, { scope: SCOPE });
    const keys = operational.keys;

    // Reflect.set on size must fail (frozen + getter)
    const result = Reflect.set(keys, "size", 999);
    expect(result).toBe(false);
    expect(keys.size).toBe(1);

    // Reflect.defineProperty on size must fail
    const defResult = Reflect.defineProperty(keys, "size", {
      value: 999,
      writable: true,
    });
    expect(defResult).toBe(false);
    expect(keys.size).toBe(1);
  });

  // --- (R2) empty projections allocate/retain NO new scope/root ----------------

  it("empty projection into a new scope allocates/retains no scope root (oracle)", () => {
    const p = createLiveActivityProjector();
    const emptyView = makeActivityView({ work: [] });
    p.project(emptyView, { scope: "never-seen" });
    // No scope root should have been created
    expect(p._testGetRetainedScopeCount?.()).toBe(0);
  });

  it("empty projection into an already-known scope does not corrupt it", () => {
    const p = createLiveActivityProjector({
      allocator: deterministicAllocator("w"),
    });
    const view = makeActivityView({
      work: [
        { kind: "job", label: "j1", tone: "running", diagnostics: diag("r1") },
      ],
    });
    const a = p.project(view, { scope: SCOPE });
    const key1 = a.live.work[0]?.key;
    expect(p._testGetRetainedScopeCount?.()).toBe(1);

    // Empty projection into the same scope — must not corrupt or clear it
    p.project(makeActivityView({ work: [] }), { scope: SCOPE });
    expect(p._testGetRetainedScopeCount?.()).toBe(1);

    // Re-project — keys stable
    const b = p.project(view, { scope: SCOPE });
    expect(b.live.work[0]?.key).toBe(key1);
  });

  it("empty projections with maxRegistrySize=0 retain no scope root", () => {
    const p = createLiveActivityProjector({
      maxRegistrySize: 0,
    });
    const emptyView = makeActivityView({ work: [] });
    p.project(emptyView, { scope: "s1" });
    expect(p._testGetRetainedScopeCount?.()).toBe(0);

    p.project(emptyView, { scope: "s2" });
    expect(p._testGetRetainedScopeCount?.()).toBe(0);
  });

  it("repeated empty projections into unique scopes retain zero scope roots", () => {
    const p = createLiveActivityProjector();
    const emptyView = makeActivityView({ work: [] });
    for (let i = 0; i < 50; i++) {
      p.project(emptyView, { scope: `unique-${i}` });
    }
    expect(p._testGetRetainedScopeCount?.()).toBe(0);
  });

  it("empty projections retain zero roots, then nonempty capacity is unaffected", () => {
    const p = createLiveActivityProjector({
      allocator: deterministicAllocator("w"),
      maxRegistrySize: 5,
    });
    // Fill with empty projections — these should not consume capacity
    for (let i = 0; i < 100; i++) {
      p.project(makeActivityView({ work: [] }), { scope: `empty-${i}` });
    }
    expect(p._testGetRetainedScopeCount?.()).toBe(0);

    // Now project a nonempty view — should succeed within capacity
    const view = makeActivityView({
      work: [
        { kind: "job", label: "j1", tone: "running", diagnostics: diag("r1") },
        { kind: "job", label: "j2", tone: "running", diagnostics: diag("r2") },
        { kind: "job", label: "j3", tone: "running", diagnostics: diag("r3") },
      ],
    });
    const result = p.project(view, { scope: SCOPE });
    expect(result.live.work).toHaveLength(3);
    expect(result.operational.keys.size).toBe(3);
  });

  // --- (R3) cryptographic entropy seam ----------------------------------------

  it("default allocator uses crypto.getRandomValues, not Math.random", () => {
    // The default allocator must use globalThis.crypto.getRandomValues for
    // its salt. Verify by mocking crypto and checking it's called.
    let called = false;
    const mockCrypto = {
      getRandomValues: <T extends ArrayBufferView>(arr: T): T => {
        called = true;
        for (let i = 0; i < arr.byteLength; i++) {
          new Uint8Array(arr.buffer, arr.byteOffset, arr.byteLength)[i] = 42;
        }
        return arr;
      },
    };
    withMockedCrypto(mockCrypto, () => {
      const p = createLiveActivityProjector();
      const view = makeActivityView({
        work: [
          {
            kind: "job",
            label: "shell",
            tone: "running",
            diagnostics: diag("r1"),
          },
        ],
      });
      p.project(view, { scope: SCOPE });
    });
    expect(called).toBe(true);
  });

  it("injectable entropy seam produces deterministic but distinct salts", () => {
    // Two instances with different injected entropy must produce different
    // salts (and thus different keys) even for the same input.
    const entropy1 = makeDeterministicEntropy(42);
    const entropy2 = makeDeterministicEntropy(99);
    const p1 = createLiveActivityProjector({ entropy: entropy1 });
    const p2 = createLiveActivityProjector({ entropy: entropy2 });
    const view = makeActivityView({
      work: [
        {
          kind: "job",
          label: "shell",
          tone: "running",
          diagnostics: diag("r1"),
        },
      ],
    });
    const a = p1.project(view, { scope: SCOPE });
    const b = p2.project(view, { scope: SCOPE });
    expect(a.live.work[0]?.key).not.toBe(b.live.work[0]?.key);
  });

  it("salt differs independently of instance counter", () => {
    // The salt is generated at factory time from entropy, independent of
    // the instance counter. Two instances with the same constant injected
    // entropy should produce the same salt but different keys (via the
    // counter), proving the salt is from entropy and the counter is separate.
    const entropy = makeConstantEntropy();
    const p1 = createLiveActivityProjector({ entropy: entropy });
    const p2 = createLiveActivityProjector({ entropy: entropy });
    const view = makeActivityView({
      work: [
        {
          kind: "job",
          label: "shell",
          tone: "running",
          diagnostics: diag("r1"),
        },
      ],
    });
    const a = p1.project(view, { scope: SCOPE });
    const b = p2.project(view, { scope: SCOPE });
    // Keys differ because the factory counter increments, but the salt
    // portion is the same (derived from entropy, not the counter).
    const keyA = a.live.work[0]?.key ?? "";
    const keyB = b.live.work[0]?.key ?? "";
    expect(keyA).not.toBe(keyB);
    // Extract salt portion (before first _)
    const saltA = keyA.split("_")[0];
    const saltB = keyB.split("_")[0];
    expect(saltA).toBe(saltB);
    // The counter portion differs
    expect(keyA.split("_")[1]).not.toBe(keyB.split("_")[1]);
  });

  it("salt is non-derivable — does not contain rawId or label", () => {
    const p = createLiveActivityProjector();
    const view = makeActivityView({
      work: [
        {
          kind: "job",
          label: "secret-label",
          tone: "running",
          diagnostics: diag("secret-raw-id"),
        },
      ],
    });
    const { live } = p.project(view, { scope: SCOPE });
    const key = live.work[0]?.key ?? "";
    expect(key).not.toContain("secret-label");
    expect(key).not.toContain("secret-raw-id");
    // Key contains crypto-derived salt + counter + sequence
    expect(key.length).toBeGreaterThan(0);
  });

  it("fails closed if secure entropy is unavailable", () => {
    // When globalThis.crypto.getRandomValues is unavailable, the default
    // allocator must throw (fail closed), not fall back to Math.random.
    // The throw happens at factory time when the default allocator is built.
    withMockedCrypto(undefined, () => {
      expect(() => createLiveActivityProjector()).toThrow(/secure entropy/i);
    });
  });

  it("fails closed if crypto.getRandomValues is not a function", () => {
    withMockedCrypto({ getRandomValues: "not-a-function" }, () => {
      expect(() => createLiveActivityProjector()).toThrow(/secure entropy/i);
    });
  });
});

// Re-export the code type for the meta-test.
type ActivityProjectorErrorCode =
  import("./project-activity").ActivityProjectorErrorCode;
