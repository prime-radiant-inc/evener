// The read-only transcript pane (wave 8 T6). It renders ANOTHER thread's
// transcript for open-beside viewing (a subagent "open transcript" row, floor
// §3.7) - the M4 transcript engine in read-only mode: the SAME useTranscript /
// TurnBlock / item + tool renderers the live session pane uses, with NO
// composer, NO pending chips, NO session chrome. It is a DISTINCT surface from
// /thread/{ref}'s single-pane mode (which renders the live SESSION pane); this
// one has no URL (routing.ts's paneToURL returns null for it) and is reached
// only contextually via openBeside.
//
// It hydrates through the SAME refcounted threads store as the session pane
// (ensureThread/releaseThread, thread/read - no new data path), so a session
// pane and a transcript pane open on the same ref share one ThreadModel.
//
// Both live and read-only panes now hand their hydrated model to the shared
// TranscriptBody. The read-only surface injects only its older-row affordance
// and deliberately omits live flow-overlay/new-content-pill machinery - but it
// runs the SAME scroll coordinator (useTranscriptScroll) the live pane does,
// so landing at the latest, near-top paging, following a prepend, and the
// geometry fill behind a too-short page all behave identically on both
// surfaces instead of drifting apart in a second implementation.
import { useStore } from "zustand";
import { conversationPaneLifetime } from "../../shell/paneLifetime";
import type { PaneProps } from "../../shell/paneRegistry";
import { ScopeCrumbs } from "../../shell/statusbar/ScopeCrumbs";
import { deriveScope } from "../../shell/statusbar/statusScope";
import { workspaceStore } from "../../shell/workspace";
import { navigationStore } from "../../stores/navigation/store";
import { useSessionActivity } from "../../stores/sessionActivity";
import { useThreadsStore } from "../../stores/threads";
import { EmptyState, PaneScaffold } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import { retainedTranscriptReadView } from "../session/transcript/transcriptReadView";
import { JobLog } from "./JobLog";
import { ReadOnlyThreadContent } from "./ReadOnlyThreadContent";
import styles from "./transcript.module.css";

const CLASS = {
  body: requireClass(styles.body, "transcript.module.css", "body"),
  list: requireClass(styles.list, "transcript.module.css", "list"),
  scope: requireClass(styles.scope, "transcript.module.css", "scope"),
};

export interface TranscriptParams {
  ref: string;
  // The enclosing session's ref, when this pane was opened from a subagent
  // row (subagentModule.tsx's openTranscript - the only producer today).
  // Carried in the pane's OWN params - not passed around as a one-off
  // argument - so it survives a layout restore/reload. A "job:<id>" ref
  // resolves its owning session from it (JobLog fetches output through the
  // owner, never through thread/read). Undefined for a hypothetical future
  // producer with no enclosing session.
  parentRef?: string;
}

export default function Transcript({ params, paneId }: PaneProps<TranscriptParams>) {
  // A "job:<id>" ref is a shell job's output log, not a thread: it renders
  // through the job-log surface, which never touches the thread engine (no
  // thread/read, no ensureThread). Refs never change for a mounted pane, so
  // this dispatch is stable for the component's lifetime.
  if (params.ref.startsWith("job:")) {
    return <JobLog jobRef={params.ref} parentRef={params.parentRef} paneId={paneId} />;
  }
  return <ThreadTranscript params={params} paneId={paneId} />;
}

function ThreadTranscript({ params, paneId }: { params: TranscriptParams; paneId?: string }) {
  const { ref } = params;
  const pane = useStore(workspaceStore, (state) =>
    state.panes.find(
      (record) =>
        record.id === paneId && record.type === "transcript" && (record.params as TranscriptParams).ref === ref,
    ),
  );
  const view = pane ? retainedTranscriptReadView(conversationPaneLifetime(pane), ref, "transcript") : undefined;
  const model = useThreadsStore((state) => state.threads.get(ref));
  const { snapshot: activity } = useSessionActivity(ref);
  // Only the crumbs are shown here, so no subagent count is read.
  const scope = deriveScope(navigationStore.getState(), ref, activity, null);
  return (
    <PaneScaffold title={model?.name || ref}>
      <div className={CLASS.body}>
        {scope.ancestryKnown && scope.path.length > 1 && (
          <div className={CLASS.scope}>
            <ScopeCrumbs path={scope.path} hierarchy />
          </div>
        )}
        <div className={CLASS.list}>
          {view ? <ReadOnlyThreadContent ref={ref} view={view} /> : <EmptyState title="Loading transcript…" />}
        </div>
      </div>
    </PaneScaffold>
  );
}
