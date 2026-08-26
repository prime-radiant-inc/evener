import { afterEach, beforeAll, beforeEach, describe, expect, test, vi } from "vitest";
import { FakeClient } from "../protocol/testing/fakeClient";
import { resetWorkspaceStoreForTests } from "../shell/workspace";
import { connectionStore } from "../stores/connection";
import { initNavigation, navigationStore } from "../stores/navigation/store";
import { prefsStore, resetPrefsStoreForTests } from "../stores/prefs";
import { resetTreeStoreForTests, type TreeNode, type TreeResponse, treeStore } from "../stores/tree";
import { initNotifications, resetNotificationsForTests } from "./index";
import { resetLeaderForTests, setLeaderForTests } from "./leader";

const navigationCapability = (generationId = "generation_test", version = 1) => ({
  version,
  generationId,
  sequence: 0,
});

const navigationManifest = (generationId = "generation_test") =>
  new Response(
    JSON.stringify({
      generation_id: generationId,
      revision: 1,
      sources: [],
      attentionSummary: { needsYou: 0, error: 0, working: 0 },
      sections: { live: { count: 0 }, needs_you: { count: 0 }, pin_sections: { count: 0 } },
      catalogs: { projects: { count: 0 }, archived_projects: { count: 0 }, test_runs: { count: 0 } },
    }),
    {
      headers: {
        "content-type": "application/json",
        "X-Evener-Navigation-Generation": generationId,
        "X-Evener-Navigation-Revision": "1",
        etag: '"manifest"',
      },
    },
  );

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

function node(ref: string, state: string, askPending = false): TreeNode {
  return {
    row_id: `needsyou:${ref}`,
    ref,
    host_id: "local",
    session_id: ref.replace(/^local:/, ""),
    title: ref,
    project: "proj",
    state,
    kind: "session",
    tier: "needsyou",
    live: true,
    ask_pending: askPending,
    children: [],
  };
}

function treeOf(nodes: TreeNode[], working = 0): TreeResponse {
  let needsYou = 0;
  let error = 0;
  for (const n of nodes) {
    if (n.state === "errored") error += 1;
    else needsYou += 1;
  }
  return {
    generated_at: "2026-01-01T00:00:00Z",
    sources: [],
    live: [],
    needs_you: nodes,
    pin_sections: [],
    projects: [],
    archived_projects: [],
    test_runs: [],
    attentionSummary: { needsYou, error, working },
  };
}

