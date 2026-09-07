// prefs.ts is the localStorage-backed home for every settings preference
// that has no server-side counterpart (Theme/Transcript/Display/
// Notifications - see docs/web-ui/parity/parity-m7-settings.md §§3-6):
// General/Hub/Storage read live daemon state through settingsOverview.ts
// instead. Every key lives under the "evener.prefs.<name>" contract (wave-7
// plan): a flat, one-value-per-key namespace, NOT the legacy's per-section
// JSON blobs (evener-hub.composer, evener-hub.transcript.systemStatus, ...).
// evener.prefs.enterToSend/evener.prefs.showCost are PINNED: this is a live
// contract reachable from Settings today, not a hypothetical one - Settings
// -> Display's own setEnterToSend/setShowCost (below) write both keys, and
// Composer.tsx reads enterToSend from this same store on every render and
// keydown. The encoding already broke this contract once during
// development: commit 932eeddca ("fix boolean encoding to '1'/'0'
// (cross-wave contract break)") fixed readBool/writeBool back from a brief
// "true"/"false" regression. Never repeat it - the key names and encoding
// are permanent, not just this store's own choice, the way every other key
// below is. The PIN covers the key NAME and the value ENCODING; a default
// (what an unset key reads as) is a product decision and stays free to
// change - a browser with a stored "1"/"0" is unaffected either way.
//
// transcript.roundTimings / transcript.tokenCounts / showCost each gate one
// segment of the per-turn transcript meta line (TurnSeparator: duration,
// token counts, cost). All three default OFF: the transcript is for prose
// and tool calls, and the session Details panel already carries the exact
// figures. The footer status strip's session-total cost is NOT gated by
// showCost - that is the glance-level supervisory number, not per-turn
// clutter.
//
// Hydrated once from localStorage at module load (mirroring every other
// store's singleton-at-import-time shape - connection.ts/threads.ts/
// tree.ts), then kept live: every setter writes through to localStorage
// immediately ("persisting on set") and updates the Zustand state in the
// same call, so a subscribed component re-renders with no separate
// "reapply" step: a controlled React input driven straight off this store's
// state is always correct on every render.
//
// Theme is the one preference with a real, already-wired visual consumer
// (src/styles/tokens.css keys light-theme overrides off `[data-theme="light"]`
// on the document root; the default `:root` block IS the dark theme, so
// "dark" has no override block of its own). setTheme mirrors the legacy's
// own assets/theme.js "system is absence, never the literal string"
// contract: light/dark persist and set data-theme directly; "system"
// persists nothing and instead resolves live against the OS's own
// prefers-color-scheme (systemPrefersDark below) to one of the same two
// document states - tokens.css itself gains no new media query (it still
// has only the two blocks above), applyTheme just picks between them
// instead of unconditionally clearing the attribute the way it used to.
// The section's own help copy ("default follows your OS preference") is
// what this makes true; before this, "system" always rendered dark
// regardless of the OS. phoneDensity/fontSize/transcriptMeasure
// get the same document-mirroring treatment the legacy's settings-
// appearance.js gave them (onto document.body.dataset), and tokens.css now
// keys off both: the type ramp scales off <body data-font-size> and the
// phone line-height off <body data-phone-density> (its "font-size + phone-
// density preference application" block), so the mirror changes what renders.
// sidebarHidden is a plain persisted BOOLEAN (desktop only): true = the
// docked rail is hidden behind the top-left ☰ chip. It replaced the former
// tri-state sidebarMode (auto/pane/rail overlay-drawer system, removed
// 2026-07-24) — a stale evener.prefs.sidebarMode key is simply never read.
// No document mirror: RailHost reads it directly.
// sidebarWidth is the docked rail's dragged width in CSS pixels (desktop
// only; the mobile drawer sizes itself). Always CLAMPED on read as well as
// on write — a hand-edited, corrupted or stale-bounds localStorage value
// must never be able to render a 4px-wide or workspace-eating rail, which
// no drag could then recover from.
//
// No cross-tab `storage` event sync: deliberately omitted, matching the
// legacy exactly - none of assets/{theme,settings-appearance,settings-
// transcript,settings-display,settings-notifications}.js attach a
// `window.addEventListener("storage", ...)` listener either, so a change
// in one tab has never live-updated another already-open tab's state in
// this app. Not a gap this task introduces.

