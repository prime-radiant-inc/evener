// workspace.ts owns which logical panes are open (type + params) and which
// one is focused - the single source of truth DockHost's dockview instance
// mirrors, not the other way around. dockview-driven interactions DockHost
// can't intercept before they happen (dragging a tab closed, clicking a
// different tab) still round-trip back into this store via DockHost's own
// onDidRemovePanel/onDidActivePanelChange handlers, so this store is always
// the answer to "what's open and what's focused" regardless of which side
// initiated a change. Keeping the pane list independent of any particular
// host (rather than reading it back out of a live DockviewApi) is also what
// lets a future mobile host (Task 4) share it without dockview at all.

import type { FileReference } from "@evener/appwire-client/docContent";
import type { DockviewApi, IDockviewPanel, SerializedDockview } from "dockview-core";
import { useEffect } from "react";
import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import {
  composerSelectionScope,
  readPaneComposerSeat,
  readRecoverySelection,
  readRefComposerSeat,
  transferRecoverySelection,
  writeComposerSeat,
  writePaneComposerSeat,
  writeRefComposerSeat,
} from "../panes/session/composer/draft";
import { createSecureUUID } from "../stores/secureUUID";
import { type PaneTypeId, paneFor } from "./paneRegistry";
import { paramString, refParam } from "./routing";
import { isMobileViewport } from "./useIsMobile";

// Which of the workspace's two slots a pane lives in. The main slot holds
// exactly ONE pane - the big one in the top left, beside the rail - and
// everything else stacks as tabs in the secondary group to its right. A main
// pane therefore never shares a tab bar with anything, which is the whole
// point (Jesse: "there should only ever be one pane in the 'main group'").
export type PaneSlot = "main" | "secondary" | "parked";

export function presentedPanes(panes: readonly OpenPaneRecord[]): OpenPaneRecord[] {
  return panes.filter((pane) => pane.slot !== "parked");
}

type PrimaryPaneType = "settings" | "spawn" | "session" | "welcome";

export interface OpenPaneRecord {
  id: string;
  type: PaneTypeId;
  params: unknown;
  // Stable recovery-selection seat for this conversation pane. The pane id
  // survives a desktop layout restore; a mobile reload reconstructs the route
  // with a new id, so the seat is also persisted per tab in composer storage
  // (draft.ts). It is deliberately not part of the saved layout geometry: a
  // layout is shared by every tab, and another tab must never write this tab's
  // seat. Absent on records no placement has seated yet; consumers fall back to
  // the pane id.
  composerSeatId?: string;
  // Which slot this pane belongs to. openPane assigns it initially and
  // promotePane swaps existing record slots without replacing their identities.
  // DockHost turns changes into dockview addPanel/moveTo operations while
  // leaving all other split and drag geometry under dockview's control.
  slot: PaneSlot;
}

// Open's exact return surface is not part of transcript params or saved layout:
// a same-ref SESSION and transcript are distinct retained contexts. Object keys
// bind each edge to these pane lifetimes, not IDs a restore/reset can reuse.
const transcriptOpenOrigins = new Map<OpenPaneRecord, OpenPaneRecord>();

// A conversation pane's identity is its ref alone, so the parent context it
// was opened with lives here instead of in its params. Object keys bind each
// edge to pane lifetimes, not IDs a restore/reset can reuse, while the ref
// value survives the parent pane closing - the same retention a transcript
// pane's parentRef param has.
const conversationOpenParentRefs = new Map<OpenPaneRecord, string>();

export interface DocumentPaneState {
  readonly reference: FileReference;
  readonly origin: OpenPaneRecord | undefined;
  readonly reopen: number;
}

const documentPaneStates = new Map<OpenPaneRecord, DocumentPaneState>();

export function documentPaneState(pane: OpenPaneRecord): DocumentPaneState | undefined {
  return documentPaneStates.get(pane);
}

export function recordDocumentPaneState(pane: OpenPaneRecord, state: DocumentPaneState): void {
  const panes = workspaceStore.getState().panes;
  if (!panes.includes(pane)) return;
  documentPaneStates.set(pane, state);
  // The retained value deliberately lives outside dedup params, but consumers
  // still need one workspace publication when an existing document is reopened.
  workspaceStore.setState({ panes: [...panes] });
}

export function recordTranscriptOpenOrigin(pane: OpenPaneRecord, origin: OpenPaneRecord | undefined): void {
  transcriptOpenOrigins.delete(pane);
  const panes = workspaceStore.getState().panes;
  const params = pane.params as { ref?: unknown; parentRef?: unknown };
  if (
    pane.type === "transcript" &&
    origin !== undefined &&
    origin !== pane &&
    panes.includes(pane) &&
    panes.includes(origin) &&
    (origin.type === "session" || origin.type === "transcript") &&
    typeof params.parentRef === "string" &&
    params.parentRef !== "" &&
    params.parentRef !== params.ref &&
    (origin.params as { ref?: unknown }).ref === params.parentRef
  ) {
    transcriptOpenOrigins.set(pane, origin);
  }
}

export function transcriptOpenOrigin(pane: OpenPaneRecord): OpenPaneRecord | undefined {
  return transcriptOpenOrigins.get(pane);
}

export function recordConversationOpenParent(pane: OpenPaneRecord, parentRef: string | undefined): void {
  conversationOpenParentRefs.delete(pane);
  const params = pane.params as { ref?: unknown };
  if (
    pane.type === "session" &&
    typeof parentRef === "string" &&
    parentRef !== "" &&
    parentRef !== params.ref &&
    workspaceStore.getState().panes.includes(pane)
  ) {
    conversationOpenParentRefs.set(pane, parentRef);
  }
}

export function conversationOpenParentRef(pane: OpenPaneRecord): string | undefined {
  return conversationOpenParentRefs.get(pane);
}

const paneRetypeListeners = new Set<(previous: OpenPaneRecord, replacement: OpenPaneRecord) => void>();

// Runtime owners transfer before removal observers see the replacement. Keep
// the dependency one-way so workspace never imports recovery-backed owners.
export function onPaneRetype(listener: (previous: OpenPaneRecord, replacement: OpenPaneRecord) => void): () => void {
  paneRetypeListeners.add(listener);
  return () => paneRetypeListeners.delete(listener);
}

const workspaceRestoreListeners = new Set<(panes: readonly OpenPaneRecord[]) => void>();

export function onWorkspaceRestore(listener: (panes: readonly OpenPaneRecord[]) => void): () => void {
  workspaceRestoreListeners.add(listener);
  return () => workspaceRestoreListeners.delete(listener);
}

