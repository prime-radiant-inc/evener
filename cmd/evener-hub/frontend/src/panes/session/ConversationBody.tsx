import { configFingerprint, formatQuoteBlock, projectThread, resolveEffectiveConfig } from "@evener/appwire-client";
import { useEffect, useMemo, useRef, useState } from "react";
import { useStore } from "zustand";
import { paneToURL, refParam } from "../../shell/routing";
import { workspaceStore } from "../../shell/workspace";
import { connectionStore } from "../../stores/connection";
import { controlsFor, recoveryFence } from "../../stores/liveControls";
import { ownerRetainedRef, threadsStore, useThreadsStore } from "../../stores/threads";
import { transcriptDisplayStore } from "../../stores/transcriptDisplay";
import { Button, Cadence, EmptyState, InlineNotice, type VirtualListHandle, VisuallyHidden } from "../../widgets";
import { zoomPaneCoversConversation } from "../zoom/cascadeCoverage";
import { SessionChrome } from "./chrome/SessionChrome";
import { TopNotesPanel } from "./chrome/TopNotesPanel";
import { ColdStartSkeleton, useColdStartSkeleton } from "./coldStart";
import { AskDock, AskDockAnnouncements, useAskDockActivationEpoch, useAskDockPending } from "./composer/askDock";
import { Composer } from "./composer/Composer";
import { carryComposerDraft } from "./composer/draft";
import {
  useBlockedMutationEntries,
  usePendingTurnEntries,
  useQueueStripMutationIds,
} from "./composer/queue/pendingTurnsStore";
import { requestQuoteInsert } from "./composer/quoteInsert";
import type { ComposerSourceState } from "./composer/sourceState";
import { cadenceStateForStatus, NOW_TICK_MS, SessionNowContext, useNowTick } from "./liveness";
import { useMarkSessionSeenOnOpen } from "./markSeen";
import { PendingChips } from "./pending/PendingChips";
import styles from "./session.module.css";
import { LivenessLine } from "./transcript/flow/LivenessLine";
import { LoadOlderRow } from "./transcript/flow/LoadOlderRow";
import { NewContentPill } from "./transcript/flow/NewContentPill";
import { useSeenDivider } from "./transcript/flow/useSeenDivider";
import { useTranscriptScroll } from "./transcript/flow/useTranscriptScroll";
import { useTranscriptScrollKeys } from "./transcript/flow/useTranscriptScrollKeys";
import { HeldSteerAnnouncements } from "./transcript/messages/HeldSteerAnnouncements";
import { HeldSteerStack, heldSteerEntries, useHeldSteerEpoch } from "./transcript/messages/HeldSteerStack";
import { SelectionQuote } from "./transcript/SelectionQuote";
import {
  TranscriptBody,
  transcriptAnchorEntriesForRows,
  transcriptRowsForProjection,
  transcriptSourceTurnRowIndexesForRows,
} from "./transcript/TranscriptBody";
import { SandboxEscalationRail } from "./transcript/tools/sandboxEscalation";
import type { TranscriptReadView } from "./transcript/transcriptReadView";
import { isDormantTranscript } from "./transcript/transcriptVisibility";
import { useTranscript } from "./transcript/useTranscript";

