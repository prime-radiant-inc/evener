import { describe, expect, test } from "vitest";
import { WireError } from "./errors";
import { deferred } from "./testing/deferred";
import { memoryDraftStorage } from "./testing/draftStorage";
import { FakeClient } from "./testing/fakeClient";
import { nextMacrotask } from "./testing/macrotask";
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
  type TranscriptDisplayStoreDeps,
  type TranscriptDraftCheckpoint,
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

function conflictError(layout: "desktop" | "mobile", current: HubTranscriptDisplayDefault): WireError {
  return new WireError("revision conflict", -32013, {
    evenerErrorInfo: "conflict",
    layout,
    current: toWireDefault(current),
  });
}

function postApplyError(layout: "desktop" | "mobile", applied: HubTranscriptDisplayDefault): WireError {
  return new WireError("sync transcript display state: boom", -32603, {
    evenerErrorInfo: "transcriptDisplayPostApply",
    layout,
    applied: toWireDefault(applied),
  });
}

function internalError(): WireError {
  return new WireError("after rename failed", -32603, { evenerErrorInfo: "internal" });
}

function captureLoadedPublications(store: TranscriptDisplayStore): {
  publications: ReturnType<TranscriptDisplayStore["getState"]>[];
  unsubscribe: () => void;
} {
  const publications: ReturnType<TranscriptDisplayStore["getState"]>[] = [];
  const unsubscribe = store.subscribe((state) => {
    if (state.loaded) publications.push(state);
  });
  return { publications, unsubscribe };
}

