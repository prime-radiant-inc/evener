// The real transcript pane (wave 4 T1), replacing the wave-3 placeholder.
// dockview UNMOUNTS a pane's whole tree when its tab isn't active (see
// PaneHost's own comment in shell/DockHost.tsx), so every durable piece of
// state here lives in the threads store (ThreadModel, frameTimes) - this
// component's own state is limited to what may honestly die on a tab
// switch: the live decay clock (nowTick, from ./liveness) and the
// connection-ready gate's local closure, neither of which loses anything a
// remount can't immediately reconstruct from the store.
//
// Column layout: PaneScaffold's `body` slot (the transcript, scrollable) is
// the ONLY part of this pane that grows/shrinks with content - composer and
// inline session controls sit in the `footer` slot instead, which PaneScaffold keeps
// after the body; when AskDock is active, that footer can shrink to the
// pane's actual allocation. LivenessLine lives
// here too now (kata x47h): FlowOverlay's `top` slot is a non-reserved
// absolute overlay floating over the scrollable transcript, so the one
// thing every liveness message needs - never landing on top of transcript
// text - is exactly what that slot cannot promise. The footer's layout can.
// PendingChips travels with the composer (it's contextually
// "chips beside the composer", per its own doc comment) and shares its
// 76rem measure so the input aligns with the transcript's own content
// column; SessionChrome now lives in the composer's own PromptCard control row.
import { useEffect, useMemo, useRef, useState } from "react";
import { useStore } from "zustand";
import type { ThreadModel } from "../../protocol/model";
import type { PaneProps } from "../../shell/paneRegistry";
import { navigate, paneToURL } from "../../shell/routing";
import { workspaceStore } from "../../shell/workspace";
import { connectionStore } from "../../stores/connection";
import { useNavigationStore } from "../../stores/navigation/store";
import { threadsStore, useThreadsStore } from "../../stores/threads";
import { transcriptDisplayStore } from "../../stores/transcriptDisplay";
import { configFingerprint, resolveEffectiveConfig } from "../../transcriptDisplay/config";
import { projectThread } from "../../transcriptDisplay/projector";
import { Button, Cadence, EmptyState, PaneScaffold, type VirtualListHandle } from "../../widgets";
import { VisuallyHidden } from "../../widgets/internal/VisuallyHidden";
import { ColdStartSkeleton, useColdStartSkeleton } from "./coldStart";
import { AskDock, AskDockAnnouncements, useAskDockActivationEpoch, useAskDockPending } from "./composer/askDock";
import { Composer } from "./composer/Composer";
import { useBlockedMutationEntries } from "./composer/queue/pendingTurnsStore";
import { requestQuoteInsert } from "./composer/quoteInsert";
import { cadenceStateForStatus, NOW_TICK_MS, SessionNowContext, useNowTick } from "./liveness";
import { PendingChips } from "./pending/PendingChips";
import styles from "./session.module.css";
import { navigationSummaryFor, resolveThreadName } from "./threadTitle";
import { LivenessLine } from "./transcript/flow/LivenessLine";
import { LoadOlderRow } from "./transcript/flow/LoadOlderRow";
import { NewContentPill } from "./transcript/flow/NewContentPill";
import { useSeenDivider } from "./transcript/flow/useSeenDivider";
import { useTranscriptScroll } from "./transcript/flow/useTranscriptScroll";
import { useTranscriptScrollKeys } from "./transcript/flow/useTranscriptScrollKeys";
import { SelectionQuote } from "./transcript/SelectionQuote";
import { formatQuoteBlock } from "./transcript/selectionQuoteLogic";
import {
  TranscriptBody,
  transcriptAnchorEntriesForRows,
  transcriptRowsForProjection,
  transcriptSourceTurnRowIndexesForRows,
} from "./transcript/TranscriptBody";
import { SandboxEscalationRail } from "./transcript/tools/sandboxEscalation";
import { isDormantTranscript } from "./transcript/transcriptVisibility";
import { useTranscript } from "./transcript/useTranscript";

export interface SessionPaneParams {
  ref: string;
}

const EMPTY_FRAME_TIMES: number[] = [];
const EMPTY_THREADS = new Map<string, ThreadModel>();

