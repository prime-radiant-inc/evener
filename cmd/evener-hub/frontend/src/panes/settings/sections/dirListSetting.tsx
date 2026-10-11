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
// PathField for the add row, keeping the browse-assisted input the
// dir-picker contract (test-settings-dir-picker.js / assets/settings-
// pickers.js) requires on every directory add-row, while CollectionEditor
// itself owns the list rendering and the add field's draft/busy/inline-
// error state. ConfirmDialog-gated removal (this wave's binding "every
// destructive action confirms" constraint) is layered outside
// CollectionEditor's own immediate-fire onRemove, keyed on a `pending` path
// exactly like every other confirm-gated row in this settings cluster.

import type { LaunchConfigLayer } from "@evener/appwire-client";
import { friendlyErrorMessage } from "@evener/appwire-client";
import { useId, useState } from "react";
import { extensionsStoreForHost, useExtensionsStoreForHost } from "../../../stores/extensions";
import { LOCAL_HOST } from "../../../stores/hostRouting";
import {
  Button,
  type CollectionAddResult,
  CollectionEditor,
  ConfirmDialog,
  EmptyState,
  FormRow,
  PathField,
  type PathFieldKind,
  Skeleton,
  useToasts,
} from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import type { DirectoryActions } from "../../../widgets/pathfield";
import styles from "./dirListSetting.module.css";
import { useHostScopedLoad } from "./useConnectedEffect";

const CLASS = {
  section: requireClass(styles.section, "dirListSetting.module.css", "section"),
  header: requireClass(styles.header, "dirListSetting.module.css", "header"),
  title: requireClass(styles.title, "dirListSetting.module.css", "title"),
  count: requireClass(styles.count, "dirListSetting.module.css", "count"),
  help: requireClass(styles.help, "dirListSetting.module.css", "help"),
  addBlock: requireClass(styles.addBlock, "dirListSetting.module.css", "addBlock"),
  addRow: requireClass(styles.addRow, "dirListSetting.module.css", "addRow"),
  addField: requireClass(styles.addField, "dirListSetting.module.css", "addField"),
};

export interface PathListEditorProps {
  /** Accessible name for the row list. */
  label: string;
  /** Visible FormRow label for the add field specifically (e.g. "New
   * directory" / "New config file") - distinct from `label` (the list's own
   * name) so the two don't render as a visually duplicated heading. */
  addLabel: string;
  /** What the rows of this list name, forwarded to the add row's picker: the
   * two directory lists browse directories, mcp.tsx's config-file list
   * browses files. */
  kind: PathFieldKind;
  directory: DirectoryActions;
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
  /** The add row's path completion (evener/paths/complete), injected so this
   * editor stays wire-free like the picker it renders. */
  complete: (prefix: string, includeFiles: boolean) => Promise<string[]>;
  emptyMessage: string;
  removeConfirmTitle: string;
  removeConfirmBody: (path: string) => string;
  addPlaceholder?: string;
}

/** A shared path field stages a value; Add performs the domain write.
 * Directory browsing and creation stop their submit events, so neither can
 * submit this enclosing editor. Removal still requires confirmation. */
