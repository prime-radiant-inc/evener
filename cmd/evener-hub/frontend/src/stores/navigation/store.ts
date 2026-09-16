// The web's one navigation store: the package's createNavigationStore bound
// to the browser's rail-expansion persistence, with zustand's useStore for
// the reactive read. Every navigation rule - the capability gate, the
// revalidator wiring, boot fan-out, invalidation sequencing, expansion and
// the convergence wait - is the package's; this file owns the browser
// bindings and the names the app has always imported.
import { createNavigationStore, type NavigationStoreState } from "@evener/appwire-client/state/navigation";
import { useStore } from "zustand";
import { railExpansionPersistence } from "./persistence";

export type { NavigationStoreState };

export const navigationStore = createNavigationStore({ persistence: railExpansionPersistence });

export function useNavigationStore<T>(selector: (s: NavigationStoreState) => T): T;
export function useNavigationStore(): NavigationStoreState;
export function useNavigationStore<T>(selector?: (s: NavigationStoreState) => T): T | NavigationStoreState {
  // biome-ignore lint/correctness/useHookAtTopLevel: both branches call the same Zustand hook
  return selector ? useStore(navigationStore, selector) : useStore(navigationStore);
}

export const initNavigation = navigationStore.init;
export const awaitNavigationConvergence = navigationStore.awaitConvergence;
export const resetNavigationStoreForTests = navigationStore.reset;