// An empty transcript is two situations wearing one face, and no single line
// is true for both.
//
// Since dormant spawn shipped (kata ytpa) a session can exist having never run
// a turn. That transcript is blank because it is waiting on the USER, and the
// composer that ends the wait sits directly below it - so its empty state
// names the act, using the same word the composer's own button carries
// ("Send"), and the same word the rail row uses for the same fact ("Not
// started", shell/rail/RailRow.tsx).
//
// A session spawned WITH a prompt shows the same blank transcript until its
// first frame lands, and there the wait belongs to the AGENT. Inviting that
// user to send would ask them to redo what they just did, so that window
// reports the wait instead and confirms the message arrived.
//
// `status.type === "active"` is the wire vocabulary's word for "a turn is
// running right now" (appwire's ThreadStatus, mapped in ./liveness), which is
// exactly the mid-first-turn window. An incompatible session needs a restart;
// other empty sessions invite their first message.
function EmptyTranscript({ active, restartRequired }: { active: boolean; restartRequired: boolean }) {
  if (restartRequired) {
    return <EmptyState title="Session unavailable until restart" hint="Stop the daemon, then resume this session." />;
  }
  if (active) {
    return <EmptyState title="Waiting for the first reply" hint="The agent has your message." />;
  }
  return <EmptyState title="Send the first message" hint="This session hasn't started yet." />;
}

// Failure-feedback convention: a USER-INITIATED action that fails surfaces via
// the useToasts() singleton, kind "error" - no new banner systems, no silent
// `.catch(() => {})`. Every stream's failure handling (composer
// send/steer/queue, queue strip promote/edit/cancel, ask answering, session
// actions) follows that shape. Automatic older-turn paging is the deliberate
// exception: nobody pressed anything, so its failure reports inline at the top
// of the transcript instead (useTranscript's olderError -> LoadOlderRow).
function RestartRequiredNotice({ sessionRef, stopped = false }: { sessionRef: string; stopped?: boolean }) {
  const [refreshing, setRefreshing] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const refresh = async () => {
    setRefreshing(true);
    setError(null);
    try {
      if (stopped) {
        const { client, state } = connectionStore.getState();
        if (!client || state !== "ready") throw new Error("Connect to the hub before resuming this session.");
        await client.request("thread/resume", { ref: sessionRef });
      }
      await threadsStore.getState().refreshThread(sessionRef);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setRefreshing(false);
    }
  };
  return (
    <div role="alert">
      {stopped
        ? "Resume this session to check whether its uncertain messages were delivered."
        : "Session restart required. Stop the older daemon, then refresh this session. Stopping interrupts active work."}
      <Button disabled={refreshing} onClick={() => void refresh()}>
        {stopped ? "Resume session" : "Refresh session"}
      </Button>
      {error && <span>{error}</span>}
    </div>
  );
}

