import { navigate, paneToURL } from "./routing";
import { workspaceStore } from "./workspace";

// openSessionByRef is how anything in the app follows a link to a session.
// It routes through the URL rather than placing a pane itself, so the
// router's own rules apply to every such link: a nested session opens beside
// its top-level owner instead of landing in main next to an unrelated root
// (see openRouteAsPane). Callers that already know the placement they want
// use the two helpers below directly.
export function openSessionByRef(ref: string): void {
  const url = paneToURL("session", { ref });
  if (url) navigate(url);
}

// Reusable session-placement helpers shared by AppShell and contextual callers.
export function openTopLevelSession(ref: string): void {
  workspaceStore.getState().replacePrimary("session", { ref });
}

// Keeps a nested session out of main by replacing the owner into main and then
// opening the contextual child in secondary.
export function openNestedSessionWithOwner(ref: string, ownerRef: string): void {
  const workspace = workspaceStore.getState();
  workspace.replacePrimary("session", { ref: ownerRef });
  workspace.openPane("session", { ref }, { slot: "secondary" });
}
