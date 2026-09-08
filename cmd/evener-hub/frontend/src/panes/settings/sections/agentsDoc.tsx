// Settings -> AGENTS.md: the personal instructions file every session loads
// ahead of the repo's own project docs (spec 2026-09-07 §1). A whole-file
// editor over stores/agentsDoc.ts: Save is dirty-gated, Revert restores the
// last loaded content, and there is no conflict precondition by design (last
// write wins) - what the section does instead is tell the user when the file
// changed under a dirty draft and let them load the current version.
//
// `baseline` is the content the draft was last synced to; `dirty` is
// draft !== baseline. A hub push (the `doc` subscription) syncs the draft
// while it is clean, and adopts a document that already equals the draft as
// the new baseline. A push whose content matches neither the baseline
// nor the draft while dirty is someone else's write, and flips `stale` -
// this client's own save lands with content equal to the draft, so it never
// reads as stale.
import { useEffect, useState } from "react";
import { friendlyErrorMessage } from "../../../protocol/errors";
import { agentsDocStore, useAgentsDocStore } from "../../../stores/agentsDoc";
import { Button, Skeleton, Textarea, useToasts } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import styles from "./agentsDoc.module.css";
import { Code } from "./settingsField";
import { useConnectedEffect } from "./useConnectedEffect";

const CLASS = {
  root: requireClass(styles.root, "agentsDoc.module.css", "root"),
  help: requireClass(styles.help, "agentsDoc.module.css", "help"),
  error: requireClass(styles.error, "agentsDoc.module.css", "error"),
  editor: requireClass(styles.editor, "agentsDoc.module.css", "editor"),
  note: requireClass(styles.note, "agentsDoc.module.css", "note"),
  actions: requireClass(styles.actions, "agentsDoc.module.css", "actions"),
};

export interface AgentsDocSectionProps {
  /** Unused - kept so this component's signature matches every other
   * dispatched settings section (see Settings.tsx's SECTION_COMPONENTS map). */
  sectionId: string;
}

export function AgentsDocSection(_props: AgentsDocSectionProps) {
  const doc = useAgentsDocStore((s) => s.doc);
  const loading = useAgentsDocStore((s) => s.loading);
  const error = useAgentsDocStore((s) => s.error);
  const [draft, setDraft] = useState("");
  const [baseline, setBaseline] = useState<string | null>(null);
  const [stale, setStale] = useState(false);
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);
  const toast = useToasts();

  // Reconnects are the store's business (stores/agentsDoc.ts refetches on
  // every one); this is the mount read. The content-keyed effect below is
  // what keeps either of them from taking a dirty draft with it.
  useConnectedEffect(() => agentsDocStore.getState().fetch(), []);

  // biome-ignore lint/correctness/useExhaustiveDependencies: doc is the only trigger; draft and baseline are read at that moment, not watched
  useEffect(() => {
    if (doc === null) return;
    if (baseline === null || draft === baseline) {
      setDraft(doc.content);
      setBaseline(doc.content);
      setStale(false);
      return;
    }
    // A document that equals the draft is what the editor already holds, so
    // there is nothing to warn about and nothing left to save.
    if (doc.content === draft) {
      setBaseline(doc.content);
      setStale(false);
      return;
    }
    if (doc.content !== baseline && doc.content !== draft) setStale(true);
  }, [doc]);

  const dirty = baseline !== null && draft !== baseline;

  async function handleSave(): Promise<void> {
    const sent = draft;
    setSaving(true);
    setSaveError(null);
    try {
      const saved = await agentsDocStore.getState().save(sent);
      // The store resolves a save with whatever document is newer than the
      // write when one landed behind it, so a save that comes back holding
      // something other than what it sent was overwritten on its way in -
      // the file moved under the user and the notice has to stay up. The
      // reference is the store's own document rather than the resolved value:
      // a response from a client the store has since replaced never lands
      // there, so it can be older than what the replacement already fetched.
      const authoritative = agentsDocStore.getState().doc?.content ?? saved.content;
      setBaseline(authoritative);
      if (authoritative === sent) {
        setStale(false);
        toast.push("success", "Saved AGENTS.md");
      } else {
        setStale(true);
        toast.push(
          "warning",
          "Saved AGENTS.md, but it changed on disk since. Review the current copy before saving again.",
        );
      }
    } catch (err) {
      setSaveError(friendlyErrorMessage(err));
    } finally {
      setSaving(false);
    }
  }

  function handleRevert(): void {
    if (baseline !== null) setDraft(baseline);
    setSaveError(null);
  }

  function handleLoadCurrent(): void {
    if (doc === null) return;
    setDraft(doc.content);
    setBaseline(doc.content);
    setStale(false);
    setSaveError(null);
  }

  if (doc === null && loading) return <Skeleton />;
  // The store has already flattened the rejection to text, and for a file
  // editor that text IS the fix - "permission denied", "is a directory" -
  // so it goes to the user as it stands (inrepo.tsx does the same).
  if (doc === null && error)
    return (
      <p className={CLASS.error} role="alert">
        Failed to load: {error}
      </p>
    );
  if (doc === null) return null; // not yet fetched: the connected effect hasn't run

  return (
    <div className={CLASS.root}>
      <h2>AGENTS.md</h2>
      <p className={CLASS.help}>
        Personal instructions loaded into every session, ahead of the repo's own AGENTS.md. Stored at{" "}
        <Code>{doc.path}</Code>.
      </p>
      <div className={CLASS.editor}>
        <Textarea
          aria-label="AGENTS.md contents"
          value={draft}
          onChange={(event) => setDraft(event.target.value)}
          minLines={16}
          autoGrow
          disabled={saving}
          placeholder="# Instructions for every session"
        />
      </div>
      {stale && (
        <p className={CLASS.note} role="status">
          AGENTS.md changed on disk while you were editing.
          <Button size="sm" variant="quiet" onClick={handleLoadCurrent}>
            Load current
          </Button>
        </p>
      )}
      {/* A refetch that failed leaves `doc` where it was, so the editor is
          showing a copy it can no longer confirm. Save stays enabled: a
          reload that failed because the hub is unreachable fails a save the
          same way, and disabling it would only trap the draft. The way out
          rides the notice, as "Load current" does above: the next read often
          succeeds, and waiting for a reconnect to fire one is not a fix. */}
      {error !== null && (
        <p className={CLASS.error} role="alert">
          Couldn't reload AGENTS.md: {error}. The editor shows the last copy it loaded; saving would overwrite anything
          changed on disk since.
          {/* The store lands its own failure in `error` above; the rejection is
              caught only so it never surfaces as an unhandled one. */}
          <Button
            size="sm"
            variant="quiet"
            onClick={() =>
              void agentsDocStore
                .getState()
                .fetch()
                .catch(() => {})
            }
          >
            Reload
          </Button>
        </p>
      )}
      {saveError !== null && (
        <p className={CLASS.error} role="alert">
          Save failed: {saveError}
        </p>
      )}
      <div className={CLASS.actions}>
        <Button onClick={() => void handleSave()} disabled={!dirty || saving}>
          Save
        </Button>
        <Button variant="quiet" onClick={handleRevert} disabled={!dirty || saving}>
          Revert
        </Button>
      </div>
    </div>
  );
}
