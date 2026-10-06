import "fake-indexeddb/auto";
import {
  AppwireClient,
  applyNotification,
  currentTranscriptState,
  hydrateThread,
  type ThreadModel,
} from "@evener/appwire-client";
import { currentRead, textDelta } from "@evener/appwire-client/testing/currentTranscript";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { FakeSocket } from "@evener/appwire-client/testing/fakeSocket";
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

  test("queued foreground hydration wins the next real Appwire request slot", async () => {
    const model = hydrateThread(currentRead("local:one"), "local:one", 1);
    const { residency } = setup(model);
    const socket = new FakeSocket({ autoInitialize: true });
    const client = new AppwireClient({ url: "ws://test/rpc", socketFactory: () => socket });
    const connected = client.connect();
    socket.open();
    await connected;

    const readFrames = () =>
      socket.sent
        .map((frame) => JSON.parse(frame) as { id?: number; method?: string; params?: { ref?: string } })
        .filter(
          (frame): frame is { id: number; method: string; params: { ref: string } } =>
            frame.method === "thread/read" && typeof frame.id === "number" && typeof frame.params?.ref === "string",
        )
        .map(({ id, params }) => ({ id, ref: params.ref }));
    const read = (ref: string) => async () => {
      await client.request("thread/read", {
        ref,
        includeTurns: false,
        itemsView: "full",
        subscribe: true,
        itemLimit: 80,
      });
    };
    const tasks = [
      residency.scheduleHydration("local:active-one", "background", read("local:active-one")),
      residency.scheduleHydration("local:active-two", "background", read("local:active-two")),
      residency.scheduleHydration("local:retry", "background", read("local:retry")),
      residency.scheduleHydration("local:healthy", "background", read("local:healthy")),
      residency.scheduleHydration("local:foreground", "foreground", read("local:foreground")),
    ];
    try {
      await vi.waitFor(() =>
        expect(readFrames().map(({ ref }) => ref)).toEqual(["local:active-one", "local:active-two"]),
      );
      expect(residency.snapshot().queuedHydrationRefs).toEqual(["local:foreground", "local:retry", "local:healthy"]);
      expect(residency.snapshot().networkHydrations).toBe(2);

      const first = readFrames()[0]!;
      socket.receive({ id: first.id, result: currentRead(first.ref) });
      await vi.waitFor(() => expect(readFrames()).toHaveLength(3));
      expect(readFrames()[2]?.ref).toBe("local:foreground");

      const second = readFrames()[1]!;
      socket.receive({ id: second.id, result: currentRead(second.ref) });
      await vi.waitFor(() => expect(readFrames()).toHaveLength(4));
      expect(readFrames()[3]?.ref).toBe("local:retry");

      const retry = readFrames()[3]!;
      socket.receive({ id: retry.id, result: currentRead(retry.ref) });
      await vi.waitFor(() => expect(readFrames()).toHaveLength(5));
      expect(readFrames()[4]?.ref).toBe("local:healthy");

      for (const request of [readFrames()[2]!, readFrames()[4]!]) {
        socket.receive({ id: request.id, result: currentRead(request.ref) });
      }
      await Promise.all(tasks);
      expect(residency.snapshot().networkHydrations).toBe(0);
      expect(readFrames().map(({ ref }) => ref)).toEqual([
        "local:active-one",
        "local:active-two",
        "local:foreground",
        "local:retry",
        "local:healthy",
      ]);
    } finally {
      client.close();
      residency.dispose();
      await Promise.allSettled(tasks);
    }
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

  test("uses a one-second trailing checkpoint delay", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    const cache = new SessionCacheIndexedDB({
      indexedDB: new IDBFactory(),
      databaseName: `residency-${crypto.randomUUID()}`,
    });
    caches.push(cache);
    const ref = "local:timed";
    let model: ThreadModel | undefined = hydrateThread(currentRead(ref), ref, 1);
    const port: TranscriptResidencyPort = {
      cache,
      resident: () => model,
      publish: (_ref, next) => {
        model = next;
      },
      unload: () => {
        model = undefined;
      },
      admission: () => undefined,
      fold: (_ref, current, update) => applyNotification(current, update.notification, update.now),
      hydrate: async () => {},
    };
    const residency = createTranscriptResidency(port, DEFAULT_BACKGROUND_TRANSCRIPT_OPTIONS);
    const originalPut = cache.putCurrent.bind(cache);
    const put = vi.spyOn(cache, "putCurrent").mockImplementation((record, now) => originalPut(record, now));
    try {
      residency.connection(1, true);
      residency.owners(ref, { foreground: 0, history: 0, mutationPinned: false, background: 1 });
      residency.published(ref, model!, 0);
      await residency.settled();
      put.mockClear();

      const notification = textDelta(ref, "🙂0");
      model = applyNotification(model!, notification, Date.now());
      residency.published(ref, model!, 1);
      await vi.advanceTimersByTimeAsync(999);
      expect(put).not.toHaveBeenCalled();
      await vi.advanceTimersByTimeAsync(1);
      await vi.waitFor(() => expect(put).toHaveBeenCalledTimes(1));
      await residency.settled();
    } finally {
      residency.dispose();
      put.mockRestore();
      vi.useRealTimers();
    }
  });

  test("caps a streaming checkpoint burst at five seconds", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    const cache = new SessionCacheIndexedDB({
      indexedDB: new IDBFactory(),
      databaseName: `residency-${crypto.randomUUID()}`,
    });
    caches.push(cache);
    const ref = "local:maximum";
    let model: ThreadModel | undefined = hydrateThread(currentRead(ref), ref, 1);
    const port: TranscriptResidencyPort = {
      cache,
      resident: () => model,
      publish: (_ref, next) => {
        model = next;
      },
      unload: () => {
        model = undefined;
      },
      admission: () => undefined,
      fold: (_ref, current, update) => applyNotification(current, update.notification, update.now),
      hydrate: async () => {},
    };
    const residency = createTranscriptResidency(port, DEFAULT_BACKGROUND_TRANSCRIPT_OPTIONS);
    const originalPut = cache.putCurrent.bind(cache);
    const put = vi.spyOn(cache, "putCurrent").mockImplementation((record, now) => originalPut(record, now));
    try {
      residency.connection(1, true);
      residency.owners(ref, { foreground: 0, history: 0, mutationPinned: false, background: 1 });
      residency.published(ref, model!, 0);
      await residency.settled();
      put.mockClear();
      for (let index = 0; index < 7; index += 1) {
        const notification = textDelta(ref, `🙂${index}`);
        model = applyNotification(model!, notification, Date.now());
        residency.published(ref, model!, index + 1);
        if (index < 6) await vi.advanceTimersByTimeAsync(800);
      }
      await vi.advanceTimersByTimeAsync(199);
      expect(put).not.toHaveBeenCalled();
      await vi.advanceTimersByTimeAsync(1);
      await vi.waitFor(() => expect(put).toHaveBeenCalledTimes(1));
    } finally {
      residency.dispose();
      put.mockRestore();
      vi.useRealTimers();
    }
  });

  test("foreground promotion settles its ref after a bounded cut, not the global drain", async () => {
    const cache = new SessionCacheIndexedDB({
      indexedDB: new IDBFactory(),
      databaseName: `residency-${crypto.randomUUID()}`,
    });
    caches.push(cache);
    const target = hydrateThread(currentRead("local:target"), "local:target", 1);
    const unrelated = hydrateThread(currentRead("local:unrelated"), "local:unrelated", 1);
    const residents = new Map<string, ThreadModel>([
      [target.ref, target],
      [unrelated.ref, unrelated],
    ]);
    const port: TranscriptResidencyPort = {
      cache,
      resident: (ref) => residents.get(ref),
      publish: (ref, model) => {
        residents.set(ref, model);
      },
      unload: (ref) => {
        residents.delete(ref);
      },
      admission: () => undefined,
      fold: (_ref, model, update) => applyNotification(model, update.notification, update.now),
      hydrate: async () => {},
    };
    const residency = createTranscriptResidency(port, {
      ...DEFAULT_BACKGROUND_TRANSCRIPT_OPTIONS,
      residentBytes: 0,
    });
    residency.connection(1, true);
    for (const model of [target, unrelated]) {
      residency.owners(model.ref, { foreground: 0, history: 0, mutationPinned: false, background: 1 });
      residency.published(model.ref, model, 0);
    }
    await residency.settled();

    const targetWrite = deferred<void>();
    const targetWriteReached = deferred<void>();
    const unrelatedWrite = deferred<void>();
    const unrelatedWriteReached = deferred<void>();
    const promotionLookup = deferred<void>();
    const releasePromotionLookup = deferred<void>();
    let holdPromotionLookup = false;
    let promotionLookupStarted = false;
    let targetWriteStarted = false;
    let unrelatedWriteStarted = false;
    const originalPut = cache.putCurrent.bind(cache);
    const put = vi.spyOn(cache, "putCurrent").mockImplementation(async (record, now) => {
      if (record.ref === target.ref && !targetWriteStarted) {
        targetWriteStarted = true;
        targetWriteReached.resolve();
        await targetWrite.promise;
      } else if (record.ref === unrelated.ref && !unrelatedWriteStarted) {
        unrelatedWriteStarted = true;
        unrelatedWriteReached.resolve();
        await unrelatedWrite.promise;
      }
      return originalPut(record, now);
    });
    const originalGet = cache.getCurrent.bind(cache);
    const get = vi.spyOn(cache, "getCurrent").mockImplementation(async (...args) => {
      if (holdPromotionLookup && args[0][1] === target.ref) {
        promotionLookupStarted = true;
        promotionLookup.resolve();
        await releasePromotionLookup.promise;
      }
      return originalGet(...args);
    });
    let promotion: Promise<ThreadModel | undefined> | undefined;
    try {
      const throughCut = textDelta(target.ref, "-through-cut");
      residency.accept(target.ref, {
        notification: throughCut,
        sequence: 1,
        now: 2,
        bytes: encodedPayloadBytes(throughCut),
      });
      await targetWriteReached.promise;
      holdPromotionLookup = true;
      promotion = residency.promote(target.ref);
      const unrelatedFrame = textDelta(unrelated.ref, "-unrelated");
      residency.accept(unrelated.ref, {
        notification: unrelatedFrame,
        sequence: 1,
        now: 3,
        bytes: encodedPayloadBytes(unrelatedFrame),
      });
      targetWrite.resolve();
      await unrelatedWriteReached.promise;
      await vi.waitFor(() => expect(promotionLookupStarted).toBe(true), { timeout: 500 });

      const later = textDelta(target.ref, "-later");
      residency.accept(target.ref, {
        notification: later,
        sequence: 2,
        now: 4,
        bytes: encodedPayloadBytes(later),
      });
      releasePromotionLookup.resolve();
      const promoted = await promotion;
      expect(promoted).toBeDefined();
      const text = JSON.stringify(currentTranscriptState(promoted!));
      expect(text).toContain("-through-cut");
      expect(text).toContain("-later");
      expect(put).toHaveBeenCalledTimes(2);
    } finally {
      targetWrite.resolve();
      unrelatedWrite.resolve();
      releasePromotionLookup.resolve();
      await promotion?.catch(() => undefined);
      get.mockRestore();
      put.mockRestore();
      residency.dispose();
    }
  });
});