// A conversation rendered in a seat. The session pane is one seat; a drilled
// cascade column is another. The seat supplies what the conversation cannot
// derive from its ref alone, and owns its own frame: the pane's scaffold and
// its deleted/loading faces, or the column's chrome and scoped explanations.
export interface ConversationSeat {
  // Scroll-position retention: the pane's own read view, or a column's
  // retained cascade view.
  readView: TranscriptReadView | null;
  // Composer state for this conversation: the pane lifetime's source, or a
  // column's retained per-ref state. Null hides the composer.
  composerSource: ComposerSourceState | null;
  // Whether the seat still renders this conversation — the scroll
  // coordinator's remount gate. The pane checks its own record; a column
  // checks its readability.
  keepViewRemount(): boolean;
  // Identity for the scroll view and pane-scoped plumbing when no read view
  // exists: the hosting pane's id.
  viewFallbackId: string;
  // Whether the seat's frame is focused: pane focus, or a readable column.
  focused: boolean;
  // A resumed conversation continues under a new ref. A pane replaces its
  // route; a cascade column lets its own reconciliation replace the scope in
  // place. The draft always carries to the resumed identity first. The
  // callback reports whether the frame applied the change: the identity is
  // acknowledged only then, so a rejected replacement retries on the next
  // seat change instead of being lost.
  onResumedIdentity?(resumedRef: string): boolean | undefined;
  // The pane whose focus gates this seat's keyboard scroll: the pane's own
  // id, or the host pane's id for the cascade's active leaf column. Null on
  // every other column - the dispatcher runs same-action handlers in
  // registration order until one accepts, so a second registration under
  // the host pane's id would hand every scroll key to whichever column
  // mounted first.
  keyboardScrollPaneId: string | null;
  // The session menu belongs to the conversation pane; a cascade column's
  // chrome owns its actions.
  sessionMenu?: boolean;
}

export interface ConversationParts {
  state: "deleted" | "loading" | "live";
  content: React.ReactNode;
  footer: React.ReactNode;
  cadence: React.ReactNode;
}

// Whether a workspace view other than `exceptPaneId` still retains `ref` and so
// still has to follow a resume published for it: a session pane naming the ref
// in its params, or a cascade pane whose path covers it. The seat's own hosting
// pane is excluded — a cascade column replaces its whole path in place, so its
// interior scopes are handled with it — and a ref left with no other view is the
// ordinary single-view case, cleared as before.
function otherRetainedViewOf(ref: string, exceptPaneId: string): boolean {
  return workspaceStore.getState().panes.some((pane) => {
    if (pane.id === exceptPaneId) return false;
    if (pane.type === "session") return refParam(pane.params) === ref;
    return zoomPaneCoversConversation(pane, ref);
  });
}

