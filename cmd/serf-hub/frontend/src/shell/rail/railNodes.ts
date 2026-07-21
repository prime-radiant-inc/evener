// railNodes.ts is the pure tree-shaping layer between stores/tree.ts's wire
// data and the widgets/tree Tree widget: it decides row identity, nesting,
// and (given the caller's own expand-state map) each branch's `expanded`
// flag. No React, no fetching - Rail.tsx owns the state these functions are
// pure functions OF (the expand-override map, the lazily-loaded archived
// project detail map) and wires the results into <Tree>.

import type { TreeNode as ApiTreeNode, TreeProject as ApiTreeProject } from "../../stores/tree";
import type { TreeNode as WidgetTreeNode } from "../../widgets";

export interface SessionRailNode extends WidgetTreeNode {
  kind: "session";
  session: ApiTreeNode;
  // Always a real array (never absent) - an empty array reads as "leaf" to
  // the Tree widget exactly the same way `undefined` would (see its own
  // hasChildrenOf), so there's no reason to carry two representations of
  // the same "nothing to expand" case.
  children: SessionRailNode[];
}

export interface ProjectRailNode extends WidgetTreeNode {
  kind: "project";
  project: ApiTreeProject;
  // Usually SessionRailNode[]; an archived project not yet hydrated (see
  // archivedProjectNodes) instead gets a single LoadingRailNode child so it
  // still renders a chevron before its real sessions have loaded.
  children: RailNode[];
}

export interface LoadingRailNode extends WidgetTreeNode {
  kind: "loading";
}

export type RailNode = SessionRailNode | ProjectRailNode | LoadingRailNode;

// Resolves one node's expanded state: an explicit user toggle (tracked by
// Rail.tsx, keyed by rail node id) wins; anything not yet toggled falls
// back to the given default (a project's own default_expanded wire field,
// or false when there's no natural default). A single function rather than
// exposing the override map's own shape here keeps Rail.tsx free to store
// that map however it likes.
export type IsExpanded = (id: string, defaultExpanded: boolean) => boolean;

/** The IsExpanded Rail.tsx actually uses in production: a plain override
 * map, falling back to each call's own default. Exported so tests (and
 * Rail.tsx) share one implementation of "override wins, else default"
 * instead of two copies drifting apart. */
export function overrideLookup(overrides: ReadonlyMap<string, boolean>): IsExpanded {
  return (id, defaultExpanded) => overrides.get(id) ?? defaultExpanded;
}

function toSessionNode(n: ApiTreeNode, isExpanded: IsExpanded): SessionRailNode {
  return {
    id: n.row_id,
    kind: "session",
    session: n,
    expanded: isExpanded(n.row_id, false),
    children: n.children.map((c) => toSessionNode(c, isExpanded)),
  };
}

/** Builds rail nodes for a flat, childless-at-this-level session list - the
 * Needs-you, Live, and Pinned tiers, each of which is just TreeNode[] on
 * the wire. A session can still recurse into its own children (subagent
 * clusters), handled by toSessionNode regardless of which tier it's in. */
export function sessionNodes(nodes: ApiTreeNode[], isExpanded: IsExpanded): SessionRailNode[] {
  return nodes.map((n) => toSessionNode(n, isExpanded));
}

// Namespaced so a project branch's own id can never collide with a
// session's row_id (row_ids are always "<scope>:...", but never start with
// "projectnode:") within the same Tree instance.
function projectNodeId(key: string): string {
  return `projectnode:${key}`;
}

/** Builds rail nodes for the Projects and Test-runs tiers: both are
 * TreeProject[] on the wire, both ship their sessions inline (no lazy
 * load - only archived-project stubs omit sessions; see
 * cmd/serf-hub/web_api_tree.go's apiTreeProject doc comment), so both use
 * this same builder. */
export function projectNodes(projects: ApiTreeProject[], isExpanded: IsExpanded): ProjectRailNode[] {
  return projects.map((p) => {
    const id = projectNodeId(p.key);
    return {
      id,
      kind: "project",
      project: p,
      expanded: isExpanded(id, p.default_expanded ?? false),
      children: p.sessions.map((n) => toSessionNode(n, isExpanded)),
    };
  });
}

/** Builds rail nodes for the Archived tier. An archived project's sessions
 * ship as a stub (session_count only, sessions omitted) until
 * treeStore.loadProjectDetail(key) hydrates it into `projectDetails` - the
 * rail triggers that on first expand. Until it resolves, a project with a
 * nonzero session_count still gets a single LoadingRailNode child so it
 * renders a chevron and can be expanded at all; a genuinely empty project
 * gets no children.
 *
 * Ignores default_expanded (unlike projectNodes): every archived project
 * starts collapsed regardless of what the wire says, so simply opening the
 * Archived disclosure never fires N lazy-load fetches at once for whichever
 * projects happened to look "active" server-side. */
export function archivedProjectNodes(
  projects: ApiTreeProject[],
  projectDetails: ReadonlyMap<string, ApiTreeProject>,
  isExpanded: IsExpanded,
): ProjectRailNode[] {
  return projects.map((p) => {
    const id = projectNodeId(p.key);
    const detail = projectDetails.get(p.key);
    let children: RailNode[];
    if (detail) {
      children = detail.sessions.map((n) => toSessionNode(n, isExpanded));
    } else if ((p.session_count ?? 0) > 0) {
      children = [{ id: `${id}:loading`, kind: "loading" }];
    } else {
      children = [];
    }
    return { id, kind: "project", project: p, expanded: isExpanded(id, false), children };
  });
}
