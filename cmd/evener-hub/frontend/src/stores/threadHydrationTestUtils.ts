import {
  acquireThreadSubscription,
  type ThreadReadResponse,
  type ThreadSubscriptionLease,
} from "@evener/appwire-client";
import type { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { expect } from "vitest";
import { type TestThreadOverrides, testThread } from "../threadTestUtils";
import { threadsStore } from "./threads";

// The arrays below are module state. Vitest's vmThreads pool gives every test
// file its own module registry, so each importing file has its own copy, as it
// did when each file declared them itself.

export function readResponse(ref: string, overrides: TestThreadOverrides = {}): ThreadReadResponse {
  return { thread: testThread(ref, overrides) };
}

/** Every membership establishMembership acquired; the importing file's
 * teardown releases them. */
export const establishedMemberships: ThreadSubscriptionLease[] = [];

export async function establishMembership(fake: FakeClient, ref = "ref_a"): Promise<void> {
  fake.on("thread/read", () => readResponse(ref));
  const lease = acquireThreadSubscription(fake, ref);
  establishedMemberships.push(lease);
  await lease.ensure();
  fake.calls.length = 0;
}

export interface ScheduledHydrationRetry {
  attempt: number;
  retry: () => void;
  cancelled: boolean;
}

/** The retries the importing file's injected hydration retry scheduler
 * captured; the file empties it before each test. */
export const scheduledHydrationRetries: ScheduledHydrationRetry[] = [];

// runScheduledHydrationRetry invokes exactly one scheduled retry, proving first
// that it exists and was not cancelled - a cancelled entry that still fires
// would make every "release/swap cancels the retry" assertion vacuous.
export function runScheduledHydrationRetry(index = 0): void {
  const scheduled = scheduledHydrationRetries[index];
  expect(scheduled, `no hydration retry scheduled at index ${index}`).toBeDefined();
  expect(scheduled?.cancelled).toBe(false);
  scheduled?.retry();
}

/** `response` with an `olderCursor` getter that sets `cut.reached`: hydration
 * reads it after the response arrives and before the snapshot publishes. */
export function markResponseCut(response: ThreadReadResponse, cut: { reached: boolean }): ThreadReadResponse {
  return {
    ...response,
    get olderCursor(): string | undefined {
      cut.reached = true;
      return response.olderCursor;
    },
  };
}

/** Waits for the cut markResponseCut armed, proves the snapshot has not
 * published yet, then calls `emit` inside that window. */
export async function emitAtResponseCut(
  cut: { reached: boolean },
  ref: string,
  emit: () => void,
  published: () => boolean = () => threadsStore.getState().threads.has(ref),
): Promise<void> {
  for (let i = 0; i < 20 && !cut.reached; i += 1) await Promise.resolve();
  expect(cut.reached, "the response cut never ran: hydrateThread no longer reads olderCursor").toBe(true);
  expect(published(), "the snapshot published before the cut window was reached").toBe(false);
  emit();
}
