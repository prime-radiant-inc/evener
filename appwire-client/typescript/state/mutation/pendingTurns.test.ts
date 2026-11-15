// @vitest-environment node

import { describe, expect, test } from "vitest";
import type { ItemModel, ThreadModel } from "../../model";
import { ownPendingSend } from "./pendingEntries";
import type { PendingTurnsDraftPort, PendingTurnsThreadsPort } from "./pendingTurns";
import {
  awaitingFirstFrameSend,
  blockedEntries,
  blockedEntriesIncludingDisplayed,
  createPendingTurnsStore,
  recoveryEntries,
} from "./pendingTurns";
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
  commandNames: string[];
  cleared: string[];
}

function fakeDraftPort(initial: Partial<FakeDraftState> = {}): PendingTurnsDraftPort & { state: FakeDraftState } {
  const state: FakeDraftState = { revision: 0, text: "", skillNames: [], commandNames: [], cleared: [], ...initial };
  return {
    state,
    readDraftRevision: () => state.revision,
    readComposerDraft: () => ({ text: state.text, skillNames: state.skillNames, commandNames: state.commandNames }),
    clearDraft: (ref) => {
      state.cleared.push(ref);
    },
  };
}

describe("createPendingTurnsStore", () => {
  test("settling a send retains a draft whose command selection changed but text and skills match", () => {
    const draft = fakeDraftPort({ text: "DATA_318", skillNames: ["probe"], commandNames: ["other"] });
    const store = createPendingTurnsStore({ threads: fakeThreadsPort(), draft, identity: UNATTRIBUTED_ONLY_IDENTITY });
    const submitted = { draftRevisionAtStart: 0, text: "DATA_318", skillNames: ["probe"], commandNames: ["probe"] };
    expect(store.settleSubmittedDraft("ref-a", submitted).cleared).toBe(false);
    expect(draft.state.cleared).toEqual([]);
    draft.state.commandNames = ["probe"];
    expect(store.settleSubmittedDraft("ref-a", submitted).cleared).toBe(true);
    expect(draft.state.cleared).toEqual(["ref-a"]);
  });

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
      // re-discover.
      expect(store.getState().submittedHere.get("own-1")).toBe(1234);
    });
  });

  describe("recordDirectSend", () => {
    test("claims an id with no durable record, so the daemon's pending projection reads as this client's", () => {
      const store = createPendingTurnsStore({
        threads: fakeThreadsPort({
          "ref-a": threadModel({
            turns: [],
            pendingMutations: [
              {
                clientMutationId: "cmid-direct",
                method: "turn/start",
                input: [{ type: "text", text: "sent with no durable row" }],
                executionState: "claimed",
                projectionState: "pending",
              },
            ],
          }),
        }),
        draft: fakeDraftPort(),
        identity: fakeIdentity("client-x"),
      });

      store.recordDirectSend("cmid-direct", 9876);

      // Nothing durable exists to describe the send: the direct fallback wrote
      // no outbox or optimistic row.
      expect(store.getState().outbox.size).toBe(0);
      expect(store.getState().optimistic.size).toBe(0);
      expect(store.getState().submittedHere.get("cmid-direct")).toBe(9876);
      expect(store.pendingTurnEntries("ref-a")).toEqual([
        expect.objectContaining({ id: "cmid-direct", source: "authoritative", fromThisClient: true, createdAt: 9876 }),
      ]);
      expect(ownPendingSend(store.pendingTurnEntries("ref-a"))).toBe(true);
    });

    test("is a no-op when the id is already known", () => {
      const store = createPendingTurnsStore({
        threads: fakeThreadsPort(),
        draft: fakeDraftPort(),
        identity: fakeIdentity("client-x"),
      });
      store.recordDirectSend("cmid-direct", 9876);
      const stateAfterFirst = store.getState();

      store.recordDirectSend("cmid-direct", 1234);

      expect(store.getState()).toBe(stateAfterFirst); // no new setState published
      expect(store.getState().submittedHere.get("cmid-direct")).toBe(9876);
    });
  });

  describe("projectSnapshot", () => {
    // A durable read of ref-a as storage hands it back: new objects on every
    // call, with the same content.
    function readRefA() {
      return {
        outbox: [
          outboxRecord({
            clientMutationId: "out-1",
            targetRef: "ref-a",
            payload: { input: [{ type: "text", text: "queued" }] },
          }),
        ],
        optimistic: [optimisticRecord({ clientMutationId: "opt-1", targetRef: "ref-a" })],
        recovery: [recoveryRecord({ clientMutationId: "rec-1", targetRef: "ref-a", recoveryReason: "refused" })],
      };
    }

    test("is a no-op when a fresh read holds what the store already does", () => {
      const store = createPendingTurnsStore({
        threads: fakeThreadsPort(),
        draft: fakeDraftPort(),
        identity: fakeIdentity("client-x"),
      });
      store.projectSnapshot(new Set(["ref-a"]), readRefA());
      const stateAfterFirst = store.getState();

      store.projectSnapshot(new Set(["ref-a"]), readRefA());
      expect(store.getState()).toBe(stateAfterFirst); // no new setState published
    });

    test("publishes a change, and the record maps it did not change keep their identity", () => {
      const store = createPendingTurnsStore({
        threads: fakeThreadsPort(),
        draft: fakeDraftPort(),
        identity: fakeIdentity("client-x"),
      });
      store.projectSnapshot(new Set(["ref-a"]), readRefA());
      const before = store.getState();

      const changed = readRefA();
      changed.outbox = [outboxRecord({ ...changed.outbox[0], state: "blockedUnknown" })];
      store.projectSnapshot(new Set(["ref-a"]), changed);

      const after = store.getState();
      expect(after.outbox.get("out-1")?.state).toBe("blockedUnknown");
      expect(after.optimistic).toBe(before.optimistic);
      expect(after.recovery).toBe(before.recovery);
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

    // Round-3 review M-B (half 2), the replacement corner of the invariant
    // above: a replacement owns a fresh storage key while traveling under its
    // predecessor's wire id - the id the daemon's feed names. Discovery keyed
    // on the storage key strands the ownership trail the moment
    // authoritative reconciliation settles the replacement's durable copy
    // out of the outbox: the map points at a dead key while the lineage's
    // successor still needs the "mine" answer for Send/Queue routing.
    // Discovery keys the WIRE id, which is stable across the mints.
    test("a replacement's discovery keys its wire id, so ownership survives the storage-key churn", () => {
      const store = createPendingTurnsStore({
        threads: fakeThreadsPort({
          "ref-a": threadModel({
            turns: [],
            pendingMutations: [
              {
                clientMutationId: "original-1",
                method: "turn/start",
                input: [{ type: "text", text: "hello" }],
                executionState: "claimed",
                projectionState: "pending",
              },
            ],
          }),
        }),
        draft: fakeDraftPort(),
        identity: fakeIdentity("client-x"),
      });
      // The replacement row this client's own reconcile minted: fresh storage
      // key, inherited wire id, this client's provenance.
      store.recordSubmittedHere({
        outbox: [
          outboxRecord({
            clientMutationId: "replacement-2",
            originClientId: "client-x",
            payload: { clientMutationId: "original-1" },
          }),
        ],
        optimistic: [],
      });
      // The replacement's durable copy has settled out of the outbox; the
      // authoritative feed names the wire id.
      expect(store.getState().outbox.size).toBe(0);
      const entries = store.pendingTurnEntries("ref-a");
      expect(entries).toEqual([
        expect.objectContaining({ id: "original-1", source: "authoritative", fromThisClient: true }),
      ]);
      expect(ownPendingSend(entries)).toBe(true);
    });
  });
});

