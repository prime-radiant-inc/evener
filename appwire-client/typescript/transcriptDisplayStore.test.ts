import { afterEach, describe, expect, test, vi } from "vitest";
import { WireError } from "./errors";
import { deferred } from "./testing/deferred";
import { memoryDraftStorage } from "./testing/draftStorage";
import { FakeClient } from "./testing/fakeClient";
import {
  type HubTranscriptDisplayDefault,
  makeTranscriptDisplayConfig,
  shippedDefault,
  type TranscriptDisplayConfigV1,
  toWireConfig,
  toWireDefault,
} from "./transcriptDisplayConfig";
import {
  createTranscriptDisplayStore,
  type TranscriptDisplayStore,
  type TranscriptDisplayStoreDeps,
  type TranscriptDraftCheckpoint,
  type TranscriptDraftStorage,
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

function patchAnswer(layout: "desktop" | "mobile", value: HubTranscriptDisplayDefault) {
  return { layout, revision: value.revision, config: toWireConfig(value.config) };
}

async function readyStore(client: FakeClient, deps: Partial<TranscriptDisplayStoreDeps> = {}) {
  const store = createTranscriptDisplayStore({ client, ...deps });
  store.setSupport("supported");
  store.beginReadyGeneration();
  await store.getState().refreshHubDefaults();
  return store;
}

afterEach(() => {
  vi.useRealTimers();
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
    });
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

  test("a changed notification applies a newer revision and ignores a stale or equal one", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const store = await readyStore(client);
    client.emitNotification({
      method: changedMethod,
      params: { layout: "mobile", revision: 2, config: toWireConfig(proposed) },
    });
    expect(store.getState().hub.mobile).toEqual(hubDefault(2, mobileConfig));
    client.emitNotification({
      method: changedMethod,
      params: { layout: "mobile", revision: 1, config: toWireConfig(proposed) },
    });
    expect(store.getState().hub.mobile).toEqual(hubDefault(2, mobileConfig));
    client.emitNotification({
      method: changedMethod,
      params: { layout: "mobile", revision: 5, config: toWireConfig(proposed) },
    });
    expect(store.getState().hub.mobile).toEqual(hubDefault(5, proposed));
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

  test("a notification landing before the generation's first read confirms is not lost: the read follows up once", async () => {
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

  test("a new generation's first read applies a lower revision: numbering is the hub's", async () => {
    const client = serving(hubDefault(7, desktopConfig), hubDefault(7, mobileConfig));
    const store = await readyStore(client);
    store.endReadyGeneration();
    expect(store.getState().loaded).toBe(false);
    // The rows keep presenting while disconnected.
    expect(store.getState().hub.desktop?.revision).toBe(7);
    client.on(getMethod, () => ({
      desktop: toWireDefault(hubDefault(1, proposed)),
      mobile: toWireDefault(hubDefault(1, proposed)),
    }));
    store.beginReadyGeneration();
    await store.getState().refreshHubDefaults();
    expect(store.getState().hub.desktop).toEqual(hubDefault(1, proposed));
    // Every layer of that first payload is the new hub's, not just the one
    // that happened to be applied first.
    expect(store.getState().hub.mobile).toEqual(hubDefault(1, proposed));
    expect(store.getState().loaded).toBe(true);
  });
  test("a change arriving while the first read FAILS survives to the next successful read", async () => {
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

    // A change lands while nothing is confirmed. It is dropped - its revision
    // predates whatever a read will confirm - and remembered.
    client.emitNotification({
      method: changedMethod,
      params: { layout: "mobile", revision: 9, config: toWireConfig(proposed) },
    });
    expect(store.getState().hub.mobile).toBeUndefined();

    // The retry confirms, and the remembered change is honoured by it: one
    // follow-up read, so nothing the response predates is lost.
    client.on(getMethod, () => ({
      desktop: toWireDefault(hubDefault(3, desktopConfig)),
      mobile: toWireDefault(hubDefault(9, proposed)),
    }));
    await store.getState().refreshHubDefaults();
    expect(store.getState().loaded).toBe(true);
    await vi.waitFor(() => expect(store.getState().hub.mobile).toEqual(hubDefault(9, proposed)));
  });

  test("a change relayed before the first read is not lost: the read follows up once", async () => {
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

    // Relayed by the host rather than delivered to this store's own
    // subscription - the same cargo, and the same problem: the in-flight
    // read's response may PREDATE it.
    store.getState().applyHubChange({ layout: "mobile", revision: 9, config: proposed });

    first.resolve({
      desktop: toWireDefault(hubDefault(3, desktopConfig)),
      mobile: toWireDefault(hubDefault(2, mobileConfig)),
    });
    await refresh;
    await vi.waitFor(() => expect(store.getState().hub.mobile).toEqual(hubDefault(9, proposed)));
    expect(reads).toBe(2);
  });

  test("a valid newer broadcast clears a stale store-wide error", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const store = await readyStore(client);
    client.on(patchMethod, () => {
      throw new Error("hub unreachable");
    });
    await expect(store.getState().patchHubDefault("mobile", proposed)).rejects.toThrow("hub unreachable");
    expect(store.getState().hubError).toBe("hub unreachable");

    // The hub then confirms a newer value by itself. The retry notice describes
    // a failure the hub has since moved past.
    client.emitNotification({
      method: changedMethod,
      params: { layout: "mobile", revision: 5, config: toWireConfig(proposed) },
    });
    expect(store.getState().hub.mobile).toEqual(hubDefault(5, proposed));
    expect(store.getState().hubError).toBeNull();
    // The layout's own write error is the write's, not the hub's, and stays.
    expect(store.getState().hubErrors.mobile).toBe("hub unreachable");
  });

  test("a STALE broadcast leaves the store-wide error alone", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(5, mobileConfig));
    const store = await readyStore(client);
    client.on(patchMethod, () => {
      throw new Error("hub unreachable");
    });
    await expect(store.getState().patchHubDefault("mobile", proposed)).rejects.toThrow("hub unreachable");
    expect(store.getState().hubError).toBe("hub unreachable");

    // Older than what is confirmed: it moves nothing, so it clears nothing.
    client.emitNotification({
      method: changedMethod,
      params: { layout: "mobile", revision: 4, config: toWireConfig(proposed) },
    });
    expect(store.getState().hub.mobile).toEqual(hubDefault(5, mobileConfig));
    expect(store.getState().hubError).toBe("hub unreachable");
  });

  test("a broadcast relayed before the generation's first read does not make that read stale", async () => {
    const client = serving(hubDefault(7, desktopConfig), hubDefault(7, mobileConfig));
    const store = await readyStore(client);
    store.endReadyGeneration();
    client.on(getMethod, () => ({
      desktop: toWireDefault(hubDefault(1, proposed)),
      mobile: toWireDefault(hubDefault(1, proposed)),
    }));
    store.beginReadyGeneration();
    // The host relays a change it holds from before this generation's read
    // went out. It is not this generation's confirmation, so the read that
    // follows is still the authoritative first payload.
    store.getState().applyHubChange({ layout: "desktop", revision: 9, config: desktopConfig });

    await store.getState().refreshHubDefaults();
    expect(store.getState().hub.desktop).toEqual(hubDefault(1, proposed));
    expect(store.getState().hub.mobile).toEqual(hubDefault(1, proposed));
  });
});

