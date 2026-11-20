// The final release of a conversation's last view must retire the module
// maps model-control reads keep (observed through modelControlReadStateForTests,
// the store's sanctioned seam for module-state lifecycles): browsing distinct
// conversations must not grow them for the tab's lifetime, and a settled
// failed read must not keep a closed thread's slot alive.
import "fake-indexeddb/auto";
import { type ModelControlSnapshot, type ThreadReadResponse, WireError } from "@evener/appwire-client";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { IDBFactory, IDBKeyRange } from "fake-indexeddb";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { connectFakeClient } from "../testing/connectionTestUtils";
import { ALL_CAPABILITIES as CAPABILITIES } from "../threadTestUtils";
import { resetConnectionStoreForTests } from "./connection";
import { setProductionEntryCacheForTests } from "./entryTranscriptRoutes";
import { SessionEntryCache } from "./sessionEntryCache";
import { modelControlReadStateForTests, resetThreadsStoreForTests, threadsStore } from "./threads";
import { flushUntil, readResponse } from "./threadsSessionActionsTestUtils";

let cache: SessionEntryCache;

beforeEach(() => {
  resetConnectionStoreForTests();
  resetThreadsStoreForTests();
  cache = new SessionEntryCache({
    indexedDB: new IDBFactory(),
    keyRange: IDBKeyRange,
    databaseName: `model-control-lifecycle-${crypto.randomUUID()}`,
  });
  setProductionEntryCacheForTests(cache);
});

afterEach(() => {
  resetThreadsStoreForTests();
  cache.close();
  vi.restoreAllMocks();
});

function snapshot(
  revision: number,
  options: { instanceId?: string; bootGeneration?: string } = {},
): ModelControlSnapshot {
  return {
    authority: {
      instanceId: options.instanceId ?? "thr_ref_a",
      bootGeneration: options.bootGeneration ?? "1",
    },
    revision,
    active: {
      modelProvider: "anthropic",
      model: "claude-sonnet-4-5",
      reasoningEffortLevels: ["low", "high"],
      supportsReasoning: true,
    },
  };
}

/** A read that establishes v1 model-control authority for `ref`. */
function capableRead(ref: string, model: ModelControlSnapshot): ThreadReadResponse {
  return readResponse(ref, {
    modelProvider: model.active.modelProvider,
    evener: {
      ref,
      capabilities: { ...CAPABILITIES, modelControlVersion: 1 },
      modelControl: model,
      queue: { revision: 0 },
    },
  });
}

describe("model-control read bookkeeping lifecycle", () => {
  test("the final release retires the closed ref's read bookkeeping while another conversation stays open", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", (params) =>
      capableRead(
        String((params as { ref: string }).ref),
        snapshot(1, { instanceId: `thr_${String((params as { ref: string }).ref)}` }),
      ),
    );
    await threadsStore.getState().ensureThread("ref_a");
    expect(threadsStore.getState().threads.get("ref_a")?.modelControlState.kind).toBe("ready");
    expect(modelControlReadStateForTests().sequences.has("ref_a")).toBe(true);

    // The last view of ref_a closes; its model drops with the release.
    threadsStore.getState().releaseThread("ref_a");
    await vi.waitFor(() => expect(threadsStore.getState().threads.has("ref_a")).toBe(false));

    // A different conversation opens: browsing on, the tab's bookkeeping must
    // not still carry the closed conversation's read state.
    await threadsStore.getState().ensureThread("ref_b");
    expect(threadsStore.getState().threads.has("ref_b")).toBe(true);
    const retained = modelControlReadStateForTests();
    expect(retained.sequences.has("ref_a")).toBe(false);
    expect(retained.bindingRefs).not.toContain("ref_a");
  });

  test("the final release retires a settled failed read's binding entry", async () => {
    const fake = connectFakeClient();
    const ref = "ref_a";
    fake.on("thread/read", () => capableRead(ref, snapshot(1)));
    await threadsStore.getState().ensureThread(ref);

    // A stale-authority refusal moves the model to `recovering` and starts the
    // held replacement read; that read's pending carries the ref's binding.
    const replacement = deferred<ThreadReadResponse>();
    fake.on("thread/read", () => replacement.promise);
    fake.on("thread/model/set", () => {
      throw new WireError("authority changed", -32602, {
        evenerErrorInfo: "authorityStale",
        mutationOutcome: "notAccepted",
        retryDisposition: "none",
      });
    });
    await expect(threadsStore.getState().setModel(ref, "openai", "gpt-5.5")).rejects.toBeInstanceOf(WireError);
    await flushUntil(() => threadsStore.getState().threads.get(ref)?.modelControlState.kind === "recovering");
    expect(modelControlReadStateForTests().bindingRefs).toContain(ref);

    // The last view closes while the replacement read is still out. The
    // release retires the pending (settling the binding failed) and the final
    // drain drops the model once the read answers.
    threadsStore.getState().releaseThread(ref);
    replacement.resolve(capableRead(ref, snapshot(2)));
    await vi.waitFor(() => expect(threadsStore.getState().threads.has(ref)).toBe(false));

    // The binding settled failed for the closed lifetime: the entry holding
    // that settled error must not survive the close.
    expect(modelControlReadStateForTests().bindingRefs).not.toContain(ref);
    expect(modelControlReadStateForTests().sequences.has(ref)).toBe(false);
  });

  test("a reopened conversation's read sequence never reuses a value from before the close", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", (params) =>
      capableRead(
        String((params as { ref: string }).ref),
        snapshot(1, { instanceId: `thr_${String((params as { ref: string }).ref)}` }),
      ),
    );
    await threadsStore.getState().ensureThread("ref_a");
    const before = modelControlReadStateForTests().sequences.get("ref_a");
    expect(before).toBeDefined();

    threadsStore.getState().releaseThread("ref_a");
    await vi.waitFor(() => expect(threadsStore.getState().threads.has("ref_a")).toBe(false));

    // A fence captured while the first view was open must never read as
    // current again after the close, whatever the reopened view does: the
    // fresh read's sequence value has to be strictly newer than every value
    // the closed lifetime stamped, not restart from the same base.
    await threadsStore.getState().ensureThread("ref_a");
    const after = modelControlReadStateForTests().sequences.get("ref_a");
    expect(after).toBeDefined();
    expect(after!).toBeGreaterThan(before!);
  });
});
