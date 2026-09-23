import {
  makeTranscriptDisplayConfig,
  shippedMobileConfig,
  type TranscriptDisplayConfigV1,
  type TranscriptDisplayPatchResponse,
  toWireConfig,
  toWireDefault,
  WireError,
} from "@evener/appwire-client";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { installLocalStorage, MemoryStorage } from "../storageTestUtils";
import { connectionStore } from "./connection";
import {
  initTranscriptDisplay,
  resetTranscriptDisplayStoreForTests,
  transcriptDisplayStore,
} from "./transcriptDisplay";

const storage = new MemoryStorage();

function preset(level: "chat" | "intent" | "tools" | "activity" | "full"): TranscriptDisplayConfigV1 {
  return makeTranscriptDisplayConfig({ kind: "preset", level });
}

beforeEach(() => {
  storage.clear();
  installLocalStorage(storage);
  connectionStore.setState({ state: "idle", serverInfo: undefined, features: undefined, client: null });
  resetTranscriptDisplayStoreForTests();
  initTranscriptDisplay();
});

afterEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, features: undefined, client: null });
  resetTranscriptDisplayStoreForTests();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("transcript display adapter (package store delegation)", () => {
  test("a replacement client clears the replaced client's stale read error at the rewire", async () => {
    const first = new FakeClient("ready");
    first.on("evener/settings/transcriptDisplay/get", () => {
      throw new Error("first read failed");
    });
    connectionStore.getState().connect(first);
    connectionStore.setState({
      features: { ...(await first.connect()).features, transcriptDisplaySettings: true },
    });
    await transcriptDisplayStore.getState().refreshHubDefaults();
    expect(transcriptDisplayStore.getState().hubError).toBe("first read failed");
    expect(transcriptDisplayStore.getState().hubLoading).toBe(false);

    const second = new FakeClient("ready");
    second.on("evener/settings/transcriptDisplay/get", () => ({
      desktop: { revision: 1, config: preset("tools") },
      mobile: { revision: 1, config: shippedMobileConfig },
    }));
    connectionStore.getState().connect(second);
    // The stale error is cleared by the rewire itself, synchronously, before
    // the replacement client's read can even settle. The change-only mirror
    // would never republish it otherwise: the fresh store starts with a null
    // hubError and no publication ever changes it.
    expect(transcriptDisplayStore.getState().hubError).toBeNull();
    connectionStore.setState({
      features: { ...(await second.connect()).features, transcriptDisplaySettings: true },
    });
    await transcriptDisplayStore.getState().refreshHubDefaults();
    expect(transcriptDisplayStore.getState().hubError).toBeNull();
    expect(transcriptDisplayStore.getState().hub.desktop).toEqual({ revision: 1, config: preset("tools") });
  });

  test("a malformed-reply preview survives a write on the other layout", async () => {
    const client = new FakeClient("ready");
    client.on("evener/settings/transcriptDisplay/get", () => ({
      desktop: { revision: 1, config: preset("tools") },
      mobile: { revision: 1, config: shippedMobileConfig },
    }));
    let patchReply: () => TranscriptDisplayPatchResponse = () =>
      ({
        layout: "desktop",
        revision: 2,
        config: toWireConfig(preset("activity")),
        unexpected: "a hub reply field the web contract rejects",
      }) as TranscriptDisplayPatchResponse;
    client.on("evener/settings/transcriptDisplay/patch", () => patchReply());
    connectionStore.getState().connect(client);
    connectionStore.setState({
      features: { ...(await client.connect()).features, transcriptDisplaySettings: true },
    });
    await transcriptDisplayStore.getState().refreshHubDefaults();

    await expect(transcriptDisplayStore.getState().patchHubDefault("desktop", preset("activity"))).rejects.toThrow(
      "Hub returned malformed transcript display PATCH response",
    );
    expect(transcriptDisplayStore.getState().drafts.desktop).toEqual(preset("activity"));

    patchReply = () => ({ layout: "mobile", revision: 2, config: toWireConfig(preset("chat")) });
    const mobilePatch = transcriptDisplayStore.getState().patchHubDefault("mobile", preset("chat"));
    expect(transcriptDisplayStore.getState().drafts.mobile).toEqual(preset("chat"));
    // The retained Desktop preview must survive the Mobile write's draft
    // publications - drafts are per-layout.
    expect(transcriptDisplayStore.getState().drafts.desktop).toEqual(preset("activity"));
    await mobilePatch;
    expect(transcriptDisplayStore.getState().drafts.mobile).toBeUndefined();
    expect(transcriptDisplayStore.getState().drafts.desktop).toEqual(preset("activity"));
    expect(transcriptDisplayStore.getState().hub.mobile).toEqual({ revision: 2, config: preset("chat") });
  });

  test("a newer write on the same layout supersedes the restored preview", async () => {
    const client = new FakeClient("ready");
    client.on("evener/settings/transcriptDisplay/get", () => ({
      desktop: { revision: 1, config: preset("tools") },
      mobile: { revision: 1, config: shippedMobileConfig },
    }));
    let patchReply: () => TranscriptDisplayPatchResponse = () =>
      ({
        layout: "desktop",
        revision: 2,
        config: toWireConfig(preset("activity")),
        unexpected: "a hub reply field the web contract rejects",
      }) as TranscriptDisplayPatchResponse;
    client.on("evener/settings/transcriptDisplay/patch", () => patchReply());
    connectionStore.getState().connect(client);
    connectionStore.setState({
      features: { ...(await client.connect()).features, transcriptDisplaySettings: true },
    });
    await transcriptDisplayStore.getState().refreshHubDefaults();
    await expect(transcriptDisplayStore.getState().patchHubDefault("desktop", preset("activity"))).rejects.toThrow(
      "Hub returned malformed transcript display PATCH response",
    );
    expect(transcriptDisplayStore.getState().drafts.desktop).toEqual(preset("activity"));

    patchReply = () => ({ layout: "desktop", revision: 2, config: toWireConfig(preset("full")) });
    const retry = transcriptDisplayStore.getState().patchHubDefault("desktop", preset("full"));
    // The newer write's preview replaces the restored one immediately.
    expect(transcriptDisplayStore.getState().drafts.desktop).toEqual(preset("full"));
    await retry;
    expect(transcriptDisplayStore.getState().drafts.desktop).toBeUndefined();
    expect(transcriptDisplayStore.getState().hub.desktop).toEqual({ revision: 2, config: preset("full") });
  });

  test("a replacement with a pending handshake drops the stale support at the rewire", async () => {
    const first = new FakeClient("ready");
    first.on("evener/settings/transcriptDisplay/get", () => ({
      desktop: { revision: 1, config: preset("tools") },
      mobile: { revision: 1, config: shippedMobileConfig },
    }));
    connectionStore.getState().connect(first);
    connectionStore.setState({
      features: { ...(await first.connect()).features, transcriptDisplaySettings: true },
    });
    await transcriptDisplayStore.getState().refreshHubDefaults();
    expect(transcriptDisplayStore.getState().hubSupport).toBe("supported");

    // The replacement arrives with its handshake still pending: the publish
    // carries the new client and the cleared features together, and the web
    // store must not keep presenting the replaced client's "supported".
    const second = new FakeClient("connecting");
    connectionStore.setState({ client: second, features: undefined });
    expect(transcriptDisplayStore.getState().hubSupport).toBe("unknown");
  });

  test("a malformed reply rejected during a replacement rewire restores no preview", async () => {
    const client = new FakeClient("ready");
    client.on("evener/settings/transcriptDisplay/get", () => ({
      desktop: { revision: 1, config: preset("tools") },
      mobile: { revision: 1, config: shippedMobileConfig },
    }));
    let resolvePatch: ((value: TranscriptDisplayPatchResponse) => void) | undefined;
    client.on("evener/settings/transcriptDisplay/patch", () => {
      let resolve!: (value: TranscriptDisplayPatchResponse) => void;
      const promise = new Promise<TranscriptDisplayPatchResponse>((res) => {
        resolve = res;
      });
      resolvePatch = resolve;
      return promise;
    });
    connectionStore.getState().connect(client);
    connectionStore.setState({
      features: { ...(await client.connect()).features, transcriptDisplaySettings: true },
    });
    await transcriptDisplayStore.getState().refreshHubDefaults();

    const second = new FakeClient("ready");
    second.on("evener/settings/transcriptDisplay/get", () => ({
      desktop: { revision: 7, config: preset("full") },
      mobile: { revision: 7, config: shippedMobileConfig },
    }));

    // The package's failure publication runs the mirror synchronously, and a
    // web subscriber firing inside that same synchronous frame can replace the
    // client between the package's rejection and the adapter's restore - the
    // exact window the restore's lifecycle fence must close.
    const malformed = "Hub returned malformed transcript display PATCH response";
    let stopSwapping: (() => void) | undefined;
    const stop = transcriptDisplayStore.subscribe((state) => {
      if (state.hubError !== malformed || state.drafts.desktop !== undefined) return;
      stopSwapping?.();
      connectionStore.getState().connect(second);
    });
    stopSwapping = stop;

    const write = transcriptDisplayStore.getState().patchHubDefault("desktop", preset("activity"));
    expect(transcriptDisplayStore.getState().drafts.desktop).toEqual(preset("activity"));
    // The fake defers handler invocation by one microtask, so the write's
    // resolver is only assignable after that hop.
    await Promise.resolve();
    expect(resolvePatch).toBeDefined();
    resolvePatch?.({
      layout: "desktop",
      revision: 2,
      config: toWireConfig(preset("activity")),
      unexpected: "a hub reply field the web contract rejects",
    } as TranscriptDisplayPatchResponse);
    await expect(write).rejects.toThrow(malformed);
    stop();

    // The stale write must not resurrect its preview into the replacement's
    // world: the rewire anchored the fresh store's state - drafts empty,
    // support unknown, error clear - and the restore is fenced.
    expect(transcriptDisplayStore.getState().drafts.desktop).toBeUndefined();
    expect(transcriptDisplayStore.getState().hubSupport).toBe("unknown");
    expect(transcriptDisplayStore.getState().hubError).toBeNull();

    // The replacement's own handshake completes: only now does its read land,
    // and the fenced restore must stay fenced.
    connectionStore.setState({
      features: { ...(await second.connect()).features, transcriptDisplaySettings: true },
    });
    await transcriptDisplayStore.getState().refreshHubDefaults();
    expect(transcriptDisplayStore.getState().hub.desktop).toEqual({ revision: 7, config: preset("full") });
    expect(transcriptDisplayStore.getState().drafts.desktop).toBeUndefined();
  });

  test("an outgoing client's remaining publication lands nothing after a mid-publication replacement", async () => {
    const client = new FakeClient("ready");
    let getReply = {
      desktop: { revision: 1, config: preset("tools") },
      mobile: { revision: 1, config: shippedMobileConfig },
    };
    client.on("evener/settings/transcriptDisplay/get", () => getReply);
    connectionStore.getState().connect(client);
    connectionStore.setState({
      features: { ...(await client.connect()).features, transcriptDisplaySettings: true },
    });
    await transcriptDisplayStore.getState().refreshHubDefaults();
    expect(transcriptDisplayStore.getState().hub.desktop?.revision).toBe(1);

    const second = new FakeClient("ready");
    second.on("evener/settings/transcriptDisplay/get", () => ({
      desktop: { revision: 7, config: preset("full") },
      mobile: { revision: 7, config: shippedMobileConfig },
    }));

    // A synchronous web subscriber replaces the client during the Desktop
    // layout's transition publication, inside the mirror's own loop: the
    // outgoing client's Mobile default and remaining fields must not land in
    // the replacement's freshly anchored state afterwards.
    let stopSwapping: (() => void) | undefined;
    const stop = transcriptDisplayStore.subscribe((state) => {
      if (state.hub.desktop?.revision !== 2) return;
      stopSwapping?.();
      connectionStore.getState().connect(second);
    });
    stopSwapping = stop;

    getReply = {
      desktop: { revision: 2, config: preset("activity") },
      mobile: { revision: 2, config: preset("chat") },
    };
    await transcriptDisplayStore.getState().refreshHubDefaults();

    // The rewire's anchor owns the web store: the outgoing client's Mobile
    // default never lands, and the support follows the replacement's pending
    // handshake.
    expect(transcriptDisplayStore.getState().hub).toEqual({});
    expect(transcriptDisplayStore.getState().hubSupport).toBe("unknown");

    connectionStore.setState({
      features: { ...(await second.connect()).features, transcriptDisplaySettings: true },
    });
    await transcriptDisplayStore.getState().refreshHubDefaults();
    expect(transcriptDisplayStore.getState().hub).toEqual({
      desktop: { revision: 7, config: preset("full") },
      mobile: { revision: 7, config: shippedMobileConfig },
    });
  });

  test("a replacement connected during the detach publication keeps its mirror", async () => {
    const first = new FakeClient("ready");
    first.on("evener/settings/transcriptDisplay/get", () => ({
      desktop: { revision: 1, config: preset("tools") },
      mobile: { revision: 1, config: shippedMobileConfig },
    }));
    connectionStore.getState().connect(first);
    connectionStore.setState({
      features: { ...(await first.connect()).features, transcriptDisplaySettings: true },
    });
    await transcriptDisplayStore.getState().refreshHubDefaults();
    expect(transcriptDisplayStore.getState().hub.desktop?.revision).toBe(1);

    const second = new FakeClient("ready");
    second.on("evener/settings/transcriptDisplay/get", () => ({
      desktop: { revision: 7, config: preset("full") },
      mobile: { revision: 7, config: shippedMobileConfig },
    }));

    // A synchronous web subscriber connects the replacement during the
    // detach publication itself: the detach must unsubscribe only the
    // outgoing mirror and must not reset the replacement's anchored state.
    let stopSwapping: (() => void) | undefined;
    const stop = transcriptDisplayStore.subscribe((state) => {
      if (state.hub.desktop !== undefined) return;
      stopSwapping?.();
      connectionStore.getState().connect(second);
    });
    stopSwapping = stop;

    connectionStore.setState({ client: null });
    // The replacement was wired inside the publication window: complete its
    // handshake and read - its mirror must still be subscribed.
    connectionStore.setState({
      features: { ...(await second.connect()).features, transcriptDisplaySettings: true },
    });
    await transcriptDisplayStore.getState().refreshHubDefaults();
    expect(transcriptDisplayStore.getState().hub).toEqual({
      desktop: { revision: 7, config: preset("full") },
      mobile: { revision: 7, config: shippedMobileConfig },
    });
  });

  test("a retry started by an error subscriber keeps its preview against the outer mirror", async () => {
    const client = new FakeClient("ready");
    client.on("evener/settings/transcriptDisplay/get", () => ({
      desktop: { revision: 1, config: preset("tools") },
      mobile: { revision: 1, config: shippedMobileConfig },
    }));
    let patchAttempt = 0;
    let resolveRetry: ((value: TranscriptDisplayPatchResponse) => void) | undefined;
    client.on("evener/settings/transcriptDisplay/patch", () => {
      patchAttempt += 1;
      if (patchAttempt === 1) throw new Error("write failed");
      let resolve!: (value: TranscriptDisplayPatchResponse) => void;
      const promise = new Promise<TranscriptDisplayPatchResponse>((res) => {
        resolve = res;
      });
      resolveRetry = resolve;
      return promise;
    });
    connectionStore.getState().connect(client);
    connectionStore.setState({
      features: { ...(await client.connect()).features, transcriptDisplaySettings: true },
    });
    await transcriptDisplayStore.getState().refreshHubDefaults();

    let retry: Promise<unknown> | undefined;
    let retryStarted = false;
    const stop = transcriptDisplayStore.subscribe((state) => {
      if (state.hubError !== "write failed" || retryStarted) return;
      // The in-flight flag flips BEFORE the call: the retry's own start
      // publication runs synchronously inside it, and this subscriber must
      // not fire again for the still-present error during that publication.
      retryStarted = true;
      retry = transcriptDisplayStore.getState().patchHubDefault("desktop", preset("full"));
    });

    const write = transcriptDisplayStore.getState().patchHubDefault("desktop", preset("activity"));
    await expect(write).rejects.toThrow("write failed");
    stop();
    // The retry write owns the layout now: its preview survived the outer
    // mirror loop, and the failure's stale per-layout error never landed
    // back over the retry's cleared slot.
    expect(transcriptDisplayStore.getState().drafts.desktop).toEqual(preset("full"));
    expect(transcriptDisplayStore.getState().hubErrors.desktop).toBeUndefined();
    expect(transcriptDisplayStore.getState().hubError).toBe("write failed");
    resolveRetry?.({ layout: "desktop", revision: 2, config: toWireConfig(preset("full")) });
    await retry;
    expect(transcriptDisplayStore.getState().hub.desktop).toEqual({ revision: 2, config: preset("full") });
    expect(transcriptDisplayStore.getState().drafts.desktop).toBeUndefined();
  });

  test("handshake features supplied during the anchor publication survive the anchor", async () => {
    const first = new FakeClient("ready");
    first.on("evener/settings/transcriptDisplay/get", () => ({
      desktop: { revision: 1, config: preset("tools") },
      mobile: { revision: 1, config: shippedMobileConfig },
    }));
    connectionStore.getState().connect(first);
    connectionStore.setState({
      features: { ...(await first.connect()).features, transcriptDisplaySettings: true },
    });
    await transcriptDisplayStore.getState().refreshHubDefaults();

    const second = new FakeClient("ready");
    second.on("evener/settings/transcriptDisplay/get", () => ({
      desktop: { revision: 7, config: preset("full") },
      mobile: { revision: 7, config: shippedMobileConfig },
    }));
    const secondFeatures = { ...(await second.connect()).features, transcriptDisplaySettings: true };

    let stopSwapping: (() => void) | undefined;
    const stop = transcriptDisplayStore.subscribe((state) => {
      if (state.hub.desktop !== undefined) return;
      stopSwapping?.();
      // The replacement's handshake completes during the anchor's own
      // publication: the package publishes its "supported" transition, and
      // the anchor must not overwrite it with the state it captured before
      // its layout publications began.
      connectionStore.setState({ features: secondFeatures });
    });
    stopSwapping = stop;

    connectionStore.getState().connect(second);
    expect(transcriptDisplayStore.getState().hubSupport).toBe("supported");
    stop();
    await transcriptDisplayStore.getState().refreshHubDefaults();
    expect(transcriptDisplayStore.getState().hub.desktop).toEqual({ revision: 7, config: preset("full") });
  });

  test("a disconnect during one layout's publication does not strand the other layout's update", async () => {
    const client = new FakeClient("ready");
    let getReply = {
      desktop: { revision: 1, config: preset("tools") },
      mobile: { revision: 1, config: shippedMobileConfig },
    };
    client.on("evener/settings/transcriptDisplay/get", () => getReply);
    connectionStore.getState().connect(client);
    connectionStore.setState({
      features: { ...(await client.connect()).features, transcriptDisplaySettings: true },
    });
    await transcriptDisplayStore.getState().refreshHubDefaults();

    let stopSwapping: (() => void) | undefined;
    const stop = transcriptDisplayStore.subscribe((state) => {
      if (state.hub.desktop?.revision !== 2) return;
      stopSwapping?.();
      // The connection drops while the mirror is mid-way through the GET's
      // two-layout publication: the same store keeps its confirmed values,
      // so the interrupted publication must finish, not strand Mobile.
      connectionStore.setState({ state: "connecting" });
    });
    stopSwapping = stop;

    getReply = {
      desktop: { revision: 2, config: preset("activity") },
      mobile: { revision: 2, config: preset("chat") },
    };
    await transcriptDisplayStore.getState().refreshHubDefaults();

    expect(transcriptDisplayStore.getState().hub.desktop).toEqual({ revision: 2, config: preset("activity") });
    expect(transcriptDisplayStore.getState().hub.mobile).toEqual({ revision: 2, config: preset("chat") });
  });

  test("a replacement during the anchor leaves the superseded client's ready listener unregistered", async () => {
    const first = new FakeClient("ready");
    first.on("evener/settings/transcriptDisplay/get", () => ({
      desktop: { revision: 1, config: preset("tools") },
      mobile: { revision: 1, config: shippedMobileConfig },
    }));
    connectionStore.getState().connect(first);
    connectionStore.setState({
      features: { ...(await first.connect()).features, transcriptDisplaySettings: true },
    });
    await transcriptDisplayStore.getState().refreshHubDefaults();

    const second = new FakeClient("ready");
    const third = new FakeClient("ready");
    third.on("evener/settings/transcriptDisplay/get", () => ({
      desktop: { revision: 7, config: preset("full") },
      mobile: { revision: 7, config: shippedMobileConfig },
    }));

    let stopSwapping: (() => void) | undefined;
    const stop = transcriptDisplayStore.subscribe((state) => {
      if (state.hub.desktop !== undefined) return;
      stopSwapping?.();
      // Replaces the client from inside the anchor's publication window:
      // the outer rewire (second's) must not register second's ready
      // callback once third's rewire owns the module slots.
      connectionStore.getState().connect(third);
    });
    stopSwapping = stop;

    connectionStore.getState().connect(second);
    stop();
    expect(second.listenerCount).toBe(0);

    // The replacement's world is functional: handshake and read land.
    connectionStore.setState({
      features: { ...(await third.connect()).features, transcriptDisplaySettings: true },
    });
    await transcriptDisplayStore.getState().refreshHubDefaults();
    expect(transcriptDisplayStore.getState().hub.desktop).toEqual({ revision: 7, config: preset("full") });
  });

  test("draft actions forward to the package store and the checkpoint survives a client swap", async () => {
    const client = new FakeClient("ready");
    client.on("evener/settings/transcriptDisplay/get", () => ({
      desktop: { revision: 3, config: preset("intent") },
      mobile: { revision: 2, config: shippedMobileConfig },
    }));
    connectionStore.getState().connect(client);
    connectionStore.setState({
      features: { ...(await client.connect()).features, transcriptDisplaySettings: true },
    });
    await transcriptDisplayStore.getState().refreshHubDefaults();

    // editDraft forwards and the composed draft mirrors into the web state.
    transcriptDisplayStore.getState().editDraft("mobile", preset("tools"));
    expect(transcriptDisplayStore.getState().draft).toEqual({
      layout: "mobile",
      revision: 2,
      config: preset("tools"),
      generation: 1,
    });
    expect(storage.getItem("evener.prefs.transcriptDisplay.draft")).toBeTruthy();

    // A replacement client builds a fresh package store: the browser port
    // hands it the same durable checkpoint, and the new generation's read
    // stamps it.
    const replacement = new FakeClient("ready");
    replacement.on("evener/settings/transcriptDisplay/get", () => ({
      desktop: { revision: 3, config: preset("intent") },
      mobile: { revision: 2, config: shippedMobileConfig },
    }));
    connectionStore.getState().connect(replacement);
    expect(transcriptDisplayStore.getState().draft).toMatchObject({
      layout: "mobile",
      revision: 2,
      config: preset("tools"),
    });
    connectionStore.setState({
      features: { ...(await replacement.connect()).features, transcriptDisplaySettings: true },
    });
    await transcriptDisplayStore.getState().refreshHubDefaults();
    expect(transcriptDisplayStore.getState().draft).toEqual({
      layout: "mobile",
      revision: 2,
      config: preset("tools"),
      generation: 1,
    });

    // discardDraft forwards and removes the durable record.
    transcriptDisplayStore.getState().discardDraft();
    expect(transcriptDisplayStore.getState().draft).toBeNull();
    expect(storage.getItem("evener.prefs.transcriptDisplay.draft")).toBeNull();
  });

  test("a blocked draft port surfaces the package's storage failure through the mirror", async () => {
    const client = new FakeClient("ready");
    client.on("evener/settings/transcriptDisplay/get", () => ({
      desktop: { revision: 3, config: preset("intent") },
      mobile: { revision: 2, config: shippedMobileConfig },
    }));
    connectionStore.getState().connect(client);
    connectionStore.setState({
      features: { ...(await client.connect()).features, transcriptDisplaySettings: true },
    });
    await transcriptDisplayStore.getState().refreshHubDefaults();

    // A storage that rejects writes: the package's port failure feeds
    // storageUnavailable/draftError through the mirror, and the package's
    // own gate - reached by forwarding, with no web gate of its own -
    // refuses the next edit.
    const blocked = {
      getItem: (key: string) => storage.getItem(key),
      setItem: () => {
        throw new Error("quota exceeded");
      },
      removeItem: () => {
        throw new Error("quota exceeded");
      },
    };
    vi.stubGlobal("localStorage", blocked);
    expect(() => transcriptDisplayStore.getState().editDraft("mobile", preset("tools"))).toThrow(
      /save the transcript draft/,
    );
    expect(transcriptDisplayStore.getState().storageUnavailable).toBe(true);
    expect(transcriptDisplayStore.getState().draftError).toMatch(/save the transcript draft/);
    expect(() => transcriptDisplayStore.getState().editDraft("mobile", preset("tools"))).toThrow(/unavailable/);

    // Once storage recovers, a refresh re-reads the port and clears the
    // failure through the mirror.
    vi.stubGlobal("localStorage", storage);
    await transcriptDisplayStore.getState().refreshHubDefaults();
    expect(transcriptDisplayStore.getState().storageUnavailable).toBe(false);
    expect(() => transcriptDisplayStore.getState().editDraft("mobile", preset("tools"))).not.toThrow();
  });

  test("saveDraft settles through the adapter and the direct write stays gated while it is in flight", async () => {
    const client = new FakeClient("ready");
    client.on("evener/settings/transcriptDisplay/get", () => ({
      desktop: { revision: 3, config: preset("intent") },
      mobile: { revision: 2, config: shippedMobileConfig },
    }));
    connectionStore.getState().connect(client);
    connectionStore.setState({
      features: { ...(await client.connect()).features, transcriptDisplaySettings: true },
    });
    await transcriptDisplayStore.getState().refreshHubDefaults();
    transcriptDisplayStore.getState().editDraft("mobile", preset("tools"));

    const reply = deferred<TranscriptDisplayPatchResponse>();
    client.on("evener/settings/transcriptDisplay/patch", () => reply.promise);
    const save = transcriptDisplayStore.getState().saveDraft();
    await vi.waitFor(() => expect(transcriptDisplayStore.getState().saving).toBe(true));
    // The one end-to-end pass of the package's direct-write gate: the write
    // forwards and refuses while the checkpointed save holds the layer.
    await expect(transcriptDisplayStore.getState().patchHubDefault("mobile", preset("full"))).rejects.toThrow(
      /unavailable/,
    );
    reply.resolve({ layout: "mobile", revision: 3, config: toWireConfig(preset("tools")) });
    expect(await save).toEqual({ revision: 3, config: preset("tools") });
    expect(transcriptDisplayStore.getState().saving).toBe(false);
    expect(transcriptDisplayStore.getState().draft).toBeNull();
    expect(storage.getItem("evener.prefs.transcriptDisplay.draft")).toBeNull();
  });

  test("conflict review, rebase, and an uncertain write's settlement mirror through the adapter", async () => {
    const client = new FakeClient("ready");
    client.on("evener/settings/transcriptDisplay/get", () => ({
      desktop: { revision: 3, config: preset("intent") },
      mobile: { revision: 2, config: shippedMobileConfig },
    }));
    connectionStore.getState().connect(client);
    connectionStore.setState({
      features: { ...(await client.connect()).features, transcriptDisplaySettings: true },
    });
    await transcriptDisplayStore.getState().refreshHubDefaults();
    transcriptDisplayStore.getState().editDraft("mobile", preset("tools"));

    // A known conflict keeps the proposal for review; rebaseDraft forwards
    // the review and clears the conflict.
    client.on("evener/settings/transcriptDisplay/patch", () => {
      throw new WireError("revision conflict", -32013, {
        evenerErrorInfo: "conflict",
        layout: "mobile",
        current: toWireDefault({ revision: 5, config: preset("chat") }),
      });
    });
    await expect(transcriptDisplayStore.getState().saveDraft()).rejects.toThrow("revision conflict");
    expect(transcriptDisplayStore.getState().draftConflict).toBe(true);
    transcriptDisplayStore.getState().rebaseDraft(5);
    expect(transcriptDisplayStore.getState().draftConflict).toBe(false);
    expect(transcriptDisplayStore.getState().draft).toMatchObject({ layout: "mobile", revision: 5 });

    // A lost reply leaves the uncertainty mirrored, and an authoritative
    // read settles it - all package machinery, all mirrored state.
    client.on("evener/settings/transcriptDisplay/patch", () => {
      throw new Error("connection lost");
    });
    await expect(transcriptDisplayStore.getState().saveDraft()).rejects.toThrow("connection lost");
    expect(transcriptDisplayStore.getState().writeUncertain).toBe(true);
    await transcriptDisplayStore.getState().refreshHubDefaults();
    expect(transcriptDisplayStore.getState().writeUncertain).toBe(false);
  });

  test("a seeded unreadable draft record stays discardable through the adapter", async () => {
    // A record some other build or a corrupted profile wrote - bytes the
    // port cannot parse: present, tagged, and handed to the package, which
    // classifies it unreadable rather than reporting the port failed.
    storage.setItem("evener.prefs.transcriptDisplay.draft", "not a draft");
    const client = new FakeClient("ready");
    client.on("evener/settings/transcriptDisplay/get", () => ({
      desktop: { revision: 3, config: preset("intent") },
      mobile: { revision: 2, config: shippedMobileConfig },
    }));
    connectionStore.getState().connect(client);
    expect(transcriptDisplayStore.getState().draftUnreadable).toBe(true);
    expect(transcriptDisplayStore.getState().storageUnavailable).toBe(true);
    connectionStore.setState({
      features: { ...(await client.connect()).features, transcriptDisplaySettings: true },
    });
    await transcriptDisplayStore.getState().refreshHubDefaults();
    expect(transcriptDisplayStore.getState().draft).toBeNull();

    // The one recovery an unreadable record allows forwards and clears it.
    transcriptDisplayStore.getState().discardDraft();
    expect(transcriptDisplayStore.getState().draftUnreadable).toBe(false);
    expect(transcriptDisplayStore.getState().storageUnavailable).toBe(false);
    expect(storage.getItem("evener.prefs.transcriptDisplay.draft")).toBeNull();
  });
});
