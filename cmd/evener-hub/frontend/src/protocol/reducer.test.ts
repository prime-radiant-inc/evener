import { expect, test, vi } from "vitest";
// Vite's `?raw` import (declared ambiently by the "vite/client" lib already
// in tsconfig.json) loads each fixture's text at build time — this project
// has no @types/node, so node:fs is not an option here.
import basicTurnFixture from "./fixtures/basic-turn.jsonl?raw";
import queueAndStatusFixture from "./fixtures/queue-and-status.jsonl?raw";
import streamingWithResetFixture from "./fixtures/streaming-with-reset.jsonl?raw";
import toolAndJobsFixture from "./fixtures/tool-and-jobs.jsonl?raw";
import { type ItemModel, SYSTEM_PRELUDE_TURN_ID, type ThreadModel, type TurnModel } from "./model";
import {
  applyNotification,
  chunkViewBackingForTests,
  collectAuthoritativeMutationIds,
  hydrateThread,
  mergeOlderItemPage,
  notificationTargetsThread,
  pendingTextJoined,
  prependOlderTurns,
  resolvePendingEscalation,
} from "./reducer";
import { hydrateStreamingAgentMessage } from "./testing/tokenFlood";
import type {
  AnyNotification,
  QueueState,
  SandboxEscalationRequested,
  Thread,
  ThreadCapabilities,
  ThreadItem,
  ThreadReadResponse,
  ThreadTurnsListResponse,
} from "./types.gen";
import { NOTIFICATION_NAMES } from "./types.gen";

// Each fixture is newline-delimited JSON: the first line is
// {"hydrate": ThreadReadResponse, "ref": string}, every following line is a
// raw wire notification object ({method, params}).
interface FixtureHeader {
  hydrate: ThreadReadResponse;
  ref: string;
}

const FIXTURE_TEXT: Record<string, string> = {
  "basic-turn": basicTurnFixture,
  "streaming-with-reset": streamingWithResetFixture,
  "tool-and-jobs": toolAndJobsFixture,
  "queue-and-status": queueAndStatusFixture,
};

const KNOWN_NOTIFICATIONS: ReadonlySet<string> = new Set(NOTIFICATION_NAMES);

interface Fixture {
  header: FixtureHeader;
  notifications: AnyNotification[];
}

// parseFixture turns one fixture's text into a header plus a list of
// notifications, checking every record's method against the hub's generated
// catalog on the way through.
//
// The check has to happen HERE, at the read, because a fixture is data: it is
// JSON on disk, so no type check reaches it — not tsc (the notifications never
// exist as source literals) and not FakeClient's emitNotification guard (the
// replay drives applyNotification directly, never a client). Left unchecked, a
// notification renamed on the wire leaves every recorded line stale, the
// reducer's `default:` case returns the model unchanged for each one, and the
// replay's toMatchSnapshot() assertion re-records the resulting do-nothing
// model as the new truth on the next `-u` run. The suite goes green on a lie.
// Validating the name at load turns that silent staleness into a failure that
// names the fixture and the line.
function parseFixture(name: string, text: string): Fixture {
  const records = text
    .split("\n")
    .map((line, index) => ({ text: line.trim(), line: index + 1 }))
    .filter((record) => record.text.length > 0)
    .map((record) => ({ ...record, value: JSON.parse(record.text) as { method?: unknown } }));

  const first = records[0];
  if (!first) throw new Error(`fixture ${name} is empty`);
  const header = first.value as unknown as FixtureHeader;
  if (header.hydrate === undefined || header.ref === undefined) {
    throw new Error(`fixture ${name} line ${first.line}: expected a {hydrate, ref} header, got ${first.text}`);
  }

  const notifications = records.slice(1).map((record) => {
    const method = record.value.method;
    if (typeof method !== "string") {
      throw new Error(`fixture ${name} line ${record.line}: record has no string "method"`);
    }
    if (!KNOWN_NOTIFICATIONS.has(method)) {
      throw new Error(
        `fixture ${name} line ${record.line}: unknown notification "${method}" — not in the hub's generated ` +
          `notification catalog (NOTIFICATION_NAMES in protocol/types.gen.ts). Either the notification was ` +
          `renamed or removed on the wire and this recorded replay is stale, or the name is a typo; either way ` +
          `the reducer would ignore this line via its default: case and the snapshot would record nothing.`,
      );
    }
    return record.value as AnyNotification;
  });

  return { header, notifications };
}

function readFixture(name: string): Fixture {
  const text = FIXTURE_TEXT[name];
  if (text === undefined) throw new Error(`no fixture registered for ${name}`);
  return parseFixture(name, text);
}

function turnAt(model: ThreadModel, index: number): TurnModel {
  const turn = model.turns[index];
  if (!turn) throw new Error(`expected a turn at index ${index}`);
  return turn;
}

function itemAt(turn: TurnModel, index: number): ItemModel {
  const item = turn.items[index];
  if (!item) throw new Error(`expected an item at index ${index}`);
  return item;
}

const CAPABILITIES: ThreadCapabilities = {
  send: true,
  steer: true,
  interrupt: true,
  compact: true,
  clear: true,
  forkFromTurn: true,
  shutdown: true,
  changeModel: true,
  changeVisionModel: true,
  queue: true,
  goal: true,
  rename: true,
};

type TestThreadOverrides = Omit<Partial<Thread>, "evener"> & {
  evener?: Partial<Omit<Thread["evener"], "queue">> & { queue?: Partial<QueueState> };
};

function testThread(overrides: TestThreadOverrides = {}): Thread {
  const { evener, ...threadOverrides } = overrides;
  return {
    id: "thr_t",
    sessionId: "sess_t",
    preview: "test",
    ephemeral: false,
    modelProvider: "anthropic/claude-sonnet-4-5",
    createdAt: 1000,
    updatedAt: 1000,
    status: { type: "idle" },
    cwd: "/tmp/project",
    cliVersion: "1.0.0",
    source: "evener",
    evener: {
      ref: "ref_t",
      capabilities: CAPABILITIES,
      ...evener,
      queue: { revision: 0, ...evener?.queue },
    },
    ...threadOverrides,
  };
}

function testHydrate(overrides: TestThreadOverrides = {}): ThreadModel {
  const thread = testThread(overrides);
  return hydrateThread({ thread }, thread.evener.ref, 1000);
}

test("evener/goal/updated replaces and explicitly clears model.goal", () => {
  const initial = testHydrate({
    evener: { goal: { objective: "old objective", status: "active", iterations: 3 } },
  });
  const updated = applyNotification(
    initial,
    {
      method: "evener/goal/updated",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        goal: { objective: "ship focus sentence", status: "active", iterations: 1 },
      },
    },
    2000,
  );
  expect(updated.goal).toEqual({ objective: "ship focus sentence", status: "active", iterations: 1 });
  expect(updated.lastFrameAt).toBe(2000);

  const cleared = applyNotification(
    updated,
    { method: "evener/goal/updated", params: { threadId: "thr_t", ref: "ref_t", goal: null } },
    3000,
  );
  expect(cleared.goal).toBeNull();
  expect(cleared.lastFrameAt).toBe(3000);
});

test("hydrateThread carries the snapshot plugin diagnostics into ThreadModel", () => {
  const model = testHydrate({
    evener: {
      ref: "ref_t",
      capabilities: CAPABILITIES,
      queue: {},
      diagnostics: {
        plugins: [
          { name: "enabled", skillCount: 1, agentCount: 0, hookCount: 0, mcpCount: 0 },
          { name: "another", skillCount: 0, agentCount: 1, hookCount: 0, mcpCount: 0 },
        ],
      },
    },
  });

  expect(model.diagnostics?.plugins?.map((plugin) => plugin.name)).toEqual(["enabled", "another"]);
});

test("hydrateThread preserves an explicit empty plugin inventory", () => {
  const model = testHydrate({
    evener: {
      ref: "ref_t",
      capabilities: CAPABILITIES,
      queue: {},
      diagnostics: { plugins: [] },
    },
  });

  expect(model.diagnostics?.plugins).toEqual([]);
});

test("hydrateThread leaves diagnostics unavailable when the wire omits them", () => {
  expect(testHydrate().diagnostics).toBeUndefined();
});

test("hydrateThread retains canonical skill descriptors", () => {
  const model = testHydrate({
    evener: { diagnostics: { skills: [{ name: "plugin:simplify", description: "rewrite" }] } },
  });
  expect(model.skills).toEqual([{ name: "plugin:simplify", description: "rewrite" }]);
});

test("hydrateThread defaults missing skills and copies wire descriptors", () => {
  expect(testHydrate().skills).toEqual([]);

  const skills = [{ name: "plugin:simplify", description: "rewrite" }];
  const model = testHydrate({ evener: { diagnostics: { skills } } });
  expect(model.skills).not.toBe(skills);
  expect(model.skills?.[0]).not.toBe(skills[0]);
});

test("applyNotification preserves skills while applying a status update", () => {
  const model = testHydrate({
    evener: { diagnostics: { skills: [{ name: "plugin:simplify", description: "rewrite" }] } },
  });
  const notification: AnyNotification = {
    method: "thread/status/changed",
    params: {
      threadId: model.threadId,
      ref: model.ref,
      status: { type: "active" },
      capabilities: CAPABILITIES,
    },
  };

  const next = applyNotification(model, notification, 2000);

  expect(next).not.toBe(model);
  expect(next.skills).toEqual([{ name: "plugin:simplify", description: "rewrite" }]);
});

function testEscalation(overrides: Partial<SandboxEscalationRequested> = {}): SandboxEscalationRequested {
  return {
    threadId: "thr_t",
    ref: "ref_t",
    escalationId: "esc_1",
    mode: "exempt_denied_path",
    tool: "write_file",
    kind: "file_tool",
    deniedPath: "/etc/passwd",
    ...overrides,
  };
}

for (const f of ["basic-turn", "streaming-with-reset", "tool-and-jobs", "queue-and-status"]) {
  test(`fixture ${f} reduces to the expected model`, () => {
    const { header, notifications } = readFixture(f);
    let model = hydrateThread(header.hydrate, header.ref, 1000);
    for (const [i, n] of notifications.entries()) {
      const notification: AnyNotification =
        n.method === "turn/completed"
          ? {
              ...n,
              params: {
                ...n.params,
                threadId: header.hydrate.thread.id,
                ref: header.ref,
              },
            }
          : n;
      model = applyNotification(model, notification, 1000 + i);
    }
    expect(model).toMatchSnapshot();
  });
}

// The replay above is the only place in this suite that reaches
// applyNotification with data instead of a source literal — everywhere else
// the notification is an object literal in an AnyNotification position, which
// tsc checks against the generated union. So parseFixture's name check is the
// entire guard on the fixture path, and these cover it.
const PROBE_HEADER = JSON.stringify({ hydrate: { thread: testThread() }, ref: "ref_t" });

function probeFixture(...records: object[]): string {
  return [PROBE_HEADER, ...records.map((r) => JSON.stringify(r))].join("\n");
}

test("a fixture record naming a notification the hub no longer sends is rejected, not replayed", () => {
  // What a wire rename leaves behind: a recorded line whose method the hub
  // stopped sending. Unchecked it would reduce to nothing and re-snapshot green.
  const stale = probeFixture({ method: "turn/renamed", params: { turnId: "turn_1" } });
  expect(() => parseFixture("probe", stale)).toThrow(/unknown notification "turn\/renamed"/);
  expect(() => parseFixture("probe", stale)).toThrow(/NOTIFICATION_NAMES/);
});

test("a rejected fixture record is reported by fixture name and line number", () => {
  const stale = probeFixture({ method: "turn/started", params: {} }, { method: "item/agentMessage/chunk", params: {} });
  expect(() => parseFixture("tool-and-jobs", stale)).toThrow(/fixture tool-and-jobs line 3/);
});

test("every name in the generated catalog is accepted by the fixture reader", () => {
  const everyName = probeFixture(...NOTIFICATION_NAMES.map((method) => ({ method, params: {} })));
  const parsed = parseFixture("probe", everyName);
  expect(parsed.notifications.map((n) => n.method)).toEqual([...NOTIFICATION_NAMES]);
});

test("a fixture whose records lack a method, or whose first line is not a header, is rejected", () => {
  expect(() => parseFixture("probe", probeFixture({ params: {} }))).toThrow(/line 2: record has no string "method"/);
  expect(() => parseFixture("probe", JSON.stringify({ method: "turn/started", params: {} }))).toThrow(
    /line 1: expected a \{hydrate, ref\} header/,
  );
  expect(() => parseFixture("probe", "")).toThrow(/fixture probe is empty/);
});

test("hydrate carries the thread's location facts (cwd, git branch, project path)", () => {
  const model = testHydrate({
    projectPath: "/home/u/proj",
    gitInfo: { branch: "main" },
  });
  expect(model.cwd).toBe("/tmp/project");
  expect(model.gitBranch).toBe("main");
  expect(model.projectPath).toBe("/home/u/proj");
});

test("a zero or negative activeTurnStartedAt hydrates as absent, never an epoch anchor", () => {
  const zero = testHydrate({
    evener: { ref: "ref_t", capabilities: CAPABILITIES, queue: { revision: 0 }, activeTurnStartedAt: 0 },
  });
  expect(zero.activeTurnStartedAt).toBeUndefined();
  const negative = testHydrate({
    evener: { ref: "ref_t", capabilities: CAPABILITIES, queue: { revision: 0 }, activeTurnStartedAt: -1 },
  });
  expect(negative.activeTurnStartedAt).toBeUndefined();
});

test("stable delegate diagnostics preserve lossless fields and omit call-scoped wait reasons", () => {
  const thread = testThread();
  (thread.evener as unknown as Record<string, unknown>).diagnostics = {
    delegates: [
      {
        delegateId: "dlg_lossless",
        ownerSessionId: "sess_t",
        rootSessionId: "sess_t",
        childSessionId: "sess_child",
        transcriptRef: "local:sess_child",
        parentDelegateId: "dlg_parent",
        type: "delegate",
        lifecycle: "idle",
        phase: "idle",
        status: "idle",
        outcome: "exhausted",
        reason: "turn_budget_exhausted",
        terminal: true,
        resumable: false,
        notResumableReason: "turn_budget_exhausted",
        projectionRevision: 9,
        task: "Inspect the repo",
        description: "repository inspection",
        agentType: "explorer",
        requestedModel: "fast",
        resolvedProfileId: "anthropic",
        resolvedModel: "claude-sonnet",
        model: "anthropic/claude-sonnet",
        reasoningEffort: "high",
        originTurnId: "turn_1",
        originToolCallId: "call_1",
        originItemId: "item_1",
        runStartedAt: "2026-08-15T10:00:00Z",
        runEndedAt: "2026-08-15T10:01:00Z",
        latestActivityAt: "2026-08-15T10:00:59Z",
        runningForMs: null,
        quietForMs: 1000,
        durationMs: 60000,
        packetKind: "communicate",
        message: null,
        structuredResult: null,
        structuredResultValid: true,
        structuredResultReason: "explicit null",
        warnings: ["warning one"],
        diagnostics: ["observer armed"],
        exhaustionBudget: "turns",
        exhaustionLimit: 12,
        exhaustionResumable: false,
        delegationAllowance: 2,
        parentWatchGranted: true,
        usage: { inputTokens: 41, outputTokens: 7, cacheReadTokens: 3, totalTokens: 48 },
        worktree: { path: "/tmp/wt", branch: "delegate/dlg_lossless", headSha: "abc", ahead: 2, dirty: true },
        waitIgnoredReason: "must never enter stable state",
      },
    ],
    turnSlots: { inUse: 1, cap: 4, jobs: 2, driveTurns: 1 },
  };

  const model = hydrateThread({ thread }, thread.evener.ref, 1000) as ThreadModel & {
    delegates?: Array<Record<string, unknown>>;
    turnSlots?: Record<string, unknown>;
  };

  expect(model.delegates).toHaveLength(1);
  expect(model.delegates?.[0]).toMatchObject({
    delegateId: "dlg_lossless",
    message: null,
    structuredResult: null,
    structuredResultValid: true,
    structuredResultReason: "explicit null",
    exhaustionBudget: "turns",
    exhaustionLimit: 12,
    exhaustionResumable: false,
    runningForMs: null,
    quietForMs: 1000,
    durationMs: 60000,
    usage: { inputTokens: 41, outputTokens: 7, cacheReadTokens: 3, totalTokens: 48 },
    worktree: { path: "/tmp/wt", branch: "delegate/dlg_lossless", headSha: "abc", ahead: 2, dirty: true },
    warnings: ["warning one"],
    diagnostics: ["observer armed"],
    delegationAllowance: 2,
    parentWatchGranted: true,
  });
  expect(model.delegates?.[0]).not.toHaveProperty("waitIgnoredReason");
  expect(model.turnSlots).toEqual({ inUse: 1, cap: 4, jobs: 2, driveTurns: 1 });
});

test("delta accumulates into pendingText chunks and joins on completion", () => {
  let model = testHydrate();
  model = applyNotification(
    model,
    {
      method: "turn/started",
      params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } },
    },
    1001,
  );
  model = applyNotification(
    model,
    {
      method: "item/started",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        item: { type: "agentMessage", id: "item_1", turnId: "turn_1", status: "inProgress" },
      },
    },
    1002,
  );
  model = applyNotification(
    model,
    {
      method: "item/agentMessage/delta",
      params: { threadId: "thr_t", ref: "ref_t", turnId: "turn_1", itemId: "item_1", delta: "Hel" },
    },
    1003,
  );
  model = applyNotification(
    model,
    {
      method: "item/agentMessage/delta",
      params: { threadId: "thr_t", ref: "ref_t", turnId: "turn_1", itemId: "item_1", delta: "lo" },
    },
    1004,
  );

  const streaming = itemAt(turnAt(model, 0), 0);
  expect(streaming.pendingText).toEqual(["Hel", "lo"]);
  expect(streaming.text).toBe(""); // not yet settled — the model never string-concats deltas into text

  model = applyNotification(
    model,
    {
      method: "item/completed",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        item: { type: "agentMessage", id: "item_1", turnId: "turn_1", text: "Hello!", status: "completed" },
      },
    },
    1005,
  );

  const settled = itemAt(turnAt(model, 0), 0);
  expect(settled.text).toBe("Hello!"); // payload text ("Hello!") wins over the joined chunks ("Hello")
  expect(settled.pendingText).toBeUndefined();
});

// --- O(1) per-delta accumulation (perf fix, PR3) ----------------------------
// Rationale and machinery: reducer.ts's chunk-view section header.

// The shared streaming-agentMessage scaffold (src/protocol/testing/
// tokenFlood.ts) on this suite's thr_t/ref_t/turn_1/item_1 identity.
function streamingItem(): ThreadModel {
  return hydrateStreamingAgentMessage("ref_t", { threadId: "thr_t" });
}

function agentMessageDelta(delta: string): AnyNotification {
  return {
    method: "item/agentMessage/delta",
    params: { threadId: "thr_t", ref: "ref_t", turnId: "turn_1", itemId: "item_1", delta },
  };
}

