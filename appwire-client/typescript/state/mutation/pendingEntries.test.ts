// @vitest-environment node

import { expect, test } from "vitest";
import type { InputItem, PendingMutation } from "../../types.gen";
import type { PendingTurnEntry } from "./pendingEntries";
import {
  imagePlaceholder,
  normalizeText,
  ownPendingSend,
  ownPendingTurnInput,
  parseImagePlaceholder,
  pendingEntryPreview,
  queueEntryPreviewText,
  reconcilePendingEntries,
  truncateForDisplay,
} from "./pendingEntries";
import type { MutationOutboxRecord } from "./records";
import { threadModel as model } from "./testing";

test("normalizeText collapses whitespace and trims", () => {
  expect(normalizeText("fix   the\n\nlint   errors")).toBe("fix the lint errors");
  expect(normalizeText("  hello world  ")).toBe("hello world");
  expect(normalizeText("   \n\t  ")).toBe("");
});

test("imagePlaceholder names the image count", () => {
  expect(imagePlaceholder(0)).toBe("");
  expect(imagePlaceholder(1)).toBe("[image]");
  expect(imagePlaceholder(2)).toBe("[2 images]");
  expect(imagePlaceholder(8)).toBe("[8 images]");
});

test("parseImagePlaceholder reads the same grammar", () => {
  expect(parseImagePlaceholder(undefined)).toBe(0);
  expect(parseImagePlaceholder("hello")).toBe(0);
  expect(parseImagePlaceholder("[image]")).toBe(1);
  expect(parseImagePlaceholder(" [2 images] ")).toBe(2);
});

test("queueEntryPreviewText prefers normalized text and falls back to images", () => {
  expect(queueEntryPreviewText("  Fix the lint errors  ", 3)).toBe("Fix the lint errors");
  expect(queueEntryPreviewText("", 1)).toBe("[image]");
  expect(queueEntryPreviewText("   ", 2)).toBe("[2 images]");
  expect(queueEntryPreviewText("", 0)).toBe("");
});

test("truncateForDisplay returns text unchanged at or under the 140-char default cap", () => {
  expect(truncateForDisplay("a".repeat(140))).toBe("a".repeat(140));
  expect(truncateForDisplay("short text")).toBe("short text");
});

test("truncateForDisplay caps text over 140 chars to 140 chars plus a trailing ellipsis", () => {
  expect(truncateForDisplay("a".repeat(141))).toBe(`${"a".repeat(140)}…`);
});

test("truncateForDisplay accepts a custom max length", () => {
  expect(truncateForDisplay("abcdef", 4)).toBe("abcd…");
  expect(truncateForDisplay("abcd", 4)).toBe("abcd");
});

