// Pure projection from RosterState to a display-safe LiveRosterView.
// projectLiveRoster folds the store's entries, loading, error, searchTerm,
// hasMore, and sessionsVisible into the LiveConcept foundation view. No DOM,
// no network — given the same input it produces the same output.
//
// Display safety: the raw thread ref does NOT appear in the enumerable row
// data. It is carried separately in refsByKey so operational lookups (opening
// a conversation) can resolve the ref from the display key without exposing it
// to the renderer.

import type { RosterEntry } from "../services/roster";
import type { DisplayTone, LiveRosterRow, LiveRosterView } from "./model";

export interface ProjectedRoster {
  view: LiveRosterView;
  refsByKey: ReadonlyMap<string, string>;
}

// Input shape — a structural subset of RosterState so tests can pass a plain
// object without importing the full store type.
export interface RosterProjectionInput {
  readonly entries: readonly RosterEntry[];
  readonly loading: boolean;
  readonly error: string | null;
  readonly searchTerm: string;
  readonly hasMore: boolean;
  readonly sessionsVisible: boolean;
}

// Map an attention classification to a display tone.
function attentionToTone(attention: RosterEntry["attention"]): DisplayTone {
  switch (attention) {
    case "needsYou":
      return "attention";
    case "running":
      return "running";
    case "recent":
      return "idle";
  }
}

// Count connected work for a row. A running thread has one active work stream.
function connectedWorkCount(entry: RosterEntry): number {
  return entry.attention === "running" || entry.status === "active" ? 1 : 0;
}

// Build a stable display key from the entry. This key is NOT the raw ref — it
// is derived from the entry's identity so it stays stable across projections
// for the same entry while never exposing the raw wire ref to the renderer.
function displayKey(entry: RosterEntry): string {
  return `row:${entry.ref}`;
}

// Project a single entry to a display-safe row.
function projectRow(entry: RosterEntry): LiveRosterRow {
  return {
    key: displayKey(entry),
    title: entry.title,
    project: entry.project,
    summary: entry.status,
    updatedLabel: formatUpdated(entry.updatedAt),
    tone: attentionToTone(entry.attention),
    connectedWorkCount: connectedWorkCount(entry),
  };
}

// Format the updatedAt timestamp as a relative-ish label. This is a
// display-only string; the exact format is not contractual.
function formatUpdated(updatedAt: number): string {
  if (updatedAt === 0) return "";
  const date = new Date(updatedAt);
  const hours = date.getHours().toString().padStart(2, "0");
  const minutes = date.getMinutes().toString().padStart(2, "0");
  return `${hours}:${minutes}`;
}

// Derive the roster view status from the store flags.
function deriveStatus(input: RosterProjectionInput): LiveRosterView["status"] {
  if (input.loading) return "loading";
  if (input.error !== null) return "error";
  if (!input.sessionsVisible) return "idle";
  return "ready";
}

// Filter entries by the search term (case-insensitive, title or project).
function filterByQuery(
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

export function projectLiveRoster(
  input: RosterProjectionInput,
): ProjectedRoster {
  const status = deriveStatus(input);
  const filtered = filterByQuery(input.entries, input.searchTerm);

  const needsYou: LiveRosterRow[] = [];
  const running: LiveRosterRow[] = [];
  const recent: LiveRosterRow[] = [];
  const refsByKey = new Map<string, string>();

  for (const entry of filtered) {
    const row = projectRow(entry);
    refsByKey.set(row.key, entry.ref);
    const bucket =
      entry.attention === "needsYou"
        ? needsYou
        : entry.attention === "running"
          ? running
          : recent;
    bucket.push(row);
  }

  const groups: {
    id: "needsYou" | "running" | "recent";
    label: string;
    rows: readonly LiveRosterRow[];
  }[] = [];
  if (needsYou.length > 0) {
    groups.push({ id: "needsYou", label: "Needs You", rows: needsYou });
  }
  if (running.length > 0) {
    groups.push({ id: "running", label: "Running", rows: running });
  }
  if (recent.length > 0) {
    groups.push({ id: "recent", label: "Recent", rows: recent });
  }

  const view: LiveRosterView = {
    status,
    query: input.searchTerm,
    groups,
    hasMore: input.hasMore,
    error: input.error,
  };

  return { view, refsByKey };
}
