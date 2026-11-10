import "fake-indexeddb/auto";
import type { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import type { IDBFactory } from "fake-indexeddb";
import { MutationOutboxIndexedDB } from "./mutationOutboxIndexedDB";
import { pumpIndexedDBUntil } from "./testing/indexedDBPump";
import { installResumeRetrySchedulerForTests, setMutationStorageForTests } from "./threads";
import { connectMutationClient, ensureActiveMutationTarget } from "./threadsSessionActionsTestUtils";
export async function flushUntilArrived(what: string, arrived: () => boolean | Promise<boolean>, maxTurns = 60) {
  if (!(await pumpIndexedDBUntil({ until: arrived, turns: maxTurns }))) {
    throw new Error(`never arrived after ${maxTurns} IndexedDB turns: ${what}`);
  }
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

// Every armed-retry test stages the same scheduler: capture each scheduled
// fire so the test can fire it by hand, and flag it cancelled when the store
// retires it. Installs the scheduler and registers its restore on the test's
// own teardown, so a test body just reads the returned array.
export function trackScheduledResumeRetries(
  onTestFinished: (teardown: () => void) => void,
): { attempt: number; fire: () => void; cancelled: boolean }[] {
  const retries: { attempt: number; fire: () => void; cancelled: boolean }[] = [];
  const restore = installResumeRetrySchedulerForTests((attempt, retry) => {
    const scheduled = { attempt, fire: retry, cancelled: false };
    retries.push(scheduled);
    return () => {
      scheduled.cancelled = true;
    };
  });
  onTestFinished(restore);
  return retries;
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