describe("the direct write", () => {
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

  test("a conflict reply landing after the generation ended applies nothing", async () => {
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
    await write;
    // The canonical belongs to the hub this write was fenced out of. Landing
    // it would repopulate a retired payload AND mark it confirmed, so the next
    // hub's refresh - a restart numbering from 1 - is eaten by the stale guard.
    expect(store.getState().hub.mobile).toEqual(hubDefault(2, mobileConfig));
    expect(store.getState().loaded).toBe(false);
  });

  test("a later checkpointed write on the same layout supersedes an earlier direct write's reply", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const drafts = memoryDraftStorage<TranscriptDraftCheckpoint>();
    const store = await readyStore(client, { drafts: drafts.storage });
    const first = deferred<TranscriptDisplayPatchResponse>();
    const second = deferred<TranscriptDisplayPatchResponse>();
    let sends = 0;
    client.on(patchMethod, () => (++sends === 1 ? first.promise : second.promise));

    const direct = store.getState().patchHubDefault("mobile", proposed);
    await vi.waitFor(() => expect(sends).toBe(1));
    const checkpointed = store.getState().saveDraft("mobile", mobileConfig);
    await vi.waitFor(() => expect(sends).toBe(2));

    // The direct write's reply is now the OLDER write on this layout: a later
    // write took the layout, so this reply may not land its value.
    first.resolve(patchAnswer("mobile", hubDefault(3, proposed)));
    await direct;
    expect(store.getState().hub.mobile).toEqual(hubDefault(2, mobileConfig));

    second.resolve(patchAnswer("mobile", hubDefault(3, mobileConfig)));
    await checkpointed;
    expect(store.getState().hub.mobile).toEqual(hubDefault(3, mobileConfig));
  });

  test("a preview survives a transient disconnect and is dropped when the hub identity is", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const store = await readyStore(client);
    const reply = deferred<TranscriptDisplayPatchResponse>();
    client.on(patchMethod, () => reply.promise);
    const write = store.getState().patchHubDefault("mobile", proposed);
    await vi.waitFor(() => expect(store.getState().drafts.mobile).toEqual(proposed));

    // Retirement alone does not clear it: the value the user asked for may well
    // have been applied, and nothing authoritative has said otherwise yet.
    store.endReadyGeneration();
    expect(store.getState().drafts.mobile).toEqual(proposed);

    // (What the NEXT authoritative read does to it is decided by whether that
    // payload matches the preview - the two tests above own that rule.)

    // A DIFFERENT hub: the preview describes a write sent to the old one and
    // says nothing about this one's value.
    store.detachHub();
    expect(store.getState().drafts.mobile).toBeUndefined();
    client.on(getMethod, () => ({
      desktop: toWireDefault(hubDefault(1, proposed)),
      mobile: toWireDefault(hubDefault(1, mobileConfig)),
    }));
    store.beginReadyGeneration();
    await store.getState().refreshHubDefaults();
    expect(store.getState().drafts.mobile).toBeUndefined();

    reply.resolve(patchAnswer("mobile", hubDefault(3, proposed)));
    await write;
    expect(store.getState().hub.mobile).toEqual(hubDefault(1, mobileConfig));
  });

  test("an unsupported transition drops the preview with the rest of the hub identity", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const store = await readyStore(client);
    client.on(patchMethod, () => deferred<TranscriptDisplayPatchResponse>().promise);
    void store.getState().patchHubDefault("mobile", proposed);
    await vi.waitFor(() => expect(store.getState().drafts.mobile).toEqual(proposed));

    store.setSupport("unsupported");
    expect(store.getState().drafts.mobile).toBeUndefined();
  });

  test("a direct write is refused while a checkpointed write owns the layer", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const drafts = memoryDraftStorage<TranscriptDraftCheckpoint>();
    const store = await readyStore(client, { drafts: drafts.storage });
    const reply = deferred<TranscriptDisplayPatchResponse>();
    client.on(patchMethod, () => reply.promise);
    const save = store.getState().saveDraft("mobile", proposed);
    await vi.waitFor(() => expect(store.getState().saving).toBe(true));

    // The checkpointed write holds the layer. A direct write would take the
    // layer's token, fence the save's own reply out, and leave it saving
    // forever.
    await expect(store.getState().patchHubDefault("mobile", mobileConfig)).rejects.toThrow(/unavailable/);

    reply.resolve(patchAnswer("mobile", hubDefault(3, proposed)));
    await save;
    expect(store.getState()).toMatchObject({ saving: false, writeUncertain: false });
  });

  test("an older save's late reply does not clear a newer save's saving flag", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const drafts = memoryDraftStorage<TranscriptDraftCheckpoint>();
    const store = await readyStore(client, { drafts: drafts.storage });
    const first = deferred<TranscriptDisplayPatchResponse>();
    const second = deferred<TranscriptDisplayPatchResponse>();
    let sends = 0;
    client.on(patchMethod, () => (++sends === 1 ? first.promise : second.promise));

    const one = store.getState().saveDraft("mobile", proposed);
    await vi.waitFor(() => expect(sends).toBe(1));

    // A transient disconnect retires the payload, which frees the editor while
    // the first request is STILL OUT - that is what lets a second save start.
    store.endReadyGeneration();
    expect(store.getState().saving).toBe(false);
    store.beginReadyGeneration();
    await store.getState().refreshHubDefaults();
    expect(store.getState()).toMatchObject({ saving: false, writeUncertain: false });
    store.getState().rebaseDraft(2);

    const two = store.getState().saveDraft("mobile", mobileConfig);
    await vi.waitFor(() => expect(sends).toBe(2));
    expect(store.getState().saving).toBe(true);

    // The first write's reply finally lands. It was superseded, not fenced by a
    // support flip, so it may not report the write the editor IS waiting on as
    // finished.
    first.resolve(patchAnswer("mobile", hubDefault(3, proposed)));
    await one;
    expect(store.getState().saving).toBe(true);

    second.resolve(patchAnswer("mobile", hubDefault(3, mobileConfig)));
    await two;
    expect(store.getState().saving).toBe(false);
  });

  test("a rejection arriving after support went unknown still clears saving", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const drafts = memoryDraftStorage<TranscriptDraftCheckpoint>();
    const store = await readyStore(client, { drafts: drafts.storage });
    const reply = deferred<TranscriptDisplayPatchResponse>();
    client.on(patchMethod, () => reply.promise);
    const save = store.getState().saveDraft("mobile", proposed);
    await vi.waitFor(() => expect(store.getState().saving).toBe(true));

    // The transient-disconnect window: support goes unknown, which keeps the
    // payload and the in-flight work but makes isSupported() false - so the
    // reply is fenced WITHOUT any retirement having published saving false.
    store.setSupport("unknown");
    expect(store.getState().saving).toBe(true);

    reply.reject(new Error("connection lost"));
    await expect(save).rejects.toThrow("connection lost");

    // Whatever fenced it, the editor may not be left mid-write: nothing else
    // will ever settle this one.
    expect(store.getState().saving).toBe(false);
    expect(store.getState().writeUncertain).toBe(true);
  });

  test("a superseded checkpointed reply always clears saving", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const drafts = memoryDraftStorage<TranscriptDraftCheckpoint>();
    const store = await readyStore(client, { drafts: drafts.storage });
    const reply = deferred<TranscriptDisplayPatchResponse>();
    client.on(patchMethod, () => reply.promise);
    const save = store.getState().saveDraft("mobile", proposed);
    await vi.waitFor(() => expect(store.getState().saving).toBe(true));

    // Whatever fenced this reply out, the editor may not be left mid-write.
    store.endReadyGeneration();
    reply.resolve(patchAnswer("mobile", hubDefault(3, proposed)));
    await save;
    expect(store.getState().saving).toBe(false);
  });

  test("a reply the hub has already moved past drops the preview and keeps the newer value", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const store = await readyStore(client);
    const reply = deferred<TranscriptDisplayPatchResponse>();
    client.on(patchMethod, () => reply.promise);
    const write = store.getState().patchHubDefault("mobile", proposed);
    await vi.waitFor(() => expect(store.getState().drafts.mobile).toEqual(proposed));

    // Someone else moved the layer on while this write was out.
    client.emitNotification({
      method: changedMethod,
      params: { layout: "mobile", revision: 7, config: toWireConfig(mobileConfig) },
    });
    reply.resolve(patchAnswer("mobile", hubDefault(3, proposed)));
    await expect(write).rejects.toThrow(/malformed/);

    // The newer value stands, and the preview of a write it overtook must not
    // keep sitting on top of it.
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

    // A transient disconnect fences the reply. The preview stays up, because
    // the hub may well have applied it.
    store.endReadyGeneration();
    reply.resolve(patchAnswer("mobile", hubDefault(3, proposed)));
    await write;
    expect(store.getState().drafts.mobile).toEqual(proposed);

    // On reconnect the hub confirms a NEWER revision holding something else:
    // the layer settled at a value this preview contradicts, so the guess must
    // not keep sitting on top of it.
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

    // The hub goes away with the reply still out, then comes back RESTARTED,
    // numbering from its own 3. The preview was composed against revision 7 of
    // a hub that no longer exists, so revision is no guide at all here.
    store.endReadyGeneration();
    reply.resolve(patchAnswer("mobile", hubDefault(8, proposed)));
    await write;
    expect(store.getState().drafts.mobile).toEqual(proposed);

    client.on(getMethod, () => ({
      desktop: toWireDefault(hubDefault(3, desktopConfig)),
      mobile: toWireDefault(hubDefault(3, mobileConfig)),
    }));
    store.beginReadyGeneration();
    await store.getState().refreshHubDefaults();
    expect(store.getState().hub.mobile).toEqual(hubDefault(3, mobileConfig));
    expect(store.getState().drafts.mobile).toBeUndefined();
  });

  test("a new generation's EQUAL-revision read still invalidates a contradicted preview", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const store = await readyStore(client);
    const reply = deferred<TranscriptDisplayPatchResponse>();
    client.on(patchMethod, () => reply.promise);
    const write = store.getState().patchHubDefault("mobile", proposed);
    await vi.waitFor(() => expect(store.getState().drafts.mobile).toEqual(proposed));

    store.endReadyGeneration();
    reply.resolve(patchAnswer("mobile", hubDefault(3, proposed)));
    await write;
    expect(store.getState().drafts.mobile).toEqual(proposed);

    // The new generation's first authoritative payload numbers the SAME as the
    // preview's base but holds something else. The number says nothing across a
    // generation boundary; the configuration is what decides.
    client.on(getMethod, () => ({
      desktop: toWireDefault(hubDefault(3, desktopConfig)),
      mobile: toWireDefault(hubDefault(2, desktopConfig)),
    }));
    store.beginReadyGeneration();
    await store.getState().refreshHubDefaults();
    expect(store.getState().hub.mobile).toEqual(hubDefault(2, desktopConfig));
    expect(store.getState().drafts.mobile).toBeUndefined();
  });

  test("a newer confirmed payload MATCHING a stranded preview leaves it for the host to report", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const store = await readyStore(client);
    const reply = deferred<TranscriptDisplayPatchResponse>();
    client.on(patchMethod, () => reply.promise);
    const write = store.getState().patchHubDefault("mobile", proposed);
    await vi.waitFor(() => expect(store.getState().drafts.mobile).toEqual(proposed));
    store.endReadyGeneration();
    reply.resolve(patchAnswer("mobile", hubDefault(3, proposed)));
    await write;

    // The hub holds what the write asked for, but this write's reply never
    // landed: the host still has an unacknowledged write to tell the user
    // about, and the preview is what it tells them about.
    client.on(getMethod, () => ({
      desktop: toWireDefault(hubDefault(3, desktopConfig)),
      mobile: toWireDefault(hubDefault(3, proposed)),
    }));
    store.beginReadyGeneration();
    await store.getState().refreshHubDefaults();
    expect(store.getState().hub.mobile).toEqual(hubDefault(3, proposed));
    expect(store.getState().drafts.mobile).toEqual(proposed);
  });

  test("a malformed success changes no hub state and reports it", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const store = await readyStore(client);
    client.on(patchMethod, () => ({ layout: "mobile", revision: 9, config: toWireConfig(proposed) }));
    await expect(store.getState().patchHubDefault("mobile", proposed)).rejects.toThrow(/malformed/);
    expect(store.getState().hub.mobile).toEqual(hubDefault(2, mobileConfig));
    expect(store.getState().hubErrors.mobile).toMatch(/malformed/);
  });

  test("refuses without a confirmed, supported hub", async () => {
    const store = createTranscriptDisplayStore({ client: new FakeClient("ready") });
    await expect(store.getState().patchHubDefault("mobile", proposed)).rejects.toThrow(/unavailable/);
    expect(store.getState().hubErrors.mobile).toMatch(/unavailable/);
  });
});

