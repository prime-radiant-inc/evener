import type { ViewportClass } from "@evener/appwire-client";

export const LOCAL_KEYS: Record<ViewportClass, string> = {
  desktop: "evener.prefs.transcriptDisplay.desktop",
  mobile: "evener.prefs.transcriptDisplay.mobile",
};

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

export function reportStorageResult(setStorageWarning: StorageWarningSetter, localOK: boolean): void {
  if (localOK) {
    setStorageWarning(null);
    return;
  }
  setStorageWarning(
    "Transcript display changed for this tab, but browser storage is unavailable; it may not survive restart.",
  );
}
