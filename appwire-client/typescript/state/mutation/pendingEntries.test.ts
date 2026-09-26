// @vitest-environment node

import { expect, test } from "vitest";
import type { InputItem, PendingMutation } from "../../types.gen";
import { queueEntryPreviewText, reconcilePendingEntries } from "./pendingEntries";
import type { MutationOutboxRecord } from "./records";
import { threadModel as model } from "./testing";

test("queueEntryPreviewText still returns the empty string for contentless input", () => {
  expect(queueEntryPreviewText("", 0)).toBe("");
  expect(queueEntryPreviewText("   ", 0)).toBe("");
});

function outbox(
  clientMutationId: string,
  method = "turn/start",
  text = "hello",
  state: MutationOutboxRecord["state"] = "submitting",
): MutationOutboxRecord {
  const input = [{ type: "text", text }];
  return {
    version: 1,
    clientMutationId,
    intentSequence: Number(clientMutationId.replace(/\D/g, "")) || 1,
    createdAt: 1,
    state,
    targetRef: "ref_a",
    threadId: "thread_a",
    method,
    payload: { ref: "ref_a", input, clientMutationId },
    attachments: [],
    optimisticDisplay: { method, input },
  };
}

// The ids this client's own durable projection has held. Empty unless a test
// is specifically about a submission whose local record is already gone.
const NOTHING_SUBMITTED_HERE: ReadonlyMap<string, number> = new Map();

// The safe unattributed-only answer a caller with no ClientIdentity instance
// handy still gets. Every outbox() fixture below leaves originClientId unset,
// so this is indistinguishable from a real identity's rule for them; the
// "names this client" branch gets its own predicate and test further down.
const UNATTRIBUTED_ONLY = (record: { originClientId?: string }): boolean => record.originClientId === undefined;

test("two same-text outbox records remain distinct by client mutation identity", () => {
  expect(
    reconcilePendingEntries(
      "ref_a",
      [outbox("mutation_1"), outbox("mutation_2")],
      model(),
      NOTHING_SUBMITTED_HERE,
      UNATTRIBUTED_ONLY,
    ),
  ).toMatchObject([
    { id: "mutation_1", text: "hello", source: "outbox" },
    { id: "mutation_2", text: "hello", source: "outbox" },
  ]);
});

// A queued submission can carry ONLY skills, with no typed prose. The
// canonical skill name is then the entry's entire content, so the pending
// projection has to keep it - otherwise the row on screen is blank until the
// daemon's own queue record arrives. Collection goes through the project's one
// canonicalizer: a padded or duplicated raw item collapses to the same name.
test("a skill-only pending entry carries its canonical skill selection", () => {
  const skillInput: InputItem[] = [
    { type: "skill", name: " pkg:probe " },
    { type: "skill", name: "pkg:probe" },
  ];
  const record: MutationOutboxRecord = {
    ...outbox("mutation_1", "turn/queue", ""),
    payload: { ref: "ref_a", input: skillInput, clientMutationId: "mutation_1" },
    optimisticDisplay: { method: "turn/queue", input: skillInput },
  };
  expect(reconcilePendingEntries("ref_a", [record], model(), NOTHING_SUBMITTED_HERE, UNATTRIBUTED_ONLY)).toEqual([
    expect.objectContaining({ id: "mutation_1", text: "", imageCount: 0, skillNames: ["pkg:probe"] }),
  ]);
});

test("the authoritative pending projection replaces the same outbox identity", () => {
  const pending: PendingMutation = {
    clientMutationId: "mutation_1",
    method: "turn/steer",
    input: [{ type: "text", text: "keep steering" }],
    executionState: "claimed",
    projectionState: "reflected",
  };
  expect(
    reconcilePendingEntries(
      "ref_a",
      [outbox("mutation_1", "turn/steer", "keep steering")],
      model({ pendingMutations: [pending] }),
      NOTHING_SUBMITTED_HERE,
      UNATTRIBUTED_ONLY,
    ),
  ).toEqual([
    expect.objectContaining({
      id: "mutation_1",
      method: "steer",
      state: "claimed",
      source: "authoritative",
    }),
  ]);
});

