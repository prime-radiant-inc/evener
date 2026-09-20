import { describe, expect, test, vi } from "vitest";
import { deferred } from "./testing/deferred";
import { FakeClient } from "./testing/fakeClient";
import {
  type HubTranscriptDisplayDefault,
  makeTranscriptDisplayConfig,
  type TranscriptDisplayConfigV1,
  toWireConfig,
  toWireDefault,
} from "./transcriptDisplayConfig";
import {
  createTranscriptDisplayStore,
  fromWireChange,
  type TranscriptDisplayStore,
  transcriptDisplaySupport,
} from "./transcriptDisplayStore";
import type { AnyNotification, TranscriptDisplayDefaults } from "./types.gen";

const getMethod = "evener/settings/transcriptDisplay/get";
const changedMethod = "evener/settings/transcriptDisplay/changed";

const desktopConfig = makeTranscriptDisplayConfig({ kind: "preset", level: "intent" });
const mobileConfig = makeTranscriptDisplayConfig({ kind: "preset", level: "chat" });
const proposed = makeTranscriptDisplayConfig({ kind: "preset", level: "tools" });

function hubDefault(revision: number, config: TranscriptDisplayConfigV1): HubTranscriptDisplayDefault {
  return { revision, config };
}

function serving(desktop: HubTranscriptDisplayDefault, mobile: HubTranscriptDisplayDefault): FakeClient {
  const client = new FakeClient("ready");
  client.on(getMethod, () => ({ desktop: toWireDefault(desktop), mobile: toWireDefault(mobile) }));
  return client;
}

async function readyStore(client: FakeClient): Promise<TranscriptDisplayStore> {
  const store = createTranscriptDisplayStore({ client });
  store.setSupport("supported");
  store.beginReadyGeneration();
  await store.getState().refreshHubDefaults();
  return store;
}

describe("transcript display wire decoders", () => {
  test("rejects malformed and foreign changed payloads", () => {
    expect(fromWireChange(undefined)).toBeUndefined();
    expect(fromWireChange({ method: changedMethod, layout: "tablet", revision: 1, config: {} })).toBeUndefined();
    expect(fromWireChange({ layout: "mobile", revision: "one", config: toWireConfig(proposed) })).toBeUndefined();
    expect(fromWireChange({ layout: "mobile", revision: 1, config: { ...toWireConfig(proposed), future: true } })).toBe(
      undefined,
    );
    expect(
      fromWireChange({ layout: "mobile", revision: 1, config: toWireConfig(proposed), futureField: "ignored" }),
    ).toEqual({ layout: "mobile", revision: 1, config: proposed });
  });
});

describe("two stores share nothing", () => {
  test("a refresh and a support change in one store leave the other untouched", async () => {
    const first = await readyStore(serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig)));
    const second = createTranscriptDisplayStore({ client: new FakeClient("ready") });
    expect(first.getState().hub.desktop?.revision).toBe(3);
    expect(first.getState().loaded).toBe(true);
    expect(second.getState().hub).toEqual({});
    expect(second.getState().loaded).toBe(false);
    first.setSupport("unsupported");
    expect(second.getState().hubSupport).toBe("unknown");
  });
});

describe("support", () => {
  test.each([
    [undefined, "unknown"],
    [{ transcriptDisplaySettings: true }, "supported"],
    [{ transcriptDisplaySettings: false }, "unsupported"],
    [{}, "unsupported"],
  ] as const)("%j resolves to %s", (features, support) => {
    expect(transcriptDisplaySupport(features)).toBe(support);
  });

  test("no GET leaves while support is unknown; the transition into supported under a ready generation loads", async () => {
    const client = serving(hubDefault(1, desktopConfig), hubDefault(1, mobileConfig));
    const store = createTranscriptDisplayStore({ client });
    store.beginReadyGeneration();
    await store.getState().refreshHubDefaults();
    expect(client.calls).toHaveLength(0);
    store.setSupport("supported");
    await vi.waitFor(() => expect(store.getState().loaded).toBe(true));
    expect(client.calls.filter((call) => call.method === getMethod)).toHaveLength(1);
  });

  test("support resolving to unsupported retires the payload and clears the read state", async () => {
    const store = await readyStore(serving(hubDefault(1, desktopConfig), hubDefault(1, mobileConfig)));
    store.setSupport("unsupported");
    expect(store.getState()).toMatchObject({
      hubSupport: "unsupported",
      loaded: false,
      hubLoading: false,
      hubError: null,
      hub: {},
    });
  });

  test("a change relayed while unsupported does not repopulate hub", () => {
    const store = createTranscriptDisplayStore({ client: new FakeClient("ready") });
    store.setSupport("unsupported");
    store.getState().applyHubChange({ layout: "desktop", revision: 9, config: proposed });
    expect(store.getState().hub).toEqual({});
    expect(store.getState().loaded).toBe(false);
  });
});

