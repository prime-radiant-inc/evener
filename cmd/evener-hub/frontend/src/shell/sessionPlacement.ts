import { parseZoomParams } from "../panes/zoom/intent";
import { navigate, paneToURL, refParam } from "./routing";
import { workspaceStore } from "./workspace";

// openSessionByRef is how anything in the app follows a link to a session.
// Existing live session panes retain their identity and regain focus.
// Read-only transcript context belongs to its contextual callers.
// Unopened sessions route through the URL, so the
// router's own rules apply to every such link: a nested session opens beside
// its top-level owner instead of landing in main next to an unrelated root
// (see openRouteAsPane). Callers that already know the placement they want
// use the two helpers below directly.
export function openSessionByRef(ref: string): void {
  const workspace = workspaceStore.getState();
  const existing = workspace.panes.find((pane) => pane.type === "session" && refParam(pane.params) === ref);
  if (existing) {
    workspace.focusPane(existing.id);
  }
  const url = paneToURL("session", { ref });
  if (url) navigate(url);
}

// A cascade retains its original route role: opening the session a cascade
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
function cascadeMainFor(ref: string) {
  const main = workspaceStore.getState().mainPane();
  if (main?.type !== "sessionZoom") return null;
  const source = parseZoomParams(main.params)?.source;
  return source?.type === "session" && source.params.ref === ref ? main : null;
}

// Reusable session-placement helpers shared by AppShell and contextual callers.
export function openTopLevelSession(ref: string): void {
  const cascade = cascadeMainFor(ref);
  if (cascade) {
    const workspace = workspaceStore.getState();
    // The duplicate contract of replacePrimary's matching arm, with the
    // cascade owning the route's main slot: a plain session pane for the same
    // ref elsewhere in the workspace is the duplicate, and closes instead of
    // the cascade. Closing a focused duplicate nulls focus (closePane's own
    // rule), but this branch never redirects focus, so a restored layout's
    // saved focus survives the placement.
    for (const pane of workspace.panes) {
      if (pane.type === "session" && refParam(pane.params) === ref) workspace.closePane(pane.id);
    }
    return;
  }
  workspaceStore.getState().replacePrimary("session", { ref });
}

// Keeps a nested session out of main by replacing the owner into main and then
// opening the contextual child in secondary.
export function openNestedSessionWithOwner(ref: string, ownerRef: string): void {
  openTopLevelSession(ownerRef);
  workspaceStore.getState().openPane("session", { ref }, { slot: "secondary" });
}