// The O(1)-append test's ceiling is a tripwire for a hang, not a
// responsiveness bar: its 20,000-delta fold measures ~0.4s in isolation and
// in-suite, so the 5s default holds ~12x headroom — until the whole gate's
// concurrent Go and vitest streams saturate the runner and a single worker
// loses more than that (the #672 CI failure: 5,000ms exceeded, same tree
// green on rerun). Sized like hookTimeout/WARM_ROUTE_TRIPWIRE_MS: well above
// the work, still bounded, and a regression to O(n^2) blows through it
// regardless (a copy per delta is ~200M string copies at N=20,000).
const O1_APPEND_TRIPWIRE_MS = 30_000;

test("every delta's fold appends onto the SAME backing array, which grows by exactly one (O(1) append)", {
  timeout: O1_APPEND_TRIPWIRE_MS,
}, () => {
  // White-box on purpose (chunkViewBackingForTests): chunk strings are
  // PRIMITIVES, so element-level identity checks survive a per-delta copy
  // ([...chunks, delta] preserves every string reference) — only the
  // BACKING ARRAY reference distinguishes a true append from a copy: a
  // copy mints a fresh backing per delta, an append returns the same
  // array one longer. Each iteration therefore asserts (a) the backing
  // reference is IDENTICAL to the previous fold's and (b) its length grew
  // by exactly one, keeping the test O(n) — it must not recreate the very
  // blowup it guards against. Rationale: reducer.ts's chunk-view header.
  const N = 20_000;
  let model = streamingItem();
  let prevBacking: string[] | undefined;
  for (let i = 0; i < N; i++) {
    model = applyNotification(model, agentMessageDelta(`c${i} `), 1003 + i);
    const chunks = itemAt(turnAt(model, 0), 0).pendingText;
    expect(chunks).toHaveLength(i + 1);
    const backing = chunkViewBackingForTests(chunks ?? []);
    expect(backing).toBeDefined();
    if (i === 0) {
      prevBacking = backing;
    } else {
      expect(backing).toBe(prevBacking);
      expect(backing?.length).toBe(i + 1);
    }
  }
  const chunks = itemAt(turnAt(model, 0), 0).pendingText;
  expect(chunks?.length).toBe(N);
  expect(chunks).toEqual(Array.from({ length: N }, (_, i) => `c${i} `));
  // And the O(1) joined-text cache agrees with a full structural join.
  expect(chunks?.join("")).toBe(pendingTextJoined(chunks ?? []));
});

test("a mid-stream model state stays observationally frozen while later deltas continue folding (view purity)", () => {
  let model = streamingItem();
  model = applyNotification(model, agentMessageDelta("Hel"), 1003);
  model = applyNotification(model, agentMessageDelta("lo"), 1004);
  const frozen = itemAt(turnAt(model, 0), 0);
  const snapshot = [...(frozen.pendingText ?? [])];
  // Snapshot the JOIN too — the most common read (settleItem, renderers).
  const joined = frozen.pendingText?.join("");
  // Keep folding well past the snapshotted state.
  for (let i = 0; i < 500; i++) {
    model = applyNotification(model, agentMessageDelta("x"), 1005 + i);
  }
  expect(frozen.pendingText?.length).toBe(2);
  expect([...(frozen.pendingText ?? [])]).toEqual(snapshot);
  expect(frozen.pendingText?.join("")).toBe(joined);
  // And the live item carries all 502 chunks, first two unchanged.
  const live = itemAt(turnAt(model, 0), 0);
  expect(live.pendingText?.length).toBe(502);
  expect(live.pendingText?.slice(0, 2)).toEqual(["Hel", "lo"]);
  // Mutating traps throw rather than corrupting the shared backing.
  expect(() => (live.pendingText as string[]).push("y")).toThrow();
  expect(() => {
    (live.pendingText as string[])[0] = "z";
  }).toThrow();
  // Settling after the fold still joins exactly the streamed text.
  model = applyNotification(
    model,
    {
      method: "turn/completed",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        turn: { id: "turn_1", status: "completed", itemsView: "" },
      },
    },
    2000,
  );
  const settled = itemAt(turnAt(model, 0), 0);
  expect(settled.text).toBe(`Hello${"x".repeat(500)}`);
  expect(settled.pendingText).toBeUndefined();
});

test("a delta folded onto a STALE mid-stream state branches cleanly — no aliasing into the newer fold (copy-on-branch)", () => {
  let base = streamingItem();
  base = applyNotification(base, agentMessageDelta("a"), 1003);
  base = applyNotification(base, agentMessageDelta("b"), 1004);
  const branchedAt = base;

  // One line of history continues from branchedAt...
  let live = branchedAt;
  for (let i = 0; i < 100; i++) {
    live = applyNotification(live, agentMessageDelta("L"), 1100 + i);
  }
  const liveChunks = itemAt(turnAt(live, 0), 0).pendingText;
  expect(liveChunks?.length).toBe(102);
  expect(liveChunks?.join("")).toBe(`ab${"L".repeat(100)}`);

  // ...and a second fold from the SAME stale state takes its own branch.
  // The stale state's view is not the backing's newest, so this append
  // must NOT push into the backing the live branch reads — it copies.
  let fork = branchedAt;
  for (let i = 0; i < 100; i++) {
    fork = applyNotification(fork, agentMessageDelta("F"), 1200 + i);
  }
  const forkChunks = itemAt(turnAt(fork, 0), 0).pendingText;
  expect(forkChunks?.length).toBe(102);
  expect(forkChunks?.join("")).toBe(`ab${"F".repeat(100)}`);
});

test("item/completed inserts an item that had no preceding item/started", () => {
  // userMessage and systemMessage items go straight to item/completed with
  // no item/started (internal/appprojector/appwire_projection.go: a new user
  // turn emits turn/started with an empty turn, then item/completed for the
  // userMessage — item/started is never sent for it).
  let model = testHydrate();
  model = applyNotification(
    model,
    {
      method: "turn/started",
      params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } },
    },
    1001,
  );
  expect(turnAt(model, 0).items).toHaveLength(0);

  model = applyNotification(
    model,
    {
      method: "item/completed",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        item: { type: "userMessage", id: "item_user", turnId: "turn_1", text: "Hi there", status: "completed" },
      },
    },
    1002,
  );

  const item = itemAt(turnAt(model, 0), 0);
  expect(item.type).toBe("userMessage");
  expect(item.text).toBe("Hi there");
});

// transcriptEntryIndex is the item's 1-based position in the parent
// transcript's entry list (appwire.ThreadItem.TranscriptEntryIndex), and it is
// the ONLY field that names a fork divergence position: thread/fork's
// sourceTurnId is read as that index, while a LIVE turn id is numbered off a
// different counter entirely. The model must therefore carry it through
// verbatim rather than leaving renderers to reach for turnId.
test("wire items carry transcriptEntryIndex into the model - it is what thread/fork's divergence position is read from", () => {
  let model = testHydrate();
  model = applyNotification(
    model,
    {
      method: "turn/started",
      params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_2", status: "inProgress", itemsView: "" } },
    },
    1001,
  );
  model = applyNotification(
    model,
    {
      method: "item/completed",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_2",
        item: {
          type: "userMessage",
          id: "item_user",
          turnId: "turn_2",
          transcriptEntryIndex: 5,
          text: "second task",
          status: "completed",
        },
      },
    },
    1002,
  );
  expect(itemAt(turnAt(model, 0), 0).transcriptEntryIndex).toBe(5);

  // Absent on the wire stays absent in the model: a missing index means "this
  // entry has no persisted transcript position", never entry 0.
  const hydrated = hydrateThread(
    {
      thread: testThread({
        turns: [
          {
            id: "turn_1",
            status: "completed",
            itemsView: "full",
            items: [{ id: "item_a", turnId: "turn_1", type: "userMessage", text: "hello" }],
          },
        ],
      }),
    },
    "ref_t",
    1000,
  );
  expect(itemAt(turnAt(hydrated, 0), 0).transcriptEntryIndex).toBeUndefined();
});

test("keyless item/completed updates a keyed hydrated item without losing identity metadata", () => {
  let model = testHydrate({
    turns: [
      {
        id: "turn_1",
        status: "inProgress",
        itemsView: "full",
        items: [
          {
            id: "item_1",
            turnId: "turn_1",
            transcriptKey: "transcript:item_1",
            position: { entry: 4, item: 2 },
            type: "agentMessage",
            text: "old",
            status: "inProgress",
          },
        ],
      },
    ],
  });

  model = applyNotification(
    model,
    {
      method: "item/completed",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        item: { id: "item_1", turnId: "turn_1", type: "agentMessage", text: "current", status: "completed" },
      },
    },
    1001,
  );

  const item = itemAt(turnAt(model, 0), 0);
  expect(turnAt(model, 0).items).toHaveLength(1);
  expect(item).toMatchObject({
    id: "item_1",
    text: "current",
    status: "completed",
    transcriptKey: "transcript:item_1",
    position: { entry: 4, item: 2 },
  });
});

test("turn/completed only merges same-ID keyless items, not conflicting keys or unrelated IDs", () => {
  let model = testHydrate({
    turns: [
      {
        id: "turn_1",
        status: "completed",
        itemsView: "full",
        items: [
          {
            id: "item_1",
            turnId: "turn_1",
            transcriptKey: "transcript:item_1",
            position: { entry: 4, item: 2 },
            type: "agentMessage",
            text: "old",
            status: "completed",
          },
        ],
      },
    ],
  });

  const completed = (item: ThreadItem): AnyNotification => ({
    method: "turn/completed",
    params: {
      threadId: "thr_t",
      ref: "ref_t",
      turnId: "turn_1",
      turn: { id: "turn_1", status: "completed", itemsView: "full", items: [item] },
    },
  });

  model = applyNotification(
    model,
    completed({ id: "item_1", turnId: "turn_1", type: "agentMessage", text: "keyless current", status: "completed" }),
    1001,
  );
  expect(turnAt(model, 0).items).toHaveLength(1);
  expect(itemAt(turnAt(model, 0), 0).text).toBe("keyless current");

  model = applyNotification(
    model,
    completed({
      id: "item_1",
      turnId: "turn_1",
      transcriptKey: "transcript:other",
      type: "agentMessage",
      text: "conflicting key",
      status: "completed",
    }),
    1002,
  );
  expect(turnAt(model, 0).items).toHaveLength(2);

  model = applyNotification(
    model,
    completed({ id: "item_unrelated", turnId: "turn_1", type: "agentMessage", text: "unrelated", status: "completed" }),
    1003,
  );
  expect(turnAt(model, 0).items).toHaveLength(3);
});

test("active full turn/completed preserves only identity-matched hydrated item metadata", () => {
  const keepPosition = { entry: 8, item: 0 };
  const conflictingPosition = { entry: 8, item: 1 };
  const oldPosition = { entry: 8, item: 2 };
  let model = testHydrate({
    status: { type: "active" },
    evener: { activeTurnId: "turn_active" },
    turns: [
      {
        id: "turn_active",
        status: "inProgress",
        itemsView: "full",
        items: [
          {
            id: "item_keep",
            turnId: "turn_active",
            transcriptKey: "transcript:item_keep",
            position: keepPosition,
            type: "reasoning",
            text: "hydrated reasoning",
            argumentsJson: '{"from":"hydrate"}',
            status: "inProgress",
          },
          {
            id: "item_conflicting",
            turnId: "turn_active",
            transcriptKey: "transcript:item_conflicting:old",
            position: conflictingPosition,
            type: "agentMessage",
            text: "old conflicting item",
            status: "completed",
          },
          {
            id: "item_old",
            turnId: "turn_active",
            transcriptKey: "transcript:item_old",
            position: oldPosition,
            type: "agentMessage",
            text: "removed by authoritative stamp",
            status: "completed",
          },
        ],
      },
    ],
  });
  expect(model.activeTurnId).toBe("turn_active");

  // This live delta gives the hydrated item the model-only fields that the
  // existing full-stamp merge must continue preserving.
  model = applyNotification(
    model,
    {
      method: "item/reasoning/summaryTextDelta",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_active",
        itemId: "item_keep",
        summaryIndex: 0,
        delta: " + live",
      },
    },
    1100,
  );
  const beforeCompletion = itemAt(turnAt(model, 0), 0);
  expect(beforeCompletion.observedStartedAt).toBeDefined();
  expect(beforeCompletion.reasoningSummaries).toEqual([["hydrated reasoning", " + live"]]);

  model = applyNotification(
    model,
    {
      method: "turn/completed",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_active",
        turn: {
          id: "turn_active",
          status: "completed",
          itemsView: "full",
          items: [
            {
              id: "item_conflicting",
              turnId: "turn_active",
              transcriptKey: "transcript:item_conflicting:new",
              type: "agentMessage",
              text: "new conflicting item",
              status: "completed",
            },
            {
              id: "item_keep",
              turnId: "turn_active",
              type: "reasoning",
              text: "final reasoning",
              status: "completed",
            },
            {
              id: "item_new",
              turnId: "turn_active",
              type: "agentMessage",
              text: "new unrelated item",
              status: "completed",
            },
          ],
        },
      },
    },
    1200,
  );

  const settledTurn = turnAt(model, 0);
  expect(model.activeTurnId).toBeUndefined();
  expect(settledTurn.status).toBe("completed");
  expect(settledTurn.items.map((item) => item.id)).toEqual(["item_conflicting", "item_keep", "item_new"]);
  expect(settledTurn.items).toHaveLength(3);

  const conflicting = itemAt(settledTurn, 0);
  expect(conflicting).toMatchObject({
    id: "item_conflicting",
    transcriptKey: "transcript:item_conflicting:new",
    text: "new conflicting item",
    status: "completed",
  });
  expect(conflicting.position).toBeUndefined();

  const keep = itemAt(settledTurn, 1);
  expect(keep).toMatchObject({
    id: "item_keep",
    text: "final reasoning",
    status: "completed",
    transcriptKey: "transcript:item_keep",
    position: keepPosition,
    argumentsJSON: '{"from":"hydrate"}',
    observedStartedAt: beforeCompletion.observedStartedAt,
    observedCompletedAt: new Date(1200).toISOString(),
  });
  expect(keep.reasoningSummaries).toEqual([["hydrated reasoning", " + live"]]);

  const unrelated = itemAt(settledTurn, 2);
  expect(unrelated).toMatchObject({ id: "item_new", text: "new unrelated item", status: "completed" });
  expect(unrelated.transcriptKey).toBeUndefined();
  expect(unrelated.position).toBeUndefined();
});

test("agentMessage/reset discards the in-flight item", () => {
  let model = testHydrate();
  model = applyNotification(
    model,
    {
      method: "turn/started",
      params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } },
    },
    1001,
  );
  model = applyNotification(
    model,
    {
      method: "item/started",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        item: { type: "agentMessage", id: "item_1", turnId: "turn_1", status: "inProgress" },
      },
    },
    1002,
  );
  expect(turnAt(model, 0).items).toHaveLength(1);

  model = applyNotification(
    model,
    {
      method: "item/agentMessage/reset",
      params: { threadId: "thr_t", ref: "ref_t", turnId: "turn_1", itemId: "item_1" },
    },
    1003,
  );
  expect(turnAt(model, 0).items).toHaveLength(0);
});

test("notification for a different thread is ignored (same object returned)", () => {
  const model = testHydrate();
  const result = applyNotification(
    model,
    {
      method: "thread/status/changed",
      params: { threadId: "thr_t", ref: "some_other_ref", status: { type: "active" } },
    },
    2000,
  );
  expect(result).toBe(model);
});

test("notification method with no handler leaves the model unchanged", () => {
  // evener/auth/updated is a real, known NotificationName the reducer does not
  // model any state for (ThreadModel has no auth-provider fields); it also
  // carries neither ref nor threadId, so it can never target a thread.
  const model = testHydrate();
  const result = applyNotification(model, { method: "evener/auth/updated", params: {} }, 2000);
  expect(result).toBe(model);
});

test("notificationTargetsThread matches on ref, falls back to threadId, else false", () => {
  const model = testHydrate();
  expect(
    notificationTargetsThread(
      {
        method: "thread/status/changed",
        params: { threadId: "thr_t", ref: "ref_t", status: { type: "active" } },
      },
      model,
    ),
  ).toBe(true);
  expect(
    notificationTargetsThread(
      {
        method: "thread/status/changed",
        params: { threadId: "thr_t", ref: "not_ref_t", status: { type: "active" } },
      },
      model,
    ),
  ).toBe(false);
  // v2 notifications carry both authoritative identities.
  expect(
    notificationTargetsThread(
      { method: "evener/task/updated", params: { threadId: "thr_t", ref: "ref_t", total: 1, done: 0 } },
      model,
    ),
  ).toBe(true);
  expect(
    notificationTargetsThread(
      { method: "evener/task/updated", params: { threadId: "not_thr_t", ref: "not_ref_t", total: 1, done: 0 } },
      model,
    ),
  ).toBe(false);
  // Neither field present (e.g. evener/auth/updated) targets no thread model.
  expect(notificationTargetsThread({ method: "evener/auth/updated", params: {} }, model)).toBe(false);
});

test("evener/jobs/treeUpdated updates only jobsTreeRevision and jobsUpdatedAt, not lastFrameAt", () => {
  const model = testHydrate();

  const updated = applyNotification(
    model,
    {
      method: "evener/jobs/treeUpdated",
      params: { threadId: "thr_t", ref: "ref_t", revision: 9 },
    },
    2000,
  );

  expect(updated.jobsTreeRevision).toBe(9);
  expect(updated.jobsUpdatedAt).toBe(2000);
  expect(updated.lastFrameAt).toBe(model.lastFrameAt);
});

test("turn/completed applies with authoritative ref and thread identity", () => {
  let model = testHydrate();
  model = applyNotification(
    model,
    {
      method: "turn/started",
      params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } },
    },
    1001,
  );
  const beforeCompletion = model;
  const turnCompleted: AnyNotification = {
    method: "turn/completed",
    params: {
      threadId: "thr_t",
      ref: "ref_t",
      turnId: "turn_1",
      turn: { id: "turn_1", status: "completed", itemsView: "", items: [] },
    },
  };

  expect(notificationTargetsThread(turnCompleted, model)).toBe(true);
  const wrongThreadCompletion: AnyNotification = {
    ...turnCompleted,
    params: { ...turnCompleted.params, threadId: "thr_other", ref: "ref_other" },
  };
  expect(applyNotification(model, wrongThreadCompletion, 1002)).toBe(model);
  model = applyNotification(model, turnCompleted, 1002);

  expect(model).not.toBe(beforeCompletion);
  expect(turnAt(model, 0).status).toBe("completed");
  expect(model.activeTurnId).toBeUndefined();
});

