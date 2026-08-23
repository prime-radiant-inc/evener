// useRailSetting — feature flag for the Session Rail, default-ON on desktop.
//
// Persisted to localStorage as "evener:rail" ("on" | "off"). Default is "on"
// on desktop (≥900px viewport), "off" on mobile/tabletish. The user can toggle
// it in Settings; the rail reads this hook to decide whether to mount.

import { useEffect, useState } from "react";
import { useIsMobile } from "../../../shell/useIsMobile";

const STORAGE_KEY = "evener:rail";

function readRailSetting(): boolean | null {
  try {
    const v = localStorage.getItem(STORAGE_KEY);
    if (v === "on") return true;
    if (v === "off") return false;
  } catch {
    // localStorage unavailable (SSR, test env)
  }
  return null;
}

function writeRailSetting(on: boolean) {
  try {
    localStorage.setItem(STORAGE_KEY, on ? "on" : "off");
  } catch {
    // localStorage unavailable
  }
}

/**
 * Returns [railEnabled, setRailEnabled]. Default ON on desktop, OFF on
 * mobile. The user's explicit choice (if any) overrides the default.
 *
 * In jsdom (test environments), matchMedia is unavailable so isMobile
 * returns false, which would enable the rail by default. Since the rail's
 * canvas rendering needs a real browser, tests should explicitly enable or
 * disable the rail as needed; the hook returns false when no real DOM
 * canvas is available (getContext('2d') returns null in jsdom).
 */
export function useRailSetting(): [boolean, (on: boolean) => void] {
  const isMobile = useIsMobile();
  const [enabled, setEnabled] = useState<boolean>(() => {
    const stored = readRailSetting();
    if (stored !== null) return stored;
    // jsdom (test envs) has no matchMedia; the rail's canvas rendering
    // needs a real browser. Default OFF when matchMedia is unavailable.
    if (typeof window !== "undefined" && typeof window.matchMedia !== "function") return false;
    return !isMobile;
  });

  // Update when the mobile state changes and the user hasn't set a preference.
  useEffect(() => {
    const stored = readRailSetting();
    if (stored === null) {
      setEnabled(!isMobile);
    }
  }, [isMobile]);

  const set = (on: boolean) => {
    writeRailSetting(on);
    setEnabled(on);
  };

  return [enabled, set];
}
