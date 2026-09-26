import { expect, test, vi } from "vitest";
// Vite's `?raw` import (declared ambiently by the "vite/client" lib already
// in tsconfig.json) loads each fixture's text at build time — this project
// has no @types/node, so node:fs is not an option here.
import basicTurnFixture from "./fixtures/basic-turn.jsonl?raw";
import queueAndStatusFixture from "./fixtures/queue-and-status.jsonl?raw";
import streamingWithResetFixture from "./fixtures/streaming-with-reset.jsonl?raw";
import toolAndJobsFixture from "./fixtures/tool-and-jobs.jsonl?raw";
import type { ItemModel, ThreadModel, TurnModel } from "./model";
import {
  applyNotification,
  collectAuthoritativeMutationIds,
  foldWarningParams,
  hydrateThread,
  imageSessionRouteForSession,
  markItemTextOmitted,
  mergeOlderItemPage,
  mergeTurnHistory,
  mergeTurnHistoryWithFolds,
  notificationTargetsThread,
  prependOlderTurns,
  RAW_WARNING_FRAME_MAX_CHARS,
  resolvePendingEscalation,
} from "./reducer";
import { itemAt, turnAt } from "./testing/modelAccessors";
import type {
  AnyNotification,
  InputItem,
  QueueState,
  SandboxEscalationRequested,
  Thread,
  ThreadCapabilities,
  ThreadItem,
  ThreadReadResponse,
  ThreadTurnsListResponse,
  Turn,
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
          `notification catalog (NOTIFICATION_NAMES in appwire-client/typescript/types.gen.ts). Either the notification was ` +
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
  sharedNotes: true,
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

// testHydrate's v6 twin: adds `snapshot`, so a fold that follows can be a
// live history/updated (item/completed's and turn/completed's read-model
// replacement) — testHydrate's own plain hydrate has no snapshot, so its
// model.history is never established and a live history/updated on it can
// only ever invalidate (reducer.ts's classifySignal), never merge. Kept
// separate from testHydrate itself rather than added there: most of this
// file's tests build a turn's FULL item set directly through the fixture
// (never folding a live history/updated at all), and mergeHistory orders
// items lacking an explicit `position` by inserting each new one ahead of
// same-position (i.e. still-undefined) siblings — switching every
// testHydrate call to v6 reorders any of those fixtures that hydrate 2+
// undated items in one turn, a regression this split avoids.
function testHydrateV6(overrides: TestThreadOverrides = {}): ThreadModel {
  const thread = testThread(overrides);
  return hydrateThread(
    { thread, bootGeneration: "", epoch: 1, snapshot: { incarnation: "inc-1", length: 1 } },
    thread.evener.ref,
    1000,
  );
}

function fragmentTurn(id: string, itemIds: string[], overrides: Partial<Turn> = {}): Turn {
  return {
    id,
    status: "completed",
    itemsView: "fragment",
    items: itemIds.map((itemId) => ({
      id: itemId,
      turnId: id,
      type: "agentMessage",
      text: `${id}-${itemId}`,
      status: "completed",
    })),
    ...overrides,
  };
}

function positionedFragmentTurn(
  id: string,
  items: Array<readonly [string, number]>,
  overrides: Partial<Turn> = {},
): Turn {
  const turn = fragmentTurn(
    id,
    items.map(([itemId]) => itemId),
    overrides,
  );
  return {
    ...turn,
    items: items.map(([itemId, entry], item) => ({
      id: itemId,
      turnId: id,
      type: "agentMessage",
      text: `${id}-${itemId}`,
      status: "completed",
      position: { entry, item },
    })),
  };
}

function toolWireTurn(turnId: string, itemId: string, callId: string, overrides: Partial<ThreadItem> = {}): Turn {
  return {
    id: turnId,
    status: "completed",
    itemsView: "full",
    items: [
      {
        id: itemId,
        turnId,
        type: "commandExecution",
        toolName: "shell",
        callId,
        ...overrides,
      },
    ],
  };
}

function toolModelTurn(turnId: string, item: Partial<ItemModel> & Pick<ItemModel, "id" | "callId">): TurnModel {
  const { id, callId, ...overrides } = item;
  return {
    id: turnId,
    status: "completed",
    items: [
      {
        ...overrides,
        id,
        turnId,
        type: overrides.type ?? "commandExecution",
        text: overrides.text ?? "",
        toolName: overrides.toolName ?? "shell",
        callId,
      },
    ],
  };
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
    evener: {
      diagnostics: {
        skills: [
          {
            name: "plugin:simplify",
            description: "rewrite",
            disableModelInvocation: false,
            userInvocable: true,
            available: true,
          },
        ],
      },
    },
  });
  expect(model.skills).toEqual([
    {
      name: "plugin:simplify",
      description: "rewrite",
      disableModelInvocation: false,
      userInvocable: true,
      available: true,
    },
  ]);
});

test("hydrateThread defaults missing skills and copies wire descriptors", () => {
  expect(testHydrate().skills).toEqual([]);

  const skills = [
    {
      name: "plugin:simplify",
      description: "rewrite",
      disableModelInvocation: false,
      userInvocable: true,
      available: true,
    },
  ];
  const model = testHydrate({ evener: { diagnostics: { skills } } });
  expect(model.skills).not.toBe(skills);
  expect(model.skills?.[0]).not.toBe(skills[0]);
});

