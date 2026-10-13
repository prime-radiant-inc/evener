// The live session pane. dockview UNMOUNTS a pane's whole tree when its tab
// isn't active (see PaneHost's own comment in shell/DockHost.tsx), so every
// durable piece of state here lives in the threads store (ThreadModel,
// frameTimes) — this component's own state is limited to what may honestly
// die on a tab switch.
//
// The conversation itself — transcript, composer, live surfaces, retention —
// lives in ConversationBody, shared with the cascade's embedded columns. This
// pane is one seat for it: it supplies the pane record's lifetime, the
// pane-owned scroll view, route navigation on resume, keyboard scroll
// registration, the session menu, and the deleted/loading scaffold faces
// with their pane-only affordances (the Close button, force-stop recovery).
import { sessionActionError, type ThreadModel } from "@evener/appwire-client";
import { useMemo, useState } from "react";
import { useStore } from "zustand";
import { conversationPaneLifetime } from "../../shell/paneLifetime";
import type { PaneProps } from "../../shell/paneRegistry";
import { navigate, paneToURL, refParam } from "../../shell/routing";
import { ForceStopDialog } from "../../shell/sessionMenu/ForceStopDialog";
import { StatusBar } from "../../shell/statusbar/StatusBar";
import { useIsMobile } from "../../shell/useIsMobile";
import { workspaceStore } from "../../shell/workspace";
import { useNavigationStore } from "../../stores/navigation/store";
import { useSessionActivity } from "../../stores/sessionActivity";
import { threadsStore, useThreadsStore } from "../../stores/threads";
import { Button, EmptyState, InlineNotice, Loader, PaneScaffold } from "../../widgets";
import { ConversationBody, type ConversationSeat } from "./ConversationBody";
import { RepoLocation } from "./composer/RepoLocation";
import styles from "./session.module.css";
import { navigationSummaryFor, resolveThreadName } from "./threadTitle";
import { retainedTranscriptReadView } from "./transcript/transcriptReadView";

export interface SessionPaneParams {
  ref: string;
}

const EMPTY_THREADS = new Map<string, ThreadModel>();

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
        Force shutdown…
      </Button>
      <ForceStopDialog open={open} onClose={() => setOpen(false)} onConfirm={stop} />
      {error && <InlineNotice tone="danger">{error}</InlineNotice>}
    </>
  );
}

