// railPending.ts is the rail's optimistic layer: a list of in-flight
// mutations, and one pure function that projects them onto the tree the store
// last fetched. Rail.tsx holds the list (adding an op before its POST,
// dropping it once the follow-up refresh has settled) and renders from the
// projected tree, so a pin, a rename, or an archive shows the instant you
// click it instead of after a round trip.
//
// Deliberately simpler than the htmx sidebar's equivalent
// (parity-m3-sidebar-tree.md §15: a keyed pending Map, per-op confirm()
// predicates, "mutation-type" vs "disappearance-type" completion rules and a
// 30-second eviction backstop). All of that machinery existed because that UI
// resynced on a coalesced 2-second debounce that could not be awaited, so an
// op had no defined moment at which it was safe to drop and needed a rule for
// recognising its own effect in a later tree. Rail awaits its refresh
// directly (treeStore.refresh() always resolves, never rejects), so every op
// has exactly one such moment and none of those rules are needed.
//
// Only the ARCHIVING direction hides a row. Unarchiving has no optimistic
// form - where the row reappears depends on server-side tier classification
// this layer cannot predict - which matches the htmx behavior exactly.

import type {
  TreeNode as ApiTreeNode,
  TreeProject as ApiTreeProject,
  PinSectionSummary,
  PinSectionTree,
  TreeResponse,
} from "../../stores/tree";

export type PendingOp =
  | { kind: "hideSession"; ref: string }
  | { kind: "hideProject"; key: string }
  | { kind: "sessionPin"; ref: string; source: ApiTreeNode; section: PinSectionSummary }
  | { kind: "sessionUnpin"; ref: string }
  | { kind: "pinSectionRename"; id: string; name: string }
  | { kind: "pinSectionDelete"; id: string }
  | { kind: "projectFavorite"; key: string; value: boolean }
  | { kind: "sessionTitle"; ref: string; title: string };

// Rebuilds a session list with `fn` applied to every node at every depth,
// dropping any node fn maps to null. Returns new arrays/objects throughout -
// the store's tree is never mutated, so a later render off the unmodified
// tree (once the op is dropped) is always correct.
function mapNodes(nodes: ApiTreeNode[], fn: (n: ApiTreeNode) => ApiTreeNode | null): ApiTreeNode[] {
  const out: ApiTreeNode[] = [];
  for (const n of nodes) {
    const mapped = fn(n);
    if (mapped === null) continue;
    out.push({ ...mapped, children: mapNodes(mapped.children, fn) });
  }
  return out;
}

function mapEverySession(tree: TreeResponse, fn: (n: ApiTreeNode) => ApiTreeNode | null): TreeResponse {
  const mapProjects = (ps: ApiTreeProject[]): ApiTreeProject[] =>
    ps.map((p) => ({ ...p, sessions: mapNodes(p.sessions, fn) }));
  return {
    ...tree,
    live: mapNodes(tree.live, fn),
    needs_you: mapNodes(tree.needs_you, fn),
    pin_sections: tree.pin_sections.map((section) => ({ ...section, sessions: mapNodes(section.sessions, fn) })),
    projects: mapProjects(tree.projects),
    archived_projects: mapProjects(tree.archived_projects),
    test_runs: mapProjects(tree.test_runs),
  };
}

const NON_TOP_LEVEL_KINDS: ReadonlySet<string> = new Set(["fork", "subagent", "cluster"]);

function eligiblePinSource(
  tree: TreeResponse,
  requested: ApiTreeNode,
  ref: string,
  supplementalTopLevelRows: readonly ApiTreeNode[],
): ApiTreeNode | undefined {
  if (requested.ref !== ref || NON_TOP_LEVEL_KINDS.has(requested.kind)) return undefined;
  const projectRows = (projects: ApiTreeProject[]) => projects.flatMap((project) => project.sessions);
  const topLevelRows = [
    ...tree.live,
    ...tree.needs_you,
    ...projectRows(tree.projects),
    ...projectRows(tree.archived_projects),
    ...projectRows(tree.test_runs),
    ...tree.pin_sections.flatMap((section) => section.sessions),
    ...supplementalTopLevelRows,
  ];
  return topLevelRows.find(
    (candidate) =>
      candidate.row_id === requested.row_id && candidate.ref === ref && !NON_TOP_LEVEL_KINDS.has(candidate.kind),
  );
}

function annotateSessionPin(tree: TreeResponse, ref: string, sectionID: string | undefined): TreeResponse {
  return mapEverySession(tree, (node) => {
    if (node.ref !== ref) return node;
    if (sectionID === undefined) {
      const { pin_section_id: _pinSectionID, ...unpinned } = node;
      return unpinned;
    }
    return { ...node, pin_section_id: sectionID };
  });
}

function annotateSourcePin(source: ApiTreeNode, ref: string, sectionID: string): ApiTreeNode {
  return {
    ...source,
    pin_section_id: sectionID,
    children: mapNodes(source.children, (node) => (node.ref === ref ? { ...node, pin_section_id: sectionID } : node)),
  };
}

