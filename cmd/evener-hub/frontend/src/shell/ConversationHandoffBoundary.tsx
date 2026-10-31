import { type ReactNode, useEffect } from "react";
import { completeConversationHandoffPane } from "../stores/conversationClaimHandoff";
import { type PanePanelParams, workspaceStore } from "./workspace";

/** Inside Suspense, so this passive acknowledgment follows descendant claims. */
export function ConversationHandoffBoundary({
  generation,
  paneId,
  renderedPane,
  children,
}: {
  generation: number | undefined;
  paneId: string;
  renderedPane?: PanePanelParams;
  children: ReactNode;
}) {
  useEffect(() => {
    if (renderedPane) {
      const live = workspaceStore.getState().panes.find((pane) => pane.id === paneId);
      // Restored Dockview contents can commit before live intent is pushed.
      // Reconciliation supplies the exact live params object and retries here.
      if (live?.type !== renderedPane.paneType || live.params !== renderedPane.paneParams) return;
    }
    completeConversationHandoffPane(generation, paneId);
  }, [generation, paneId, renderedPane]);
  return children;
}