export default function Session({ params, paneId, focused: paneFocused }: PaneProps<SessionPaneParams>) {
  const { ref } = params;
  const isMobile = useIsMobile();
  const pane = useStore(workspaceStore, (state) =>
    state.panes.find((record) => record.id === paneId && record.type === "session" && refParam(record.params) === ref),
  );
  const lifetime = pane ? conversationPaneLifetime(pane) : null;
  const composerSource = lifetime?.composer ?? null;
  const readView = lifetime ? retainedTranscriptReadView(lifetime, ref, "session") : null;
  // A plain model read for the pane's own chrome (the repo location in the
  // status bar, the header title); hydration and retention belong to the
  // conversation body's claim.
  const model = useThreadsStore((state) => state.threads.get(ref));
  const deletedRef = useThreadsStore((state) => state.deletedRefs.has(ref));
  const navigation = useNavigationStore();
  // The shared session-activity availability state, observed for the pane's
  // whole life. A target whose thread and activity reads both return
  // sessionUnavailable surfaces the same terminal explanation a cascade column
  // shows for this snapshot (see the loading branch below), instead of
  // spinning forever. Deriving it live - never a latched flag - keeps the
  // pane's automatic recovery: a snapshot that clears unavailability returns
  // it to its normal loading state.
  const { snapshot: activitySnapshot } = useSessionActivity(ref);

  // Same fallback chain, and same shared resolver, as DockHost's dockview
  // tab title — the live thread name wins once hydrated, else the rail's
  // tree store title, else the raw ref. Never the raw ref while the deleted
  // state is showing: the ref is the one thing about a gone session that
  // means nothing to a person reading the pane's own header.
  const title = deletedRef
    ? "Session deleted"
    : (resolveThreadName(model ? new Map([[ref, model]]) : EMPTY_THREADS, navigationSummaryFor(ref, navigation), ref) ??
      ref);

  // Closing follows Settings.tsx's own handleClose seam exactly: navigate()
  // to "/" FIRST, then closePane. AppShell reconciles the CURRENT pathname
  // against the workspace on every pane change, so closePane alone — leaving
  // window.location.pathname on /s/{ref} — would just get this pane reopened
  // right back onto the same eternal loading state.
  function handleCloseDeleted() {
    const url = paneToURL("welcome", {});
    if (url !== null) navigate(url);
    workspaceStore.getState().closePane(paneId);
  }

  const footerCwd = !deletedRef && model ? model.cwd : null;
  const edgeFooter = useMemo(() => {
    if (isMobile) return undefined;
    const location = footerCwd === null ? null : <RepoLocation cwd={footerCwd} local={ref.startsWith("local:")} />;
    return <StatusBar sessionRef={ref} paneId={paneId} leading={location} />;
  }, [footerCwd, isMobile, paneId, ref]);

  const seat = useMemo<ConversationSeat>(
    () => ({
      readView,
      composerSource,
      keepViewRemount: () =>
        workspaceStore
          .getState()
          .panes.some(
            (record) =>
              record.id === paneId && record.type === "session" && (record.params as SessionPaneParams).ref === ref,
          ),
      viewFallbackId: paneId,
      focused: paneFocused,
      // A resumed conversation continues under a new ref. This pane follows it
      // by retyping its OWN workspace record in place - the recorded
      // parent-edge carries to the replacement - and only then moves the URL.
      // Retyping first is what lets the route reconciliation resolve the
      // resumed ref back to this pane: relying on the URL alone, an ordinary
      // child beside its owner makes AppShell open a SECOND pane and strand
      // this one on the superseded identity. The callback reports whether the
      // record moved, so the resume is acknowledged only after the workspace
      // update (ConversationBody's resume effect).
      onResumedIdentity: (resumedRef) => {
        const addressed = workspaceStore
          .getState()
          .panes.find((record) => record.id === paneId && record.type === "session" && refParam(record.params) === ref);
        if (!addressed) return false;
        // The pane's lifetime survives the retype (paneLifetime's onPaneRetype
        // moves it onto the replacement record), so its retained SOURCE
        // ownership has to move with it. Leaving sourceRef on the superseded ref
        // would keep supplying that ref's composer - whose draft persistence
        // and submission are bound to that ref - to the resumed conversation,
        // so a later edit would be stored under the old ref and reloading the
        // resumed ref would lose it. Moving it here retires the superseded
        // composer and lets the resumed ref's own (or carried) draft load,
        // exactly as resumeCascadeScope does for its source column. Read the
        // lifetime BEFORE the retype: the retype listener rekeys the WeakMap,
        // so a later lookup would mint a fresh lifetime and miss this one.
        const lifetime = conversationPaneLifetime(addressed);
        if (!workspaceStore.getState().retypePane(addressed, "session", { ref: resumedRef })) return false;
        lifetime.adoptResumedSource(resumedRef);
        // The addressed pane now names the resumed ref, but an ordinary session
        // pane for that ref may ALREADY sit in the workspace (a sibling opened
        // beside the owner before the resume). retypePane deliberately does not
        // dedupe the way openPane/openBeside do, so without this the workspace
        // would hold two session panes for the resumed ref: routePlacementIsApplied's
        // nested single-child invariant (childPanes.length === 1) could never
        // pass, and its reconciliation would refocus whichever duplicate came
        // first - not necessarily the run the user is in. Keep the addressed
        // pane: it is the record the resume moved, it carries the opened-from
        // parent edge, and its unsent draft was already carried onto the
        // resumed ref by ConversationBody's resume effect (carryComposerDraft)
        // before this callback ran. Close every OTHER ordinary session pane for
        // the resumed ref, then promote the survivor into main if a closed
        // duplicate had emptied that slot. Only after both does the workspace
        // hold the single pane the acknowledgement below reports.
        for (const duplicate of workspaceStore
          .getState()
          .panes.filter(
            (record) => record.id !== paneId && record.type === "session" && refParam(record.params) === resumedRef,
          )) {
          workspaceStore.getState().closePane(duplicate.id);
        }
        if (workspaceStore.getState().mainPane() === null) workspaceStore.getState().promotePane(paneId);
        const url = paneToURL("session", { ref: resumedRef });
        if (url !== null) navigate(url, { replace: true });
        return true;
      },
      keyboardScrollPaneId: paneId,
      sessionMenu: true,
    }),
    [readView, composerSource, paneId, paneFocused, ref],
  );

  return (
    <ConversationBody
      ref={ref}
      seat={seat}
      render={({ state, content, footer, cadence }) => {
        // The deleted surface outranks any cached model: a durably deleted
        // target can never hydrate a fresh one, and its last cached snapshot
        // is stale.
        if (state === "deleted") {
          return (
            <PaneScaffold
              paneId={paneId}
              focused={paneFocused}
              scaffoldMarker={`session:${ref}`}
              title={title}
              edgeFooter={edgeFooter}
            >
              <EmptyState
                title="This session was deleted"
                hint="Its transcript is gone. You can close this pane."
                reason="deleted"
                action={
                  <Button variant="quiet" onClick={handleCloseDeleted}>
                    Close
                  </Button>
                }
              />
            </PaneScaffold>
          );
        }
        if (state === "loading") {
          // A permanently unavailable target can never hydrate a model: its
          // thread and activity reads both refuse with sessionUnavailable. Show
          // the shared terminal explanation instead of an eternal spinner,
          // exactly as a cascade column does for this snapshot. The live
          // snapshot is read straight into the branch (not latched) so any
          // recovery returns the pane to its normal loading state.
          return (
            <PaneScaffold
              paneId={paneId}
              focused={paneFocused}
              scaffoldMarker={`session:${ref}`}
              title={title}
              edgeFooter={edgeFooter}
            >
              {activitySnapshot?.summaryState?.permanent && activitySnapshot.summaryState.unavailable ? (
                <EmptyState
                  title="Transcript unavailable"
                  reason="unavailable"
                  hint={sessionActionError("Couldn't load transcript", activitySnapshot.summaryState.error)}
                />
              ) : (
                <div className={styles.loading}>
                  <Loader label="Loading transcript" />
                  {ref.startsWith("local:") && (
                    <>
                      <p className={styles.loadingHint}>
                        If the session is unresponsive, you can stop its process to recover it.
                      </p>
                      <SessionForceStopRecovery sessionRef={ref} />
                    </>
                  )}
                </div>
              )}
            </PaneScaffold>
          );
        }
        return (
          <PaneScaffold
            paneId={paneId}
            focused={paneFocused}
            focusSelector='[data-composer] [role="textbox"]'
            scaffoldMarker={`session:${ref}`}
            title={title}
            cadence={cadence}
            footer={footer}
            edgeFooter={edgeFooter}
          >
            {content}
          </PaneScaffold>
        );
      }}
    />
  );
}
