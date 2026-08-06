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
// status row sit in the `footer` slot instead, which PaneScaffold keeps
// after the body; when AskDock is active, that footer can shrink to the
// pane's actual allocation. LivenessLine lives
// here too now (kata x47h): FlowOverlay's `top` slot is a non-reserved
// absolute overlay floating over the scrollable transcript, so the one
// thing every liveness message needs - never landing on top of transcript
// text - is exactly what that slot cannot promise. The footer's layout can.
// PendingChips travels with the composer (it's contextually
// "chips beside the composer", per its own doc comment) and shares its
// 76rem measure so the input aligns with the transcript's own content
// column; SessionChrome (the status row) stays full-width beneath, reading
// like a status bar.
import { useEffect, useMemo, useRef, useState } from "react";
import type { PaneProps } from "../../shell/paneRegistry";
import { connectionStore } from "../../stores/connection";
import { threadsStore, useThreadsStore } from "../../stores/threads";
import { Cadence, EmptyState, PaneScaffold, RadioGroup, VirtualList, type VirtualListHandle } from "../../widgets";
import { SessionChrome } from "./chrome/SessionChrome";
import { modelLabel } from "./chrome/statusFormat";
import { ColdStartSkeleton, useColdStartSkeleton } from "./coldStart";
import { Composer } from "./composer/Composer";
import { cadenceStateForStatus, NOW_TICK_MS, useNowTick } from "./liveness";
import { PendingChips } from "./pending/PendingChips";
import { exchangeOpenersFor } from "./transcript/exchangeOpeners";
import { isItemLive, TurnBlock } from "./transcript/TurnBlock";
import { isDormantTranscript } from "./transcript/transcriptVisibility";
import { itemRendererFor } from "./transcript/types";
import { useTranscript } from "./transcript/useTranscript";
// Side-effect barrels: registering every message item renderer (T2) and
// every tool descriptor (T3) the moment the pane module loads, so the
// registries are full regardless of import order elsewhere (same
// principle as TurnBlock.tsx's own ToolCallItem import).
import "./transcript/messages";
import "./transcript/tools";
import styles from "./session.module.css";
import { FlowOverlay } from "./transcript/flow/FlowOverlay";
import { LivenessLine } from "./transcript/flow/LivenessLine";
import { LoadOlderRow } from "./transcript/flow/LoadOlderRow";
import { NewContentPill } from "./transcript/flow/NewContentPill";
import { useSeenDivider } from "./transcript/flow/useSeenDivider";
import { useTranscriptScroll } from "./transcript/flow/useTranscriptScroll";
import { SandboxEscalationRail } from "./transcript/tools/sandboxEscalation";
import { type FocusedEntry, focusedEntries, SESSION_VIEW_MODES, type SessionViewMode } from "./viewModes";

export interface SessionPaneParams {
  ref: string;
}

const EMPTY_FRAME_TIMES: number[] = [];

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
// exactly the mid-first-turn window. Every other status with zero turns -
// idle, notLoaded, closed, "" - has nothing running, so the invitation holds.
function EmptyTranscript({ active }: { active: boolean }) {
  if (active) {
    return <EmptyState title="Waiting for the first reply" hint="The agent has your message." />;
  }
  return <EmptyState title="Send the first message" hint="This session hasn't started yet." />;
}

// A reasonable average-turn guess for VirtualList's `dynamic` mode to
// correct post-mount from each turn's real rendered height (turns vary
// wildly: a one-line tool call vs. a long streamed response) - see
// widgets/virtuallist's own `dynamic` prop doc comment.
const ESTIMATED_TURN_HEIGHT = 96;

type ViewRow =
  | {
      id: string;
      turnId: string;
      sourceIndex: number;
      visible: true;
    }
  | {
      id: string;
      turnId: string;
      sourceIndex: number;
      visible: boolean;
      entries: FocusedEntry[];
    };

function normalizeViewMode(value: string): SessionViewMode {
  return SESSION_VIEW_MODES.some((mode) => mode.value === value) ? (value as SessionViewMode) : "everything";
}