import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import {
  dualWriteLegacyPreferences,
  encodeLocalConfig,
  type LegacyPreferenceKey,
  type LegacyPreferenceValues,
  legacyConfigFromValues,
  type TranscriptDisplayConfigV1,
  type TranscriptViewportClass,
} from "../transcriptDisplay/config";

export type ThemePref = "system" | "light" | "dark";
export type PhoneDensityPref = "compact" | "comfortable";
export type FontSizePref = "s" | "m" | "l" | "xl";
// Settings -> Theme -> Transcript width: the conversation column's measure.
// tokens.css keys --session-measure off <body data-transcript-measure>
// (44rem reading, 64rem wide).
export type TranscriptMeasurePref = "reading" | "wide";
export type TranscriptStatusKey = "roundTimings" | "tokenCounts" | "hookExitsAll" | "hookExitsNormal" | "promptLoaded";
export type NotificationKey = "title" | "favicon" | "os" | "sound";
export type NotificationsLoudScopePref = "asks" | "all";

export interface PrefsStoreState {
  theme: ThemePref;
  phoneDensity: PhoneDensityPref;
  sidebarHidden: boolean;
  sidebarWidth: number;
  fontSize: FontSizePref;
  transcriptMeasure: TranscriptMeasurePref;
  transcript: Record<TranscriptStatusKey, boolean>;
  // Composer prefs (Display section, parity-m7-settings.md §5). Field names
  // match the PINNED evener.prefs.enterToSend / evener.prefs.showCost keys.
  enterToSend: boolean;
  showCost: boolean;
  // Keybindings (Settings -> Keybindings): whether character-key triggers -
  // today exactly the "?" cheatsheet-overlay trigger - are live. WCAG 2.1.4
  // requires a way to turn single-character shortcuts off; default ON (the
  // turn-off exists, per the p4 plan's Design decision 3). Browser-local by
  // controller ruling: NOT a hub settings key.
  characterKeyTriggers: boolean;
  // The settings section the user last visited (a sections.ts id), so
  // reopening Settings returns there instead of always landing on General.
  // null = never visited (or cleared). Persisted as the RAW section-id
  // string with no validation here - this store must not import the
  // settings section inventory (layering), so an unrecognized stored value
  // round-trips and the CONSUMER (Settings.tsx) falls back to the default
  // section via isKnownSettingsSection.
  lastSettingsSection: string | null;
  notifications: Record<NotificationKey, boolean>;
  notificationsLoudScope: NotificationsLoudScopePref;

  setTheme(value: ThemePref): void;
  setPhoneDensity(value: PhoneDensityPref): void;
  setSidebarHidden(value: boolean): void;
  setSidebarWidth(value: number): void;
  setFontSize(value: FontSizePref): void;
  setTranscriptMeasure(value: TranscriptMeasurePref): void;
  setTranscriptStatus(key: TranscriptStatusKey, value: boolean): void;
  setEnterToSend(value: boolean): void;
  setShowCost(value: boolean): void;
  setCharacterKeyTriggers(value: boolean): void;
  setLastSettingsSection(value: string | null): void;
  setNotification(key: NotificationKey, value: boolean): void;
  setNotificationsLoudScope(value: NotificationsLoudScopePref): void;
}

const KEY_PREFIX = "evener.prefs.";

const TRANSCRIPT_DISPLAY_LOCAL_KEYS: Record<TranscriptViewportClass, string> = {
  desktop: "transcriptDisplay.desktop",
  mobile: "transcriptDisplay.mobile",
};

const LEGACY_TRANSCRIPT_KEYS: readonly LegacyPreferenceKey[] = [
  "transcriptRoundTimings",
  "transcriptTokenCounts",
  "transcriptHookExitsAll",
  "transcriptHookExitsNormal",
  "transcriptPromptLoaded",
  "showCost",
];

