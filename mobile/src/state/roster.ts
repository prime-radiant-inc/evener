// RosterStore — the session roster's Zustand state. Wraps a RosterService
// (which wraps an AppwireClient's thread/list). Holds entries, loading, error,
// and a search term. Groups entries by attention and filters locally after
// the roster loads.
//
// The store never auto-retries a refresh. On error it sets the error message
// and keeps the last entries. Pull-to-refresh calls refresh(service) again.

import { create } from "zustand";
import type { RosterEntry, RosterService } from "../services/roster";

export interface RosterState {
  readonly entries: RosterEntry[];
  readonly loading: boolean;
  readonly error: string | null;
  readonly searchTerm: string;

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

export function createRosterStore() {
  return create<RosterState>((set, get) => ({
    entries: [],
    loading: false,
    error: null,
    searchTerm: "",
    ...derive([], ""),

    async refresh(service) {
      set({ loading: true, error: null });
      try {
        const result = await service.list();
        set({
          entries: result.threads,
          loading: false,
          error: null,
          ...derive(result.threads, get().searchTerm),
        });
      } catch (err) {
        set({
          loading: false,
          error: err instanceof Error ? err.message : String(err),
        });
      }
    },

    setSearch(term) {
      set({ searchTerm: term, ...derive(get().entries, term) });
    },

    reset() {
      set({
        entries: [],
        loading: false,
        error: null,
        searchTerm: "",
        ...derive([], ""),
      });
    },
  }));
}
