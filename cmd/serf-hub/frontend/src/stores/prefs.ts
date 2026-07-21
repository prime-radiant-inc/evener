// prefs.ts is the localStorage-backed home for every settings preference
// that has no server-side counterpart (Theme/Transcript/Display/
// Notifications - see docs/web-ui/parity/parity-m7-settings.md §§3-6):
// General/Hub/Storage read live daemon state through settingsOverview.ts
// instead. Every key lives under the "serf.prefs.<name>" contract (wave-7
// plan): a flat, one-value-per-key namespace, NOT the legacy's per-section
// JSON blobs (serf-hub.composer, serf-hub.transcript.systemStatus, ...) -
// chosen so a parallel wave's interim hook can read/write exactly one named
// key (serf.prefs.enterToSend, serf.prefs.showCost) without depending on
// this store's shape at all. Those two names are PINNED (both waves must
// converge on the literal string at merge); every other key below is this
// store's own choice, consistently named `serf.prefs.<fieldPath>`.
//
// Hydrated once from localStorage at module load (mirroring every other
// store's singleton-at-import-time shape - connection.ts/threads.ts/
// tree.ts), then kept live: every setter writes through to localStorage
// immediately ("persisting on set") and updates the Zustand state in the
// same call, so a subscribed component re-renders with no separate
// "reapply after swap" step - unlike the legacy's own htmx world (server-
// rendered radios/checkboxes with no `checked` attribute, resynced by a
// dedicated applyXState() rerun on every htmx:afterSwap), a controlled
// React input driven straight off this store's state is always correct on
// every render, so that whole category of legacy machinery has no
// successor here.
//
// Theme is the one preference with a real, already-wired visual consumer
// (src/styles/tokens.css keys light-theme overrides off `[data-theme="light"]`
// on the document root; the default `:root` block IS the dark theme, so
// "dark" has no override block of its own and "system" has never had one -
// there is no prefers-color-scheme media query in tokens.css, a wave-2
// decision this store doesn't revisit). setTheme therefore mirrors the
// legacy's own assets/theme.js exactly, including its "system is absence,
// never the literal string" contract: light/dark set data-theme, anything
// else removes both the attribute and the stored key. phoneDensity/fontSize
// get the same document-mirroring treatment the legacy's settings-
// appearance.js gave them (onto document.body.dataset), even though no CSS
// in the new design system keys off those two attributes yet - reproducing
// the legacy's own mechanism is this task's explicit brief; a future task
// adding the corresponding CSS gate has something to attach to either way.
// sidebarMode gets NO document mirror: the legacy delegates it to
// window.SerfSidebar.applySidebarMode, a vanilla-JS global with no
// equivalent in this app (shell/rail/Rail.tsx owns an unrelated, frozen-
// this-wave boolean collapse toggle of its own, `serf.rail.collapsed.v1`) -
// wiring a real 3-state auto/pane/rail mode into the shell is out of this
// store's manifest; persisting the preference is as far as T4 goes.

import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";

export type ThemePref = "system" | "light" | "dark";
export type PhoneDensityPref = "compact" | "comfortable";
export type SidebarModePref = "auto" | "pane" | "rail";
export type FontSizePref = "s" | "m" | "l" | "xl";
export type TranscriptStatusKey = "roundTimings" | "hookExitsAll" | "hookExitsNormal" | "promptLoaded";
export type NotificationKey = "title" | "favicon" | "os" | "sound";
export type NotificationsLoudScopePref = "asks" | "all";

export interface PrefsStoreState {
  theme: ThemePref;
  phoneDensity: PhoneDensityPref;
  sidebarMode: SidebarModePref;
  fontSize: FontSizePref;
  transcript: Record<TranscriptStatusKey, boolean>;
  // Composer prefs (Display section, parity-m7-settings.md §5). Field names
  // match the PINNED serf.prefs.enterToSend / serf.prefs.showCost keys.
  enterToSend: boolean;
  showCost: boolean;
  notifications: Record<NotificationKey, boolean>;
  notificationsLoudScope: NotificationsLoudScopePref;

  setTheme(value: ThemePref): void;
  setPhoneDensity(value: PhoneDensityPref): void;
  setSidebarMode(value: SidebarModePref): void;
  setFontSize(value: FontSizePref): void;
  setTranscriptStatus(key: TranscriptStatusKey, value: boolean): void;
  setEnterToSend(value: boolean): void;
  setShowCost(value: boolean): void;
  setNotification(key: NotificationKey, value: boolean): void;
  setNotificationsLoudScope(value: NotificationsLoudScopePref): void;
}

const KEY_PREFIX = "serf.prefs.";

