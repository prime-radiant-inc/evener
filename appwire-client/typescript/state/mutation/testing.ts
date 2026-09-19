// Test-only builders shared by this subpath's suites (pendingEntries.test.ts,
// pendingTurns.test.ts, projection.test.ts, commitFeed.test.ts): a
// fully-populated, typed ThreadModel, outbox/recovery record and a store
// wired to no-op ports, each overridable on the one or two fields a test
// cares about, rather than every suite hand-rolling (or casting past) the
// same shapes.

import type { ThreadModel } from "../../model";
import { createPendingTurnsStore, type PendingTurnsStore, type PendingTurnsStoreDeps } from "./pendingTurns";
import type { MutationOutboxRecord, MutationRecoveryRecord } from "./records";

export function threadModel(overrides: Partial<ThreadModel> = {}): ThreadModel {
  const { jobsTreeRevision = null, ...rest } = overrides;
  return {
    ref: "ref_a",
    threadId: "thread_a",
    name: "",
    status: { type: "active" },
    modelProvider: "",
    model: "",
    visionModel: "",
    askPending: false,
    pendingEscalations: [],
    turns: [],
    queue: { revision: 1 },
    tasks: null,
    jobsUpdatedAt: null,
    lastFrameAt: 0,
    capabilities: {} as ThreadModel["capabilities"],
    goal: null,
    humanNote: "",
    agentNote: "",
    sessionUrls: [],
    contextUsed: 0,
    contextWindow: 0,
    contextPressure: 0,
    usage: null,
    workMillis: 0,
    reasoningEffortLevels: [],
    supportsReasoning: false,
    cwd: "",
    createdAt: "",
    updatedAt: "",
    ...rest,
    jobsTreeRevision,
  };
}

export function outboxRecord(overrides: Partial<MutationOutboxRecord> = {}): MutationOutboxRecord {
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

export function recoveryRecord(overrides: Partial<MutationRecoveryRecord> = {}): MutationRecoveryRecord {
  return { ...outboxRecord(), recoveryKind: "rejected", ...overrides };
}

// A pending-turns store wired to no-op threads/draft ports and an
// always-own identity - what a test needs when it exercises the store's own
// state (recordSubmittedHere, setState) without reading a live thread model
// or composer draft storage. `overrides` replaces one dependency wholesale
// (there is nothing to merge field-by-field within a port), for a test that
// needs a specific draft or identity behaviour instead of the no-op default.
export function testPendingTurnsStore(overrides: Partial<PendingTurnsStoreDeps> = {}): PendingTurnsStore {
  return createPendingTurnsStore({
    threads: { getThreadModel: () => undefined },
    draft: {
      readDraftRevision: () => 0,
      readComposerDraft: () => ({ text: "", skillNames: [] }),
      clearDraft: () => undefined,
    },
    identity: { isOwnMutationRecord: () => true },
    ...overrides,
  });
}
