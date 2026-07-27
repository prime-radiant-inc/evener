import { workspaceStore } from "./workspace";

// Reusable session-placement helpers shared by AppShell and contextual callers.
export function openTopLevelSession(ref: string): void {
  workspaceStore.getState().replacePrimary("session", { ref }, ref);
}

// Keeps a nested session out of main by replacing the owner into main and then
// opening the contextual child in secondary.
export function openNestedSessionWithOwner(ref: string, ownerRef: string): void {
  const workspace = workspaceStore.getState();
  workspace.replacePrimary("session", { ref: ownerRef }, ownerRef);
  workspace.openPane("session", { ref }, { slot: "secondary" });
}