describe("awaitingFirstFrameSend", () => {
  test("derives from the identified running turn and needs no confirmation timer", () => {
    const model = threadModel({
      runningTurnId: "turn_1",
      turns: [{ id: "turn_1", status: "inProgress", items: [userMessageItem({ clientMutationId: "mutation_1" })] }],
    });
    expect(awaitingFirstFrameSend(model)).toBe(true);
  });

  test("an authoritative assistant frame retires first-frame state by model identity", () => {
    const model = threadModel({
      runningTurnId: "turn_1",
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

  test("a fresh read's activeTurnId alone (before any live status frame) still counts", () => {
    // threadFields/runningTurn (reducer.ts) set runningTurnId from the read's
    // own evener.activeTurnId at every hydrate, so a resumed cold session
    // that has not yet received a live thread/status/changed frame still
    // resolves through this same field.
    const model = threadModel({
      activeTurnId: "turn_1",
      runningTurnId: "turn_1",
      turns: [{ id: "turn_1", status: "inProgress", items: [userMessageItem({ clientMutationId: "mutation_1" })] }],
    });
    expect(awaitingFirstFrameSend(model)).toBe(true);
  });

  test("returns false with no running turn or no model at all", () => {
    expect(awaitingFirstFrameSend(threadModel({ runningTurnId: undefined, turns: [] }))).toBe(false);
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

describe("blockedEntriesIncludingDisplayed", () => {
  // A retargeted recovery's delivery-uncertain row targets the destination
  // the peer chose while the pane it was authored in stays the displayed
  // ref - the author's only durable copy, so the message rows must keep it.
  test("includes a blocked row whose displayed ref is the pane, ordered oldest first", () => {
    const outbox = new Map([
      [
        "cmid-displayed",
        outboxRecord({
          clientMutationId: "cmid-displayed",
          targetRef: "ref-destination",
          displayedRef: "ref-a",
          state: "blockedUnknown",
          intentSequence: 2,
        }),
      ],
      [
        "cmid-target",
        outboxRecord({
          clientMutationId: "cmid-target",
          targetRef: "ref-a",
          state: "blockedUnknown",
          intentSequence: 1,
        }),
      ],
      // A row with no association to this pane stays out, whichever state.
      [
        "cmid-foreign",
        outboxRecord({
          clientMutationId: "cmid-foreign",
          targetRef: "ref-destination",
          state: "blockedUnknown",
          intentSequence: 0,
        }),
      ],
    ]);
    expect(blockedEntriesIncludingDisplayed(outbox, "ref-a").map((record) => record.clientMutationId)).toEqual([
      "cmid-target",
      "cmid-displayed",
    ]);
  });

  test("never includes another pane's displayed row", () => {
    const outbox = new Map([
      [
        "cmid-other-displayed",
        outboxRecord({
          clientMutationId: "cmid-other-displayed",
          targetRef: "ref-destination",
          displayedRef: "ref-b",
          state: "blockedUnknown",
          intentSequence: 1,
        }),
      ],
    ]);
    expect(blockedEntriesIncludingDisplayed(outbox, "ref-a")).toEqual([]);
  });
});
