import { act, cleanup, renderHook } from "@testing-library/react";
import { afterEach, beforeAll, beforeEach, describe, expect, test, vi } from "vitest";
import { decodeLocalConfig, makeTranscriptDisplayConfig } from "../transcriptDisplay/config";
import {
  clampSidebarWidth,
  dualWriteTranscriptDisplayLegacy,
  hasTranscriptDisplayLocal,
  initPrefs,
  migrateLegacyTranscriptDisplay,
  prefsStore,
  readLegacyPreference,
  readTranscriptDisplayLocal,
  removeTranscriptDisplayLocal,
  resetPrefsStoreForTests,
  SIDEBAR_WIDTH_DEFAULT,
  SIDEBAR_WIDTH_MAX,
  SIDEBAR_WIDTH_MIN,
  usePrefsStore,
  writeLegacyBooleanPreference,
} from "./prefs";

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
// "evener.prefs.<name>" contract (docs/superpowers/plans/
// 2026-07-21-webui-rewrite-wave7-settings.md). enterToSend/showCost are
// PINNED exact names (the composer and transcript read them through this
// store), so those two get their own named contract test below rather
// than relying only on the generic round-trip coverage every other field
// gets.
const KEY = (name: string) => `evener.prefs.${name}`;

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
  vi.unstubAllGlobals();
});

