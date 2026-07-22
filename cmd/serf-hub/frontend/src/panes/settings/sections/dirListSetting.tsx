// dirListSetting.tsx is the parameterized directory-list settings section
// (DirListSetting) plus the reusable path-list editor it's built on
// (PathListEditor, also reused by mcp.tsx's "MCP config files" half - the
// same shape, just a different wireField/pathKind). Instantiated twice,
// byte-identically apart from the 3 params, per parity-m7-settings.md §14's
// own recommendation: pluginsDirs.tsx (wireField:"pluginDirs") and
// skillsDirs.tsx (wireField:"skillsDirs").
//
// PathListEditor is a widgets/collectioneditor CollectionEditor instance:
// CollectionEditor's own renderAddField slot swaps in a FormRow-wrapped
// PathPicker for the add row, keeping the Browse-button-assisted input the
// dir-picker contract (test-settings-dir-picker.js / assets/settings-
// pickers.js) requires on every directory add-row, while CollectionEditor
// itself owns the list rendering and the add field's draft/busy/inline-
// error state. ConfirmDialog-gated removal (this wave's binding "every
// destructive action confirms" constraint) is layered outside
// CollectionEditor's own immediate-fire onRemove, keyed on a `pending` path
// exactly like every other confirm-gated row in this settings cluster.
import { useId, useState } from "react";
import type { LaunchConfigLayer } from "../../../protocol/types.gen";
import { extensionsStore, useExtensionsStore } from "../../../stores/extensions";
import {
  Button,
  type CollectionAddResult,
  CollectionEditor,
  ConfirmDialog,
  EmptyState,
  FormRow,
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
  addRow: requireClass(styles.addRow, "dirListSetting.module.css", "addRow"),
  addField: requireClass(styles.addField, "dirListSetting.module.css", "addField"),
};

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
   * binding "every destructive action confirms" constraint. The returned
   * promise drives the dialog's own `busy` state (disables both buttons
   * until it settles) - callers that report failure via a toast rather
   * than a rejection (this file's own DirListSetting.handleRemove, mcp.tsx's
   * handleRemoveConfig) still resolve normally, so the dialog closes once
   * the attempt finishes either way, exactly matching those callers'
   * existing toast-on-failure convention. */
  onRemove: (path: string) => Promise<void>;
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
 * error below the add row (CollectionEditor's own, already on token-
 * contract.test.ts's --danger allowlist as a widget). Reused by
 * DirListSetting below and by mcp.tsx's "MCP config files" list.
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
  const [pending, setPending] = useState<string | null>(null);
  const [removing, setRemoving] = useState(false);
  const addFieldId = useId();

  async function handleConfirmRemove() {
    if (pending === null) return;
    setRemoving(true);
    try {
      await onRemove(pending);
    } finally {
      setRemoving(false);
      setPending(null);
    }
  }

  return (
    <>
      <CollectionEditor<string>
        label={label}
        items={items}
        getKey={(item) => item}
        renderItem={(item) => item}
        removeLabel={(item) => `Remove ${item}`}
        onRemove={(item) => setPending(item)}
        emptyMessage={emptyMessage}
        onAdd={onAdd}
        renderAddField={({ value, onChange, disabled }) => (
          <FormRow label={addLabel} htmlFor={addFieldId}>
            <div className={CLASS.addRow}>
              <span className={CLASS.addField}>
                <PathPicker
                  id={addFieldId}
                  value={value}
                  onChange={onChange}
                  listChildren={listChildren}
                  placeholder={addPlaceholder}
                  disabled={disabled}
                />
              </span>
              <Button type="submit" disabled={value.trim() === "" || disabled}>
                Add
              </Button>
            </div>
          </FormRow>
        )}
      />
      <ConfirmDialog
        open={pending !== null}
        title={removeConfirmTitle}
        confirmLabel="Remove"
        busy={removing}
        onConfirm={() => void handleConfirmRemove()}
        onCancel={() => setPending(null)}
      >
        {pending !== null ? removeConfirmBody(pending) : ""}
      </ConfirmDialog>
    </>
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

  async function handleRemove(path: string): Promise<void> {
    const current = extensionsStore.getState().launchLayer ?? ({} as LaunchConfigLayer);
    const nextList = (current[wireField] ?? []).filter((p) => p !== path);
    try {
      await extensionsStore.getState().setLaunchLayer({ ...current, [wireField]: nextList });
    } catch (err) {
      toasts.push("error", `Remove failed: ${errorMessage(err)}`);
    }
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