export interface WorkspaceStoreState {
  panes: OpenPaneRecord[];
  focusedPaneId: string | null;
  // Returns the paneId - a fresh one for a genuinely new pane, or the
  // existing one when opening a singleton type that's already open (its
  // params are updated in place if they differ) or a non-singleton pane
  // with identical params to one already open (deep-equal via sameParams).
  // Either way the (possibly pre-existing) pane becomes focused, unless
  // keepExistingFocus asks otherwise.
  //
  // keepExistingFocus: focus a pane this call had to CREATE, but leave focus
  // alone when it resolved to one already open. A restore caller can use this
  // for a captured secondary or other non-primary pane: preserve the current
  // layout focus when that pane is already present, while still focusing it
  // when the restore genuinely has to create it.
  //
  // Placement is this function's own decision, not a caller's: the FIRST pane
  // takes the main slot, every later one goes secondary. Putting the policy
  // here rather than at the ~21 call sites is what makes it one rule instead
  // of twenty-one (see PaneSlot above for the rule itself).
  // slot: "secondary" means "place this beside the main pane, never as it" -
  // for a caller that knows its pane is not a primary one (the rail, opening
  // a subagent). Omitted, placement follows the default rule above. It only
  // affects a pane being CREATED: reopening an already-open pane resolves to
  // that pane wherever it currently sits. Only promotePane changes its slot.
  openPane(
    type: PaneTypeId,
    params?: unknown,
    opts?: { keepExistingFocus?: boolean; slot?: PaneSlot; recoverySelectionFrom?: string },
  ): string;
  togglePane(type: PaneTypeId, params?: unknown): { paneId: string; opened: boolean };
  // Makes (type, params) the one pane in the main slot, keeping that pane (and
  // its id, and everything mounted under it) only when it is already the same
  // primary - which for a session means the same ref. See primaryMatches for
  // why the identity is derived here rather than named by the caller.
  replacePrimary(type: PrimaryPaneType, params: unknown): string;
  promotePane(paneId: string): void;
  retypePane(expected: OpenPaneRecord, type: PaneTypeId, params: unknown): boolean;
  closePane(paneId: string): void;
  // The pane occupying the main slot, or null when it is empty (the state
  // DockHost relaunches welcome into). Exposed because "is the main slot
  // empty" is one predicate with two consumers - DockHost's boot fallback and
  // its per-close relaunch.
  mainPane(): OpenPaneRecord | null;
  focusPane(paneId: string): void;
  layoutJSON(): unknown;
  // On host reconstruction the live store still owns pane lifetimes, slots
  // and focus. Restore only dockview geometry, never saved runtime ownership.
  restoreLayout(json: unknown, opts?: { preserveLivePanes?: boolean }): boolean;
}

// The wire shape stored in each dockview panel's own `params` bag - how a
// logical pane (type + its own params) round-trips through dockview's
// addPanel()/toJSON()/fromJSON(). DockHost imports this to type its
// addPanel() calls and its generic pane-host renderer's props consistently
// with what restoreLayout() below reconstructs them into.
export interface PanePanelParams {
  paneType: PaneTypeId;
  paneParams: unknown;
}

// Plain structural equality for the small, always-JSON-safe param objects
// every pane type uses ({ref}, {ref,path}, {section?}, {}). JSON.stringify
// is deterministic here because none of them have more than a couple of
// primitive-valued keys with no realistic key-ordering ambiguity - a full
// deep-equal dependency would be more machinery than this actually needs.
function sameParams(a: unknown, b: unknown): boolean {
  return JSON.stringify(a) === JSON.stringify(b);
}

const pendingPaneFocus = new Set<string>();

export function isPaneOpen(state: WorkspaceStoreState, type: PaneTypeId, params: unknown): boolean {
  return presentedPanes(state.panes).some((pane) => pane.type === type && sameParams(pane.params, params));
}

// A transcript names its SUBJECT in `ref`, and a job log's subject is a job
// ("job:<id>" - the prefix panes/transcript/Transcript.tsx switches on), not a
// session. A job log belongs to the session that owns the job, which the
// opener records in `parentRef`; a ref with this prefix therefore reads as no
// session on its own.
const JOB_REF_PREFIX = "job:";

// Conversation sources retain raw owner fields, callers decide whether they
// match a requested session. Job transcripts name that owner in parentRef.
export function sourcePaneSessionRef(pane: OpenPaneRecord): unknown {
  if (pane.type !== "session" && pane.type !== "transcript") return undefined;
  const params = pane.params as { ref?: unknown; parentRef?: unknown };
  if (pane.type === "transcript" && typeof params.ref === "string" && params.ref.startsWith(JOB_REF_PREFIX)) {
    return params.parentRef;
  }
  return params.ref;
}

// The session a pane is about, or null when it is about none. The key that
// names it depends on the pane: the session pane and its companion panels
// name it `ref`; a transcript names its subject in `ref`, falling to
// `parentRef` when that subject is a job; a doc names its session under
// `session` (panes/doc/openDoc.ts). Every other pane (welcome, settings,
// spawn) names no session at all.
function paneSessionRef(pane: OpenPaneRecord | undefined): string | null {
  const params = pane?.params as { ref?: unknown; parentRef?: unknown; session?: unknown } | null | undefined;
  const ref = refParam(params);
  if (ref !== null) return ref.startsWith(JOB_REF_PREFIX) ? paramString(params?.parentRef) : ref;
  return paramString(params?.session);
}

// The session the workspace is currently SHOWING: the session the focused
// pane is about, or - when it is about none, e.g. a settings pane focused
// beside a session still holding main - the session the main pane is about.
// That is the rail's answer to "which session am I looking at", and what marks
// its row as the selected item.
//
// Deliberately wider than AppShell's own focusedSessionRef, which answers a
// narrower question ("is a session PANE the focused pane?") for the
// composer/needs-you chords and must ignore a subagent's companion panel.
// This one follows those panels and falls back to main.
export function currentSessionRef(state: WorkspaceStoreState): string | null {
  const focused = presentedPanes(state.panes).find((pane) => pane.id === state.focusedPaneId);
  return paneSessionRef(focused) ?? paneSessionRef(state.panes.find((pane) => pane.slot === "main"));
}

export function requestPaneFocus(paneId: string): void {
  pendingPaneFocus.add(paneId);
}

export function consumePaneFocus(paneId: string): boolean {
  if (!pendingPaneFocus.delete(paneId)) return false;
  return true;
}

export function cancelPaneFocus(paneId: string): void {
  pendingPaneFocus.delete(paneId);
}

