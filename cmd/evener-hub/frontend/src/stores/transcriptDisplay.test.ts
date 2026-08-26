import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { WireError } from "../protocol/errors";
import { FakeClient } from "../protocol/testing/fakeClient";
import {
  encodeLocalConfig,
  makeTranscriptDisplayConfig,
  shippedDesktopConfig,
  shippedMobileConfig,
  type TranscriptDisplayConfigV1,
  toWireConfig,
} from "../transcriptDisplay/config";
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

class FakeBroadcastChannel {
  static instances: FakeBroadcastChannel[] = [];
  readonly posted: unknown[] = [];
  private readonly listeners = new Set<(event: MessageEvent<unknown>) => void>();
  constructor(readonly name: string) {
    FakeBroadcastChannel.instances.push(this);
  }
  addEventListener(type: string, listener: (event: MessageEvent<unknown>) => void): void {
    if (type === "message") this.listeners.add(listener);
  }
  removeEventListener(type: string, listener: (event: MessageEvent<unknown>) => void): void {
    if (type === "message") this.listeners.delete(listener);
  }
  postMessage(message: unknown): void {
    this.posted.push(message);
  }
  close(): void {}
  emit(data: unknown): void {
    for (const listener of this.listeners) listener({ data } as MessageEvent<unknown>);
  }
}

const storage = new MemoryStorage();
const desktopKey = "evener.prefs.transcriptDisplay.desktop";
const mobileKey = "evener.prefs.transcriptDisplay.mobile";
const legacyKeys = [
  "evener.prefs.transcriptRoundTimings",
  "evener.prefs.transcriptTokenCounts",
  "evener.prefs.transcriptHookExitsAll",
  "evener.prefs.transcriptHookExitsNormal",
  "evener.prefs.transcriptPromptLoaded",
  "evener.prefs.showCost",
] as const;

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