// `source` says which projection is DESCRIBING the row and flips to
// "authoritative" the moment a hydrate reports the same clientMutationId.
// Whose submission it is cannot flip with it - the id is the same submission
// either way - and that is the question send/queue routing asks (tier 6 of
// deriveSendQueueAvailability takes strictly this client's own sends).
test("a locally submitted mutation stays this client's own once the authoritative projection describes it", () => {
  const pending: PendingMutation = {
    clientMutationId: "mutation_1",
    method: "turn/start",
    input: [{ type: "text", text: "hello" }],
    executionState: "accepted",
    projectionState: "pending",
  };
  expect(
    reconcilePendingEntries(
      "ref_a",
      [outbox("mutation_1")],
      model({ pendingMutations: [pending] }),
      new Map(),
      UNATTRIBUTED_ONLY,
    ),
  ).toEqual([expect.objectContaining({ id: "mutation_1", source: "authoritative", fromThisClient: true })]);
});

// The hydrate that re-describes the row also settles the durable record behind
// it (the host's reconcileIdentities -> settleApplied), so the outbox list is
// empty by the time the next message is composed. The submitted-here identities
// are what carries the answer across that deletion.
test("a mutation this client submitted stays its own after its durable record is settled away", () => {
  const pending: PendingMutation = {
    clientMutationId: "mutation_1",
    method: "turn/start",
    input: [{ type: "text", text: "hello" }],
    executionState: "accepted",
    projectionState: "pending",
  };
  expect(
    reconcilePendingEntries(
      "ref_a",
      [],
      model({ pendingMutations: [pending] }),
      new Map([["mutation_1", 1]]),
      UNATTRIBUTED_ONLY,
    ),
  ).toEqual([expect.objectContaining({ id: "mutation_1", source: "authoritative", fromThisClient: true })]);
});

test("a pending mutation this client never submitted is not its own", () => {
  const pending: PendingMutation = {
    clientMutationId: "mutation_from_another_client",
    method: "turn/start",
    input: [{ type: "text", text: "someone else's message" }],
    executionState: "accepted",
    projectionState: "pending",
  };
  expect(
    reconcilePendingEntries(
      "ref_a",
      [],
      model({ pendingMutations: [pending] }),
      new Map([["mutation_1", 1]]),
      UNATTRIBUTED_ONLY,
    ),
    // createdAt: undefined pins spec §4's no-inheritance from the
    // authoritative side: the foreign id lands in the unknown-createdAt
    // bucket even beside a populated submittedHere map - nothing lets
    // mutation_1's timestamp leak into another client's entry.
  ).toEqual([
    expect.objectContaining({ id: "mutation_from_another_client", fromThisClient: false, createdAt: undefined }),
  ]);
});

test("a foreign durable timestamp survives authoritative replacement", () => {
  const record: MutationOutboxRecord = {
    ...outbox("mutation_1", "turn/steer"),
    createdAt: 1234,
    originClientId: "another-client",
  };
  const pending: PendingMutation = {
    clientMutationId: "mutation_1",
    method: "turn/steer",
    input: [{ type: "text", text: "hello" }],
    executionState: "accepted",
    projectionState: "pending",
  };
  expect(
    reconcilePendingEntries(
      "ref_a",
      [record],
      model({ pendingMutations: [pending] }),
      NOTHING_SUBMITTED_HERE,
      UNATTRIBUTED_ONLY,
    ),
  ).toEqual([expect.objectContaining({ id: "mutation_1", createdAt: 1234, fromThisClient: false })]);
});

test("a transcript item with the identity removes the optimistic projection regardless of text", () => {
  expect(
    reconcilePendingEntries(
      "ref_a",
      [outbox("mutation_1")],
      model({
        turns: [
          {
            id: "turn_1",
            status: "inProgress",
            items: [
              {
                id: "item_1",
                turnId: "turn_1",
                type: "userMessage",
                text: "server normalized text",
                clientMutationId: "mutation_1",
              },
            ],
          },
        ],
      }),
      NOTHING_SUBMITTED_HERE,
      UNATTRIBUTED_ONLY,
    ),
  ).toEqual([]);
});

test("an authoritative queue identity is rendered by QueueStrip rather than duplicated as pending", () => {
  expect(
    reconcilePendingEntries(
      "ref_a",
      [outbox("mutation_1", "turn/queue")],
      model({ queue: { revision: 1, clientMutationIds: ["mutation_1"] } }),
      NOTHING_SUBMITTED_HERE,
      UNATTRIBUTED_ONLY,
    ),
  ).toEqual([]);
});

