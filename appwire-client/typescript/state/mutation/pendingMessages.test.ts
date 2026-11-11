// @vitest-environment node

import { expect, test } from "vitest";
import type { InputItem, PendingMutation } from "../../types.gen";
import { type PendingMessagesInput, projectPendingMessages } from "./pendingMessages";
import type { MutationOptimisticRecord, MutationOutboxRecord, MutationRecoveryRecord } from "./records";
import { outboxRecord, recoveryRecord, threadModel } from "./testing";

const identity = (record: { originClientId?: string }): boolean => record.originClientId === undefined;
const NOTHING_SUBMITTED_HERE: ReadonlyMap<string, number> = new Map();

function textInput(text: string): InputItem[] {
  return [{ type: "text", text }];
}

function local(
  id: string,
  method = "turn/queue",
  text = "hello",
  overrides: Partial<MutationOutboxRecord> = {},
): MutationOutboxRecord {
  return outboxRecord({
    clientMutationId: id,
    targetRef: "ref_a",
    method,
    payload: { ref: "ref_a", input: textInput(text), clientMutationId: id },
    optimisticDisplay: { method, input: textInput(text) },
    ...overrides,
  });
}

function accepted(
  id: string,
  method = "turn/start",
  text = "hello",
  overrides: Partial<MutationOptimisticRecord> = {},
): MutationOptimisticRecord {
  const { state: _state, ...base } = local(id, method, text);
  return { ...base, state: "accepted", ...overrides };
}

function recovery(
  id: string,
  kind: MutationRecoveryRecord["recoveryKind"],
  method = "turn/start",
  text = "hello",
  overrides: Partial<MutationRecoveryRecord> = {},
): MutationRecoveryRecord {
  return recoveryRecord({
    ...local(id, method, text),
    recoveryKind: kind,
    ...overrides,
  });
}

function pending(id: string, method: string, text: string, overrides: Partial<PendingMutation> = {}): PendingMutation {
  return {
    clientMutationId: id,
    method,
    input: textInput(text),
    executionState: "accepted",
    projectionState: "pending",
    ...overrides,
  };
}

function project(overrides: Partial<PendingMessagesInput> = {}) {
  return projectPendingMessages({
    ref: "ref_a",
    outbox: [],
    optimistic: [],
    recovery: [],
    model: threadModel(),
    submittedHere: NOTHING_SUBMITTED_HERE,
    isOwnMutationRecord: identity,
    ...overrides,
  });
}

test("a daemon queue row and its local pending row project as one message", () => {
  const model = threadModel({
    queue: {
      depth: 1,
      revision: 2,
      ids: ["q1"],
      clientMutationIds: ["m1"],
      texts: ["hello"],
      preview: ["hello"],
    },
  });
  const rows = project({ outbox: [local("m1")], model });
  expect(rows).toHaveLength(1);
  expect(rows[0]).toMatchObject({
    id: "m1",
    method: "queue",
    state: "queued",
    text: "hello",
    queueIndex: 0,
    queueEntryId: "q1",
  });
});

test("a queue row keeps the daemon's mentions when it merges with the local row", () => {
  const model = threadModel({
    queue: {
      depth: 1,
      revision: 2,
      ids: ["q1"],
      clientMutationIds: ["m1"],
      texts: ["hello"],
      preview: ["hello"],
      mentions: [[{ kind: "skill", name: "review", offset: 0 }]],
    },
  });
  const rows = project({ outbox: [local("m1")], model });
  expect(rows[0]?.mentions).toEqual([{ kind: "skill", name: "review", offset: 0 }]);
});

test("a text item's selected mentions survive into the pending row", () => {
  const record = local("m1", "turn/start", "hello", {
    payload: {
      ref: "ref_a",
      input: [{ type: "text", text: "hello", mentions: [{ kind: "command", name: "ship", offset: 0 }] }],
      clientMutationId: "m1",
    },
    optimisticDisplay: {
      method: "turn/start",
      input: [{ type: "text", text: "hello", mentions: [{ kind: "command", name: "ship", offset: 0 }] }],
    },
  });
  const rows = project({ outbox: [record] });
  expect(rows[0]?.mentions).toEqual([{ kind: "command", name: "ship", offset: 0 }]);
});

