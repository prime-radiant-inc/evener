// The app's one tasks-panel store: the package's framework-free factory bound
// to zustand's useStore for the reactive read, reading evener/tasks/list
// through threadsStore.listTasks so a fetch waits out a reconnect like every
// other read-only action (issue #195's RCA, in threads.ts). Its per-ref
// entries are evicted with the other panel stores when no open pane keeps the
// ref alive. threads.ts imports this module's reset for its own; the read
// below reaches back into threadsStore lazily, so the two modules never need
// each other at evaluation time.
import { createTasksPanelStore, type TasksPanelState } from "@evener/appwire-client";
import { useStore } from "zustand";
import { registerPanelStoreEvictor } from "./panelStoreEviction";
import { threadsStore } from "./threads";

export const tasksPanelStore = createTasksPanelStore((ref) => threadsStore.getState().listTasks(ref));

registerPanelStoreEvictor({
  refs: () => tasksPanelStore.getState().entries.keys(),
  evict: (ref) => tasksPanelStore.getState().evict(ref),
});

export function useTasksPanelStore(): TasksPanelState;
export function useTasksPanelStore<T>(selector: (state: TasksPanelState) => T): T;
export function useTasksPanelStore<T>(selector?: (state: TasksPanelState) => T): T | TasksPanelState {
  // Both branches call the same zustand hook; the default selector is the
  // identity selector, so this overload mirrors the other vanilla stores.
  // biome-ignore lint/correctness/useHookAtTopLevel: both arms call useStore
  return selector ? useStore(tasksPanelStore, selector) : useStore(tasksPanelStore);
}

export function resetTasksPanelStoreForTests(): void {
  tasksPanelStore.getState().resetForTests();
}