// Is `pane` the same primary as the (type, params) being requested? The
// identity is READ OUT OF the requested params here, at the one seam that
// consumes it, rather than passed in beside them: a caller that could spell a
// session's identity separately from its ref could pair one with the other,
// and replacePrimary would then keep the live main pane while rewriting its
// params - same pane id, new ref. Neither host remounts for that (both key a
// pane by its id), and nothing below the pane is keyed either, so the previous
// session's job list and badge count would stay on screen (kata z44z; it is
// this rule that makes pcx5/tmyw unreachable). Matching on the params makes
// the mismatch unrepresentable instead of merely unwritten.
function primaryMatches(pane: OpenPaneRecord | null, type: PrimaryPaneType, params: unknown): boolean {
  if (pane?.type !== type) return false;
  // Settings, spawn, and welcome are singletons: their type IS their identity, and a
  // section change is a params update to the pane that already holds it.
  if (type !== "session") return true;
  return (pane.params as { ref?: unknown }).ref === (params as { ref?: unknown }).ref;
}

let nextPaneSeq = 0;
function nextPaneId(type: PaneTypeId): string {
  nextPaneSeq += 1;
  return `pane_${type}_${nextPaneSeq}`;
}

// A conversation pane owns a recovery-selection seat. The seat, not the pane
// id, is the scope: a mobile reload reconstructs the routed pane under a new
// id, and this tab's stored mapping hands the same seat back.
function isConversationPaneType(type: PaneTypeId): boolean {
  return type === "session" || type === "sessionZoom";
}

function liveComposerSeats(panes: readonly OpenPaneRecord[]): Set<string> {
  return new Set(panes.map((pane) => pane.composerSeatId ?? pane.id));
}

// Give a placed pane this tab's stored seat for the conversation. An explicit
// outgoing seat is reusable only once its own pane is leaving; a retained pane
// keeps its scope and the new pane receives a copy of the selection. A choice
// already established for the destination seat always wins.
//
// This is the route/open path, not layout restoration. Pane ids restart on a
// reload, so a reconstructed route's id may equal a different conversation's
// former id; its pane-id mapping is stale and must not win. Restoration reads
// the pane mapping itself (see restoreLayout).
function placeComposerSeat(ref: string, panes: readonly OpenPaneRecord[], outgoingSeat?: string): string {
  const live = liveComposerSeats(panes);
  let seat = outgoingSeat !== undefined && !live.has(outgoingSeat) ? outgoingSeat : undefined;
  if (seat === undefined) {
    const stored = readRefComposerSeat(ref);
    if (stored !== undefined && !live.has(stored)) seat = stored;
  }
  if (seat === undefined) seat = createSecureUUID();
  // Route/open placement writes only the conversation mapping. A pane-id
  // mapping belongs to saved desktop geometry and is written when that layout
  // is saved or restored, so a restarted id cannot clobber another
  // conversation's saved seat before restoration.
  writeRefComposerSeat(ref, seat);
  if (outgoingSeat !== undefined && outgoingSeat !== seat) transferRecoverySelection(ref, outgoingSeat, seat);
  return seat;
}

// The outgoing seat that already owns this ref's selection, if any. Read
// before a placement that is about to remove its pane.
function selectionSeatForRef(
  ref: string,
  panes: readonly OpenPaneRecord[],
  preferred?: OpenPaneRecord | null,
): string | undefined {
  for (const pane of preferred ? [preferred, ...panes] : panes) {
    if (!isConversationPaneType(pane.type)) continue;
    const seat = pane.composerSeatId ?? pane.id;
    if (readRecoverySelection(ref, composerSelectionScope(seat)) !== undefined) return seat;
  }
  return undefined;
}

// The live DockviewApi, registered by DockHost once dockview is ready and
// cleared again on unmount. layoutJSON()/restoreLayout() are the only two
// locked methods that need it - both stay inert (null / false) with no host
// mounted, matching the plan's "dockview serialization (desktop only)". A
// module-private mutable reference, not store state, mirrors connection.ts/
// threads.ts's own precedent for "the live thing a store rides but doesn't
// own the lifecycle of".
let dockviewApi: DockviewApi | null = null;
let workspaceHasMounted = false;

// A cold route creates records before any host mounts, but does not create
// runtime owners. A presented phone workspace or an explicit document/transcript
// open is already living, even when this is its first desktop registration.
// The marker lasts for this page's workspace, not for an individual host mount.
export function registerDockviewApi(api: DockviewApi | null): boolean {
  const reconstructing =
    api !== null && (workspaceHasMounted || documentPaneStates.size > 0 || transcriptOpenOrigins.size > 0);
  if (api !== null) workspaceHasMounted = true;
  dockviewApi = api;
  return reconstructing;
}

// getDockviewApi exposes the live api (or null when no dockview host is
// mounted - a mobile StackHost session, or before DockHost's onReady) for the
// two imperative pane actions that genuinely need it: shell/paneActions.ts's
// popOutPane (dockview-native popout has no store-level equivalent) and its
// openBeside (a null api is the "no split capability here" signal that drives
// the mobile degrade-to-plain-open). A read-only accessor beside the existing
// registerDockviewApi/layoutJSON/restoreLayout, NOT store state - the same
// "the live thing a store rides but doesn't own the lifecycle of" precedent
// those already follow.
export function getDockviewApi(): DockviewApi | null {
  return dockviewApi;
}

// Type predicate: true only for a value that's both a string AND
// currently registered in the pane registry. paneFor() (imported above) is
// the single source of truth for "is this a real pane type" - registering
// a SECOND, hand-maintained list of PaneTypeId's own union members here
// (an earlier version did exactly that, via a `Set` literal) would drift
// silently the moment PaneTypeId's union in paneRegistry.ts ever changes,
// with no compiler check to catch it. The cast reflects paneFor()'s own
// runtime check as the actual source of truth, not a hidden assumption -
// the registry Map's own .get() (which paneFor calls) doesn't "trust" its
// argument's type at runtime, it just looks the key up; whether that
// lookup succeeds IS the validation.
function isRegistered(type: unknown): type is PaneTypeId {
  if (typeof type !== "string") return false;
  try {
    paneFor(type as PaneTypeId);
    return true;
  } catch {
    return false;
  }
}