export function PathListEditor({
  label,
  addLabel,
  kind,
  directory,
  items,
  onAdd,
  onRemove,
  complete,
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
          <div className={CLASS.addBlock}>
            <FormRow label={addLabel} htmlFor={addFieldId}>
              <div className={CLASS.addRow}>
                <span className={CLASS.addField}>
                  <PathField
                    ariaLabel={addLabel}
                    directory={directory}
                    id={addFieldId}
                    value={value}
                    onChange={onChange}
                    kind={kind}
                    complete={complete}
                    placeholder={addPlaceholder}
                    disabled={disabled}
                  />
                </span>
                <Button type="submit" disabled={value.trim() === "" || disabled}>
                  Add
                </Button>
              </div>
            </FormRow>
          </div>
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
  /** The host whose own directory list this instance edits (component 07b).
   * Defaults to the local hub, so a direct render is today's local section. */
  host?: string;
}

/**
 * The parameterized directory-list settings section - one component behind
 * both Plugins (dirs) and Skills (dirs), per parity-m7-settings.md §14's own
 * "collapse to one parameterized DirListSetting widget" recommendation.
 * Fetches the global launch layer on mount (gated on the shared client
 * actually being ready - see the mount effect's own comment), validates an
 * add via evener/path/validate before saving, and confirms every row removal.
 */
export function DirListSetting({ wireField, label, copy, host = LOCAL_HOST }: DirListSettingProps) {
  // The extensions instance for the selected host: the controller's own store
  // for the local hub, a per-host instance (over evener/host/request) for a
  // remote one, so its launch layer and path helpers are that host's own.
  const store = extensionsStoreForHost(host);
  const layer = useExtensionsStoreForHost(host, (s) => s.launchLayer);
  const loading = useExtensionsStoreForHost(host, (s) => s.launchLayerLoading);
  const error = useExtensionsStoreForHost(host, (s) => s.launchLayerError);
  const toasts = useToasts();

  // useConnectedEffect (not a bare useEffect): a direct deep link to
  // /settings/plugins-dirs or /settings/skills-dirs can mount this section
  // before AppShell's own connect() handshake finishes, and fetchLaunchLayer
  // requires a connected client - see that hook's own doc comment for the
  // race this guards against.
  useHostScopedLoad(host, () => store.getState().fetchLaunchLayer(), [store]);

  async function handleAdd(path: string): Promise<CollectionAddResult> {
    const validated = await store.getState().validatePath(path, "dir");
    if (!validated.valid) return { ok: false, error: validated.error || "path does not exist" };
    const current = store.getState().launchLayer ?? ({} as LaunchConfigLayer);
    const canonical = validated.path || path;
    const nextList = [...(current[wireField] ?? []), canonical];
    try {
      await store.getState().setLaunchLayer({ ...current, [wireField]: nextList });
      return { ok: true };
    } catch (err) {
      return { ok: false, error: friendlyErrorMessage(err) };
    }
  }

  async function handleRemove(path: string): Promise<void> {
    const current = store.getState().launchLayer ?? ({} as LaunchConfigLayer);
    const nextList = (current[wireField] ?? []).filter((p) => p !== path);
    try {
      await store.getState().setLaunchLayer({ ...current, [wireField]: nextList });
    } catch (err) {
      toasts.push("error", `Remove failed: ${friendlyErrorMessage(err)}`);
    }
  }

  const lowerLabel = label.toLowerCase();
  const items = layer?.[wireField] ?? [];
  // The count only means something once the layer has actually loaded - show
  // it neither during the Skeleton (would flash a misleading "0 entries")
  // nor on a load failure (the count isn't known, not zero).
  const showCount = error === null && !(loading && layer === null);

  return (
    <section className={CLASS.section}>
      <header className={CLASS.header}>
        <h2 className={CLASS.title}>{label}</h2>
        {showCount && (
          <span className={CLASS.count}>
            {items.length} {items.length === 1 ? "entry" : "entries"}
          </span>
        )}
      </header>
      <p className={CLASS.help}>{copy}</p>
      {error !== null ? (
        <EmptyState title="Failed to load" hint={error} />
      ) : loading && layer === null ? (
        <Skeleton />
      ) : (
        <PathListEditor
          directory={{
            validatePath: (path, kind) => store.getState().validatePath(path, kind),
            createDirectory: (path) => store.getState().createDirectory(path),
          }}
          label={label}
          addLabel="New directory"
          kind="dir"
          items={items}
          onAdd={handleAdd}
          onRemove={handleRemove}
          complete={(prefix, includeFiles) => store.getState().completePaths(prefix, includeFiles)}
          emptyMessage={`No ${lowerLabel}. Add one below.`}
          removeConfirmTitle="Remove directory"
          removeConfirmBody={(path) => `Remove "${path}" from ${lowerLabel}?`}
        />
      )}
    </section>
  );
}