test("a visible transcript item suppresses its queue row even when the queue snapshot still lists it", () => {
  const model = threadModel({
    queue: {
      depth: 1,
      revision: 2,
      ids: ["q1"],
      clientMutationIds: ["m1"],
      texts: ["hello"],
      preview: ["hello"],
    },
    turns: [
      {
        id: "t1",
        status: "inProgress",
        items: [{ id: "i1", turnId: "t1", type: "userMessage", text: "hello", clientMutationId: "m1" }],
      },
    ],
  });
  expect(project({ model })).toEqual([]);
});

test("a visible transcript item suppresses its local pending row", () => {
  const model = threadModel({
    turns: [
      {
        id: "t1",
        status: "inProgress",
        items: [{ id: "i1", turnId: "t1", type: "userMessage", text: "hello", clientMutationId: "m1" }],
      },
    ],
  });
  expect(project({ outbox: [local("m1", "turn/start")], model })).toEqual([]);
});

test("a drain's authoritative queueEntryIds suppress the consumed queue rows", () => {
  const model = threadModel({
    queue: {
      depth: 2,
      revision: 4,
      ids: ["q1", "q2"],
      clientMutationIds: ["m1", "m2"],
      texts: ["one", "two"],
      preview: ["one", "two"],
    },
    pendingMutations: [
      pending("m3", "turn/drainAsSteer", "three", {
        queueEntryIds: ["q1", "q2"],
      }),
    ],
  });
  const rows = project({
    model,
    outbox: [local("m3", "turn/drainAsSteer", "three")],
  });
  expect(rows).toHaveLength(1);
  expect(rows[0]).toMatchObject({ id: "m3", method: "drain", state: "steering" });
  expect(rows[0]!.text).toBe("three");
});

test("a promote row suppresses its queue entry", () => {
  const model = threadModel({
    queue: {
      depth: 1,
      revision: 3,
      ids: ["q1"],
      clientMutationIds: ["m1"],
      texts: ["the message"],
      preview: ["the message"],
    },
    pendingMutations: [
      pending("m2", "turn/promoteQueuedAsSteer", "the message", {
        queueEntryIds: ["q1"],
      }),
    ],
  });
  const rows = project({
    model,
    outbox: [local("m2", "turn/promoteQueuedAsSteer", "the message")],
  });
  expect(rows).toHaveLength(1);
  expect(rows[0]).toMatchObject({ id: "m2", method: "promote", state: "steering" });
});

test("an accepted send with no daemon pending entry and no visible item stays as a delivering row", () => {
  const rows = project({ optimistic: [accepted("m1")] });
  expect(rows).toHaveLength(1);
  expect(rows[0]).toMatchObject({ id: "m1", state: "delivering", text: "hello" });
});

test("two messages with identical text stay two rows", () => {
  const rows = project({ outbox: [local("m1", "turn/start"), local("m2", "turn/start")] });
  expect(rows.map((row) => row.id)).toEqual(["m1", "m2"]);
});

test("a legacy queue row without clientMutationIds merges with the pending mutation by queue entry id", () => {
  const model = threadModel({
    queue: {
      depth: 1,
      revision: 2,
      ids: ["q1"],
      texts: ["hello"],
      preview: ["hello"],
    },
    pendingMutations: [pending("m1", "turn/queue", "hello", { queueEntryIds: ["q1"] })],
  });
  const rows = project({ model });
  expect(rows).toHaveLength(1);
  expect(rows[0]).toMatchObject({ id: "m1", method: "queue", state: "queued", queueIndex: 0 });
});

test("a queue row with no mutation identity still renders once", () => {
  const model = threadModel({
    queue: {
      depth: 1,
      revision: 2,
      ids: ["q1"],
      texts: ["hello"],
      preview: ["hello"],
    },
  });
  const rows = project({ model });
  expect(rows).toHaveLength(1);
  expect(rows[0]).toMatchObject({
    id: "queue:q1",
    method: "queue",
    state: "queued",
    queueIndex: 0,
    textComplete: true,
  });
});

