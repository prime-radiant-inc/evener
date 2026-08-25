import { createContext, type ReactNode, useContext, useMemo } from "react";
import { useStore } from "zustand";
import { createStore, type StoreApi } from "zustand/vanilla";
import { decodeFixture } from "./decodeFixture";
import { canonicalFixture } from "./fixtures";
import type { DiagnosticSink, Platform } from "./model";
import {
  decodePreferences,
  encodePreferences,
  preferenceStorageKey,
} from "./persistence";
import { createInitialState, reducePrototype } from "./reducer";
import { projectScenario } from "./scenarios";
import type { PrototypeAction, PrototypeState } from "./state";

export interface PreferenceStorage {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
  removeItem(key: string): void;
}

export interface PrototypeActions {
  dispatch(action: PrototypeAction): void;
  resetPrototype(): void;
}

export type PrototypeStore = PrototypeState & PrototypeActions;

export interface CreateStoreOptions {
  platform: Platform;
  storage: PreferenceStorage;
  fixtureInput: unknown;
  diagnostics: DiagnosticSink;
}

const persistedActions: ReadonlySet<PrototypeAction["type"]> = new Set([
  "selectConcept",
  "setScenario",
  "setAppearance",
  "setTextScale",
  "setReducedMotion",
]);

export function createPrototypeStore(
  options: CreateStoreOptions,
): StoreApi<PrototypeStore> {
  const preferences = decodePreferences(
    options.storage.getItem(preferenceStorageKey),
  );
  const fixture = decodeFixture(
    options.fixtureInput,
    canonicalFixture,
    options.diagnostics,
  );
  const sourceFixture = projectScenario(fixture, "baseline").fixture;
  const initialState = createInitialState({
    platform: options.platform,
    preferences,
    sourceFixture,
    projection: projectScenario(sourceFixture, preferences.scenario),
  });

  return createStore<PrototypeStore>()((set, get) => ({
    ...initialState,
    dispatch(action) {
      let changed = false;
      set((state) => {
        const next = reducePrototype(state, action);
        changed = next !== state;
        return next as PrototypeStore;
      });
      if (changed && persistedActions.has(action.type)) {
        options.storage.setItem(preferenceStorageKey, encodePreferences(get()));
      }
    },
    resetPrototype() {
      options.storage.removeItem(preferenceStorageKey);
      get().dispatch({ type: "reset" });
    },
  }));
}

const PrototypeStoreContext = createContext<StoreApi<PrototypeStore> | null>(
  null,
);

export interface PrototypeProviderProps {
  store: StoreApi<PrototypeStore>;
  children: ReactNode;
}

export function PrototypeProvider({ store, children }: PrototypeProviderProps) {
  return (
    <PrototypeStoreContext.Provider value={store}>
      {children}
    </PrototypeStoreContext.Provider>
  );
}

function usePrototypeStore(): StoreApi<PrototypeStore> {
  const store = useContext(PrototypeStoreContext);
  if (store === null) {
    throw new Error("PrototypeProvider is required");
  }
  return store;
}

export function usePrototypeState<T>(
  selector: (state: PrototypeState) => T,
): T {
  return useStore(usePrototypeStore(), selector);
}

export function usePrototypeActions(): PrototypeActions {
  const store = usePrototypeStore();
  const dispatch = useStore(store, (state) => state.dispatch);
  const resetPrototype = useStore(store, (state) => state.resetPrototype);
  return useMemo(
    () => ({ dispatch, resetPrototype }),
    [dispatch, resetPrototype],
  );
}