// --- localStorage access: every read/write is best-effort - a private
// browsing mode that throws on storage access must never be fatal to the
// settings UI.
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
    // Best-effort: a full quota (or a browser that blocks storage entirely)
    // must never be fatal to the settings UI; the in-memory state this call
    // also sets still takes effect for the rest of the session.
  }
}

function removeRaw(name: string): void {
  try {
    localStorage.removeItem(`${KEY_PREFIX}${name}`);
  } catch {
    // Best-effort, same rationale as writeRaw.
  }
}

// These adapters deliberately name the only compatibility keys that the
// transcript-display store may touch. Callers cannot use them to turn this
// module into a general localStorage wrapper.
export function readLegacyPreference(key: LegacyPreferenceKey): string | null {
  return readRaw(key);
}

export function hasLegacyPreference(key: LegacyPreferenceKey): boolean {
  return readLegacyPreference(key) !== null;
}

export function writeLegacyBooleanPreference(key: LegacyPreferenceKey, value: boolean): void {
  writeBool(key, value);
}

export function removeLegacyPreference(key: LegacyPreferenceKey): void {
  removeRaw(key);
}

export function readTranscriptDisplayLocal(layout: TranscriptViewportClass): string | null {
  return readRaw(TRANSCRIPT_DISPLAY_LOCAL_KEYS[layout]);
}

export function hasTranscriptDisplayLocal(layout: TranscriptViewportClass): boolean {
  return readTranscriptDisplayLocal(layout) !== null;
}

export function writeTranscriptDisplayLocal(layout: TranscriptViewportClass, encodedConfig: string): void {
  writeRaw(TRANSCRIPT_DISPLAY_LOCAL_KEYS[layout], encodedConfig);
}

export function writeTranscriptDisplayConfig(layout: TranscriptViewportClass, config: TranscriptDisplayConfigV1): void {
  writeTranscriptDisplayLocal(layout, encodeLocalConfig(config));
}

export function removeTranscriptDisplayLocal(layout: TranscriptViewportClass): void {
  removeRaw(TRANSCRIPT_DISPLAY_LOCAL_KEYS[layout]);
}

export const readLegacyPref = readLegacyPreference;
export const hasLegacyPref = hasLegacyPreference;
export const writeLegacyBool = writeLegacyBooleanPreference;
export const removeLegacyPref = removeLegacyPreference;

/**
 * Convert the six pre-v1 global transcript keys once, before the new
 * layout-specific store loads. Existing keys are intentionally retained.
 */
export function migrateLegacyTranscriptDisplay(): TranscriptDisplayConfigV1 | undefined {
  if (hasTranscriptDisplayLocal("desktop") || hasTranscriptDisplayLocal("mobile")) return undefined;
  const values: LegacyPreferenceValues = Object.fromEntries(
    LEGACY_TRANSCRIPT_KEYS.map((key) => [key, readLegacyPreference(key)]),
  ) as LegacyPreferenceValues;
  const config = legacyConfigFromValues(values);
  if (config === undefined) return undefined;
  writeTranscriptDisplayConfig("desktop", config);
  writeTranscriptDisplayConfig("mobile", config);
  return config;
}

export const migrateLegacyPrefs = migrateLegacyTranscriptDisplay;

/** Keep old transcript consumers in sync without changing their 1/0 wire. */
export function dualWriteTranscriptDisplayLegacy(config: TranscriptDisplayConfigV1): void {
  dualWriteLegacyPreferences(config, writeLegacyBooleanPreference);
}

export const dualWriteLegacyPrefs = dualWriteTranscriptDisplayLegacy;

// "1"/"0", not JS's "true"/"false": every boolean this store persists
// (enterToSend, showCost, every transcript.* and notifications.* member)
// uses this one encoding, so readBool/writeBool only have to get it right
// once. evener.prefs.enterToSend/evener.prefs.showCost in
// particular are a live contract reachable from Settings today (Display's
// own setEnterToSend/setShowCost, Composer.tsx's own enterToSend read) -
// this encoding already broke that contract once during development
// (commit 932eeddca, "fix boolean encoding to '1'/'0' (cross-wave contract
// break)"): switching back to "true"/"false" would silently strand every
// "1"/"0" value already written as unrecognized, reading back as this
// function's fallback rather than erroring.
function readBool(name: string, fallback: boolean): boolean {
  const raw = readRaw(name);
  if (raw === "1") return true;
  if (raw === "0") return false;
  return fallback; // absent, or corrupted - never silently coerce to false
}

