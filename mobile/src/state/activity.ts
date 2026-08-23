// ActivityStore — Zustand state wrapping an ActivityService. Owns the current
// ActivityView projection (never the raw wire Thread) and exposes project()
// (re-project from a Thread) and reset() (clear on conversation switch).
//
// Generation safety: the generation counter increments only when the
// conversation identity changes (thread id / ref), not on every re-projection
// of the same conversation. This lets a caller re-project the same thread
// (e.g. after a notification) without "regressing" the generation, while a
// switch to a different conversation bumps the generation so a late
// completion from the older conversation is dropped. reset() bumps the
// generation and returns to idle.

import { create } from "zustand";
import type { Thread } from "../../../cmd/evener-hub/frontend/src/protocol/types.gen";
import {
  type ActivityView,
  createActivityService as defaultService,
} from "../services/activity";

export type ActivityStatus = "idle" | "open" | "error";

// The minimal service surface the store depends on. Structurally compatible
// with ActivityService so tests can inject a scripted stub.
export interface ActivityServiceLike {
  projectActivity(thread: Thread): ActivityView;
}

export interface ActivityState {
  readonly view: ActivityView | null;
  readonly status: ActivityStatus;
  readonly error: string | null;

  project(service: ActivityServiceLike, thread: Thread): void;
  reset(): void;

  /** @internal Generation counter for deterministic tests. */
  generationForTest(): number;
}

export function createActivityStore() {
  let generation = 0;
  let lastThreadKey: string | null = null;

  return create<ActivityState>((set) => ({
    view: null,
    status: "idle",
    error: null,

    project(service, thread) {
      // Bump the generation only when the conversation identity changes, so a
      // re-projection of the same thread (after a notification) does not
      // regress or advance the generation — only a switch to a different
      // conversation bumps it.
      const key = `${thread.id}:${thread.evener.ref}`;
      if (key !== lastThreadKey) {
        generation += 1;
        lastThreadKey = key;
      }
      try {
        const view = service.projectActivity(thread);
        set({ view, status: "open", error: null });
      } catch (err) {
        set({
          view: null,
          status: "error",
          error: err instanceof Error ? err.message : String(err),
        });
      }
    },

    reset() {
      generation += 1;
      lastThreadKey = null;
      set({ view: null, status: "idle", error: null });
    },

    generationForTest() {
      return generation;
    },
  }));
}

// Re-export for callers that want the default service.
export { defaultService as createDefaultActivityService };