// Reconstructs one dockview panel's params back into our own wire shape, or
// null for a panel this build can't render: one whose params are missing or
// whose paneType is no longer registered. Null is a SKIP, not a throw - a
// pane type removed from the app (e.g. sessionNotes, retired with the notes
// sidebar pane) or shipped by a newer build leaves its panel in the restored
// dockview layout, and discarding the user's ENTIRE workspace over one
// unloadable panel is a data-loss bug, not a validation stance. restoreLayout
// drops the skipped panel from `panes` and removes it from the live api in
// the same pass, so the rest of the layout recovers from the first render.
function readPanelParams(panel: IDockviewPanel): PanePanelParams | null {
  const raw = panel.params;
  if (!raw || !isRegistered(raw.paneType)) return null;
  const parse = paneFor(raw.paneType).parseParams;
  const params = parse ? parse(raw.paneParams) : raw.paneParams;
  if (parse && params === null) return null;
  // Layouts saved before the conversation-view ruling carry session refs on
  // transcript panes; those reopen as their live conversation pane. Job
  // logs keep the transcript pane.
  if (raw.paneType === "transcript") {
    const ref = (params as { ref?: unknown } | null)?.ref;
    if (typeof ref === "string" && ref !== "" && !ref.startsWith(JOB_REF_PREFIX)) {
      return { paneType: "session", paneParams: { ref } };
    }
  }
  return { paneType: raw.paneType, paneParams: params };
}

// The same migration applied to the incoming layout JSON before dockview
// rebuilds its panels: a migrated panel must be CREATED as a session panel,
// or the dockview panel would still render the removed transcript surface
// while the workspace record beside it says session.
function migrateLegacyLayout(json: unknown): unknown {
  const layout = json as {
    panels?: Record<string, { params?: { paneType?: unknown; paneParams?: unknown } }>;
    conversationParents?: Record<string, string>;
  } | null;
  if (!layout || typeof layout !== "object" || layout.panels === null || typeof layout.panels !== "object") return json;
  let changed = false;
  let parentsChanged = false;
  const panels: Record<string, unknown> = {};
  // A legacy transcript panel carried its open-parent inside its params; the
  // conversation pane records that edge out of band, so the migration lifts
  // each converted panel's parentRef into the layout's edge map - the same
  // key layoutJSON writes for panes saved by this build.
  const conversationParents: Record<string, string> = { ...layout.conversationParents };
  for (const [id, panel] of Object.entries(layout.panels)) {
    const params = panel?.params as
      | {
          paneType?: unknown;
          paneParams?: {
            ref?: unknown;
            parentRef?: unknown;
            source?: { type?: unknown; params?: { ref?: unknown; parentRef?: unknown } };
          };
        }
      | undefined;
    const ref = params?.paneParams?.ref;
    if (
      params &&
      params.paneType === "transcript" &&
      typeof ref === "string" &&
      ref !== "" &&
      !ref.startsWith(JOB_REF_PREFIX)
    ) {
      changed = true;
      panels[id] = { ...panel, params: { paneType: "session", paneParams: { ref } } };
      const parentRef = params.paneParams?.parentRef;
      if (typeof parentRef === "string" && parentRef !== "" && parentRef !== ref) {
        conversationParents[id] = parentRef;
        parentsChanged = true;
      }
    } else {
      panels[id] = panel;
      // A legacy cascade's source descriptor named the conversation it was
      // opened from; on a read-only layout that descriptor was a transcript
      // carrying the source's own parentRef. parseZoomParams drops that field
      // when the record is normalized on restore, so lift it into the edge map
      // now, while the raw panel JSON still holds it. Return retypes this pane
      // into the source conversation and retypePane carries the recorded edge
      // across, so Back reaches the retained parent instead of Welcome.
      const source = params?.paneType === "sessionZoom" ? params.paneParams?.source : undefined;
      const sourceRef = source?.params?.ref;
      const sourceParentRef = source?.params?.parentRef;
      if (
        typeof sourceRef === "string" &&
        sourceRef !== "" &&
        typeof sourceParentRef === "string" &&
        sourceParentRef !== "" &&
        sourceParentRef !== sourceRef
      ) {
        conversationParents[id] = sourceParentRef;
        parentsChanged = true;
      }
    }
  }
  if (!changed && !parentsChanged) return json;
  return parentsChanged || layout.conversationParents !== undefined
    ? { ...layout, panels, conversationParents }
    : { ...layout, panels };
}

// A layout saved while the read-only transcript surface still existed can
// hold BOTH that pane and a session pane for the same conversation, and the
// migration maps the transcript panel to a session panel - so the restore
// would produce two identical conversations. Route reconciliation requires
// exactly one pane per ref (its placement checks count child panes), so the
// duplicates are reconciled here, while the legacy paneType is still
// visible: the panel already typed session is the live conversation and
// survives, else the first panel for the ref. The map returned is
// dropped panel id -> surviving panel id.
function legacyDuplicatePanels(json: unknown): Map<string, string> {
  const layout = json as {
    panels?: Record<string, { params?: { paneType?: unknown; paneParams?: { ref?: unknown } } }>;
  } | null;
  const panels = layout?.panels;
  if (panels === null || panels === undefined || typeof panels !== "object") return new Map();
  const byRef = new Map<string, { sessions: string[]; all: string[] }>();
  for (const [id, panel] of Object.entries(panels)) {
    const params = panel?.params;
    const ref = params?.paneParams?.ref;
    if (
      (params?.paneType !== "session" && params?.paneType !== "transcript") ||
      typeof ref !== "string" ||
      ref === "" ||
      ref.startsWith(JOB_REF_PREFIX)
    ) {
      continue;
    }
    const entry = byRef.get(ref) ?? { sessions: [], all: [] };
    entry.all.push(id);
    if (params.paneType === "session") entry.sessions.push(id);
    byRef.set(ref, entry);
  }
  const dropped = new Map<string, string>();
  for (const { sessions, all } of byRef.values()) {
    if (all.length < 2) continue;
    const survivor = sessions[0] ?? all[0];
    if (survivor === undefined) continue;
    for (const id of all) if (id !== survivor) dropped.set(id, survivor);
  }
  return dropped;
}

// Restored panel ids came from a PREVIOUS page load's own independently-
// numbered nextPaneId counter (nextPaneSeq resets to 0 on every fresh page
// load - see nextPaneId's own comment above), so a freshly-minted id AFTER
// a restore can collide with one just restored - e.g. both sessions' very
// first pane is "pane_doc_1". Left unguarded, the store would then hold two
// DIFFERENT logical panes sharing one id, and DockHost's reconciliation
// effect (which looks dockview panels up BY id) would silently push the
// second one's params into the first one's real dockview panel instead of
// creating a new one - discarding whichever pane's content lost that race,
// with no error or warning. Bumping the counter past every id restoreLayout
// just brought back makes every id minted AFTER this collision-proof
// against it. This was always a latent hazard (any pane opened after a
// restore could have hit it), but DockHost's merge-restore boot sequence
// (handleReady re-opening a routed pane on top of a restored base) made it
// routine rather than rare - caught via a live save/restore/re-open round
// trip in that flow's own test, not by inspection.
function bumpPastRestoredIds(panes: { id: string }[]): void {
  for (const p of panes) {
    const suffix = /_(\d+)$/.exec(p.id)?.[1];
    if (suffix !== undefined) nextPaneSeq = Math.max(nextPaneSeq, Number(suffix));
  }
}

