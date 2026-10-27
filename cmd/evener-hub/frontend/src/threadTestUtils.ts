import type { HistoryUpdatedParams, QueueState, Thread, ThreadCapabilities } from "@evener/appwire-client";
import type { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { wireThread } from "@evener/appwire-client/testing/notifications";

/**
 * ALL_CAPABILITIES is the every-permission ThreadCapabilities set the thread
 * fixtures grant: a thread that may do anything. The identical literal was
 * copy-pasted into dozens of test files as CAPABILITIES / FULL_CAPABILITIES /
 * CAPS; it lives here once. Nothing mutates it — a test that needs a reduced
 * set spreads it into a new object (`{ ...ALL_CAPABILITIES, send: false }`).
 */
export const ALL_CAPABILITIES: ThreadCapabilities = {
  send: true,
  steer: true,
  interrupt: true,
  compact: true,
  clear: true,
  forkFromTurn: true,
  shutdown: true,
  changeModel: true,
  changeVisionModel: true,
  queue: true,
  goal: true,
  sharedNotes: true,
  rename: true,
};

export type TestThreadOverrides = Omit<Partial<Thread>, "evener"> & {
  evener?: Omit<Thread["evener"], "queue"> & { queue: Partial<QueueState> };
};

/**
 * testThread builds a Thread fixture with stable ids derived from `ref`, the
 * all-permission capability set, and per-field overrides, delegating the
 * wire's base Thread shape to the SDK testing package's wireThread.
 * `evener.queue` overrides merge onto `{ revision: 0 }`; every other field
 * replaces wholesale.
 */
export function testThread(ref: string, overrides: TestThreadOverrides = {}): Thread {
  const { evener, ...threadOverrides } = overrides;
  const threadID = threadOverrides.id ?? `thr_${ref}`;
  const wire = wireThread(ref);
  return {
    ...wire,
    ...threadOverrides,
    evener: {
      ...wire.evener,
      instanceId: threadID,
      mutationStateAuthoritative: true,
      ...evener,
      queue: { ...wire.evener.queue, ...evener?.queue },
    },
  };
}

/**
 * emitHistoryUpdated emits the wire's history/updated notification for the
 * caller's fixture identity, with the standard boot generation and epoch and
 * whatever params the test layers on top. Store tests keep a one-line wrapper
 * holding their identity defaults.
 */
export function emitHistoryUpdated(
  client: FakeClient,
  ref: string,
  threadId: string,
  rest: Partial<HistoryUpdatedParams> & Pick<HistoryUpdatedParams, "snapshot">,
): void {
  client.emitNotification({
    method: "history/updated",
    params: { ref, threadId, bootGeneration: "1", epoch: 1, ...rest },
  });
}
