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
// --session-measure so the input aligns with the transcript's own content
// column; SessionChrome now lives in the composer's own PromptCard control row.

import type { ThreadModel } from "@evener/appwire-client";
import { configFingerprint, projectThread, resolveEffectiveConfig } from "@evener/appwire-client";
import { useEffect, useMemo, useRef, useState } from "react";
import { useStore } from "zustand";
import type { PaneProps } from "../../shell/paneRegistry";
import { navigate, paneToURL } from "../../shell/routing";
import { ForceStopDialog } from "../../shell/sessionMenu/ForceStopDialog";
import { workspaceStore } from "../../shell/workspace";
import { connectionStore } from "../../stores/connection";
import { controlsFor } from "../../stores/liveControls";
import { useNavigationStore } from "../../stores/navigation/store";
import { resumeStopBaseline, threadsStore, useThreadsStore } from "../../stores/threads";
import { transcriptDisplayStore } from "../../stores/transcriptDisplay";
import { Button, Cadence, EmptyState, PaneScaffold, type VirtualListHandle } from "../../widgets";
import { VisuallyHidden } from "../../widgets/internal/VisuallyHidden";
import { SessionChrome } from "./chrome/SessionChrome";
import { TopNotesPanel } from "./chrome/TopNotesPanel";
import { ColdStartSkeleton, useColdStartSkeleton } from "./coldStart";
import { AskDock, AskDockAnnouncements, useAskDockActivationEpoch, useAskDockPending } from "./composer/askDock";
import { Composer } from "./composer/Composer";
import { useBlockedMutationEntries, usePendingTurnEntries } from "./composer/queue/pendingTurnsStore";
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
import { HeldSteerAnnouncements } from "./transcript/messages/HeldSteerAnnouncements";
import { HeldSteerStack, heldSteerEntries, useHeldSteerEpoch } from "./transcript/messages/HeldSteerStack";
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

