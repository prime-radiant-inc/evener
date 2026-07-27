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
// status row sit in the `footer` slot instead, which PaneScaffold pins
// outside the scroll region (flex:none, always after body), so they never
// scroll out of view regardless of transcript length. PendingChips travels
// with the composer (it's contextually "chips beside the composer", per its
// own doc comment) and shares its 76rem measure so the input aligns with the
// transcript's own content column; SessionChrome (the status row) stays
// full-width beneath, reading like a status bar.
import { useEffect, useRef } from "react";
import type { PaneProps } from "../../shell/paneRegistry";
import { connectionStore } from "../../stores/connection";
import { threadsStore, useThreadsStore } from "../../stores/threads";
import { Cadence, EmptyState, PaneScaffold, VirtualList, type VirtualListHandle } from "../../widgets";
import { SessionChrome } from "./chrome/SessionChrome";
import { ColdStartSkeleton, useColdStartSkeleton } from "./coldStart";
import { Composer } from "./composer/Composer";
import { cadenceStateForStatus, NOW_TICK_MS, useNowTick } from "./liveness";
import { PendingChips } from "./pending/PendingChips";
import { TurnBlock } from "./transcript/TurnBlock";
import { SYSTEM_PRELUDE_TURN_ID } from "./transcript/transcriptVisibility";
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

// model.turns is never actually empty for a real serf session:
// apptranscript.go's PreludeTurn (and, live, appprojector's bundled
// SESSION_START announcements) always synthesize one turn - the shared,
// well-known SYSTEM_PRELUDE_TURN_ID - from the session's system prompt,
// which agent/session_config guarantees is never blank. Left unhandled,
// that made EmptyTranscript above unreachable for any real dormant session
// (kata bz2z): the transcript branch below rendered instead, with nothing
// in it to show but that one collapsed "System prompt · Nk chars"
// disclosure - real information, but not a conversation, and not the
// invitation to start one. A transcript whose only turn is the prelude
// counts as empty for exactly the reason zero turns does: nothing has
// happened here yet that a user would call content. The instant a real
// turn joins it, the prelude turn stops being the WHOLE transcript and
// renders exactly as it always has - the scaffold, then the conversation.
function isDormantTranscript(turns: readonly { id: string }[]): boolean {
  return turns.length === 0 || (turns.length === 1 && turns[0]?.id === SYSTEM_PRELUDE_TURN_ID);
}

// A reasonable average-turn guess for VirtualList's `dynamic` mode to
// correct post-mount from each turn's real rendered height (turns vary
// wildly: a one-line tool call vs. a long streamed response) - see
// widgets/virtuallist's own `dynamic` prop doc comment.
const ESTIMATED_TURN_HEIGHT = 96;

// Failure-feedback convention: a USER-INITIATED action that fails surfaces via
// the useToasts() singleton, kind "error" - no new banner systems, no silent
// `.catch(() => {})`. Every stream's failure handling (composer
// send/steer/queue, queue strip promote/edit/cancel, ask answering, session
// actions) follows that shape. Automatic older-turn paging is the deliberate
// exception: nobody pressed anything, so its failure reports inline at the top
// of the transcript instead (useTranscript's olderError -> LoadOlderRow).
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

  // VirtualList's own imperative handle (getScrollElement/scrollToIndex) is
  // the seam useTranscriptScroll needs for every scroll-behavior concern
  // (T4's own scope) - called unconditionally, same as every other hook
  // here, even though the ref only ever populates once turns.length > 0
  // (see useTranscriptScroll's own "hasContent" handling for that).
  const virtualListRef = useRef<VirtualListHandle>(null);
  const flow = useTranscriptScroll({ ref, model, listRef: virtualListRef, loadOlder });
  const showColdStartSkeleton = useColdStartSkeleton(ref, model);
  // kata g2ez: names the one turn (if any) that starts what's arrived since
  // this pane was last open, so a reopened session shows where to pick up.
  const seenDividerTurnId = useSeenDivider(ref, model);

  if (!model) {
    return (
      <PaneScaffold title={ref}>
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

  const transcript = (
    <div className={styles.transcript}>
      <FlowOverlay
        top={
          <>
            {model.olderCursor && (
              <LoadOlderRow onLoad={loadOlderReportingError} loading={loadingOlder} error={olderError} />
            )}
            <LivenessLine
              lastFrameAt={model.lastFrameAt}
              now={now}
              active={model.status.type === "active"}
              sessionRef={ref}
              turnId={model.activeTurnId}
            />
          </>
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
              count={model.turns.length}
              estimateSize={() => ESTIMATED_TURN_HEIGHT}
              getItemKey={(index) => turnAt(index).id}
              renderRow={(index) => {
                const t = turnAt(index);
                return <TurnBlock turn={t} sessionRef={ref} showSeenDivider={t.id === seenDividerTurnId} />;
              }}
            />
          </div>
          {showColdStartSkeleton && <ColdStartSkeleton />}
        </div>
      </FlowOverlay>
    </div>
  );

  return (
    <PaneScaffold
      title={model.name || ref}
      cadence={cadence}
      footer={
        <div className={styles.footer}>
          <div className={styles.measure}>
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
