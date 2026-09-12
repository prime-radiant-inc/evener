// MarketplacesSection: the registered-marketplaces list + add-marketplace
// form (parity-m7-settings.md §12b/§12c). Each row is one tappable button
// that selects its marketplace; every per-marketplace action - editing the
// name and source, Refresh, Remove - lives in MarketplaceSheet, which is
// why `expandedMarketplaces` now flows to the sheet (Refresh's own "if the
// node is currently expanded, immediately reload it" behavior, §12b) rather
// than here.
import { type FormEvent, useId, useState } from "react";
import { errorText } from "../../../../protocol/errors";
import type { MarketplaceSourceInput } from "../../../../protocol/types.gen";
import { directoryActions, extensionsStore, useExtensionsStore } from "../../../../stores/extensions";
import { Button, Chevron, FormRow, Input, PathField, RadioGroup, useToasts } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { MARKETPLACE_SOURCE_OPTIONS, type MarketplaceSourceKind } from "./marketplaceEdit";
import styles from "./marketplacesPlugins.module.css";
import { sourceLabel } from "./sourceLabel";

const CLASS = {
  section: requireClass(styles.section, "marketplacesPlugins.module.css", "section"),
  list: requireClass(styles.list, "marketplacesPlugins.module.css", "list"),
  rowButton: requireClass(styles.rowButton, "marketplacesPlugins.module.css", "rowButton"),
  rowMain: requireClass(styles.rowMain, "marketplacesPlugins.module.css", "rowMain"),
  rowText: requireClass(styles.rowText, "marketplacesPlugins.module.css", "rowText"),
  rowKind: requireClass(styles.rowKind, "marketplacesPlugins.module.css", "rowKind"),
  rowMeta: requireClass(styles.rowMeta, "marketplacesPlugins.module.css", "rowMeta"),
  rowChevron: requireClass(styles.rowChevron, "marketplacesPlugins.module.css", "rowChevron"),
  empty: requireClass(styles.empty, "marketplacesPlugins.module.css", "empty"),
  addForm: requireClass(styles.addForm, "marketplacesPlugins.module.css", "addForm"),
  formActions: requireClass(styles.formActions, "marketplacesPlugins.module.css", "formActions"),
};

export interface MarketplacesSectionProps {
  onSelect: (name: string) => void;
}

export function MarketplacesSection({ onSelect }: MarketplacesSectionProps) {
  const marketplaces = useExtensionsStore((s) => s.marketplaces) ?? [];
  const toasts = useToasts();

  const [addOpen, setAddOpen] = useState(false);
  const [kind, setKind] = useState<MarketplaceSourceKind>("url");
  const [urlValue, setUrlValue] = useState("");
  const [repoValue, setRepoValue] = useState("");
  const [pathValue, setPathValue] = useState("");
  const [nameValue, setNameValue] = useState("");
  const [submitting, setSubmitting] = useState(false);

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
      toasts.push("error", `Add marketplace failed: ${errorText(err)}`);
    } finally {
      setSubmitting(false);
    }
  }

  function handleCancelAdd() {
    setAddOpen(false);
    resetAddForm();
  }

  return (
    <section className={CLASS.section}>
      <ul aria-label="Marketplaces" className={CLASS.list}>
        {marketplaces.length === 0 ? (
          <li className={CLASS.empty}>No marketplaces registered. Add one below.</li>
        ) : (
          marketplaces.map((m) => (
            <li key={m.name}>
              <button type="button" className={CLASS.rowButton} onClick={() => onSelect(m.name)}>
                <div className={CLASS.rowMain}>
                  <div className={CLASS.rowText}>
                    {m.name} <span className={CLASS.rowKind}>{m.source.kind}</span>
                  </div>
                  <div className={CLASS.rowMeta}>{sourceLabel(m.source)}</div>
                </div>
                <span className={CLASS.rowChevron} aria-hidden="true">
                  <Chevron direction="right" />
                </span>
              </button>
            </li>
          ))
        )}
      </ul>
      {addOpen ? (
        <form className={CLASS.addForm} onSubmit={(event) => void handleSubmit(event)}>
          <RadioGroup
            label="Source"
            value={kind}
            onChange={(value) => setKind(value as MarketplaceSourceKind)}
            options={MARKETPLACE_SOURCE_OPTIONS}
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
              <PathField
                ariaLabel="Local path"
                directory={directoryActions}
                id={pathId}
                value={pathValue}
                onChange={setPathValue}
                kind="dir"
                complete={(prefix, includeFiles) => extensionsStore.getState().completePaths(prefix, includeFiles)}
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
    </section>
  );
}
