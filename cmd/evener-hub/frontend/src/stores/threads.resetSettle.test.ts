// The store generation a test reset bumps (issue #7236): async work that began
// before a reset must settle into nothing. Its durable enqueue count and its
// thread-claim hold were discarded with the store it started under, so settling
// into the reset's successor would either raise a bookkeeping error or consume a
// hold that successor now owns. Both halves were observed in one CI flake -
// "Composer.recovery bounced-send test flaked" - where a refused recovery
// resend was still in flight when its test ended: the next test's reset dropped
// its count and its destination claim, and the settle then threw
// ("released an in-flight durable enqueue for ref_a that was never acquired")
// and retired the fresh test's thread model, so its composer never mounted.

import "fake-indexeddb/auto";
import { afterEach, beforeEach, expect, test } from "vitest";
import { resetConnectionStoreForTests } from "./connection";
import { MutationOutboxIndexedDB } from "./mutationOutboxIndexedDB";
import { holdNextWriteTransaction } from "./testing/stalledIndexedDB";
import { foregroundHolds } from "./threadOwners";
import { currentStoreGeneration, resetThreadsStoreForTests, setMutationStorageForTests, threadsStore } from "./threads";
import { connectMutationClient, ensureActiveMutationTarget } from "./threadsSessionActionsTestUtils";
import { acquireUnreadThreadClaim } from "./useThreadModel";

beforeEach(() => {
  resetConnectionStoreForTests();
  resetThreadsStoreForTests();
});

afterEach(() => {
  resetThreadsStoreForTests();
});

test("a durable enqueue a reset caught in flight settles without releasing into the fresh store", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const client = connectMutationClient();
  await ensureActiveMutationTarget(client, "ref_a");
  const generation = currentStoreGeneration();

  const hold = holdNextWriteTransaction(["outbox", "optimistic", "recovery", "sequences"]);
  const send = threadsStore.getState().send("ref_a", "my send");
  // The enqueue's write has opened: the click is counted in flight.
  await hold.reached;
  // The store the click belongs to is thrown away while its write is in flight,
  // the way a test reset lands under a send a finished test left behind.
  resetThreadsStoreForTests();
  expect(currentStoreGeneration()).toBe(generation + 1);

  hold.release();
  // Before the generation fence this rejected with "threads store: released an
  // in-flight durable enqueue for ref_a that was never acquired", which the
  // composer toasted as "Send failed: ..." in the NEXT test. The count belonged
  // to the discarded store, so the settle now ends with the committed record.
  await expect(send).resolves.toBeUndefined();
});

test("a claim lease released after a reset leaves the hold the reset's successor owns", async () => {
  connectMutationClient();
  const lease = await acquireUnreadThreadClaim("ref_a");
  expect(threadsStore.getState().threads.has("ref_a")).toBe(true);

  // The reset discards the store the lease's claim belongs to.
  resetThreadsStoreForTests();
  resetConnectionStoreForTests();

  // The successor test claims the same ref and hydrates its own model.
  const next = connectMutationClient();
  await ensureActiveMutationTarget(next, "ref_a");
  expect(threadsStore.getState().threads.has("ref_a")).toBe(true);
  expect(foregroundHolds("ref_a")).toBe(1);

  // The stale lease's release must not consume the successor's hold. Without
  // the generation fence it did: the count went to 0, which is a final release
  // for the ref, so it retired the model the fresh test had just mounted on
  // (the composer that then rendered nothing).
  lease.release();
  expect(foregroundHolds("ref_a")).toBe(1);
  expect(threadsStore.getState().threads.has("ref_a")).toBe(true);
});