// A synchronous save can precede the host effect that removes parked panels.
// Filter that snapshot only, never replay or mutate the user's live geometry.
function omitParkedPanels(layout: SerializedDockview, parked: ReadonlySet<string>): void {
  if (parked.size === 0) return;
  type Node = SerializedDockview["grid"]["root"];
  type Group = Exclude<Node["data"], Node[]>;
  const groups = new Set<string>();
  function keepGroup(group: Group): boolean {
    group.views = group.views.filter((id) => !parked.has(id));
    if (group.activeView && parked.has(group.activeView)) group.activeView = group.views[0];
    if (group.tabGroups) {
      group.tabGroups = group.tabGroups.flatMap((tabs) => {
        const panelIds = tabs.panelIds.filter((id) => !parked.has(id));
        return panelIds.length ? [{ ...tabs, panelIds }] : [];
      });
    }
    if (group.views.length === 0) return false;
    groups.add(group.id);
    return true;
  }
  function keepNode(node: Node): boolean {
    if (!Array.isArray(node.data)) return keepGroup(node.data);
    node.data = node.data.filter(keepNode);
    return node.data.length > 0;
  }
  if (!keepNode(layout.grid.root)) layout.grid.root = { type: "branch", data: [] };
  for (const key of ["floatingGroups", "popoutGroups"] as const) {
    const windows = layout[key];
    if (!windows) continue;
    // Mutate the arrays in place to retain each window kind's typed geometry.
    for (let index = windows.length - 1; index >= 0; index--) {
      const window = windows[index];
      if (window && ((window.data && !keepGroup(window.data)) || (window.grid && !keepNode(window.grid.root))))
        windows.splice(index, 1);
    }
  }
  if (layout.edgeGroups) {
    for (const key of ["top", "bottom", "left", "right"] as const) {
      const edge = layout.edgeGroups[key];
      if (edge?.group && !keepGroup(edge.group as Group)) delete layout.edgeGroups[key];
    }
  }
  for (const id of parked) delete layout.panels[id];
  if (layout.activeGroup && !groups.has(layout.activeGroup)) layout.activeGroup = groups.values().next().value;
}

