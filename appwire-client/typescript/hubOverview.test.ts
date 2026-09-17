import { describe, expect, test } from "vitest";
import { createHubOverviewStore, type HubOverviewClient } from "./hubOverview";
import { FakeClient, failing } from "./testing/fakeClient";
import type { SettingsOverviewResponse } from "./types.gen";

const SAMPLE: SettingsOverviewResponse = {
  hub: {
    version: "1.2.3",
    listenAddr: "127.0.0.1:9180",
    runDir: "/tmp/run",
    daemonIdleTimeoutMillis: 3600000,
  },
  storage: { stateDir: "/home/user/.evener" },
  agents: [{ name: "default" }],
};

const OVERVIEW = "evener/settings/overview";

function pending() {
  let resolve!: (value: SettingsOverviewResponse) => void;
  const promise = new Promise<SettingsOverviewResponse>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

function storeWithFake() {
  const fake = new FakeClient("ready");
  return { fake, store: createHubOverviewStore(fake) };
}

describe("store shape", () => {
  test("two stores share nothing: a load into one leaves the other at its initial state", async () => {
    const first = storeWithFake();
    first.fake.on(OVERVIEW, () => SAMPLE);
    const second = storeWithFake();
    second.fake.on(OVERVIEW, failing("boom"));

    await first.store.getState().fetch();
    expect(first.store.getState().data).toEqual(SAMPLE);
    expect(second.store.getState()).toMatchObject({ data: null, loading: false, error: null });

    await second.store.getState().refresh();
    expect(second.store.getState().error).toBe("boom");
    expect(first.store.getState().error).toBeNull();

    first.store.reset();
    expect(first.store.getState().data).toBeNull();
    expect(second.store.getState().error).toBe("boom");
  });

  test("getInitialState is the state the store was created with, and setState notifies with new and previous", () => {
    const { store } = storeWithFake();
    const initial = store.getInitialState();
    const seen: Array<[boolean, boolean]> = [];
    store.subscribe((state, previous) => seen.push([state.loading, previous.loading]));

    store.setState({ loading: true });

    expect(seen).toEqual([[true, false]]);
    expect(store.getInitialState()).toBe(initial);
    expect(store.getState().fetch).toBe(initial.fetch);
  });
});

describe("fetch and refresh", () => {
  test("fetch requests evener/settings/overview once with empty params and caches; refresh always re-requests", async () => {
    const { fake, store } = storeWithFake();
    fake.on(OVERVIEW, () => SAMPLE);

    await store.getState().fetch();
    await store.getState().fetch();
    expect(fake.calls).toEqual([{ method: OVERVIEW, params: {} }]);
    expect(store.getState()).toMatchObject({ data: SAMPLE, loading: false, error: null });

    await store.getState().refresh();
    expect(fake.calls).toHaveLength(2);
  });

  test("concurrent callers share one in-flight request", async () => {
    const { fake, store } = storeWithFake();
    fake.on(OVERVIEW, () => SAMPLE);

    await Promise.all([store.getState().fetch(), store.getState().refresh(), store.getState().fetch()]);

    expect(fake.calls).toHaveLength(1);
  });

  test("loading is true while the request is in flight", async () => {
    const slow = pending();
    const { fake, store } = storeWithFake();
    fake.on(OVERVIEW, () => slow.promise);

    const read = store.getState().fetch();
    expect(store.getState().loading).toBe(true);

    slow.resolve(SAMPLE);
    await read;
    expect(store.getState().loading).toBe(false);
  });

  test("a failed request keeps the last good data, surfaces the error text, and is retried by the next fetch", async () => {
    const { fake, store } = storeWithFake();
    fake.on(OVERVIEW, () => SAMPLE);
    await store.getState().fetch();

    fake.on(OVERVIEW, failing("network down"));
    await store.getState().refresh();
    expect(store.getState()).toMatchObject({ data: SAMPLE, loading: false, error: "network down" });

    fake.on(OVERVIEW, () => ({ agents: [] }));
    await store.getState().refresh();
    expect(store.getState()).toMatchObject({ data: { agents: [] }, error: null });
  });

  test("a failure before any load leaves data null and is not cached", async () => {
    const { fake, store } = storeWithFake();
    fake.on(OVERVIEW, failing("boom"));

    await store.getState().fetch();
    expect(store.getState()).toMatchObject({ data: null, error: "boom" });

    fake.on(OVERVIEW, () => SAMPLE);
    await store.getState().fetch();
    expect(fake.calls).toHaveLength(2);
    expect(store.getState().data).toEqual(SAMPLE);
  });

  // The web hands in a port that resolves its connection store's current
  // client and throws synchronously when there is none; FakeClient turns a
  // synchronous throw into a rejection, so this one needs a raw port.
  test("a client that throws synchronously is reported like any other failure", async () => {
    const client: HubOverviewClient = { request: failing("no client connected") };
    const store = createHubOverviewStore(client);

    await store.getState().fetch();

    expect(store.getState()).toMatchObject({ data: null, loading: false, error: "no client connected" });
  });
});

describe("wire decoding", () => {
  test("omitted empty collections and zero counters decode to [] and 0; absent sections stay absent", async () => {
    const { fake, store } = storeWithFake();
    fake.on(OVERVIEW, () => ({
      hub: { pastIndex: { path: "/index" }, daemonIdleTimeoutMillis: 3600000 },
      mcpDiscovered: {},
    }));

    await store.getState().refresh();

    expect(store.getState().data).toEqual({
      hub: { pastIndex: { path: "/index", count: 0, perPage: 0 }, daemonIdleTimeoutMillis: 3600000 },
      mcpDiscovered: { servers: [] },
      agents: [],
    });
    expect(store.getState().data?.storage).toBeUndefined();
  });

  test("populated collections and counters are left alone", async () => {
    const populated: SettingsOverviewResponse = {
      hub: { pastIndex: { path: "/index", count: 3, perPage: 20 }, daemonIdleTimeoutMillis: 3600000 },
      mcpDiscovered: { servers: [{ name: "one" }] },
      agents: [{ name: "a" }],
    };
    const { fake, store } = storeWithFake();
    fake.on(OVERVIEW, () => populated);

    await store.getState().refresh();

    expect(store.getState().data).toEqual(populated);
  });
});

describe("reset and dispose", () => {
  test("reset returns to the initial state, drops the in-flight result and lets the next fetch request again", async () => {
    const slow = pending();
    const { fake, store } = storeWithFake();
    fake.on(OVERVIEW, () => slow.promise);

    const read = store.getState().fetch();
    store.reset();
    expect(store.getState()).toMatchObject({ data: null, loading: false, error: null });

    slow.resolve(SAMPLE);
    await read;
    expect(store.getState().data).toBeNull();

    fake.on(OVERVIEW, () => SAMPLE);
    await store.getState().fetch();
    expect(fake.calls).toHaveLength(2);
    expect(store.getState().data).toEqual(SAMPLE);
  });

  test("dispose ignores the pending response, silences subscribers and refuses further reads", async () => {
    const slow = pending();
    const { fake, store } = storeWithFake();
    fake.on(OVERVIEW, () => slow.promise);
    let updates = 0;
    store.subscribe(() => {
      updates += 1;
    });

    const read = store.getState().refresh();
    store.dispose();
    const before = updates; // dispose's own reset is the last notification a subscriber hears

    slow.resolve(SAMPLE);
    await read;
    await store.getState().refresh();

    expect(updates).toBe(before);
    expect(fake.calls).toHaveLength(1);
    expect(store.getState().data).toBeNull();
  });
});