test("a legacy queue row with no texts array keeps its preview but is not editable", () => {
  const model = threadModel({
    queue: {
      depth: 1,
      revision: 2,
      ids: ["q1"],
      texts: undefined,
      preview: ["truncated…"],
    },
  });
  const rows = project({ model });
  expect(rows).toHaveLength(1);
  expect(rows[0]).toMatchObject({ id: "queue:q1", text: "truncated…", textComplete: false });
});

test("a legacy queue row with complete text merges with its one local queue row", () => {
  const model = threadModel({
    queue: {
      depth: 1,
      revision: 2,
      ids: ["q1"],
      texts: ["hello"],
      preview: ["hello"],
    },
  });
  const rows = project({
    model,
    outbox: [
      local("m1", "turn/queue", "hello", {
        optimisticDisplay: {
          method: "turn/queue",
          input: [{ type: "text", text: "hello", mentions: [{ kind: "skill", name: "review", offset: 0 }] }],
        },
      }),
    ],
  });
  expect(rows).toHaveLength(1);
  expect(rows[0]).toMatchObject({ id: "m1", text: "hello", queueEntryId: "q1" });
  expect(rows[0]?.mentions).toEqual([{ kind: "skill", name: "review", offset: 0 }]);
});

test("a Stop-canceled local row does not absorb an identical legacy queue row", () => {
  const canceled = local("m1", "turn/queue", "same", { state: "canceled", attempted: false });
  const model = threadModel({
    queue: {
      depth: 1,
      revision: 2,
      ids: ["q1"],
      texts: ["same"],
      preview: ["same"],
    },
  });
  const rows = project({ model, outbox: [canceled] });
  expect(rows).toHaveLength(2);
  expect(rows.some((row) => row.id === "m1" && row.state === "canceled")).toBe(true);
  expect(rows.some((row) => row.queueEntryId === "q1")).toBe(true);
});

test("a preview-only legacy row keeps its named skill marker", () => {
  const model = threadModel({
    queue: {
      depth: 1,
      revision: 1,
      ids: ["q1"],
      texts: undefined,
      preview: ["[1 skill]"],
      skillNames: [["pkg:probe"]],
    },
  });
  const rows = project({ model });
  expect(rows[0]?.displayText).toBe("[skill: pkg:probe]");
  expect(rows[0]?.textComplete).toBe(false);
});

test("a legacy queue preview never merges with a different full local text", () => {
  const model = threadModel({
    queue: {
      depth: 1,
      revision: 2,
      ids: ["q1"],
      texts: undefined,
      preview: ["hello"],
    },
  });
  const rows = project({ model, outbox: [local("m1", "turn/queue", "hello world")] });
  expect(rows.map((row) => row.id).sort()).toEqual(["m1", "queue:q1"]);
});

test("two identical legacy queue rows pair with two local rows in order", () => {
  const model = threadModel({
    queue: {
      depth: 2,
      revision: 2,
      ids: ["q1", "q2"],
      texts: ["hello", "hello"],
      preview: ["hello", "hello"],
    },
  });
  const rows = project({
    model,
    outbox: [local("m1", "turn/queue", "hello"), local("m2", "turn/queue", "hello")],
  });
  expect(rows).toHaveLength(2);
  expect(rows.map((row) => row.id).sort()).toEqual(["m1", "m2"]);
  expect(rows.every((row) => row.queueEntryId !== undefined)).toBe(true);
});

test("a legacy queue row merges with an accepted local copy", () => {
  const model = threadModel({
    queue: {
      depth: 1,
      revision: 1,
      ids: ["q1"],
      texts: ["hello"],
      preview: ["hello"],
    },
  });
  const rows = project({ model, optimistic: [accepted("m1", "turn/queue", "hello")] });
  expect(rows).toHaveLength(1);
  expect(rows[0]).toMatchObject({ id: "m1", state: "queued", queueEntryId: "q1" });
  expect(rows[0]?.optimistic).toBeDefined();
});

test("a legacy queue row merges with a blocked local row", () => {
  const model = threadModel({
    queue: {
      depth: 1,
      revision: 1,
      ids: ["q1"],
      texts: ["hello"],
      preview: ["hello"],
    },
  });
  const rows = project({
    model,
    outbox: [local("m1", "turn/queue", "hello", { state: "blockedUnknown" })],
  });
  expect(rows).toHaveLength(1);
  expect(rows[0]).toMatchObject({ id: "m1", state: "queued", queueEntryId: "q1" });
});

