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
  favorites: TreeNode[];
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
interface WireTreeResponse
  extends Omit<
    TreeResponse,
    "sources" | "live" | "needs_you" | "favorites" | "projects" | "archived_projects" | "test_runs"
  > {
  sources: Source[] | null;
  live: WireTreeNode[] | null;
  needs_you: WireTreeNode[] | null;
  favorites: WireTreeNode[] | null;
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

function normalizeResponse(r: WireTreeResponse): TreeResponse {
  return {
    ...r,
    sources: r.sources ?? [],
    live: (r.live ?? []).map(normalizeNode),
    needs_you: (r.needs_you ?? []).map(normalizeNode),
    favorites: (r.favorites ?? []).map(normalizeNode),
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

export interface TreeStoreState {
  // null until the first successful load; a failed refresh leaves whatever
  // was last successfully loaded in place rather than blanking it (a
  // transient fetch error must not flash the whole sidebar to empty).
  tree: TreeResponse | null;
  loading: boolean;
  error: string | null;
  // Lazily-hydrated archived-project detail (real `sessions`, not the
  // session_count-only stub /api/tree ships), keyed by TreeProject.key.
  // Never cleared on refresh() - a project's key is stable identity, and a
  // fresh /api/tree stub is worse than what's already loaded, not better.
  projectDetails: Map<string, TreeProject>;
  refresh(): Promise<void>;
  loadProjectDetail(key: string): Promise<void>;
}

export const treeStore = createStore<TreeStoreState>((set) => ({
  tree: null,
  loading: false,
  error: null,
  projectDetails: new Map(),

  async refresh() {
    set({ loading: true, error: null });
    try {
      const tree = await fetchTree();
      set({ tree, loading: false, error: null });
    } catch (err) {
      set({ loading: false, error: errorText(err) });
    }
  },

  async loadProjectDetail(key) {
    try {
      const detail = await fetchProjectDetail(key);
      set((s) => {
        const next = new Map(s.projectDetails);
        next.set(key, detail);
        return { projectDetails: next };
      });
    } catch {
      // Best-effort: projectDetails simply doesn't gain an entry, so the
      // rail's disclosure stays retriable (collapse + re-expand tries
      // again) instead of getting stuck showing a hard failure state.
    }
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
  clearTimeout(refetchTimer);
  refetchTimer = undefined;
  treeStore.setState({ tree: null, loading: false, error: null, projectDetails: new Map() });
}