describe("effective transcript display state", () => {
  test("uses shipped Desktop and Mobile defaults when no local or hub value exists", () => {
    expect(transcriptDisplayStore.getState().hubSupport).toBe("unknown");
    expect(transcriptDisplayStore.getState().effective("desktop")).toEqual(shippedDesktopConfig);
    expect(transcriptDisplayStore.getState().effective("mobile")).toEqual(shippedMobileConfig);
  });

  test("keeps Desktop and Mobile local values independent", () => {
    transcriptDisplayStore.getState().setLocal("desktop", preset("full"));
    transcriptDisplayStore.getState().setLocal("mobile", preset("chat"));
    expect(transcriptDisplayStore.getState().effective("desktop")).toEqual(preset("full"));
    expect(transcriptDisplayStore.getState().effective("mobile")).toEqual(preset("chat"));
    expect(storage.getItem(desktopKey)).not.toBe(storage.getItem(mobileKey));
  });

  test("keeps a local Desktop view while a newer hub default updates followers", () => {
    transcriptDisplayStore.getState().setLocal("desktop", preset("activity"));
    transcriptDisplayStore.getState().applyHubChange({ layout: "desktop", revision: 2, config: preset("chat") });
    expect(transcriptDisplayStore.getState().effective("desktop")).toEqual(preset("activity"));
    expect(transcriptDisplayStore.getState().hub.desktop?.config).toEqual(preset("chat"));
  });

  test("previews a draft immediately and commits the canonical PATCH response", async () => {
    const client = new FakeClient("ready");
    const confirmed = preset("tools");
    const canonical = preset("activity");
    client.on("evener/settings/transcriptDisplay/get", () => ({
      desktop: { revision: 1, config: confirmed },
      mobile: { revision: 1, config: shippedMobileConfig },
    }));
    client.on("evener/settings/transcriptDisplay/patch", () => ({
      layout: "desktop",
      revision: 2,
      config: canonical,
    }));
    connectionStore.getState().connect(client);
    connectionStore.setState({ features: { ...(await client.connect()).features, transcriptDisplaySettings: true } });
    await transcriptDisplayStore.getState().refreshHubDefaults();
    const patch = transcriptDisplayStore.getState().patchHubDefault("desktop", canonical);
    expect(transcriptDisplayStore.getState().drafts.desktop).toEqual(canonical);
    await patch;
    expect(transcriptDisplayStore.getState().drafts.desktop).toBeUndefined();
    expect(transcriptDisplayStore.getState().hub.desktop).toEqual({ revision: 2, config: canonical });
    expect(transcriptDisplayStore.getState().effective("desktop")).toEqual(canonical);
  });

  test("reverts a failed or conflicting draft to the narrow canonical conflict response", async () => {
    const client = new FakeClient("ready");
    const current = preset("activity");
    client.on("evener/settings/transcriptDisplay/get", () => ({
      desktop: { revision: 3, config: current },
      mobile: { revision: 0, config: shippedMobileConfig },
    }));
    client.on("evener/settings/transcriptDisplay/patch", () => {
      throw new WireError("revision conflict", -32013, {
        evenerErrorInfo: "conflict",
        layout: "desktop",
        current: { revision: 4, config: preset("full") },
        ignored: "not part of the conflict contract",
      });
    });
    connectionStore.getState().connect(client);
    connectionStore.setState({ features: { ...(await client.connect()).features, transcriptDisplaySettings: true } });
    await transcriptDisplayStore.getState().refreshHubDefaults();
    await expect(transcriptDisplayStore.getState().patchHubDefault("desktop", preset("chat"))).rejects.toThrow(
      "revision conflict",
    );
    expect(transcriptDisplayStore.getState().drafts.desktop).toBeUndefined();
    expect(transcriptDisplayStore.getState().hub.desktop).toEqual({ revision: 4, config: preset("full") });
    expect(transcriptDisplayStore.getState().hubError).toBe("revision conflict");
  });

  test("ignores stale and equal-revision notifications", () => {
    const current = preset("tools");
    transcriptDisplayStore.getState().applyHubChange({ layout: "desktop", revision: 3, config: current });
    transcriptDisplayStore.getState().applyHubChange({ layout: "desktop", revision: 2, config: preset("chat") });
    transcriptDisplayStore.getState().applyHubChange({ layout: "desktop", revision: 3, config: preset("full") });
    expect(transcriptDisplayStore.getState().hub.desktop).toEqual({ revision: 3, config: current });
  });

  test("refreshes hub defaults on each ready generation", async () => {
    const client = new FakeClient("idle");
    let revision = 0;
    client.on("evener/settings/transcriptDisplay/get", () => {
      revision += 1;
      return {
        desktop: { revision, config: revision === 1 ? preset("tools") : preset("activity") },
        mobile: { revision, config: shippedMobileConfig },
      };
    });
    connectionStore.getState().connect(client);
    connectionStore.setState({ features: { ...(await client.connect()).features, transcriptDisplaySettings: true } });
    const first = waitForHubRevision(1);
    client.emitReady();
    await first;
    const second = waitForHubRevision(2);
    client.emitStateChange("reconnecting");
    client.emitReady();
    await second;
    expect(revision).toBe(2);
    expect(transcriptDisplayStore.getState().hub.desktop?.config).toEqual(preset("activity"));
  });

  test("does not issue transcript RPCs to an older hub with no capability field", async () => {
    const client = new FakeClient("ready");
    client.on("evener/settings/transcriptDisplay/get", () => {
      throw new Error("must not call the new method on an older hub");
    });
    connectionStore.getState().connect(client);
    connectionStore.setState({ features: (await client.connect()).features });
    await transcriptDisplayStore.getState().refreshHubDefaults();
    expect(transcriptDisplayStore.getState().hubSupport).toBe("unsupported");
    expect(client.calls).toHaveLength(0);
  });

  test("fences notifications from a replaced client", async () => {
    const stale = new FakeClient("ready");
    const current = new FakeClient("ready");
    current.on("evener/settings/transcriptDisplay/get", () => ({
      desktop: { revision: 1, config: preset("tools") },
      mobile: { revision: 1, config: shippedMobileConfig },
    }));
    connectionStore.getState().connect(stale);
    connectionStore.setState({ features: { ...(await stale.connect()).features, transcriptDisplaySettings: true } });
    connectionStore.getState().connect(current);
    connectionStore.setState({ features: { ...(await current.connect()).features, transcriptDisplaySettings: true } });
    await transcriptDisplayStore.getState().refreshHubDefaults();
    stale.emitNotification({
      method: "evener/settings/transcriptDisplay/changed",
      params: { layout: "desktop", revision: 9, config: toWireConfig(preset("full")) },
    });
    expect(transcriptDisplayStore.getState().hub.desktop).toEqual({ revision: 1, config: preset("tools") });
  });
});

