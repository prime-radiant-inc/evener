// @vitest-environment node

import { describe, expect, test, vi } from "vitest";
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

describe("submitWithPendingTracking", () => {
  test("rejects immediately when a submission is already pending for the ref", async () => {
    const store = testPendingTurnsStore();
    store.beginSubmission("ref-a");
    const fence = createMutationProjectionFence();
    const { track } = trackingSpy();

    await expect(
      submitWithPendingTracking(
        store,
        fence,
        track,
        { ref: "ref-a", draftRevisionAtStart: 0, text: "hi", skillNames: [], onFailure: vi.fn() },
        () => Promise.resolve(),
        () => undefined,
      ),
    ).rejects.toThrow("A message submission is already pending for this task");
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
      { ref: "ref-a", draftRevisionAtStart: 0, text: "hi", skillNames: [], onFailure: vi.fn() },
      () =>
        new Promise<void>((resolve) => {
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
        { ref: "ref-a", draftRevisionAtStart: 0, text: "hi", skillNames: [], onFailure },
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
      { ref: "ref-a", draftRevisionAtStart: 0, text: "", skillNames: [], onFailure: vi.fn() },
      () => Promise.resolve(),
      () => undefined,
      onCommitted,
    );

    expect(onCommitted).toHaveBeenCalledWith({ cleared: true, draftUnchanged: true });
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
      { ref: "ref-a", draftRevisionAtStart: 0, text: "", skillNames: [], onFailure: vi.fn() },
      () => Promise.resolve(),
      () => undefined,
      onCommitted,
    );

    expect(onCommitted).toHaveBeenCalledWith({ cleared: false, draftUnchanged: false });
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
      { ref: "ref-a", draftRevisionAtStart: 0, text: "", skillNames: [], onFailure: vi.fn() },
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
});