test("turn/completed does not cross-apply to a different thread's same-numbered turn", () => {
  // Turn IDs are per-thread sequential ("turn_%d") and turn/completed carries
  // no ref/threadId, so two unrelated threads can each legitimately have
  // their own "turn_1" — one active on thread A, one long since settled on
  // thread B. Applying A's completion notification to B's model (e.g. a
  // store-layer routing bug, or delivery before the store learns better)
  // must be a true no-op: same reference, B's content untouched.
  const threadA = testThread({
    id: "thr_a",
    evener: { ref: "ref_a", capabilities: CAPABILITIES, queue: { revision: 0 } },
  });
  let modelA = hydrateThread({ thread: threadA }, threadA.evener.ref, 1000);
  modelA = applyNotification(
    modelA,
    {
      method: "turn/started",
      params: { threadId: "thr_a", ref: "ref_a", turn: { id: "turn_1", status: "inProgress", itemsView: "" } },
    },
    1001,
  );
  expect(modelA.activeTurnId).toBe("turn_1");

  // Stream A's own item BEFORE settling — wire-true: the real turn/completed
  // never carries items (see the "turn/completed preserves..." test below),
  // so A's item must already be in the model via item/completed, not
  // smuggled in through the settle payload.
  modelA = applyNotification(
    modelA,
    {
      method: "item/completed",
      params: {
        threadId: "thr_a",
        ref: "ref_a",
        turnId: "turn_1",
        item: { type: "agentMessage", id: "item_a1", turnId: "turn_1", text: "A's answer", status: "completed" },
      },
    },
    1500,
  );

  const threadB = testThread({
    id: "thr_b",
    evener: { ref: "ref_b", capabilities: CAPABILITIES, queue: { revision: 0 } },
    turns: [
      {
        id: "turn_1",
        status: "completed",
        itemsView: "full",
        items: [{ type: "agentMessage", id: "item_b1", turnId: "turn_1", text: "B's own answer", status: "completed" }],
      },
    ],
  });
  const modelB = hydrateThread({ thread: threadB }, threadB.evener.ref, 1000);
  expect(modelB.activeTurnId).toBeUndefined();

  // The real wire's turn/completed is a bare stamp (see
  // internal/appprojector/appwire_projection.go: EventUserInput,
  // EventGoalContinuation, EventError, EventSessionEnd all emit
  // Turn{ID,Status[,Error]} with Items nil, ItemsView "") — no items key.
  const aTurnCompleted: AnyNotification = {
    method: "turn/completed",
    params: {
      threadId: "thr_a",
      ref: "ref_a",
      turnId: "turn_1",
      turn: { id: "turn_1", status: "completed", itemsView: "" },
    },
  };

  // Sanity: the same notification legitimately completes A's own active
  // turn — and A's already-streamed item SURVIVES the bare settle stamp.
  const settledA = applyNotification(modelA, aTurnCompleted, 2000);
  expect(settledA).not.toBe(modelA);
  expect(itemAt(turnAt(settledA, 0), 0).text).toBe("A's answer");

  // But applying it to B — which merely happens to share the turn id — must
  // no-op entirely.
  const result = applyNotification(modelB, aTurnCompleted, 2000);
  expect(result).toBe(modelB);
  expect(itemAt(turnAt(result, 0), 0).text).toBe("B's own answer");
});

// szw1: reducer.ts's turn/started and turn/completed have no defense against
// a duplicate turn id in model.turns. Both known causes of a live collision
// (eptj, bz2z) are fixed server-side, so this is hardening against a defect
// this reducer would still have if a duplicate ever arrived by some other
// path — turns is presented everywhere else (mapTurn, findItemTurnId) as if
// ids are unique. The failure must not be both silent and destructive: a
// duplicate id is loudly reported (console.error — a reducer is a bad place
// to throw) AND handled without clobbering unrelated data.
test("turn/started with an id already in model.turns replaces that turn in place, loudly, instead of appending a duplicate row", () => {
  const spy = vi.spyOn(console, "error").mockImplementation(() => {});
  let model = testHydrate();
  model = applyNotification(
    model,
    {
      method: "turn/started",
      params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } },
    },
    1001,
  );
  model = applyNotification(
    model,
    {
      method: "item/completed",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        item: { type: "agentMessage", id: "item_old", turnId: "turn_1", text: "old content", status: "completed" },
      },
    },
    1002,
  );
  expect(model.turns).toHaveLength(1);

  // A second turn/started arrives reusing "turn_1" — the shape this kata is
  // hardening against, not a reachable defect after eptj/bz2z.
  model = applyNotification(
    model,
    {
      method: "turn/started",
      params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } },
    },
    1003,
  );

  expect(model.turns).toHaveLength(1); // never TWO rows sharing one id
  expect(turnAt(model, 0).items).toHaveLength(0); // the fresh turn replaced the old one in place
  expect(spy).toHaveBeenCalledTimes(1);
  expect(spy.mock.calls[0]?.[0]).toMatch(/turn\/started.*turn_1.*already exists/i);
  spy.mockRestore();
});

test("turn/completed settles only the FIRST turn matching a duplicated id, leaving any other same-id turn untouched (not silently overwritten)", () => {
  const spy = vi.spyOn(console, "error").mockImplementation(() => {});
  // Construct the corrupted state directly (two turns sharing "turn_1") —
  // this is the shape a duplicate-id collision would produce; the reducer
  // must not make it worse by clobbering the second entry's unrelated
  // content when settling the first.
  const thread = testThread({
    turns: [
      {
        id: "turn_1",
        status: "inProgress",
        itemsView: "full",
        items: [
          {
            type: "agentMessage",
            id: "item_first",
            turnId: "turn_1",
            text: "first turn's content",
            status: "inProgress",
          },
        ],
      },
      {
        id: "turn_1",
        status: "completed",
        itemsView: "full",
        items: [
          {
            type: "agentMessage",
            id: "item_second",
            turnId: "turn_1",
            text: "unrelated persisted content",
            status: "completed",
          },
        ],
      },
    ],
  });
  let model = hydrateThread({ thread }, thread.evener.ref, 1000);
  model = { ...model, activeTurnId: "turn_1" };

  model = applyNotification(
    model,
    {
      method: "turn/completed",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        turn: { id: "turn_1", status: "completed", itemsView: "" },
      },
    },
    2000,
  );

  expect(model.turns).toHaveLength(2);
  // The first turn settled normally, keeping its own item.
  expect(itemAt(turnAt(model, 0), 0).text).toBe("first turn's content");
  expect(turnAt(model, 0).status).toBe("completed");
  // The second turn's unrelated content must survive untouched — not
  // overwritten with the first turn's settle stamp.
  expect(itemAt(turnAt(model, 1), 0).text).toBe("unrelated persisted content");
  expect(spy).toHaveBeenCalledTimes(1);
  expect(spy.mock.calls[0]?.[0]).toMatch(/turn\/completed.*turn_1.*2 turns/i);
  spy.mockRestore();
});

// Part A regression coverage: the real wire's turn/completed is a bare
// status/timing stamp with no items (see the case's own comment in
// reducer.ts for the full projector-site citation list). A settled turn
// must KEEP whatever items the model already accumulated via
// item/started + deltas + item/completed, not wipe them.

test("turn/completed with a bare stamp preserves the turn's already-streamed items", () => {
  let model = testHydrate();
  model = applyNotification(
    model,
    {
      method: "turn/started",
      params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } },
    },
    1001,
  );
  model = applyNotification(
    model,
    {
      method: "item/completed",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        item: { type: "agentMessage", id: "item_1", turnId: "turn_1", text: "Hello, world!", status: "completed" },
      },
    },
    1002,
  );
  expect(turnAt(model, 0).items).toHaveLength(1);

  model = applyNotification(
    model,
    {
      method: "turn/completed",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        turn: { id: "turn_1", status: "completed", itemsView: "" },
      },
    },
    1003,
  );

  const items = turnAt(model, 0).items;
  expect(items).toHaveLength(1);
  expect(itemAt(turnAt(model, 0), 0).text).toBe("Hello, world!");
});

test("turn/completed's bare stamp fields (status, timing, usage, cost) land on the settled turn", () => {
  let model = testHydrate();
  model = applyNotification(
    model,
    {
      method: "turn/started",
      params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } },
    },
    1001,
  );

  model = applyNotification(
    model,
    {
      method: "turn/completed",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        turn: {
          id: "turn_1",
          status: "completed",
          itemsView: "",
          startedAt: 5000,
          completedAt: 6500,
          durationMs: 1500,
          usage: { inputTokens: 10, outputTokens: 20 },
          cost: "0.01",
        },
      },
    },
    1002,
  );

  const turn = turnAt(model, 0);
  expect(turn.status).toBe("completed");
  expect(turn.startedAt).toBe(new Date(5000).toISOString());
  expect(turn.completedAt).toBe(new Date(6500).toISOString());
  expect(turn.durationMs).toBe(1500);
  expect(turn.usage).toEqual({ inputTokens: 10, outputTokens: 20 });
  expect(turn.cost).toBe("0.01");
});

test('turn/completed with itemsView "full" still replaces items, and mergeReasoning still preserves reasoningSummaries', () => {
  // itemsView "full" is the snapshot/hydration projector's own discriminator
  // (internal/apptranscript/apptranscript.go:58,520); the one live site that
  // sends it on turn/completed is systemAnnouncementWithRaw's no-active-turn
  // branch (internal/appprojector/appwire_projection.go:~963). This payload
  // shape must still fully replace items, same as before this task's fix.
  let model = testHydrate();
  model = applyNotification(
    model,
    {
      method: "turn/started",
      params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } },
    },
    1001,
  );
  model = applyNotification(
    model,
    {
      method: "item/started",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        item: { type: "reasoning", id: "item_r", turnId: "turn_1", status: "inProgress" },
      },
    },
    1002,
  );
  model = applyNotification(
    model,
    {
      method: "item/reasoning/summaryTextDelta",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        itemId: "item_r",
        summaryIndex: 0,
        delta: "thinking...",
      },
    },
    1003,
  );

  model = applyNotification(
    model,
    {
      method: "turn/completed",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        turn: {
          id: "turn_1",
          status: "completed",
          itemsView: "full",
          items: [{ type: "reasoning", id: "item_r", turnId: "turn_1", status: "completed" }],
        },
      },
    },
    1004,
  );

  const items = turnAt(model, 0).items;
  expect(items).toHaveLength(1);
  expect(itemAt(turnAt(model, 0), 0).reasoningSummaries).toEqual([["thinking..."]]);
});

test('turn/completed with itemsView "full" replaces items outright — a payload carrying a differently-id\'d item drops the streamed one entirely', () => {
  // The test above reuses the streamed item's id in the settle payload, so
  // it cannot distinguish a true replace from a preserve-and-merge that
  // happens to key on id (mergeReasoning's own same-id coverage already
  // exists there — this test targets the "full" branch's replace semantics
  // directly, independent of any id coincidence).
  let model = testHydrate();
  model = applyNotification(
    model,
    {
      method: "turn/started",
      params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } },
    },
    1001,
  );
  model = applyNotification(
    model,
    {
      method: "item/completed",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        item: { type: "agentMessage", id: "item_x", turnId: "turn_1", text: "X's text", status: "completed" },
      },
    },
    1002,
  );
  expect(turnAt(model, 0).items).toHaveLength(1);

  model = applyNotification(
    model,
    {
      method: "turn/completed",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        turn: {
          id: "turn_1",
          status: "completed",
          itemsView: "full",
          items: [{ type: "agentMessage", id: "item_y", turnId: "turn_1", text: "Y's text", status: "completed" }],
        },
      },
    },
    1003,
  );

  const items = turnAt(model, 0).items;
  expect(items).toHaveLength(1);
  const survivor = itemAt(turnAt(model, 0), 0);
  expect(survivor.id).toBe("item_y");
  expect(survivor.text).toBe("Y's text");
});

test("turn/completed's settle fold joins a mid-stream item's pendingText into text and flips inProgress to completed", () => {
  let model = testHydrate();
  model = applyNotification(
    model,
    {
      method: "turn/started",
      params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } },
    },
    1001,
  );
  model = applyNotification(
    model,
    {
      method: "item/started",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        item: { type: "agentMessage", id: "item_1", turnId: "turn_1", status: "inProgress" },
      },
    },
    1002,
  );
  model = applyNotification(
    model,
    {
      method: "item/agentMessage/delta",
      params: { threadId: "thr_t", ref: "ref_t", turnId: "turn_1", itemId: "item_1", delta: "Hel" },
    },
    1003,
  );
  model = applyNotification(
    model,
    {
      method: "item/agentMessage/delta",
      params: { threadId: "thr_t", ref: "ref_t", turnId: "turn_1", itemId: "item_1", delta: "lo" },
    },
    1004,
  );

  // Settle arrives mid-stream — no item/completed ever landed for item_1
  // (e.g. an interrupt or session end cut the stream short).
  model = applyNotification(
    model,
    {
      method: "turn/completed",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        turn: { id: "turn_1", status: "interrupted", itemsView: "" },
      },
    },
    1005,
  );

  const item = itemAt(turnAt(model, 0), 0);
  expect(item.text).toBe("Hello"); // joined chunks — no authoritative item/completed text ever arrived
  expect(item.pendingText).toBeUndefined();
  expect(item.status).toBe("completed"); // an in-progress item inside a settled turn is a lie
});

test("turn/completed's failed-turn stamp (EventError shape) preserves items and carries the error", () => {
  let model = testHydrate();
  model = applyNotification(
    model,
    {
      method: "turn/started",
      params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } },
    },
    1001,
  );
  model = applyNotification(
    model,
    {
      method: "item/completed",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        item: { type: "agentMessage", id: "item_1", turnId: "turn_1", text: "partial answer", status: "completed" },
      },
    },
    1002,
  );

  const error = { message: "rate limited", source: "provider", title: "Provider error" };
  model = applyNotification(
    model,
    {
      method: "turn/completed",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        turn: { id: "turn_1", status: "failed", itemsView: "", error },
      },
    },
    1003,
  );

  const turn = turnAt(model, 0);
  expect(turn.status).toBe("failed");
  expect(turn.error).toEqual(error);
  expect(turn.items).toHaveLength(1);
  expect(itemAt(turn, 0).text).toBe("partial answer");
});

test("turn/completed's failed-turn stamp folds a mid-stream item's pendingText AND carries the error together (real EventError shape)", () => {
  // The test above uses an item that was already completed before the
  // error arrived, so it never exercises settleItem's fold — it only
  // proves items survive and the error lands. EventError's actual shape
  // (internal/appprojector/appwire_projection.go:507-572) fires while an
  // item can still be mid-stream (e.g. a rate limit interrupts the agent
  // mid-message); this test uses that shape, asserting the fold and the
  // error land together in one settle.
  let model = testHydrate();
  model = applyNotification(
    model,
    {
      method: "turn/started",
      params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } },
    },
    1001,
  );
  model = applyNotification(
    model,
    {
      method: "item/started",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        item: { type: "agentMessage", id: "item_1", turnId: "turn_1", status: "inProgress" },
      },
    },
    1002,
  );
  model = applyNotification(
    model,
    {
      method: "item/agentMessage/delta",
      params: { threadId: "thr_t", ref: "ref_t", turnId: "turn_1", itemId: "item_1", delta: "partial " },
    },
    1003,
  );
  model = applyNotification(
    model,
    {
      method: "item/agentMessage/delta",
      params: { threadId: "thr_t", ref: "ref_t", turnId: "turn_1", itemId: "item_1", delta: "answer" },
    },
    1004,
  );

  const error = { message: "rate limited", source: "provider", title: "Provider error" };
  model = applyNotification(
    model,
    {
      method: "turn/completed",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        turn: { id: "turn_1", status: "failed", itemsView: "", error },
      },
    },
    1005,
  );

  const turn = turnAt(model, 0);
  expect(turn.status).toBe("failed");
  expect(turn.error).toEqual(error);
  const item = itemAt(turn, 0);
  expect(item.text).toBe("partial answer"); // joined chunks — no authoritative item/completed text ever arrived
  expect(item.pendingText).toBeUndefined();
  expect(item.status).toBe("completed"); // an in-progress item inside a settled turn is a lie
});

// Part B regression coverage: evener/steering/injected's payload is declared
// `nil` in the AppWire catalog, but the live projector
// (internal/appprojector/appwire_projection.go:573-593) actually sends
// {threadId, ref, text, images, source?} — source present ("user") only for
// human-sent steers, omitted for daemon-originated ones. A live steer into
// an in-flight turn must become a "steering" transcript item, mirroring how
// reload already renders persisted steering turns
// (internal/apptranscript/apptranscript.go:211-229).

test('evener/steering/injected with source "user" appends a steering item to the active turn', () => {
  let model = testHydrate();
  model = applyNotification(
    model,
    {
      method: "turn/started",
      params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } },
    },
    1001,
  );

  model = applyNotification(
    model,
    {
      method: "evener/steering/injected",
      params: { threadId: "thr_t", ref: "ref_t", text: "please also check X", source: "user" },
    },
    1002,
  );

  const items = turnAt(model, 0).items;
  expect(items).toHaveLength(1);
  expect(items[0]).toMatchObject({
    id: "item_steering_live_turn_1_0",
    turnId: "turn_1",
    type: "steering",
    text: "please also check X",
    status: "completed",
    source: "user",
  });
});

test("evener/steering/injected with no source field appends an item with source undefined", () => {
  let model = testHydrate();
  model = applyNotification(
    model,
    {
      method: "turn/started",
      params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } },
    },
    1001,
  );

  model = applyNotification(
    model,
    {
      method: "evener/steering/injected",
      params: { threadId: "thr_t", ref: "ref_t", text: "daemon steer text" },
    },
    1002,
  );

  const item = itemAt(turnAt(model, 0), 0);
  expect(item.source).toBeUndefined();
  expect(item.text).toBe("daemon steer text");
});

test("two steers in one turn get distinct ids in arrival order", () => {
  let model = testHydrate();
  model = applyNotification(
    model,
    {
      method: "turn/started",
      params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } },
    },
    1001,
  );

  model = applyNotification(
    model,
    { method: "evener/steering/injected", params: { threadId: "thr_t", ref: "ref_t", text: "first" } },
    1002,
  );
  model = applyNotification(
    model,
    {
      method: "evener/steering/injected",
      params: { threadId: "thr_t", ref: "ref_t", text: "second" },
    },
    1003,
  );

  const items = turnAt(model, 0).items;
  expect(items.map((it) => it.id)).toEqual(["item_steering_live_turn_1_0", "item_steering_live_turn_1_1"]);
  expect(items.map((it) => it.text)).toEqual(["first", "second"]);
});

test("evener/steering/injected with no active turn only updates lastFrameAt (no turn fabricated client-side)", () => {
  const model = testHydrate();
  expect(model.activeTurnId).toBeUndefined();

  const result = applyNotification(
    model,
    {
      method: "evener/steering/injected",
      params: { threadId: "thr_t", ref: "ref_t", text: "orphaned steer" },
    },
    2000,
  );

  expect(result).toEqual({ ...model, lastFrameAt: 2000 });
  expect(result.turns).toBe(model.turns); // same reference: no turn was touched, none fabricated
});

test("a steering item survives a bare turn/completed settle stamp (composition with Part A)", () => {
  let model = testHydrate();
  model = applyNotification(
    model,
    {
      method: "turn/started",
      params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } },
    },
    1001,
  );
  model = applyNotification(
    model,
    {
      method: "evener/steering/injected",
      params: { threadId: "thr_t", ref: "ref_t", text: "mid-turn steer" },
    },
    1002,
  );

  model = applyNotification(
    model,
    {
      method: "turn/completed",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        turn: { id: "turn_1", status: "completed", itemsView: "" },
      },
    },
    1003,
  );

  const items = turnAt(model, 0).items;
  expect(items).toHaveLength(1);
  expect(items[0]).toMatchObject({ type: "steering", text: "mid-turn steer", status: "completed" });
});

