// dirListSetting.tsx is the parameterized directory-list settings section
// (DirListSetting) plus the reusable path-list editor it's built on
// (PathListEditor, also reused by mcp.tsx's "MCP config files" half - the
// same shape, just a different wireField/pathKind). Instantiated twice,
// byte-identically apart from the 3 params, per parity-m7-settings.md §14's
// own recommendation: pluginsDirs.tsx (wireField:"pluginDirs") and
// skillsDirs.tsx (wireField:"skillsDirs").
//
// Deliberately NOT built on widgets/collectioneditor's CollectionEditor:
// CollectionEditor unconditionally renders its own add-row (a plain Input
// bound to state it owns internally) with no slot to swap in a different
// add-field component - there is no way to compose it with PathPicker's
// Browse-button-assisted input for the SAME row. Per the dir-picker
// contract (test-settings-dir-picker.js / assets/settings-pickers.js) this
// wave's floor docs hold PathListSetting to, both plugins.html AND
// skills.html wire an explicit Browse button (`data-settings-dir-picker`)
// alongside inline typeahead on every directory add-row - dropping that to
// reuse CollectionEditor's plain input would be a real UX regression, not a
// simplification, so this hand-rolls the list+add-row directly on
// PathPicker instead (visually mirroring CollectionEditor's own row/list
// styling for consistency). See the wave-7 task-3 report for the full
// reasoning.
import { useId, useState } from "react";
import type { LaunchConfigLayer } from "../../../protocol/types.gen";
import { extensionsStore, useExtensionsStore } from "../../../stores/extensions";
import {
  Button,
  type CollectionAddResult,
  ConfirmDialog,
  EmptyState,
  FormRow,
  IconButton,
  PathPicker,
  Skeleton,
  useToasts,
} from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import styles from "./dirListSetting.module.css";
import { useConnectedEffect } from "./useConnectedEffect";

const CLASS = {
  section: requireClass(styles.section, "dirListSetting.module.css", "section"),
  title: requireClass(styles.title, "dirListSetting.module.css", "title"),
  help: requireClass(styles.help, "dirListSetting.module.css", "help"),
  root: requireClass(styles.root, "dirListSetting.module.css", "root"),
  list: requireClass(styles.list, "dirListSetting.module.css", "list"),
  row: requireClass(styles.row, "dirListSetting.module.css", "row"),
  content: requireClass(styles.content, "dirListSetting.module.css", "content"),
  empty: requireClass(styles.empty, "dirListSetting.module.css", "empty"),
  addRow: requireClass(styles.addRow, "dirListSetting.module.css", "addRow"),
  addField: requireClass(styles.addField, "dirListSetting.module.css", "addField"),
};

function RemoveIcon() {
  return (
    <svg viewBox="0 0 12 12" width="10" height="10" aria-hidden="true">
      <path d="M2 2 L10 10 M10 2 L2 10" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" />
    </svg>
  );
}

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

export interface PathListEditorProps {
  /** Accessible name for the row list. */
  label: string;
  /** Visible FormRow label for the add field specifically (e.g. "New
   * directory" / "New config file") - distinct from `label` (the list's own
   * name) so the two don't render as a visually duplicated heading. */
  addLabel: string;
  items: readonly string[];
  onAdd: (path: string) => Promise<CollectionAddResult>;
  /** Fires only once the caller confirms the ConfirmDialog this widget
   * shows itself - unlike CollectionEditor's onRemove (immediate, no
   * confirm), every row removal here confirms first, per this wave's
   * binding "every destructive action confirms" constraint. */
  onRemove: (path: string) => void;
  listChildren: (path: string) => Promise<string[]>;
  emptyMessage: string;
  removeConfirmTitle: string;
  removeConfirmBody: (path: string) => string;
  addPlaceholder?: string;
}

/**
 * The generic "list of paths" editor: PathPicker-assisted add row (Browse
 * button + inline typeahead, matching plugins.html/skills.html's contract),
 * a ConfirmDialog-gated remove button per row, and an inline validation
 * error next to the add row (matching the legacy's own `.row-error`
 * placement - a save/validate failure is shown right next to the field
 * that caused it, not toasted). Reused by DirListSetting below and by
 * mcp.tsx's "MCP config files" list.
 */