test("blockedUnknown remains visible and is not converted by elapsed time", () => {
  expect(
    reconcilePendingEntries(
      "ref_a",
      [outbox("mutation_1", "turn/start", "uncertain", "blockedUnknown")],
      model(),
      NOTHING_SUBMITTED_HERE,
      UNATTRIBUTED_ONLY,
    ),
  ).toEqual([
    expect.objectContaining({
      id: "mutation_1",
      state: "blockedUnknown",
      text: "uncertain",
    }),
  ]);
});

// A real ClientIdentity's isOwnMutationRecord answers true for BOTH an
// unattributed record and one whose originClientId names this client
// (records.ts:158-160) - the branch UNATTRIBUTED_ONLY above never exercises.
// An outbox record attributed to this client's own id must reach the entry
// as fromThisClient: true, same as an unattributed one; a record attributed
// to a different id must not.
test("an outbox record whose originClientId names this client is its own", () => {
  const ownedByThisClient = (record: { originClientId?: string }): boolean =>
    record.originClientId === undefined || record.originClientId === "this-client";
  const ownRecord: MutationOutboxRecord = { ...outbox("mutation_1"), originClientId: "this-client" };
  const otherRecord: MutationOutboxRecord = { ...outbox("mutation_2"), originClientId: "another-client" };
  expect(
    reconcilePendingEntries("ref_a", [ownRecord, otherRecord], model(), NOTHING_SUBMITTED_HERE, ownedByThisClient),
  ).toEqual([
    expect.objectContaining({ id: "mutation_1", fromThisClient: true }),
    expect.objectContaining({ id: "mutation_2", fromThisClient: false }),
  ]);
});

test("promote maps to its own PendingMethod, not folded into steer", () => {
  expect(
    reconcilePendingEntries(
      "ref_a",
      [outbox("mutation_1", "turn/promoteQueuedAsSteer", "hello")],
      model(),
      NOTHING_SUBMITTED_HERE,
      UNATTRIBUTED_ONLY,
    ),
  ).toEqual([expect.objectContaining({ id: "mutation_1", method: "promote", text: "hello" })]);
});

test("a promote's display input previews in the entry", () => {
  const record: MutationOutboxRecord = {
    ...outbox("mutation_1", "turn/promoteQueuedAsSteer", ""),
    optimisticDisplay: { method: "turn/promoteQueuedAsSteer", input: [{ type: "text", text: "promoted body" }] },
  };
  expect(reconcilePendingEntries("ref_a", [record], model(), NOTHING_SUBMITTED_HERE, UNATTRIBUTED_ONLY)).toEqual([
    expect.objectContaining({ id: "mutation_1", method: "promote", text: "promoted body" }),
  ]);
});

test("sorts known-createdAt entries first, ascending, with unknown-createdAt after them", () => {
  const late = { ...outbox("mutation_30", "turn/steer", "late"), createdAt: 30 };
  const early = { ...outbox("mutation_10", "turn/steer", "early"), createdAt: 10 };
  const mid = { ...outbox("mutation_20", "turn/steer", "mid"), createdAt: 20 };
  const unknown: PendingMutation = {
    clientMutationId: "mutation_remote",
    method: "turn/steer",
    input: [{ type: "text", text: "remote client's steer" }],
    executionState: "accepted",
    projectionState: "pending",
  };
  const entries = reconcilePendingEntries(
    "ref_a",
    [late, early, mid],
    model({ pendingMutations: [unknown] }),
    NOTHING_SUBMITTED_HERE,
    UNATTRIBUTED_ONLY,
  );
  expect(entries.map((entry) => entry.id)).toEqual(["mutation_10", "mutation_20", "mutation_30", "mutation_remote"]);
});

test("the map carrier hands an authoritative entry its createdAt after the settle removed the record", () => {
  const pending: PendingMutation = {
    clientMutationId: "mutation_1",
    method: "turn/steer",
    input: [{ type: "text", text: "hello" }],
    executionState: "accepted",
    projectionState: "pending",
  };
  expect(
    reconcilePendingEntries(
      "ref_a",
      [],
      model({ pendingMutations: [pending] }),
      new Map([["mutation_1", 42]]),
      UNATTRIBUTED_ONLY,
    ),
  ).toEqual([expect.objectContaining({ id: "mutation_1", createdAt: 42, fromThisClient: true })]);
});