describe("hub defaults", () => {
  test("a refresh lands both layouts, one transition per layout", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const store = createTranscriptDisplayStore({ client });
    const layouts: string[] = [];
    store.subscribe((state, previous) => {
      for (const layout of ["desktop", "mobile"] as const)
        if (state.hub[layout] !== previous.hub[layout]) layouts.push(layout);
    });
    store.setSupport("supported");
    store.beginReadyGeneration();
    await store.getState().refreshHubDefaults();
    expect(store.getState().hub).toEqual({
      desktop: hubDefault(3, desktopConfig),
      mobile: hubDefault(2, mobileConfig),
    });
    expect(layouts).toEqual(["desktop", "mobile"]);
  });

  test("a GET reply with extra top-level keys still decodes", async () => {
    const client = new FakeClient("ready");
    client.on(getMethod, () => ({
      desktop: { ...toWireDefault(hubDefault(3, desktopConfig)), futureField: "ignored" },
      mobile: toWireDefault(hubDefault(2, mobileConfig)),
      futureTopLevel: true,
    }));
    const store = await readyStore(client);
    expect(store.getState().hub.desktop).toEqual(hubDefault(3, desktopConfig));
  });

  test("a changed notification applies a newer revision and ignores a stale or equal one", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const store = await readyStore(client);
    for (const revision of [2, 1]) {
      client.emitNotification({
        method: changedMethod,
        params: { layout: "mobile", revision, config: toWireConfig(proposed) },
      });
      expect(store.getState().hub.mobile).toEqual(hubDefault(2, mobileConfig));
    }
    client.emitNotification({
      method: changedMethod,
      params: { layout: "mobile", revision: 5, config: toWireConfig(proposed) },
    });
    expect(store.getState().hub.mobile).toEqual(hubDefault(5, proposed));
  });

  test("a foreign notification is ignored without a read", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const store = await readyStore(client);
    const calls = client.calls.length;
    client.emitUnknownNotification({ method: "evener/thread/changed", params: {} });
    expect(client.calls).toHaveLength(calls);
    expect(store.getState().hub.mobile).toEqual(hubDefault(2, mobileConfig));
  });

  test("an undecodable changed notification schedules a read instead of being dropped", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const store = await readyStore(client);
    client.on(getMethod, () => ({
      desktop: toWireDefault(hubDefault(3, desktopConfig)),
      mobile: toWireDefault(hubDefault(6, proposed)),
    }));
    client.emitNotification({
      method: changedMethod,
      params: { layout: "mobile", revision: "six" },
    } as unknown as AnyNotification);
    await vi.waitFor(() => expect(store.getState().hub.mobile).toEqual(hubDefault(6, proposed)));
    expect(client.calls.filter((call) => call.method === getMethod)).toHaveLength(2);
  });

  test("a notification landing before the generation's first read confirms is followed by one read", async () => {
    const client = new FakeClient("ready");
    const first = deferred<TranscriptDisplayDefaults>();
    let reads = 0;
    client.on(getMethod, () => {
      reads += 1;
      return reads === 1
        ? first.promise
        : { desktop: toWireDefault(hubDefault(3, desktopConfig)), mobile: toWireDefault(hubDefault(9, proposed)) };
    });
    const store = createTranscriptDisplayStore({ client });
    store.setSupport("supported");
    store.beginReadyGeneration();
    const refresh = store.getState().refreshHubDefaults();
    await vi.waitFor(() => expect(reads).toBe(1));
    client.emitNotification({
      method: changedMethod,
      params: { layout: "mobile", revision: 9, config: toWireConfig(proposed) },
    });
    expect(store.getState().hub.mobile).toBeUndefined();
    first.resolve({
      desktop: toWireDefault(hubDefault(3, desktopConfig)),
      mobile: toWireDefault(hubDefault(2, mobileConfig)),
    });
    await refresh;
    await vi.waitFor(() => expect(store.getState().hub.mobile).toEqual(hubDefault(9, proposed)));
    expect(reads).toBe(2);
  });

  test("a new generation's first read applies lower revisions from a restarted hub", async () => {
    const client = serving(hubDefault(7, desktopConfig), hubDefault(7, mobileConfig));
    const store = await readyStore(client);
    store.endReadyGeneration();
    expect(store.getState().loaded).toBe(false);
    expect(store.getState().hub.desktop?.revision).toBe(7);
    client.on(getMethod, () => ({
      desktop: toWireDefault(hubDefault(1, proposed)),
      mobile: toWireDefault(hubDefault(1, proposed)),
    }));
    store.beginReadyGeneration();
    await store.getState().refreshHubDefaults();
    expect(store.getState().hub).toEqual({ desktop: hubDefault(1, proposed), mobile: hubDefault(1, proposed) });
    expect(store.getState().loaded).toBe(true);
  });

  test("a change arriving while the first read fails survives to the next successful read", async () => {
    const client = new FakeClient("ready");
    let reads = 0;
    client.on(getMethod, () => {
      reads += 1;
      if (reads === 1) throw new Error("hub unreachable");
      return {
        desktop: toWireDefault(hubDefault(3, desktopConfig)),
        mobile: toWireDefault(hubDefault(2, mobileConfig)),
      };
    });
    const store = createTranscriptDisplayStore({ client });
    store.setSupport("supported");
    store.beginReadyGeneration();
    await store.getState().refreshHubDefaults();
    expect(store.getState()).toMatchObject({ loaded: false, hubError: "hub unreachable" });
    client.emitNotification({
      method: changedMethod,
      params: { layout: "mobile", revision: 9, config: toWireConfig(proposed) },
    });
    expect(store.getState().hub.mobile).toBeUndefined();
    client.on(getMethod, () => ({
      desktop: toWireDefault(hubDefault(3, desktopConfig)),
      mobile: toWireDefault(hubDefault(9, proposed)),
    }));
    await store.getState().refreshHubDefaults();
    expect(store.getState().loaded).toBe(true);
    expect(store.getState().hub.mobile).toEqual(hubDefault(9, proposed));
  });

  test("a relayed change before the first read is followed by one read", async () => {
    const client = new FakeClient("ready");
    const first = deferred<TranscriptDisplayDefaults>();
    let reads = 0;
    client.on(getMethod, () => {
      reads += 1;
      return reads === 1
        ? first.promise
        : { desktop: toWireDefault(hubDefault(3, desktopConfig)), mobile: toWireDefault(hubDefault(9, proposed)) };
    });
    const store = createTranscriptDisplayStore({ client });
    store.setSupport("supported");
    store.beginReadyGeneration();
    const refresh = store.getState().refreshHubDefaults();
    await vi.waitFor(() => expect(reads).toBe(1));
    store.getState().applyHubChange({ layout: "mobile", revision: 9, config: proposed });
    first.resolve({
      desktop: toWireDefault(hubDefault(3, desktopConfig)),
      mobile: toWireDefault(hubDefault(2, mobileConfig)),
    });
    await refresh;
    await vi.waitFor(() => expect(store.getState().hub.mobile).toEqual(hubDefault(9, proposed)));
    expect(reads).toBe(2);
  });

  test("a relayed change during an active generation normalizes and clears a read error", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const store = await readyStore(client);
    client.on(getMethod, () => {
      throw new Error("hub unreachable");
    });
    await store.getState().refreshHubDefaults();
    expect(store.getState().hubError).toBe("hub unreachable");
    store.getState().applyHubChange({ layout: "mobile", revision: 5, config: proposed });
    expect(store.getState().hub.mobile).toEqual(hubDefault(5, proposed));
    expect(store.getState().hubError).toBeNull();
  });
});