export default function Session({ params, paneId, focused: paneFocused }: PaneProps<SessionPaneParams>) {
  const { ref } = params;
  const blockedMutations = useBlockedMutationEntries(ref);

  // One ensureThread(ref) claim on mount, one matching releaseThread(ref) on
  // unmount. AppShell mounts DockHost (and therefore this pane)
  // unconditionally, independent of whether the one AppwireClient has finished
  // its connect() handshake yet (see AppShell.tsx: the connect effect and the
  // pane tree are siblings, not sequenced) - a direct deep link to /s/{ref}
  // routinely reaches this effect before the client is "ready", and
  // AppwireClient.request() rejects any non-exempt method until then. So the
  // claim waits for a usable client (immediately, if one already is - the
  // common case for a pane opened into an already-connected app).
  //
  // The claim, not this call, is what the store converges on: while this pane
  // holds the ref, stores/threads.ts owns reading it - retrying a failed read
  // on a still-ready client, rejoining a replaced one, and re-subscribing
  // across reconnects. That is why claiming once here is enough, and why this
  // component has no timer, no reload, and no retry loop of its own.
  useEffect(() => {
    let started = false;

    const tryStart = () => {
      if (started || connectionStore.getState().state !== "ready") return;
      started = true;
      // ensureThread resolves once the ref is hydrated or this pane's claim is
      // gone; a transient read failure is the store's to retry, not this
      // effect's. It can still reject for a condition no retry can fix (no
      // connected client at all, or - as in some pane-routing tests - a client
      // with no thread/read handler scripted), which leaves the pane on its
      // loading state; there is nothing further to do with that rejection here,
      // but it must be observed so it never surfaces as an unhandled one.
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

  // Older-turn paging reports its own failures IN the transcript, not as a
  // toast: it is automatic (nobody pressed anything, so a toast would be a
  // notification about work the reader never asked for) and the failure belongs
  // at the exact spot in the scroll where history stops. LoadOlderRow renders
  // olderError with a Retry beside it - the recovery path, since Jesse ruled out
  // a standing "load more" button and silent failure is not an option.
  const { model, loadOlder, loadingOlder, loadOlderReportingError, olderError } = useTranscript(ref);

  // A DELETED ref never hydrates: the hub durably fences every request
  // against a deleted target (cmd/evener-hub/app_sources.go's
  // deletionFenceError, stamping data.mutationOutcome "targetDeleted" -
  // hubcore.DeletionStore never clears that fence once set). threads.ts's own
  // hydrateAndSubscribe records that specific rejection into `deletedRefs`
  // (its own doc comment) as it happens - the SAME thread/read attempt
  // ensureThread's claim above already keeps retrying, not a second request
  // from here - while still retrying exactly as it always has, since
  // ensureThread's returned promise never settles for a deleted ref (its
  // retry loop has no terminal state and cannot otherwise tell "the daemon
  // is slow" apart from "this ref is gone"). Reading the flag here is what
  // lets this pane render an honest terminal state instead of "Loading
  // transcript…" forever.
  const deletedRef = useThreadsStore((s) => !model && s.deletedRefs.has(ref));
  const restartPending = useThreadsStore((s) => s.restartBlockingObligations.has(ref));
  const reconciliationFailed = useThreadsStore((s) => s.mutationReconciliationFailures.has(ref));
  const navigation = useNavigationStore();

  const frameTimes = useThreadsStore((s) => s.frameTimes.get(ref) ?? EMPTY_FRAME_TIMES);
  const now = useNowTick(NOW_TICK_MS);
  // While any question batch is pending, the answering surface is the
  // transcript's trailing row below (a scrollable part of the content, not
  // the footer-anchored composer replacement it used to be). Read
  // unconditionally with the rest of this component's hooks, ahead of the
  // !model early return, per the rules of hooks; the composer reads the same
  // seam to hide its own input row meanwhile.
  const askPending = useAskDockPending(ref);
  // The pending set's activation counter: the pill edge keys on this (not
  // the boolean) so an atomic pending-set replacement on resync re-fires it.
  const askEpoch = useAskDockActivationEpoch(ref);
  const displayViewport = useStore(transcriptDisplayStore, (state) => state.viewport);
  const displayLocal = useStore(transcriptDisplayStore, (state) => state.local[displayViewport]);
  const displayHub = useStore(transcriptDisplayStore, (state) => state.hub[displayViewport]);
  const displayConfig = useMemo(
    () => resolveEffectiveConfig({ local: displayLocal, hub: displayHub, layout: displayViewport }),
    [displayHub, displayLocal, displayViewport],
  );
  const projection = useMemo(() => (model ? projectThread(model, displayConfig) : undefined), [model, displayConfig]);
  const renderRows = useMemo(() => (projection ? transcriptRowsForProjection(projection) : []), [projection]);
  const anchorEntries = useMemo(() => transcriptAnchorEntriesForRows(renderRows), [renderRows]);
  const sourceTurnRowIndexes = useMemo(() => transcriptSourceTurnRowIndexesForRows(renderRows), [renderRows]);

  // VirtualList's own imperative handle (getScrollElement/scrollToIndex) is
  // the seam useTranscriptScroll needs for every scroll-behavior concern
  // (T4's own scope) - called unconditionally, same as every other hook
  // here, even though the ref only ever populates once turns.length > 0
  // (see useTranscriptScroll's own "hasContent" handling for that).
  const virtualListRef = useRef<VirtualListHandle>(null);
  const announcementSequence = useRef(0);
  const [viewAnnouncement, setViewAnnouncement] = useState({ text: "", key: 0 });
  // SelectionQuote's own positioning/containment context (its header
  // comment): the non-scrolling `.transcript` wrapper below, not
  // VirtualList's internal scroll node - a selection's own
  // getBoundingClientRect() is already viewport-relative regardless of
  // scroll position, so this ref only needs to bound "is this selection
  // inside the transcript pane at all" and clamp the floating bar to that
  // same visible area. The bar is position: fixed and does not track
  // scroll - any scroll dismisses it instead (SelectionQuote's own
  // document-level capture listener).
  const transcriptContainerRef = useRef<HTMLDivElement>(null);
  const flow = useTranscriptScroll({
    ref,
    model,
    listRef: virtualListRef,
    loadOlder,
    viewKey: configFingerprint(displayConfig),
    anchorEntries,
    // The pending-questions dock is a real virtual row (trailingRow below),
    // so every end-targeted scroll path - initial positioning, append-follow,
    // jump-to-bottom - must count it or it lands one row short, leaving the
    // answering surface below the viewport.
    renderedRowCount: renderRows.length + (askPending ? 1 : 0),
    sourceTurnRowIndexes,
    // ...and its activation is new content: an ask_user item completing
    // changes no turn/item shape, so without this signal a scrolled-away
    // reader would get no pill while the composer's input hides itself. The
    // edge keys on the epoch so an atomic pending-set replacement (a resync
    // swapping an answered-elsewhere batch for a new one) re-fires it while
    // the boolean never leaves true.
    askDockPending: askPending,
    askDockActivationEpoch: askEpoch,
  });
  // The transcript's keyboard scroll (Alt+Arrow/Alt+Shift+Arrow, Phase 3):
  // per-pane handlers against the shared registry that decline unless THIS
  // pane is the workspace's focused one. Nothing registers on mobile.
  useTranscriptScrollKeys({ paneId, listRef: virtualListRef, jumpToBottom: flow.jumpToBottom });
  const showColdStartSkeleton = useColdStartSkeleton(ref, model);
  // kata g2ez: names the one turn (if any) that starts what's arrived since
  // this pane was last open, so a reopened session shows where to pick up.
  const seenDividerTurnId = useSeenDivider(ref, model);

  // Same fallback chain, and same shared resolver, as DockHost's dockview
  // tab title (shell/threadTitle's own doc comment) - the live thread name
  // wins once hydrated, else the rail's already-loaded tree store's title
  // for this ref, else the raw ref as the last resort. Without this, a pane
  // opened before its transcript hydrates showed the raw ref here even when
  // the tree store already knew the friendly title, while the dockview tab
  // right above it already showed that title.
  // Never the raw ref while the deleted state is showing (below): the ref is
  // the one thing about a gone session that means nothing to a person
  // reading the pane's own header.
  const title = deletedRef
    ? "Session deleted"
    : (resolveThreadName(model ? new Map([[ref, model]]) : EMPTY_THREADS, navigationSummaryFor(ref, navigation), ref) ??
      ref);

  // Closing follows Settings.tsx's own handleClose seam exactly (its own doc
  // comment on the trap this avoids, and needsYouCycle.ts's identical note):
  // navigate() to "/" FIRST, then closePane. AppShell reconciles the CURRENT
  // pathname against the workspace on every pane change, so closePane alone
  // - leaving window.location.pathname on /s/{ref} - would just get this
  // pane reopened right back onto the same eternal loading state.
  function handleCloseDeleted() {
    const url = paneToURL("welcome", {});
    if (url !== null) navigate(url);
    workspaceStore.getState().closePane(paneId);
  }

  if (!model) {
    if (deletedRef) {
      return (
        <PaneScaffold paneId={paneId} focused={paneFocused} scaffoldMarker={`session:${ref}`} title={title}>
          <EmptyState
            title="This session was deleted"
            hint="Its transcript is gone. You can close this pane."
            action={
              <Button variant="quiet" onClick={handleCloseDeleted}>
                Close
              </Button>
            }
          />
        </PaneScaffold>
      );
    }
    return (
      <PaneScaffold paneId={paneId} focused={paneFocused} scaffoldMarker={`session:${ref}`} title={title}>
        <EmptyState title="Loading transcript…" />
      </PaneScaffold>
    );
  }

  const cadence = <Cadence state={cadenceStateForStatus(model.status.type)} frameTimes={frameTimes} now={now} />;

  const transcriptContent = (
    <div className={styles.transcript} ref={transcriptContainerRef}>
      <SelectionQuote
        containerRef={transcriptContainerRef}
        actions={[
          {
            label: "Quote in reply",
            onInvoke: (selectedText) => {
              const quoted = formatQuoteBlock(selectedText);
              if (quoted !== "") requestQuoteInsert(ref, quoted);
            },
          },
        ]}
      />
      <TranscriptBody
        model={model}
        config={displayConfig}
        surface="live"
        disclosureScope={`transcript:live:${ref}`}
        sessionRef={ref}
        viewId={paneId}
        onAnnounceViewChange={(summary) => {
          announcementSequence.current += 1;
          setViewAnnouncement({ text: `Transcript detail: ${summary}`, key: announcementSequence.current });
        }}
        showSeenDividerTurnId={seenDividerTurnId ?? undefined}
        loadOlderRow={
          model.olderCursor && (
            <LoadOlderRow onLoad={loadOlderReportingError} loading={loadingOlder} error={olderError} />
          )
        }
        liveOverlay={
          <NewContentPill
            count={flow.pillCount}
            visible={flow.pillVisible}
            needsYou={flow.pillNeedsYou}
            error={flow.pillError}
            pillArrowDirection={flow.pillArrowDirection}
            onClick={flow.jumpToBottom}
          />
        }
        listRef={virtualListRef}
        onMeasurementsChange={flow.restoreViewAnchorAfterMeasurement}
        trailingContent={showColdStartSkeleton && <ColdStartSkeleton />}
        // The pending-questions dock is the transcript's last row while any
        // batch is pending: it scrolls with the content (a reader scrolling
        // back for context scrolls it away), its answer state lives in
        // askDockStore so the virtual list unmounting the row loses nothing,
        // and the list's end-anchoring surfaces a new question for a reader
        // at the bottom without yanking one who scrolled up. Passed only
        // while pending so no empty zero-height row pads the list otherwise.
        trailingRow={askPending ? { id: "ask-dock", content: <AskDock ref={ref} /> } : undefined}
      />
      <div role="status" aria-live="polite" data-testid="transcript-view-announcement">
        <VisuallyHidden key={viewAnnouncement.key}>{viewAnnouncement.text}</VisuallyHidden>
      </div>
      {/* The ask dock's ONE live region lives here, outside the virtual
          list: the dock row is virtualized, so an in-row region would
          re-announce on every scroll-away/scroll-back remount. This
          component announces only real pending/count transitions. */}
      <AskDockAnnouncements ref={ref} />
    </div>
  );
  const transcript = <SessionNowContext.Provider value={now}>{transcriptContent}</SessionNowContext.Provider>;

  return (
    <PaneScaffold
      paneId={paneId}
      focused={paneFocused}
      scaffoldMarker={`session:${ref}`}
      title={title}
      cadence={cadence}
      footer={
        <div className={styles.footer}>
          <div className={styles.measure}>
            <LivenessLine
              lastFrameAt={model.lastFrameAt}
              now={now}
              active={model.status.type === "active"}
              sessionRef={ref}
              turnId={model.activeTurnId}
              retry={model.modelRetry}
              primaryModel={model.model}
            />
            {(model.status.type === "restartRequired" ||
              (model.status.type === "notLoaded" && (restartPending || blockedMutations.length > 0))) && (
              <RestartRequiredNotice sessionRef={ref} stopped={model.status.type === "notLoaded"} />
            )}
            {reconciliationFailed && (
              <div role="alert">
                Message recovery is waiting for browser storage. Sending will resume after recovery succeeds.
              </div>
            )}
            <PendingChips sessionRef={ref} />
            <Composer ref={ref} />
          </div>
        </div>
      }
    >
      <SandboxEscalationRail sessionRef={ref} />
      {showColdStartSkeleton && isDormantTranscript(model.turns) ? (
        <ColdStartSkeleton />
      ) : isDormantTranscript(model.turns) ? (
        <EmptyTranscript
          active={model.status.type === "active"}
          restartRequired={model.status.type === "restartRequired"}
        />
      ) : (
        transcript
      )}
    </PaneScaffold>
  );
}
