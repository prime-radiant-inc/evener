// Test-only builders shared by this subpath's suites (pendingEntries.test.ts,
// pendingTurns.test.ts, projection.test.ts, commitFeed.test.ts): a
// fully-populated, typed ThreadModel, outbox/recovery record and a store
// wired to no-op ports, each overridable on the one or two fields a test
// cares about, rather than every suite hand-rolling (or casting past) the
// same shapes.

import { beforeEach, describe, expect, test } from "vitest";
import type { ThreadModel } from "../../model";
import type { MutationOutboxStorage } from "./outbox";
import { createPendingTurnsStore, type PendingTurnsStore, type PendingTurnsStoreDeps } from "./pendingTurns";
import type { MutationAttachmentRef, MutationIntent, MutationOutboxRecord, MutationRecoveryRecord } from "./records";

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

// The storage options a conformance run varies: the id generator, the clock,
// and the identity each host reads fresh at enqueue time (the web's
// sessionStorage singleton, the native adapter's getOwnClientId option).
export interface MutationOutboxStorageFactoryOptions {
  createMutationId?: () => string;
  now?: () => number;
  getOwnClientId?: () => string | undefined;
}

// One host's storage under conformance. Each adapter (the web IndexedDB
// adapter, the native expo-sqlite adapter) supplies a factory, and the SAME
// contracts below run against both, so a contract change cannot drift between
// hosts the way it does when each hand-transcribes the other's tests with
// "Oracle:" comments.
//
// createStorage must return a storage over empty persistence: the suite calls
// it once per test and never shares an instance across tests.
export interface MutationOutboxStorageFactory {
  // Human-readable host name, used as the describe block's label.
  readonly name: string;
  createStorage(options?: MutationOutboxStorageFactoryOptions): MutationOutboxStorage | Promise<MutationOutboxStorage>;
}

const CONFORMANCE_TARGET = "local:conformance-1";
const CONFORMANCE_OTHER_TARGET = "local:conformance-2";

function conformanceTextIntent(text: string, targetRef = CONFORMANCE_TARGET): MutationIntent {
  return {
    targetRef,
    threadId: "thread-1",
    method: "turn/queue",
    payload: { ref: targetRef, input: [{ type: "text", text }] },
    attachments: [],
    optimisticDisplay: { text },
  };
}

function conformanceInterruptIntent(targetRef = CONFORMANCE_TARGET): MutationIntent {
  return {
    targetRef,
    method: "turn/interrupt",
    payload: { ref: targetRef },
    attachments: [],
    optimisticDisplay: { method: "turn/interrupt" },
  };
}