describe("the checkpointed draft editor", () => {
  test("editDraft persists the proposal; saveDraft checkpoints before the request leaves and releases it on confirmation", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const drafts = memoryDraftStorage<TranscriptDraftCheckpoint>();
    const store = await readyStore(client, { drafts: drafts.storage });
    store.getState().editDraft("mobile", proposed);
    expect(store.getState().draft).toEqual({ layout: "mobile", revision: 2, config: proposed });
    expect(drafts.calls).toEqual(["load", "save:settled"]);

    const reply = deferred<TranscriptDisplayPatchResponse>();
    client.on(patchMethod, () => reply.promise);
    const save = store.getState().saveDraft();
    await vi.waitFor(() => expect(client.calls.filter((call) => call.method === patchMethod)).toHaveLength(1));
    expect(drafts.calls.at(-1)).toBe("save:uncertain");
    expect(store.getState().saving).toBe(true);
    reply.resolve(patchAnswer("mobile", hubDefault(3, proposed)));
    expect(await save).toEqual(hubDefault(3, proposed));
    expect(store.getState()).toMatchObject({ saving: false, writeUncertain: false, draft: null, draftConflict: false });
    expect(drafts.calls.at(-1)).toBe("removeIf");
    expect(drafts.stored()).toBeNull();
    expect(store.getState().hub.mobile).toEqual(hubDefault(3, proposed));
  });

  test("a lost reply leaves the write uncertain and edits blocked until an authoritative read settles it", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const drafts = memoryDraftStorage<TranscriptDraftCheckpoint>();
    const store = await readyStore(client, { drafts: drafts.storage });
    client.on(patchMethod, () => {
      throw new Error("connection lost");
    });
    await expect(store.getState().saveDraft("mobile", proposed)).rejects.toThrow("connection lost");
    expect(store.getState()).toMatchObject({ saving: false, writeUncertain: true, draftConflict: true });
    expect(drafts.stored()?.writeUncertain).toBe(true);
    expect(() => store.getState().editDraft("mobile", mobileConfig)).toThrow(/unavailable/);
    await store.getState().refreshHubDefaults();
    expect(store.getState().writeUncertain).toBe(false);
    expect(drafts.stored()?.writeUncertain).toBe(false);
  });

  test("saveDraft refuses a reply that is not this write's own outcome", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const drafts = memoryDraftStorage<TranscriptDraftCheckpoint>();
    const store = await readyStore(client, { drafts: drafts.storage });
    // Structurally a valid reply for this layout, and one revision past the
    // confirmed one - but it carries a configuration this write never asked
    // for, so it cannot be this write's outcome.
    client.on(patchMethod, () => patchAnswer("mobile", hubDefault(3, desktopConfig)));
    await expect(store.getState().saveDraft("mobile", proposed)).rejects.toThrow(/malformed/);
    expect(store.getState().hub.mobile).toEqual(hubDefault(2, mobileConfig));
    expect(store.getState()).toMatchObject({ saving: false, writeUncertain: true, draftConflict: true });
  });

  test("a revision conflict is a KNOWN outcome: the canonical lands and the proposal stays for review", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const drafts = memoryDraftStorage<TranscriptDraftCheckpoint>();
    const store = await readyStore(client, { drafts: drafts.storage });
    client.on(patchMethod, () => {
      throw new WireError("revision conflict", -32013, {
        evenerErrorInfo: "conflict",
        layout: "mobile",
        current: toWireDefault(hubDefault(5, desktopConfig)),
      });
    });
    await expect(store.getState().saveDraft("mobile", proposed)).rejects.toThrow("revision conflict");
    // The hub said what happened: the write was refused and this is the
    // current value. Nothing about the outcome is unknown.
    expect(store.getState()).toMatchObject({ saving: false, writeUncertain: false, draftConflict: true });
    expect(store.getState().hub.mobile).toEqual(hubDefault(5, desktopConfig));
    expect(store.getState().draft?.config).toEqual(proposed);
    expect(drafts.stored()?.writeUncertain).toBe(false);
  });

  test("a newer external revision keeps the proposal for review instead of reporting it applied", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const drafts = memoryDraftStorage<TranscriptDraftCheckpoint>();
    const store = await readyStore(client, { drafts: drafts.storage });
    const reply = deferred<TranscriptDisplayPatchResponse>();
    client.on(patchMethod, () => reply.promise);
    const save = store.getState().saveDraft("mobile", proposed);
    await vi.waitFor(() => expect(store.getState().saving).toBe(true));
    client.emitNotification({
      method: changedMethod,
      params: { layout: "mobile", revision: 5, config: toWireConfig(desktopConfig) },
    });
    reply.resolve(patchAnswer("mobile", hubDefault(3, proposed)));
    await save;
    expect(store.getState()).toMatchObject({ saving: false, writeUncertain: false, draftConflict: true });
    expect(store.getState().draft?.config).toEqual(proposed);
    expect(store.getState().hub.mobile).toEqual(hubDefault(5, desktopConfig));
    expect(drafts.stored()?.writeUncertain).toBe(false);
  });

  test("a hub change during a draft applies and flags the draft for review; rebaseDraft moves it on", async () => {
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
    expect(store.getState().hubError).toBeNull();
    expect(() => store.getState().rebaseDraft(3)).toThrow(/changed again/);
    store.getState().rebaseDraft(4);
    expect(store.getState().draft).toEqual({ layout: "mobile", revision: 4, config: proposed });
    expect(store.getState().draftConflict).toBe(false);
  });

  test("a restored checkpoint is the store's first state; a corrupt one marks the port unavailable", async () => {
    const drafts = memoryDraftStorage<TranscriptDraftCheckpoint>({
      id: "d1",
      layout: "mobile",
      baseRevision: 2,
      config: proposed,
      writeUncertain: true,
    });
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const store = createTranscriptDisplayStore({ client, drafts: drafts.storage });
    expect(store.getState().draft).toEqual({ layout: "mobile", revision: 2, config: proposed });
    expect(store.getState().writeUncertain).toBe(true);

    const corrupt = memoryDraftStorage<TranscriptDraftCheckpoint>({ id: "", baseRevision: -1 });
    const broken = createTranscriptDisplayStore({ client, drafts: corrupt.storage });
    expect(broken.getState().storageUnavailable).toBe(true);
    expect(broken.getState().draftError).toMatch(/restore/);
  });

  test("the draft editor stays open while a read is in flight; the older reply is discarded", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const store = await readyStore(client, { drafts: memoryDraftStorage<TranscriptDraftCheckpoint>().storage });
    const reply = deferred<TranscriptDisplayDefaults>();
    client.on(getMethod, () => reply.promise);
    const refresh = store.getState().refreshHubDefaults();
    expect(store.getState().hubLoading).toBe(true);

    // The direct write refuses here - it composes an expectedRevision the
    // in-flight read is about to replace, and its preview is not durable.
    await expect(store.getState().patchHubDefault("mobile", proposed)).rejects.toThrow(/unavailable/);
    // The checkpointed editor does not: the proposal is on disk before
    // anything leaves, so the intent survives whatever the read confirms.
    expect(() => store.getState().editDraft("mobile", proposed)).not.toThrow();

    reply.resolve({
      desktop: toWireDefault(hubDefault(3, desktopConfig)),
      mobile: toWireDefault(hubDefault(2, mobileConfig)),
    });
    await refresh;
    expect(store.getState().draft?.config).toEqual(proposed);
  });

  test("an unreadable stored record never locks the section: the hub loads, discard clears it, edits resume", async () => {
    // The shape the native host wrote before layouts were recorded - and any
    // other value this build cannot read.
    const legacy = memoryDraftStorage<TranscriptDraftCheckpoint>({ id: "d0", baseRevision: 2, config: proposed, writeUncertain: false });
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const store = await readyStore(client, { drafts: legacy.storage });

    // The record is unreadable, not the port, and neither is the hub's
    // business: the section still has its defaults.
    expect(store.getState().draft).toBeNull();
    expect(store.getState().loaded).toBe(true);
    expect(store.getState().hub.mobile).toEqual(hubDefault(2, mobileConfig));
    expect(store.getState().storageUnavailable).toBe(true);

    // One tap throws the unreadable record away.
    store.getState().discardDraft();
    expect(legacy.stored()).toBeNull();
    expect(store.getState().storageUnavailable).toBe(false);

    // And the editor is usable again.
    expect(() => store.getState().editDraft("mobile", proposed)).not.toThrow();
    expect(store.getState().draft?.config).toEqual(proposed);
  });

  test("a checkpoint without a layout is undecodable, like any other malformed one", async () => {
    // The shape the native host wrote before layouts were recorded. A
    // checkpoint that does not say which layer it proposes names no draft:
    // it is not restored, and it takes the same path as any other malformed
    // stored value rather than being guessed at.
    const legacy = memoryDraftStorage<TranscriptDraftCheckpoint>({ id: "d0", baseRevision: 2, config: proposed, writeUncertain: false });
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const store = await readyStore(client, { drafts: legacy.storage });
    expect(store.getState().draft).toBeNull();
    expect(store.getState().storageUnavailable).toBe(true);
    expect(store.getState().draftError).toMatch(/restore/);
  });

  test("a restored proposal is discardable while the hub read has failed", async () => {
    const drafts = memoryDraftStorage<TranscriptDraftCheckpoint>({
      id: "d1",
      layout: "mobile",
      baseRevision: 2,
      config: proposed,
      writeUncertain: false,
    });
    const client = new FakeClient("ready");
    client.on(getMethod, () => {
      throw new Error("hub unreachable");
    });
    const store = await readyStore(client, { drafts: drafts.storage });
    expect(store.getState().loaded).toBe(false);
    expect(store.getState().draft?.config).toEqual(proposed);

    // Throwing away a proposal needs no confirmed hub state: it composes
    // nothing and sends nothing.
    store.getState().discardDraft();
    expect(store.getState().draft).toBeNull();
    expect(drafts.stored()).toBeNull();
  });

  test("discardDraft drops the proposal and its checkpoint", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const drafts = memoryDraftStorage<TranscriptDraftCheckpoint>();
    const store = await readyStore(client, { drafts: drafts.storage });
    store.getState().editDraft("mobile", proposed);
    store.getState().discardDraft();
    expect(store.getState().draft).toBeNull();
    expect(drafts.stored()).toBeNull();
  });
});

