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
// and deliberately omits live flow-overlay/new-content-pill machinery.
import { useEffect, useMemo, useRef, useState } from "react";
import { useStore } from "zustand";
import type { PaneProps } from "../../shell/paneRegistry";
import { navigate, paneToURL } from "../../shell/routing";
import { connectionStore } from "../../stores/connection";
import { threadsStore } from "../../stores/threads";
import { transcriptDisplayStore } from "../../stores/transcriptDisplay";
import { resolveEffectiveConfig } from "../../transcriptDisplay/config";
import { EmptyState, PaneScaffold, type VirtualListHandle } from "../../widgets";
import { VisuallyHidden } from "../../widgets/visuallyHidden";
import { NOW_TICK_MS, SessionNowContext, useNowTick } from "../session/liveness";
import { LoadOlderRow } from "../session/transcript/flow/LoadOlderRow";
import { TranscriptBody } from "../session/transcript/TranscriptBody";
import { TranscriptDetailControl } from "../session/transcript/TranscriptDetailControl";
import { useTranscript } from "../session/transcript/useTranscript";
import { JobLog } from "./JobLog";

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
    return <JobLog jobRef={params.ref} parentRef={params.parentRef} />;
  }
  return <ThreadTranscript params={params} paneId={paneId} />;
}

function ThreadTranscript({ params, paneId }: { params: TranscriptParams; paneId?: string }) {
  const { ref } = params;
  const now = useNowTick(NOW_TICK_MS);

  // ensureThread on mount / releaseThread on unmount, deferred until the one
  // client is actually ready - a deep-linked open can reach this effect before
  // the handshake finishes, and request() rejects until then. Exactly the
  // SessionPane lifecycle (see its own comment); once the single attempt has
  // fired it is never repeated (a reconnect blip is the store's own concern).
  useEffect(() => {
    let started = false;
    const tryStart = () => {
      if (started || connectionStore.getState().state !== "ready") return;
      started = true;
      threadsStore
        .getState()
        .ensureThread(ref)
        .catch(() => {});
    };
    tryStart();
    const unsubscribe = connectionStore.subscribe(tryStart);
    return () => {
      unsubscribe();
      if (started) threadsStore.getState().releaseThread(ref);
    };
  }, [ref]);

  // Older-turn paging is automatic here too (LoadOlderRow's own sentinel), and
  // reports a failed page inline with a Retry rather than as a toast - see
  // Session.tsx's own comment on the same wiring.
  const { model, loadingOlder, loadOlderReportingError, olderError } = useTranscript(ref);
  const listRef = useRef<VirtualListHandle>(null);
  const detailTriggerRef = useRef<HTMLButtonElement>(null);
  const announcementSequence = useRef(0);
  const [viewAnnouncement, setViewAnnouncement] = useState({ text: "", key: 0 });

  // Open at the latest turn once, when content first arrives. anchorToEnd on
  // the VirtualList below keeps the viewport pinned to the TRUE end while the
  // initial estimate->measured correction settles (and follows later appends
  // only while the reader stays at the end) - without it this one-shot scroll
  // lands at the ESTIMATED end and strands the reader mid-transcript. The ref
  // guard keeps it a one-shot: a later "load older" prepend doesn't yank the
  // view back to the bottom (and the list's own end-anchor keeps a prepend
  // visually anchored either way).
  const turnCount = model?.turns.length ?? 0;
  const didInitialScrollRef = useRef(false);
  useEffect(() => {
    if (!didInitialScrollRef.current && turnCount > 0) {
      didInitialScrollRef.current = true;
      listRef.current?.scrollToIndex(turnCount - 1, { align: "end" });
    }
  }, [turnCount]);

  const displayViewport = useStore(transcriptDisplayStore, (state) => state.viewport);
  const displayLocal = useStore(transcriptDisplayStore, (state) => state.local[displayViewport]);
  const displayHub = useStore(transcriptDisplayStore, (state) => state.hub[displayViewport]);
  const displayConfig = useMemo(
    () => resolveEffectiveConfig({ local: displayLocal, hub: displayHub, layout: displayViewport }),
    [displayHub, displayLocal, displayViewport],
  );

  if (!model) {
    return (
      <PaneScaffold title={ref}>
        <EmptyState title="Loading transcript…" />
      </PaneScaffold>
    );
  }

  const content = (
    <PaneScaffold title={model.name || ref}>
      {model.turns.length === 0 ? (
        <EmptyState title="No turns yet" hint="This thread hasn't sent or received anything yet." />
      ) : (
        <TranscriptBody
          model={model}
          config={displayConfig}
          surface="readOnly"
          disclosureScope={`transcript:readOnly:${ref}`}
          sessionRef={ref}
          viewId={paneId}
          detailTriggerRef={detailTriggerRef}
          toolbar={
            <TranscriptDetailControl
              layout={displayViewport}
              triggerRef={detailTriggerRef}
              onEditHubDefaults={() => {
                const url = paneToURL("settings", { section: "transcript" });
                if (url !== null) navigate(url);
              }}
            />
          }
          onAnnounceViewChange={(summary) => {
            announcementSequence.current += 1;
            setViewAnnouncement({ text: `Transcript detail: ${summary}`, key: announcementSequence.current });
          }}
          loadOlderRow={
            model.olderCursor && (
              <LoadOlderRow onLoad={loadOlderReportingError} loading={loadingOlder} error={olderError} />
            )
          }
          listRef={listRef}
        />
      )}
      <div role="status" aria-live="polite" data-testid="transcript-view-announcement">
        <VisuallyHidden key={viewAnnouncement.key}>{viewAnnouncement.text}</VisuallyHidden>
      </div>
    </PaneScaffold>
  );
  return <SessionNowContext.Provider value={now}>{content}</SessionNowContext.Provider>;
}
