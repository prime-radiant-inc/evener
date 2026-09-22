import { legacyWritesFromConfig, type TranscriptDisplayConfigV1, type ViewportClass } from "@evener/appwire-client";
import { readLegacyPreference } from "../prefs";

export const LOCAL_KEYS: Record<ViewportClass, string> = {
  desktop: "evener.prefs.transcriptDisplay.desktop",
  mobile: "evener.prefs.transcriptDisplay.mobile",
};

export const LEGACY_KEYS = [
  "transcriptRoundTimings",
  "transcriptTokenCounts",
  "transcriptHookExitsAll",
  "transcriptHookExitsNormal",
  "transcriptPromptLoaded",
  "showCost",
] as const;

export type StorageWarningSetter = (message: string | null) => void;

export function writeLocal(layout: ViewportClass, encoded: string): boolean {
  try {
    if (typeof localStorage === "undefined") throw new Error("localStorage is unavailable");
    localStorage.setItem(LOCAL_KEYS[layout], encoded);
    if (localStorage.getItem(LOCAL_KEYS[layout]) !== encoded) throw new Error("localStorage did not retain the value");
    return true;
  } catch {
    return false;
  }
}

export function removeLocal(layout: ViewportClass): boolean {
  try {
    if (typeof localStorage === "undefined") throw new Error("localStorage is unavailable");
    localStorage.removeItem(LOCAL_KEYS[layout]);
    if (localStorage.getItem(LOCAL_KEYS[layout]) !== null) throw new Error("localStorage retained the value");
    return true;
  } catch {
    return false;
  }
}

export function verifyLegacyWrite(config: TranscriptDisplayConfigV1): boolean {
  try {
    const expected = legacyWritesFromConfig(config);
    for (const key of LEGACY_KEYS) {
      const raw = readLegacyPreference(key);
      if (raw !== (expected[key] ? "1" : "0")) return false;
    }
    return true;
  } catch {
    return false;
  }
}

export function reportStorageResult(
  setStorageWarning: StorageWarningSetter,
  localOK: boolean,
  legacyOK: boolean,
): void {
  if (localOK && legacyOK) {
    setStorageWarning(null);
    return;
  }
  setStorageWarning(
    "Transcript display changed for this tab, but browser storage is unavailable; it may not survive restart.",
  );
}
