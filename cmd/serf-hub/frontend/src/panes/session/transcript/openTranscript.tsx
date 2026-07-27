import * as paneActions from "../../../shell/paneActions";
import { openTopLevelSession } from "../../../shell/sessionPlacement";
import { Button } from "../../../widgets";
import { OpenBesideIcon } from "./fileOpenBeside";

// Opens the read-only transcript surface through the workspace action so its
// desktop beside-placement, mobile fallback, and pane deduplication stay one
// behavior for every transcript link.
export function openTranscript(ref: string, parentRef?: string): void {
  if (parentRef !== undefined) openTopLevelSession(parentRef);
  paneActions.openBeside({
    type: "transcript",
    params: parentRef === undefined ? { ref } : { ref, parentRef },
  });
}

export function OpenTranscriptButton({
  transcriptRef,
  parentRef,
  label = "Open transcript",
}: {
  transcriptRef: string;
  parentRef?: string;
  label?: string;
}) {
  return (
    <Button
      variant="quiet"
      size="sm"
      aria-label={label}
      onClick={(event) => {
        event.stopPropagation();
        openTranscript(transcriptRef, parentRef);
      }}
    >
      open
      <OpenBesideIcon />
    </Button>
  );
}