export const workspaceStore = createStore<WorkspaceStoreState>((set, get) => ({
  panes: [],
  focusedPaneId: null,

  mainPane() {
    return get().panes.find((p) => p.slot === "main") ?? null;
  },

  openPane(type, params = {}, opts) {
    const descriptor = paneFor(type);
    const state = get();

    const keepFocus = opts?.keepExistingFocus === true;

    if (descriptor.singleton) {
      const existing = presentedPanes(state.panes).find((p) => p.type === type);
      if (existing) {
        const paramsChanged = !sameParams(existing.params, params);
        const focusChanged = !keepFocus && state.focusedPaneId !== existing.id;
        const ref = isConversationPaneType(type) ? refParam(params) : null;
        const seatAdded = ref !== null && existing.composerSeatId === undefined;
        if (seatAdded) {
          // Attach in place: object-keyed edges bind to this record.
          existing.composerSeatId = placeComposerSeat(ref, state.panes, opts?.recoverySelectionFrom);
        }
        if (paramsChanged || focusChanged || seatAdded) {
          set({
            panes: paramsChanged
              ? state.panes.map((p) => (p.id === existing.id ? { ...existing, params } : p))
              : seatAdded
                ? [...state.panes]
                : state.panes,
            ...(keepFocus ? {} : { focusedPaneId: existing.id }),
          });
        }
        return existing.id;
      }
    } else {
      const existing = presentedPanes(state.panes).find((p) => p.type === type && sameParams(p.params, params));
      if (existing) {
        let panes = state.panes;
        const ref = isConversationPaneType(type) ? refParam(params) : null;
        if (ref !== null) {
          if (existing.composerSeatId === undefined) {
            // Attach in place: object-keyed edges bind to this record.
            existing.composerSeatId = placeComposerSeat(ref, state.panes, opts?.recoverySelectionFrom);
            panes = [...state.panes];
          } else if (
            opts?.recoverySelectionFrom !== undefined &&
            opts.recoverySelectionFrom !== existing.composerSeatId
          ) {
            transferRecoverySelection(ref, opts.recoverySelectionFrom, existing.composerSeatId);
          }
        }
        const focusChanged = !keepFocus && state.focusedPaneId !== existing.id;
        if (panes !== state.panes || focusChanged) set({ panes, ...(keepFocus ? {} : { focusedPaneId: existing.id }) });
        return existing.id;
      }
    }

    // The one placement rule, in the one place every open goes through: the
    // main slot takes a pane only when it is empty, so it holds exactly one at
    // a time and everything else stacks in the secondary group to its right.
    //
    // A welcome pane in the main slot counts as empty. Welcome IS the main
    // slot's empty state ("No session open"), so the first real pane replaces
    // it instead of opening beside it - without this, navigating from "/" to a
    // session left the workspace split between a session and a placeholder
    // telling the user nothing was open (seen in a real browser, not a
    // fixture). Only the main slot's welcome is displaced, and only by a pane
    // that is not itself welcome.
    //
    // opts.slot === "secondary" is the single exception, for a caller that
    // knows its pane must never be the primary one: a subagent opens BESIDE
    // the session that spawned it, never as it. This store sees only a type
    // and a {ref} and so cannot tell those apart itself - the rail can, and
    // says so (see this file's PaneSlot comment and docs/web-ui/specs/
    // 2026-07-26-subagent-opens-beside-main.md §A). Such a pane displaces
    // nothing: it never wanted the slot welcome is holding.
    const id = nextPaneId(type);
    const ref = isConversationPaneType(type) ? refParam(params) : null;
    const composerSeatId = ref === null ? undefined : placeComposerSeat(ref, state.panes, opts?.recoverySelectionFrom);
    const main = state.panes.find((p) => p.slot === "main");
    const refusesMain = opts?.slot === "secondary";
    const displaced = !refusesMain && type !== "welcome" && main?.type === "welcome" ? main : undefined;
    const slot: PaneSlot = !refusesMain && (main === undefined || displaced !== undefined) ? "main" : "secondary";
    const kept = displaced ? state.panes.filter((p) => p.id !== displaced.id) : state.panes;
    set({
      panes: [...kept, { id, type, params, slot, ...(composerSeatId === undefined ? {} : { composerSeatId }) }],
      focusedPaneId: id,
    });
    return id;
  },

  togglePane(type, params = {}) {
    const state = get();
    const existing = presentedPanes(state.panes).find((pane) => pane.type === type && sameParams(pane.params, params));
    if (existing) {
      get().closePane(existing.id);
      cancelPaneFocus(existing.id);
      return { paneId: existing.id, opened: false };
    }
    const paneId = get().openPane(type, params, { slot: "secondary" });
    requestPaneFocus(paneId);
    return { paneId, opened: true };
  },

  replacePrimary(type, params) {
    paneFor(type);
    const state = get();
    const main = state.panes.find((pane) => pane.slot === "main") ?? null;

    if (main !== null && primaryMatches(main, type, params)) {
      const panes = state.panes.filter((pane) => pane.id === main.id || !primaryMatches(pane, type, params));
      const paramsChanged = !sameParams(main.params, params);
      const duplicatesRemoved = panes.length !== state.panes.length;
      const ref = isConversationPaneType(type) ? refParam(params) : null;
      const seat =
        ref !== null && main.composerSeatId === undefined ? placeComposerSeat(ref, state.panes) : main.composerSeatId;
      const seatChanged = seat !== main.composerSeatId;
      // Attach in place so object-keyed edges keep their pane identity.
      if (seatChanged && seat !== undefined) main.composerSeatId = seat;
      if (!paramsChanged && !duplicatesRemoved && !seatChanged && state.focusedPaneId === main.id) return main.id;
      const mainWithParams = paramsChanged ? { ...main, params } : main;
      const nextPanes = panes.map((pane) => (pane.id === main.id ? mainWithParams : pane));
      const nextFocusedPaneId = main.id;
      set({ panes: nextPanes, focusedPaneId: nextFocusedPaneId });
      return main.id;
    }

    const id = nextPaneId(type);
    const ref = isConversationPaneType(type) ? refParam(params) : null;
    // This arm removes every existing pane, so a promoted secondary's seat is
    // free to move to the replacement.
    const outgoingSeat = ref === null ? undefined : selectionSeatForRef(ref, state.panes, main);
    const composerSeatId = ref === null ? undefined : placeComposerSeat(ref, [], outgoingSeat);
    set({
      panes: [{ id, type, params, slot: "main", ...(composerSeatId === undefined ? {} : { composerSeatId }) }],
      focusedPaneId: id,
    });
    return id;
  },

  promotePane(paneId) {
    const panes = get().panes;
    const source = panes.find((pane) => pane.id === paneId);
    if (!source || source.slot === "main") return;
    for (const pane of panes) {
      if (pane === source) pane.slot = "main";
      else if (pane.slot === "main") pane.slot = "secondary";
    }
    set({ panes: [...panes] });
  },

  retypePane(expected, type, params) {
    const state = get();
    if (!state.panes.includes(expected)) return false;
    paneFor(type);
    if (expected.type === type && expected.params === params) return true;
    const replacement = { ...expected, type, params };
    if (!isConversationPaneType(type)) {
      // A read-only transcript owns no composer selection.
      delete replacement.composerSeatId;
    } else {
      const ref = refParam(params);
      if (ref !== null) {
        if (replacement.composerSeatId === undefined) {
          replacement.composerSeatId = placeComposerSeat(ref, state.panes);
        } else {
          // A retarget (resume, then return to the resumed session) keeps the
          // pane's seat. Persist it under the destination ref so a mobile
          // route reconstruction can find that seat after reload.
          writeRefComposerSeat(ref, replacement.composerSeatId);
        }
      }
    }
    for (const listener of paneRetypeListeners) listener(expected, replacement);
    const origins = [...transcriptOpenOrigins];
    transcriptOpenOrigins.clear();
    for (const [pane, origin] of origins) {
      transcriptOpenOrigins.set(pane === expected ? replacement : pane, origin === expected ? replacement : origin);
    }
    // A cascade's drill, pop and resume retype its pane; a document opened
    // from one of its columns keeps pointing at the surviving inspector, so
    // its origin binding must follow the replacement the same way.
    const documents = [...documentPaneStates];
    documentPaneStates.clear();
    for (const [pane, state] of documents) {
      documentPaneStates.set(
        pane === expected ? replacement : pane,
        state.origin === expected ? { ...state, origin: replacement } : state,
      );
    }
    const parents = [...conversationOpenParentRefs];
    conversationOpenParentRefs.clear();
    for (const [pane, parentRef] of parents) {
      conversationOpenParentRefs.set(pane === expected ? replacement : pane, parentRef);
    }
    set({ panes: state.panes.map((pane) => (pane === expected ? replacement : pane)) });
    return true;
  },

  closePane(paneId) {
    const state = get();
    if (!state.panes.some((p) => p.id === paneId)) return; // already closed: no-op
    set({
      panes: state.panes.filter((p) => p.id !== paneId),
      focusedPaneId: state.focusedPaneId === paneId ? null : state.focusedPaneId,
    });
  },

  focusPane(paneId) {
    const state = get();
    if (state.focusedPaneId === paneId) return; // already focused: no-op
    if (!presentedPanes(state.panes).some((p) => p.id === paneId)) return; // unknown or parked: no-op
    set({ focusedPaneId: paneId });
  },

  layoutJSON() {
    if (!dockviewApi) return null;
    const layout = dockviewApi.toJSON() as SerializedDockview & {
      conversationParents?: Record<string, string>;
    };
    omitParkedPanels(
      layout,
      new Set(
        get()
          .panes.filter((pane) => pane.slot === "parked")
          .map((pane) => pane.id),
      ),
    );
    for (const pane of get().panes) {
      const panel = layout.panels?.[pane.id];
      if (panel) panel.params = { ...panel.params, paneType: pane.type, paneParams: pane.params };
      // A saved layout is the one moment a pane id and a conversation are
      // known to belong together. Persist the pane mapping here, not at route
      // placement: before restoration, a restarted id may still name another
      // conversation's saved pane.
      if (isConversationPaneType(pane.type) && pane.composerSeatId !== undefined) {
        const ref = refParam(pane.params);
        if (ref !== null) writePaneComposerSeat(pane.id, ref, pane.composerSeatId);
      }
    }
    // The conversation parent-context record is object-keyed beside this
    // store, so a layout must carry its edges separately or a reload
    // strands every restored child's Back walk (the legacy transcript
    // params used to hold the same edge and serialize with the panel).
    const conversationParents: Record<string, string> = {};
    for (const [pane, parentRef] of conversationOpenParentRefs) {
      if (layout.panels?.[pane.id]) conversationParents[pane.id] = parentRef;
    }
    if (Object.keys(conversationParents).length > 0) layout.conversationParents = conversationParents;
    return layout;
  },

  restoreLayout(json, opts) {
    if (!dockviewApi) return false;
    try {
      // Keep the migrated layout, not just hand it to dockview: its
      // conversationParents edge map (lifted from legacy transcript params by
      // the migration, written by layoutJSON for this build's saves) records
      // the open-parent edges below, after the restored records publish.
      const migrated = migrateLegacyLayout(json) as {
        panels?: Record<string, { params?: unknown }>;
        conversationParents?: Record<string, unknown>;
      };
      // The migration maps a legacy transcript panel to its live conversation
      // pane, so a layout holding both that panel and a session panel for the
      // same conversation would restore the conversation twice. Reconcile the
      // duplicate before dockview builds it: the dropped panel's open-parent
      // edge moves to the survivor, and an inspection origin that named the
      // dropped pane is rewritten to the pane that now is that conversation.
      // A host swap restores the app's own current layout against live records
      // and has no legacy paneType to reconcile.
      const duplicates = opts?.preserveLivePanes ? new Map<string, string>() : legacyDuplicatePanels(json);
      if (duplicates.size > 0) {
        const parents: Record<string, unknown> = { ...(migrated.conversationParents ?? {}) };
        for (const [droppedId, survivorId] of duplicates) {
          const edge = parents[droppedId];
          if (edge !== undefined) {
            if (parents[survivorId] === undefined) parents[survivorId] = edge;
            delete parents[droppedId];
          }
        }
        for (const panel of Object.values(migrated.panels ?? {})) {
          const params = panel?.params as
            | { paneParams?: { inspection?: { origin?: { paneId?: unknown } | null } | null } }
            | undefined;
          const origin = params?.paneParams?.inspection?.origin;
          if (!origin || typeof origin.paneId !== "string") continue;
          const survivorId = duplicates.get(origin.paneId);
          if (survivorId === undefined) continue;
          panel.params = {
            ...(panel.params as Record<string, unknown>),
            paneParams: { ...params?.paneParams, inspection: { origin: { ...origin, paneId: survivorId } } },
          };
        }
        migrated.conversationParents = parents;
      }
      dockviewApi.fromJSON(migrated as SerializedDockview);
      // Slots come back from dockview's OWN restored geometry, not from the
      // saved JSON: api.panels is in grid order, so the FIRST panel is the one
      // in the top-left group - the main pane. Read only `panels` (plus each
      // panel's own group), never api.groups: this function's whole
      // dockview surface stays the five members it uses
      // (fromJSON/panels/activePanel/removePanel/clear), which is also what
      // keeps the unit doubles in workspace.test.ts/paneRestore.test.ts
      // honest doubles rather than a growing mirror of the real api.
      //
      // A layout that somehow restores a SECOND panel into that same first
      // group leaves only the first as "main" - impossible for a layout this
      // build saved (the one-pane rule holds on the way in), and it degrades to
      // a stacked main group rather than a crash for anything else. Panels
      // readPanelParams skips (an unregistered/removed pane type) drop out
      // BEFORE the slot assignment, so the first SURVIVING panel is main.
      const entries = dockviewApi.panels.map((panel) => ({ panel, params: readPanelParams(panel) }));
      // The saved selection is read BEFORE any panel leaves the api: removing
      // the active panel makes dockview select an unrelated neighbor, and
      // reading it afterwards would restore that neighbor instead of the
      // conversation the removed pane became.
      const savedActiveId = dockviewApi.activePanel?.id;
      // A panel this build cannot render must leave the live api NOW, not in
      // DockHost's later structural reconciliation: the commit between this
      // restore and that reconciliation renders every api panel through
      // paneFor(), which throws for an unregistered type and can take the
      // whole workspace chunk down with it. A reconciled duplicate leaves the
      // same way: the migration created it as a session panel, so leaving it
      // in the api would undo the collapse this restore just made.
      for (const entry of entries) {
        if (entry.params === null || duplicates.has(entry.panel.id)) dockviewApi.removePanel(entry.panel);
      }
      // A host swap is not a cold load. Even valid saved geometry can be
      // stale after phone opens, closes, promotion or focus changes. Keep the
      // exact living records and their object-keyed state; DockHost reconciles
      // the restored grid against them before applying the current focus.
      if (opts?.preserveLivePanes) return true;
      const restored = entries.filter(
        (entry): entry is { panel: IDockviewPanel; params: PanePanelParams } =>
          entry.params !== null && !duplicates.has(entry.panel.id),
      );
      const panes: OpenPaneRecord[] = restored.map((entry, index) => ({
        id: entry.panel.id,
        type: entry.params.paneType,
        params: entry.params.paneParams,
        slot: index === 0 ? "main" : "secondary",
      }));
      // Reattach every restored conversation to this tab's own seat. The pane
      // mapping survives a desktop reload; the ref mapping covers a route
      // reconstructed under a new pane id. Candidates already held by another
      // restored pane are skipped, so two live panes never share a scope.
      const usedSeats = new Set<string>();
      const conversations = panes.flatMap((pane) => {
        if (!isConversationPaneType(pane.type)) return [];
        return [{ pane, ref: refParam(pane.params) }];
      });
      // Reserve every validated pane-specific seat across the whole layout
      // first. Otherwise an unfamiliar pane listed earlier can consume the ref
      // fallback that belongs to a known pane later in the same conversation.
      const reserved = new Set<OpenPaneRecord>();
      for (const { pane, ref } of conversations) {
        if (ref === null) continue;
        const saved = readPaneComposerSeat(pane.id, ref);
        if (saved === undefined || usedSeats.has(saved)) continue;
        pane.composerSeatId = saved;
        usedSeats.add(saved);
        reserved.add(pane);
      }
      for (const { pane, ref } of conversations) {
        if (reserved.has(pane)) continue;
        const candidates = [ref === null ? undefined : readRefComposerSeat(ref), pane.id];
        pane.composerSeatId =
          candidates.find((candidate) => candidate !== undefined && !usedSeats.has(candidate)) ?? createSecureUUID();
        usedSeats.add(pane.composerSeatId);
      }
      // Persist fallback seats first, then validated ones, so a reserved pane's
      // conversation mapping is the one left behind.
      for (const { pane, ref } of conversations) {
        if (reserved.has(pane)) continue;
        writeComposerSeat(pane.id, ref, pane.composerSeatId ?? pane.id);
      }
      for (const { pane, ref } of conversations) {
        if (!reserved.has(pane)) continue;
        writeComposerSeat(pane.id, ref, pane.composerSeatId ?? pane.id);
      }
      for (const listener of workspaceRestoreListeners) listener(panes);
      bumpPastRestoredIds(panes);
      // The saved active panel may be one the skip dropped - or the duplicate
      // this restore reconciled, in which case focus follows the conversation
      // that survived. Focus only an id that actually survived, else the
      // first survivor.
      const wantedId = savedActiveId === undefined ? undefined : (duplicates.get(savedActiveId) ?? savedActiveId);
      set({
        panes,
        focusedPaneId: panes.find((p) => p.id === wantedId)?.id ?? panes[0]?.id ?? null,
      });
      const conversationParents = migrated.conversationParents ?? {};
      for (const pane of panes) {
        const parentRef = conversationParents[pane.id];
        if (typeof parentRef !== "string" || parentRef === "") continue;
        // A legacy cascade's returned-to source conversation is not a pane of
        // its own: the migration lifted the source's parentRef onto the cascade
        // record, and Return retypes that record into the source session
        // (retypePane carries conversation parent edges to the replacement).
        // Record it here, describing the SOURCE conversation rather than the
        // drilled leaf, so the reopened conversation keeps its Back target.
        if (pane.type === "sessionZoom") {
          const sourceRef = (pane.params as { source?: { params?: { ref?: unknown } } } | null)?.source?.params?.ref;
          if (typeof sourceRef === "string" && parentRef !== sourceRef) conversationOpenParentRefs.set(pane, parentRef);
          continue;
        }
        recordConversationOpenParent(pane, parentRef);
      }
      return true;
    } catch {
      // Dockview may have partially rebuilt its grid. Clear that failed grid,
      // but keep the committed records so reconciliation can remount their
      // original views without discarding runtime source work.
      dockviewApi?.clear();
      return false;
    }
  },
}));

