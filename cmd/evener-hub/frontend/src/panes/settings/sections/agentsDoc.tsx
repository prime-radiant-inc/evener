// Settings -> AGENTS.md: the personal instructions file every session loads
// ahead of the repo's own project docs (spec 2026-09-07 §1). A whole-file
// editor over stores/agentsDoc.ts: Save is dirty-gated, Revert restores the
// last loaded content, and there is no conflict precondition by design (last
// write wins) - what the section does instead is tell the user when the file
// changed under a dirty draft and let them load the current version.
//
// `baseline` is the content the draft was last synced to; `dirty` is
// draft !== baseline. A hub push (the `doc` subscription) syncs the draft
// only while it is clean. A push whose content matches neither the baseline
// nor the draft while dirty is someone else's write, and flips `stale` -
// this client's own save lands with content equal to the draft, so it never
// reads as stale.
import { useEffect, useId, useState } from "react";
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
  const fieldId = useId();

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
    if (doc.content !== baseline && doc.content !== draft) setStale(true);
  }, [doc]);

  const dirty = baseline !== null && draft !== baseline;

  async function handleSave(): Promise<void> {
    setSaving(true);
    setSaveError(null);
    try {
      const saved = await agentsDocStore.getState().save(draft);
      setBaseline(saved.content);
      setStale(false);
      toast.push("success", "Saved AGENTS.md");
    } catch (err) {
      setSaveError(friendlyErrorMessage(err));
    } finally {
      setSaving(false);
    }
  }

  function handleRevert(): void {
    if (baseline !== null) setDraft(baseline);
  }

  function handleLoadCurrent(): void {
    if (doc === null) return;
    setDraft(doc.content);
    setBaseline(doc.content);
    setStale(false);
  }

  if (doc === null && loading) return <Skeleton />;
  if (doc === null && error) return <p className={CLASS.error}>Failed to load: {friendlyErrorMessage(error)}</p>;
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
          id={fieldId}
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
