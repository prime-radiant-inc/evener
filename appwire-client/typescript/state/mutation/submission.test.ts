// @vitest-environment node

import { describe, expect, test, vi } from "vitest";
import type { SubmittedDraft } from "./pendingTurns";
import { createMutationProjectionFence } from "./projection";
import { submitWithPendingTracking } from "./submission";
import { testPendingTurnsStore } from "./testing";

function trackingSpy() {
  const tracked: Promise<unknown>[] = [];
  const track = <T>(work: Promise<T>): Promise<T> => {
    tracked.push(work);
    return work;
  };
  return { track, tracked };
}

function deferred<T>() {
  let resolve: (value: T) => void = () => undefined;
  const promise = new Promise<T>((res) => {
    resolve = res;
  });
  return { promise, resolve };
}

describe("submitWithPendingTracking", () => {
  test("rejects immediately when a submission is already pending for the ref", async () => {
    const store = testPendingTurnsStore();
    store.beginSubmission("ref-a");
    const fence = createMutationProjectionFence();
    const { track } = trackingSpy();
    const prepare = vi.fn(async () => ({ draftRevisionAtStart: 0, text: "hi", skillNames: [] }));

    await expect(
      submitWithPendingTracking(
        store,
        fence,
        track,
        { ref: "ref-a", prepare, onFailure: vi.fn() },
        () => Promise.resolve(),
        () => undefined,
      ),
    ).rejects.toThrow("A message submission is already pending for this task");
    expect(prepare).not.toHaveBeenCalled();
  });

  test("a preparation still pending holds the guard: a second submission on the same ref is refused outright", async () => {
    const store = testPendingTurnsStore();
    const fence = createMutationProjectionFence();
    const { track } = trackingSpy();
    const preparation = deferred<void>();
    const performFirst = vi.fn(async () => {});
    const performSecond = vi.fn(async () => {});

    const first = submitWithPendingTracking(
      store,
      fence,
      track,
      {
        ref: "ref-a",
        onFailure: vi.fn(),
        prepare: () => preparation.promise.then(() => ({ draftRevisionAtStart: 0, text: "first", skillNames: [] })),
      },
      performFirst,
      () => undefined,
    );

    // The guard is taken synchronously at entry, before the preparation's
    // first await: a same-ref press pressed while the capture is in flight
    // cannot slip under it (#4605).
    expect(store.getState().submittingRefs.has("ref-a")).toBe(true);

    await expect(
      submitWithPendingTracking(
        store,
        fence,
        track,
        {
          ref: "ref-a",
          onFailure: vi.fn(),
          prepare: async () => ({ draftRevisionAtStart: 0, text: "second", skillNames: [] }),
        },
        performSecond,
        () => undefined,
      ),
    ).rejects.toThrow("A message submission is already pending for this task");
    expect(performSecond).not.toHaveBeenCalled();

    preparation.resolve();
    await first;
    expect(performFirst).toHaveBeenCalledTimes(1);
    // The guarded submission's settlement released the guard: the ref is
    // acquirable again.
    expect(store.getState().submittingRefs.has("ref-a")).toBe(false);
  });

  test("a failed preparation reports through onFailure, never performs, and releases the guard", async () => {
    const store = testPendingTurnsStore();
    const fence = createMutationProjectionFence();
    const onFailure = vi.fn();
    const perform = vi.fn(async () => {});
    const refresh = vi.fn();
    const failure = new Error("durable draft read failed");

    await expect(
      submitWithPendingTracking(
        store,
        fence,
        (work) => work,
        {
          ref: "ref-a",
          onFailure,
          prepare: async () => {
            throw failure;
          },
        },
        perform,
        refresh,
      ),
    ).rejects.toBe(failure);

    expect(onFailure).toHaveBeenCalledWith(failure);
    expect(perform).not.toHaveBeenCalled();
    expect(refresh).toHaveBeenCalledWith("ref-a");
    expect(store.getState().submittingRefs.has("ref-a")).toBe(false);
  });

  test("perform, settlement and the committed report all receive the prepared draft", async () => {
    const store = testPendingTurnsStore();
    const fence = createMutationProjectionFence();
    const prepared: SubmittedDraft = { draftRevisionAtStart: 4, text: "captured", skillNames: ["pkg:x"] };
    const seenInPerform: SubmittedDraft[] = [];
    const settleDraft = vi.fn(async () => ({ cleared: true, draftUnchanged: true }));
    const onCommitted = vi.fn();

    await submitWithPendingTracking(
      store,
      fence,
      (work) => work,
      { ref: "ref-a", onFailure: vi.fn(), prepare: async () => prepared, settleDraft },
      async (submitted) => {
        seenInPerform.push(submitted);
      },
      () => undefined,
      onCommitted,
    );

    expect(seenInPerform).toEqual([prepared]);
    expect(settleDraft).toHaveBeenCalledWith("ref-a", prepared);
    expect(onCommitted).toHaveBeenCalledWith({ cleared: true, draftUnchanged: true }, prepared);
  });

  test("tracks the whole call before perform ever resolves", () => {
    const store = testPendingTurnsStore();
    const fence = createMutationProjectionFence();
    const { track, tracked } = trackingSpy();
    let resolvePerform: () => void = () => undefined;

    void submitWithPendingTracking(
      store,
      fence,
      track,
      {
        ref: "ref-a",
        prepare: async () => ({ draftRevisionAtStart: 0, text: "hi", skillNames: [] }),
        onFailure: vi.fn(),
      },
      (submitted) =>
        new Promise<void>((resolve) => {
          expect(submitted.text).toBe("hi");
          resolvePerform = resolve;
        }),
      () => undefined,
    );

    expect(tracked).toHaveLength(1);
    resolvePerform();
  });

  test("reports perform's rejection to onFailure, rejects the caller, and still refreshes", async () => {
    const store = testPendingTurnsStore();
    const fence = createMutationProjectionFence();
    const onFailure = vi.fn();
    const refresh = vi.fn();
    const failure = new Error("network down");

    await expect(
      submitWithPendingTracking(
        store,
        fence,
        (work) => work,
        {
          ref: "ref-a",
          prepare: async () => ({ draftRevisionAtStart: 0, text: "hi", skillNames: [] }),
          onFailure,
        },
        () => Promise.reject(failure),
        refresh,
      ),
    ).rejects.toBe(failure);

    expect(onFailure).toHaveBeenCalledWith(failure);
    expect(refresh).toHaveBeenCalledWith("ref-a");
    expect(store.getState().submittingRefs.has("ref-a")).toBe(false);
  });

  test("settles the draft and reports what it decided once perform commits", async () => {
    const store = testPendingTurnsStore();
    const fence = createMutationProjectionFence();
    const onCommitted = vi.fn();

    await submitWithPendingTracking(
      store,
      fence,
      (work) => work,
      {
        ref: "ref-a",
        prepare: async () => ({ draftRevisionAtStart: 0, text: "", skillNames: [] }),
        onFailure: vi.fn(),
      },
      () => Promise.resolve(),
      () => undefined,
      onCommitted,
    );

    expect(onCommitted).toHaveBeenCalledWith(
      { cleared: true, draftUnchanged: true },
      {
        draftRevisionAtStart: 0,
        text: "",
        skillNames: [],
      },
    );
  });

  test("reports the draft as neither cleared nor unchanged once it has moved on since the submission started", async () => {
    const store = testPendingTurnsStore({
      draft: {
        readDraftRevision: () => 7,
        readComposerDraft: () => ({ text: "edited", skillNames: [] }),
        clearDraft: () => undefined,
      },
    });
    const fence = createMutationProjectionFence();
    const onCommitted = vi.fn();

    await submitWithPendingTracking(
      store,
      fence,
      (work) => work,
      {
        ref: "ref-a",
        prepare: async () => ({ draftRevisionAtStart: 0, text: "", skillNames: [] }),
        onFailure: vi.fn(),
      },
      () => Promise.resolve(),
      () => undefined,
      onCommitted,
    );

    expect(onCommitted).toHaveBeenCalledWith(
      { cleared: false, draftUnchanged: false },
      {
        draftRevisionAtStart: 0,
        text: "",
        skillNames: [],
      },
    );
  });

  test("a fence reset mid-flight skips settling the draft and releasing the submission guard, but still refreshes", async () => {
    const store = testPendingTurnsStore();
    const fence = createMutationProjectionFence();
    const onCommitted = vi.fn();
    const refresh = vi.fn();

    await submitWithPendingTracking(
      store,
      fence,
      (work) => work,
      {
        ref: "ref-a",
        prepare: async () => ({ draftRevisionAtStart: 0, text: "", skillNames: [] }),
        onFailure: vi.fn(),
      },
      () => {
        fence.reset();
        return Promise.resolve();
      },
      refresh,
      onCommitted,
    );

    expect(onCommitted).not.toHaveBeenCalled();
    // A retired mount's submission guard is not this call's to clear once a
    // reset has moved the epoch out from under it - only a fresh
    // beginSubmission (or a store reset) clears it now.
    expect(store.getState().submittingRefs.has("ref-a")).toBe(true);
    expect(refresh).toHaveBeenCalledWith("ref-a");
  });

  test("awaits supplied async settlement, reports its exact result, and skips synchronous settlement", async () => {
    const readRevision = vi.fn(() => 0);
    const clearDraft = vi.fn();
    const store = testPendingTurnsStore({
      draft: {
        readDraftRevision: readRevision,
        readComposerDraft: () => ({ text: "sent", skillNames: [] }),
        clearDraft,
      },
    });
    const fence = createMutationProjectionFence();
    const onCommitted = vi.fn();
    const settlementEntered = deferred<void>();
    const settlementGate = deferred<{ cleared: boolean; draftUnchanged: boolean }>();

    const pending = submitWithPendingTracking(
      store,
      fence,
      (work) => work,
      {
        ref: "ref-a",
        prepare: async () => ({ draftRevisionAtStart: 0, text: "sent", skillNames: [] }),
        onFailure: vi.fn(),
        settleDraft: () => {
          settlementEntered.resolve();
          return settlementGate.promise;
        },
      },
      () => Promise.resolve(),
      () => undefined,
      onCommitted,
    );

    try {
      // Observe settlement entry directly. With no hook the completed submission
      // wins this race promptly instead of the assertion hanging on a gate.
      await Promise.race([settlementEntered.promise, pending]);
      // While settlement is entered the submission is still in flight: the
      // observer is not notified, and the synchronous store settlement does not run.
      expect(onCommitted).not.toHaveBeenCalled();
      expect(readRevision).not.toHaveBeenCalled();
      expect(clearDraft).not.toHaveBeenCalled();
      settlementGate.resolve({ cleared: false, draftUnchanged: false });
      await pending;
      expect(onCommitted).toHaveBeenCalledWith(
        { cleared: false, draftUnchanged: false },
        {
          draftRevisionAtStart: 0,
          text: "sent",
          skillNames: [],
        },
      );
    } finally {
      // Always release and await the owned pending work, on failure as well as success.
      settlementGate.resolve({ cleared: false, draftUnchanged: false });
      await pending;
    }
  });

  test("settlement rejection reports both flags false without relabeling accepted input as failed", async () => {
    const store = testPendingTurnsStore({
      draft: {
        readDraftRevision: () => 0,
        readComposerDraft: () => ({ text: "sent", skillNames: [] }),
        clearDraft: () => undefined,
      },
    });
    const fence = createMutationProjectionFence();
    const onFailure = vi.fn();
    const perform = vi.fn(async () => {});
    const onCommitted = vi.fn();

    await expect(
      submitWithPendingTracking(
        store,
        fence,
        (work) => work,
        {
          ref: "ref-a",
          prepare: async () => ({ draftRevisionAtStart: 0, text: "sent", skillNames: [] }),
          onFailure,
          settleDraft: async () => {
            throw new Error("storage");
          },
        },
        perform,
        () => undefined,
        onCommitted,
      ),
    ).resolves.toBeUndefined();

    expect(perform).toHaveBeenCalledTimes(1);
    expect(onFailure).not.toHaveBeenCalled();
    expect(onCommitted).toHaveBeenCalledWith(
      { cleared: false, draftUnchanged: false },
      {
        draftRevisionAtStart: 0,
        text: "sent",
        skillNames: [],
      },
    );
  });

  test("a committed-observer fault cannot reject accepted input once async settlement is installed", async () => {
    const store = testPendingTurnsStore();
    const fence = createMutationProjectionFence();
    const perform = vi.fn(async () => {});
    const onFailure = vi.fn();
    const onCommitted = vi.fn(() => {
      throw new Error("observer");
    });

    await expect(
      submitWithPendingTracking(
        store,
        fence,
        (work) => work,
        {
          ref: "ref-a",
          prepare: async () => ({ draftRevisionAtStart: 0, text: "sent", skillNames: [] }),
          onFailure,
          settleDraft: async () => ({ cleared: true, draftUnchanged: true }),
        },
        perform,
        () => undefined,
        onCommitted,
      ),
    ).resolves.toBeUndefined();

    expect(perform).toHaveBeenCalledTimes(1);
    expect(onFailure).not.toHaveBeenCalled();
    expect(onCommitted).toHaveBeenCalledTimes(1);
  });

  test("a refresh fault cannot reject accepted input once async settlement is installed", async () => {
    const store = testPendingTurnsStore();
    const fence = createMutationProjectionFence();
    const onFailure = vi.fn();
    const refresh = vi.fn(() => {
      throw new Error("refresh");
    });

    await expect(
      submitWithPendingTracking(
        store,
        fence,
        (work) => work,
        {
          ref: "ref-a",
          prepare: async () => ({ draftRevisionAtStart: 0, text: "sent", skillNames: [] }),
          onFailure,
          settleDraft: async () => ({ cleared: true, draftUnchanged: true }),
        },
        () => Promise.resolve(),
        refresh,
        vi.fn(),
      ),
    ).resolves.toBeUndefined();

    expect(onFailure).not.toHaveBeenCalled();
    expect(refresh).toHaveBeenCalledWith("ref-a");
  });

  test("an epoch change while settlement waits neither notifies nor releases a newer submission's guard", async () => {
    const store = testPendingTurnsStore();
    const fence = createMutationProjectionFence();
    const onCommitted = vi.fn();
    const refresh = vi.fn();
    const settlementEntered = deferred<void>();
    const settlementGate = deferred<{ cleared: boolean; draftUnchanged: boolean }>();

    const pending = submitWithPendingTracking(
      store,
      fence,
      (work) => work,
      {
        ref: "ref-a",
        prepare: async () => ({ draftRevisionAtStart: 0, text: "sent", skillNames: [] }),
        onFailure: vi.fn(),
        settleDraft: () => {
          settlementEntered.resolve();
          return settlementGate.promise;
        },
      },
      () => Promise.resolve(),
      refresh,
      onCommitted,
    );

    try {
      await Promise.race([settlementEntered.promise, pending]);
      expect(onCommitted).not.toHaveBeenCalled();
      // The captured epoch moves out from under the in-flight settlement, and a
      // newer submission takes the guard through the real store lifecycle.
      fence.reset();
      store.endSubmission("ref-a");
      expect(store.beginSubmission("ref-a")).toBe(true);
      settlementGate.resolve({ cleared: true, draftUnchanged: true });
      await pending;

      // The stale settlement completion must not notify or release the newer guard.
      expect(onCommitted).not.toHaveBeenCalled();
      expect(store.getState().submittingRefs.has("ref-a")).toBe(true);
      expect(refresh).toHaveBeenCalledWith("ref-a");
    } finally {
      settlementGate.resolve({ cleared: true, draftUnchanged: true });
      await pending;
    }
  });

  test("perform rejection remains the failure path and the settlement hook never runs", async () => {
    const store = testPendingTurnsStore();
    const fence = createMutationProjectionFence();
    const onFailure = vi.fn();
    const settleDraft = vi.fn(async () => ({ cleared: true, draftUnchanged: true }));
    const refresh = vi.fn();
    const failure = new Error("network down");

    await expect(
      submitWithPendingTracking(
        store,
        fence,
        (work) => work,
        {
          ref: "ref-a",
          prepare: async () => ({ draftRevisionAtStart: 0, text: "sent", skillNames: [] }),
          onFailure,
          settleDraft,
        },
        () => Promise.reject(failure),
        refresh,
      ),
    ).rejects.toBe(failure);

    expect(onFailure).toHaveBeenCalledWith(failure);
    expect(settleDraft).not.toHaveBeenCalled();
    expect(refresh).toHaveBeenCalledWith("ref-a");
    expect(store.getState().submittingRefs.has("ref-a")).toBe(false);
  });

  test("without an async hook, a committed-observer fault still propagates without relabeling failure", async () => {
    const store = testPendingTurnsStore();
    const fence = createMutationProjectionFence();
    const onFailure = vi.fn();
    const observerError = new Error("observer");
    const onCommitted = vi.fn(() => {
      throw observerError;
    });

    await expect(
      submitWithPendingTracking(
        store,
        fence,
        (work) => work,
        {
          ref: "ref-a",
          prepare: async () => ({ draftRevisionAtStart: 0, text: "", skillNames: [] }),
          onFailure,
        },
        () => Promise.resolve(),
        () => undefined,
        onCommitted,
      ),
    ).rejects.toBe(observerError);

    expect(onFailure).not.toHaveBeenCalled();
    expect(store.getState().submittingRefs.has("ref-a")).toBe(false);
  });

  test("without an async hook, a refresh fault still propagates", async () => {
    const store = testPendingTurnsStore();
    const fence = createMutationProjectionFence();
    const onFailure = vi.fn();
    const refreshError = new Error("refresh");
    const refresh = vi.fn(() => {
      throw refreshError;
    });

    await expect(
      submitWithPendingTracking(
        store,
        fence,
        (work) => work,
        {
          ref: "ref-a",
          prepare: async () => ({ draftRevisionAtStart: 0, text: "", skillNames: [] }),
          onFailure,
        },
        () => Promise.resolve(),
        refresh,
        vi.fn(),
      ),
    ).rejects.toBe(refreshError);

    expect(onFailure).not.toHaveBeenCalled();
    expect(store.getState().submittingRefs.has("ref-a")).toBe(false);
  });

  test("without an async hook, the synchronous store settlement notifies in order before resolution", async () => {
    const store = testPendingTurnsStore();
    const fence = createMutationProjectionFence();
    const order: string[] = [];
    const settle = store.settleSubmittedDraft.bind(store);
    store.settleSubmittedDraft = (ref, submitted) => {
      order.push("settle");
      return settle(ref, submitted);
    };
    let observedAtNotify: string[] = [];
    const onCommitted = vi.fn(() => {
      observedAtNotify = [...order];
    });

    await submitWithPendingTracking(
      store,
      fence,
      (work) => work,
      {
        ref: "ref-a",
        prepare: async () => ({ draftRevisionAtStart: 0, text: "", skillNames: [] }),
        onFailure: vi.fn(),
      },
      () => Promise.resolve(),
      () => undefined,
      onCommitted,
    );
    order.push("resolved");

    expect(observedAtNotify).toEqual(["settle"]);
    expect(order).toEqual(["settle", "resolved"]);
    expect(onCommitted).toHaveBeenCalledWith(
      { cleared: true, draftUnchanged: true },
      {
        draftRevisionAtStart: 0,
        text: "",
        skillNames: [],
      },
    );
  });
});
