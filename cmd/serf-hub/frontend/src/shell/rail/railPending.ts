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
  | { kind: "sessionPin"; ref: string; section: PinSectionSummary }
  | { kind: "sessionUnpin"; ref: string }
  | { kind: "pinSectionRename"; id: string; name: string }
  | { kind: "pinSectionDelete"; id: string }
  | { kind: "sessionFavorite"; ref: string; value: boolean }
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

function findSessionNode(tree: TreeResponse, ref: string): ApiTreeNode | undefined {
  const findIn = (nodes: ApiTreeNode[]): ApiTreeNode | undefined => {
    for (const node of nodes) {
      if (node.ref === ref) return node;
      const child = findIn(node.children);
      if (child) return child;
    }
    return undefined;
  };
  const projectRows = (projects: ApiTreeProject[]) => projects.flatMap((project) => project.sessions);
  return (
    findIn(tree.live) ??
    findIn(tree.needs_you) ??
    findIn(projectRows(tree.projects)) ??
    findIn(projectRows(tree.archived_projects)) ??
    findIn(projectRows(tree.test_runs)) ??
    findIn(tree.pin_sections.flatMap((section) => section.sessions))
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

function withoutSession(section: PinSectionTree, ref: string): PinSectionTree {
  return { ...section, sessions: mapNodes(section.sessions, (node) => (node.ref === ref ? null : node)) };
}

function applySessionPin(tree: TreeResponse, ref: string, summary: PinSectionSummary): TreeResponse {
  const source = findSessionNode(tree, ref);
  if (!source) return tree;

  const annotated = annotateSessionPin(tree, ref, summary.id);
  let foundTarget = false;
  const pinSections = annotated.pin_sections.flatMap((section) => {
    const currentIndex = section.sessions.findIndex((node) => node.ref === ref);
    const cleaned = withoutSession(section, ref);
    if (section.id !== summary.id) return cleaned.sessions.length === 0 ? [] : [cleaned];
    foundTarget = true;
    const sessions = [...cleaned.sessions];
    sessions.splice(currentIndex < 0 ? sessions.length : Math.min(currentIndex, sessions.length), 0, {
      ...source,
      pin_section_id: summary.id,
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
    pinSections.push({ id: summary.id, name: summary.name, sessions: [{ ...source, pin_section_id: summary.id }] });
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

function applyOne(tree: TreeResponse, op: PendingOp): TreeResponse {
  switch (op.kind) {
    case "hideSession":
      // Every tier, not just the one you clicked in: a session can be listed
      // in Live and under its project at the same time, and clearing one
      // listing while the other lingers reads as the action half-working.
      return mapEverySession(tree, (n) => (n.ref === op.ref ? null : n));
    case "hideProject":
      return mapEveryProject(tree, (p) => (p.key === op.key ? null : p));
    case "sessionPin":
      return applySessionPin(tree, op.ref, op.section);
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
    case "sessionFavorite":
      return mapEverySession(tree, (n) => (n.ref === op.ref ? { ...n, favorite: op.value } : n));
    case "sessionTitle":
      return mapEverySession(tree, (n) => (n.ref === op.ref ? { ...n, title: op.title } : n));
    case "projectFavorite":
      return mapEveryProject(tree, (p) => (p.key === op.key ? { ...p, favorite: op.value } : p));
  }
}

/** Projects every in-flight op onto `tree`, in order. Returns the SAME tree
 * object when there is nothing pending, so the common case costs no copy and
 * no re-render. */
export function applyPending(tree: TreeResponse, ops: readonly PendingOp[]): TreeResponse {
  if (ops.length === 0) return tree;
  return ops.reduce(applyOne, tree);
}
