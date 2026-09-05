// RosterStore — the session roster's Zustand state. Wraps a RosterService
// (which wraps an AppwireClient's thread/list). Holds entries, loading, error,
// a search term, hasMore, sessionsVisible, and a generation counter. Groups
// entries by attention and filters locally after the roster loads.
//
// Generation safety: refresh() captures the generation at call time. If the
// generation changed during the await (e.g. a profile switch bumped it), the
// late result is silently dropped so it cannot overwrite a newer state.
//
// Event refresh: when sessionsVisible is true, canonical notifications that can
// create, close, reclassify, queue, rename, or change attention for a roster
// entry schedule a debounced refresh through the injected scheduler. The
// scheduler key is scoped to the current generation so a pending refresh from
// an old generation is invalidated by a generation bump. Two rapid signals
// coalesce into one refresh because they share the same key.
//
// Last-good retention: on refresh error the store keeps the existing entries
// and sets the error message. Pull-to-refresh calls refresh(service) again.

import { create } from "zustand";
import type { AnyNotification } from "../../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type { RosterEntry, RosterService } from "../services/roster";

/** A scheduler that coalesces refresh effects by key. */
export interface RosterScheduler {
  schedule(key: string, effect: () => void): void;
}

/** Subscribe to AppWire notifications. Returns an unsubscribe function. */
export type RosterSubscribe = (
  handler: (n: AnyNotification) => void,
) => () => void;

/** Default scheduler uses setTimeout with a short debounce. */
const DEFAULT_DEBOUNCE_MS = 300;

function createDefaultScheduler(
  debounceMs = DEFAULT_DEBOUNCE_MS,
): RosterScheduler {
  const timers = new Map<string, ReturnType<typeof setTimeout>>();
  return {
    schedule(key, effect) {
      const existing = timers.get(key);
      if (existing !== undefined) clearTimeout(existing);
      timers.set(
        key,
        setTimeout(() => {
          timers.delete(key);
          effect();
        }, debounceMs),
      );
    },
  };
}

export interface RosterState {
  readonly entries: RosterEntry[];
  readonly loading: boolean;
  readonly error: string | null;
  readonly searchTerm: string;
  readonly hasMore: boolean;
  readonly sessionsVisible: boolean;
  readonly generation: number;

  // Filtered entries based on the current search term (local filter).
  readonly visibleEntries: RosterEntry[];

  // Entries grouped by attention, respecting the current search filter.
  readonly groupedEntries: {
    needsYou: RosterEntry[];
    running: RosterEntry[];
    recent: RosterEntry[];
  };

  refresh(service: RosterService): Promise<void>;
  setSearch(term: string): void;
  setSessionsVisible(visible: boolean): void;
  bumpGeneration(): void;
  handleNotification(n: AnyNotification, service: RosterService): void;
  reset(): void;
}

// Filter entries by search term (case-insensitive, matches title or project).
function filterEntries(
  entries: readonly RosterEntry[],
  term: string,
): RosterEntry[] {
  const trimmed = term.trim().toLowerCase();
  if (trimmed === "") return [...entries];
  return entries.filter(
    (e) =>
      e.title.toLowerCase().includes(trimmed) ||
      e.project.toLowerCase().includes(trimmed),
  );
}

// Group entries by attention, preserving order within each group.
function groupByAttention(
  entries: readonly RosterEntry[],
): RosterState["groupedEntries"] {
  const needsYou: RosterEntry[] = [];
  const running: RosterEntry[] = [];
  const recent: RosterEntry[] = [];
  for (const entry of entries) {
    const bucket: RosterEntry[] =
      entry.attention === "needsYou"
        ? needsYou
        : entry.attention === "running"
          ? running
          : recent;
    bucket.push(entry);
  }
  return { needsYou, running, recent };
}

// Derive visibleEntries and groupedEntries from entries + searchTerm.
function derive(
  entries: readonly RosterEntry[],
  searchTerm: string,
): Pick<RosterState, "visibleEntries" | "groupedEntries"> {
  const visibleEntries = filterEntries(entries, searchTerm);
  return { visibleEntries, groupedEntries: groupByAttention(visibleEntries) };
}

// Notification methods that should trigger a roster refresh.
const REFRESH_METHODS = new Set<AnyNotification["method"]>([
  "thread/started",
  "thread/closed",
  "thread/status/changed",
  "thread/queueChanged",
  "evener/thread/name/changed",
  "evener/attention/changed",
]);

export interface CreateRosterStoreOptions {
  readonly scheduler?: RosterScheduler;
  readonly subscribe?: RosterSubscribe;
}

export function createRosterStore(options: CreateRosterStoreOptions = {}) {
  const scheduler = options.scheduler ?? createDefaultScheduler();
  const subscribe = options.subscribe;

  // Track the last service passed to refresh so the auto-subscribed notification
  // handler can schedule a debounced refresh without an explicit service ref.
  let currentService: RosterService | null = null;

  const store = create<RosterState>((set, get) => ({
    entries: [],
    loading: false,
    error: null,
    searchTerm: "",
    hasMore: false,
    sessionsVisible: false,
    generation: 0,
    ...derive([], ""),

    async refresh(service) {
      currentService = service;
      const gen = get().generation;
      set({ loading: true, error: null });
      try {
        const result = await service.list();
        if (gen !== get().generation) {
          set({ loading: false });
          return;
        }
        set({
          entries: result.threads,
          loading: false,
          error: null,
          hasMore: result.hasMore,
          ...derive(result.threads, get().searchTerm),
        });
      } catch (err) {
        if (gen !== get().generation) {
          set({ loading: false });
          return;
        }
        set({
          loading: false,
          error: err instanceof Error ? err.message : String(err),
        });
      }
    },

    setSearch(term) {
      set({ searchTerm: term, ...derive(get().entries, term) });
    },

    setSessionsVisible(visible) {
      set({ sessionsVisible: visible });
    },

    bumpGeneration() {
      set((s) => ({ generation: s.generation + 1 }));
    },

    handleNotification(n, service) {
      if (!get().sessionsVisible) return;
      if (!REFRESH_METHODS.has(n.method)) return;
      const gen = get().generation;
      const key = `roster-refresh:${gen}`;
      scheduler.schedule(key, () => {
        // Execution-time generation check: if the generation changed between
        // scheduling and execution (profile switch / reset), this callback is
        // a no-op — it must not call the old service or mutate state.
        if (gen !== get().generation) return;
        void get().refresh(service);
      });
    },

    reset() {
      set((s) => ({
        entries: [],
        loading: false,
        error: null,
        searchTerm: "",
        hasMore: false,
        generation: s.generation + 1,
        ...derive([], ""),
      }));
    },
  }));

  // Auto-subscribe to the notification seam when provided. The handler uses
  // the last refresh service to schedule a debounced refresh.
  if (subscribe !== undefined) {
    subscribe((n) => {
      const svc = currentService;
      if (svc === null) return;
      store.getState().handleNotification(n, svc);
    });
  }

  return store;
}

// Wire the store's notification handler to a subscribe seam. Returns the
// unsubscribe function. The handler reads the current service at call time.
export function connectRosterNotifications(
  store: ReturnType<typeof createRosterStore>,
  service: RosterService,
  subscribe: RosterSubscribe,
): () => void {
  return subscribe((n) => {
    store.getState().handleNotification(n, service);
  });
}
