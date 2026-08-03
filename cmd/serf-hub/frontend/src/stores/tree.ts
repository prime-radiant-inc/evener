// tree.ts owns the sidebar's data: a debounced-refetch mirror of GET
// /api/tree, plus the lazy per-project detail fetch archived-project rows
// need on first expand (their sessions ship as a stub - see
// cmd/serf-hub/web_api_tree.go's apiTreeProject doc comment). Every type
// below mirrors hubapi/types.go's wire shape FIELD-FOR-FIELD (same names,
// same snake_case json tags) rather than an idiomatically-camelCased TS
// shape - there is no codegen step for these REST types (unlike
// protocol/types.gen.ts, which covers only the appwire JSON-RPC catalog),
// and hubapi.AttentionSummary's own doc comment explains why: it's
// deliberately camelCase in Go specifically so "the JS layer applies one
// field-access path to either the REST baseline or the live notification" -
// reasoning that only makes sense if the frontend consumes wire JSON
// directly, with no renaming layer to make the two line up on its own.

import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import { errorText } from "../protocol/errors";
import type { AppwireClientLike } from "../protocol/testing/fakeClient";
import type { AnyNotification } from "../protocol/types.gen";
import { connectionStore } from "./connection";

export interface Source {
  id: string;
  label: string;
  kind: string;
  online: boolean;
}

export interface TreeNode {
  row_id: string;
  ref: string;
  host_id: string;
  session_id: string;
  title: string;
  project: string;
  state: string;
  kind: string;
  tier?: string;
  branch?: string;
  cluster_count?: number;
  favorite?: boolean;
  pin_section_id?: string;
  rename?: boolean;
  live: boolean;
  ask_pending?: boolean;
  // True for a session that has never run: no model response, no accepted
  // input. It rides beside `state` rather than inside it, because a dormant
  // session reports state "idle" - exactly what a session that ran and went
  // quiet reports. See hubcore.TreeNode.Dormant for why it is not a state.
  dormant?: boolean;
  updated_at?: string;
  age?: string;
  model?: string;
  // Normalized: the wire's `children,omitempty` (absent when there are none)
  // is always a real array here, never absent/undefined - see normalizeNode.
  children: TreeNode[];
  more_subagents?: number;
}

export interface TreeProject {
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
  // Mirrors the project-kind decision POST /api/favorite accepts
  // (kind:"project"); TreeNode's own favorite field above is the
  // session-kind counterpart. omitempty bool on the Go side (like
  // TreeNode.favorite), never a nullable array - absent/false collapse to
  // the same falsy value, so no normalize* handling is needed beyond this
  // optional typing.
  favorite?: boolean;
  session_count?: number;
  // Normalized: the wire's `sessions` (no omitempty - explicit JSON `null`
  // on an archived-project stub) is always a real array here - see
  // normalizeProject. An archived stub normalizes to [] with session_count
  // still carrying the real row count; the rail's own lazy-load (
  // loadProjectDetail) is what turns that into real rows on first expand.
  sessions: TreeNode[];
}

export interface PinSectionSummary {
  id: string;
  name: string;
  member_count: number;
}

export interface PinSectionTree {
  id: string;
  name: string;
  sessions: TreeNode[];
}

export type TreeTier = "current" | "recent" | "archived";

export interface TreeProjectPage {
  key: string;
  tier: TreeTier;
  offset: number;
  sessions: TreeNode[];
  remaining: number;
}

export interface AttentionSummary {
  needsYou: number;
  error: number;
  working: number;
}

export interface TreeResponse {
  generated_at: string;
  sources: Source[];
  live: TreeNode[];
  needs_you: TreeNode[];
  pin_sections: PinSectionTree[];
  projects: TreeProject[];
  archived_projects: TreeProject[];
  test_runs: TreeProject[];
  attentionSummary: AttentionSummary;
}