function jsonResponse(body: unknown): Response {
  return { ok: true, status: 200, statusText: "OK", json: () => Promise.resolve(body) } as Response;
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

let fetchMock: ReturnType<typeof vi.fn>;

beforeEach(() => {
  resetNotificationsForTests();
  resetTreeStoreForTests();
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
  fetchMock = vi.fn().mockResolvedValue(jsonResponse(treeOf([])));
  vi.stubGlobal("fetch", fetchMock);
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

// Boot the engine with `baseline` as its first (fetched) snapshot, then settle
// so that snapshot is the established baseline (electLeader ⇒ leader = true).
async function boot(baseline: TreeResponse): Promise<void> {
  fetchMock.mockResolvedValueOnce(jsonResponse(baseline));
  if (!connectionStore.getState().client) connectionStore.getState().connect(new FakeClient("ready"));
  initNotifications();
  await tick();
}

describe("initNotifications lifecycle", () => {
  test("supported v1 boots navigation and never fetches the legacy tree", async () => {
    fetchMock.mockImplementation(async (input: RequestInfo | URL) => {
      expect(input).toBe("/api/navigation");
      return navigationManifest();
    });
    const client = new FakeClient("ready");
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
    expect(fetchMock.mock.calls.map(([url]) => url)).toEqual(["/api/navigation"]);
  });

  test("absent capability uses exactly the legacy tree and never navigation", async () => {
    const client = new FakeClient("ready");
    connectionStore.getState().connect(client);
    initNotifications();
    await flushMicrotasks();

    expect(navigationStore.getState().mode).toBe("legacy");
    expect(fetchMock.mock.calls.map(([url]) => url)).toEqual(["/api/tree"]);
    expect(treeStore.getState().tree).not.toBeNull();
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
    expect(fetchMock).not.toHaveBeenCalled();
    expect(treeStore.getState().tree).toBeNull();
  });

  test("is idempotent (safe to call repeatedly)", async () => {
    await boot(treeOf([]));
    expect(() => initNotifications()).not.toThrow();
  });

  // The tree is this engine's whole data source, and nothing else guarantees a
  // fetch: the rail is the app's only other mount-time fetcher and it does not
  // mount at all on a mobile boot (it lives inside the tree drawer's sheet,
  // which renders null while closed). AppShell calls initNotifications() at
  // module evaluation, so this baseline fetch is what makes the tree arrive on
  // every host. Kata bbsv mis-read its absence as the cause of mobile deep
  // links being discarded; it is present, and the shell relies on it.
  test("fetches the tree after the handshake selects legacy mode", async () => {
    await boot(treeOf([]));

    expect(fetchMock).toHaveBeenCalledWith("/api/tree", expect.anything());
    expect(treeStore.getState().tree).not.toBeNull();
  });

  // kata p5w9. The baseline's duty is "a tree exists", not "fetch again" - so
  // where the rail HAS already loaded one (the desktop boot, where both run),
  // it must not issue a second identical GET milliseconds after the first.
  test("does not re-fetch a tree that is already loaded", async () => {
    treeStore.setState({ tree: treeOf([]) });

    initNotifications();
    await tick();

    expect(fetchMock).not.toHaveBeenCalled();
  });
});

describe("counts apply unconditionally", () => {
  test("v1 attention owns counts and edge fires without tree or extra navigation fetches", async () => {
    armPrefs("all");
    const client = new FakeClient("ready");
    fetchMock.mockImplementation(async () => navigationManifest());
    initNavigation(client, navigationCapability());
    initNotifications();
    await flushMicrotasks();
    fetchMock.mockClear();

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
    treeStore.setState({ tree: treeOf([node("legacy", "errored")]) });
    expect(document.title).toBe("(2) evener hub");
    expect(fetchMock).not.toHaveBeenCalled();
  });

  test("capability downgrade clears v1 attention and restores legacy tree authority", async () => {
    prefsStore.getState().setNotification("title", true);
    const client = new FakeClient("ready");
    fetchMock.mockImplementation(async () => navigationManifest());
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

    treeStore.setState({ tree: treeOf([node("legacy", "errored")]) });
    initNavigation(client, null);
    await flushMicrotasks();

    expect(navigationStore.getState().mode).toBe("legacy");
    expect(navigationStore.getState().attention.summary).toBeNull();
    expect(document.title).toBe("(1) evener hub");
  });

  test("v1 attention removes downgraded entries so a later escalation is a new edge", async () => {
    armPrefs("all");
    const client = new FakeClient("ready");
    fetchMock.mockImplementation(async () => navigationManifest());
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
    await boot(treeOf([]));
    setFocused(true); // focused
    setLeaderForTests(false); // non-leader
    treeStore.setState({ tree: treeOf([node("local:a", "awaiting"), node("local:b", "errored")]) });
    expect(document.title).toBe("(2) evener hub");
    expect(faviconHref()).toContain("%23f7768e"); // error dot
  });
});

describe("baseline suppression (the reload trap)", () => {
  test("the first snapshot never fires, even with attention already present", async () => {
    armPrefs("all");
    await boot(treeOf([node("local:a", "awaiting", true), node("local:e", "errored")]));
    expect(fires()).toEqual({ os: 0, sound: 0 });
  });
});

describe("edge-fire", () => {
  test("a new needs_you entry after the baseline fires OS + sound", async () => {
    armPrefs("all");
    await boot(treeOf([]));
    treeStore.setState({ tree: treeOf([node("local:a", "awaiting")]) });
    expect(fires()).toEqual({ os: 1, sound: 1 });
  });

  test("focused document suppresses the fire", async () => {
    armPrefs("all");
    await boot(treeOf([]));
    setFocused(true);
    treeStore.setState({ tree: treeOf([node("local:a", "awaiting")]) });
    expect(fires()).toEqual({ os: 0, sound: 0 });
  });

  test("a non-leader tab does not fire", async () => {
    armPrefs("all");
    await boot(treeOf([]));
    setLeaderForTests(false);
    treeStore.setState({ tree: treeOf([node("local:a", "awaiting")]) });
    expect(fires()).toEqual({ os: 0, sound: 0 });
  });

  test("os and sound gate independently", async () => {
    prefsStore.getState().setNotification("os", true); // sound stays OFF
    await boot(treeOf([]));
    // an error transition fires under the default "asks" scope
    treeStore.setState({ tree: treeOf([node("local:e", "errored")]) });
    expect(fires()).toEqual({ os: 1, sound: 0 });
  });
});

describe("loudScope", () => {
  test("asks: a plain your-move needs_you is silent; an ask fires", async () => {
    armPrefs("asks");
    await boot(treeOf([]));
    treeStore.setState({ tree: treeOf([node("local:a", "awaiting", false)]) });
    expect(fires()).toEqual({ os: 0, sound: 0 });
    treeStore.setState({ tree: treeOf([node("local:a", "awaiting", false), node("local:b", "awaiting", true)]) });
    expect(fires()).toEqual({ os: 1, sound: 1 });
  });

  test("all: a plain your-move needs_you fires", async () => {
    armPrefs("all");
    await boot(treeOf([]));
    treeStore.setState({ tree: treeOf([node("local:a", "awaiting", false)]) });
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
    await boot(treeOf([node("local:a", "awaiting", true), node("local:e", "errored")]));
    expect(document.title).toBe("(2) evener hub"); // title default ON: count prefix present
    expect(faviconHref()).not.toContain("%23f7768e"); // favicon still OFF by default: no error dot
    // a fresh transition still fires nothing while os/sound stay OFF
    treeStore.setState({
      tree: treeOf([node("local:a", "awaiting", true), node("local:e", "errored"), node("local:f", "errored")]),
    });
    expect(fires()).toEqual({ os: 0, sound: 0 });
  });
});

describe("reconnect re-baselines silently", () => {
  test("v1 reconnect forces one navigation handshake for same or new generation, never legacy refresh", async () => {
    const client = new FakeClient("ready");
    let generation = "generation_test";
    client.scriptConnect(() => ({
      serverInfo: { name: "fake", version: "1" },
      protocolVersion: "evener-appwire-v3",
      sourceId: "fake",
      features: {} as never,
      navigation: navigationCapability(generation),
    }));
    fetchMock.mockImplementation(async () => navigationManifest(generation));
    initNavigation(client);
    await flushMicrotasks();
    expect(fetchMock).toHaveBeenCalledTimes(1);

    client.emitStateChange("reconnecting");
    client.emitReady();
    await flushMicrotasks();
    expect(fetchMock).toHaveBeenCalledTimes(2); // one forced manifest reload
    expect(fetchMock.mock.calls.every(([url]) => url === "/api/navigation")).toBe(true);

    generation = "generation_next";
    client.emitStateChange("reconnecting");
    client.emitReady();
    await flushMicrotasks();
    expect(fetchMock).toHaveBeenCalledTimes(3); // one reset reload, not reset + legacy tree
    expect(navigationStore.getState().clientGenerationID).toBe("generation_next");
    expect(fetchMock.mock.calls.every(([url]) => url === "/api/navigation")).toBe(true);
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
    connectionStore.getState().connect(fake);
    await boot(treeOf([node("local:a", "awaiting")]));
    expect(fires()).toEqual({ os: 0, sound: 0 }); // baseline

    // The reconnect's own refresh returns a tree that GAINED local:b in the gap.
    fetchMock.mockResolvedValueOnce(jsonResponse(treeOf([node("local:a", "awaiting"), node("local:b", "awaiting")])));
    fake.emitStateChange("reconnecting");
    fake.emitReady();
    await tick();
    expect(fires()).toEqual({ os: 0, sound: 0 }); // silent re-baseline, not a fresh alert

    // ...but a genuinely new transition AFTER the reconnect still fires.
    treeStore.setState({
      tree: treeOf([node("local:a", "awaiting"), node("local:b", "awaiting"), node("local:c", "awaiting")]),
    });
    expect(fires()).toEqual({ os: 1, sound: 1 });
  });

  // kata p5w9. Only a TRANSITION into "ready" is a (re)connection. AppShell
  // publishes serverInfo through connectionStore once its own connect()
  // promise resolves, which is an ordinary store change with the state still
  // "ready" - reading that as a reconnect made every boot issue an extra,
  // pointless GET /api/tree (and re-baseline the attention snapshot for no
  // reason). The kata's own probe saw the third call and put it down to the
  // FakeClient; it is the real boot sequence, and it happens in the browser
  // too.
  test("a serverInfo update on an already-ready connection is not read as a reconnect", async () => {
    initNotifications();
    await tick();
    connectionStore.getState().connect(new FakeClient("ready")); // the initial connect
    await tick();
    const callsAfterConnect = fetchMock.mock.calls.length;

    connectionStore.setState({ serverInfo: { name: "evener-hub", version: "1.0.0" } });
    await tick();

    expect(fetchMock.mock.calls.length).toBe(callsAfterConnect);
  });
});
