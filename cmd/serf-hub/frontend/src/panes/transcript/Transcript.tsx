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
import { workspaceStore } from "../../shell/workspace";
import { connectionStore } from "../../stores/connection";
import { threadsStore, useThreadsStore } from "../../stores/threads";
import { Button, EmptyState, PaneScaffold, VirtualList, type VirtualListHandle } from "../../widgets";
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
  backLabel: requireClass(styles.backLabel, "transcript.module.css", "backLabel"),
};

// The app's 16x16 stroke grammar (see fileOpenBeside.tsx's OpenBesideIcon,
// mobile/StackHost.tsx's own BackIcon for the same chevron - this is a
// pane-header-scoped twin of that one, not an import: StackHost's is
// component-local and mobile-specific, this one is desktop/dockview-header
// specific, and duplicating one small path is cheaper than threading a
// shared icon module across a mobile/desktop boundary for a single glyph).
function BackIcon() {
  return (
    <svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true">
      <path
        d="M10 3 L5 8 L10 13"
        stroke="currentColor"
        strokeWidth="1.5"
        strokeLinecap="round"
        strokeLinejoin="round"
        fill="none"
      />
    </svg>
  );
}

// BackToParentAction is the pane's whole answer to kata 0pzz's "no way
// back": a single header action that is BOTH the identity marker (its own
// visible label names the parent, so a reader lands already knowing whose
// child this is - no hover, no memory) and the return path (one click
// re-focuses that session, or reopens it if the reader closed it meanwhile
// - workspaceStore.openPane's own dedup makes "already open" vs. "reopen
// fresh" the same call). The parent's live name is read reactively (a
// rename while this pane is open stays current); a parent whose name
// hasn't hydrated yet (or was evicted after its own pane closed - threads
// are refcounted, see stores/threads.ts) falls back to the raw ref, the
// same fallback every other pane title in this app already uses.
function BackToParentAction({ parentRef }: { parentRef: string }) {
  const name = useThreadsStore((s) => s.threads.get(parentRef)?.name);
  const label = name || parentRef;
  return (
    <Button
      variant="quiet"
      size="sm"
      icon={<BackIcon />}
      onClick={() => workspaceStore.getState().openPane("session", { ref: parentRef })}
    >
      <span className={CLASS.backLabel}>Back to {label}</span>
    </Button>
  );
}

// Same average-turn guess SessionPane feeds VirtualList's `dynamic` mode - real
// heights are measured post-mount per turn (see the VirtualList widget's own
// dynamic-mode comment).
const ESTIMATED_TURN_HEIGHT = 96;

export default function Transcript({ params }: PaneProps<TranscriptParams>) {
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
