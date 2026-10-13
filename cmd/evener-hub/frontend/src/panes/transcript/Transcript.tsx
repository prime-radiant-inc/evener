// The job-log pane. A "job:<id>" ref is a shell job's output log, not a
// thread: it renders through the job-log surface, which never touches the
// thread engine (no thread/read, no ensureThread). The read-only session
// transcript this pane once rendered is gone — the session pane is the
// conversation view and the zoom cascade embeds it — so a legacy session-ref
// transcript pane (a stale saved layout or a URL the workspace migration
// could not rewrite) recovers as the live conversation rather than losing
// the view.
import { useEffect } from "react";
import { useStore } from "zustand";
import type { PaneProps } from "../../shell/paneRegistry";
import { refParam } from "../../shell/routing";
import { recordConversationOpenParent, workspaceStore } from "../../shell/workspace";
import Session from "../session/Session";
import { JobLog } from "./JobLog";

export interface TranscriptParams {
  ref: string;
  // The enclosing session's ref, when this pane was opened from a job row.
  // Carried in the pane's own params - not passed around as a one-off
  // argument - so it survives a layout restore/reload. A "job:<id>" ref
  // resolves its owning session from it (JobLog fetches output through the
  // owner, never through thread/read).
  parentRef?: string;
}

export default function Transcript({ params, paneId, focused }: PaneProps<TranscriptParams>) {
  const job = params.ref.startsWith("job:");

  // Session resolves its lifetime — the composer, the retained reader, the
  // pane-owned scroll — only from a workspace record typed "session". A
  // session-ref record can still arrive typed "transcript": a stale saved
  // layout the migration could not rewrite, or a cascade Return that retypes
  // its pane back to its legacy source. When this pane is the ref's only
  // conversation view it is the view that must carry the live surfaces, so
  // migrate the record before Session reads it, so the conversation recovers
  // live rather than as dead history. A conversation pane's identity is its ref
  // alone, so migrating beside a surviving session pane for the same ref would
  // put two panes on one conversation — the duplicate the restore-time
  // reconciliation exists to prevent; leave it to that pane in that case.
  //
  // "Alone" is a SUBSCRIPTION, not a one-time read: a legacy cascade can Return
  // onto a ref that is already open elsewhere, and the conflicting pane can
  // leave the workspace afterwards (the user closes it). Re-evaluating against
  // the current membership is what recovers the selected view's composer then;
  // a mount-time read would strand it typed transcript with no composer. The
  // selector returns only this boolean, so unrelated pane changes do not
  // re-render the pane.
  const alone = useStore(
    workspaceStore,
    (state) =>
      !state.panes.some(
        (pane) =>
          pane.id !== paneId &&
          (pane.type === "session" || pane.type === "transcript") &&
          refParam(pane.params) === params.ref,
      ),
  );

  // Runs after commit (never during render, which would update the subscribing
  // host mid-render) and is guarded on the record still being a transcript, so
  // it migrates once and cannot loop. The parent context the legacy params
  // carried moves to the conversation edge the saved-layout migration records
  // it on.
  useEffect(() => {
    if (job || !alone) return;
    const workspace = workspaceStore.getState();
    const record = workspace.panes.find((pane) => pane.id === paneId);
    if (record?.type !== "transcript") return;
    workspace.retypePane(record, "session", { ref: params.ref });
    const migrated = workspaceStore.getState().panes.find((pane) => pane.id === paneId);
    if (migrated) recordConversationOpenParent(migrated, params.parentRef);
  }, [job, alone, paneId, params.ref, params.parentRef]);

  if (job) {
    return <JobLog jobRef={params.ref} parentRef={params.parentRef} paneId={paneId} />;
  }
  return <Session focused={focused} params={{ ref: params.ref }} paneId={paneId} />;
}