// --- wire shapes: identical, except every Go-nullable array is honestly
// typed `T[] | null` instead of glossed over. Never exported - normalize*
// below is the one seam that turns these into the always-an-array types
// above, so nothing outside this module ever has to think about `?? []`.
interface WireTreeNode extends Omit<TreeNode, "children"> {
  children?: WireTreeNode[] | null;
}
interface WireTreeProject extends Omit<TreeProject, "sessions"> {
  sessions: WireTreeNode[] | null;
}
interface WirePinSectionTree extends Omit<PinSectionTree, "sessions"> {
  sessions: WireTreeNode[] | null;
}
interface WireTreeProjectPage extends Omit<TreeProjectPage, "sessions"> {
  sessions: WireTreeNode[] | null;
}
interface WireTreeResponse
  extends Omit<
    TreeResponse,
    "sources" | "live" | "needs_you" | "pin_sections" | "projects" | "archived_projects" | "test_runs"
  > {
  sources: Source[] | null;
  live: WireTreeNode[] | null;
  needs_you: WireTreeNode[] | null;
  pin_sections: WirePinSectionTree[] | null;
  projects: WireTreeProject[] | null;
  archived_projects: WireTreeProject[] | null;
  test_runs: WireTreeProject[] | null;
}

function normalizeNode(n: WireTreeNode): TreeNode {
  return { ...n, children: (n.children ?? []).map(normalizeNode) };
}

function normalizeProject(p: WireTreeProject): TreeProject {
  return { ...p, sessions: (p.sessions ?? []).map(normalizeNode) };
}

function normalizePinSection(section: WirePinSectionTree): PinSectionTree {
  return { ...section, sessions: (section.sessions ?? []).map(normalizeNode) };
}

function normalizeProjectPage(p: WireTreeProjectPage): TreeProjectPage {
  return { ...p, sessions: (p.sessions ?? []).map(normalizeNode) };
}

function normalizeResponse(r: WireTreeResponse): TreeResponse {
  return {
    ...r,
    sources: r.sources ?? [],
    live: (r.live ?? []).map(normalizeNode),
    needs_you: (r.needs_you ?? []).map(normalizeNode),
    pin_sections: (r.pin_sections ?? []).map(normalizePinSection),
    projects: (r.projects ?? []).map(normalizeProject),
    archived_projects: (r.archived_projects ?? []).map(normalizeProject),
    test_runs: (r.test_runs ?? []).map(normalizeProject),
  };
}

// Same-origin fetch already carries the session cookie hubedge.AuthGuard
// checks (fetch's own default credentials mode is "same-origin"; passed
// explicitly here so that stays true regardless of any future fetch spec
// default change) - this app has no separate Bearer-token scheme (see
// cmd/serf-hub/web.go: the auth guard is cookie-only, minted via GET
// /auth?token=).
const FETCH_INIT: RequestInit = { credentials: "same-origin" };

async function parseErrorBody(res: Response): Promise<string> {
  try {
    const data = (await res.json()) as { error?: string };
    if (typeof data.error === "string" && data.error !== "") return data.error;
  } catch {
    // non-JSON (or empty) error body: fall through to the status line
  }
  return `${res.status} ${res.statusText}`;
}

async function fetchTree(): Promise<TreeResponse> {
  const res = await fetch("/api/tree", FETCH_INIT);
  if (!res.ok) throw new Error(await parseErrorBody(res));
  return normalizeResponse((await res.json()) as WireTreeResponse);
}

async function fetchProjectDetail(key: string): Promise<TreeProject> {
  const res = await fetch(`/api/tree/project?key=${encodeURIComponent(key)}`, FETCH_INIT);
  if (!res.ok) throw new Error(await parseErrorBody(res));
  return normalizeProject((await res.json()) as WireTreeProject);
}

async function fetchProjectPage(key: string, tier: TreeTier, offset: number, limit: number): Promise<TreeProjectPage> {
  const res = await fetch(
    `/api/tree/project?key=${encodeURIComponent(key)}&tier=${tier}&offset=${offset}&limit=${limit}`,
    FETCH_INIT,
  );
  if (!res.ok) throw new Error(await parseErrorBody(res));
  return normalizeProjectPage((await res.json()) as WireTreeProjectPage);
}

