// MarketplacesSection: the registered-marketplaces list + add-marketplace
// form (parity-m7-settings.md §12b/§12c). `expandedMarketplaces` is lifted
// to the parent (marketplacesPlugins/index.tsx) rather than owned here,
// because Refresh's own "if the node is currently expanded, immediately
// reload it" behavior (§12b) needs to read BrowseSection's expansion state,
// and these are sibling components - see index.tsx's own comment.
import { type FormEvent, useId, useState } from "react";
import type { MarketplaceSourceInput } from "../../../../protocol/types.gen";
import { extensionsStore, useExtensionsStore } from "../../../../stores/extensions";
import { Button, ConfirmDialog, FormRow, Input, PathPicker, RadioGroup, useToasts } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import styles from "./marketplacesPlugins.module.css";
import { sourceLabel } from "./sourceLabel";

const CLASS = {
  section: requireClass(styles.section, "marketplacesPlugins.module.css", "section"),
  header: requireClass(styles.header, "marketplacesPlugins.module.css", "header"),
  title: requireClass(styles.title, "marketplacesPlugins.module.css", "title"),
  count: requireClass(styles.count, "marketplacesPlugins.module.css", "count"),
  list: requireClass(styles.list, "marketplacesPlugins.module.css", "list"),
  row: requireClass(styles.row, "marketplacesPlugins.module.css", "row"),
  rowMain: requireClass(styles.rowMain, "marketplacesPlugins.module.css", "rowMain"),
  rowText: requireClass(styles.rowText, "marketplacesPlugins.module.css", "rowText"),
  rowKind: requireClass(styles.rowKind, "marketplacesPlugins.module.css", "rowKind"),
  rowMeta: requireClass(styles.rowMeta, "marketplacesPlugins.module.css", "rowMeta"),
  rowActions: requireClass(styles.rowActions, "marketplacesPlugins.module.css", "rowActions"),
  empty: requireClass(styles.empty, "marketplacesPlugins.module.css", "empty"),
  addForm: requireClass(styles.addForm, "marketplacesPlugins.module.css", "addForm"),
  formActions: requireClass(styles.formActions, "marketplacesPlugins.module.css", "formActions"),
};

type SourceKind = "url" | "github" | "directory";

const SOURCE_OPTIONS = [
  { value: "url", label: "Git URL" },
  { value: "github", label: "owner/repo" },
  { value: "directory", label: "Local path" },
];

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

export interface MarketplacesSectionProps {
  /** Read-only here - only used to decide whether a Refresh should also
   * immediately re-browse (owned by BrowseSection's sibling, lifted to
   * marketplacesPlugins/index.tsx). */
  expandedMarketplaces: Set<string>;
}

