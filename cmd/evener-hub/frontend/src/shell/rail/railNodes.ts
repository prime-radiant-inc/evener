// railNodes.ts is the pure tree-shaping layer between navigation resources
// data and the widgets/tree Tree widget: it decides row identity, the
// project/host grouping, and (given the caller's own expand-state map) each
// branch's `expanded` flag. No React, no fetching - Rail.tsx owns the state
// these functions are pure functions OF (the expand-override map, the
// lazily-loaded archived project detail map, the manifest's launch sources)
// and wires the results into <Tree>.
//
// Session rows are FLAT: nothing nests under a session anymore. A session's
// subagents live in the activity sidebar's Agents tab (the paged subagents
// resource), its jobs and watches ride the row's own summary figures, and
// the two counts the nested rows used to make derivable ship on the row:
// `subagents` (a whole-tree tally) and `needs_you_subagents`. The only branch
// rows left are projects and host groups.

import {
  approvalWaiting,
  type NavigationSessionSummary,
  type NavigationWatchSummary,
  type Source,
} from "@evener/appwire-client";
import {
  canonicalHostId,
  orderedHosts,
  projectHostIds as ownerHostIds,
  projectNodeExpansionKey,
} from "@evener/appwire-client/state/navigation";

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
  // Mirrors the wire field, but the rail's adapter (Rail's summarySession)
  // always leaves it empty: nested summaries never become rail rows.
  children: RailSession[];
  project_key?: string;
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
  /** The catalog the project was listed in; its archived list is keyed by it. */
  catalog?: "projects" | "archived_projects" | "test_runs";
  /** The navigation summary's archived session count. A loaded archived list
   * whose total differs is stale (rows were archived elsewhere) and refetches. */
  archived_total?: number;
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
  // Always an empty array: a session row is a leaf. The rail's rows no
  // longer carry nested session children, job rows, or watch rows - the
  // activity sidebar owns those - so the only thing the row expands into is
  // nothing. The field stays because the Tree widget reads it for its
  // leaf/branch call and an empty array is that call's "leaf" answer.
  children: RailNode[];
  // Set only on the ROOT rows of a flat cross-project tier - the ones
  // sessionNodes builds (Live, Needs-you, Pinned): those rows name their
  // project and suppress the pin star, wherever host grouping nests them.
  // RailRow reads this mark instead of nesting depth: host subheaders put
  // tier roots at depth 1, where project rows sit too, so depth no longer
  // separates the two shapes.
  crossProjectTier?: boolean;
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
  // (project-first, flat, test-runs, and the archived tiers). The
  // builder's no-sources call leaves it absent: that call is for
  // callers with no manifest to read.
  spawnHost?: string;
  // True on the ONE row that renders the project's aggregate facts - the
  // overflow row and the rollup signal/badge - so they read once instead of
  // claiming per-host counts the wire does not carry: every row that
  // renders a project's own shape (flat, project-first, test-runs,
  // the archived tiers), or the first rows-bearing copy in rail order in
  // host-first. A hostless row (the builder's no-sources call) still reads
  // its aggregate role from that absence.
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
 * carry the tier offsets needed to reveal those rows; a section's overflow
 * carries its page descriptor the same way. */
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

export type RailNode = SessionRailNode | ProjectRailNode | HostRailNode | LoadingRailNode | OverflowRailNode;

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
const sessionNodeCache = new WeakMap<object, SessionRailNode>();
// The crossProjectTier variant of sessionNodeCache, kept apart so one tier
// shape's node can never serve the other (see toSessionNode).
const crossProjectSessionNodeCache = new WeakMap<object, SessionRailNode>();
const projectChildrenCache = new WeakMap<object, WeakMap<IsExpanded, Map<string, RailNode[]>>>();
const projectNodeCache = new WeakMap<object, WeakMap<IsExpanded, Map<string, ProjectNodeCacheEntry>>>();

