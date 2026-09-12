// railNodes.ts is the pure tree-shaping layer between navigation resources
// data and the widgets/tree Tree widget: it decides row identity, nesting,
// and (given the caller's own expand-state map) each branch's `expanded`
// flag. No React, no fetching - Rail.tsx owns the state these functions are
// pure functions OF (the expand-override map, the lazily-loaded archived
// project detail map) and wires the results into <Tree>.

import type { NavigationJobSummary, NavigationSessionSummary } from "../../protocol/types.gen";
import { projectNodeExpansionKey } from "./railExpansion";

export type TreeTier = "current" | "recent" | "archived";

/** Resource summaries adapted to the presentation contract at the rail edge. */
export interface RailSession extends NavigationSessionSummary {
  row_id: string;
  tier?: string;
  pin_section_id?: string;
  age?: string;
  model?: string;
  children: RailSession[];
  project_key?: string;
}

export interface RailJob extends NavigationJobSummary {
  row_id: string;
}

export interface RailProject {
  key: string;
  name: string;
  working_dir?: string;
  rollup_state?: string;
  rollup_live?: number;
  rollup_attn?: number;
  default_expanded?: boolean;
  more_current?: number;
  more_recent?: number;
  more_archived?: number;
  worktrees?: number;
  is_archived?: boolean;
  favorite?: boolean;
  session_count?: number;
  sessions: RailSession[];
  loaded?: boolean;
  resourceError?: string;
  nextOffsets?: Partial<Record<TreeTier, number>>;
}

export interface RailPinSection {
  id: string;
  name: string;
  member_count?: number;
  sessions: RailSession[];
  remaining?: number;
  offset?: number;
  limit?: number;
}

import type { TreeNode as WidgetTreeNode } from "../../widgets";

export interface SessionRailNode extends WidgetTreeNode {
  kind: "session";
  session: RailSession;
  // Always a real array (never absent) - an empty array reads as "leaf" to
  // the Tree widget exactly the same way `undefined` would (see its own
  // hasChildrenOf), so there's no reason to carry two representations of
  // the same "nothing to expand" case.
  //
  // Its current subagents and jobs, followed by independent inactive-subagent
  // and completed-job folds when either has rows (see splitChildren).
  children: (SessionRailNode | JobRailNode | InactiveFoldRailNode | CompletedJobsFoldRailNode)[];
}

export interface JobRailNode extends WidgetTreeNode {
  kind: "job";
  job: RailJob;
  active: boolean;
}

