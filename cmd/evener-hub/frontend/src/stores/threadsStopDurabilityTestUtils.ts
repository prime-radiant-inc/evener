import "fake-indexeddb/auto";
import type { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import type { IDBFactory } from "fake-indexeddb";
import { MutationOutboxIndexedDB } from "./mutationOutboxIndexedDB";
import { setMutationStorageForTests } from "./threads";
import { connectMutationClient, ensureActiveMutationTarget } from "./threadsSessionActionsTestUtils";
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
