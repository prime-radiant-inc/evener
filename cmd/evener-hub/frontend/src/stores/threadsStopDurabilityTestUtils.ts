import { ALL_CAPABILITIES as CAPABILITIES, type TestThreadOverrides, testThread } from "../threadTestUtils";
import "fake-indexeddb/auto";
import { hydrateThread, type ThreadReadResponse, WireError } from "@evener/appwire-client";
import { MutationDispatcher } from "@evener/appwire-client/state/mutation";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { nextMacrotask } from "@evener/appwire-client/testing/macrotask";
import { act, cleanup, waitFor } from "@testing-library/react";
import { IDBDatabase, IDBFactory, IDBKeyRange, IDBVersionChangeEvent } from "fake-indexeddb";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { resetPendingTurnsStoreForTests } from "../panes/session/composer/queue/pendingTurnsStore";
import { resetSubagentModuleStoreForTests } from "../panes/session/transcript/tools/subagentModuleStore";
import { resetWorkspaceStoreForTests } from "../shell/workspace";
import { connectionStore } from "./connection";
import { setProductionEntryCacheForTests } from "./entryTranscriptRoutes";
import { IDBUpgradeError } from "./idbConnection";
import type { MutationOutboxRecord } from "./mutationOutbox";
import { MutationOutboxIndexedDB, MutationStorageTimeoutError } from "./mutationOutboxIndexedDB";
import { SessionEntryCache } from "./sessionEntryCache";
import { blockedUpgradeRequest, holdNextWriteTransaction, wedgeDatabaseOpens } from "./testing/stalledIndexedDB";
import {
  discardRecoveryMutation,
  installHydrationRetrySchedulerForTests,
  notifyReadyForMutationDispatch,
  resetThreadsStoreForTests,
  resumeStopBaseline,
  retryBlockedMutation,
  setMutationStorageForTests,
  subscribeMutationPersistence,
  threadsStore,
} from "./threads";
import { ConflictError } from "./threadsErrors";
import {
  clearResponse,
  connectMutationClient,
  deferred,
  ensureActiveMutationTarget,
  flushIndexedDBUntil,
  flushMicrotasks,
  flushUntil,
  mutationReceipt,
  onTurnStart,
  readResponse,
  timeOutDurableEnqueues,
} from "./threadsSessionActionsTestUtils";
export async function flushUntilArrived(what: string, arrived: () => boolean | Promise<boolean>, maxTurns = 60) {
  const probe = new MutationOutboxIndexedDB();
  try {
    for (let turn = 0; turn < maxTurns && !(await arrived()); turn += 1) await probe.listTargetRefs();
  } finally {
    probe.close();
  }
  if (!(await arrived())) throw new Error(`never arrived after ${maxTurns} IndexedDB turns: ${what}`);
}

export function queueIntent(text: string) {
  return {
    targetRef: "ref_a",
    threadId: "thr_ref_a",
    method: "turn/queue",
    payload: { ref: "ref_a", input: [{ type: "text", text }] },
    attachments: [],
    optimisticDisplay: null,
  };
}

export async function hydratedFallbackTarget({
  indexedDB,
  databaseName,
}: {
  indexedDB: IDBFactory;
  databaseName: string;
}): Promise<{ storage: MutationOutboxIndexedDB; fake: FakeClient }> {
  const storage = new MutationOutboxIndexedDB({ indexedDB, databaseName });
  setMutationStorageForTests(storage);
  const fake = connectMutationClient();
  await ensureActiveMutationTarget(fake, "ref_a");
  return { storage, fake };
}
