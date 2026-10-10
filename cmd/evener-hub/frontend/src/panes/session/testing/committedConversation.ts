import { useLayoutEffect } from "react";
import { refParam } from "../../../shell/routing";
import { workspaceStore } from "../../../shell/workspace";

/** Give isolated conversation mounts the committed record supplied by the shell. */
export function useCommittedConversationPane(type: "session" | "transcript", paneId: string, ref: string): void {
  useLayoutEffect(() => {
    const { panes } = workspaceStore.getState();
    if (panes.some((pane) => pane.id === paneId && pane.type === type && refParam(pane.params) === ref)) return;
    workspaceStore.setState({
      panes: [...panes.filter((pane) => pane.id !== paneId), { id: paneId, type, params: { ref }, slot: "main" }],
    });
  }, [paneId, ref, type]);
}
