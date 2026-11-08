// The destination claim lease the recovery Send hydrates an unread
// destination with (issue #5182's Medium finding): acquired only after
// ensureThread succeeds, so a failed acquisition - whose rollback ensureThread
// already performed - can never release a claim another foreground reader
// holds. Both tests drive the real claim machinery (the same
// ensureThread/releaseThread pair useThreadClaim holds a pane's subscription
// with), never a mock of it.

import "fake-indexeddb/auto";
import type { ThreadReadResponse } from "@evener/appwire-client";
import { nextMacrotask } from "@evener/appwire-client/testing/macrotask";
import { beforeEach, expect, test, vi } from "vitest";
import { connectReadyFakeClient } from "../testing/connectionTestUtils";
import { testThread } from "../threadTestUtils";
import { resetConnectionStoreForTests } from "./connection";
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

test("a destination this tab already tracks needs no claim and releases nothing", async () => {
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () => ({ thread: testThread("ref_dest") }));
  await threadsStore.getState().ensureThread("ref_dest");
  expect(threadsStore.getState().threads.has("ref_dest")).toBe(true);
  await expect(acquireUnreadThreadClaim("ref_dest")).resolves.toBeNull();
  // No lease means no release: the reader that already tracks the destination
  // keeps its model exactly as it was.
  expect(threadsStore.getState().threads.has("ref_dest")).toBe(true);
  threadsStore.getState().releaseThread("ref_dest");
  await vi.waitFor(() => expect(threadsStore.getState().threads.has("ref_dest")).toBe(false));
});