// The live conversation for one ref: transcript, composer, live surfaces and
// their retention. One implementation, two seats. The deleted and loading
// faces stay with the seat — the shell callback branches on `state` and
// renders its own face — because the pane's faces carry pane-only affordances
// (the Close button, the force-stop recovery) the column must not inherit.
export function ConversationBody({
  ref,
  seat,
  render,
}: {
  ref: string;
  seat: ConversationSeat;
  render: (parts: ConversationParts) => React.ReactNode;
}) {
  // One ensureThread(ref) claim on mount, one matching releaseThread(ref) on
  // unmount — the threads store owns hydration, retries and re-subscription
  // from there; the claim, not this effect, is what the store converges on.
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

  // Opening a conversation marks its session seen on the hub, so the
  // session's blue dot clears on the phone too.
  useMarkSessionSeenOnOpen(ref);

  const { model, hasOlder, loadOlder, loadingOlder, loadOlderReportingError, olderError, cancelOlder } = useTranscript(
    ref,
    seat.readView,
  );
  const deletedRef = useThreadsStore((s) => s.deletedRefs.has(ref));
  const restartPending = useThreadsStore((s) => s.restartBlockingObligations.has(ref));
  const mutationStateAuthoritative = useThreadsStore((s) => s.mutationAuthorityRefs.has(ref));
  const blockedMutations = useBlockedMutationEntries(ref);
  const reconciliationFailed = useThreadsStore((s) => s.mutationReconciliationFailures.has(ref));

  // Identity follow-up for a store-driven resume: the transcript is already
  // hydrated under the new ref (the driver's refreshThread). The draft
  // carries to the resumed identity in every seat; the seat then decides
  // whether its frame follows the new ref (a pane route) or lets cascade
  // reconciliation replace the scope in place. One ref can be retained by more
  // than one view at once — an ordinary pane and a cascade column embedding it,
  // or a collapsed column in another cascade — and dockview unmounts an inactive
  // pane's whole tree, so the notification is consumed only once no OTHER
  // retained view of `ref` still has to follow: an inactive view reads the
  // surviving entry on its next mount instead of reopening on the superseded
  // identity.
  const resumedIdentity = useThreadsStore((s) => s.resumedIdentities.get(ref));
  useEffect(() => {
    if (resumedIdentity === undefined) return;
    carryComposerDraft(ref, resumedIdentity);
    const applied = seat.onResumedIdentity?.(resumedIdentity);
    if (applied !== false && !otherRetainedViewOf(ref, seat.viewFallbackId)) {
      threadsStore.getState().clearResumedIdentity(ref);
    }
  }, [resumedIdentity, ref, seat]);

  const frameTimes = useThreadsStore((s) => s.frameTimes.get(ref) ?? EMPTY_FRAME_TIMES);
  const now = useNowTick(NOW_TICK_MS);
  // While any question batch is pending, the answering surface is the
  // transcript's trailing row below (scrollable content, not the footer).
  const askPending = useAskDockPending(ref);
  const askEpoch = useAskDockActivationEpoch(ref);
  const pendingEntries = usePendingTurnEntries(ref);
  const queueStripIds = useQueueStripMutationIds(ref);
  const heldSteers = useMemo(() => heldSteerEntries(pendingEntries, queueStripIds), [pendingEntries, queueStripIds]);
  // Spec §1's live gate, complete: the read-only status family, plus the two
  // fence marks that can hold a session read-only while its status still
  // reads active/idle.
  const heldSurfaceLive =
    model !== undefined &&
    model.resumeRequired !== true &&
    !restartPending &&
    !HELD_SURFACE_OFF_STATUSES.has(model.status.type);
  const heldEpoch = useHeldSteerEpoch(ref, heldSteers, heldSurfaceLive);
  const heldVisible = heldSurfaceLive && heldSteers.length > 0;
  const trailingRow =
    askPending || heldVisible
      ? {
          id: "live-edge",
          content: (
            <>
              {askPending && <AskDock ref={ref} paneId={seat.viewFallbackId} />}
              {heldVisible && <HeldSteerStack ref={ref} />}
            </>
          ),
        }
      : undefined;

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
  const preparedView = useMemo(
    () => (projection ? { projection, rows: renderRows, anchorEntries } : undefined),
    [projection, renderRows, anchorEntries],
  );

  const virtualListRef = useRef<VirtualListHandle>(null);
  const announcementSequence = useRef(0);
  const [viewAnnouncement, setViewAnnouncement] = useState({ text: "", key: 0 });
  const transcriptContainerRef = useRef<HTMLDivElement>(null);
  const flow = useTranscriptScroll({
    ref,
    viewId: seat.readView?.id ?? seat.viewFallbackId,
    keepViewRemount: seat.keepViewRemount,
    model,
    listRef: virtualListRef,
    initialViewCapture: seat.readView?.getCapture(),
    onReaderIntent: seat.readView?.supersedePositioning,
    onReaderMovement: seat.readView?.syncPositioningMovement,
    loadOlder,
    cancelOlder,
    viewKey: configFingerprint(displayConfig),
    anchorEntries,
    // The trailing row is a real virtual row, so every end-targeted scroll
    // path must count it or it lands one row short.
    renderedRowCount: renderRows.length + (trailingRow !== undefined ? 1 : 0),
    sourceTurnRowIndexes,
    // An ask batch completing changes no turn/item shape: without this
    // signal a scrolled-away reader would get no new-content pill.
    askDockPending: askPending,
    askDockActivationEpoch: askEpoch,
    heldVisible,
    heldEpoch,
  });
  // Per-pane keyboard scroll registers against the shared registry and
  // declines unless the hosting pane is the focused one.
  useTranscriptScrollKeys({
    paneId: seat.keyboardScrollPaneId,
    listRef: virtualListRef,
    jumpToBottom: flow.jumpToBottom,
    markGesture: flow.markGesture,
    onPositioningCommand: seat.readView?.supersedePositioning,
  });

  const showColdStartSkeleton = useColdStartSkeleton(ref, model);
  const seenDividerTurnId = useSeenDivider(ref, model);

  // A deleted ref never hydrates a fresh model and its cached snapshot is
  // stale: the seat's deleted face outranks anything this body could render.
  if (deletedRef) return render({ state: "deleted", content: null, footer: null, cadence: null });
  if (!model) return render({ state: "loading", content: null, footer: null, cadence: null });

  // A held ghost is mounted content even on a turnless transcript: the
  // dormant empty surface yields to the transcript subtree so the ghost
  // stack and its announcements mount (steering-ghost spec §2).
  const showDormantSurface = isDormantTranscript(model.turns) && !heldVisible;
  const dormantSurface = showColdStartSkeleton ? (
    <ColdStartSkeleton />
  ) : (
    <EmptyTranscript
      active={model.status.type === "active"}
      restartRequired={model.status.type === "restartRequired"}
    />
  );

  // Send drives the resume on every recoverable shape, so the notice belongs
  // only to the two Send cannot complete: an incompatible-protocol daemon that
  // must be stopped first, and a retained child whose work its owner must
  // reconcile. Every descendant read carries mutationStateAuthoritative=false,
  // so retention alone would put the owner notice on every subagent pane; the
  // notice also needs the uncertainty or the fence that gives it something to
  // say.
  const fence = recoveryFence(model, restartPending, {
    uncertainMessages: blockedMutations.length > 0,
  });
  const recoveryOwnerRef = ownerRetainedRef(model, mutationStateAuthoritative);
  const showRestartNotice =
    model.status.type === "restartRequired" ||
    (recoveryOwnerRef !== undefined && (restartPending || blockedMutations.length > 0) && !fence.sendDriven);

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
              if (quoted !== "") requestQuoteInsert(ref, quoted, "append", seat.viewFallbackId);
            },
          },
        ]}
      />
      <TranscriptBody
        model={model}
        config={displayConfig}
        preparedView={preparedView}
        surface="live"
        disclosureScope={seat.readView?.id ?? seat.viewFallbackId}
        sessionRef={ref}
        sourcePaneId={seat.viewFallbackId}
        viewId={seat.readView?.id}
        initialViewCapture={seat.readView?.getCapture()}
        readView={seat.readView ?? undefined}
        onAnnounceViewChange={(summary) => {
          announcementSequence.current += 1;
          setViewAnnouncement({ text: `Transcript detail: ${summary}`, key: announcementSequence.current });
        }}
        showSeenDividerTurnId={seenDividerTurnId ?? undefined}
        loadOlderRow={
          hasOlder && (
            <LoadOlderRow
              onLoad={loadOlderReportingError}
              loading={loadingOlder}
              error={olderError}
              scrollElement={() => virtualListRef.current?.getScrollElement() ?? null}
            />
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
        trailingRow={trailingRow}
      />
      <div role="status" aria-live="polite" data-testid="transcript-view-announcement">
        <VisuallyHidden key={viewAnnouncement.key}>{viewAnnouncement.text}</VisuallyHidden>
      </div>
      {/* The ask dock's ONE live region lives here, outside the virtual
          list: the dock row is virtualized, so an in-row region would
          re-announce on every scroll-away/scroll-back remount. */}
      <AskDockAnnouncements ref={ref} />
    </div>
  );
  const transcript = <SessionNowContext.Provider value={now}>{transcriptContent}</SessionNowContext.Provider>;

  const content = (
    <div className={styles.contentColumn}>
      <TopNotesPanel sessionRef={ref} model={model} sourcePaneId={seat.viewFallbackId} />
      <SandboxEscalationRail sessionRef={ref} />
      {/* The held-steer ghosts' ONE live region, same rule as the ask
          dock's: outside the virtual list, live-gated per spec §1. */}
      {heldSurfaceLive && <HeldSteerAnnouncements ref={ref} />}
      {showDormantSurface ? dormantSurface : transcript}
    </div>
  );

  const footer = (
    <div className={styles.footer}>
      <div className={styles.measure}>
        <LivenessLine
          lastFrameAt={model.lastFrameAt}
          now={now}
          active={model.status.type === "active"}
          sessionRef={ref}
          // runningTurnId is the live-updated field; falls back to
          // activeTurnId for a thread that has not hydrated through the v6
          // path.
          turnId={model.runningTurnId ?? model.activeTurnId}
          retry={model.modelRetry}
          primaryModel={model.model}
        />
        {showRestartNotice && <RestartRequiredNotice sessionRef={ref} ownerRef={recoveryOwnerRef} />}
        {/* Recovery keeps the composer and its menu mounted. Retain the
            fallback only for other local snapshots with no Send surface;
            owner-retained sessions direct recovery to their owner. */}
        {model.status.type === "notLoaded" &&
          !recoveryOwnerRef &&
          ref.startsWith("local:") &&
          !restartPending &&
          !controlsFor(model).send &&
          seat.sessionMenu && (
            <SessionChrome ref={ref} paneId={seat.viewFallbackId} placement="menu" discoverActivity />
          )}
        {reconciliationFailed && (
          <InlineNotice live="alert" data-testid="message-recovery-pending">
            Message recovery has not completed. Sending will resume after recovery succeeds.
          </InlineNotice>
        )}
        <PendingChips sessionRef={ref} />
        {seat.composerSource && (
          <Composer ref={ref} paneId={seat.viewFallbackId} source={seat.composerSource} focused={seat.focused} />
        )}
      </div>
    </div>
  );

  return render({ state: "live", content, footer, cadence });
}