test("a legacy row with the same text but different image content stays separate", () => {
  const input: InputItem[] = [
    { type: "text", text: "hello" },
    { type: "image", mediaType: "image/png", data: "AAAA" },
  ];
  const record = outboxRecord({
    clientMutationId: "m1",
    targetRef: "ref_a",
    method: "turn/queue",
    payload: { ref: "ref_a", input, clientMutationId: "m1" },
    optimisticDisplay: { method: "turn/queue", input },
  });
  const model = threadModel({
    queue: {
      depth: 1,
      revision: 1,
      ids: ["q1"],
      texts: ["hello"],
      preview: ["hello"],
    },
  });
  const rows = project({ model, outbox: [record] });
  expect(rows.map((row) => row.id).sort()).toEqual(["m1", "queue:q1"]);
});

test("a retargeted Stop-canceled row stays visible under the displayed ref", () => {
  const record = local("m1", "turn/start", "hello", {
    state: "canceled",
    targetRef: "ref_b",
    displayedRef: "ref_a",
  });
  const rows = project({ outbox: [record] });
  expect(rows).toHaveLength(1);
  expect(rows[0]).toMatchObject({ id: "m1", ref: "ref_a", state: "canceled", text: "hello" });
});

test("a Retry-minted row is suppressed by its wire id", () => {
  const record = local("fresh", "turn/start", "hello", {
    payload: { ref: "ref_a", input: textInput("hello"), clientMutationId: "m1" },
  });
  expect(project({ outbox: [record], deliveredHere: new Set(["m1"]) })).toEqual([]);
});

test("a Retry-minted row merges with its wire-id queue row", () => {
  const record = local("fresh", "turn/start", "hello", {
    payload: { ref: "ref_a", input: textInput("hello"), clientMutationId: "m1" },
  });
  const model = threadModel({
    queue: {
      depth: 1,
      revision: 1,
      ids: ["q1"],
      clientMutationIds: ["m1"],
      texts: ["hello"],
      preview: ["hello"],
    },
  });
  const rows = project({ outbox: [record], model });
  expect(rows).toHaveLength(1);
  expect(rows[0]).toMatchObject({ id: "m1", text: "hello", queueEntryId: "q1" });
});

test("durable local and recovery states map to their pending states", () => {
  const rows = project({
    outbox: [
      local("m1", "turn/start", "waiting", { state: "submitting", attempted: false }),
      local("m2", "turn/start", "sending", { state: "submitting", attempted: true }),
      local("m3", "turn/start", "uncertain", { state: "blockedUnknown" }),
      local("m4", "turn/start", "canceled", { state: "canceled" }),
    ],
    recovery: [recovery("m5", "rejected"), recovery("m6", "orphaned")],
  });
  expect(Object.fromEntries(rows.map((row) => [row.id, row.state]))).toEqual({
    m1: "waiting",
    m2: "sending",
    m3: "uncertain",
    m4: "canceled",
    m5: "refused",
    m6: "orphaned",
  });
});

test("an edited recovery survives delivery of the attempt it was edited from", () => {
  const edited = recovery("m1", "rejected", "turn/start", "edited request", {
    compositionRevision: 1,
    composerText: "edited request",
  });
  const rows = project({ recovery: [edited], transcriptIds: new Set(["m1"]) });
  expect(rows).toHaveLength(1);
  expect(rows[0]?.text).toBe("edited request");
});

test("an edited recovery does not attach itself to the original authoritative row", () => {
  const original = local("m1", "turn/start", "original");
  const edited = recovery("m1", "rejected", "turn/start", "edited", { compositionRevision: 1 });
  const rows = project({ outbox: [original], recovery: [edited] });
  expect(rows).toHaveLength(2);
  expect(rows.find((row) => row.id === "m1")?.recovery).toBeUndefined();
  expect(rows.find((row) => row.recovery !== undefined)?.id).not.toBe("m1");
});

