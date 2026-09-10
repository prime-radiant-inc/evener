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
  // saveLoop is the ONE in-flight save chain for this panel: requestSave
  // always lands on the latest draft (coalesced, never dropped) and the chain
  // settles only when the draft it last persisted still matches the store.
  // Every save path - blur, the unmount/session-switch flush, the
  // live-to-ended flush - funnels through it, so two of them racing can
  // never issue a second untracked mutation with a fresh client mutation ID.
  const saveLoop = useRef<Promise<void> | null>(null);
  // dirtyRef is saveLoop's pending-draft queue: one entry per session holding
  // that session's LATEST draft, drained FIFO. A ref (not state) because the
  // queue only ever matters to the loop itself - no render reads it. Keyed
  // by session so a B-save parking behind an in-flight A-save cannot
  // overwrite A's draft: the loop persists every queued session in order.
  const dirtyRef = useRef(new Map<string, string>());
  // snapRef records every rendered session's latest draft and liveness, keyed
  // by ref. The sessionRef-change cleanup below runs AFTER the incoming
  // session's render already overwrote the shared render-scope refs, so
  // reading those at cleanup time would compare the outgoing draft against
  // the INCOMING stored note; the snapshot map keeps old-against-old.
  const snapRef = useRef(new Map<string, { draft: string; live: boolean }>());
  const live = !NOTES_ENDED_STATUSES.has(model.status.type);
  snapRef.current.set(sessionRef, { draft, live });
  const draftRef = useRef(draft);
  draftRef.current = draft;
  const modelNoteRef = useRef(model.humanNote);
  modelNoteRef.current = model.humanNote;
  // uiRef is the session the status line currently describes. The loop only
  // touches saving/saved/error while its session is still mounted: without
  // the guard a slow save for session A settling after the switch to B would
  // paint "Saved" under B's editor (whose own reset already cleared it).
  const uiRef = useRef(sessionRef);
  uiRef.current = sessionRef;

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

  // storedNote reads the threads store directly (not the render-scope model):
  // setHumanNote commits locally on success and pushes update the store, so
  // the store is the freshest committed value - and it stays correct for an
  // outgoing session after a switch, when the render-scope model moved on.
  function storedNote(ref: string): string | undefined {
    return threadsStore.getState().threads.get(ref)?.humanNote;
  }

  // requestSave persists note for ref unless it already matches the latest
  // stored text, coalescing with an in-flight save: a second request while
  // one is running parks its draft in the per-session queue, and the loop
  // drains every queued session before settling - focus-blur-focus-blur on
  // a slow RPC converges on the latest text instead of losing the newer
  // keystrokes, and a B-save behind an in-flight A-save persists in turn
  // rather than overwriting A's draft. Sequential setHumanNote calls
  // serialize per-thread in the store, so cross-session drains land in
  // queue order.
  function requestSave(ref: string, note: string) {
    // Matching the store means nothing to persist; drop any stale queued
    // entry for the session (e.g. a kept failure the store has since
    // converged with via push) so it can never block the Saved guard.
    if (note === storedNote(ref)) {
      dirtyRef.current.delete(ref);
      return;
    }
    dirtyRef.current.set(ref, note);
    if (saveLoop.current !== null) return;
    if (uiRef.current === ref) {
      setSaving(true);
      setSaved(false);
      setError(null);
    }
    saveLoop.current = (async () => {
      // Outcomes are tracked PER SESSION: successes accumulate in a set (a
      // later B-failure must not erase an earlier A-success), and each
      // failure is recorded beside its own ref (a newer A-save succeeding in
      // the same drain must not leave the older A-failure queued to overwrite
      // it on retry). A failure does NOT requeue into this drain: retrying it
      // here would hot-loop a persistently failing RPC (request, toast, and
      // saving state forever). The draft stays in the textarea, so the next
      // explicit save (e.g. the next blur) retries with the latest text.
      const savedRefs = new Set<string>();
      const failedMessages = new Map<string, string>();
      for (;;) {
        const next = dirtyRef.current.entries().next();
        if (next.done) break;
        const [nextRef, nextNote] = next.value;
        dirtyRef.current.delete(nextRef);
        if (nextNote === storedNote(nextRef)) continue;
        try {
          await threadsStore.getState().setHumanNote(nextRef, nextNote);
        } catch (err) {
          const message = sessionActionError("Couldn't save note", err);
          failedMessages.set(nextRef, message);
          if (uiRef.current === nextRef) setError(message);
          toasts.push("error", message);
          continue;
        }
        // Success clears only this session's failure: a sibling's failure is
        // that sibling's latest outcome and stays reported.
        failedMessages.delete(nextRef);
        savedRefs.add(nextRef);
      }
      saveLoop.current = null;
      // saving is global to the panel (one loop at a time), so it always
      // clears on settle; Saved and error paint only for the session still on
      // screen, from that session's own latest outcome.
      setSaving(false);
      const ui = uiRef.current;
      const uiFailure = failedMessages.get(ui);
      if (uiFailure !== undefined) setError(uiFailure);
      if (savedRefs.has(ui) && uiFailure === undefined && draftRef.current === storedNote(ui)) {
        setSaved(true);
      }
    })();
  }

  // Blur saves when the draft differs from the latest stored note. The
  // flushes below (unmount/session-switch, live-to-ended) share requestSave,
  // so whichever path runs first wins and the rest coalesce instead of
  // racing.
  function handleBlur() {
    setFocused(false);
    const snap = snapRef.current.get(sessionRef);
    if (!snap?.live) return;
    requestSave(sessionRef, draftRef.current);
  }

  // Safety net for a blur that never fires: switching sessions, closing the
  // panel, or the session ending while focused (a textarea removed from the
  // DOM is not guaranteed to emit blur). Blur-first ordering means this is a
  // no-op in the common case (blur already persisted or found nothing to
  // save). The outgoing session's values come from its snapshot (captured
  // every render), NOT the shared render-scope refs the incoming session's
  // commit already overwrote: old draft flushes against the old thread, and
  // an old-draft/new-note string coincidence can never skip the save.
  // biome-ignore lint/correctness/useExhaustiveDependencies: the flush must capture the outgoing session without re-arming per render
  useEffect(() => {
    return () => {
      const snap = snapRef.current.get(sessionRef);
      snapRef.current.delete(sessionRef);
      if (!snap?.live) return;
      requestSave(sessionRef, snap.draft);
    };
  }, [sessionRef]);

  // Live-to-ended flush: when the session's story ends while a draft is
  // dirty, the editor unmounts (read-only takes over) without blur firing.
  // Flush while the RPC path is still valid for the stored text on hand.
  useEffect(() => {
    if (!live && draftRef.current !== modelNoteRef.current) {
      requestSave(sessionRef, draftRef.current);
    }
    // Deps are the transition inputs: a status-type or stored-note change
    // re-evaluates whether a dirty draft needs flushing.
    // biome-ignore lint/correctness/useExhaustiveDependencies: draft/focus churn must not re-arm the flush
  }, [model.status.type, model.humanNote]);

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
                    // The destination stays visible beside an agent-controlled
                    // label: a trusted-looking label over a phishing URL must
                    // never display the label alone. Matches the file-link and
                    // TUI `label (URL) [id]` shape.
                    <span>
                      <a href={url.url} target="_blank" rel="noopener noreferrer">
                        {url.label || url.url}
                      </a>{" "}
                      {url.label !== "" && <span className={CLASS.linkUrl}>{url.url}</span>}
                    </span>
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