function writeBool(name: string, value: boolean): void {
  writeRaw(name, value ? "1" : "0");
}

// Docked-rail width bounds, in CSS pixels. Exported because they are one
// contract with three consumers: this store's clamp, the resize handle's
// aria-valuemin/aria-valuemax, and the drag arithmetic itself.
//
// SIDEBAR_WIDTH_MIN is measured against the rail's own header content rather
// than picked for looks: at 200px the brand row, the full-width search field
// (its "Search" label plus the ⌘K chord) and the "+ New session" button all
// still render on one line each inside the header's 2×var(--space-4) padding,
// verified in a real browser at every font-size pref. Narrower starts
// clipping the search field's chord.
// SIDEBAR_WIDTH_MAX keeps the rail a sidebar: past ~560px it starts eating
// the workspace dockview needs rather than buying any more title text (rows
// truncate with an ellipsis, so extra width has diminishing returns).
export const SIDEBAR_WIDTH_MIN = 200;
export const SIDEBAR_WIDTH_MAX = 560;
export const SIDEBAR_WIDTH_DEFAULT = 280;

export function clampSidebarWidth(value: number): number {
  if (!Number.isFinite(value)) return SIDEBAR_WIDTH_DEFAULT;
  return Math.round(Math.min(SIDEBAR_WIDTH_MAX, Math.max(SIDEBAR_WIDTH_MIN, value)));
}

// Numeric prefs persist as a plain base-10 string. Anything unparseable
// (absent key, "", "abc", "NaN") falls back rather than coercing to 0 the way
// Number("") would - the same "absent or corrupted, never silently coerce"
// rule readBool follows. The caller supplies the clamp, so an in-range
// fallback plus an out-of-range stored value both land somewhere usable.
function readNumber(name: string, fallback: number, clamp: (value: number) => number): number {
  const raw = readRaw(name);
  if (raw === null || raw.trim() === "") return fallback;
  const parsed = Number(raw);
  return Number.isFinite(parsed) ? clamp(parsed) : fallback;
}

function writeNumber(name: string, value: number): void {
  writeRaw(name, String(value));
}

function readEnum<T extends string>(name: string, allowed: readonly T[], fallback: T): T {
  const raw = readRaw(name);
  return raw !== null && (allowed as readonly string[]).includes(raw) ? (raw as T) : fallback;
}

const THEME_VALUES: readonly ThemePref[] = ["system", "light", "dark"];
const PHONE_DENSITY_VALUES: readonly PhoneDensityPref[] = ["compact", "comfortable"];
const FONT_SIZE_VALUES: readonly FontSizePref[] = ["s", "m", "l", "xl"];
const TRANSCRIPT_MEASURE_VALUES: readonly TranscriptMeasurePref[] = ["reading", "wide"];
const LOUD_SCOPE_VALUES: readonly NotificationsLoudScopePref[] = ["asks", "all"];

