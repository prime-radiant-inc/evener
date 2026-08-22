/**
 * Preferences store — theme, reduced motion, and Dynamic Type category.
 *
 * Owns only user appearance choices; never stores credentials or drafts.
 * The theme is `system` by default and resolved to light/dark at render time
 * via the `prefers-color-scheme` media query (or the explicit `data-theme`
 * override set when `theme !== "system"`).
 */

import { create } from "zustand";
import type { ContentSizeCategory } from "../native/contract";

export type ThemeChoice = "system" | "light" | "dark";

export interface PreferencesState {
  readonly theme: ThemeChoice;
  readonly reducedMotion: boolean;
  readonly contentSize: ContentSizeCategory;
  setTheme(theme: ThemeChoice): void;
  setReducedMotion(reduced: boolean): void;
  setContentSize(category: ContentSizeCategory): void;
}

export function createPreferencesStore() {
  return create<PreferencesState>((set) => ({
    theme: "system",
    reducedMotion: false,
    contentSize: "large",
    setTheme: (theme) => set({ theme }),
    setReducedMotion: (reducedMotion) => set({ reducedMotion }),
    setContentSize: (contentSize) => set({ contentSize }),
  }));
}
