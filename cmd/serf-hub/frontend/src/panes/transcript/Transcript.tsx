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
// The composition duplicates the session pane's transcript region rather than
// sharing a component: Session.tsx is frozen this wave and its transcript
// engine lives in a concurrently-edited stream's directory, so a shared
// <TranscriptBody> extraction isn't reachable here - a conscious, boundary-
// forced divergence (T8 sweep). It couples only to the engine's STABLE seams
// (useTranscript, TurnBlock, LoadOlderRow) and deliberately omits the live
// flow-overlay/new-content-pill machinery, which is a live-session affordance.
import { useEffect, useMemo, useRef } from "react";
import type { PaneProps } from "../../shell/paneRegistry";
import { connectionStore } from "../../stores/connection";
import { threadsStore } from "../../stores/threads";
import { EmptyState, PaneScaffold, VirtualList, type VirtualListHandle } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import { BackToParentAction } from "../backToParentAction";
import { exchangeOpenersFor } from "../session/transcript/exchangeOpeners";
import { LoadOlderRow } from "../session/transcript/flow/LoadOlderRow";
import { TurnBlock } from "../session/transcript/TurnBlock";
import { useTranscript } from "../session/transcript/useTranscript";
import { JobLog } from "./JobLog";
// Side-effect barrels: register every message item renderer and every tool
// descriptor the moment this pane loads, exactly as SessionPane does, so a
// transcript pane opened before any session pane still renders every item type
// (the registries must never depend on import order).
import "../session/transcript/messages";
import "../session/transcript/tools";
import styles from "./transcript.module.css";

export interface TranscriptParams {
  ref: string;
  // kata 0pzz: the enclosing session's ref, when this pane was opened from a
  // subagent row (subagentModule.tsx's openTranscript - the only producer
  // today). Carried in the pane's OWN params - not passed around as a
  // one-off argument - so it survives a layout restore/reload and so the
  // pane can answer "whose child am I" and offer a way back entirely on its
  // own, regardless of where dockview/StackHost happened to place it.
  // Undefined for a hypothetical future producer with no enclosing session.
  parentRef?: string;
}

const CLASS = {
  body: requireClass(styles.body, "transcript.module.css", "body"),
  list: requireClass(styles.list, "transcript.module.css", "list"),
};

// Same average-turn guess SessionPane feeds VirtualList's `dynamic` mode - real
// heights are measured post-mount per turn (see the VirtualList widget's own
// dynamic-mode comment).
const ESTIMATED_TURN_HEIGHT = 96;

export default function Transcript({ params }: PaneProps<TranscriptParams>) {
  // A "job:<id>" ref is a shell job's output log, not a thread: it renders
  // through the job-log surface, which never touches the thread engine (no
  // thread/read, no ensureThread). Refs never change for a mounted pane, so
  // this dispatch is stable for the component's lifetime.
  if (params.ref.startsWith("job:")) {
    return <JobLog jobRef={params.ref} parentRef={params.parentRef} />;
  }
  return <ThreadTranscript params={params} />;
}

function ThreadTranscript({ params }: { params: TranscriptParams }) {
  const { ref, parentRef } = params;
  const backAction = parentRef !== undefined ? <BackToParentAction parentRef={parentRef} /> : undefined;

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

  // Open at the latest turn once, when content first arrives. anchorToEnd on
  // the VirtualList below keeps the viewport pinned to the TRUE end while the
  // initial estimate->measured correction settles (and follows later appends
  // only while the reader stays at the end) - without it this one-shot scroll
  // lands at the ESTIMATED end and strands the reader mid-transcript. The ref
  // guard keeps it a one-shot: a later "load older" prepend doesn't yank the
  // view back to the bottom (and the list's own end-anchor keeps a prepend
  // visually anchored either way).
  const turnCount = model?.turns.length ?? 0;
  const openers = useMemo(() => (model ? exchangeOpenersFor(model.turns) : undefined), [model]);
  const didInitialScrollRef = useRef(false);
  useEffect(() => {
    if (!didInitialScrollRef.current && turnCount > 0) {
      didInitialScrollRef.current = true;
      listRef.current?.scrollToIndex(turnCount - 1, { align: "end" });
    }
  }, [turnCount]);

  if (!model) {
    return (
      <PaneScaffold title={ref} actions={backAction}>
        <EmptyState title="Loading transcript…" />
      </PaneScaffold>
    );
  }

  // VirtualList only ever hands back an in-range index (count-bounded), but
  // that guarantee crosses a boundary TypeScript can't see - check it for real
  // so a future bug fails loudly instead of rendering `undefined` (mirrors
  // SessionPane's own turnAt guard).
  const turnAt = (index: number) => {
    const turn = model.turns[index];
    if (!turn) throw new Error(`VirtualList index ${index} out of range for ${model.turns.length} turns`);
    return turn;
  };

  return (
    <PaneScaffold title={model.name || ref} actions={backAction}>
      {model.turns.length === 0 ? (
        <EmptyState title="No turns yet" hint="This thread hasn't sent or received anything yet." />
      ) : (
        <div className={CLASS.body}>
          {model.olderCursor && (
            <LoadOlderRow onLoad={loadOlderReportingError} loading={loadingOlder} error={olderError} />
          )}
          <div className={CLASS.list}>
            <VirtualList
              ref={listRef}
              dynamic
              anchorToEnd
              count={model.turns.length}
              estimateSize={() => ESTIMATED_TURN_HEIGHT}
              getItemKey={(index) => turnAt(index).id}
              renderRow={(index) => <TurnBlock turn={turnAt(index)} exchangeOpeners={openers} />}
            />
          </div>
        </div>
      )}
    </PaneScaffold>
  );
}