// The rows a given list has hidden. Each caller passes the tiers it actually
// renders: an active project's inline list shows Current+Recent (the archived
// tier is diverted out of it), the archived sub-branch shows only Archived,
// and a hydrated archived project shows all three.
function overflowNode(id: string, count: number, pages: OverflowPage[] = []): OverflowRailNode[] {
  return count > 0 ? [{ id: `${id}:overflow`, kind: "overflow", count, pages }] : [];
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
        ...(tier === "archived" && p.catalog ? { catalog: p.catalog } : {}),
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

/** How many of a session's OWN live watches are still armed. Deliberately not
 * a subtree rollup: the hub projects each watch onto exactly the summary of
 * the session that receives it (navigation_projection.go's navigationWatches),
 * so summing descendants here would print a subagent's watch on every ancestor
 * row as well as on the subagent's own - one watch, several counts. A receiver
 * watch belongs to the session whose summary carries it. */
export function activeWatchCount(node: NavigationSessionSummary): number {
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
 * the retained rows the activity sidebar's Watches tab lists plus the exact
 * number of rows the projector omitted (a session above its per-session cap,
 * or rows its byte fitter shed) as "+N more" - so the summary line and the
 * sidebar cannot disagree about how many watches the session holds.
 *
 * When every retained row is armed the base is the armed count, byte-identical
 * to the wording that shipped before the retained/inactive distinction existed
 * (`1 watch`, `2 watches · +1 more`). When a retained row is inactive - a fired
 * one-shot whose teardown is still pending projects inactive - the base is the
 * retained total and the armed count stays visible beside it
 * (`5 watches · 2 armed`, `5 watches · 2 armed · +3 more`), because the
 * summary's number must match the Watches tab that lists all of them.
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

// The states a subagent still has live work in, read through displayState
// (approval_pending displays as "awaiting"). The same set marks every level:
// a subagent is current when its own state is here, when it has a running
// job, or when any descendant is current by this same rule - so an idle
// subagent whose child awaits the user never folds as "inactive".
const CURRENT_SUBAGENT_STATES: ReadonlySet<string> = new Set([
  "active",
  "awaiting",
  "warning",
  "restartRequired",
  "notLoaded",
  // Not "done" in the fold's sense: a failed subagent needs the user (its
  // row paints danger), and warning - the less severe signal - is current.
  "errored",
]);

// The wire's children carry fork originals (kind "fork") beside subagents -
// the rail renders those as nested session rows, and the activity surfaces
// count and list agents only.
export function subagentChildrenOf(session: NavigationSessionSummary): NavigationSessionSummary[] {
  return (session.children ?? []).filter((child) => child.kind === "subagent");
}

// The activity sidebar's Agents tab splits its scope's children on this; the
// rail itself no longer does (its rows follow the wire's order below). Reads
// displayState, not the raw wire state: approval_pending displays as
// "awaiting" (the rail's needs-you badge counts it), so a blocked subagent
// is current here too, never folded as inactive about the same node.
export function subagentIsCurrent(child: NavigationSessionSummary): boolean {
  if (CURRENT_SUBAGENT_STATES.has(displayState(child))) return true;
  if ((child.running_jobs ?? []).length > 0) return true;
  return child.children.some(subagentIsCurrent);
}

// One session summary, one leaf row. The row's identity is the session
// object itself: an unchanged summary keeps its node across renders (RailRow
// memoizes on node identity), a fresh summary mints a fresh node. No
// expansion state: there is nothing under the row to expand into.
function toSessionNode(n: RailSession, crossProjectTier = false): SessionRailNode {
  const cache = crossProjectTier ? crossProjectSessionNodeCache : sessionNodeCache;
  const cached = cache.get(n as object);
  if (cached) return cached;
  const result: SessionRailNode = crossProjectTier
    ? { id: n.row_id, kind: "session", session: n, children: [], crossProjectTier: true }
    : { id: n.row_id, kind: "session", session: n, children: [] };
  cache.set(n as object, result);
  return result;
}

/** Builds rail nodes for a flat session list - the Needs-you, Live, and
 * Pinned tiers, each of which is just TreeNode[] on the wire.
 * Every row this returns is the root of a cross-project tier, so each one
 * carries the crossProjectTier mark - host grouping nests these rows under
 * subheaders, and the mark is how RailRow keeps telling a tier root from a
 * project-nested row. */
export function sessionNodes(nodes: readonly RailSession[]): SessionRailNode[] {
  return nodes.map((n) => toSessionNode(n, true));
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

// The state a row PRESENTS, which is not always the wire state. A row waiting
// on an approval presents as needs-you from any state but a failure: the
// escalation blocks its turn mid-tool, so the wire state stays "active"
// (approvalWaiting). Every per-node attention judgment
// (the row's dot and gloss in RailRow, the badge count, the working count and
// the sort below, the palette's needs-you dots) reads this one helper, so
// they can never disagree about the same row.
export function displayState(node: Pick<NavigationSessionSummary, "state" | "approval_pending">): string {
  if (approvalWaiting(node.state, node.approval_pending === true)) return "awaiting";
  return node.state;
}

export interface ActiveWorkSummary {
  runningJobs: number;
}

export function activeWorkSummary(node: RailSession): ActiveWorkSummary {
  let runningJobs = (node.running_jobs ?? []).length;
  for (const child of node.children) {
    runningJobs += activeWorkSummary(child).runningJobs;
  }
  return { runningJobs };
}

// A session "wants you" either directly (its own state) or transitively (a
// needs-you subagent, counted by the hub into needs_you_subagents - the flat
// lists' replacement for the children walk) - either way it should sort
// ahead of a quiet sibling within the same project.
function sessionWantsYou(n: RailSession): boolean {
  return stateNeedsYou(displayState(n)) || (n.needs_you_subagents ?? 0) > 0;
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

/** Builds rail nodes for the Projects and Test-runs tiers: both are
 * TreeProject[] on the wire, both ship their sessions inline (no lazy
 * load - only archived-project stubs omit sessions; see
 * cmd/evener-hub/web_api_tree.go's apiTreeProject doc comment), so both use
 * this same builder. Sessions sort needs-you-first (vbh8, §2.2) - a stable
 * partition (Array.prototype.sort is stable in the target engines), so
 * sessions that don't need you keep their incoming relative order.
 *
 * With sources the rows name their launch host, so a remote-owned
 * project's "+" cannot fall back to this hub
 * whatever the grouping, and a local project's launch always names this
 * hub (a spawn draft left on a remote host cannot survive the click) -
 * every tier in the rail passes its display sources. Without them the row
 * stays hostless - the builder's contract for callers with no manifest
 * to read. */
export function projectNodes(
  projects: readonly RailProject[],
  isExpanded: IsExpanded,
  sources?: readonly Source[],
): ProjectRailNode[] {
  return projectNodesWith(
    projects,
    isExpanded,
    sources ? `active:${sourcesSignature(sources)}` : "active",
    (p, id) => activeChildren(p, id),
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
function activeSessionNodes(p: RailProject, hostId?: string): SessionRailNode[] {
  return p.sessions
    .filter((n) => !isArchivedTier(n) && (hostId === undefined || n.host_id === hostId))
    .sort((a, b) => Number(sessionWantsYou(b)) - Number(sessionWantsYou(a)))
    .map((n) => toSessionNode(n));
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
function activeChildren(p: RailProject, id: string, hostId?: string): RailNode[] {
  return activeTierChildren(p, id, () => activeSessionNodes(p, hostId));
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
  const result = ownerHostIds(p.sources, p.sessions);
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
    overflowHost.set(
      p,
      canonicalHostId(
        projectHostIds(p),
        p.sessions.filter((n) => !isArchivedTier(n)),
        sources,
      ),
    );
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
      ? activeChildren(p, id, hostId)
      : activeTierChildren(p, id, () => activeSessionNodes(p, hostId), []),
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
 * host-first copies already follow. Flat mode resolves it too: the
 * call passes the display sources, so a local project names this hub
 * and a remote-owned one names its host. */
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
    const rows = activeSessionNodes(p, hostId);
    if (rows.length === 0) return [];
    const id = hostBranchId(projectId, hostId);
    return [{ id, kind: "host", host: { id: hostId, label, online }, expanded: isExpanded(id, false), children: rows }];
  });
  if (branches.length <= 1) return activeSessionNodes(p);
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
  const hostIds = new Set(nodes.map((n) => n.session.host_id));
  if (hostIds.size <= 1) return nodes;
  return orderedHosts(hostIds, sources).map(({ id: hostId, label, online }): HostRailNode => {
    const id = liveHostGroupId(hostId);
    return cachedHostNode(
      id,
      { id: hostId, label, online },
      isExpanded(id, true),
      nodes.filter((n) => n.session.host_id === hostId),
    );
  });
}

/** The expansion chain, outermost first, a deep-link reveal must walk to
 * expose the row `ref` renders at: the project's own node in flat mode, the
 * owning host group then the project's copy in "Host, then project", the
 * project then its per-host branch in "Project, then host", and a Live host
 * subheader for a live row whenever Live groups. Only TOP-LEVEL rows can be
 * found here - the lists are flat, so a ref that exists only as a nested
 * summary (a subagent, a fork original) names no rail row; its reveal
 * resolves through the location lookup, which names the top-level carrier
 * (top_level_ref) to land on. An archived-tier row
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
    const carrier = p.sessions.find((n) => n.ref === ref);
    if (!carrier) continue;
    // An archived-tier row renders in the Archived sessions section's
    // archived-group fold (archivedSessionGroups), never under the flat or
    // grouped project branch, whatever mode the rail is in - unless these
    // projects render every row under their own node (whole-archived
    // projects do; see archivedProjectNodes).
    if (!options?.rowsUnderProjectNode && isArchivedTier(carrier)) return [archivedGroupId(p.key)];
    const id = projectNodeExpansionKey(p.key);
    const carrierHost = carrier.host_id;
    if (mode === "host-project") return [hostGroupId(carrierHost), hostProjectCopyId(id, carrierHost)];
    if (mode === "project-host") {
      // Branches render only while the project's loaded rows span hosts
      // (hostBranchNodes draws the same line), so a single-host chain stops
      // at the project fold instead of naming a fold that does not exist.
      const rowsHosts = new Set(p.sessions.filter((n) => !isArchivedTier(n)).map((n) => n.host_id));
      return rowsHosts.size > 1 ? [id, hostBranchId(id, carrierHost)] : [id];
    }
    return [id];
  }
  const carrier = live.find((n) => n.ref === ref);
  if (!carrier) return [];
  // Flat mode renders Live ungrouped (Rail wraps it only while grouping),
  // so a subheader id would name a fold that does not exist.
  if (mode !== "flat" && new Set(live.map((n) => n.host_id)).size > 1) return [liveHostGroupId(carrier.host_id)];
  return [];
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
    const spawnHost = projectLaunchHost(p, sources);
    const children = projectChildren(p, isExpanded, "archived-group", () => [
      ...archived.map((n) => toSessionNode(n)),
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
    if (
      cached &&
      cached.children === children &&
      cached.displayName === displayName &&
      cached.expanded === expanded &&
      cached.spawnHost === spawnHost &&
      cached.canonicalCopy === true
    ) {
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
      spawnHost,
      canonicalCopy: true,
    };
    cacheProjectNode(p, isExpanded, variant, {
      children,
      displayName,
      expanded,
      spawnHost,
      canonicalCopy: true,
      value: result,
    });
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
        ...detail.sessions.map((n) => toSessionNode(n)),
        ...projectOverflowNode(id, detail, ["current", "recent", "archived"]),
      ]);
    } else if ((p.session_count ?? 0) > 0) {
      children = projectChildren(p, isExpanded, "archived-loading", () => [{ id: `${id}:loading`, kind: "loading" }]);
    } else {
      children = projectChildren(p, isExpanded, "archived-empty", () => []);
    }
    const displayName = labels.get(p.key);
    const expanded = isExpanded(id, false);
    const spawnHost = projectLaunchHost(p, sources);
    const variant = `archived-project:${sourcesSignature(sources)}`;
    const cached = projectNodeCache
      .get(p as object)
      ?.get(isExpanded)
      ?.get(variant);
    if (
      cached &&
      cached.children === children &&
      cached.displayName === displayName &&
      cached.expanded === expanded &&
      cached.spawnHost === spawnHost &&
      cached.canonicalCopy === true
    )
      return cached.value;
    const result: ProjectRailNode = {
      id,
      kind: "project",
      project: p,
      displayName,
      expanded,
      children,
      spawnHost,
      canonicalCopy: true,
    };
    cacheProjectNode(p, isExpanded, variant, {
      children,
      displayName,
      expanded,
      spawnHost,
      canonicalCopy: true,
      value: result,
    });
    return result;
  });
}