export function MarketplacesSection({ expandedMarketplaces }: MarketplacesSectionProps) {
  const marketplaces = useExtensionsStore((s) => s.marketplaces) ?? [];
  const toasts = useToasts();

  const [addOpen, setAddOpen] = useState(false);
  const [kind, setKind] = useState<SourceKind>("url");
  const [urlValue, setUrlValue] = useState("");
  const [repoValue, setRepoValue] = useState("");
  const [pathValue, setPathValue] = useState("");
  const [nameValue, setNameValue] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [pendingRemove, setPendingRemove] = useState<string | null>(null);
  const [removeBusy, setRemoveBusy] = useState(false);
  // Keyed by marketplace name so a Refresh in flight on one row never
  // disables another row's own button (§12f: "withBusy... disables the
  // triggering button", singular - not every row's Refresh at once).
  const [refreshBusy, setRefreshBusy] = useState<Set<string>>(new Set());

  const urlId = useId();
  const repoId = useId();
  const pathId = useId();
  const nameId = useId();

  function resetAddForm() {
    setKind("url");
    setUrlValue("");
    setRepoValue("");
    setPathValue("");
    setNameValue("");
  }

  function buildSource(): MarketplaceSourceInput {
    if (kind === "github") return { kind: "github", repo: repoValue.trim() };
    if (kind === "directory") return { kind: "directory", path: pathValue.trim() };
    return { kind: "url", url: urlValue.trim() };
  }

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (submitting) return;
    setSubmitting(true);
    const trimmedName = nameValue.trim();
    try {
      await extensionsStore.getState().addMarketplace({ name: trimmedName, source: buildSource() });
      setAddOpen(false);
      resetAddForm();
      toasts.push("success", `Added marketplace${trimmedName ? ` ${trimmedName}` : ""}`);
    } catch (err) {
      toasts.push("error", `Add marketplace failed: ${errorMessage(err)}`);
    } finally {
      setSubmitting(false);
    }
  }

  function handleCancelAdd() {
    setAddOpen(false);
    resetAddForm();
  }

  async function handleRefresh(name: string) {
    setRefreshBusy((prev) => new Set(prev).add(name));
    try {
      await extensionsStore.getState().refreshMarketplace(name);
      // refreshMarketplace already invalidated the browse cache entry for
      // `name` (stores/extensions.ts); if BrowseSection currently has it
      // expanded, immediately re-browse rather than leaving it showing a
      // stale catalog until the user collapses/re-expands it themselves.
      if (expandedMarketplaces.has(name)) void extensionsStore.getState().browseMarketplace(name);
      toasts.push("success", `Refreshed ${name}`);
    } catch (err) {
      toasts.push("error", `Refresh failed: ${errorMessage(err)}`);
    } finally {
      setRefreshBusy((prev) => {
        const next = new Set(prev);
        next.delete(name);
        return next;
      });
    }
  }

  async function handleConfirmRemove() {
    const name = pendingRemove;
    if (name === null) return;
    setRemoveBusy(true);
    try {
      await extensionsStore.getState().removeMarketplace(name);
      toasts.push("success", `Removed marketplace ${name}`);
      setPendingRemove(null);
    } catch (err) {
      toasts.push("error", `Remove marketplace failed: ${errorMessage(err)}`);
    } finally {
      setRemoveBusy(false);
    }
  }

  return (
    <section className={CLASS.section}>
      <header className={CLASS.header}>
        <h3 className={CLASS.title}>Marketplaces</h3>
        <span className={CLASS.count}>
          {marketplaces.length} {marketplaces.length === 1 ? "entry" : "entries"}
        </span>
      </header>
      <ul aria-label="Marketplaces" className={CLASS.list}>
        {marketplaces.length === 0 ? (
          <li className={CLASS.empty}>No marketplaces registered. Add one below.</li>
        ) : (
          marketplaces.map((m) => (
            <li key={m.name} className={CLASS.row}>
              <div className={CLASS.rowMain}>
                <div className={CLASS.rowText}>
                  {m.name} <span className={CLASS.rowKind}>{m.source.kind}</span>
                </div>
                <div className={CLASS.rowMeta}>{sourceLabel(m.source)}</div>
              </div>
              <div className={CLASS.rowActions}>
                <Button
                  variant="quiet"
                  size="sm"
                  onClick={() => void handleRefresh(m.name)}
                  disabled={refreshBusy.has(m.name)}
                >
                  Refresh
                </Button>
                <Button variant="danger" size="sm" onClick={() => setPendingRemove(m.name)}>
                  Remove
                </Button>
              </div>
            </li>
          ))
        )}
      </ul>
      {addOpen ? (
        <form className={CLASS.addForm} onSubmit={(event) => void handleSubmit(event)}>
          <RadioGroup
            label="Source"
            value={kind}
            onChange={(value) => setKind(value as SourceKind)}
            options={SOURCE_OPTIONS}
          />
          {kind === "url" && (
            <FormRow label="Git URL" htmlFor={urlId}>
              <Input
                id={urlId}
                value={urlValue}
                onChange={(event) => setUrlValue(event.target.value)}
                placeholder="https://github.com/owner/repo.git"
              />
            </FormRow>
          )}
          {kind === "github" && (
            <FormRow label="owner/repo" htmlFor={repoId}>
              <Input
                id={repoId}
                value={repoValue}
                onChange={(event) => setRepoValue(event.target.value)}
                placeholder="owner/repo"
              />
            </FormRow>
          )}
          {kind === "directory" && (
            <FormRow label="Local path" htmlFor={pathId}>
              <PathPicker
                id={pathId}
                value={pathValue}
                onChange={setPathValue}
                listChildren={(path) => extensionsStore.getState().listDirChildren(path)}
                placeholder="/absolute/path"
              />
            </FormRow>
          )}
          <FormRow label="Name (optional)" htmlFor={nameId} help="Defaults to the marketplace's own name.">
            <Input
              id={nameId}
              value={nameValue}
              onChange={(event) => setNameValue(event.target.value)}
              placeholder="defaults to the marketplace's own name"
            />
          </FormRow>
          <div className={CLASS.formActions}>
            <Button type="submit" disabled={submitting}>
              Add
            </Button>
            <Button type="button" variant="quiet" onClick={handleCancelAdd} disabled={submitting}>
              Cancel
            </Button>
          </div>
        </form>
      ) : (
        <Button variant="quiet" onClick={() => setAddOpen(true)}>
          + Add marketplace
        </Button>
      )}
      <ConfirmDialog
        open={pendingRemove !== null}
        title="Remove marketplace"
        confirmLabel="Remove"
        busy={removeBusy}
        onConfirm={() => void handleConfirmRemove()}
        onCancel={() => setPendingRemove(null)}
      >
        {pendingRemove !== null
          ? `Remove marketplace "${pendingRemove}"? Installed plugins from it are unaffected.`
          : ""}
      </ConfirmDialog>
    </section>
  );
}
