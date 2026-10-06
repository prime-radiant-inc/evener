import "fake-indexeddb/auto";
import { acquireThreadSubscription, type ThreadTurnsListResponse } from "@evener/appwire-client";
import { currentRead, textDelta } from "@evener/appwire-client/testing/currentTranscript";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, beforeEach, describe, expect, test } from "vitest";
import { connectionStore } from "./connection";
import { SessionCacheIndexedDB } from "./sessionCacheIndexedDB";
import {
  acquireBackgroundThread,
  resetThreadsStoreForTests,
  setSessionCacheAdapterForTests,
  threadsStore,
} from "./threads";

const databaseName = `background-thread-${crypto.randomUUID()}`;
let cache: SessionCacheIndexedDB;
const activityLeases: ReturnType<typeof acquireThreadSubscription>[] = [];

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  cache = new SessionCacheIndexedDB({ indexedDB: new IDBFactory(), databaseName });
  setSessionCacheAdapterForTests(cache);
});

afterEach(() => {
  for (const lease of activityLeases.splice(0)) lease.release();
  resetThreadsStoreForTests();
  cache.close();
});

function connectCurrentClient(): FakeClient {
  const fake = new FakeClient("ready");
  fake.on("thread/read", ({ ref }) => currentRead(ref ?? "local:one"));
  fake.on("thread/unsubscribe", () => ({}));
  connectionStore.getState().connect(fake);
  return fake;
}

function membershipReads(fake: FakeClient): number {
  return fake.calls.filter(
    (call) => call.method === "thread/read" && (call.params as { subscribe?: boolean }).subscribe === true,
  ).length;
}

function unsubscriptions(fake: FakeClient): number {
  return fake.calls.filter((call) => call.method === "thread/unsubscribe").length;
}

describe("background thread claims", () => {
  test("pane adopts a background-hydrated model and releases only its own claim", async () => {
    const fake = connectCurrentClient();
    const activity = acquireThreadSubscription(fake, "local:one");
    activityLeases.push(activity);
    await activity.ensure();

    const first = acquireBackgroundThread("local:one");
    const second = acquireBackgroundThread("local:one");
    await Promise.all([first.ready, second.ready]);
    const resident = threadsStore.getState().threads.get("local:one");
    expect(
      resident?.turns
        .flatMap((turn) => turn.items)
        .map((item) => item.text)
        .join(""),
    ).toContain("alpha");
    const subscribedReads = membershipReads(fake);
    expect(subscribedReads).toBe(1);

    await Promise.all([
      threadsStore.getState().ensureThread("local:one"),
      threadsStore.getState().ensureThread("local:one"),
    ]);
    expect(threadsStore.getState().threads.get("local:one")).toBe(resident);
    expect(membershipReads(fake)).toBe(subscribedReads);

    await first.release();
    await second.release();
    fake.emitNotification(textDelta("local:one", "-after-background-close"));
    expect(
      threadsStore
        .getState()
        .threads.get("local:one")
        ?.turns.flatMap((turn) => turn.items)
        .map((item) => item.text)
        .join(""),
    ).toContain("alpha-after-background-close");
    expect(unsubscriptions(fake)).toBe(0);

    threadsStore.getState().releaseThread("local:one");
    expect(unsubscriptions(fake)).toBe(0);
    const unsubscribed = deferred<void>();
    fake.on("thread/unsubscribe", () => {
      unsubscribed.resolve();
      return {};
    });
    threadsStore.getState().releaseThread("local:one");
    activity.release();
    await unsubscribed.promise;
    expect(unsubscriptions(fake)).toBe(1);
  });

  test("foreground release leaves background routing alive until its final claim", async () => {
    const fake = connectCurrentClient();
    const pane = threadsStore.getState().ensureThread("local:one");
    await pane;
    const background = acquireBackgroundThread("local:one");
    await background.ready;
    const reads = fake.calls.filter((call) => call.method === "thread/read").length;

    threadsStore.getState().releaseThread("local:one");
    fake.emitNotification(textDelta("local:one", "-live"));
    expect(
      threadsStore
        .getState()
        .threads.get("local:one")
        ?.turns.flatMap((turn) => turn.items)
        .map((item) => item.text)
        .join(""),
    ).toContain("alpha-live");
    expect(unsubscriptions(fake)).toBe(0);
    expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(reads);

    await background.release();
    expect(unsubscriptions(fake)).toBe(1);
    expect(threadsStore.getState().threads.has("local:one")).toBe(false);
  });

  test("releasing the last background claim retires a held read without publishing it", async () => {
    const fake = connectCurrentClient();
    const entered = deferred<void>();
    const held = deferred<ReturnType<typeof currentRead>>();
    fake.on("thread/read", () => {
      entered.resolve();
      return held.promise;
    });
    const background = acquireBackgroundThread("local:one");
    await entered.promise;
    const readsBeforeRelease = membershipReads(fake);
    await background.release();
    await background.ready;
    held.resolve(currentRead("local:one"));
    await Promise.resolve();
    expect(threadsStore.getState().threads.has("local:one")).toBe(false);
    expect(membershipReads(fake)).toBe(readsBeforeRelease);
    expect(unsubscriptions(fake)).toBe(0);
  });

  test("keeps the model owned until an in-flight history page finishes", async () => {
    const fake = connectCurrentClient();
    await threadsStore.getState().ensureThread("local:one");
    const entered = deferred<void>();
    const page = deferred<ThreadTurnsListResponse>();
    fake.on("thread/turns/list", () => {
      entered.resolve();
      return page.promise;
    });

    const loading = threadsStore.getState().loadOlderTurns("local:one");
    await entered.promise;
    threadsStore.getState().releaseThread("local:one");
    expect(threadsStore.getState().threads.has("local:one")).toBe(true);

    page.resolve({ data: [], bootGeneration: "1", epoch: 1, snapshot: { incarnation: "inc-1", length: 40 } });
    await loading;
    expect(threadsStore.getState().threads.has("local:one")).toBe(false);
  });
});
