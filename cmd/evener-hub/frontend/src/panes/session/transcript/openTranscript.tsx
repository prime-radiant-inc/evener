import type { NavigationSessionLocation } from "../../../protocol/types.gen";
import * as paneActions from "../../../shell/paneActions";
import { openTopLevelSession } from "../../../shell/sessionPlacement";
import { recordTranscriptOpenOrigin, workspaceStore } from "../../../shell/workspace";
import { selectLocation } from "../../../stores/navigation/selectors";
import { navigationStore } from "../../../stores/navigation/store";
import { OpenButton } from "../../../widgets";

function transcriptRefOf(params: unknown): string | undefined {
  const ref = (params as { ref?: unknown }).ref;
  return typeof ref === "string" ? ref : undefined;
}

function sameParams(a: unknown, b: unknown): boolean {
  return JSON.stringify(a) === JSON.stringify(b);
}

// Follow only retained read-only parent context, not arbitrary focused panes.
// This also works before a child's navigation location has been fetched.
function transcriptContextRefs(ref: string): string[] {
  const panes = workspaceStore.getState().panes;
  const visited = new Set<string>();
  let current: string | undefined = ref;
  while (current !== undefined && !visited.has(current)) {
    visited.add(current);
    const pane = panes.find((pane) => pane.type === "transcript" && transcriptRefOf(pane.params) === current);
    const parentRef = (pane?.params as { parentRef?: unknown } | undefined)?.parentRef;
    current = typeof parentRef === "string" ? parentRef : undefined;
  }
  return [...visited];
}

export function transcriptContextIncludes(ref: string, ancestorRef: string): boolean {
  return transcriptContextRefs(ref).includes(ancestorRef);
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
  // Capture before canonicalization or owner placement changes selection. The
  // workspace validates this against parentRef and the surviving pane lifetime.
  const workspace = workspaceStore.getState();
  const origin = workspace.panes.find((pane) => pane.id === workspace.focusedPaneId);
  const params = parentRef === undefined ? { ref } : { ref, parentRef };
  const exactPaneId = canonicalTranscriptPane(ref, params);
  if (parentRef !== undefined) {
    const main = workspaceStore.getState().mainPane();
    const mainRef = main?.type === "session" ? transcriptRefOf(main.params) : undefined;
    const context = transcriptContextRefs(parentRef);
    // parentRef stays the immediate Back target. It is not necessarily the
    // owner: a retained transcript chain or a routed nested session identifies
    // the real main session without promoting the immediate parent into it.
    if (mainRef === undefined || !context.includes(mainRef)) {
      let ownerRef: string | undefined;
      for (const contextRef of context) {
        const location = selectLocation(contextRef)(navigationStore.getState())?.data as
          | NavigationSessionLocation
          | undefined;
        if (location) {
          ownerRef = location.top_level_ref ?? contextRef;
          break;
        }
      }
      // Without a resolved navigation location the immediate parent is only a
      // guess, and a wrong guess is destructive: replacePrimary would discard
      // a restored main session and every secondary pane to promote a nested
      // session that may not be the owner. Promote only when ownership is
      // proven (a loaded location) or when no main pane exists to destroy (the
      // orphan-child recovery path); otherwise keep the retained primary and
      // let the openBeside placement carry the child beside its origin.
      if (ownerRef !== undefined || main === null) {
        openTopLevelSession(ownerRef ?? parentRef);
      }
    }
  }
  if (exactPaneId !== undefined) {
    const retained = workspaceStore.getState().panes.find((pane) => pane.id === exactPaneId);
    if (retained) {
      recordTranscriptOpenOrigin(retained, origin);
      workspaceStore.getState().focusPane(retained.id);
      return;
    }
  }
  paneActions.openBeside({
    type: "transcript",
    params,
  });
  const opened = workspaceStore
    .getState()
    .panes.find((pane) => pane.type === "transcript" && sameParams(pane.params, params));
  if (opened) recordTranscriptOpenOrigin(opened, origin);
}

export function OpenTranscriptButton({
  transcriptRef,
  parentRef,
  label = "Open transcript",
  tabIndex,
}: {
  transcriptRef: string;
  parentRef?: string;
  label?: string;
  tabIndex?: number;
}) {
  return <OpenButton label={label} tabIndex={tabIndex} onClick={() => openTranscript(transcriptRef, parentRef)} />;
}
