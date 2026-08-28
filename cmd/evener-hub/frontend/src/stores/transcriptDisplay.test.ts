import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import {
  type CapturedTranscriptView,
  registerTranscriptView,
  resetTranscriptViewRegistryForTests,
} from "../panes/session/transcript/flow/transcriptViewRegistry";
import { WireError } from "../protocol/errors";
import { FakeClient } from "../protocol/testing/fakeClient";
import type { TranscriptDisplayPatchResponse } from "../protocol/types.gen";
import {
  encodeLocalConfig,
  makeTranscriptDisplayConfig,
  presetContent,
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

function deferred<T>(): {
  promise: Promise<T>;
  resolve(value: T): void;
} {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((next) => {
    resolve = next;
  });
  return { promise, resolve };
}

function viewSnapshot(id: string): CapturedTranscriptView {
  return {
    anchorId: `${id}-anchor`,
    anchorOffset: 18,
    normalizedOffset: 0.25,
    followingBottom: false,
    focusedEntryId: `${id}-entry`,
  };
}

beforeEach(() => {
  storage.clear();
  // @ts-expect-error MemoryStorage is the deterministic browser storage seam.
  globalThis.localStorage = storage;
  connectionStore.setState({ state: "idle", serverInfo: undefined, features: undefined, client: null });
  resetTranscriptDisplayStoreForTests();
  resetTranscriptViewRegistryForTests();
  initTranscriptDisplay();
});

afterEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, features: undefined, client: null });
  resetTranscriptDisplayStoreForTests();
  resetTranscriptViewRegistryForTests();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("effective transcript display state", () => {
  test("captures both mounted panes before a local publish and coalesces an identical fingerprint", () => {
    const events: string[] = [];
    const leftSnapshot = viewSnapshot("left");
    const rightSnapshot = viewSnapshot("right");
    const left = {
      id: "left",
      capture: vi.fn(() => {
        events.push("capture:left");
        return leftSnapshot;
      }),
      restore: vi.fn(() => events.push("restore:left")),
      announce: vi.fn(() => events.push("announce:left")),
    };
    const right = {
      id: "right",
      capture: vi.fn(() => {
        events.push("capture:right");
        return rightSnapshot;
      }),
      restore: vi.fn(() => events.push("restore:right")),
      announce: vi.fn(() => events.push("announce:right")),
    };
    const unregisterLeft = registerTranscriptView(left);
    const unregisterRight = registerTranscriptView(right);

    const next = preset("full");
    transcriptDisplayStore.getState().setLocal("desktop", next);
    transcriptDisplayStore.getState().setLocal("desktop", next);

    expect(events).toEqual([
      "capture:left",
      "capture:right",
      "restore:left",
      "restore:right",
      "announce:left",
      "announce:right",
    ]);
    expect(transcriptDisplayStore.getState().effective("desktop")).toEqual(next);
    unregisterLeft();
    unregisterRight();
  });

  test("does not capture or announce a hub update hidden by a local override", () => {
    const capture = vi.fn(() => viewSnapshot("pane"));
    const announce = vi.fn();
    const unregister = registerTranscriptView({
      id: "pane",
      capture,
      restore: vi.fn(),
      announce,
    });

    transcriptDisplayStore.getState().setLocal("desktop", preset("activity"));
    capture.mockClear();
    announce.mockClear();
    transcriptDisplayStore.getState().applyHubChange({ layout: "desktop", revision: 1, config: preset("chat") });

    expect(capture).not.toHaveBeenCalled();
    expect(announce).not.toHaveBeenCalled();
    unregister();
  });

  test("announces semantically effective changes even when the concise summary repeats", () => {
    const announce = vi.fn();
    const unregister = registerTranscriptView({
      id: "announcement-pane",
      capture: vi.fn(() => viewSnapshot("announcement-pane")),
      restore: vi.fn(),
      announce,
    });
    const timings = makeTranscriptDisplayConfig({ kind: "preset", level: "tools" }, { roundTimings: true });
    const tokens = makeTranscriptDisplayConfig({ kind: "preset", level: "tools" }, { tokenCounts: true });
    transcriptDisplayStore.getState().setLocal("desktop", timings);
    transcriptDisplayStore.getState().setLocal("desktop", tokens);
    expect(announce).toHaveBeenNthCalledWith(1, "Tools · 1 advanced");
    expect(announce).toHaveBeenNthCalledWith(2, "Tools · 1 advanced");
    unregister();
  });

  test("captures a breakpoint transition even when both layout configurations are identical", () => {
    const events: string[] = [];
    const unregister = registerTranscriptView({
      id: "pane",
      layout: "desktop",
      capture: vi.fn(() => {
        events.push("capture");
        return viewSnapshot("pane");
      }),
      restore: vi.fn(() => events.push("restore")),
      announce: vi.fn(() => events.push("announce")),
    });
    const sameConfig = preset("full");
    transcriptDisplayStore.getState().setLocal("desktop", sameConfig);
    transcriptDisplayStore.getState().setLocal("mobile", sameConfig);
    events.length = 0;

    transcriptDisplayStore.getState().setViewport("mobile");

    expect(events).toEqual(["capture", "restore"]);
    unregister();
  });

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

  test("rejects every malformed PATCH success without changing hub state or clearing the draft", async () => {
    const client = new FakeClient("ready");
    const confirmed = preset("tools");
    const requested = preset("activity");
    let response = { layout: "mobile", revision: 2, config: toWireConfig(requested) } as TranscriptDisplayPatchResponse;
    client.on("evener/settings/transcriptDisplay/get", () => ({
      desktop: { revision: 1, config: toWireConfig(confirmed) },
      mobile: { revision: 1, config: toWireConfig(shippedMobileConfig) },
    }));
    client.on("evener/settings/transcriptDisplay/patch", () => response);
    connectionStore.getState().connect(client);
    connectionStore.setState({ features: { ...(await client.connect()).features, transcriptDisplaySettings: true } });
    await transcriptDisplayStore.getState().refreshHubDefaults();

    const invalidResponses: unknown[] = [
      { layout: "mobile", revision: 2, config: toWireConfig(requested) },
      { layout: "desktop", revision: -1, config: toWireConfig(requested) },
      { layout: "desktop", revision: 1.5, config: toWireConfig(requested) },
      { layout: "desktop", revision: Number.MAX_SAFE_INTEGER + 1, config: toWireConfig(requested) },
      { layout: "desktop", revision: 0, config: toWireConfig(requested) },
      { layout: "desktop", revision: 3, config: toWireConfig(requested) },
      { layout: "desktop", revision: 2, config: toWireConfig(requested), extra: true },
      { layout: "desktop", revision: 2 },
      { layout: "desktop", revision: 2, config: { version: 1, content: { kind: "preset", level: "wat" } } },
      null,
      "not an object",
    ];
    for (const invalid of invalidResponses) {
      response = invalid as TranscriptDisplayPatchResponse;
      await expect(transcriptDisplayStore.getState().patchHubDefault("desktop", requested)).rejects.toThrow(
        "malformed",
      );
      expect(transcriptDisplayStore.getState().hub.desktop).toEqual({ revision: 1, config: confirmed });
      expect(transcriptDisplayStore.getState().drafts.desktop).toEqual(requested);
    }

    response = { layout: "desktop", revision: 1, config: toWireConfig(confirmed) };
    await transcriptDisplayStore.getState().patchHubDefault("desktop", confirmed);
    expect(transcriptDisplayStore.getState().drafts.desktop).toBeUndefined();
    expect(transcriptDisplayStore.getState().hub.desktop).toEqual({ revision: 1, config: confirmed });

    response = { layout: "desktop", revision: 2, config: toWireConfig(preset("full")) };
    await transcriptDisplayStore.getState().patchHubDefault("desktop", preset("full"));
    expect(transcriptDisplayStore.getState().hub.desktop).toEqual({ revision: 2, config: preset("full") });
  });

  test("never lets a fenced revision hint authorize a later active jump", async () => {
    const client = new FakeClient("ready");
    const confirmed = preset("tools");
    const firstDraft = preset("activity");
    const newestDraft = preset("full");
    const responses: Array<ReturnType<typeof deferred<TranscriptDisplayPatchResponse>>> = [];
    client.on("evener/settings/transcriptDisplay/get", () => ({
      desktop: { revision: 1, config: toWireConfig(confirmed) },
      mobile: { revision: 1, config: toWireConfig(shippedMobileConfig) },
    }));
    client.on("evener/settings/transcriptDisplay/patch", () => {
      const response = deferred<TranscriptDisplayPatchResponse>();
      responses.push(response);
      return response.promise;
    });
    connectionStore.getState().connect(client);
    connectionStore.setState({ features: { ...(await client.connect()).features, transcriptDisplaySettings: true } });
    await transcriptDisplayStore.getState().refreshHubDefaults();
    const fenced = transcriptDisplayStore.getState().patchHubDefault("desktop", firstDraft);
    const active = transcriptDisplayStore.getState().patchHubDefault("desktop", newestDraft);
    await expect.poll(() => responses).toHaveLength(2);
    responses[0]?.resolve({ layout: "desktop", revision: 1000, config: toWireConfig(firstDraft) });
    await fenced;
    responses[1]?.resolve({ layout: "desktop", revision: 1001, config: toWireConfig(newestDraft) });
    await expect(active).rejects.toThrow("malformed");
    expect(transcriptDisplayStore.getState().hub.desktop).toEqual({ revision: 1, config: confirmed });
    expect(transcriptDisplayStore.getState().drafts.desktop).toEqual(newestDraft);
  });

  test("rejects an advancing response whose canonical config differs from the request", async () => {
    const client = new FakeClient("ready");
    const confirmed = preset("tools");
    const requested = preset("full");
    client.on("evener/settings/transcriptDisplay/get", () => ({
      desktop: { revision: 1, config: toWireConfig(confirmed) },
      mobile: { revision: 1, config: toWireConfig(shippedMobileConfig) },
    }));
    client.on("evener/settings/transcriptDisplay/patch", () => ({
      layout: "desktop",
      revision: 2,
      config: toWireConfig(preset("activity")),
    }));
    connectionStore.getState().connect(client);
    connectionStore.setState({ features: { ...(await client.connect()).features, transcriptDisplaySettings: true } });
    await transcriptDisplayStore.getState().refreshHubDefaults();
    await expect(transcriptDisplayStore.getState().patchHubDefault("desktop", requested)).rejects.toThrow("malformed");
    expect(transcriptDisplayStore.getState().hub.desktop).toEqual({ revision: 1, config: confirmed });
    expect(transcriptDisplayStore.getState().drafts.desktop).toEqual(requested);
  });

  test.each(["desktop-first", "mobile-first"] as const)(
    "keeps cross-layout acknowledgements independent when %s settles",
    async (order) => {
      const client = new FakeClient("ready");
      const desktop = preset("activity");
      const mobile = preset("full");
      const desktopResponse = deferred<TranscriptDisplayPatchResponse>();
      let rejectMobile!: (error: Error) => void;
      const mobileResponse = new Promise<TranscriptDisplayPatchResponse>((_, reject) => {
        rejectMobile = reject;
      });
      client.on("evener/settings/transcriptDisplay/get", () => ({
        desktop: { revision: 1, config: toWireConfig(preset("tools")) },
        mobile: { revision: 1, config: toWireConfig(preset("intent")) },
      }));
      client.on("evener/settings/transcriptDisplay/patch", (params) =>
        params.layout === "desktop" ? desktopResponse.promise : mobileResponse,
      );
      connectionStore.getState().connect(client);
      connectionStore.setState({ features: { ...(await client.connect()).features, transcriptDisplaySettings: true } });
      await transcriptDisplayStore.getState().refreshHubDefaults();
      const desktopPatch = transcriptDisplayStore.getState().patchHubDefault("desktop", desktop);
      const mobilePatch = transcriptDisplayStore.getState().patchHubDefault("mobile", mobile);
      if (order === "desktop-first") {
        desktopResponse.resolve({ layout: "desktop", revision: 2, config: toWireConfig(desktop) });
        await desktopPatch;
        rejectMobile(new Error("mobile failed"));
      } else {
        rejectMobile(new Error("mobile failed"));
        await expect(mobilePatch).rejects.toThrow("mobile failed");
        desktopResponse.resolve({ layout: "desktop", revision: 2, config: toWireConfig(desktop) });
      }
      await expect(mobilePatch).rejects.toThrow("mobile failed");
      await desktopPatch;
      expect(transcriptDisplayStore.getState().hub.desktop?.config).toEqual(desktop);
      expect(transcriptDisplayStore.getState().hubErrors.desktop).toBeUndefined();
      expect(transcriptDisplayStore.getState().hubErrors.mobile).toBe("mobile failed");
    },
  );

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

  test("ignores a deferred refresh and notification after the active client closes", async () => {
    const client = new FakeClient("ready");
    const pending = deferred<{
      desktop: { revision: number; config: TranscriptDisplayConfigV1 };
      mobile: { revision: number; config: TranscriptDisplayConfigV1 };
    }>();
    client.on("evener/settings/transcriptDisplay/get", () => pending.promise);
    connectionStore.getState().connect(client);
    connectionStore.setState({ features: { ...(await client.connect()).features, transcriptDisplaySettings: true } });
    const refresh = transcriptDisplayStore.getState().refreshHubDefaults();
    client.emitStateChange("closed");
    client.emitNotification({
      method: "evener/settings/transcriptDisplay/changed",
      params: { layout: "desktop", revision: 9, config: toWireConfig(preset("full")) },
    });
    pending.resolve({
      desktop: { revision: 9, config: preset("full") },
      mobile: { revision: 9, config: shippedMobileConfig },
    });
    await refresh;
    expect(transcriptDisplayStore.getState().hub.desktop).toBeUndefined();
    expect(transcriptDisplayStore.getState().hubSupport).toBe("unknown");
  });

  test("ignores an older ready-generation refresh and PATCH completion", async () => {
    const client = new FakeClient("idle");
    const notificationRegistration = vi.spyOn(client, "onNotification");
    const firstRefresh = deferred<{
      desktop: { revision: number; config: TranscriptDisplayConfigV1 };
      mobile: { revision: number; config: TranscriptDisplayConfigV1 };
    }>();
    const pendingPatch = deferred<{
      layout: "desktop";
      revision: number;
      config: TranscriptDisplayConfigV1;
    }>();
    let getCalls = 0;
    client.on("evener/settings/transcriptDisplay/get", () => {
      getCalls += 1;
      if (getCalls === 1) return firstRefresh.promise;
      return {
        desktop: { revision: 2, config: preset("activity") },
        mobile: { revision: 2, config: shippedMobileConfig },
      };
    });
    client.on("evener/settings/transcriptDisplay/patch", () => pendingPatch.promise);
    connectionStore.getState().connect(client);
    connectionStore.setState({ features: { ...(await client.connect()).features, transcriptDisplaySettings: true } });
    client.emitReady();
    const firstGeneration = transcriptDisplayStore.getState().refreshHubDefaults();
    const oldNotificationHandler = notificationRegistration.mock.calls[0]?.[0];
    client.emitStateChange("reconnecting");
    const secondGeneration = waitForHubRevision(2);
    client.emitReady();
    await secondGeneration;
    oldNotificationHandler?.({
      method: "evener/settings/transcriptDisplay/changed",
      params: { layout: "desktop", revision: 9, config: toWireConfig(preset("full")) },
    });
    expect(transcriptDisplayStore.getState().hub.desktop).toEqual({ revision: 2, config: preset("activity") });
    const patch = transcriptDisplayStore.getState().patchHubDefault("desktop", preset("full"));
    client.emitStateChange("reconnecting");
    firstRefresh.resolve({
      desktop: { revision: 1, config: preset("chat") },
      mobile: { revision: 1, config: shippedMobileConfig },
    });
    pendingPatch.resolve({ layout: "desktop", revision: 3, config: preset("full") });
    await firstGeneration;
    await patch;
    expect(transcriptDisplayStore.getState().hub.desktop).toEqual({ revision: 2, config: preset("activity") });
    expect(transcriptDisplayStore.getState().drafts.desktop).toEqual(preset("full"));
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

test("preserves an explicit Tools-equivalent Custom through local and hub persistence", async () => {
  const custom = makeTranscriptDisplayConfig({ kind: "custom", ...presetContent("tools") });
  transcriptDisplayStore.getState().setLocal("desktop", custom);
  expect(transcriptDisplayStore.getState().local.desktop?.content.kind).toBe("custom");
  expect(transcriptDisplayStore.getState().effective("desktop").content.kind).toBe("custom");
  expect(JSON.parse(storage.getItem(desktopKey) ?? "{}").content.kind).toBe("custom");

  resetTranscriptDisplayStoreForTests();
  initTranscriptDisplay();
  expect(transcriptDisplayStore.getState().local.desktop?.content.kind).toBe("custom");

  const client = new FakeClient("ready");
  client.on("evener/settings/transcriptDisplay/get", () => ({
    desktop: { revision: 1, config: toWireConfig(custom) },
    mobile: { revision: 1, config: toWireConfig(shippedMobileConfig) },
  }));
  connectionStore.getState().connect(client);
  connectionStore.setState({ features: { ...(await client.connect()).features, transcriptDisplaySettings: true } });
  await transcriptDisplayStore.getState().refreshHubDefaults();
  expect(transcriptDisplayStore.getState().hub.desktop?.config.content.kind).toBe("custom");

  client.on("evener/settings/transcriptDisplay/patch", () => ({
    layout: "desktop",
    revision: 2,
    config: toWireConfig(custom),
  }));
  const canonical = await transcriptDisplayStore.getState().patchHubDefault("desktop", custom);
  expect(canonical.config.content.kind).toBe("custom");
  expect(transcriptDisplayStore.getState().hub.desktop?.config.content.kind).toBe("custom");
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

  const desktopEdited = makeTranscriptDisplayConfig(
    { kind: "preset", level: "full" },
    { roundTimings: true, promptEvents: true },
  );
  const mobileEdited = makeTranscriptDisplayConfig(
    { kind: "preset", level: "chat" },
    { tokenCounts: true, estimatedCost: true, hookExits: "all" },
  );
  transcriptDisplayStore.getState().setLocal("desktop", desktopEdited);
  expect(legacyKeys.map((key) => storage.getItem(key))).toEqual(["1", "0", "0", "0", "1", "0"]);
  transcriptDisplayStore.getState().setLocal("mobile", mobileEdited);
  expect(legacyKeys.map((key) => storage.getItem(key))).toEqual(["0", "1", "1", "0", "0", "1"]);
});

test("clearLocal removes the persisted key, broadcasts null, follows the hub, and dual-writes its flags", () => {
  vi.stubGlobal("BroadcastChannel", FakeBroadcastChannel);
  resetTranscriptDisplayStoreForTests();
  initTranscriptDisplay();
  const active = FakeBroadcastChannel.instances.at(-1);
  if (active === undefined) throw new Error("test BroadcastChannel was not created");
  const hub = makeTranscriptDisplayConfig(
    { kind: "preset", level: "activity" },
    { roundTimings: true, promptEvents: true },
  );
  transcriptDisplayStore.getState().applyHubChange({ layout: "desktop", revision: 4, config: hub });
  transcriptDisplayStore.getState().setLocal("desktop", preset("full"));
  transcriptDisplayStore.getState().clearLocal("desktop");

  expect(storage.getItem(desktopKey)).toBeNull();
  expect(transcriptDisplayStore.getState().local.desktop).toBeUndefined();
  expect(transcriptDisplayStore.getState().effective("desktop")).toEqual(hub);
  expect(active.posted.at(-1)).toMatchObject({ config: null, fingerprint: null, layout: "desktop" });
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

test("routes a storage reset through capture/restore, while masking a non-current layout", () => {
  const events: string[] = [];
  const capture = vi.fn(() => {
    events.push("capture");
    return viewSnapshot("pane");
  });
  const restore = vi.fn(() => events.push("restore"));
  const announce = vi.fn(() => events.push("announce"));
  const unregister = registerTranscriptView({
    id: "pane",
    capture,
    restore,
    announce,
  });

  transcriptDisplayStore.getState().setLocal("desktop", preset("full"));
  events.length = 0;
  capture.mockClear();
  restore.mockClear();
  announce.mockClear();
  window.dispatchEvent(new StorageEvent("storage", { key: desktopKey, newValue: null }));

  expect(events).toEqual(["capture", "restore", "announce"]);
  expect(transcriptDisplayStore.getState().local.desktop).toBeUndefined();

  transcriptDisplayStore.getState().setLocal("mobile", preset("chat"));
  events.length = 0;
  capture.mockClear();
  restore.mockClear();
  announce.mockClear();
  window.dispatchEvent(new StorageEvent("storage", { key: mobileKey, newValue: null }));

  expect(events).toEqual([]);
  unregister();
});
