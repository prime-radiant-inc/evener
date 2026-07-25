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
import { useEffect, useRef } from "react";
import type { PaneProps } from "../../shell/paneRegistry";
import { connectionStore } from "../../stores/connection";
import { threadsStore } from "../../stores/threads";
import { EmptyState, PaneScaffold, useToasts, VirtualList, type VirtualListHandle } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import { LoadOlderRow } from "../session/transcript/flow/LoadOlderRow";
import { TurnBlock } from "../session/transcript/TurnBlock";
import { useTranscript } from "../session/transcript/useTranscript";
// Side-effect barrels: register every message item renderer and every tool
// descriptor the moment this pane loads, exactly as SessionPane does, so a
// transcript pane opened before any session pane still renders every item type
// (the registries must never depend on import order).
import "../session/transcript/messages";
import "../session/transcript/tools";
import styles from "./transcript.module.css";

export interface TranscriptParams {
  ref: string;
}

const CLASS = {
  body: requireClass(styles.body, "transcript.module.css", "body"),
  list: requireClass(styles.list, "transcript.module.css", "list"),
};

// Same average-turn guess SessionPane feeds VirtualList's `dynamic` mode - real
// heights are measured post-mount per turn (see the VirtualList widget's own
// dynamic-mode comment).
const ESTIMATED_TURN_HEIGHT = 96;

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

export default function Transcript({ params }: PaneProps<TranscriptParams>) {
  const { ref } = params;
  const toasts = useToasts();

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

  const { model, loadOlder, loadingOlder } = useTranscript(ref);
  const listRef = useRef<VirtualListHandle>(null);

  // Open at the latest turn once, when content first arrives - the one scroll
  // behavior a read-only viewer needs. Deliberately fires only on the FIRST
  // non-empty render (a ref guard), not on every turn-count change, so a
  // later "load older" prepend doesn't yank the view back to the bottom.
  const turnCount = model?.turns.length ?? 0;
  const didInitialScrollRef = useRef(false);
  useEffect(() => {
    if (!didInitialScrollRef.current && turnCount > 0) {
      didInitialScrollRef.current = true;
      listRef.current?.scrollToIndex(turnCount - 1);
    }
  }, [turnCount]);

  if (!model) {
    return (
      <PaneScaffold title={ref}>
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
    <PaneScaffold title={model.name || ref}>
      {model.turns.length === 0 ? (
        <EmptyState title="No turns yet" hint="This thread hasn't sent or received anything yet." />
      ) : (
        <div className={CLASS.body}>
          {model.olderCursor && (
            <LoadOlderRow
              onClick={() =>
                void loadOlder().catch((err) => {
                  toasts.push("error", `Couldn't load older messages: ${errorMessage(err)}`);
                })
              }
              loading={loadingOlder}
            />
          )}
          <div className={CLASS.list}>
            <VirtualList
              ref={listRef}
              dynamic
              count={model.turns.length}
              estimateSize={() => ESTIMATED_TURN_HEIGHT}
              getItemKey={(index) => turnAt(index).id}
              renderRow={(index) => <TurnBlock turn={turnAt(index)} />}
            />
          </div>
        </div>
      )}
    </PaneScaffold>
  );
}