const EMPTY_FRAME_TIMES: number[] = [];

const HELD_SURFACE_OFF_STATUSES = new Set(["ended", "closed", "notLoaded", "restartRequired"]);

// An empty transcript is two situations wearing one face: a session that
// never ran a turn names the act the composer's button carries; a spawned
// session waiting on its first frame reports the wait instead.
function EmptyTranscript({ active, restartRequired }: { active: boolean; restartRequired: boolean }) {
  if (restartRequired) {
    return (
      <EmptyState title={DEGRADED_VERSION_EMPTY_TITLE} hint={DEGRADED_VERSION_EMPTY_HINT} reason="version-mismatch" />
    );
  }
  if (active) {
    return (
      <EmptyState title="Waiting for the first reply" hint="The agent has your message." reason="awaiting-reply" />
    );
  }
  return <EmptyState title="Send the first message" hint="This session hasn't started yet." reason="no-turns" />;
}

const DEGRADED_VERSION_EMPTY_TITLE = "This session is on a different Evener version";
const DEGRADED_VERSION_EMPTY_HINT =
  "To use it, stop it with Force shutdown in the session menu, then send a message to start it.";

// Failure-feedback convention: a USER-INITIATED action that fails surfaces
// via the useToasts() singleton; the resume the Send-resumes face drives
// keeps no inline sequence of its own — the store owns the one resume
// sequence.
function RestartRequiredNotice({ sessionRef, ownerRef }: { sessionRef: string; ownerRef?: string }) {
  const [refreshing, setRefreshing] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const refresh = async () => {
    setRefreshing(true);
    setError(null);
    try {
      await threadsStore.getState().refreshThread(sessionRef);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setRefreshing(false);
    }
  };
  return (
    <div role="alert" data-reason={ownerRef ? "owner-retained" : "version-mismatch"}>
      {ownerRef
        ? "This session is retained by its owning session. Its uncertain messages cannot be checked here until the owner releases it."
        : DEGRADED_VERSION_NOTICE}
      {ownerRef && <a href={paneToURL("session", { ref: ownerRef }) ?? undefined}>Open owning session</a>}
      {ownerRef && (
        <Button disabled={refreshing} onClick={() => void refresh()}>
          Refresh session
        </Button>
      )}
      {error && <span>{error}</span>}
    </div>
  );
}

const DEGRADED_VERSION_NOTICE =
  "This session is still working, and everything already saved is kept. It's running a different Evener version than this app, so it can't receive new messages. To use it again, use Force shutdown in the session menu to stop it (interrupting anything in progress), then send your next message to start it.";
