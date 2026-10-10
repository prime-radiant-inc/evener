import type { PaneProps } from "../../../shell/paneRegistry";
import { useCommittedConversationPane } from "../../session/testing/committedConversation";
import Transcript, { type TranscriptParams } from "../Transcript";

/** The real Transcript, mounted with its real shell record in isolated tests. */
export default function CommittedTranscript(props: PaneProps<TranscriptParams>) {
  useCommittedConversationPane("transcript", props.paneId, props.params.ref);
  return <Transcript {...props} />;
}
