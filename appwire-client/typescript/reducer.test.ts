import { expect, test, vi } from "vitest";
import basicTurnFixture from "./fixtures/basic-turn.jsonl?raw";
import queueAndStatusFixture from "./fixtures/queue-and-status.jsonl?raw";
import streamingWithResetFixture from "./fixtures/streaming-with-reset.jsonl?raw";
import toolAndJobsFixture from "./fixtures/tool-and-jobs.jsonl?raw";
import type { ThreadModel } from "./model";
import {
  applyNotification,
  notificationTargetsThread,
  notificationTouchesTranscript,
  resolvePendingEscalation,
} from "./reducer";
import { itemAt, turnAt } from "./testing/modelAccessors";
import {
  applyHistory,
  CAPABILITIES,
  fragmentTurn,
  hydrateWithShaImage,
  positionedFragmentTurn,
  SHA_IMAGE,
  testEscalation,
  testHydrate,
  testHydrateV6,
  testThread,
} from "./testing/reducerTestKit";
import {
  applyReadModel,
  collectAuthoritativeMutationIds,
  hydrateThread,
  mergeOlderItemPage,
} from "./transcriptHistory";
import type { AnyNotification, EvenerDelegateInfo, ThreadReadResponse, Turn } from "./types.gen";
import { NOTIFICATION_NAMES } from "./types.gen";
import { RAW_WARNING_FRAME_MAX_CHARS } from "./warningFold";
import { WarningCodeContextBudget } from "./warnings";

test("recorded history rejects equal-version explicit empty fields but accepts a newer clear", () => {
  const heldImages = [{ type: "image", url: "data:image/png;base64,AA==", mediaType: "image/png" }];
  let model = testHydrateV6({
    turns: [
      {
        id: "turn_1",
        status: "completed",
        itemsView: "full",
        items: [
          {
            id: "item_1",
            transcriptKey: "entry-4/item-0",
            turnId: "turn_1",
            type: "assistant",
            text: "known text",
            images: heldImages,
            version: 4,
            status: "completed",
          },
        ],
      },
    ],
  });
  const update = (version: number, text: string, images: typeof heldImages) =>
    applyHistory(
      model,
      {
        snapshot: { incarnation: "inc-1", length: version },

        items: [
          {
            id: "item_1",
            transcriptKey: "entry-4/item-0",
            turnId: "turn_1",
            type: "assistant",
            text,
            images,
            version,
            status: "completed",
          },
        ],
      },
      1000 + version,
    );

  model = update(4, "", []);
  const equalVersion = itemAt(turnAt(model, 0), 0);
  expect(equalVersion).toMatchObject({ text: "known text", version: 4 });
  expect(equalVersion.images?.map(({ src }) => src)).toEqual([heldImages[0]!.url]);

  model = update(5, "", []);
  const newerVersion = itemAt(turnAt(model, 0), 0);
  expect(newerVersion).toMatchObject({ text: "", version: 5 });
  expect(newerVersion.images?.map(({ src }) => src)).toEqual([heldImages[0]!.url]);
});

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

