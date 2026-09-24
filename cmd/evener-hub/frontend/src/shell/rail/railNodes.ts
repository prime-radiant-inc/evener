// railNodes.ts is the pure tree-shaping layer between navigation resources
// data and the widgets/tree Tree widget: it decides row identity, nesting,
// and (given the caller's own expand-state map) each branch's `expanded`
// flag. No React, no fetching - Rail.tsx owns the state these functions are
// pure functions OF (the expand-override map, the lazily-loaded archived
// project detail map, the manifest's launch sources) and wires the results
// into <Tree>.

import type {
  NavigationJobSummary,
  NavigationSessionSummary,
  NavigationWatchSummary,
  Source,
} from "@evener/appwire-client";
import { projectNodeExpansionKey } from "@evener/appwire-client/state/navigation";
import { LOCAL_HOST } from "../../stores/hostRouting";

export type TreeTier = "current" | "recent" | "archived";

/** Resource summaries adapted to the presentation contract at the rail edge.
 *
 * Carries NO preformatted age: a relative stamp is wall-clock-dependent, so the
 * adapter computing one would freeze it at whatever the summary read when it
 * arrived (the sidebar's idle "stays 'now'" bug). Rows derive it from the
 * summary's own `updated_at` anchor against the rail clock. */
export interface RailSession extends NavigationSessionSummary {
  row_id: string;
  tier?: string;
  pin_section_id?: string;
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
  // The navigation summary's owning sources ("local" for this hub's own
  // sessions, a configured host name for each remote host's). Project-level
  // mutations are keyed by (source, project ID), so the rail passes them
  // through to favorite/archive/delete instead of letting the request default
  // to this hub's own project of the same ID or path.
  sources?: string[];
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
  // Its current subagents, running jobs, and own live watches, followed by
  // independent inactive-subagent and completed-job folds when either has
  // rows (see splitChildren).
  children: (
    | SessionRailNode
    | JobRailNode
    | WatchRailNode
    | InactiveFoldRailNode
    | CompletedJobsFoldRailNode
    | OverflowRailNode
  )[];
  // Set only on the ROOT rows of a flat cross-project tier - the ones
  // sessionNodes builds (Live, Needs-you, Pinned): those rows name their
  // project and suppress the pin star, wherever host grouping nests them.
  // RailRow reads this mark instead of nesting depth: host subheaders put
  // tier roots at depth 1, where project rows sit too, so depth no longer
  // separates the two shapes.
  crossProjectTier?: boolean;
}

export interface JobRailNode extends WidgetTreeNode {
  kind: "job";
  job: RailJob;
  active: boolean;
}

/** One of a session's own live watches, as a quiet leaf row in that
 * session's fold-out. Mirrors JobRailNode: it carries no children and no
 * rail-side state of its own - the wire row ("active", cadence, note) is the
 * whole truth RailRow renders. */
export interface WatchRailNode extends WidgetTreeNode {
  kind: "watch";
  watch: NavigationWatchSummary;
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
  // The host a row's launch affordances target, not this hub: the host a
  // "Host, then project" copy nests under, or the first owning host in
  // rail order on every row that renders a project's own shape
  // (project-first, test-runs, and the archived tiers). The flat
  // Projects tier leaves it absent - no remote source exists in flat
  // mode, so every project is this hub's own.
  spawnHost?: string;
  // True on the ONE row that renders the project's aggregate facts - the
  // overflow row and the rollup signal/badge - so they read once instead of
  // claiming per-host counts the wire does not carry: every row that
  // renders a project's own shape (project-first, test-runs, the
  // archived tiers), or the first rows-bearing copy in rail order in
  // host-first. The flat Projects tier carries no mark - its rows claim
  // no launch host, and RailRow reads their aggregate role from that
  // absence.
  canonicalCopy?: boolean;
}

export interface LoadingRailNode extends WidgetTreeNode {
  kind: "loading";
}

/** One configured host as a tree group - the rail's organize-by setting.
 * Hosts are the top groups in "host, then project" mode, a sub-branch inside
 * each project in "project, then host" mode, and the Live section's
 * subheaders whenever its rows span more than one host. Carries no rollup
 * of its own: the rows under it keep their own signals, and an honest
 * per-host attention count would need wire support the manifest does not
 * carry. */
