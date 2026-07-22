// Enter-to-send preference (parity-m5-composer.md §J): whether a bare
// Enter submits the composer (on) or inserts a newline, Shift+Enter
// steering (off, the legacy default).
//
// EXACT key, not a wave-local guess: "serf.prefs.enterToSend" is the key a
// future M6 prefs store adopts verbatim (see docs/superpowers/plans/
// 2026-07-21-webui-rewrite-wave5-interaction.md's T2 bullet) - this module
// is that store's stand-in for this wave, a single flat boolean-ish key
// rather than the legacy's nested `serf-hub.composer.enterToSend` JSON
// object, and "1"/"0" string encoding rather than JSON, matching this
// codebase's own existing precedent for a single persisted boolean (see
// shell/rail/Rail.tsx's COLLAPSED_STORAGE_KEY).
export const ENTER_TO_SEND_STORAGE_KEY = "serf.prefs.enterToSend";

// A plain, uncached read (not React state) is deliberate: the legacy
// composer reads this pref FRESH on every keydown, never a value cached at
// an earlier render, so a change from elsewhere can never be observed stale
// mid-keystroke. Every call site (the keydown handler AND the kbd-hint
// render below) calls this directly rather than closing over a stale hook
// value.
export function readEnterToSendPref(): boolean {
  try {
    return localStorage.getItem(ENTER_TO_SEND_STORAGE_KEY) === "1";
  } catch {
    return false;
  }
}

// useEnterToSendPref: this wave's stand-in for the M6 prefs store's own
// hook. No settings UI exists yet to toggle this key from within the same
// tab (M6/M7's job), so there is nothing here to subscribe to for a live
// update - every render already re-reads readEnterToSendPref() fresh (see
// its own comment), which is all a hook wrapper needs to provide today.
export function useEnterToSendPref(): boolean {
  return readEnterToSendPref();
}
