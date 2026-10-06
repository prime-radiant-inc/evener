import "fake-indexeddb/auto";
import { currentTranscriptState, hydrateThread, type ThreadModel } from "@evener/appwire-client";
import { currentRead } from "@evener/appwire-client/testing/currentTranscript";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, describe, expect, test, vi } from "vitest";
import {
  type BackgroundTranscriptOptions,
  backgroundTranscriptOptions,
  DEFAULT_BACKGROUND_TRANSCRIPT_OPTIONS,
  installBackgroundTranscriptOptionsForTests,
} from "./backgroundTranscriptOptions";
import { SessionCacheIndexedDB } from "./sessionCacheIndexedDB";
import { encodedPayloadBytes } from "./transcriptPayloadBudget";
import { createTranscriptResidency, type TranscriptResidencyPort } from "./transcriptResidency";

const caches: SessionCacheIndexedDB[] = [];
afterEach(() => {
  for (const cache of caches.splice(0)) cache.close();
});

function setup(model: ThreadModel) {
  const cache = new SessionCacheIndexedDB({
    indexedDB: new IDBFactory(),
    databaseName: `residency-${crypto.randomUUID()}`,
  });
  caches.push(cache);
  let resident: ThreadModel | undefined = model;
  const port: TranscriptResidencyPort = {
    cache,
    resident: () => resident,
    publish: (_ref, next) => {
      resident = next;
    },
    unload: () => {
      resident = undefined;
    },
    admission: () => undefined,
    fold: (_ref, current) => current,
    hydrate: async () => {},
  };
  const residency = createTranscriptResidency(port, { ...DEFAULT_BACKGROUND_TRANSCRIPT_OPTIONS, residentBytes: 0 });
  residency.connection(1, true);
  residency.owners(model.ref, { foreground: 0, history: 0, mutationPinned: false, background: 1 });
  return { cache, residency, port, getResident: () => resident };
}

