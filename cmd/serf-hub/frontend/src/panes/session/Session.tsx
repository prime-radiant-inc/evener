// The real transcript pane (wave 4 T1), replacing the wave-3 placeholder.
// dockview UNMOUNTS a pane's whole tree when its tab isn't active (see
// PaneHost's own comment in shell/DockHost.tsx), so every durable piece of
// state here lives in the threads store (ThreadModel, frameTimes) - this
// component's own state is limited to what may honestly die on a tab
// switch: the live decay clock (nowTick, below) and the connection-ready
// gate's local closure, neither of which loses anything a remount can't
// immediately reconstruct from the store.
import { useEffect, useRef, useState } from "react";
import { PaneScaffold, VirtualList, Cadence, EmptyState, type CadenceState, type VirtualListHandle } from "../../widgets";
import type { PaneProps } from "../../shell/paneRegistry";
import { connectionStore } from "../../stores/connection";
import { threadsStore, useThreadsStore } from "../../stores/threads";
import { useTranscript } from "./transcript/useTranscript";
import { TurnBlock } from "./transcript/TurnBlock";
import { useTranscriptScroll } from "./transcript/flow/useTranscriptScroll";
import { FlowOverlay } from "./transcript/flow/FlowOverlay";
import { NewContentPill } from "./transcript/flow/NewContentPill";
import { LivenessLine } from "./transcript/flow/LivenessLine";
import { LoadOlderRow } from "./transcript/flow/LoadOlderRow";
import styles from "./session.module.css";

export interface SessionPaneParams {
  ref: string;
}

const EMPTY_FRAME_TIMES: number[] = [];
// Same interval as the legacy renderer's own liveness tick
// (cmd/serf-hub/assets/renderer.js LIVENESS_TICK_MS=3000) - fine-grained
// enough that Cadence's tick decay visibly advances promptly, coarse enough
// to be a non-issue re-rendering cost-wise.
const NOW_TICK_MS = 3_000;

// cadenceStateForStatus maps the WIRE ThreadStatus.type vocabulary
// (appwire/types.go's constants: idle/active/awaiting/warning/closed/
// notLoaded/systemError - ThreadModel.status.type carries this straight
// through, see reducer.ts) onto Cadence's four-family state space.
// Deliberately a SEPARATE function from shell/rail/RailRow.tsx's own
// cadenceStateFor: that one consumes hubcore.NormalizeState's ALREADY-
// remapped output (closed->ended, systemError->errored folded in) from the
// REST /api/tree snapshot, not the raw wire vocabulary this pane's live
// ThreadModel carries - collapsing the raw wire vocabulary straight to
// CadenceState in one hop here mirrors NormalizeState's own remapping
// without making this pane depend on shell/rail's module for it. Exported
// for direct testing, same precedent as cadenceStateFor.
export function cadenceStateForStatus(type: string): CadenceState {
  switch (type) {
    case "systemError":
      return "failed";
    case "awaiting":
    case "warning":
      return "needs-you";
    case "active":
      return "working";
    case "closed":
      return "ended";
    default: // "idle", "notLoaded", "", and any future/unknown value
      return "idle";
  }
}

// useNowTick is the only thing in this pane that owns a timer: Cadence is a
// pure prop-driven render (widgets/cadence's own doc comment - "no timers,
// no Date.now()"), so something above it has to own the clock ticks that
// make its trace visibly decay even between live frames. Transient by
// design - unmounting drops the interval and a remount just starts a fresh
// one from the current instant, which is exactly right for a pure "what
// time is it" signal.
function useNowTick(intervalMs: number): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), intervalMs);
    return () => clearInterval(id);
  }, [intervalMs]);
  return now;
}

// A reasonable average-turn guess for VirtualList's `dynamic` mode to
// correct post-mount from each turn's real rendered height (turns vary
// wildly: a one-line tool call vs. a long streamed response) - see
// widgets/virtuallist's own `dynamic` prop doc comment.
const ESTIMATED_TURN_HEIGHT = 96;

