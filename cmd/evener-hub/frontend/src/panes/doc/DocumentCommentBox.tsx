// Controlled multiline comment box for the browser reader (Task 5).
//
// The component owns no reader state: `work` carries the literal note and the
// frozen capture, and every interaction is delegated through the approved
// callback props. Note text is rendered as literal text in a labeled textarea;
// Send validates with trim only (never trimming the stored note), and Return
// inserts a normal newline without reaching a surrounding shortcut. Opening
// does not autofocus or alter the current selection. Cancel delegates
// unsaved-work cleanup to the host and never touches saved annotations.
import type { AnnotationCommentWork } from "@evener/appwire-client";
import { type ChangeEvent, type KeyboardEvent, useId } from "react";
import { Button, Textarea } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import styles from "./documentAnnotations.module.css";

// The status vocabulary the later DocPane integration supplies: "unsaved" for a
// retained attempt/newer comment, "expiry-pending" for that phase, "saved" only
// for an applied receipt, or "saved, pending draft" for a saved pending handoff.
export type DocumentCommentStatus = "unsaved" | "expiry-pending" | "saved" | "saved, pending draft";

export interface DocumentCommentBoxProps {
  work: AnnotationCommentWork;
  onNote: (note: string) => void;
  onSend: () => void;
  onCancel: () => void;
  saving: boolean;
  status: DocumentCommentStatus;
  // True while the reader's newest raw response did not accept annotation
  // authority: Send stays unavailable rather than persisting a new operation from
  // retained but unconfirmed lease/capture evidence. The note keeps editing and
  // Cancel stays usable.
  suspended?: boolean;
}

const CLASS = {
  commentBox: requireClass(styles.commentBox, "documentAnnotations.module.css", "commentBox"),
  passage: requireClass(styles.passage, "documentAnnotations.module.css", "passage"),
  label: requireClass(styles.label, "documentAnnotations.module.css", "label"),
  status: requireClass(styles.status, "documentAnnotations.module.css", "status"),
  actions: requireClass(styles.actions, "documentAnnotations.module.css", "actions"),
};

export function DocumentCommentBox({
  work,
  onNote,
  onSend,
  onCancel,
  saving,
  status,
  suspended = false,
}: DocumentCommentBoxProps) {
  const noteId = useId();
  // Trim is validation only: it decides whether Send is available, never what is
  // stored. The literal note (including leading/trailing whitespace and newlines)
  // reaches the host unchanged.
  const blank = work.note.trim().length === 0;

  const handleNote = (event: ChangeEvent<HTMLTextAreaElement>) => {
    onNote(event.target.value);
  };
  const handleKeyDown = (event: KeyboardEvent<HTMLTextAreaElement>) => {
    // Return inserts a normal newline; stop it from reaching a composer-style
    // shortcut listening on an ancestor. Default insertion is untouched.
    if (event.key === "Enter") event.stopPropagation();
  };

  return (
    <div className={CLASS.commentBox}>
      {/* The passage the pending Send will annotate: the reader's retained
          capture, shown as inert literal text so the user keeps a visible
          confirmation of what the annotation points at. It is not derived from
          the live DOM selection, so editing, selection adjustment and remount
          keep showing the frozen capture. */}
      {work.capture && (
        <p data-annotation-passage="" className={CLASS.passage}>
          {work.capture.selectedRenderedText}
        </p>
      )}
      <label className={CLASS.label} htmlFor={noteId}>
        Comment
      </label>
      <Textarea id={noteId} value={work.note} onChange={handleNote} onKeyDown={handleKeyDown} minLines={2} />
      <div className={CLASS.status} role="status">
        {status}
      </div>
      <div className={CLASS.actions}>
        <Button type="button" variant="quiet" size="sm" onClick={onCancel}>
          Cancel
        </Button>
        {/* Send is unavailable while saving, while the note is blank, or while the
            reader's current raw read has suspended annotation authority. */}
        <Button type="button" variant="primary" size="sm" onClick={onSend} disabled={saving || blank || suspended}>
          Send
        </Button>
      </div>
    </div>
  );
}
