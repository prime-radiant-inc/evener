import { describe, expect, it, vi } from "vitest";
import { getToasts } from "../../../widgets/toast/store";
import {
  captureRecoverySendIntent,
  pushRecoveryRefusal,
  pushSelectionRefusal,
  selectionRefusal,
  spelledImageMarkers,
  unbackedImageMarkersIn,
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

describe("unbacked image markers", () => {
  it("reads every marker the text spells, deduplicated and ascending", () => {
    expect(spelledImageMarkers("no anchors here")).toEqual([]);
    expect(spelledImageMarkers("[image 2] then [image 1] then [image 2]")).toEqual([1, 2]);
    // A near miss is not a marker: the pattern is the one markerText renders.
    expect(spelledImageMarkers("[image two] [image ] image 3")).toEqual([]);
  });

  it("reports adopted markers the outgoing text still spells, and nothing else", () => {
    expect(unbackedImageMarkersIn("Cascade reader draft[image 1]", [1])).toEqual([1]);
    // Typed prose is never adopted, so it is never refused.
    expect(unbackedImageMarkersIn("look at [image 1]", [])).toEqual([]);
    // The person deleted the adopted reference: the anchor is gone with it.
    expect(unbackedImageMarkersIn("Cascade reader draft", [1])).toEqual([]);
    // An adopted marker that no longer appears is not reported, while one that
    // still does is - the pair is read as it stands.
    expect(unbackedImageMarkersIn("[image 2][image 3]", [1, 3])).toEqual([3]);
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
