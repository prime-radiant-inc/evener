import { forwardRef, useEffect, useImperativeHandle, useRef, useState } from "react";
import { sessionActionError } from "../../../protocol/errors";
import type { ThreadModel } from "../../../protocol/model";
import { canReadSharedNotes } from "../../../protocol/sharedNotesAvailability";
import type { SessionURL } from "../../../protocol/types.gen";
import {
  blurHumanNote,
  canWriteHumanNote,
  editHumanNote,
  focusHumanNote,
  syncHumanNote,
  unmountHumanNote,
  useHumanNoteDraft,
} from "../../../stores/humanNoteDrafts";
import { threadsStore } from "../../../stores/threads";
import { Button, Sheet, Textarea, useToasts } from "../../../widgets";
import { isWebHref } from "../../../widgets/contextcard";
import { requireClass } from "../../../widgets/internal/requireClass";
import { FileOpenBesideButton } from "../transcript/fileOpenBeside";
import styles from "./notespanel.module.css";

// Session file URLs canonicalize to file:///absolute forms (agent
// validation); the open-beside flow takes a filesystem path, so decode the
// URL path back (percent-escapes from delimiter filenames included).
export function fileURLToPath(fileURL: string): string {
  let url: URL;
  try {
    url = new URL(fileURL);
  } catch {
    // Not a URL at all: give the open-beside flow no path rather than the raw
    // string it would treat as one.
    return "";
  }
  try {
    return decodeURIComponent(url.pathname);
  } catch {
    // A malformed escape must not turn the whole file:/// URL into the
    // open-beside target; the undecoded path is the closest true answer.
    return url.pathname;
  }
}

function isFileHref(href: string): boolean {
  return /^file:\/\//.test(href);
}

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
  const owner = useRef(Symbol("notes editor"));
  const state = useHumanNoteDraft(sessionRef);
  const draft = state?.text ?? model.humanNote;
  const saving = !!state?.submitted && !state.error;
  const saved = state?.saved ?? false;
  const error = state?.error ?? null;
  // Editability here tracks the store's own write gate: rendering controls that
  // setHumanNote/RemoveSessionURL would refuse leaves dead affordances.
  const live = canWriteHumanNote(model);
  const idleWake = live && model.status.type === "idle";
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

  async function handleRemoveURL(url: SessionURL) {
    try {
      await threadsStore.getState().removeURL(sessionRef, url.id);
    } catch (err) {
      toasts.push("error", sessionActionError("Couldn't remove link", err));
    }
  }

  // Rule 1 of the ordered display rule: capability unset hides the panel
  // body entirely. After the hooks above (Rules of Hooks).
  if (!canReadSharedNotes(model)) return null;

  const hasContent = model.humanNote !== "" || model.agentNote !== "" || model.sessionUrls.length > 0;

  return (
    <div data-testid="shared-notes-section">
      <Section title="Your note">
        {live ? (
          <>
            <div className={CLASS.editor} data-testid="shared-notes-editor">
              <Textarea
                value={draft}
                onChange={(event) => editHumanNote(sessionRef, event.target.value)}
                onFocus={() => focusHumanNote(sessionRef, owner.current)}
                onBlur={() => blurHumanNote(sessionRef, owner.current)}
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
                      {url.label ? <span className={CLASS.linkUrl}>{url.url}</span> : null}
                    </span>
                  ) : isFileHref(url.url) ? (
                    // Local file references open through the in-app
                    // document/open-beside flow (design requirement), with the
                    // URL text retained. Out-of-scope paths keep the visible
                    // text but no affordance.
                    <span>
                      {url.label || url.url} <span className={CLASS.linkUrl}>{url.url}</span>{" "}
                      <FileOpenBesideButton absPath={fileURLToPath(url.url)} sessionRef={sessionRef} cwd={model.cwd} />
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
  const canReadNotes = canReadSharedNotes(model);
  useImperativeHandle(
    ref,
    () => ({
      open: () => {
        if (canReadNotes) setOpen(true);
      },
    }),
    [canReadNotes],
  );

  return (
    <>
      {/* Omitted while hideTrigger is set (SessionChrome collapses this into
          the "..." menu instead). The palette's /notes toggles the
          sessionNotes workspace pane (shell/palette/commands.ts). */}
      {!hideTrigger && canReadNotes && (
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