describe("a retired payload fences every reply still in flight", () => {
  const late = { desktop: toWireDefault(hubDefault(9, proposed)), mobile: toWireDefault(hubDefault(9, proposed)) };
  const resetSites: [string, (store: TranscriptDisplayStore) => void][] = [
    ["endReadyGeneration", (store) => store.endReadyGeneration()],
    ["setSupport(unsupported)", (store) => store.setSupport("unsupported")],
    ["detachHub", (store) => store.detachHub()],
    ["dispose", (store) => store.dispose()],
  ];
  const paths: [string, typeof getMethod | typeof patchMethod, (store: TranscriptDisplayStore) => Promise<unknown>][] =
    [
      ["patchHubDefault", patchMethod, (store) => store.getState().patchHubDefault("mobile", proposed)],
      ["saveDraft", patchMethod, (store) => store.getState().saveDraft("mobile", proposed)],
      ["refreshHubDefaults", getMethod, (store) => store.getState().refreshHubDefaults()],
    ];
  const cells = resetSites.flatMap(([site, reset]) =>
    paths.map(([path, method, start]) => [site, path, reset, method, start] as const),
  );

  test.each(cells)("%s while a %s reply is in flight", async (_site, _path, reset, method, start) => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const drafts = memoryDraftStorage<TranscriptDraftCheckpoint>();
    const store = await readyStore(client, { drafts: drafts.storage });
    const reply = deferred<TranscriptDisplayDefaults & TranscriptDisplayPatchResponse>();
    client.on(method, () => reply.promise);
    const settled = start(store).then(
      () => undefined,
      () => undefined,
    );
    await vi.waitFor(() =>
      expect(client.calls.filter((call) => call.method === method)).toHaveLength(method === getMethod ? 2 : 1),
    );
    reset(store);
    reply.resolve(
      (method === getMethod ? late : patchAnswer("mobile", hubDefault(9, proposed))) as TranscriptDisplayDefaults &
        TranscriptDisplayPatchResponse,
    );
    await settled;
    // The late reply landed nothing: the payload is retired and nothing is in flight.
    expect(store.getState()).toMatchObject({ loaded: false, saving: false, hubLoading: false });
    expect(store.getState().hub.mobile?.revision).not.toBe(9);
    expect(() => store.getState().editDraft("mobile", proposed)).toThrow(/unavailable/);
    await expect(store.getState().patchHubDefault("mobile", proposed)).rejects.toThrow(/unavailable/);
  });
});

describe("shipped defaults", () => {
  test("a store that never loaded still resolves the shipped default per layout", () => {
    expect(shippedDefault("desktop").revision).toBe(0);
  });
});
