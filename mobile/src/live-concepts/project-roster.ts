// Pure projection from RosterState to a display-safe LiveRosterView.
// projectLiveRoster folds the store's entries, loading, error, searchTerm,
// hasMore, and sessionsVisible into the LiveConcept foundation view. No DOM,
// no network — given the same input it produces the same output.
//
// Display safety: the raw thread ref does NOT appear in the enumerable row
// data or anywhere in the serialized view. Row keys are opaque hashes derived
// deterministically from the ref — not reversible, not prefixed with the ref.
// The raw ref is carried separately in refsByKey so operational lookups
// (opening a conversation) can resolve the ref from the opaque key.
//
// createRosterProjector() returns a projector instance whose .project() method
// shares the same deterministic key derivation as the standalone projectLiveRoster
// so keys are stable across independent instances for the same ref.

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

// Deterministic non-cryptographic hash (djb2) of a string, returned as a
// base36 string. This produces a short, opaque, stable key from the raw ref
// without revealing the ref itself or any reversible prefix. The same input
// always produces the same output across instances; different inputs produce
// different outputs with negligible collision probability for typical refs.
function opaqueKey(ref: string): string {
  let hash = 5381;
  for (let i = 0; i < ref.length; i++) {
    hash = ((hash << 5) + hash + ref.charCodeAt(i)) | 0;
  }
  return `k${(hash >>> 0).toString(36)}`;
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

// Project a single entry to a display-safe row.
function projectRow(entry: RosterEntry): LiveRosterRow {
  return {
    key: opaqueKey(entry.ref),
    title: entry.title,
    project: entry.project,
    summary: entry.status,
    updatedLabel: formatUpdated(entry.updatedAt),
    tone: attentionToTone(entry.attention),
    connectedWorkCount: connectedWorkCount(entry),
  };
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

// Core projection logic shared by the standalone function and the projector
// instance.
function project(input: RosterProjectionInput): ProjectedRoster {
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

// Standalone projection function (backward-compatible with existing callers).
export function projectLiveRoster(
  input: RosterProjectionInput,
): ProjectedRoster {
  return project(input);
}

// A projector instance with a .project() method. The opaque key derivation is
// deterministic (pure function of the ref) so keys are stable across
// independent instances — no instance-local random state.
export interface RosterProjector {
  project(input: RosterProjectionInput): ProjectedRoster;
}

export function createRosterProjector(): RosterProjector {
  return {
    project(input) {
      return project(input);
    },
  };
}