export interface HostRailNode extends WidgetTreeNode {
  kind: "host";
  // The manifest's own facts for this host: its display label (the one name
  // the spawn picker shows too) and its display-view online flag.
  host: { id: string; label: string; online: boolean };
  children: RailNode[];
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
 * leave pages empty because their omitted children are not project pages.
 *
 * A capped WATCH list reuses this same row shape (see MAX_INLINE_WATCHES) with
 * `suffix` set, so "+N more watches" reads in the rail's one existing overflow
 * grammar instead of inventing a second one. */
export interface OverflowRailNode extends WidgetTreeNode {
  kind: "overflow";
  count: number;
  pages: OverflowPage[];
  /** The wording after the count. Absent means the tier cap's "older". */
  suffix?: string;
  /** True when the row stands for rows that cannot be revealed by a fetch
   * (the local watch cap: the wire already carried every row). Such a row is a
   * count, not a control: nothing activates it. Tier and catalog overflow rows
   * leave this false and reveal their next page on activation. */
  passive?: boolean;
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
  | WatchRailNode
  | ProjectRailNode
  | HostRailNode
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
  // Absent on every variant a host-grouped copy is not (see
  // ProjectRailNode.spawnHost); compared as undefined against those.
  spawnHost?: string;
  canonicalCopy?: boolean;
  value: ProjectRailNode;
}>;
const sessionChildrenCache = new WeakMap<object, WeakMap<IsExpanded, SessionRailNode["children"]>>();
const sessionNodeCache = new WeakMap<object, WeakMap<IsExpanded, SessionNodeCacheEntry>>();
// The crossProjectTier variant of sessionNodeCache, kept apart so one tier
// shape's node can never serve the other (see toSessionNode).
const crossProjectSessionNodeCache = new WeakMap<object, WeakMap<IsExpanded, SessionNodeCacheEntry>>();
const projectChildrenCache = new WeakMap<object, WeakMap<IsExpanded, Map<string, RailNode[]>>>();
const projectNodeCache = new WeakMap<object, WeakMap<IsExpanded, Map<string, ProjectNodeCacheEntry>>>();

// The rows a given list has hidden. Each caller passes the tiers it actually
// renders: an active project's inline list shows Current+Recent (the archived
// tier is diverted out of it), the archived sub-branch shows only Archived,
// and a hydrated archived project shows all three.
function overflowNode(
  id: string,
  count: number,
  pages: OverflowPage[] = [],
  suffix?: string,
  passive = false,
): OverflowRailNode[] {
  return count > 0 ? [{ id: `${id}:overflow`, kind: "overflow", count, pages, suffix, passive }] : [];
}

function tierOverflow(p: RailProject, tiers: readonly ("current" | "recent" | "archived")[]): number {
  const field = { current: p.more_current, recent: p.more_recent, archived: p.more_archived };
  return tiers.reduce((sum, t) => sum + (field[t] ?? 0), 0);
}

function tierOverflowPages(p: RailProject, tiers: readonly TreeTier[]): OverflowPage[] {
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

function projectOverflowNode(id: string, p: RailProject, tiers: readonly TreeTier[]): OverflowRailNode[] {
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

/** Watch rows a session renders inline before the rest fold behind one
 * "+N more watches" note. Three keeps a fold-out scannable in the rail's
 * ~280px column; past that the watches are inventory, and the count on the
 * summary line is the honest summary of them. */
const MAX_INLINE_WATCHES = 3;

// Namespaced off the PARENT's row_id like the job rows are, so two sessions
// carrying a same-id watch (each the watch's own receiver) still get distinct
// tree ids.
function toWatchNode(parent: RailSession, watch: NavigationWatchSummary): WatchRailNode {
  const rowID = `watch:${parent.row_id}:${watch.id}`;
  return { id: rowID, kind: "watch", watch, children: [] };
}

function watchOverflowId(parentRowID: string): string {
  return `watches:${parentRowID}`;
}

/** How many of a session's OWN live watches are still armed. Deliberately not
 * a subtree rollup: the hub projects each watch onto exactly the summary of
 * the session that receives it (navigation_projection.go's navigationWatches),
 * so summing descendants here would print a subagent's watch on every ancestor
 * row as well as on the subagent's own - one watch, several counts. A receiver
 * watch belongs to the session whose summary carries it. */
export function activeWatchCount(node: RailSession): number {
  // Include the armed rows the hub omitted: past the per-session cap they are
  // not on `node.watches`, but they are still this session's armed watches, and
  // counting only the retained rows understates the total.
  return armedWatchCount(node.watches) + (node.omitted_armed_watches ?? 0);
}

/** The same count read straight off the wire list, so the activity panel's
 * Watches header and the rail row's count cannot drift apart: both numbers are
 * one predicate, not two copies of one. */
export function armedWatchCount(watches: readonly NavigationWatchSummary[] | undefined): number {
  return (watches ?? []).filter((watch) => watch.active).length;
}

/** The session's summary-line watch count. It reports ONE total per session -
 * the retained rows the fold-out lists plus the exact number of rows the
 * projector omitted (a session above its per-session cap, or rows its byte
 * fitter shed) as "+N more" - so the summary and the fold-out hanging under it
 * cannot disagree about how many watches the session holds.
 *
 * When every retained row is armed the base is the armed count, byte-identical
 * to the wording that shipped before the retained/inactive distinction existed
 * (`1 watch`, `2 watches · +1 more`). When a retained row is inactive - a fired
 * one-shot whose teardown is still pending projects inactive - the base is the
 * retained total and the armed count stays visible beside it
 * (`5 watches · 2 armed`, `5 watches · 2 armed · +3 more`), because the
 * summary's number must match the fold-out that shows all of them.
 *
 * `armed` is the session's TRUE armed total (activeWatchCount), so it already
 * includes armed rows the hub omitted. Whenever rows were omitted the figure is
 * labelled `N armed total`, because the retained rows alone cannot say what
 * covers the hidden ones - and the label must never understate the session's
 * armed watches. */
export function watchCountLabel(armed: number, retained: number, omitted: number): string {
  if (omitted > 0) {
    // The byte fitter can shed every retained row, and a leading "0 watches"
    // would then contradict the totals beside it: the session does hold watches,
    // the hub just could not fit a single row. Dropping the base count there
    // matches what the panel's own watch header says in the same case.
    const total = `${armed} armed total · +${omitted} more`;
    return retained === 0 ? total : `${retained} watch${retained === 1 ? "" : "es"} · ${total}`;
  }
  const retainedLabel = `${retained} watch${retained === 1 ? "" : "es"}`;
  return retained === armed ? retainedLabel : `${retainedLabel} · ${armed} armed`;
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
function splitChildren(parent: RailSession, isExpanded: IsExpanded): SessionRailNode["children"] {
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
  const children: SessionRailNode["children"] = [
    ...current,
    ...(parent.running_jobs ?? []).map((job) => activeJobNode(parent, job)),
  ];
  // The session's own live watches, after running work: an armed watch is
  // pending, not happening, so it reads below the things currently running.
  // Every row the wire sent stays reachable - the inline cap only hides the
  // tail behind a count, it never drops it. The count also carries the rows the
  // hub projector omitted (over the per-session cap, or shed by its byte
  // fitter), which the summary line already reports as "+N more": a fold-out
  // that ignored them would contradict the row it hangs under.
  const watches = parent.watches ?? [];
  const inlineWatches = watches.slice(0, MAX_INLINE_WATCHES);
  children.push(...inlineWatches.map((watch) => toWatchNode(parent, watch)));
  const hiddenWatches = watches.length - inlineWatches.length + (parent.omitted_watches ?? 0);
  if (hiddenWatches > 0) {
    children.push(
      // The cap is local to the rail: the wire already carried every retained
      // watch, and the omitted rows the hub dropped have no page to fetch
      // either. The row is an honest count, not a control.
      ...overflowNode(watchOverflowId(parent.row_id), hiddenWatches, [], "more watches", true),
    );
  }
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

function toSessionNode(n: RailSession, isExpanded: IsExpanded, crossProjectTier = false): SessionRailNode {
  const expanded = isExpanded(n.row_id, false);
  const children = splitChildren(n, isExpanded);
  const cache = crossProjectTier ? crossProjectSessionNodeCache : sessionNodeCache;
  const cached = cache.get(n as object)?.get(isExpanded);
  if (cached && cached.expanded === expanded && cached.children === children) return cached.value;
  const result: SessionRailNode = crossProjectTier
    ? { id: n.row_id, kind: "session", session: n, expanded, children, crossProjectTier: true }
    : { id: n.row_id, kind: "session", session: n, expanded, children };
  let entries = cache.get(n as object);
  if (!entries) {
    entries = new WeakMap();
    cache.set(n as object, entries);
  }
  entries.set(isExpanded, { children, expanded, value: result });
  return result;
}

/** Builds rail nodes for a flat, childless-at-this-level session list - the
 * Needs-you, Live, and Pinned tiers, each of which is just TreeNode[] on
 * the wire. A session can still recurse into its own children (subagent
 * clusters), handled by toSessionNode regardless of which tier it's in.
 * Every row this returns is the root of a cross-project tier, so each one
 * carries the crossProjectTier mark - host grouping nests these rows under
 * subheaders, and the mark is how RailRow keeps telling a tier root from a
 * nested row once depth stops doing it. */
export function sessionNodes(nodes: readonly RailSession[], isExpanded: IsExpanded): SessionRailNode[] {
  return nodes.map((n) => toSessionNode(n, isExpanded, true));
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

/** Builds rail nodes for the Projects and Test-runs tiers: both are
 * TreeProject[] on the wire, both ship their sessions inline (no lazy
 * load - only archived-project stubs omit sessions; see
 * cmd/evener-hub/web_api_tree.go's apiTreeProject doc comment), so both use
 * this same builder. Sessions sort needs-you-first (vbh8, §2.2) - a stable
 * partition (Array.prototype.sort is stable in the target engines), so
 * sessions that don't need you keep their incoming relative order.
 *
 * With sources the rows name their launch host - the test-runs tier does
 * this, so a remote-owned project's "+" cannot fall back to this hub
 * whatever the grouping. Without them the row stays hostless, the flat
 * Projects tier's shape, where no remote source exists. */
export function projectNodes(
  projects: readonly RailProject[],
  isExpanded: IsExpanded,
  sources?: readonly Source[],
): ProjectRailNode[] {
  return projectNodesWith(
    projects,
    isExpanded,
    sources ? `active:${sourcesSignature(sources)}` : "active",
    (p, id) => activeChildren(p, id, isExpanded),
    sources ? (p) => projectLaunchHost(p, sources) : undefined,
    sources !== undefined,
  );
}

/** The shared shape of the flat and project-first builders: one cached node
 * per (project, isExpanded, variant), so switching the rail's grouping keeps
 * each variant's rows referentially stable. `childrenFor` is the single place
 * a variant differs. Host-first's per-host copies (hostProjectCopyNode) do
 * the same cache dance by hand because both their ids and their cache
 * variants vary per host. */
function projectNodesWith(
  projects: readonly RailProject[],
  isExpanded: IsExpanded,
  variant: string,
  childrenFor: (p: RailProject, id: string) => RailNode[],
  spawnHostFor?: (p: RailProject) => string | undefined,
  canonicalRow = false,
): ProjectRailNode[] {
  const labels = projectDisplayLabels(projects);
  return projects.map((p) => {
    const id = projectNodeExpansionKey(p.key);
    const expanded = isExpanded(id, p.default_expanded ?? false);
    const children = projectChildren(p, isExpanded, variant, () => childrenFor(p, id));
    const spawnHost = spawnHostFor?.(p);
    return cachedProjectNode(p, isExpanded, variant, {
      id,
      displayName: labels.get(p.key),
      expanded,
      children,
      ...(spawnHost === undefined ? {} : { spawnHost }),
      ...(canonicalRow ? { canonicalCopy: true } : {}),
    });
  });
}

/** The cached-node dance every project row does: identity is keyed by
 * (project, isExpanded, variant) so unchanged rows keep their object across
 * renders (RailRow memoizes on node identity) while a changed expansion or
 * children list produces a fresh node. */
function cachedProjectNode(
  p: RailProject,
  isExpanded: IsExpanded,
  variant: string,
  fields: {
    id: string;
    displayName: string | undefined;
    expanded: boolean;
    children: RailNode[];
    spawnHost?: string;
    canonicalCopy?: boolean;
  },
): ProjectRailNode {
  const cached = projectNodeCache
    .get(p as object)
    ?.get(isExpanded)
    ?.get(variant);
  if (
    cached &&
    cached.children === fields.children &&
    cached.displayName === fields.displayName &&
    cached.expanded === fields.expanded &&
    cached.spawnHost === fields.spawnHost &&
    cached.canonicalCopy === fields.canonicalCopy
  )
    return cached.value;
  const result: ProjectRailNode = {
    id: fields.id,
    kind: "project",
    project: p,
    resourceError: p.resourceError,
    displayName: fields.displayName,
    expanded: fields.expanded,
    children: fields.children,
    ...(fields.spawnHost === undefined ? {} : { spawnHost: fields.spawnHost }),
    ...(fields.canonicalCopy === true ? { canonicalCopy: true } : {}),
  };
  cacheProjectNode(p, isExpanded, variant, { ...fields, value: result });
  return result;
}

/** True while a project promises rows (session_count > 0) but has shipped
 * none and no page fetch has marked it loaded: its tier renders the loading
 * placeholder rather than an empty branch. */
function projectIsLoading(p: RailProject): boolean {
  return p.sessions.length === 0 && p.loaded !== true && (p.session_count ?? 0) > 0;
}

/** A project's active-tier session rows in the needs-you-first order every
 * tier uses. `hostId` narrows to one host's rows for the grouped variants;
 * the filter copies before sorting, so the sort never touches the
 * project's own list. */
function activeSessionNodes(p: RailProject, isExpanded: IsExpanded, hostId?: string): SessionRailNode[] {
  return p.sessions
    .filter((n) => !isArchivedTier(n) && (hostId === undefined || sessionGroupHostId(n) === hostId))
    .sort((a, b) => Number(sessionWantsYou(b)) - Number(sessionWantsYou(a)))
    .map((n) => toSessionNode(n, isExpanded));
}

/** The envelope every active-tier children list shares: the loading
 * placeholder while a project's rows have not loaded, else the variant's
 * own rows followed by the overflow that can reveal more. `id` is the
 * caller's own node id, so a grouped copy's placeholder and overflow rows
 * re-id under the copy and two copies of one project never share a node id.
 * `tiers` picks which hidden-row counts the overflow reports; an empty list
 * renders no overflow at all (a host copy that is not its project's
 * overflow carrier, see hostProjectNodes). */
function activeTierChildren(
  p: RailProject,
  id: string,
  rowsFor: () => RailNode[],
  tiers: readonly TreeTier[] = ["current", "recent"],
): RailNode[] {
  if (projectIsLoading(p)) return [{ id: `${id}:loading`, kind: "loading" as const }];
  return [...rowsFor(), ...projectOverflowNode(id, p, tiers)];
}

/** One project's active-tier session rows (optionally one host's) in the
 * shared envelope above. */
function activeChildren(p: RailProject, id: string, isExpanded: IsExpanded, hostId?: string): RailNode[] {
  return activeTierChildren(p, id, () => activeSessionNodes(p, isExpanded, hostId));
}

// Host grouping - the rail's organize-by setting. Three more projections of
// data Rail.tsx already holds: a project's owning `sources`, a session's
// `host_id`, and the manifest's Source rows themselves (label, online). No
// fetching, no new wire fields.

/** The rail's current grouping shape: the pref's two modes, or "flat" while
 * the manifest lists no remote source (today's rail, whatever the pref
 * says). */
export type RailGroupingMode = "flat" | "host-project" | "project-host";

/** The host-grouped id grammar, in one place: the same project renders under
 * ids its flat mode never uses, and the reveal path (revealExpansionIds) has
 * to compute exactly these to reach a row through the grouped shapes. */
export function hostGroupId(hostId: string): string {
  return `host:${hostId}`;
}

export function liveHostGroupId(hostId: string): string {
  return `livehost:${hostId}`;
}

function hostProjectCopyId(projectId: string, hostId: string): string {
  return `${projectId}@${hostId}`;
}

function hostBranchId(projectId: string, hostId: string): string {
  return `${projectId}@host:${hostId}`;
}

/** The manifest's id→Source lookup, single-slot memoized on the sources
 * array's identity: the display selector hands back the manifest's own
 * array, so every grouped builder call between manifest updates shares one
 * Map instead of building one per call. */
const sourceLookupCache: { sources: readonly Source[] | null; known: Map<string, Source> } = {
  sources: null,
  known: new Map(),
};

function sourceLookup(sources: readonly Source[]): Map<string, Source> {
  if (sourceLookupCache.sources !== sources) {
    sourceLookupCache.sources = sources;
    sourceLookupCache.known = new Map(sources.map((source): [string, Source] => [source.id, source]));
  }
  return sourceLookupCache.known;
}

type HostFacts = { id: string; label: string; online: boolean };

/** Hosts in rail order: this hub first, then online hosts by their display
 * labels (the id breaking ties), offline hosts last (an offline host cannot
 * reveal rows until it reconnects, so it sorts behind the hosts that can). A
 * host the manifest does not name reads as ONLINE - the same unknown-host
 * contract the session rows' own chips follow (RailRow's useHostOnline) -
 * and falls back to its id as a label. */
function orderedHosts(hostIds: Iterable<string>, sources: readonly Source[]): HostFacts[] {
  return [...new Set(hostIds)]
    .map((id) => {
      const source = sourceLookup(sources).get(id);
      return {
        id,
        label: source?.label ?? id,
        online: source ? source.online : true,
        tier: id === LOCAL_HOST ? 0 : source ? (source.online ? 1 : 2) : 1,
      };
    })
    .sort((a, b) => a.tier - b.tier || a.label.localeCompare(b.label) || a.id.localeCompare(b.id))
    .map(({ id, label, online }) => ({ id, label, online }));
}

/** The host a row groups under. A CLUSTER row's own host_id is synthetic -
 * "cluster", the scope prefix of its id, because the hub names no host for a
 * row it folded out of repeated titles (navigationNodeRef falls back to the
 * node ID, so the wire carries "cluster:<hex>"). It groups under its
 * most-recent member's host, the member the cluster itself carries recency
 * from; a memberless cluster (the hub never builds one) falls back to this
 * hub so the row still renders somewhere. */
function sessionGroupHostId(n: RailSession): string {
  if (n.kind !== "cluster") return n.host_id;
  return n.children[0]?.host_id ?? LOCAL_HOST;
}

/** Every host a project's rows can appear under: its owning sources plus any
 * host its loaded sessions name (a project whose summary predates a host
 * still lands where its rows are). A project naming neither is this hub's
 * own. Memoized per project: the host-first top level asks about every
 * project on every render, and project objects keep their identity between
 * data changes. */
const projectHostIdsCache = new WeakMap<
  RailProject,
  { sources: readonly string[] | undefined; sessions: readonly RailSession[]; hosts: string[] }
>();

function projectHostIds(p: RailProject): string[] {
  const cached = projectHostIdsCache.get(p);
  if (cached && cached.sources === p.sources && cached.sessions === p.sessions) return cached.hosts;
  const hosts = new Set<string>(p.sources ?? []);
  for (const n of p.sessions) hosts.add(sessionGroupHostId(n));
  if (hosts.size === 0) hosts.add(LOCAL_HOST);
  const result = [...hosts];
  projectHostIdsCache.set(p, { sources: p.sources, sessions: p.sessions, hosts: result });
  return result;
}

/** Host group nodes keyed by their own id, one slot per host: the children
 * are themselves identity-cached (project copies, session nodes), so an
 * element-wise compare lets an unchanged group keep its object across
 * renders (RailRow memoizes on node identity) while any real change - a new
 * row, a toggle, an online flip - produces a fresh node. */
type HostNodeCacheEntry = Readonly<{
  host: HostRailNode["host"];
  expanded: boolean;
  children: RailNode[];
  value: HostRailNode;
}>;

const hostNodeCache = new Map<string, HostNodeCacheEntry>();

function cachedHostNode(id: string, host: HostRailNode["host"], expanded: boolean, children: RailNode[]): HostRailNode {
  const cached = hostNodeCache.get(id);
  if (
    cached &&
    cached.host.id === host.id &&
    cached.host.label === host.label &&
    cached.host.online === host.online &&
    cached.expanded === expanded &&
    cached.children.length === children.length &&
    cached.children.every((child, index) => child === children[index])
  )
    return cached.value;
  const value: HostRailNode = { id, kind: "host", host, expanded, children };
  hostNodeCache.set(id, { host, expanded, children, value });
  return value;
}

/** "Host, then project": one top-level host group per host in play (a host
 * owning nothing renders nothing), each holding a copy of every project
 * owned on that host. A copy is never dropped for having no loaded rows:
 * the project's hidden rows can still be that host's, and revealed rows
 * land under the host they name. The project's overflow row renders once,
 * on the first copy in rail order that has loaded rows (the canonical copy
 * below), so the project-wide count does not claim "+N" under every host. Copy ids suffix
 * the host so two copies of one project never share expand state or
 * overflow row ids. */
export function hostProjectNodes(
  projects: readonly RailProject[],
  sources: readonly Source[],
  isExpanded: IsExpanded,
): HostRailNode[] {
  const labels = projectDisplayLabels(projects);
  const projectsByHost = new Map<string, RailProject[]>();
  // The host whose copy carries the project-level overflow: the FIRST in
  // the rail order the copies render in that has loaded active rows. The
  // overflow's count and pages are the project's own, so it must read once
  // instead of claiming "+N older" under every host that owns the project -
  // and anchoring it to a host with no rows would park it inside an empty
  // group, where collapsing the group hides the project's only "+N older".
  // A project no loaded row names yet keeps the first ordered host, so the
  // anchor cannot flip copy-to-copy while rows stream in.
  const overflowHost = new Map<RailProject, string>();
  for (const p of projects) {
    const ordered = orderedHosts(projectHostIds(p), sources);
    const withRows = ordered.find(({ id }) =>
      p.sessions.some((n) => !isArchivedTier(n) && sessionGroupHostId(n) === id),
    );
    overflowHost.set(p, (withRows ?? ordered[0])?.id ?? LOCAL_HOST);
    for (const hostId of projectHostIds(p)) {
      const owned = projectsByHost.get(hostId) ?? [];
      owned.push(p);
      projectsByHost.set(hostId, owned);
    }
  }
  return orderedHosts(projectsByHost.keys(), sources).map(({ id: hostId, label, online }): HostRailNode => {
    const id = hostGroupId(hostId);
    return cachedHostNode(
      id,
      { id: hostId, label, online },
      isExpanded(id, true),
      (projectsByHost.get(hostId) ?? []).map((p) =>
        hostProjectCopyNode(p, hostId, labels.get(p.key), isExpanded, overflowHost.get(p) === hostId),
      ),
    );
  });
}

/** One project's copy under one host (see hostProjectNodes).
 * `carryProjectOverflow` names the one copy that renders the project-level
 * overflow; the rest keep only their own rows and the loading placeholder. */
function hostProjectCopyNode(
  p: RailProject,
  hostId: string,
  displayName: string | undefined,
  isExpanded: IsExpanded,
  carryProjectOverflow: boolean,
): ProjectRailNode {
  // The carry decision rides the variant: it is derived from the live host
  // order (see hostProjectNodes), so the same copy must not reuse children
  // cached under the other decision when that order changes.
  const variant = `host:${hostId}:${carryProjectOverflow ? "overflow" : "rows"}`;
  const id = hostProjectCopyId(projectNodeExpansionKey(p.key), hostId);
  const expanded = isExpanded(id, p.default_expanded ?? false);
  const children = projectChildren(p, isExpanded, variant, () =>
    carryProjectOverflow
      ? activeChildren(p, id, isExpanded, hostId)
      : activeTierChildren(p, id, () => activeSessionNodes(p, isExpanded, hostId), []),
  );
  return cachedProjectNode(p, isExpanded, variant, {
    id,
    displayName,
    expanded,
    children,
    spawnHost: hostId,
    canonicalCopy: carryProjectOverflow,
  });
}

/** "Project, then host": project rows stay as they are today, but a loaded
 * project's sessions group under per-host branches inside it. A branch with
 * no loaded rows does not render: the project's own overflow row still sits
 * at the project level and can reveal those rows, so there is no second
 * copy guarding them (that is host-first's job). An unloaded project keeps
 * its loading placeholder. */
export function projectNodesWithHostBranches(
  projects: readonly RailProject[],
  sources: readonly Source[],
  isExpanded: IsExpanded,
): ProjectRailNode[] {
  return projectNodesWith(
    projects,
    isExpanded,
    `host-branches:${sourcesSignature(sources)}`,
    (p, id) => activeTierChildren(p, id, () => hostBranchNodes(p, id, sources, isExpanded)),
    (p) => projectLaunchHost(p, sources),
    // The project row is the project's ONE aggregate row in this mode -
    // rollup and overflow still read here, whatever rows its branches hold.
    true,
  );
}

/** The host a project row's launch targets when no copy names one: the first
 * host in rail order among the project's owners (this hub orders first).
 * Naming it keeps a remote-owned working_dir from silently launching on this
 * hub through the draft's remembered source, and lets useHostLaunchable hide
 * the affordance while that host is offline - the same contract the
 * host-first copies already follow. Flat mode claims no host on purpose: no
 * remote source exists there, so every project is this hub's own. */
function projectLaunchHost(p: RailProject, sources: readonly Source[]): string | undefined {
  return orderedHosts(projectHostIds(p), sources)[0]?.id;
}

// A manifest update swaps the sources ARRAY identity while the project
// objects keep theirs, and the branches embed host facts (label, online)
// read from that array - so the children cache must key on the facts too,
// or the branches keep stale facts until the project object itself changes.
// The signature is exactly the content the branches embed (ids, labels,
// online flags, in array order - which orders the branches): an unchanged
// revalidation reuses the built children, a change mints a fresh variant.
// Content-keyed, not identity-keyed - identity would grow a new cache
// entry per revalidation with nothing ever evicting the old ones.
function sourcesSignature(sources: readonly Source[]): string {
  return JSON.stringify(sources.map((source) => [source.id, source.label, source.online]));
}

/** The per-host branches inside one loaded project (see
 * projectNodesWithHostBranches), in the host order every grouped tier uses.
 * A branch holds only that host's loaded rows and starts collapsed; its
 * identity rides the project's cached children, so it needs no cache of its
 * own. Branches render only while the loaded rows themselves span hosts -
 * the same line liveNodesGroupedByHost draws: a project whose rows sit on
 * one host keeps today's flat children, so a lone "this host" branch cannot
 * bury every session one expansion deeper for no grouping gained. */
function hostBranchNodes(
  p: RailProject,
  projectId: string,
  sources: readonly Source[],
  isExpanded: IsExpanded,
): RailNode[] {
  const branches = orderedHosts(projectHostIds(p), sources).flatMap(({ id: hostId, label, online }): HostRailNode[] => {
    const rows = activeSessionNodes(p, isExpanded, hostId);
    if (rows.length === 0) return [];
    const id = hostBranchId(projectId, hostId);
    return [{ id, kind: "host", host: { id: hostId, label, online }, expanded: isExpanded(id, false), children: rows }];
  });
  if (branches.length <= 1) return activeSessionNodes(p, isExpanded);
  return branches;
}

/** The Live tier's flat rows, grouped under host subheaders whenever they
 * span more than one host; a single host (or none) keeps today's flat list,
 * byte for byte. Like every host group these default expanded: collapsing
 * them is the rare move. */
export function liveNodesGroupedByHost(
  nodes: SessionRailNode[],
  sources: readonly Source[],
  isExpanded: IsExpanded,
): RailNode[] {
  const hostIds = new Set(nodes.map((n) => sessionGroupHostId(n.session)));
  if (hostIds.size <= 1) return nodes;
  return orderedHosts(hostIds, sources).map(({ id: hostId, label, online }): HostRailNode => {
    const id = liveHostGroupId(hostId);
    return cachedHostNode(
      id,
      { id: hostId, label, online },
      isExpanded(id, true),
      nodes.filter((n) => sessionGroupHostId(n.session) === hostId),
    );
  });
}

/** The top-level row that visually owns `ref`: itself when `ref` is
 * top-level, the ancestor it nests under otherwise. A grouped branch holds
 * the TOP-LEVEL row's host - a subagent renders under its parent's row
 * wherever that row landed, never under its own host's group. (Distinct from
 * topLevelAncestorRef's "opens beside" carrier, which skips CLUSTER rows; a
 * cluster is still the row its children visibly nest under.) */
function topLevelCarrier(nodes: readonly RailSession[], ref: string): RailSession | null {
  for (const n of nodes) {
    if (n.ref === ref || sessionListHasRef(n.children, ref)) return n;
  }
  return null;
}

/** The session rows and folds between a nested target and its top-level
 * carrier: each ancestor session's row id, plus the inactive-subagents fold
 * in front of a settled one (splitChildren folds settled children behind
 * it). A CLUSTER carrier needs only its own row - splitChildren renders its
 * members inline - and a top-level target has no ancestors at all, so the
 * chain for one is empty. */
function revealAncestorIds(session: RailSession, ref: string): string[] {
  for (const child of session.children) {
    if (child.ref === ref) {
      if (session.kind === "cluster") return [session.row_id];
      return subagentIsCurrent(child) ? [session.row_id] : [session.row_id, inactiveFoldId(session.row_id)];
    }
    const deeper = child.children.length > 0 ? revealAncestorIds(child, ref) : [];
    if (deeper.length > 0) {
      const fold = session.kind !== "cluster" && !subagentIsCurrent(child) ? [inactiveFoldId(session.row_id)] : [];
      return [session.row_id, ...fold, ...deeper];
    }
  }
  return [];
}

/** The expansion chain, outermost first, a deep-link reveal must walk to
 * expose the row `ref` renders at: the project's own node in flat mode, the
 * owning host group then the project's copy in "Host, then project", the
 * project then its per-host branch in "Project, then host", and a Live host
 * subheader for a live row whenever Live groups. A nested target then names
 * every session row between it and its top-level carrier, plus the inactive
 * fold in front of each settled one - the carrier's own row is a fold too,
 * and a chain that skips it never renders the target. An archived-tier row
 * routes to its project's archived-group fold instead - the one tier no
 * grouping mode rewrites - unless `options.rowsUnderProjectNode` says these
 * projects render every row under the project's own node (whole-archived
 * projects do; see archivedProjectNodes). Empty when nothing loaded holds the ref
 * yet - the reveal's location-lookup path owns that case. Callers expand
 * one id per pass and re-run, so reaching the end of the chain means every
 * fold it needs is already open. */
export function revealExpansionIds(
  projects: readonly RailProject[],
  live: readonly RailSession[],
  ref: string,
  mode: RailGroupingMode,
  options?: { rowsUnderProjectNode?: boolean },
): string[] {
  for (const p of projects) {
    const carrier = topLevelCarrier(p.sessions, ref);
    if (!carrier) continue;
    // An archived-tier row renders in the Archived sessions section's
    // archived-group fold (archivedSessionGroups), never under the flat or
    // grouped project branch, whatever mode the rail is in - unless these
    // projects render every row under their own node (whole-archived
    // projects do; see archivedProjectNodes).
    const ancestors = revealAncestorIds(carrier, ref);
    if (!options?.rowsUnderProjectNode && isArchivedTier(carrier)) return [archivedGroupId(p.key), ...ancestors];
    const id = projectNodeExpansionKey(p.key);
    const carrierHost = sessionGroupHostId(carrier);
    if (mode === "host-project") return [hostGroupId(carrierHost), hostProjectCopyId(id, carrierHost), ...ancestors];
    if (mode === "project-host") {
      // Branches render only while the project's loaded rows span hosts
      // (hostBranchNodes draws the same line), so a single-host chain stops
      // at the project fold instead of naming a fold that does not exist.
      const rowsHosts = new Set(p.sessions.filter((n) => !isArchivedTier(n)).map(sessionGroupHostId));
      return rowsHosts.size > 1 ? [id, hostBranchId(id, carrierHost), ...ancestors] : [id, ...ancestors];
    }
    return [id, ...ancestors];
  }
  const carrier = topLevelCarrier(live, ref);
  if (!carrier) return [];
  // Flat mode renders Live ungrouped (Rail wraps it only while grouping),
  // so a subheader id would name a fold that does not exist - the
  // carrier rows still apply.
  const ancestors = revealAncestorIds(carrier, ref);
  if (mode !== "flat" && new Set(live.map(sessionGroupHostId)).size > 1)
    return [liveHostGroupId(sessionGroupHostId(carrier)), ...ancestors];
  return ancestors;
}

/** The expansion ids that mean "this project's rows are in view" under the
 * current grouping: the project's own node, plus - in host-first mode - one
 * id per host copy. Rail's lazy-load effect loads whichever project has any
 * of these expanded, so expanding a copy of an unloaded project fetches its
 * rows instead of sticking on the loading placeholder forever. */
export function projectLoadExpansionKeys(p: RailProject, mode: RailGroupingMode): string[] {
  const id = projectNodeExpansionKey(p.key);
  if (mode !== "host-project") return [id];
  return [id, ...projectHostIds(p).map((hostId) => hostProjectCopyId(id, hostId))];
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
export function archivedSessionGroups(
  projects: readonly RailProject[],
  isExpanded: IsExpanded,
  sources: readonly Source[],
): ProjectRailNode[] {
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
    // The launch host rides the node cache's key, so a manifest that
    // reorders or renames sources rebuilds the row instead of serving a
    // stale host (the same reason host-branches' variant carries a
    // sources signature).
    const variant = `archived-group:${sourcesSignature(sources)}`;
    const cached = projectNodeCache
      .get(p as object)
      ?.get(isExpanded)
      ?.get(variant);
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
      spawnHost: projectLaunchHost(p, sources),
      canonicalCopy: true,
    };
    cacheProjectNode(p, isExpanded, variant, { children, displayName, expanded, value: result });
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
  sources: readonly Source[],
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
    const variant = `archived-project:${sourcesSignature(sources)}`;
    const cached = projectNodeCache
      .get(p as object)
      ?.get(isExpanded)
      ?.get(variant);
    if (cached && cached.children === children && cached.displayName === displayName && cached.expanded === expanded)
      return cached.value;
    const result: ProjectRailNode = {
      id,
      kind: "project",
      project: p,
      displayName,
      expanded,
      children,
      spawnHost: projectLaunchHost(p, sources),
      canonicalCopy: true,
    };
    cacheProjectNode(p, isExpanded, variant, { children, displayName, expanded, value: result });
    return result;
  });
}
