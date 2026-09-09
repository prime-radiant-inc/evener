// NotesPanel: the session's shared notes as their own panel, beside
// Tasks/Activity/Details rather than a section inside Details.
//
// Three stacked cards share DetailsPanel's section rhythm (a titled group
// over rows, separated by the same hairline rule) so the two panels read as
// one family:
//   - "Your note": the human textarea, permanently visible while the session
//     is live - no Edit/Add trigger, no Save button. The draft saves on blur
//     (and on unmount/session-switch as a safety net) when it differs from
//     the stored note. A failed save keeps the draft in place with an inline
//     error plus the wave's toast convention, never a silent loss.
//   - "Agent": the agent's note, read-only always (strict ownership: only
//     the agent writes it, via its own tools).
//   - "Links": the session URL list with quiet Remove buttons while live.
//
// Display follows the same ordered rule DetailsPanel's section used (design
// spec §Hub Details UI): (1) capability unset hides the whole panel body;
// (2) capability set but session not live shows read-only with no editor
// and no remove buttons; (3) capability set and live shows full editing.
//
// Draft ownership: the textarea owns the draft from focus until blur. An
// incoming push (or a session switch) reseeds it ONLY while unfocused, so a
// push can never clobber mid-keystroke typing. Blur compares against the
// latest stored note, so saving after a push that already carried the same
// text is a no-op, not a redundant RPC.
import { forwardRef, useEffect, useImperativeHandle, useRef, useState } from "react";
import { sessionActionError } from "../../../protocol/errors";
import type { ThreadModel } from "../../../protocol/model";
import type { SessionURL } from "../../../protocol/types.gen";
import { threadsStore } from "../../../stores/threads";
import { Button, Sheet, Textarea, useToasts } from "../../../widgets";
import { isWebHref } from "../../../widgets/contextcard";
import { requireClass } from "../../../widgets/internal/requireClass";
import styles from "./notespanel.module.css";

export interface NotesPanelBodyProps {
  sessionRef: string;
  model: ThreadModel;
}

export interface NotesPanelProps extends NotesPanelBodyProps {
  // True when SessionChrome's row has collapsed this panel's trigger into the
  // "..." menu instead (the same hideTrigger contract Tasks/Details carry).
  hideTrigger?: boolean;
}

/** Lets SessionChrome open this panel's Sheet from the menu, without lifting
 * `open` out of this component (the same rationale as DetailsPanelHandle). */
export interface NotesPanelHandle {
  open: () => void;
}

const CLASS = {
  section: requireClass(styles.section, "notespanel.module.css", "section"),
  sectionTitle: requireClass(styles.sectionTitle, "notespanel.module.css", "sectionTitle"),
  editor: requireClass(styles.editor, "notespanel.module.css", "editor"),
  status: requireClass(styles.status, "notespanel.module.css", "status"),
  statusError: requireClass(styles.statusError, "notespanel.module.css", "statusError"),
  prose: requireClass(styles.prose, "notespanel.module.css", "prose"),
  linkList: requireClass(styles.linkList, "notespanel.module.css", "linkList"),
  linkRow: requireClass(styles.linkRow, "notespanel.module.css", "linkRow"),
  linkUrl: requireClass(styles.linkUrl, "notespanel.module.css", "linkUrl"),
  dim: requireClass(styles.dim, "notespanel.module.css", "dim"),
};