const TREE_TIERS: readonly TreeTier[] = ["current", "recent", "archived"];

function tierField(tier: TreeTier): "more_current" | "more_recent" | "more_archived" {
  switch (tier) {
    case "current":
      return "more_current";
    case "recent":
      return "more_recent";
    case "archived":
      return "more_archived";
  }
}

/** Merges a page at its tier offset while retaining the server's tier order. */
export function mergeProjectPage(project: TreeProject, page: TreeProjectPage): TreeProject {
  const tierRows = project.sessions.filter((n) => n.tier === page.tier);
  const nextTierRows = [...tierRows];
  for (const [index, row] of page.sessions.entries()) {
    const existing = nextTierRows.findIndex((n) => n.row_id === row.row_id);
    if (existing >= 0) {
      nextTierRows[existing] = row;
    } else {
      nextTierRows.splice(Math.min(page.offset + index, nextTierRows.length), 0, row);
    }
  }

  const byTier = new Map<TreeTier, TreeNode[]>();
  byTier.set(page.tier, nextTierRows);
  for (const tier of TREE_TIERS) {
    if (!byTier.has(tier))
      byTier.set(
        tier,
        project.sessions.filter((n) => n.tier === tier),
      );
  }
  const currentRows = byTier.get("current") ?? [];
  const recentRows = byTier.get("recent") ?? [];
  const archivedRows = byTier.get("archived") ?? [];
  const untyped = project.sessions.filter((n) => !TREE_TIERS.includes(n.tier as TreeTier));
  return {
    ...project,
    sessions: [...currentRows, ...recentRows, ...archivedRows, ...untyped],
    [tierField(page.tier)]: page.remaining,
  };
}

function mergeProjectInList(projects: TreeProject[], page: TreeProjectPage): TreeProject[] {
  return projects.map((project) => (project.key === page.key ? mergeProjectPage(project, page) : project));
}

export interface TreeStoreState {
  // null until the first successful load; a failed refresh leaves whatever
  // was last successfully loaded in place rather than blanking it (a
  // transient fetch error must not flash the whole sidebar to empty).
  tree: TreeResponse | null;
  // Identity of the last authoritative tree accepted by refresh(). Cached
  // project detail is tagged with this generation so a later refresh cannot
  // make an old detail row look currently rendered merely because its project
  // key still exists.
  treeGeneration: number;
  loading: boolean;
  error: string | null;
  // Lazily-hydrated archived-project detail (real `sessions`, not the
  // session_count-only stub /api/tree ships), keyed by TreeProject.key.
  // Never cleared on refresh() - a project's key is stable identity, and a
  // fresh /api/tree stub is worse than what's already loaded, not better.
  projectDetails: Map<string, TreeProject>;
  projectDetailGenerations: Map<string, number>;
  // true means this call's response became authoritative; false means the
  // request failed or was superseded by a newer refresh. ALWAYS issues its
  // own request - see inflightRefresh below for why it never joins one.
  refresh(): Promise<boolean>;
  // "Make sure a tree exists", for the mount-time callers that want data
  // present rather than data re-fetched: resolves straight away when one is
  // already loaded, joins an in-flight refresh when there is one, and
  // otherwise starts a refresh. Two callers run on every desktop boot -
  // initNotifications()'s baseline (AppShell's module evaluation, so it runs
  // on every host, including the mobile one where no rail ever mounts) and
  // the rail's own mount effect - and this is what collapses them into ONE
  // GET /api/tree instead of two identical ones milliseconds apart (kata
  // p5w9). A FAILED load is not cached: the next caller gets a real retry,
  // the same rule settingsOverview.ts's own fetch() follows.
  ensureLoaded(): Promise<boolean>;
  loadProjectDetail(key: string): Promise<void>;
  loadProjectPage(key: string, tier: TreeTier, offset: number, limit: number): Promise<void>;
  reconcileProjectDelete(key: string, deletedIDs: string[], skippedIDs: string[]): void;
}