// Per-field localStorage key names for the two grouped record fields -
// transcript/notifications are exposed as small in-memory records for a
// convenient single destructure at each section's call site, but each
// member still round-trips through its OWN flat evener.prefs.<name> key
// (never one shared JSON blob), per this file's own top comment.
const TRANSCRIPT_KEY_NAMES: Record<TranscriptStatusKey, string> = {
  roundTimings: "transcriptRoundTimings",
  tokenCounts: "transcriptTokenCounts",
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
    // On by default as of the five-participant study (docs/web-ui/ux-plan-2026-07.md).
    // Its most-repeated finding, from four participants independently on
    // different tasks, was that the older server-rendered UI showed per-step
    // timing with zero clicks and this one showed none anywhere - and that,
    // not typography, was why the old build read better.
    //
    // It defaulted off for a good reason that has since expired: the old form
    // was a nine-field raw dump of nanosecond durations (kata 7zkv reworked it
    // into a summary). What renders now is one line per round plus a short
    // suffix on tool-call summaries the eye already scans, with the breakdown
    // collapsed behind a click.
    //
    // A persona panel voted 3-1 to keep it off, arguing per-row chrome taxes
    // readers who never look at it. The fifth participant used a real session
    // both ways and overruled that: the panel was weighing hypothetical
    // clutter, while four people doing real work each independently noticed
    // the absence.
    roundTimings: readBool(TRANSCRIPT_KEY_NAMES.roundTimings, true),
    tokenCounts: readBool(TRANSCRIPT_KEY_NAMES.tokenCounts, false),
    hookExitsAll: readBool(TRANSCRIPT_KEY_NAMES.hookExitsAll, false),
    hookExitsNormal: readBool(TRANSCRIPT_KEY_NAMES.hookExitsNormal, false),
    // Defaults ON, unlike its two neighbours, because it is the only one of the
    // three that governs items the transcript ALREADY renders: the system-prompt
    // scaffold and each "prompt loaded" notice are unfiltered until this pref
    // gains a consumer. Off would make a working, visible feature disappear for
    // everyone who never opened Settings. The hook toggles govern lines nothing
    // renders yet, so off costs those nothing.
    promptLoaded: readBool(TRANSCRIPT_KEY_NAMES.promptLoaded, true),
  };
}

// code-wins (wave-7 pre-adjudication): the floor doc's own §6 flags a
// copy/code discrepancy (page copy claimed title/favicon default on; both
// the static markup and the legacy JS default landed all four at OFF) - all
// four shipped false, replicating the CODE's behavior.
//
// 2026-08-14 reversal (docs/web-ui/decisions.md): title flips back to ON by
// default. The all-OFF floor made the attention system invisible to anyone
// who never opened Settings - the count badge/title-bar prefix is the
// quietest, most reversible of the four channels (no OS permission prompt,
// no sound), so it's the one that ships on. favicon/os/sound stay OFF; this
// is a one-channel exception to the floor, not a re-litigation of it. As
// with every other pref here, this only changes the ABSENT-key default - a
// browser that already stored an explicit "0" for title keeps reading false
// (see prefs.test.ts's "existing opt-out survives the default flip").
function loadNotifications(): Record<NotificationKey, boolean> {
  return {
    title: readBool(NOTIFICATION_KEY_NAMES.title, true),
    favicon: readBool(NOTIFICATION_KEY_NAMES.favicon, false),
    os: readBool(NOTIFICATION_KEY_NAMES.os, false),
    sound: readBool(NOTIFICATION_KEY_NAMES.sound, false),
  };
}

// systemPrefersDark resolves the OS's current color-scheme preference for
// "system" theme mode via the standard prefers-color-scheme media query.
// Guarded for environments with no matchMedia at all - this project's own
// jsdom test environment has none (confirmed empirically) - falling back to
// dark, this file's own pre-existing "system has always rendered dark"
// default, so a matchMedia-less environment degrades to exactly the
// behavior this store had before this query existed.
function systemPrefersDark(): boolean {
  return typeof window === "undefined" || typeof window.matchMedia !== "function"
    ? true
    : window.matchMedia("(prefers-color-scheme: dark)").matches;
}

// --- document application: the DOM side effects the legacy mirrored
// alongside localStorage (assets/theme.js; assets/settings-appearance.js's
// two script-parse-time IIFEs) - see this file's own top comment for which
// prefs get one.
function applyTheme(value: ThemePref): void {
  if (value === "system") {
    // Resolves to one of the same two document states an explicit pick
    // would - light sets the attribute, dark removes it (never the
    // literal string "dark"), matching this function's own explicit-value
    // branch below applied to whichever the OS currently prefers.
    if (systemPrefersDark()) document.documentElement.removeAttribute("data-theme");
    else document.documentElement.setAttribute("data-theme", "light");
  } else {
    document.documentElement.setAttribute("data-theme", value);
  }
  ensureSystemSchemeListener();
}