// Failure-feedback convention: a USER-INITIATED action that fails surfaces via
// the useToasts() singleton, kind "error" - no new banner systems, no silent
// `.catch(() => {})`. Every stream's failure handling (composer
// send/steer/queue, queue strip promote/edit/cancel, ask answering, session
// actions) follows that shape. Automatic older-turn paging is the deliberate
// exception: nobody pressed anything, so its failure reports inline at the top
// of the transcript instead (useTranscript's olderError -> LoadOlderRow).
export default function Session({ params, paneId, focused: paneFocused }: PaneProps<SessionPaneParams>) {
  const { ref } = params;
  const [viewMode, setViewMode] = useState<SessionViewMode>("everything");

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
  const frameTimes = useThreadsStore((s) => s.frameTimes.get(ref) ?? EMPTY_FRAME_TIMES);
  const now = useNowTick(NOW_TICK_MS);
  const openers = useMemo(() => (model ? exchangeOpenersFor(model.turns) : undefined), [model]);
  const agentLabel = model ? modelLabel(model.modelProvider, model.model) : undefined;
  const focused = useMemo(
    () => (model && viewMode !== "everything" ? focusedEntries(model.turns, viewMode) : []),
    [model, viewMode],
  );
  const itemSourceIndexes = useMemo(() => {
    const indexes = new Map<string, number>();
    let sourceIndex = 0;
    for (const turn of model?.turns ?? []) {
      for (const item of turn.items) {
        indexes.set(item.id, sourceIndex);
        sourceIndex += 1;
      }
    }
    return indexes;
  }, [model]);
  const viewRows = useMemo<ViewRow[]>(() => {
    if (!model) return [];
    if (viewMode === "everything") {
      return model.turns.map((turn, index) => ({
        id: turn.id,
        turnId: turn.id,
        sourceIndex: index,
        visible: true as const,
      }));
    }
    const entriesByTurn = new Map<string, FocusedEntry[]>();
    for (const entry of focused) {
      const entries = entriesByTurn.get(entry.turnId);
      if (entries) entries.push(entry);
      else entriesByTurn.set(entry.turnId, [entry]);
    }
    return model.turns.map((turn, sourceIndex) => {
      const entries = entriesByTurn.get(turn.id) ?? [];
      return {
        id: turn.id,
        turnId: turn.id,
        sourceIndex,
        visible: entries.length > 0,
        entries,
      };
    });
  }, [model, viewMode, focused]);
  const anchorEntries = useMemo(() => {
    if (!model) return [];
    if (viewMode === "everything") {
      return model.turns.flatMap((turn, index) =>
        turn.items.map((item) => ({
          id: item.id,
          sourceIndex: itemSourceIndexes.get(item.id) ?? 0,
          index,
          isMessage: item.type === "userMessage" || item.type === "agentMessage",
        })),
      );
    }
    return viewRows.flatMap((row, index) =>
      "entries" in row
        ? row.entries.map((entry) => ({
            id: entry.id,
            sourceIndex: entry.sourceIndex,
            index,
            isMessage: entry.kind === "message",
          }))
        : [],
    );
  }, [model, viewMode, viewRows, itemSourceIndexes]);

  // VirtualList's own imperative handle (getScrollElement/scrollToIndex) is
  // the seam useTranscriptScroll needs for every scroll-behavior concern
  // (T4's own scope) - called unconditionally, same as every other hook
  // here, even though the ref only ever populates once turns.length > 0
  // (see useTranscriptScroll's own "hasContent" handling for that).
  const virtualListRef = useRef<VirtualListHandle>(null);
  const flow = useTranscriptScroll({
    ref,
    model,
    listRef: virtualListRef,
    loadOlder,
    viewKey: viewMode,
    anchorEntries,
  });
  const showColdStartSkeleton = useColdStartSkeleton(ref, model);
  // kata g2ez: names the one turn (if any) that starts what's arrived since
  // this pane was last open, so a reopened session shows where to pick up.
  const seenDividerTurnId = useSeenDivider(ref, model);

  if (!model) {
    return (
      <PaneScaffold paneId={paneId} focused={paneFocused} scaffoldMarker={`session:${ref}`} title={ref}>
        <EmptyState title="Loading transcript…" />
      </PaneScaffold>
    );
  }

  const cadence = <Cadence state={cadenceStateForStatus(model.status.type)} frameTimes={frameTimes} now={now} />;

  // VirtualList only ever calls getItemKey/renderRow with an index it got
  // back from its own count-bounded virtualizer (count={model.turns.length}
  // below), so this index is always in range - but that guarantee crosses a
  // component boundary TypeScript can't see through. Check it for real
  // rather than asserting past it, so a future bug here (e.g. turns
  // shrinking mid-render) fails loudly instead of silently rendering
  // `undefined`.
  const turnAt = (index: number) => {
    const turn = model.turns[index];
    if (!turn) throw new Error(`VirtualList index ${index} out of range for ${model.turns.length} turns`);
    return turn;
  };

  const rowAt = (index: number) => {
    const row = viewRows[index];
    if (!row) throw new Error(`VirtualList index ${index} out of range for ${viewRows.length} view rows`);
    return row;
  };

  const transcript = (
    <div className={styles.transcript}>
      <FlowOverlay
        top={
          model.olderCursor && (
            <LoadOlderRow onLoad={loadOlderReportingError} loading={loadingOlder} error={olderError} />
          )
        }
        pill={
          <NewContentPill
            count={flow.pillCount}
            needsYou={flow.pillNeedsYou}
            error={flow.pillError}
            pillArrowDirection={flow.pillArrowDirection}
            onClick={flow.jumpToBottom}
          />
        }
      >
        <div className={styles.transcriptContent}>
          <div className={styles.transcriptList}>
            <VirtualList
              ref={virtualListRef}
              dynamic
              anchorToEnd
              count={viewRows.length}
              estimateSize={() => ESTIMATED_TURN_HEIGHT}
              getItemKey={(index) => rowAt(index).id}
              renderRow={(index) => {
                const row = rowAt(index);
                if (!("entries" in row)) {
                  const t = turnAt(row.sourceIndex);
                  return (
                    <div>
                      <TurnBlock
                        turn={t}
                        sessionRef={ref}
                        showSeenDivider={t.id === seenDividerTurnId}
                        exchangeOpeners={openers}
                        agentLabel={agentLabel}
                        viewAnchorIndex={index}
                        viewAnchorSourceIndexes={itemSourceIndexes}
                      />
                    </div>
                  );
                }
                if (!row.visible) return null;
                return (
                  <div className={styles.focusedTranscript} data-testid="focused-transcript">
                    {row.entries.map((entry) => {
                      const anchor = {
                        "data-view-anchor-id": entry.id,
                        "data-view-anchor-index": index,
                        "data-view-anchor-source-index": entry.sourceIndex,
                        "data-view-anchor-message": entry.kind === "message",
                      } as const;
                      if (entry.kind === "tool-count") {
                        return (
                          <div key={entry.id} className={styles.toolCount} {...anchor}>
                            {entry.label}
                          </div>
                        );
                      }
                      if (entry.kind === "intent") {
                        return (
                          <div key={entry.id} className={styles.intent} {...anchor}>
                            {entry.rationale}
                          </div>
                        );
                      }
                      const turn = model.turns[row.sourceIndex];
                      if (!turn) return null;
                      const ItemRenderer = itemRendererFor(entry.message.type);
                      const opensExchange = openers?.has(entry.message.id);
                      return (
                        <div
                          key={entry.id}
                          className={entry.role === "agent" && !opensExchange ? styles.focusedRunContent : undefined}
                          {...anchor}
                        >
                          <ItemRenderer
                            item={entry.message}
                            turn={turn}
                            live={isItemLive(entry.message)}
                            sessionRef={ref}
                            opensExchange={opensExchange}
                            agentLabel={agentLabel}
                          />
                        </div>
                      );
                    })}
                  </div>
                );
              }}
              onChange={flow.restoreViewAnchorAfterMeasurement}
            />
          </div>
          {showColdStartSkeleton && <ColdStartSkeleton />}
        </div>
      </FlowOverlay>
    </div>
  );

  return (
    <PaneScaffold
      paneId={paneId}
      focused={paneFocused}
      scaffoldMarker={`session:${ref}`}
      title={model.name || ref}
      cadence={cadence}
      actions={
        <div className={styles.viewSelector}>
          <RadioGroup
            label="Session view"
            value={viewMode}
            options={[...SESSION_VIEW_MODES]}
            onChange={(value) => {
              flow.captureViewAnchor();
              setViewMode(normalizeViewMode(value));
            }}
          />
        </div>
      }
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
            />
            <PendingChips sessionRef={ref} />
            <Composer ref={ref} />
          </div>
          <SessionChrome ref={ref} />
        </div>
      }
    >
      <SandboxEscalationRail sessionRef={ref} />
      {showColdStartSkeleton && isDormantTranscript(model.turns) ? (
        <ColdStartSkeleton />
      ) : isDormantTranscript(model.turns) ? (
        <EmptyTranscript active={model.status.type === "active"} />
      ) : (
        transcript
      )}
    </PaneScaffold>
  );
}