test("evener/steering/injected images populate display-ready ItemImages via the same conversion other item paths use", () => {
  // Steering images use the same appwire.InputItem shape as userMessage
  // images (internal/appprojector/appwire_projection.go's
  // projectUserInputImages: Type "image", MediaType, Data, Name — no
  // Url/Path), so imagesToItemImages resolves the inline bytes to a data:
  // URI src here, exactly as it would for any other image-bearing item
  // (kata w53n; the old name fallback made a relative URL the browser
  // 404s on) — and name still rides alongside src, not just consumed by it.
  let model = testHydrate();
  model = applyNotification(
    model,
    {
      method: "turn/started",
      params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } },
    },
    1001,
  );

  model = applyNotification(
    model,
    {
      method: "evener/steering/injected",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        text: "",
        images: [{ type: "image", mediaType: "image/png", data: "iVBORw0KGgo=", name: "screenshot.png" }],
      },
    },
    1002,
  );

  const item = itemAt(turnAt(model, 0), 0);
  expect(item.images).toEqual([{ src: "data:image/png;base64,iVBORw0KGgo=", name: "screenshot.png" }]);
});

// kata byq2: the reducer used to resolve each image down to one bare string
// (whichever of url/path/name/source won the fallback), so a renderer could
// never caption or (later) group an image — nothing survived to caption it
// WITH. These two pin that name/path/source now ride alongside the resolved
// src rather than being discarded, by picking fixtures where a DIFFERENT
// field wins the src fallback than the ones being asserted on — proving
// they're preserved in their own right, not just visible because they
// happened to become src.

test("item/completed resolves a data-carrying user image to a data: URI src (kata w53n)", () => {
  // A composer-attached image reaches the wire as inline bytes — Type
  // "image", MediaType, Data, Name, no Url/Path (appwire_projection.go's
  // projectUserInputImages). Falling through to name produced a relative
  // src the browser 404s on, so ImageGallery's onError dropped the
  // thumbnail and the transcript showed no image at all.
  let model = testHydrate();
  model = applyNotification(
    model,
    {
      method: "turn/started",
      params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } },
    },
    1001,
  );
  model = applyNotification(
    model,
    {
      method: "item/completed",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        item: {
          type: "userMessage",
          id: "item_user",
          turnId: "turn_1",
          text: "[image 1]what is this?",
          status: "completed",
          images: [{ type: "image", mediaType: "image/png", data: "iVBORw0KGgo=", name: "tiny.png" }],
        },
      },
    },
    1002,
  );

  const item = itemAt(turnAt(model, 0), 0);
  expect(item.images).toEqual([{ src: "data:image/png;base64,iVBORw0KGgo=", name: "tiny.png" }]);
});

test("item/completed preserves an input image's name alongside a src resolved from a different field", () => {
  let model = testHydrate();
  model = applyNotification(
    model,
    {
      method: "turn/started",
      params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } },
    },
    1001,
  );
  model = applyNotification(
    model,
    {
      method: "item/completed",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        item: {
          type: "userMessage",
          id: "item_user",
          turnId: "turn_1",
          text: "look at this",
          status: "completed",
          images: [{ type: "image", path: "uploads/photo.jpg", name: "photo.jpg" }],
        },
      },
    },
    1002,
  );

  const item = itemAt(turnAt(model, 0), 0);
  // path (not name) wins the src fallback here — name survives regardless.
  expect(item.images).toEqual([{ src: "uploads/photo.jpg", name: "photo.jpg", path: "uploads/photo.jpg" }]);
});

test("item/completed preserves an output image's name/path/source alongside a src resolved from yet another field", () => {
  let model = testHydrate();
  model = applyNotification(
    model,
    {
      method: "turn/started",
      params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } },
    },
    1001,
  );
  model = applyNotification(
    model,
    {
      method: "item/completed",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        item: {
          type: "commandExecution",
          id: "item_tool",
          turnId: "turn_1",
          toolName: "shell",
          callId: "call_1",
          status: "completed",
          outputImages: [{ source: "written-file", name: "plot.png", path: "out/plot.png" }],
        },
      },
    },
    1002,
  );

  const item = itemAt(turnAt(model, 0), 0);
  // path (not name, not source) wins the src fallback — name AND source both
  // still survive, distinct from each other and from src.
  expect(item.outputImages).toEqual([
    { src: "out/plot.png", name: "plot.png", path: "out/plot.png", source: "written-file" },
  ]);
});

// A still-streaming session's tool result is described by sha and routed by the
// hub (kata 2fxm), so the live descriptor arrives with a url and no path. src
// has to resolve to that url — a fallback to name would try to load the tool's
// own name as an image.
test("item/completed resolves a sha-routed tool-result image's src from its url", () => {
  let model = testHydrate();
  model = applyNotification(
    model,
    {
      method: "turn/started",
      params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } },
    },
    1001,
  );
  model = applyNotification(
    model,
    {
      method: "item/completed",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        item: {
          type: "commandExecution",
          id: "item_shot",
          turnId: "turn_1",
          toolName: "screenshot",
          callId: "call_shot",
          status: "completed",
          outputImages: [
            {
              source: "tool-result",
              name: "screenshot",
              mediaType: "image/png",
              size: 11,
              sha: "abc",
              url: "/s/02wMz5Txv733WHFsVy66SR/images/abc",
            },
          ],
        },
      },
    },
    1002,
  );

  expect(itemAt(turnAt(model, 0), 0).outputImages).toEqual([
    {
      src: "/s/02wMz5Txv733WHFsVy66SR/images/abc",
      name: "screenshot",
      path: undefined,
      source: "tool-result",
    },
  ]);
});

// Task 1-3 carried a typed kind (events.SteeringKind* on the Go side) onto
// the wire at each injection site, through to EvenerSteeringInjectedParams.kind
// on the live notification. The model must carry it the last hop onto the
// item so the transcript can label a steer from the wire kind instead of
// pattern-matching its prose (a later task's job — this one only carries the
// string).
test("a live steer carries its wire kind onto the item", () => {
  let model = testHydrate();
  model = applyNotification(
    model,
    {
      method: "turn/started",
      params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } },
    },
    1001,
  );

  model = applyNotification(
    model,
    {
      method: "evener/steering/injected",
      params: { threadId: "thr_t", ref: "ref_t", text: "You have completed all tasks", kind: "tasks-done" },
    },
    1002,
  );

  const item = itemAt(turnAt(model, 0), 0);
  expect(item.type).toBe("steering");
  expect(item.steeringKind).toBe("tasks-done");
});

test("a live steer with no wire kind leaves steeringKind undefined", () => {
  let model = testHydrate();
  model = applyNotification(
    model,
    {
      method: "turn/started",
      params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } },
    },
    1001,
  );

  model = applyNotification(
    model,
    {
      method: "evener/steering/injected",
      params: { threadId: "thr_t", ref: "ref_t", text: "something unclassified" },
    },
    1002,
  );

  expect(itemAt(turnAt(model, 0), 0).steeringKind).toBeUndefined();
});

test("prependOlderTurns keeps order and advances olderCursor", () => {
  const thread = testThread({ turns: [{ id: "turn_2", status: "completed", itemsView: "full", items: [] }] });
  const model = hydrateThread({ thread, olderCursor: "cursor_1" }, thread.evener.ref, 1000);

  const resp: ThreadTurnsListResponse = {
    data: [
      { id: "turn_0", status: "completed", itemsView: "full", items: [] },
      { id: "turn_1", status: "completed", itemsView: "full", items: [] },
    ],
    nextCursor: "cursor_0",
  };
  const result = prependOlderTurns(model, resp);

  expect(result.turns.map((t) => t.id)).toEqual(["turn_0", "turn_1", "turn_2"]);
  expect(result.olderCursor).toBe("cursor_0");
});

// ThreadTurnsListResponse.data is typed as a required Turn[], but that's a
// TS-side promise, not a wire guarantee - mirrors the same defense
// hydrateThread/wireToTurnModel already apply to thread.turns/turn.items
// (both `?? []`). A response missing `data` entirely (the Go zero value for
// a nil slice marshals as JSON `null`, not `[]`) must not crash and must
// behave as "an empty page" rather than losing the turns already in model.
test("prependOlderTurns tolerates a wire-nullable data array (treats it as an empty page)", () => {
  const thread = testThread({ turns: [{ id: "turn_2", status: "completed", itemsView: "full", items: [] }] });
  const model = hydrateThread({ thread, olderCursor: "cursor_1" }, thread.evener.ref, 1000);

  const resp = { nextCursor: "cursor_0" } as unknown as ThreadTurnsListResponse;
  const result = prependOlderTurns(model, resp);

  expect(result.turns.map((t) => t.id)).toEqual(["turn_2"]);
  expect(result.olderCursor).toBe("cursor_0");
});

test("mergeOlderItemPage merges shared turns and transcript items in position order with current precedence", () => {
  const thread = testThread({
    turns: [
      {
        id: "live-turn",
        status: "completed",
        itemsView: "full",
        items: [
          {
            id: "live-k1",
            transcriptKey: "k1",
            position: { entry: 2, item: 1 },
            turnId: "live-turn",
            type: "agentMessage",
            text: "new text",
            status: "completed",
          },
          {
            id: "live-k2",
            transcriptKey: "k2",
            position: { entry: 3, item: 1 },
            turnId: "live-turn",
            type: "agentMessage",
            text: "current-only",
            status: "completed",
          },
        ],
      },
    ],
  });
  const model = hydrateThread({ thread, olderCursor: "cursor_1" }, thread.evener.ref, 1000);
  const current = model.turns[0];
  if (!current) throw new Error("expected current turn");
  const currentItem = current.items[0];
  if (!currentItem) throw new Error("expected current item");
  currentItem.observedStartedAt = "1970-01-01T00:00:01.001Z";
  currentItem.observedCompletedAt = "1970-01-01T00:00:01.002Z";
  currentItem.reasoningSummaries = [["kept reasoning"]];
  currentItem.outputImages = [{ src: "new-image" }];

  const result = mergeOlderItemPage(model, {
    data: [
      {
        id: "historical-turn",
        status: "inProgress",
        itemsView: "full",
        items: [
          {
            id: "old-k0",
            transcriptKey: "k0",
            position: { entry: 1, item: 1 },
            turnId: "historical-turn",
            type: "agentMessage",
            text: "older-only",
            status: "completed",
          },
          {
            id: "old-k1",
            transcriptKey: "k1",
            position: { entry: 2, item: 1 },
            turnId: "historical-turn",
            type: "agentMessage",
            text: "old duplicate",
            argumentsJson: "old arguments",
            outputImages: [{ url: "old-image", source: "old-source" }],
            status: "inProgress",
          },
        ],
      },
    ],
    nextCursor: "cursor_0",
    pageUnit: "item",
  });

  expect(result.turns).toHaveLength(1);
  expect(result.turns[0]?.items.map((item) => item.transcriptKey)).toEqual(["k0", "k1", "k2"]);
  expect(result.turns[0]?.items.filter((item) => item.transcriptKey === "k1")).toHaveLength(1);
  expect(result.turns[0]?.items[1]).toMatchObject({
    id: "live-k1",
    text: "new text",
    argumentsJSON: "old arguments",
    observedStartedAt: "1970-01-01T00:00:01.001Z",
    observedCompletedAt: "1970-01-01T00:00:01.002Z",
    reasoningSummaries: [["kept reasoning"]],
    outputImages: [{ src: "new-image" }],
    status: "completed",
  });
  expect(result.olderCursor).toBe("cursor_0");
});

test("mergeOlderItemPage retains the older status when an equal-rank newer item omits status", () => {
  const thread = testThread({
    turns: [
      {
        id: "shared-turn",
        status: "inProgress",
        itemsView: "fragment",
        items: [
          {
            id: "newer-item",
            transcriptKey: "shared-key",
            position: { entry: 4, item: 0 },
            turnId: "shared-turn",
            type: "agentMessage",
            text: "newer text without status",
          },
        ],
      },
    ],
  });
  const model = hydrateThread({ thread, olderCursor: "cursor_1", pageUnit: "item" }, thread.evener.ref, 1000);

  const result = mergeOlderItemPage(model, {
    data: [
      {
        id: "shared-turn",
        status: "inProgress",
        itemsView: "fragment",
        items: [
          {
            id: "older-item",
            transcriptKey: "shared-key",
            position: { entry: 4, item: 0 },
            turnId: "shared-turn",
            type: "agentMessage",
            text: "older text",
            status: "inProgress",
          },
        ],
      },
    ],
    pageUnit: "item",
  });

  expect(result.turns[0]?.items[0]).toMatchObject({ text: "newer text without status", status: "inProgress" });
});

test("mergeOlderItemPage retains older identity when a newer matching item omits it", () => {
  const thread = testThread({
    turns: [
      {
        id: "shared-turn",
        status: "completed",
        itemsView: "fragment",
        items: [
          {
            id: "same-id",
            transcriptKey: "",
            turnId: "shared-turn",
            type: "agentMessage",
            text: "newer unkeyed",
          },
        ],
      },
    ],
  });
  const model = hydrateThread({ thread, olderCursor: "cursor_1", pageUnit: "item" }, thread.evener.ref, 1000);
  const result = mergeOlderItemPage(model, {
    data: [
      {
        id: "shared-turn",
        status: "completed",
        itemsView: "fragment",
        items: [
          {
            id: "same-id",
            transcriptKey: "older-key",
            position: { entry: 1, item: 0 },
            turnId: "shared-turn",
            type: "agentMessage",
            text: "older keyed",
          },
        ],
      },
    ],
    pageUnit: "item",
  });
  expect(result.turns[0]?.items).toHaveLength(1);
  expect(result.turns[0]?.items[0]).toMatchObject({
    id: "same-id",
    text: "newer unkeyed",
    transcriptKey: "older-key",
    position: { entry: 1, item: 0 },
  });
});

test("mergeOlderItemPage lets newer defined identity replace older identity", () => {
  const thread = testThread({
    turns: [
      {
        id: "shared-turn",
        status: "completed",
        itemsView: "fragment",
        items: [
          {
            id: "same-id",
            transcriptKey: "newer-key",
            position: { entry: 2, item: 0 },
            turnId: "shared-turn",
            type: "agentMessage",
            text: "newer keyed",
          },
        ],
      },
    ],
  });
  const model = hydrateThread({ thread, olderCursor: "cursor_1", pageUnit: "item" }, thread.evener.ref, 1000);
  const result = mergeOlderItemPage(model, {
    data: [
      {
        id: "shared-turn",
        status: "completed",
        itemsView: "fragment",
        items: [
          {
            id: "same-id",
            turnId: "shared-turn",
            type: "agentMessage",
            text: "older unkeyed",
          },
        ],
      },
    ],
    pageUnit: "item",
  });
  expect(result.turns[0]?.items).toHaveLength(1);
  expect(result.turns[0]?.items[0]).toMatchObject({
    transcriptKey: "newer-key",
    position: { entry: 2, item: 0 },
    text: "newer keyed",
  });
});

test("mergeOlderItemPage position-orders the final items when pages arrive out of chronology", () => {
  const thread = testThread({
    turns: [
      {
        id: "shared-turn",
        status: "completed",
        itemsView: "fragment",
        items: [
          {
            id: "current-earlier",
            transcriptKey: "key-1",
            position: { entry: 1, item: 0 },
            turnId: "shared-turn",
            type: "agentMessage",
            text: "current earlier position",
          },
        ],
      },
    ],
  });
  const model = hydrateThread({ thread, olderCursor: "cursor_1", pageUnit: "item" }, thread.evener.ref, 1000);

  const result = mergeOlderItemPage(model, {
    data: [
      {
        id: "shared-turn",
        status: "completed",
        itemsView: "fragment",
        items: [
          {
            id: "arrived-later",
            transcriptKey: "key-3",
            position: { entry: 3, item: 0 },
            turnId: "shared-turn",
            type: "agentMessage",
            text: "later position arrived on older request",
          },
        ],
      },
    ],
    pageUnit: "item",
  });

  expect(result.turns[0]?.items.map((item) => item.transcriptKey)).toEqual(["key-1", "key-3"]);
});

test("mergeOlderItemPage preserves unmatched results and folds a result-only turn only after its call arrives", () => {
  const model = testHydrate();
  const resultOnly = {
    id: "result-turn",
    status: "completed",
    itemsView: "full",
    items: [
      {
        id: "item_tool_result_orphan",
        transcriptKey: "result-key",
        position: { entry: 1, item: 1 },
        turnId: "result-turn",
        type: "commandExecution",
        callId: "orphan-call",
        output: "orphan output",
        status: "completed",
      },
    ],
  };
  const visible = mergeOlderItemPage(model, { data: [resultOnly], nextCursor: "cursor_0", pageUnit: "item" });
  expect(visible.turns).toHaveLength(1);
  expect(visible.turns[0]?.items[0]?.output).toBe("orphan output");

  const withCall = mergeOlderItemPage(visible, {
    data: [
      {
        id: "call-turn",
        status: "completed",
        itemsView: "full",
        items: [
          {
            id: "item_tool_call_orphan",
            transcriptKey: "call-key",
            position: { entry: 0, item: 1 },
            turnId: "call-turn",
            type: "commandExecution",
            callId: "orphan-call",
            argumentsJson: "{}",
            status: "inProgress",
          },
        ],
      },
    ],
    nextCursor: "cursor_done",
    pageUnit: "item",
  });
  expect(withCall.turns).toHaveLength(1);
  expect(withCall.turns[0]?.items).toHaveLength(1);
  expect(withCall.turns[0]?.items[0]).toMatchObject({
    id: "item_tool_call_orphan",
    argumentsJSON: "{}",
    output: "orphan output",
    status: "completed",
  });
});

test("item/started upserts an existing transcript key instead of appending a duplicate", () => {
  let model = testHydrate({
    turns: [
      {
        id: "turn_1",
        status: "inProgress",
        itemsView: "full",
        items: [
          {
            id: "historical-id",
            transcriptKey: "same-key",
            position: { entry: 1, item: 1 },
            turnId: "turn_1",
            type: "agentMessage",
            text: "old",
            status: "inProgress",
          },
        ],
      },
    ],
  });
  model = applyNotification(
    model,
    {
      method: "item/started",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        item: {
          id: "live-id",
          transcriptKey: "same-key",
          position: { entry: 1, item: 1 },
          turnId: "turn_1",
          type: "agentMessage",
          text: "new",
          status: "inProgress",
        },
      },
    },
    2000,
  );
  expect(model.turns[0]?.items).toHaveLength(1);
  expect(model.turns[0]?.items[0]).toMatchObject({ id: "live-id", text: "new", transcriptKey: "same-key" });
});

test("item/completed settles an existing transcript key despite a different display ID", () => {
  let model = testHydrate({
    turns: [
      {
        id: "turn_1",
        status: "inProgress",
        itemsView: "fragment",
        items: [
          {
            id: "historical-id",
            transcriptKey: "stable-key",
            position: { entry: 1, item: 0 },
            turnId: "turn_1",
            type: "agentMessage",
            text: "partial",
            status: "inProgress",
          },
        ],
      },
    ],
  });
  model = applyNotification(
    model,
    {
      method: "item/completed",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        item: {
          id: "live-id",
          transcriptKey: "stable-key",
          position: { entry: 1, item: 0 },
          turnId: "turn_1",
          type: "agentMessage",
          text: "settled",
          status: "completed",
        },
      },
    },
    2000,
  );
  expect(model.turns[0]?.items).toHaveLength(1);
  expect(model.turns[0]?.items[0]).toMatchObject({
    id: "live-id",
    text: "settled",
    status: "completed",
    transcriptKey: "stable-key",
  });
});

