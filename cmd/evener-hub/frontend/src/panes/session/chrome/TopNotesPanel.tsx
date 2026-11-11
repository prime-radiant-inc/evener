import type { ThreadModel } from "@evener/appwire-client";
import { canReadSharedNotes, canWriteSharedNotes } from "@evener/appwire-client";
import { useEffect, useRef } from "react";
import { syncHumanNote, useHumanNoteDraft } from "../../../stores/humanNoteDrafts";
import { topNotesStore, usePendingTopNotesFocus, useTopNotesExpanded } from "../../../stores/topNotes";
import { Chevron, Eyebrow, ToolIcon } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import { useDismissOnOutsidePress } from "../../../widgets/internal/useDismissOnOutsidePress";
import { useOwnedDocument } from "../../../widgets/internal/useOwnedDocument";
import { hasNoteText, NotesPanelBody } from "./NotesPanel";
import styles from "./topnotespanel.module.css";

const CLASS = {
  topNotesPanel: requireClass(styles.topNotesPanel, "topnotespanel.module.css", "topNotesPanel"),
  summary: requireClass(styles.summary, "topnotespanel.module.css", "summary"),
  summaryLeft: requireClass(styles.summaryLeft, "topnotespanel.module.css", "summaryLeft"),
  chevron: requireClass(styles.chevron, "topnotespanel.module.css", "chevron"),
  sourceIcon: requireClass(styles.sourceIcon, "topnotespanel.module.css", "sourceIcon"),
  clampedText: requireClass(styles.clampedText, "topnotespanel.module.css", "clampedText"),
  placeholder: requireClass(styles.placeholder, "topnotespanel.module.css", "placeholder"),
  expandedHeader: requireClass(styles.expandedHeader, "topnotespanel.module.css", "expandedHeader"),
  expandedBody: requireClass(styles.expandedBody, "topnotespanel.module.css", "expandedBody"),
};

export interface TopNotesPanelProps {
  sessionRef: string;
  model: ThreadModel;
  sourcePaneId?: string;
}

/** Collapse the panel for one conversation. Module-level, so the dismissal
 * effect below binds a stable callback instead of re-subscribing per render. */
function collapseNotes(ref: string): void {
  topNotesStore.getState().setExpanded(ref, false);
}