function withoutSession(section: PinSectionTree, ref: string): PinSectionTree {
  return { ...section, sessions: section.sessions.filter((node) => node.ref !== ref) };
}

function applySessionPin(
  tree: TreeResponse,
  ref: string,
  requestedSource: ApiTreeNode,
  summary: PinSectionSummary,
  supplementalTopLevelRows: readonly ApiTreeNode[],
): TreeResponse {
  const source = eligiblePinSource(tree, requestedSource, ref, supplementalTopLevelRows);
  if (!source) return tree;
  const pinnedSource = annotateSourcePin(source, ref, summary.id);

  const annotated = annotateSessionPin(tree, ref, summary.id);
  let foundTarget = false;
  const pinSections = annotated.pin_sections.flatMap((section) => {
    const currentIndex = section.sessions.findIndex((node) => node.ref === ref);
    const cleaned = withoutSession(section, ref);
    if (section.id !== summary.id) return cleaned.sessions.length === 0 ? [] : [cleaned];
    foundTarget = true;
    const sessions = [...cleaned.sessions];
    sessions.splice(currentIndex < 0 ? sessions.length : Math.min(currentIndex, sessions.length), 0, {
      ...pinnedSource,
    });
    return [
      {
        ...cleaned,
        name: summary.name,
        sessions,
      },
    ];
  });
  if (!foundTarget) {
    pinSections.push({ id: summary.id, name: summary.name, sessions: [pinnedSource] });
  }
  return { ...annotated, pin_sections: pinSections };
}

function applySessionUnpin(tree: TreeResponse, ref: string): TreeResponse {
  const annotated = annotateSessionPin(tree, ref, undefined);
  return {
    ...annotated,
    pin_sections: annotated.pin_sections
      .map((section) => withoutSession(section, ref))
      .filter((section) => section.sessions.length > 0),
  };
}

function applyPinSectionDelete(tree: TreeResponse, id: string): TreeResponse {
  const deleted = tree.pin_sections.find((section) => section.id === id);
  if (!deleted) return tree;
  const refs = new Set<string>();
  const collect = (nodes: ApiTreeNode[]) => {
    for (const node of nodes) {
      if (node.pin_section_id === id) refs.add(node.ref);
      collect(node.children);
    }
  };
  collect(deleted.sessions);
  let projected = { ...tree, pin_sections: tree.pin_sections.filter((section) => section.id !== id) };
  for (const ref of refs) projected = annotateSessionPin(projected, ref, undefined);
  return projected;
}

function mapEveryProject(tree: TreeResponse, fn: (p: ApiTreeProject) => ApiTreeProject | null): TreeResponse {
  const keep = (ps: ApiTreeProject[]): ApiTreeProject[] => ps.map(fn).filter((p): p is ApiTreeProject => p !== null);
  return {
    ...tree,
    projects: keep(tree.projects),
    archived_projects: keep(tree.archived_projects),
    test_runs: keep(tree.test_runs),
  };
}

function applyOne(tree: TreeResponse, op: PendingOp, supplementalTopLevelRows: readonly ApiTreeNode[]): TreeResponse {
  switch (op.kind) {
    case "hideSession":
      // Every tier, not just the one you clicked in: a session can be listed
      // in Live and under its project at the same time, and clearing one
      // listing while the other lingers reads as the action half-working.
      return mapEverySession(tree, (n) => (n.ref === op.ref ? null : n));
    case "hideProject":
      return mapEveryProject(tree, (p) => (p.key === op.key ? null : p));
    case "sessionPin":
      return applySessionPin(tree, op.ref, op.source, op.section, supplementalTopLevelRows);
    case "sessionUnpin":
      return applySessionUnpin(tree, op.ref);
    case "pinSectionRename":
      return {
        ...tree,
        pin_sections: tree.pin_sections.map((section) =>
          section.id === op.id ? { ...section, name: op.name } : section,
        ),
      };
    case "pinSectionDelete":
      return applyPinSectionDelete(tree, op.id);
    case "sessionTitle":
      return mapEverySession(tree, (n) => (n.ref === op.ref ? { ...n, title: op.title } : n));
    case "projectFavorite":
      return mapEveryProject(tree, (p) => (p.key === op.key ? { ...p, favorite: op.value } : p));
  }
}

/** Projects every in-flight op onto `tree`, in order. Returns the SAME tree
 * object when there is nothing pending, so the common case costs no copy and
 * no re-render. */
export function applyPending(
  tree: TreeResponse,
  ops: readonly PendingOp[],
  supplementalTopLevelRows: readonly ApiTreeNode[] = [],
): TreeResponse {
  if (ops.length === 0) return tree;
  return ops.reduce((projected, op) => applyOne(projected, op, supplementalTopLevelRows), tree);
}