test("item/completed retains legacy display-ID matching when stable identity is unavailable", () => {
  let model = testHydrate({
    turns: [
      {
        id: "turn_1",
        status: "inProgress",
        itemsView: "full",
        items: [{ id: "legacy-id", turnId: "turn_1", type: "agentMessage", text: "partial", status: "inProgress" }],
      },
    ],
  });
  model = applyNotification(
    model,
    {
      method: "item/completed",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        item: { id: "legacy-id", turnId: "turn_1", type: "agentMessage", text: "settled", status: "completed" },
      },
    },
    2000,
  );
  expect(model.turns[0]?.items).toHaveLength(1);
  expect(model.turns[0]?.items[0]).toMatchObject({ id: "legacy-id", text: "settled", status: "completed" });
});

// askPending is a THREAD-level wire signal (EvenerThread.askPending, mirroring
// the daemon's long-lived HasPendingAsk - "this session is waiting on a human
// answer", agent/session_tools_ask.go). It is snapshot-authoritative: only a
// wire snapshot (hydrateThread) sets it; no notification carries it (askPending
// appears only on EvenerThread in types.gen.ts). The AskDock derives its OWN,
// separate in-tool pending signal from ask_user items (composer/askDock), so
// the reducer must NOT recompute this thread field from item lifecycle - doing
// so clobbers the wire's authoritative value whenever items churn.
test("askPending is wire-authoritative from the thread snapshot", () => {
  const asking = testHydrate({
    evener: { ref: "ref_t", capabilities: CAPABILITIES, queue: { revision: 0 }, askPending: true },
  });
  expect(asking.askPending).toBe(true);

  const notAsking = testHydrate({
    evener: { ref: "ref_t", capabilities: CAPABILITIES, queue: { revision: 0 }, askPending: false },
  });
  expect(notAsking.askPending).toBe(false);

  // Absent on the wire (omitempty) defaults to false.
  expect(testHydrate().askPending).toBe(false);
});

test("item lifecycle never clobbers the wire's thread-level askPending", () => {
  const turnStarted: AnyNotification = {
    method: "turn/started",
    params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } },
  };
  const askUser = (method: "item/started" | "item/completed", status: string): AnyNotification => ({
    method,
    params: {
      threadId: "thr_t",
      ref: "ref_t",
      turnId: "turn_1",
      item: {
        type: "commandExecution",
        id: "item_ask",
        turnId: "turn_1",
        toolName: "ask_user",
        callId: "call_ask",
        status,
      },
    },
  });

  // A session the wire says is waiting on a human (askPending: true) stays
  // waiting across an ask_user call's whole open->settle lifecycle: the tool
  // call completing is NOT a wire signal that the thread-level ask was
  // answered (that arrives only via the next snapshot / HasPendingAsk).
  let waiting = testHydrate({
    evener: { ref: "ref_t", capabilities: CAPABILITIES, queue: { revision: 0 }, askPending: true },
  });
  waiting = applyNotification(waiting, turnStarted, 1001);
  waiting = applyNotification(waiting, askUser("item/started", "inProgress"), 1002);
  expect(waiting.askPending).toBe(true);
  waiting = applyNotification(waiting, askUser("item/completed", "completed"), 1003);
  expect(waiting.askPending).toBe(true);

  // Symmetrically, a thread the wire says is NOT waiting stays not-waiting when
  // an ask_user item merely opens: the reducer no longer fabricates a
  // thread-level true from item lifecycle either.
  let idle = testHydrate();
  idle = applyNotification(idle, turnStarted, 1001);
  idle = applyNotification(idle, askUser("item/started", "inProgress"), 1002);
  expect(idle.askPending).toBe(false);
});

// A failed/denied tool call carries its failure in the wire item's `error`
// field (ThreadItem.error) while its `status` is projected "completed"
// regardless (a known Go limitation). The model must carry `error` so a
// denied/errored ask is distinguishable from a clean completion.
test("item/completed maps the wire item's error onto the model (live path)", () => {
  let model = testHydrate();
  model = applyNotification(
    model,
    {
      method: "turn/started",
      params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } },
    },
    1001,
  );
  model = applyNotification(
    model,
    {
      method: "item/completed",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        item: {
          type: "commandExecution",
          id: "item_tool",
          turnId: "turn_1",
          toolName: "ask_user",
          callId: "call_1",
          error: "denied: user rejected",
          status: "completed",
        },
      },
    },
    1002,
  );
  const item = itemAt(turnAt(model, 0), 0);
  expect(item.error).toBe("denied: user rejected");
  // Status is "completed" even for the errored call - error presence, not
  // status, is the honest failure signal.
  expect(item.status).toBe("completed");
});

test("hydrateThread maps a settled item's error onto the model (snapshot path)", () => {
  const thread = testThread({
    turns: [
      {
        id: "turn_1",
        status: "completed",
        itemsView: "full",
        items: [
          {
            type: "commandExecution",
            id: "item_tool",
            turnId: "turn_1",
            toolName: "run_tests",
            callId: "call_1",
            error: "exit status 1",
            status: "completed",
          },
        ],
      },
    ],
  });
  const model = hydrateThread({ thread }, thread.evener.ref, 1000);
  expect(itemAt(turnAt(model, 0), 0).error).toBe("exit status 1");
});

// A settled shell tool call now carries its process exit code as a typed wire
// field (ThreadItem.exitCode, wire-honesty spec Part A) — the model must carry
// it so a descriptor reads a structured number rather than parsing the output
// footer text.
test("item/completed maps the wire item's exitCode onto the model (live path)", () => {
  let model = testHydrate();
  model = applyNotification(
    model,
    {
      method: "turn/started",
      params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } },
    },
    1001,
  );
  model = applyNotification(
    model,
    {
      method: "item/completed",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        item: {
          type: "commandExecution",
          id: "item_tool",
          turnId: "turn_1",
          toolName: "shell",
          callId: "call_1",
          output: "boom",
          exitCode: 2,
          status: "completed",
        },
      },
    },
    1002,
  );
  expect(itemAt(turnAt(model, 0), 0).exitCode).toBe(2);
});

test("hydrateThread maps a settled item's exitCode onto the model (snapshot path)", () => {
  const thread = testThread({
    turns: [
      {
        id: "turn_1",
        status: "completed",
        itemsView: "full",
        items: [
          {
            type: "commandExecution",
            id: "item_tool",
            turnId: "turn_1",
            toolName: "shell",
            callId: "call_1",
            output: "ok",
            exitCode: 0,
            status: "completed",
          },
        ],
      },
    ],
  });
  const model = hydrateThread({ thread }, thread.evener.ref, 1000);
  // A real typed 0 must round-trip as 0, never collapse to undefined — the
  // descriptor distinguishes "ran, exit 0" from "no code (backgrounded)".
  expect(itemAt(turnAt(model, 0), 0).exitCode).toBe(0);
});

// A tool-call's intent crosses the wire as ThreadItem.description (set
// server-side, e.g. delegate's mandate); wireItemToModel historically dropped
// it. The model must carry it so the subagent Activity feed can render each
// child tool-call's intent (§4.2). Both hydrate and live paths fold through
// wireItemToModel, so the snapshot path proves the carry.
test("wireItemToModel carries the wire description (tool-call intent) onto the item", () => {
  const thread = testThread({
    turns: [
      {
        id: "turn_1",
        status: "completed",
        itemsView: "full",
        items: [
          {
            id: "item_tool_1_0",
            type: "commandExecution",
            toolName: "delegate",
            callId: "c1",
            description: "audit the reducer",
            status: "completed",
          },
        ],
      },
    ],
  });
  const model = hydrateThread({ thread }, thread.evener.ref, 1000);
  expect(itemAt(turnAt(model, 0), 0).description).toBe("audit the reducer");
});

// systemMessage items carry a stable typed discriminator, ThreadItem.eventKind
// (appwire.ThreadItemEventKind*), naming what happened — "system_prompt",
// "compaction", etc. wireItemToModel historically dropped it, forcing the
// transcript renderer to guess scaffold items from their char count. The model
// must carry it so classification is by wire type, not a heuristic (kata ckgw).
test("wireItemToModel carries the wire eventKind (scaffold/system discriminator) onto the item", () => {
  const thread = testThread({
    turns: [
      {
        id: "turn_system",
        status: "completed",
        itemsView: "full",
        items: [
          {
            id: "item_system_prompt",
            type: "systemMessage",
            text: "You are Evener.",
            eventKind: "system_prompt",
            status: "completed",
          },
        ],
      },
    ],
  });
  const model = hydrateThread({ thread }, thread.evener.ref, 1000);
  expect(itemAt(turnAt(model, 0), 0).eventKind).toBe("system_prompt");
});

// A system item can attach structured detail behind its prose text, e.g. a
// round_timings item's per-phase durations (ThreadItem.raw; kata 7zkv) or a
// compaction item's before/after counts. wireItemToModel historically dropped
// it, forcing a renderer to re-parse numbers out of human-readable text. The
// model must carry it so a renderer can read the real numbers instead.
test("wireItemToModel carries the wire raw (structured system-item detail) onto the item", () => {
  const thread = testThread({
    turns: [
      {
        id: "turn_timings",
        status: "completed",
        itemsView: "full",
        items: [
          {
            id: "item_round_timings_1",
            type: "systemMessage",
            text: "Round 0 total=1.5s llm=1.2s",
            eventKind: "round_timings",
            raw: { roundTimings: { round: 0, total_round_ns: 1_500_000_000, llm_call_ns: 1_200_000_000 } },
            status: "completed",
          },
        ],
      },
    ],
  });
  const model = hydrateThread({ thread }, thread.evener.ref, 1000);
  expect(itemAt(turnAt(model, 0), 0).raw).toEqual({
    roundTimings: { round: 0, total_round_ns: 1_500_000_000, llm_call_ns: 1_200_000_000 },
  });
});

// A reloaded transcript must label a steer the same way the live one did.
// internal/apptranscript persists ThreadItem.steeringKind alongside the
// steering item (Task 2); wireItemToModel must carry it through on the
// snapshot path exactly as it does for description/eventKind/raw above, or
// the label would work live and vanish on refresh.
test("a reloaded steering item carries steeringKind from the snapshot", () => {
  const thread = testThread({
    turns: [
      {
        id: "turn_0",
        status: "completed",
        itemsView: "full",
        items: [
          {
            id: "item_steering_0",
            type: "steering",
            text: "done",
            steeringKind: "tasks-done",
            status: "completed",
          },
        ],
      },
    ],
  });
  const model = hydrateThread({ thread }, thread.evener.ref, 1000);
  expect(itemAt(turnAt(model, 0), 0).steeringKind).toBe("tasks-done");
});

// On reload, apptranscript.TurnsFromFile mints one wire turn per transcript
// entry, so a tool CALL (assistant entry) and its RESULT (tool-results entry)
// arrive as two items sharing a callId, with different ids, in separate turns.
// The Go contract says "the client merges the two by call id"; the reducer must
// collapse them into the single item the live path already produces, and drop
// the now-empty result turn so its TurnSeparator disappears. (zrzr)
test("reload merges a tool CALL and its RESULT (separate turns, same callId) into one item", () => {
  const thread = testThread({
    turns: [
      {
        id: "turn_1",
        status: "completed",
        itemsView: "full",
        items: [
          {
            id: "item_tool_1_0",
            type: "commandExecution",
            toolName: "shell",
            callId: "call_A",
            argumentsJson: JSON.stringify({ command: "make test" }),
            startedAt: 1,
            status: "inProgress",
          },
        ],
      },
      {
        id: "turn_2",
        status: "completed",
        itemsView: "full",
        items: [
          {
            id: "item_tool_result_2_0",
            type: "commandExecution",
            toolName: "shell",
            callId: "call_A",
            output: "ok",
            exitCode: 0,
            completedAt: 2,
            status: "completed",
          },
        ],
      },
    ],
  });
  const model = hydrateThread({ thread }, thread.evener.ref, 0);

  // Exactly one tool item survives, carrying both halves.
  const items = model.turns.flatMap((t) => t.items).filter((i) => i.callId === "call_A");
  expect(items).toHaveLength(1);
  const merged = items[0];
  if (!merged) throw new Error("expected merged item");
  expect(merged.id).toBe("item_tool_1_0"); // keeps the CALL id
  expect(merged.argumentsJSON).toBe(JSON.stringify({ command: "make test" })); // from the CALL
  expect(merged.output).toBe("ok"); // from the RESULT
  expect(merged.exitCode).toBe(0); // from the RESULT
  expect(merged.status).toBe("completed"); // settled from the RESULT
  expect(merged.startedAt).toBeTruthy(); // carried from the CALL half
  expect(merged.completedAt).toBeTruthy(); // carried from the RESULT half

  // The now-empty result turn is gone, so only one turn (and one separator) remains.
  expect(model.turns).toHaveLength(1);
});

test("reload merges a tool RESULT's raw state into the CALL item (hydration preserves structured raw)", () => {
  const delegateRaw = { id: "dlg_42", type: "delegate", status: "running", task: "do work" };
  const thread = testThread({
    turns: [
      {
        id: "turn_1",
        status: "completed",
        itemsView: "full",
        items: [
          {
            id: "item_tool_1_0",
            type: "commandExecution",
            toolName: "job_status",
            callId: "call_B",
            argumentsJson: JSON.stringify({ target: "dlg_42" }),
            startedAt: 1,
            status: "inProgress",
          },
        ],
      },
      {
        id: "turn_2",
        status: "completed",
        itemsView: "full",
        items: [
          {
            id: "item_tool_result_2_0",
            type: "commandExecution",
            toolName: "job_status",
            callId: "call_B",
            output: JSON.stringify(delegateRaw),
            raw: delegateRaw,
            completedAt: 2,
            status: "completed",
          },
        ],
      },
    ],
  });
  const model = hydrateThread({ thread }, thread.evener.ref, 0);
  const items = model.turns.flatMap((t) => t.items).filter((i) => i.callId === "call_B");
  expect(items).toHaveLength(1);
  expect(items[0]?.raw).toEqual(delegateRaw); // raw from the RESULT survives the merge
});

test("thread/reasoning-effort/changed updates reasoningEffort", () => {
  let model = testHydrate();
  expect(model.reasoningEffort).toBeUndefined();
  model = applyNotification(
    model,
    {
      method: "thread/reasoning-effort/changed",
      params: { threadId: "thr_t", ref: "ref_t", reasoningEffort: "high" },
    },
    2000,
  );
  expect(model.reasoningEffort).toBe("high");
});

test("hydrateThread carries visionModel and defaults an absent wire value", () => {
  expect(testHydrate().visionModel).toBe("");
  expect(
    testHydrate({
      evener: {
        ref: "ref_t",
        capabilities: CAPABILITIES,
        queue: { revision: 0 },
        visionModel: "anthropic/claude-haiku-4-5",
      },
    }).visionModel,
  ).toBe("anthropic/claude-haiku-4-5");
});

test("thread/vision-model/changed updates visionModel", () => {
  let model = testHydrate();
  expect(model.visionModel).toBe("");
  model = applyNotification(
    model,
    {
      method: "thread/vision-model/changed",
      params: { threadId: "thr_t", ref: "ref_t", visionModel: "anthropic/claude-haiku-4-5" },
    },
    2000,
  );
  expect(model.visionModel).toBe("anthropic/claude-haiku-4-5");
});

// Wave 5 T1: thread/model/changed's real payload (appwire/types.go's
// ThreadModelChangedParams, lines 867-874) carries reasoningEffortLevels/
// supportsReasoning alongside modelProvider/model — "describe the NEW
// profile so a client's effort picker re-keys without a separate model/list
// round trip" (that struct's own doc comment), so a model switch replaces
// the picker's ladder wholesale rather than patching it.
test("thread/model/changed updates modelProvider/model and the new reasoning-effort profile (reasoningEffortLevels/supportsReasoning)", () => {
  let model = testHydrate();
  expect(model.modelProvider).toBe("anthropic/claude-sonnet-4-5");
  expect(model.reasoningEffortLevels).toEqual([]);
  expect(model.supportsReasoning).toBe(false);

  model = applyNotification(
    model,
    {
      method: "thread/model/changed",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        modelProvider: "anthropic",
        model: "claude-opus-4-1",
        reasoningEffortLevels: ["low", "medium", "high"],
        supportsReasoning: true,
      },
    },
    2000,
  );

  expect(model.modelProvider).toBe("anthropic");
  expect(model.model).toBe("claude-opus-4-1");
  expect(model.reasoningEffortLevels).toEqual(["low", "medium", "high"]);
  expect(model.supportsReasoning).toBe(true);
  expect(model.lastFrameAt).toBe(2000);
});

test("thread/model/changed resets reasoningEffortLevels/supportsReasoning to empty/false when the new payload omits them - it describes the NEW model completely, not a partial patch onto the old one", () => {
  let model = testHydrate({
    evener: {
      ref: "ref_t",
      capabilities: CAPABILITIES,
      queue: { revision: 0 },
      reasoningEffortLevels: ["low", "medium", "high"],
      supportsReasoning: true,
    },
  });

  model = applyNotification(
    model,
    {
      method: "thread/model/changed",
      params: { threadId: "thr_t", ref: "ref_t", modelProvider: "openai", model: "gpt-5.5" },
    },
    2000,
  );

  expect(model.reasoningEffortLevels).toEqual([]);
  expect(model.supportsReasoning).toBe(false);
});

// Observed reasoning timing: the wire never carries reasoning timestamps at
// all (neither the live projector nor the historical reader sets
// ThreadItem.StartedAt/CompletedAt for reasoning items), so the reducer
// stamps its own client-observed arrival times from `now` — see
// ItemModel.observedStartedAt/observedCompletedAt's doc comment in
// model.ts and the reducer's appendReasoningDelta/mergeObservedTiming/
// settleItem comments for the full rationale.

test("first reasoning delta stamps observedStartedAt; a later delta does not move it", () => {
  let model = testHydrate();
  model = applyNotification(
    model,
    {
      method: "turn/started",
      params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } },
    },
    1001,
  );
  model = applyNotification(
    model,
    {
      method: "item/started",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        item: { type: "reasoning", id: "item_r", turnId: "turn_1", status: "inProgress" },
      },
    },
    1002,
  );

  model = applyNotification(
    model,
    {
      method: "item/reasoning/summaryTextDelta",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        itemId: "item_r",
        summaryIndex: 0,
        delta: "thinking",
      },
    },
    1003,
  );
  expect(itemAt(turnAt(model, 0), 0).observedStartedAt).toBe(new Date(1003).toISOString());

  model = applyNotification(
    model,
    {
      method: "item/reasoning/summaryTextDelta",
      params: { threadId: "thr_t", ref: "ref_t", turnId: "turn_1", itemId: "item_r", summaryIndex: 0, delta: " more" },
    },
    1050,
  );
  expect(itemAt(turnAt(model, 0), 0).observedStartedAt).toBe(new Date(1003).toISOString()); // unchanged by the second delta
});