function waitForHubRevision(revision: number): Promise<void> {
  return new Promise((resolve) => {
    const check = () => {
      if (transcriptDisplayStore.getState().hub.desktop?.revision !== revision) return;
      stop();
      resolve();
    };
    const stop = transcriptDisplayStore.subscribe(check);
    check();
  });
}

test("local values survive store reinitialization", () => {
  const value = preset("activity");
  transcriptDisplayStore.getState().setLocal("desktop", value);
  resetTranscriptDisplayStoreForTests();
  initTranscriptDisplay();
  expect(transcriptDisplayStore.getState().local.desktop).toEqual(value);
});

test("migration runs before local load, keeps old keys, and dual-writes literal booleans", () => {
  storage.setItem("evener.prefs.transcriptRoundTimings", "0");
  storage.setItem("evener.prefs.transcriptTokenCounts", "1");
  storage.setItem("evener.prefs.showCost", "1");
  resetTranscriptDisplayStoreForTests();
  initTranscriptDisplay();
  expect(transcriptDisplayStore.getState().local.desktop?.content).toEqual({ kind: "preset", level: "activity" });
  expect(transcriptDisplayStore.getState().local.mobile).toEqual(transcriptDisplayStore.getState().local.desktop);
  expect(storage.getItem(desktopKey)).not.toBeNull();
  expect(storage.getItem(mobileKey)).not.toBeNull();
  expect(storage.getItem("evener.prefs.transcriptRoundTimings")).toBe("0");
  expect(storage.getItem("evener.prefs.transcriptTokenCounts")).toBe("1");

  transcriptDisplayStore
    .getState()
    .setLocal(
      "desktop",
      makeTranscriptDisplayConfig({ kind: "preset", level: "full" }, { roundTimings: true, promptEvents: true }),
    );
  expect(legacyKeys.map((key) => storage.getItem(key))).toEqual(["1", "0", "0", "0", "1", "0"]);
});

test("keeps an in-memory local change and warns when browser storage is blocked", () => {
  const blocked = {
    getItem: () => {
      throw new Error("blocked");
    },
    setItem: () => {
      throw new Error("blocked");
    },
    removeItem: () => {
      throw new Error("blocked");
    },
  };
  vi.stubGlobal("localStorage", blocked);
  const value = preset("activity");
  transcriptDisplayStore.getState().setLocal("desktop", value);
  expect(transcriptDisplayStore.getState().local.desktop).toEqual(value);
  expect(transcriptDisplayStore.getState().storageWarning).toMatch(/may not survive restart/);
});

test("syncs only local values through BroadcastChannel and storage fallback", () => {
  vi.stubGlobal("BroadcastChannel", FakeBroadcastChannel);
  resetTranscriptDisplayStoreForTests();
  initTranscriptDisplay();
  const active = FakeBroadcastChannel.instances.at(-1)!;
  expect(active.name).toBe("evener.transcript-display.v1");

  const local = preset("activity");
  transcriptDisplayStore.getState().setLocal("desktop", local);
  expect(active.posted).toHaveLength(1);
  const own = active.posted[0] as Record<string, unknown>;
  expect(own.config).toBe(encodeLocalConfig(local));
  expect(own.fingerprint).toBe(encodeLocalConfig(local));

  active.emit(own);
  active.emit({ ...own, config: "malformed", fingerprint: "malformed" });
  expect(transcriptDisplayStore.getState().local.desktop).toEqual(local);

  const remote = preset("chat");
  const remoteMessage = {
    ...own,
    sourceId: "another-tab",
    config: encodeLocalConfig(remote),
    fingerprint: encodeLocalConfig(remote),
  };
  active.emit(remoteMessage);
  expect(transcriptDisplayStore.getState().local.desktop).toEqual(remote);

  window.dispatchEvent(new StorageEvent("storage", { key: desktopKey, newValue: "malformed" }));
  expect(transcriptDisplayStore.getState().local.desktop).toEqual(remote);
  window.dispatchEvent(new StorageEvent("storage", { key: desktopKey, newValue: encodeLocalConfig(preset("full")) }));
  expect(transcriptDisplayStore.getState().local.desktop).toEqual(preset("full"));
  transcriptDisplayStore.getState().applyHubChange({ layout: "desktop", revision: 1, config: preset("tools") });
  expect(active.posted).toHaveLength(1);
});
