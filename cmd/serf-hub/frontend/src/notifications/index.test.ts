import { afterEach, beforeAll, beforeEach, describe, expect, test, vi } from "vitest";
import { FakeClient } from "../protocol/testing/fakeClient";
import { connectionStore } from "../stores/connection";
import { prefsStore, resetPrefsStoreForTests } from "../stores/prefs";
import { resetTreeStoreForTests, type TreeNode, type TreeResponse, treeStore } from "../stores/tree";
import { initNotifications, resetNotificationsForTests } from "./index";
import { resetLeaderForTests, setLeaderForTests } from "./leader";

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
    favorites: [],
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
  initNotifications();
  await tick();
}

describe("initNotifications lifecycle", () => {
  test("is idempotent (safe to call repeatedly)", async () => {
    await boot(treeOf([]));
    expect(() => initNotifications()).not.toThrow();
  });
});

describe("counts apply unconditionally", () => {
  test("title + favicon update on a tree change even focused, even non-leader", async () => {
    prefsStore.getState().setNotification("title", true);
    prefsStore.getState().setNotification("favicon", true);
    await boot(treeOf([]));
    setFocused(true); // focused
    setLeaderForTests(false); // non-leader
    treeStore.setState({ tree: treeOf([node("local:a", "awaiting"), node("local:b", "errored")]) });
    expect(document.title).toBe("(2) serf hub");
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

describe("all-OFF defaults (the top cross-wave trap)", () => {
  test("with the shipped defaults, nothing counts, tints, or fires", async () => {
    // leader + unfocused (defaults) — ONLY the OFF prefs hold anything back.
    await boot(treeOf([node("local:a", "awaiting", true), node("local:e", "errored")]));
    expect(document.title).toBe("serf hub"); // no "(2)" prefix
    expect(faviconHref()).not.toContain("%23f7768e"); // no error dot
    // a fresh transition still fires nothing while every channel pref is OFF
    treeStore.setState({
      tree: treeOf([node("local:a", "awaiting", true), node("local:e", "errored"), node("local:f", "errored")]),
    });
    expect(fires()).toEqual({ os: 0, sound: 0 });
  });
});

describe("reconnect re-baselines silently", () => {
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
});