export default function Session({ params }: PaneProps<SessionPaneParams>) {
  const { ref } = params;

  // ensureThread on mount / releaseThread on unmount, exactly once each.
  // AppShell mounts DockHost (and therefore this pane) unconditionally,
  // independent of whether the one AppwireClient has finished its
  // connect() handshake yet (see AppShell.tsx: the connect effect and the
  // pane tree are siblings, not sequenced) - a direct deep link to /s/{ref}
  // routinely reaches this effect before the client is "ready", and
  // AppwireClient.request() rejects any non-exempt method until then. This
  // defers the ONE ensureThread attempt until a client is actually usable
  // (immediately, if one already is - the common case for a pane opened
  // into an already-connected app) rather than failing fast and never
  // retrying; once that attempt has fired (success or failure), it is
  // never repeated - a later reconnect blip is threads.ts's own
  // handleReady's job for whatever is already tracked, not this effect's.
  useEffect(() => {
    let started = false;

    const tryStart = () => {
      if (started || connectionStore.getState().state !== "ready") return;
      started = true;
      // Best-effort: a failed hydrate (network error, or - as in some
      // pane-routing tests - a client with no thread/read handler scripted
      // at all) leaves the pane showing its loading state; there is
      // nothing further to do with the rejection here, but it must be
      // observed so it never surfaces as an unhandled rejection.
      // ensureThread's own catch already rolled back this attempt's
      // refcount claim (stores/threads.ts), so there is no leak to clean
      // up on top of that.
      threadsStore.getState().ensureThread(ref).catch(() => {});
    };

    tryStart();
    const unsubscribe = connectionStore.subscribe(tryStart);

    return () => {
      unsubscribe();
      if (started) threadsStore.getState().releaseThread(ref);
    };
  }, [ref]);

  const { model, loadOlder, loadingOlder } = useTranscript(ref);
  const frameTimes = useThreadsStore((s) => s.frameTimes.get(ref) ?? EMPTY_FRAME_TIMES);
  const now = useNowTick(NOW_TICK_MS);

  // VirtualList's own imperative handle (getScrollElement/scrollToIndex) is
  // the seam useTranscriptScroll needs for every scroll-behavior concern
  // (T4's own scope) - called unconditionally, same as every other hook
  // here, even though the ref only ever populates once turns.length > 0
  // (see useTranscriptScroll's own "hasContent" handling for that).
  const virtualListRef = useRef<VirtualListHandle>(null);
  const flow = useTranscriptScroll({ ref, model, listRef: virtualListRef, loadOlder });

  if (!model) {
    return (
      <PaneScaffold title={ref}>
        <EmptyState title="Loading transcript…" />
      </PaneScaffold>
    );
  }

  const cadence = <Cadence state={cadenceStateForStatus(model.status.type)} frameTimes={frameTimes} now={now} />;

  return (
    <PaneScaffold title={model.name || ref} cadence={cadence}>
      {model.turns.length === 0 ? (
        <EmptyState title="No turns yet" hint="This session hasn't sent or received anything yet." />
      ) : (
        <div className={styles.transcript}>
          <FlowOverlay
            top={
              <>
                {model.olderCursor && (
                  <LoadOlderRow onClick={() => void loadOlder().catch(() => {})} loading={loadingOlder} />
                )}
                <LivenessLine lastFrameAt={model.lastFrameAt} now={now} active={model.status.type === "active"} />
              </>
            }
            pill={<NewContentPill count={flow.pillCount} needsYou={flow.pillNeedsYou} onClick={flow.jumpToBottom} />}
          >
            <VirtualList
              ref={virtualListRef}
              dynamic
              count={model.turns.length}
              estimateSize={() => ESTIMATED_TURN_HEIGHT}
              getItemKey={(index) => model.turns[index]!.id}
              renderRow={(index) => <TurnBlock turn={model.turns[index]!} />}
            />
          </FlowOverlay>
        </div>
      )}
    </PaneScaffold>
  );
}
