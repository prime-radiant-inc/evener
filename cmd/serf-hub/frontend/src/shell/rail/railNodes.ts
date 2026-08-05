// railNodes.ts is the pure tree-shaping layer between stores/tree.ts's wire
// data and the widgets/tree Tree widget: it decides row identity, nesting,
// and (given the caller's own expand-state map) each branch's `expanded`
// flag. No React, no fetching - Rail.tsx owns the state these functions are
// pure functions OF (the expand-override map, the lazily-loaded archived
// project detail map) and wires the results into <Tree>.

import type {
  TreeNode as ApiTreeNode,
  TreeProject as ApiTreeProject,
  PinSectionTree,
  TreeTier,
} from "../../stores/tree";
import type { TreeNode as WidgetTreeNode } from "../../widgets";

export interface SessionRailNode extends WidgetTreeNode {
  kind: "session";
  session: ApiTreeNode;
  // Always a real array (never absent) - an empty array reads as "leaf" to
  // the Tree widget exactly the same way `undefined` would (see its own
  // hasChildrenOf), so there's no reason to carry two representations of
  // the same "nothing to expand" case.
  //
  // Its current subagents, followed by at most one InactiveFoldRailNode
  // holding the finished ones (see splitChildren).
  children: (SessionRailNode | InactiveFoldRailNode)[];
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

/** The "Inactive subagents (N)" disclosure one parent gets for its own
 * finished children (parity-m3-sidebar-tree.md §3). A synthetic branch: it
 * has no session of its own, only the count it hides and the rows behind
 * it. */
export interface InactiveFoldRailNode extends WidgetTreeNode {
  kind: "inactiveFold";
  count: number;
  children: (SessionRailNode | OverflowRailNode)[];
}

export interface OverflowPage {
  projectKey: string;
  tier: TreeTier;
  offset: number;
  limit: number;
}

/** A quiet "+N older" note standing for the rows the server capped away
 * (hubcore's maxSidebarSessionsPerTier, 50 per tier). Project overflow rows
 * carry the tier offsets needed to reveal those rows; synthetic child folds
 * leave pages empty because their omitted children are not project pages. */
export interface OverflowRailNode extends WidgetTreeNode {
  kind: "overflow";
  count: number;
  pages: OverflowPage[];
}

export type RailNode = SessionRailNode | ProjectRailNode | LoadingRailNode | InactiveFoldRailNode | OverflowRailNode;

// The rows a given list has hidden. Each caller passes the tiers it actually
// renders: an active project's inline list shows Current+Recent (the archived
// tier is diverted out of it), the archived sub-branch shows only Archived,
// and a hydrated archived project shows all three.
function overflowNode(id: string, count: number, pages: OverflowPage[] = []): OverflowRailNode[] {
  return count > 0 ? [{ id: `${id}:overflow`, kind: "overflow", count, pages }] : [];
}

function tierOverflow(p: ApiTreeProject, tiers: ("current" | "recent" | "archived")[]): number {
  const field = { current: p.more_current, recent: p.more_recent, archived: p.more_archived };
  return tiers.reduce((sum, t) => sum + (field[t] ?? 0), 0);
}

function tierOverflowPages(p: ApiTreeProject, tiers: TreeTier[]): OverflowPage[] {
  const fields = { current: p.more_current, recent: p.more_recent, archived: p.more_archived };
  return tiers.flatMap((tier) => {
    const count = fields[tier] ?? 0;
    if (count <= 0) return [];
    return [
      { projectKey: p.key, tier, offset: p.sessions.filter((n) => n.tier === tier).length, limit: Math.min(count, 50) },
    ];
  });
}

function projectOverflowNode(id: string, p: ApiTreeProject, tiers: TreeTier[]): OverflowRailNode[] {
  return overflowNode(id, tierOverflow(p, tiers), tierOverflowPages(p, tiers));
}

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

// The states that make a subagent CURRENT - something you might still be
// supervising. Everything else (idle, ended, closed, errored, and any future
// terminal state) is settled and folds away. Written as the positive list
// because that is the side worth being conservative about: an unrecognized
// state folding is a row one click away, while an unrecognized state
// rendering inline forever is the clutter this exists to remove.
//
// "errored" folds with the rest, deliberately, even though the rail treats
// `failed` as a signal state elsewhere: terminal is terminal, matching the
// htmx UI this replaced (parity-m3-sidebar-tree.md §3).
//
// "idle" folds too: since sessions stopped closing on provider failure
// (ff859dbbe), a finished child rests open at idle indefinitely, so an idle
// child is settled work - it would otherwise sit in the current list forever.
// A child that picks work back up (a drive turn, job_send) reports active and
// surfaces again.
const CURRENT_SUBAGENT_STATES: ReadonlySet<string> = new Set(["active", "awaiting", "warning", "notLoaded"]);

// Namespaced the same way projectNodeId is, and off the PARENT's row_id, so
// every parent's fold is its own key at every nesting depth - expanding one
// never opens another's.
function inactiveFoldId(parentRowID: string): string {
  return `inactive:${parentRowID}`;
}

// Splits one parent's children into the rows that render inline and the
// single fold node carrying the rest. Both sides keep their incoming order,
// and the fold always lands last, so a parent's live work stays at the top of
// its own subtree.
//
// A CLUSTER row is exempt. hubcore's repeated-title clustering (tree.go's
// clusterable) only ever folds idle/ended sessions, so every member of a
// cluster is terminal by construction - splitting on state here would put
// every cluster's entire membership behind a second fold inside it, labelled
// "Inactive subagents" for rows that are neither inactive-in-that-sense nor
// subagents. A cluster is already a disclosure; its members are ordinary
// top-level sessions (parity-m3-sidebar-tree.md §3).
function splitChildren(parent: ApiTreeNode, isExpanded: IsExpanded): (SessionRailNode | InactiveFoldRailNode)[] {
  const current: SessionRailNode[] = [];
  const inactive: SessionRailNode[] = [];
  if (parent.kind === "cluster") return parent.children.map((c) => toSessionNode(c, isExpanded));
  for (const child of parent.children) {
    (CURRENT_SUBAGENT_STATES.has(child.state) ? current : inactive).push(toSessionNode(child, isExpanded));
  }
  const inactiveCount = inactive.length + (parent.more_subagents ?? 0);
  if (inactiveCount === 0) return current;
  const id = inactiveFoldId(parent.row_id);
  const omitted = overflowNode(id, parent.more_subagents ?? 0);
  return [
    ...current,
    {
      id,
      kind: "inactiveFold",
      count: inactiveCount,
      expanded: isExpanded(id, false),
      children: [...inactive, ...omitted],
    },
  ];
}

function toSessionNode(n: ApiTreeNode, isExpanded: IsExpanded): SessionRailNode {
  return {
    id: n.row_id,
    kind: "session",
    session: n,
    expanded: isExpanded(n.row_id, false),
    children: splitChildren(n, isExpanded),
  };
}

/** Builds rail nodes for a flat, childless-at-this-level session list - the
 * Needs-you, Live, and Pinned tiers, each of which is just TreeNode[] on
 * the wire. A session can still recurse into its own children (subagent
 * clusters), handled by toSessionNode regardless of which tier it's in. */
export function sessionNodes(nodes: ApiTreeNode[], isExpanded: IsExpanded): SessionRailNode[] {
  return nodes.map((n) => toSessionNode(n, isExpanded));
}

export function pinSectionNodes(section: PinSectionTree, isExpanded: IsExpanded): SessionRailNode[] {
  return sessionNodes(section.sessions, isExpanded);
}

export function pinSectionDisclosureID(sectionID: string): string {
  return `pinsection:${sectionID}`;
}

// Inlined rather than imported from RailRow's cadenceStateFor: importing it
// here would cycle railNodes.ts <-> RailRow.tsx (RailRow already imports
// railNodes for its node types). Same two wire states RailRow's own
// cadenceStateFor maps to Cadence's "needs-you" family.
function stateNeedsYou(state: string): boolean {
  return state === "awaiting" || state === "warning";
}

/** Count of nodes in `node.children` (recursed through the whole subtree,
 * not just direct children) whose own state is needs-you - i.e. how many
 * things under this session need attention, excluding the node itself.
 * Backs both the session row's derived attention Badge (vbh8, §2.2) and
 * the needs-you-first sort below. */
export function needsYouDescendantCount(node: ApiTreeNode): number {
  return node.children.reduce((sum, c) => sum + (stateNeedsYou(c.state) ? 1 : 0) + needsYouDescendantCount(c), 0);
}

export function workingDescendantCount(node: ApiTreeNode): number {
  return node.children.reduce(
    (count, child) => count + (child.state === "active" ? 1 : 0) + workingDescendantCount(child),
    0,
  );
}

// A session "wants you" either directly (its own state) or transitively (a
// needs-you descendant) - either way it should sort ahead of a quiet
// sibling within the same project.
function sessionWantsYou(n: ApiTreeNode): boolean {
  return stateNeedsYou(n.state) || needsYouDescendantCount(n) > 0;
}

// Namespaced so a project branch's own id can never collide with a
// session's row_id (row_ids are always "<scope>:...", but never start with
// "projectnode:") within the same Tree instance.
function projectNodeId(key: string): string {
  return `projectnode:${key}`;
}

// True when `nodes` (a project's session list or a tier) contains a session
// with `ref`, recursing into subagent-cluster children.
function sessionListHasRef(nodes: ApiTreeNode[], ref: string): boolean {
  return nodes.some((n) => n.ref === ref || sessionListHasRef(n.children, ref));
}

/** The ref of the TOP-LEVEL session `ref` sits under - itself when it is
 * already top-level, or null when it is not in `projects` at all (a tier-only
 * entry, or an archived stub whose sessions have not been hydrated).
 *
 * A subagent opens beside the session that spawned it, and "the session that
 * spawned it" means the top-level row, not the immediate parent: a
 * three-deep subagent still belongs beside the one row that owns the whole
 * task tree. See docs/web-ui/specs/2026-07-26-subagent-opens-beside-main.md
 * §B. */
export function topLevelAncestorRef(projects: ApiTreeProject[], ref: string): string | null {
  // A CLUSTER row is a repeated-title grouping, not the owner of a task tree:
  // its members are ordinary top-level sessions that happen to share a title,
  // and its own ref is synthetic (a SHA of project + title) naming no session
  // at all. So the search descends THROUGH it and treats its members as the
  // top-level rows they are - reporting the cluster instead would name a
  // "parent" that cannot be opened.
  const tops = (project: ApiTreeProject): ApiTreeNode[] =>
    project.sessions.flatMap((n) => (n.kind === "cluster" ? n.children : [n]));
  for (const project of projects) {
    for (const top of tops(project)) {
      if (top.ref === ref || sessionListHasRef(top.children, ref)) return top.ref;
    }
  }
  return null;
}

/** The projectnode: id of the project (or test-run) whose sessions include
 * `ref`, or null when `ref` is a top-level tier entry (needs-you/live/pinned)
 * or lives in an unloaded archived stub - i.e. nothing to un-collapse before
 * scrolling. Rail's reveal effect (railController's /project) uses this to
 * expand the right project section, matching the id projectNodes assigns. */
export function projectNodeIdForSessionRef(projects: ApiTreeProject[], ref: string): string | null {
  for (const project of projects) {
    if (sessionListHasRef(project.sessions, ref)) return projectNodeId(project.key);
  }
  return null;
}

/** Builds rail nodes for the Projects and Test-runs tiers: both are
 * TreeProject[] on the wire, both ship their sessions inline (no lazy
 * load - only archived-project stubs omit sessions; see
 * cmd/serf-hub/web_api_tree.go's apiTreeProject doc comment), so both use
 * this same builder. Sessions sort needs-you-first (vbh8, §2.2) - a stable
 * partition (Array.prototype.sort is stable in the target engines), so
 * sessions that don't need you keep their incoming relative order. */
export function projectNodes(projects: ApiTreeProject[], isExpanded: IsExpanded): ProjectRailNode[] {
  return projects.map((p) => {
    const id = projectNodeId(p.key);
    return {
      id,
      kind: "project",
      project: p,
      expanded: isExpanded(id, p.default_expanded ?? false),
      children: [
        ...p.sessions
          .filter((n) => !isArchivedTier(n))
          .sort((a, b) => Number(sessionWantsYou(b)) - Number(sessionWantsYou(a)))
          .map((n) => toSessionNode(n, isExpanded)),
        ...projectOverflowNode(id, p, ["current", "recent"]),
      ],
    };
  });
}

// A session the server put in the archived tier. `tier` is the only archived
// signal on a session (see RailRow's own note: there is no boolean), and it is
// decision-driven, not merely age-driven, when an explicit archive decision
// exists - see hubcore.classifySession.
function isArchivedTier(n: ApiTreeNode): boolean {
  return n.tier === "archived";
}

// Namespaced apart from projectNodeId on purpose: the SAME project renders
// twice when it has both live and archived sessions - once in Projects, once
// as a sub-branch here - and two Tree branches sharing an id would share
// expand state.
function archivedGroupId(key: string): string {
  return `archivedgroup:${key}`;
}

/** For each project (active or test-run) holding archived-tier sessions, one
 * branch under the project's own name revealing just those. They already ride
 * the main /api/tree snapshot - unlike whole archived projects, which ship as
 * stubs (archivedProjectNodes) - so nothing here lazy-loads.
 *
 * Carries the REAL project object, so the row's menu acts on the project
 * itself rather than on a synthetic stand-in. */
export function archivedSessionGroups(projects: ApiTreeProject[], isExpanded: IsExpanded): ProjectRailNode[] {
  const groups: ProjectRailNode[] = [];
  for (const p of projects) {
    const archived = p.sessions.filter(isArchivedTier);
    if (archived.length === 0) continue;
    const id = archivedGroupId(p.key);
    groups.push({
      id,
      kind: "project",
      project: p,
      expanded: isExpanded(id, false),
      children: [...archived.map((n) => toSessionNode(n, isExpanded)), ...projectOverflowNode(id, p, ["archived"])],
    });
  }
  return groups;
}

/** How many sessions the "Archived sessions" section stands for: every whole
 * archived project's own rows, plus the archived-tier sessions still living
 * inside active projects. A stub's session_count is authoritative; a
 * hydrated detail has capped rows plus pagination overflow to account for. */
function archivedProjectSessionCount(p: ApiTreeProject): number {
  if (p.session_count !== undefined) return p.session_count;
  return p.sessions.length + tierOverflow(p, ["current", "recent", "archived"]);
}

export function archivedCount(archivedProjects: ApiTreeProject[], otherProjects: ApiTreeProject[]): number {
  const whole = archivedProjects.reduce((sum, p) => sum + archivedProjectSessionCount(p), 0);
  return otherProjects.reduce((sum, p) => sum + p.sessions.filter(isArchivedTier).length, whole);
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
      // The hydrated detail is the authority on both the rows and what was
      // capped away from them - the stub carried neither.
      children = [
        ...detail.sessions.map((n) => toSessionNode(n, isExpanded)),
        ...projectOverflowNode(id, detail, ["current", "recent", "archived"]),
      ];
    } else if ((p.session_count ?? 0) > 0) {
      children = [{ id: `${id}:loading`, kind: "loading" }];
    } else {
      children = [];
    }
    return { id, kind: "project", project: p, expanded: isExpanded(id, false), children };
  });
}
