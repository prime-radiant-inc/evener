// railExpansion.ts persists the rail's per-row expand state, so a project you
// opened, a subagent fold you looked inside, and the Archived-sessions section
// are all still where you left them after a reload. Rail.tsx owns the live
// map; this module only turns it into localStorage and back.
//
// One JSON blob under one key, following DockHost's serf.workspace.layout.v2
// precedent rather than the htmx UI's key-per-row
// (localStorage["serf-hub.sidebar.expanded.<key>"], parity-m3-sidebar-tree.md
// §10.1): the behavior is identical row by row, but this reads in one call at
// boot instead of scanning the namespace, and it cannot leave orphaned keys
// behind when a row's id changes.
//
// Not a prefs.ts key: that store is a fixed, enumerated set of settings a
// person chooses (see its own top comment), while this is unbounded
// machine-generated row state with no Settings UI.

/** The localStorage key. Exported for tests, which seed corrupt values
 * directly to prove the load path tolerates them. */
export const EXPANSION_STORAGE_KEY = "serf.rail.expanded.v1";

/** Entries kept before the oldest are evicted. The map only ever holds ids
 * the user explicitly toggled, but rows outlive the sessions they name, so on
 * a long-lived hub it would otherwise grow without bound. Generous enough
 * that no real session's state is lost in practice, small enough that the
 * blob stays trivial to parse at boot. */
export const EXPANSION_LIMIT = 2000;

/** Reads the persisted map. Every failure mode - key absent, storage blocked,
 * malformed JSON, wrong shape, a non-boolean value - yields a usable map
 * rather than throwing: a corrupt preference must cost you your expand state,
 * never the sidebar. */
export function loadExpansion(): Map<string, boolean> {
  const out = new Map<string, boolean>();
  let raw: string | null;
  try {
    raw = localStorage.getItem(EXPANSION_STORAGE_KEY);
  } catch {
    return out; // storage unavailable (private mode, blocked cookies)
  }
  if (raw === null) return out;
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return out;
  }
  // Arrays and null are both typeof "object"; neither is the record shape.
  if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) return out;
  for (const [id, value] of Object.entries(parsed)) {
    if (typeof value === "boolean") out.set(id, value);
  }
  return out;
}

/** Writes the map, capped at EXPANSION_LIMIT. Best-effort: a full quota or a
 * browser that blocks storage entirely leaves the caller's in-memory state
 * untouched and working for the rest of the session. */
export function saveExpansion(overrides: ReadonlyMap<string, boolean>): void {
  // Map iterates in insertion order, so the tail is the most recently toggled
  // - which is what a person is most likely to come back to.
  const entries = [...overrides];
  const kept = entries.length > EXPANSION_LIMIT ? entries.slice(entries.length - EXPANSION_LIMIT) : entries;
  try {
    localStorage.setItem(EXPANSION_STORAGE_KEY, JSON.stringify(Object.fromEntries(kept)));
  } catch {
    // Best-effort, same rationale as stores/prefs.ts's writeRaw.
  }
}
