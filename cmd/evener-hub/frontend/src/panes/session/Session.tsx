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
import type { ThreadModel } from "@evener/appwire-client";
import { useMemo, useState } from "react";
import { useStore } from "zustand";
import { attentionEntryOwnsReader } from "../../shell/attentionHistory";
import { conversationPaneLifetime } from "../../shell/paneLifetime";
import type { PaneProps } from "../../shell/paneRegistry";
import { navigate, paneToURL, refParam } from "../../shell/routing";
import { ForceStopDialog } from "../../shell/sessionMenu/ForceStopDialog";
import { StatusBar } from "../../shell/statusbar/StatusBar";
import { useIsMobile } from "../../shell/useIsMobile";
import { workspaceStore } from "../../shell/workspace";
import { useNavigationStore } from "../../stores/navigation/store";
import { threadsStore, useThreadsStore } from "../../stores/threads";
import { useTranscriptDisplayStore } from "../../stores/transcriptDisplay";
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
  const presentation = useTranscriptDisplayStore((state) => state.presentation[state.viewport]);
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
  // #4343: a ref whose latest read answered the hub's transcriptIndexRebuilding
  // discriminant is being repaired in the background, not stalled: the
  // loading face says so. Follows the ref's reads — cleared by the next read
  // that does not fail with the code — so it never outlives the repair.
  const repairingRef = useThreadsStore((state) => state.repairingRefs.has(ref));
  const navigation = useNavigationStore();

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
      mountAutofocus: !attentionEntryOwnsReader(paneId, ref),
      // A resumed conversation replaces this pane's route so the workspace
      // opens the resumed session rather than leaving a stale identity.
      onResumedIdentity: (resumedRef) => {
        const url = paneToURL("session", { ref: resumedRef });
        if (url === null) return false;
        navigate(url, { replace: true });
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
              footer={footer}
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
          return (
            <PaneScaffold
              paneId={paneId}
              focused={paneFocused}
              scaffoldMarker={`session:${ref}`}
              title={title}
              footer={footer}
              edgeFooter={edgeFooter}
            >
              <div className={styles.loading}>
                <Loader label={repairingRef ? "Repairing session history" : "Loading transcript"} />
                {ref.startsWith("local:") && (
                  <>
                    <p className={styles.loadingHint}>
                      If the session is unresponsive, you can stop its process to recover it.
                    </p>
                    <SessionForceStopRecovery sessionRef={ref} />
                  </>
                )}
              </div>
            </PaneScaffold>
          );
        }
        return (
          <PaneScaffold
            paneId={paneId}
            focused={paneFocused}
            focusSelector='[data-composer] [role="textbox"], [data-comic-composer] [role="textbox"]'
            scaffoldMarker={`session:${ref}`}
            title={title}
            bodyScroll={presentation !== "comic"}
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
