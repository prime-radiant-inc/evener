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

function setup(model: ThreadModel, pendingBytes = DEFAULT_BACKGROUND_TRANSCRIPT_OPTIONS.pendingBytes) {
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
  const residency = createTranscriptResidency(port, {
    ...DEFAULT_BACKGROUND_TRANSCRIPT_OPTIONS,
    pendingBytes,
    residentBytes: 0,
  });
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
    const { cache, residency, getResident } = setup(model);
    residency.published(model.ref, model, 0);
    await residency.settled();

    expect(getResident()).toBeUndefined();
    expect(residency.snapshot().committedWrites).toBe(1);
    const lookupReached = deferred<void>();
    const releaseLookup = deferred<void>();
    const originalGet = cache.getCurrent.bind(cache);
    const get = vi.spyOn(cache, "getCurrent").mockImplementation(async (...args) => {
      lookupReached.resolve();
      await releaseLookup.promise;
      return originalGet(...args);
    });
    try {
      const promotion = residency.promote(model.ref);
      await lookupReached.promise;
      expect(residency.snapshot().refs.get(model.ref)?.current).toBe(true);
      releaseLookup.resolve();
      const promoted = await promotion;
      expect(promoted?.ref).toBe(model.ref);
      expect(getResident()).toBe(promoted);
      expect(residency.snapshot().networkHydrations).toBe(0);
    } finally {
      releaseLookup.resolve();
      get.mockRestore();
    }
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
    let failFirst = true;
    const cache = new SessionCacheIndexedDB({
      indexedDB: new IDBFactory(),
      databaseName: `residency-${crypto.randomUUID()}`,
      beforeCommit: (operation, transaction) => {
        if (operation === "putCurrent" && failFirst) {
          failFirst = false;
          transaction.abort();
        }
      },
    });
    caches.push(cache);
    const first = hydrateThread(currentRead("local:one"), "local:one", 1);
    const second = hydrateThread(currentRead("local:two"), "local:two", 1);
    const residents = new Map<string, ThreadModel>();
    const recoveryStarted = deferred<void>();
    const releaseRecovery = deferred<void>();
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
        recoveryStarted.resolve();
        await releaseRecovery.promise;
      },
    };
    const residency = createTranscriptResidency(port, DEFAULT_BACKGROUND_TRANSCRIPT_OPTIONS);
    residency.connection(1, true);
    for (const ref of [first.ref, second.ref]) {
      residency.owners(ref, { foreground: 0, history: 0, mutationPinned: false, background: 1 });
    }
    const originalPut = cache.putCurrent.bind(cache);
    const order: string[] = [];
    const committed: Array<{ ownerToken: string; ref: string }> = [];
    const put = vi.spyOn(cache, "putCurrent").mockImplementation(async (record, now) => {
      order.push(record.ref);
      const outcome = await originalPut(record, now);
      if (outcome.outcome === "written") committed.push({ ownerToken: record.ownerToken, ref: record.ref });
      return outcome;
    });
    try {
      port.publish(first.ref, first);
      port.publish(second.ref, second);
      residency.published(first.ref, first, 0);
      residency.published(first.ref, first, 1);
      residency.published(second.ref, second, 0);
      await residency.settled();
      await recoveryStarted.promise;

      expect(order).toEqual([first.ref, second.ref]);
      expect(residency.snapshot().diskWorkers).toBe(0);
      expect(residency.snapshot().refs.get(first.ref)?.persistence).toBe("repairing");
      expect(residency.snapshot().networkHydrations).toBe(1);
      const secondWrite = committed.find((record) => record.ref === second.ref);
      expect(secondWrite).toBeDefined();
      await expect(cache.getCurrent([secondWrite!.ownerToken, second.ref], Date.now())).resolves.toMatchObject({
        record: { ref: second.ref },
      });
    } finally {
      releaseRecovery.resolve();
      await residency.settled();
      put.mockRestore();
      residency.dispose();
    }
  });

  test("serves unrelated disk work while a ref is replenished at each commit boundary", async () => {
    const cache = new SessionCacheIndexedDB({
      indexedDB: new IDBFactory(),
      databaseName: `residency-${crypto.randomUUID()}`,
    });
    caches.push(cache);
    const busy = hydrateThread(currentRead("local:busy"), "local:busy", 1);
    const unrelated = hydrateThread(currentRead("local:unrelated"), "local:unrelated", 1);
    const residents = new Map<string, ThreadModel>([
      [busy.ref, busy],
      [unrelated.ref, unrelated],
    ]);
    const port: TranscriptResidencyPort = {
      cache,
      resident: (ref) => residents.get(ref),
      publish: (ref, model) => residents.set(ref, model),
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
    for (const model of [busy, unrelated]) {
      residency.owners(model.ref, { foreground: 0, history: 0, mutationPinned: false, background: 1 });
      residency.published(model.ref, model, 0, true);
    }
    await residency.settled();

    const firstBusyWriteStarted = deferred<void>();
    const releaseFirstBusyWrite = deferred<void>();
    const unrelatedWriteStarted = deferred<void>();
    const starts: Array<{ ref: string; sequence: number }> = [];
    const commits: Array<{ ref: string; sequence: number; ownerToken: string }> = [];
    let replenishedNotifications = 0;
    const originalPut = cache.putCurrent.bind(cache);
    const put = vi.spyOn(cache, "putCurrent").mockImplementation(async (record, now) => {
      starts.push({ ref: record.ref, sequence: record.appliedSequence });
      if (record.ref === busy.ref && record.appliedSequence === 1) {
        firstBusyWriteStarted.resolve();
        await releaseFirstBusyWrite.promise;
      }
      if (record.ref === unrelated.ref) unrelatedWriteStarted.resolve();
      const outcome = await originalPut(record, now);
      if (outcome.outcome === "written") {
        commits.push({ ref: record.ref, sequence: record.appliedSequence, ownerToken: record.ownerToken });
        if (record.ref === busy.ref && record.appliedSequence < 4) {
          const sequence = record.appliedSequence + 1;
          const replenished = textDelta(busy.ref, `-busy-follow-up-${sequence}`);
          residency.accept(busy.ref, {
            notification: replenished,
            sequence,
            now: sequence + 2,
            bytes: encodedPayloadBytes(replenished),
          });
          replenishedNotifications += 1;
        }
      }
      return outcome;
    });
    try {
      const busyFrame = textDelta(busy.ref, "-busy-first");
      residency.accept(busy.ref, {
        notification: busyFrame,
        sequence: 1,
        now: 2,
        bytes: encodedPayloadBytes(busyFrame),
      });
      await firstBusyWriteStarted.promise;
      const unrelatedFrame = textDelta(unrelated.ref, "-unrelated-ready");
      residency.accept(unrelated.ref, {
        notification: unrelatedFrame,
        sequence: 1,
        now: 3,
        bytes: encodedPayloadBytes(unrelatedFrame),
      });
      releaseFirstBusyWrite.resolve();
      await unrelatedWriteStarted.promise;
      expect(starts).toEqual([
        { ref: busy.ref, sequence: 1 },
        { ref: unrelated.ref, sequence: 1 },
      ]);
      expect(residency.snapshot().pendingBytes).toBeGreaterThan(0);
      await residency.settled();

      expect(starts).toEqual([
        { ref: busy.ref, sequence: 1 },
        { ref: unrelated.ref, sequence: 1 },
        { ref: busy.ref, sequence: 2 },
        { ref: busy.ref, sequence: 3 },
        { ref: busy.ref, sequence: 4 },
      ]);
      expect(replenishedNotifications).toBe(3);
      expect(commits.map(({ ref, sequence }) => [ref, sequence])).toEqual([
        [busy.ref, 1],
        [unrelated.ref, 1],
        [busy.ref, 2],
        [busy.ref, 3],
        [busy.ref, 4],
      ]);
      const busyWrite = commits.find(({ ref, sequence }) => ref === busy.ref && sequence === 4);
      const unrelatedWrite = commits.find(({ ref }) => ref === unrelated.ref);
      if (!busyWrite || !unrelatedWrite) throw new Error("expected both fairness checkpoints to commit");
      const busyState = await cache.getCurrent([busyWrite.ownerToken, busy.ref], Date.now());
      const unrelatedState = await cache.getCurrent([unrelatedWrite.ownerToken, unrelated.ref], Date.now());
      if (!busyState?.record || !unrelatedState?.record) throw new Error("expected committed checkpoints in IndexedDB");
      for (let sequence = 2; sequence <= 4; sequence += 1) {
        expect(JSON.stringify(busyState.record.state)).toContain(`-busy-follow-up-${sequence}`);
      }
      expect(JSON.stringify(unrelatedState.record.state)).toContain("-unrelated-ready");
    } finally {
      releaseFirstBusyWrite.resolve();
      await residency.settled();
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

  test("promotion does not restore a revoked current certificate after a held lookup", async () => {
    const ref = "local:revoked-certificate";
    const model = hydrateThread(currentRead(ref), ref, 1);
    const { cache, residency, port, getResident } = setup(model, 4_096);
    const recovery = deferred<void>();
    const recoveryPublished = deferred<void>();
    const lookupReached = deferred<void>();
    const releaseLookup = deferred<void>();
    port.hydrate = async () => {
      await recovery.promise;
      const repaired = hydrateThread(currentRead(ref), ref, 3);
      port.publish(ref, repaired);
      residency.published(ref, repaired, 1, true);
      recoveryPublished.resolve();
    };
    residency.published(ref, model, 0, true);
    let promotion: Promise<ThreadModel | undefined> | undefined;
    const originalGet = cache.getCurrent.bind(cache);
    const get = vi.spyOn(cache, "getCurrent").mockImplementation(async (...args) => {
      lookupReached.resolve();
      await releaseLookup.promise;
      return originalGet(...args);
    });
    try {
      await residency.settled();
      expect(getResident()).toBeUndefined();
      expect(residency.snapshot().refs.get(ref)?.current).toBe(true);

      promotion = residency.promote(ref);
      await lookupReached.promise;
      const rejected = textDelta(ref, "x".repeat(5_000));
      expect(
        residency.accept(ref, {
          notification: rejected,
          sequence: 1,
          now: 2,
          bytes: encodedPayloadBytes(rejected),
        }),
      ).toBe(true);
      expect(residency.snapshot().refs.get(ref)).toMatchObject({ current: false, persistence: "repairing" });

      releaseLookup.resolve();
      const promoted = await promotion;
      expect({
        published: promoted !== undefined,
        current: residency.snapshot().refs.get(ref)?.current,
        persistence: residency.snapshot().refs.get(ref)?.persistence,
      }).toEqual({ published: false, current: false, persistence: "repairing" });
      expect(getResident()).toBeUndefined();

      recovery.resolve();
      await recoveryPublished.promise;
      await residency.settled();
      expect(residency.snapshot().refs.get(ref)?.current).toBe(true);
      expect(getResident()).toBeUndefined();
      const repairedPromotion = await residency.promote(ref);
      expect(repairedPromotion).toBeDefined();
      expect(JSON.stringify(currentTranscriptState(repairedPromotion!))).not.toContain("xxxxx");
      const useful = textDelta(ref, "-after-repair");
      expect(
        residency.accept(ref, { notification: useful, sequence: 2, now: 4, bytes: encodedPayloadBytes(useful) }),
      ).toBe(false);
      const continued = applyNotification(repairedPromotion!, useful, 4);
      port.publish(ref, continued);
      residency.published(ref, continued, 2);
      expect(JSON.stringify(currentTranscriptState(continued))).toContain("-after-repair");
    } finally {
      releaseLookup.resolve();
      recovery.resolve();
      await promotion?.catch(() => undefined);
      get.mockRestore();
      residency.dispose();
    }
  });

  test("promotion moves an already queued target ahead of unrelated disk work", async () => {
    const refs: [string, string, string] = ["local:blocker", "local:unrelated", "local:target"];
    const cache = new SessionCacheIndexedDB({
      indexedDB: new IDBFactory(),
      databaseName: `residency-${crypto.randomUUID()}`,
    });
    caches.push(cache);
    const models = new Map(refs.map((ref) => [ref, hydrateThread(currentRead(ref), ref, 1)]));
    const port: TranscriptResidencyPort = {
      cache,
      resident: (ref) => models.get(ref),
      publish: (ref, model) => models.set(ref, model),
      unload: (ref) => {
        models.delete(ref);
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
    for (const ref of refs) {
      residency.owners(ref, { foreground: 0, history: 0, mutationPinned: false, background: 1 });
      residency.published(ref, models.get(ref)!, 0, true);
    }
    await residency.settled();

    const blockerReached = deferred<void>();
    const releaseBlocker = deferred<void>();
    const targetReached = deferred<void>();
    const releaseTarget = deferred<void>();
    const unrelatedReached = deferred<void>();
    const releaseUnrelated = deferred<void>();
    const operationOrder: string[] = [];
    const commits: Array<{ ref: string; ownerToken: string }> = [];
    const originalPut = cache.putCurrent.bind(cache);
    const put = vi.spyOn(cache, "putCurrent").mockImplementation(async (record, now) => {
      operationOrder.push(record.ref);
      if (record.ref === refs[0]) {
        blockerReached.resolve();
        await releaseBlocker.promise;
      } else if (record.ref === refs[2]) {
        targetReached.resolve();
        await releaseTarget.promise;
      } else {
        unrelatedReached.resolve();
        await releaseUnrelated.promise;
      }
      const outcome = await originalPut(record, now);
      if (outcome.outcome === "written") commits.push({ ref: record.ref, ownerToken: record.ownerToken });
      return outcome;
    });
    let promotion: Promise<ThreadModel | undefined> | undefined;
    try {
      const blockerFrame = textDelta(refs[0]!, "-blocker");
      residency.accept(refs[0]!, {
        notification: blockerFrame,
        sequence: 1,
        now: 2,
        bytes: encodedPayloadBytes(blockerFrame),
      });
      await blockerReached.promise;

      for (const ref of refs.slice(1)) {
        const frame = textDelta(ref, `-${ref}`);
        residency.accept(ref, { notification: frame, sequence: 1, now: 3, bytes: encodedPayloadBytes(frame) });
      }
      promotion = residency.promote(refs[2]!);
      releaseBlocker.resolve();
      await targetReached.promise;
      expect(operationOrder).toEqual([refs[0], refs[2]]);
      releaseTarget.resolve();
      await unrelatedReached.promise;
      const promoted = await promotion;
      expect(promoted?.ref).toBe(refs[2]);
      expect(JSON.stringify(currentTranscriptState(promoted!))).toContain(`-${refs[2]}`);
      expect(residency.snapshot().diskWorkers).toBe(1);
      expect(operationOrder).toEqual([refs[0], refs[2], refs[1]]);
      expect(commits.map(({ ref }) => ref)).toEqual([refs[0], refs[2]]);
      releaseUnrelated.resolve();
      await residency.settled();
      expect(commits.map(({ ref }) => ref)).toEqual([refs[0], refs[2], refs[1]]);
      const unrelatedCommit = commits.find(({ ref }) => ref === refs[1]);
      if (!unrelatedCommit) throw new Error("expected unrelated checkpoint to commit");
      const unrelatedRecord = await cache.getCurrent([unrelatedCommit.ownerToken, refs[1]], Date.now());
      if (!unrelatedRecord?.record) throw new Error("expected unrelated checkpoint in IndexedDB");
      expect(JSON.stringify(unrelatedRecord.record.state)).toContain(`-${refs[1]}`);
    } finally {
      releaseBlocker.resolve();
      releaseTarget.resolve();
      releaseUnrelated.resolve();
      await promotion?.catch(() => undefined);
      await residency.settled();
      put.mockRestore();
      residency.dispose();
    }
  });

  test("promotion publishes its captured cut while completion-driven later writes remain held", async () => {
    const ref = "local:finite-cut";
    const cache = new SessionCacheIndexedDB({
      indexedDB: new IDBFactory(),
      databaseName: `residency-${crypto.randomUUID()}`,
    });
    caches.push(cache);
    let model: ThreadModel | undefined = hydrateThread(currentRead(ref), ref, 1);
    let hydrations = 0;
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
      hydrate: async () => {
        hydrations += 1;
      },
    };
    const residency = createTranscriptResidency(port, {
      ...DEFAULT_BACKGROUND_TRANSCRIPT_OPTIONS,
      residentBytes: 0,
    });
    residency.connection(1, true);
    residency.owners(ref, { foreground: 0, history: 0, mutationPinned: false, background: 1 });
    residency.published(ref, model!, 0, true);
    await residency.settled();
    expect(model).toBeUndefined();

    const firstWriteReached = deferred<void>();
    const releaseFirstWrite = deferred<void>();
    const laterWriteReached = deferred<void>();
    const releaseLaterWrite = deferred<void>();
    const lookupReached = deferred<void>();
    const releaseLookup = deferred<void>();
    const originalPut = cache.putCurrent.bind(cache);
    let completionNotifications = 0;
    const put = vi.spyOn(cache, "putCurrent").mockImplementation(async (record, now) => {
      if (record.appliedSequence === 1) {
        firstWriteReached.resolve();
        await releaseFirstWrite.promise;
      } else if (record.appliedSequence > 1) {
        laterWriteReached.resolve();
        await releaseLaterWrite.promise;
      }
      const result = await originalPut(record, now);
      if (record.appliedSequence === 1) {
        for (let sequence = 2; sequence <= 5; sequence += 1) {
          const frame = textDelta(ref, `-completion-${sequence}`);
          const accepted = residency.accept(ref, {
            notification: frame,
            sequence,
            now: sequence + 2,
            bytes: encodedPayloadBytes(frame),
          });
          expect(accepted).toBe(true);
          completionNotifications += 1;
        }
      }
      return result;
    });
    const originalGet = cache.getCurrent.bind(cache);
    let getCalls = 0;
    const get = vi.spyOn(cache, "getCurrent").mockImplementation(async (...args) => {
      getCalls += 1;
      if (getCalls === 3) {
        lookupReached.resolve();
        await releaseLookup.promise;
      }
      return originalGet(...args);
    });
    let promotion: Promise<ThreadModel | undefined> | undefined;
    try {
      const first = textDelta(ref, "-captured");
      residency.accept(ref, { notification: first, sequence: 1, now: 2, bytes: encodedPayloadBytes(first) });
      await firstWriteReached.promise;
      promotion = residency.promote(ref);
      releaseFirstWrite.resolve();
      await laterWriteReached.promise;
      await lookupReached.promise;

      const duringLookup = textDelta(ref, "-lookup");
      residency.accept(ref, {
        notification: duringLookup,
        sequence: 6,
        now: 8,
        bytes: encodedPayloadBytes(duringLookup),
      });
      releaseLookup.resolve();
      const promoted = await promotion;
      expect(promoted).toBeDefined();
      expect(model).toBe(promoted);
      const text = JSON.stringify(currentTranscriptState(promoted!));
      expect(text).toContain("-captured");
      expect(completionNotifications).toBe(4);
      let precedingIndex = text.indexOf("-captured");
      expect(precedingIndex).toBeGreaterThanOrEqual(0);
      for (let sequence = 2; sequence <= 5; sequence += 1) {
        const marker = `-completion-${sequence}`;
        expect(text.split(marker)).toHaveLength(2);
        const nextIndex = text.indexOf(marker);
        expect(nextIndex).toBeGreaterThan(precedingIndex);
        precedingIndex = nextIndex;
      }
      expect(text).toContain("-lookup");
      expect(text.indexOf("-lookup")).toBeGreaterThan(precedingIndex);
      expect(hydrations).toBe(0);

      const after = textDelta(ref, "-after-publication");
      expect(
        residency.accept(ref, { notification: after, sequence: 7, now: 9, bytes: encodedPayloadBytes(after) }),
      ).toBe(false);
      const folded = applyNotification(promoted!, after, 9);
      residency.published(ref, folded, 7);
      model = folded;
      const finalText = JSON.stringify(currentTranscriptState(model));
      expect(finalText.split("-after-publication")).toHaveLength(2);
      expect(finalText.indexOf("-lookup")).toBeLessThan(finalText.indexOf("-after-publication"));
    } finally {
      releaseFirstWrite.resolve();
      releaseLaterWrite.resolve();
      releaseLookup.resolve();
      await promotion?.catch(() => undefined);
      get.mockRestore();
      put.mockRestore();
      residency.dispose();
    }
  });
});
