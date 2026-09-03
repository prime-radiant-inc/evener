import { afterEach, beforeAll, beforeEach, describe, expect, test, vi } from "vitest";
import { FakeClient } from "../protocol/testing/fakeClient";
import type {
  AttentionChanged,
  InitializeResponse,
  NavigationReadParams,
  NavigationReadResponse,
  NavigationSessionSummary,
} from "../protocol/types.gen";
import { resetWorkspaceStoreForTests } from "../shell/workspace";
import { connectionStore } from "../stores/connection";
import { initNavigation, navigationStore, resetNavigationStoreForTests } from "../stores/navigation/store";
import { capability, manifest } from "../stores/navigation/testing";
import { prefsStore, resetPrefsStoreForTests } from "../stores/prefs";
import { initNotifications, resetNotificationsForTests } from "./index";
import { resetLeaderForTests, setLeaderForTests } from "./leader";

const navigationCapability = capability;
const navigationManifest = (generationId = "generation_test") => manifest({ generation_id: generationId });
const navigationInitialize = (generationId = "generation_test"): InitializeResponse => ({
  serverInfo: { name: "fake", version: "1" },
  protocolVersion: "evener-appwire-v3",
  sourceId: "fake",
  features: {
    threadList: false,
    threadTurnsList: false,
    turnStart: false,
    turnSteer: false,
    threadClear: false,
    threadShutdown: false,
    forkFromTurn: false,
    tasks: false,
    transcriptList: false,
    modelList: false,
    directoryComplete: false,
    auth: false,
  },
  navigation: navigationCapability(generationId),
});

const navigationReadResponse = (generationId = "generation_test"): NavigationReadResponse => ({
  status: "ok",
  generationId,
  revision: 1,
  etag: '"manifest"',
  data: navigationManifest(generationId),
});

function scriptNavigationManifest(client: FakeClient, generationId: string | (() => string) = "generation_test"): void {
  client.on("evener/navigation/read", (params: NavigationReadParams) => {
    expect(params).toEqual({ resource: "manifest" });
    return navigationReadResponse(typeof generationId === "function" ? generationId() : generationId);
  });
}

const flushMicrotasks = async (): Promise<void> => {
  for (let i = 0; i < 12; i++) await Promise.resolve();
};

// localStorage shim (Node shadows jsdom's; see prefs.test.ts's identical note)
// so the prefs setters this file drives actually round-trip.
class MemoryStorage {
  private store = new Map<string, string>();
  getItem(key: string): string | null {
    return this.store.has(key) ? (this.store.get(key) ?? null) : null;
  }
  setItem(key: string, value: string): void {
    this.store.set(key, String(value));
  }
  removeItem(key: string): void {
    this.store.delete(key);
  }
  clear(): void {
    this.store.clear();
  }
}
beforeAll(() => {
  // @ts-expect-error see MemoryStorage's own comment
  globalThis.localStorage = new MemoryStorage();
});

function node(ref: string, state: string, askPending = false): NavigationSessionSummary {
  return {
    ref,
    host_id: "local",
    session_id: ref.replace(/^local:/, ""),
    title: ref,
    project: "proj",
    state,
    kind: "session",
    live: true,
    ask_pending: askPending,
    children: [],
  };
}

function attentionFromNodes(nodes: NavigationSessionSummary[]) {
  let needsYou = 0;
  let error = 0;
  for (const n of nodes) {
    if (n.state === "errored") error += 1;
    else needsYou += 1;
  }
  return {
    changed: nodes.map((n) => ({
      threadId: n.ref,
      title: n.title,
      project: n.project ?? "",
      level: n.state === "errored" ? "error" : "needs_you",
      askPending: n.ask_pending === true,
      prevLevel: "idle",
    })),
    summary: { needsYou, error, working: 0 },
  };
}

const tick = () => new Promise<void>((r) => setTimeout(r, 0));