// --- localStorage access: every read/write is best-effort - a private
// browsing mode that throws on storage access must never be fatal to the
// settings UI, matching shell/rail/Rail.tsx's own readCollapsed/
// persistCollapsed precedent (COLLAPSED_STORAGE_KEY).
function readRaw(name: string): string | null {
  try {
    return localStorage.getItem(`${KEY_PREFIX}${name}`);
  } catch {
    return null; // localStorage unavailable: behave as if the key were never set
  }
}

function writeRaw(name: string, value: string): void {
  try {
    localStorage.setItem(`${KEY_PREFIX}${name}`, value);
  } catch {
    // Best-effort, mirrors Rail.tsx's own persistCollapsed precedent - a
    // full quota (or a browser that blocks storage entirely) must never be
    // fatal to the settings UI; the in-memory state this call also sets
    // still takes effect for the rest of the session.
  }
}

function removeRaw(name: string): void {
  try {
    localStorage.removeItem(`${KEY_PREFIX}${name}`);
  } catch {
    // Best-effort, same rationale as writeRaw.
  }
}

// "1"/"0", not JS's "true"/"false": this codebase's own established
// precedent for a single persisted boolean (shell/rail/Rail.tsx's
// COLLAPSED_STORAGE_KEY: `localStorage.getItem(...) === "1"` /
// `collapsed ? "1" : "0"`), and the encoding W5's already-shipped interim
// composer hook (webui-w5-composer/.../composer/enterToSendPref.ts) reads
// on this exact serf.prefs.enterToSend key with a strict `=== "1"` check -
// its own test asserts the literal string "true" reads as false. Using
// "true"/"false" here would silently break that convergence: both waves
// writing/reading the SAME key with different encodings corrupts each
// other's value rather than erroring.
function readBool(name: string, fallback: boolean): boolean {
  const raw = readRaw(name);
  if (raw === "1") return true;
  if (raw === "0") return false;
  return fallback; // absent, or corrupted - never silently coerce to false
}

function writeBool(name: string, value: boolean): void {
  writeRaw(name, value ? "1" : "0");
}

function readEnum<T extends string>(name: string, allowed: readonly T[], fallback: T): T {
  const raw = readRaw(name);
  return raw !== null && (allowed as readonly string[]).includes(raw) ? (raw as T) : fallback;
}

const THEME_VALUES: readonly ThemePref[] = ["system", "light", "dark"];
const PHONE_DENSITY_VALUES: readonly PhoneDensityPref[] = ["compact", "comfortable"];
const SIDEBAR_MODE_VALUES: readonly SidebarModePref[] = ["auto", "pane", "rail"];
const FONT_SIZE_VALUES: readonly FontSizePref[] = ["s", "m", "l", "xl"];
const LOUD_SCOPE_VALUES: readonly NotificationsLoudScopePref[] = ["asks", "all"];

// Per-field localStorage key names for the two grouped record fields -
// transcript/notifications are exposed as small in-memory records for a
// convenient single destructure at each section's call site, but each
// member still round-trips through its OWN flat serf.prefs.<name> key
// (never one shared JSON blob), per this file's own top comment.
const TRANSCRIPT_KEY_NAMES: Record<TranscriptStatusKey, string> = {
  roundTimings: "transcriptRoundTimings",
  hookExitsAll: "transcriptHookExitsAll",
  hookExitsNormal: "transcriptHookExitsNormal",
  promptLoaded: "transcriptPromptLoaded",
};

const NOTIFICATION_KEY_NAMES: Record<NotificationKey, string> = {
  title: "notificationsTitle",
  favicon: "notificationsFavicon",
  os: "notificationsOs",
  sound: "notificationsSound",
};

function loadTranscript(): Record<TranscriptStatusKey, boolean> {
  return {
    roundTimings: readBool(TRANSCRIPT_KEY_NAMES.roundTimings, false),
    hookExitsAll: readBool(TRANSCRIPT_KEY_NAMES.hookExitsAll, false),
    hookExitsNormal: readBool(TRANSCRIPT_KEY_NAMES.hookExitsNormal, false),
    promptLoaded: readBool(TRANSCRIPT_KEY_NAMES.promptLoaded, false),
  };
}

// code-wins: the floor doc's own §6 flags a copy/code discrepancy (page
// copy claims title/favicon default on; both the static markup and the
// legacy JS default land all four at OFF) - replicating the CODE's
// behavior per this wave's pre-adjudication, all four false.
function loadNotifications(): Record<NotificationKey, boolean> {
  return {
    title: readBool(NOTIFICATION_KEY_NAMES.title, false),
    favicon: readBool(NOTIFICATION_KEY_NAMES.favicon, false),
    os: readBool(NOTIFICATION_KEY_NAMES.os, false),
    sound: readBool(NOTIFICATION_KEY_NAMES.sound, false),
  };
}