describe("transcript residency", () => {
  test("accepts zero resident budget and restores options while rejecting invalid limits", () => {
    const original = backgroundTranscriptOptions();
    const restore = installBackgroundTranscriptOptionsForTests({ residentBytes: 0 });
    try {
      expect(backgroundTranscriptOptions().residentBytes).toBe(0);
      const invalidOptions: Partial<BackgroundTranscriptOptions>[] = [
        { residentBytes: -1 },
        { residentBytes: Number.NaN },
        { pendingBytes: 0 },
        { pendingBytes: -1 },
        { pendingBytes: 1.5 },
        { networkHydrations: 0 },
        { networkHydrations: Number.POSITIVE_INFINITY },
      ];
      invalidOptions.push({ diskWorkers: 2 as 1 });
      for (const invalid of invalidOptions) {
        expect(() => installBackgroundTranscriptOptionsForTests(invalid)).toThrow(RangeError);
      }
    } finally {
      restore();
    }
    expect(backgroundTranscriptOptions()).toBe(original);
  });

  test("commits an owner-scoped checkpoint before unloading and promotes it without hydration", async () => {
    const model = hydrateThread(currentRead("local:one"), "local:one", 1);
    const { residency, getResident } = setup(model);
    residency.published(model.ref, model, 0);
    await residency.settled();

    expect(getResident()).toBeUndefined();
    expect(residency.snapshot().committedWrites).toBe(1);
    const promoted = await residency.promote(model.ref);
    expect(promoted?.ref).toBe(model.ref);
    expect(getResident()).toBe(promoted);
    expect(residency.snapshot().networkHydrations).toBe(0);
  });

  test("limits background hydration to two slots and starts queued foreground work first", async () => {
    const model = hydrateThread(currentRead("local:one"), "local:one", 1);
    const { residency, port } = setup(model);
    const refs = ["local:one", "local:two", "local:three", "local:four"];
    const gates = new Map(refs.map((ref) => [ref, deferred<void>()]));
    const started: Array<{ ref: string; intent: string }> = [];
    port.hydrate = async (ref, intent) => {
      started.push({ ref, intent });
      await gates.get(ref)?.promise;
    };
    for (const ref of refs) {
      residency.owners(ref, {
        foreground: ref === "local:four" ? 1 : 0,
        history: 0,
        mutationPinned: false,
        background: 1,
      });
    }
    residency.revoke("local:one", "gap");
    residency.revoke("local:two", "gap");
    await vi.waitFor(() => expect(started.map(({ ref }) => ref)).toEqual(["local:one", "local:two"]));
    residency.revoke("local:three", "gap");
    residency.revoke("local:four", "gap");
    expect(residency.snapshot().networkHydrations).toBe(2);
    gates.get("local:one")?.resolve();
    await vi.waitFor(() => expect(started.map(({ ref }) => ref)).toEqual(["local:one", "local:two", "local:four"]));
    expect(started[2]).toEqual({ ref: "local:four", intent: "foreground" });
    gates.get("local:two")?.resolve();
    await vi.waitFor(() =>
      expect(started.map(({ ref }) => ref)).toEqual(["local:one", "local:two", "local:four", "local:three"]),
    );
    gates.get("local:four")?.resolve();
    gates.get("local:three")?.resolve();
    await vi.waitFor(() => expect(residency.snapshot().networkHydrations).toBe(0));
  });

  test("continues FIFO disk work after one real-cache boundary fails", async () => {
    const cache = new SessionCacheIndexedDB({
      indexedDB: new IDBFactory(),
      databaseName: `residency-${crypto.randomUUID()}`,
    });
    caches.push(cache);
    const first = hydrateThread(currentRead("local:one"), "local:one", 1);
    const second = hydrateThread(currentRead("local:two"), "local:two", 1);
    const residents = new Map<string, ThreadModel>();
    const port: TranscriptResidencyPort = {
      cache,
      resident: (ref) => residents.get(ref),
      publish: (ref, model) => residents.set(ref, model),
      unload: (ref) => {
        residents.delete(ref);
      },
      admission: () => undefined,
      fold: (_ref, model) => model,
      hydrate: async () => {},
    };
    const residency = createTranscriptResidency(port, DEFAULT_BACKGROUND_TRANSCRIPT_OPTIONS);
    residency.connection(1, true);
    for (const ref of [first.ref, second.ref]) {
      residency.owners(ref, { foreground: 0, history: 0, mutationPinned: false, background: 1 });
    }
    const originalPut = cache.putCurrent.bind(cache);
    const order: string[] = [];
    const committed: Array<{ ownerToken: string; ref: string }> = [];
    let failFirst = true;
    const put = vi.spyOn(cache, "putCurrent").mockImplementation(async (record, now) => {
      order.push(record.ref);
      if (record.ref === first.ref && failFirst) {
        failFirst = false;
        throw new Error("injected cache write failure");
      }
      committed.push({ ownerToken: record.ownerToken, ref: record.ref });
      return originalPut(record, now);
    });
    try {
      port.publish(first.ref, first);
      port.publish(second.ref, second);
      residency.published(first.ref, first, 0);
      residency.published(first.ref, first, 1);
      residency.published(second.ref, second, 0);
      await residency.settled();

      expect(order).toEqual([first.ref, second.ref]);
      expect(residency.snapshot().diskWorkers).toBe(0);
      expect(residency.snapshot().refs.get(first.ref)?.persistence).toBe("repairing");
      const secondWrite = committed.find((record) => record.ref === second.ref);
      expect(secondWrite).toBeDefined();
      await expect(cache.getCurrent([secondWrite!.ownerToken, second.ref], Date.now())).resolves.toMatchObject({
        record: { ref: second.ref },
      });
    } finally {
      put.mockRestore();
      residency.dispose();
    }
  });

  test("evicts the least recently foreground-used resident when admitting another checkpoint", async () => {
    const cache = new SessionCacheIndexedDB({
      indexedDB: new IDBFactory(),
      databaseName: `residency-${crypto.randomUUID()}`,
    });
    caches.push(cache);
    const first = hydrateThread(currentRead("local:one"), "local:one", 1);
    const second = hydrateThread(currentRead("local:two"), "local:two", 1);
    const third = hydrateThread(currentRead("local:tri"), "local:tri", 1);
    const residents = new Map<string, ThreadModel>();
    let hydrateCalls = 0;
    const port: TranscriptResidencyPort = {
      cache,
      resident: (ref) => residents.get(ref),
      publish: (ref, model) => residents.set(ref, model),
      unload: (ref) => {
        residents.delete(ref);
      },
      admission: () => undefined,
      fold: (_ref, model) => model,
      hydrate: async () => {
        hydrateCalls += 1;
      },
    };
    const bytes = encodedPayloadBytes(currentTranscriptState(first));
    expect(encodedPayloadBytes(currentTranscriptState(second))).toBe(bytes);
    expect(encodedPayloadBytes(currentTranscriptState(third))).toBe(bytes);
    const residency = createTranscriptResidency(port, {
      ...DEFAULT_BACKGROUND_TRANSCRIPT_OPTIONS,
      residentBytes: bytes * 2,
    });
    residency.connection(1, true);
    for (const ref of [first.ref, second.ref, third.ref]) {
      residency.owners(ref, { foreground: 0, history: 0, mutationPinned: false, background: 1 });
    }
    port.publish(first.ref, first);
    residency.published(first.ref, first, 0);
    await residency.settled();
    port.publish(second.ref, second);
    residency.published(second.ref, second, 0);
    await residency.settled();

    let clock = 0;
    const now = vi.spyOn(Date, "now").mockImplementation(() => ++clock);
    residency.owners(second.ref, { foreground: 1, history: 0, mutationPinned: false, background: 1 });
    residency.owners(second.ref, { foreground: 0, history: 0, mutationPinned: false, background: 1 });
    residency.owners(first.ref, { foreground: 1, history: 0, mutationPinned: false, background: 1 });
    residency.owners(first.ref, { foreground: 0, history: 0, mutationPinned: false, background: 1 });
    now.mockRestore();

    port.publish(third.ref, third);
    residency.published(third.ref, third, 0);
    await residency.settled();

    const snapshot = residency.snapshot();
    expect(snapshot.residentBytes).toBeLessThanOrEqual(bytes * 2);
    expect(Object.fromEntries([...snapshot.refs].map(([ref, entry]) => [ref, entry.resident]))).toEqual({
      [first.ref]: true,
      [second.ref]: false,
      [third.ref]: true,
    });
    expect(snapshot.refs.get(second.ref)?.current).toBe(true);
    expect(snapshot.refs.get(second.ref)?.resident).toBe(false);
    expect(snapshot.refs.get(third.ref)?.resident).toBe(true);
    expect(hydrateCalls).toBe(0);
  });
});