test("pendingEntryPreview preserves the entry's own line structure", () => {
  const entry = {
    text: "step 1:\n    do the thing\n\nstep 2:\n    do the next thing",
    imageCount: 0,
    skillNames: ["fixer"],
    commandNames: [] as string[],
  };
  expect(pendingEntryPreview(entry)).toBe("step 1:\n    do the thing\n\nstep 2:\n    do the next thing [skill: fixer]");
  // A blank text still composes to the image placeholder.
  expect(pendingEntryPreview({ ...entry, text: "   ", skillNames: [] })).toBe("");
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

// A retargeted resend targets the destination a peer chose while the pane
// that authored it stays its displayedRef (records.ts' field contract). The
// blocked, canceled and recovery selectors already match displayedRef for
// exactly that reason; the entries reconcile is the one remaining surface,
// and without the same widening an in-flight resend (attempted, submitting)
// vanishes from its author's pane for the whole send: the editable selector
// drops it (attempted), and the reconcile drops it too (targetRef is the
// destination). The author keeps seeing their undelivered work.
test("a retargeted in-flight resend stays in its author's pending entries", () => {
  const retargeted: MutationOutboxRecord = {
    ...outbox("mutation_retargeted"),
    targetRef: "local:resumed",
    displayedRef: "ref_a",
    attempted: true,
  };
  expect(reconcilePendingEntries("ref_a", [retargeted], model(), NOTHING_SUBMITTED_HERE, UNATTRIBUTED_ONLY)).toEqual([
    // The entry keeps its own destination as its ref: presentation follows
    // the displayed pane, routing keeps the record's targetRef.
    expect.objectContaining({ id: "mutation_retargeted", ref: "local:resumed", method: "send" }),
  ]);
});

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

test("pending input retains a command alongside the same-spelling skill across serialized recovery", () => {
  const input: InputItem[] = [
    { type: "skill", name: "probe" },
    { type: "command", name: "probe" },
    { type: "command", name: "probe" },
  ];
  const record: MutationOutboxRecord = {
    ...outbox("mutation_1", "turn/queue", ""),
    payload: { ref: "ref_a", input, clientMutationId: "mutation_1" },
    optimisticDisplay: { method: "turn/queue", input },
  };
  const recovered = JSON.parse(JSON.stringify(record)) as MutationOutboxRecord;
  expect(
    reconcilePendingEntries("ref_a", [recovered], model(), NOTHING_SUBMITTED_HERE, UNATTRIBUTED_ONLY),
  ).toMatchObject([{ skillNames: ["probe"], commandNames: ["probe"] }]);
});

// RoboRev round-1 M3: a mint's replacement carries the ORIGINAL wire id in
// its payload while owning a fresh storage key, so the durable record and
// the daemon's feed name the same composition under two ids. The pending
// projection is keyed by the effective wire identity, or the one composition
// projects twice - and a reflected feed id must retire the durable
// replacement that carries it.
function mintedReplacement(): MutationOutboxRecord {
  const record = outbox("mutation_20");
  return {
    ...record,
    payload: { ...record.payload, clientMutationId: "mutation_1" },
  };
}

test("a replacement row and its feed identity project as one pending entry", () => {
  const pending: PendingMutation = {
    clientMutationId: "mutation_1",
    method: "turn/steer",
    input: [{ type: "text", text: "hello" }],
    executionState: "claimed",
    projectionState: "reflected",
  };
  expect(
    reconcilePendingEntries(
      "ref_a",
      [mintedReplacement()],
      model({ pendingMutations: [pending] }),
      NOTHING_SUBMITTED_HERE,
      UNATTRIBUTED_ONLY,
    ),
  ).toHaveLength(1);
});

test("a reflected wire identity retires the replacement's pending entry", () => {
  const reflected = model();
  reflected.queue = { revision: 1, clientMutationIds: ["mutation_1"] };
  expect(
    reconcilePendingEntries("ref_a", [mintedReplacement()], reflected, NOTHING_SUBMITTED_HERE, UNATTRIBUTED_ONLY),
  ).toEqual([]);
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
    // createdAt: undefined pins no-inheritance from the
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

test("an authoritative queue identity is rendered once rather than duplicated as pending", () => {
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

test("an active promotion names its expected entry, while a canceled one names only confirmed lineage", () => {
  const activeBase = outbox("mutation_1", "turn/promoteQueuedAsSteer", "");
  const active: MutationOutboxRecord = {
    ...activeBase,
    payload: { ...activeBase.payload, expectedEntryId: "queue_3" },
  };
  const canceledBase = outbox("mutation_2", "turn/promoteQueuedAsSteer", "", "canceled");
  const canceled: MutationOutboxRecord = {
    ...canceledBase,
    payload: { ...canceledBase.payload, expectedEntryId: "queue_4" },
  };
  const confirmed: MutationOutboxRecord = {
    ...outbox("mutation_3", "turn/promoteQueuedAsSteer", "", "canceled"),
    queueEntryIds: ["queue_5"],
  };
  const entries = reconcilePendingEntries(
    "ref_a",
    [active, canceled, confirmed],
    model(),
    NOTHING_SUBMITTED_HERE,
    UNATTRIBUTED_ONLY,
  );
  expect(entries.map((entry) => [entry.id, entry.queueEntryId])).toEqual([
    ["mutation_1", "queue_3"],
    ["mutation_2", undefined],
    ["mutation_3", "queue_5"],
  ]);
});

test("a drain names the queue revision it steers with", () => {
  const drain = outbox("mutation_1", "turn/drainAsSteer", "");
  const record: MutationOutboxRecord = { ...drain, payload: { ...drain.payload, expectedQueueRevision: 7 } };
  const [entry] = reconcilePendingEntries("ref_a", [record], model(), NOTHING_SUBMITTED_HERE, UNATTRIBUTED_ONLY);
  expect(entry?.queueRevision).toBe(7);
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

function entry(over: Partial<PendingTurnEntry> = {}): PendingTurnEntry {
  return {
    id: "cmid-1",
    ref: "ref-1",
    method: "send",
    text: "hello",
    imageCount: 0,
    skillNames: [],
    state: "submitting",
    source: "outbox",
    fromThisClient: true,
    ...over,
  };
}

test("ownPendingSend counts this client's unreflected send, uncertain ones included", () => {
  expect(ownPendingSend([entry()])).toBe(true);
  expect(ownPendingSend([entry({ state: "blockedUnknown" })])).toBe(true);
  expect(ownPendingSend([entry({ state: "accepted", source: "authoritative" })])).toBe(true);
});

test("ownPendingSend ignores a send Stop canceled, another client's send, and other methods", () => {
  expect(ownPendingSend([entry({ state: "canceled" })])).toBe(false);
  expect(ownPendingSend([entry({ fromThisClient: false })])).toBe(false);
  expect(ownPendingSend([entry({ method: "queue" })])).toBe(false);
  expect(ownPendingSend([])).toBe(false);
  expect(ownPendingSend(null)).toBe(false);
  expect(ownPendingSend(undefined)).toBe(false);
});

test("ownPendingTurnInput counts this client's unsettled sends and queues", () => {
  for (const method of ["send", "queue"] as const) {
    for (const state of ["submitting", "blockedUnknown", "accepted", "claimed"] as const) {
      expect(ownPendingTurnInput([entry({ method, state })])).toBe(true);
    }
  }
});

test("ownPendingTurnInput ignores canceled and other clients' input, and every other method", () => {
  expect(ownPendingTurnInput([entry({ state: "canceled" })])).toBe(false);
  expect(ownPendingTurnInput([entry({ method: "queue", state: "canceled" })])).toBe(false);
  expect(ownPendingTurnInput([entry({ fromThisClient: false })])).toBe(false);
  expect(ownPendingTurnInput([entry({ method: "queue", fromThisClient: false })])).toBe(false);
  for (const method of ["steer", "drain", "promote"] as const) {
    expect(ownPendingTurnInput([entry({ method })])).toBe(false);
  }
  expect(ownPendingTurnInput([])).toBe(false);
  expect(ownPendingTurnInput(null)).toBe(false);
  expect(ownPendingTurnInput(undefined)).toBe(false);
});

test("ownPendingTurnInput sees one own unsettled entry in a mixed list", () => {
  expect(
    ownPendingTurnInput([
      entry({ id: "foreign-send", fromThisClient: false }),
      entry({ id: "own-steer", method: "steer" }),
      entry({ id: "own-canceled", state: "canceled" }),
      entry({ id: "own-queue", method: "queue" }),
    ]),
  ).toBe(true);
  expect(
    ownPendingTurnInput([
      entry({ id: "foreign-send", fromThisClient: false }),
      entry({ id: "own-steer", method: "steer" }),
      entry({ id: "own-canceled", state: "canceled" }),
    ]),
  ).toBe(false);
});

test("ownPendingSend keeps counting only this client's sends", () => {
  expect(ownPendingSend([entry({ method: "send", state: "claimed" })])).toBe(true);
  expect(ownPendingSend([entry({ method: "queue", state: "claimed" })])).toBe(false);
  expect(ownPendingSend([entry({ method: "steer", state: "claimed" })])).toBe(false);
  expect(ownPendingSend([entry({ method: "send", fromThisClient: false })])).toBe(false);
});
