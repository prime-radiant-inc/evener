import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import type { TaskRow } from "../panes/session/chrome/taskData";
import { registerPanelStoreEvictor } from "./panelStoreEviction";

export interface PanelLoadFailure {
  headline: string;
  detail?: string;
  sentence: string;
}

export interface TasksPanelEntry {
  rows: TaskRow[] | null;
  unsupported: boolean;
  daemonGone: boolean;
  failure: PanelLoadFailure | null;
  loading: boolean;
  fetchID: number;
}

export type TasksFetchResult =
  | { kind: "rows"; rows: TaskRow[] }
  | { kind: "unsupported" }
  | { kind: "daemon-gone" }
  | { kind: "empty" }
  | { kind: "failure"; failure: PanelLoadFailure };

export interface TasksPanelStoreState {
  entries: Map<string, TasksPanelEntry>;
  beginFetch(ref: string): number;
  publishFetch(ref: string, fetchID: number, result: TasksFetchResult): void;
  setRows(ref: string, rows: TaskRow[]): void;
  setFailure(ref: string, failure: PanelLoadFailure): void;
  setUnsupported(ref: string): void;
  setDaemonGone(ref: string): void;
  resetForTests(): void;
}

export const EMPTY_TASKS_PANEL_ENTRY: TasksPanelEntry = {
  rows: null,
  unsupported: false,
  daemonGone: false,
  failure: null,
  loading: false,
  fetchID: 0,
};

function newEntry(): TasksPanelEntry {
  return { ...EMPTY_TASKS_PANEL_ENTRY };
}

function entryFor(entries: Map<string, TasksPanelEntry>, ref: string): TasksPanelEntry {
  return entries.get(ref) ?? newEntry();
}

function updateEntry(
  set: (update: (state: TasksPanelStoreState) => Partial<TasksPanelStoreState>) => void,
  ref: string,
  update: (entry: TasksPanelEntry) => TasksPanelEntry,
): void {
  set((state) => {
    const next = new Map(state.entries);
    next.set(ref, update(entryFor(state.entries, ref)));
    return { entries: next };
  });
}

// Monotonic across every entry and NOT stored per-entry: an entry recreated
// after eviction must never hand out an ID that a request begun in its
// previous life could still complete with (publishFetch matches on fetchID).
let nextFetchID = 0;

export const tasksPanelStore = createStore<TasksPanelStoreState>((set) => ({
  entries: new Map(),

  beginFetch(ref) {
    let fetchID = 0;
    set((state) => {
      const current = entryFor(state.entries, ref);
      fetchID = ++nextFetchID;
      const next = new Map(state.entries);
      next.set(ref, {
        ...current,
        loading: true,
        unsupported: false,
        daemonGone: false,
        failure: null,
        fetchID,
      });
      return { entries: next };
    });
    return fetchID;
  },

  publishFetch(ref, fetchID, result) {
    set((state) => {
      const current = state.entries.get(ref);
      if (!current || current.fetchID !== fetchID) return state;
      const next = new Map(state.entries);
      switch (result.kind) {
        case "rows":
          next.set(ref, {
            ...current,
            rows: result.rows,
            unsupported: false,
            daemonGone: false,
            failure: null,
            loading: false,
          });
          break;
        case "empty":
          next.set(ref, {
            ...current,
            rows: [],
            unsupported: false,
            daemonGone: false,
            failure: null,
            loading: false,
          });
          break;
        case "unsupported":
          next.set(ref, {
            ...current,
            rows: null,
            unsupported: true,
            daemonGone: false,
            failure: null,
            loading: false,
          });
          break;
        case "daemon-gone":
          next.set(ref, {
            ...current,
            daemonGone: true,
            unsupported: false,
            failure: null,
            loading: false,
          });
          break;
        case "failure":
          next.set(ref, {
            ...current,
            failure: result.failure,
            unsupported: false,
            daemonGone: false,
            loading: false,
          });
          break;
      }
      return { entries: next };
    });
  },

  setRows(ref, rows) {
    updateEntry(set, ref, (entry) => ({
      ...entry,
      rows,
      unsupported: false,
      daemonGone: false,
      failure: null,
      loading: false,
    }));
  },

  setFailure(ref, failure) {
    updateEntry(set, ref, (entry) => ({
      ...entry,
      failure,
      loading: false,
    }));
  },

  setUnsupported(ref) {
    updateEntry(set, ref, (entry) => ({
      ...entry,
      rows: null,
      unsupported: true,
      daemonGone: false,
      failure: null,
      loading: false,
    }));
  },

  setDaemonGone(ref) {
    updateEntry(set, ref, (entry) => ({
      ...entry,
      daemonGone: true,
      unsupported: false,
      failure: null,
      loading: false,
    }));
  },

  resetForTests() {
    nextFetchID = 0;
    set({ entries: new Map() });
  },
}));

registerPanelStoreEvictor({
  refs: () => tasksPanelStore.getState().entries.keys(),
  evict: (ref) => {
    tasksPanelStore.setState((state) => {
      if (!state.entries.has(ref)) return state;
      const entries = new Map(state.entries);
      entries.delete(ref);
      return { entries };
    });
  },
});

export function useTasksPanelStore(): TasksPanelStoreState;
export function useTasksPanelStore<T>(selector: (state: TasksPanelStoreState) => T): T;
export function useTasksPanelStore<T>(selector?: (state: TasksPanelStoreState) => T): T | TasksPanelStoreState {
  // Both branches call the same zustand hook; the default selector is the
  // identity selector, so this overload mirrors the other vanilla stores.
  // biome-ignore lint/correctness/useHookAtTopLevel: both arms call useStore
  return selector ? useStore(tasksPanelStore, selector) : useStore(tasksPanelStore);
}

export function resetTasksPanelStoreForTests(): void {
  tasksPanelStore.getState().resetForTests();
}