test("applyNotification preserves skills while applying a status update", () => {
  const model = testHydrate({
    evener: {
      diagnostics: {
        skills: [
          {
            name: "plugin:simplify",
            description: "rewrite",
            disableModelInvocation: false,
            userInvocable: true,
            available: true,
          },
        ],
      },
    },
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
  expect(next.skills).toEqual([
    {
      name: "plugin:simplify",
      description: "rewrite",
      disableModelInvocation: false,
      userInvocable: true,
      available: true,
    },
  ]);
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
      // These fixtures (basic-turn, streaming-with-reset, tool-and-jobs) were
      // captured on the old lifecycle-notification wire and still carry
      // turn/completed, item/started and the rest — none of which reach the
      // reducer's switch any more (its default case is a no-op), so this
      // replay no longer exercises real reduction for them. Left in place,
      // untouched, rather than deleted or hand-rewritten to a protocol they
      // were never captured on: flagged in the porting task's report as a
      // fixture-refresh follow-up, not a mechanical rename this pass can make
      // honestly.
      const notification: AnyNotification =
        (n.method as string) === "turn/completed"
          ? ({
              ...n,
              params: {
                ...(n.params as object),
                threadId: header.hydrate.thread.id,
                ref: header.ref,
              },
            } as AnyNotification)
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
  const stale = probeFixture(
    { method: "history/updated", params: {} },
    { method: "item/agentMessage/chunk", params: {} },
  );
  expect(() => parseFixture("tool-and-jobs", stale)).toThrow(/fixture tool-and-jobs line 3/);
});

test("every name in the generated catalog is accepted by the fixture reader", () => {
  const everyName = probeFixture(...NOTIFICATION_NAMES.map((method) => ({ method, params: {} })));
  const parsed = parseFixture("probe", everyName);
  expect(parsed.notifications.map((n) => n.method)).toEqual([...NOTIFICATION_NAMES]);
});

test("a fixture whose records lack a method, or whose first line is not a header, is rejected", () => {
  expect(() => parseFixture("probe", probeFixture({ params: {} }))).toThrow(/line 2: record has no string "method"/);
  expect(() => parseFixture("probe", JSON.stringify({ method: "history/updated", params: {} }))).toThrow(
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

test("item/completed inserts an item that had no preceding item/started", () => {
  // userMessage and systemMessage items go straight to item/completed with
  // no item/started (internal/appprojector/appwire_projection.go: a new user
  // turn emits turn/started with an empty turn, then item/completed for the
  // userMessage — item/started is never sent for it).
  let model = testHydrateV6();
  model = applyNotification(
    model,
    {
      method: "history/updated",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        bootGeneration: "",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        turns: [{ id: "turn_1", status: "inProgress", itemsView: "" }],
      },
    },
    1001,
  );
  expect(turnAt(model, 0).items).toHaveLength(0);

  model = applyNotification(
    model,
    {
      method: "history/updated",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        bootGeneration: "",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        items: [
          {
            ...{ type: "userMessage", id: "item_user", turnId: "turn_1", text: "Hi there", status: "completed" },
            turnId: "turn_1",
          },
        ],
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
  let model = testHydrateV6();
  model = applyNotification(
    model,
    {
      method: "history/updated",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        bootGeneration: "",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        turns: [{ id: "turn_2", status: "inProgress", itemsView: "" }],
      },
    },
    1001,
  );
  model = applyNotification(
    model,
    {
      method: "history/updated",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        bootGeneration: "",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        items: [
          {
            ...{
              type: "userMessage",
              id: "item_user",
              turnId: "turn_2",
              transcriptEntryIndex: 5,
              text: "second task",
              status: "completed",
            },
            turnId: "turn_2",
          },
        ],
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
  let model = testHydrateV6();
  model = applyNotification(
    model,
    {
      method: "history/updated",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        bootGeneration: "",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        turns: [{ id: "turn_1", status: "inProgress", itemsView: "" }],
      },
    },
    1001,
  );
  const beforeCompletion = model;
  const turnCompleted: AnyNotification = {
    method: "history/updated",
    params: {
      threadId: "thr_t",
      ref: "ref_t",
      bootGeneration: "",
      epoch: 1,
      snapshot: { incarnation: "inc-1", length: 1 },
      turns: [{ id: "turn_1", status: "completed", itemsView: "" }],
      items: [],
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
  let modelA = hydrateThread(
    { thread: threadA, epoch: 1, snapshot: { incarnation: "inc-1", length: 1 } },
    threadA.evener.ref,
    1000,
  );
  modelA = applyNotification(
    modelA,
    {
      method: "history/updated",
      params: {
        threadId: "thr_a",
        ref: "ref_a",
        bootGeneration: "",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        turns: [{ id: "turn_1", status: "inProgress", itemsView: "" }],
      },
    },
    1001,
  );
  expect(turnAt(modelA, 0).id).toBe("turn_1");

  // Stream A's own item BEFORE settling — wire-true: the real turn/completed
  // never carries items (see the "turn/completed preserves..." test below),
  // so A's item must already be in the model via item/completed, not
  // smuggled in through the settle payload.
  modelA = applyNotification(
    modelA,
    {
      method: "history/updated",
      params: {
        threadId: "thr_a",
        ref: "ref_a",
        bootGeneration: "",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        items: [
          {
            ...{ type: "agentMessage", id: "item_a1", turnId: "turn_1", text: "A's answer", status: "completed" },
            turnId: "turn_1",
          },
        ],
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
  const modelB = hydrateThread(
    { thread: threadB, epoch: 1, snapshot: { incarnation: "inc-1", length: 1 } },
    threadB.evener.ref,
    1000,
  );

  // The real wire's turn/completed is a bare stamp (see
  // internal/appprojector/appwire_projection.go: EventUserInput,
  // EventGoalContinuation, EventError, EventSessionEnd all emit
  // Turn{ID,Status[,Error]} with Items nil, ItemsView "") — no items key.
  const aTurnCompleted: AnyNotification = {
    method: "history/updated",
    params: {
      threadId: "thr_a",
      ref: "ref_a",
      bootGeneration: "",
      epoch: 1,
      snapshot: { incarnation: "inc-1", length: 1 },
      turns: [{ id: "turn_1", status: "completed", itemsView: "" }],
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

// Part A regression coverage: the real wire's turn/completed is a bare
// status/timing stamp with no items (see the case's own comment in
// reducer.ts for the full projector-site citation list). A settled turn
// must KEEP whatever items the model already accumulated via
// item/started + deltas + item/completed, not wipe them.

test("item/completed's own reasoning text differs from the seeded chunks and replaces them", () => {
  // The row's chunks were seeded from the item's own text (item/started here;
  // hydrate's wireItemToModel does the same). Item/completed's explicit text
  // is authoritative for a reasoning row exactly as it is for assistant text
  // (mergeCompletedText): the settle carries the complete flattened reasoning,
  // so it corrects the row instead of leaving the stale seed on screen.
  let model = testHydrateV6();
  model = applyNotification(
    model,
    {
      method: "history/updated",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        bootGeneration: "",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        turns: [{ id: "turn_1", status: "inProgress", itemsView: "" }],
      },
    },
    1001,
  );
  model = applyNotification(
    model,
    {
      method: "history/updated",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        bootGeneration: "",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        items: [
          {
            ...{ type: "reasoning", id: "item_r", turnId: "turn_1", text: "stale partial seed", status: "inProgress" },
            turnId: "turn_1",
          },
        ],
      },
    },
    1002,
  );
  expect(itemAt(turnAt(model, 0), 0).reasoningSummaries).toEqual([["stale partial seed"]]);

  model = applyNotification(
    model,
    {
      method: "history/updated",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        bootGeneration: "",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        items: [
          {
            ...{ type: "reasoning", id: "item_r", turnId: "turn_1", text: "settled reasoning", status: "completed" },
            turnId: "turn_1",
          },
        ],
      },
    },
    1003,
  );

  const settled = itemAt(turnAt(model, 0), 0);
  expect(settled.text).toBe("settled reasoning");
  expect(settled.reasoningSummaries).toEqual([["settled reasoning"]]);
});

// The "full" branch maps its own settled items through the same per-item helper
// chain item/completed uses (see its own comment), so it owes the same image
// rule: "full" replaces the item SET, not every field — a payload that carries no
// images list for an item must not erase the ones that item already had (#1656).

// Both directions of the same selector on this path too: the images list the
// payload does carry replaces the item's own, while the one it does not carry is
// kept. A test that only ever omitted the field would stay green if the selector
// were reversed, and a stale attachment would silently win.

// Part B regression coverage: evener/steering/injected's payload is declared
// `nil` in the AppWire catalog, but the live projector
// (internal/appprojector/appwire_projection.go:573-593) actually sends
// {threadId, ref, text, images, source?} — source present ("user") only for
// human-sent steers, omitted for daemon-originated ones. A live steer into
// an in-flight turn must become a "steering" transcript item, mirroring how
// reload already renders persisted steering turns
// (internal/apptranscript/apptranscript.go:211-229).

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
  let model = testHydrateV6();
  model = applyNotification(
    model,
    {
      method: "history/updated",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        bootGeneration: "",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        turns: [{ id: "turn_1", status: "inProgress", itemsView: "" }],
      },
    },
    1001,
  );
  model = applyNotification(
    model,
    {
      method: "history/updated",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        bootGeneration: "",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        items: [
          {
            ...{
              type: "userMessage",
              id: "item_user",
              turnId: "turn_1",
              text: "[image 1]what is this?",
              status: "completed",
              images: [{ type: "image", mediaType: "image/png", data: "iVBORw0KGgo=", name: "tiny.png" }],
            },
            turnId: "turn_1",
          },
        ],
      },
    },
    1002,
  );

  const item = itemAt(turnAt(model, 0), 0);
  expect(item.images).toEqual([{ src: "data:image/png;base64,iVBORw0KGgo=", name: "tiny.png" }]);
});

test("item/completed preserves an input image's name alongside a src resolved from a different field", () => {
  let model = testHydrateV6();
  model = applyNotification(
    model,
    {
      method: "history/updated",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        bootGeneration: "",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        turns: [{ id: "turn_1", status: "inProgress", itemsView: "" }],
      },
    },
    1001,
  );
  model = applyNotification(
    model,
    {
      method: "history/updated",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        bootGeneration: "",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        items: [
          {
            ...{
              type: "userMessage",
              id: "item_user",
              turnId: "turn_1",
              text: "look at this",
              status: "completed",
              images: [{ type: "image", path: "uploads/photo.jpg", name: "photo.jpg" }],
            },
            turnId: "turn_1",
          },
        ],
      },
    },
    1002,
  );

  const item = itemAt(turnAt(model, 0), 0);
  // path (not name) wins the src fallback here — name survives regardless.
  expect(item.images).toEqual([{ src: "uploads/photo.jpg", name: "photo.jpg", path: "uploads/photo.jpg" }]);
});

test("item/completed preserves an output image's name/path/source alongside a src resolved from yet another field", () => {
  let model = testHydrateV6();
  model = applyNotification(
    model,
    {
      method: "history/updated",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        bootGeneration: "",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        turns: [{ id: "turn_1", status: "inProgress", itemsView: "" }],
      },
    },
    1001,
  );
  model = applyNotification(
    model,
    {
      method: "history/updated",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        bootGeneration: "",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        items: [
          {
            ...{
              type: "commandExecution",
              id: "item_tool",
              turnId: "turn_1",
              toolName: "shell",
              callId: "call_1",
              status: "completed",
              outputImages: [{ source: "written-file", name: "plot.png", path: "out/plot.png" }],
            },
            turnId: "turn_1",
          },
        ],
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
  let model = testHydrateV6();
  model = applyNotification(
    model,
    {
      method: "history/updated",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        bootGeneration: "",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        turns: [{ id: "turn_1", status: "inProgress", itemsView: "" }],
      },
    },
    1001,
  );
  model = applyNotification(
    model,
    {
      method: "history/updated",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        bootGeneration: "",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        items: [
          {
            ...{
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
            turnId: "turn_1",
          },
        ],
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

const SHA_IMAGE = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855";

function hydrateWithShaImage(imageOverrides: Partial<InputItem> = {}): ThreadModel {
  // The default fixture's wire session ("sess_t") differs from its ref
  // ("ref_t") on purpose: the sha route must be built from the serving
  // session, never the ref — a stable workspace alias (e.g. local:stable)
  // names no Past.Find entry and its /s/{ref}/images/{sha} 404s.
  const turns: Turn[] = [
    {
      id: "turn_1",
      status: "completed",
      itemsView: "full",
      items: [
        {
          type: "userMessage",
          id: "item_user",
          turnId: "turn_1",
          text: "look at this",
          status: "completed",
          images: [{ type: "image", name: "photo.png", metadata: { sha: SHA_IMAGE }, ...imageOverrides }],
        },
      ],
    },
  ];
  return testHydrate({ turns });
}

test("hydrateThread resolves a sha-bearing image without a stamped url to the serving session's image route", () => {
  const model = hydrateWithShaImage();
  expect(model.imageSessionId).toBe("sess_t");
  expect(itemAt(turnAt(model, 0), 0).images).toEqual([
    { src: `/s/sess_t/images/${SHA_IMAGE}`, name: "photo.png", path: undefined },
  ]);
});

test("a stamped url wins over the rebuilt sha route", () => {
  const stamped = `/s/sess_t/images/${SHA_IMAGE}`;
  const model = hydrateWithShaImage({ url: stamped });
  expect(itemAt(turnAt(model, 0), 0).images).toEqual([{ src: stamped, name: "photo.png", path: undefined }]);
});

test("inline bytes win over the rebuilt sha route (bytes render when the Past index cannot serve the route)", () => {
  // A sha+bytes payload (legacy/live frames the hub never stripped) resolves
  // to the bytes: the route 404s for any session absent from the hub's Past
  // index (handleSessionImage, image_serve.go), while the bytes render
  // unconditionally. Sha-only replay descriptors still resolve to the route
  // (the test above).
  const model = hydrateWithShaImage({
    mediaType: "image/png",
    data: "iVBORw0KGgo=",
  });
  expect(itemAt(turnAt(model, 0), 0).images).toEqual([
    { src: "data:image/png;base64,iVBORw0KGgo=", name: "photo.png", path: undefined },
  ]);
});

test("a non-hex metadata sha falls back to the inline data-URI, never a hub-400 route", () => {
  const model = hydrateWithShaImage({
    metadata: { sha: "not-a-sha" },
    mediaType: "image/png",
    data: "iVBORw0KGgo=",
  });
  expect(itemAt(turnAt(model, 0), 0).images).toEqual([
    { src: "data:image/png;base64,iVBORw0KGgo=", name: "photo.png", path: undefined },
  ]);
});

test("a sha+bytes image with no known session keeps the data-URI (unknown-session fallback)", () => {
  // Blank wire ids name no fetchable route (imageSessionRoute undefined keeps
  // the branch dark), so the same sha+bytes payload that resolves to a route
  // above resolves to the usable bytes here instead of a broken src.
  const model = hydrateWithShaImage({});
  const wireModel = { ...model, imageSessionId: "", threadId: "" };
  const page: ThreadTurnsListResponse = {
    data: [
      {
        id: "turn_0",
        status: "completed",
        itemsView: "full",
        items: [
          {
            type: "userMessage",
            id: "item_paged",
            turnId: "turn_0",
            text: "older",
            status: "completed",
            images: [
              {
                type: "image",
                name: "photo.png",
                mediaType: "image/png",
                data: "iVBORw0KGgo=",
                metadata: { sha: SHA_IMAGE },
              },
            ],
          },
        ],
      },
    ],
    nextCursor: undefined,
  };
  const paged = mergeOlderItemPage(wireModel, page);
  expect(itemAt(turnAt(paged, 0), 0).images).toEqual([
    { src: "data:image/png;base64,iVBORw0KGgo=", name: "photo.png", path: undefined },
  ]);
});

test("a live item/completed with sha but no stamped url resolves to the hydrated session's route", () => {
  let model = testHydrateV6();
  model = applyNotification(
    model,
    {
      method: "history/updated",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        bootGeneration: "",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        turns: [{ id: "turn_1", status: "inProgress", itemsView: "" }],
      },
    },
    1001,
  );
  model = applyNotification(
    model,
    {
      method: "history/updated",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        bootGeneration: "",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        items: [
          {
            ...{
              type: "userMessage",
              id: "item_user",
              turnId: "turn_1",
              text: "look at this",
              status: "completed",
              images: [{ type: "image", name: "photo.png", metadata: { sha: SHA_IMAGE } }],
            },
            turnId: "turn_1",
          },
        ],
      },
    },
    1002,
  );

  expect(itemAt(turnAt(model, 0), 0).images).toEqual([
    { src: `/s/sess_t/images/${SHA_IMAGE}`, name: "photo.png", path: undefined },
  ]);
});

test("imageSessionRouteForSession escapes the session id and rejects what cannot serve", () => {
  expect(imageSessionRouteForSession("sess_t")).toBe("sess_t");
  expect(imageSessionRouteForSession("")).toBeUndefined();
  expect(imageSessionRouteForSession("proj/one")).toBeUndefined();
});

test("hydrateThread trims a whitespace-padded sessionId and falls back to the trimmed thread id", () => {
  // stampThreadImageURLs (output_images.go) trims both (strings.TrimSpace):
  // a padded-but-blank "  " session id must fall back to the thread id, and
  // a clean thread id must survive — neither may escape to a /s/%20... route.
  const blankPadded = testHydrate({ id: "thr_t", sessionId: "   " });
  expect(blankPadded.imageSessionId).toBe("thr_t");
  const padded = testHydrate({ id: "  thr_t  ", sessionId: "  sess_t  " });
  expect(padded.imageSessionId).toBe("sess_t");
});

// Task 1-3 carried a typed kind (events.SteeringKind* on the Go side) onto
// the wire at each injection site, through to EvenerSteeringInjectedParams.kind
// on the live notification. The model must carry it the last hop onto the
// item so the transcript can label a steer from the wire kind instead of
// pattern-matching its prose (a later task's job — this one only carries the
// string).

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

// A hydrate reads an empty input-images list the same way a live frame does: as
// absence. The wire rarely sends one — `appwire.ThreadItem.Images` is
// `json:",omitempty"` and the producers send nil — but a real fixture does
// (`fixtures/tool-and-jobs.jsonl:4`), and folding it to absent is what keeps an
// older page's images from being erased on merge.
test("an empty input-images list on the wire leaves the item's images unset", () => {
  const thread = testThread({
    turns: [
      {
        id: "turn_1",
        status: "completed",
        itemsView: "default",
        items: [
          {
            id: "user-item",
            turnId: "turn_1",
            type: "userMessage",
            text: "look",
            status: "completed",
            images: [],
          },
        ],
      },
    ],
  });
  const model = hydrateThread({ thread }, thread.evener.ref, 1000);
  expect(itemAt(turnAt(model, 0), 0).images).toBeUndefined();
});

// The same rule on the turn's own settle path: a turn/completed carrying
// itemsView "full" restates the turn's items, and an item in that payload that
// says nothing about images keeps the ones the model already holds — the merge
// chain there is the same composition item/completed uses, so it must carry the
// same fields.
test("a full turn settle that omits image fields keeps the item's images", () => {
  const thread = testThread({
    turns: [
      {
        id: "turn_1",
        status: "inProgress",
        itemsView: "default",
        items: [
          {
            id: "user-item",
            turnId: "turn_1",
            type: "userMessage",
            text: "look",
            status: "inProgress",
            images: [{ type: "image", mediaType: "image/png", data: "iVBORw0KGgo=", name: "shot.png" }],
          },
          {
            id: "tool-item",
            turnId: "turn_1",
            type: "commandExecution",
            toolName: "shell",
            callId: "call-1",
            text: "",
            status: "inProgress",
            outputImages: [{ source: "written-file", name: "plot.png", path: "out/plot.png" }],
          },
        ],
      },
    ],
  });
  let model = hydrateThread({ thread }, thread.evener.ref, 1000);
  expect(itemAt(turnAt(model, 0), 0).images).toHaveLength(1);
  expect(itemAt(turnAt(model, 0), 1).outputImages).toHaveLength(1);

  model = applyNotification(
    model,
    {
      method: "history/updated",
      params: {
        threadId: thread.id,
        ref: thread.evener.ref,
        bootGeneration: "",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        turns: [
          {
            id: "turn_1",
            status: "completed",
            itemsView: "full",
          },
        ],
        items: [
          { id: "user-item", turnId: "turn_1", type: "userMessage", text: "look", status: "completed" },
          {
            id: "tool-item",
            turnId: "turn_1",
            type: "commandExecution",
            toolName: "shell",
            callId: "call-1",
            text: "",
            status: "completed",
          },
        ].map((it) => ({ ...it, turnId: it.turnId ?? "turn_1" })),
      },
    } as AnyNotification,
    2000,
  );
  expect(itemAt(turnAt(model, 0), 0).images).toHaveLength(1);
  expect(itemAt(turnAt(model, 0), 1).outputImages).toHaveLength(1);
});

test("mergeOlderItemPage keeps a fresh completed call ahead of an older standalone result", () => {
  const model = testHydrate({
    turns: [
      toolWireTurn("fresh-call", "item_tool_1_0", "call_A", {
        output: "fresh output",
        status: "completed",
        completedAt: 20,
      }),
    ],
  });

  const merged = mergeOlderItemPage(model, {
    data: [toolWireTurn("old-result", "item_tool_result_0_0", "call_A", { output: "old output", completedAt: 10 })],
  });
  const item = merged.turns.flatMap((turn) => turn.items).find((candidate) => candidate.callId === "call_A");

  expect(item).toMatchObject({
    id: "item_tool_1_0",
    output: "fresh output",
    status: "completed",
    completedAt: new Date(20).toISOString(),
  });
});

test("mergeOlderItemPage fills absent fresh call fields from an older result without replacing fresh status", () => {
  const model = testHydrate({
    turns: [toolWireTurn("fresh-call", "item_tool_1_0", "call_B", { status: "inProgress", startedAt: 20 })],
  });

  const merged = mergeOlderItemPage(model, {
    data: [
      toolWireTurn("old-result", "item_tool_result_0_0", "call_B", {
        output: "older output",
        status: "completed",
        completedAt: 10,
        exitCode: 3,
      }),
    ],
  });
  const item = merged.turns.flatMap((turn) => turn.items).find((candidate) => candidate.callId === "call_B");

  expect(item).toMatchObject({
    output: "older output",
    status: "inProgress",
    completedAt: new Date(10).toISOString(),
    exitCode: 3,
  });
});

test("mergeOlderItemPage lets a fresh result settle an older call", () => {
  const model = testHydrate({
    turns: [
      toolWireTurn("fresh-result", "item_tool_result_2_0", "call_C", {
        output: "fresh result",
        status: "completed",
        completedAt: 20,
      }),
    ],
  });

  const merged = mergeOlderItemPage(model, {
    data: [toolWireTurn("old-call", "item_tool_1_0", "call_C", { status: "inProgress", startedAt: 10 })],
  });
  const item = merged.turns.flatMap((turn) => turn.items).find((candidate) => candidate.callId === "call_C");

  expect(item).toMatchObject({
    id: "item_tool_1_0",
    output: "fresh result",
    status: "completed",
    completedAt: new Date(20).toISOString(),
    startedAt: new Date(10).toISOString(),
  });
});

test("mergeOlderItemPage prefers the fresh result even when an older result is visited later", () => {
  const model = testHydrate();
  model.turns = [
    toolModelTurn("fresh-call", { id: "item_tool_1_0", callId: "call_D", status: "inProgress" }),
    toolModelTurn("fresh-result", {
      id: "item_tool_result_2_0",
      callId: "call_D",
      output: "fresh result",
      status: "completed",
      completedAt: new Date(20).toISOString(),
    }),
  ];

  const merged = mergeOlderItemPage(model, {
    data: [
      toolWireTurn("old-result", "item_tool_result_3_0", "call_D", {
        output: "old result",
        status: "completed",
        completedAt: 10,
      }),
    ],
  });
  const item = merged.turns.flatMap((turn) => turn.items).find((candidate) => candidate.callId === "call_D");

  expect(item).toMatchObject({ output: "fresh result", completedAt: new Date(20).toISOString() });
});

test("mergeOlderItemPage preserves an older empty metadata turn beside an unrelated result", () => {
  const model = testHydrate({
    turns: [toolWireTurn("fresh-result", "item_tool_result_2_0", "unrelated-call", { output: "fresh" })],
  });

  const merged = mergeOlderItemPage(model, {
    data: [
      {
        id: "older-empty",
        status: "completed",
        itemsView: "full",
        startedAt: 10,
        completedAt: 20,
        durationMs: 10,
        usage: { inputTokens: 3 },
        cost: "0.03",
        error: { message: "older turn error" },
        items: [],
      },
    ],
  });

  expect(merged.turns.map((turn) => turn.id)).toEqual(["older-empty", "fresh-result"]);
  expect(merged.turns[0]).toMatchObject({
    startedAt: new Date(10).toISOString(),
    completedAt: new Date(20).toISOString(),
    durationMs: 10,
    usage: { inputTokens: 3 },
    cost: "0.03",
    error: { message: "older turn error" },
  });
});

test("mergeOlderItemPage preserves a fresh empty metadata turn beside an unrelated older result", () => {
  const model = testHydrate({
    turns: [
      {
        id: "fresh-empty",
        status: "completed",
        itemsView: "full",
        items: [],
        startedAt: 30,
        completedAt: 40,
        durationMs: 10,
        usage: { outputTokens: 4 },
        cost: "0.04",
        error: { message: "fresh turn error" },
      },
    ],
  });

  const merged = mergeOlderItemPage(model, {
    data: [toolWireTurn("older-result", "item_tool_result_0_0", "unrelated-call", { output: "old" })],
  });

  expect(merged.turns.map((turn) => turn.id)).toEqual(["older-result", "fresh-empty"]);
  expect(merged.turns[1]).toMatchObject({
    startedAt: new Date(30).toISOString(),
    completedAt: new Date(40).toISOString(),
    durationMs: 10,
    usage: { outputTokens: 4 },
    cost: "0.04",
    error: { message: "fresh turn error" },
  });
});

test("mergeOlderItemPage preserves metadata on a consumed result-only turn", () => {
  const model = testHydrate({
    turns: [toolWireTurn("fresh-call", "item_tool_1_0", "call-with-metadata", { status: "inProgress" })],
  });

  const merged = mergeOlderItemPage(model, {
    data: [
      {
        id: "older-result",
        status: "completed",
        itemsView: "full",
        startedAt: 50,
        completedAt: 60,
        durationMs: 10,
        usage: { inputTokens: 5 },
        cost: "0.05",
        error: { message: "consumed turn error" },
        items: [
          {
            id: "item_tool_result_0_0",
            turnId: "older-result",
            type: "commandExecution",
            toolName: "shell",
            callId: "call-with-metadata",
            output: "old result",
            status: "completed",
          },
        ],
      },
    ],
  });

  expect(merged.turns.map((turn) => turn.id)).toEqual(["older-result", "fresh-call"]);
  expect(merged.turns[0]).toMatchObject({
    items: [],
    startedAt: new Date(50).toISOString(),
    completedAt: new Date(60).toISOString(),
    durationMs: 10,
    usage: { inputTokens: 5 },
    cost: "0.05",
    error: { message: "consumed turn error" },
  });
  expect(merged.turns[1]?.items[0]).toMatchObject({
    id: "item_tool_1_0",
    callId: "call-with-metadata",
    toolName: "shell",
    output: "old result",
    status: "inProgress",
  });
});

test("mergeOlderItemPage drops a metadata-free consumed result-only turn", () => {
  const model = testHydrate({
    turns: [toolWireTurn("fresh-call", "item_tool_1_0", "call-without-metadata", { status: "inProgress" })],
  });

  const merged = mergeOlderItemPage(model, {
    data: [
      toolWireTurn("older-result", "item_tool_result_0_0", "call-without-metadata", {
        output: "old result",
        status: "completed",
      }),
    ],
  });

  expect(merged.turns.map((turn) => turn.id)).toEqual(["fresh-call"]);
});

// The #2213 final-verdict Medium: contribution tracking attributed text
// suppliers by strict value equality, so an explicitly provided empty text
// and an omitted text — both hydrating to "" — recorded as sharing the text
// supplier. The side that actually supplied text then held every supplier
// only together with the side that supplied none, so itemSideContributes
// denied it the claim and the page's contribution went unrecorded. The
// attribution must follow mergePageItem's own presence rule: itemTextPresence
// for text, property presence for the spread-merged fields.
test("an explicitly provided empty text claims the text supply over an omitted-text item", () => {
  // The page side: a hand-built item carries no presence marker, which the
  // package reads as "provided" — the same semantics an older/mixed producer
  // that spells text: "" gets. The retained side: the sparse wire shape,
  // which hydrates an omitted text to "" under the omitted marker.
  const pageItem: ItemModel = { id: "x1", turnId: "tp", type: "assistant", text: "" };
  const retainedItem: ItemModel = markItemTextOmitted({
    id: "x1",
    turnId: "t1",
    type: "assistant",
    text: "",
  });
  const folds = mergeTurnHistoryWithFolds(
    [{ id: "tp", status: "completed", items: [pageItem] }],
    [{ id: "t1", status: "completed", items: [retainedItem] }],
  );
  const merged = folds.turns.flatMap((turn) => turn.items).find((item) => item.id === "x1");
  expect(merged).toBeDefined();
  // The merge kept the page's text (mergePageItem's presence rule: a provided
  // text wins over an omitted one), so the page is the only side that
  // supplied it — removing the page loses the merged item's text settle.
  const pageInputs = new Set<ItemModel>([pageItem]);
  expect(folds.itemSideContributes(merged!, (input) => pageInputs.has(input))).toBe(true);
});

test("mergeOlderItemPage preserves same-source call/result folding", () => {
  const merged = mergeOlderItemPage(testHydrate(), {
    data: [
      toolWireTurn("old-call", "item_tool_1_0", "call_E", { status: "inProgress", startedAt: 10 }),
      toolWireTurn("old-result", "item_tool_result_2_0", "call_E", {
        output: "same-source result",
        status: "completed",
        completedAt: 20,
      }),
    ],
  });

  const items = merged.turns.flatMap((turn) => turn.items).filter((item) => item.callId === "call_E");
  expect(items).toHaveLength(1);
  expect(items[0]).toMatchObject({
    id: "item_tool_1_0",
    output: "same-source result",
    status: "completed",
    completedAt: new Date(20).toISOString(),
  });
});

test("mergeOlderItemPage keeps the last same-source result in traversal order", () => {
  const merged = mergeOlderItemPage(testHydrate(), {
    data: [
      toolWireTurn("old-call", "item_tool_1_0", "call_F", { status: "inProgress", startedAt: 10 }),
      toolWireTurn("old-result-one", "item_tool_result_2_0", "call_F", {
        output: "first result",
        status: "completed",
        completedAt: 20,
      }),
      toolWireTurn("old-result-two", "item_tool_result_3_0", "call_F", {
        output: "last result",
        status: "completed",
        completedAt: 30,
      }),
    ],
  });

  const items = merged.turns.flatMap((turn) => turn.items).filter((item) => item.callId === "call_F");
  expect(items).toHaveLength(1);
  expect(items[0]).toMatchObject({ output: "last result", completedAt: new Date(30).toISOString() });
});

test("mergeOlderItemPage does not promote fields inherited by a fresh result fragment", () => {
  const model = testHydrate();
  model.turns = [
    toolModelTurn("turn-provenance", {
      id: "item_tool_1_0",
      callId: "call-provenance",
      output: "fresh call output",
      status: "completed",
    }),
    toolModelTurn("turn-provenance", {
      id: "item_tool_result_2_0",
      callId: "call-provenance",
      status: "completed",
    }),
  ];

  const merged = mergeOlderItemPage(model, {
    data: [
      toolWireTurn("turn-provenance", "item_tool_result_2_0", "call-provenance", {
        output: "older result output",
        status: "completed",
      }),
    ],
  });

  const item = merged.turns.flatMap((turn) => turn.items).find((candidate) => candidate.callId === "call-provenance");
  expect(item).toMatchObject({ id: "item_tool_1_0", output: "fresh call output" });
});

test("mergeOlderItemPage preserves older fields through a transitive result alias", () => {
  const model = testHydrate();
  const freshCall = toolModelTurn("fresh-turn", {
    id: "item_tool_0_0",
    callId: "call-alias",
    position: { entry: 1, item: 0 },
  });
  const freshResultA = toolModelTurn("fresh-turn", {
    id: "item_tool_result_a",
    callId: "call-alias",
    transcriptKey: "result-key",
    position: { entry: 1, item: 1 },
  });
  const freshResultB = toolModelTurn("fresh-turn", {
    id: "item_tool_result_b",
    callId: "call-alias",
    transcriptKey: "result-key",
    position: { entry: 1, item: 2 },
  });
  model.turns = [
    {
      id: "fresh-turn",
      status: "completed",
      items: [freshCall.items[0]!, freshResultA.items[0]!, freshResultB.items[0]!],
    },
  ];

  const merged = mergeOlderItemPage(model, {
    data: [
      toolWireTurn("older-result", "item_tool_result_a", "call-alias", {
        output: "older output",
        transcriptKey: undefined,
        status: "completed",
        position: { entry: 1, item: 1 },
      }),
    ],
  });
  const item = merged.turns.flatMap((turn) => turn.items).find((candidate) => candidate.callId === "call-alias");

  expect(item).toMatchObject({ id: "item_tool_0_0", output: "older output" });
});

test("mergeOlderItemPage routes a fresh result with an omitted callId through its resolved item", () => {
  const model = testHydrate({
    turns: [
      toolWireTurn("fresh-call", "item_tool_0_0", "call-routing"),
      toolWireTurn("fresh-result", "item_tool_result_1_0", "call-routing", {
        callId: undefined,
        output: "fresh output",
        status: "completed",
      }),
    ],
  });

  const merged = mergeOlderItemPage(model, {
    data: [
      toolWireTurn("older-result", "item_tool_result_1_0", "call-routing", {
        output: "older output",
        status: "completed",
      }),
    ],
  });
  const item = merged.turns.flatMap((turn) => turn.items).find((candidate) => candidate.callId === "call-routing");

  expect(merged.turns.flatMap((turn) => turn.items)).toHaveLength(1);
  expect(item).toMatchObject({ id: "item_tool_0_0", output: "fresh output" });
});

test("mergeOlderItemPage routes a fresh call with an omitted callId through its resolved item", () => {
  const model = testHydrate({
    turns: [
      toolWireTurn("fresh-call", "item_tool_0_0", "call-routing", {
        callId: undefined,
        output: "fresh output",
      }),
    ],
  });

  const merged = mergeOlderItemPage(model, {
    data: [
      toolWireTurn("older-call", "item_tool_0_0", "call-routing"),
      toolWireTurn("older-result", "item_tool_result_1_0", "call-routing", {
        output: "older output",
        status: "completed",
      }),
    ],
  });
  const item = merged.turns.flatMap((turn) => turn.items).find((candidate) => candidate.callId === "call-routing");

  expect(item).toMatchObject({ id: "item_tool_0_0", output: "fresh output" });
});

test("mergeOlderItemPage routes an older fallback with an omitted callId through its resolved item", () => {
  const model = testHydrate();
  const freshCall = toolModelTurn("fresh-turn", { id: "item_tool_0_0", callId: "call-routing" });
  const freshResult = toolModelTurn("fresh-result", { id: "item_tool_result_1_0", callId: "call-routing" });
  model.turns = [freshCall, freshResult];

  const merged = mergeOlderItemPage(model, {
    data: [
      toolWireTurn("older-result", "item_tool_result_1_0", "call-routing", {
        callId: undefined,
        output: "older output",
        status: "completed",
      }),
    ],
  });
  const item = merged.turns.flatMap((turn) => turn.items).find((candidate) => candidate.callId === "call-routing");

  expect(item).toMatchObject({ id: "item_tool_0_0", output: "older output" });
});

function countIdentityReadsForMerge(count: number, includeTool: boolean): number {
  let reads = 0;
  const items = Array.from({ length: count }, (_, index) => {
    const item: ItemModel = {
      id: `message-${index}`,
      turnId: "fresh",
      type: "agentMessage",
      text: "",
    };
    Object.defineProperty(item, "id", {
      configurable: true,
      enumerable: true,
      get: () => {
        reads += 1;
        return `message-${index}`;
      },
    });
    return item;
  });
  if (includeTool) {
    items.push(
      {
        id: "item_tool_0_0",
        turnId: "fresh",
        type: "commandExecution",
        text: "",
        callId: "cost-call",
      },
      {
        id: "item_tool_result_1_0",
        turnId: "fresh",
        type: "commandExecution",
        text: "",
        callId: "cost-call",
        output: "done",
        status: "completed",
      },
    );
  }
  const model = testHydrate();
  model.turns = [{ id: "fresh", status: "completed", items }];
  mergeOlderItemPage(model, { data: [] });
  return reads;
}

test("mergeOlderItemPage keeps no-tool provenance work linear as history grows", () => {
  const smaller = countIdentityReadsForMerge(8, false);
  const larger = countIdentityReadsForMerge(16, false);
  expect(larger).toBeLessThanOrEqual(smaller * 3 + 16);
});

test("mergeOlderItemPage keeps one-call provenance work linear as history grows", () => {
  const smaller = countIdentityReadsForMerge(20, true);
  const larger = countIdentityReadsForMerge(40, true);
  expect(larger).toBeLessThanOrEqual(smaller * 3 + 40);
});

function countActiveToolCandidateReads(callCount: number): { reads: number; items: ItemModel[] } {
  let reads = 0;
  const charged = (id: string, overrides: Partial<ItemModel>): ItemModel => {
    const item: ItemModel = {
      id,
      turnId: "fresh",
      type: "commandExecution",
      text: "",
      toolName: "shell",
      ...overrides,
    };
    Object.defineProperty(item, "id", {
      configurable: true,
      enumerable: true,
      get: () => {
        reads += 1;
        return id;
      },
    });
    return item;
  };
  const items: ItemModel[] = [];
  for (let index = 0; index < callCount; index += 1) {
    const callId = `cost-active-${index}`;
    // The call carries a provisional output of its own; the result below
    // supersedes it (fresh results precede fresh calls in preferredToolField),
    // so the merged item's output is a real field-precedence assertion, not
    // just a filled-in blank.
    items.push(charged(`item_tool_${index}_0`, { callId, output: `call-output-${index}` }));
    items.push(charged(`item_tool_result_${index}_0`, { callId, output: `output-${index}`, status: "completed" }));
  }
  const model = testHydrate();
  model.turns = [{ id: "fresh", status: "completed", items }];
  const merged = mergeOlderItemPage(model, { data: [] });
  return { reads, items: merged.turns.flatMap((turn) => turn.items) };
}

// The sibling guards above instrument ordinary (agentMessage) item ids, so they
// pin only work done BEFORE the `if (!item.callId) continue` guard: a regression
// that scans the active call/result candidates themselves — the work after that
// guard — never touches an ordinary item's id and would be invisible to them.
// This fixture grows the active candidate set (N then 2N genuine call/result
// pairs, the shape the live path produces) with every call and result id
// instrumented, so a superlinear scan of those candidates shows up as a
// superlinear read count.
//
// Measured on the linear collector: 20 active calls -> 180 reads, 40 -> 360
// (9 reads per call: exactly 2x work for 2x the candidates). The pre-#1982
// quadratic candidate collector (9747f949) measured 1820 -> 6840 on this same
// fixture (3.76x). The 3x + 40 bound below therefore fails the quadratic scan
// by a wide margin while leaving a linear collector ~1.5x of headroom; it is
// deterministic, with no elapsed-time threshold.
test("mergeOlderItemPage keeps growing active tool-call candidates linear, folding each result", () => {
  const smaller = countActiveToolCandidateReads(20);
  const larger = countActiveToolCandidateReads(40);

  // The counter must actually observe candidate identity reads, not zero.
  expect(smaller.reads).toBeGreaterThan(0);

  // The fold is real, so the count above is taken over a merge that did the
  // provenance work: one surviving item per call, each carrying its RESULT's
  // output (precedence over the call's own) and status, and no standalone
  // result left behind.
  expect(larger.items).toHaveLength(40);
  expect(larger.items.some((item) => item.id.startsWith("item_tool_result_"))).toBe(false);
  for (const item of larger.items) {
    expect(item.callId).toBeDefined();
    expect(item.output).toBe(`output-${item.callId?.slice("cost-active-".length)}`);
    expect(item.status).toBe("completed");
  }

  expect(larger.reads).toBeLessThanOrEqual(smaller.reads * 3 + 40);
});

test("mergeOlderItemPage preserves older fallback fields across distinct result fragments", () => {
  const model = testHydrate();
  model.turns = [
    toolModelTurn("turn-fallback", { id: "item_tool_1_0", callId: "call-fallback" }),
    toolModelTurn("turn-fallback", { id: "item_tool_result_2_0", callId: "call-fallback" }),
  ];

  const merged = mergeOlderItemPage(model, {
    data: [
      toolWireTurn("turn-fallback", "item_tool_result_0_0", "call-fallback", {
        output: "older output",
        error: "older error",
        prevalOnly: false,
        exitCode: 0,
        completedAt: 10,
        status: "completed",
        outputImages: [{ source: "test", url: "older-image" }],
        raw: { source: "older" },
      }),
    ],
  });

  const item = merged.turns.flatMap((turn) => turn.items).find((candidate) => candidate.callId === "call-fallback");
  expect(item).toMatchObject({
    output: "older output",
    error: "older error",
    prevalOnly: false,
    exitCode: 0,
    completedAt: new Date(10).toISOString(),
    status: "completed",
    outputImages: [{ src: "older-image" }],
    raw: { source: "older" },
  });
});

test("mergeOlderItemPage keeps every defined fresh call field ahead of an older result", () => {
  const model = testHydrate();
  model.turns = [
    toolModelTurn("turn-fields-call", {
      id: "item_tool_1_0",
      callId: "call-fields-call",
      output: "fresh output",
      error: "fresh error",
      prevalOnly: true,
      exitCode: 7,
      completedAt: new Date(20).toISOString(),
      status: "inProgress",
      outputImages: [{ src: "fresh-image" }],
      raw: { source: "fresh" },
    }),
  ];

  const merged = mergeOlderItemPage(model, {
    data: [
      toolWireTurn("turn-fields-call", "item_tool_result_0_0", "call-fields-call", {
        output: "older output",
        error: "older error",
        prevalOnly: false,
        exitCode: 3,
        completedAt: 10,
        status: "completed",
        outputImages: [{ source: "test", url: "older-image" }],
        raw: { source: "older" },
      }),
    ],
  });

  const item = merged.turns.flatMap((turn) => turn.items).find((candidate) => candidate.callId === "call-fields-call");
  expect(item).toMatchObject({
    output: "fresh output",
    error: "fresh error",
    prevalOnly: true,
    exitCode: 7,
    completedAt: new Date(20).toISOString(),
    status: "inProgress",
    outputImages: [{ src: "fresh-image" }],
    raw: { source: "fresh" },
  });
});

test("mergeOlderItemPage keeps every defined fresh result field ahead of an older call", () => {
  const model = testHydrate();
  model.turns = [
    toolModelTurn("turn-fields-result", {
      id: "item_tool_result_2_0",
      callId: "call-fields-result",
      output: "fresh output",
      error: "fresh error",
      prevalOnly: true,
      exitCode: 7,
      completedAt: new Date(20).toISOString(),
      status: "completed",
      outputImages: [{ src: "fresh-image" }],
      raw: { source: "fresh" },
    }),
  ];

  const merged = mergeOlderItemPage(model, {
    data: [
      toolWireTurn("turn-fields-result", "item_tool_1_0", "call-fields-result", {
        output: "older output",
        error: "older error",
        prevalOnly: false,
        exitCode: 3,
        completedAt: 10,
        status: "inProgress",
        outputImages: [{ source: "test", url: "older-image" }],
        raw: { source: "older" },
      }),
    ],
  });

  const item = merged.turns
    .flatMap((turn) => turn.items)
    .find((candidate) => candidate.callId === "call-fields-result");
  expect(item).toMatchObject({
    id: "item_tool_1_0",
    output: "fresh output",
    error: "fresh error",
    prevalOnly: true,
    exitCode: 7,
    completedAt: new Date(20).toISOString(),
    status: "completed",
    outputImages: [{ src: "fresh-image" }],
    raw: { source: "fresh" },
  });
});

test("mergeOlderItemPage combines duplicate fresh result fragments without older fills", () => {
  const model = testHydrate();
  model.turns = [
    toolModelTurn("turn-duplicate-fresh", { id: "item_tool_1_0", callId: "call-duplicate-fresh" }),
    toolModelTurn("turn-duplicate-fresh", {
      id: "item_tool_result_2_0",
      callId: "call-duplicate-fresh",
      output: "fresh output",
    }),
    toolModelTurn("turn-duplicate-fresh", {
      id: "item_tool_result_2_0",
      callId: "call-duplicate-fresh",
      error: "fresh error",
    }),
  ];

  const merged = mergeOlderItemPage(model, {
    data: [
      toolWireTurn("turn-duplicate-fresh", "item_tool_result_0_0", "call-duplicate-fresh", {
        output: "older output",
        error: "older error",
        raw: { source: "older" },
      }),
    ],
  });

  const item = merged.turns
    .flatMap((turn) => turn.items)
    .find((candidate) => candidate.callId === "call-duplicate-fresh");
  expect(item).toMatchObject({ output: "fresh output", error: "fresh error", raw: { source: "older" } });
});

test("mergeOlderItemPage keeps normalized same-source result traversal for older fragments", () => {
  const merged = mergeOlderItemPage(testHydrate(), {
    data: [
      toolWireTurn("turn-order-old", "item_tool_1_0", "call-order-old", { position: { entry: 1, item: 0 } }),
      toolWireTurn("turn-order-old", "item_tool_result_3_0", "call-order-old", {
        position: { entry: 1, item: 2 },
        output: "later",
      }),
      toolWireTurn("turn-order-old", "item_tool_result_2_0", "call-order-old", {
        position: { entry: 1, item: 1 },
        output: "earlier",
      }),
    ],
  });

  expect(merged.turns[0]?.items[0]).toMatchObject({ id: "item_tool_1_0", output: "later" });
});

test("mergeOlderItemPage keeps normalized fresh-only result traversal on an empty page", () => {
  const model = testHydrate();
  model.turns = [
    toolModelTurn("turn-order-fresh", {
      id: "item_tool_1_0",
      callId: "call-order-fresh",
      position: { entry: 1, item: 0 },
    }),
    toolModelTurn("turn-order-fresh", {
      id: "item_tool_result_3_0",
      callId: "call-order-fresh",
      position: { entry: 1, item: 2 },
      output: "later",
    }),
    toolModelTurn("turn-order-fresh", {
      id: "item_tool_result_2_0",
      callId: "call-order-fresh",
      position: { entry: 1, item: 1 },
      output: "earlier",
    }),
  ];

  const merged = mergeOlderItemPage(model, { data: [] });
  expect(merged.turns[0]?.items[0]).toMatchObject({ id: "item_tool_1_0", output: "later" });
});

test("mergeOlderItemPage keeps normalized fresh traversal when an older bridge joins fragments", () => {
  const model = testHydrate();
  model.turns = [
    toolModelTurn("fresh-call", {
      id: "item_tool_1_0",
      callId: "call-order-bridge",
      position: { entry: 1, item: 0 },
    }),
    toolModelTurn("fresh-late", {
      id: "item_tool_result_3_0",
      callId: "call-order-bridge",
      position: { entry: 1, item: 2 },
      output: "later",
    }),
    toolModelTurn("fresh-early", {
      id: "item_tool_result_2_0",
      callId: "call-order-bridge",
      position: { entry: 1, item: 1 },
      output: "earlier",
    }),
  ];

  const bridge = {
    id: "old-bridge",
    status: "completed" as const,
    itemsView: "full" as const,
    items: [
      {
        id: "item_tool_1_0",
        turnId: "old-bridge",
        type: "commandExecution",
        callId: "call-order-bridge",
        position: { entry: 1, item: 0 },
      },
      {
        id: "item_tool_result_3_0",
        turnId: "old-bridge",
        type: "commandExecution",
        callId: "call-order-bridge",
        position: { entry: 1, item: 2 },
        output: "older late",
      },
      {
        id: "item_tool_result_2_0",
        turnId: "old-bridge",
        type: "commandExecution",
        callId: "call-order-bridge",
        position: { entry: 1, item: 1 },
        output: "older early",
      },
    ],
  };

  const merged = mergeOlderItemPage(model, { data: [bridge] });
  expect(merged.turns.flatMap((turn) => turn.items).find((item) => item.callId === "call-order-bridge")).toMatchObject({
    id: "item_tool_1_0",
    output: "later",
  });
});

test("mergeTurnHistory keeps a fresh completed call ahead of an older partial result", () => {
  const merged = mergeTurnHistory(
    [
      toolModelTurn("turn-public-A", {
        id: "item_tool_result_0_0",
        callId: "call-public-A",
        output: "older output",
        completedAt: new Date(10).toISOString(),
      }),
    ],
    [
      toolModelTurn("turn-public-A", {
        id: "item_tool_1_0",
        callId: "call-public-A",
        output: "fresh output",
        status: "completed",
        completedAt: new Date(20).toISOString(),
      }),
    ],
  );

  expect(merged.turns).toHaveLength(1);
  expect(merged.turns[0]?.items[0]).toMatchObject({
    id: "item_tool_1_0",
    output: "fresh output",
    status: "completed",
    completedAt: new Date(20).toISOString(),
  });
  expect(merged.olderCoverage).toBe(false);
});

test("mergeTurnHistory keeps a fresh result ahead of an older call", () => {
  const merged = mergeTurnHistory(
    [
      toolModelTurn("turn-public-B", {
        id: "item_tool_1_0",
        callId: "call-public-B",
        status: "inProgress",
        startedAt: new Date(10).toISOString(),
      }),
    ],
    [
      toolModelTurn("turn-public-B", {
        id: "item_tool_result_2_0",
        callId: "call-public-B",
        output: "fresh result",
        status: "completed",
        completedAt: new Date(20).toISOString(),
      }),
    ],
  );

  expect(merged.turns).toHaveLength(1);
  expect(merged.turns[0]?.items[0]).toMatchObject({
    id: "item_tool_1_0",
    output: "fresh result",
    status: "completed",
    completedAt: new Date(20).toISOString(),
    startedAt: new Date(10).toISOString(),
  });
  expect(merged.olderCoverage).toBe(true);
});

test("mergeTurnHistory counts an older result field the fresh call does not supply", () => {
  const merged = mergeTurnHistory(
    [
      toolModelTurn("turn-public-C", {
        id: "item_tool_result_0_0",
        callId: "call-public-C",
        output: "older output",
        exitCode: 0,
      }),
    ],
    [
      toolModelTurn("turn-public-C", {
        id: "item_tool_1_0",
        callId: "call-public-C",
        output: "fresh output",
        status: "completed",
      }),
    ],
  );

  expect(merged.olderCoverage).toBe(true);
  expect(merged.turns[0]?.items[0]).toMatchObject({ id: "item_tool_1_0", output: "fresh output", exitCode: 0 });
});

test("mergeTurnHistory does not count a result a fresh call in another turn supersedes", () => {
  const merged = mergeTurnHistory(
    [
      toolModelTurn("turn-older-result", {
        id: "item_tool_result_0_0",
        callId: "call-cross-turn",
        output: "older output",
        completedAt: new Date(10).toISOString(),
      }),
    ],
    [
      toolModelTurn("turn-fresh-call", {
        id: "item_tool_1_0",
        callId: "call-cross-turn",
        output: "fresh output",
        status: "completed",
        completedAt: new Date(20).toISOString(),
      }),
    ],
  );

  expect(merged.olderCoverage).toBe(false);
  expect(merged.turns).toHaveLength(1);
  expect(merged.turns[0]?.items[0]).toMatchObject({ id: "item_tool_1_0", output: "fresh output" });
});

test("mergeTurnHistory counts a consumed shell's preserved usage as coverage", () => {
  const merged = mergeTurnHistory(
    [
      {
        id: "turn-older-result",
        status: "completed",
        usage: { inputTokens: 30, outputTokens: 4 },
        items: [
          {
            id: "item_tool_result_0_0",
            turnId: "turn-older-result",
            type: "commandExecution",
            toolName: "shell",
            callId: "call-cross-turn",
            text: "",
            output: "older output",
            status: "completed",
            completedAt: new Date(10).toISOString(),
          },
        ],
      },
    ],
    [
      toolModelTurn("turn-fresh-call", {
        id: "item_tool_1_0",
        callId: "call-cross-turn",
        output: "fresh output",
        status: "completed",
        completedAt: new Date(20).toISOString(),
      }),
    ],
  );

  expect(merged.olderCoverage).toBe(true);
  expect(merged.turns.map((turn) => turn.id)).toEqual(["turn-older-result", "turn-fresh-call"]);
  expect(merged.turns[0]?.items).toHaveLength(0);
  expect(merged.turns[0]?.usage).toEqual({ inputTokens: 30, outputTokens: 4 });
  expect(merged.turns[1]?.items[0]).toMatchObject({ id: "item_tool_1_0", output: "fresh output" });
  expect(merged.turns[1]?.usage).toBeUndefined();
});

test("mergeTurnHistory does not count fallback fields on a fold-discarded matched result", () => {
  const merged = mergeTurnHistory(
    [
      {
        id: "turn-matched",
        status: "completed",
        items: [
          {
            id: "item_tool_result_1",
            turnId: "turn-matched",
            type: "commandExecution",
            toolName: "shell",
            callId: "call-matched",
            text: "",
            output: "older output",
            startedAt: "2026-09-19T10:00:00.000Z",
            status: "completed",
            completedAt: new Date(10).toISOString(),
          },
        ],
      },
    ],
    [
      {
        id: "turn-matched",
        status: "completed",
        items: [
          {
            id: "item_tool_1",
            turnId: "turn-matched",
            type: "commandExecution",
            toolName: "shell",
            callId: "call-matched",
            text: "",
            output: "fresh output",
            status: "completed",
            completedAt: new Date(20).toISOString(),
          },
          {
            id: "item_tool_result_1",
            turnId: "turn-matched",
            type: "commandExecution",
            toolName: "shell",
            callId: "call-matched",
            text: "",
            status: "completed",
            completedAt: new Date(20).toISOString(),
          },
        ],
      },
    ],
  );

  expect(merged.olderCoverage).toBe(false);
  expect(merged.turns).toHaveLength(1);
  const items = merged.turns[0]?.items ?? [];
  expect(items).toHaveLength(1);
  expect(items[0]).toMatchObject({ id: "item_tool_1", output: "fresh output" });
  expect(items[0]?.startedAt).toBeUndefined();
  expect(items.map((item) => item.id)).not.toContain("item_tool_result_1");
});

test("mergeTurnHistory counts surviving result output beside a consumed shell's preserved usage", () => {
  const merged = mergeTurnHistory(
    [
      {
        id: "turn-older-result",
        status: "completed",
        usage: { inputTokens: 30, outputTokens: 4 },
        items: [
          {
            id: "item_tool_result_0_0",
            turnId: "turn-older-result",
            type: "commandExecution",
            toolName: "shell",
            callId: "call-cross-turn",
            text: "",
            output: "older output",
            status: "completed",
            completedAt: new Date(10).toISOString(),
          },
        ],
      },
    ],
    [
      toolModelTurn("turn-fresh-call", {
        id: "item_tool_1_0",
        callId: "call-cross-turn",
        status: "completed",
        completedAt: new Date(20).toISOString(),
      }),
    ],
  );

  expect(merged.olderCoverage).toBe(true);
  expect(merged.turns.map((turn) => turn.id)).toEqual(["turn-older-result", "turn-fresh-call"]);
  expect(merged.turns[0]?.items).toHaveLength(0);
  expect(merged.turns[0]?.usage).toEqual({ inputTokens: 30, outputTokens: 4 });
  expect(merged.turns[1]?.items[0]).toMatchObject({ id: "item_tool_1_0", output: "older output" });
  expect(merged.turns[1]?.usage).toBeUndefined();
});

test("mergeTurnHistory counts surviving output despite a folded result's discarded text", () => {
  let model = testHydrateV6({
    turns: [{ id: "turn-live", status: "inProgress", itemsView: "full", items: [] }],
  });
  const startItem = (item: ThreadItem, at: number): ThreadModel =>
    applyNotification(
      model,
      {
        method: "history/updated",
        params: {
          threadId: "thr_t",
          ref: "ref_t",
          bootGeneration: "",
          epoch: 1,
          snapshot: { incarnation: "inc-1", length: 1 },
          items: [{ ...item, turnId: "turn-live" }],
        },
      },
      at,
    );
  // Wire items without text land with omitted text presence, and the live path
  // appends both without folding the result into its call.
  model = startItem(
    { type: "commandExecution", id: "item_tool_1", turnId: "turn-live", toolName: "shell", callId: "call-text" },
    2000,
  );
  model = startItem(
    {
      type: "commandExecution",
      id: "item_tool_result_1",
      turnId: "turn-live",
      toolName: "shell",
      callId: "call-text",
      status: "completed",
    },
    2100,
  );

  const merged = mergeTurnHistory(
    [
      {
        id: "turn-page",
        status: "completed",
        items: [
          {
            id: "item_tool_result_1",
            turnId: "turn-page",
            type: "commandExecution",
            toolName: "shell",
            callId: "call-text",
            text: "older settled text",
            output: "older output",
            status: "completed",
          },
        ],
      },
    ],
    model.turns,
  );

  expect(merged.olderCoverage).toBe(true);
  expect(merged.turns).toHaveLength(1);
  const items = merged.turns[0]?.items ?? [];
  expect(items).toHaveLength(1);
  expect(items[0]).toMatchObject({ id: "item_tool_1", output: "older output" });
  expect(items[0]?.text).toBe("");
});

test("mergeTurnHistory counts an older result whose only call coalesced into a fresh result", () => {
  const merged = mergeTurnHistory(
    [
      {
        id: "turn-call",
        status: "completed",
        items: [
          {
            id: "item_tool_1",
            turnId: "turn-call",
            type: "commandExecution",
            toolName: "shell",
            callId: "call-z",
            transcriptKey: "shared-key",
            text: "",
            argumentsJSON: "same args",
          },
        ],
      },
      {
        id: "turn-page",
        status: "completed",
        items: [
          {
            id: "item_tool_result_2",
            turnId: "turn-page",
            type: "commandExecution",
            toolName: "shell",
            callId: "call-z",
            text: "",
            output: "older output",
            status: "completed",
            completedAt: new Date(10).toISOString(),
          },
        ],
      },
    ],
    [
      {
        id: "turn-fresh",
        status: "completed",
        items: [
          {
            id: "item_tool_result_9",
            turnId: "turn-fresh",
            type: "commandExecution",
            toolName: "shell",
            callId: "call-z",
            transcriptKey: "shared-key",
            text: "",
            argumentsJSON: "same args",
            output: "fresh output",
            status: "completed",
            completedAt: new Date(20).toISOString(),
          },
        ],
      },
    ],
  );

  expect(merged.olderCoverage).toBe(true);
  expect(merged.turns.map((turn) => turn.id)).toEqual(["turn-fresh", "turn-page"]);
  expect(merged.turns[1]?.items[0]).toMatchObject({ id: "item_tool_result_2", output: "older output" });
});

test("mergeTurnHistory does not count an older field a fresh alias chain replaces", () => {
  const newer: TurnModel[] = [
    {
      id: "turn-f1",
      status: "completed",
      items: [
        {
          id: "a",
          turnId: "turn-f1",
          type: "agentMessage",
          text: "",
          transcriptKey: "k",
        },
      ],
    },
    {
      id: "turn-f2",
      status: "completed",
      items: [
        {
          id: "b",
          turnId: "turn-f2",
          type: "agentMessage",
          text: "",
          transcriptKey: "k",
          output: "fresh",
        },
      ],
    },
  ];
  const merged = mergeTurnHistory(
    [
      {
        id: "turn-old",
        status: "completed",
        items: [
          {
            id: "a",
            turnId: "turn-old",
            type: "agentMessage",
            text: "",
            output: "old",
          },
        ],
      },
    ],
    newer,
  );

  expect(merged.turns).toBe(newer);
  expect(merged.olderCoverage).toBe(false);
  expect(merged.transcriptOverlap).toBe(true);
});

test("mergeTurnHistory does not count a field a later older fragment clears", () => {
  const newer: TurnModel[] = [
    {
      id: "turn-f",
      status: "completed",
      items: [{ id: "a", turnId: "turn-f", type: "agentMessage", text: "", status: "completed" }],
    },
  ];
  const merged = mergeTurnHistory(
    [
      {
        id: "turn-1",
        status: "completed",
        items: [
          { id: "a", turnId: "turn-1", type: "agentMessage", text: "", status: "completed", transcriptEntryIndex: 5 },
        ],
      },
      {
        id: "turn-2",
        status: "completed",
        items: [
          {
            id: "a",
            turnId: "turn-2",
            type: "agentMessage",
            text: "",
            status: "completed",
            transcriptEntryIndex: undefined,
          },
        ],
      },
    ],
    newer,
  );

  expect(merged.turns).toBe(newer);
  expect(merged.olderCoverage).toBe(false);
  expect(merged.transcriptOverlap).toBe(true);
});

test("mergeTurnHistory counts an older status the rank merge retains", () => {
  const item = { id: "a", turnId: "turn-1", type: "agentMessage", text: "same" };
  const merged = mergeTurnHistory(
    [{ id: "turn-1", status: "completed", items: [{ ...item, status: "completed" }] }],
    [{ id: "turn-1", status: "completed", items: [{ ...item, status: "inProgress" }] }],
  );

  expect(merged.olderCoverage).toBe(true);
  expect(merged.turns).toHaveLength(1);
  expect(merged.turns[0]?.items[0]).toMatchObject({ id: "a", status: "completed" });
});

test("mergeTurnHistory counts a turn status the rank merge retains", () => {
  const merged = mergeTurnHistory(
    [{ id: "t", status: "completed", items: [] }],
    [{ id: "t", status: "inProgress", items: [] }],
  );

  expect(merged.olderCoverage).toBe(true);
  expect(merged.turns).toHaveLength(1);
  expect(merged.turns[0]?.id).toBe("t");
  expect(merged.turns[0]?.status).toBe("completed");
});

test("mergeTurnHistory does not count an older call a coalesced result supersedes", () => {
  const merged = mergeTurnHistory(
    [
      {
        id: "turn-call",
        status: "completed",
        items: [
          {
            id: "item_tool_1",
            turnId: "turn-call",
            type: "commandExecution",
            toolName: "shell",
            callId: "call-x",
            transcriptKey: "shared-key",
            text: "",
            argumentsJSON: "older args",
          },
        ],
      },
      {
        id: "turn-result",
        status: "completed",
        items: [
          {
            id: "item_tool_result_2",
            turnId: "turn-result",
            type: "commandExecution",
            toolName: "shell",
            callId: "call-x",
            transcriptKey: "shared-key",
            text: "",
            output: "older output",
            status: "completed",
            completedAt: new Date(10).toISOString(),
          },
        ],
      },
    ],
    [
      toolModelTurn("turn-fresh-call", {
        id: "item_tool_9_0",
        callId: "call-x",
        output: "fresh output",
        status: "completed",
        completedAt: new Date(20).toISOString(),
      }),
    ],
  );

  expect(merged.olderCoverage).toBe(false);
  expect(merged.turns).toHaveLength(1);
  expect(merged.turns[0]?.id).toBe("turn-fresh-call");
  expect(merged.turns[0]?.items[0]).toMatchObject({ id: "item_tool_9_0", output: "fresh output" });
});

test("mergeTurnHistory counts a result the fold keeps when no call item exists", () => {
  const merged = mergeTurnHistory(
    [
      toolModelTurn("turn-shared", {
        id: "item_tool_result_0_0",
        callId: "call-results-only",
        output: "older output",
      }),
    ],
    [
      toolModelTurn("turn-shared", {
        id: "item_tool_result_2_0",
        callId: "call-results-only",
        output: "fresh result",
        status: "completed",
      }),
    ],
  );

  expect(merged.olderCoverage).toBe(true);
  expect(merged.turns).toHaveLength(1);
  expect(merged.turns[0]?.items).toHaveLength(2);
});

test("mergeTurnHistory keeps older fallback fields and fragments while fresh fields win", () => {
  const older: TurnModel[] = [
    {
      id: "turn-old",
      status: "completed",
      usage: { inputTokens: 500, outputTokens: 20 },
      items: [
        {
          id: "old-only",
          transcriptKey: "old-only",
          turnId: "turn-old",
          type: "agentMessage",
          text: "older-only item",
          position: { entry: 1, item: 0 },
          status: "completed",
        },
        {
          id: "old-shared",
          transcriptKey: "shared-item",
          turnId: "turn-old",
          type: "agentMessage",
          text: "older text",
          position: { entry: 2, item: 0 },
          status: "completed",
        },
      ],
    },
  ];
  const newer: TurnModel[] = [
    {
      id: "turn-fresh",
      status: "completed",
      items: [
        {
          id: "fresh-shared",
          transcriptKey: "shared-item",
          turnId: "turn-fresh",
          type: "agentMessage",
          text: "fresh text",
          position: { entry: 2, item: 0 },
          status: "completed",
        },
        {
          id: "fresh-only",
          transcriptKey: "fresh-only",
          turnId: "turn-fresh",
          type: "agentMessage",
          text: "fresh-only item",
          position: { entry: 3, item: 0 },
          status: "completed",
        },
      ],
    },
  ];

  const merged = mergeTurnHistory(older, newer);
  expect(merged.olderCoverage).toBe(true);
  expect(merged.transcriptOverlap).toBe(true);
  expect(merged.turns).toHaveLength(1);
  expect(merged.turns[0]?.id).toBe("turn-fresh");
  expect(merged.turns[0]?.usage).toEqual({
    inputTokens: 500,
    outputTokens: 20,
  });
  expect(merged.turns[0]?.items.map((item) => item.transcriptKey)).toEqual(["old-only", "shared-item", "fresh-only"]);
  expect(merged.turns[0]?.items.find((item) => item.transcriptKey === "shared-item")?.text).toBe("fresh text");
});

test("mergeTurnHistory returns the fresh view when older history contributes nothing", () => {
  const newer: TurnModel[] = [{ id: "turn-1", status: "completed", items: [], usage: { inputTokens: 1 } }];
  const older: TurnModel[] = [
    {
      id: "turn-1",
      status: "completed",
      items: [],
      usage: { inputTokens: 99 },
    },
  ];

  const merged = mergeTurnHistory(older, newer);
  expect(merged.turns).toBe(newer);
  expect(merged.olderCoverage).toBe(false);
  expect(merged.transcriptOverlap).toBe(false);
});

test("mergeTurnHistory does not fold duplicate fresh fragments when older history contributes nothing", () => {
  const older: TurnModel[] = [{ id: "turn-1", status: "completed", items: [] }];
  const newer: TurnModel[] = [
    { id: "turn-0", status: "completed", items: [] },
    {
      id: "turn-1",
      status: "completed",
      items: [
        {
          id: "fresh-a",
          turnId: "turn-1",
          type: "agentMessage",
          text: "a",
          status: "completed",
        },
      ],
    },
    {
      id: "turn-1",
      status: "completed",
      items: [
        {
          id: "fresh-b",
          turnId: "turn-1",
          type: "agentMessage",
          text: "b",
          status: "completed",
        },
      ],
    },
  ];

  const merged = mergeTurnHistory(older, newer);
  expect(merged.turns).toBe(newer);
  expect(merged.turns.map((turn) => turn.id)).toEqual(["turn-0", "turn-1", "turn-1"]);
  expect(merged.olderCoverage).toBe(false);
  expect(merged.transcriptOverlap).toBe(false);
});

test("mergeTurnHistory treats matching fresh fragments as one coverage window", () => {
  const older: TurnModel[] = [
    {
      id: "turn-1",
      status: "completed",
      usage: { inputTokens: 1 },
      items: [{ id: "item-a", turnId: "turn-1", type: "agentMessage", text: "a", status: "completed" }],
    },
  ];
  const newer: TurnModel[] = [
    {
      id: "turn-1",
      status: "completed",
      items: [{ id: "item-a", turnId: "turn-1", type: "agentMessage", text: "a", status: "completed" }],
    },
    {
      id: "turn-1",
      status: "completed",
      usage: { inputTokens: 1 },
      items: [{ id: "item-b", turnId: "turn-1", type: "agentMessage", text: "b", status: "completed" }],
    },
  ];

  const merged = mergeTurnHistory(older, newer);
  expect(merged.turns).toBe(newer);
  expect(merged.turns).toHaveLength(2);
  expect(merged.olderCoverage).toBe(false);
  expect(merged.transcriptOverlap).toBe(true);
});

test("mergeTurnHistory checks coverage against every transitively matching fresh fragment", () => {
  const newer: TurnModel[] = [
    {
      id: "turn-a",
      status: "completed",
      items: [{ id: "item-x", turnId: "turn-a", type: "agentMessage", text: "fresh-a", status: "completed" }],
    },
    {
      id: "turn-b",
      status: "completed",
      usage: { inputTokens: 11 },
      items: [{ id: "item-x", turnId: "turn-b", type: "agentMessage", text: "fresh-b", status: "completed" }],
    },
  ];

  const merged = mergeTurnHistory(
    [{ id: "turn-a", status: "completed", usage: { inputTokens: 11 }, items: [] }],
    newer,
  );

  expect(merged.turns).toBe(newer);
  expect(merged.olderCoverage).toBe(false);
  expect(merged.transcriptOverlap).toBe(false);
});

test("mergeTurnHistory treats collectively supplied item fields as covered", () => {
  const newer: TurnModel[] = [
    {
      id: "turn-a",
      status: "completed",
      items: [{ id: "item-x", turnId: "turn-a", type: "agentMessage", text: "fresh-a", status: "completed" }],
    },
    {
      id: "turn-b",
      status: "completed",
      items: [
        {
          id: "item-x",
          turnId: "turn-b",
          type: "agentMessage",
          text: "fresh-b",
          output: "settled output",
          status: "completed",
        },
      ],
    },
  ];

  const merged = mergeTurnHistory(
    [
      {
        id: "turn-a",
        status: "completed",
        items: [
          {
            id: "item-x",
            turnId: "turn-a",
            type: "agentMessage",
            text: "older",
            output: "settled output",
            status: "completed",
          },
        ],
      },
    ],
    newer,
  );

  expect(merged.turns).toBe(newer);
  expect(merged.olderCoverage).toBe(false);
  expect(merged.transcriptOverlap).toBe(true);
});

function orderedHistoryTurn(id: string, entry?: number): TurnModel {
  return {
    id,
    status: "completed",
    items:
      entry === undefined
        ? []
        : [
            {
              id: `item-${id}`,
              turnId: id,
              type: "agentMessage",
              text: id,
              status: "completed",
              position: { entry, item: 0 },
            },
          ],
  };
}

test.each([
  ["unmatched turn between anchors", ["A", "B", "C"], ["A", "C"], ["A", "B", "C"]],
  ["unanchored history and fresh prefix", ["A", "B", "C"], ["D", "A", "C"], ["D", "A", "B", "C"]],
  ["fresh unmatched turn wins the no-position tie", ["A", "B", "C"], ["A", "D", "C"], ["A", "D", "B", "C"]],
  ["unmatched head and tail", ["X", "A", "C", "Z"], ["A", "C"], ["X", "A", "C", "Z"]],
])("mergeTurnHistory weaves %s deterministically", (_name, olderIds, newerIds, expected) => {
  const merged = mergeTurnHistory(
    olderIds.map((id) => orderedHistoryTurn(id)),
    newerIds.map((id) => orderedHistoryTurn(id)),
  );
  expect(merged.turns.map((turn) => turn.id)).toEqual(expected);
  expect(merged.olderCoverage).toBe(true);
});

test("mergeTurnHistory uses canonical item positions to place an older run", () => {
  const merged = mergeTurnHistory(
    [orderedHistoryTurn("A", 1), orderedHistoryTurn("B", 2), orderedHistoryTurn("C", 4)],
    [orderedHistoryTurn("A", 1), orderedHistoryTurn("D", 3), orderedHistoryTurn("C", 4)],
  );

  expect(merged.turns.map((turn) => turn.id)).toEqual(["A", "B", "D", "C"]);
});

test("mergeTurnHistory keeps live-only fields and warning items out of transcript coverage", () => {
  const merged = mergeTurnHistory(
    [
      {
        id: "turn-1",
        status: "completed",
        items: [
          {
            id: "reasoning-1",
            turnId: "turn-1",
            type: "reasoning",
            text: "same",
            status: "completed",
            pendingText: ["live delta"],
            reasoningSummaries: [["same"]],
          },
          {
            id: "warning-1",
            turnId: "turn-1",
            type: "warning",
            text: "provider warning",
            status: "completed",
            warning: { source: "provider", title: "Provider", hint: "slow" },
          },
        ],
      },
    ],
    [
      {
        id: "turn-1",
        status: "completed",
        items: [{ id: "reasoning-1", turnId: "turn-1", type: "reasoning", text: "same", status: "completed" }],
      },
    ],
  );

  expect(merged.olderCoverage).toBe(false);
  expect(merged.turns[0]?.items).toHaveLength(2);
  expect(merged.turns[0]?.items[0]?.pendingText).toEqual(["live delta"]);
  expect(merged.turns[0]?.items[0]?.reasoningSummaries).toEqual([["same"]]);
  expect(merged.turns[0]?.items[1]).toMatchObject({ type: "warning", warning: { title: "Provider" } });

  const warningOnly = mergeTurnHistory(
    [
      {
        id: "turn-warning",
        status: "completed",
        items: [
          {
            id: "warning-only",
            turnId: "turn-warning",
            type: "warning",
            text: "provider warning",
            status: "completed",
            warning: { title: "Provider" },
          },
        ],
      },
    ],
    [{ id: "turn-warning", status: "completed", items: [] }],
  );
  expect(warningOnly.olderCoverage).toBe(false);
  expect(warningOnly.turns[0]?.items).toHaveLength(1);

  const unmatchedWarning = mergeTurnHistory(
    [
      {
        id: "unmatched-warning-turn",
        status: "completed",
        items: [
          {
            id: "unmatched-warning",
            turnId: "unmatched-warning-turn",
            type: "warning",
            text: "provider warning",
            status: "completed",
            warning: { title: "Provider" },
          },
        ],
      },
    ],
    [],
  );
  expect(unmatchedWarning.olderCoverage).toBe(false);
  expect(unmatchedWarning.turns[0]?.items).toHaveLength(1);
});

test("mergeTurnHistory counts canonical fields on an unmatched warning-only turn", () => {
  const merged = mergeTurnHistory(
    [
      {
        id: "unmatched-warning-with-usage",
        status: "completed",
        usage: { inputTokens: 11, outputTokens: 4 },
        completedAt: "2026-09-19T12:00:00.000Z",
        items: [
          {
            id: "warning-with-usage",
            turnId: "unmatched-warning-with-usage",
            type: "warning",
            text: "provider warning",
            status: "completed",
            warning: { title: "Provider" },
          },
        ],
      },
    ],
    [],
  );

  expect(merged.olderCoverage).toBe(true);
  expect(merged.turns[0]?.usage).toEqual({ inputTokens: 11, outputTokens: 4 });
  expect(merged.turns[0]?.completedAt).toBe("2026-09-19T12:00:00.000Z");
  expect(merged.turns[0]?.items).toHaveLength(1);
  expect(merged.turns[0]?.items[0]).toMatchObject({ type: "warning", warning: { title: "Provider" } });
});

test("mergeTurnHistory counts older usage a fresh null does not overwrite", () => {
  const item = { id: "item-1", turnId: "turn-1", type: "agentMessage", text: "shared", status: "completed" };
  const merged = mergeTurnHistory(
    [{ id: "turn-1", status: "completed", usage: { inputTokens: 7, outputTokens: 2 }, items: [item] }],
    [{ id: "turn-1", status: "completed", usage: null, items: [{ ...item }] }],
  );

  expect(merged.olderCoverage).toBe(true);
  expect(merged.turns[0]?.usage).toEqual({ inputTokens: 7, outputTokens: 2 });
});

test("mergeTurnHistory does not count an older null as persisted coverage", () => {
  const item = { id: "item-1", turnId: "turn-1", type: "agentMessage", text: "shared", status: "completed" };
  const merged = mergeTurnHistory(
    [{ id: "turn-1", status: "completed", usage: null, items: [item] }],
    [{ id: "turn-1", status: "completed", items: [{ ...item }] }],
  );

  expect(merged.olderCoverage).toBe(false);
  expect(merged.turns[0]?.usage).toBeNull();
});

test("mergeTurnHistory counts older item raw a fresh null does not overwrite", () => {
  const merged = mergeTurnHistory(
    [
      {
        id: "turn-1",
        status: "completed",
        items: [
          {
            id: "item-1",
            turnId: "turn-1",
            type: "agentMessage",
            text: "",
            raw: { roundTimings: { totalMs: 5 } },
            status: "completed",
          },
        ],
      },
    ],
    [
      {
        id: "turn-1",
        status: "completed",
        items: [{ id: "item-1", turnId: "turn-1", type: "agentMessage", text: "", raw: null, status: "completed" }],
      },
    ],
  );

  expect(merged.olderCoverage).toBe(true);
  expect(merged.turns[0]?.items[0]?.raw).toEqual({ roundTimings: { totalMs: 5 } });
});

test("mergeTurnHistory does not count a spread-overwritten transcript index as coverage", () => {
  const item = { id: "item-1", turnId: "turn-1", type: "agentMessage", text: "shared", status: "completed" };
  const merged = mergeTurnHistory(
    [{ id: "turn-1", status: "completed", items: [{ ...item, transcriptEntryIndex: 5 }] }],
    [{ id: "turn-1", status: "completed", items: [{ ...item, transcriptEntryIndex: undefined }] }],
  );

  expect(merged.olderCoverage).toBe(false);
  expect(merged.turns[0]?.items[0]?.transcriptEntryIndex).toBeUndefined();
});

test("mergeTurnHistory counts older position a fresh undefined does not overwrite", () => {
  const item = { id: "item-1", turnId: "turn-1", type: "agentMessage", text: "shared", status: "completed" };
  const merged = mergeTurnHistory(
    [{ id: "turn-1", status: "completed", items: [{ ...item, position: { entry: 2, item: 0 } }] }],
    [{ id: "turn-1", status: "completed", items: [{ ...item, position: undefined }] }],
  );

  expect(merged.olderCoverage).toBe(true);
  expect(merged.turns[0]?.items[0]?.position).toEqual({ entry: 2, item: 0 });
});

test("mergeTurnHistory does not duplicate an older turn consumed by shared fresh fragments", () => {
  const sharedItem = {
    id: "shared-item",
    turnId: "turn-a",
    type: "agentMessage",
    text: "shared",
    status: "completed",
  };
  const merged = mergeTurnHistory(
    [{ id: "turn-a", status: "completed", usage: { inputTokens: 10 }, items: [] }],
    [
      { id: "turn-a", status: "completed", items: [sharedItem] },
      { id: "turn-b", status: "completed", items: [{ ...sharedItem, turnId: "turn-b" }] },
    ],
  );

  expect(merged.turns).toHaveLength(1);
  expect(merged.turns[0]?.id).toBe("turn-b");
  expect(merged.turns[0]?.usage).toEqual({ inputTokens: 10 });
  expect(merged.turns[0]?.items).toHaveLength(1);
});

function historyTurnWithItems(id: string, itemIds: string[]): TurnModel {
  return {
    id,
    status: "completed",
    items: itemIds.map((itemId) => ({
      id: itemId,
      turnId: id,
      type: "agentMessage",
      text: itemId,
      status: "completed",
    })),
  };
}

test.each([
  ["collapsed anchor alone", ["F"], ["F", "B"]],
  ["fresh prefix", ["X", "F"], ["X", "F", "B"]],
  ["fresh suffix", ["F", "Y"], ["F", "Y", "B"]],
  ["fresh prefix and suffix", ["X", "F", "Y"], ["X", "F", "Y", "B"]],
])("mergeTurnHistory retains a run between collapsed anchors with %s", (_name, freshIds, expectedIds) => {
  const merged = mergeTurnHistory(
    [
      historyTurnWithItems("A", ["item-A"]),
      historyTurnWithItems("B", ["item-B"]),
      historyTurnWithItems("C", ["item-C"]),
    ],
    freshIds.map((id) => historyTurnWithItems(id, id === "F" ? ["item-A", "item-C"] : [])),
  );

  expect(merged.turns.map((turn) => turn.id)).toEqual(expectedIds);
  expect(merged.olderCoverage).toBe(true);
  expect(merged.transcriptOverlap).toBe(true);
  expect(merged.turns.flatMap((turn) => turn.items).filter((item) => item.id === "item-B")).toHaveLength(1);
});

test("mergeTurnHistory retains a warning-only run between collapsed anchors", () => {
  const merged = mergeTurnHistory(
    [
      historyTurnWithItems("A", ["item-A"]),
      {
        id: "B",
        status: "completed",
        items: [{ id: "warning-B", turnId: "B", type: "warning", text: "live warning", status: "completed" }],
      },
      historyTurnWithItems("C", ["item-C"]),
    ],
    [historyTurnWithItems("F", ["item-A", "item-C"])],
  );

  expect(merged.turns.map((turn) => turn.id)).toEqual(["F", "B"]);
  expect(merged.olderCoverage).toBe(false);
  expect(merged.transcriptOverlap).toBe(true);
  expect(merged.turns[1]?.items).toMatchObject([{ id: "warning-B", type: "warning" }]);
});

test("mergeTurnHistory retains runs across multiple collapsed anchor groups", () => {
  const merged = mergeTurnHistory(
    [
      historyTurnWithItems("A", ["item-A"]),
      historyTurnWithItems("B", ["item-B"]),
      historyTurnWithItems("C", ["item-C"]),
      historyTurnWithItems("D", ["item-D"]),
      historyTurnWithItems("E", ["item-E"]),
      historyTurnWithItems("F", ["item-F"]),
      historyTurnWithItems("G", ["item-G"]),
    ],
    [
      historyTurnWithItems("X", []),
      historyTurnWithItems("H", ["item-A", "item-C"]),
      historyTurnWithItems("I", ["item-F", "item-G"]),
      historyTurnWithItems("Y", []),
    ],
  );

  expect(merged.turns.map((turn) => turn.id)).toEqual(["X", "H", "B", "D", "E", "I", "Y"]);
  expect(merged.olderCoverage).toBe(true);
  expect(merged.transcriptOverlap).toBe(true);
  expect(
    merged.turns
      .flatMap((turn) => turn.items)
      .map((item) => item.id)
      .sort(),
  ).toEqual(["item-A", "item-B", "item-C", "item-D", "item-E", "item-F", "item-G"].sort());
});

test("mergeTurnHistory preserves fresh ordering when its window extends before retained turns", () => {
  const older: TurnModel[] = [
    { id: "turn-1", status: "completed", items: [], usage: { inputTokens: 1 } },
    { id: "turn-2", status: "completed", items: [], usage: { inputTokens: 2 } },
  ];
  const newer: TurnModel[] = [
    { id: "turn-0", status: "completed", items: [], usage: { inputTokens: 0 } },
    { id: "turn-1", status: "completed", items: [], usage: { inputTokens: 1 } },
    { id: "turn-2", status: "completed", items: [], usage: { inputTokens: 2 } },
  ];

  const merged = mergeTurnHistory(older, newer);
  expect(merged.turns).toBe(newer);
  expect(merged.turns.map((turn) => turn.id)).toEqual(["turn-0", "turn-1", "turn-2"]);
});

test("mergeTurnHistory preserves fresh ordering when matching older fields contribute", () => {
  const older: TurnModel[] = [
    {
      id: "turn-1",
      status: "completed",
      usage: { inputTokens: 500 },
      items: [
        {
          id: "shared-old",
          transcriptKey: "shared",
          turnId: "turn-1",
          type: "agentMessage",
          text: "old",
          position: { entry: 1, item: 0 },
          status: "completed",
        },
      ],
    },
    { id: "turn-2", status: "completed", items: [], usage: { inputTokens: 2 } },
  ];
  const newer: TurnModel[] = [
    { id: "turn-0", status: "completed", items: [], usage: { inputTokens: 0 } },
    {
      id: "turn-1",
      status: "completed",
      items: [
        {
          id: "shared-new",
          transcriptKey: "shared",
          turnId: "turn-1",
          type: "agentMessage",
          text: "new",
          position: { entry: 1, item: 0 },
          status: "completed",
        },
      ],
    },
    { id: "turn-2", status: "completed", items: [], usage: { inputTokens: 2 } },
  ];

  const merged = mergeTurnHistory(older, newer);
  expect(merged.turns.map((turn) => turn.id)).toEqual(["turn-0", "turn-1", "turn-2"]);
  expect(merged.turns[1]?.usage).toEqual({ inputTokens: 500 });
  expect(merged.olderCoverage).toBe(true);
  expect(merged.transcriptOverlap).toBe(true);
});

test("mergeTurnHistory folds matching fresh fragments once and keeps disjoint items", () => {
  const older: TurnModel[] = [
    {
      id: "turn-1",
      status: "completed",
      items: [
        {
          id: "old",
          transcriptKey: "old",
          turnId: "turn-1",
          type: "agentMessage",
          text: "old",
          status: "completed",
        },
      ],
    },
  ];
  const newer: TurnModel[] = [
    {
      id: "turn-1",
      status: "completed",
      items: [
        {
          id: "fresh-a",
          transcriptKey: "fresh-a",
          turnId: "turn-1",
          type: "agentMessage",
          text: "a",
          status: "completed",
        },
      ],
    },
    {
      id: "turn-1",
      status: "completed",
      items: [
        {
          id: "fresh-b",
          transcriptKey: "fresh-b",
          turnId: "turn-1",
          type: "agentMessage",
          text: "b",
          status: "completed",
        },
      ],
    },
  ];

  const merged = mergeTurnHistory(older, newer);
  expect(merged.turns).toHaveLength(1);
  expect(merged.turns[0]?.items.map((item) => item.transcriptKey)).toEqual(["old", "fresh-a", "fresh-b"]);
  expect(merged.olderCoverage).toBe(true);
  expect(merged.transcriptOverlap).toBe(false);
});

test("mergeTurnHistory keeps unproven disjoint turn fragments separate", () => {
  const merged = mergeTurnHistory(
    [
      {
        id: "old-fragment",
        status: "completed",
        items: [
          {
            id: "old-item",
            turnId: "old-fragment",
            type: "agentMessage",
            text: "old",
            status: "completed",
          },
        ],
      },
    ],
    [
      {
        id: "fresh-fragment",
        status: "completed",
        items: [
          {
            id: "fresh-item",
            turnId: "fresh-fragment",
            type: "agentMessage",
            text: "fresh",
            status: "completed",
          },
        ],
      },
    ],
  );

  expect(merged.turns.map((turn) => turn.id)).toEqual(["old-fragment", "fresh-fragment"]);
  expect(merged.olderCoverage).toBe(true);
  expect(merged.transcriptOverlap).toBe(false);
});

test("mergeTurnHistory does not treat retained observation metadata as transcript coverage", () => {
  const older: TurnModel[] = [
    {
      id: "turn-1",
      status: "completed",
      items: [
        {
          id: "item-1",
          turnId: "turn-1",
          type: "reasoning",
          text: "same",
          status: "completed",
          observedStartedAt: "2026-09-18T00:00:01.000Z",
        },
      ],
    },
  ];
  const newer: TurnModel[] = [
    {
      id: "turn-1",
      status: "completed",
      items: [
        {
          id: "item-1",
          turnId: "turn-1",
          type: "reasoning",
          text: "same",
          status: "completed",
        },
      ],
    },
  ];

  const merged = mergeTurnHistory(older, newer);
  expect(merged.olderCoverage).toBe(false);
  expect(merged.transcriptOverlap).toBe(true);
  expect(merged.turns[0]?.items[0]?.observedStartedAt).toBe("2026-09-18T00:00:01.000Z");
});

test("mergeOlderItemPage coalesces every transitively overlapping fragment", () => {
  const model = testHydrate({
    turns: [
      fragmentTurn("fresh-x", ["x"]),
      fragmentTurn("fresh-w", ["w"]),
      fragmentTurn("fresh-zw", ["z", "w"]),
      fragmentTurn("fresh-yz", ["y", "z"]),
      fragmentTurn("fresh-xy", ["x", "y"]),
    ],
  });

  const result = mergeOlderItemPage(model, {
    data: [
      fragmentTurn("old-xp", ["x", "p"]),
      fragmentTurn("old-y", ["y"]),
      fragmentTurn("old-z", ["z"]),
      fragmentTurn("old-w", ["w"]),
    ],
    nextCursor: "cursor_0",
  });

  expect(result.turns).toHaveLength(1);
  expect(result.turns[0]?.items.map((item) => item.id)).toEqual(["x", "p", "y", "z", "w"]);
  expect(new Set(result.turns[0]?.items.map((item) => item.id))).toHaveLength(5);
  expect(result.olderCursor).toBe("cursor_0");
});

test("mergeOlderItemPage coalesces overlapping fresh fragments without an older match", () => {
  const model = testHydrate({
    turns: [
      fragmentTurn("fresh-a", ["x"]),
      fragmentTurn("fresh-b", ["y"]),
      fragmentTurn("fresh-c", ["x"], { usage: { inputTokens: 2 } }),
    ],
  });

  const result = mergeOlderItemPage(model, {
    data: [fragmentTurn("old-z", ["z"])],
  });

  expect(result.turns.map((turn) => turn.id)).toEqual(["old-z", "fresh-c", "fresh-b"]);
  expect(result.turns[1]?.items.map((item) => item.id)).toEqual(["x"]);
  expect(result.turns[1]?.usage).toEqual({ inputTokens: 2 });
});

test("mergeOlderItemPage applies fresh fields after coalescing a fragment chain", () => {
  const model = testHydrate({
    turns: [
      fragmentTurn("fresh-x", ["x"], { usage: { inputTokens: 2 } }),
      fragmentTurn("fresh-xy", ["x", "y"], { usage: { inputTokens: 3 } }),
      fragmentTurn("fresh-y", ["y"], { usage: { inputTokens: 4 } }),
    ],
  });

  const result = mergeOlderItemPage(model, {
    data: [fragmentTurn("old-x", ["x"], { usage: { inputTokens: 1 } }), fragmentTurn("old-y", ["y"])],
  });

  expect(result.turns).toHaveLength(1);
  expect(result.turns[0]).toMatchObject({ id: "fresh-y", usage: { inputTokens: 4 } });
  expect(result.turns[0]?.items.map((item) => item.id).sort()).toEqual(["x", "y"]);
});

test("mergeOlderItemPage places a positioned retained run inside fresh anchors", () => {
  const model = testHydrate({
    turns: [
      positionedFragmentTurn("fresh-a", [["a", 1]]),
      positionedFragmentTurn("fresh-d", [["d", 3]]),
      positionedFragmentTurn("fresh-c", [["c", 4]]),
    ],
  });

  const result = mergeOlderItemPage(model, {
    data: [
      positionedFragmentTurn("old-a", [["a", 1]]),
      positionedFragmentTurn("old-b", [["b", 2]]),
      positionedFragmentTurn("old-c", [["c", 4]]),
    ],
  });

  expect(result.turns.map((turn) => turn.id)).toEqual(["fresh-a", "old-b", "fresh-d", "fresh-c"]);
});

test("mergeOlderItemPage preserves retained prefix and suffix runs", () => {
  const model = testHydrate({
    turns: [positionedFragmentTurn("fresh-a", [["a", 1]]), positionedFragmentTurn("fresh-c", [["c", 4]])],
  });

  const result = mergeOlderItemPage(model, {
    data: [
      positionedFragmentTurn("old-p", [["p", 0]]),
      positionedFragmentTurn("old-a", [["a", 1]]),
      positionedFragmentTurn("old-b", [["b", 2]]),
      positionedFragmentTurn("old-c", [["c", 4]]),
      positionedFragmentTurn("old-s", [["s", 5]]),
    ],
  });

  expect(result.turns.map((turn) => turn.id)).toEqual(["old-p", "fresh-a", "old-b", "fresh-c", "old-s"]);
});

test("mergeOlderItemPage preserves fresh prefix and suffix around a retained run", () => {
  const model = testHydrate({
    turns: [
      positionedFragmentTurn("fresh-x", [["x", 0]]),
      positionedFragmentTurn("fresh-a", [["a", 1]]),
      positionedFragmentTurn("fresh-c", [["c", 4]]),
      positionedFragmentTurn("fresh-y", [["y", 5]]),
    ],
  });

  const result = mergeOlderItemPage(model, {
    data: [
      positionedFragmentTurn("old-a", [["a", 1]]),
      positionedFragmentTurn("old-b", [["b", 2]]),
      positionedFragmentTurn("old-c", [["c", 4]]),
    ],
  });

  expect(result.turns.map((turn) => turn.id)).toEqual(["fresh-x", "fresh-a", "old-b", "fresh-c", "fresh-y"]);
});

test("mergeOlderItemPage retains a warning-only run after a collapsed anchor", () => {
  const model = testHydrate({
    turns: [fragmentTurn("fresh-f", ["a", "c"])],
  });
  const warning = {
    id: "old-b",
    status: "completed" as const,
    itemsView: "fragment" as const,
    items: [
      {
        id: "warning-b",
        turnId: "old-b",
        type: "warning" as const,
        text: "live warning",
        status: "completed" as const,
      },
    ],
  };

  const result = mergeOlderItemPage(model, {
    data: [positionedFragmentTurn("old-a", [["a", 1]]), warning, positionedFragmentTurn("old-c", [["c", 3]])],
  });

  expect(result.turns.map((turn) => turn.id)).toEqual(["fresh-f", "old-b"]);
  expect(result.turns.filter((turn) => turn.id === "old-b")).toHaveLength(1);
  expect(result.turns[1]?.items).toMatchObject([{ id: "warning-b", type: "warning" }]);
});

test("mergeOlderItemPage places an unpositioned warning run with its next positioned turn", () => {
  const model = testHydrate({
    turns: [
      positionedFragmentTurn("fresh-a", [["a", 1]]),
      positionedFragmentTurn("fresh-d", [["d", 3]]),
      positionedFragmentTurn("fresh-c", [["c", 4]]),
    ],
  });
  const warning = {
    id: "old-warning",
    status: "completed" as const,
    itemsView: "fragment" as const,
    items: [
      {
        id: "warning-item",
        turnId: "old-warning",
        type: "warning" as const,
        text: "retained warning",
        status: "completed" as const,
      },
    ],
  };

  const result = mergeOlderItemPage(model, {
    data: [
      positionedFragmentTurn("old-a", [["a", 1]]),
      warning,
      positionedFragmentTurn("old-b", [["b", 2]]),
      positionedFragmentTurn("old-c", [["c", 4]]),
    ],
  });

  expect(result.turns.map((turn) => turn.id)).toEqual(["fresh-a", "old-warning", "old-b", "fresh-d", "fresh-c"]);
});

test.each([
  { gap: "prefix", freshEntry: 1 },
  { gap: "prefix", freshEntry: 3 },
  { gap: "interior", freshEntry: 1 },
  { gap: "interior", freshEntry: 3 },
  { gap: "suffix", freshEntry: 1 },
  { gap: "suffix", freshEntry: 3 },
])(
  "mergeOlderItemPage honors positions past a fresh announcement in a $gap gap at $freshEntry",
  ({ gap, freshEntry }) => {
    const before = gap === "prefix" ? [] : [positionedFragmentTurn("fresh-a", [["a", 0]])];
    const after = gap === "suffix" ? [] : [positionedFragmentTurn("fresh-c", [["c", 4]])];
    const model = testHydrate({
      turns: [...before, positionedFragmentTurn("fresh-d", [["d", freshEntry]]), ...after],
    });
    model.turns.splice(before.length, 0, {
      id: "announcement",
      status: "completed",
      items: [{ id: "u", turnId: "announcement", type: "systemMessage", text: "notice", status: "completed" }],
    });
    const freshIds = model.turns.map((turn) => turn.id);
    const result = mergeOlderItemPage(model, {
      data: [...before, positionedFragmentTurn("old-b", [["b", 2]]), ...after],
    });
    const turnIds = result.turns.map((turn) => turn.id);
    const itemIds = result.turns.flatMap((turn) => turn.items.map((item) => item.id));

    expect(turnIds.filter((id) => freshIds.includes(id))).toEqual(freshIds);
    expect(turnIds.filter((id) => !freshIds.includes(id))).toEqual(["old-b"]);
    expect(itemIds.sort()).toEqual([
      ...(gap === "prefix" ? [] : ["a"]),
      "b",
      ...(gap === "suffix" ? [] : ["c"]),
      "d",
      "u",
    ]);
    if (before.length > 0) expect(turnIds.indexOf("fresh-a")).toBeLessThan(turnIds.indexOf("old-b"));
    if (after.length > 0) expect(turnIds.indexOf("old-b")).toBeLessThan(turnIds.indexOf("fresh-c"));
    if (freshEntry < 2) expect(turnIds.indexOf("fresh-d")).toBeLessThan(turnIds.indexOf("old-b"));
    else expect(turnIds.indexOf("old-b")).toBeLessThan(turnIds.indexOf("fresh-d"));
  },
);

test("mergeOlderItemPage keeps retained turns ordered across interleaved collapsed anchors", () => {
  const model = testHydrate({
    turns: [
      positionedFragmentTurn("fresh-ac", [
        ["a", 1],
        ["c", 4],
      ]),
      positionedFragmentTurn("fresh-bd", [
        ["b", 2],
        ["d", 6],
      ]),
    ],
  });

  const result = mergeOlderItemPage(model, {
    data: [
      positionedFragmentTurn("old-a", [["a", 1]]),
      positionedFragmentTurn("old-b", [["b", 2]]),
      positionedFragmentTurn("old-x", [["x", 3]]),
      positionedFragmentTurn("old-c", [["c", 4]]),
      positionedFragmentTurn("old-y", [["y", 5]]),
      positionedFragmentTurn("old-d", [["d", 6]]),
    ],
  });

  expect(result.turns.map((turn) => turn.id)).toEqual(["fresh-ac", "fresh-bd", "old-x", "old-y"]);
});

test("mergeOlderItemPage uses the earliest compatible successor anchor", () => {
  const model = testHydrate({
    turns: [
      positionedFragmentTurn("fresh-a", [["a", 1]]),
      positionedFragmentTurn("fresh-b", [["b", 4]]),
      positionedFragmentTurn("fresh-c", [["c", 3]]),
    ],
  });

  const result = mergeOlderItemPage(model, {
    data: [
      positionedFragmentTurn("old-a", [["a", 1]]),
      positionedFragmentTurn("old-x", [["x", 2]]),
      positionedFragmentTurn("old-c", [["c", 3]]),
      positionedFragmentTurn("old-b", [["b", 4]]),
    ],
  });

  expect(result.turns.map((turn) => turn.id)).toEqual(["fresh-a", "old-x", "fresh-b", "fresh-c"]);
});

type PlacementMatrixOlderTurn = {
  id: string;
  itemId: string;
  entry?: number;
  warning?: boolean;
};

type PlacementMatrixCase = {
  name: string;
  fresh: Array<[string, Array<[string, number]>]>;
  older: PlacementMatrixOlderTurn[];
  retainedIds: string[];
};

function placementMatrixOlderTurn(spec: PlacementMatrixOlderTurn): Turn {
  if (spec.warning) {
    return {
      id: spec.id,
      status: "completed",
      itemsView: "fragment",
      items: [
        {
          id: spec.itemId,
          turnId: spec.id,
          type: "warning",
          text: "retained warning",
          status: "completed",
        },
      ],
    };
  }
  return spec.entry === undefined
    ? fragmentTurn(spec.id, [spec.itemId])
    : positionedFragmentTurn(spec.id, [[spec.itemId, spec.entry]]);
}

const placementMatrixCases: PlacementMatrixCase[] = [
  {
    name: "crossed partitions with a fresh-only gap turn",
    fresh: [
      [
        "fresh-ac",
        [
          ["a", 1],
          ["c", 4],
        ],
      ],
      ["fresh-only", [["q", 3]]],
      [
        "fresh-bd",
        [
          ["b", 2],
          ["d", 6],
        ],
      ],
    ],
    older: [
      { id: "old-a", itemId: "a", entry: 1 },
      { id: "old-b", itemId: "b", entry: 2 },
      { id: "old-x", itemId: "x", entry: 3 },
      { id: "old-c", itemId: "c", entry: 4 },
      { id: "old-y", itemId: "y", entry: 5 },
      { id: "old-d", itemId: "d", entry: 6 },
    ],
    retainedIds: ["old-x", "old-y"],
  },
  {
    name: "unpositioned warning and positioned retained gaps",
    fresh: [
      [
        "fresh-ac",
        [
          ["a", 1],
          ["c", 4],
        ],
      ],
      [
        "fresh-bd",
        [
          ["b", 2],
          ["d", 6],
        ],
      ],
    ],
    older: [
      { id: "old-a", itemId: "a", entry: 1 },
      { id: "old-warning", itemId: "warning", warning: true },
      { id: "old-b", itemId: "b", entry: 2 },
      { id: "old-x", itemId: "x", entry: 3 },
      { id: "old-c", itemId: "c", entry: 4 },
      { id: "old-y", itemId: "y", entry: 5 },
      { id: "old-d", itemId: "d", entry: 6 },
    ],
    retainedIds: ["old-warning", "old-x", "old-y"],
  },
  {
    name: "two independent crossed partitions",
    fresh: [
      [
        "fresh-ac",
        [
          ["a", 1],
          ["c", 4],
        ],
      ],
      [
        "fresh-bd",
        [
          ["b", 2],
          ["d", 6],
        ],
      ],
      [
        "fresh-eg",
        [
          ["e", 7],
          ["g", 9],
        ],
      ],
      [
        "fresh-fh",
        [
          ["f", 8],
          ["h", 10],
        ],
      ],
    ],
    older: [
      { id: "old-a", itemId: "a", entry: 1 },
      { id: "old-b", itemId: "b", entry: 2 },
      { id: "old-x", itemId: "x", entry: 3 },
      { id: "old-c", itemId: "c", entry: 4 },
      { id: "old-y", itemId: "y", entry: 5 },
      { id: "old-d", itemId: "d", entry: 6 },
      { id: "old-e", itemId: "e", entry: 7 },
      { id: "old-f", itemId: "f", entry: 8 },
      { id: "old-z", itemId: "z", entry: 8.5 },
      { id: "old-g", itemId: "g", entry: 9 },
      { id: "old-w", itemId: "w", entry: 9.5 },
      { id: "old-h", itemId: "h", entry: 10 },
    ],
    retainedIds: ["old-x", "old-y", "old-z", "old-w"],
  },
];

for (const placementCase of placementMatrixCases) {
  test(`mergeOlderItemPage preserves placement invariants for ${placementCase.name}`, () => {
    const model = testHydrate({
      turns: placementCase.fresh.map(([id, items]) => positionedFragmentTurn(id, items)),
    });
    const result = mergeOlderItemPage(model, {
      data: placementCase.older.map(placementMatrixOlderTurn),
    });
    const turnIds = result.turns.map((turn) => turn.id);
    const resultItemIds = result.turns.flatMap((turn) => turn.items.map((item) => item.id));
    const sourceItemIds = [
      ...placementCase.fresh.flatMap(([, items]) => items.map(([itemId]) => itemId)),
      ...placementCase.older.map(({ itemId }) => itemId),
    ];
    const freshIds = placementCase.fresh.map(([id]) => id);
    const resultIndex = new Map(turnIds.map((id, index) => [id, index]));
    const freshGroupByItem = new Map(
      placementCase.fresh.flatMap(([id, items]) => items.map(([itemId]) => [itemId, id] as const)),
    );

    expect(new Set(resultItemIds)).toHaveLength(resultItemIds.length);
    for (const itemId of sourceItemIds) {
      expect(resultItemIds.filter((resultItemId) => resultItemId === itemId)).toHaveLength(1);
    }
    expect(turnIds.filter((id) => freshIds.includes(id))).toEqual(freshIds);
    expect(turnIds.filter((id) => placementCase.retainedIds.includes(id))).toEqual(placementCase.retainedIds);
    for (const retainedId of placementCase.retainedIds) {
      const olderIndex = placementCase.older.findIndex(({ id }) => id === retainedId);
      const retainedIndex = resultIndex.get(retainedId);
      if (olderIndex === -1 || retainedIndex === undefined) continue;
      for (const { itemId } of placementCase.older.slice(0, olderIndex)) {
        const freshId = freshGroupByItem.get(itemId);
        const freshIndex = freshId === undefined ? undefined : resultIndex.get(freshId);
        if (freshIndex !== undefined) expect(freshIndex).toBeLessThan(retainedIndex);
      }
    }
  });
}

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

test("mergeOlderItemPage preserves older settled payload and usage when the current same-key fragment omits them", () => {
  const thread = testThread({
    turns: [
      {
        id: "shared-turn",
        status: "completed",
        itemsView: "fragment",
        items: [
          {
            id: "current-item",
            transcriptKey: "shared-key",
            position: { entry: 4, item: 0 },
            turnId: "shared-turn",
            type: "agentMessage",
            text: "newer text wins",
            status: "completed",
          },
        ],
      },
    ],
  });

  const model = hydrateThread({ thread, olderCursor: "cursor_1" }, thread.evener.ref, 1000);
  const result = mergeOlderItemPage(model, {
    data: [
      {
        id: "shared-turn",
        status: "completed",
        usage: { inputTokens: 11, outputTokens: 7, totalTokens: 18 },
        itemsView: "fragment",
        items: [
          {
            id: "older-item",
            transcriptKey: "shared-key",
            position: { entry: 4, item: 0 },
            turnId: "shared-turn",
            type: "agentMessage",
            text: "older text",
            output: "older output",
            error: "older error",
            exitCode: 3,
            completedAt: 2,
            status: "completed",
          },
        ],
      },
    ],
  });

  expect(result.turns[0]?.usage).toEqual({ inputTokens: 11, outputTokens: 7, totalTokens: 18 });
  expect(result.turns[0]?.items[0]).toMatchObject({
    id: "current-item",
    text: "newer text wins",
    output: "older output",
    error: "older error",
    exitCode: 3,
    completedAt: "1970-01-01T00:00:00.002Z",
    status: "completed",
  });
});

test.each([
  ["omitted text falls back to the older settled text", undefined, "settled text"],
  ["explicit empty text remains empty", "", ""],
  ["explicit nonempty text remains the newer text", "newer text", "newer text"],
])("mergeOlderItemPage preserves same-key text presence semantics: %s", (_case, newerText, expectedText) => {
  const model = hydrateThread(
    {
      thread: testThread({
        turns: [
          {
            id: "shared-turn",
            status: "completed",
            itemsView: "fragment",
            items: [
              {
                id: "newer-item",
                transcriptKey: "shared-key",
                position: { entry: 2, item: 0 },
                turnId: "shared-turn",
                type: "agentMessage",
                ...(newerText === undefined ? {} : { text: newerText }),
                status: "completed",
              },
              {
                id: "newer-after",
                transcriptKey: "after-key",
                position: { entry: 2, item: 1 },
                turnId: "shared-turn",
                type: "agentMessage",
                text: "after",
                status: "completed",
              },
            ],
          },
        ],
      }),
    },
    "ref_t",
    1000,
  );

  const result = mergeOlderItemPage(model, {
    data: [
      {
        id: "shared-turn",
        status: "completed",
        itemsView: "full",
        items: [
          {
            id: "settled-item",
            transcriptKey: "shared-key",
            position: { entry: 2, item: 0 },
            turnId: "shared-turn",
            type: "agentMessage",
            text: "settled text",
            output: "settled output",
            status: "completed",
          },
        ],
      },
    ],
    nextCursor: "cursor_0",
  });

  expect(result.turns[0]?.items.map((item) => item.transcriptKey)).toEqual(["shared-key", "after-key"]);
  expect(result.turns[0]?.items[0]).toMatchObject({
    id: "newer-item",
    text: expectedText,
    output: "settled output",
    position: { entry: 2, item: 0 },
  });
  expect(result.olderCursor).toBe("cursor_0");
});

test("mergeOlderItemPage keeps omitted text absent across repeated same-key merges", () => {
  const model = hydrateThread(
    {
      thread: testThread({
        turns: [
          {
            id: "shared-turn",
            status: "completed",
            itemsView: "fragment",
            items: [{ id: "newer-item", transcriptKey: "shared-key", turnId: "shared-turn", type: "agentMessage" }],
          },
        ],
      }),
    },
    "ref_t",
    1000,
  );
  const page = {
    data: [
      {
        id: "shared-turn",
        status: "completed" as const,
        itemsView: "full" as const,
        items: [
          {
            id: "settled-item",
            transcriptKey: "shared-key",
            turnId: "shared-turn",
            type: "agentMessage" as const,
            text: "settled text",
            status: "completed" as const,
          },
        ],
      },
    ],
    nextCursor: "cursor_0",
  };

  const first = mergeOlderItemPage(model, page);
  const second = mergeOlderItemPage(first, page);

  expect(first.turns[0]?.items[0]?.text).toBe("settled text");
  expect(second.turns[0]?.items[0]?.text).toBe("settled text");
  expect(second.turns[0]?.items).toHaveLength(1);
});

test("an even older omitted fragment does not erase an older provided text", () => {
  const model = hydrateThread(
    {
      thread: testThread({
        turns: [
          {
            id: "shared-turn",
            status: "completed",
            itemsView: "fragment",
            items: [{ id: "newer-item", transcriptKey: "shared-key", turnId: "shared-turn", type: "agentMessage" }],
          },
        ],
      }),
    },
    "ref_t",
    1000,
  );
  const provided = mergeOlderItemPage(model, {
    data: [
      {
        id: "shared-turn",
        status: "completed",
        itemsView: "full",
        items: [
          {
            id: "provided-item",
            transcriptKey: "shared-key",
            turnId: "shared-turn",
            type: "agentMessage",
            text: "A",
          },
        ],
      },
    ],
  });
  const result = mergeOlderItemPage(provided, {
    data: [
      {
        id: "shared-turn",
        status: "completed",
        itemsView: "fragment",
        items: [
          {
            id: "omitted-item",
            transcriptKey: "shared-key",
            turnId: "shared-turn",
            type: "agentMessage",
          },
        ],
      },
    ],
  });

  expect(result.turns[0]?.items[0]?.text).toBe("A");
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

  const model = hydrateThread({ thread, olderCursor: "cursor_1" }, thread.evener.ref, 1000);
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
  const model = hydrateThread({ thread, olderCursor: "cursor_1" }, thread.evener.ref, 1000);
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
  const model = hydrateThread({ thread, olderCursor: "cursor_1" }, thread.evener.ref, 1000);
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

  const model = hydrateThread({ thread, olderCursor: "cursor_1" }, thread.evener.ref, 1000);
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
  const visible = mergeOlderItemPage(model, { data: [resultOnly], nextCursor: "cursor_0" });
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
  let model = testHydrateV6({
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
      method: "history/updated",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        bootGeneration: "",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        items: [
          {
            ...{
              id: "live-id",
              transcriptKey: "same-key",
              position: { entry: 1, item: 1 },
              turnId: "turn_1",
              type: "agentMessage",
              text: "new",
              status: "inProgress",
            },
            turnId: "turn_1",
          },
        ],
      },
    },
    2000,
  );
  expect(model.turns[0]?.items).toHaveLength(1);
  expect(model.turns[0]?.items[0]).toMatchObject({ id: "live-id", text: "new", transcriptKey: "same-key" });
});

test("item/completed settles an existing transcript key despite a different display ID", () => {
  let model = testHydrateV6({
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
      method: "history/updated",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        bootGeneration: "",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        items: [
          {
            ...{
              id: "live-id",
              transcriptKey: "stable-key",
              position: { entry: 1, item: 0 },
              turnId: "turn_1",
              type: "agentMessage",
              text: "settled",
              status: "completed",
            },
            turnId: "turn_1",
          },
        ],
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
  let model = testHydrateV6({
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
      method: "history/updated",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        bootGeneration: "",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        items: [
          {
            ...{ id: "legacy-id", turnId: "turn_1", type: "agentMessage", text: "settled", status: "completed" },
            turnId: "turn_1",
          },
        ],
      },
    },
    2000,
  );
  expect(model.turns[0]?.items).toHaveLength(1);
  expect(model.turns[0]?.items[0]).toMatchObject({ id: "legacy-id", text: "settled", status: "completed" });
});

// askPending is a THREAD-level wire signal (EvenerThread.askPending, mirroring
// the daemon's long-lived HasPendingAsk - "this session is waiting on a human
// answer", agent/session_tools_ask.go). It is wire-authoritative: a wire
// snapshot (hydrateThread) sets it, and thread/status/changed refreshes it under
// the absent-means-no-update rule (#1613 - the pending set clears only at a turn
// boundary, which is when that frame is announced). Nothing else may write it:
// the AskDock derives its OWN, separate in-tool pending signal from ask_user
// items (composer/askDock), so the reducer must NOT recompute this thread field
// from item lifecycle - doing so clobbers the wire's authoritative value
// whenever items churn.
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
    method: "history/updated",
    params: {
      threadId: "thr_t",
      ref: "ref_t",
      bootGeneration: "",
      epoch: 1,
      snapshot: { incarnation: "inc-1", length: 1 },
      turns: [{ id: "turn_1", status: "inProgress", itemsView: "" }],
    },
  };
  const askUser = (_stage: "item/started" | "item/completed", status: string): AnyNotification => ({
    method: "history/updated",
    params: {
      threadId: "thr_t",
      ref: "ref_t",
      bootGeneration: "",
      epoch: 1,
      snapshot: { incarnation: "inc-1", length: 1 },
      items: [
        {
          type: "commandExecution",
          id: "item_ask",
          turnId: "turn_1",
          toolName: "ask_user",
          callId: "call_ask",
          status,
        },
      ],
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
  let model = testHydrateV6();
  model = applyNotification(
    model,
    {
      method: "history/updated",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        bootGeneration: "",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        turns: [{ id: "turn_1", status: "inProgress", itemsView: "" }],
      },
    },
    1001,
  );
  model = applyNotification(
    model,
    {
      method: "history/updated",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        bootGeneration: "",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        items: [
          {
            ...{
              type: "commandExecution",
              id: "item_tool",
              turnId: "turn_1",
              toolName: "ask_user",
              callId: "call_1",
              error: "denied: user rejected",
              status: "completed",
            },
            turnId: "turn_1",
          },
        ],
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
  let model = testHydrateV6();
  model = applyNotification(
    model,
    {
      method: "history/updated",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        bootGeneration: "",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        turns: [{ id: "turn_1", status: "inProgress", itemsView: "" }],
      },
    },
    1001,
  );
  model = applyNotification(
    model,
    {
      method: "history/updated",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        bootGeneration: "",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        items: [
          {
            ...{
              type: "commandExecution",
              id: "item_tool",
              turnId: "turn_1",
              toolName: "shell",
              callId: "call_1",
              output: "boom",
              exitCode: 2,
              status: "completed",
            },
            turnId: "turn_1",
          },
        ],
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

// Warnings reach the model: the reducer's `case "warning"` (see reducer.ts
// for the wire receipts — internal/appprojector/appwire_projection.go's
// EventWarning and EventError's user-cancel branch) folds NotifyWarning
// notifications into the active turn as ordinary items, mirroring
// evener/steering/injected's own shape.

// A hydrated model with one started turn ("turn_1") ready to receive a
// warning notification - the preamble every test below needs before it can
// send its own `warning` params. The "warning" case's gate reads
// model.activeTurnId (the pre-v6 field a live turn/started used to set —
// see model.ts's doc comment on activeTurnId vs runningTurnId), so building
// this straight from hydrate is the port: hydrateThread's own
// activeTurnIdFromThread falls back to a turn already marked inProgress in
// the snapshot, same as the live frame this replaces used to produce.
function warningTurnModel(): ThreadModel {
  return testHydrate({ turns: [{ id: "turn_1", status: "inProgress", itemsView: "", items: [] }] });
}

// Spies on JSON.stringify and records the length of every string it was
// asked to serialize - the pattern several oversized-frame tests below use
// to prove the PRUNE bounds its input before JSON.stringify ever walks it,
// not just the final output. Call `restore()` (in a `finally`) once done.
function recordStringifyOutputLengths(): { lengths: number[]; restore: () => void } {
  const originalStringify = JSON.stringify;
  const lengths: number[] = [];
  const spy = vi.spyOn(JSON, "stringify").mockImplementation((...args: Parameters<typeof JSON.stringify>) => {
    const result = originalStringify(...args);
    if (typeof result === "string") lengths.push(result.length);
    return result;
  });
  return { lengths, restore: () => spy.mockRestore() };
}

test("foldWarningParams scans each stored warning string once", () => {
  const scannedLengths: number[] = [];
  const originalExec = RegExp.prototype.exec;
  const spy = vi.spyOn(RegExp.prototype, "exec").mockImplementation(function (this: RegExp, value: string) {
    if (this.source === "\\S") scannedLengths.push(value.length);
    return originalExec.call(this, value);
  });

  let folded: ReturnType<typeof foldWarningParams>;
  try {
    folded = foldWarningParams({ threadId: "thr_t", ref: "ref_t", title: "Title", hint: "Hint", source: "Source" });
  } finally {
    spy.mockRestore();
  }

  expect(folded).toEqual({ text: "", title: "Title", hint: "Hint", source: "Source" });
  expect([...scannedLengths].sort((a, b) => a - b)).toEqual(
    ["Title".length, "Hint".length, "Source".length].sort((a, b) => a - b),
  );
});

test("warning mid-turn appends an item to the active turn with text=message and the meta populated", () => {
  let model = warningTurnModel();

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

test("a runtime non-string title/hint/source folds to undefined, never a value ItemModel.warning claims is a string", () => {
  let model = warningTurnModel();

  model = applyNotification(
    model,
    {
      method: "warning",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        message: "rate limit approaching",
        // A malformed frame from a producer that doesn't honor the wire's
        // declared string type — the reducer must not carry these through
        // verbatim, since ItemModel.warning.title/hint/source are read as
        // strings by every consumer (WarningItem.tsx renders them as React
        // children).
        source: { nested: "object" } as unknown as string,
        title: { nested: "object" } as unknown as string,
        hint: 42 as unknown as string,
      },
    },
    1002,
  );

  const items = turnAt(model, 0).items;
  expect(items[0]?.warning?.source).toBeUndefined();
  expect(items[0]?.warning?.title).toBeUndefined();
  expect(items[0]?.warning?.hint).toBeUndefined();
});

// The string-or-absent contract means "absent" too, not just "not a
// string": a whitespace-only value is not real content (hasWarningText's
// own reading, which every consumer must apply), so storing it verbatim
// leaves a future reader one missed hasWarningText call away from
// rendering blank content. Normalize at the fold instead.
test("a whitespace-only title/hint/source folds to undefined at the source, not just at each consumer", () => {
  let model = warningTurnModel();

  model = applyNotification(
    model,
    {
      method: "warning",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        message: "rate limit approaching",
        source: "   ",
        title: "   ",
        hint: "\n\t",
      },
    },
    1002,
  );

  const items = turnAt(model, 0).items;
  expect(items[0]?.warning?.source).toBeUndefined();
  expect(items[0]?.warning?.title).toBeUndefined();
  expect(items[0]?.warning?.hint).toBeUndefined();
});

test("two warnings in one turn get distinct ids in arrival order", () => {
  let model = warningTurnModel();

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
  let model = warningTurnModel();
  model = applyNotification(
    model,
    { method: "warning", params: { threadId: "thr_t", ref: "ref_t", message: "mid-turn warning" } },
    1002,
  );

  model = applyNotification(
    model,
    {
      method: "history/updated",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        bootGeneration: "",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        turns: [{ id: "turn_1", status: "completed", itemsView: "" }],
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
  let model = warningTurnModel();

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
    // An empty-string hint is blank, not absent-but-still-a-string — the
    // fold normalizes it to undefined (foldWarningParams), the same
    // string-or-absent reading every consumer already applies via
    // hasWarningText.
    warning: { source: "user", title: "Cancelled", hint: undefined },
  });
});

test("warning with object-form `warning.message` and no top-level message renders that nested message", () => {
  let model = warningTurnModel();

  model = applyNotification(
    model,
    { method: "warning", params: { threadId: "thr_t", ref: "ref_t", warning: { message: "nested warning text" } } },
    1002,
  );

  const item = itemAt(turnAt(model, 0), 0);
  expect(item.text).toBe("nested warning text");
});

test("warning with bare-string `warning` and no top-level message renders that string", () => {
  let model = warningTurnModel();

  model = applyNotification(
    model,
    { method: "warning", params: { threadId: "thr_t", ref: "ref_t", warning: "provider hiccup" } },
    1002,
  );

  const item = itemAt(turnAt(model, 0), 0);
  expect(item.text).toBe("provider hiccup");
});

// A warning frame that carries no message anywhere renders the frame itself,
// not a blank row — the same contract appwire/warning.go's EffectiveMessage/
// DecodeWarningParams enforces server-side (cmd/evener-tui/hub_notifications_test.go's
// "no message anywhere renders the frame itself, not a bare title"): a
// malformed or message-less warning must stay visible, or a producer's typo
// vanishes silently instead of surfacing as the diagnosis it is. Bounded
// because the frame's shape is unknown on the wire and can carry anything.
test.each([
  ["blank string warning", ""],
  ["object warning with no message field", { source: "x" }],
  ["object warning with non-string message", { message: 42 }],
  ["number warning", 42],
])("warning with no message anywhere (%s) renders the frame itself, bounded", (_case, warning) => {
  let model = warningTurnModel();

  const params = { threadId: "thr_t", ref: "ref_t", warning };
  model = applyNotification(model, { method: "warning", params }, 1002);

  const item = itemAt(turnAt(model, 0), 0);
  expect(item.text).toBe(JSON.stringify(params));
});

// A routed message-less frame (threadId/ref present, so
// notificationTargetsThread accepts it) whose only other field is a
// non-string `warning: 42` still renders its own JSON rather than a blank
// item — the same case test.each pins above, kept as its own named test
// since RoboRev's round-20 review of #1580 named this exact shape directly.
// A genuinely routing-less frame (no threadId/ref at all) is dropped by
// notificationTargetsThread before it ever reaches this fold; that is a
// different, untested-here case, not what this test verifies.
test('warning with a routed {"warning":42} frame renders that frame itself', () => {
  let model = warningTurnModel();

  const params = { threadId: "thr_t", ref: "ref_t", warning: 42 };
  model = applyNotification(model, { method: "warning", params }, 1002);

  const item = itemAt(turnAt(model, 0), 0);
  expect(item.text).toBe(JSON.stringify(params));
});

// A raw-frame fallback that is itself oversized must not paste an unbounded
// blob into ItemModel.text: this is package-level code feeding both hosts,
// and neither host's own display bound can be assumed to run before
// something else reads item.text (a test, a notification log). `warning: 42`
// is message-less (warningMessage returns "" for a non-string, non-object
// warning field), so this actually reaches rawWarningFrame — a plain string
// `warning` IS a usable message and would short-circuit before the fallback
// ever runs, making the bound assertion trivially true either way.
test("an oversized warning frame's fallback text stays bounded", () => {
  let model = warningTurnModel();

  const params = { threadId: "thr_t", ref: "ref_t", warning: 42, extra: "x".repeat(10_000) };
  model = applyNotification(model, { method: "warning", params }, 1002);

  const item = itemAt(turnAt(model, 0), 0);
  expect(item.text.length).toBeLessThan(JSON.stringify(params).length);
  expect(item.text.length).toBeGreaterThan(0);
});

// rawWarningFrame's bound must truncate by code point, not by UTF-16 unit: a
// plain String#slice can cut a surrogate pair (an emoji, a codepoint outside
// the BMP - two UTF-16 units) exactly in half, leaving a lone, unpaired
// surrogate at the tail. warning: 42 keeps warningMessage from short-
// circuiting on a usable message, so text falls all the way to
// rawWarningFrame(params) - the JSON.stringify of the whole frame, `extra`
// included.
test("an oversized warning frame's fallback text never splits a surrogate pair at the truncation boundary", () => {
  let model = warningTurnModel();

  const EMOJI = "😀"; // U+1F600: one surrogate pair, two UTF-16 code units.
  const marker = '"extra":"';
  const withoutContent = JSON.stringify({ threadId: "thr_t", ref: "ref_t", warning: 42, extra: "" });
  const contentStart = withoutContent.indexOf(marker) + marker.length;
  // Pad so the emoji's high surrogate lands exactly at index 2000 (0-indexed
  // 1999) of the stringified frame - the byte a naive slice(0, 2000) keeps,
  // cutting the low surrogate that follows.
  const padLen = RAW_WARNING_FRAME_MAX_CHARS - 1 - contentStart;
  const params = { threadId: "thr_t", ref: "ref_t", warning: 42, extra: "a".repeat(padLen) + EMOJI };
  model = applyNotification(model, { method: "warning", params }, 1002);

  const item = itemAt(turnAt(model, 0), 0);
  expect(item.text.length).toBeGreaterThan(0);
  // Bounded by CODE POINTS, not UTF-16 units: keeping the boundary emoji
  // whole can run one code point's worth of extra UTF-16 units past
  // RAW_WARNING_FRAME_MAX_CHARS, which is exactly what must happen instead of splitting it.
  expect(Array.from(item.text).length).toBeLessThanOrEqual(RAW_WARNING_FRAME_MAX_CHARS);
  const lastUnit = item.text.charCodeAt(item.text.length - 1);
  expect(lastUnit >= 0xd800 && lastUnit <= 0xdbff).toBe(false);
});

// prunedForStringify truncates a field's own string value with its own
// UTF-16 slice, one level in from the outer frame's boundedCodePoints - a
// surrogate pair straddling THAT boundary can be split the same way the
// test above closes for the outer frame. The outer frame bound is the SAME
// 2000-code-point cap, so anything the field-level slice mangles near ITS
// own boundary always sits past the frame's own cutoff and would otherwise
// be silently dropped rather than observed - spying on JSON.stringify's
// argument inspects the pruned object BEFORE the outer bound ever runs.
test("prunedForStringify's own field truncation never splits a surrogate pair either", () => {
  let model = warningTurnModel();

  const EMOJI = "😀"; // U+1F600: one surrogate pair, two UTF-16 code units.
  // 1999 'a' code points + one emoji code point = exactly 2000 code points -
  // within the field cap, so a code-point-safe truncation must leave this
  // value untouched. A naive UTF-16 slice(0, 2000) instead cuts at UTF-16
  // index 2000 (extraValue.length is 2001, one past the emoji's high
  // surrogate), splitting the pair.
  const extraValue = "a".repeat(RAW_WARNING_FRAME_MAX_CHARS - 1) + EMOJI;
  const params = { threadId: "thr_t", ref: "ref_t", warning: 42, extra: extraValue };

  const originalStringify = JSON.stringify;
  let prunedArg: { extra?: unknown } | undefined;
  const spy = vi.spyOn(JSON, "stringify").mockImplementation((...args: Parameters<typeof JSON.stringify>) => {
    if (prunedArg === undefined) prunedArg = args[0] as { extra?: unknown };
    return originalStringify(...args);
  });
  try {
    model = applyNotification(model, { method: "warning", params }, 1002);
  } finally {
    spy.mockRestore();
  }

  expect(prunedArg?.extra).toBe(extraValue);
});

// #1731 piece 3 round 2 Low finding: rawWarningFrame must bound its input
// BEFORE expanding to code points, not after — Array.from(JSON.stringify
// (params)) on the whole frame is an O(frame-size) temporary allocation, and
// the transport allows frames up to 128 MiB. Spies on Array.from to prove
// every string it actually expands is already bounded well under the huge
// frame, never the full JSON blob.
test("an oversized warning frame's fallback bounds its input before Array.from, not after", () => {
  let model = warningTurnModel();

  const HUGE = 500_000;
  const params = { threadId: "thr_t", ref: "ref_t", warning: 42, extra: "x".repeat(HUGE) };
  const originalArrayFrom = Array.from;
  const stringArgLengths: number[] = [];
  const spy = vi.spyOn(Array, "from").mockImplementation((...args: unknown[]) => {
    const [input] = args;
    if (typeof input === "string") stringArgLengths.push(input.length);
    // biome-ignore lint/suspicious/noExplicitAny: passes through to the real Array.from, whatever its overload.
    return (originalArrayFrom as (...a: any[]) => unknown[])(...args);
  });

  try {
    model = applyNotification(model, { method: "warning", params }, 1002);
  } finally {
    spy.mockRestore();
  }

  const item = itemAt(turnAt(model, 0), 0);
  expect(Array.from(item.text).length).toBeLessThanOrEqual(RAW_WARNING_FRAME_MAX_CHARS);
  expect(stringArgLengths.length).toBeGreaterThan(0);
  for (const len of stringArgLengths) {
    // Nowhere near the ~500,000-char frame: bounded well before expansion.
    expect(len).toBeLessThan(RAW_WARNING_FRAME_MAX_CHARS * 4);
  }
});

// rawWarningFrame's own JSON.stringify(params) call walks the whole object
// graph before this file gets a chance to slice anything — a transport-sized
// (up to 128 MiB) malformed warning can still make that ONE call allocate
// proportional to the whole frame even though the code-point bound above
// only ever touches its output. The frame must be pruned (strings truncated,
// arrays capped, depth capped) before it reaches JSON.stringify at all.
test("an oversized warning frame's fallback bounds the frame before JSON.stringify walks it, not just its output", () => {
  let model = warningTurnModel();

  const { lengths: outputLengths, restore } = recordStringifyOutputLengths();

  const HUGE = 5_000_000; // a 5 MB single string field
  const params = {
    threadId: "thr_t",
    ref: "ref_t",
    warning: 42,
    extra: "z".repeat(HUGE),
  };
  try {
    model = applyNotification(model, { method: "warning", params }, 1002);
  } finally {
    restore();
  }

  const item = itemAt(turnAt(model, 0), 0);
  expect(item.text.length).toBeGreaterThan(0);
  expect(outputLengths.length).toBeGreaterThan(0);
  for (const len of outputLengths) {
    // Nowhere near the 5 MB field: JSON.stringify itself never walks more
    // than the pruned (small-string, capped-array, depth-capped) frame.
    expect(len).toBeLessThan(10_000);
  }
});

test("an oversized warning frame's fallback prunes deep nesting before JSON.stringify walks it", () => {
  let model = warningTurnModel();

  // A deeply nested structure well past the prune's depth cap — without
  // pruning, JSON.stringify still walks every level.
  let deep: unknown = "leaf";
  for (let i = 0; i < 50; i++) deep = { nested: deep };

  const params = { threadId: "thr_t", ref: "ref_t", warning: 42, extra: deep };
  model = applyNotification(model, { method: "warning", params }, 1002);

  const item = itemAt(turnAt(model, 0), 0);
  expect(item.text.length).toBeGreaterThan(0);
  // The pruned representation replaces anything past the depth cap with a
  // short placeholder, so "leaf" never survives 50 levels of nesting into
  // the bounded output.
  expect(item.text).not.toContain("leaf");
});

// The per-array/per-string/per-depth caps alone leave a gap: many small
// object keys (each individually tiny, each within the array/string/depth
// bounds) can still sum to a huge object for JSON.stringify to walk. The
// prune needs a total node budget too, not just per-level caps.
test("an oversized warning frame's fallback bounds a many-key object, not just deep nesting or long strings", () => {
  let model = warningTurnModel();

  const manyKeys: Record<string, string> = {};
  for (let i = 0; i < 100_000; i++) manyKeys[`key${i}`] = "v";

  const { lengths: outputLengths, restore } = recordStringifyOutputLengths();

  const params = { threadId: "thr_t", ref: "ref_t", warning: 42, extra: manyKeys };
  try {
    model = applyNotification(model, { method: "warning", params }, 1002);
  } finally {
    restore();
  }

  const item = itemAt(turnAt(model, 0), 0);
  expect(item.text.length).toBeGreaterThan(0);
  expect(outputLengths.length).toBeGreaterThan(0);
  for (const len of outputLengths) {
    // 100,000 keys would stringify to well over a megabyte unbounded; the
    // per-object key cap (RAW_WARNING_FRAME_MAX_OBJECT_KEYS, 50) alone
    // already keeps JSON.stringify's own walk small here - this flat,
    // single-level object never grows past 50 nodes, so it says nothing
    // about the total node budget on its own (see the node-budget test
    // below, which reaches the same conclusion via branching instead).
    expect(len).toBeLessThan(10_000);
  }
});

// The per-level caps (RAW_WARNING_FRAME_MAX_OBJECT_KEYS /
// RAW_WARNING_FRAME_MAX_ARRAY_ITEMS, 50 each) bound how many entries survive
// at any ONE level, but say nothing about the total across levels: many
// small objects, each individually within the 50-key cap, can still sum to
// far more than RAW_WARNING_FRAME_MAX_NODES (500) nodes overall. This frame
// stays within every per-level cap at every level (50 keys, each holding a
// 50-key object - 50 + 50*50 = 2,550 nodes) yet only the total node budget
// stops JSON.stringify from walking all of it; the many-key test above
// can't tell the two caps apart, since a single flat level never exceeds 50
// nodes regardless of the total budget.
test("an oversized warning frame's fallback bounds a tree that breaches only the total node budget, not any per-level cap", () => {
  let model = warningTurnModel();

  const branches: Record<string, Record<string, string>> = {};
  for (let i = 0; i < 50; i++) {
    const leaf: Record<string, string> = {};
    for (let j = 0; j < 50; j++) leaf[`leaf${j}`] = "v";
    branches[`branch${i}`] = leaf;
  }

  const { lengths: outputLengths, restore } = recordStringifyOutputLengths();

  const params = { threadId: "thr_t", ref: "ref_t", warning: 42, extra: branches };
  try {
    model = applyNotification(model, { method: "warning", params }, 1002);
  } finally {
    restore();
  }

  const item = itemAt(turnAt(model, 0), 0);
  expect(item.text.length).toBeGreaterThan(0);
  expect(outputLengths.length).toBeGreaterThan(0);
  for (const len of outputLengths) {
    expect(len).toBeLessThan(10_000);
  }
});

// for...in still needs one full key enumeration (ownKeys) — the same O(keys)
// cost Object.entries(value) (or Object.keys) would pay, and the same order
// as the JSON.parse that produced this object in the first place, so this
// loop adds no NEW asymptotic cost there. What it avoids is allocating a
// [key, value] pair PER OWN KEY and reading more values than survive the
// cap: Object.entries reads and copies every value up front, regardless of
// the array's own later .slice(0, 50); this loop counts and breaks. A Proxy
// observes both — one ownKeys call, and every property GET the prune
// actually performs — so the test documents the true bound instead of
// overclaiming that the walk never materializes the key list at all.
test("an oversized warning frame's fallback enumerates keys once and never accesses more than the key cap's worth of property values", () => {
  let model = warningTurnModel();

  const MAX_OBJECT_KEYS = 50; // mirrors reducer.ts's RAW_WARNING_FRAME_MAX_OBJECT_KEYS
  const target: Record<string, string> = {};
  for (let i = 0; i < 100_000; i++) target[`key${i}`] = "v";
  let ownKeysCalls = 0;
  let getCount = 0;
  const observed = new Proxy(target, {
    ownKeys(t) {
      ownKeysCalls++;
      return Reflect.ownKeys(t);
    },
    get(t, prop, receiver) {
      if (typeof prop === "string" && prop.startsWith("key")) getCount++;
      return Reflect.get(t, prop, receiver);
    },
  });

  const params = { threadId: "thr_t", ref: "ref_t", warning: 42, extra: observed };
  model = applyNotification(model, { method: "warning", params }, 1002);

  const item = itemAt(turnAt(model, 0), 0);
  expect(item.text.length).toBeGreaterThan(0);
  // One enumeration of the full key list, no more — the cost the loop
  // cannot avoid, and no worse than a single Object.keys/entries call would
  // cost.
  expect(ownKeysCalls).toBe(1);
  // A small margin above the cap for any incidental re-reads, but nowhere
  // near the 100,000 keys an Object.entries/.keys allocation would touch.
  expect(getCount).toBeLessThanOrEqual(MAX_OBJECT_KEYS + 1);
});

// The object-key COUNT cap (RAW_WARNING_FRAME_MAX_OBJECT_KEYS) bounds how
// many properties survive the prune, but says nothing about how long each
// property NAME is — a single key whose own name is multi-megabyte still
// rides through verbatim into the pruned object and JSON.stringify's walk.
test("an oversized warning frame's fallback bounds an oversized property NAME, not just its value", () => {
  let model = warningTurnModel();

  const HUGE = 5_000_000;
  const params = { threadId: "thr_t", ref: "ref_t", warning: 42, extra: { [`k${"x".repeat(HUGE)}`]: "v" } };

  const { lengths: outputLengths, restore } = recordStringifyOutputLengths();

  try {
    model = applyNotification(model, { method: "warning", params }, 1002);
  } finally {
    restore();
  }

  const item = itemAt(turnAt(model, 0), 0);
  expect(item.text.length).toBeGreaterThan(0);
  expect(outputLengths.length).toBeGreaterThan(0);
  for (const len of outputLengths) {
    expect(len).toBeLessThan(10_000);
  }
});

// JSON.parse creates an own, enumerable property literally named
// "__proto__" (it does not invoke any setter) - the same shape a wire frame
// carrying that field name arrives in after being parsed off the transport.
// Assigning pruned[boundedKey] into a plain `{}` accumulator invokes
// Object.prototype's __proto__ SETTER instead, silently dropping the field
// from the rendered fallback (and repointing the accumulator's own
// prototype, though that has no observable effect here since the result
// only ever reaches JSON.stringify).
test("prunedForStringify preserves a wire key literally named __proto__ instead of setting a prototype", () => {
  let model = warningTurnModel();

  const params = JSON.parse(
    '{"threadId":"thr_t","ref":"ref_t","warning":42,"extra":{"__proto__":{"marker":"present"}}}',
  );
  model = applyNotification(model, { method: "warning", params }, 1002);

  const item = itemAt(turnAt(model, 0), 0);
  expect(item.text).toContain("present");
});

// Two distinct wire keys that share their first RAW_WARNING_FRAME_MAX_CHARS
// (2000) code points truncate to the identical boundedKey ("<2000 shared
// chars>…") - a naive `pruned[boundedKey] = ...` assignment then has the
// second key's value silently overwrite the first's, so one of the two
// fields vanishes from the pruned object even though both survived pruning.
// Spying on JSON.stringify's argument inspects the pruned object BEFORE the
// outer frame bound runs (see the surrogate-pair test above): the two
// truncated keys alone already exceed the frame's own 2000-code-point cap,
// so item.text can never show both regardless of this bug - the loss has to
// be observed one level in, on the object prunedForStringify actually built.
test("prunedForStringify keeps both values when two keys truncate to the same bounded prefix", () => {
  let model = warningTurnModel();

  const sharedPrefix = "x".repeat(RAW_WARNING_FRAME_MAX_CHARS + 1);
  const params = {
    threadId: "thr_t",
    ref: "ref_t",
    warning: 42,
    extra: { [`${sharedPrefix}A`]: "valueA", [`${sharedPrefix}B`]: "valueB" },
  };

  const originalStringify = JSON.stringify;
  let prunedArg: { extra?: Record<string, unknown> } | undefined;
  const spy = vi.spyOn(JSON, "stringify").mockImplementation((...args: Parameters<typeof JSON.stringify>) => {
    if (prunedArg === undefined) prunedArg = args[0] as { extra?: Record<string, unknown> };
    return originalStringify(...args);
  });
  try {
    model = applyNotification(model, { method: "warning", params }, 1002);
  } finally {
    spy.mockRestore();
  }

  expect(Object.values(prunedArg?.extra ?? {})).toEqual(expect.arrayContaining(["valueA", "valueB"]));
});

// Only the message-less raw-frame fallback was bounded; a huge message,
// title, hint, or source string reaches item.text / ItemModel.warning
// verbatim otherwise, leaving the same oversized-frame vector open through
// a different field. Every string the fold puts into the model must be
// bounded, not just the fallback.
test("an oversized message, title, hint, and source are each bounded at the fold", () => {
  let model = warningTurnModel();

  const HUGE = 5_000_000;
  const params = {
    threadId: "thr_t",
    ref: "ref_t",
    message: "m".repeat(HUGE),
    title: "t".repeat(HUGE),
    hint: "h".repeat(HUGE),
    source: "s".repeat(HUGE),
  };
  model = applyNotification(model, { method: "warning", params }, 1002);

  const item = itemAt(turnAt(model, 0), 0);
  expect(item.text.length).toBeGreaterThan(0);
  expect(item.text.length).toBeLessThan(RAW_WARNING_FRAME_MAX_CHARS * 2);
  expect(item.warning?.title?.length).toBeLessThan(RAW_WARNING_FRAME_MAX_CHARS * 2);
  expect(item.warning?.hint?.length).toBeLessThan(RAW_WARNING_FRAME_MAX_CHARS * 2);
  expect(item.warning?.source?.length).toBeLessThan(RAW_WARNING_FRAME_MAX_CHARS * 2);
});

// warningMessage and hasWarningText decide "is there any content here" with
// a regex scan of the raw wire value, never a copy of it (a bounded copy
// then trimmed used to be how this was answered, which was also the source
// of the misclassification the test above closes - the bound ran before the
// scan). Spies on String.prototype.trim to prove hasWarningText no longer
// calls it at all.
test("a huge warning message is never trimmed at full size before it's bounded", () => {
  let model = warningTurnModel();

  const HUGE = 5_000_000;
  const originalTrim = String.prototype.trim;
  const trimmedLengths: number[] = [];
  const spy = vi.spyOn(String.prototype, "trim").mockImplementation(function (this: string) {
    trimmedLengths.push(this.length);
    return originalTrim.call(this);
  });

  const params = { threadId: "thr_t", ref: "ref_t", message: "m".repeat(HUGE) };
  try {
    model = applyNotification(model, { method: "warning", params }, 1002);
  } finally {
    spy.mockRestore();
  }

  const item = itemAt(turnAt(model, 0), 0);
  expect(item.text.length).toBeGreaterThan(0);
  expect(trimmedLengths.length).toBe(0);
});

// hasWarningText bounded its candidate to RAW_WARNING_FRAME_MAX_CHARS code
// points BEFORE checking for non-blank content, so real text starting past
// that bound was invisible to the check - a message with more than 2000
// leading blank code points then real text was misclassified as blank
// (falling back to the raw-frame JSON dump instead of storing the message).
// The stored value keeps the window starting at the message's own first
// non-whitespace code point, not the leading padding, so the real text is
// what item.text ends up holding.
test("a warning message with more than 2000 leading blank code points is not misclassified as blank", () => {
  let model = warningTurnModel();

  const params = { threadId: "thr_t", ref: "ref_t", message: `${" ".repeat(3000)}real content` };
  model = applyNotification(model, { method: "warning", params }, 1002);

  const item = itemAt(turnAt(model, 0), 0);
  expect(item.text).toBe("real content");
});

// hasWarningText detects a title's content anywhere in the string, but the
// stored title used to be bounded from index 0 regardless - a title with
// more than 2000 leading blank code points then real text passed
// hasWarningText yet was stored as nothing but the blank prefix, so
// WarningItem.tsx (which renders item.warning.title verbatim) had nothing
// visible to show. Invariant: stored warning strings are the bounded
// prefix of the CONTENT, never of the padding in front of it.
test("a warning title with more than 2000 leading blank code points renders its real text, not the padding", () => {
  let model = warningTurnModel();

  const params = { threadId: "thr_t", ref: "ref_t", title: `${" ".repeat(3000)}URGENT` };
  model = applyNotification(model, { method: "warning", params }, 1002);

  const item = itemAt(turnAt(model, 0), 0);
  expect(item.warning?.title).toBe("URGENT");
});

// The scan-for-content-then-bound-from-there rule above only has anything to
// skip when the value actually needs truncating - a short title well under
// the bound must survive verbatim, leading whitespace included, since no
// bounding is happening at all.
test("a short warning title keeps its leading whitespace when it's nowhere near the bound", () => {
  let model = warningTurnModel();

  const params = { threadId: "thr_t", ref: "ref_t", title: " URGENT" };
  model = applyNotification(model, { method: "warning", params }, 1002);

  const item = itemAt(turnAt(model, 0), 0);
  expect(item.warning?.title).toBe(" URGENT");
});

// A message-less frame that DOES carry a title or hint is something to show:
// the fold leaves ItemModel.text blank rather than duplicating title/hint
// with the raw JSON envelope (WarningItem.tsx renders title/hint directly;
// mobile's warning row reads them from item.warning when text is blank - see
// mobile/src/conversation/project.ts's warningItem).
test("a message-less warning with a title leaves text blank instead of falling back to the raw frame", () => {
  let model = warningTurnModel();

  const params = { threadId: "thr_t", ref: "ref_t", title: "Sandbox blocked" };
  model = applyNotification(model, { method: "warning", params }, 1002);

  const item = itemAt(turnAt(model, 0), 0);
  expect(item.text).toBe("");
  expect(item.warning?.title).toBe("Sandbox blocked");
});

// Settled tool calls keep their arguments: the live projector's
// EventToolCallEnd (internal/appprojector/appwire_projection.go:414-442)
// resolves argsJSON at :424-427 but uses it only to derive Description —
// the settled ThreadItem it emits carries no ArgumentsJSON, even though the
// streamed item/started item (:373) had it. Historical items DO carry it
// (internal/apptranscript/apptranscript.go:284,312), so this is a
// live-settle-only loss the reducer corrects, mergeReasoning-style.

test("item/completed with its own argumentsJson replaces the old value (wire truth wins)", () => {
  let model = testHydrateV6();
  model = applyNotification(
    model,
    {
      method: "history/updated",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        bootGeneration: "",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        turns: [{ id: "turn_1", status: "inProgress", itemsView: "" }],
      },
    },
    1001,
  );
  model = applyNotification(
    model,
    {
      method: "history/updated",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        bootGeneration: "",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        items: [
          {
            ...{
              type: "commandExecution",
              id: "item_tool",
              turnId: "turn_1",
              toolName: "bash",
              callId: "call_1",
              argumentsJson: '{"command":"ls"}',
              status: "inProgress",
            },
            turnId: "turn_1",
          },
        ],
      },
    },
    1002,
  );

  model = applyNotification(
    model,
    {
      method: "history/updated",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        bootGeneration: "",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        items: [
          {
            ...{
              type: "commandExecution",
              id: "item_tool",
              turnId: "turn_1",
              toolName: "bash",
              callId: "call_1",
              argumentsJson: '{"command":"ls -la"}',
              status: "completed",
            },
            turnId: "turn_1",
          },
        ],
      },
    },
    1003,
  );

  const item = itemAt(turnAt(model, 0), 0);
  expect(item.argumentsJSON).toBe('{"command":"ls -la"}');
});

test("item/completed inserting a never-started item has no argumentsJSON (no crash, no fabrication)", () => {
  let model = testHydrateV6();
  model = applyNotification(
    model,
    {
      method: "history/updated",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        bootGeneration: "",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        turns: [{ id: "turn_1", status: "inProgress", itemsView: "" }],
      },
    },
    1001,
  );

  model = applyNotification(
    model,
    {
      method: "history/updated",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        bootGeneration: "",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        items: [
          {
            ...{ type: "userMessage", id: "item_user", turnId: "turn_1", text: "hi", status: "completed" },
            turnId: "turn_1",
          },
        ],
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

test("hydrateThread defaults the wave 5 snapshot-only fields when thread.evener omits them (old daemon / source-backed thread)", () => {
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
      method: "history/updated",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        bootGeneration: "",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        turns: [{ id: "turn_1", status: "inProgress", itemsView: "" }],
      },
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
      method: "history/updated",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        bootGeneration: "",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        turns: [{ id: "turn_1", status: "inProgress", itemsView: "" }],
      },
    },
    1001,
  );

  model = applyNotification(
    model,
    {
      method: "history/updated",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        bootGeneration: "",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        turns: [{ id: "turn_1", status: "completed", itemsView: "" }],
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
      method: "history/updated",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        bootGeneration: "",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        turns: [{ id: "turn_1", status: "inProgress", itemsView: "" }],
      },
    },
    1001,
  );

  model = applyNotification(
    model,
    {
      method: "history/updated",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        bootGeneration: "",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        items: [
          {
            ...{ type: "userMessage", id: "item_user", turnId: "turn_1", text: "Hi there", status: "completed" },
            turnId: "turn_1",
          },
        ],
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
      method: "history/updated",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        bootGeneration: "",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        turns: [{ id: "turn_1", status: "inProgress", itemsView: "" }],
      },
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
  let model = testHydrate({ turns: [{ id: "turn_1", status: "inProgress", itemsView: "", items: [] }] });
  model = applyNotification(model, retryNotification("turn_1"), 1002);
  expect(model.modelRetry).toBeDefined();
  return model;
}

test("modelRetry survives an assistant message delta", () => {
  let model = withActiveRetry();
  model = applyNotification(
    model,
    {
      method: "history/updated",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        bootGeneration: "",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        items: [
          {
            ...{ type: "systemMessage", id: "item_sys_1", turnId: "turn_1", status: "completed", text: "note" },
            turnId: "turn_1",
          },
        ],
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
      method: "history/updated",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        bootGeneration: "",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        items: [
          {
            ...{ type: "userMessage", id: "item_user_1", turnId: "turn_1", status: "completed", text: "hi" },
            turnId: "turn_1",
          },
        ],
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
      method: "history/updated",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        bootGeneration: "",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        items: [
          { ...{ type: "agentMessage", id: "item_1", turnId: "turn_1", status: "inProgress" }, turnId: "turn_1" },
        ],
      },
    },
    1003,
  );
  model = applyNotification(
    model,
    {
      method: "history/updated",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        bootGeneration: "",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        items: [
          {
            ...{ type: "agentMessage", id: "item_1", turnId: "turn_1", status: "completed", text: "done" },
            turnId: "turn_1",
          },
        ],
      },
    },
    1004,
  );
  expect(model.modelRetry).toBeUndefined();
});

// Deleted: "modelRetry clears when its turn completes" and "modelRetry
// clears when a new turn starts". Both asserted the turnBoundary half of
// modelRetry clearing (turn/completed or turn/started ending the wait
// regardless of output) — the same rule applyNotification's own comment
// documents as dropped (no read-model replacement: a turn boundary carries
// no notification of its own any more), matching the Go TUI port's
// clearModelRetryOnProgress trim (fc12305a41).

// announcementFrame/PLUGIN_LOADED_ITEM/PROMPT_LOADED_ITEM (the no-active-turn
// announcement path's turn/completed fixture) were deleted along with the
// prelude-turn tests that used them — foldNonActiveTurnCompleted, the
// mechanism they exercised, is gone from reducer.ts along with turn/started
// and turn/completed themselves.

// The synthetic prelude turn is never the model's activeTurnId, so a
// live-connected tab used to drop the whole startup burst and only saw the
// "N system events" disclosure once a hydrate/re-subscribe took the snapshot
// path. The authoritative snapshot reduction is the spec here
// (server/appwire_turns.go's ensureTurn/upsertItem): the prelude is created
// at the FRONT — it is the one turn whose id fixes its position — and each
// announcement's item accumulates into it rather than replacing the last.

// Placement is the assertion, not just presence: a live burst must leave the
// same turn order and the same items a hydrate/re-subscribe would have handed
// this client, so the prelude group reads at the top either way.

// A between-turns gap shares the prelude's grouping rationale but not its
// position: it happened AFTER the real turn it follows, so the snapshot
// reduction APPENDS it (only the prelude id front-inserts). The live path
// must place it the same way.

// Accumulation is by ITEM ID, so a redelivered announcement frame — the
// reconnect/hydration replay path hands the reducer frames it may already
// have folded — updates its item in place instead of growing a second copy
// of it (server/appwire_turns.go's upsertItem merges by id for the same
// reason).

// The same-id-replaces-both hazard the active path guards against (see the
// "settles only the FIRST turn" test above) applies to the non-active path
// too: a duplicate id must not let one announcement's settle overwrite an
// unrelated turn's content, silently.

// A genuine turn failure ends as turn/completed{status: "failed", error} and
// is followed by its own status frame: the agent's failure exit
// (agent/session_lifecycle.go endInputAtTurnFailure, kata hen0) emits
// EventSessionEnd with Reason "turn_failed", announced as
// thread/status/changed(idle) with the capabilities inline. Like a completed
// turn's, that frame is the status's authority; the failed stamp settles the
// turn alone.

test("a completed active turn leaves the status to the frame that follows it (inline boundary)", () => {
  const initial = hydrateThread(
    {
      thread: testThread({
        status: { type: "active" },
        evener: { activeTurnId: "turn_1" },
        turns: [{ id: "turn_1", status: "inProgress", itemsView: "full", items: [] }],
      }),
    },
    "ref_t",
    1000,
  );
  const completed = applyNotification(
    initial,
    {
      method: "history/updated",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        bootGeneration: "",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        turns: [{ id: "turn_1", status: "completed", itemsView: "" }],
      },
    },
    2000,
  );
  expect(completed.status.type).toBe("active");
});

// The status is authoritative and the transcript's id can be absent while the
// session is active (a hydrate cut between turns, or the gap after
// turn/completed at an inline boundary). A failed completion arriving then is
// still the session's own failure, but its status frame follows (kata hen0)
// and owns the settle.
test("a failed turn/completed with no active turn id leaves the settle to its status frame", () => {
  const initial = hydrateThread({ thread: testThread({ status: { type: "active" } }) }, "ref_t", 1000);
  expect(initial.activeTurnId).toBeUndefined();
  const failed = applyNotification(
    initial,
    {
      method: "history/updated",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        bootGeneration: "",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        turns: [{ id: "turn_x", status: "failed", itemsView: "", error: { message: "boom" } }],
      },
    },
    2000,
  );
  expect(failed.status.type).toBe("active");

  const settled = applyNotification(
    failed,
    {
      method: "thread/status/changed",
      params: { threadId: "thr_t", ref: "ref_t", status: { type: "idle" }, capabilities: CAPABILITIES },
    },
    3000,
  );
  expect(settled.status.type).toBe("idle");
});

// The work-clock anchor goes with the status, and thread/status/changed drops
// a live anchor on any non-active transition: a hydrate can carry a live
// anchor with no turn id, and StatusRow clocks now-minus-anchor for as long as
// the model holds one. The failed stamp leaves both alone; the status frame
// that follows ends them together.
test("a failed turn/completed with no active turn id leaves the work-clock anchor to its status frame", () => {
  const initial = hydrateThread(
    { thread: testThread({ status: { type: "active" }, evener: { activeTurnStartedAt: 900 } }) },
    "ref_t",
    1000,
  );
  expect(initial.activeTurnId).toBeUndefined();
  expect(initial.activeTurnStartedAt).toBeDefined();
  const failed = applyNotification(
    initial,
    {
      method: "history/updated",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        bootGeneration: "",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        turns: [{ id: "turn_x", status: "failed", itemsView: "", error: { message: "boom" } }],
      },
    },
    2000,
  );
  expect(failed.status.type).toBe("active");
  expect(failed.activeTurnStartedAt).toBeDefined();

  const settled = applyNotification(
    failed,
    {
      method: "thread/status/changed",
      params: { threadId: "thr_t", ref: "ref_t", status: { type: "idle" }, capabilities: CAPABILITIES },
    },
    3000,
  );
  expect(settled.status.type).toBe("idle");
  expect(settled.activeTurnStartedAt).toBeUndefined();
});

// A failed completion for a turn that another turn has since superseded is
// stale bookkeeping about the past, not the session's state.
test("a failed turn/completed for a superseded turn leaves the active session alone", () => {
  const initial = hydrateThread(
    {
      thread: testThread({
        status: { type: "active" },
        evener: { activeTurnId: "turn_2" },
        turns: [
          { id: "turn_1", status: "completed", itemsView: "full", items: [] },
          { id: "turn_2", status: "inProgress", itemsView: "full", items: [] },
        ],
      }),
    },
    "ref_t",
    1000,
  );
  const folded = applyNotification(
    initial,
    {
      method: "history/updated",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        bootGeneration: "",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        turns: [{ id: "turn_1", status: "failed", itemsView: "", error: { message: "late" } }],
      },
    },
    2000,
  );
  expect(folded.status.type).toBe("active");
  expect(folded.activeTurnId).toBe("turn_2");
});

// askPending stays wire-authoritative and the wire can now refresh it: the hub
// stamps the flag on thread/status/changed, which is the frame that goes with
// every clear of the pending set (a resolving user turn, an interrupt), so a
// client stops saying "question waiting" without a reread (#1613). Absent still
// means "no update", so an older hub cannot blank a flag the hydrate gave us.
test("thread/status/changed carries askPending, and absence leaves it alone", () => {
  const asking = testHydrate({
    evener: { ref: "ref_t", capabilities: CAPABILITIES, queue: { revision: 0 }, askPending: true },
  });
  expect(asking.askPending).toBe(true);

  const answered = applyNotification(
    asking,
    {
      method: "thread/status/changed",
      params: { threadId: asking.threadId, ref: "ref_t", status: { type: "active" }, askPending: false },
    } as AnyNotification,
    1_000,
  );
  expect(answered.askPending).toBe(false);

  const olderHub = applyNotification(
    asking,
    {
      method: "thread/status/changed",
      params: { threadId: asking.threadId, ref: "ref_t", status: { type: "active" } },
    } as AnyNotification,
    2_000,
  );
  expect(olderHub.askPending).toBe(true);
});

// The hub says "this item's output images are gone" with an explicit [] on the
// item frame (appwire.ThreadItem.OutputImages is omitzero for exactly this). An
// absent field still means "this frame says nothing", where the other side's
// list is the only one anybody has.
test("an explicit empty outputImages list removes the images an older page still carries", () => {
  const thread = testThread({
    turns: [
      {
        id: "turn_1",
        status: "completed",
        itemsView: "full",
        items: [
          {
            id: "live-k0",
            transcriptKey: "k0",
            position: { entry: 1, item: 1 },
            turnId: "turn_1",
            type: "commandExecution",
            callId: "call_1",
            outputImages: [],
            status: "completed",
          },
        ],
      },
    ],
  });
  const model = hydrateThread({ thread, olderCursor: "cursor_1" }, thread.evener.ref, 1000);
  expect(itemAt(turnAt(model, 0), 0).outputImages).toEqual([]);

  const merged = mergeOlderItemPage(model, {
    data: [
      {
        id: "turn_1",
        status: "completed",
        itemsView: "full",
        items: [
          {
            id: "old-k0",
            transcriptKey: "k0",
            position: { entry: 1, item: 1 },
            turnId: "turn_1",
            type: "commandExecution",
            callId: "call_1",
            outputImages: [{ url: "stale-image", source: "tool-result" }],
            status: "completed",
          },
        ],
      },
    ],
    nextCursor: "cursor_0",
  });
  expect(itemAt(turnAt(merged, 0), 0).outputImages).toEqual([]);
});

test("an absent outputImages field keeps the images the other page carries", () => {
  const thread = testThread({
    turns: [
      {
        id: "turn_1",
        status: "completed",
        itemsView: "full",
        items: [
          {
            id: "live-k0",
            transcriptKey: "k0",
            position: { entry: 1, item: 1 },
            turnId: "turn_1",
            type: "commandExecution",
            callId: "call_1",
            status: "completed",
          },
        ],
      },
    ],
  });
  const model = hydrateThread({ thread, olderCursor: "cursor_1" }, thread.evener.ref, 1000);
  expect(itemAt(turnAt(model, 0), 0).outputImages).toBeUndefined();

  const merged = mergeOlderItemPage(model, {
    data: [
      {
        id: "turn_1",
        status: "completed",
        itemsView: "full",
        items: [
          {
            id: "old-k0",
            transcriptKey: "k0",
            position: { entry: 1, item: 1 },
            turnId: "turn_1",
            type: "commandExecution",
            callId: "call_1",
            outputImages: [{ url: "kept-image", source: "tool-result" }],
            status: "completed",
          },
        ],
      },
    ],
    nextCursor: "cursor_0",
  });
  expect(itemAt(turnAt(merged, 0), 0).outputImages).toEqual([{ src: "kept-image", source: "tool-result" }]);
});

// Output images: see appwire.MergeOutputImages; input images: see appwire.MergeInputImages.
test("an empty input images list never erases the images an older page carries", () => {
  const thread = testThread({
    turns: [
      {
        id: "turn_1",
        status: "completed",
        itemsView: "full",
        items: [
          {
            id: "live-k0",
            transcriptKey: "k0",
            position: { entry: 1, item: 1 },
            turnId: "turn_1",
            type: "userMessage",
            text: "look at this",
            images: [],
            status: "completed",
          },
        ],
      },
    ],
  });
  const model = hydrateThread({ thread, olderCursor: "cursor_1" }, thread.evener.ref, 1000);

  const merged = mergeOlderItemPage(model, {
    data: [
      {
        id: "turn_1",
        status: "completed",
        itemsView: "full",
        items: [
          {
            id: "old-k0",
            transcriptKey: "k0",
            position: { entry: 1, item: 1 },
            turnId: "turn_1",
            type: "userMessage",
            text: "look at this",
            images: [{ type: "image", mediaType: "image/png", data: "aGk=", name: "shot.png" }],
            status: "completed",
          },
        ],
      },
    ],
    nextCursor: "cursor_0",
  });
  const images = itemAt(turnAt(merged, 0), 0).images;
  expect(images).toHaveLength(1);
});

// The other direction of the same selector, and the reason the preserve cases
// above cannot stand alone: a settle that DOES carry images replaces the ones
// the item had. Reverse mergeItemImages' `??` (`existing.images ??
// settled.images`) and every "keeps ..." assertion above stays green while a
// stale attachment silently wins — this case is what turns that red.
test("item/completed carrying a different images list overrides the images the item already had", () => {
  let model = testHydrateV6();
  model = applyNotification(
    model,
    {
      method: "history/updated",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        bootGeneration: "",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        turns: [{ id: "turn_1", status: "inProgress", itemsView: "" }],
      },
    },
    1001,
  );
  model = applyNotification(
    model,
    {
      method: "history/updated",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        bootGeneration: "",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        items: [
          {
            ...{
              type: "userMessage",
              id: "item_user",
              turnId: "turn_1",
              text: "look",
              status: "completed",
              images: [{ type: "image", mediaType: "image/png", data: "iVBORw0KGgo=", name: "shot.png" }],
            },
            turnId: "turn_1",
          },
        ],
      },
    } as AnyNotification,
    1002,
  );
  expect(itemAt(turnAt(model, 0), 0).images).toEqual([{ src: "data:image/png;base64,iVBORw0KGgo=", name: "shot.png" }]);

  // Same item, and this settle's own image is the one that must win.
  model = applyNotification(
    model,
    {
      method: "history/updated",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        bootGeneration: "",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        items: [
          {
            ...{
              type: "userMessage",
              id: "item_user",
              turnId: "turn_1",
              text: "look",
              status: "completed",
              images: [{ type: "image", mediaType: "image/png", data: "BAUG", name: "new.png" }],
            },
            turnId: "turn_1",
          },
        ],
      },
    } as AnyNotification,
    1003,
  );
  expect(itemAt(turnAt(model, 0), 0).images).toEqual([{ src: "data:image/png;base64,BAUG", name: "new.png" }]);
});

test("item/completed carrying a different outputImages list overrides the output images the item already had", () => {
  let model = testHydrateV6();
  model = applyNotification(
    model,
    {
      method: "history/updated",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        bootGeneration: "",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        turns: [{ id: "turn_1", status: "inProgress", itemsView: "" }],
      },
    },
    1001,
  );
  model = applyNotification(
    model,
    {
      method: "history/updated",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        bootGeneration: "",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        items: [
          {
            ...{
              type: "commandExecution",
              id: "item_tool",
              turnId: "turn_1",
              toolName: "shell",
              callId: "call_1",
              status: "completed",
              outputImages: [{ source: "written-file", name: "plot.png", path: "out/plot.png" }],
            },
            turnId: "turn_1",
          },
        ],
      },
    } as AnyNotification,
    1002,
  );
  expect(itemAt(turnAt(model, 0), 0).outputImages).toEqual([
    { src: "out/plot.png", name: "plot.png", path: "out/plot.png", source: "written-file" },
  ]);

  model = applyNotification(
    model,
    {
      method: "history/updated",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        bootGeneration: "",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        items: [
          {
            ...{
              type: "commandExecution",
              id: "item_tool",
              turnId: "turn_1",
              toolName: "shell",
              callId: "call_1",
              status: "completed",
              outputImages: [{ source: "tool-result", url: "/s/sess_t/images/capture", name: "capture.png" }],
            },
            turnId: "turn_1",
          },
        ],
      },
    } as AnyNotification,
    1003,
  );
  expect(itemAt(turnAt(model, 0), 0).outputImages).toEqual([
    { src: "/s/sess_t/images/capture", name: "capture.png", source: "tool-result" },
  ]);
});