// --- browser-API doubles: count fires, don't re-verify their internals ---
class FakeNotification {
  static permission: NotificationPermission = "granted";
  static instances: FakeNotification[] = [];
  onclick: (() => void) | null = null;
  constructor(readonly title: string) {
    FakeNotification.instances.push(this);
  }
}
class FakeAudioContext {
  static instances: FakeAudioContext[] = [];
  destination = {};
  close() {}
  createOscillator() {
    return { frequency: { value: 0 }, connect() {}, start() {}, stop() {} };
  }
  createGain() {
    return { gain: { value: 0 }, connect() {} };
  }
  constructor() {
    FakeAudioContext.instances.push(this);
  }
}

function fires(): { os: number; sound: number } {
  return { os: FakeNotification.instances.length, sound: FakeAudioContext.instances.length };
}

function faviconHref(): string {
  return document.querySelector<HTMLLinkElement>("link[rel='icon']")?.getAttribute("href") ?? "";
}

function setFocused(value: boolean): void {
  vi.spyOn(document, "hasFocus").mockReturnValue(value);
}

beforeEach(() => {
  resetNotificationsForTests();
  resetNavigationStoreForTests();
  resetLeaderForTests();
  // baseTitle() (notifications/title.ts) reads workspaceStore's focused pane
  // - workspaceStore is a module singleton shared with every other file in
  // the worker, so this file's own "no focused pane -> 'evener hub'" title
  // assertions need a pristine workspace regardless of what an earlier file
  // left focused.
  resetWorkspaceStoreForTests();
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  localStorage.clear();
  resetPrefsStoreForTests();
  FakeNotification.instances = [];
  FakeNotification.permission = "granted";
  FakeAudioContext.instances = [];
  setFocused(false); // unfocused by default (edge-fire is allowed unless a test focuses)
  // No Web Locks in this env ⇒ electLeader() elects this tab leader
  // deterministically during init; a test wanting a follower overrides with
  // setLeaderForTests(false) AFTER boot.
  Object.defineProperty(navigator, "locks", { value: undefined, configurable: true });
  vi.stubGlobal("Notification", FakeNotification);
  vi.stubGlobal("AudioContext", FakeAudioContext);
});
afterEach(() => {
  resetNotificationsForTests();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

// Turn on both loud channels (prefs read at fire time, so setting them any
// time before the triggering snapshot is enough).
function armPrefs(loudScope: "asks" | "all" = "all"): void {
  prefsStore.getState().setNotification("os", true);
  prefsStore.getState().setNotification("sound", true);
  prefsStore.getState().setNotificationsLoudScope(loudScope);
}

// Boot the engine with `baseline` as its first attention snapshot, then settle
// so that snapshot is the established baseline (electLeader ⇒ leader = true).
// The navigation store must reach mode "v1" (a capability-advertising client +
// a valid manifest fetch) before the baseline is delivered, because
// onNavigationAttention returns early while mode !== "v1" — so the baseline
// is delivered through the client's attention notification after the manifest
// settles, establishing prevNavigationAttention for later transition detection.
//
// onNavigationAttention skips a first snapshot whose changed list is empty
// (the guard `changed.length === 0 && prevNavigationAttention === null`), so
// an empty baseline is delivered with a single idle sentinel entry that the
// loop deletes from the map, leaving prevNavigationAttention as an empty Map.
async function boot(baseline: {
  changed: unknown[];
  summary: { needsYou: number; error: number; working: number };
}): Promise<FakeClient> {
  // Use the already-connected client if one exists (reconnect tests pre-wire
  // their own FakeClient so they can drive emitStateChange/emitReady on it);
  // otherwise create one with a navigation capability.
  let client = connectionStore.getState().client as FakeClient | null;
  if (!client) {
    client = new FakeClient("ready");
    client.scriptConnect(() => ({
      serverInfo: { name: "fake", version: "1" },
      protocolVersion: "evener-appwire-v3",
      sourceId: "fake",
      features: {} as never,
      navigation: navigationCapability(),
    }));
    connectionStore.getState().connect(client);
  }
  scriptNavigationManifest(client);
  initNavigation(client, navigationCapability());
  initNotifications();
  await flushMicrotasks();
  const changed = (
    baseline.changed.length > 0
      ? baseline.changed
      : [{ threadId: "__baseline__", title: "", project: "", level: "idle", askPending: false, prevLevel: "" }]
  ) as AttentionChanged[];
  client.emitNotification({
    method: "evener/attention/changed",
    params: { changed, summary: baseline.summary },
  });
  await tick();
  return client;
}

describe("initNotifications lifecycle", () => {
  test("supported v1 boots navigation through the typed AppWire read", async () => {
    const client = new FakeClient("ready");
    scriptNavigationManifest(client);
    client.scriptConnect(() => ({
      serverInfo: { name: "fake", version: "1" },
      protocolVersion: "evener-appwire-v3",
      sourceId: "fake",
      features: {} as never,
      navigation: navigationCapability(),
    }));
    connectionStore.getState().connect(client);
    initNotifications();
    await flushMicrotasks();

    expect(navigationStore.getState().mode).toBe("v1");
    expect(client.calls).toEqual([{ method: "evener/navigation/read", params: { resource: "manifest" } }]);
  });

  test("absent capability is an error state, never fetches navigation", async () => {
    const client = new FakeClient("ready");
    connectionStore.getState().connect(client);
    initNotifications();
    await flushMicrotasks();

    expect(navigationStore.getState().mode).toBe("error");
    expect(client.calls).toEqual([]);
    expect(navigationStore.getState().manifest).toBeNull();
  });

  test("unsupported capability is an explicit protocol error, not a tree fallback", async () => {
    const client = new FakeClient("ready");
    client.scriptConnect(() => ({
      serverInfo: { name: "fake", version: "1" },
      protocolVersion: "evener-appwire-v3",
      sourceId: "fake",
      features: {} as never,
      navigation: navigationCapability("generation_test", 2),
    }));
    connectionStore.getState().connect(client);
    initNotifications();
    await flushMicrotasks();

    expect(navigationStore.getState().mode).toBe("error");
    expect(navigationStore.getState().protocolError?.message).toContain("unsupported navigation capability version 2");
    expect(client.calls).toEqual([]);
    expect(navigationStore.getState().manifest).toBeNull();
  });

  test("is idempotent (safe to call repeatedly)", async () => {
    await boot(attentionFromNodes([]));
    expect(() => initNotifications()).not.toThrow();
  });

  // The navigation manifest is this engine's whole data source, and nothing
  // else guarantees a read: the rail is the app's only other mount-time
  // fetcher and it does not mount at all on a mobile boot (it lives inside the
  // tree drawer's sheet, which renders null while closed). AppShell calls
  // initNotifications() at module evaluation, so this baseline fetch is what
  // makes the manifest arrive on every host. Kata bbsv mis-read its absence as
  // the cause of mobile deep links being discarded; it is present, and the
  // shell relies on it.
  test("reads the manifest after the handshake selects v1 mode", async () => {
    const client = await boot(attentionFromNodes([]));

    expect(client.calls).toContainEqual({ method: "evener/navigation/read", params: { resource: "manifest" } });
    expect(navigationStore.getState().manifest?.data).not.toBeNull();
  });

  // kata p5w9. The baseline's duty is "a tree exists", not "fetch again" - so
  // where the rail HAS already loaded one (the desktop boot, where both run),
  // it must not issue a second identical read milliseconds after the first.
  test("does not re-read navigation that is already loaded", async () => {
    const client = await boot(attentionFromNodes([]));
    client.calls.length = 0;
    navigationStore.setState({ attention: attentionFromNodes([]) });

    initNotifications();
    await tick();

    expect(client.calls).toEqual([]);
  });
});

describe("counts apply unconditionally", () => {
  test("v1 attention owns counts and edge fires without tree or extra navigation reads", async () => {
    armPrefs("all");
    const client = new FakeClient("ready");
    scriptNavigationManifest(client);
    initNavigation(client, navigationCapability());
    initNotifications();
    await flushMicrotasks();
    client.calls.length = 0;

    client.emitNotification({
      method: "evener/attention/changed",
      params: {
        changed: [{ threadId: "a", title: "A", project: "", level: "needs_you", askPending: true, prevLevel: "" }],
        summary: { needsYou: 1, error: 0, working: 0 },
      },
    });
    expect(document.title).toBe("(1) evener hub");
    expect(fires()).toEqual({ os: 0, sound: 0 }); // first notification establishes baseline
    client.emitNotification({
      method: "evener/attention/changed",
      params: {
        changed: [{ threadId: "b", title: "B", project: "", level: "needs_you", askPending: true, prevLevel: "" }],
        summary: { needsYou: 2, error: 0, working: 0 },
      },
    });

    expect(document.title).toBe("(2) evener hub");
    expect(fires()).toEqual({ os: 1, sound: 1 });
    navigationStore.setState({ attention: attentionFromNodes([node("v1-extra", "errored")]) });
    expect(document.title).toBe("(1) evener hub");
    expect(client.calls).toEqual([]);
  });

  test("losing the capability clears v1 attention and enters error mode", async () => {
    prefsStore.getState().setNotification("title", true);
    const client = new FakeClient("ready");
    scriptNavigationManifest(client);
    initNavigation(client, navigationCapability());
    initNotifications();
    await flushMicrotasks();
    client.emitNotification({
      method: "evener/attention/changed",
      params: {
        changed: [
          { threadId: "a", title: "A", project: "", level: "needs_you", askPending: true, prevLevel: "" },
          { threadId: "b", title: "B", project: "", level: "error", askPending: false, prevLevel: "" },
        ],
        summary: { needsYou: 1, error: 1, working: 0 },
      },
    });
    expect(document.title).toBe("(2) evener hub");

    initNavigation(client, null);
    await flushMicrotasks();

    expect(navigationStore.getState().mode).toBe("error");
    expect(navigationStore.getState().attention.summary).toBeNull();
    expect(document.title).toBe("evener hub");
  });

  test("v1 attention removes downgraded entries so a later escalation is a new edge", async () => {
    armPrefs("all");
    const client = new FakeClient("ready");
    scriptNavigationManifest(client);
    initNavigation(client, navigationCapability());
    initNotifications();
    await flushMicrotasks();

    const change = (level: string, needsYou: number) =>
      client.emitNotification({
        method: "evener/attention/changed",
        params: {
          changed: [{ threadId: "a", title: "A", project: "", level, askPending: true, prevLevel: "" }],
          summary: { needsYou, error: 0, working: level === "working" ? 1 : 0 },
        },
      });
    change("needs_you", 1);
    change("working", 0);
    expect(fires()).toEqual({ os: 0, sound: 0 });
    change("needs_you", 1);
    expect(fires()).toEqual({ os: 1, sound: 1 });
  });

  test("title + favicon update on a tree change even focused, even non-leader", async () => {
    prefsStore.getState().setNotification("title", true);
    prefsStore.getState().setNotification("favicon", true);
    await boot(attentionFromNodes([]));
    setFocused(true); // focused
    setLeaderForTests(false); // non-leader
    navigationStore.setState({
      attention: attentionFromNodes([node("local:a", "awaiting"), node("local:b", "errored")]),
    });
    expect(document.title).toBe("(2) evener hub");
    expect(faviconHref()).toContain("%23f7768e"); // error dot
  });
});

describe("baseline suppression (the reload trap)", () => {
  test("the first snapshot never fires, even with attention already present", async () => {
    armPrefs("all");
    await boot(attentionFromNodes([node("local:a", "awaiting", true), node("local:e", "errored")]));
    expect(fires()).toEqual({ os: 0, sound: 0 });
  });
});

describe("edge-fire", () => {
  test("a new needs_you entry after the baseline fires OS + sound", async () => {
    armPrefs("all");
    await boot(attentionFromNodes([]));
    navigationStore.setState({ attention: attentionFromNodes([node("local:a", "awaiting")]) });
    expect(fires()).toEqual({ os: 1, sound: 1 });
  });

  test("focused document suppresses the fire", async () => {
    armPrefs("all");
    await boot(attentionFromNodes([]));
    setFocused(true);
    navigationStore.setState({ attention: attentionFromNodes([node("local:a", "awaiting")]) });
    expect(fires()).toEqual({ os: 0, sound: 0 });
  });

  test("a non-leader tab does not fire", async () => {
    armPrefs("all");
    await boot(attentionFromNodes([]));
    setLeaderForTests(false);
    navigationStore.setState({ attention: attentionFromNodes([node("local:a", "awaiting")]) });
    expect(fires()).toEqual({ os: 0, sound: 0 });
  });

  test("os and sound gate independently", async () => {
    prefsStore.getState().setNotification("os", true); // sound stays OFF
    await boot(attentionFromNodes([]));
    // an error transition fires under the default "asks" scope
    navigationStore.setState({ attention: attentionFromNodes([node("local:e", "errored")]) });
    expect(fires()).toEqual({ os: 1, sound: 0 });
  });
});

describe("loudScope", () => {
  test("asks: a plain your-move needs_you is silent; an ask fires", async () => {
    armPrefs("asks");
    await boot(attentionFromNodes([]));
    navigationStore.setState({ attention: attentionFromNodes([node("local:a", "awaiting", false)]) });
    expect(fires()).toEqual({ os: 0, sound: 0 });
    navigationStore.setState({
      attention: attentionFromNodes([node("local:a", "awaiting", false), node("local:b", "awaiting", true)]),
    });
    expect(fires()).toEqual({ os: 1, sound: 1 });
  });

  test("all: a plain your-move needs_you fires", async () => {
    armPrefs("all");
    await boot(attentionFromNodes([]));
    navigationStore.setState({ attention: attentionFromNodes([node("local:a", "awaiting", false)]) });
    expect(fires()).toEqual({ os: 1, sound: 1 });
  });
});

// 2026-08-14: title flipped to ON by default (docs/web-ui/decisions.md);
// favicon/os/sound stay at the wave-7 all-OFF floor. This block now proves
// BOTH halves of that split default, not just the OFF one - a regression
// either direction (title reverting to OFF, or a favicon/os/sound default
// creeping to ON) should fail here.
describe("shipped defaults (title ON, favicon/os/sound OFF)", () => {
  test("title counts unconditionally by default; favicon/os/sound stay off", async () => {
    // leader + unfocused (defaults) — ONLY the favicon/os/sound OFF prefs hold anything back.
    await boot(attentionFromNodes([node("local:a", "awaiting", true), node("local:e", "errored")]));
    expect(document.title).toBe("(2) evener hub"); // title default ON: count prefix present
    expect(faviconHref()).not.toContain("%23f7768e"); // favicon still OFF by default: no error dot
    // a fresh transition still fires nothing while os/sound stay OFF
    navigationStore.setState({
      attention: attentionFromNodes([
        node("local:a", "awaiting", true),
        node("local:e", "errored"),
        node("local:f", "errored"),
      ]),
    });
    expect(fires()).toEqual({ os: 0, sound: 0 });
  });
});

describe("reconnect re-baselines silently", () => {
  test("equal-sequence reconnect skips navigation read and a new generation reloads exactly once", async () => {
    const client = new FakeClient("ready");
    let generation = "generation_test";
    client.scriptConnect(() => navigationInitialize(generation));
    scriptNavigationManifest(client, () => generation);
    initNavigation(client);
    await flushMicrotasks();
    expect(client.calls).toHaveLength(1);

    client.emitStateChange("reconnecting");
    client.emitReady();
    await flushMicrotasks();
    expect(client.calls).toHaveLength(1); // equal sequence does not reload

    generation = "generation_next";
    client.emitStateChange("reconnecting");
    client.emitReady(navigationInitialize(generation));
    await flushMicrotasks();
    expect(client.calls).toHaveLength(2); // one reset reload, not reset plus another read
    expect(client.calls.slice(1)).toEqual([{ method: "evener/navigation/read", params: { resource: "manifest" } }]);
    expect(navigationStore.getState().clientGenerationID).toBe("generation_next");
    expect(client.calls.every(({ method }) => method === "evener/navigation/read")).toBe(true);
  });

  test("stale client connect and notifications cannot mutate the active generation", async () => {
    let releaseOld!: (value: ReturnType<typeof navigationCapability>) => void;
    const old = new FakeClient("ready");
    old.scriptConnect(() =>
      new Promise<ReturnType<typeof navigationCapability>>((resolve) => {
        releaseOld = resolve;
      }).then((cap) => ({
        serverInfo: { name: "old", version: "1" },
        protocolVersion: "evener-appwire-v3",
        sourceId: "old",
        features: {} as never,
        navigation: cap,
      })),
    );
    const active = new FakeClient("ready");
    initNavigation(old);
    initNavigation(active, navigationCapability("active"));
    await flushMicrotasks();
    expect(navigationStore.getState().clientGenerationID).toBe("active");

    releaseOld(navigationCapability("stale"));
    old.emitNotification({
      method: "evener/attention/changed",
      params: {
        changed: [{ threadId: "stale", title: "stale", project: "", level: "error", askPending: false, prevLevel: "" }],
        summary: { needsYou: 0, error: 9, working: 0 },
      },
    });
    await flushMicrotasks();
    expect(navigationStore.getState().clientGenerationID).toBe("active");
    expect(navigationStore.getState().attention.summary).toBeNull();
  });

  test("attention that appeared across a reconnect does not re-alert", async () => {
    armPrefs("all");
    const fake = new FakeClient("ready");
    fake.scriptConnect(() => ({
      serverInfo: { name: "fake", version: "1" },
      protocolVersion: "evener-appwire-v3",
      sourceId: "fake",
      features: {} as never,
      navigation: navigationCapability(),
    }));
    connectionStore.getState().connect(fake);
    await boot(attentionFromNodes([node("local:a", "awaiting")]));
    expect(fires()).toEqual({ os: 0, sound: 0 }); // baseline

    // The equal-sequence reconnect does not refresh navigation. The server
    // delivers an attention snapshot that GAINED local:b in the gap.
    fake.emitStateChange("reconnecting");
    fake.emitReady(navigationInitialize());
    await tick();
    fake.emitNotification({
      method: "evener/attention/changed",
      params: {
        changed: [
          { threadId: "local:a", title: "local:a", project: "", level: "needs_you", askPending: false, prevLevel: "" },
          { threadId: "local:b", title: "local:b", project: "", level: "needs_you", askPending: false, prevLevel: "" },
        ],
        summary: { needsYou: 2, error: 0, working: 0 },
      },
    });
    await tick();
    expect(fires()).toEqual({ os: 0, sound: 0 }); // silent re-baseline, not a fresh alert

    // ...but a genuinely new transition AFTER the reconnect still fires.
    navigationStore.setState({
      attention: attentionFromNodes([
        node("local:a", "awaiting"),
        node("local:b", "awaiting"),
        node("local:c", "awaiting"),
      ]),
    });
    expect(fires()).toEqual({ os: 1, sound: 1 });
  });

  // kata p5w9. Only a TRANSITION into "ready" is a (re)connection. AppShell
  // publishes serverInfo through connectionStore once its own connect()
  // promise resolves, which is an ordinary store change with the state still
  // "ready" - reading that as a reconnect made every boot issue an extra,
  // pointless manifest read (and re-baseline the attention snapshot for no
  // reason). The kata's own probe saw the third call and put it down to the
  // FakeClient; it is the real boot sequence, and it happens in the browser
  // too.
  test("a serverInfo update on an already-ready connection is not read as a reconnect", async () => {
    initNotifications();
    await tick();
    const client = new FakeClient("ready");
    scriptNavigationManifest(client);
    client.scriptConnect(() => ({
      serverInfo: { name: "fake", version: "1" },
      protocolVersion: "evener-appwire-v3",
      sourceId: "fake",
      features: {} as never,
      navigation: navigationCapability(),
    }));
    connectionStore.getState().connect(client); // the initial connect
    await tick();
    const callsAfterConnect = client.calls.length;

    connectionStore.setState({ serverInfo: { name: "evener-hub", version: "1.0.0" } });
    await tick();

    expect(client.calls.length).toBe(callsAfterConnect);
  });
});
