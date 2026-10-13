// Grouped rows for evener/search results: the one grouping implementation the
// web palette and the native Board search both render subagent hits from.
// Wire truth: types.gen.ts's SearchResult and SearchTopLevel, and
// docs/appwire-protocol.md's field catalog. The hub owns lineage — it
// annotates each matching subagent row with its top-level session — so this
// module only orders and nests; it resolves nothing and dedups nothing (an
// older hub's response must render exactly as before).
import type { SearchResult, SearchTopLevel } from "./types.gen";

export interface GroupedSearchRow {
  /** The row to render: a session's own match row, or an anchor synthesized
   *  from a child's SearchTopLevel when that session has no row here. */
  result: SearchResult;
  /** A matching subagent: render indented, inside its group. */
  nested: boolean;
  /** A synthesized anchor: its own session has no row in the response, so
   *  render it greyed — the match came from its subagents. It stays a
   *  normal, activatable row. */
  indirect: boolean;
}

export interface GroupedSearch {
  /** Live rows, each followed inline by the matching subagents that group
   *  under it — so a live anchor renders once, with its family. */
  live: GroupedSearchRow[];
  /** Past rows in groups: each group is its top-level session's row (real
   *  or synthesized greyed anchor) followed by its matching subagents, at
   *  the position of the group's first member. */
  past: GroupedSearchRow[];
}

/** How many of the rows are the response's own matches: an indirect anchor
 *  is context for its group's matched subagents, not a match of its own. */
export function countSearchMatches(rows: GroupedSearchRow[]): number {
  return rows.filter((row) => !row.indirect).length;
}

/** The row a synthesized anchor renders as: the wire's anchor fields on a
 * SearchResult shape, so every renderer treats it like any other row. */
function topLevelRow(topLevel: SearchTopLevel): SearchResult {
  return {
    id: topLevel.id,
    ref: topLevel.ref,
    title: topLevel.title,
    project: topLevel.project ?? "",
    state: topLevel.state,
    age: topLevel.age ?? "",
    askPending: topLevel.askPending,
    approvalPending: topLevel.approvalPending,
  };
}

/** groupSearchResults orders a search response's rows for grouped rendering.
 *
 * Rules: children (rows with `topLevel`) nest under their top-level session's
 * own row when the response carries it — in the live section when it is a
 * matched live row, else in the past section; when it carries no row for that
 * session, a greyed anchor is synthesized from the child's `topLevel` (a
 * session that matched but was cut from the page included). A group renders
 * contiguously at the position of its first member, children in match order.
 * Rows without `topLevel` — unannotated subagents included — render flat,
 * exactly as before the grouping.
 *
 * Inputs are the hub's response shape, whose refs are unique per response.
 * (No hub — current or older — can send two rows sharing a ref alongside a
 * child grouping under it; a garbage input that does keeps only the later row
 * as the anchor and drops the earlier, rather than nesting a duplicate.) */
export function groupSearchResults(live: SearchResult[], past: SearchResult[]): GroupedSearch {
  // A row with topLevel is a subagent; the hub only ever resolves anchors
  // to non-subagent sessions, so only topLevel-less rows can be anchors.
  const liveRefs = new Set(live.map((row) => row.ref));
  const anchorRowByRef = new Map<string, SearchResult>();
  const childrenByAnchor = new Map<string, SearchResult[]>();
  const liveChildrenByRef = new Map<string, SearchResult[]>();
  for (const row of past) {
    if (!row.topLevel) {
      anchorRowByRef.set(row.ref, row);
      continue;
    }
    const target = liveRefs.has(row.topLevel.ref) ? liveChildrenByRef : childrenByAnchor;
    const group = target.get(row.topLevel.ref);
    if (group) group.push(row);
    else target.set(row.topLevel.ref, [row]);
  }

  const liveRows: GroupedSearchRow[] = [];
  for (const row of live) {
    liveRows.push({ result: row, nested: false, indirect: false });
    for (const child of liveChildrenByRef.get(row.ref) ?? []) {
      liveRows.push({ result: child, nested: true, indirect: false });
    }
  }

  const pastRows: GroupedSearchRow[] = [];
  const openedGroups = new Set<string>();
  const openGroup = (ref: string, topLevel: SearchTopLevel | undefined) => {
    // One group renders once: the first member opens it, and this guard also
    // turns the group's later child rows into no-ops.
    if (openedGroups.has(ref)) return;
    openedGroups.add(ref);
    const anchorRow = anchorRowByRef.get(ref);
    if (anchorRow) {
      pastRows.push({ result: anchorRow, nested: false, indirect: false });
    } else if (topLevel) {
      pastRows.push({ result: topLevelRow(topLevel), nested: false, indirect: true });
    }
    for (const child of childrenByAnchor.get(ref) ?? []) {
      pastRows.push({ result: child, nested: true, indirect: false });
    }
  };
  for (const row of past) {
    if (row.topLevel && liveRefs.has(row.topLevel.ref)) continue; // renders under its live anchor
    if (row.topLevel) {
      openGroup(row.topLevel.ref, row.topLevel);
      continue;
    }
    if (childrenByAnchor.has(row.ref)) {
      openGroup(row.ref, undefined);
      continue;
    }
    pastRows.push({ result: row, nested: false, indirect: false });
  }
  return { live: liveRows, past: pastRows };
}