let refreshGeneration = 0;
const projectMutationGenerations = new Map<string, number>();
const projectDetailsInFlight = new Map<string, Promise<void>>();

// The refresh() currently in flight, if any - what ensureLoaded() joins
// instead of issuing a second identical GET. refresh() itself deliberately
// never joins it: every refresh() caller is reacting to something that just
// changed (a serf/tree/changed push, a reconnect), and a request already in
// flight may have been issued BEFORE that change, so joining it would drop
// the update with nothing left to re-fetch it. refreshGeneration decides
// which response wins; this decides whether to ask at all.
let inflightRefresh: Promise<boolean> | null = null;

function sessionIDMatches(node: TreeNode, id: string): boolean {
  if (node.ref === id || node.session_id === id) return true;
  if (node.host_id === "local") {
    const raw = id.startsWith("local:") ? id.slice("local:".length) : id;
    return node.session_id === raw || node.ref === `local:${raw}`;
  }
  return false;
}

function reconcileNodes(nodes: TreeNode[], deletedIDs: string[]): TreeNode[] {
  return nodes.flatMap((node) => {
    if (deletedIDs.some((id) => sessionIDMatches(node, id))) return [];
    const children = reconcileNodes(node.children, deletedIDs);
    return [{ ...node, children }];
  });
}

function projectHasOverflow(project: TreeProject): boolean {
  return (project.more_current ?? 0) > 0 || (project.more_recent ?? 0) > 0 || (project.more_archived ?? 0) > 0;
}

function reconcileProjectList(
  projects: TreeProject[],
  key: string,
  deletedIDs: string[],
  skippedIDs: string[],
  hydratedDetailIsEmpty: boolean,
): TreeProject[] {
  return projects.flatMap((project) => {
    if (project.key !== key) return [project];
    const sessions = reconcileNodes(project.sessions, deletedIDs);
    const sessionCount =
      sessions.length > 0
        ? sessions.length
        : (project.session_count ?? (skippedIDs.length > 0 ? skippedIDs.length : undefined));
    if (
      skippedIDs.length === 0 &&
      !projectHasOverflow(project) &&
      sessions.length === 0 &&
      (hydratedDetailIsEmpty || sessionCount === undefined)
    ) {
      return [];
    }
    return [
      {
        ...project,
        sessions,
        ...(sessionCount === undefined ? {} : { session_count: sessionCount }),
      },
    ];
  });
}