// The wire statuses that mean this session's story is over for shared-notes
// affordances. Restated here (not imported) because Composer.tsx's
// ENDED_STATUSES is module-private by reviewer decision; keep the three
// values ("ended", "closed", "notLoaded") in sync with DetailsPanel's copy
// until that copy is deleted with the section move.
const NOTES_ENDED_STATUSES: ReadonlySet<string> = new Set(["ended", "closed", "notLoaded"]);

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section className={CLASS.section}>
      <h3 className={CLASS.sectionTitle}>{title}</h3>
      {children}
    </section>
  );
}

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
    return (
      <p className={CLASS.status} data-testid="shared-notes-saving" role="status">
        Saving…
      </p>
    );
  }
  if (error !== null) {
    return (
      <p className={CLASS.statusError} data-testid="shared-notes-error" role="alert">
        {error}
      </p>
    );
  }
  if (saved) {
    return (
      <p className={CLASS.status} data-testid="shared-notes-saved" role="status">
        Saved
      </p>
    );
  }
  if (idleWake) {
    return (
      <p className={CLASS.status} data-testid="shared-notes-idle-wake">
        Saving will wake the agent.
      </p>
    );
  }
  return null;
}

export function NotesPanelBody({ sessionRef, model }: NotesPanelBodyProps) {
  const toasts = useToasts();
  const [draft, setDraft] = useState(model.humanNote);
  const [focused, setFocused] = useState(false);
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // saveInFlight guards the unmount/session-switch flush against a blur-save
  // already awaiting its RPC: without it both paths could dispatch
  // notes/human/set for the same text.
  const saveInFlight = useRef(false);
  const draftRef = useRef(draft);
  draftRef.current = draft;
  const modelNoteRef = useRef(model.humanNote);
  modelNoteRef.current = model.humanNote;

  const live = !NOTES_ENDED_STATUSES.has(model.status.type);
  const idleWake = live && model.status.type === "idle";

  // A different session starts a fresh draft lifetime; a push for the same
  // session reseeds only while unfocused, so live typing is never clobbered.
  // The reseed compares against the previously seen stored note (not merely
  // the focus transition): blur itself flips focused with the stored note
  // unchanged, and resetting there would wipe the in-flight draft of a save
  // that then fails - the exact loss the no-optimistic-state rule forbids.
  // biome-ignore lint/correctness/useExhaustiveDependencies: sessionRef is the deliberate draft-reset boundary
  useEffect(() => {
    setDraft(model.humanNote);
    setSaved(false);
    setError(null);
  }, [sessionRef]);
  const prevStoredNote = useRef(model.humanNote);
  useEffect(() => {
    if (prevStoredNote.current !== model.humanNote) {
      prevStoredNote.current = model.humanNote;
      if (!focused) setDraft(model.humanNote);
    }
  }, [focused, model.humanNote]);

  async function persist(note: string) {
    if (saveInFlight.current) return;
    saveInFlight.current = true;
    setSaving(true);
    setSaved(false);
    setError(null);
    try {
      await threadsStore.getState().setHumanNote(sessionRef, note);
      setSaved(true);
    } catch (err) {
      const message = sessionActionError("Couldn't save note", err);
      setError(message);
      toasts.push("error", message);
    } finally {
      setSaving(false);
      saveInFlight.current = false;
    }
  }

  // Blur saves when the draft differs from the latest stored note. The
  // flush below (unmount/session-switch) shares this comparison through the
  // refs, so whichever path runs first wins and the second is a no-op.
  function handleBlur() {
    setFocused(false);
    if (!live) return;
    const note = draftRef.current;
    if (note === modelNoteRef.current) return;
    void persist(note);
  }

  // Safety net for a blur that never fires: switching sessions or closing
  // the panel while focused. Blur-first ordering means this is a no-op in
  // the common case (blur already persisted or found nothing to save).
  // biome-ignore lint/correctness/useExhaustiveDependencies: the flush must capture the final draft without re-arming per keystroke
  useEffect(() => {
    return () => {
      if (!live) return;
      const note = draftRef.current;
      if (note === modelNoteRef.current) return;
      void threadsStore
        .getState()
        .setHumanNote(sessionRef, note)
        .catch(() => {});
    };
  }, [sessionRef]);

  async function handleRemoveURL(url: SessionURL) {
    try {
      await threadsStore.getState().removeURL(sessionRef, url.id);
    } catch (err) {
      toasts.push("error", sessionActionError("Couldn't remove link", err));
    }
  }

  // Rule 1 of the ordered display rule: capability unset hides the panel
  // body entirely. After the hooks above (Rules of Hooks).
  if (!model.capabilities.sharedNotes) return null;

  const hasContent = model.humanNote !== "" || model.agentNote !== "" || model.sessionUrls.length > 0;

  return (
    <div data-testid="shared-notes-section">
      <Section title="Your note">
        {live ? (
          <>
            <div className={CLASS.editor} data-testid="shared-notes-editor">
              <Textarea
                value={draft}
                onChange={(event) => {
                  setDraft(event.target.value);
                  setSaved(false);
                }}
                onFocus={() => setFocused(true)}
                onBlur={handleBlur}
                aria-label="Human note"
                placeholder="Add context for the agent…"
                autoGrow
                rows={4}
              />
            </div>
            <HumanStatus saving={saving} saved={saved} error={error} idleWake={idleWake} />
          </>
        ) : model.humanNote !== "" ? (
          <p className={CLASS.prose} data-testid="shared-notes-human">
            {model.humanNote}
          </p>
        ) : null}
      </Section>
      {(model.agentNote !== "" || live) && (
        <Section title="Agent">
          {model.agentNote !== "" ? (
            <p className={CLASS.prose} data-testid="shared-notes-agent">
              {model.agentNote}
            </p>
          ) : (
            <p className={CLASS.dim} data-testid="shared-notes-agent-empty">
              No agent note yet
            </p>
          )}
        </Section>
      )}
      {(model.sessionUrls.length > 0 || live) && (
        <Section title="Links">
          {model.sessionUrls.length > 0 ? (
            <ul className={CLASS.linkList} data-testid="shared-notes-urls">
              {model.sessionUrls.map((url) => (
                <li key={url.id} className={CLASS.linkRow} data-testid={`shared-notes-url-${url.id}`}>
                  {isWebHref(url.url) ? (
                    <a href={url.url} target="_blank" rel="noopener noreferrer">
                      {url.label || url.url}
                    </a>
                  ) : (
                    <span>
                      {url.label || url.url} <span className={CLASS.linkUrl}>{url.url}</span>
                    </span>
                  )}
                  {live && (
                    <Button
                      variant="quiet"
                      size="sm"
                      onClick={() => void handleRemoveURL(url)}
                      aria-label={`Remove ${url.label || url.url}`}
                      data-testid={`shared-notes-url-remove-${url.id}`}
                    >
                      Remove
                    </Button>
                  )}
                </li>
              ))}
            </ul>
          ) : (
            <p className={CLASS.dim} data-testid="shared-notes-urls-empty">
              No links yet
            </p>
          )}
        </Section>
      )}
      {!live && !hasContent && (
        <Section title="Notes">
          <p className={CLASS.dim} data-testid="shared-notes-empty">
            No shared notes
          </p>
        </Section>
      )}
    </div>
  );
}

/** Shared stateless notes body used by the mobile Sheet and desktop pane. */
export const NotesPanel = forwardRef<NotesPanelHandle, NotesPanelProps>(function NotesPanel(
  { sessionRef, model, hideTrigger = false },
  ref,
) {
  const [open, setOpen] = useState(false);
  useImperativeHandle(ref, () => ({ open: () => setOpen(true) }), []);

  return (
    <>
      {/* Omitted while hideTrigger is set (SessionChrome collapses this into
          the "..." menu instead). The palette's /notes toggles the
          sessionNotes workspace pane (shell/palette/commands.ts). */}
      {!hideTrigger && (
        <Button variant="quiet" size="sm" onClick={() => setOpen(true)}>
          Notes
        </Button>
      )}
      <Sheet open={open} onClose={() => setOpen(false)} title="Session notes">
        {open ? <NotesPanelBody sessionRef={sessionRef} model={model} /> : null}
      </Sheet>
    </>
  );
});