export function PathListEditor({
  label,
  addLabel,
  items,
  onAdd,
  onRemove,
  listChildren,
  emptyMessage,
  removeConfirmTitle,
  removeConfirmBody,
  addPlaceholder = "/absolute/path",
}: PathListEditorProps) {
  const [draft, setDraft] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [pending, setPending] = useState<string | null>(null);
  const addFieldId = useId();

  const trimmed = draft.trim();

  async function handleAdd() {
    if (trimmed === "" || busy) return;
    setBusy(true);
    const result = await onAdd(trimmed);
    setBusy(false);
    if (result.ok) {
      setDraft("");
      setError(null);
    } else {
      setError(result.error);
    }
  }

  return (
    <div className={CLASS.root}>
      <ul aria-label={label} className={CLASS.list}>
        {items.length === 0 ? (
          <li className={CLASS.empty}>{emptyMessage}</li>
        ) : (
          items.map((item) => (
            <li key={item} className={CLASS.row}>
              <div className={CLASS.content}>{item}</div>
              <IconButton
                label={`Remove ${item}`}
                icon={<RemoveIcon />}
                variant="quiet"
                size="sm"
                onClick={() => setPending(item)}
              />
            </li>
          ))
        )}
      </ul>
      {/* FormRow (not a hand-rolled error paragraph): its error treatment
          is already on token-contract.test.ts's --danger allowlist, and a
          pane-level stylesheet like this one's own module.css cannot add
          itself to that widget-only allowlist - see the wave-7 task-3
          report for the full reasoning. */}
      <FormRow label={addLabel} htmlFor={addFieldId} error={error ?? undefined}>
        <div className={CLASS.addRow}>
          <span className={CLASS.addField}>
            <PathPicker
              id={addFieldId}
              value={draft}
              onChange={setDraft}
              listChildren={listChildren}
              placeholder={addPlaceholder}
              disabled={busy}
            />
          </span>
          <Button onClick={() => void handleAdd()} disabled={trimmed === "" || busy}>
            Add
          </Button>
        </div>
      </FormRow>
      <ConfirmDialog
        open={pending !== null}
        title={removeConfirmTitle}
        confirmLabel="Remove"
        onConfirm={() => {
          onRemove(pending as string);
          setPending(null);
        }}
        onCancel={() => setPending(null)}
      >
        {pending !== null ? removeConfirmBody(pending) : ""}
      </ConfirmDialog>
    </div>
  );
}

export interface DirListSettingProps {
  /** Which LaunchConfigLayer array field this instance edits. */
  wireField: "pluginDirs" | "skillsDirs";
  label: string;
  copy: string;
}

/**
 * The parameterized directory-list settings section - one component behind
 * both Plugins (dirs) and Skills (dirs), per parity-m7-settings.md §14's own
 * "collapse to one parameterized DirListSetting widget" recommendation.
 * Fetches the global launch layer on mount (gated on the shared client
 * actually being ready - see the mount effect's own comment), validates an
 * add via serf/path/validate before saving, and confirms every row removal.
 */
export function DirListSetting({ wireField, label, copy }: DirListSettingProps) {
  const layer = useExtensionsStore((s) => s.launchLayer);
  const loading = useExtensionsStore((s) => s.launchLayerLoading);
  const error = useExtensionsStore((s) => s.launchLayerError);
  const toasts = useToasts();

  // useConnectedEffect (not a bare useEffect): a direct deep link to
  // /settings/plugins-dirs or /settings/skills-dirs can mount this section
  // before AppShell's own connect() handshake finishes, and fetchLaunchLayer
  // requires a connected client - see that hook's own doc comment for the
  // race this guards against.
  useConnectedEffect(() => extensionsStore.getState().fetchLaunchLayer(), []);

  async function handleAdd(path: string): Promise<CollectionAddResult> {
    const validated = await extensionsStore.getState().validatePath(path, "dir");
    if (!validated.valid) return { ok: false, error: validated.error || "path does not exist" };
    const current = extensionsStore.getState().launchLayer ?? ({} as LaunchConfigLayer);
    const canonical = validated.path || path;
    const nextList = [...(current[wireField] ?? []), canonical];
    try {
      await extensionsStore.getState().setLaunchLayer({ ...current, [wireField]: nextList });
      return { ok: true };
    } catch (err) {
      return { ok: false, error: errorMessage(err) };
    }
  }

  function handleRemove(path: string) {
    const current = extensionsStore.getState().launchLayer ?? ({} as LaunchConfigLayer);
    const nextList = (current[wireField] ?? []).filter((p) => p !== path);
    extensionsStore
      .getState()
      .setLaunchLayer({ ...current, [wireField]: nextList })
      .catch((err: unknown) => {
        toasts.push("error", `Remove failed: ${errorMessage(err)}`);
      });
  }

  const lowerLabel = label.toLowerCase();

  return (
    <section className={CLASS.section}>
      <h2 className={CLASS.title}>{label}</h2>
      <p className={CLASS.help}>{copy}</p>
      {error !== null ? (
        <EmptyState title="Failed to load" hint={error} />
      ) : loading && layer === null ? (
        <Skeleton />
      ) : (
        <PathListEditor
          label={label}
          addLabel="New directory"
          items={layer?.[wireField] ?? []}
          onAdd={handleAdd}
          onRemove={handleRemove}
          listChildren={(path) => extensionsStore.getState().listDirChildren(path)}
          emptyMessage={`No ${lowerLabel}. Add one below.`}
          removeConfirmTitle="Remove directory"
          removeConfirmBody={(path) => `Remove "${path}" from ${lowerLabel}?`}
        />
      )}
    </section>
  );
}