export const treeStore = createStore<TreeStoreState>((set, get) => ({
  tree: null,
  treeGeneration: 0,
  loading: false,
  error: null,
  projectDetails: new Map(),
  projectDetailGenerations: new Map(),

  refresh() {
    const run = (async (): Promise<boolean> => {
      const generation = ++refreshGeneration;
      set({ loading: true, error: null });
      try {
        const tree = await fetchTree();
        if (generation !== refreshGeneration) return false;
        set({ tree, treeGeneration: generation, loading: false, error: null });
        return true;
      } catch (err) {
        if (generation !== refreshGeneration) return false;
        set({ loading: false, error: errorText(err) });
        return false;
      }
    })();
    // Published only after the call above has already bumped the generation
    // and set loading - both synchronous, so nothing can observe a half-
    // started refresh. Cleared only if this is still the latest one, so a
    // slower predecessor settling later never retires its successor's marker.
    inflightRefresh = run;
    const clear = () => {
      if (inflightRefresh === run) inflightRefresh = null;
    };
    void run.then(clear, clear);
    return run;
  },

  ensureLoaded() {
    if (get().tree !== null) return Promise.resolve(true);
    return inflightRefresh ?? get().refresh();
  },

  loadProjectDetail(key) {
    const mutationGeneration = projectMutationGenerations.get(key) ?? 0;
    const treeGeneration = get().treeGeneration;
    const requestKey = `${treeGeneration}:${key}`;
    const existing = projectDetailsInFlight.get(requestKey);
    if (existing) return existing;
    const request = (async () => {
      try {
        const detail = await fetchProjectDetail(key);
        set((s) => {
          if (
            (projectMutationGenerations.get(key) ?? 0) !== mutationGeneration ||
            s.treeGeneration !== treeGeneration
          ) {
            return s;
          }
          const next = new Map(s.projectDetails);
          const nextGenerations = new Map(s.projectDetailGenerations);
          next.set(key, detail);
          nextGenerations.set(key, treeGeneration);
          return { projectDetails: next, projectDetailGenerations: nextGenerations };
        });
      } catch {
        // Best-effort: projectDetails simply doesn't gain an entry, so the
        // rail's disclosure stays retriable (collapse + re-expand tries
        // again) instead of getting stuck showing a hard failure state.
      }
    })();
    projectDetailsInFlight.set(requestKey, request);
    const clear = () => {
      if (projectDetailsInFlight.get(requestKey) === request) projectDetailsInFlight.delete(requestKey);
    };
    void request.then(clear, clear);
    return request;
  },

  async loadProjectPage(key, tier, offset, limit) {
    const mutationGeneration = projectMutationGenerations.get(key) ?? 0;
    const treeGeneration = get().treeGeneration;
    const page = await fetchProjectPage(key, tier, offset, limit);
    set((s) => {
      if ((projectMutationGenerations.get(key) ?? 0) !== mutationGeneration || s.treeGeneration !== treeGeneration) {
        return s;
      }
      const nextDetails = new Map(s.projectDetails);
      const nextDetailGenerations = new Map(s.projectDetailGenerations);
      const detail = nextDetails.get(key);
      if (detail && nextDetailGenerations.get(key) === s.treeGeneration) {
        nextDetails.set(key, mergeProjectPage(detail, page));
      }
      if (!s.tree) return { projectDetails: nextDetails, projectDetailGenerations: nextDetailGenerations };
      return {
        projectDetails: nextDetails,
        projectDetailGenerations: nextDetailGenerations,
        tree: {
          ...s.tree,
          projects: mergeProjectInList(s.tree.projects, page),
          archived_projects: mergeProjectInList(s.tree.archived_projects, page),
          test_runs: mergeProjectInList(s.tree.test_runs, page),
        },
      };
    });
  },

  reconcileProjectDelete(key, deletedIDs, skippedIDs) {
    refreshGeneration++;
    projectMutationGenerations.set(key, (projectMutationGenerations.get(key) ?? 0) + 1);
    set((s) => {
      const nextDetails = new Map(s.projectDetails);
      const nextDetailGenerations = new Map(s.projectDetailGenerations);
      const detail = nextDetails.get(key);
      let hydratedDetailIsEmpty = false;
      if (detail) {
        const sessions = reconcileNodes(detail.sessions, deletedIDs);
        hydratedDetailIsEmpty = sessions.length === 0 && skippedIDs.length === 0 && !projectHasOverflow(detail);
        if (hydratedDetailIsEmpty) {
          nextDetails.delete(key);
          nextDetailGenerations.delete(key);
        } else nextDetails.set(key, { ...detail, sessions });
      }
      if (!s.tree) {
        return { projectDetails: nextDetails, projectDetailGenerations: nextDetailGenerations, loading: false };
      }
      return {
        projectDetails: nextDetails,
        projectDetailGenerations: nextDetailGenerations,
        loading: false,
        tree: {
          ...s.tree,
          projects: reconcileProjectList(s.tree.projects, key, deletedIDs, skippedIDs, hydratedDetailIsEmpty),
          archived_projects: reconcileProjectList(
            s.tree.archived_projects,
            key,
            deletedIDs,
            skippedIDs,
            hydratedDetailIsEmpty,
          ),
          test_runs: reconcileProjectList(s.tree.test_runs, key, deletedIDs, skippedIDs, hydratedDetailIsEmpty),
        },
      };
    });
  },
}));

