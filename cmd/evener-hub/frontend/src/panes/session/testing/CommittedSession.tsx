import type { PaneProps } from "../../../shell/paneRegistry";
import Session, { type SessionPaneParams } from "../Session";
import { useCommittedConversationPane } from "./committedConversation";

/** Give isolated Session tests the committed record normally supplied by the shell. */
export function CommittedSession(props: PaneProps<SessionPaneParams>) {
  const { paneId, params } = props;
  useCommittedConversationPane("session", paneId, params.ref);
  return <Session {...props} />;
}