// While theme==="system", re-applies whenever the OS's own color-scheme
// preference changes - installed lazily (on first applyTheme() call whose
// environment actually has matchMedia) rather than unconditionally at
// module load, specifically so a test can stub matchMedia AFTER this
// module has already been imported (module-eval order would otherwise
// always see the real, matchMedia-less jsdom environment) and still
// observe this get wired up on its own next resetPrefsStoreForTests()/
// applyTheme() call - see resetPrefsStoreForTests's own reset of the
// installed-flag below. A no-op guard (early return inside the handler)
// for explicit light/dark, not an add/remove-listener dance, since the one
// listener never needs to come off - it simply stops doing anything.
let systemSchemeListenerInstalled = false;

function handleSystemSchemeChange(): void {
  if (prefsStore.getState().theme === "system") applyTheme("system");
}

function ensureSystemSchemeListener(): void {
  if (systemSchemeListenerInstalled) return;
  if (typeof window === "undefined" || typeof window.matchMedia !== "function") return;
  systemSchemeListenerInstalled = true;
  window.matchMedia("(prefers-color-scheme: dark)").addEventListener("change", handleSystemSchemeChange);
}

function applyPhoneDensity(value: PhoneDensityPref): void {
  document.body.dataset.phoneDensity = value;
}

function applyFontSize(value: FontSizePref): void {
  document.body.dataset.fontSize = value;
}

function applyTranscriptMeasure(value: TranscriptMeasurePref): void {
  document.body.dataset.transcriptMeasure = value;
}

// loadInitialState re-derives every field from localStorage (plus the
// document side effects above) - shared by the store's own creator function
// and resetPrefsStoreForTests(), so a test that seeds localStorage and then
// resets sees exactly what a fresh module load would have produced.
function loadInitialState(): Omit<
  PrefsStoreState,
  | "setTheme"
  | "setPhoneDensity"
  | "setSidebarHidden"
  | "setSidebarWidth"
  | "setFontSize"
  | "setTranscriptMeasure"
  | "setTranscriptStatus"
  | "setEnterToSend"
  | "setShowCost"
  | "setCharacterKeyTriggers"
  | "setLastSettingsSection"
  | "setNotification"
  | "setNotificationsLoudScope"
