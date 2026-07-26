import { workspaceStore } from "./workspace";

interface SessionPane {
  id: string;
  type: string;
  params: unknown;
  slot: "main" | "secondary";
}

function sessionRefOf(pane: Pick<SessionPane, "params">): string | null {
  const ref = (pane.params as { ref?: unknown }).ref;
  return typeof ref === "string" ? ref : null;
}

// Reusable session-placement helpers shared by AppShell and the rail.
// They intentionally close first and open again to respect workspace's
// assign-once slot contract (no in-place move); a helper cannot "move"
// a pane from one slot to another.
export function openTopLevelSession(ref: string): void {
  const workspace = workspaceStore.getState();
  const existing = workspace.panes.find((pane) => pane.type === "session" && sessionRefOf(pane) === ref);
  if (existing && existing.slot === "secondary") {
    workspace.closePane(existing.id);
  }

  const main = workspace.mainPane();
  if (main && (main.type !== "session" || sessionRefOf(main) !== ref)) {
    workspace.closePane(main.id);
  }
  workspace.openPane("session", { ref });
}

// Keeps a nested session out of main by ensuring:
// - if the target session is stuck in main, close it first
// - if main is unrelated, replace it with the known owner
// - open the target in secondary
export function openNestedSessionWithOwner(ref: string, ownerRef: string | null): void {
  const workspace = workspaceStore.getState();

  const stuckInMain = workspace.panes.find(
    (pane) => pane.slot === "main" && pane.type === "session" && sessionRefOf(pane) === ref,
  );
  if (stuckInMain) workspace.closePane(stuckInMain.id);

  if (ownerRef !== null && ownerRef !== ref) {
    const existingOwner = workspace.panes.find(
      (pane) => pane.type === "session" && pane.slot === "secondary" && sessionRefOf(pane) === ownerRef,
    );
    if (existingOwner) workspace.closePane(existingOwner.id);

    const main = workspace.mainPane();
    if (main && (main.type !== "session" || sessionRefOf(main) !== ownerRef)) {
      workspace.closePane(main.id);
    }
    workspace.openPane("session", { ref: ownerRef }, { keepExistingFocus: true });
  }
  workspace.openPane("session", { ref }, { slot: "secondary" });
}
