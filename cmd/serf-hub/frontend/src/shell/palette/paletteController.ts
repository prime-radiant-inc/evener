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
}

export const paletteStore = createStore<PaletteState>(() => ({ open: false, query: "" }));

export function openPalette(initialQuery = ""): void {
  paletteStore.setState({ open: true, query: initialQuery });
}

export function closePalette(): void {
  paletteStore.setState({ open: false, query: "" });
}

export function usePaletteStore<T>(selector: (state: PaletteState) => T): T {
  return useStore(paletteStore, selector);
}
