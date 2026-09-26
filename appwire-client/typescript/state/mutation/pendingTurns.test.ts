// @vitest-environment node

import { describe, expect, test } from "vitest";
import type { ItemModel, ThreadModel } from "../../model";
import type { PendingTurnsDraftPort, PendingTurnsThreadsPort } from "./pendingTurns";
import { awaitingFirstFrameSend, blockedEntries, createPendingTurnsStore, recoveryEntries } from "./pendingTurns";
import type { ClientIdentity, MutationOptimisticRecord } from "./records";
import { outboxRecord, recoveryRecord, threadModel } from "./testing";

// A fake ClientIdentity's isOwnMutationRecord half, over a fixed id rather
// than createClientIdentity's own storage/random-source machinery - that
// machinery is records.test.ts's oracle, not this module's.
function fakeIdentity(ownId: string): Pick<ClientIdentity, "isOwnMutationRecord"> {
  return {
    isOwnMutationRecord: (record) => record.originClientId === undefined || record.originClientId === ownId,
  };
}

const UNATTRIBUTED_ONLY_IDENTITY: Pick<ClientIdentity, "isOwnMutationRecord"> = {
  isOwnMutationRecord: (record) => record.originClientId === undefined,
};

function optimisticRecord(overrides: Partial<MutationOptimisticRecord> = {}): MutationOptimisticRecord {
  return {
    version: 1,
    clientMutationId: "cmid-2",
    targetRef: "ref-a",
    method: "turn/start",
    payload: {},
    attachments: [],
    optimisticDisplay: null,
    intentSequence: 0,
    createdAt: 0,
    state: "accepted",
    ...overrides,
  };
}

function userMessageItem(overrides: Partial<ItemModel> = {}): ItemModel {
  return { id: "item-1", turnId: "turn_1", type: "userMessage", text: "hello", ...overrides };
}

function fakeThreadsPort(models: Record<string, ThreadModel | undefined> = {}): PendingTurnsThreadsPort {
  return { getThreadModel: (ref) => models[ref] };
}

interface FakeDraftState {
  revision: number;
  text: string;
  skillNames: string[];
  cleared: string[];
}

function fakeDraftPort(initial: Partial<FakeDraftState> = {}): PendingTurnsDraftPort & { state: FakeDraftState } {
  const state: FakeDraftState = { revision: 0, text: "", skillNames: [], cleared: [], ...initial };
  return {
    state,
    readDraftRevision: () => state.revision,
    readComposerDraft: () => ({ text: state.text, skillNames: state.skillNames }),
    clearDraft: (ref) => {
      state.cleared.push(ref);
    },
  };
}