// The removal on the settle path, which mergeItemImages owns: a later
// item/completed carrying an explicit empty outputImages list clears the images
// an earlier frame set, because that [] is the hub's only way to say the
// pictures are gone (appwire.ThreadItem.OutputImages is omitzero for exactly
// this). A settle that carries no field at all still keeps them — the two cases
// this merge has to tell apart.
test("item/completed carrying an explicit empty outputImages list clears the images a prior frame set", () => {
  const settle = (outputImages: unknown, at: number, model: ThreadModel): ThreadModel =>
    applyNotification(
      model,
      {
        method: "history/updated",
        params: {
          threadId: "thr_t",
          ref: "ref_t",
          bootGeneration: "",
          epoch: 1,
          snapshot: { incarnation: "inc-1", length: 1 },
          items: [
            {
              ...{
                type: "commandExecution",
                id: "item_tool",
                turnId: "turn_1",
                toolName: "shell",
                callId: "call_1",
                status: "completed",
                ...(outputImages === undefined ? {} : { outputImages }),
              },
              turnId: "turn_1",
            },
          ],
        },
      } as AnyNotification,
      at,
    );

  let model = testHydrateV6();
  model = applyNotification(
    model,
    {
      method: "history/updated",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        bootGeneration: "",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        turns: [{ id: "turn_1", status: "inProgress", itemsView: "" }],
      },
    },
    1001,
  );
  model = settle([{ source: "written-file", name: "plot.png", path: "out/plot.png" }], 1002, model);
  expect(itemAt(turnAt(model, 0), 0).outputImages).toEqual([
    { src: "out/plot.png", name: "plot.png", path: "out/plot.png", source: "written-file" },
  ]);

  // Deleted here: "a settle that says nothing keeps them" (mergeItemImages'
  // omission-preserves-existing rule) — mergeItemImages is gone with
  // item/completed; history/updated's mergeHistory replaces the item
  // wholesale, so an incoming record that omits outputImages now genuinely
  // clears it too, same open question as chunkview.test.ts's/
  // liveImages.test.ts's flagged images-preservation gap.

  // An explicit empty list removes them.
  model = settle([], 1004, model);
  expect(itemAt(turnAt(model, 0), 0).outputImages).toEqual([]);
});

