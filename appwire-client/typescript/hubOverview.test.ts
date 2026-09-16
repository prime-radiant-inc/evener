import { describe, expect, test } from "vitest";
import { createHubOverviewStore, type HubOverviewClient, normalizeSettingsOverview } from "./hubOverview";
import type { SettingsOverviewResponse } from "./types.gen";

const SAMPLE: SettingsOverviewResponse = {
  hub: { version: "1.2.3", listenAddr: "127.0.0.1:9180", runDir: "/tmp/run" },
  storage: { stateDir: "/home/user/.evener" },
  agents: [{ name: "default" }],
};

// A scripted client through the store's port: one handler, swapped between
// calls, with every call recorded.
function fakeClient(initial: () => Promise<SettingsOverviewResponse>) {
  const calls: string[] = [];
  let handler = initial;
  const client: HubOverviewClient = {
    request: ((method: string) => {
      calls.push(method);
      return handler();
    }) as HubOverviewClient["request"],
  };
  return {
    client,
    calls,
    respondWith(next: () => Promise<SettingsOverviewResponse>) {
      handler = next;
    },
  };
}

function pending() {
  let resolve!: (value: SettingsOverviewResponse) => void;
  const promise = new Promise<SettingsOverviewResponse>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

describe("store shape", () => {
  test("two stores share nothing: a load into one leaves the other at its initial state", async () => {
    const first = createHubOverviewStore(fakeClient(async () => SAMPLE).client);
    const second = createHubOverviewStore(
      fakeClient(async () => {
        throw new Error("boom");
      }).client,
    );

    await first.getState().fetch();
    expect(first.getState().data).toEqual(SAMPLE);
    expect(second.getState()).toMatchObject({ data: null, loading: false, error: null });

    await second.getState().refresh();
    expect(second.getState().error).toBe("boom");
    expect(first.getState().error).toBeNull();

    first.reset();
    expect(first.getState().data).toBeNull();
    expect(second.getState().error).toBe("boom");
  });

  test("getInitialState is the state the store was created with, and setState notifies with new and previous", () => {
    const store = createHubOverviewStore(fakeClient(async () => SAMPLE).client);
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
  test("fetch requests evener/settings/overview once and caches; refresh always re-requests", async () => {
    const fake = fakeClient(async () => SAMPLE);
    const store = createHubOverviewStore(fake.client);

    await store.getState().fetch();
    await store.getState().fetch();
    expect(fake.calls).toEqual(["evener/settings/overview"]);
    expect(store.getState()).toMatchObject({ data: SAMPLE, loading: false, error: null });

    await store.getState().refresh();
    expect(fake.calls).toHaveLength(2);
  });

  test("concurrent callers share one in-flight request", async () => {
    const fake = fakeClient(async () => SAMPLE);
    const store = createHubOverviewStore(fake.client);

    await Promise.all([store.getState().fetch(), store.getState().refresh(), store.getState().fetch()]);

    expect(fake.calls).toHaveLength(1);
  });

  test("loading is true while the request is in flight", async () => {
    const slow = pending();
    const store = createHubOverviewStore(fakeClient(() => slow.promise).client);

    const read = store.getState().fetch();
    expect(store.getState().loading).toBe(true);

    slow.resolve(SAMPLE);
    await read;
    expect(store.getState().loading).toBe(false);
  });

  test("a failed request keeps the last good data, surfaces the error text, and is retried by the next fetch", async () => {
    const fake = fakeClient(async () => SAMPLE);
    const store = createHubOverviewStore(fake.client);
    await store.getState().fetch();

    fake.respondWith(async () => {
      throw new Error("network down");
    });
    await store.getState().refresh();
    expect(store.getState()).toMatchObject({ data: SAMPLE, loading: false, error: "network down" });

    fake.respondWith(async () => ({ agents: [] }));
    await store.getState().refresh();
    expect(store.getState()).toMatchObject({ data: { agents: [] }, error: null });
  });

  test("a failure before any load leaves data null and is not cached", async () => {
    const fake = fakeClient(async () => {
      throw new Error("boom");
    });
    const store = createHubOverviewStore(fake.client);

    await store.getState().fetch();
    expect(store.getState()).toMatchObject({ data: null, error: "boom" });

    fake.respondWith(async () => SAMPLE);
    await store.getState().fetch();
    expect(fake.calls).toHaveLength(2);
    expect(store.getState().data).toEqual(SAMPLE);
  });

  test("describeError decides the error text the store publishes", async () => {
    const store = createHubOverviewStore(
      fakeClient(async () => {
        throw new Error("private internal detail");
      }).client,
      { describeError: () => "Could not refresh." },
    );

    await store.getState().refresh();

    expect(store.getState().error).toBe("Could not refresh.");
  });

  test("a client that throws synchronously is reported like any other failure", async () => {
    const client: HubOverviewClient = {
      request: (() => {
        throw new Error("no client connected");
      }) as HubOverviewClient["request"],
    };
    const store = createHubOverviewStore(client);

    await store.getState().fetch();

    expect(store.getState()).toMatchObject({ data: null, loading: false, error: "no client connected" });
  });
});

describe("wire decoding", () => {
  test("omitted empty collections and zero counters decode to [] and 0; absent sections stay absent", async () => {
    const store = createHubOverviewStore(
      fakeClient(async () => ({ hub: { pastIndex: { path: "/index" } }, mcpDiscovered: {} })).client,
    );

    await store.getState().refresh();

    expect(store.getState().data).toEqual({
      hub: { pastIndex: { path: "/index", count: 0, perPage: 0 } },
      mcpDiscovered: { servers: [] },
      agents: [],
    });
    expect(store.getState().data?.storage).toBeUndefined();
  });

  test("normalizeSettingsOverview leaves populated fields alone", () => {
    const populated: SettingsOverviewResponse = {
      hub: { pastIndex: { path: "/index", count: 3, perPage: 20 } },
      mcpDiscovered: { servers: [{ name: "one" }] },
      agents: [{ name: "a" }],
    };
    expect(normalizeSettingsOverview(populated)).toEqual(populated);
  });
});

describe("reset and dispose", () => {
  test("reset returns to the initial state, drops the in-flight result and lets the next fetch request again", async () => {
    const slow = pending();
    const fake = fakeClient(() => slow.promise);
    const store = createHubOverviewStore(fake.client);

    const read = store.getState().fetch();
    store.reset();
    expect(store.getState()).toMatchObject({ data: null, loading: false, error: null });

    slow.resolve(SAMPLE);
    await read;
    expect(store.getState().data).toBeNull();

    fake.respondWith(async () => SAMPLE);
    await store.getState().fetch();
    expect(fake.calls).toHaveLength(2);
    expect(store.getState().data).toEqual(SAMPLE);
  });

  test("dispose ignores the pending response, silences subscribers and refuses further reads", async () => {
    const slow = pending();
    const fake = fakeClient(() => slow.promise);
    const store = createHubOverviewStore(fake.client);
    let updates = 0;
    store.subscribe(() => {
      updates += 1;
    });

    const read = store.getState().refresh();
    store.dispose();
    const before = updates;

    slow.resolve(SAMPLE);
    await read;
    await store.getState().refresh();

    expect(updates).toBe(before);
    expect(fake.calls).toHaveLength(1);
    expect(store.getState().data).toBeNull();
  });
});
