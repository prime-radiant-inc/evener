import * as paneActions from "../../../shell/paneActions";
import { openTopLevelSession } from "../../../shell/sessionPlacement";
import { workspaceStore } from "../../../shell/workspace";
import { Button, IconButton } from "../../../widgets";
import { OpenBesideIcon } from "./fileOpenBeside";

function transcriptRefOf(params: unknown): string | undefined {
  const ref = (params as { ref?: unknown }).ref;
  return typeof ref === "string" ? ref : undefined;
}

function sameParams(a: unknown, b: unknown): boolean {
  return JSON.stringify(a) === JSON.stringify(b);
}

// Workspace deduplication deliberately compares the entire params bag. That
// is correct for most pane types, but a transcript's identity is its child
// ref: adding parentRef later should update the navigation context rather than
// leave two panes showing the same child. Keep one exact canonical pane when
// possible and close only same-child variants before the normal opener runs.
function canonicalTranscriptPane(ref: string, params: { ref: string; parentRef?: string }): string | undefined {
  const workspace = workspaceStore.getState();
  const sameChild = workspace.panes.filter(
    (pane) => pane.type === "transcript" && transcriptRefOf(pane.params) === ref,
  );
  const exact = sameChild.find((pane) => sameParams(pane.params, params));
  for (const pane of sameChild) {
    if (pane.id !== exact?.id) workspace.closePane(pane.id);
  }
  return exact?.id;
}

// Opens the read-only transcript surface through the workspace action so its
// desktop beside-placement, mobile fallback, and pane deduplication stay one
// behavior for every transcript link.
export function openTranscript(ref: string, parentRef?: string): void {
  const params = parentRef === undefined ? { ref } : { ref, parentRef };
  const exactPaneId = canonicalTranscriptPane(ref, params);
  if (parentRef !== undefined) openTopLevelSession(parentRef);
  if (exactPaneId !== undefined) {
    const retained = workspaceStore.getState().panes.find((pane) => pane.id === exactPaneId);
    if (retained) {
      workspaceStore.getState().focusPane(retained.id);
      return;
    }
  }
  paneActions.openBeside({
    type: "transcript",
    params,
  });
}

export function OpenTranscriptButton({
  transcriptRef,
  parentRef,
  label = "Open transcript",
  iconOnly = false,
  tabIndex,
}: {
  transcriptRef: string;
  parentRef?: string;
  label?: string;
  // iconOnly is the dense-row form: just the glyph with the accessible name,
  // for surfaces (the activity tree's first line) with no room for the word.
  iconOnly?: boolean;
  // tabIndex forwards to the underlying button: the activity tree rows are
  // their own tab stop, so their nested open glyph takes -1 there.
  tabIndex?: number;
}) {
  if (iconOnly) {
    return (
      <IconButton
        label={label}
        title={label}
        tabIndex={tabIndex}
        icon={<OpenBesideIcon />}
        variant="quiet"
        size="xs"
        onClick={(event) => {
          event.stopPropagation();
          openTranscript(transcriptRef, parentRef);
        }}
      />
    );
  }
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
