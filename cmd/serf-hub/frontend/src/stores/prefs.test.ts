import { act, cleanup, renderHook } from "@testing-library/react";
import { afterEach, beforeAll, beforeEach, describe, expect, test, vi } from "vitest";
import { prefsStore, resetPrefsStoreForTests, usePrefsStore } from "./prefs";

// See shell/rail/Rail.test.tsx's identical comment: Node 26 shadows jsdom's
// real window.localStorage with its own (non-functional under vitest)
// global, so every test file that touches localStorage needs this same
// small in-memory stand-in. Scoped to this file only.
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
  // @ts-expect-error see MemoryStorage's own comment for why this is needed
  globalThis.localStorage = new MemoryStorage();
});

// Every key this store reads/writes lives under this prefix - the plan's own
// "serf.prefs.<name>" contract (docs/superpowers/plans/
// 2026-07-21-webui-rewrite-wave7-settings.md). enterToSend/showCost are
// PINNED exact names (a parallel wave's interim hook already reads them
// directly), so those two get their own named contract test below rather
// than relying only on the generic round-trip coverage every other field
// gets.
const KEY = (name: string) => `serf.prefs.${name}`;

beforeEach(() => {
  localStorage.clear();
  document.documentElement.removeAttribute("data-theme");
  delete document.body.dataset.phoneDensity;
  delete document.body.dataset.fontSize;
  resetPrefsStoreForTests();
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("defaults (empty localStorage)", () => {
  test("theme defaults to system", () => {
    expect(prefsStore.getState().theme).toBe("system");
  });
  test("phoneDensity defaults to compact", () => {
    expect(prefsStore.getState().phoneDensity).toBe("compact");
  });
  test("sidebarMode defaults to auto", () => {
    expect(prefsStore.getState().sidebarMode).toBe("auto");
  });
  test("fontSize defaults to m", () => {
    expect(prefsStore.getState().fontSize).toBe("m");
  });
  test("every transcript toggle defaults to false", () => {
    expect(prefsStore.getState().transcript).toEqual({
      roundTimings: false,
      hookExitsAll: false,
      hookExitsNormal: false,
      promptLoaded: false,
    });
  });
  test("enterToSend defaults to false", () => {
    expect(prefsStore.getState().enterToSend).toBe(false);
  });
  test("showCost defaults to true", () => {
    expect(prefsStore.getState().showCost).toBe(true);
  });
  // Pre-adjudicated (wave-7 plan): the floor doc flags a copy/code
  // discrepancy for notifications ("title/favicon default on" per copy, but
  // both the static markup and the JS default land all four at OFF) - the
  // CODE's behavior wins, replicated here as all-false.
  test("every notification toggle defaults to false (code-wins discrepancy resolution, not the copy's stated default)", () => {
    expect(prefsStore.getState().notifications).toEqual({
      title: false,
      favicon: false,
      os: false,
      sound: false,
    });
  });
  test("notificationsLoudScope defaults to asks", () => {
    expect(prefsStore.getState().notificationsLoudScope).toBe("asks");
  });
});

describe("hydration from existing localStorage", () => {
  test("reads a previously stored theme", () => {
    localStorage.setItem(KEY("theme"), "light");
    resetPrefsStoreForTests();
    expect(prefsStore.getState().theme).toBe("light");
  });

  test("reads a previously stored phoneDensity", () => {
    localStorage.setItem(KEY("phoneDensity"), "comfortable");
    resetPrefsStoreForTests();
    expect(prefsStore.getState().phoneDensity).toBe("comfortable");
  });

  test("reads a previously stored sidebarMode", () => {
    localStorage.setItem(KEY("sidebarMode"), "rail");
    resetPrefsStoreForTests();
    expect(prefsStore.getState().sidebarMode).toBe("rail");
  });

  test("reads a previously stored fontSize", () => {
    localStorage.setItem(KEY("fontSize"), "xl");
    resetPrefsStoreForTests();
    expect(prefsStore.getState().fontSize).toBe("xl");
  });

  test("reads previously stored transcript toggles independently", () => {
    localStorage.setItem(KEY("transcriptRoundTimings"), "true");
    localStorage.setItem(KEY("transcriptHookExitsNormal"), "true");
    resetPrefsStoreForTests();
    expect(prefsStore.getState().transcript).toEqual({
      roundTimings: true,
      hookExitsAll: false,
      hookExitsNormal: true,
      promptLoaded: false,
    });
  });

  test("reads a previously stored enterToSend/showCost", () => {
    localStorage.setItem(KEY("enterToSend"), "true");
    localStorage.setItem(KEY("showCost"), "false");
    resetPrefsStoreForTests();
    expect(prefsStore.getState().enterToSend).toBe(true);
    expect(prefsStore.getState().showCost).toBe(false);
  });

  test("reads previously stored notification toggles and loud scope", () => {
    localStorage.setItem(KEY("notificationsOs"), "true");
    localStorage.setItem(KEY("notificationsLoudScope"), "all");
    resetPrefsStoreForTests();
    expect(prefsStore.getState().notifications.os).toBe(true);
    expect(prefsStore.getState().notificationsLoudScope).toBe("all");
  });
});

describe("corrupted/unrecognized localStorage values fall back to the documented default", () => {
  test("an unrecognized theme string falls back to system", () => {
    localStorage.setItem(KEY("theme"), "not-a-real-theme");
    resetPrefsStoreForTests();
    expect(prefsStore.getState().theme).toBe("system");
  });

  test("an unrecognized fontSize falls back to m", () => {
    localStorage.setItem(KEY("fontSize"), "xxl");
    resetPrefsStoreForTests();
    expect(prefsStore.getState().fontSize).toBe("m");
  });

  test("a non-'true'/'false' boolean pref falls back to its default rather than reading as false", () => {
    // showCost's default is true - a corrupted value must not silently
    // collapse to false the way a naive `=== "true"` read would.
    localStorage.setItem(KEY("showCost"), "yes");
    resetPrefsStoreForTests();
    expect(prefsStore.getState().showCost).toBe(true);
  });
});

describe("pinned key contract: serf.prefs.enterToSend / serf.prefs.showCost", () => {
  // W5's interim hook (a different, parallel wave worktree) already reads
  // these two exact keys directly - see the wave-7 plan's own binding
  // constraints. This store MUST write through those same literal names so
  // the two converge at merge, not just "a" key of the store's own choosing.
  test("setEnterToSend writes the literal key serf.prefs.enterToSend", () => {
    prefsStore.getState().setEnterToSend(true);
    expect(localStorage.getItem("serf.prefs.enterToSend")).toBe("true");
  });

  test("setShowCost writes the literal key serf.prefs.showCost", () => {
    prefsStore.getState().setShowCost(false);
    expect(localStorage.getItem("serf.prefs.showCost")).toBe("false");
  });
});

describe("setTheme", () => {
  test("light: persists, updates state, and sets data-theme on the document root", () => {
    prefsStore.getState().setTheme("light");
    expect(localStorage.getItem(KEY("theme"))).toBe("light");
    expect(prefsStore.getState().theme).toBe("light");
    expect(document.documentElement.getAttribute("data-theme")).toBe("light");
  });

  test("dark: persists, updates state, and sets data-theme on the document root", () => {
    prefsStore.getState().setTheme("dark");
    expect(localStorage.getItem(KEY("theme"))).toBe("dark");
    expect(document.documentElement.getAttribute("data-theme")).toBe("dark");
  });

  // Matches the legacy contract exactly (parity-m7-settings.md §3): "system"
  // is represented by ABSENCE of the key, never the literal string "system" -
  // and correspondingly, the DOM attribute is removed rather than set to
  // some placeholder value.
  test("system: removes the localStorage key entirely (not the literal string 'system') and removes data-theme", () => {
    prefsStore.getState().setTheme("light"); // start from a non-default value
    prefsStore.getState().setTheme("system");
    expect(localStorage.getItem(KEY("theme"))).toBeNull();
    expect(prefsStore.getState().theme).toBe("system");
    expect(document.documentElement.hasAttribute("data-theme")).toBe(false);
  });
});

describe("setPhoneDensity", () => {
  test("persists, updates state, and mirrors onto document.body.dataset.phoneDensity", () => {
    prefsStore.getState().setPhoneDensity("comfortable");
    expect(localStorage.getItem(KEY("phoneDensity"))).toBe("comfortable");
    expect(prefsStore.getState().phoneDensity).toBe("comfortable");
    expect(document.body.dataset.phoneDensity).toBe("comfortable");
  });
});

describe("setSidebarMode", () => {
  test("persists and updates state", () => {
    prefsStore.getState().setSidebarMode("rail");
    expect(localStorage.getItem(KEY("sidebarMode"))).toBe("rail");
    expect(prefsStore.getState().sidebarMode).toBe("rail");
  });
});

describe("setFontSize", () => {
  test("persists, updates state, and mirrors onto document.body.dataset.fontSize", () => {
    prefsStore.getState().setFontSize("xl");
    expect(localStorage.getItem(KEY("fontSize"))).toBe("xl");
    expect(prefsStore.getState().fontSize).toBe("xl");
    expect(document.body.dataset.fontSize).toBe("xl");
  });
});

describe("setTranscriptStatus", () => {
  test("persists under a per-key name and updates only the targeted field", () => {
    prefsStore.getState().setTranscriptStatus("hookExitsAll", true);
    expect(localStorage.getItem(KEY("transcriptHookExitsAll"))).toBe("true");
    expect(prefsStore.getState().transcript).toEqual({
      roundTimings: false,
      hookExitsAll: true,
      hookExitsNormal: false,
      promptLoaded: false,
    });
  });
});

describe("setNotification", () => {
  test("persists under a per-key name and updates only the targeted field", () => {
    prefsStore.getState().setNotification("favicon", true);
    expect(localStorage.getItem(KEY("notificationsFavicon"))).toBe("true");
    expect(prefsStore.getState().notifications).toEqual({
      title: false,
      favicon: true,
      os: false,
      sound: false,
    });
  });
});

describe("setNotificationsLoudScope", () => {
  test("persists and updates state", () => {
    prefsStore.getState().setNotificationsLoudScope("all");
    expect(localStorage.getItem(KEY("notificationsLoudScope"))).toBe("all");
    expect(prefsStore.getState().notificationsLoudScope).toBe("all");
  });
});

describe("localStorage unavailable (e.g. Safari private mode)", () => {
  test("reading falls back to defaults rather than throwing", () => {
    vi.spyOn(MemoryStorage.prototype, "getItem").mockImplementation(() => {
      throw new Error("SecurityError");
    });
    expect(() => resetPrefsStoreForTests()).not.toThrow();
    expect(prefsStore.getState().theme).toBe("system");
    expect(prefsStore.getState().showCost).toBe(true);
  });

  test("writing is best-effort and never throws", () => {
    vi.spyOn(MemoryStorage.prototype, "setItem").mockImplementation(() => {
      throw new Error("QuotaExceededError");
    });
    expect(() => prefsStore.getState().setTheme("dark")).not.toThrow();
    expect(prefsStore.getState().theme).toBe("dark"); // in-memory state still updates
  });
});

describe("usePrefsStore hook", () => {
  test("reflects store state and re-renders on change", () => {
    const { result } = renderHook(() => usePrefsStore((s) => s.enterToSend));
    expect(result.current).toBe(false);

    act(() => {
      prefsStore.getState().setEnterToSend(true);
    });
    expect(result.current).toBe(true);
  });

  test("called with no selector returns the whole state", () => {
    const { result } = renderHook(() => usePrefsStore());
    expect(result.current.theme).toBe("system");
  });
});