export function useTreeStore(): TreeStoreState;
export function useTreeStore<T>(selector: (state: TreeStoreState) => T): T;
export function useTreeStore<T>(selector?: (state: TreeStoreState) => T): T | TreeStoreState {
  // Not a real conditional hook call - see stores/connection.ts's own
  // useConnectionStore for the full explanation (zustand's useStore has a
  // `selector = identity` JS default param, so both arms run identically).
  // biome-ignore lint/correctness/useHookAtTopLevel: same hook both arms, JS default param not a real conditional - see stores/connection.ts
  return selector ? useStore(treeStore, selector) : useStore(treeStore);
}

// --- notification-triggered refetch -------------------------------------
//
// Notification methods that invalidate the cached tree and should trigger a
// debounced refresh(). `serf/tree/changed` (a sibling Go stream, landing
// after this task) is a one-line addition to this list once it exists in
// NotificationName - everything else here (the debounce, the subscribe
// wiring) already generalizes to N methods and needs no other change.
export const REFRESH_NOTIFICATIONS: readonly AnyNotification["method"][] = [
  "thread/started",
  "thread/closed",
  "serf/attention/changed",
  // The hub's dedicated tree push (roster deltas, past-index changes, and
  // every successful mutation, exactly once each) — the reason the old
  // UI's 60s sidebar resync poll has no successor here.
  "serf/tree/changed",
];

const REFETCH_DEBOUNCE_MS = 250;

let wiredClient: AppwireClientLike | null = null;
let refetchTimer: ReturnType<typeof setTimeout> | undefined;

function scheduleRefetch(): void {
  clearTimeout(refetchTimer);
  refetchTimer = setTimeout(() => {
    void treeStore.getState().refresh();
  }, REFETCH_DEBOUNCE_MS);
}

function handleNotification(n: AnyNotification): void {
  if (REFRESH_NOTIFICATIONS.includes(n.method)) scheduleRefetch();
}

function attachNotifications(client: AppwireClientLike): void {
  if (client === wiredClient) return; // already wired to this exact client
  wiredClient = client;
  client.onNotification(handleNotification);
}

// Watches connectionStore for the client becoming available and attaches
// this store's own notification handler to it - subscribed once, at module
// load, rather than lazily inside refresh(). refresh() itself never needs
// the client (it's a plain fetch()), so nothing would otherwise ever retry
// the attachment if the very first attempt raced AppShell's own connect()
// effect: React runs child effects before parent effects within one commit,
// so a sibling's mount effect (this store's first refresh(), driven by the
// rail's own mount) can easily run before AppShell's connect() has set
// connectionStore's client at all. Reacting to the store instead of reading
// it once means the attachment lands the moment the client actually shows
// up, no matter which order those two effects fire in.
connectionStore.subscribe((state) => {
  if (state.client) attachNotifications(state.client);
});
const initialClient = connectionStore.getState().client;
if (initialClient) attachNotifications(initialClient);

// resetTreeStoreForTests resets every module-private/store field to its
// initial state, including the pending debounce timer - tree.ts is a
// singleton store (one cached tree, one wired-client marker, at most one
// in-flight debounce) shared by the whole app, so tree.test.ts must reset
// it between tests to keep them isolated. No production code should ever
// call this (mirrors threads.ts's resetThreadsStoreForTests precedent).
export function resetTreeStoreForTests(): void {
  wiredClient = null;
  refreshGeneration = 0;
  inflightRefresh = null;
  projectMutationGenerations.clear();
  projectDetailsInFlight.clear();
  clearTimeout(refetchTimer);
  refetchTimer = undefined;
  treeStore.setState({
    tree: null,
    treeGeneration: 0,
    loading: false,
    error: null,
    projectDetails: new Map(),
    projectDetailGenerations: new Map(),
  });
}