// The MutationOutboxStorage port's behavioral contracts, run against any
// host's factory. Every assertion goes through the port's public methods:
// nothing here reads a host's raw rows, so the suite is the one set of
// contracts both adapters must satisfy rather than a second transcription of
// one of them.
//
// Tracked divergence, deliberately not asserted here (#1927, measured at
// #1965 ae2d1944e): an explicit `optimisticDisplay: null` round-trips as null
// on the web adapter but normalizes to undefined on the native adapter (an
// absent display and an explicit null are both serialized as JSON null and
// read back as undefined). Existing native consumers treat the two
// equivalently, so no contract here pins either spelling until a host
// actually depends on the difference.
export function describeMutationOutboxStorage(factory: MutationOutboxStorageFactory): void {
  describe(`MutationOutboxStorage conformance (${factory.name})`, () => {
    let storage: MutationOutboxStorage;
    let nextId = 0;
    const idSequence = () => `conformance-${++nextId}`;

    beforeEach(async () => {
      nextId = 0;
      storage = await factory.createStorage({ createMutationId: idSequence, now: () => 1234 });
    });

    // The central durable-intent contract: the full intent survives, and the
    // generated clientMutationId rides the payload the daemon correlates on.
    test("enqueueIntent persists the full intent with the generated id on its payload", async () => {
      const record = await storage.enqueueIntent(conformanceTextIntent("survive reload"));
      expect(record).toMatchObject({
        version: 1,
        clientMutationId: "conformance-1",
        targetRef: CONFORMANCE_TARGET,
        threadId: "thread-1",
        intentSequence: 1,
        createdAt: 1234,
        method: "turn/queue",
        payload: {
          ref: CONFORMANCE_TARGET,
          input: [{ type: "text", text: "survive reload" }],
          clientMutationId: "conformance-1",
        },
        attachments: [],
        optimisticDisplay: { text: "survive reload" },
        state: "submitting",
        attempted: false,
      });
      await expect(storage.getOutbox("conformance-1")).resolves.toMatchObject({
        state: "submitting",
        attempted: false,
      });
    });

    // #1927 Low (raw GLM review of #1916): a nonempty attachment, composerText
    // and an injected getOwnClientId must all round-trip through persistence.
    test("enqueueIntent persists attachments, composerText, and the injected submitting identity", async () => {
      const identified = await factory.createStorage({
        createMutationId: idSequence,
        now: () => 1234,
        getOwnClientId: () => "client-one",
      });
      const attachment: MutationAttachmentRef = {
        presentationId: "presentation-1",
        marker: 1,
        name: "photo.png",
        mediaType: "image/png",
      };
      const record = await identified.enqueueIntent({
        ...conformanceTextIntent("with attachment"),
        attachments: [attachment],
        composerText: "with attachment [image 1]",
      });
      expect(record).toMatchObject({
        attachments: [attachment],
        composerText: "with attachment [image 1]",
        originClientId: "client-one",
      });
      await expect(identified.getOutbox(record.clientMutationId)).resolves.toMatchObject({
        attachments: [attachment],
        composerText: "with attachment [image 1]",
        originClientId: "client-one",
      });
    });

    test("enqueueIntent rejects an empty or whitespace targetRef", async () => {
      await expect(storage.enqueueIntent(conformanceTextIntent("no target", "   "))).rejects.toThrow(
        "targetRef is required",
      );
    });

    test("intentSequence is gap-free and per target ref", async () => {
      const first = await storage.enqueueIntent(conformanceTextIntent("first", CONFORMANCE_TARGET));
      const second = await storage.enqueueIntent(conformanceTextIntent("second", CONFORMANCE_TARGET));
      const other = await storage.enqueueIntent(conformanceTextIntent("other", CONFORMANCE_OTHER_TARGET));
      expect([first.intentSequence, second.intentSequence]).toEqual([1, 2]);
      expect(other.intentSequence).toBe(1);
    });

    test("markAttempted flips a submitting record's flag and refuses a non-submitting one", async () => {
      const record = await storage.enqueueIntent(conformanceTextIntent("attempt me"));
      await expect(storage.markAttempted(record.clientMutationId)).resolves.toBe(true);
      await expect(storage.getOutbox(record.clientMutationId)).resolves.toMatchObject({ attempted: true });
      await expect(storage.markAttempted("missing")).resolves.toBe(false);
      await storage.markUnknown(record.clientMutationId, "blockedUnknown");
      await expect(storage.markAttempted(record.clientMutationId)).resolves.toBe(false);
    });

    test("markUnknown's onlyAttempted guard refuses an un-attempted record", async () => {
      const record = await storage.enqueueIntent(conformanceTextIntent("unknown outcome"));
      await expect(
        storage.markUnknown(record.clientMutationId, "blockedUnknown", { onlyAttempted: true }),
      ).resolves.toBe(false);
      await expect(storage.getOutbox(record.clientMutationId)).resolves.toMatchObject({ state: "submitting" });
      await storage.markAttempted(record.clientMutationId);
      await expect(
        storage.markUnknown(record.clientMutationId, "blockedUnknown", { onlyAttempted: true }),
      ).resolves.toBe(true);
      await expect(storage.getOutbox(record.clientMutationId)).resolves.toMatchObject({ state: "blockedUnknown" });
    });

    // #1927 Low (raw GLM review of #1916): an intent attempted before a
    // recovery-to-optimistic settlement must not carry its attempt evidence
    // into the accepted optimistic record.
    test("markAttempted before settlement resets attempted in the accepted optimistic record", async () => {
      const record = await storage.enqueueIntent({
        ...conformanceTextIntent("attempted before receipt"),
        optimisticDisplay: { input: [{ type: "text", text: "attempted before receipt" }] },
      });
      await storage.markAttempted(record.clientMutationId);
      await expect(storage.settleReceipt(record.clientMutationId, "pending")).resolves.toBe(true);
      await expect(storage.getOutbox(record.clientMutationId)).resolves.toBeUndefined();
      const accepted = await storage.getOptimistic(record.clientMutationId);
      expect(accepted).toMatchObject({ state: "accepted" });
      expect(accepted).not.toHaveProperty("attempted");
    });

    test("nextDispatchable is blocked by an earlier blockedUnknown on the same target only", async () => {
      const first = await storage.enqueueIntent(conformanceTextIntent("first", CONFORMANCE_TARGET));
      const second = await storage.enqueueIntent(conformanceTextIntent("second", CONFORMANCE_TARGET));
      const other = await storage.enqueueIntent(conformanceTextIntent("other", CONFORMANCE_OTHER_TARGET));
      await storage.markAttempted(first.clientMutationId);
      await storage.markUnknown(first.clientMutationId, "blockedUnknown");

      await expect(storage.nextDispatchable(CONFORMANCE_TARGET)).resolves.toBeUndefined();
      await expect(storage.nextDispatchable(CONFORMANCE_OTHER_TARGET)).resolves.toMatchObject({
        clientMutationId: other.clientMutationId,
      });
      await storage.settleApplied(first.clientMutationId);
      await expect(storage.nextDispatchable(CONFORMANCE_TARGET)).resolves.toMatchObject({
        clientMutationId: second.clientMutationId,
      });
    });

    // #1927 Low (raw #1917 panel): restoreProvenAbsent's target scoping is
    // distinct from authoritative-id membership. A blockedUnknown row on
    // another target is also absent from authoritativeIds, and restoring the
    // selected target must leave it blocked.
    test("restoreProvenAbsent reopens only the selected target's omitted records", async () => {
      const omitted = await storage.enqueueIntent(conformanceTextIntent("omitted", CONFORMANCE_TARGET));
      const named = await storage.enqueueIntent(conformanceTextIntent("named", CONFORMANCE_TARGET));
      const blockedElsewhere = await storage.enqueueIntent(
        conformanceTextIntent("blocked elsewhere", CONFORMANCE_OTHER_TARGET),
      );
      await storage.markUnknown(omitted.clientMutationId, "blockedUnknown");
      await storage.markUnknown(named.clientMutationId, "blockedUnknown");
      await storage.markUnknown(blockedElsewhere.clientMutationId, "blockedUnknown");

      await expect(storage.restoreProvenAbsent(CONFORMANCE_TARGET, new Set([named.clientMutationId]))).resolves.toEqual(
        [omitted.clientMutationId],
      );
      await expect(storage.getOutbox(omitted.clientMutationId)).resolves.toMatchObject({ state: "submitting" });
      await expect(storage.getOutbox(named.clientMutationId)).resolves.toMatchObject({ state: "blockedUnknown" });
      await expect(storage.getOutbox(blockedElsewhere.clientMutationId)).resolves.toMatchObject({
        state: "blockedUnknown",
      });
    });

    test("enqueueInterruptAndCancel cancels non-attempted rows and commits the interrupt with them", async () => {
      const queued = await storage.enqueueIntent(conformanceTextIntent("queued", CONFORMANCE_TARGET));
      const inFlight = await storage.enqueueIntent(conformanceTextIntent("in flight", CONFORMANCE_TARGET));
      await storage.markAttempted(inFlight.clientMutationId);
      const other = await storage.enqueueIntent(conformanceTextIntent("other ref", CONFORMANCE_OTHER_TARGET));

      const interrupt = await storage.enqueueInterruptAndCancel(conformanceInterruptIntent(CONFORMANCE_TARGET));
      expect(interrupt).toMatchObject({ method: "turn/interrupt", state: "submitting", attempted: false });
      await expect(storage.getOutbox(queued.clientMutationId)).resolves.toMatchObject({ state: "canceled" });
      await expect(storage.getOutbox(inFlight.clientMutationId)).resolves.toMatchObject({
        state: "submitting",
        attempted: true,
      });
      await expect(storage.getOutbox(other.clientMutationId)).resolves.toMatchObject({ state: "submitting" });
    });

    test("a canceled record cannot be marked attempted or reclassified", async () => {
      const queued = await storage.enqueueIntent(conformanceTextIntent("queued", CONFORMANCE_TARGET));
      await storage.enqueueInterruptAndCancel(conformanceInterruptIntent(CONFORMANCE_TARGET));
      await expect(storage.markAttempted(queued.clientMutationId)).resolves.toBe(false);
      await expect(storage.markUnknown(queued.clientMutationId, "blockedUnknown")).resolves.toBe(false);
      await expect(storage.getOutbox(queued.clientMutationId)).resolves.toMatchObject({
        state: "canceled",
        attempted: false,
      });
    });

    test("transferToRecovery moves a record out of the outbox with the daemon's reason", async () => {
      const record = await storage.enqueueIntent(conformanceTextIntent("steer that lost its turn"));
      const recovery = await storage.transferToRecovery(record.clientMutationId, "rejected", "turn is not active");
      expect(recovery).toMatchObject({
        clientMutationId: record.clientMutationId,
        recoveryKind: "rejected",
        recoveryReason: "turn is not active",
      });
      await expect(storage.getOutbox(record.clientMutationId)).resolves.toBeUndefined();
      await expect(storage.getRecovery(record.clientMutationId)).resolves.toMatchObject({
        recoveryReason: "turn is not active",
      });
    });

    test("listTargetRefs reports every ref with a waiting outbox or optimistic record", async () => {
      await storage.enqueueIntent(conformanceTextIntent("a", CONFORMANCE_TARGET));
      const accepted = await storage.enqueueIntent({
        ...conformanceTextIntent("b", CONFORMANCE_OTHER_TARGET),
        optimisticDisplay: { input: [{ type: "text", text: "b" }] },
      });
      await storage.settleReceipt(accepted.clientMutationId, "pending");
      await expect(storage.listTargetRefs()).resolves.toEqual([CONFORMANCE_TARGET, CONFORMANCE_OTHER_TARGET].sort());
    });
  });
}