for (const f of ["basic-turn", "streaming-with-reset", "tool-and-jobs", "queue-and-status"]) {
  test(`fixture ${f} reduces to the expected model`, () => {
    const { header, notifications } = readFixture(f);
    let model = hydrateThread(header.hydrate, header.ref, 1000);
    for (const [i, n] of notifications.entries()) {
      model = applyNotification(model, n, 1000 + i);
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

test("lastTurnEndedAt hydrates as an ISO string, and absent or zero hydrates as absent", () => {
  const ended = testHydrate({
    evener: { ref: "ref_t", capabilities: CAPABILITIES, queue: { revision: 0 }, lastTurnEndedAt: 1_790_000_000_123 },
  });
  expect(ended.lastTurnEndedAt).toBe("2026-09-21T14:13:20.123Z");
  expect(Date.parse(ended.lastTurnEndedAt ?? "")).toBe(1_790_000_000_123);
  const absent = testHydrate({ evener: { ref: "ref_t", capabilities: CAPABILITIES, queue: { revision: 0 } } });
  expect(absent.lastTurnEndedAt).toBeUndefined();
  const zero = testHydrate({
    evener: { ref: "ref_t", capabilities: CAPABILITIES, queue: { revision: 0 }, lastTurnEndedAt: 0 },
  });
  expect(zero.lastTurnEndedAt).toBeUndefined();
});

test("item/completed inserts an item that had no preceding item/started", () => {
  // userMessage and systemMessage items go straight to item/completed with
  // no item/started (internal/appprojector/appwire_projection.go: a new user
  // turn emits turn/started with an empty turn, then item/completed for the
  // userMessage — item/started is never sent for it).
  let model = testHydrateV6();
  model = applyHistory(
    model,
    {
      turns: [{ id: "turn_1", status: "inProgress", itemsView: "" }],
    },
    1001,
  );
  expect(turnAt(model, 0).items).toHaveLength(0);

  model = applyHistory(
    model,
    {
      items: [
        {
          ...{ type: "userMessage", id: "item_user", turnId: "turn_1", text: "Hi there", status: "completed" },
          turnId: "turn_1",
        },
      ],
    },
    1002,
  );

  const item = itemAt(turnAt(model, 0), 0);
  expect(item.type).toBe("userMessage");
  expect(item.text).toBe("Hi there");
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
  model = applyHistory(
    model,
    {
      turns: [{ id: "turn_1", status: "inProgress", itemsView: "" }],
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
  model = applyHistory(
    model,
    {
      turns: [{ id: "turn_1", status: "inProgress", itemsView: "" }],
    },
    1001,
  );
  model = applyHistory(
    model,
    {
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
    1002,
  );

  const item = itemAt(turnAt(model, 0), 0);
  expect(item.images).toEqual([{ src: "data:image/png;base64,iVBORw0KGgo=", name: "tiny.png" }]);
});

test("item/completed preserves an input image's name alongside a src resolved from a different field", () => {
  let model = testHydrateV6();
  model = applyHistory(
    model,
    {
      turns: [{ id: "turn_1", status: "inProgress", itemsView: "" }],
    },
    1001,
  );
  model = applyHistory(
    model,
    {
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
    1002,
  );

  const item = itemAt(turnAt(model, 0), 0);
  // path (not name) wins the src fallback here — name survives regardless.
  expect(item.images).toEqual([{ src: "uploads/photo.jpg", name: "photo.jpg", path: "uploads/photo.jpg" }]);
});

test("item/completed preserves an output image's name/path/source alongside a src resolved from yet another field", () => {
  let model = testHydrateV6();
  model = applyHistory(
    model,
    {
      turns: [{ id: "turn_1", status: "inProgress", itemsView: "" }],
    },
    1001,
  );
  model = applyHistory(
    model,
    {
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
  model = applyHistory(
    model,
    {
      turns: [{ id: "turn_1", status: "inProgress", itemsView: "" }],
    },
    1001,
  );
  model = applyHistory(
    model,
    {
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

test("a live item/completed with sha but no stamped url resolves to the hydrated session's route", () => {
  let model = testHydrateV6();
  model = applyHistory(
    model,
    {
      turns: [{ id: "turn_1", status: "inProgress", itemsView: "" }],
    },
    1001,
  );
  model = applyHistory(
    model,
    {
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
    1002,
  );

  expect(itemAt(turnAt(model, 0), 0).images).toEqual([
    { src: `/s/sess_t/images/${SHA_IMAGE}`, name: "photo.png", path: undefined },
  ]);
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
  model = applyHistory(
    model,
    {
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
  model = applyHistory(
    model,
    {
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
  model = applyHistory(
    model,
    {
      items: [
        {
          ...{ id: "legacy-id", turnId: "turn_1", type: "agentMessage", text: "settled", status: "completed" },
          turnId: "turn_1",
        },
      ],
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
  model = applyHistory(
    model,
    {
      turns: [{ id: "turn_1", status: "inProgress", itemsView: "" }],
    },
    1001,
  );
  model = applyHistory(
    model,
    {
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
    1002,
  );
  const item = itemAt(turnAt(model, 0), 0);
  expect(item.error).toBe("denied: user rejected");
  // Status is "completed" even for the errored call - error presence, not
  // status, is the honest failure signal.
  expect(item.status).toBe("completed");
});

// A settled shell tool call now carries its process exit code as a typed wire
// field (ThreadItem.exitCode, wire-honesty spec Part A) — the model must carry
// it so a descriptor reads a structured number rather than parsing the output
// footer text.
test("item/completed maps the wire item's exitCode onto the model (live path)", () => {
  let model = testHydrateV6();
  model = applyHistory(
    model,
    {
      turns: [{ id: "turn_1", status: "inProgress", itemsView: "" }],
    },
    1001,
  );
  model = applyHistory(
    model,
    {
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
    1002,
  );
  expect(itemAt(turnAt(model, 0), 0).exitCode).toBe(2);
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

// Wave 5 T1: thread/model/changed's real payload (appwire/types_turn.go's
// ThreadModelChangedParams) carries reasoningEffortLevels/
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

test("the warning fold carries the wire code onto the item", () => {
  let model = warningTurnModel();

  model = applyNotification(
    model,
    {
      method: "warning",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        message: "Output allocation reduced for inst/model: requested=100 admitted=50",
        code: WarningCodeContextBudget,
      },
    },
    1002,
  );

  expect(turnAt(model, 0).items[0]).toMatchObject({ type: "warning", warning: { code: WarningCodeContextBudget } });
});

test("a blank wire code folds to absent, like every other blank warning field", () => {
  let model = warningTurnModel();

  model = applyNotification(
    model,
    {
      method: "warning",
      params: { threadId: "thr_t", ref: "ref_t", message: "rate limit approaching", code: "   " },
    },
    1002,
  );

  expect(turnAt(model, 0).items[0]?.warning?.code).toBeUndefined();
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

  model = applyHistory(
    model,
    {
      turns: [{ id: "turn_1", status: "completed", itemsView: "" }],
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
// DecodeWarningParams enforces server-side ("no message anywhere renders the
// frame itself, not a bare title"): a malformed or message-less warning must
// stay visible, or a producer's typo
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
  model = applyHistory(
    model,
    {
      turns: [{ id: "turn_1", status: "inProgress", itemsView: "" }],
    },
    1001,
  );
  model = applyHistory(
    model,
    {
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
    1002,
  );

  model = applyHistory(
    model,
    {
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
    1003,
  );

  const item = itemAt(turnAt(model, 0), 0);
  expect(item.argumentsJSON).toBe('{"command":"ls -la"}');
});

test("item/completed inserting a never-started item has no argumentsJSON (no crash, no fabrication)", () => {
  let model = testHydrateV6();
  model = applyHistory(
    model,
    {
      turns: [{ id: "turn_1", status: "inProgress", itemsView: "" }],
    },
    1001,
  );

  model = applyHistory(
    model,
    {
      items: [
        {
          ...{ type: "userMessage", id: "item_user", turnId: "turn_1", text: "hi", status: "completed" },
          turnId: "turn_1",
        },
      ],
    },
    1002,
  );

  const item = itemAt(turnAt(model, 0), 0);
  expect(item.argumentsJSON).toBeUndefined();
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
// push to refresh it, so a cold hydrate mid-turn (server/appwire_read_handlers.go
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

  model = applyHistory(
    model,
    {
      turns: [{ id: "turn_1", status: "inProgress", itemsView: "" }],
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
  model = applyHistory(
    model,
    {
      turns: [{ id: "turn_1", status: "inProgress", itemsView: "" }],
    },
    1001,
  );

  model = applyHistory(
    model,
    {
      turns: [{ id: "turn_1", status: "completed", itemsView: "" }],
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
  // card (appwire/types_thread.go's PendingEscalations doc comment). Last write
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
// completion actually moved the count (server/appwire_status_stamps.go's
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
  model = applyHistory(
    model,
    {
      turns: [{ id: "turn_1", status: "inProgress", itemsView: "" }],
    },
    1001,
  );

  model = applyHistory(
    model,
    {
      items: [
        {
          ...{ type: "userMessage", id: "item_user", turnId: "turn_1", text: "Hi there", status: "completed" },
          turnId: "turn_1",
        },
      ],
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

// kata 4zn8: a rate-limited model call must land as explainable liveness state
// WITHOUT restamping lastFrameAt. The model has genuinely produced nothing —
// restamping would reset the quiet/stall clock and make a four-hour rate limit
// render calmer than it does today, which is the opposite of the point.
test("evener/thread/modelRetry records retry state and leaves lastFrameAt alone", () => {
  let model = testHydrate();
  model = applyHistory(
    model,
    {
      turns: [{ id: "turn_1", status: "inProgress", itemsView: "" }],
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
  model = applyHistory(
    model,
    {
      items: [
        {
          ...{ type: "systemMessage", id: "item_sys_1", turnId: "turn_1", status: "completed", text: "note" },
          turnId: "turn_1",
        },
      ],
    },
    1003,
  );
  expect(model.modelRetry).toBeDefined();
});

test("modelRetry survives a userMessage item completion", () => {
  let model = withActiveRetry();
  model = applyHistory(
    model,
    {
      items: [
        {
          ...{ type: "userMessage", id: "item_user_1", turnId: "turn_1", status: "completed", text: "hi" },
          turnId: "turn_1",
        },
      ],
    },
    1003,
  );
  expect(model.modelRetry).toBeDefined();
});

test("modelRetry clears once the model's own output item (agentMessage) completes", () => {
  let model = withActiveRetry();
  model = applyHistory(
    model,
    {
      items: [{ ...{ type: "agentMessage", id: "item_1", turnId: "turn_1", status: "inProgress" }, turnId: "turn_1" }],
    },
    1003,
  );
  model = applyHistory(
    model,
    {
      items: [
        {
          ...{ type: "agentMessage", id: "item_1", turnId: "turn_1", status: "completed", text: "done" },
          turnId: "turn_1",
        },
      ],
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
// no notification of its own any more).

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

// The other direction of the same selector, and the reason the preserve cases
// above cannot stand alone: a settle that DOES carry images replaces the ones
// the item had. Reverse mergeItemImages' `??` (`existing.images ??
// settled.images`) and every "keeps ..." assertion above stays green while a
// stale attachment silently wins — this case is what turns that red.
test("item/completed carrying a different images list overrides the images the item already had", () => {
  let model = testHydrateV6();
  model = applyHistory(
    model,
    {
      turns: [{ id: "turn_1", status: "inProgress", itemsView: "" }],
    },
    1001,
  );
  model = applyHistory(
    model,
    {
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
    1002,
  );
  expect(itemAt(turnAt(model, 0), 0).images).toEqual([{ src: "data:image/png;base64,iVBORw0KGgo=", name: "shot.png" }]);

  // Same item, and this settle's own image is the one that must win.
  model = applyHistory(
    model,
    {
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
    1003,
  );
  expect(itemAt(turnAt(model, 0), 0).images).toEqual([{ src: "data:image/png;base64,BAUG", name: "new.png" }]);
});

test("item/completed carrying a different outputImages list overrides the output images the item already had", () => {
  let model = testHydrateV6();
  model = applyHistory(
    model,
    {
      turns: [{ id: "turn_1", status: "inProgress", itemsView: "" }],
    },
    1001,
  );
  model = applyHistory(
    model,
    {
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
    1002,
  );
  expect(itemAt(turnAt(model, 0), 0).outputImages).toEqual([
    { src: "out/plot.png", name: "plot.png", path: "out/plot.png", source: "written-file" },
  ]);

  model = applyHistory(
    model,
    {
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
  model = applyHistory(
    model,
    {
      turns: [{ id: "turn_1", status: "inProgress", itemsView: "" }],
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

// Edge cases for reducer.ts that close the remaining uncovered lines:
// - laterActivity: incoming is NaN, current is NaN, incoming > current
// - mergeStableDelegate: incoming projectionRevision > current, activity merge
// - upsertStableDelegate: new delegate, existing delegate merge, no-op merge
// - applyNotification "evener/delegate/updated": adds/updates delegates
// - applyNotification "evener/jobs/treeUpdated": stale revision

function delegateInfo(overrides: Partial<EvenerDelegateInfo> = {}): EvenerDelegateInfo {
  return {
    runGeneration: 1,
    delegateId: "dlg_1",
    ownerSessionId: "sess_owner",
    rootSessionId: "sess_root",
    childSessionId: "sess_child",
    transcriptRef: "local:01dlg_1",
    type: "delegate",
    lifecycle: "stable",
    phase: "idle",
    status: "idle",
    outcome: "completed",
    terminal: true,
    resumable: true,
    needsAttention: false,
    projectionRevision: 1,
    ...overrides,
  };
}

function delegateNotification(ref: string, delegate: EvenerDelegateInfo): AnyNotification {
  return {
    method: "evener/delegate/updated",
    params: { ref, delegate },
  } as AnyNotification;
}

function jobsTreeNotification(ref: string, revision: number): AnyNotification {
  return {
    method: "evener/jobs/treeUpdated",
    params: { ref, revision },
  } as AnyNotification;
}

// --- laterActivity edge cases (exercised through delegate/updated) ---

test("delegate/updated adds a new delegate when none exist", () => {
  const model = testHydrate();
  const dlg = delegateInfo({ delegateId: "dlg_new", projectionRevision: 1 });
  const result = applyNotification(model, delegateNotification("ref_t", dlg), 2000);
  expect(result.delegates).toHaveLength(1);
  expect(result.delegates?.[0]).toMatchObject({ delegateId: "dlg_new" });
  expect(result.jobsUpdatedAt).toBe(2000);
  expect(result.lastFrameAt).toBe(2000);
});

test("delegate/updated merges an existing delegate with higher projectionRevision", () => {
  const model = testHydrate();
  const dlg1 = delegateInfo({
    delegateId: "dlg_1",
    projectionRevision: 1,
    status: "running",
    outcome: undefined,
    latestActivityAt: "2026-01-03T00:00:00Z",
  });
  let result = applyNotification(model, delegateNotification("ref_t", dlg1), 2000);
  const dlg2 = delegateInfo({
    delegateId: "dlg_1",
    projectionRevision: 2,
    status: "idle",
    outcome: "completed",
    latestActivityAt: "2026-01-02T00:00:00Z",
  });
  result = applyNotification(result, delegateNotification("ref_t", dlg2), 3000);
  expect(result.delegates?.[0]).toMatchObject({
    delegateId: "dlg_1",
    projectionRevision: 2,
    status: "idle",
    outcome: "completed",
    latestActivityAt: "2026-01-03T00:00:00Z",
  });
});

test("delegate/updated with same projectionRevision and later activity updates latestActivityAt", () => {
  const model = testHydrate();
  const dlg1 = delegateInfo({
    delegateId: "dlg_1",
    projectionRevision: 1,
    status: "running",
    latestActivityAt: "2026-01-01T00:00:00Z",
  });
  let result = applyNotification(model, delegateNotification("ref_t", dlg1), 2000);
  // Same revision, later activity
  const dlg2 = delegateInfo({
    delegateId: "dlg_1",
    projectionRevision: 1,
    status: "idle",
    latestActivityAt: "2026-01-03T00:00:00Z",
  });
  result = applyNotification(result, delegateNotification("ref_t", dlg2), 3000);
  expect(result.delegates?.[0]).toMatchObject({
    projectionRevision: 1,
    status: "running",
    latestActivityAt: "2026-01-03T00:00:00Z",
  });
});

test("delegate/updated with same projectionRevision and earlier activity keeps current activity", () => {
  const model = testHydrate();
  const dlg1 = delegateInfo({
    delegateId: "dlg_1",
    projectionRevision: 1,
    latestActivityAt: "2026-01-03T00:00:00Z",
  });
  let result = applyNotification(model, delegateNotification("ref_t", dlg1), 2000);
  const delegatesBefore = result.delegates;
  // Same revision, earlier activity
  const dlg2 = delegateInfo({
    delegateId: "dlg_1",
    projectionRevision: 1,
    latestActivityAt: "2026-01-01T00:00:00Z",
  });
  result = applyNotification(result, delegateNotification("ref_t", dlg2), 3000);
  expect(result.delegates).toBe(delegatesBefore);
  expect(result.delegates?.[0]?.latestActivityAt).toBe("2026-01-03T00:00:00Z");
  expect(result.jobsUpdatedAt).toBe(3000);
  expect(result.lastFrameAt).toBe(3000);
});

test("delegate/updated with NaN incoming activity keeps current activity", () => {
  const model = testHydrate();
  const dlg1 = delegateInfo({
    delegateId: "dlg_1",
    projectionRevision: 1,
    latestActivityAt: "2026-01-01T00:00:00Z",
  });
  let result = applyNotification(model, delegateNotification("ref_t", dlg1), 2000);
  // Incoming with unparseable activity
  const dlg2 = delegateInfo({
    delegateId: "dlg_1",
    projectionRevision: 1,
    latestActivityAt: "not-a-date",
  });
  result = applyNotification(result, delegateNotification("ref_t", dlg2), 3000);
  expect(result.delegates?.[0]?.latestActivityAt).toBe("2026-01-01T00:00:00Z");
});

test("delegate/updated orders activity below a millisecond like the activity store", () => {
  const model = testHydrate();
  const dlg1 = delegateInfo({
    delegateId: "dlg_1",
    projectionRevision: 1,
    latestActivityAt: "2026-01-01T00:00:00.000100Z",
  });
  let result = applyNotification(model, delegateNotification("ref_t", dlg1), 2000);
  const dlg2 = delegateInfo({
    delegateId: "dlg_1",
    projectionRevision: 1,
    latestActivityAt: "2026-01-01T00:00:00.000900Z",
  });
  result = applyNotification(result, delegateNotification("ref_t", dlg2), 3000);
  expect(result.delegates?.[0]?.latestActivityAt).toBe("2026-01-01T00:00:00.000900Z");
  const earlier = delegateInfo({
    delegateId: "dlg_1",
    projectionRevision: 1,
    latestActivityAt: "2026-01-01T00:00:00.000500Z",
  });
  result = applyNotification(result, delegateNotification("ref_t", earlier), 4000);
  expect(result.delegates?.[0]?.latestActivityAt).toBe("2026-01-01T00:00:00.000900Z");
});

test("delegate/updated with no incoming activity keeps current", () => {
  const model = testHydrate();
  const dlg1 = delegateInfo({
    delegateId: "dlg_1",
    projectionRevision: 1,
    latestActivityAt: "2026-01-01T00:00:00Z",
  });
  let result = applyNotification(model, delegateNotification("ref_t", dlg1), 2000);
  const dlg2 = delegateInfo({
    delegateId: "dlg_1",
    projectionRevision: 1,
    // no latestActivityAt
  });
  result = applyNotification(result, delegateNotification("ref_t", dlg2), 3000);
  expect(result.delegates?.[0]?.latestActivityAt).toBe("2026-01-01T00:00:00Z");
});

test("delegate/updated with no current activity uses incoming", () => {
  const model = testHydrate();
  const dlg1 = delegateInfo({
    delegateId: "dlg_1",
    projectionRevision: 1,
    // no latestActivityAt
  });
  let result = applyNotification(model, delegateNotification("ref_t", dlg1), 2000);
  const dlg2 = delegateInfo({
    delegateId: "dlg_1",
    projectionRevision: 1,
    latestActivityAt: "2026-01-01T00:00:00Z",
  });
  result = applyNotification(result, delegateNotification("ref_t", dlg2), 3000);
  expect(result.delegates?.[0]?.latestActivityAt).toBe("2026-01-01T00:00:00Z");
});

test("delegate/updated with identical delegate is a no-op (returns same delegates reference)", () => {
  const model = testHydrate();
  const dlg = delegateInfo({ delegateId: "dlg_1", projectionRevision: 1, latestActivityAt: "2026-01-01T00:00:00Z" });
  let result = applyNotification(model, delegateNotification("ref_t", dlg), 2000);
  const delegatesBefore = result.delegates;
  // Same delegate again
  result = applyNotification(result, delegateNotification("ref_t", dlg), 3000);
  expect(result.delegates).toBe(delegatesBefore);
});

test("delegate/updated for a different ref is a no-op", () => {
  const model = testHydrate();
  const dlg = delegateInfo({ delegateId: "dlg_1" });
  const result = applyNotification(model, delegateNotification("ref_other", dlg), 2000);
  expect(result).toBe(model);
});

// --- evener/jobs/treeUpdated ---

test("jobs/treeUpdated sets jobsTreeRevision when it is null", () => {
  const model = testHydrate();
  const result = applyNotification(model, jobsTreeNotification("ref_t", 5), 2000);
  expect(result.jobsTreeRevision).toBe(5);
  expect(result.jobsUpdatedAt).toBe(2000);
});

test("jobs/treeUpdated with higher revision updates jobsTreeRevision", () => {
  const model = testHydrate();
  let result = applyNotification(model, jobsTreeNotification("ref_t", 5), 2000);
  result = applyNotification(result, jobsTreeNotification("ref_t", 10), 3000);
  expect(result.jobsTreeRevision).toBe(10);
});

test("jobs/treeUpdated with stale revision is a no-op", () => {
  const model = testHydrate();
  let result = applyNotification(model, jobsTreeNotification("ref_t", 10), 2000);
  const before = result;
  result = applyNotification(result, jobsTreeNotification("ref_t", 5), 3000);
  expect(result).toBe(before);
  expect(result.jobsTreeRevision).toBe(10);
  expect(result.jobsUpdatedAt).toBe(2000);
});

test("jobs/treeUpdated for a different ref is a no-op", () => {
  const model = testHydrate();
  const result = applyNotification(model, jobsTreeNotification("ref_other", 5), 2000);
  expect(result).toBe(model);
});

test("evener/notes/updated replaces both notes and stamps lastFrameAt", () => {
  const initial = testHydrate({ evener: { humanNote: "old human", agentNote: "old agent" } });
  const updated = applyNotification(
    initial,
    {
      method: "evener/notes/updated",
      params: { threadId: "thr_t", ref: "ref_t", humanNote: "new human", agentNote: "new agent" },
    },
    2000,
  );
  expect(updated.humanNote).toBe("new human");
  expect(updated.agentNote).toBe("new agent");
  expect(updated.lastFrameAt).toBe(2000);

  const cleared = applyNotification(
    updated,
    { method: "evener/notes/updated", params: { threadId: "thr_t", ref: "ref_t" } },
    3000,
  );
  expect(cleared.humanNote).toBe("");
  expect(cleared.agentNote).toBe("");
  expect(cleared.lastFrameAt).toBe(3000);
});

test("evener/urls/updated replaces the list and stamps lastFrameAt", () => {
  const initial = testHydrate({ evener: { sessionUrls: [{ id: "u1", url: "https://x.test/y" }] } });
  const updated = applyNotification(
    initial,
    {
      method: "evener/urls/updated",
      params: { threadId: "thr_t", ref: "ref_t", urls: [{ id: "u2", url: "https://y.test/z", label: "y" }] },
    },
    2000,
  );
  expect(updated.sessionUrls).toEqual([{ id: "u2", url: "https://y.test/z", label: "y" }]);
  expect(updated.lastFrameAt).toBe(2000);

  const cleared = applyNotification(
    updated,
    { method: "evener/urls/updated", params: { threadId: "thr_t", ref: "ref_t" } },
    3000,
  );
  expect(cleared.sessionUrls).toEqual([]);
  expect(cleared.lastFrameAt).toBe(3000);
});

test("notes/urls pushes for a different thread are ignored (same object returned)", () => {
  const initial = testHydrate({ evener: { humanNote: "h", agentNote: "a" } });
  const other = { threadId: "thr_other", ref: "ref_other" } as const;
  expect(
    applyNotification(initial, { method: "evener/notes/updated", params: { ...other, humanNote: "x" } }, 2000),
  ).toBe(initial);
  expect(applyNotification(initial, { method: "evener/urls/updated", params: { ...other, urls: [] } }, 2000)).toBe(
    initial,
  );
});

// Every notification the wire names is classified here, so a new one fails this
// table until someone decides whether it can change `turns`: the phone store
// refetches when a transcript-shaped frame leaves `turns` untouched.
const SCALAR_ONLY_NOTIFICATIONS = [
  "thread/queueChanged",
  "thread/status/changed",
  "thread/model/changed",
  "thread/reasoning-effort/changed",
  "evener/goal/updated",
  "evener/notes/updated",
  "evener/urls/updated",
  "thread/vision-model/changed",
  "evener/task/updated",
  "evener/thread/name/changed",
  "evener/sandbox/escalation/requested",
  "evener/sandbox/escalation/resolved",
  "evener/job/started",
  "evener/job/finished",
  "evener/delegate/updated",
  "evener/jobs/treeUpdated",
  "evener/thread/modelRetry",
] as const;

const TRANSCRIPT_SHAPED_NOTIFICATIONS = [
  "thread/started",
  "thread/closed",
  "warning",
  "evener/auth/updated",
  "evener/launch/updated",
  "evener/attention/changed",
  "evener/navigation/invalidated",
  "evener/marketplace/updated",
  "evener/plugin/updated",
  "evener/notices/changed",
  "evener/runtime/session/observation",
  "evener/thread/resync",
  "evener/thread/activity/changed",
  "evener/settings/transcriptDisplay/changed",
  "evener/settings/keybindings/changed",
  "evener/settings/agentsDoc/changed",
  "evener/host/notification",
  "history/updated",
  "overlay/upserted",
  "overlay/delta",
  "overlay/reset",
  "overlay/end",
] as const;

test("every wire notification is classified as scalar-only or transcript-shaped", () => {
  const classified = [...SCALAR_ONLY_NOTIFICATIONS, ...TRANSCRIPT_SHAPED_NOTIFICATIONS];
  expect([...classified].sort()).toEqual([...NOTIFICATION_NAMES].sort());
  for (const method of SCALAR_ONLY_NOTIFICATIONS) expect(notificationTouchesTranscript(method)).toBe(false);
  for (const method of TRANSCRIPT_SHAPED_NOTIFICATIONS) expect(notificationTouchesTranscript(method)).toBe(true);
});

test("a method with no reducer case is transcript-shaped", () => {
  expect(notificationTouchesTranscript("evener/not/yet/known")).toBe(true);
});

test.each([true, false, undefined])("Manager start flag %s preserves the selected public identity", (manager) => {
  const thread = testThread({
    evener: { ref: "remote:canonical", instanceId: "instance", manager },
  });
  const model = hydrateThread({ thread }, "remote:historical", 1000);
  expect(model).toMatchObject({
    ref: "remote:historical",
    threadId: thread.id,
    instanceId: "instance",
    manager: manager === true,
  });
  const refreshed = applyReadModel(model, hydrateThread({ thread }, model.ref, 2000));
  expect(refreshed).toMatchObject({ ref: model.ref, instanceId: "instance", manager: manager === true });
});

test("Epochless resync leaves replacement decisions to authoritative history identity", () => {
  const model = testHydrateV6({
    turns: [
      {
        id: "old",
        status: "completed",
        itemsView: "full",
        items: [{ id: "older-page", turnId: "old", type: "agentMessage", text: "old-sentinel" }],
      },
    ],
  });
  const hinted = applyNotification(
    model,
    { method: "evener/thread/resync", params: { ref: model.ref, threadId: model.threadId } },
    2,
  );
  expect(hinted.history).toBe(model.history);
  const same = applyReadModel(hinted, testHydrateV6());
  expect(same.turns.flatMap((turn) => turn.items).map((item) => item.id)).toContain("older-page");
  const changed = hydrateThread(
    { thread: testThread(), bootGeneration: "", epoch: 2, snapshot: { incarnation: "replacement", length: 0 } },
    model.ref,
    3,
  );
  expect(applyReadModel(hinted, changed).turns).toEqual([]);
  const invalidated = applyNotification(
    model,
    {
      method: "evener/thread/resync",
      params: { ref: model.ref, threadId: model.threadId, bootGeneration: "", epoch: 2 },
    },
    2,
  );
  expect(invalidated.history?.invalidatedAtGeneration).toBeDefined();
});

test("Manager reads follow the session start flag across Clear and new session identities", () => {
  const before = testHydrate({ evener: { manager: true } });
  const cleared = applyReadModel(before, testHydrate({ id: "replacement", evener: { manager: true } }));
  expect(cleared).toMatchObject({ ref: before.ref, threadId: "replacement", manager: true });
  for (const ref of ["local:new", "local:fork"]) {
    const fresh = testHydrate({ evener: { ref } });
    expect(fresh).toMatchObject({ ref, manager: false });
  }
  const reconnected = applyReadModel(before, testHydrate({ evener: { manager: true, instanceId: "new-runtime" } }));
  expect(reconnected).toMatchObject({ ref: before.ref, instanceId: "new-runtime", manager: true });
});