// --- document application: the DOM side effects the legacy mirrored
// alongside localStorage (assets/theme.js; assets/settings-appearance.js's
// two script-parse-time IIFEs) - see this file's own top comment for which
// three get one and why sidebarMode doesn't.
function applyTheme(value: ThemePref): void {
  if (value === "system") {
    document.documentElement.removeAttribute("data-theme");
  } else {
    document.documentElement.setAttribute("data-theme", value);
  }
}

function applyPhoneDensity(value: PhoneDensityPref): void {
  document.body.dataset.phoneDensity = value;
}

function applyFontSize(value: FontSizePref): void {
  document.body.dataset.fontSize = value;
}

// loadInitialState re-derives every field from localStorage (plus the
// document side effects above) - shared by the store's own creator function
// and resetPrefsStoreForTests(), so a test that seeds localStorage and then
// resets sees exactly what a fresh module load would have produced.
function loadInitialState(): Omit<
  PrefsStoreState,
  | "setTheme"
  | "setPhoneDensity"
  | "setSidebarMode"
  | "setFontSize"
  | "setTranscriptStatus"
  | "setEnterToSend"
  | "setShowCost"
  | "setNotification"
  | "setNotificationsLoudScope"
> {
  const theme = readEnum("theme", THEME_VALUES, "system");
  const phoneDensity = readEnum("phoneDensity", PHONE_DENSITY_VALUES, "compact");
  const fontSize = readEnum("fontSize", FONT_SIZE_VALUES, "m");
  applyTheme(theme);
  applyPhoneDensity(phoneDensity);
  applyFontSize(fontSize);
  return {
    theme,
    phoneDensity,
    sidebarMode: readEnum("sidebarMode", SIDEBAR_MODE_VALUES, "auto"),
    fontSize,
    transcript: loadTranscript(),
    enterToSend: readBool("enterToSend", false),
    showCost: readBool("showCost", true),
    notifications: loadNotifications(),
    notificationsLoudScope: readEnum("notificationsLoudScope", LOUD_SCOPE_VALUES, "asks"),
  };
}

export const prefsStore = createStore<PrefsStoreState>((set) => ({
  ...loadInitialState(),

  setTheme(value) {
    // Matches the legacy's own absence-means-system contract (see this
    // file's top comment) - "system" is never written as a literal string.
    if (value === "system") {
      removeRaw("theme");
    } else {
      writeRaw("theme", value);
    }
    applyTheme(value);
    set({ theme: value });
  },

  setPhoneDensity(value) {
    writeRaw("phoneDensity", value);
    applyPhoneDensity(value);
    set({ phoneDensity: value });
  },

  setSidebarMode(value) {
    writeRaw("sidebarMode", value);
    set({ sidebarMode: value });
  },

  setFontSize(value) {
    writeRaw("fontSize", value);
    applyFontSize(value);
    set({ fontSize: value });
  },

  setTranscriptStatus(key, value) {
    writeBool(TRANSCRIPT_KEY_NAMES[key], value);
    set((s) => ({ transcript: { ...s.transcript, [key]: value } }));
  },

  setEnterToSend(value) {
    writeBool("enterToSend", value);
    set({ enterToSend: value });
  },

  setShowCost(value) {
    writeBool("showCost", value);
    set({ showCost: value });
  },

  setNotification(key, value) {
    writeBool(NOTIFICATION_KEY_NAMES[key], value);
    set((s) => ({ notifications: { ...s.notifications, [key]: value } }));
  },

  setNotificationsLoudScope(value) {
    writeRaw("notificationsLoudScope", value);
    set({ notificationsLoudScope: value });
  },
}));

export function usePrefsStore(): PrefsStoreState;
export function usePrefsStore<T>(selector: (state: PrefsStoreState) => T): T;
export function usePrefsStore<T>(selector?: (state: PrefsStoreState) => T): T | PrefsStoreState {
  // Not a real conditional hook call - see stores/connection.ts's own
  // useConnectionStore for the full explanation (zustand's useStore has a
  // `selector = identity` JS default param, so both arms run identically).
  // biome-ignore lint/correctness/useHookAtTopLevel: same hook both arms, JS default param not a real conditional - see stores/connection.ts
  return selector ? useStore(prefsStore, selector) : useStore(prefsStore);
}

// resetPrefsStoreForTests re-derives every field from whatever's in
// localStorage right now (loadInitialState, the same function the store's
// own creator runs once at module load) and overwrites the store with it -
// letting a test seed localStorage and then observe the hydration a fresh
// module load would have produced, without actually reloading the module.
// No production code should ever call this (mirrors threads.ts's
// resetThreadsStoreForTests / tree.ts's resetTreeStoreForTests precedent).
export function resetPrefsStoreForTests(): void {
  prefsStore.setState(loadInitialState());
}
