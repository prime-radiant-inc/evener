import { OpenButton } from "../../../widgets";
import { openTranscript } from "../transcript/openTranscript";

export function ActivityTranscriptAction({
  transcriptRef,
  parentRef,
}: {
  transcriptRef: string | undefined;
  parentRef?: string;
}) {
  const trimmedTranscriptRef = transcriptRef?.trim();
  if (!trimmedTranscriptRef) return null;
  const resolvedTranscriptRef = trimmedTranscriptRef;

  return (
    <OpenButton
      iconOnly
      size="sm"
      label="Open transcript beside"
      onClick={() => openTranscript(resolvedTranscriptRef, parentRef)}
    />
  );
}
