import type { ThreadModel } from "@evener/appwire-client";
import { canReadSharedNotes } from "@evener/appwire-client";
import { useEffect, useRef } from "react";
import { canWriteHumanNote, syncHumanNote, useHumanNoteDraft } from "../../../stores/humanNoteDrafts";
import { topNotesStore, usePendingTopNotesFocus, useTopNotesExpanded } from "../../../stores/topNotes";
import { Chevron, ToolIcon } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
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
  expandedTitle: requireClass(styles.expandedTitle, "topnotespanel.module.css", "expandedTitle"),
  expandedBody: requireClass(styles.expandedBody, "topnotespanel.module.css", "expandedBody"),
};

export interface TopNotesPanelProps {
  sessionRef: string;
  model: ThreadModel;
}

export function TopNotesPanel({ sessionRef, model }: TopNotesPanelProps) {
  const expanded = useTopNotesExpanded(sessionRef);
  const pendingRequest = usePendingTopNotesFocus(sessionRef);
  const readable = canReadSharedNotes(model);
  const canWrite = canWriteHumanNote(model);
  // Subscribed before the canReadSharedNotes guard below so the early return
  // can never skip a hook (Rules of Hooks).
  const draftState = useHumanNoteDraft(sessionRef);
  const editorRef = useRef<HTMLTextAreaElement>(null);
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
        topNotesStore.getState().takePendingFocus(sessionRef);
        return;
      }
    }
    if (!topNotesStore.getState().takePendingFocus(sessionRef)) return;
    // The pane can be reused for another session between the take and the
    // frame, and React hands the successor the same DOM editor, so the
    // element alone cannot say whose request it carries: only the session
    // identity can.
    const requestedRef = sessionRef;
    requestAnimationFrame(() => {
      if (latestSessionRef.current === requestedRef) editorRef.current?.focus();
    });
  }, [sessionRef, canWrite, expanded, pendingRequest]);

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

  return (
    <div className={CLASS.topNotesPanel} data-expanded={expanded} data-testid="top-notes-panel">
      {/* One disclosure trigger both states share (the collapsed summary and
          the expanded header row): same button role, same toggle, same
          chevron - only the resting style, label, and lead content differ. */}
      <button
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
            <span className={CLASS.expandedTitle}>Session Notes</span>
          ) : (
            <>
              {sourceIconKind && (
                <span className={CLASS.sourceIcon} data-testid={`top-notes-icon-${sourceIconKind}`} aria-hidden="true">
                  <ToolIcon kind={sourceIconKind} size={14} />
                </span>
              )}
              <span className={isPlaceholder ? CLASS.placeholder : CLASS.clampedText}>{summaryText}</span>
            </>
          )}
        </span>
      </button>
      {expanded && (
        <div className={CLASS.expandedBody} data-testid="top-notes-expanded-content">
          <NotesPanelBody sessionRef={sessionRef} model={model} editorRef={editorRef} />
        </div>
      )}
    </div>
  );
}
