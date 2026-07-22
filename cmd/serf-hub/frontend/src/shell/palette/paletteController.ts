// The command palette's global opener: a vanilla store the mounted
// <CommandPalette/> subscribes to, plus the two functions the whole app wires
// to (AppShell's ⌘K/Ctrl-K + [data-search-trigger] listeners, Composer's
// leading-"/"-on-empty hook). openPalette seeds the input the same way the
// legacy openWith did (search.js:164-169) - "render immediately, pre-seeded".
//
// T1 ships this singleton and an empty overlay (CommandPalette.tsx); T3 fills
// the overlay body (search + the 22-command registry) reading the same store.
import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";

export interface PaletteState {
  open: boolean;
  query: string;
  // Bumped on every openPalette call. The overlay keys its body on this so
  // EVERY open (even one issued while already open) remounts the body fresh,
  // which is how the atomic "reset state on open" contract (§2.1,
  // search.js:150-161) is met - a fresh mount seeds its state from `query`
  // and starts with a cleared error strip / active row.
  openSeq: number;
}

export const paletteStore = createStore<PaletteState>(() => ({ open: false, query: "", openSeq: 0 }));

export function openPalette(initialQuery = ""): void {
  paletteStore.setState((s) => ({ open: true, query: initialQuery, openSeq: s.openSeq + 1 }));
}

export function closePalette(): void {
  paletteStore.setState({ open: false, query: "" });
}

export function usePaletteStore<T>(selector: (state: PaletteState) => T): T {
  return useStore(paletteStore, selector);
}
