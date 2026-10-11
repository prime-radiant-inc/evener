// The app's one disclosure store: the package's framework-free factory bound
// to zustand's useStore for the reactive read. A disclosure's expansion lives
// here rather than in component-local useState so it survives the remount
// that VirtualList (off-window transcript rows) and dockview (a layout change
// unmounting the pane tree) inflict (yt2q). Tests that need a clean slate call
// resetDisclosureStoreForTests; Vitest file isolation keeps this instance from
// crossing files.
import { createDisclosureStore, type DisclosureReadOptions, isDisclosureOpenIn } from "@evener/appwire-client";
import { createContext, useContext, useLayoutEffect } from "react";
import { useStore } from "zustand";

const store = createDisclosureStore();

/** Only a view that promises reload retention opts in. The namespace includes
 * its owning public ref and category; unrelated transcript disclosures remain
 * page-lifetime choices. This binding persists booleans, not domain data. */
export const DisclosurePersistenceContext = createContext<string | null>(null);
export const DISCLOSURE_STORAGE_KEY = "evener.disclosure.v1";
export const DISCLOSURE_STORAGE_LIMIT = 2000;

let savedRaw: string | null = null;
let savedChoices = new Map<string, boolean>();

function readChoices(): ReadonlyMap<string, boolean> {
  const raw = localStorage.getItem(DISCLOSURE_STORAGE_KEY);
  if (raw !== savedRaw) {
    savedRaw = raw;
    savedChoices = new Map();
    const parsed: unknown = JSON.parse(raw ?? "null");
    if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
      for (const [key, value] of Object.entries(parsed).slice(-DISCLOSURE_STORAGE_LIMIT))
        if (typeof value === "boolean") savedChoices.set(key, value);
    }
  }
  return savedChoices;
}

function saveChoice(key: string, choice: boolean | undefined): void {
  let choices: Map<string, boolean>;
  try {
    choices = new Map(readChoices());
  } catch {
    choices = new Map();
  }
  choices.delete(key);
  if (choice !== undefined) choices.set(key, choice);
  for (const oldest of choices.keys()) {
    if (choices.size <= DISCLOSURE_STORAGE_LIMIT) break;
    choices.delete(oldest);
  }
  localStorage.setItem(DISCLOSURE_STORAGE_KEY, JSON.stringify(Object.fromEntries(choices)));
}

function useDisclosurePersistence(id: string): void {
  const scope = useContext(DisclosurePersistenceContext);
  useLayoutEffect(() => {
    if (scope === null) return;
    const key = JSON.stringify([scope, id]);
    try {
      const saved = readChoices().get(key);
      if (!store.getState().open.has(id) && saved !== undefined) store.setOpen(id, saved);
    } catch {
      // Storage is optional; the existing in-memory owner stays usable.
    }
    return store.subscribe((state, previous) => {
      const choice = state.open.get(id)?.open;
      if (choice === previous.open.get(id)?.open) return;
      try {
        saveChoice(key, choice);
      } catch {
        // Failed persistence must not undo the reader's live choice.
      }
    });
  }, [id, scope]);
}

/** Reactive: re-renders the caller when this id's open state changes. This IS
 * a custom hook (it rides zustand's useStore, exactly as useSubagentRows does);
 * it is only ever called at the top of a component's render (Disclosure). Its
 * name follows the boolean-predicate shape the interface specifies rather than
 * a use- prefix, so biome's hook-name heuristic can't recognize it as a hook. */
export function isDisclosureOpen(id: string, fallback: boolean, options?: DisclosureReadOptions): boolean {
  // biome-ignore lint/correctness/useHookAtTopLevel: this predicate is a custom hook, as explained below
  useDisclosurePersistence(id);
  // biome-ignore lint/correctness/useHookAtTopLevel: custom hook wrapping useStore; called unconditionally at the top of Disclosure's render, only the non-use- name defeats the heuristic
  return useStore(store, (s) => isDisclosureOpenIn(s, id, fallback, options));
}

export const setDisclosureOpen = store.setOpen;
export const toggleDisclosure = store.toggle;
export const beginDisclosureBaseline = store.beginBaseline;
export const disclosureDefault = store.defaultFor;
export const clearDisclosureScope = store.clearScope;

export function resetDisclosureStoreForTests(): void {
  store.setState(store.getInitialState());
  savedRaw = null;
  savedChoices = new Map();
}