// applyNotification is generic over the model: a caller whose model is a
// ThreadModel plus its own fields (native's MobileConversation = ThreadModel
// & { items }, say) gets that SAME type back, extra fields intact — at runtime
// and in the type. The WrappedModel annotations are the compile-time half of
// this test: if applyNotification returned ThreadModel again, these assignments
// would not typecheck, and `npm run typecheck` (the frontend's tsc program,
// which includes the package's test files) is what fails then.
type WrappedModel = ThreadModel & { wrapperMarker: number };

test("applyNotification keeps a wrapper model's extra fields and type through every fold shape", () => {
  // snapshot establishes versioned history at hydrate — the read model's own
  // bootstrap rule (reducer.ts's classifySignal): the "cleared" step below
  // folds a live history/updated, which can only ever merge once an
  // authoritative read has first established an incarnation to merge
  // against.
  const wrapped: WrappedModel = {
    ...hydrateThread(
      {
        thread: testThread({ turns: [{ id: "turn_1", status: "inProgress", itemsView: "", items: [] }] }),
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
      },
      "ref_t",
      1000,
    ),
    wrapperMarker: 7,
  };

  // thread/status/changed builds its result from `{ ...model, ... }` — the
  // case the wrapper used to need a cast for. turn/started's own activeTurnId
  // job moved here (model.ts's doc comment on activeTurnId vs
  // runningTurnId): thread/status/changed sets runningTurnId, the live field.
  const started: WrappedModel = applyNotification(
    wrapped,
    {
      method: "thread/status/changed",
      params: { threadId: "thr_t", ref: "ref_t", status: { type: "active" }, activeTurnId: "turn_1" },
    },
    1001,
  );
  expect(started.wrapperMarker).toBe(7);
  expect(started.runningTurnId).toBe("turn_1");

  // A scalar patch spreads the same way.
  const status: WrappedModel = applyNotification(
    started,
    { method: "thread/status/changed", params: { threadId: "thr_t", ref: "ref_t", status: { type: "active" } } },
    1002,
  );
  expect(status.wrapperMarker).toBe(7);
  expect(status.status).toEqual({ type: "active" });

  // A frame for another thread is the same-reference no-op; the extra field
  // rides along because the object is the same one.
  const untouched: WrappedModel = applyNotification(
    status,
    { method: "thread/status/changed", params: { threadId: "thr_other", ref: "ref_other", status: { type: "idle" } } },
    1003,
  );
  expect(untouched).toBe(status);
  expect(untouched.wrapperMarker).toBe(7);

  // The modelRetry-clearing branch rebuilds the model without its retry field;
  // the wrapper field survives that rebuild too, and the cleared key is gone
  // rather than present-and-undefined.
  const retrying: WrappedModel = applyNotification(
    started,
    {
      method: "evener/thread/modelRetry",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        attempt: 1,
        maxAttempts: 3,
        delayMs: 1000,
        errorClass: "rate_limit",
        statusCode: 429,
        groupElapsedMs: 500,
        attemptCap: 3,
      },
    },
    1004,
  );
  expect(retrying.modelRetry).toBeDefined();
  const cleared: WrappedModel = applyNotification(
    retrying,
    {
      method: "history/updated",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        bootGeneration: "",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        items: [
          {
            ...{ type: "agentMessage", id: "item_1", turnId: "turn_1", status: "completed", text: "done" },
            turnId: "turn_1",
          },
        ],
      },
    },
    1005,
  );
  expect(cleared.wrapperMarker).toBe(7);
  expect(cleared.modelRetry).toBeUndefined();
  expect("modelRetry" in cleared).toBe(false);
});