describe("defaults (empty localStorage)", () => {
  test("theme defaults to system", () => {
    expect(prefsStore.getState().theme).toBe("system");
  });
  test("phoneDensity defaults to compact", () => {
    expect(prefsStore.getState().phoneDensity).toBe("compact");
  });
  test("fontSize defaults to m", () => {
    expect(prefsStore.getState().fontSize).toBe("m");
  });
  // promptLoaded and roundTimings are the two transcript toggles that default
  // ON. promptLoaded governs items the transcript already renders
  // unconditionally (the system-prompt scaffold and each "prompt loaded"
  // notice); defaulting it off would delete those for every user who never
  // opened Settings. roundTimings ships on per the five-participant study
  // (docs/web-ui/ux-plan-2026-07.md): its absence was the most-repeated
  // complaint, independently, from four of the five participants. The other
  // three gate lines that do not exist until you ask for them.
  test("transcript toggles default off, except promptLoaded and roundTimings", () => {
    expect(prefsStore.getState().transcript).toEqual({
      roundTimings: true,
      tokenCounts: false,
      hookExitsAll: false,
      hookExitsNormal: false,
      promptLoaded: true,
    });
  });
  test("enterToSend defaults to false", () => {
    expect(prefsStore.getState().enterToSend).toBe(false);
  });
  // The per-turn transcript meta line is opt-in in all three of its
  // segments (duration/tokens/cost); the session's total cost in the footer
  // strip is not gated by this pref.
  test("showCost defaults to false", () => {
    expect(prefsStore.getState().showCost).toBe(false);
  });
  // 2026-08-14 reversal (docs/web-ui/decisions.md): title-count is now the
  // one channel that defaults ON - the attention system was otherwise
  // invisible to anyone who never opened Settings. favicon/os/sound stay at
  // the wave-7 code-wins floor (all OFF).
  test("title defaults to true; favicon/os/sound default to false", () => {
    expect(prefsStore.getState().notifications).toEqual({
      title: true,
      favicon: false,
      os: false,
      sound: false,
    });
  });
  test("notificationsLoudScope defaults to asks", () => {
    expect(prefsStore.getState().notificationsLoudScope).toBe("asks");
  });
  // The WCAG 2.1.4 character-key turn-off (the p4 plan's Design decision 3):
  // the turn-off exists, and the default is ON.
  test("characterKeyTriggers defaults to true", () => {
    expect(prefsStore.getState().characterKeyTriggers).toBe(true);
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

  test("reads a previously stored fontSize", () => {
    localStorage.setItem(KEY("fontSize"), "xl");
    resetPrefsStoreForTests();
    expect(prefsStore.getState().fontSize).toBe("xl");
  });

  test("reads previously stored transcript toggles independently", () => {
    localStorage.setItem(KEY("transcriptRoundTimings"), "1");
    localStorage.setItem(KEY("transcriptTokenCounts"), "1");
    localStorage.setItem(KEY("transcriptHookExitsNormal"), "1");
    resetPrefsStoreForTests();
    expect(prefsStore.getState().transcript).toEqual({
      roundTimings: true,
      tokenCounts: true,
      hookExitsAll: false,
      // Stored nothing for this one, so it falls back to its ON default.
      promptLoaded: true,
      hookExitsNormal: true,
    });
  });

  // The mirror of the case above: promptLoaded's stored "0" has to WIN over its
  // ON default, or turning it off would not survive a reload.
  test("a stored promptLoaded of 0 beats its on-by-default", () => {
    localStorage.setItem(KEY("transcriptPromptLoaded"), "0");
    resetPrefsStoreForTests();
    expect(prefsStore.getState().transcript.promptLoaded).toBe(false);
  });

  // Both default off, so "1" is the value that actually proves the stored
  // value wins over the fallback rather than coinciding with it.
  test("reads a previously stored enterToSend/showCost", () => {
    localStorage.setItem(KEY("enterToSend"), "1");
    localStorage.setItem(KEY("showCost"), "1");
    resetPrefsStoreForTests();
    expect(prefsStore.getState().enterToSend).toBe(true);
    expect(prefsStore.getState().showCost).toBe(true);
  });

  // Proves "0" never decodes truthy - not branch-reached for either pref:
  // the false fallback coincides with "0"'s own decoding.
  test("reads a previously stored enterToSend/showCost of '0' as false", () => {
    localStorage.setItem(KEY("enterToSend"), "0");
    localStorage.setItem(KEY("showCost"), "0");
    resetPrefsStoreForTests();
    expect(prefsStore.getState().enterToSend).toBe(false);
    expect(prefsStore.getState().showCost).toBe(false);
  });

  test("reads previously stored notification toggles and loud scope", () => {
    localStorage.setItem(KEY("notificationsOs"), "1");
    localStorage.setItem(KEY("notificationsLoudScope"), "all");
    resetPrefsStoreForTests();
    expect(prefsStore.getState().notifications.os).toBe(true);
    expect(prefsStore.getState().notificationsLoudScope).toBe("all");
  });

  // The migration case the 2026-08-14 default flip must not break: a browser
  // that explicitly turned title OFF before the flip stored a real "0" - that
  // stored value has to keep winning over the new ON default, exactly like
  // the pinned-key-contract's "stored value wins in both directions" case
  // below. Only the ABSENT-key default changed; an existing opt-out is not a
  // hypothetical migration risk here, it round-trips through the same
  // readBool() every other pref uses.
  test("a stored title of '0' beats the new on-by-default (existing opt-out survives the default flip)", () => {
    localStorage.setItem(KEY("notificationsTitle"), "0");
    resetPrefsStoreForTests();
    expect(prefsStore.getState().notifications.title).toBe(false);
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

  test("an unrecognized transcriptMeasure falls back to reading", () => {
    localStorage.setItem(KEY("transcriptMeasure"), "huge");
    resetPrefsStoreForTests();
    expect(prefsStore.getState().transcriptMeasure).toBe("reading");
  });

  test("a non-'1'/'0' boolean pref falls back to its default rather than reading as true", () => {
    // Every boolean pref this store holds now defaults off, so the fallback
    // is only observable in this direction: a garbage value must not read as
    // a truthy "something is stored here" the way a naive `!== null` read
    // would. readBool's fallback ARGUMENT is what carries the other
    // direction, and it is a plain parameter shared by every caller.
    localStorage.setItem(KEY("showCost"), "yes");
    resetPrefsStoreForTests();
    expect(prefsStore.getState().showCost).toBe(false);
  });

  // Pins the exact regression this store must never reintroduce: the
  // literal string "true" is NOT a valid encoding of true under the "1"/"0"
  // contract (see pinned-key-contract describe block below) - a stray
  // "true"/"false" value (e.g. left over from some other app's differently-
  // encoded write to the same localStorage origin) must read as the
  // default, not as true.
  test("the literal string 'true' is not a valid boolean encoding - falls back to default, does not read as true", () => {
    localStorage.setItem(KEY("enterToSend"), "true");
    resetPrefsStoreForTests();
    expect(prefsStore.getState().enterToSend).toBe(false); // default, not true
  });
});

describe("transcriptMeasure (Settings -> Theme -> Transcript width)", () => {
  // Mirrors fontSize/phoneDensity: an enum pref persisted under its own flat
  // evener.prefs.<name> key and mirrored onto <body data-transcript-measure>,
  // which tokens.css keys --session-measure off (44rem reading / 64rem wide).
  test("defaults to reading, persists under evener.prefs.transcriptMeasure, and mirrors onto body", () => {
    expect(prefsStore.getState().transcriptMeasure).toBe("reading");
    expect(document.body.dataset.transcriptMeasure).toBe("reading");
    prefsStore.getState().setTranscriptMeasure("wide");
    expect(localStorage.getItem("evener.prefs.transcriptMeasure")).toBe("wide");
    expect(document.body.dataset.transcriptMeasure).toBe("wide");
    expect(prefsStore.getState().transcriptMeasure).toBe("wide");
  });
});

describe("pinned key contract: evener.prefs.enterToSend / evener.prefs.showCost", () => {
  // This is a live contract reachable from Settings today, not a
  // hypothetical one - Settings -> Display's own setEnterToSend/setShowCost
  // write both keys, and Composer.tsx reads enterToSend from this same
  // store. The "1"/"0" VALUE ENCODING (this store's own uniform encoding
  // for every persisted boolean - readBool/writeBool, shared across
  // enterToSend, showCost, and every transcript/notifications member, not
  // JS's "true"/"false") already broke this contract once during
  // development: commit 932eeddca ("fix boolean
  // encoding to '1'/'0' (cross-wave contract break)") fixed readBool/
  // writeBool back from a brief "true"/"false" regression. Never repeat it -
  // both the key NAME and the encoding must stay exactly as they are.
  test("setEnterToSend writes the literal key evener.prefs.enterToSend with '1'/'0' encoding", () => {
    prefsStore.getState().setEnterToSend(true);
    expect(localStorage.getItem("evener.prefs.enterToSend")).toBe("1");
    prefsStore.getState().setEnterToSend(false);
    expect(localStorage.getItem("evener.prefs.enterToSend")).toBe("0");
  });

  test("setShowCost writes the literal key evener.prefs.showCost with '1'/'0' encoding", () => {
    prefsStore.getState().setShowCost(false);
    expect(localStorage.getItem("evener.prefs.showCost")).toBe("0");
    prefsStore.getState().setShowCost(true);
    expect(localStorage.getItem("evener.prefs.showCost")).toBe("1");
  });

  // The pin covers the key NAME and the value ENCODING. A browser that has
  // ever toggled the switch keeps whatever it stored, whatever the default
  // for an UNSET key happens to be - so a default change can never silently
  // flip an existing user's preference.
  test("a stored value wins over the default in both directions", () => {
    localStorage.setItem("evener.prefs.showCost", "1");
    resetPrefsStoreForTests();
    expect(prefsStore.getState().showCost).toBe(true);
    localStorage.setItem("evener.prefs.showCost", "0");
    resetPrefsStoreForTests();
    expect(prefsStore.getState().showCost).toBe(false);
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

// fakeMatchMedia stands in for window.matchMedia (which this project's own
// jsdom test environment does not implement at all - confirmed empirically,
// not assumed) so a test can both seed the OS's current preference and
// simulate it changing later, via the same change-event contract the real
// MediaQueryList carries.
function fakeMatchMedia(initialMatches: boolean) {
  let matches = initialMatches;
  let listener: (() => void) | null = null;
  const mql = {
    get matches() {
      return matches;
    },
    addEventListener: (_event: string, cb: () => void) => {
      listener = cb;
    },
    removeEventListener: () => {
      listener = null;
    },
  };
  return {
    mql,
    setMatches(next: boolean) {
      matches = next;
      listener?.();
    },
  };
}

describe("system theme: live OS-scheme tracking", () => {
  // theme.tsx's own help copy claims "default follows your OS preference" -
  // until this behavior existed, "system" always rendered dark regardless of
  // the OS (see this file's own module-level top comment, pre this task).
  test("system resolves to light when the OS currently prefers light", () => {
    const { mql } = fakeMatchMedia(false); // prefers-color-scheme: dark does NOT match -> prefers light
    vi.stubGlobal(
      "matchMedia",
      vi.fn(() => mql),
    );
    resetPrefsStoreForTests();
    expect(document.documentElement.getAttribute("data-theme")).toBe("light");
  });

  test("system resolves to dark when the OS currently prefers dark", () => {
    const { mql } = fakeMatchMedia(true);
    vi.stubGlobal(
      "matchMedia",
      vi.fn(() => mql),
    );
    resetPrefsStoreForTests();
    expect(document.documentElement.hasAttribute("data-theme")).toBe(false);
  });

  test("live-tracks a later OS preference change while theme stays 'system'", () => {
    const { mql, setMatches } = fakeMatchMedia(true); // starts OS-prefers-dark
    vi.stubGlobal(
      "matchMedia",
      vi.fn(() => mql),
    );
    resetPrefsStoreForTests();
    expect(document.documentElement.hasAttribute("data-theme")).toBe(false);

    setMatches(false); // OS flips to light
    expect(document.documentElement.getAttribute("data-theme")).toBe("light");

    setMatches(true); // OS flips back to dark
    expect(document.documentElement.hasAttribute("data-theme")).toBe(false);
  });

  test("does not react to an OS change while an explicit light/dark theme is selected", () => {
    const { mql, setMatches } = fakeMatchMedia(true);
    vi.stubGlobal(
      "matchMedia",
      vi.fn(() => mql),
    );
    resetPrefsStoreForTests();
    prefsStore.getState().setTheme("dark");

    setMatches(false); // OS flips to light - irrelevant, theme is explicitly "dark"
    expect(document.documentElement.getAttribute("data-theme")).toBe("dark");
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

// A stale evener.prefs.sidebarMode key from before collapsed mode's removal
// (2026-07-24) must be inert: never read, never crashing the loader.
describe("stale sidebarMode key", () => {
  test("is ignored by a fresh load", () => {
    localStorage.setItem(KEY("sidebarMode"), "rail");
    resetPrefsStoreForTests();
    expect("sidebarMode" in prefsStore.getState()).toBe(false);
  });
});

// The docked rail's dragged width. Unlike every other pref here it carries
// real bounds, and a value outside them would render an unusable (or
// unrecoverable) sidebar - so the clamp is asserted on BOTH the read and the
// write path, not just once.
describe("sidebarWidth", () => {
  test("defaults to SIDEBAR_WIDTH_DEFAULT with nothing persisted", () => {
    expect(prefsStore.getState().sidebarWidth).toBe(SIDEBAR_WIDTH_DEFAULT);
  });

  test("setSidebarWidth persists as a plain number string and updates state", () => {
    prefsStore.getState().setSidebarWidth(320);
    expect(localStorage.getItem(KEY("sidebarWidth"))).toBe("320");
    expect(prefsStore.getState().sidebarWidth).toBe(320);
  });

  test("round-trips a persisted width across a fresh load", () => {
    prefsStore.getState().setSidebarWidth(400);
    resetPrefsStoreForTests();
    expect(prefsStore.getState().sidebarWidth).toBe(400);
  });

  test("setSidebarWidth clamps below the minimum and above the maximum", () => {
    prefsStore.getState().setSidebarWidth(10);
    expect(prefsStore.getState().sidebarWidth).toBe(SIDEBAR_WIDTH_MIN);
    prefsStore.getState().setSidebarWidth(5000);
    expect(prefsStore.getState().sidebarWidth).toBe(SIDEBAR_WIDTH_MAX);
  });

  test("setSidebarWidth rounds a fractional drag position to a whole pixel", () => {
    prefsStore.getState().setSidebarWidth(301.6);
    expect(prefsStore.getState().sidebarWidth).toBe(302);
    expect(localStorage.getItem(KEY("sidebarWidth"))).toBe("302");
  });

  test.each([
    ["a huge value", "99999", SIDEBAR_WIDTH_MAX],
    ["a negative value", "-400", SIDEBAR_WIDTH_MIN],
    ["zero", "0", SIDEBAR_WIDTH_MIN],
    ["non-numeric garbage", "wide-please", SIDEBAR_WIDTH_DEFAULT],
    ["an empty string", "", SIDEBAR_WIDTH_DEFAULT],
    ["NaN", "NaN", SIDEBAR_WIDTH_DEFAULT],
    ["Infinity", "Infinity", SIDEBAR_WIDTH_DEFAULT],
  ])("clamps %s on read (%s -> %i)", (_label, stored, expected) => {
    localStorage.setItem(KEY("sidebarWidth"), stored);
    resetPrefsStoreForTests();
    expect(prefsStore.getState().sidebarWidth).toBe(expected);
  });

  test("clampSidebarWidth keeps an in-bounds value untouched", () => {
    expect(clampSidebarWidth(333)).toBe(333);
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
    expect(localStorage.getItem(KEY("transcriptHookExitsAll"))).toBe("1");
    expect(prefsStore.getState().transcript).toEqual({
      // Untouched by this call, so it stays at its shipped default (on).
      roundTimings: true,
      tokenCounts: false,
      hookExitsAll: true,
      hookExitsNormal: false,
      promptLoaded: true,
    });
  });

  test("tokenCounts persists under its own evener.prefs.transcriptTokenCounts key", () => {
    prefsStore.getState().setTranscriptStatus("tokenCounts", true);
    expect(localStorage.getItem(KEY("transcriptTokenCounts"))).toBe("1");
    expect(prefsStore.getState().transcript.tokenCounts).toBe(true);
    // Untouched by this call, so it stays at its shipped default (on).
    expect(prefsStore.getState().transcript.roundTimings).toBe(true);
  });
});

describe("setNotification", () => {
  test("persists under a per-key name and updates only the targeted field", () => {
    prefsStore.getState().setNotification("favicon", true);
    expect(localStorage.getItem(KEY("notificationsFavicon"))).toBe("1");
    expect(prefsStore.getState().notifications).toEqual({
      // Untouched by this call, so it stays at its shipped default (on).
      title: true,
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

describe("setCharacterKeyTriggers", () => {
  test("persists under its own evener.prefs.characterKeyTriggers key and updates state", () => {
    prefsStore.getState().setCharacterKeyTriggers(false);
    expect(localStorage.getItem(KEY("characterKeyTriggers"))).toBe("0");
    expect(prefsStore.getState().characterKeyTriggers).toBe(false);
    // A fresh hydration (what the next page load does) reads it back.
    resetPrefsStoreForTests();
    expect(prefsStore.getState().characterKeyTriggers).toBe(false);
    prefsStore.getState().setCharacterKeyTriggers(true);
    expect(localStorage.getItem(KEY("characterKeyTriggers"))).toBe("1");
  });
});

describe("setLastSettingsSection", () => {
  test("persists under its own evener.prefs.lastSettingsSection key and updates state", () => {
    expect(prefsStore.getState().lastSettingsSection).toBeNull();
    prefsStore.getState().setLastSettingsSection("keybindings");
    expect(localStorage.getItem(KEY("lastSettingsSection"))).toBe("keybindings");
    expect(prefsStore.getState().lastSettingsSection).toBe("keybindings");
    // A fresh hydration (what the next page load does) reads it back.
    resetPrefsStoreForTests();
    expect(prefsStore.getState().lastSettingsSection).toBe("keybindings");
    // null is absence: the key is removed, never a literal "null" string.
    prefsStore.getState().setLastSettingsSection(null);
    expect(localStorage.getItem(KEY("lastSettingsSection"))).toBeNull();
    expect(prefsStore.getState().lastSettingsSection).toBeNull();
  });
});

describe("localStorage unavailable (e.g. Safari private mode)", () => {
  test("reading falls back to defaults rather than throwing", () => {
    vi.spyOn(MemoryStorage.prototype, "getItem").mockImplementation(() => {
      throw new Error("SecurityError");
    });
    expect(() => resetPrefsStoreForTests()).not.toThrow();
    expect(prefsStore.getState().theme).toBe("system");
    expect(prefsStore.getState().showCost).toBe(false);
    // Migrated from W5's interim hook (enterToSendPref.test.ts, deleted at the
    // absorb-a2 merge): "degrades to off when localStorage throws" - readBool
    // shares the exact same readRaw try/catch this asserts generically above,
    // but enterToSend's own OFF default was that hook's specifically-named
    // contract, so it gets its own explicit assertion rather than relying on
    // theme's coverage of the shared code path alone.
    expect(prefsStore.getState().enterToSend).toBe(false);
  });

  test("writing is best-effort and never throws", () => {
    vi.spyOn(MemoryStorage.prototype, "setItem").mockImplementation(() => {
      throw new Error("QuotaExceededError");
    });
    expect(() => prefsStore.getState().setTheme("dark")).not.toThrow();
    expect(prefsStore.getState().theme).toBe("dark"); // in-memory state still updates
  });
});

describe("initPrefs (production entry point)", () => {
  // initPrefs is what a caller with no visibility into this store's own
  // internals (the shell's app-root boot sequence, per the wave-7 review's
  // "prefs hydration reachability" finding) imports and calls to GUARANTEE
  // hydration+document-application has happened, rather than relying on it
  // having already fired via whichever section a user happened to open
  // first. These tests deliberately do NOT call resetPrefsStoreForTests()
  // and do NOT render any component - initPrefs() itself is the only thing
  // under test, exercised the same way the shell would call it.
  test("applies a previously-saved theme to document.documentElement with no component mounted", () => {
    localStorage.setItem(KEY("theme"), "light");
    document.documentElement.removeAttribute("data-theme"); // undo whatever this file's own earlier module import already set

    initPrefs();

    expect(document.documentElement.getAttribute("data-theme")).toBe("light");
    expect(prefsStore.getState().theme).toBe("light");
  });

  test("also applies phoneDensity and fontSize to document.body.dataset", () => {
    localStorage.setItem(KEY("phoneDensity"), "comfortable");
    localStorage.setItem(KEY("fontSize"), "xl");
    delete document.body.dataset.phoneDensity;
    delete document.body.dataset.fontSize;

    initPrefs();

    expect(document.body.dataset.phoneDensity).toBe("comfortable");
    expect(document.body.dataset.fontSize).toBe("xl");
  });

  test("idempotent: calling it again with unchanged localStorage is a harmless no-op re-application", () => {
    localStorage.setItem(KEY("theme"), "dark");
    initPrefs();
    expect(() => initPrefs()).not.toThrow();
    expect(document.documentElement.getAttribute("data-theme")).toBe("dark");
  });
});

describe("usePrefsStore hook", () => {
  // Migrated from W5's interim hook (enterToSendPref.test.ts, deleted at the
  // absorb-a2 merge): "useEnterToSendPref reflects the stored value at render
  // time" - a value already persisted BEFORE the component ever mounts, as
  // opposed to the next test's live update-after-mount case.
  test("reflects a value already hydrated from localStorage before the component mounts", () => {
    localStorage.setItem(KEY("enterToSend"), "1");
    resetPrefsStoreForTests();

    const { result } = renderHook(() => usePrefsStore((s) => s.enterToSend));
    expect(result.current).toBe(true);
  });

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

describe("transcript display legacy adapters", () => {
  test("migrates legacy values to both layout keys and retains every old key", () => {
    localStorage.setItem(KEY("transcriptRoundTimings"), "0");
    localStorage.setItem(KEY("transcriptTokenCounts"), "1");
    localStorage.setItem(KEY("transcriptHookExitsNormal"), "1");
    localStorage.setItem(KEY("transcriptPromptLoaded"), "0");
    localStorage.setItem(KEY("showCost"), "1");

    const migrated = migrateLegacyTranscriptDisplay();

    expect(migrated?.content).toEqual({ kind: "preset", level: "activity" });
    expect(migrated?.advanced).toEqual({
      roundTimings: false,
      tokenCounts: true,
      estimatedCost: true,
      systemEvents: true,
      promptEvents: false,
      hookExits: "successful",
    });
    expect(decodeLocalConfig(readTranscriptDisplayLocal("desktop"))).toEqual(migrated);
    expect(decodeLocalConfig(readTranscriptDisplayLocal("mobile"))).toEqual(migrated);
    expect(hasTranscriptDisplayLocal("desktop")).toBe(true);
    expect(hasTranscriptDisplayLocal("mobile")).toBe(true);
    expect(readLegacyPreference("transcriptRoundTimings")).toBe("0");
    expect(readLegacyPreference("transcriptTokenCounts")).toBe("1");
    expect(readLegacyPreference("transcriptHookExitsNormal")).toBe("1");
    expect(readLegacyPreference("transcriptPromptLoaded")).toBe("0");
    expect(readLegacyPreference("showCost")).toBe("1");
  });

  test("uses the specified defaults when a legacy key is present but malformed", () => {
    localStorage.setItem(KEY("transcriptRoundTimings"), "garbage");
    expect(migrateLegacyTranscriptDisplay()?.advanced).toEqual({
      roundTimings: true,
      tokenCounts: false,
      estimatedCost: false,
      systemEvents: true,
      promptEvents: true,
      hookExits: "none",
    });
  });

  test("does not migrate if either layout key already exists", () => {
    localStorage.setItem(KEY("transcriptRoundTimings"), "1");
    localStorage.setItem(KEY("transcriptDisplay.desktop"), "not-valid-config");
    expect(migrateLegacyTranscriptDisplay()).toBeUndefined();
    expect(readTranscriptDisplayLocal("mobile")).toBeNull();
  });

  test("does not write layout keys when no exact legacy key exists", () => {
    expect(migrateLegacyTranscriptDisplay()).toBeUndefined();
    expect(hasTranscriptDisplayLocal("desktop")).toBe(false);
    expect(hasTranscriptDisplayLocal("mobile")).toBe(false);
  });

  test("maps all-on hooks to all and preserves the old values", () => {
    localStorage.setItem(KEY("transcriptHookExitsAll"), "1");
    localStorage.setItem(KEY("transcriptHookExitsNormal"), "1");
    const migrated = migrateLegacyTranscriptDisplay();
    expect(migrated?.advanced.hookExits).toBe("all");
    expect(readLegacyPreference("transcriptHookExitsAll")).toBe("1");
    expect(readLegacyPreference("transcriptHookExitsNormal")).toBe("1");
  });

  test("guarded bool writes use literal 1/0 and removal targets only the layout key", () => {
    writeLegacyBooleanPreference("showCost", true);
    expect(localStorage.getItem(KEY("showCost"))).toBe("1");
    writeLegacyBooleanPreference("showCost", false);
    expect(localStorage.getItem(KEY("showCost"))).toBe("0");

    localStorage.setItem(KEY("transcriptDisplay.desktop"), "encoded");
    removeTranscriptDisplayLocal("desktop");
    expect(localStorage.getItem(KEY("transcriptDisplay.desktop"))).toBeNull();
    expect(localStorage.getItem(KEY("showCost"))).toBe("0");
  });

  test("dual-write adapter writes exact 1/0 values for all legacy keys", () => {
    dualWriteTranscriptDisplayLegacy(
      makeTranscriptDisplayConfig(
        { kind: "preset", level: "activity" },
        {
          roundTimings: true,
          tokenCounts: false,
          estimatedCost: true,
          promptEvents: true,
          hookExits: "all",
        },
      ),
    );
    expect(localStorage.getItem(KEY("transcriptRoundTimings"))).toBe("1");
    expect(localStorage.getItem(KEY("transcriptTokenCounts"))).toBe("0");
    expect(localStorage.getItem(KEY("transcriptHookExitsAll"))).toBe("1");
    expect(localStorage.getItem(KEY("transcriptHookExitsNormal"))).toBe("0");
    expect(localStorage.getItem(KEY("transcriptPromptLoaded"))).toBe("1");
    expect(localStorage.getItem(KEY("showCost"))).toBe("1");
  });
});