test("item/completed stamps observedCompletedAt when observation began; the wire's own (absent) timestamps stay absent", () => {
  let model = testHydrate();
  model = applyNotification(
    model,
    {
      method: "turn/started",
      params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } },
    },
    1001,
  );
  model = applyNotification(
    model,
    {
      method: "item/started",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        item: { type: "reasoning", id: "item_r", turnId: "turn_1", status: "inProgress" },
      },
    },
    1002,
  );
  model = applyNotification(
    model,
    {
      method: "item/reasoning/summaryTextDelta",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        itemId: "item_r",
        summaryIndex: 0,
        delta: "thinking",
      },
    },
    1003,
  );

  model = applyNotification(
    model,
    {
      method: "item/completed",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        item: { type: "reasoning", id: "item_r", turnId: "turn_1", status: "completed" },
      },
    },
    1010,
  );

  const item = itemAt(turnAt(model, 0), 0);
  expect(item.observedStartedAt).toBe(new Date(1003).toISOString());
  expect(item.observedCompletedAt).toBe(new Date(1010).toISOString());
  expect(item.startedAt).toBeUndefined();
  expect(item.completedAt).toBeUndefined();
});

test("a reasoning item still in-flight at a bare turn/completed settle gets observedCompletedAt from the settle (composition with R1's preserve path)", () => {
  let model = testHydrate();
  model = applyNotification(
    model,
    {
      method: "turn/started",
      params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } },
    },
    1001,
  );
  model = applyNotification(
    model,
    {
      method: "item/started",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        item: { type: "reasoning", id: "item_r", turnId: "turn_1", status: "inProgress" },
      },
    },
    1002,
  );
  model = applyNotification(
    model,
    {
      method: "item/reasoning/summaryTextDelta",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        itemId: "item_r",
        summaryIndex: 0,
        delta: "thinking",
      },
    },
    1003,
  );

  // Settle arrives mid-stream — no item/completed ever landed for item_r.
  model = applyNotification(
    model,
    {
      method: "turn/completed",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        turn: { id: "turn_1", status: "interrupted", itemsView: "" },
      },
    },
    1020,
  );

  const item = itemAt(turnAt(model, 0), 0);
  expect(item.observedStartedAt).toBe(new Date(1003).toISOString());
  expect(item.observedCompletedAt).toBe(new Date(1020).toISOString());
});

test("hydrated items never carry observed timing fields", () => {
  const thread = testThread({
    turns: [
      {
        id: "turn_1",
        status: "completed",
        itemsView: "full",
        items: [{ type: "reasoning", id: "item_r", turnId: "turn_1", text: "done thinking", status: "completed" }],
      },
    ],
  });
  const model = hydrateThread({ thread }, thread.evener.ref, 1000);

  const item = itemAt(turnAt(model, 0), 0);
  expect(item.observedStartedAt).toBeUndefined();
  expect(item.observedCompletedAt).toBeUndefined();
});

test("wire startedAt/completedAt, when present, coexist untouched alongside observed stamps", () => {
  let model = testHydrate();
  model = applyNotification(
    model,
    {
      method: "turn/started",
      params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } },
    },
    1001,
  );
  model = applyNotification(
    model,
    {
      method: "item/started",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        item: { type: "reasoning", id: "item_r", turnId: "turn_1", status: "inProgress" },
      },
    },
    1002,
  );
  model = applyNotification(
    model,
    {
      method: "item/reasoning/summaryTextDelta",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        itemId: "item_r",
        summaryIndex: 0,
        delta: "thinking",
      },
    },
    1003,
  );

  model = applyNotification(
    model,
    {
      method: "item/completed",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        item: {
          type: "reasoning",
          id: "item_r",
          turnId: "turn_1",
          status: "completed",
          startedAt: 5000,
          completedAt: 6000,
        },
      },
    },
    1004,
  );

  const item = itemAt(turnAt(model, 0), 0);
  expect(item.startedAt).toBe(new Date(5000).toISOString());
  expect(item.completedAt).toBe(new Date(6000).toISOString());
  expect(item.observedStartedAt).toBe(new Date(1003).toISOString());
  expect(item.observedCompletedAt).toBe(new Date(1004).toISOString());
});

// Warnings reach the model: the reducer's `case "warning"` (see reducer.ts
// for the wire receipts — internal/appprojector/appwire_projection.go's
// EventWarning and EventError's user-cancel branch) folds NotifyWarning
// notifications into the active turn as ordinary items, mirroring
// evener/steering/injected's own shape.

test("warning mid-turn appends an item to the active turn with text=message and the meta populated", () => {
  let model = testHydrate();
  model = applyNotification(
    model,
    {
      method: "turn/started",
      params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } },
    },
    1001,
  );

  model = applyNotification(
    model,
    {
      method: "warning",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        message: "rate limit approaching",
        source: "provider",
        title: "Provider warning",
        hint: "slow down",
      },
    },
    1002,
  );

  const items = turnAt(model, 0).items;
  expect(items).toHaveLength(1);
  expect(items[0]).toMatchObject({
    id: "item_warning_live_turn_1_0",
    turnId: "turn_1",
    type: "warning",
    text: "rate limit approaching",
    status: "completed",
    warning: { source: "provider", title: "Provider warning", hint: "slow down" },
  });
});

test("two warnings in one turn get distinct ids in arrival order", () => {
  let model = testHydrate();
  model = applyNotification(
    model,
    {
      method: "turn/started",
      params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } },
    },
    1001,
  );

  model = applyNotification(
    model,
    { method: "warning", params: { threadId: "thr_t", ref: "ref_t", message: "first" } },
    1002,
  );
  model = applyNotification(
    model,
    { method: "warning", params: { threadId: "thr_t", ref: "ref_t", message: "second" } },
    1003,
  );

  const items = turnAt(model, 0).items;
  expect(items.map((it) => it.id)).toEqual(["item_warning_live_turn_1_0", "item_warning_live_turn_1_1"]);
  expect(items.map((it) => it.text)).toEqual(["first", "second"]);
});

test("warning with no active turn only updates lastFrameAt (no turn fabricated client-side)", () => {
  const model = testHydrate();
  expect(model.activeTurnId).toBeUndefined();

  const result = applyNotification(
    model,
    { method: "warning", params: { threadId: "thr_t", ref: "ref_t", message: "orphaned warning" } },
    2000,
  );

  expect(result).toEqual({ ...model, lastFrameAt: 2000 });
  expect(result.turns).toBe(model.turns); // same reference: no turn was touched, none fabricated
});

test("a warning item survives a bare turn/completed settle stamp (composition with Part A)", () => {
  let model = testHydrate();
  model = applyNotification(
    model,
    {
      method: "turn/started",
      params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } },
    },
    1001,
  );
  model = applyNotification(
    model,
    { method: "warning", params: { threadId: "thr_t", ref: "ref_t", message: "mid-turn warning" } },
    1002,
  );

  model = applyNotification(
    model,
    {
      method: "turn/completed",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        turn: { id: "turn_1", status: "completed", itemsView: "" },
      },
    },
    1003,
  );

  const items = turnAt(model, 0).items;
  expect(items).toHaveLength(1);
  expect(items[0]).toMatchObject({ type: "warning", text: "mid-turn warning", status: "completed" });
});

test("a cancel-shaped warning (cause present) still lands, ignoring cause", () => {
  // EventError's user-cancel branch sends the same inline shape as
  // EventWarning plus `cause` (internal/appprojector/appwire_projection.go:
  // 520-535). cause has no model consumer — assert only that the item
  // lands with its meta; do not invent a field to carry it.
  let model = testHydrate();
  model = applyNotification(
    model,
    {
      method: "turn/started",
      params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } },
    },
    1001,
  );

  model = applyNotification(
    model,
    {
      method: "warning",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        message: "context canceled",
        source: "user",
        title: "Cancelled",
        hint: "",
        cause: { kind: "provider", provider: "anthropic" },
      },
    },
    1002,
  );

  const item = itemAt(turnAt(model, 0), 0);
  expect(item).toMatchObject({
    type: "warning",
    text: "context canceled",
    warning: { source: "user", title: "Cancelled", hint: "" },
  });
});

// Settled tool calls keep their arguments: the live projector's
// EventToolCallEnd (internal/appprojector/appwire_projection.go:414-442)
// resolves argsJSON at :424-427 but uses it only to derive Description —
// the settled ThreadItem it emits carries no ArgumentsJSON, even though the
// streamed item/started item (:373) had it. Historical items DO carry it
// (internal/apptranscript/apptranscript.go:284,312), so this is a
// live-settle-only loss the reducer corrects, mergeReasoning-style.

test("item/completed without argumentsJson keeps the item's original argumentsJSON alongside settled output/status", () => {
  let model = testHydrate();
  model = applyNotification(
    model,
    {
      method: "turn/started",
      params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } },
    },
    1001,
  );
  model = applyNotification(
    model,
    {
      method: "item/started",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        item: {
          type: "commandExecution",
          id: "item_tool",
          turnId: "turn_1",
          toolName: "bash",
          callId: "call_1",
          argumentsJson: '{"command":"ls"}',
          status: "inProgress",
        },
      },
    },
    1002,
  );

  model = applyNotification(
    model,
    {
      method: "item/completed",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        item: {
          type: "commandExecution",
          id: "item_tool",
          turnId: "turn_1",
          toolName: "bash",
          callId: "call_1",
          output: "file1\nfile2",
          status: "completed",
        },
      },
    },
    1003,
  );

  const item = itemAt(turnAt(model, 0), 0);
  expect(item.argumentsJSON).toBe('{"command":"ls"}');
  expect(item.output).toBe("file1\nfile2");
  expect(item.status).toBe("completed");
});

test("item/completed with its own argumentsJson replaces the old value (wire truth wins)", () => {
  let model = testHydrate();
  model = applyNotification(
    model,
    {
      method: "turn/started",
      params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } },
    },
    1001,
  );
  model = applyNotification(
    model,
    {
      method: "item/started",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        item: {
          type: "commandExecution",
          id: "item_tool",
          turnId: "turn_1",
          toolName: "bash",
          callId: "call_1",
          argumentsJson: '{"command":"ls"}',
          status: "inProgress",
        },
      },
    },
    1002,
  );

  model = applyNotification(
    model,
    {
      method: "item/completed",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        item: {
          type: "commandExecution",
          id: "item_tool",
          turnId: "turn_1",
          toolName: "bash",
          callId: "call_1",
          argumentsJson: '{"command":"ls -la"}',
          status: "completed",
        },
      },
    },
    1003,
  );

  const item = itemAt(turnAt(model, 0), 0);
  expect(item.argumentsJSON).toBe('{"command":"ls -la"}');
});

test("item/completed inserting a never-started item has no argumentsJSON (no crash, no fabrication)", () => {
  let model = testHydrate();
  model = applyNotification(
    model,
    {
      method: "turn/started",
      params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } },
    },
    1001,
  );

  model = applyNotification(
    model,
    {
      method: "item/completed",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        item: { type: "userMessage", id: "item_user", turnId: "turn_1", text: "hi", status: "completed" },
      },
    },
    1002,
  );

  const item = itemAt(turnAt(model, 0), 0);
  expect(item.argumentsJSON).toBeUndefined();
});

// pendingEscalations (M7): appwire/types.go's ThreadEvener.PendingEscalations
// doc comment calls it the "surface-on-entry snapshot ... so a client
// entering / reconnecting to / not-having-seen-live this session surfaces
// the card(s)" and rules it a HUMAN-CLIENT field only, never part of the
// transcript. hydrateThread must therefore carry it verbatim (or default it
// to [], per the Go wire-nullable-array rule: omitempty absent means empty)
// as a THREAD-level ThreadModel field, not a turn item.

test("hydrateThread maps evener.pendingEscalations verbatim into pendingEscalations", () => {
  const escalation = testEscalation();
  const model = testHydrate({
    evener: { ref: "ref_t", capabilities: CAPABILITIES, queue: { revision: 0 }, pendingEscalations: [escalation] },
  });
  expect(model.pendingEscalations).toEqual([escalation]);
});

test("hydrateThread defaults pendingEscalations to an empty array when evener.pendingEscalations is absent", () => {
  const model = testHydrate();
  expect(model.pendingEscalations).toEqual([]);
});

// EvenerThread.Cost is the session-level estimated dollar total (the sibling of
// per-turn Turn.Cost), snapshot-authoritative like usage/workMillis: only a
// wire snapshot (hydrateThread) sets it, and everything else preserves it via
// the reducer's ...model spread. It is null when the daemon omits it (no usage,
// or an uncataloged model) — an honest "unknown" the status row renders as no
// chip, never a misleading ~$0.00.
test("hydrateThread maps evener.cost into the model, null when absent", () => {
  const withCost = testHydrate({
    evener: { ref: "ref_t", capabilities: CAPABILITIES, queue: { revision: 0 }, cost: "~$1.23" },
  });
  expect(withCost.cost).toBe("~$1.23");

  expect(testHydrate().cost).toBeNull();
});

test("hydrateThread preserves task aggregate through notification mutation and reconnect rehydrate", () => {
  const snapshot = {
    ref: "ref_t",
    capabilities: CAPABILITIES,
    queue: { revision: 0 },
    tasks: { total: 7, done: 6, current: { id: 6, description: "hydrated current task" } },
  };
  let model = testHydrate({ evener: snapshot });
  expect(model.tasks).toEqual({ total: 7, done: 6, current: { id: 6, description: "hydrated current task" } });

  model = applyNotification(
    model,
    {
      method: "evener/task/updated",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        total: 7,
        done: 7,
        current: { id: 7, description: "replacement task" },
      },
    },
    2000,
  );
  expect(model.tasks).toEqual({
    total: 7,
    done: 7,
    current: { id: 7, description: "replacement task" },
  });

  model = applyNotification(
    model,
    { method: "evener/task/updated", params: { threadId: "thr_t", ref: "ref_t", total: 7, done: 7 } },
    3000,
  );
  expect(model.tasks).toEqual({ total: 7, done: 7 });

  model = applyNotification(
    model,
    {
      method: "evener/task/updated",
      params: { threadId: "thr_t", ref: "ref_t", total: 7, done: 1, cancelled: 5, remaining: 1 },
    },
    4000,
  );
  expect(model.tasks).toEqual({ total: 7, done: 1, cancelled: 5, remaining: 1 });

  model = applyNotification(
    model,
    { method: "evener/task/updated", params: { threadId: "thr_t", ref: "ref_t", total: 3, done: 0, cancelled: 3 } },
    5000,
  );
  expect(model.tasks).toEqual({ total: 3, done: 0, cancelled: 3, remaining: 0 });

  const rehydrated = testHydrate({
    evener: {
      ref: "ref_t",
      capabilities: CAPABILITIES,
      queue: { revision: 0 },
      tasks: { total: 7, done: 7, current: { id: 7, description: "replacement task" } },
    },
  });
  expect(rehydrated.tasks).toEqual({ total: 7, done: 7, current: { id: 7, description: "replacement task" } });
});

test("hydrateThread keeps absent task aggregate null and distinguishes an authoritative zero", () => {
  expect(testHydrate().tasks).toBeNull();
  expect(
    testHydrate({
      evener: { ref: "ref_t", capabilities: CAPABILITIES, queue: { revision: 0 }, tasks: { total: 0, done: 0 } },
    }).tasks,
  ).toEqual({ total: 0, done: 0 });
});

// The jobs panel's refetch trigger rides the job lifecycle notifications the
// client already receives, rather than a second stream at the same instants
// (kata j7y6). Both ends of the lifecycle bump it: a job that starts and a
// job that finishes both change what evener/jobs/list would return.
const jobParams = (ref: string, status: string) => ({
  threadId: ref === "ref_t" ? "thr_t" : "not_thr_t",
  ref,
  job: { jobId: "job_1", jobType: "shell", status, outputBytes: 0 },
});

// Structural equality against the whole prior model, not just the two fields
// that move: a job push carries no job LIST, so touching anything else here
// would be the reducer inventing state off a notification that never said so.
// The starting model carries a populated task aggregate and goal on purpose —
// a guard like this only has teeth over fields that hold a distinguishable
// value, so a clobber to a field left at its null default would slip past.
test("evener/job/started bumps jobsUpdatedAt and lastFrameAt, and changes nothing else", () => {
  const before = testHydrate({
    name: "Session J",
    evener: {
      ref: "ref_t",
      capabilities: CAPABILITIES,
      queue: { revision: 0 },
      tasks: { total: 3, done: 1 },
      goal: { status: "active", iterations: 2 },
    },
  });
  expect(before.jobsUpdatedAt).toBeNull();
  const after = applyNotification(
    before,
    { method: "evener/job/started", params: jobParams("ref_t", "running") },
    2000,
  );
  expect(after).toEqual({ ...before, jobsUpdatedAt: 2000, lastFrameAt: 2000 });
});

test("evener/job/finished bumps jobsUpdatedAt for the targeted thread", () => {
  let model = testHydrate();
  model = applyNotification(model, { method: "evener/job/finished", params: jobParams("ref_t", "completed") }, 3000);
  expect(model.jobsUpdatedAt).toBe(3000);
  expect(model.lastFrameAt).toBe(3000);
});

test("a job notification for another thread leaves the model untouched", () => {
  const model = testHydrate();
  expect(
    applyNotification(model, { method: "evener/job/started", params: jobParams("not_ref_t", "running") }, 2000),
  ).toBe(model);
});

// Wave 5 T1: ThreadModel gains capabilities/goal/context*/usage/workMillis/
// activeTurnStartedAt/reasoningEffortLevels/supportsReasoning, all hydrated
// from thread.evener (appwire/types.go's EvenerThread, lines 223-274). None of
// these except reasoningEffortLevels/supportsReasoning (via
// thread/model/changed, tested above) and reasoningEffort (via
// thread/reasoning-effort/changed, tested above) ever get a live push - see
// EvenerThread's own doc comment ("read on demand ... rather than pushed on
// every event") and appwire/protocol.go's Notifications catalog, which has
// no capabilities/goal/context/usage entry at all.
test("hydrateThread maps capabilities/goal/context*/usage/workMillis/activeTurnStartedAt/reasoningEffortLevels/supportsReasoning verbatim from thread.evener", () => {
  const model = testHydrate({
    evener: {
      ref: "ref_t",
      capabilities: CAPABILITIES,
      queue: { revision: 0 },
      goal: { status: "active", iterations: 2 },
      contextUsed: 12_000,
      contextWindow: 200_000,
      contextPressure: 0.06,
      usage: { inputTokens: 100, outputTokens: 50, cacheReadTokens: 10, totalTokens: 160 },
      workMillis: 45_000,
      activeTurnStartedAt: 1_000,
      reasoningEffortLevels: ["low", "medium", "high"],
      supportsReasoning: true,
    },
  });

  expect(model.capabilities).toEqual(CAPABILITIES);
  expect(model.goal).toEqual({ status: "active", iterations: 2 });
  expect(model.contextUsed).toBe(12_000);
  expect(model.contextWindow).toBe(200_000);
  expect(model.contextPressure).toBe(0.06);
  expect(model.usage).toEqual({ inputTokens: 100, outputTokens: 50, cacheReadTokens: 10, totalTokens: 160 });
  expect(model.workMillis).toBe(45_000);
  expect(model.activeTurnStartedAt).toBe(new Date(1_000).toISOString());
  expect(model.reasoningEffortLevels).toEqual(["low", "medium", "high"]);
  expect(model.supportsReasoning).toBe(true);
});

