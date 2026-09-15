import { useEffect, useRef } from "react";
import type { ThreadModel } from "../../../protocol/model";
import { canReadSharedNotes } from "../../../protocol/sharedNotesAvailability";
import { topNotesStore, useTopNotesExpanded, useTopNotesFocusEpoch } from "../../../stores/topNotes";
import { Chevron } from "../../../widgets/chevron";
import { requireClass } from "../../../widgets/internal/requireClass";
import { ToolIcon } from "../../../widgets/toolicon";
import { NotesPanelBody } from "./NotesPanel";
import styles from "./topnotespanel.module.css";

const CLASS = {
  topNotesPanel: requireClass(styles.topNotesPanel, "topnotespanel.module.css", "topNotesPanel"),
  summary: requireClass(styles.summary, "topnotespanel.module.css", "summary"),
  summaryLeft: requireClass(styles.summaryLeft, "topnotespanel.module.css", "summaryLeft"),
  chevron: requireClass(styles.chevron, "topnotespanel.module.css", "chevron"),
  sourceIcon: requireClass(styles.sourceIcon, "topnotespanel.module.css", "sourceIcon"),
  clampedText: requireClass(styles.clampedText, "topnotespanel.module.css", "clampedText"),
  placeholder: requireClass(styles.placeholder, "topnotespanel.module.css", "placeholder"),
  hint: requireClass(styles.hint, "topnotespanel.module.css", "hint"),
  expandedPanel: requireClass(styles.expandedPanel, "topnotespanel.module.css", "expandedPanel"),
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
  const focusEpoch = useTopNotesFocusEpoch(sessionRef);
  const editorRef = useRef<HTMLTextAreaElement>(null);
  const prevEpoch = useRef(focusEpoch);

  useEffect(() => {
    if (focusEpoch > prevEpoch.current) {
      prevEpoch.current = focusEpoch;
      requestAnimationFrame(() => {
        editorRef.current?.focus();
      });
    }
  }, [focusEpoch]);

  if (!canReadSharedNotes(model)) return null;

  const hasHumanNote = model.humanNote.trim() !== "";
  const hasAgentNote = model.agentNote.trim() !== "";
  const hasUrls = model.sessionUrls.length > 0;

  let sourceIconKind: "person" | "skill" | "globe" | null = null;
  let summaryText = "";
  let isPlaceholder = false;

  if (hasHumanNote) {
    sourceIconKind = "person";
    summaryText = model.humanNote;
  } else if (hasAgentNote) {
    sourceIconKind = "skill";
    summaryText = model.agentNote;
  } else if (hasUrls) {
    sourceIconKind = "globe";
    summaryText = `${model.sessionUrls.length} ${model.sessionUrls.length === 1 ? "link" : "links"}`;
  } else {
    isPlaceholder = true;
    summaryText = "Add a note…";
  }

  const toggle = () => {
    topNotesStore.getState().toggle(sessionRef);
  };

  return (
    <div className={CLASS.topNotesPanel} data-testid="top-notes-panel">
      {!expanded ? (
        <button
          type="button"
          className={CLASS.summary}
          onClick={toggle}
          aria-expanded={false}
          aria-label={isPlaceholder ? "Add a note" : "Session notes"}
          data-testid="top-notes-summary"
        >
          <div className={CLASS.summaryLeft}>
            <span className={CLASS.chevron} data-open="false">
              <Chevron size={16} />
            </span>
            {sourceIconKind && (
              <span className={CLASS.sourceIcon} data-testid={`top-notes-icon-${sourceIconKind}`} aria-hidden="true">
                <ToolIcon kind={sourceIconKind} size={14} />
              </span>
            )}
            <div className={isPlaceholder ? CLASS.placeholder : CLASS.clampedText}>{summaryText}</div>
          </div>
          <span className={CLASS.hint} aria-hidden="true">
            {isPlaceholder ? "Click to write" : "Click to expand"}
          </span>
        </button>
      ) : (
        <div className={CLASS.expandedPanel} data-testid="top-notes-expanded-content">
          <button
            type="button"
            className={CLASS.expandedHeader}
            onClick={toggle}
            aria-expanded={true}
            aria-label="Collapse session notes"
            data-testid="top-notes-collapse-trigger"
          >
            <div className={CLASS.summaryLeft}>
              <span className={CLASS.chevron} data-open="true">
                <Chevron size={16} />
              </span>
              <span className={CLASS.expandedTitle}>Session Notes</span>
            </div>
            <span className={CLASS.hint} aria-hidden="true">
              Click to collapse
            </span>
          </button>
          <div className={CLASS.expandedBody}>
            <NotesPanelBody sessionRef={sessionRef} model={model} editorRef={editorRef} />
          </div>
        </div>
      )}
    </div>
  );
}