// The generic preserves the caller's OWN extra fields, not a narrowing of
// ThreadModel's. The reducer owns and rewrites lastFrameAt/modelRetry/status,
// so its return types those as ThreadModel's — a caller that intersects one to
// a narrower type must not read the narrowed type back off the result.
type NarrowedRetryModel = ThreadModel & { modelRetry: NonNullable<ThreadModel["modelRetry"]> };

test("applyNotification returns ThreadModel's type for the fields the fold owns, not the caller's narrowing", () => {
  // snapshot establishes versioned history at hydrate (classifySignal's
  // bootstrap rule) — required for the history/updated fold below to merge
  // rather than invalidate.
  const narrowed: NarrowedRetryModel = {
    ...hydrateThread(
      {
        thread: testThread({ turns: [{ id: "turn_1", status: "inProgress", itemsView: "", items: [] }] }),
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
      },
      "ref_t",
      1000,
    ),
    modelRetry: {
      attempt: 1,
      maxAttempts: 3,
      delayMs: 1000,
      groupElapsedMs: 0,
      attemptCap: 3,
      receivedAt: 1000,
    },
  };
  // A model-output item (agentMessage) completing is the surviving
  // modelRetry-clearing rule (the turn-boundary half is dead — see the
  // deleted "modelRetry clears when..." tests above).
  const folded = applyNotification(
    narrowed,
    {
      method: "history/updated",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        bootGeneration: "",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        items: [{ type: "agentMessage", id: "item_1", turnId: "turn_1", status: "completed", text: "done" }],
      },
    },
    1001,
  );
  // @ts-expect-error the fold owns modelRetry: the return types it as ThreadModel["modelRetry"] (possibly undefined), not the caller's required narrowing.
  const stillRequired: NonNullable<ThreadModel["modelRetry"]> = folded.modelRetry;
  expect(stillRequired).toBeUndefined();
});

