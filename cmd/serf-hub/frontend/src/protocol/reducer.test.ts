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
  collectAuthoritativeMutationIds,
  hydrateThread,
  notificationTargetsThread,
  prependOlderTurns,
  resolvePendingEscalation,
} from "./reducer";
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
  queue: true,
  goal: true,
  rename: true,
};

type TestThreadOverrides = Omit<Partial<Thread>, "serf"> & {
  serf?: Omit<Thread["serf"], "queue"> & { queue: Partial<QueueState> };
};

function testThread(overrides: TestThreadOverrides = {}): Thread {
  const { serf, ...threadOverrides } = overrides;
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
    source: "serf",
    serf: {
      ref: "ref_t",
      capabilities: CAPABILITIES,
      ...serf,
      queue: { revision: 0, ...serf?.queue },
    },
    ...threadOverrides,
  };
}

function testHydrate(overrides: TestThreadOverrides = {}): ThreadModel {
  const thread = testThread(overrides);
  return hydrateThread({ thread }, thread.serf.ref, 1000);
}

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
    serf: { ref: "ref_t", capabilities: CAPABILITIES, queue: { revision: 0 }, activeTurnStartedAt: 0 },
  });
  expect(zero.activeTurnStartedAt).toBeUndefined();
  const negative = testHydrate({
    serf: { ref: "ref_t", capabilities: CAPABILITIES, queue: { revision: 0 }, activeTurnStartedAt: -1 },
  });
  expect(negative.activeTurnStartedAt).toBeUndefined();
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
  // serf/auth/updated is a real, known NotificationName the reducer does not
  // model any state for (ThreadModel has no auth-provider fields); it also
  // carries neither ref nor threadId, so it can never target a thread.
  const model = testHydrate();
  const result = applyNotification(model, { method: "serf/auth/updated", params: {} }, 2000);
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
      { method: "serf/task/updated", params: { threadId: "thr_t", ref: "ref_t", total: 1, done: 0 } },
      model,
    ),
  ).toBe(true);
  expect(
    notificationTargetsThread(
      { method: "serf/task/updated", params: { threadId: "not_thr_t", ref: "not_ref_t", total: 1, done: 0 } },
      model,
    ),
  ).toBe(false);
  // Neither field present (e.g. serf/auth/updated) targets no thread model.
  expect(notificationTargetsThread({ method: "serf/auth/updated", params: {} }, model)).toBe(false);
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
    serf: { ref: "ref_a", capabilities: CAPABILITIES, queue: { revision: 0 } },
  });
  let modelA = hydrateThread({ thread: threadA }, threadA.serf.ref, 1000);
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
    serf: { ref: "ref_b", capabilities: CAPABILITIES, queue: { revision: 0 } },
    turns: [
      {
        id: "turn_1",
        status: "completed",
        itemsView: "full",
        items: [{ type: "agentMessage", id: "item_b1", turnId: "turn_1", text: "B's own answer", status: "completed" }],
      },
    ],
  });
  const modelB = hydrateThread({ thread: threadB }, threadB.serf.ref, 1000);
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
  let model = hydrateThread({ thread }, thread.serf.ref, 1000);
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

// Part B regression coverage: serf/steering/injected's payload is declared
// `nil` in the AppWire catalog, but the live projector
// (internal/appprojector/appwire_projection.go:573-593) actually sends
// {threadId, ref, text, images, source?} — source present ("user") only for
// human-sent steers, omitted for daemon-originated ones. A live steer into
// an in-flight turn must become a "steering" transcript item, mirroring how
// reload already renders persisted steering turns
// (internal/apptranscript/apptranscript.go:211-229).

