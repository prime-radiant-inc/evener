import { zoomIntentOf } from "../panes/zoom/intent";
import { navigate, paneToURL, refParam } from "./routing";
import {
  cancelCarriedComposerSeat,
  type OpenPaneRecord,
  presentedPanes,
  recordConversationOpenParent,
  workspaceStore,
} from "./workspace";

// A session pane's identity is the ref in its own params, derived here rather
// than spelled by the caller so the type and the ref cannot diverge.
function isSessionPaneFor(pane: OpenPaneRecord, ref: string): boolean {
  return pane.type === "session" && refParam(pane.params) === ref;
}

// A cascade presents its source conversation; placement must see that role
// without losing the original record's identity or its parent metadata.
function sessionRouteRole(pane: OpenPaneRecord): OpenPaneRecord {
  if (pane.type !== "sessionZoom") return pane;
  const params = zoomIntentOf(pane);
  return params !== null && !params.inspection ? { ...pane, ...params.source } : pane;
}

/** Placement facts the caller already holds about a session link. */
export interface SessionOpenOptions {
  /** The caller's own row says this ref names a top-level session (the rail's
   * navigation summary carries the session kind). */
  readonly topLevel?: boolean;
}

// openSessionByRef is how anything in the app follows a link to a session.
// Existing live session panes retain their identity and regain focus.
// Read-only transcript context belongs to its contextual callers.
// Unopened sessions route through the URL, so the
// router's own rules apply to every such link: a nested session opens beside
// its top-level owner instead of landing in main next to an unrelated root
// (see openRouteAsPane). Callers that already know the placement they want
// use the two helpers below directly.
//
// A caller that also knows the link is top-level (the rail row's kind) says so
// through `topLevel`: the session is then placed here, without waiting for the
// route's location read. That read is a hub round trip, and leaving placement
// behind it made a press look ignored while the read was in flight. The route
// effect still confirms the placement when the read answers, and a stale hint
// is corrected through the ordinary nested-owner path.
//
// Every other unopened link opens its pane here too, non-destructively; the
// route then nests it beside the owner the location read names. Without this,
// a nested row or palette link sat ignored for the length of that read.
export function openSessionByRef(ref: string, options: SessionOpenOptions = {}): void {
  const workspace = workspaceStore.getState();
  const existing = presentedPanes(workspace.panes).find((pane) => isSessionPaneFor(sessionRouteRole(pane), ref));
  const url = paneToURL("session", { ref });
  // Publish the ordinary route BEFORE touching the workspace. Replacing or
  // focusing a pane can retire an attention-owned history entry, and that
  // retirement navigates with replace; doing it first would turn this press's
  // push into a replacement, so Back would skip the session just left.
  if (url) navigate(url, { ordinary: true });
  if (existing) {
    workspace.focusPane(existing.id);
  } else if (options.topLevel === true) {
    openTopLevelSession(ref);
  } else {
    workspace.openPane("session", { ref });
  }
}

// A legacy cascade retains its original route role: opening the session a cascade
// zooms on must preserve the cascade and its neighboring panes, not replace
// the workspace with a plain pane of the same session. replacePrimary cannot
// express that - its match is deliberately type-strict (a sessionZoom is not
// a session, so a match would rewrite the zoom's params into plain session
// params) - so the rule lives at this seam, which every top-level session
// route placement funnels through: DockHost's boot re-apply, AppShell's
// deferred deep link (a location read that resolves after boot restore - the
// boot shape a loaded machine produces, where the deferred placement used to
// discard the whole restored workspace), and every later placement of the
// same route.
function cascadeMainCoversRef(ref: string): boolean {
  const main = workspaceStore.getState().mainPane();
  const params = main === null ? null : zoomIntentOf(main);
  return params !== null && !params.inspection && params.source.params.ref === ref;
}

// Reusable session-placement helpers shared by AppShell and contextual callers.
export function openTopLevelSession(ref: string): void {
  if (cascadeMainCoversRef(ref)) {
    const workspace = workspaceStore.getState();
    // The duplicate contract of replacePrimary's matching arm, with the
    // cascade owning the route's main slot: a plain session pane for the same
    // ref elsewhere in the workspace is the duplicate, and closes instead of
    // the cascade. Closing a focused duplicate nulls focus (closePane's own
    // rule), but this branch never redirects focus, so a restored layout's
    // saved focus survives the placement.
    // Retaining the cascade in main is the placement decision for the
    // route's conversation: a pending carry dies with it.
    cancelCarriedComposerSeat(ref);
    for (const pane of workspace.panes) {
      if (isSessionPaneFor(pane, ref)) workspace.closePane(pane.id);
    }
    return;
  }
  workspaceStore.getState().replacePrimary("session", { ref });
}

// Keeps a nested session out of main by putting the owner in main and the
// contextual child in secondary. A child already open - the rail's immediate
// placement, before the location read confirms ownership - moves beside the
// owner instead of being recreated, preserving its per-mount work.
export function openNestedSessionWithOwner(ref: string, ownerRef: string): void {
  const workspace = workspaceStore.getState();
  const childRecord = presentedPanes(workspace.panes).find((pane) => isSessionPaneFor(sessionRouteRole(pane), ref));
  const child = childRecord === undefined ? undefined : sessionRouteRole(childRecord);
  if (child !== undefined) {
    cancelCarriedComposerSeat(ref);
    // Ownership is now confirmed nested, so keep the child - and its per-mount
    // composer work, including staged images - and move the owner in beside it
    // instead of replacing the workspace and remounting the child.
    const main = workspace.mainPane();
    const ownerPane = presentedPanes(workspace.panes).find((pane) => isSessionPaneFor(pane, ownerRef));
    const ownerRole = presentedPanes(workspace.panes).find((pane) =>
      isSessionPaneFor(sessionRouteRole(pane), ownerRef),
    );
    const ownerIsMain = ownerRole !== undefined && ownerRole.id === main?.id;
    const cascadeMain = cascadeMainCoversRef(ownerRef);
    if (ownerIsMain || cascadeMain) {
      // The owner or its cascade already owns main. Keep every neighboring
      // pane; only a duplicate plain owner beside a cascade retires.
      if (cascadeMain && ownerPane !== undefined) workspace.closePane(ownerPane.id);
      workspace.focusPane(child.id);
      const retained = workspaceStore.getState().panes.find((pane) => pane.id === child.id);
      if (retained) recordConversationOpenParent(retained, ownerRef);
      // Focus and the recorded parent edge are not store-visible when nothing
      // moved, so publish the unchanged records for route reconciliation.
      workspaceStore.setState({ panes: [...workspaceStore.getState().panes] });
      return;
    }
    // An unrelated primary still clears. Keep the child and an existing owner
    // pane, then promote the owner.
    for (const pane of [...workspace.panes]) {
      if (pane.id !== child.id && pane.id !== ownerPane?.id) workspace.closePane(pane.id);
    }
    const ownerId = ownerPane?.id ?? workspace.openPane("session", { ref: ownerRef }, { slot: "secondary" });
    workspace.promotePane(ownerId);
    workspace.focusPane(child.id);
    const opened = workspaceStore.getState().panes.find((pane) => pane.id === child.id);
    if (opened) recordConversationOpenParent(opened, ownerRef);
    return;
  }
  openTopLevelSession(ownerRef);
  const openedId = workspaceStore.getState().openPane("session", { ref }, { slot: "secondary" });
  const opened = workspaceStore.getState().panes.find((pane) => pane.id === openedId);
  if (opened) recordConversationOpenParent(opened, ownerRef);
}