test("hydrateThread defaults the wave 5 snapshot-only fields when thread.evener omits them (old daemon / codex thread)", () => {
  const model = testHydrate(); // testThread()'s default evener carries none of these

  expect(model.goal).toBeNull();
  expect(model.contextUsed).toBe(0);
  expect(model.contextWindow).toBe(0);
  expect(model.contextPressure).toBe(0);
  expect(model.usage).toBeNull();
  expect(model.workMillis).toBe(0);
  expect(model.activeTurnStartedAt).toBeUndefined();
  expect(model.reasoningEffortLevels).toEqual([]);
  expect(model.supportsReasoning).toBe(false);
});

// Thread.createdAt/updatedAt are top-level wire fields in Unix SECONDS
// (hubcore.UnixSeconds for a past session, entry.StartedAt.Unix() for a live
// one), converted to the model's ISO-string convention like every other
// timestamp on ThreadModel. The session-details panel is what reads them.
test("hydrateThread maps the thread's created/updated wire seconds to ISO instants", () => {
  const model = testHydrate({ createdAt: 1_784_829_766, updatedAt: 1_784_872_877 });

  expect(model.createdAt).toBe(new Date(1_784_829_766_000).toISOString());
  expect(model.updatedAt).toBe(new Date(1_784_872_877_000).toISOString());
});

test("hydrateThread treats Go's zero created/updated stamps as absent rather than 1970", () => {
  const model = testHydrate({ createdAt: 0, updatedAt: 0 });

  expect(model.createdAt).toBeUndefined();
  expect(model.updatedAt).toBeUndefined();
});

test("capabilities/goal/context*/usage/workMillis/activeTurnStartedAt survive live notifications untouched - no wire push exists for any of them", () => {
  let model = testHydrate({
    evener: {
      ref: "ref_t",
      capabilities: CAPABILITIES,
      queue: { revision: 0 },
      goal: { status: "active", iterations: 1 },
      contextUsed: 500,
      contextWindow: 100_000,
      contextPressure: 0.005,
      usage: { inputTokens: 1, outputTokens: 1, totalTokens: 2 },
      workMillis: 10,
      activeTurnStartedAt: 500,
    },
  });
  const before = {
    goal: model.goal,
    contextUsed: model.contextUsed,
    contextWindow: model.contextWindow,
    contextPressure: model.contextPressure,
    usage: model.usage,
    workMillis: model.workMillis,
    activeTurnStartedAt: model.activeTurnStartedAt,
  };

  model = applyNotification(
    model,
    {
      method: "thread/status/changed",
      params: { threadId: "thr_t", ref: "ref_t", status: { type: "active" } },
    },
    2000,
  );

  expect(model.goal).toEqual(before.goal);
  expect(model.contextUsed).toBe(before.contextUsed);
  expect(model.contextWindow).toBe(before.contextWindow);
  expect(model.contextPressure).toBe(before.contextPressure);
  expect(model.usage).toEqual(before.usage);
  expect(model.workMillis).toBe(before.workMillis);
  expect(model.activeTurnStartedAt).toBe(before.activeTurnStartedAt);
});

// The work-clock anchor (activeTurnStartedAt) is the sole snapshot-only evener
// field with a rest-state exception to "survives untouched": it has no live
// push to refresh it, so a cold hydrate mid-turn (server/appwire_runtime.go:865
// sets evener.activeTurnId, agent stamps ActiveTurnStartedAt) leaves a live anchor
// that would keep clocking now-minus-anchor forever once the turn ends. The
// reducer clears it on the two transitions it already handles — thread/status/
// changed to any non-active status, and turn/completed — so the model never
// carries a live anchor while at rest. StatusRow.tsx:130 feeds it to
// totalWorkMillis unconditionally, so a stale anchor is a ticking idle clock.
test("thread/status/changed to a non-active status clears the live work-clock anchor", () => {
  // Wire shapes: hydrate evener.activeTurnStartedAt is epoch-ms (reducer.ts:266
  // epochMsToISO); ThreadStatusChangedParams is {threadId, ref?, status} with
  // status {type} (types.gen.ts:963-972, reducer.ts:574-577).
  let model = testHydrate({
    status: { type: "active" },
    evener: {
      ref: "ref_t",
      capabilities: CAPABILITIES,
      queue: { revision: 0 },
      activeTurnStartedAt: 1_700_000_000_000,
    },
  });
  expect(model.activeTurnStartedAt).toBe(new Date(1_700_000_000_000).toISOString());

  model = applyNotification(
    model,
    {
      method: "thread/status/changed",
      params: { threadId: "thr_t", ref: "ref_t", status: { type: "awaiting" } },
    },
    2000,
  );

  expect(model.status.type).toBe("awaiting");
  expect(model.activeTurnStartedAt).toBeUndefined();
});

test("turn/completed clears the live work-clock anchor — the active turn just ended", () => {
  // Wire shapes: evener.activeTurnId sets model.activeTurnId (reducer.ts:231-233,
  // server/appwire_runtime.go:865); TurnCompletedParams is the bare {turnId,
  // turn} settle stamp with itemsView "" (reducer.ts:396-412, 430-433 citing
  // the internal/appprojector live settle sites).
  let model = testHydrate({
    status: { type: "active" },
    evener: {
      ref: "ref_t",
      capabilities: CAPABILITIES,
      queue: { revision: 0 },
      activeTurnId: "turn_1",
      activeTurnStartedAt: 1_700_000_000_000,
    },
  });
  expect(model.activeTurnId).toBe("turn_1");
  expect(model.activeTurnStartedAt).toBe(new Date(1_700_000_000_000).toISOString());

  model = applyNotification(
    model,
    {
      method: "turn/completed",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        turn: { id: "turn_1", status: "completed", itemsView: "" },
      },
    },
    2000,
  );

  expect(model.activeTurnId).toBeUndefined();
  expect(model.activeTurnStartedAt).toBeUndefined();
});

test("thread/status/changed staying active preserves the live work-clock anchor", () => {
  // The clear fires only on the rest transition; an active→active status frame
  // (e.g. an activeFlags change) must not drop a legitimately running anchor.
  let model = testHydrate({
    status: { type: "active" },
    evener: {
      ref: "ref_t",
      capabilities: CAPABILITIES,
      queue: { revision: 0 },
      activeTurnStartedAt: 1_700_000_000_000,
    },
  });
  const anchor = model.activeTurnStartedAt;

  model = applyNotification(
    model,
    {
      method: "thread/status/changed",
      params: { threadId: "thr_t", ref: "ref_t", status: { type: "active" } },
    },
    2000,
  );

  expect(model.activeTurnStartedAt).toBe(anchor);
});

test("pendingEscalations survives a turn/started notification — thread-level state, untouched by turn machinery", () => {
  const escalation = testEscalation();
  let model = testHydrate({
    evener: { ref: "ref_t", capabilities: CAPABILITIES, queue: { revision: 0 }, pendingEscalations: [escalation] },
  });

  model = applyNotification(
    model,
    {
      method: "turn/started",
      params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } },
    },
    1001,
  );

  expect(model.pendingEscalations).toEqual([escalation]);
});

test("pendingEscalations survives a turn/completed bare-stamp settle — thread-level state, untouched by turn machinery", () => {
  const escalation = testEscalation();
  let model = testHydrate({
    evener: { ref: "ref_t", capabilities: CAPABILITIES, queue: { revision: 0 }, pendingEscalations: [escalation] },
  });
  model = applyNotification(
    model,
    {
      method: "turn/started",
      params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } },
    },
    1001,
  );

  model = applyNotification(
    model,
    {
      method: "turn/completed",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        turn: { id: "turn_1", status: "completed", itemsView: "" },
      },
    },
    1002,
  );

  expect(model.pendingEscalations).toEqual([escalation]);
});

test('"evener/sandbox/escalation/requested" appends a new card with full field mapping and stamps lastFrameAt', () => {
  let model = testHydrate();
  const escalation = testEscalation({ command: "rm -rf /tmp/x", outputSoFar: "partial output", partiallyRan: true });

  model = applyNotification(model, { method: "evener/sandbox/escalation/requested", params: escalation }, 2000);

  expect(model.pendingEscalations).toEqual([escalation]);
  expect(model.lastFrameAt).toBe(2000);
});

test('"evener/sandbox/escalation/requested" with an already-present escalationId replaces that entry IN PLACE, index-preserving — not a filter-then-append', () => {
  // Snapshot-then-subscribe overlap: hydration's pendingEscalations snapshot
  // and a live requested notification can race and both deliver the same
  // card (appwire/types.go's PendingEscalations doc comment). Last write
  // wins — replace in place, don't drop the update or duplicate the entry.
  //
  // A single seeded entry can't tell an index-preserving replace apart from
  // "filter the old one out, then append the update" - both produce the
  // same one-element array. Seeding TWO entries and updating the FIRST is
  // what actually distinguishes them: filter+append would put the update
  // LAST ([second, updatedFirst]); this asserts it stays first instead.
  const first = testEscalation({ escalationId: "esc_1", mode: "exempt_denied_path" });
  const second = testEscalation({ escalationId: "esc_2", mode: "exempt_command" });
  let model = testHydrate({
    evener: { ref: "ref_t", capabilities: CAPABILITIES, queue: { revision: 0 }, pendingEscalations: [first, second] },
  });

  const updatedFirst = testEscalation({ escalationId: "esc_1", mode: "exempt_path_prefix", partiallyRan: true });
  model = applyNotification(model, { method: "evener/sandbox/escalation/requested", params: updatedFirst }, 2000);

  expect(model.pendingEscalations).toEqual([updatedFirst, second]);
});

test('"evener/sandbox/escalation/requested" for a different thread is a same-reference no-op', () => {
  const model = testHydrate();
  const escalation = testEscalation({ ref: "some_other_ref", threadId: "thr_other" });

  const result = applyNotification(model, { method: "evener/sandbox/escalation/requested", params: escalation }, 2000);

  expect(result).toBe(model);
});

test('"evener/sandbox/escalation/resolved" clears the matching card by id and stamps lastFrameAt', () => {
  // Wire-honesty spec Part B: the daemon now broadcasts escalation/resolved to
  // every OTHER subscribed client when a pending escalation leaves the set
  // (resolved, turn-interrupted, or cleared by session close). A client still
  // showing that card drops it — reusing the exact by-id clear the local
  // resolve path already uses (resolvePendingEscalation).
  const escalation = testEscalation();
  let model = testHydrate({
    evener: { ref: "ref_t", capabilities: CAPABILITIES, queue: { revision: 0 }, pendingEscalations: [escalation] },
  });

  model = applyNotification(
    model,
    {
      method: "evener/sandbox/escalation/resolved",
      params: { threadId: "thr_t", ref: "ref_t", escalationId: escalation.escalationId },
    },
    2000,
  );

  expect(model.pendingEscalations).toEqual([]);
  expect(model.lastFrameAt).toBe(2000);
});

test('"evener/sandbox/escalation/resolved" for an id this client never held leaves the set intact but still stamps lastFrameAt', () => {
  // The resolved broadcast is a genuine live frame even when this client's own
  // pending set never carried that id (it hydrated after the raise, or the id
  // belongs to a sibling escalation) — stamp liveness like every other targeted
  // notification, and leave the surviving cards untouched.
  const escalation = testEscalation({ escalationId: "esc_1" });
  let model = testHydrate({
    evener: { ref: "ref_t", capabilities: CAPABILITIES, queue: { revision: 0 }, pendingEscalations: [escalation] },
  });

  model = applyNotification(
    model,
    {
      method: "evener/sandbox/escalation/resolved",
      params: { threadId: "thr_t", ref: "ref_t", escalationId: "esc_never_held" },
    },
    2000,
  );

  expect(model.pendingEscalations).toEqual([escalation]);
  expect(model.lastFrameAt).toBe(2000);
});

test('"evener/sandbox/escalation/resolved" for a different thread is a same-reference no-op', () => {
  const escalation = testEscalation();
  const model = testHydrate({
    evener: { ref: "ref_t", capabilities: CAPABILITIES, queue: { revision: 0 }, pendingEscalations: [escalation] },
  });

  const result = applyNotification(
    model,
    {
      method: "evener/sandbox/escalation/resolved",
      params: { threadId: "thr_other", ref: "some_other_ref", escalationId: escalation.escalationId },
    },
    2000,
  );

  expect(result).toBe(model);
});

test("resolvePendingEscalation removes the entry with a matching escalationId", () => {
  const escalation = testEscalation();
  const model = testHydrate({
    evener: { ref: "ref_t", capabilities: CAPABILITIES, queue: { revision: 0 }, pendingEscalations: [escalation] },
  });

  const result = resolvePendingEscalation(model, escalation.escalationId);

  expect(result.pendingEscalations).toEqual([]);
});

test("resolvePendingEscalation on an unknown escalationId is a same-reference no-op", () => {
  const escalation = testEscalation();
  const model = testHydrate({
    evener: { ref: "ref_t", capabilities: CAPABILITIES, queue: { revision: 0 }, pendingEscalations: [escalation] },
  });

  const result = resolvePendingEscalation(model, "esc_does_not_exist");

  expect(result).toBe(model);
});

test('turn/completed\'s "full" replace branch composes mergeArguments and mergeObservedTiming alongside mergeReasoning', () => {
  // Addendum (R2 review): the "full" branch (reducer.ts's turn/completed
  // case) only composed mergeReasoning; mergeArguments and
  // mergeObservedTiming (added by R2 for item/completed) were not, even
  // though the same live-settle-only gaps apply here. R2's reviewer
  // verified this cannot lose data in today's live traffic (the only live
  // "full" emitter mints a brand-new turn with a fresh systemMessage —
  // internal/appprojector/appwire_projection.go:962-972) — this closes the
  // same bug class Part C fixed, one code path over.
  let model = testHydrate();
  model = applyNotification(
    model,
    {
      method: "turn/started",
      params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } },
    },
    1001,
  );
  model = applyNotification(
    model,
    {
      method: "item/started",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        item: {
          type: "commandExecution",
          id: "item_tool",
          turnId: "turn_1",
          toolName: "bash",
          callId: "call_1",
          argumentsJson: '{"command":"ls"}',
          status: "inProgress",
        },
      },
    },
    1002,
  );
  model = applyNotification(
    model,
    {
      method: "item/started",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        item: {
          type: "commandExecution",
          id: "item_tool2",
          turnId: "turn_1",
          toolName: "bash",
          callId: "call_2",
          argumentsJson: '{"command":"stale"}',
          status: "inProgress",
        },
      },
    },
    1003,
  );
  model = applyNotification(
    model,
    {
      method: "item/started",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        item: { type: "reasoning", id: "item_r", turnId: "turn_1", status: "inProgress" },
      },
    },
    1004,
  );
  model = applyNotification(
    model,
    {
      method: "item/reasoning/summaryTextDelta",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        itemId: "item_r",
        summaryIndex: 0,
        delta: "thinking...",
      },
    },
    1005,
  );

  model = applyNotification(
    model,
    {
      method: "turn/completed",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        turn: {
          id: "turn_1",
          status: "completed",
          itemsView: "full",
          items: [
            {
              type: "commandExecution",
              id: "item_tool",
              turnId: "turn_1",
              toolName: "bash",
              callId: "call_1",
              output: "file1\nfile2",
              status: "completed",
            },
            {
              type: "commandExecution",
              id: "item_tool2",
              turnId: "turn_1",
              toolName: "bash",
              callId: "call_2",
              argumentsJson: '{"command":"pwd"}',
              status: "completed",
            },
            { type: "reasoning", id: "item_r", turnId: "turn_1", status: "completed" },
          ],
        },
      },
    },
    1006,
  );

  const items = turnAt(model, 0).items;
  const tool = items.find((it) => it.id === "item_tool")!;
  const tool2 = items.find((it) => it.id === "item_tool2")!;
  const reasoning = items.find((it) => it.id === "item_r")!;

  // mergeArguments: the settle payload omits argumentsJson for item_tool — the old value survives.
  expect(tool.argumentsJSON).toBe('{"command":"ls"}');
  // item_tool2's settle payload carries its OWN (different) argumentsJson — wire truth wins over memory.
  expect(tool2.argumentsJSON).toBe('{"command":"pwd"}');
  // mergeReasoning: accumulated chunks survive settlement (already covered elsewhere; reconfirmed here as part of the composition).
  expect(reasoning.reasoningSummaries).toEqual([["thinking..."]]);
  // mergeObservedTiming: observedStartedAt carries forward from the delta's stamp; observedCompletedAt gets
  // stamped from `now` since the turn settling is the honest end of observation.
  expect(reasoning.observedStartedAt).toBe(new Date(1005).toISOString());
  expect(reasoning.observedCompletedAt).toBe(new Date(1006).toISOString());
});

// The failure count is otherwise snapshot-only: hydrate sets it, and nothing
// refreshes it until the next thread/read. A client that attached while the
// session was clean would then keep showing nothing however many failures
// followed — the watcher the count was built for (kata 12rq). Every status
// transition is a turn boundary, so the figure rides along there.
test("thread/status/changed carries a fresher failure count onto the model", () => {
  let model = testHydrate({
    status: { type: "active" },
    evener: { ref: "ref_t", capabilities: CAPABILITIES, queue: { revision: 0 }, failedToolCalls: 0 },
  });
  expect(model.failedToolCalls).toBe(0);

  model = applyNotification(
    model,
    {
      method: "thread/status/changed",
      params: { threadId: "thr_t", ref: "ref_t", status: { type: "awaiting" }, failedToolCalls: 3 },
    },
    2000,
  );

  expect(model.failedToolCalls).toBe(3);
});

// Absent on a NOTIFICATION means "no update", not "nobody counted". Clearing
// the model's figure here would blank a count the hydrate legitimately gave it
// every time an old daemon changed status — turning a measured session back
// into an unmeasured one on the strip.
test("thread/status/changed without a failure count leaves the hydrated one alone", () => {
  let model = testHydrate({
    status: { type: "active" },
    evener: { ref: "ref_t", capabilities: CAPABILITIES, queue: { revision: 0 }, failedToolCalls: 4 },
  });
  expect(model.failedToolCalls).toBe(4);

  model = applyNotification(
    model,
    {
      method: "thread/status/changed",
      params: { threadId: "thr_t", ref: "ref_t", status: { type: "awaiting" } },
    },
    2000,
  );

  expect(model.failedToolCalls).toBe(4);
});

// A measured zero must still be able to arrive by push: a session whose only
// failure was rolled back, or simply a first status change on a clean run,
// reports 0 and the strip falls silent. Treating 0 as "nothing to say" here
// would make the count monotonic and unable to correct itself.
test("thread/status/changed can push a measured zero", () => {
  let model = testHydrate({
    status: { type: "active" },
    evener: { ref: "ref_t", capabilities: CAPABILITIES, queue: { revision: 0 }, failedToolCalls: 2 },
  });

  model = applyNotification(
    model,
    {
      method: "thread/status/changed",
      params: { threadId: "thr_t", ref: "ref_t", status: { type: "awaiting" }, failedToolCalls: 0 },
    },
    2000,
  );

  expect(model.failedToolCalls).toBe(0);
});

