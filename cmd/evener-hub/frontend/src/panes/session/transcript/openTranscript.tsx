import type { NavigationSessionLocation } from "@evener/appwire-client";
import * as paneActions from "../../../shell/paneActions";
import { openTopLevelSession } from "../../../shell/sessionPlacement";
import {
  conversationOpenParentRef,
  presentedPanes,
  recordConversationOpenParent,
  recordTranscriptOpenOrigin,
  workspaceStore,
} from "../../../shell/workspace";
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

// Follow retained conversation parent context, then any retained read-only
// parent context that chain carries. A conversation pane keeps its parent
// beside the pane lifetime instead of in params, so the walk alternates the
// two records. This also works before a child's navigation location has been
// fetched.
function conversationContextRefs(ref: string): string[] {
  const panes = workspaceStore.getState().panes;
  const presented = presentedPanes(panes);
  const visited = new Set<string>();
  let current: string | undefined = ref;
  while (current !== undefined && !visited.has(current)) {
    visited.add(current);
    const holder =
      presented.find(
        (pane) => (pane.type === "session" || pane.type === "transcript") && transcriptRefOf(pane.params) === current,
      ) ??
      panes.find(
        (pane) => (pane.type === "session" || pane.type === "transcript") && transcriptRefOf(pane.params) === current,
      );
    if (holder === undefined) break;
    const parentParam = (holder.params as { parentRef?: unknown }).parentRef;
    current = conversationOpenParentRef(holder) ?? (typeof parentParam === "string" ? parentParam : undefined);
  }
  return [...visited];
}

export function conversationContextIncludes(ref: string, ancestorRef: string): boolean {
  return conversationContextRefs(ref).includes(ancestorRef);
}

// Workspace deduplication deliberately compares the entire params bag. That
// is correct for most pane types, but a job log's identity is its job ref:
// adding parentRef later should update the navigation context rather than
// leave two panes showing the same job. Keep one exact canonical pane when
// possible and close only same-job variants before the normal opener runs.
function canonicalTranscriptPane(ref: string, params: { ref: string; parentRef?: string }): string | undefined {
  const workspace = workspaceStore.getState();
  const sameChild = presentedPanes(workspace.panes).filter(
    (pane) => pane.type === "transcript" && transcriptRefOf(pane.params) === ref,
  );
  const exact = sameChild.find((pane) => sameParams(pane.params, params));
  for (const pane of sameChild) {
    if (pane.id !== exact?.id) workspace.closePane(pane.id);
  }
  return exact?.id;
}

// Opens the conversation a row points at. A session ref opens the live
// conversation pane (the read-only transcript surface no longer exists); a
// "job:" ref keeps the job-log transcript pane. Both go through the
// workspace action so desktop beside-placement, mobile fallback, and pane
// deduplication stay one behavior for every link.
export function openTranscript(ref: string, parentRef?: string): void {
  // Capture before owner placement changes selection. The workspace validates
  // this against parentRef and the surviving pane lifetime.
  const workspace = workspaceStore.getState();
  const origin = presentedPanes(workspace.panes).find((pane) => pane.id === workspace.focusedPaneId);
  const params = parentRef === undefined ? { ref } : { ref, parentRef };
  if (parentRef !== undefined) {
    const main = workspaceStore.getState().mainPane();
    const mainRef = main?.type === "session" ? transcriptRefOf(main.params) : undefined;
    const context = conversationContextRefs(parentRef);
    // parentRef stays the immediate Back target. It is not necessarily the
    // owner: a retained transcript chain or a routed nested session identifies
    // the real main session without promoting the immediate parent into it.
    if (mainRef === undefined || !context.includes(mainRef)) {
      let ownerRef: string | undefined;
      for (const contextRef of context) {
        const resource = selectLocation(contextRef)(navigationStore.getState());
        // During a reconnect or generation change the store retains the
        // previous payload while marking it stale, so an obsolete
        // top_level_ref could promote the wrong session. Require settled,
        // non-stale, error-free data before trusting the location.
        if (
          resource?.data &&
          !resource.stale &&
          !resource.loading &&
          resource.error === null &&
          resource.loadedRevision !== null
        ) {
          const location = resource.data as NavigationSessionLocation;
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

  // A job ref is a shell job's output log, not a thread: it keeps the
  // job-log transcript pane and its owner-context origin recording.
  if (ref.startsWith("job:")) {
    const exactPaneId = canonicalTranscriptPane(ref, params);
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
    const opened = presentedPanes(workspaceStore.getState().panes).find(
      (pane) => pane.type === "transcript" && sameParams(pane.params, params),
    );
    if (opened) recordTranscriptOpenOrigin(opened, origin);
    return;
  }

  // A session ref opens its live conversation pane: reuse the open one for
  // this ref, else open it beside the current selection. The conversation
  // pane's identity is its ref, so no parent-context variants exist to
  // canonicalize.
  const existing = presentedPanes(workspaceStore.getState().panes).find(
    (pane) => pane.type === "session" && transcriptRefOf(pane.params) === ref,
  );
  if (existing) {
    recordConversationOpenParent(existing, parentRef);
    workspaceStore.getState().focusPane(existing.id);
    return;
  }
  paneActions.openBeside({
    type: "session",
    params: { ref },
  });
  const opened = presentedPanes(workspaceStore.getState().panes).find(
    (pane) => pane.type === "session" && transcriptRefOf(pane.params) === ref,
  );
  if (opened) recordConversationOpenParent(opened, parentRef);
}

// The row control names the view it opens: the conversation for a session,
// the transcript for a job log.
export function OpenTranscriptButton({
  transcriptRef,
  parentRef,
  label,
  tabIndex,
}: {
  transcriptRef: string;
  parentRef?: string;
  label?: string;
  tabIndex?: number;
}) {
  return (
    <OpenButton
      label={label ?? (transcriptRef.startsWith("job:") ? "Open transcript" : "Open conversation")}
      tabIndex={tabIndex}
      onClick={() => openTranscript(transcriptRef, parentRef)}
    />
  );
}