// Spec §1: held-steer surfaces render only while the session is live -
// never on notLoaded or read-only surfaces. The read-only status family
// matches the shared-notes surface's (humanNoteDrafts.ts): ended, closed,
// notLoaded, restartRequired. Awaiting stays live: a hold parked at a
// boundary delivers with the next turn. Two further read-only marks ride
// outside status - a snapshot's resumeRequired and the recovery-fence
// obligation - and are read where the live gate composes them below.
const HELD_SURFACE_OFF_STATUSES = new Set(["ended", "closed", "notLoaded", "restartRequired"]);

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
function RestartRequiredNotice({
  sessionRef,
  resumeRequired = false,
  ownerRef,
}: {
  sessionRef: string;
  resumeRequired?: boolean;
  ownerRef?: string;
}) {
  const [refreshing, setRefreshing] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const refresh = async () => {
    setRefreshing(true);
    setError(null);
    try {
      let refreshedRef = sessionRef;
      if (resumeRequired) {
        const { client, state } = connectionStore.getState();
        if (!client || state !== "ready") throw new Error("Connect to the hub before resuming this session.");
        // Baseline every Stop generation BEFORE the resume starts: the resume
        // may return a different identity, and that new ref can be named by a
        // Stop while the resume RPC is still in flight (any surface already
        // tracking the resumed ref records it). A fence captured after the
        // resolve would take that Stop as its baseline and never fire, so both
        // refs are checked against their pre-resume generations.
        const stopBaseline = resumeStopBaseline();
        // beforeRequest runs before the resumed identity is knowable, so a
        // per-ref fence cannot name it: a Stop acknowledged against ANY ref in
        // the reconnect window (the resumed identity among them) suppresses
        // the resume RPC. Once the RPC resolves and the new identity exists,
        // the checks below name both refs exactly.
        const { thread } = await client.resumeThread(sessionRef, { beforeRequest: stopBaseline });
        refreshedRef = thread.evener.ref;
        // During the post-resume hydration the pane still shows the old ref
        // (the navigate below has not run), so a Stop against EITHER ref must
        // cancel it: the old ref is what the visible Stop names, the new one
        // is what another holder of the resumed session names.
        const identityFence = () => {
          stopBaseline(sessionRef);
          stopBaseline(refreshedRef);
        };
        identityFence();
        await threadsStore.getState().refreshThread(refreshedRef, identityFence);
        identityFence();
        if (refreshedRef !== sessionRef) {
          const url = paneToURL("session", { ref: refreshedRef });
          if (url !== null) navigate(url, { replace: true });
        }
      } else {
        await threadsStore.getState().refreshThread(refreshedRef);
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setRefreshing(false);
    }
  };
  return (
    <div role="alert">
      {ownerRef
        ? "This session is retained by its owning session. Its uncertain messages cannot be checked here until the owner releases it."
        : resumeRequired
          ? "Resume this session before continuing. Any uncertain messages will be checked before sending."
          : "Session restart required. Stop the older daemon, then refresh this session. Stopping interrupts active work."}
      {ownerRef && <a href={paneToURL("session", { ref: ownerRef }) ?? undefined}>Open owning session</a>}
      <Button disabled={refreshing} onClick={() => void refresh()}>
        {resumeRequired ? "Resume session" : "Refresh session"}
      </Button>
      {error && <span>{error}</span>}
    </div>
  );
}

function SessionForceStopRecovery({ sessionRef }: { sessionRef: string }) {
  const [open, setOpen] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const stop = async () => {
    setError(null);
    try {
      await threadsStore.getState().forceStop(sessionRef);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      throw err;
    }
    try {
      await threadsStore.getState().refreshThread(sessionRef);
    } catch (err) {
      setError(`Session stopped; couldn't refresh its view: ${err instanceof Error ? err.message : String(err)}`);
    }
  };
  return (
    <>
      <Button variant="quiet" onClick={() => setOpen(true)}>
        Force stop…
      </Button>
      <ForceStopDialog open={open} onClose={() => setOpen(false)} onConfirm={stop} />
      {error && <span role="alert">{error}</span>}
    </>
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
  const mutationStateAuthoritative = useThreadsStore((s) => s.mutationAuthorityRefs.has(ref));
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
  // Held steering (steer/drain/promote ghosts - HeldSteerStack) renders as
  // the transcript's second trailing-row tenant, below the AskDock when both
  // exist (steering-ghost spec §1). Read ahead of the !model early return,
  // per the rules of hooks, same as askPending/askEpoch above.
  const pendingEntries = usePendingTurnEntries(ref);
  const heldSteers = useMemo(() => heldSteerEntries(pendingEntries), [pendingEntries]);
  // Spec §1's live gate, complete: the read-only status family, plus the two
  // fence marks that can hold a session read-only while its status still
  // reads active/idle - a snapshot's resumeRequired (cleared only by an
  // explicit thread/resume) and the restart-blocking recovery obligation
  // (restartPending above, the same fence liveControls' press-time rule
  // consults). Composed ahead of the epoch so the pill signal and ghost
  // visibility agree: the epoch counts only VISIBLE arrivals.
  const heldSurfaceLive =
    model !== undefined &&
    model.resumeRequired !== true &&
    !restartPending &&
    !HELD_SURFACE_OFF_STATUSES.has(model.status.type);
  const heldEpoch = useHeldSteerEpoch(ref, heldSteers, heldSurfaceLive);
  // One predicate decides the row and the count (spec §1): renderedRowCount
  // derives from the trailingRow handed to the list - the same form
  // TranscriptBody itself uses - so the count and the row cannot drift and
  // jump-to-bottom/append-follow cannot land one row short.
  const heldVisible = heldSurfaceLive && heldSteers.length > 0;
  const trailingRow =
    askPending || heldVisible
      ? {
          id: "live-edge",
          content: (
            <>
              {askPending && <AskDock ref={ref} />}
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
    // The transcript's trailing row - the ONE live-edge row the AskDock and
    // the held-steer ghost stack share below - is a real virtual row
    // (trailingRow below), so every end-targeted scroll path - initial
    // positioning, append-follow, jump-to-bottom - must count it or it lands
    // one row short, leaving the answering surface or the ghost below the
    // viewport. The count derives from that same trailingRow value - the
    // form TranscriptBody itself uses - so the row and the count cannot
    // drift.
    renderedRowCount: renderRows.length + (trailingRow !== undefined ? 1 : 0),
    sourceTurnRowIndexes,
    // ...and its activation is new content: an ask_user item completing
    // changes no turn/item shape, so without this signal a scrolled-away
    // reader would get no pill while the composer's input hides itself. The
    // edge keys on the epoch so an atomic pending-set replacement (a resync
    // swapping an answered-elsewhere batch for a new one) re-fires it while
    // the boolean never leaves true.
    askDockPending: askPending,
    askDockActivationEpoch: askEpoch,
    // A held row mounts the transcript even when its only turn is the
    // synthetic prelude. Keep the coordinator's mounted-content predicate
    // aligned with the render branch below; ask-only dormant behavior stays
    // unchanged.
    heldVisible,
    // ...and a held steer APPEARING is new content the same way an ask
    // activation is: it changes no turn/item shape, so the pill's edge
    // detector never sees it without this signal. Arrival is the only edge
    // (useHeldSteerEpoch never bumps on removal - departures are announced
    // by HeldSteerAnnouncements, not counted as new content).
    heldEpoch,
  });
  // The transcript's keyboard scroll (Alt+Arrow/Alt+Shift+Arrow, Phase 3):
  // per-pane handlers against the shared registry that decline unless THIS
  // pane is the workspace's focused one. Nothing registers on mobile.
  useTranscriptScrollKeys({
    paneId,
    listRef: virtualListRef,
    jumpToBottom: flow.jumpToBottom,
    markGesture: flow.markGesture,
  });
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
        <EmptyState
          title="Loading transcript…"
          hint={
            ref.startsWith("local:")
              ? "If the session is unresponsive, you can stop its process to recover it."
              : undefined
          }
          action={ref.startsWith("local:") ? <SessionForceStopRecovery sessionRef={ref} /> : undefined}
        />
      </PaneScaffold>
    );
  }

  // A held ghost is mounted content even on a turnless transcript: the
  // dormant empty surface yields to the transcript subtree so the ghost
  // stack and its announcements mount for an idle drain (steering-ghost
  // spec §2).
  const showDormantSurface = isDormantTranscript(model.turns) && !heldVisible;
  const dormantSurface = showColdStartSkeleton ? (
    <ColdStartSkeleton />
  ) : (
    <EmptyTranscript
      active={model.status.type === "active"}
      restartRequired={model.status.type === "restartRequired"}
    />
  );

  const recoveryOwnerRef =
    !mutationStateAuthoritative &&
    model.status.type !== "notLoaded" &&
    model.status.type !== "restartRequired" &&
    model.parentRef?.startsWith("local:")
      ? model.parentRef
      : undefined;

  const showRestartNotice =
    model.status.type === "restartRequired" ||
    restartPending ||
    (blockedMutations.length > 0 && (model.status.type === "notLoaded" || !mutationStateAuthoritative));

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
        // The live-edge row is the transcript's last row while either
        // bottom-dwelling tenant exists: the pending-questions dock while any
        // batch is pending, and the held-steer ghost stack while any held
        // steering is in flight, rendered below the dock when both exist
        // (the one-row decision steering-ghost spec §1 makes). It scrolls
        // with the content (a reader scrolling back for context scrolls it
        // away), its tenants' interactive state lives in their own stores
        // (askDockStore, the shared pendingTurnsStore) so the virtual list
        // unmounting the row loses nothing, and the list's end-anchoring
        // surfaces new content for a reader at the bottom without yanking
        // one who scrolled up. Passed only while a tenant exists so no
        // empty zero-height row pads the list otherwise.
        trailingRow={trailingRow}
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
            {showRestartNotice && (
              <RestartRequiredNotice
                sessionRef={ref}
                ownerRef={recoveryOwnerRef}
                resumeRequired={model.status.type !== "restartRequired" && !recoveryOwnerRef}
              />
            )}
            {/* Recovery keeps the composer and its menu mounted. Retain the
                fallback only for other local snapshots with no Send surface;
                owner-retained sessions direct recovery to their owner. */}
            {model.status.type === "notLoaded" &&
              !recoveryOwnerRef &&
              ref.startsWith("local:") &&
              !restartPending &&
              !controlsFor(model).send && <SessionChrome ref={ref} placement="menu" discoverActivity />}
            {reconciliationFailed && (
              <div role="alert">Message recovery has not completed. Sending will resume after recovery succeeds.</div>
            )}
            <PendingChips sessionRef={ref} />
            <Composer ref={ref} focused={paneFocused} />
          </div>
        </div>
      }
    >
      <div className={styles.contentColumn}>
        <TopNotesPanel sessionRef={ref} model={model} />
        <SandboxEscalationRail sessionRef={ref} />
        {/* The held-steer ghosts' ONE live region, same rule as the ask
            dock's: outside the virtual list, announcing only real
            appearance/delivery/departure transitions. It mounts here,
            INDEPENDENT of the transcript subtree: a dormant transcript's
            LAST departure unmounts that subtree in the same commit
            (heldVisible flips false, the dormant empty surface takes
            over), so a region inside it would never observe the departure
            it must announce (spec §5). Live-gated per spec §1. */}
        {heldSurfaceLive && <HeldSteerAnnouncements ref={ref} />}
        {showDormantSurface ? dormantSurface : transcript}
      </div>
    </PaneScaffold>
  );
}
