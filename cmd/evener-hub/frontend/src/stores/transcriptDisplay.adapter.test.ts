import {
  makeTranscriptDisplayConfig,
  shippedMobileConfig,
  type TranscriptDisplayConfigV1,
  type TranscriptDisplayPatchResponse,
  toWireConfig,
} from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { connectionStore } from "./connection";
import {
  initTranscriptDisplay,
  resetTranscriptDisplayStoreForTests,
  transcriptDisplayStore,
} from "./transcriptDisplay";

class MemoryStorage {
  private values = new Map<string, string>();
  getItem(key: string): string | null {
    return this.values.get(key) ?? null;
  }
  setItem(key: string, value: string): void {
    this.values.set(key, value);
  }
  removeItem(key: string): void {
    this.values.delete(key);
  }
  clear(): void {
    this.values.clear();
  }
}

const storage = new MemoryStorage();

function preset(level: "chat" | "intent" | "tools" | "activity" | "full"): TranscriptDisplayConfigV1 {
  return makeTranscriptDisplayConfig({ kind: "preset", level });
}

beforeEach(() => {
  storage.clear();
  // @ts-expect-error MemoryStorage is the deterministic browser storage seam.
  globalThis.localStorage = storage;
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
});
