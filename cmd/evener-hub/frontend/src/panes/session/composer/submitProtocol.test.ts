import { describe, expect, it, vi } from "vitest";
import { getToasts } from "../../../widgets/toast/store";
import {
  captureRecoverySendIntent,
  pushRecoveryRefusal,
  pushSelectionRefusal,
  selectionRefusal,
} from "./submitProtocol";

describe("selectionRefusal", () => {
  it("refuses staged selections the target never advertised", () => {
    expect(selectionRefusal({ skillNames: ["a"] }, undefined)).toBe("skill-unsupported");
    expect(selectionRefusal({ skillNames: ["a"] }, { skillInput: false })).toBe("skill-unsupported");
    expect(selectionRefusal({ commandNames: ["c"] }, { skillInput: true })).toBe("command-unsupported");
  });

  it("accepts selections the target advertises and empty selections anywhere", () => {
    expect(selectionRefusal({ skillNames: ["a"], commandNames: ["c"] }, { skillInput: true, commandInput: true })).toBe(
      undefined,
    );
    expect(selectionRefusal({ skillNames: [], commandNames: [] }, undefined)).toBe(undefined);
    expect(selectionRefusal({}, undefined)).toBe(undefined);
  });
});

describe("refusal toasts", () => {
  it("names each refusal outcome by reason so surfaces report the same thing", () => {
    const before = getToasts().length;
    pushSelectionRefusal("skill-unsupported");
    pushSelectionRefusal("command-unsupported");
    pushRecoveryRefusal("changed");
    pushRecoveryRefusal("consumed");
    expect(
      getToasts()
        .slice(before)
        .map((toast) => [toast.kind, toast.reason]),
    ).toEqual([
      ["error", "skill-unsupported"],
      ["error", "command-unsupported"],
      ["info", "changed"],
      ["info", "consumed"],
    ]);
  });
});

// Round-5 review L2: the press-time capture derives two promises from ONE
// snapshot, and a submission that exits through one half's rejection (the
// capture read failed while the Send waited) leaves the other half forever
// unobserved - a rejected promise with no handler that surfaces as an
// unhandled rejection. Each derived half needs its own inert rejection sink.
import { IDBFactory } from "fake-indexeddb";
import { MutationOutboxIndexedDB } from "../../../stores/mutationOutboxIndexedDB";
import { setMutationStorageForTests } from "../../../stores/threads";

class CaptureFailingStorage extends MutationOutboxIndexedDB {
  getRecoveryWithStopEpoch(): Promise<never> {
    return Promise.reject(new Error("capture read failed"));
  }
}

it("a failed capture leaves the unobserved half no unhandled rejection", async () => {
  const indexedDB = new IDBFactory();
  vi.stubGlobal("indexedDB", indexedDB);
  try {
    const storage = new CaptureFailingStorage({ indexedDB, databaseName: "capture-fails" });
    setMutationStorageForTests(storage);
    const intent = captureRecoverySendIntent("ref-a", "mutation-a");
    // The submission observes one half and exits through its rejection; the
    // other half is never awaited. The no-unhandled-rejection claim is pinned
    // by this file run's exit, not an in-test listener (vitest intercepts
    // process-level unhandledRejection dispatch, so a per-test listener cannot
    // discriminate - the threads.backpressure and transcript-scroll precedents):
    // with the sinks removed this run exits 1 naming the capture failure as
    // the unobserved half's unhandled rejection; with them it exits 0.
    await expect(intent.durableSnapshot).rejects.toBeDefined();
    await new Promise<void>((resolve) => setTimeout(resolve, 0));
    await new Promise<void>((resolve) => setTimeout(resolve, 0));
  } finally {
    vi.unstubAllGlobals();
  }
});
