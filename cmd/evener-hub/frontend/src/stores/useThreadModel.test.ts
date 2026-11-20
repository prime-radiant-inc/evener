// The destination claim lease the recovery Send holds for its duration (issues
// #5182 and #5516): acquired only after ensureThread succeeds, so a failed
// acquisition - whose rollback ensureThread already performed - can never
// release a claim another foreground reader holds, and always refcounted even
// when a tracked pane already holds the destination. Both tests drive the real
// claim machinery (the same ensureThread/releaseThread pair useThreadClaim
// holds a pane's subscription with), never a mock of it.

import "fake-indexeddb/auto";
import type { ThreadReadResponse } from "@evener/appwire-client";
import { callsTo } from "@evener/appwire-client/testing/fakeClient";
import { nextMacrotask } from "@evener/appwire-client/testing/macrotask";
import { beforeEach, expect, test, vi } from "vitest";
import { connectReadyFakeClient } from "../testing/connectionTestUtils";
import { testThread } from "../threadTestUtils";
import { resetConnectionStoreForTests } from "./connection";
import { foregroundHolds } from "./threadOwners";
import { resetThreadsStoreForTests, threadsStore } from "./threads";
import { acquireUnreadThreadClaim } from "./useThreadModel";

beforeEach(() => {
  resetConnectionStoreForTests();
  resetThreadsStoreForTests();
});

async function flushUntil(done: () => boolean, maxTurns = 40): Promise<void> {
  for (let turn = 0; turn < maxTurns && !done(); turn += 1) await nextMacrotask();
  if (!done()) throw new Error("condition not reached");
}

test("a failed destination claim acquisition releases no claim another reader holds", async () => {
  const first = connectReadyFakeClient();
  let rejectRead: ((error: Error) => void) | undefined;
  first.on(
    "thread/read",
    () =>
      new Promise<ThreadReadResponse>((_, reject) => {
        rejectRead = reject;
      }),
  );
  // Another foreground reader holds the destination while its hydration read
  // is still in flight - the claim a pane's useThreadClaim mount would hold.
  const otherReader = threadsStore.getState().ensureThread("ref_dest");
  await flushUntil(() => rejectRead !== undefined);
  // The acquisition runs with no wired client, so ensureThread refuses
  // before taking any claim of its own.
  resetConnectionStoreForTests();
  // The rejection itself is the behavior; its message wording is not.
  await expect(acquireUnreadThreadClaim("ref_dest")).rejects.toThrow();
  // A replacement connection answers the destination read, and the first
  // connection's stalled read dies with it.
  const second = connectReadyFakeClient();
  second.on("thread/read", () => ({ thread: testThread("ref_dest") }));
  rejectRead?.(new Error("transport died"));
  await otherReader;
  // The other reader's claim survived the failed acquisition: it re-arms on
  // the replacement connection and publishes the destination model. A lease
  // that released on failure would have consumed that claim, retired its
  // hydration lifecycle, and left the destination unpublished forever.
  expect(threadsStore.getState().threads.has("ref_dest")).toBe(true);
  threadsStore.getState().releaseThread("ref_dest");
  await vi.waitFor(() => expect(threadsStore.getState().threads.has("ref_dest")).toBe(false));
});

test("a destination a tracked pane holds still takes a refcounted claim, pinning it past the pane's unmount", async () => {
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () => ({ thread: testThread("ref_dest") }));
  await threadsStore.getState().ensureThread("ref_dest");
  expect(threadsStore.getState().threads.has("ref_dest")).toBe(true);
  // The tracked pane's own claim: one foreground hold, the model published.
  expect(foregroundHolds("ref_dest")).toBe(1);
  const readsBefore = callsTo(fake, "thread/read");

  // The resend acquires its own claim even though a pane already holds the
  // destination, so the model is pinned for the resend's duration.
  const lease = await acquireUnreadThreadClaim("ref_dest");
  expect(foregroundHolds("ref_dest")).toBe(2);
  // The destination is already hydrated: the claim issues no extra read.
  expect(callsTo(fake, "thread/read")).toBe(readsBefore);

  // The pane unmounts mid-resend. Its hold goes, but the lease still pins the
  // model, so the resend can never observe its destination retired.
  threadsStore.getState().releaseThread("ref_dest");
  expect(foregroundHolds("ref_dest")).toBe(1);
  expect(threadsStore.getState().threads.has("ref_dest")).toBe(true);

  // The lease's release is the final one. Releasing twice is idempotent.
  lease.release();
  lease.release();
  expect(foregroundHolds("ref_dest")).toBe(0);
  await vi.waitFor(() => expect(threadsStore.getState().threads.has("ref_dest")).toBe(false));
});