> {
  const theme = readEnum("theme", THEME_VALUES, "system");
  const phoneDensity = readEnum("phoneDensity", PHONE_DENSITY_VALUES, "compact");
  const fontSize = readEnum("fontSize", FONT_SIZE_VALUES, "m");
  const transcriptMeasure = readEnum("transcriptMeasure", TRANSCRIPT_MEASURE_VALUES, "reading");
  applyTheme(theme);
  applyPhoneDensity(phoneDensity);
  applyFontSize(fontSize);
  applyTranscriptMeasure(transcriptMeasure);
  return {
    theme,
    phoneDensity,
    sidebarHidden: readBool("sidebarHidden", false),
    sidebarWidth: readNumber("sidebarWidth", SIDEBAR_WIDTH_DEFAULT, clampSidebarWidth),
    fontSize,
    transcriptMeasure,
    transcript: loadTranscript(),
    enterToSend: readBool("enterToSend", false),
    showCost: readBool("showCost", false),
    characterKeyTriggers: readBool("characterKeyTriggers", true),
    lastSettingsSection: readRaw("lastSettingsSection"),
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

  setSidebarHidden(value) {
    writeBool("sidebarHidden", value);
    set({ sidebarHidden: value });
  },

  // Clamps on the way in too, so the persisted value can never be out of
  // bounds even if a caller does its own (looser) arithmetic first.
  setSidebarWidth(value) {
    const width = clampSidebarWidth(value);
    writeNumber("sidebarWidth", width);
    set({ sidebarWidth: width });
  },

  setFontSize(value) {
    writeRaw("fontSize", value);
    applyFontSize(value);
    set({ fontSize: value });
  },

  setTranscriptMeasure(value) {
    writeRaw("transcriptMeasure", value);
    applyTranscriptMeasure(value);
    set({ transcriptMeasure: value });
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

  setCharacterKeyTriggers(value) {
    writeBool("characterKeyTriggers", value);
    set({ characterKeyTriggers: value });
  },

  setLastSettingsSection(value) {
    // null is absence (never written as a literal), same contract as
    // theme's "system".
    if (value === null) {
      removeRaw("lastSettingsSection");
    } else {
      writeRaw("lastSettingsSection", value);
    }
    set({ lastSettingsSection: value });
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

// hydrate re-derives every field from whatever's in localStorage right now
// (loadInitialState) and overwrites the store with it, reapplying the
// document side effects along the way. Shared by initPrefs (production) and
// resetPrefsStoreForTests (test-only) below - the two exist as separate
// public names for two different callers' intents (see each one's own doc
// comment), not because the underlying work differs.
function hydrate(): void {
  prefsStore.setState(loadInitialState());
}

// initPrefs is this store's production entry point - the wave-7 review's
// "prefs hydration reachability" finding: this module's own createStore
// initializer below already runs loadInitialState() once, synchronously,
// the moment prefs.ts is first imported (a plain JS module-eval guarantee,
// same as every other store here) - so in principle nothing further is
// needed. In practice, NOTHING today imports prefs.ts until a user opens
// Settings and one of the four prefs-driven sections (Theme/Transcript/
// Display/Notifications) mounts: panes are React.lazy()'d
// (shell/AppShell.tsx), and the "Settings" chunk is a separate bundle from
// the eagerly-loaded app shell (confirmed via `npm run build`'s own chunk
// list). A saved theme/density/font-size would therefore render wrong
// (falling through to the un-mirrored defaults) for the entire session
// until Settings happens to be opened.
//
// initPrefs() exists to give the app root (Settings.tsx/AppShell.tsx are
// frozen this wave, not in this store's manifest - see the wave-7 report
// for the exact one-line call the controller still needs to add) an
// explicit, named, guaranteed-not-dead-code call site to import and invoke
// as its own early-boot step - AppShell.tsx already has exactly this shape
// for three OTHER "make sure this module's top-level side effect has run"
// needs (`import "../panes/welcome"; // registers the "welcome" pane type`
// and its two siblings), but those are bare side-effect imports with no
// bound name. A bare `import "./stores/prefs"` would work today (this
// project's package.json declares no `sideEffects: false`, so nothing
// currently tree-shakes it away) but has no visible "used" binding at the
// call site, unlike the three pane-registration imports it would sit next
// to (whose OWN necessity is at least somewhat self-evident from their
// comment) - a real, if modest, risk of a future cleanup pass or a
// stricter bundler config silently dropping it. A named function call
// cannot be mistaken for dead code, and is directly testable without
// reaching for `vi.resetModules()` + dynamic `import()` gymnastics to
// simulate "before this module was ever imported" (see this file's own
// "initPrefs (production entry point)" describe block, which calls
// exactly this function with no section rendered).
//
// Idempotent and safe to call more than once: it just re-derives from
// localStorage and reapplies the same document attributes, identical to
// what already happened at module-eval time - calling it again when
// nothing has changed is a harmless no-op re-application.
export function initPrefs(): void {
  hydrate();
}

// resetPrefsStoreForTests re-derives every field from whatever's in
// localStorage right now and overwrites the store with it - letting a test
// seed localStorage and then observe the hydration a fresh module load
// would have produced, without actually reloading the module. No
// production code should ever call this (mirrors threads.ts's
// resetThreadsStoreForTests / tree.ts's resetTreeStoreForTests precedent) -
// use initPrefs() instead for the production "make sure this ran" case.
//
// Also resets the module-private system-scheme-listener-installed flag
// (ensureSystemSchemeListener's own doc comment) - a test that stubs
// window.matchMedia AFTER this module was first imported needs the next
// hydrate() to retry installing against that stub rather than seeing the
// flag already latched true from module-eval time's real, matchMedia-less
// jsdom environment.
export function resetPrefsStoreForTests(): void {
  systemSchemeListenerInstalled = false;
  hydrate();
}