export function TopNotesPanel({ sessionRef, model, sourcePaneId }: TopNotesPanelProps) {
  const expanded = useTopNotesExpanded(sessionRef);
  const pendingRequest = usePendingTopNotesFocus(sessionRef);
  const readable = canReadSharedNotes(model);
  const canWrite = canWriteSharedNotes(model);
  // Subscribed before the canReadSharedNotes guard below so the early return
  // can never skip a hook (Rules of Hooks).
  const draftState = useHumanNoteDraft(sessionRef);
  const editorRef = useRef<HTMLTextAreaElement>(null);
  const rootRef = useRef<HTMLDivElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  // Resolved from the panel's own root so a panel moved into a Dockview popout
  // listens on THAT window's document, not the opener's.
  const ownerDocument = useOwnedDocument(rootRef, expanded);
  // The frame-callback focus guard below compares against the ref the pane
  // CURRENTLY serves; mirroring the prop here keeps that current through
  // every render (the latest-ref pattern).
  const latestSessionRef = useRef(sessionRef);
  latestSessionRef.current = sessionRef;

  useEffect(() => {
    // Gated on readability so a session without the notes capability never
    // grows the drafts store at all; eviction bounds the rest.
    if (!readable) return;
    syncHumanNote(sessionRef, model.humanNote);
  }, [sessionRef, model.humanNote, readable]);

  // An expanded panel is a disclosure floating over the conversation, so a
  // press anywhere else in the pane means the reader has moved on and it
  // collapses.
  //
  // A press inside ANY notes panel for this conversation is inside the
  // surface being worked in: one conversation can hold two panels at once (an
  // ordinary session pane and a cascade column) and they share one expanded
  // flag, so a press in the other one must not collapse this one.
  useDismissOnOutsidePress({
    ownerDocument,
    enabled: expanded,
    isInside: (target) =>
      // Read nodeType rather than `instanceof Element`: a Dockview popout has
      // its own constructors, and its nodes fail an opener-realm instanceof.
      target !== null &&
      (target as Node).nodeType === 1 &&
      (target as Element).closest("[data-notes-panel-ref]")?.getAttribute("data-notes-panel-ref") === sessionRef,
    onDismiss: () => collapseNotes(sessionRef),
  });

  // Serve the outstanding focus request whenever the editor is visible:
  // immediately for /notes on a mounted panel, and on mount for a request
  // that arrived while the session pane was still being opened (the /notes
  // command opens the pane first). The token is per-ref and served exactly
  // once, so a pane reused for another session neither misses its own
  // request nor serves the other session's.
  useEffect(() => {
    // A read-only session has no editor to focus, so it must not consume the
    // request either: held here, /notes on an ended session still lands focus
    // if the session resumes without a remount.
    if (!canWrite) return;
    if (!expanded || !pendingRequest) return;
    // A request BORN read-only may be redeemed long after the user moved on
    // (the session resumes, the panel unmounts and remounts), so it must not
    // yank focus from an active control (a composer mid-typing): it is
    // consumed silently instead, so it cannot pounce on some later
    // interaction either. A request born writable was just asked for and
    // focuses normally.
    if (pendingRequest.originReadOnly) {
      const active = document.activeElement;
      if (active !== null && active !== document.body) {
        topNotesStore.getState().takePendingFocus(sessionRef, sourcePaneId);
        return;
      }
    }
    if (!topNotesStore.getState().takePendingFocus(sessionRef, sourcePaneId)) return;
    // The pane can be reused for another session between the take and the
    // frame, and React hands the successor the same DOM editor, so the
    // element alone cannot say whose request it carries: only the session
    // identity can.
    const requestedRef = sessionRef;
    requestAnimationFrame(() => {
      if (latestSessionRef.current === requestedRef) editorRef.current?.focus();
    });
  }, [sessionRef, canWrite, expanded, pendingRequest, sourcePaneId]);

  if (!readable) return null;

  // While the session accepts writes, the summary mirrors the editor's
  // draft - kept equal to the model between edits by the sync effect above,
  // while edits themselves land in the draft store immediately (the 10s
  // blur-save lag was the original staleness bug). A session that turned
  // read-only mid-edit keeps its pending edit in the draft store for
  // retry-after-resume, but the read-only body shows the saved note, so the
  // summary must agree with what expanding will actually show.
  const humanNote = canWrite ? (draftState?.text ?? model.humanNote) : model.humanNote;

  const hasHumanNote = hasNoteText(humanNote);
  const hasAgentNote = hasNoteText(model.agentNote);
  const hasUrls = model.sessionUrls.length > 0;

  let sourceIconKind: "person" | "skill" | "globe" | null = null;
  let summaryText = "";
  let isPlaceholder = false;

  if (hasHumanNote) {
    sourceIconKind = "person";
    summaryText = humanNote;
  } else if (hasAgentNote) {
    sourceIconKind = "skill";
    summaryText = model.agentNote;
  } else if (hasUrls) {
    sourceIconKind = "globe";
    summaryText = `${model.sessionUrls.length} ${model.sessionUrls.length === 1 ? "link" : "links"}`;
  } else {
    isPlaceholder = true;
    // Read-only sessions (ended/closed/fenced) have no editor to write in, so
    // the empty bar must not invite typing it can't accept.
    summaryText = canWrite ? "Add a note…" : "No notes yet";
  }

  const toggle = () => {
    topNotesStore.getState().toggle(sessionRef);
  };

  // Escape is the keyboard path for the same dismissal the outside press
  // performs. The handler sits on the panel root rather than on the document,
  // so it fires exactly when the key came from inside the panel (the trigger
  // or the body bubbles through here) and never eats the key for the rest of
  // the pane - an enclosing overlay, a menu, or the composer.
  const handleKeyDown = (event: React.KeyboardEvent<HTMLDivElement>) => {
    // Collapsed, the trigger is the whole panel: Escape is not ours to consume,
    // so an enclosing overlay still gets it.
    if (event.key !== "Escape" || !expanded) return;
    // preventDefault marks the key as handled so an enclosing overlay does not
    // also close, without stopping the event from being seen (OverlayPanel's
    // rule).
    event.preventDefault();
    // Collapsing unmounts whatever inside the body held focus, which would
    // otherwise drop focus onto the document body and strand a keyboard user;
    // the trigger is where the disclosure now lives.
    triggerRef.current?.focus();
    collapseNotes(sessionRef);
  };

  return (
    // biome-ignore lint/a11y/noStaticElementInteractions: keydown only dismisses on Escape, mirroring widgets/dialog/OverlayPanel.tsx; the interactive controls are the trigger button and the body
    <div
      ref={rootRef}
      className={CLASS.topNotesPanel}
      data-notes-panel-ref={sessionRef}
      data-testid="top-notes-panel"
      onKeyDown={handleKeyDown}
    >
      {/* One disclosure trigger both states share (the collapsed summary and
          the expanded header row): same button role, same toggle, same
          chevron - only the resting style, label, and lead content differ. */}
      <button
        ref={triggerRef}
        type="button"
        className={expanded ? CLASS.expandedHeader : CLASS.summary}
        onClick={toggle}
        aria-expanded={expanded}
        // Collapsed, the visible preview IS the accessible name (the note
        // text, the link count, or the placeholder) - a generic label would
        // hide that content from screen readers.
        aria-label={expanded ? "Collapse session notes" : undefined}
        data-testid={expanded ? "top-notes-collapse-trigger" : "top-notes-summary"}
      >
        <span className={CLASS.summaryLeft}>
          <span className={CLASS.chevron} data-open={expanded}>
            <Chevron size={16} />
          </span>
          {expanded ? (
            <Eyebrow as="span">Session notes</Eyebrow>
          ) : (
            <>
              {sourceIconKind && (
                <span className={CLASS.sourceIcon} data-testid={`top-notes-icon-${sourceIconKind}`} aria-hidden="true">
                  <ToolIcon kind={sourceIconKind} size={14} />
                </span>
              )}
              <span
                className={isPlaceholder ? CLASS.placeholder : CLASS.clampedText}
                data-reason={isPlaceholder ? (canWrite ? "empty" : "read-only") : undefined}
              >
                {summaryText}
              </span>
            </>
          )}
        </span>
      </button>
      {expanded && (
        <div className={CLASS.expandedBody} data-testid="top-notes-expanded-content">
          <NotesPanelBody sessionRef={sessionRef} model={model} sourcePaneId={sourcePaneId} editorRef={editorRef} />
        </div>
      )}
    </div>
  );
}