test("an edited recovery's text does not become the queued original's content", () => {
  const original = local("m1", "turn/queue", "original");
  const edited = recovery("m1", "rejected", "turn/queue", "edited", { compositionRevision: 1 });
  const model = threadModel({
    queue: {
      depth: 1,
      revision: 2,
      ids: ["q1"],
      clientMutationIds: ["m1"],
      texts: ["daemon"],
      preview: ["daemon"],
    },
  });
  const rows = project({ model, outbox: [original], recovery: [edited] });
  const originalRow = rows.find((row) => row.id === "m1");
  expect(originalRow?.text).toBe("original");
  expect(originalRow?.recovery).toBeUndefined();
});

test("a refused queue cancellation stays visible as a control failure", () => {
  const failed = recovery("m1", "rejected", "turn/cancelQueued", "", {
    recoveryReason: "the queue moved",
  });
  const rows = project({ recovery: [failed], transcriptIds: new Set(["m1"]) });
  expect(rows).toHaveLength(1);
  expect(rows[0]).toMatchObject({
    state: "refused",
    interrupt: true,
    controlFailure: "cancel",
    reason: "the queue moved",
  });
});

test("a queue resting after Stop projects as held", () => {
  const model = threadModel({
    status: { type: "idle" },
    queue: {
      depth: 1,
      revision: 2,
      ids: ["q1"],
      clientMutationIds: ["m1"],
      texts: ["hello"],
      preview: ["hello"],
    },
  });
  expect(project({ model })).toMatchObject([{ id: "m1", state: "held" }]);
});

test("a contentless drain keeps a nonempty display fallback", () => {
  const drain = local("m1", "turn/drainAsSteer", "", {
    payload: { ref: "ref_a", expectedQueueRevision: 1, clientMutationId: "m1" },
    optimisticDisplay: { method: "turn/drainAsSteer", input: [] },
  });
  const rows = project({ outbox: [drain] });
  expect(rows[0]?.displayText).toBe("[queued messages]");
});

test("image bytes and selections survive into the pending row", () => {
  const input: InputItem[] = [
    { type: "text", text: "look" },
    { type: "image", mediaType: "image/png", data: "AAAA", name: "a.png" },
    { type: "skill", name: "pkg:probe" },
    { type: "command", name: "compact" },
  ];
  const record = outboxRecord({
    clientMutationId: "m1",
    targetRef: "ref_a",
    method: "turn/start",
    payload: { ref: "ref_a", input, clientMutationId: "m1" },
    optimisticDisplay: { method: "turn/start", input },
  });
  const rows = project({ outbox: [record] });
  expect(rows[0]).toMatchObject({
    text: "look",
    displayText: "look [skill: pkg:probe] [command: compact]",
    images: [{ src: "data:image/png;base64,AAAA", name: "a.png" }],
    skillNames: ["pkg:probe"],
    commandNames: ["compact"],
  });
});

test("a skill-only row displays its marker while keeping the raw text empty", () => {
  const input: InputItem[] = [{ type: "skill", name: "pkg:probe" }];
  const record = outboxRecord({
    clientMutationId: "m1",
    targetRef: "ref_a",
    method: "turn/start",
    payload: { ref: "ref_a", input, clientMutationId: "m1" },
    optimisticDisplay: { method: "turn/start", input },
  });
  const rows = project({ outbox: [record] });
  expect(rows[0]).toMatchObject({
    text: "",
    displayText: "[skill: pkg:probe]",
    skillNames: ["pkg:probe"],
  });
});

test("a delivered identity recorded earlier cannot resurrect after a bounded read drops the item", () => {
  expect(project({ outbox: [local("m1", "turn/start")], deliveredHere: new Set(["m1"]) })).toEqual([]);
});

test("authoritative pending input replaces the local preview for the same identity", () => {
  const model = threadModel({ pendingMutations: [pending("m1", "turn/steer", "new")] });
  const rows = project({ outbox: [local("m1", "turn/steer", "old")], model });
  expect(rows).toHaveLength(1);
  expect(rows[0]).toMatchObject({ id: "m1", text: "new", state: "steering" });
});

test("known submission times order first, unknown times after them", () => {
  const model = threadModel({ pendingMutations: [pending("m3", "turn/steer", "foreign")] });
  const rows = project({
    model,
    outbox: [
      local("m2", "turn/start", "second", { createdAt: 20 }),
      local("m1", "turn/start", "first", { createdAt: 10 }),
    ],
  });
  expect(rows.map((row) => row.id)).toEqual(["m1", "m2", "m3"]);
});
