// What Find is currently landed on, for the renderers that fold their text:
// a tool body that would drop the matching words shows the stretch holding
// them instead. One landing at a time; it clears when Find moves on or closes.
import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";

interface FindReveal {
  itemId: string | null;
  needle: string;
}

const store = createStore<FindReveal>(() => ({ itemId: null, needle: "" }));

/** `needle` is the shared find needle (findNeedle); null clears the landing. */
export function revealForFind(itemId: string | null, needle: string): void {
  const state = store.getState();
  if (state.itemId === itemId && state.needle === needle) return;
  store.setState({ itemId, needle: itemId === null ? "" : needle });
}

/** The needle Find landed on in this item, or "" when it is not the landing. */
export function useFindReveal(itemId: string): string {
  return useStore(store, (state) => (itemId !== "" && state.itemId === itemId ? state.needle : ""));
}