describe("createPendingTurnsStore", () => {
  test("starts with empty state", () => {
    const store = createPendingTurnsStore({
      threads: fakeThreadsPort(),
      draft: fakeDraftPort(),
      identity: UNATTRIBUTED_ONLY_IDENTITY,
    });
    const state = store.getState();
    expect(state.outbox.size).toBe(0);
    expect(state.optimistic.size).toBe(0);
    expect(state.recovery.size).toBe(0);
    expect(state.submittingRefs.size).toBe(0);
    expect(state.submittedHere.size).toBe(0);
  });

  describe("recordSubmittedHere", () => {
    test("discovers this client's own outbox and optimistic ids, skipping foreign and already-known ones", () => {
      const store = createPendingTurnsStore({
        threads: fakeThreadsPort(),
        draft: fakeDraftPort(),
        identity: fakeIdentity("client-x"),
      });
      const own = outboxRecord({ clientMutationId: "own-1", originClientId: "client-x" });
      const foreign = outboxRecord({ clientMutationId: "foreign-1", originClientId: "client-y" });
      const unattributed = optimisticRecord({ clientMutationId: "unattributed-1", originClientId: undefined });

      store.recordSubmittedHere({ outbox: [own, foreign], optimistic: [unattributed] });

      const submittedHere = store.getState().submittedHere;
      expect(submittedHere.has("own-1")).toBe(true);
      expect(submittedHere.has("unattributed-1")).toBe(true);
      expect(submittedHere.has("foreign-1")).toBe(false);
    });

    test("is a no-op when every discovered id is already known", () => {
      const store = createPendingTurnsStore({
        threads: fakeThreadsPort(),
        draft: fakeDraftPort(),
        identity: fakeIdentity("client-x"),
      });
      const own = outboxRecord({ clientMutationId: "own-1", originClientId: "client-x" });
      store.recordSubmittedHere({ outbox: [own], optimistic: [] });
      const stateAfterFirst = store.getState();

      store.recordSubmittedHere({ outbox: [own], optimistic: [] });
      expect(store.getState()).toBe(stateAfterFirst); // no new setState published
    });

    test("writes the id -> createdAt map entry from the record's createdAt", () => {
      const store = createPendingTurnsStore({
        threads: fakeThreadsPort(),
        draft: fakeDraftPort(),
        identity: fakeIdentity("client-x"),
      });
      const own = outboxRecord({ clientMutationId: "own-1", originClientId: "client-x", createdAt: 1234 });
      store.recordSubmittedHere({ outbox: [own], optimistic: [] });
      expect(store.getState().submittedHere.get("own-1")).toBe(1234);
    });

    test("never prunes: the map outlives the settle that deletes the durable record", () => {
      const store = createPendingTurnsStore({
        threads: fakeThreadsPort(),
        draft: fakeDraftPort(),
        identity: fakeIdentity("client-x"),
      });
      const own = outboxRecord({ clientMutationId: "own-1", originClientId: "client-x", createdAt: 1234 });
      store.recordSubmittedHere({ outbox: [own], optimistic: [] });
      // The settle deletes the durable record out of the projection...
      store.setState({ outbox: new Map(), optimistic: new Map() });
      // ...and the carrier still holds the createdAt a reload-after-settle cannot
      // re-discover (steering-ghost spec §4).
      expect(store.getState().submittedHere.get("own-1")).toBe(1234);
    });
  });

  describe("beginSubmission / endSubmission", () => {
    test("guards a second call for the same ref while one is in flight, and endSubmission releases it", () => {
      const store = createPendingTurnsStore({
        threads: fakeThreadsPort(),
        draft: fakeDraftPort(),
        identity: UNATTRIBUTED_ONLY_IDENTITY,
      });
      expect(store.beginSubmission("ref-a")).toBe(true);
      expect(store.getState().submittingRefs.has("ref-a")).toBe(true);
      expect(store.beginSubmission("ref-a")).toBe(false);

      store.endSubmission("ref-a");
      expect(store.getState().submittingRefs.has("ref-a")).toBe(false);
      expect(store.beginSubmission("ref-a")).toBe(true);
    });

    test("tracks independent refs independently", () => {
      const store = createPendingTurnsStore({
        threads: fakeThreadsPort(),
        draft: fakeDraftPort(),
        identity: UNATTRIBUTED_ONLY_IDENTITY,
      });
      expect(store.beginSubmission("ref-a")).toBe(true);
      expect(store.beginSubmission("ref-b")).toBe(true);
      store.endSubmission("ref-a");
      expect(store.getState().submittingRefs.has("ref-a")).toBe(false);
      expect(store.getState().submittingRefs.has("ref-b")).toBe(true);
    });

    test("is a no-op, safe to call, for a ref with no submission in flight", () => {
      const store = createPendingTurnsStore({
        threads: fakeThreadsPort(),
        draft: fakeDraftPort(),
        identity: UNATTRIBUTED_ONLY_IDENTITY,
      });
      const stateBefore = store.getState();
      store.endSubmission("ref-never-started");
      expect(store.getState()).toBe(stateBefore); // no new setState published
    });
  });

  describe("settleSubmittedDraft", () => {
    test("clears the draft and returns true when the revision, text and selections all still match", () => {
      const draft = fakeDraftPort({ revision: 1, text: "hello", skillNames: ["a"] });
      const store = createPendingTurnsStore({
        threads: fakeThreadsPort(),
        draft,
        identity: UNATTRIBUTED_ONLY_IDENTITY,
      });
      const { cleared, draftUnchanged } = store.settleSubmittedDraft("ref-a", {
        draftRevisionAtStart: 1,
        text: "hello",
        skillNames: ["a"],
      });
      expect(cleared).toBe(true);
      expect(draftUnchanged).toBe(true);
      expect(draft.state.cleared).toEqual(["ref-a"]);
    });

    test("does not clear the draft when the revision changed (edited since submit started)", () => {
      const draft = fakeDraftPort({ revision: 2, text: "hello", skillNames: [] });
      const store = createPendingTurnsStore({
        threads: fakeThreadsPort(),
        draft,
        identity: UNATTRIBUTED_ONLY_IDENTITY,
      });
      const { cleared, draftUnchanged } = store.settleSubmittedDraft("ref-a", {
        draftRevisionAtStart: 1,
        text: "hello",
        skillNames: [],
      });
      expect(cleared).toBe(false);
      expect(draftUnchanged).toBe(false);
      expect(draft.state.cleared).toEqual([]);
    });

    test("does not clear the draft when the stored text no longer matches what was submitted, but the revision alone is still unchanged", () => {
      const draft = fakeDraftPort({ revision: 1, text: "edited", skillNames: [] });
      const store = createPendingTurnsStore({
        threads: fakeThreadsPort(),
        draft,
        identity: UNATTRIBUTED_ONLY_IDENTITY,
      });
      const { cleared, draftUnchanged } = store.settleSubmittedDraft("ref-a", {
        draftRevisionAtStart: 1,
        text: "original",
        skillNames: [],
      });
      expect(cleared).toBe(false);
      expect(draftUnchanged).toBe(true);
      expect(draft.state.cleared).toEqual([]);
    });

    test("does not clear the draft when the selected skills no longer match, but the revision alone is still unchanged", () => {
      const draft = fakeDraftPort({ revision: 1, text: "hello", skillNames: ["a", "b"] });
      const store = createPendingTurnsStore({
        threads: fakeThreadsPort(),
        draft,
        identity: UNATTRIBUTED_ONLY_IDENTITY,
      });
      const { cleared, draftUnchanged } = store.settleSubmittedDraft("ref-a", {
        draftRevisionAtStart: 1,
        text: "hello",
        skillNames: ["a"],
      });
      expect(cleared).toBe(false);
      expect(draftUnchanged).toBe(true);
      expect(draft.state.cleared).toEqual([]);
    });
  });

  describe("pendingTurnEntries", () => {
    test("reconciles this store's outbox and optimistic records against the injected thread model", () => {
      const model = threadModel({ turns: [], pendingMutations: [] });
      const store = createPendingTurnsStore({
        threads: fakeThreadsPort({ "ref-a": model }),
        draft: fakeDraftPort(),
        identity: UNATTRIBUTED_ONLY_IDENTITY,
      });
      store.setState({
        outbox: new Map([["cmid-1", outboxRecord({ clientMutationId: "cmid-1", method: "turn/queue" })]]),
      });

      const entries = store.pendingTurnEntries("ref-a");
      expect(entries).toHaveLength(1);
      expect(entries[0]?.id).toBe("cmid-1");
      expect(entries[0]?.method).toBe("queue");
    });

    test("filters by method when one is given", () => {
      const model = threadModel({ turns: [], pendingMutations: [] });
      const store = createPendingTurnsStore({
        threads: fakeThreadsPort({ "ref-a": model }),
        draft: fakeDraftPort(),
        identity: UNATTRIBUTED_ONLY_IDENTITY,
      });
      store.setState({
        outbox: new Map([
          ["cmid-1", outboxRecord({ clientMutationId: "cmid-1", method: "turn/queue" })],
          ["cmid-2", outboxRecord({ clientMutationId: "cmid-2", method: "turn/start" })],
        ]),
      });

      expect(store.pendingTurnEntries("ref-a", "queue").map((entry) => entry.id)).toEqual(["cmid-1"]);
      expect(store.pendingTurnEntries("ref-a", "send").map((entry) => entry.id)).toEqual(["cmid-2"]);
    });

    test("reads the thread model through the injected port, not a parameter", () => {
      const modelA = threadModel({ turns: [], pendingMutations: [] });
      const store = createPendingTurnsStore({
        threads: fakeThreadsPort({ "ref-a": modelA, "ref-b": undefined }),
        draft: fakeDraftPort(),
        identity: UNATTRIBUTED_ONLY_IDENTITY,
      });
      // No records target ref-b; an absent thread model must not throw.
      expect(store.pendingTurnEntries("ref-b")).toEqual([]);
    });

    // The store's central invariant: recordSubmittedHere's discovery is what
    // lets pendingTurnEntries still answer "mine" for a mutation the
    // authoritative projection now describes, once the durable outbox/
    // optimistic record behind it is gone (settled away by the hydrate that
    // reported it) - see reconcilePendingEntries's own comment on
    // submittedHere.
    test("a record recordSubmittedHere discovered still reads as fromThisClient once its durable record is gone", () => {
      const store = createPendingTurnsStore({
        threads: fakeThreadsPort({
          "ref-a": threadModel({
            turns: [],
            pendingMutations: [
              {
                clientMutationId: "cmid-1",
                method: "turn/start",
                input: [{ type: "text", text: "hello" }],
                executionState: "accepted",
                projectionState: "pending",
              },
            ],
          }),
        }),
        draft: fakeDraftPort(),
        identity: fakeIdentity("client-x"),
      });
      store.recordSubmittedHere({
        outbox: [outboxRecord({ clientMutationId: "cmid-1", originClientId: "client-x" })],
        optimistic: [],
      });
      // recordSubmittedHere only scans the snapshot it is handed; this store's
      // own outbox/optimistic never held the record, matching a durable read
      // that already settled it away.
      expect(store.getState().outbox.size).toBe(0);
      expect(store.getState().optimistic.size).toBe(0);

      expect(store.pendingTurnEntries("ref-a")).toEqual([
        expect.objectContaining({ id: "cmid-1", source: "authoritative", fromThisClient: true }),
      ]);
    });
  });
});

