import { describe, expect, it, vi } from "vitest";
import { getToasts } from "../../../widgets/toast/store";
import {
  captureRecoverySendIntent,
  pushRecoveryRefusal,
  pushSelectionRefusal,
  selectionRefusal,
  sendableAttachments,
} from "./submitProtocol";

describe("sendableAttachments", () => {
  const image = (id: string, marker: number) => ({ id, marker, name: `${id}.png` });

  it("ships a staged image the submitted text spells", () => {
    expect(sendableAttachments([image("a", 1)], "look [image 1]", { foreign: [], restored: [] })).toEqual([
      image("a", 1),
    ]);
  });

  it("keeps a restored image sendable under the number the merge gave it", () => {
    // The row's image arrived as 1, collided with this draft's own 1, and the
    // merge renumbered it to 2. Its text never spells the new number.
    expect(
      sendableAttachments([image("local", 1), image("restored", 2)], "local [image 1]", {
        foreign: [],
        restored: ["restored"],
      }),
    ).toEqual([image("local", 1), image("restored", 2)]);
  });

  it("refuses an image this source was holding before it adopted a peer draft", () => {
    // Same number, different identity: the peer's text is not this tile's.
    expect(
      sendableAttachments([image("foreign", 1)], "peer prose [image 1]", {
        foreign: ["foreign"],
        restored: [],
      }),
    ).toEqual([]);
  });

  it("gives a new image ordinary rules when it reuses a number", () => {
    // The tile that once held "1" is gone, so nothing recorded against it can
    // apply to the tile that now bears number 1.
    expect(
      sendableAttachments([image("reused", 1)], "typed [image 1]", {
        foreign: [],
        restored: [],
      }),
    ).toEqual([image("reused", 1)]);
  });
});

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