describe("lifecycle fencing", () => {
  test("a lifecycle retirement during the loading publication prevents the request", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const store = createTranscriptDisplayStore({ client });
    store.setSupport("supported");
    store.beginReadyGeneration();
    store.subscribe((state, previous) => {
      if (!previous.hubLoading && state.hubLoading) store.endReadyGeneration();
    });

    await store.getState().refreshHubDefaults();

    expect(client.calls.filter((call) => call.method === getMethod)).toHaveLength(0);
    expect(store.getState()).toMatchObject({ loaded: false, hubLoading: false });
  });

  test.each([
    ["retirement", (store: TranscriptDisplayStore) => store.endReadyGeneration()],
    [
      "replacement",
      (store: TranscriptDisplayStore) => {
        store.endReadyGeneration();
        store.beginReadyGeneration();
      },
    ],
  ] as const)("a generation %s during the desktop publication fences the remaining read", async (_name, lifecycle) => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const store = createTranscriptDisplayStore({ client });
    store.setSupport("supported");
    store.beginReadyGeneration();
    store.subscribe((state, previous) => {
      if (state.hub.desktop !== previous.hub.desktop) lifecycle(store);
    });

    await store.getState().refreshHubDefaults();

    expect(store.getState()).toMatchObject({ loaded: false, hubLoading: false });
    expect(store.getState().hub.mobile).toBeUndefined();
  });

  test("a relayed lower revision during the loaded publication cannot roll back the first read", async () => {
    const client = serving(hubDefault(2, desktopConfig), hubDefault(2, mobileConfig));
    const store = createTranscriptDisplayStore({ client });
    store.setSupport("supported");
    store.beginReadyGeneration();
    store.subscribe((state, previous) => {
      if (!previous.loaded && state.loaded)
        store.getState().applyHubChange({ layout: "mobile", revision: 1, config: proposed });
    });

    await store.getState().refreshHubDefaults();

    expect(store.getState().hub.mobile).toEqual(hubDefault(2, mobileConfig));
  });

  test("a refresh started during the loaded publication keeps the first read authoritative", async () => {
    const client = new FakeClient("ready");
    let reads = 0;
    client.on(getMethod, () => {
      reads += 1;
      return {
        desktop: toWireDefault(hubDefault(reads === 1 ? 2 : 1, desktopConfig)),
        mobile: toWireDefault(hubDefault(reads === 1 ? 2 : 1, mobileConfig)),
      };
    });
    const store = createTranscriptDisplayStore({ client });
    store.setSupport("supported");
    store.beginReadyGeneration();
    let reentrantRefresh: Promise<void> | undefined;
    store.subscribe((state, previous) => {
      if (!previous.loaded && state.loaded && reads === 1) reentrantRefresh = store.getState().refreshHubDefaults();
    });

    await store.getState().refreshHubDefaults();
    if (reentrantRefresh === undefined) throw new Error("loaded publication did not start the reentrant refresh");
    await reentrantRefresh;

    expect(store.getState().hub).toEqual({
      desktop: hubDefault(2, desktopConfig),
      mobile: hubDefault(2, mobileConfig),
    });
  });

  test.each(["endReadyGeneration", "dispose"] as const)(
    "%s fences relayed changes after the ready generation ends",
    async (lifecycle) => {
      const store = await readyStore(serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig)));
      if (lifecycle === "endReadyGeneration") store.endReadyGeneration();
      else store.dispose();

      store.getState().applyHubChange({ layout: "mobile", revision: 9, config: proposed });

      expect(store.getState().hub).toEqual({
        desktop: hubDefault(3, desktopConfig),
        mobile: hubDefault(2, mobileConfig),
      });
    },
  );

  test("disconnect retires loading but keeps confirmed defaults and fences the late read", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const store = await readyStore(client);
    const late = deferred<TranscriptDisplayDefaults>();
    client.on(getMethod, () => late.promise);
    const refresh = store.getState().refreshHubDefaults();
    await vi.waitFor(() => expect(client.calls.filter((call) => call.method === getMethod)).toHaveLength(2));
    store.endReadyGeneration();
    late.resolve({
      desktop: toWireDefault(hubDefault(9, proposed)),
      mobile: toWireDefault(hubDefault(9, proposed)),
    });
    await refresh;
    expect(store.getState()).toMatchObject({ loaded: false, hubLoading: false });
    expect(store.getState().hub).toEqual({
      desktop: hubDefault(3, desktopConfig),
      mobile: hubDefault(2, mobileConfig),
    });
  });

  test("detaching the hub clears confirmed defaults and reset restores initial state", async () => {
    const store = await readyStore(serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig)));
    store.detachHub();
    expect(store.getState()).toMatchObject({ loaded: false, hub: {}, hubError: null });
    store.setSupport("supported");
    store.reset();
    expect(store.getState()).toMatchObject({ hubSupport: "unknown", loaded: false, hub: {}, hubError: null });
  });

  test("detaching the hub fences a late read from the previous hub identity", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const store = await readyStore(client);
    const late = deferred<TranscriptDisplayDefaults>();
    client.on(getMethod, () => late.promise);
    const refresh = store.getState().refreshHubDefaults();
    await vi.waitFor(() => expect(client.calls.filter((call) => call.method === getMethod)).toHaveLength(2));
    store.detachHub();
    late.resolve({
      desktop: toWireDefault(hubDefault(9, proposed)),
      mobile: toWireDefault(hubDefault(9, proposed)),
    });
    await refresh;
    expect(store.getState().hub).toEqual({});
    expect(store.getState().loaded).toBe(false);
  });

  test("dispose fences later work and leaves the last retired state", async () => {
    const store = await readyStore(serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig)));
    store.dispose();
    await store.getState().refreshHubDefaults();
    expect(store.getState()).toMatchObject({
      loaded: false,
      hubLoading: false,
      hub: { desktop: hubDefault(3, desktopConfig), mobile: hubDefault(2, mobileConfig) },
    });
  });
});
