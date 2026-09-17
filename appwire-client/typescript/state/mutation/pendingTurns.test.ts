import { describe, expect, test } from "vitest";
import type { ThreadModel } from "../../model";
import type { PendingTurnsDraftPort, PendingTurnsThreadsPort } from "./pendingTurns";
import { createPendingTurnsStore } from "./pendingTurns";
import type { MutationOptimisticRecord, MutationOutboxRecord } from "./records";
import { setMutationClientIdentityForTests } from "./records";

function outboxRecord(overrides: Partial<MutationOutboxRecord> = {}): MutationOutboxRecord {
  return {
    version: 1,
    clientMutationId: "cmid-1",
    targetRef: "ref-a",
    method: "turn/start",
    payload: {},
    attachments: [],
    optimisticDisplay: null,
    intentSequence: 0,
    createdAt: 0,
    state: "submitting",
    ...overrides,
  };
}

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
    const store = createPendingTurnsStore({ threads: fakeThreadsPort(), draft: fakeDraftPort() });
    const state = store.getState();
    expect(state.outbox.size).toBe(0);
    expect(state.optimistic.size).toBe(0);
    expect(state.recovery.size).toBe(0);
    expect(state.submittingRefs.size).toBe(0);
    expect(state.submittedHere.size).toBe(0);
  });

  describe("recordSubmittedHere", () => {
    test("discovers this client's own outbox and optimistic ids, skipping foreign and already-known ones", () => {
      setMutationClientIdentityForTests("client-x");
      const store = createPendingTurnsStore({ threads: fakeThreadsPort(), draft: fakeDraftPort() });
      const own = outboxRecord({ clientMutationId: "own-1", originClientId: "client-x" });
      const foreign = outboxRecord({ clientMutationId: "foreign-1", originClientId: "client-y" });
      const unattributed = optimisticRecord({ clientMutationId: "unattributed-1", originClientId: undefined });

      store.recordSubmittedHere({ outbox: [own, foreign], optimistic: [unattributed] });

      const submittedHere = store.getState().submittedHere;
      expect(submittedHere.has("own-1")).toBe(true);
      expect(submittedHere.has("unattributed-1")).toBe(true);
      expect(submittedHere.has("foreign-1")).toBe(false);
      setMutationClientIdentityForTests(undefined);
    });

    test("is a no-op when every discovered id is already known", () => {
      setMutationClientIdentityForTests("client-x");
      const store = createPendingTurnsStore({ threads: fakeThreadsPort(), draft: fakeDraftPort() });
      const own = outboxRecord({ clientMutationId: "own-1", originClientId: "client-x" });
      store.recordSubmittedHere({ outbox: [own], optimistic: [] });
      const stateAfterFirst = store.getState();

      store.recordSubmittedHere({ outbox: [own], optimistic: [] });
      expect(store.getState()).toBe(stateAfterFirst); // no new setState published
      setMutationClientIdentityForTests(undefined);
    });
  });

  describe("beginSubmission / endSubmission", () => {
    test("guards a second call for the same ref while one is in flight, and endSubmission releases it", () => {
      const store = createPendingTurnsStore({ threads: fakeThreadsPort(), draft: fakeDraftPort() });
      expect(store.beginSubmission("ref-a")).toBe(true);
      expect(store.getState().submittingRefs.has("ref-a")).toBe(true);
      expect(store.beginSubmission("ref-a")).toBe(false);

      store.endSubmission("ref-a");
      expect(store.getState().submittingRefs.has("ref-a")).toBe(false);
      expect(store.beginSubmission("ref-a")).toBe(true);
    });

    test("tracks independent refs independently", () => {
      const store = createPendingTurnsStore({ threads: fakeThreadsPort(), draft: fakeDraftPort() });
      expect(store.beginSubmission("ref-a")).toBe(true);
      expect(store.beginSubmission("ref-b")).toBe(true);
      store.endSubmission("ref-a");
      expect(store.getState().submittingRefs.has("ref-a")).toBe(false);
      expect(store.getState().submittingRefs.has("ref-b")).toBe(true);
    });
  });

  describe("settleSubmittedDraft", () => {
    test("clears the draft and returns true when the revision, text and selections all still match", () => {
      const draft = fakeDraftPort({ revision: 1, text: "hello", skillNames: ["a"] });
      const store = createPendingTurnsStore({ threads: fakeThreadsPort(), draft });
      const cleared = store.settleSubmittedDraft("ref-a", {
        draftRevisionAtStart: 1,
        text: "hello",
        skillNames: ["a"],
      });
      expect(cleared).toBe(true);
      expect(draft.state.cleared).toEqual(["ref-a"]);
    });

    test("does not clear the draft when the revision changed (edited since submit started)", () => {
      const draft = fakeDraftPort({ revision: 2, text: "hello", skillNames: [] });
      const store = createPendingTurnsStore({ threads: fakeThreadsPort(), draft });
      const cleared = store.settleSubmittedDraft("ref-a", { draftRevisionAtStart: 1, text: "hello", skillNames: [] });
      expect(cleared).toBe(false);
      expect(draft.state.cleared).toEqual([]);
    });

    test("does not clear the draft when the stored text no longer matches what was submitted", () => {
      const draft = fakeDraftPort({ revision: 1, text: "edited", skillNames: [] });
      const store = createPendingTurnsStore({ threads: fakeThreadsPort(), draft });
      const cleared = store.settleSubmittedDraft("ref-a", {
        draftRevisionAtStart: 1,
        text: "original",
        skillNames: [],
      });
      expect(cleared).toBe(false);
      expect(draft.state.cleared).toEqual([]);
    });

    test("does not clear the draft when the selected skills no longer match", () => {
      const draft = fakeDraftPort({ revision: 1, text: "hello", skillNames: ["a", "b"] });
      const store = createPendingTurnsStore({ threads: fakeThreadsPort(), draft });
      const cleared = store.settleSubmittedDraft("ref-a", {
        draftRevisionAtStart: 1,
        text: "hello",
        skillNames: ["a"],
      });
      expect(cleared).toBe(false);
      expect(draft.state.cleared).toEqual([]);
    });
  });

  describe("pendingTurnEntries", () => {
    test("reconciles this store's outbox and optimistic records against the injected thread model", () => {
      const model: ThreadModel = {
        turns: [],
        pendingMutations: [],
      } as unknown as ThreadModel;
      const store = createPendingTurnsStore({ threads: fakeThreadsPort({ "ref-a": model }), draft: fakeDraftPort() });
      store.setState({
        outbox: new Map([["cmid-1", outboxRecord({ clientMutationId: "cmid-1", method: "turn/queue" })]]),
      });

      const entries = store.pendingTurnEntries("ref-a");
      expect(entries).toHaveLength(1);
      expect(entries[0]?.id).toBe("cmid-1");
      expect(entries[0]?.method).toBe("queue");
    });

    test("filters by method when one is given", () => {
      const model: ThreadModel = { turns: [], pendingMutations: [] } as unknown as ThreadModel;
      const store = createPendingTurnsStore({ threads: fakeThreadsPort({ "ref-a": model }), draft: fakeDraftPort() });
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
      const modelA: ThreadModel = { turns: [], pendingMutations: [] } as unknown as ThreadModel;
      const store = createPendingTurnsStore({
        threads: fakeThreadsPort({ "ref-a": modelA, "ref-b": undefined }),
        draft: fakeDraftPort(),
      });
      // No records target ref-b; an absent thread model must not throw.
      expect(store.pendingTurnEntries("ref-b")).toEqual([]);
    });
  });
});
