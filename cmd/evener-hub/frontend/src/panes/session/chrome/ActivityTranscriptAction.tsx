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

  // The standard open affordance (widgets/openbutton) owns the glyph and the
  // stopPropagation; this wrapper only wires it to transcript navigation.
  return (
    <OpenButton
      iconOnly
      size="sm"
      label="Open transcript beside"
      onClick={() => openTranscript(resolvedTranscriptRef, parentRef)}
    />
  );
}