test('serf/steering/injected with source "user" appends a steering item to the active turn', () => {
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
      method: "serf/steering/injected",
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

test("serf/steering/injected with no source field appends an item with source undefined", () => {
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
      method: "serf/steering/injected",
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
    { method: "serf/steering/injected", params: { threadId: "thr_t", ref: "ref_t", text: "first" } },
    1002,
  );
  model = applyNotification(
    model,
    {
      method: "serf/steering/injected",
      params: { threadId: "thr_t", ref: "ref_t", text: "second" },
    },
    1003,
  );

  const items = turnAt(model, 0).items;
  expect(items.map((it) => it.id)).toEqual(["item_steering_live_turn_1_0", "item_steering_live_turn_1_1"]);
  expect(items.map((it) => it.text)).toEqual(["first", "second"]);
});

test("serf/steering/injected with no active turn only updates lastFrameAt (no turn fabricated client-side)", () => {
  const model = testHydrate();
  expect(model.activeTurnId).toBeUndefined();

  const result = applyNotification(
    model,
    {
      method: "serf/steering/injected",
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
      method: "serf/steering/injected",
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

test("serf/steering/injected images populate display-ready ItemImages via the same conversion other item paths use", () => {
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
      method: "serf/steering/injected",
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
// the wire at each injection site, through to SerfSteeringInjectedParams.kind
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
      method: "serf/steering/injected",
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
      method: "serf/steering/injected",
      params: { threadId: "thr_t", ref: "ref_t", text: "something unclassified" },
    },
    1002,
  );

  expect(itemAt(turnAt(model, 0), 0).steeringKind).toBeUndefined();
});

test("prependOlderTurns keeps order and advances olderCursor", () => {
  const thread = testThread({ turns: [{ id: "turn_2", status: "completed", itemsView: "full", items: [] }] });
  const model = hydrateThread({ thread, olderCursor: "cursor_1" }, thread.serf.ref, 1000);

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
  const model = hydrateThread({ thread, olderCursor: "cursor_1" }, thread.serf.ref, 1000);

  const resp = { nextCursor: "cursor_0" } as unknown as ThreadTurnsListResponse;
  const result = prependOlderTurns(model, resp);

  expect(result.turns.map((t) => t.id)).toEqual(["turn_2"]);
  expect(result.olderCursor).toBe("cursor_0");
});

// askPending is a THREAD-level wire signal (SerfThread.askPending, mirroring
// the daemon's long-lived HasPendingAsk - "this session is waiting on a human
// answer", agent/session_tools_ask.go). It is snapshot-authoritative: only a
// wire snapshot (hydrateThread) sets it; no notification carries it (askPending
// appears only on SerfThread in types.gen.ts). The AskDock derives its OWN,
// separate in-tool pending signal from ask_user items (composer/askDock), so
// the reducer must NOT recompute this thread field from item lifecycle - doing
// so clobbers the wire's authoritative value whenever items churn.
test("askPending is wire-authoritative from the thread snapshot", () => {
  const asking = testHydrate({
    serf: { ref: "ref_t", capabilities: CAPABILITIES, queue: { revision: 0 }, askPending: true },
  });
  expect(asking.askPending).toBe(true);

  const notAsking = testHydrate({
    serf: { ref: "ref_t", capabilities: CAPABILITIES, queue: { revision: 0 }, askPending: false },
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
    serf: { ref: "ref_t", capabilities: CAPABILITIES, queue: { revision: 0 }, askPending: true },
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
  const model = hydrateThread({ thread }, thread.serf.ref, 1000);
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
  const model = hydrateThread({ thread }, thread.serf.ref, 1000);
  // A real typed 0 must round-trip as 0, never collapse to undefined — the
  // descriptor distinguishes "ran, exit 0" from "no code (backgrounded)".
  expect(itemAt(turnAt(model, 0), 0).exitCode).toBe(0);
});

// A tool-call's purpose crosses the wire as ThreadItem.description (set
// server-side, e.g. delegate's mandate); wireItemToModel historically dropped
// it. The model must carry it so the subagent Activity feed can render each
// child tool-call's purpose (§4.2). Both hydrate and live paths fold through
// wireItemToModel, so the snapshot path proves the carry.
test("wireItemToModel carries the wire description (tool-call purpose) onto the item", () => {
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
  const model = hydrateThread({ thread }, thread.serf.ref, 1000);
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
            text: "You are Serf.",
            eventKind: "system_prompt",
            status: "completed",
          },
        ],
      },
    ],
  });
  const model = hydrateThread({ thread }, thread.serf.ref, 1000);
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
  const model = hydrateThread({ thread }, thread.serf.ref, 1000);
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
  const model = hydrateThread({ thread }, thread.serf.ref, 1000);
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
  const model = hydrateThread({ thread }, thread.serf.ref, 0);

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
    serf: {
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
  const model = hydrateThread({ thread }, thread.serf.ref, 1000);

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
// serf/steering/injected's own shape.

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

// pendingEscalations (M7): appwire/types.go's ThreadSerf.PendingEscalations
// doc comment calls it the "surface-on-entry snapshot ... so a client
// entering / reconnecting to / not-having-seen-live this session surfaces
// the card(s)" and rules it a HUMAN-CLIENT field only, never part of the
// transcript. hydrateThread must therefore carry it verbatim (or default it
// to [], per the Go wire-nullable-array rule: omitempty absent means empty)
// as a THREAD-level ThreadModel field, not a turn item.

test("hydrateThread maps serf.pendingEscalations verbatim into pendingEscalations", () => {
  const escalation = testEscalation();
  const model = testHydrate({
    serf: { ref: "ref_t", capabilities: CAPABILITIES, queue: { revision: 0 }, pendingEscalations: [escalation] },
  });
  expect(model.pendingEscalations).toEqual([escalation]);
});

test("hydrateThread defaults pendingEscalations to an empty array when serf.pendingEscalations is absent", () => {
  const model = testHydrate();
  expect(model.pendingEscalations).toEqual([]);
});

// SerfThread.Cost is the session-level estimated dollar total (the sibling of
// per-turn Turn.Cost), snapshot-authoritative like usage/workMillis: only a
// wire snapshot (hydrateThread) sets it, and everything else preserves it via
// the reducer's ...model spread. It is null when the daemon omits it (no usage,
// or an uncataloged model) — an honest "unknown" the status row renders as no
// chip, never a misleading ~$0.00.
test("hydrateThread maps serf.cost into the model, null when absent", () => {
  const withCost = testHydrate({
    serf: { ref: "ref_t", capabilities: CAPABILITIES, queue: { revision: 0 }, cost: "~$1.23" },
  });
  expect(withCost.cost).toBe("~$1.23");

  expect(testHydrate().cost).toBeNull();
});

test("hydrateThread preserves task aggregate through notification mutation and reconnect rehydrate", () => {
  const snapshot = {
    ref: "ref_t",
    capabilities: CAPABILITIES,
    queue: { revision: 0 },
    tasks: { total: 7, done: 6 },
  };
  let model = testHydrate({ serf: snapshot });
  expect(model.tasks).toEqual({ total: 7, done: 6 });

  model = applyNotification(
    model,
    { method: "serf/task/updated", params: { threadId: "thr_t", ref: "ref_t", total: 7, done: 7 } },
    2000,
  );
  expect(model.tasks).toEqual({ total: 7, done: 7 });

  const rehydrated = testHydrate({
    serf: {
      ref: "ref_t",
      capabilities: CAPABILITIES,
      queue: { revision: 0 },
      tasks: { total: 7, done: 7 },
    },
  });
  expect(rehydrated.tasks).toEqual({ total: 7, done: 7 });
});

test("hydrateThread keeps absent task aggregate null and distinguishes an authoritative zero", () => {
  expect(testHydrate().tasks).toBeNull();
  expect(
    testHydrate({
      serf: { ref: "ref_t", capabilities: CAPABILITIES, queue: { revision: 0 }, tasks: { total: 0, done: 0 } },
    }).tasks,
  ).toEqual({ total: 0, done: 0 });
});

test("serf/job/updated bumps jobsUpdatedAt for the targeted thread", () => {
  let model = testHydrate();
  expect(model.jobsUpdatedAt).toBeNull();
  model = applyNotification(
    model,
    { method: "serf/job/updated", params: { threadId: "thr_t", ref: "ref_t", jobId: "job_1", status: "running" } },
    2000,
  );
  expect(model.jobsUpdatedAt).toBe(2000);
  expect(model.lastFrameAt).toBe(2000);
});

test("serf/job/updated for another thread leaves the model untouched", () => {
  const model = testHydrate();
  expect(
    applyNotification(
      model,
      {
        method: "serf/job/updated",
        params: { threadId: "not_thr_t", ref: "not_ref_t", jobId: "job_1", status: "running" },
      },
      2000,
    ),
  ).toBe(model);
});

// Wave 5 T1: ThreadModel gains capabilities/goal/context*/usage/workMillis/
// activeTurnStartedAt/reasoningEffortLevels/supportsReasoning, all hydrated
// from thread.serf (appwire/types.go's SerfThread, lines 223-274). None of
// these except reasoningEffortLevels/supportsReasoning (via
// thread/model/changed, tested above) and reasoningEffort (via
// thread/reasoning-effort/changed, tested above) ever get a live push - see
// SerfThread's own doc comment ("read on demand ... rather than pushed on
// every event") and appwire/protocol.go's Notifications catalog, which has
// no capabilities/goal/context/usage entry at all.
test("hydrateThread maps capabilities/goal/context*/usage/workMillis/activeTurnStartedAt/reasoningEffortLevels/supportsReasoning verbatim from thread.serf", () => {
  const model = testHydrate({
    serf: {
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

test("hydrateThread defaults the wave 5 snapshot-only fields when thread.serf omits them (old daemon / codex thread)", () => {
  const model = testHydrate(); // testThread()'s default serf carries none of these

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
    serf: {
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

// The work-clock anchor (activeTurnStartedAt) is the sole snapshot-only serf
// field with a rest-state exception to "survives untouched": it has no live
// push to refresh it, so a cold hydrate mid-turn (server/appwire_runtime.go:865
// sets serf.activeTurnId, agent stamps ActiveTurnStartedAt) leaves a live anchor
// that would keep clocking now-minus-anchor forever once the turn ends. The
// reducer clears it on the two transitions it already handles — thread/status/
// changed to any non-active status, and turn/completed — so the model never
// carries a live anchor while at rest. StatusRow.tsx:130 feeds it to
// totalWorkMillis unconditionally, so a stale anchor is a ticking idle clock.
test("thread/status/changed to a non-active status clears the live work-clock anchor", () => {
  // Wire shapes: hydrate serf.activeTurnStartedAt is epoch-ms (reducer.ts:266
  // epochMsToISO); ThreadStatusChangedParams is {threadId, ref?, status} with
  // status {type} (types.gen.ts:963-972, reducer.ts:574-577).
  let model = testHydrate({
    status: { type: "active" },
    serf: { ref: "ref_t", capabilities: CAPABILITIES, queue: { revision: 0 }, activeTurnStartedAt: 1_700_000_000_000 },
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
  // Wire shapes: serf.activeTurnId sets model.activeTurnId (reducer.ts:231-233,
  // server/appwire_runtime.go:865); TurnCompletedParams is the bare {turnId,
  // turn} settle stamp with itemsView "" (reducer.ts:396-412, 430-433 citing
  // the internal/appprojector live settle sites).
  let model = testHydrate({
    status: { type: "active" },
    serf: {
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
    serf: { ref: "ref_t", capabilities: CAPABILITIES, queue: { revision: 0 }, activeTurnStartedAt: 1_700_000_000_000 },
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
    serf: { ref: "ref_t", capabilities: CAPABILITIES, queue: { revision: 0 }, pendingEscalations: [escalation] },
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
    serf: { ref: "ref_t", capabilities: CAPABILITIES, queue: { revision: 0 }, pendingEscalations: [escalation] },
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

test('"serf/sandbox/escalation/requested" appends a new card with full field mapping and stamps lastFrameAt', () => {
  let model = testHydrate();
  const escalation = testEscalation({ command: "rm -rf /tmp/x", outputSoFar: "partial output", partiallyRan: true });

  model = applyNotification(model, { method: "serf/sandbox/escalation/requested", params: escalation }, 2000);

  expect(model.pendingEscalations).toEqual([escalation]);
  expect(model.lastFrameAt).toBe(2000);
});

test('"serf/sandbox/escalation/requested" with an already-present escalationId replaces that entry IN PLACE, index-preserving — not a filter-then-append', () => {
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
    serf: { ref: "ref_t", capabilities: CAPABILITIES, queue: { revision: 0 }, pendingEscalations: [first, second] },
  });

  const updatedFirst = testEscalation({ escalationId: "esc_1", mode: "exempt_path_prefix", partiallyRan: true });
  model = applyNotification(model, { method: "serf/sandbox/escalation/requested", params: updatedFirst }, 2000);

  expect(model.pendingEscalations).toEqual([updatedFirst, second]);
});

test('"serf/sandbox/escalation/requested" for a different thread is a same-reference no-op', () => {
  const model = testHydrate();
  const escalation = testEscalation({ ref: "some_other_ref", threadId: "thr_other" });

  const result = applyNotification(model, { method: "serf/sandbox/escalation/requested", params: escalation }, 2000);

  expect(result).toBe(model);
});

test('"serf/sandbox/escalation/resolved" clears the matching card by id and stamps lastFrameAt', () => {
  // Wire-honesty spec Part B: the daemon now broadcasts escalation/resolved to
  // every OTHER subscribed client when a pending escalation leaves the set
  // (resolved, turn-interrupted, or cleared by session close). A client still
  // showing that card drops it — reusing the exact by-id clear the local
  // resolve path already uses (resolvePendingEscalation).
  const escalation = testEscalation();
  let model = testHydrate({
    serf: { ref: "ref_t", capabilities: CAPABILITIES, queue: { revision: 0 }, pendingEscalations: [escalation] },
  });

  model = applyNotification(
    model,
    {
      method: "serf/sandbox/escalation/resolved",
      params: { threadId: "thr_t", ref: "ref_t", escalationId: escalation.escalationId },
    },
    2000,
  );

  expect(model.pendingEscalations).toEqual([]);
  expect(model.lastFrameAt).toBe(2000);
});

test('"serf/sandbox/escalation/resolved" for an id this client never held leaves the set intact but still stamps lastFrameAt', () => {
  // The resolved broadcast is a genuine live frame even when this client's own
  // pending set never carried that id (it hydrated after the raise, or the id
  // belongs to a sibling escalation) — stamp liveness like every other targeted
  // notification, and leave the surviving cards untouched.
  const escalation = testEscalation({ escalationId: "esc_1" });
  let model = testHydrate({
    serf: { ref: "ref_t", capabilities: CAPABILITIES, queue: { revision: 0 }, pendingEscalations: [escalation] },
  });

  model = applyNotification(
    model,
    {
      method: "serf/sandbox/escalation/resolved",
      params: { threadId: "thr_t", ref: "ref_t", escalationId: "esc_never_held" },
    },
    2000,
  );

  expect(model.pendingEscalations).toEqual([escalation]);
  expect(model.lastFrameAt).toBe(2000);
});

test('"serf/sandbox/escalation/resolved" for a different thread is a same-reference no-op', () => {
  const escalation = testEscalation();
  const model = testHydrate({
    serf: { ref: "ref_t", capabilities: CAPABILITIES, queue: { revision: 0 }, pendingEscalations: [escalation] },
  });

  const result = applyNotification(
    model,
    {
      method: "serf/sandbox/escalation/resolved",
      params: { threadId: "thr_other", ref: "some_other_ref", escalationId: escalation.escalationId },
    },
    2000,
  );

  expect(result).toBe(model);
});

test("resolvePendingEscalation removes the entry with a matching escalationId", () => {
  const escalation = testEscalation();
  const model = testHydrate({
    serf: { ref: "ref_t", capabilities: CAPABILITIES, queue: { revision: 0 }, pendingEscalations: [escalation] },
  });

  const result = resolvePendingEscalation(model, escalation.escalationId);

  expect(result.pendingEscalations).toEqual([]);
});

test("resolvePendingEscalation on an unknown escalationId is a same-reference no-op", () => {
  const escalation = testEscalation();
  const model = testHydrate({
    serf: { ref: "ref_t", capabilities: CAPABILITIES, queue: { revision: 0 }, pendingEscalations: [escalation] },
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
    serf: { ref: "ref_t", capabilities: CAPABILITIES, queue: { revision: 0 }, failedToolCalls: 0 },
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
    serf: { ref: "ref_t", capabilities: CAPABILITIES, queue: { revision: 0 }, failedToolCalls: 4 },
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
    serf: { ref: "ref_t", capabilities: CAPABILITIES, queue: { revision: 0 }, failedToolCalls: 2 },
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
    serf: { ref: "ref_t", capabilities: CAPABILITIES, queue: { revision: 0 }, failedToolCalls: 0 },
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
    serf: { ref: "ref_t", capabilities: CAPABILITIES, queue: { revision: 0 }, failedToolCalls: 0 },
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
    serf: { ref: "ref_t", capabilities: CAPABILITIES, queue: { revision: 0 }, failedToolCalls: 3 },
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
      serf: {
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
test("serf/thread/modelRetry records retry state and leaves lastFrameAt alone", () => {
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
      method: "serf/thread/modelRetry",
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
  });
  expect(model.lastFrameAt).toBe(frameAtBeforeRetry);
});

// The retry state answers "what is happening now". Once the model actually
// produces something, or the turn settles, it is history and must not linger
// next to live output.
test("modelRetry clears once the model produces a frame", () => {
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
      method: "serf/thread/modelRetry",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        turnId: "turn_1",
        attempt: 1,
        maxAttempts: 11,
        delayMs: 1000,
        errorClass: "rate_limit",
        statusCode: 429,
      },
    },
    1002,
  );
  expect(model.modelRetry).toBeDefined();

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
  text: "Loaded prompt serf (2.1 kB)",
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
    serf: {
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
  expect(itemAt(turnAt(model, 0), 1).text).toBe("Loaded prompt serf (2.1 kB)");
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
    serf: { ref: "ref_t", capabilities: CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
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
  let model = hydrateThread({ thread }, thread.serf.ref, 1000);
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
