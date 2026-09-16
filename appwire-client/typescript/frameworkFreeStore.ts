// The framework-free store every shared store in the package is built on: the
// getState/setState/subscribe triple plus getInitialState, the shape React's
// useSyncExternalStore (and zustand's useStore over it) binds to without the
// package depending on either. Each store module wraps one of these with its
// own state type and store-bound actions; each app builds the one instance it
// wires to its view layer. Pure logic - no DOM, no React.

/** Runs after every state change with the new state and the one it replaced
 * - the listener shape zustand's useStore subscribes with. */
export type StoreListener<S> = (state: S, previous: S) => void;

export interface FrameworkFreeStore<S> {
  getState(): S;
  /** The state the store was created with: the snapshot a view binding
   * (React's useSyncExternalStore, zustand's useStore) reads before its first
   * subscription, and what setState takes to reset the store. */
  getInitialState(): S;
  /** Shallow-merges the partial (or the updater's result) into the state and
   * notifies every subscriber, even when nothing changed. */
  setState(partial: Partial<S> | ((state: S) => Partial<S>)): void;
  /** Returns the unsubscribe function. */
  subscribe(listener: StoreListener<S>): () => void;
}

/** Builds a store whose initial state `init` returns; `init` receives the
 * store's own setState and getState so state methods can close over them. */
export function createFrameworkFreeStore<S>(
  init: (set: FrameworkFreeStore<S>["setState"], get: () => S) => S,
): FrameworkFreeStore<S> {
  const listeners = new Set<StoreListener<S>>();
  let state: S;
  const get = (): S => state;
  const set: FrameworkFreeStore<S>["setState"] = (partial) => {
    const previous = state;
    state = { ...state, ...(typeof partial === "function" ? partial(state) : partial) };
    for (const listener of listeners) listener(state, previous);
  };
  state = init(set, get);
  const initialState = state;
  return {
    getState: get,
    getInitialState: () => initialState,
    setState: set,
    subscribe(listener) {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
  };
}