export interface ProjectRailNode extends WidgetTreeNode {
  kind: "project";
  project: RailProject;
  // The label RailRow actually renders: project.name, decorated with a
  // distinguishing path segment when it collides with a sibling's name in
  // the same list (see projectDisplayLabels). Optional so a hand-built test
  // double can omit it and still render the plain name via RailRow's own
  // fallback.
  displayName?: string;
  // Usually SessionRailNode[]; an archived project not yet hydrated (see
  // archivedProjectNodes) instead gets a single LoadingRailNode child so it
  // still renders a chevron before its real sessions have loaded.
  children: RailNode[];
  resourceError?: string;
  retry?: () => void;
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

export interface CompletedJobsFoldRailNode extends WidgetTreeNode {
  kind: "completedJobsFold";
  count: number;
  children: JobRailNode[];
}

export interface OverflowPage {
  projectKey?: string;
  tier?: TreeTier;
  section?: "live" | "needs_you";
  sectionId?: string;
  catalog?: "projects" | "archived_projects" | "test_runs";
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

export function sectionOverflowNode(
  id: string,
  section: "live" | "needs_you",
  remaining: number,
  offset: number,
  limit: number,
): OverflowRailNode[] {
  return overflowNode(id, remaining, [{ section, offset, limit }]);
}

export function pinSectionOverflowNode(
  id: string,
  sectionId: string,
  remaining: number,
  offset: number,
  limit: number,
): OverflowRailNode[] {
  return overflowNode(id, remaining, [{ sectionId, offset, limit }]);
}

export function catalogOverflowNode(
  id: string,
  catalog: "projects" | "archived_projects" | "test_runs",
  remaining: number,
  offset: number,
  limit: number,
): OverflowRailNode[] {
  return overflowNode(id, remaining, [{ catalog, offset, limit }]);
}

export type RailNode =
  | SessionRailNode
  | JobRailNode
  | ProjectRailNode
  | LoadingRailNode
  | InactiveFoldRailNode
  | CompletedJobsFoldRailNode
  | OverflowRailNode;

type SessionNodeCacheEntry = Readonly<{
  children: SessionRailNode["children"];
  expanded: boolean;
  value: SessionRailNode;
}>;
type ProjectNodeCacheEntry = Readonly<{
  children: RailNode[];
  displayName: string | undefined;
  expanded: boolean;
  value: ProjectRailNode;
}>;
const sessionChildrenCache = new WeakMap<object, WeakMap<IsExpanded, SessionRailNode["children"]>>();
const sessionNodeCache = new WeakMap<object, WeakMap<IsExpanded, SessionNodeCacheEntry>>();
const projectChildrenCache = new WeakMap<object, WeakMap<IsExpanded, Map<string, RailNode[]>>>();
const projectNodeCache = new WeakMap<object, WeakMap<IsExpanded, Map<string, ProjectNodeCacheEntry>>>();

// The rows a given list has hidden. Each caller passes the tiers it actually
// renders: an active project's inline list shows Current+Recent (the archived
// tier is diverted out of it), the archived sub-branch shows only Archived,
// and a hydrated archived project shows all three.
function overflowNode(id: string, count: number, pages: OverflowPage[] = []): OverflowRailNode[] {
  return count > 0 ? [{ id: `${id}:overflow`, kind: "overflow", count, pages }] : [];
}

function tierOverflow(p: RailProject, tiers: ("current" | "recent" | "archived")[]): number {
  const field = { current: p.more_current, recent: p.more_recent, archived: p.more_archived };
  return tiers.reduce((sum, t) => sum + (field[t] ?? 0), 0);
}

function tierOverflowPages(p: RailProject, tiers: TreeTier[]): OverflowPage[] {
  const fields = { current: p.more_current, recent: p.more_recent, archived: p.more_archived };
  return tiers.flatMap((tier) => {
    const count = fields[tier] ?? 0;
    if (count <= 0) return [];
    return [
      {
        projectKey: p.key,
        tier,
        offset: p.nextOffsets?.[tier] ?? p.sessions.filter((n) => (n.tier ?? "current") === tier).length,
        limit: Math.min(count, 50),
      },
    ];
  });
}

function projectOverflowNode(id: string, p: RailProject, tiers: TreeTier[]): OverflowRailNode[] {
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
const CURRENT_SUBAGENT_STATES: ReadonlySet<string> = new Set([
  "active",
  "awaiting",
  "warning",
  "restartRequired",
  "notLoaded",
]);

// Namespaced the same way projectNodeExpansionKey is, and off the PARENT's row_id, so
// every parent's fold is its own key at every nesting depth - expanding one
// never opens another's.
function inactiveFoldId(parentRowID: string): string {
  return `inactive:${parentRowID}`;
}

function completedJobsFoldId(parentRowID: string): string {
  return `completed-jobs:${parentRowID}`;
}

function toJobNode(parent: RailSession, job: NavigationJobSummary): JobRailNode {
  const rowID = `job:${parent.row_id}:${job.job_id}`;
  return {
    id: rowID,
    kind: "job",
    job: { ...job, row_id: rowID },
    active: false,
    children: [],
  };
}

function activeJobNode(parent: RailSession, job: NavigationJobSummary): JobRailNode {
  return { ...toJobNode(parent, job), active: true };
}

function subagentIsCurrent(child: RailSession): boolean {
  const activity = activeWorkSummary(child);
  return CURRENT_SUBAGENT_STATES.has(child.state) || activity.workingSubagents > 0 || activity.runningJobs > 0;
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
function splitChildren(
  parent: RailSession,
  isExpanded: IsExpanded,
): (SessionRailNode | JobRailNode | InactiveFoldRailNode | CompletedJobsFoldRailNode)[] {
  const cached = sessionChildrenCache.get(parent as object)?.get(isExpanded);
  if (cached) return cached;
  const current: SessionRailNode[] = [];
  const inactive: SessionRailNode[] = [];
  if (parent.kind === "cluster") {
    const children = parent.children.map((c) => toSessionNode(c, isExpanded));
    cacheSessionChildren(parent, isExpanded, children);
    return children;
  }
  for (const child of parent.children) {
    (subagentIsCurrent(child) ? current : inactive).push(toSessionNode(child, isExpanded));
  }
  const inactiveCount = inactive.length + (parent.more_subagents ?? 0);
  const children: (SessionRailNode | JobRailNode | InactiveFoldRailNode | CompletedJobsFoldRailNode)[] = [
    ...current,
    ...(parent.running_jobs ?? []).map((job) => activeJobNode(parent, job)),
  ];
  if (inactiveCount > 0) {
    const id = inactiveFoldId(parent.row_id);
    const omitted = overflowNode(id, parent.more_subagents ?? 0);
    children.push({
      id,
      kind: "inactiveFold",
      count: inactiveCount,
      expanded: isExpanded(id, false),
      children: [...inactive, ...omitted],
    });
  }
  const completedJobs = parent.completed_jobs ?? [];
  if (completedJobs.length > 0) {
    const id = completedJobsFoldId(parent.row_id);
    children.push({
      id,
      kind: "completedJobsFold",
      count: completedJobs.length,
      expanded: isExpanded(id, false),
      children: completedJobs.map((job) => toJobNode(parent, job)),
    });
  }
  cacheSessionChildren(parent, isExpanded, children);
  return children;
}

function cacheSessionChildren(
  parent: RailSession,
  isExpanded: IsExpanded,
  children: SessionRailNode["children"],
): void {
  let entries = sessionChildrenCache.get(parent as object);
  if (!entries) {
    entries = new WeakMap();
    sessionChildrenCache.set(parent as object, entries);
  }
  entries.set(isExpanded, children);
}

function toSessionNode(n: RailSession, isExpanded: IsExpanded): SessionRailNode {
  const expanded = isExpanded(n.row_id, false);
  const children = splitChildren(n, isExpanded);
  const cached = sessionNodeCache.get(n as object)?.get(isExpanded);
  if (cached && cached.expanded === expanded && cached.children === children) return cached.value;
  const result: SessionRailNode = {
    id: n.row_id,
    kind: "session",
    session: n,
    expanded,
    children,
  };
  let entries = sessionNodeCache.get(n as object);
  if (!entries) {
    entries = new WeakMap();
    sessionNodeCache.set(n as object, entries);
  }
  entries.set(isExpanded, { children, expanded, value: result });
  return result;
}

/** Builds rail nodes for a flat, childless-at-this-level session list - the
 * Needs-you, Live, and Pinned tiers, each of which is just TreeNode[] on
 * the wire. A session can still recurse into its own children (subagent
 * clusters), handled by toSessionNode regardless of which tier it's in. */
export function sessionNodes(nodes: readonly RailSession[], isExpanded: IsExpanded): SessionRailNode[] {
  return nodes.map((n) => toSessionNode(n, isExpanded));
}

export function pinSectionNodes(section: RailPinSection, isExpanded: IsExpanded): SessionRailNode[] {
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
  return state === "awaiting" || state === "warning" || state === "restartRequired";
}

// The state a row PRESENTS, which is not always the wire state. "awaiting"
// means "the turn ended; the next input comes from this session's owner" -
// and a subagent's owner is its PARENT session, not the user (the user never
// steers a subagent directly). So a turn-ended subagent is, on this triage
// surface, simply idle: glossing it "your move" made every finished delegate
// read as attention it does not need. Only a genuine ask_user (ask_pending)
// keeps a subagent needs-you, because that question does reach the user.
// Every per-node attention judgment (the row's dot and gloss in RailRow, the
// badge count and sort below) reads this one helper, so they can never
// disagree about the same row.
export function displayState(node: RailSession): string {
  if (node.kind === "subagent" && node.state === "awaiting" && node.ask_pending !== true) return "idle";
  return node.state;
}

/** Count of nodes in `node.children` (recursed through the whole subtree,
 * not just direct children) whose own state is needs-you - i.e. how many
 * things under this session need attention, excluding the node itself.
 * Backs both the session row's derived attention Badge (vbh8, §2.2) and
 * the needs-you-first sort below. */
export function needsYouDescendantCount(node: RailSession): number {
  return node.children.reduce(
    (sum, c) => sum + (stateNeedsYou(displayState(c)) ? 1 : 0) + needsYouDescendantCount(c),
    0,
  );
}

export interface ActiveWorkSummary {
  workingSubagents: number;
  runningJobs: number;
}

export function activeWorkSummary(node: RailSession): ActiveWorkSummary {
  let workingSubagents = 0;
  let runningJobs = (node.running_jobs ?? []).length;
  for (const child of node.children) {
    const childActivity = activeWorkSummary(child);
    workingSubagents += (child.state === "active" ? 1 : 0) + childActivity.workingSubagents;
    runningJobs += childActivity.runningJobs;
  }
  return { workingSubagents, runningJobs };
}

export function runningJobCount(node: RailSession): number {
  return activeWorkSummary(node).runningJobs;
}

export function workingDescendantCount(node: RailSession): number {
  return activeWorkSummary(node).workingSubagents;
}

// A session "wants you" either directly (its own state) or transitively (a
// needs-you descendant) - either way it should sort ahead of a quiet
// sibling within the same project.
function sessionWantsYou(n: RailSession): boolean {
  return stateNeedsYou(displayState(n)) || needsYouDescendantCount(n) > 0;
}

// Namespaced so a project branch's own id can never collide with a
// session's row_id (row_ids are always "<scope>:...", but never start with
// "projectnode:") within the same Tree instance.
// The bit of a project's own working_dir that tells it apart from a
// same-named sibling - the parent directory's basename (two checkouts named
// "frontend" usually differ in which repo holds them, not in the leaf
// directory name itself, which is the name colliding in the first place).
// Falls back to the project's own key when there's no working_dir to read
// (never absent for a real project, but this keeps a synthetic/test project
// from decorating into "undefined").
function distinguishingSegment(p: RailProject): string {
  const dir = p.working_dir;
  if (!dir) return p.key;
  const segments = dir.split("/").filter((s) => s.length > 0);
  if (segments.length === 0) return p.key;
  return segments.length >= 2 ? (segments[segments.length - 2] as string) : (segments[segments.length - 1] as string);
}

/** The label each project in `projects` should actually render, keyed by
 * project.key: the bare name, except within a same-named group (2+ projects
 * sharing a name), where every member gets the name plus its own
 * distinguishing path segment - otherwise two different projects render as
 * identical rows. Computed over exactly the list a caller is about to
 * render (a Projects section, an archived stub list, ...), never globally,
 * so a collision in one section can't decorate an unrelated one. */
export function projectDisplayLabels(projects: readonly RailProject[]): Map<string, string> {
  const byName = new Map<string, RailProject[]>();
  for (const p of projects) {
    const group = byName.get(p.name) ?? [];
    group.push(p);
    byName.set(p.name, group);
  }
  const labels = new Map<string, string>();
  for (const group of byName.values()) {
    for (const p of group) {
      labels.set(p.key, group.length > 1 ? `${p.name} (${distinguishingSegment(p)})` : p.name);
    }
  }
  return labels;
}

// True when `nodes` (a project's session list or a tier) contains a session
// with `ref`, recursing into subagent-cluster children.
function sessionListHasRef(nodes: RailSession[], ref: string): boolean {
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
export function topLevelAncestorRef(projects: readonly RailProject[], ref: string): string | null {
  // A CLUSTER row is a repeated-title grouping, not the owner of a task tree:
  // its members are ordinary top-level sessions that happen to share a title,
  // and its own ref is synthetic (a SHA of project + title) naming no session
  // at all. So the search descends THROUGH it and treats its members as the
  // top-level rows they are - reporting the cluster instead would name a
  // "parent" that cannot be opened.
  const tops = (project: RailProject): RailSession[] =>
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
export function projectNodeIdForSessionRef(projects: readonly RailProject[], ref: string): string | null {
  for (const project of projects) {
    if (sessionListHasRef(project.sessions, ref)) return projectNodeExpansionKey(project.key);
  }
  return null;
}

/** Builds rail nodes for the Projects and Test-runs tiers: both are
 * TreeProject[] on the wire, both ship their sessions inline (no lazy
 * load - only archived-project stubs omit sessions; see
 * cmd/evener-hub/web_api_tree.go's apiTreeProject doc comment), so both use
 * this same builder. Sessions sort needs-you-first (vbh8, §2.2) - a stable
 * partition (Array.prototype.sort is stable in the target engines), so
 * sessions that don't need you keep their incoming relative order. */
export function projectNodes(projects: readonly RailProject[], isExpanded: IsExpanded): ProjectRailNode[] {
  const labels = projectDisplayLabels(projects);
  return projects.map((p) => {
    const id = projectNodeExpansionKey(p.key);
    const expanded = isExpanded(id, p.default_expanded ?? false);
    const displayName = labels.get(p.key);
    const children = projectChildren(p, isExpanded, "active", () =>
      p.sessions.length === 0 && p.loaded !== true && (p.session_count ?? 0) > 0
        ? [{ id: `${id}:loading`, kind: "loading" as const }]
        : [
            ...p.sessions
              .filter((n) => !isArchivedTier(n))
              .sort((a, b) => Number(sessionWantsYou(b)) - Number(sessionWantsYou(a)))
              .map((n) => toSessionNode(n, isExpanded)),
            ...projectOverflowNode(id, p, ["current", "recent"]),
          ],
    );
    const cached = projectNodeCache
      .get(p as object)
      ?.get(isExpanded)
      ?.get("active");
    if (cached && cached.children === children && cached.displayName === displayName && cached.expanded === expanded)
      return cached.value;
    const result: ProjectRailNode = {
      id,
      kind: "project",
      project: p,
      resourceError: p.resourceError,
      displayName,
      expanded,
      children,
    };
    cacheProjectNode(p, isExpanded, "active", { children, displayName, expanded, value: result });
    return result;
  });
}

function projectChildren(
  project: RailProject,
  isExpanded: IsExpanded,
  variant: string,
  build: () => RailNode[],
): RailNode[] {
  const cached = projectChildrenCache
    .get(project as object)
    ?.get(isExpanded)
    ?.get(variant);
  if (cached) return cached;
  const children = build();
  let byLookup = projectChildrenCache.get(project as object);
  if (!byLookup) {
    byLookup = new WeakMap();
    projectChildrenCache.set(project as object, byLookup);
  }
  let byVariant = byLookup.get(isExpanded);
  if (!byVariant) {
    byVariant = new Map();
    byLookup.set(isExpanded, byVariant);
  }
  byVariant.set(variant, children);
  return children;
}

function cacheProjectNode(
  project: RailProject,
  isExpanded: IsExpanded,
  variant: string,
  entry: ProjectNodeCacheEntry,
): void {
  let byLookup = projectNodeCache.get(project as object);
  if (!byLookup) {
    byLookup = new WeakMap();
    projectNodeCache.set(project as object, byLookup);
  }
  let byVariant = byLookup.get(isExpanded);
  if (!byVariant) {
    byVariant = new Map();
    byLookup.set(isExpanded, byVariant);
  }
  byVariant.set(variant, entry);
}

// A session the server put in the archived tier. `tier` is the only archived
// signal on a session (see RailRow's own note: there is no boolean), and it is
// decision-driven, not merely age-driven, when an explicit archive decision
// exists - see hubcore.classifySession.
function isArchivedTier(n: RailSession): boolean {
  return n.tier === "archived";
}

// Namespaced apart from projectNodeExpansionKey on purpose: the SAME project renders
// twice when it has both live and archived sessions - once in Projects, once
// as a sub-branch here - and two Tree branches sharing an id would share
// expand state.
function archivedGroupId(key: string): string {
  return `archivedgroup:${key}`;
}

/** For each project (active or test-run) holding archived-tier sessions, one
 * branch under the project's own name revealing just those. They already ride
 * the active project's loaded root - unlike whole archived projects, which
 * ship as stubs (archivedProjectNodes) - so nothing here lazy-loads.
 *
 * Carries the REAL project object, so the row's menu acts on the project
 * itself rather than on a synthetic stand-in. */
export function archivedSessionGroups(projects: readonly RailProject[], isExpanded: IsExpanded): ProjectRailNode[] {
  const labels = projectDisplayLabels(projects);
  const groups: ProjectRailNode[] = [];
  for (const p of projects) {
    const archived = p.sessions.filter(isArchivedTier);
    if (archived.length === 0) continue;
    const id = archivedGroupId(p.key);
    const displayName = labels.get(p.key);
    const expanded = isExpanded(id, false);
    const children = projectChildren(p, isExpanded, "archived-group", () => [
      ...archived.map((n) => toSessionNode(n, isExpanded)),
      ...projectOverflowNode(id, p, ["archived"]),
    ]);
    const cached = projectNodeCache
      .get(p as object)
      ?.get(isExpanded)
      ?.get("archived-group");
    if (cached && cached.children === children && cached.displayName === displayName && cached.expanded === expanded) {
      groups.push(cached.value);
      continue;
    }
    const result: ProjectRailNode = {
      id,
      kind: "project",
      project: p,
      resourceError: p.resourceError,
      displayName,
      expanded,
      children,
    };
    cacheProjectNode(p, isExpanded, "archived-group", { children, displayName, expanded, value: result });
    groups.push(result);
  }
  return groups;
}

/** How many sessions the "Archived sessions" section stands for: every whole
 * archived project's own rows, plus the archived-tier sessions still living
 * inside active projects. A stub's session_count is authoritative; a
 * hydrated detail has capped rows plus pagination overflow to account for. */
function archivedProjectSessionCount(p: RailProject): number {
  if (p.sessions.length > 0) return p.sessions.length + tierOverflow(p, ["current", "recent", "archived"]);
  if (p.session_count !== undefined) return p.session_count;
  return p.sessions.length + tierOverflow(p, ["current", "recent", "archived"]);
}

export function archivedCount(archivedProjects: readonly RailProject[], otherProjects: readonly RailProject[]): number {
  const whole = archivedProjects.reduce((sum, p) => sum + archivedProjectSessionCount(p), 0);
  return otherProjects.reduce(
    (sum, p) => sum + p.sessions.filter(isArchivedTier).length + (p.more_archived ?? 0),
    whole,
  );
}

/** Builds rail nodes for the Archived tier. An archived project's sessions
 * ship as a stub (session_count only, sessions omitted) until
 * navigationStore.loadProject(key) hydrates it into its resource state - the
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
  projects: readonly RailProject[],
  projectDetails: ReadonlyMap<string, RailProject>,
  isExpanded: IsExpanded,
): ProjectRailNode[] {
  const labels = projectDisplayLabels(projects);
  return projects.map((p) => {
    const id = projectNodeExpansionKey(p.key);
    const detail = projectDetails.get(p.key);
    let children: RailNode[];
    if (detail) {
      // The hydrated detail is the authority on both the rows and what was
      // capped away from them - the stub carried neither.
      children = projectChildren(detail, isExpanded, "archived-detail", () => [
        ...detail.sessions.map((n) => toSessionNode(n, isExpanded)),
        ...projectOverflowNode(id, detail, ["current", "recent", "archived"]),
      ]);
    } else if ((p.session_count ?? 0) > 0) {
      children = projectChildren(p, isExpanded, "archived-loading", () => [{ id: `${id}:loading`, kind: "loading" }]);
    } else {
      children = projectChildren(p, isExpanded, "archived-empty", () => []);
    }
    const displayName = labels.get(p.key);
    const expanded = isExpanded(id, false);
    const cached = projectNodeCache
      .get(p as object)
      ?.get(isExpanded)
      ?.get("archived-project");
    if (cached && cached.children === children && cached.displayName === displayName && cached.expanded === expanded)
      return cached.value;
    const result: ProjectRailNode = {
      id,
      kind: "project",
      project: p,
      displayName,
      expanded,
      children,
    };
    cacheProjectNode(p, isExpanded, "archived-project", { children, displayName, expanded, value: result });
    return result;
  });
}