// A live watcher on a long turn sees nothing move on thread/status/changed
// until the turn ends, however many tool calls fail inside it — the same
// shape of harm kata 12rq fixed at session scale (kata 895d). item/completed
// is the finer-grained carrier: the server stamps it only on the item whose
// completion actually moved the count (server/appwire_runtime.go's
// stampFailureCountOnItemCompleted), so the client applies it exactly the
// same way it applies thread/status/changed's.
test("item/completed carries a fresher failure count onto the model (existing item)", () => {
  let model = testHydrate({
    status: { type: "active" },
    evener: { ref: "ref_t", capabilities: CAPABILITIES, queue: { revision: 0 }, failedToolCalls: 0 },
  });
  model = applyNotification(
    model,
    {
      method: "turn/started",
      params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } },
    },
    1001,
  );
  model = applyNotification(
    model,
    {
      method: "item/started",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        item: { type: "commandExecution", id: "item_tool", turnId: "turn_1", status: "inProgress" },
      },
    },
    1002,
  );
  expect(model.failedToolCalls).toBe(0);

  model = applyNotification(
    model,
    {
      method: "item/completed",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        item: { type: "commandExecution", id: "item_tool", turnId: "turn_1", status: "failed", exitCode: 1 },
        failedToolCalls: 1,
      },
    },
    1003,
  );

  expect(model.failedToolCalls).toBe(1);
});

test("item/completed inserting a new item can also carry a fresher failure count", () => {
  let model = testHydrate({
    status: { type: "active" },
    evener: { ref: "ref_t", capabilities: CAPABILITIES, queue: { revision: 0 }, failedToolCalls: 0 },
  });
  model = applyNotification(
    model,
    {
      method: "turn/started",
      params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } },
    },
    1001,
  );

  model = applyNotification(
    model,
    {
      method: "item/completed",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        item: { type: "userMessage", id: "item_user", turnId: "turn_1", text: "Hi there", status: "completed" },
        failedToolCalls: 2,
      },
    },
    1002,
  );

  expect(model.failedToolCalls).toBe(2);
});

// Absent means "no change since the last stamp", exactly like thread/status/
// changed — never "nobody counted". Most item/completed notifications in a
// clean stretch of a turn carry no failedToolCalls at all (the server only
// stamps the item that moves it), and the model must not blank its figure on
// every one of them.
test("item/completed without a failure count leaves the model's figure alone", () => {
  let model = testHydrate({
    status: { type: "active" },
    evener: { ref: "ref_t", capabilities: CAPABILITIES, queue: { revision: 0 }, failedToolCalls: 3 },
  });
  model = applyNotification(
    model,
    {
      method: "turn/started",
      params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } },
    },
    1001,
  );

  model = applyNotification(
    model,
    {
      method: "item/completed",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        item: { type: "userMessage", id: "item_user", turnId: "turn_1", text: "Hi there", status: "completed" },
      },
    },
    1002,
  );

  expect(model.failedToolCalls).toBe(3);
});

test("collectAuthoritativeMutationIds uses pending, queue, and transcript identities without text matching", () => {
  const response = {
    thread: testThread({
      turns: [
        {
          id: "turn_1",
          status: "completed",
          itemsView: "full",
          items: [
            {
              id: "item_a",
              turnId: "turn_1",
              type: "userMessage",
              text: "same text",
              clientMutationId: "mutation-transcript",
            },
            {
              id: "item_b",
              turnId: "turn_1",
              type: "userMessage",
              text: "same text",
              clientMutationId: "mutation-transcript",
            },
          ],
        },
      ],
      evener: {
        ref: "ref_t",
        capabilities: CAPABILITIES,
        queue: {
          revision: 3,
          clientMutationIds: ["mutation-queue", "mutation-transcript"],
        },
        pendingMutations: [
          {
            clientMutationId: "mutation-pending",
            method: "turn/steer",
            executionState: "accepted",
            projectionState: "pending",
          },
        ],
      },
    }),
  };

  expect(collectAuthoritativeMutationIds(response)).toEqual(
    new Set(["mutation-pending", "mutation-queue", "mutation-transcript"]),
  );
});

test("hydrateThread preserves clientMutationId on authoritative transcript items", () => {
  const model = hydrateThread(
    {
      thread: testThread({
        turns: [
          {
            id: "turn_1",
            status: "completed",
            itemsView: "full",
            items: [
              {
                id: "item_a",
                turnId: "turn_1",
                type: "userMessage",
                text: "hello",
                clientMutationId: "mutation-a",
              },
            ],
          },
        ],
      }),
    },
    "ref_t",
    1000,
  );

  expect(model.turns[0]?.items[0]).toMatchObject({ clientMutationId: "mutation-a" });
});

// kata 4zn8: a rate-limited model call must land as explainable liveness state
// WITHOUT restamping lastFrameAt. The model has genuinely produced nothing —
// restamping would reset the quiet/stall clock and make a four-hour rate limit
// render calmer than it does today, which is the opposite of the point.
test("evener/thread/modelRetry records retry state and leaves lastFrameAt alone", () => {
  let model = testHydrate();
  model = applyNotification(
    model,
    {
      method: "turn/started",
      params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } },
    },
    1001,
  );
  const frameAtBeforeRetry = model.lastFrameAt;

  model = applyNotification(
    model,
    {
      method: "evener/thread/modelRetry",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        attempt: 9,
        maxAttempts: 11,
        delayMs: 60000,
        errorClass: "rate_limit",
        statusCode: 429,
        message: "rate limit exceeded",
        model: "k3",
        groupElapsedMs: 840000,
        attemptCap: 11,
      },
    },
    999000,
  );

  expect(model.modelRetry).toEqual({
    attempt: 9,
    maxAttempts: 11,
    delayMs: 60000,
    errorClass: "rate_limit",
    statusCode: 429,
    turnId: "turn_1",
    model: "k3",
    groupElapsedMs: 840000,
    attemptCap: 11,
    receivedAt: 999000,
  });
  expect(model.lastFrameAt).toBe(frameAtBeforeRetry);
});

// Component 1 (docs/superpowers/specs/2026-08-07-provider-failure-feedback-
// design.md): the retry state answers "what is happening now, in this retry
// group" and must survive ordinary mid-grind progress (deltas, a
// systemMessage announcement completing), clearing only on an actual turn
// boundary or the completion of a real model-output item.
function retryNotification(turnId: string): AnyNotification {
  return {
    method: "evener/thread/modelRetry",
    params: {
      threadId: "thr_t",
      ref: "ref_t",
      turnId,
      attempt: 1,
      maxAttempts: 11,
      delayMs: 1000,
      errorClass: "rate_limit",
      statusCode: 429,
      groupElapsedMs: 500,
      attemptCap: 11,
    },
  } as AnyNotification;
}

function withActiveRetry(): ThreadModel {
  let model = testHydrate();
  model = applyNotification(
    model,
    {
      method: "turn/started",
      params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } },
    },
    1001,
  );
  model = applyNotification(model, retryNotification("turn_1"), 1002);
  expect(model.modelRetry).toBeDefined();
  return model;
}

test("modelRetry survives an assistant message delta", () => {
  let model = withActiveRetry();
  model = applyNotification(
    model,
    {
      method: "item/started",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        item: { type: "agentMessage", id: "item_1", turnId: "turn_1", status: "inProgress" },
      },
    },
    1003,
  );
  model = applyNotification(
    model,
    {
      method: "item/agentMessage/delta",
      params: { threadId: "thr_t", ref: "ref_t", turnId: "turn_1", itemId: "item_1", delta: "hello" },
    },
    1004,
  );
  expect(model.modelRetry).toBeDefined();
});

test("modelRetry survives a systemMessage item completion - it arrives mid-grind (e.g. a user steer 'are you stuck?')", () => {
  let model = withActiveRetry();
  model = applyNotification(
    model,
    {
      method: "item/completed",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        item: { type: "systemMessage", id: "item_sys_1", turnId: "turn_1", status: "completed", text: "note" },
      },
    },
    1003,
  );
  expect(model.modelRetry).toBeDefined();
});

test("modelRetry survives a userMessage item completion", () => {
  let model = withActiveRetry();
  model = applyNotification(
    model,
    {
      method: "item/completed",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        item: { type: "userMessage", id: "item_user_1", turnId: "turn_1", status: "completed", text: "hi" },
      },
    },
    1003,
  );
  expect(model.modelRetry).toBeDefined();
});

test("modelRetry clears once the model's own output item (agentMessage) completes", () => {
  let model = withActiveRetry();
  model = applyNotification(
    model,
    {
      method: "item/started",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        item: { type: "agentMessage", id: "item_1", turnId: "turn_1", status: "inProgress" },
      },
    },
    1003,
  );
  model = applyNotification(
    model,
    {
      method: "item/completed",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        item: { type: "agentMessage", id: "item_1", turnId: "turn_1", status: "completed", text: "done" },
      },
    },
    1004,
  );
  expect(model.modelRetry).toBeUndefined();
});

test("modelRetry clears when its turn completes", () => {
  let model = withActiveRetry();
  model = applyNotification(
    model,
    {
      method: "turn/completed",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        turn: { id: "turn_1", status: "failed", itemsView: "" },
      },
    },
    1003,
  );
  expect(model.modelRetry).toBeUndefined();
});

test("modelRetry clears when a new turn starts", () => {
  let model = withActiveRetry();
  model = applyNotification(
    model,
    {
      method: "turn/started",
      params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_2", status: "inProgress", itemsView: "" } },
    },
    1003,
  );
  expect(model.modelRetry).toBeUndefined();
});

// The daemon's no-active-turn announcement path
// (internal/appprojector/appwire_projection.go's systemAnnouncementItem)
// emits ONE turn/completed per announcement, each carrying a single item and
// all naming the SAME synthetic turn: appwire.SystemPreludeTurnID before the
// session's first real turn has started, a freshly minted "turn_N" gap id
// between two real turns (kata 9ekv). The payload is a map literal with no
// top-level "turnId" key at all, so the reducer's params.turn.id fallback is
// the only id on the frame — TurnCompletedParams declares turnId required,
// hence the cast for the wire-true shape.
function announcementFrame(turnId: string, item: ThreadItem): AnyNotification {
  return {
    method: "turn/completed",
    params: {
      threadId: "thr_t",
      ref: "ref_t",
      turn: { id: turnId, status: "completed", itemsView: "full", items: [item] },
    },
  } as AnyNotification;
}

const PLUGIN_LOADED_ITEM: ThreadItem = {
  type: "systemMessage",
  id: "item_plugin_loaded_1",
  turnId: SYSTEM_PRELUDE_TURN_ID,
  description: "Plugin loaded: superpowers",
  text: "",
  eventKind: "plugin_loaded",
  status: "completed",
};

const PROMPT_LOADED_ITEM: ThreadItem = {
  type: "systemMessage",
  id: "item_prompt_loaded_2",
  turnId: SYSTEM_PRELUDE_TURN_ID,
  description: "Prompt loaded",
  text: "Loaded prompt evener (2.1 kB)",
  eventKind: "prompt_loaded",
  status: "completed",
};

// The synthetic prelude turn is never the model's activeTurnId, so a
// live-connected tab used to drop the whole startup burst and only saw the
// "N system events" disclosure once a hydrate/re-subscribe took the snapshot
// path. The authoritative snapshot reduction is the spec here
// (server/appwire_turns.go's ensureTurn/upsertItem): the prelude is created
// at the FRONT — it is the one turn whose id fixes its position — and each
// announcement's item accumulates into it rather than replacing the last.
test("a live startup burst creates the prelude turn at the front and accumulates every announcement into it", () => {
  let model = testHydrate({
    evener: {
      ref: "ref_t",
      capabilities: CAPABILITIES,
      queue: { revision: 0 },
      activeTurnStartedAt: 900,
    },
  });
  // Nothing orders a session's first turn-starting request behind its startup
  // announcements, so turn_1 can (and does) start before the prelude's frames
  // land — the interleaving d2cc7ff8 fixed server-side.
  model = applyNotification(
    model,
    {
      method: "turn/started",
      params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } },
    },
    1001,
  );

  model = applyNotification(model, announcementFrame(SYSTEM_PRELUDE_TURN_ID, PLUGIN_LOADED_ITEM), 1002);
  model = applyNotification(model, announcementFrame(SYSTEM_PRELUDE_TURN_ID, PROMPT_LOADED_ITEM), 1003);

  expect(model.turns.map((t) => t.id)).toEqual([SYSTEM_PRELUDE_TURN_ID, "turn_1"]);
  expect(turnAt(model, 0).items.map((it) => it.id)).toEqual(["item_plugin_loaded_1", "item_prompt_loaded_2"]);
  expect(turnAt(model, 0).status).toBe("completed");
  expect(itemAt(turnAt(model, 0), 0).eventKind).toBe("plugin_loaded");
  expect(itemAt(turnAt(model, 0), 1).text).toBe("Loaded prompt evener (2.1 kB)");
  // The real turn above the prelude is still in flight: an announcement's
  // completion is not the active turn's, so it must not clear the active turn
  // or its work-clock anchor (the snapshot reduction clears its own active
  // turn only on an id match, for exactly this reason).
  expect(model.activeTurnId).toBe("turn_1");
  expect(model.activeTurnStartedAt).toBe(new Date(900).toISOString());
  expect(model.lastFrameAt).toBe(1003);
});

// Placement is the assertion, not just presence: a live burst must leave the
// same turn order and the same items a hydrate/re-subscribe would have handed
// this client, so the prelude group reads at the top either way.
test("the live startup burst leaves exactly the turns the snapshot path would have placed", () => {
  let live = testHydrate();
  live = applyNotification(
    live,
    {
      method: "turn/started",
      params: { threadId: "thr_t", ref: "ref_t", turn: { id: "turn_1", status: "inProgress", itemsView: "" } },
    },
    1001,
  );
  live = applyNotification(live, announcementFrame(SYSTEM_PRELUDE_TURN_ID, PLUGIN_LOADED_ITEM), 1002);
  live = applyNotification(live, announcementFrame(SYSTEM_PRELUDE_TURN_ID, PROMPT_LOADED_ITEM), 1003);

  // What server/appwire_turns.go reduces those same records to, and what
  // thread/read would therefore return.
  const snapshot = testHydrate({
    turns: [
      {
        id: SYSTEM_PRELUDE_TURN_ID,
        status: "completed",
        itemsView: "full",
        items: [PLUGIN_LOADED_ITEM, PROMPT_LOADED_ITEM],
      },
      { id: "turn_1", status: "inProgress", itemsView: "full", items: [] },
    ],
    evener: { ref: "ref_t", capabilities: CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
  });

  const shape = (m: ThreadModel) =>
    m.turns.map((t) => ({ id: t.id, status: t.status, items: t.items.map((it) => it.id) }));
  expect(shape(live)).toEqual(shape(snapshot));
  expect(live.activeTurnId).toBe(snapshot.activeTurnId);
});

// A between-turns gap shares the prelude's grouping rationale but not its
// position: it happened AFTER the real turn it follows, so the snapshot
// reduction APPENDS it (only the prelude id front-inserts). The live path
// must place it the same way.
test("a between-turns announcement turn appends after the real turn it follows", () => {
  let model = testHydrate({
    turns: [{ id: "turn_1", status: "completed", itemsView: "full", items: [] }],
  });
  expect(model.activeTurnId).toBeUndefined();

  model = applyNotification(
    model,
    announcementFrame("turn_2", {
      type: "systemMessage",
      id: "item_hook_completed_3",
      turnId: "turn_2",
      description: "Hook",
      text: "Hook Stop finished, exit 0",
      eventKind: "hook_completed",
      exitCode: 0,
      status: "completed",
    }),
    2000,
  );

  expect(model.turns.map((t) => t.id)).toEqual(["turn_1", "turn_2"]);
  expect(turnAt(model, 1).items.map((it) => it.id)).toEqual(["item_hook_completed_3"]);
  expect(itemAt(turnAt(model, 1), 0).exitCode).toBe(0);
});

// Accumulation is by ITEM ID, so a redelivered announcement frame — the
// reconnect/hydration replay path hands the reducer frames it may already
// have folded — updates its item in place instead of growing a second copy
// of it (server/appwire_turns.go's upsertItem merges by id for the same
// reason).
test("a redelivered announcement frame updates its item in place instead of duplicating it", () => {
  let model = testHydrate();
  model = applyNotification(model, announcementFrame(SYSTEM_PRELUDE_TURN_ID, PLUGIN_LOADED_ITEM), 1001);
  model = applyNotification(model, announcementFrame(SYSTEM_PRELUDE_TURN_ID, PLUGIN_LOADED_ITEM), 1002);

  expect(model.turns.map((t) => t.id)).toEqual([SYSTEM_PRELUDE_TURN_ID]);
  expect(turnAt(model, 0).items.map((it) => it.id)).toEqual(["item_plugin_loaded_1"]);
});

// The same-id-replaces-both hazard the active path guards against (see the
// "settles only the FIRST turn" test above) applies to the non-active path
// too: a duplicate id must not let one announcement's settle overwrite an
// unrelated turn's content, silently.
test("a non-active turn/completed settles only the FIRST turn matching a duplicated id, loudly", () => {
  const spy = vi.spyOn(console, "error").mockImplementation(() => {});
  const thread = testThread({
    turns: [
      {
        id: "turn_2",
        status: "completed",
        itemsView: "full",
        items: [
          {
            type: "systemMessage",
            id: "item_first",
            turnId: "turn_2",
            text: "first gap's notice",
            status: "completed",
          },
        ],
      },
      {
        id: "turn_2",
        status: "completed",
        itemsView: "full",
        items: [
          {
            type: "agentMessage",
            id: "item_second",
            turnId: "turn_2",
            text: "unrelated persisted content",
            status: "completed",
          },
        ],
      },
    ],
  });
  let model = hydrateThread({ thread }, thread.evener.ref, 1000);
  expect(model.activeTurnId).toBeUndefined();

  model = applyNotification(
    model,
    announcementFrame("turn_2", {
      type: "systemMessage",
      id: "item_hook_completed_9",
      turnId: "turn_2",
      description: "Hook",
      text: "Hook Stop finished, exit 0",
      eventKind: "hook_completed",
      status: "completed",
    }),
    2000,
  );

  expect(model.turns).toHaveLength(2);
  // Only the first match accumulated the announcement.
  expect(turnAt(model, 0).items.map((it) => it.id)).toEqual(["item_first", "item_hook_completed_9"]);
  // The second row's unrelated content survives untouched.
  expect(turnAt(model, 1).items.map((it) => it.id)).toEqual(["item_second"]);
  expect(itemAt(turnAt(model, 1), 0).text).toBe("unrelated persisted content");
  expect(spy).toHaveBeenCalledTimes(1);
  expect(spy.mock.calls[0]?.[0]).toMatch(/turn\/completed.*turn_2.*2 turns/i);
  spy.mockRestore();
});
