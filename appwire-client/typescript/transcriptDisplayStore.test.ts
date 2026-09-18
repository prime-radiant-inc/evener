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
  type TranscriptDisplayStore,
  type TranscriptDisplayStoreDeps,
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

  // Exercises ReadyGenerationFence.awaitingFirstPayload: a new generation's
  // first read applies at ANY revision, because revision numbering is the
  // hub's and a reconnect can be a hub restart.
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

    client.emitNotification({
      method: changedMethod,
      params: { layout: "mobile", revision: 5, config: toWireConfig(proposed) },
    });
    expect(store.getState().hub.mobile).toEqual(hubDefault(5, proposed));
    expect(store.getState().hubError).toBeNull();
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

  test("a fenced no-op write rejects rather than reporting a success the current hub never acknowledged", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const store = await readyStore(client);
    const reply = deferred<TranscriptDisplayPatchResponse>();
    client.on(patchMethod, () => reply.promise);
    const write = store.getState().patchHubDefault("mobile", mobileConfig);
    await vi.waitFor(() => expect(client.calls.filter((call) => call.method === patchMethod)).toHaveLength(1));

    store.detachHub();

    reply.resolve(patchAnswer("mobile", hubDefault(2, mobileConfig)));

    await expect(write).rejects.toThrow(/hub connection changed/);
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

  test("a post-apply durable failure applies the carried canonical state instead of surfacing an error", async () => {
    // The hub's PATCH APPLIED (the error carries the canonical applied state,
    // and the broadcast reconciles every other client) - only the follow-up
    // sync back to the requester failed. Mirrors keybindingsStore.ts's
    // keybindingsPostRename handling: reporting hubError here would disable
    // editing over a default that is already live.
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const store = await readyStore(client);
    client.on(patchMethod, () => {
      throw new WireError("sync transcript display state: boom", -32603, {
        evenerErrorInfo: "transcriptDisplayPostApply",
        applied: toWireDefault(hubDefault(4, mobileConfig)),
      });
    });

    const applied = await store.getState().patchHubDefault("mobile", proposed);

    expect(applied).toEqual(hubDefault(4, mobileConfig));
    expect(store.getState().hub.mobile).toEqual(hubDefault(4, mobileConfig));
    expect(store.getState().drafts.mobile).toBeUndefined();
    expect(store.getState().hubErrors.mobile).toBeUndefined();
    expect(store.getState().hubError).toBeNull();
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
    await expect(write).rejects.toThrow(/hub connection changed/);
    expect(store.getState().hub.mobile).toEqual(hubDefault(2, mobileConfig));
    expect(store.getState().loaded).toBe(false);
  });

  test("a preview survives a transient disconnect and is dropped when the hub identity is", async () => {
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
    expect(store.getState().drafts.mobile).toBeUndefined();

    reply.resolve(patchAnswer("mobile", hubDefault(3, proposed)));
    await expect(write).rejects.toThrow(/hub connection changed/);
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

  test("a reply the hub has already moved past drops the preview and keeps the newer value", async () => {
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
    await expect(write).rejects.toThrow(/malformed/);

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
    await expect(write).rejects.toThrow(/hub connection changed/);
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
    await expect(write).rejects.toThrow(/hub connection changed/);
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
    await expect(write).rejects.toThrow(/hub connection changed/);
    expect(store.getState().drafts.mobile).toEqual(proposed);

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
    await expect(write).rejects.toThrow(/hub connection changed/);

    client.on(getMethod, () => ({
      desktop: toWireDefault(hubDefault(3, desktopConfig)),
      mobile: toWireDefault(hubDefault(3, proposed)),
    }));
    store.beginReadyGeneration();
    await store.getState().refreshHubDefaults();
    expect(store.getState().hub.mobile).toEqual(hubDefault(3, proposed));
    expect(store.getState().drafts.mobile).toEqual(proposed);
  });

  test("a malformed success changes no hub state, reports it, and clears the stranded preview", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const store = await readyStore(client);
    client.on(patchMethod, () => ({ layout: "mobile", revision: 9, config: toWireConfig(proposed) }));
    await expect(store.getState().patchHubDefault("mobile", proposed)).rejects.toThrow(/malformed/);
    expect(store.getState().hub.mobile).toEqual(hubDefault(2, mobileConfig));
    expect(store.getState().hubErrors.mobile).toMatch(/malformed/);
    // A malformed reply is exactly as unknown an outcome as a transport
    // failure (the request's own PATCH may or may not have applied on the
    // hub) - the optimistic preview must not be treated differently.
    expect(store.getState().drafts.mobile).toBeUndefined();
  });

  test("a PATCH reply with extra keys still decodes (forward compatibility)", async () => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const store = await readyStore(client);
    client.on(patchMethod, () => ({
      layout: "mobile",
      revision: 3,
      config: toWireConfig(proposed),
      // A hub-added field a future build might send: fromWireChange and
      // fromWireDefaults already tolerate this; the PATCH reply decoder
      // must not be the odd one out.
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
});

describe("a retired payload fences every reply still in flight", () => {
  const late = { desktop: toWireDefault(hubDefault(9, proposed)), mobile: toWireDefault(hubDefault(9, proposed)) };
  const resetSites: [string, (store: TranscriptDisplayStore) => void][] = [
    ["endReadyGeneration", (store) => store.endReadyGeneration()],
    ["setSupport(unsupported)", (store) => store.setSupport("unsupported")],
    ["detachHub", (store) => store.detachHub()],
    ["dispose", (store) => store.dispose()],
  ];
  // saveDraft (piece 12) is a third path over the same fence in the oracle's
  // own matrix; dropped here since this store has no checkpointed editor yet.
  const paths: [string, typeof getMethod | typeof patchMethod, (store: TranscriptDisplayStore) => Promise<unknown>][] =
    [
      ["patchHubDefault", patchMethod, (store) => store.getState().patchHubDefault("mobile", proposed)],
      ["refreshHubDefaults", getMethod, (store) => store.getState().refreshHubDefaults()],
    ];
  const cells = resetSites.flatMap(([site, reset]) =>
    paths.map(([path, method, start]) => [site, path, reset, method, start] as const),
  );

  test.each(cells)("%s while a %s reply is in flight", async (_site, _path, reset, method, start) => {
    const client = serving(hubDefault(3, desktopConfig), hubDefault(2, mobileConfig));
    const store = await readyStore(client);
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
    expect(store.getState()).toMatchObject({ loaded: false, hubLoading: false });
    expect(store.getState().hub.mobile?.revision).not.toBe(9);
    await expect(store.getState().patchHubDefault("mobile", proposed)).rejects.toThrow(/unavailable/);
  });
});
