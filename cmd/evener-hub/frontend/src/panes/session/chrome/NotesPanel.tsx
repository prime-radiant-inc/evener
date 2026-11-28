import type { ThreadModel } from "@evener/appwire-client";
import { canReadSharedNotes, canWriteSharedNotes, isSessionResting } from "@evener/appwire-client";
import { useEffect, useRef } from "react";
import {
  blurHumanNote,
  editHumanNote,
  focusHumanNote,
  syncHumanNote,
  unmountHumanNote,
  useHumanNoteDraft,
} from "../../../stores/humanNoteDrafts";
import { EmptyState, InlineNotice, Textarea } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import styles from "./notespanel.module.css";
import { PanelSection } from "./PanelSection";

/** A note carries content only when something survives trimming. The
 * collapsed summary and the expanded body must judge emptiness the same
 * way, or a whitespace-only note reads "Add a note…" collapsed while
 * expanding shows a blank paragraph. */
export function hasNoteText(text: string): boolean {
  return text.trim() !== "";
}

export interface NotesPanelBodyProps {
  sessionRef: string;
  model: ThreadModel;
  editorRef?: React.Ref<HTMLTextAreaElement>;
}

const CLASS = {
  editorGroup: requireClass(styles.editorGroup, "notespanel.module.css", "editorGroup"),
  prose: requireClass(styles.prose, "notespanel.module.css", "prose"),
};

// The human note's save status line: exactly one of saving / saved / error /
// idle-hint / nothing. The idle-wake copy names the steering-carrier cost
// before the save (design spec §Wire RPCs), mirroring the composer; it shows
// persistently while idle rather than only mid-save so the cost is visible
// before any typing begins.
function HumanStatus({
  saving,
  saved,
  error,
  idleWake,
}: {
  saving: boolean;
  saved: boolean;
  error: string | null;
  idleWake: boolean;
}) {
  if (saving) {
    return <InlineNotice data-testid="shared-notes-saving">Saving…</InlineNotice>;
  }
  if (error !== null) {
    return (
      <InlineNotice tone="danger" data-testid="shared-notes-error">
        {error}
      </InlineNotice>
    );
  }
  if (saved) {
    return <InlineNotice data-testid="shared-notes-saved">Saved</InlineNotice>;
  }
  if (idleWake) {
    return (
      <InlineNotice live="off" data-testid="shared-notes-idle-wake">
        Saving will wake the agent.
      </InlineNotice>
    );
  }
  return null;
}

export function NotesPanelBody({ sessionRef, model, editorRef }: NotesPanelBodyProps) {
  const owner = useRef(Symbol("notes editor"));
  const state = useHumanNoteDraft(sessionRef);
  const draft = state?.text ?? model.humanNote;
  const saving = !!state?.submitted && !state.error;
  const saved = state?.saved ?? false;
  const error = state?.error ?? null;
  // Editability here tracks the store's own write gate: rendering controls that
  // setHumanNote would refuse leaves dead affordances.
  const live = canWriteSharedNotes(model);
  const idleWake = live && isSessionResting(model.status.type);
  useEffect(() => {
    syncHumanNote(sessionRef, model.humanNote);
  }, [sessionRef, model.humanNote]);
  useEffect(() => {
    const id = owner.current;
    if (!live || !model.capabilities.sharedNotes) {
      unmountHumanNote(sessionRef, id);
      return;
    }
    // Close-without-blur keeps the draft and invents no save; a timer from an
    // actual earlier blur survives this unmount and still fires.
    return () => unmountHumanNote(sessionRef, id);
  }, [sessionRef, live, model.capabilities.sharedNotes]);

  // Rule 1 of the ordered display rule: capability unset hides the panel
  // body entirely. After the hooks above (Rules of Hooks).
  if (!canReadSharedNotes(model)) return null;

  const hasContent = hasNoteText(model.humanNote) || hasNoteText(model.agentNote);

  return (
    <div data-testid="shared-notes-section">
      <PanelSection title="Your note">
        {live ? (
          <div className={CLASS.editorGroup}>
            <div data-testid="shared-notes-editor">
              <Textarea
                ref={editorRef}
                value={draft}
                onChange={(event) => editHumanNote(sessionRef, event.target.value)}
                onFocus={() => focusHumanNote(sessionRef, owner.current)}
                onBlur={() => blurHumanNote(sessionRef, owner.current)}
                aria-label="Human note"
                placeholder="Make a note…"
                autoGrow
                rows={4}
                fill
              />
            </div>
            <HumanStatus saving={saving} saved={saved} error={error} idleWake={idleWake} />
          </div>
        ) : hasNoteText(model.humanNote) ? (
          <p className={CLASS.prose} data-testid="shared-notes-human">
            {model.humanNote}
          </p>
        ) : null}
      </PanelSection>
      {(hasNoteText(model.agentNote) || live) && (
        <PanelSection title="Agent">
          {hasNoteText(model.agentNote) ? (
            <p className={CLASS.prose} data-testid="shared-notes-agent">
              {model.agentNote}
            </p>
          ) : (
            <div data-testid="shared-notes-agent-empty">
              <EmptyState size="compact" title="No agent note yet" />
            </div>
          )}
        </PanelSection>
      )}
      {!live && !hasContent && (
        <PanelSection title="Notes">
          <EmptyState size="compact" title="No shared notes" />
        </PanelSection>
      )}
    </div>
  );
}