// Attention alone substitutes a presentation without removing its source from
// the living-record list. Lifetime disposal remains the removal subscriber's
// responsibility. A missing historical seat never displaces an ordinary main.
export function substituteAttentionPane(
  target: OpenPaneRecord | string,
  source: OpenPaneRecord | null,
  slot: Exclude<PaneSlot, "parked">,
  bind: (pane: OpenPaneRecord) => void,
): OpenPaneRecord {
  const state = workspaceStore.getState();
  // An attention transaction has runtime ownership even before a host mounts.
  // A later first desktop registration must not cold-restore over its readers.
  workspaceHasMounted = true;
  const pane: OpenPaneRecord =
    typeof target === "string" ? { id: nextPaneId("session"), type: "session", params: { ref: target }, slot } : target;
  if (source) source.slot = "parked";
  // A welcome pane in main IS main's empty state (see openPane's placement
  // rule above): the host installs it after the pane a reader left is closed,
  // so restoring that reader's main seat displaces the placeholder instead of
  // stacking beside a screen that says nothing is open. Any other real main
  // still wins, and the restored reader becomes its secondary.
  const main = state.mainPane();
  const displacesWelcome = slot === "main" && main !== null && main !== pane && main.type === "welcome";
  pane.slot = slot === "main" && main !== null && main !== pane && !displacesWelcome ? "secondary" : slot;
  const paneRef = isConversationPaneType(pane.type) ? refParam(pane.params) : null;
  if (paneRef !== null && pane.composerSeatId === undefined) {
    pane.composerSeatId = placeComposerSeat(paneRef, state.panes);
  }
  // Like retype ownership transfer, install runtime bindings before observers
  // can capture the newly committed presentation.
  bind(pane);
  const kept = displacesWelcome ? state.panes.filter((held) => held.id !== main?.id) : state.panes;
  workspaceStore.setState({
    panes: kept.includes(pane) ? [...kept] : [...kept, pane],
    focusedPaneId: pane.id,
  });
  return pane;
}

