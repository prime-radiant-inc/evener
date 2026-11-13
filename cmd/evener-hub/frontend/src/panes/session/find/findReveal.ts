// What Find is landed on in each transcript view, for the renderers that fold
// their text: a tool body that would hide the matching words shows them
// instead. One landing per view (its disclosure scope), so two views of one
// conversation do not disturb each other; a landing clears when Find moves on
// or closes.
import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import { useOptionalTranscriptRenderContext } from "../../../transcriptDisplay/renderContext";

interface Landing {
  itemId: string;
  needle: string;
}

const store = createStore<{ landings: ReadonlyMap<string, Landing> }>(() => ({ landings: new Map() }));

/** `needle` is the shared find needle (findNeedle); a null item clears the
 * view's landing. */
export function revealForFind(scope: string, itemId: string | null, needle: string): void {
  const current = store.getState().landings.get(scope);
  if (itemId === null ? current === undefined : current?.itemId === itemId && current.needle === needle) return;
  const landings = new Map(store.getState().landings);
  if (itemId === null) landings.delete(scope);
  else landings.set(scope, { itemId, needle });
  store.setState({ landings });
}

/** The needle Find is landed on in this item, in the transcript view this
 * renderer belongs to, or "" when it is not the landing. */
export function useFindReveal(itemId: string): string {
  const scope = useOptionalTranscriptRenderContext()?.disclosureScope ?? "";
  return useStore(store, (state) => {
    const landing = state.landings.get(scope);
    return itemId !== "" && landing?.itemId === itemId ? landing.needle : "";
  });
}
