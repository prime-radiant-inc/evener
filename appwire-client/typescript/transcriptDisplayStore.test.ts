import { describe, expect, test, vi } from "vitest";
import { WireError } from "./errors";
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
import type { AnyNotification, TranscriptDisplayDefaults, TranscriptDisplayPatchResponse } from "./types.gen";

const getMethod = "evener/settings/transcriptDisplay/get";
const patchMethod = "evener/settings/transcriptDisplay/patch";
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

function patchAnswer(layout: "desktop" | "mobile", value: HubTranscriptDisplayDefault): TranscriptDisplayPatchResponse {
  return { layout, revision: value.revision, config: toWireConfig(value.config) };
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

  test("a direct generation replacement fences a notification until its first read confirms", async () => {
    const client = serving(hubDefault(7, desktopConfig), hubDefault(7, mobileConfig));
    const store = await readyStore(client);
    let reads = 1;
    const first = deferred<TranscriptDisplayDefaults>();
    let requestStarted!: () => void;
    const requestObserved = new Promise<void>((resolve) => {
      requestStarted = resolve;
    });
    client.on(getMethod, () => {
      reads += 1;
      if (reads === 2) requestStarted();
      return reads === 2
        ? first.promise
        : {
            desktop: toWireDefault(hubDefault(9, proposed)),
            mobile: toWireDefault(hubDefault(9, proposed)),
          };
    });
    store.beginReadyGeneration();
    const refresh = store.getState().refreshHubDefaults();
    await requestObserved;
    store.getState().applyHubChange({ layout: "mobile", revision: 9, config: proposed });
    expect(store.getState().hub.mobile).toEqual(hubDefault(7, mobileConfig));
    first.resolve({
      desktop: toWireDefault(hubDefault(8, desktopConfig)),
      mobile: toWireDefault(hubDefault(8, mobileConfig)),
    });
    await refresh;
    await vi.waitFor(() => expect(store.getState().hub.mobile).toEqual(hubDefault(9, proposed)));
    expect(reads).toBe(3);
  });

  test("a direct generation replacement retires a pending read without clearing cached defaults", async () => {
    const client = serving(hubDefault(7, desktopConfig), hubDefault(7, mobileConfig));
    const store = await readyStore(client);
    const late = deferred<TranscriptDisplayDefaults>();
    client.on(getMethod, () => late.promise);

    const refresh = store.getState().refreshHubDefaults();
    await vi.waitFor(() => expect(client.calls.filter((call) => call.method === getMethod)).toHaveLength(2));
    store.beginReadyGeneration();

    expect(store.getState()).toMatchObject({ loaded: false, hubLoading: false });
    expect(store.getState().hub).toEqual({
      desktop: hubDefault(7, desktopConfig),
      mobile: hubDefault(7, mobileConfig),
    });

    late.resolve({
      desktop: toWireDefault(hubDefault(9, proposed)),
      mobile: toWireDefault(hubDefault(9, proposed)),
    });
    await refresh;

    expect(store.getState()).toMatchObject({ loaded: false, hubLoading: false });
    expect(store.getState().hub).toEqual({
      desktop: hubDefault(7, desktopConfig),
      mobile: hubDefault(7, mobileConfig),
    });
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
  ] as const)(
    "a generation %s during the authoritative publication leaves the read retired",
    async (_name, lifecycle) => {
      const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
      const store = createTranscriptDisplayStore({ client });
      store.setSupport("supported");
      store.beginReadyGeneration();
      store.subscribe((state, previous) => {
        if (state.hub.desktop !== previous.hub.desktop) lifecycle(store);
      });

      await store.getState().refreshHubDefaults();

      expect(store.getState()).toMatchObject({ loaded: false, hubLoading: false });
      expect(store.getState().hub).toEqual({
        desktop: hubDefault(3, desktopConfig),
        mobile: hubDefault(2, mobileConfig),
      });
    },
  );

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
describe("the direct write", () => {
  test("an atomic first read clears only contradicted stranded previews", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const store = await readyStore(client);
    const reply = deferred<TranscriptDisplayPatchResponse>();
    client.on(patchMethod, () => reply.promise);
    const write = store.getState().patchHubDefault("desktop", proposed);
    await vi.waitFor(() => expect(store.getState().drafts.desktop).toEqual(proposed));

    store.endReadyGeneration();
    client.on(getMethod, () => ({
      desktop: toWireDefault(hubDefault(3, proposed)),
      mobile: toWireDefault(hubDefault(3, desktopConfig)),
    }));
    store.beginReadyGeneration();
    const publications: ReturnType<TranscriptDisplayStore["getState"]>[] = [];
    const partialPublications: ReturnType<TranscriptDisplayStore["getState"]>[] = [];
    const unsubscribe = store.subscribe((state) => {
      if (state.loaded) publications.push(state);
      if (state.hubLoading && state.hub.desktop?.config.level === "tools") partialPublications.push(state);
    });
    await store.getState().refreshHubDefaults();

    expect(partialPublications).toHaveLength(0);
    expect(publications).toHaveLength(1);
    expect(publications[0]).toMatchObject({
      loaded: true,
      hub: {
        desktop: hubDefault(3, proposed),
        mobile: hubDefault(3, desktopConfig),
      },
      drafts: { desktop: proposed },
    });
    expect(publications[0].drafts.mobile).toBeUndefined();
    unsubscribe();
    reply.resolve({ layout: "desktop", revision: 3, config: toWireConfig(proposed) });
    await write;
  });

  test("previews the draft, then commits the canonical response and clears it", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const store = await readyStore(client);
    const reply = deferred<TranscriptDisplayPatchResponse>();
    client.on(patchMethod, () => reply.promise);
    const write = store.getState().patchHubDefault("mobile", proposed);
    await vi.waitFor(() => expect(store.getState().drafts.mobile).toEqual(proposed));
    reply.resolve(patchAnswer("mobile", hubDefault(3, proposed)));
    expect(await write).toEqual(hubDefault(3, proposed));
    expect(store.getState().hub.mobile).toEqual(hubDefault(3, proposed));
    expect(store.getState().drafts.mobile).toBeUndefined();
    expect(client.calls.at(-1)?.params).toEqual({
      layout: "mobile",
      expectedRevision: 2,
      config: toWireConfig(proposed),
    });
  });

  test("a fenced reply resolves with the current value instead of rejecting", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const store = await readyStore(client);
    const reply = deferred<TranscriptDisplayPatchResponse>();
    client.on(patchMethod, () => reply.promise);
    const write = store.getState().patchHubDefault("mobile", proposed);
    await vi.waitFor(() => expect(store.getState().drafts.mobile).toEqual(proposed));
    store.endReadyGeneration();
    reply.resolve(patchAnswer("mobile", hubDefault(3, proposed)));
    expect(await write).toEqual(hubDefault(2, mobileConfig));
    expect(store.getState().hub.mobile).toEqual(hubDefault(2, mobileConfig));
    expect(store.getState().drafts.mobile).toEqual(proposed);
  });

  test("a fenced conflict resolves with the retained current value and applies nothing", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const store = await readyStore(client);
    const reply = deferred<TranscriptDisplayPatchResponse>();
    client.on(patchMethod, () => reply.promise);
    const write = store.getState().patchHubDefault("mobile", proposed);
    await vi.waitFor(() => expect(store.getState().drafts.mobile).toEqual(proposed));
    store.endReadyGeneration();
    reply.reject(
      new WireError("revision conflict", -32013, {
        evenerErrorInfo: "conflict",
        layout: "mobile",
        current: toWireDefault(hubDefault(9, desktopConfig)),
      }),
    );
    expect(await write).toEqual(hubDefault(2, mobileConfig));
    expect(store.getState().hub.mobile).toEqual(hubDefault(2, mobileConfig));
  });

  test("dispose fences a direct reply and resolves with the retained current value", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const store = await readyStore(client);
    const reply = deferred<TranscriptDisplayPatchResponse>();
    client.on(patchMethod, () => reply.promise);
    const write = store.getState().patchHubDefault("mobile", proposed);
    await vi.waitFor(() => expect(store.getState().drafts.mobile).toEqual(proposed));
    store.dispose();
    reply.resolve(patchAnswer("mobile", hubDefault(3, proposed)));
    expect(await write).toEqual(hubDefault(2, mobileConfig));
    expect(store.getState().hub.mobile).toEqual(hubDefault(2, mobileConfig));
  });

  test("a lost revision race adopts the canonical current and reports the conflict on the layout", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const store = await readyStore(client);
    client.on(patchMethod, () => {
      throw new WireError("revision conflict", -32013, {
        evenerErrorInfo: "conflict",
        layout: "mobile",
        current: toWireDefault(hubDefault(4, desktopConfig)),
      });
    });
    await expect(store.getState().patchHubDefault("mobile", proposed)).rejects.toThrow("revision conflict");
    expect(store.getState().hub.mobile).toEqual(hubDefault(4, desktopConfig));
    expect(store.getState().drafts.mobile).toBeUndefined();
    expect(store.getState().hubErrors.mobile).toBe("revision conflict");
  });

  test("a conflict's canonical current with extra keys still decodes", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const store = await readyStore(client);
    client.on(patchMethod, () => {
      throw new WireError("revision conflict", -32013, {
        evenerErrorInfo: "conflict",
        layout: "mobile",
        current: { ...toWireDefault(hubDefault(4, desktopConfig)), futureField: "ignored" },
      });
    });
    await expect(store.getState().patchHubDefault("mobile", proposed)).rejects.toThrow("revision conflict");
    expect(store.getState().hub.mobile).toEqual(hubDefault(4, desktopConfig));
  });

  test("a post-apply durable failure applies the carried canonical state and reports success", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const store = await readyStore(client);
    client.on(patchMethod, () => {
      throw new WireError("sync transcript display state: boom", -32603, {
        evenerErrorInfo: "transcriptDisplayPostApply",
        applied: { ...toWireDefault(hubDefault(4, mobileConfig)), futureField: "ignored" },
      });
    });
    const applied = await store.getState().patchHubDefault("mobile", proposed);
    expect(applied).toEqual(hubDefault(4, mobileConfig));
    expect(store.getState().hub.mobile).toEqual(hubDefault(4, mobileConfig));
    expect(store.getState().drafts.mobile).toBeUndefined();
    expect(store.getState().hubErrors.mobile).toBeUndefined();
    expect(store.getState().hubError).toBeNull();
  });

  test("a post-apply payload that loses to a newer notification returns the newer value", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const store = await readyStore(client);
    client.on(patchMethod, () => {
      client.emitNotification({
        method: changedMethod,
        params: { layout: "mobile", revision: 7, config: toWireConfig(mobileConfig) },
      });
      throw new WireError("sync transcript display state: boom", -32603, {
        evenerErrorInfo: "transcriptDisplayPostApply",
        applied: toWireDefault(hubDefault(3, proposed)),
      });
    });
    const applied = await store.getState().patchHubDefault("mobile", proposed);
    expect(applied).toEqual(hubDefault(7, mobileConfig));
    expect(store.getState().hub.mobile).toEqual(hubDefault(7, mobileConfig));
    expect(store.getState().drafts.mobile).toBeUndefined();
  });

  test("an internal PATCH failure reconciles the canonical state through GET", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const store = await readyStore(client);
    client.on(patchMethod, () => {
      throw new WireError("after rename failed", -32603, { evenerErrorInfo: "internal" });
    });
    client.on(getMethod, () => ({
      desktop: toWireDefault(hubDefault(3, desktopConfig)),
      mobile: toWireDefault(hubDefault(3, proposed)),
    }));
    const reconciled = await store.getState().patchHubDefault("mobile", proposed);
    expect(reconciled).toEqual(hubDefault(3, proposed));
    expect(store.getState().hub.mobile).toEqual(hubDefault(3, proposed));
    expect(store.getState().drafts.mobile).toBeUndefined();
    expect(client.calls.map((call) => call.method)).toEqual([getMethod, patchMethod, getMethod]);
  });

  test("concurrent internal failures share their authoritative GET", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const store = await readyStore(client);
    const initialReads = client.calls.filter((call) => call.method === getMethod).length;
    const read = deferred<TranscriptDisplayDefaults>();
    client.on(getMethod, () => read.promise);
    client.on(patchMethod, () => {
      throw new WireError("after rename failed", -32603, { evenerErrorInfo: "internal" });
    });
    const desktopWrite = store.getState().patchHubDefault("desktop", proposed);
    const mobileWrite = store.getState().patchHubDefault("mobile", proposed);
    await vi.waitFor(() =>
      expect(client.calls.filter((call) => call.method === getMethod)).toHaveLength(initialReads + 1),
    );
    const canonical = {
      desktop: toWireDefault(hubDefault(4, proposed)),
      mobile: toWireDefault(hubDefault(3, proposed)),
    };
    read.resolve(canonical);
    await expect(desktopWrite).resolves.toEqual(hubDefault(4, proposed));
    await expect(mobileWrite).resolves.toEqual(hubDefault(3, proposed));
    expect(client.calls.filter((call) => call.method === getMethod)).toHaveLength(initialReads + 1);
  });

  test("a transient disconnect keeps the preview and identity detach drops it", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const store = await readyStore(client);
    const reply = deferred<TranscriptDisplayPatchResponse>();
    client.on(patchMethod, () => reply.promise);
    const write = store.getState().patchHubDefault("mobile", proposed);
    await vi.waitFor(() => expect(store.getState().drafts.mobile).toEqual(proposed));
    store.endReadyGeneration();
    expect(store.getState().drafts.mobile).toEqual(proposed);
    store.detachHub();
    expect(store.getState().drafts.mobile).toBeUndefined();
    client.on(getMethod, () => ({
      desktop: toWireDefault(hubDefault(1, proposed)),
      mobile: toWireDefault(hubDefault(1, mobileConfig)),
    }));
    store.beginReadyGeneration();
    await store.getState().refreshHubDefaults();
    reply.resolve(patchAnswer("mobile", hubDefault(3, proposed)));
    expect(await write).toEqual(hubDefault(1, mobileConfig));
    expect(store.getState().hub.mobile).toEqual(hubDefault(1, mobileConfig));
  });

  test("an unsupported transition drops the preview with the hub identity", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const store = await readyStore(client);
    client.on(patchMethod, () => new Promise<TranscriptDisplayPatchResponse>(() => {}));
    void store.getState().patchHubDefault("mobile", proposed);
    await vi.waitFor(() => expect(store.getState().drafts.mobile).toEqual(proposed));
    store.setSupport("unsupported");
    expect(store.getState().drafts.mobile).toBeUndefined();
  });

  test("a support flap retires the write and reloads before its fenced reply settles", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const store = await readyStore(client);
    const reply = deferred<TranscriptDisplayPatchResponse>();
    client.on(patchMethod, () => reply.promise);
    const write = store.getState().patchHubDefault("mobile", proposed);
    await vi.waitFor(() => expect(store.getState().drafts.mobile).toEqual(proposed));

    store.setSupport("unsupported");
    expect(store.getState().drafts.mobile).toBeUndefined();
    client.on(getMethod, () => ({
      desktop: toWireDefault(hubDefault(1, desktopConfig)),
      mobile: toWireDefault(hubDefault(1, mobileConfig)),
    }));
    store.setSupport("supported");
    await vi.waitFor(() => expect(store.getState().hub.mobile).toEqual(hubDefault(1, mobileConfig)));

    reply.resolve(patchAnswer("mobile", hubDefault(3, proposed)));
    expect(await write).toEqual(hubDefault(1, mobileConfig));
    expect(store.getState().hub.mobile).toEqual(hubDefault(1, mobileConfig));
  });

  test("a reply the hub has already moved past resolves with the newer value", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const store = await readyStore(client);
    const reply = deferred<TranscriptDisplayPatchResponse>();
    client.on(patchMethod, () => reply.promise);
    const write = store.getState().patchHubDefault("mobile", proposed);
    await vi.waitFor(() => expect(store.getState().drafts.mobile).toEqual(proposed));
    client.emitNotification({
      method: changedMethod,
      params: { layout: "mobile", revision: 7, config: toWireConfig(mobileConfig) },
    });
    reply.resolve(patchAnswer("mobile", hubDefault(3, proposed)));
    expect(await write).toEqual(hubDefault(7, mobileConfig));
    expect(store.getState().hub.mobile).toEqual(hubDefault(7, mobileConfig));
    expect(store.getState().drafts.mobile).toBeUndefined();
  });

  test("a newer confirmed payload that contradicts a stranded preview clears it", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const store = await readyStore(client);
    const reply = deferred<TranscriptDisplayPatchResponse>();
    client.on(patchMethod, () => reply.promise);
    const write = store.getState().patchHubDefault("mobile", proposed);
    await vi.waitFor(() => expect(store.getState().drafts.mobile).toEqual(proposed));
    store.endReadyGeneration();
    reply.resolve(patchAnswer("mobile", hubDefault(3, proposed)));
    expect(await write).toEqual(hubDefault(2, mobileConfig));
    expect(store.getState().drafts.mobile).toEqual(proposed);
    client.on(getMethod, () => ({
      desktop: toWireDefault(hubDefault(3, desktopConfig)),
      mobile: toWireDefault(hubDefault(4, desktopConfig)),
    }));
    store.beginReadyGeneration();
    await store.getState().refreshHubDefaults();
    expect(store.getState().hub.mobile).toEqual(hubDefault(4, desktopConfig));
    expect(store.getState().drafts.mobile).toBeUndefined();
  });

  test("a new generation's lower-revision read still invalidates a contradicted preview", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(7, mobileConfig));
    const store = await readyStore(client);
    const reply = deferred<TranscriptDisplayPatchResponse>();
    client.on(patchMethod, () => reply.promise);
    const write = store.getState().patchHubDefault("mobile", proposed);
    await vi.waitFor(() => expect(store.getState().drafts.mobile).toEqual(proposed));
    store.endReadyGeneration();
    reply.resolve(patchAnswer("mobile", hubDefault(8, proposed)));
    expect(await write).toEqual(hubDefault(7, mobileConfig));
    client.on(getMethod, () => ({
      desktop: toWireDefault(hubDefault(3, desktopConfig)),
      mobile: toWireDefault(hubDefault(3, mobileConfig)),
    }));
    store.beginReadyGeneration();
    await store.getState().refreshHubDefaults();
    expect(store.getState().hub.mobile).toEqual(hubDefault(3, mobileConfig));
    expect(store.getState().drafts.mobile).toBeUndefined();
  });

  test("a new generation's equal-revision read still invalidates a contradicted preview", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const store = await readyStore(client);
    const reply = deferred<TranscriptDisplayPatchResponse>();
    client.on(patchMethod, () => reply.promise);
    const write = store.getState().patchHubDefault("mobile", proposed);
    await vi.waitFor(() => expect(store.getState().drafts.mobile).toEqual(proposed));
    store.endReadyGeneration();
    reply.resolve(patchAnswer("mobile", hubDefault(3, proposed)));
    expect(await write).toEqual(hubDefault(2, mobileConfig));
    client.on(getMethod, () => ({
      desktop: toWireDefault(hubDefault(3, desktopConfig)),
      mobile: toWireDefault(hubDefault(2, desktopConfig)),
    }));
    store.beginReadyGeneration();
    await store.getState().refreshHubDefaults();
    expect(store.getState().hub.mobile).toEqual(hubDefault(2, desktopConfig));
    expect(store.getState().drafts.mobile).toBeUndefined();
  });

  test("a newer confirmed payload matching a stranded preview leaves it for the host", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const store = await readyStore(client);
    const reply = deferred<TranscriptDisplayPatchResponse>();
    client.on(patchMethod, () => reply.promise);
    const write = store.getState().patchHubDefault("mobile", proposed);
    await vi.waitFor(() => expect(store.getState().drafts.mobile).toEqual(proposed));
    store.endReadyGeneration();
    reply.resolve(patchAnswer("mobile", hubDefault(3, proposed)));
    expect(await write).toEqual(hubDefault(2, mobileConfig));
    client.on(getMethod, () => ({
      desktop: toWireDefault(hubDefault(3, desktopConfig)),
      mobile: toWireDefault(hubDefault(3, proposed)),
    }));
    store.beginReadyGeneration();
    await store.getState().refreshHubDefaults();
    expect(store.getState().hub.mobile).toEqual(hubDefault(3, proposed));
    expect(store.getState().drafts.mobile).toEqual(proposed);
  });

  test("a malformed success changes no hub state, reports it, and clears the preview", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const store = await readyStore(client);
    client.on(patchMethod, () => ({ layout: "mobile", revision: 9, config: toWireConfig(proposed) }));
    await expect(store.getState().patchHubDefault("mobile", proposed)).rejects.toThrow(/malformed/);
    expect(store.getState().hub.mobile).toEqual(hubDefault(2, mobileConfig));
    expect(store.getState().hubErrors.mobile).toMatch(/malformed/);
    expect(store.getState().drafts.mobile).toBeUndefined();
  });

  test("a PATCH reply with extra keys still decodes", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const store = await readyStore(client);
    client.on(patchMethod, () => ({
      ...patchAnswer("mobile", hubDefault(3, proposed)),
      futureField: "ignored",
    }));
    const result = await store.getState().patchHubDefault("mobile", proposed);
    expect(result).toEqual(hubDefault(3, proposed));
    expect(store.getState().hub.mobile).toEqual(hubDefault(3, proposed));
  });

  test("refuses without a confirmed, supported hub", async () => {
    const store = createTranscriptDisplayStore({ client: new FakeClient("ready") });
    await expect(store.getState().patchHubDefault("mobile", proposed)).rejects.toThrow(/unavailable/);
    expect(store.getState().hubErrors.mobile).toMatch(/unavailable/);
  });

  test("does not dispatch a PATCH after a preview subscriber retires the store", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const store = await readyStore(client);
    let retired = false;
    store.subscribe((state) => {
      if (!retired && state.drafts.mobile !== undefined) {
        retired = true;
        store.dispose();
      }
    });
    const result = await store.getState().patchHubDefault("mobile", proposed);
    expect(result).toEqual(hubDefault(2, mobileConfig));
    expect(client.calls.some((call) => call.method === patchMethod)).toBe(false);
  });
});
