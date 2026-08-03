import type { MouseEvent } from "react";
import { IconButton } from "../../../widgets";
import { OpenBesideIcon } from "../transcript/fileOpenBeside";
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

  function handleClick(event: MouseEvent<HTMLButtonElement>) {
    event.stopPropagation();
    openTranscript(resolvedTranscriptRef, parentRef);
  }

  return (
    <IconButton
      label="Open transcript beside"
      title="Open transcript beside"
      icon={<OpenBesideIcon />}
      variant="quiet"
      size="sm"
      onClick={handleClick}
    />
  );
}