async function readyStore(
  client: FakeClient,
  deps: Partial<TranscriptDisplayStoreDeps> = {},
): Promise<TranscriptDisplayStore> {
  const store = createTranscriptDisplayStore({ client, ...deps });
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
    await nextMacrotask();
    expect(store.getState().loaded).toBe(true);
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
    await nextMacrotask();
    expect(store.getState().hub.mobile).toEqual(hubDefault(6, proposed));
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
    await nextMacrotask();
    expect(reads).toBe(1);
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
    expect(store.getState().hub.mobile).toEqual(hubDefault(9, proposed));
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
    expect(store.getState().hub.mobile).toEqual(hubDefault(9, proposed));
    expect(reads).toBe(3);
  });

  test("a direct generation replacement retires a pending read without clearing cached defaults", async () => {
    const client = serving(hubDefault(7, desktopConfig), hubDefault(7, mobileConfig));
    const store = await readyStore(client);
    const late = deferred<TranscriptDisplayDefaults>();
    client.on(getMethod, () => late.promise);

    const refresh = store.getState().refreshHubDefaults();
    await nextMacrotask();
    expect(client.calls.filter((call) => call.method === getMethod)).toHaveLength(2);
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
    await nextMacrotask();
    expect(reads).toBe(1);
    store.getState().applyHubChange({ layout: "mobile", revision: 9, config: proposed });
    first.resolve({
      desktop: toWireDefault(hubDefault(3, desktopConfig)),
      mobile: toWireDefault(hubDefault(2, mobileConfig)),
    });
    await refresh;
    expect(store.getState().hub.mobile).toEqual(hubDefault(9, proposed));
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
    await nextMacrotask();
    expect(client.calls.filter((call) => call.method === getMethod)).toHaveLength(2);
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
    await nextMacrotask();
    expect(client.calls.filter((call) => call.method === getMethod)).toHaveLength(2);
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
  test("an atomic first read clears a contradicted stranded preview and keeps the matching one", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const store = await readyStore(client);
    const desktopReply = deferred<TranscriptDisplayPatchResponse>();
    const mobileReply = deferred<TranscriptDisplayPatchResponse>();
    let patches = 0;
    client.on(patchMethod, () => {
      patches += 1;
      return (patches === 1 ? desktopReply : mobileReply).promise;
    });
    const desktopWrite = store.getState().patchHubDefault("desktop", proposed);
    await nextMacrotask();
    expect(store.getState().drafts.desktop).toEqual(proposed);
    const mobileWrite = store.getState().patchHubDefault("mobile", proposed);
    await nextMacrotask();
    expect(store.getState().drafts.mobile).toEqual(proposed);

    store.endReadyGeneration();
    // The first read of the new generation contradicts only the desktop
    // preview: its payload differs from the stranded draft and arrives after
    // the preview's base revision. The mobile payload IS the stranded draft.
    client.on(getMethod, () => ({
      desktop: toWireDefault(hubDefault(4, desktopConfig)),
      mobile: toWireDefault(hubDefault(3, proposed)),
    }));
    const { publications, unsubscribe } = captureLoadedPublications(store);
    store.beginReadyGeneration();
    await store.getState().refreshHubDefaults();

    expect(publications).toHaveLength(1);
    const [publication] = publications;
    if (publication === undefined) throw new Error("expected exactly one publication");
    expect(publication).toMatchObject({
      loaded: true,
      hub: {
        desktop: hubDefault(4, desktopConfig),
        mobile: hubDefault(3, proposed),
      },
      drafts: { mobile: proposed },
    });
    expect(publication.drafts.desktop).toBeUndefined();
    unsubscribe();
    desktopReply.resolve(patchAnswer("desktop", hubDefault(3, proposed)));
    mobileReply.resolve(patchAnswer("mobile", hubDefault(3, proposed)));
    await desktopWrite;
    await mobileWrite;
  });

  test("an atomic first read clears only contradicted stranded previews", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const store = await readyStore(client);
    const reply = deferred<TranscriptDisplayPatchResponse>();
    client.on(patchMethod, () => reply.promise);
    const write = store.getState().patchHubDefault("desktop", proposed);
    await nextMacrotask();
    expect(store.getState().drafts.desktop).toEqual(proposed);

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
      if (
        state.hubLoading &&
        state.hub.desktop?.config.content.kind === "preset" &&
        state.hub.desktop.config.content.level === "tools"
      )
        partialPublications.push(state);
    });
    await store.getState().refreshHubDefaults();

    expect(partialPublications).toHaveLength(0);
    expect(publications).toHaveLength(1);
    const [publication] = publications;
    if (publication === undefined) throw new Error("expected exactly one publication");
    expect(publication).toMatchObject({
      loaded: true,
      hub: {
        desktop: hubDefault(3, proposed),
        mobile: hubDefault(3, desktopConfig),
      },
      drafts: { desktop: proposed },
    });
    expect(publication.drafts.mobile).toBeUndefined();
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
    await nextMacrotask();
    expect(store.getState().drafts.mobile).toEqual(proposed);
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
    await nextMacrotask();
    expect(store.getState().drafts.mobile).toEqual(proposed);
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
    await nextMacrotask();
    expect(store.getState().drafts.mobile).toEqual(proposed);
    store.endReadyGeneration();
    reply.reject(conflictError("mobile", hubDefault(9, desktopConfig)));
    expect(await write).toEqual(hubDefault(2, mobileConfig));
    expect(store.getState().hub.mobile).toEqual(hubDefault(2, mobileConfig));
  });

  test("dispose fences a direct reply and resolves with the retained current value", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const store = await readyStore(client);
    const reply = deferred<TranscriptDisplayPatchResponse>();
    client.on(patchMethod, () => reply.promise);
    const write = store.getState().patchHubDefault("mobile", proposed);
    await nextMacrotask();
    expect(store.getState().drafts.mobile).toEqual(proposed);
    store.dispose();
    reply.resolve(patchAnswer("mobile", hubDefault(3, proposed)));
    expect(await write).toEqual(hubDefault(2, mobileConfig));
    expect(store.getState().hub.mobile).toEqual(hubDefault(2, mobileConfig));
  });

  test("a lost revision race adopts the canonical current and reports the conflict on the layout", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const store = await readyStore(client);
    client.on(patchMethod, () => {
      throw conflictError("mobile", hubDefault(4, desktopConfig));
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
        layout: "mobile",
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
      throw postApplyError("mobile", hubDefault(3, proposed));
    });
    const applied = await store.getState().patchHubDefault("mobile", proposed);
    expect(applied).toEqual(hubDefault(7, mobileConfig));
    expect(store.getState().hub.mobile).toEqual(hubDefault(7, mobileConfig));
    expect(store.getState().drafts.mobile).toBeUndefined();
  });

  test("a post-apply payload naming another layout is not this write's answer", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const store = await readyStore(client);
    client.on(patchMethod, () => {
      throw postApplyError("desktop", hubDefault(4, mobileConfig));
    });
    await expect(store.getState().patchHubDefault("mobile", proposed)).rejects.toThrow();
    expect(store.getState().hub.mobile).toEqual(hubDefault(2, mobileConfig));
    expect(store.getState().hub.desktop).toEqual(hubDefault(3, desktopConfig));
    expect(store.getState().drafts.mobile).toBeUndefined();
    expect(store.getState().hubErrors.mobile).toEqual("sync transcript display state: boom");
  });

  test("an accepted canonical read clears the layout's stale write error", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const store = await readyStore(client);
    client.on(patchMethod, () => {
      throw new WireError("boom", -32000);
    });
    await expect(store.getState().patchHubDefault("desktop", proposed)).rejects.toThrow("boom");
    expect(store.getState().hubErrors.desktop).toBe("boom");

    client.on(getMethod, () => ({
      desktop: toWireDefault(hubDefault(4, proposed)),
      mobile: toWireDefault(hubDefault(2, mobileConfig)),
    }));
    await store.getState().refreshHubDefaults();
    expect(store.getState().hub.desktop).toEqual(hubDefault(4, proposed));
    expect(store.getState().hubErrors.desktop).toBeUndefined();
    expect("desktop" in store.getState().hubErrors).toBe(false);
  });

  test("an accepted change notification clears the layout's stale write error", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const store = await readyStore(client);
    client.on(patchMethod, () => {
      throw new WireError("boom", -32000);
    });
    await expect(store.getState().patchHubDefault("desktop", proposed)).rejects.toThrow("boom");
    expect(store.getState().hubErrors.desktop).toBe("boom");

    client.emitNotification({
      method: changedMethod,
      params: { layout: "desktop", revision: 4, config: toWireConfig(proposed) },
    });
    expect(store.getState().hub.desktop).toEqual(hubDefault(4, proposed));
    expect(store.getState().hubErrors.desktop).toBeUndefined();
    expect("desktop" in store.getState().hubErrors).toBe(false);
  });

  test("a stale canonical read keeps the layout's stale write error", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const store = await readyStore(client);
    client.on(patchMethod, () => {
      throw new WireError("boom", -32000);
    });
    await expect(store.getState().patchHubDefault("desktop", proposed)).rejects.toThrow("boom");
    expect(store.getState().hubErrors.desktop).toBe("boom");

    client.on(getMethod, () => ({
      desktop: toWireDefault(hubDefault(2, desktopConfig)),
      mobile: toWireDefault(hubDefault(2, mobileConfig)),
    }));
    await store.getState().refreshHubDefaults();
    expect(store.getState().hub.desktop).toEqual(hubDefault(3, desktopConfig));
    expect(store.getState().hubErrors.desktop).toBe("boom");
  });

  test("a preview stranded by a support flap clears when the next read confirms the write did not land", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const store = await readyStore(client);
    const reply = deferred<TranscriptDisplayPatchResponse>();
    client.on(patchMethod, () => reply.promise);
    const write = store.getState().patchHubDefault("desktop", proposed);
    await nextMacrotask();
    expect(store.getState().drafts.desktop).toEqual(proposed);

    store.setSupport("unknown");
    reply.reject(new Error("boom"));
    expect(await write).toEqual(hubDefault(3, desktopConfig));
    expect(store.getState().drafts.desktop).toEqual(proposed);

    // The read that restores support carries an unchanged canonical, which
    // proves the stranded write never landed: the preview clears with it.
    client.on(getMethod, () => ({
      desktop: toWireDefault(hubDefault(3, desktopConfig)),
      mobile: toWireDefault(hubDefault(2, mobileConfig)),
    }));
    store.setSupport("supported");
    await nextMacrotask();
    expect(store.getState().drafts.desktop).toBeUndefined();
    expect(store.getState().hub.desktop).toEqual(hubDefault(3, desktopConfig));
  });

  test("a superseded write's support flap does not clear its successor's live preview", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const store = await readyStore(client);
    const olderReply = deferred<TranscriptDisplayPatchResponse>();
    const newerReply = deferred<TranscriptDisplayPatchResponse>();
    let patches = 0;
    client.on(patchMethod, () => {
      patches += 1;
      return (patches === 1 ? olderReply : newerReply).promise;
    });
    const olderWrite = store.getState().patchHubDefault("desktop", desktopConfig);
    const newerWrite = store.getState().patchHubDefault("desktop", proposed);
    await nextMacrotask();
    expect(store.getState().drafts.desktop).toEqual(proposed);

    store.setSupport("unknown");
    olderReply.reject(new Error("older boom"));
    expect(await olderWrite).toEqual(hubDefault(3, desktopConfig));

    // The read that restores support carries an unchanged canonical. The
    // older write lost the layout's token to the newer one, so its dead
    // continuation must not have marked the preview for that read to clear:
    // the newer write's live preview survives it.
    client.on(getMethod, () => ({
      desktop: toWireDefault(hubDefault(3, desktopConfig)),
      mobile: toWireDefault(hubDefault(2, mobileConfig)),
    }));
    store.setSupport("supported");
    await nextMacrotask();
    expect(store.getState().hubLoading).toBe(false);
    expect(store.getState().drafts.desktop).toEqual(proposed);

    newerReply.resolve(patchAnswer("desktop", hubDefault(4, proposed)));
    expect(await newerWrite).toEqual(hubDefault(4, proposed));
    expect(store.getState().hub.desktop).toEqual(hubDefault(4, proposed));
    expect(store.getState().drafts.desktop).toBeUndefined();
  });

  test("a support flap during internal-failure recovery still strands the unseen preview", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const store = await readyStore(client);
    const read = deferred<TranscriptDisplayDefaults>();
    client.on(getMethod, () => read.promise);
    client.on(patchMethod, () => {
      throw internalError();
    });
    const write = store.getState().patchHubDefault("desktop", proposed);
    await nextMacrotask();
    expect(store.getState().drafts.desktop).toEqual(proposed);
    await nextMacrotask();
    expect(client.calls.filter((call) => call.method === getMethod)).toHaveLength(2);

    store.setSupport("unknown");
    read.resolve({
      desktop: toWireDefault(hubDefault(3, desktopConfig)),
      mobile: toWireDefault(hubDefault(2, mobileConfig)),
    });
    expect(await write).toEqual(hubDefault(3, desktopConfig));
    expect(store.getState().drafts.desktop).toEqual(proposed);

    // The recovery GET proved the write never landed, but the flap struck
    // after it, so the continuation had to strand the preview at the
    // recovery fence: the read that restores support clears it.
    client.on(getMethod, () => ({
      desktop: toWireDefault(hubDefault(3, desktopConfig)),
      mobile: toWireDefault(hubDefault(2, mobileConfig)),
    }));
    store.setSupport("supported");
    await nextMacrotask();
    expect(store.getState().drafts.desktop).toBeUndefined();
    expect(store.getState().hub.desktop).toEqual(hubDefault(3, desktopConfig));
  });

  test("a reentrant support loss during preview publication still strands the unsent write", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const store = await readyStore(client);
    client.on(patchMethod, () => new Promise<TranscriptDisplayPatchResponse>(() => {}));
    // The draft's own publication runs this subscriber synchronously, so
    // support drops before patchHubDefault has even sent its request.
    let tripped = false;
    store.subscribe(() => {
      if (tripped || store.getState().drafts.desktop === undefined) return;
      tripped = true;
      store.setSupport("unknown");
    });
    const write = store.getState().patchHubDefault("desktop", proposed);
    expect(await write).toEqual(hubDefault(3, desktopConfig));
    expect(client.calls.filter((call) => call.method === patchMethod)).toHaveLength(0);
    expect(store.getState().drafts.desktop).toEqual(proposed);

    // The request was never sent, so only the marker can clear the preview
    // when the restoring read carries an unchanged revision.
    client.on(getMethod, () => ({
      desktop: toWireDefault(hubDefault(3, desktopConfig)),
      mobile: toWireDefault(hubDefault(2, mobileConfig)),
    }));
    store.setSupport("supported");
    await nextMacrotask();
    expect(store.getState().drafts.desktop).toBeUndefined();
    expect(store.getState().hub.desktop).toEqual(hubDefault(3, desktopConfig));
  });

  test("an internal PATCH failure reconciles the canonical state through GET", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const store = await readyStore(client);
    client.on(patchMethod, () => {
      throw internalError();
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
      throw internalError();
    });
    const desktopWrite = store.getState().patchHubDefault("desktop", proposed);
    const mobileWrite = store.getState().patchHubDefault("mobile", proposed);
    await nextMacrotask();
    expect(client.calls.filter((call) => call.method === getMethod)).toHaveLength(initialReads + 1);
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
    await nextMacrotask();
    expect(store.getState().drafts.mobile).toEqual(proposed);
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
    await nextMacrotask();
    expect(store.getState().drafts.mobile).toEqual(proposed);
    store.setSupport("unsupported");
    expect(store.getState().drafts.mobile).toBeUndefined();
  });

  test("a support flap retires the write and reloads before its fenced reply settles", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const store = await readyStore(client);
    const reply = deferred<TranscriptDisplayPatchResponse>();
    client.on(patchMethod, () => reply.promise);
    const write = store.getState().patchHubDefault("mobile", proposed);
    await nextMacrotask();
    expect(store.getState().drafts.mobile).toEqual(proposed);

    store.setSupport("unsupported");
    expect(store.getState().drafts.mobile).toBeUndefined();
    client.on(getMethod, () => ({
      desktop: toWireDefault(hubDefault(1, desktopConfig)),
      mobile: toWireDefault(hubDefault(1, mobileConfig)),
    }));
    store.setSupport("supported");
    await nextMacrotask();
    expect(store.getState().hub.mobile).toEqual(hubDefault(1, mobileConfig));

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
    await nextMacrotask();
    expect(store.getState().drafts.mobile).toEqual(proposed);
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
    await nextMacrotask();
    expect(store.getState().drafts.mobile).toEqual(proposed);
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
    await nextMacrotask();
    expect(store.getState().drafts.mobile).toEqual(proposed);
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
    await nextMacrotask();
    expect(store.getState().drafts.mobile).toEqual(proposed);
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
    await nextMacrotask();
    expect(store.getState().drafts.mobile).toEqual(proposed);
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

describe("the checkpointed draft editor", () => {
  test("edit persists the proposal; save checkpoints before the PATCH leaves and clears it on confirmation", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const drafts = memoryDraftStorage<TranscriptDraftCheckpoint>();
    const store = await readyStore(client, { drafts: drafts.storage });
    store.getState().editDraft("mobile", proposed);
    expect(store.getState().draft).toEqual({ layout: "mobile", revision: 2, config: proposed, generation: 1 });
    expect(drafts.stored()).toMatchObject({
      layout: "mobile",
      baseRevision: 2,
      config: proposed,
      writeUncertain: false,
    });

    // The checkpoint must name the uncertain intent before the request can
    // leave: the PATCH handler observes what the port already holds.
    const reply = deferred<TranscriptDisplayPatchResponse>();
    let checkpointAtRequest: unknown = null;
    client.on(patchMethod, () => {
      checkpointAtRequest = drafts.stored();
      return reply.promise;
    });
    const save = store.getState().saveDraft();
    await nextMacrotask();
    expect(checkpointAtRequest).toMatchObject({ writeUncertain: true });
    expect(client.calls.at(-1)?.params).toEqual({
      layout: "mobile",
      expectedRevision: 2,
      config: toWireConfig(proposed),
    });
    expect(store.getState().saving).toBe(true);

    reply.resolve(patchAnswer("mobile", hubDefault(3, proposed)));
    expect(await save).toEqual(hubDefault(3, proposed));
    expect(store.getState()).toMatchObject({
      saving: false,
      writeUncertain: false,
      draft: null,
      draftConflict: false,
    });
    expect(drafts.stored()).toBeNull();
    expect(store.getState().hub.mobile).toEqual(hubDefault(3, proposed));
  });

  test("save refuses stale, unloaded, saving, uncertain, unsupported, and replaced-checkpoint state", async () => {
    // Unloaded: no ready generation has confirmed anything.
    const fresh = createTranscriptDisplayStore({ client: new FakeClient("ready") });
    await expect(fresh.getState().saveDraft("mobile", proposed)).rejects.toThrow(/unavailable/);

    // Unsupported: the hub does not advertise the section.
    const retired = await readyStore(serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig)));
    retired.setSupport("unsupported");
    await expect(retired.getState().saveDraft("mobile", proposed)).rejects.toThrow(/unavailable/);

    // Busy, then uncertain, then stale, in sequence on one live store.
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const drafts = memoryDraftStorage<TranscriptDraftCheckpoint>();
    const store = await readyStore(client, { drafts: drafts.storage });
    const reply = deferred<TranscriptDisplayPatchResponse>();
    client.on(patchMethod, () => reply.promise);
    const save = store.getState().saveDraft("mobile", proposed);
    await nextMacrotask();
    expect(store.getState().saving).toBe(true);
    await expect(store.getState().saveDraft("mobile", proposed)).rejects.toThrow(/unavailable/);
    reply.resolve(patchAnswer("mobile", hubDefault(3, proposed)));
    await save;

    client.on(patchMethod, () => {
      throw new Error("connection lost");
    });
    await expect(store.getState().saveDraft("mobile", mobileConfig)).rejects.toThrow("connection lost");
    expect(store.getState().writeUncertain).toBe(true);
    await expect(store.getState().saveDraft()).rejects.toThrow(/unavailable/);

    await store.getState().refreshHubDefaults();
    expect(store.getState().writeUncertain).toBe(false);
    client.emitNotification({
      method: changedMethod,
      params: { layout: "mobile", revision: 7, config: toWireConfig(desktopConfig) },
    });
    expect(store.getState().draftConflict).toBe(true);
    await expect(store.getState().saveDraft()).rejects.toThrow(/before saving your changes/);

    // A checkpoint another writer replaced since this store classified it
    // refuses through the port's compare-and-swap, and the replacement is
    // adopted rather than overwritten.
    const replaced = memoryDraftStorage<TranscriptDraftCheckpoint>();
    const replacedStore = await readyStore(serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig)), {
      drafts: replaced.storage,
    });
    replacedStore.getState().editDraft("mobile", proposed);
    const other: TranscriptDraftCheckpoint = {
      id: "other",
      layout: "desktop",
      baseRevision: 3,
      config: desktopConfig,
      writeUncertain: false,
    };
    replaced.storage.save(other);
    await expect(replacedStore.getState().saveDraft()).rejects.toThrow(/changed again/);
    expect(replaced.stored()).toEqual(other);
    expect(replacedStore.getState().draft?.layout).toBe("desktop");
  });

  test("a lost reply preserves the uncertain checkpoint; an authoritative read settles it", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const drafts = memoryDraftStorage<TranscriptDraftCheckpoint>();
    const store = await readyStore(client, { drafts: drafts.storage });
    store.getState().editDraft("mobile", proposed);
    client.on(patchMethod, () => {
      throw new Error("connection lost");
    });
    await expect(store.getState().saveDraft()).rejects.toThrow("connection lost");
    expect(store.getState()).toMatchObject({ saving: false, writeUncertain: true, draftConflict: true });
    expect(drafts.stored()).toMatchObject({ writeUncertain: true });
    // Edits stay blocked while the outcome is unknown.
    expect(() => store.getState().editDraft("mobile", mobileConfig)).toThrow(/unavailable/);

    await store.getState().refreshHubDefaults();
    expect(store.getState().writeUncertain).toBe(false);
    expect(drafts.stored()).toMatchObject({ writeUncertain: false });
    expect(store.getState().draft).toEqual({ layout: "mobile", revision: 2, config: proposed, generation: 1 });
  });

  test("a known revision conflict lands the canonical value and keeps the proposal for review", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const drafts = memoryDraftStorage<TranscriptDraftCheckpoint>();
    const store = await readyStore(client, { drafts: drafts.storage });
    client.on(patchMethod, () => {
      throw conflictError("mobile", hubDefault(5, desktopConfig));
    });
    await expect(store.getState().saveDraft("mobile", proposed)).rejects.toThrow("revision conflict");
    expect(store.getState()).toMatchObject({ saving: false, writeUncertain: false, draftConflict: true });
    expect(store.getState().hub.mobile).toEqual(hubDefault(5, desktopConfig));
    expect(store.getState().draft?.config).toEqual(proposed);
    expect(drafts.stored()).toMatchObject({ writeUncertain: false });
  });

  test("a conflict settles its checkpoint before edits unblock, so a subscriber's newer edit survives", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const drafts = memoryDraftStorage<TranscriptDraftCheckpoint>();
    const store = await readyStore(client, { drafts: drafts.storage });
    client.on(patchMethod, () => {
      throw conflictError("mobile", hubDefault(5, desktopConfig));
    });
    // A synchronous subscriber edits the moment the conflict publication
    // unblocks the editor.
    let edited = false;
    const unsubscribe = store.subscribe((state) => {
      if (edited || state.saving || !state.draftConflict) return;
      edited = true;
      store.getState().editDraft("mobile", mobileConfig);
    });
    await expect(store.getState().saveDraft("mobile", proposed)).rejects.toThrow("revision conflict");
    unsubscribe();
    // The re-mark must settle this write's own record before edits unblock -
    // not reach past the subscriber's newer edit once their save has
    // re-classified the repository.
    expect(drafts.stored()).toMatchObject({ layout: "mobile", config: mobileConfig });
  });

  test("a checkpointed takeover clears the superseded direct write's preview even when both writes fail", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const drafts = memoryDraftStorage<TranscriptDraftCheckpoint>();
    const store = await readyStore(client, { drafts: drafts.storage });
    const first = deferred<TranscriptDisplayPatchResponse>();
    const second = deferred<TranscriptDisplayPatchResponse>();
    let patchCount = 0;
    client.on(patchMethod, () => {
      patchCount += 1;
      return patchCount === 1 ? first.promise : second.promise;
    });
    const direct = store.getState().patchHubDefault("mobile", proposed);
    await nextMacrotask();
    expect(store.getState().drafts.mobile).toEqual(proposed);
    // The checkpointed editor takes the layer over while the direct write's
    // reply is still outstanding.
    store.getState().editDraft("mobile", desktopConfig);
    const save = store.getState().saveDraft();
    await nextMacrotask();
    expect(store.getState().saving).toBe(true);
    first.reject(new Error("connection lost"));
    second.reject(new Error("connection lost"));
    // The direct write's discarded reply settles nothing of its own.
    expect(await direct).toEqual(hubDefault(2, mobileConfig));
    await expect(save).rejects.toThrow("connection lost");
    // An unchanged refresh contradicts nothing, so it cannot clear the
    // preview either: only the takeover can.
    await store.getState().refreshHubDefaults();
    expect(store.getState().drafts).toEqual({});
  });

  test("a checkpoint replaced during a successful save adopts the replacement without sticking on saving", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const drafts = memoryDraftStorage<TranscriptDraftCheckpoint>();
    const store = await readyStore(client, { drafts: drafts.storage });
    store.getState().editDraft("mobile", proposed);
    // Another window replaces the checkpoint while this PATCH is in flight.
    const reply = deferred<TranscriptDisplayPatchResponse>();
    client.on(patchMethod, () => {
      drafts.storage.save({
        id: "other",
        layout: "desktop",
        baseRevision: 3,
        config: desktopConfig,
        writeUncertain: false,
      });
      return reply.promise;
    });
    const save = store.getState().saveDraft();
    await nextMacrotask();
    expect(store.getState().saving).toBe(true);
    reply.resolve(patchAnswer("mobile", hubDefault(3, proposed)));
    expect(await save).toEqual(hubDefault(3, proposed));
    // The replacement survives, the settlement clears the in-flight flags,
    // and the editor stays usable.
    expect(drafts.stored()).toMatchObject({ id: "other", layout: "desktop" });
    expect(store.getState().saving).toBe(false);
    expect(store.getState().draft?.layout).toBe("desktop");
    expect(() => store.getState().editDraft("desktop", proposed)).not.toThrow();
  });

  test("a checkpoint replaced during a newer-external save adopts the replacement without sticking on saving", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const drafts = memoryDraftStorage<TranscriptDraftCheckpoint>();
    const store = await readyStore(client, { drafts: drafts.storage });
    store.getState().editDraft("mobile", proposed);
    const reply = deferred<TranscriptDisplayPatchResponse>();
    client.on(patchMethod, () => reply.promise);
    const save = store.getState().saveDraft();
    await nextMacrotask();
    expect(store.getState().saving).toBe(true);
    // An external change beats this write's revision and another window
    // replaces the checkpoint, both while the PATCH is out.
    client.emitNotification({
      method: changedMethod,
      params: { layout: "mobile", revision: 7, config: toWireConfig(desktopConfig) },
    });
    drafts.storage.save({
      id: "other",
      layout: "desktop",
      baseRevision: 3,
      config: desktopConfig,
      writeUncertain: false,
    });
    reply.resolve(patchAnswer("mobile", hubDefault(3, proposed)));
    expect(await save).toEqual(hubDefault(3, proposed));
    expect(drafts.stored()).toMatchObject({ id: "other", layout: "desktop" });
    expect(store.getState().saving).toBe(false);
    expect(store.getState().draft?.layout).toBe("desktop");
    expect(() => store.getState().editDraft("desktop", proposed)).not.toThrow();
  });

  test("a post-apply rejection is a confirmed save, not an uncertain one", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const drafts = memoryDraftStorage<TranscriptDraftCheckpoint>();
    const store = await readyStore(client, { drafts: drafts.storage });
    store.getState().editDraft("mobile", proposed);
    client.on(patchMethod, () => {
      throw new WireError("post-apply failure", -32603, {
        evenerErrorInfo: "transcriptDisplayPostApply",
        layout: "mobile",
        applied: hubDefault(5, proposed),
      });
    });
    // The hub applied the write before a follow-up durable step failed and
    // reported what it applied: the save resolves, the flags settle, and the
    // editor stays usable - unlike a lost reply, nothing is uncertain.
    expect(await store.getState().saveDraft()).toEqual(hubDefault(5, proposed));
    expect(store.getState()).toMatchObject({
      saving: false,
      writeUncertain: false,
      draftConflict: false,
      draft: null,
    });
    expect(store.getState().hub.mobile).toEqual(hubDefault(5, proposed));
    expect(drafts.stored()).toBeNull();
    expect(() => store.getState().editDraft("mobile", proposed)).not.toThrow();
  });

  test("a generation change flags the draft stale even when the next hub reports the same revision", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const drafts = memoryDraftStorage<TranscriptDraftCheckpoint>();
    const store = await readyStore(client, { drafts: drafts.storage });
    store.getState().editDraft("mobile", proposed);
    expect(store.getState().draft).toEqual({ layout: "mobile", revision: 2, config: proposed, generation: 1 });
    expect(store.getState().draftConflict).toBe(false);

    // The connection drops and reconnects to a hub whose own numbering happens
    // to confirm the identical revision this draft was composed against.
    store.endReadyGeneration();
    store.beginReadyGeneration();
    await store.getState().refreshHubDefaults();
    expect(store.getState().hub.mobile?.revision).toBe(2);
    expect(store.getState().draftConflict).toBe(true);
  });

  test("an edit continuing the draft keeps its generation stale across a reconnect with equal revisions", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const drafts = memoryDraftStorage<TranscriptDraftCheckpoint>();
    const store = await readyStore(client, { drafts: drafts.storage });
    store.getState().editDraft("mobile", proposed);
    store.endReadyGeneration();
    store.beginReadyGeneration();
    await store.getState().refreshHubDefaults();
    expect(store.getState().draftConflict).toBe(true);

    // An ordinary edit continues the draft: it must keep the generation the
    // draft was composed under and stay in review, not re-stamp itself
    // current against the replacement hub's coincidentally equal revision.
    store.getState().editDraft("mobile", desktopConfig);
    expect(store.getState().draft).toEqual({
      layout: "mobile",
      revision: 2,
      config: desktopConfig,
      generation: 1,
    });
    expect(store.getState().draftConflict).toBe(true);
    await expect(store.getState().saveDraft()).rejects.toThrow(/before saving your changes/);

    // Reviewing the replacement hub's current value is what clears it.
    store.getState().rebaseDraft(2);
    expect(store.getState().draftConflict).toBe(false);
  });

  test("a read that confirms an adopted draft stamps its generation, so an equal-revision reconnect stays in review", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const drafts = memoryDraftStorage<TranscriptDraftCheckpoint>();
    const store = await readyStore(client, { drafts: drafts.storage });
    // An uncertain write whose checkpoint another window replaced
    // mid-flight: the settling read adopts the replacement, which by design
    // carries no generation (a replacement record knows nothing about this
    // store's).
    client.on(patchMethod, () => {
      throw new Error("connection lost");
    });
    await expect(store.getState().saveDraft("mobile", proposed)).rejects.toThrow("connection lost");
    drafts.storage.save({
      id: "other",
      layout: "mobile",
      baseRevision: 2,
      config: mobileConfig,
      writeUncertain: false,
    });
    await store.getState().refreshHubDefaults();
    expect(store.getState().draft).toEqual({
      layout: "mobile",
      revision: 2,
      config: mobileConfig,
      generation: null,
    });

    // An unchanged refresh confirms the adopted draft's base revision is
    // still the current state: that read has earned the generation stamp.
    await store.getState().refreshHubDefaults();
    expect(store.getState().draft?.generation).toBe(1);

    // A reconnect whose numbering restarts on the same revision must leave
    // the draft in review, not stamp the new generation onto it.
    store.endReadyGeneration();
    store.beginReadyGeneration();
    await store.getState().refreshHubDefaults();
    expect(store.getState().draftConflict).toBe(true);
  });

  test("a failed draft restore gates the support-driven refresh until the port recovers", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const drafts = memoryDraftStorage<TranscriptDraftCheckpoint>();
    drafts.failLoad();
    const store = createTranscriptDisplayStore({ client, drafts: drafts.storage });
    expect(store.getState().storageUnavailable).toBe(true);

    // Generation begins before support does. The support-driven refresh must
    // carry the same draft-port recovery gate an explicit one does, or its
    // read would land `loaded` with the checkpoint - possibly an uncertain
    // saved write - still unread on the port, opening the direct-write path
    // with the draft unknown.
    store.beginReadyGeneration();
    store.setSupport("supported");
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
    expect(client.calls.filter((call) => call.method === getMethod)).toHaveLength(0);
    expect(store.getState().loaded).toBe(false);
    await expect(store.getState().patchHubDefault("mobile", mobileConfig)).rejects.toThrow(/unavailable/);

    // Once the port recovers, a refresh re-reads the checkpoint (absent here)
    // and the hub read proceeds.
    drafts.failLoad(false);
    await store.getState().refreshHubDefaults();
    expect(store.getState().loaded).toBe(true);
    expect(store.getState().storageUnavailable).toBe(false);
    expect(store.getState().draft).toBeNull();
  });

  test("a draft-port failure after the hub loaded keeps direct writes gated until the uncertainty is settled", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const drafts = memoryDraftStorage<TranscriptDraftCheckpoint>();
    const store = await readyStore(client, { drafts: drafts.storage });
    // Another window's UNCERTAIN write lands on the port behind this
    // store's back, and the port fails before this store ever reads it: the
    // record is unknown here, and its outcome is unresolved.
    drafts.storage.save({
      id: "other",
      layout: "mobile",
      baseRevision: 2,
      config: mobileConfig,
      writeUncertain: true,
    });
    drafts.failSave();
    expect(() => store.getState().editDraft("mobile", proposed)).toThrow(/save the transcript draft/);
    expect(store.getState()).toMatchObject({
      loaded: true,
      storageUnavailable: true,
      writeUncertain: false,
    });
    // The direct write's gate attempts the port's recovery itself: the
    // reload reveals the other window's uncertain write, and the revealed
    // uncertainty - not the stale storage flag - is what refuses the send.
    client.on(patchMethod, () => patchAnswer("mobile", hubDefault(3, desktopConfig)));
    await expect(store.getState().patchHubDefault("mobile", desktopConfig)).rejects.toThrow(/unavailable/);
    expect(store.getState().hubErrors.mobile).toBe("Hub transcript display settings are unavailable.");
    expect(store.getState().writeUncertain).toBe(true);

    // An authoritative read settles the revealed uncertainty (the hub still
    // holds revision 2, so that write never landed), leaving the other
    // window's proposal as a clean current draft - and the direct path
    // reopens.
    drafts.failSave(false);
    await store.getState().refreshHubDefaults();
    expect(store.getState().storageUnavailable).toBe(false);
    expect(store.getState().writeUncertain).toBe(false);
    expect(store.getState().draft).toEqual({
      layout: "mobile",
      revision: 2,
      config: mobileConfig,
      generation: 1,
    });
    expect(await store.getState().patchHubDefault("mobile", desktopConfig)).toEqual(hubDefault(3, desktopConfig));
  });

  test("a port failure behind a stale unreadable classification still gates reads and writes", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const drafts = memoryDraftStorage<TranscriptDraftCheckpoint>();
    drafts.corrupt();
    const store = await readyStore(client, { drafts: drafts.storage });
    // The corrupt record is classified unreadable: the port read its bytes,
    // the draft is simply absent, and the section stays usable.
    expect(store.getState().draftUnreadable).toBe(true);
    expect(store.getState().loaded).toBe(true);

    // Another window replaces the corrupt record with an uncertain write,
    // and the port fails before this store can read the replacement.
    drafts.storage.save({
      id: "other",
      layout: "mobile",
      baseRevision: 2,
      config: mobileConfig,
      writeUncertain: true,
    });
    drafts.failLoad();
    const readsBefore = client.calls.filter((call) => call.method === getMethod).length;
    await store.getState().refreshHubDefaults();
    // No read may land past the inaccessible port, stale flag or not: the
    // record it classified unreadable may have been replaced by anything.
    expect(client.calls.filter((call) => call.method === getMethod)).toHaveLength(readsBefore);
    client.on(patchMethod, () => patchAnswer("mobile", hubDefault(3, desktopConfig)));
    await expect(store.getState().patchHubDefault("mobile", desktopConfig)).rejects.toThrow(/unavailable/);

    // The stale unreadable classification survives for discard to keep
    // naming the record it classified.
    expect(() => store.getState().discardDraft()).not.toThrow(/unavailable/);

    // Once loading succeeds again, the refresh reveals the replacement's
    // uncertainty and settles it in the same read (the hub's unchanged
    // revision proves that write never landed), and the direct path reopens.
    drafts.failLoad(false);
    await store.getState().refreshHubDefaults();
    expect(store.getState().writeUncertain).toBe(false);
    expect(await store.getState().patchHubDefault("mobile", desktopConfig)).toEqual(hubDefault(3, desktopConfig));
  });

  test("a read cannot settle an uncertainty adopted behind its back", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const drafts = memoryDraftStorage<TranscriptDraftCheckpoint>();
    const store = await readyStore(client, { drafts: drafts.storage });
    store.getState().editDraft("mobile", proposed);

    // A read leaves; then another window persists an UNCERTAIN write, and
    // this store's next edit loses its compare-and-swap to it, adopting it
    // while the read is still outstanding.
    const reply = deferred<TranscriptDisplayDefaults>();
    client.on(getMethod, () => reply.promise);
    const refresh = store.getState().refreshHubDefaults();
    drafts.storage.save({
      id: "other",
      layout: "desktop",
      baseRevision: 3,
      config: desktopConfig,
      writeUncertain: true,
    });
    expect(() => store.getState().editDraft("mobile", desktopConfig)).toThrow(/changed again/);
    expect(store.getState().writeUncertain).toBe(true);

    // That write left for the hub AFTER this read's snapshot, so the read
    // says nothing about its outcome: neither the record on the port nor
    // the published state may be settled by it.
    reply.resolve({
      desktop: toWireDefault(hubDefault(3, desktopConfig)),
      mobile: toWireDefault(hubDefault(2, mobileConfig)),
    });
    await refresh;
    expect(store.getState().writeUncertain).toBe(true);
    expect(drafts.stored()).toMatchObject({ id: "other", writeUncertain: true });

    // A read that postdates the adoption settles it and unblocks editing.
    client.on(getMethod, () => ({
      desktop: toWireDefault(hubDefault(3, desktopConfig)),
      mobile: toWireDefault(hubDefault(2, mobileConfig)),
    }));
    await store.getState().refreshHubDefaults();
    expect(store.getState().writeUncertain).toBe(false);
    expect(drafts.stored()).toMatchObject({ writeUncertain: false });
    expect(() => store.getState().editDraft("desktop", proposed)).not.toThrow();
  });

  test("reset re-reads the persisted draft instead of losing it", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const drafts = memoryDraftStorage<TranscriptDraftCheckpoint>();
    const store = await readyStore(client, { drafts: drafts.storage });
    store.getState().editDraft("mobile", proposed);
    client.on(patchMethod, () => {
      throw new Error("connection lost");
    });
    await expect(store.getState().saveDraft()).rejects.toThrow("connection lost");
    expect(store.getState().writeUncertain).toBe(true);

    // A lifecycle reset re-reads the checkpoint rather than wiping it: the
    // unresolved write survives reset with its uncertainty, so nothing may
    // compose past the still-classified record. The re-read is
    // identity-aware, so the same record keeps the generation it was
    // composed under.
    store.reset();
    expect(store.getState().draft).toEqual({
      layout: "mobile",
      revision: 2,
      config: proposed,
      generation: 1,
    });
    expect(store.getState().writeUncertain).toBe(true);
    expect(store.getState().storageUnavailable).toBe(false);
    expect(() => store.getState().editDraft("mobile", proposed)).toThrow(/unavailable/);

    // Reuse settles the retained uncertainty under a fresh generation and
    // reopens the editor.
    store.setSupport("supported");
    store.beginReadyGeneration();
    await store.getState().refreshHubDefaults();
    expect(store.getState().writeUncertain).toBe(false);
    expect(drafts.stored()).toMatchObject({ writeUncertain: false });
    expect(() => store.getState().editDraft("mobile", proposed)).not.toThrow();
  });

  test("reset keeps an unreadable classification discardable through a port failure", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const drafts = memoryDraftStorage<TranscriptDraftCheckpoint>();
    drafts.corrupt();
    const store = await readyStore(client, { drafts: drafts.storage });
    expect(store.getState().draftUnreadable).toBe(true);

    // A temporary load failure strikes. The reset's re-read fails plainly,
    // so the unreadable classification must survive it exactly as a
    // recovery reload's would: discard is the one fix for an unreadable
    // record, and the repository still holds its identity.
    drafts.failLoad();
    store.reset();
    expect(store.getState().storageUnavailable).toBe(true);
    expect(store.getState().draftUnreadable).toBe(true);
    expect(() => store.getState().discardDraft()).not.toThrow(/unavailable/);
    expect(drafts.stored()).toBeNull();
    expect(store.getState().draftUnreadable).toBe(false);
    expect(store.getState().storageUnavailable).toBe(false);
  });

  test("reset preserves a persisted draft's generation against an equal-revision replacement hub", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const drafts = memoryDraftStorage<TranscriptDraftCheckpoint>();
    const store = await readyStore(client, { drafts: drafts.storage });
    store.getState().editDraft("mobile", proposed);
    expect(store.getState().draft?.generation).toBe(1);

    // The lifecycle reset re-reads the SAME record: the generation stamp is
    // this store's own claim about it, and wiping it would let a
    // replacement hub's coincidentally equal revision read the draft as
    // current - exactly what the generation guard exists to prevent.
    store.reset();
    expect(store.getState().draft).toEqual({
      layout: "mobile",
      revision: 2,
      config: proposed,
      generation: 1,
    });

    // The replacement hub restarts numbering on the same revision: the
    // draft stays in review and saving refuses.
    store.setSupport("supported");
    store.beginReadyGeneration();
    await store.getState().refreshHubDefaults();
    expect(store.getState().draftConflict).toBe(true);
    await expect(store.getState().saveDraft()).rejects.toThrow(/before saving your changes/);
  });

  test("a pre-ready restore is stamped by the first authoritative payload; a pre-generation relay stamps nothing", async () => {
    const drafts = memoryDraftStorage<TranscriptDraftCheckpoint>({
      id: "d1",
      layout: "mobile",
      baseRevision: 2,
      config: proposed,
      writeUncertain: false,
    });
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const store = createTranscriptDisplayStore({ client, drafts: drafts.storage });
    expect(store.getState().draft).toEqual({ layout: "mobile", revision: 2, config: proposed, generation: null });

    // A relayed change before any ready generation began is fenced by the
    // live-hub gate and must not stamp a fake generation onto the draft.
    store.getState().applyHubChange({ layout: "mobile", revision: 2, config: mobileConfig });
    expect(store.getState().draft?.generation).toBeNull();
    expect(store.getState().hub).toEqual({});

    store.setSupport("supported");
    store.beginReadyGeneration();
    await store.getState().refreshHubDefaults();
    expect(store.getState().draft).toEqual({ layout: "mobile", revision: 2, config: proposed, generation: 1 });
    expect(store.getState().draftConflict).toBe(false);
  });

  test("an unreadable stored record keeps the hub usable, exposes draftUnreadable, and discard clears it", async () => {
    // The shape the native host wrote before layouts were recorded - and any
    // other value this build cannot read.
    const legacy = memoryDraftStorage<TranscriptDraftCheckpoint>({
      id: "d0",
      baseRevision: 2,
      config: proposed,
      writeUncertain: false,
    });
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const store = await readyStore(client, { drafts: legacy.storage });
    expect(store.getState().draft).toBeNull();
    expect(store.getState().loaded).toBe(true);
    expect(store.getState().hub.mobile).toEqual(hubDefault(2, mobileConfig));
    expect(store.getState().storageUnavailable).toBe(true);
    expect(store.getState().draftUnreadable).toBe(true);
    expect(store.getState().draftError).toMatch(/restore/);

    store.getState().discardDraft();
    expect(legacy.stored()).toBeNull();
    expect(store.getState().storageUnavailable).toBe(false);
    expect(store.getState().draftUnreadable).toBe(false);

    expect(() => store.getState().editDraft("mobile", proposed)).not.toThrow();
    expect(store.getState().draft?.config).toEqual(proposed);
  });

  test("discard honors compare-and-remove for readable and unreadable records; replacements are adopted", async () => {
    // An unreadable record another writer replaced before the discard.
    const legacy = memoryDraftStorage<TranscriptDraftCheckpoint>({
      id: "d0",
      baseRevision: 2,
      config: proposed,
      writeUncertain: false,
    });
    const unreadableStore = await readyStore(serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig)), {
      drafts: legacy.storage,
    });
    expect(unreadableStore.getState().draftUnreadable).toBe(true);
    const newer: TranscriptDraftCheckpoint = {
      id: "d1",
      layout: "mobile",
      baseRevision: 2,
      config: proposed,
      writeUncertain: false,
    };
    legacy.storage.save(newer);
    unreadableStore.getState().discardDraft();
    expect(legacy.stored()).toEqual(newer);
    expect(unreadableStore.getState().draftUnreadable).toBe(false);
    expect(unreadableStore.getState().storageUnavailable).toBe(false);
    expect(unreadableStore.getState().draft).toMatchObject({ layout: "mobile", revision: 2, config: proposed });

    // A readable record another writer replaced before the discard. The
    // discard must name the record this store classified, not a fresh reload.
    const readable = memoryDraftStorage<TranscriptDraftCheckpoint>({
      id: "d0",
      layout: "mobile",
      baseRevision: 2,
      config: proposed,
      writeUncertain: false,
    });
    const readableStore = await readyStore(serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig)), {
      drafts: readable.storage,
    });
    expect(readableStore.getState().draft).toEqual({
      layout: "mobile",
      revision: 2,
      config: proposed,
      generation: 1,
    });
    const replacement: TranscriptDraftCheckpoint = {
      id: "d1",
      layout: "desktop",
      baseRevision: 3,
      config: desktopConfig,
      writeUncertain: false,
    };
    readable.storage.save(replacement);
    readableStore.getState().discardDraft();
    expect(readable.stored()).toEqual(replacement);
    expect(readableStore.getState().draft).toMatchObject({
      layout: "desktop",
      revision: 3,
      config: desktopConfig,
    });
    expect(readableStore.getState().draft?.generation).toBeNull();

    // A replacement landing while an uncertain write is being settled by a
    // read is adopted, never overwritten with the stale checkpoint.
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const drafts = memoryDraftStorage<TranscriptDraftCheckpoint>();
    const settleStore = await readyStore(client, { drafts: drafts.storage });
    client.on(patchMethod, () => {
      throw new Error("connection lost");
    });
    await expect(settleStore.getState().saveDraft("mobile", proposed)).rejects.toThrow("connection lost");
    expect(settleStore.getState().writeUncertain).toBe(true);
    const other: TranscriptDraftCheckpoint = {
      id: "other",
      layout: "desktop",
      baseRevision: 3,
      config: desktopConfig,
      writeUncertain: false,
    };
    drafts.storage.save(other);
    await settleStore.getState().refreshHubDefaults();
    expect(settleStore.getState().writeUncertain).toBe(false);
    expect(drafts.stored()).toEqual(other);
    expect(settleStore.getState().draft).toMatchObject({
      layout: "desktop",
      revision: 3,
      config: desktopConfig,
    });
  });

  test("rebase requires reviewing the current revision and then allows save", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const drafts = memoryDraftStorage<TranscriptDraftCheckpoint>();
    const store = await readyStore(client, { drafts: drafts.storage });
    store.getState().editDraft("mobile", proposed);
    client.emitNotification({
      method: changedMethod,
      params: { layout: "mobile", revision: 4, config: toWireConfig(desktopConfig) },
    });
    expect(store.getState().hub.mobile).toEqual(hubDefault(4, desktopConfig));
    expect(store.getState().draftConflict).toBe(true);
    expect(() => store.getState().rebaseDraft(3)).toThrow(/changed again/);
    store.getState().rebaseDraft(4);
    expect(store.getState().draft).toEqual({ layout: "mobile", revision: 4, config: proposed, generation: 1 });
    expect(store.getState().draftConflict).toBe(false);

    client.on(patchMethod, () => patchAnswer("mobile", hubDefault(5, proposed)));
    expect(await store.getState().saveDraft()).toEqual(hubDefault(5, proposed));
    expect(store.getState().draft).toBeNull();
    expect(drafts.stored()).toBeNull();
  });

  test("port save, load, remove, and replace failures set the right flags without dropping an uncertain checkpoint", async () => {
    // A save that throws marks storage unavailable with the save-failure copy.
    const failing = memoryDraftStorage<TranscriptDraftCheckpoint>();
    const failingStore = await readyStore(serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig)), {
      drafts: failing.storage,
    });
    failing.failSave();
    expect(() => failingStore.getState().editDraft("mobile", proposed)).toThrow(/save the transcript draft locally/);
    expect(failingStore.getState().storageUnavailable).toBe(true);
    expect(failingStore.getState().draftError).toMatch(/save the transcript draft locally/);

    // A load that throws at restore marks storage unavailable with the
    // restore-failure copy, and the hub read waits: an edit cannot compose
    // against a confirmed payload with the draft unknown.
    const flaky = memoryDraftStorage<TranscriptDraftCheckpoint>();
    flaky.failLoad();
    const flakyClient = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const flakyStore = createTranscriptDisplayStore({ client: flakyClient, drafts: flaky.storage });
    expect(flakyStore.getState().storageUnavailable).toBe(true);
    expect(flakyStore.getState().draftError).toMatch(/restore/);
    flakyStore.setSupport("supported");
    flakyStore.beginReadyGeneration();
    await flakyStore.getState().refreshHubDefaults();
    expect(flakyStore.getState().loaded).toBe(false);
    expect(flakyClient.calls.filter((call) => call.method === getMethod)).toHaveLength(0);

    // A remove that throws refuses the discard and keeps the record.
    const throwing = memoryDraftStorage<TranscriptDraftCheckpoint>();
    const discardStore = await readyStore(serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig)), {
      drafts: {
        ...throwing.storage,
        removeIf: () => {
          throw new Error("disk unavailable");
        },
      },
    });
    discardStore.getState().editDraft("mobile", proposed);
    expect(() => discardStore.getState().discardDraft()).toThrow(/discard the transcript draft locally/);
    expect(discardStore.getState().storageUnavailable).toBe(true);
    expect(discardStore.getState().draftError).toMatch(/discard the transcript draft locally/);
    expect(throwing.stored()).not.toBeNull();

    // A replace that throws while an uncertain write is being settled keeps
    // the uncertainty: the checkpoint still says writeUncertain on disk.
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const drafts = memoryDraftStorage<TranscriptDraftCheckpoint>();
    const store = await readyStore(client, { drafts: drafts.storage });
    store.getState().editDraft("mobile", proposed);
    client.on(patchMethod, () => {
      throw new Error("connection lost");
    });
    await expect(store.getState().saveDraft()).rejects.toThrow("connection lost");
    expect(store.getState().writeUncertain).toBe(true);
    drafts.failReplace();
    await store.getState().refreshHubDefaults();
    expect(store.getState().storageUnavailable).toBe(true);
    expect(store.getState().draftError).toMatch(/save the transcript draft locally/);
    expect(store.getState().writeUncertain).toBe(true);
    expect(drafts.stored()).toMatchObject({ writeUncertain: true });
  });

  test("without a draft port the ephemeral fallback does not manufacture a concurrent replacement", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    // No drafts port: the documented in-memory fallback, which retains its
    // checkpoint for the instance and compares its swaps against that value
    // - only this one repository instance ever touches it, so a refusal can
    // only mean something in this instance really did replace the record.
    const store = await readyStore(client);
    client.on(patchMethod, () => {
      throw conflictError("mobile", hubDefault(5, desktopConfig));
    });
    await expect(store.getState().saveDraft("mobile", proposed)).rejects.toThrow("revision conflict");
    expect(store.getState()).toMatchObject({ saving: false, writeUncertain: false, draftConflict: true });
    // The proposal must still be here for review, not silently wiped to null
    // by a phantom concurrent writer only real storage could have.
    expect(store.getState().draft).not.toBeNull();
    expect(store.getState().draft?.config).toEqual(proposed);
  });

  test("the in-memory fallback keeps drafts and their uncertainty coherent across reset", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const store = await readyStore(client);
    store.getState().editDraft("mobile", proposed);
    client.on(patchMethod, () => {
      throw new Error("connection lost");
    });
    await expect(store.getState().saveDraft()).rejects.toThrow("connection lost");
    expect(store.getState().writeUncertain).toBe(true);

    // The fallback retains its checkpoint for the instance: reset re-reads
    // the proposal with its unresolved outcome instead of wiping both.
    store.reset();
    expect(store.getState().draft).toEqual({ layout: "mobile", revision: 2, config: proposed, generation: 1 });
    expect(store.getState().writeUncertain).toBe(true);
    expect(() => store.getState().editDraft("mobile", proposed)).toThrow(/unavailable/);

    // A refresh settles the retained uncertainty the ordinary way.
    store.setSupport("supported");
    store.beginReadyGeneration();
    await store.getState().refreshHubDefaults();
    expect(store.getState().writeUncertain).toBe(false);
    expect(() => store.getState().editDraft("mobile", proposed)).not.toThrow();
  });

  test("a record that becomes unreadable while a write is uncertain clears the stale uncertainty, unblocking discard", async () => {
    const drafts = memoryDraftStorage<TranscriptDraftCheckpoint>();
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const store = await readyStore(client, { drafts: drafts.storage });
    client.on(patchMethod, () => {
      throw new Error("connection lost");
    });
    await expect(store.getState().saveDraft("mobile", proposed)).rejects.toThrow("connection lost");
    expect(store.getState().writeUncertain).toBe(true);

    // The stored checkpoint becomes unreadable (a newer app version wrote a
    // shape this build cannot decode) while the write's outcome is still
    // unknown.
    drafts.corrupt();
    await store.getState().refreshHubDefaults();

    expect(store.getState()).toMatchObject({ storageUnavailable: true, draftUnreadable: true });
    // The record is unreadable - there is nothing left to be "uncertain"
    // about and nothing to review a "conflict" against. Both must clear, or
    // discardDraft (the one recovery this state allows) is refused too.
    expect(store.getState().writeUncertain).toBe(false);
    expect(store.getState().draftConflict).toBe(false);
    expect(store.getState().draft).toBeNull();
    expect(() => store.getState().discardDraft()).not.toThrow();
    expect(store.getState()).toMatchObject({ storageUnavailable: false, draftUnreadable: false });
  });
});
