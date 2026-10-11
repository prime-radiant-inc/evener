import { deferred } from "@evener/appwire-client/testing/deferred";
import { describe, expect, test } from "vitest";
import { observeRestartSubmission, type RestartSubmissionSnapshot } from "./retirementharness-persistence";

const EMPTY: RestartSubmissionSnapshot = { outbox: [], recovery: [] };
const PENDING: RestartSubmissionSnapshot = {
  outbox: [
    {
      clientMutationId: "mutation-1",
      method: "turn/start",
      state: "submitting",
      composerText: "restart draft",
    },
  ],
  recovery: [],
};
const RECOVERY: RestartSubmissionSnapshot = {
  outbox: [],
  recovery: [
    {
      clientMutationId: "mutation-1",
      method: "turn/start",
      state: "submitting",
      composerText: "restart draft",
    },
  ],
};

describe("observeRestartSubmission", () => {
  test("serializes persistence reads and remembers recovery observed after pending", async () => {
    const first = deferred<RestartSubmissionSnapshot>();
    const second = deferred<RestartSubmissionSnapshot>();
    const reads = [first, second];
    let listener = (): void => {};
    let activeReads = 0;
    let maxActiveReads = 0;
    const observer = observeRestartSubmission({
      read: async () => {
        activeReads += 1;
        maxActiveReads = Math.max(maxActiveReads, activeReads);
        try {
          const next = reads.shift();
          if (!next) throw new Error("unexpected read");
          return await next.promise;
        } finally {
          activeReads -= 1;
        }
      },
      subscribe: (notify) => {
        listener = notify;
        return () => {};
      },
      timeoutMs: 1_000,
    });
    const settlement = observer.settled.catch((error: unknown) => error);

    listener();
    first.resolve(PENDING);
    await expect(observer.pending).resolves.toEqual(PENDING);
    second.resolve(RECOVERY);

    await expect(settlement).resolves.toEqual(
      new Error("retirementharness: restart-gated submission entered recovery"),
    );
    expect(maxActiveReads).toBe(1);
  });

  test("settles only after the observed mutation disappears", async () => {
    const snapshots = [EMPTY, PENDING, EMPTY];
    let listener = (): void => {};
    const observer = observeRestartSubmission({
      read: async () => snapshots.shift() ?? EMPTY,
      subscribe: (notify) => {
        listener = notify;
        return () => {};
      },
      timeoutMs: 1_000,
    });
    let didSettle = false;
    void observer.settled.then(() => {
      didSettle = true;
    });

    await Promise.resolve();
    expect(didSettle).toBe(false);
    listener();
    await expect(observer.pending).resolves.toEqual(PENDING);
    expect(didSettle).toBe(false);

    listener();
    await expect(observer.settled).resolves.toEqual(EMPTY);
  });

  test("reads a queued recovery notification before accepting an empty settlement", async () => {
    const empty = deferred<RestartSubmissionSnapshot>();
    let listener = (): void => {};
    let reads = 0;
    const observer = observeRestartSubmission({
      read: async () => {
        reads += 1;
        if (reads === 1) return PENDING;
        if (reads === 2) return empty.promise;
        if (reads === 3) return RECOVERY;
        throw new Error("unexpected read");
      },
      subscribe: (notify) => {
        listener = notify;
        return () => {};
      },
      timeoutMs: 1_000,
    });
    const settlement = observer.settled.catch((error: unknown) => error);

    await observer.pending;
    listener();
    listener();
    empty.resolve(EMPTY);

    await expect(settlement).resolves.toEqual(
      new Error("retirementharness: restart-gated submission entered recovery"),
    );
    expect(reads).toBe(3);
  });
});