// ModelExtras must DISTRIBUTE over a union M. A plain Omit collapses
// Omit<A | B, keyof ThreadModel> to the members' common keys, so the return
// would degrade to bare ThreadModel and drop each member's own extra field.
type WrappedA = ThreadModel & { extraA: number };
type WrappedB = ThreadModel & { extraB: string };

// Widening through a declared union return defeats control-flow narrowing:
// `const u: WrappedA | WrappedB = <a WrappedA literal>` is narrowed to
// WrappedA at its use, so the argument would never be the union the review
// asked about.
function asUnion(model: WrappedA): WrappedA | WrappedB {
  return model;
}

test("applyNotification distributes a union model's extra fields member by member", () => {
  const folded = applyNotification(
    asUnion({ ...testHydrate(), extraA: 1 }),
    {
      method: "history/updated",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        bootGeneration: "",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        turns: [{ id: "turn_1", status: "inProgress", itemsView: "" }],
      },
    },
    1001,
  );
  // The compile-time half: the return is
  // (ThreadModel & { extraA }) | (ThreadModel & { extraB }), so it is assignable
  // to that distributive union. A non-distributive Omit types it as bare
  // ThreadModel and this assignment does not typecheck.
  const distributed: (ThreadModel & { extraA: number }) | (ThreadModel & { extraB: string }) = folded;
  expect(distributed).toBe(folded);
  expect("extraA" in folded && folded.extraA).toBe(1);
});