describe("awaitingFirstFrameSend", () => {
  test("derives from the identified active turn and needs no confirmation timer", () => {
    const model = threadModel({
      activeTurnId: "turn_1",
      turns: [{ id: "turn_1", status: "inProgress", items: [userMessageItem({ clientMutationId: "mutation_1" })] }],
    });
    expect(awaitingFirstFrameSend(model)).toBe(true);
  });

  test("an authoritative assistant frame retires first-frame state by model identity", () => {
    const model = threadModel({
      activeTurnId: "turn_1",
      turns: [
        {
          id: "turn_1",
          status: "inProgress",
          items: [
            userMessageItem({ clientMutationId: "mutation_1" }),
            { id: "item-2", turnId: "turn_1", type: "agentMessage", text: "working" },
          ],
        },
      ],
    });
    expect(awaitingFirstFrameSend(model)).toBe(false);
  });

  test("returns false with no active turn or no model at all", () => {
    expect(awaitingFirstFrameSend(threadModel({ activeTurnId: undefined, turns: [] }))).toBe(false);
    expect(awaitingFirstFrameSend(undefined)).toBe(false);
  });
});

describe("recoveryEntries", () => {
  test("returns ref's recovery records ordered by intent sequence, oldest first", () => {
    const recovery = new Map([
      ["cmid-2", recoveryRecord({ clientMutationId: "cmid-2", targetRef: "ref-a", intentSequence: 2 })],
      ["cmid-1", recoveryRecord({ clientMutationId: "cmid-1", targetRef: "ref-a", intentSequence: 1 })],
      ["cmid-3", recoveryRecord({ clientMutationId: "cmid-3", targetRef: "ref-b", intentSequence: 0 })],
    ]);
    expect(recoveryEntries(recovery, "ref-a").map((record) => record.clientMutationId)).toEqual(["cmid-1", "cmid-2"]);
  });
});

describe("blockedEntries", () => {
  test("returns ref's blockedUnknown outbox records ordered by intent sequence, oldest first", () => {
    const outbox = new Map([
      [
        "cmid-2",
        outboxRecord({ clientMutationId: "cmid-2", targetRef: "ref-a", state: "blockedUnknown", intentSequence: 2 }),
      ],
      [
        "cmid-1",
        outboxRecord({ clientMutationId: "cmid-1", targetRef: "ref-a", state: "blockedUnknown", intentSequence: 1 }),
      ],
      [
        "cmid-3",
        outboxRecord({ clientMutationId: "cmid-3", targetRef: "ref-a", state: "submitting", intentSequence: 0 }),
      ],
    ]);
    expect(blockedEntries(outbox, "ref-a").map((record) => record.clientMutationId)).toEqual(["cmid-1", "cmid-2"]);
  });
});