// One removal boundary covers close, primary replacement (including duplicate
// removal), restore, and reset. Host remounts only change focus/API, so retain
// these edges. Pruning never mutates panes or emits another store notification.
workspaceStore.subscribe((state, previous) => {
  if (state.panes === previous.panes) return;
  for (const [pane, origin] of transcriptOpenOrigins) {
    if (!state.panes.includes(pane) || !state.panes.includes(origin)) transcriptOpenOrigins.delete(pane);
  }
  for (const pane of conversationOpenParentRefs.keys()) {
    if (!state.panes.includes(pane)) conversationOpenParentRefs.delete(pane);
  }
  for (const pane of documentPaneStates.keys()) {
    if (!state.panes.includes(pane)) documentPaneStates.delete(pane);
  }
});

export function useWorkspaceStore(): WorkspaceStoreState;
export function useWorkspaceStore<T>(selector: (state: WorkspaceStoreState) => T): T;
export function useWorkspaceStore<T>(selector?: (state: WorkspaceStoreState) => T): T | WorkspaceStoreState {
  // Record a committed phone workspace, not a render-time cold route. Desktop
  // registers synchronously in onReady; phone has no DockviewApi to register.
  useEffect(() => {
    if (isMobileViewport()) workspaceHasMounted = true;
  }, []);
  // Not a real conditional hook call - see stores/connection.ts's own
  // useConnectionStore for the full explanation (zustand's useStore has a
  // `selector = identity` JS default param, so both arms run identically).
  // biome-ignore lint/correctness/useHookAtTopLevel: same hook both arms, JS default param not a real conditional - see stores/connection.ts
  return selector ? useStore(workspaceStore, selector) : useStore(workspaceStore);
}

// resetWorkspaceStoreForTests resets every module-private/store field to its
// initial state - workspace.ts is a singleton store (one pane list, one
// dockviewApi reference, one id counter) shared by the whole app, so
// workspace.test.ts (and DockHost.test.tsx) must reset it between tests to
// keep them isolated. No production code should ever call this (mirrors
// threads.ts's resetThreadsStoreForTests precedent).
export function resetWorkspaceStoreForTests(): void {
  dockviewApi = null;
  workspaceHasMounted = false;
  nextPaneSeq = 0;
  pendingPaneFocus.clear();
  documentPaneStates.clear();
  workspaceStore.setState({ panes: [], focusedPaneId: null });
}
