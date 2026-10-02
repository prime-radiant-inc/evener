import { useLayoutEffect } from "react";
import type { PaneProps } from "../../../shell/paneRegistry";
import { refParam } from "../../../shell/routing";
import { workspaceStore } from "../../../shell/workspace";
import Session, { type SessionPaneParams } from "../Session";

/** Give isolated Session tests the committed record normally supplied by the shell. */
export function CommittedSession(props: PaneProps<SessionPaneParams>) {
  const { paneId, params } = props;
  useLayoutEffect(() => {
    const { panes } = workspaceStore.getState();
    if (panes.some((pane) => pane.id === paneId && pane.type === "session" && refParam(pane.params) === params.ref))
      return;
    workspaceStore.setState({
      panes: [
        ...panes.filter((pane) => pane.id !== paneId),
        { id: paneId, type: "session", params: { ref: params.ref }, slot: "main" },
      ],
    });
  }, [paneId, params.ref]);
  return <Session {...props} />;
}
