// MarketplaceDetailBody: the inline body a marketplace row expands under
// itself - the catalog you install from, and the management actions that
// used to live in MarketplaceSheet. The catalog comes first (opening a
// marketplace is usually about its plugins); Refresh, Edit and Remove sit
// at the bottom, and Edit reveals the name/source form in place. The
// draft compares against its pinned baseline, not the live entry:
// untouched forms follow canonical changes, edited forms retain their
// input and require an explicit choice when the canonical source changes.

import type { AppwireClientLike, MarketplaceEntry } from "@evener/appwire-client";
import { errorText, marketplaceSourcePin } from "@evener/appwire-client";
import { HUB_WRITE_BUSY, isHubWriteBusy, marketplaceRemovalOutcome } from "@evener/appwire-client/state/extensions";
import { useEffect, useId, useState } from "react";
import { connectionStore } from "../../../../stores/connection";
import type { MarketplaceCatalogEntry } from "../../../../stores/extensions";
import {
  Button,
  ConfirmDialog,
  EmptyState,
  FormRow,
  InlineNotice,
  Input,
  Loader,
  PathField,
  PropertyList,
  RadioGroup,
  useToasts,
} from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { CatalogPluginRow } from "../../../plugins/PluginRow";
import { pluginDetailBodyId, samePluginTarget } from "../../../plugins/pluginRowFocus";
import { useExtensionsHostState, useExtensionsHostStore, useHostDirectoryActions } from "./hostStore";
import {
  MARKETPLACE_SOURCE_OPTIONS,
  type MarketplaceDraft,
  type MarketplaceSourceKind,
  marketplaceDraftFor,
  marketplaceDraftIncomplete,
  marketplaceEditParams,
  marketplaceSourceTouched,
} from "./marketplaceEdit";
import styles from "./marketplacesPlugins.module.css";
import { PluginDetailBody, type PluginDetailTarget } from "./PluginDetailBody";

const CLASS = {
  body: requireClass(styles.marketplaceBody, "marketplacesPlugins.module.css", "marketplaceBody"),
  children: requireClass(styles.treeChildren, "marketplacesPlugins.module.css", "treeChildren"),
  metaList: requireClass(styles.metaList, "marketplacesPlugins.module.css", "metaList"),
  form: requireClass(styles.sheetForm, "marketplacesPlugins.module.css", "sheetForm"),
  actions: requireClass(styles.sheetActions, "marketplacesPlugins.module.css", "sheetActions"),
  divider: requireClass(styles.sheetDivider, "marketplacesPlugins.module.css", "sheetDivider"),
  rowMeta: requireClass(styles.rowMeta, "marketplacesPlugins.module.css", "rowMeta"),
};

function lastUpdatedText(seconds: number): string {
  return seconds > 0 ? new Date(seconds * 1000).toLocaleString() : "never";
}

export interface MarketplaceDetailBodyProps {
  /** The body's DOM id, so the row's opener can point here with aria-controls. */
  id: string;
  marketplace: MarketplaceEntry;
  /** The marketplace's catalog cache; absent/loading/error render their own faces. */
  catalog: MarketplaceCatalogEntry | undefined;
  isInstalled: (plugin: string, marketplace: string) => boolean;
  installDisabled: boolean;
  /** The one plugin row whose details are expanded, anywhere in the tree. */
  expandedRow: PluginDetailTarget | null;
  onToggleRow: (target: PluginDetailTarget) => void;
  onInstall: (plugin: string, marketplace: string) => void;
  /** A rename moves the page's expansion from the old name to the new one. */
  onRenamed: (oldName: string, newName: string) => void;
  onCollapse: () => void;
  appliedRemovalNames: ReadonlySet<string>;
  connectionClient: AppwireClientLike | null;
  onAppliedRemoval: (name: string, owner: AppwireClientLike | null, publicationVersion: number) => void;
}

export function MarketplaceDetailBody({
  id,
  marketplace,
  catalog,
  isInstalled,
  installDisabled,
  expandedRow,
  onToggleRow,
  onInstall,
  onRenamed,
  onCollapse,
  appliedRemovalNames,
  connectionClient,
  onAppliedRemoval,
}: MarketplaceDetailBodyProps) {
  const store = useExtensionsHostStore();
  const directory = useHostDirectoryActions();
  const readError = useExtensionsHostState((s) => s.marketplacesError);
  const reading = useExtensionsHostState((s) => s.marketplacesLoading);
  const hubWriteBusy = useExtensionsHostState((s) => s.hubWriteBusy);
  const toasts = useToasts();
  const ids = useId();

  const sourcePin = marketplaceSourcePin(marketplace.source);
  const [draft, setDraft] = useState<MarketplaceDraft>(() => marketplaceDraftFor(marketplace));
  const [baseline, setBaseline] = useState<MarketplaceEntry>(marketplace);
  const [editing, setEditing] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);
  const [pendingRemove, setPendingRemove] = useState(false);

  function seed(current: MarketplaceEntry): void {
    setBaseline(current);
    setDraft(marketplaceDraftFor(current));
    setFormError(null);
  }

  // The entry is passed in, not looked up: the page mounts this body for the
  // marketplace it has expanded, and a rename or retire arrives as a new prop.
  // biome-ignore lint/correctness/useExhaustiveDependencies: identity changes reset the editor, same-name refreshes reconcile below
  useEffect(() => {
    setPendingRemove(false);
    seed(marketplace);
  }, [marketplace.name]);

  const sourceTouched = marketplaceSourceTouched(baseline, draft);
  const dirty = draft.name.trim() !== baseline.name.trim() || sourceTouched;
  const canonicalChanged = (["kind", "url", "repo", "path", "ref", "sha"] as const).some(
    (key) => baseline.source[key] !== marketplace.source[key],
  );
  const conflict = dirty && canonicalChanged;

  useEffect(() => {
    if (!dirty && baseline !== marketplace) {
      setBaseline(marketplace);
      setDraft(marketplaceDraftFor(marketplace));
    }
  }, [marketplace, baseline, dirty]);

  // An expanded marketplace whose catalog was retired out from under it - a
  // marketplace change from another client retires every cached one - asks
  // for its own replacement instead of spinning forever. browseMarketplace
  // writes its "loading" entry synchronously, so this cannot re-enter.
  useEffect(() => {
    if (catalog !== undefined) return;
    void store.getState().browseMarketplace(marketplace.name);
  }, [catalog, marketplace.name, store]);

  const params = marketplaceEditParams(baseline, draft);
  const incomplete = marketplaceDraftIncomplete(draft);
  const busy = hubWriteBusy || readError !== null;
  const removeDisabled = busy || appliedRemovalNames.has(marketplace.name);
  const canSave = params !== null && !conflict && !busy && !reading && !(sourceTouched && incomplete);

  function update(patch: Partial<MarketplaceDraft>): void {
    setDraft((current) => ({ ...current, ...patch }));
  }

  async function handleSave(): Promise<void> {
    if (params === null || !canSave) return;
    setFormError(null);
    try {
      await store.getState().editMarketplace(params);
      toasts.push("success", `Saved ${params.newName ?? marketplace.name}`);
      // A save invalidates the catalog exactly as a Refresh does; the new
      // body (or this one, on a plain save) asks for its catalog itself.
      if (params.newName !== undefined) {
        onRenamed(marketplace.name, params.newName);
        return;
      }
      const refreshed = store.getState().marketplaces?.find((m) => m.name === marketplace.name);
      if (refreshed !== undefined) seed(refreshed);
      setEditing(false);
    } catch (err) {
      const wasBusyRefused = isHubWriteBusy(err);
      const message = wasBusyRefused ? HUB_WRITE_BUSY : errorText(err);
      setFormError(message);
      toasts.push("error", wasBusyRefused ? HUB_WRITE_BUSY : `Save failed: ${message}`);
    }
  }

  async function handleRefresh(): Promise<void> {
    try {
      await store.getState().refreshMarketplace(marketplace.name);
      void store.getState().browseMarketplace(marketplace.name);
      toasts.push("success", `Refreshed ${marketplace.name}`);
    } catch (err) {
      toasts.push("error", isHubWriteBusy(err) ? HUB_WRITE_BUSY : `Refresh failed: ${errorText(err)}`);
    }
  }

  async function handleConfirmRemove(): Promise<void> {
    const removalName = marketplace.name;
    const removalClient = connectionClient;
    try {
      await store.getState().removeMarketplace(removalName);
      toasts.push("success", `Removed marketplace ${removalName}`);
      setPendingRemove(false);
      // The page prunes the vanished name from its expansion set once the
      // store's updated list lands - no explicit collapse here.
    } catch (err) {
      const outcome = marketplaceRemovalOutcome(err);
      if (outcome !== undefined) {
        if (connectionStore.getState().client !== removalClient) return;
        const current = store.getState();
        const targetPresent =
          current.marketplaces?.some((marketplaceEntry) => marketplaceEntry.name === removalName) ?? false;
        if (current.marketplaces === null || targetPresent) {
          onAppliedRemoval(removalName, removalClient, current.marketplacesPublicationVersion);
          void store.getState().fetchMarketplaces();
        }
        setPendingRemove(false);
        if (outcome.kind === "removed") {
          toasts.push(
            "info",
            `Removed marketplace ${removalName}; the updated list was unavailable, so it is being refreshed.`,
          );
        } else {
          toasts.push(
            "warning",
            "Marketplace removed; clone cleanup failed. Remove the leftover clone files manually.",
          );
        }
      } else {
        toasts.push("error", isHubWriteBusy(err) ? HUB_WRITE_BUSY : `Remove marketplace failed: ${errorText(err)}`);
      }
    }
  }

  const rows = catalog?.status === "loaded" ? catalog.plugins : [];

  return (
    <div id={id} className={CLASS.body} data-kind="marketplace-detail">
      <ul aria-label={`${marketplace.name} plugins`} className={CLASS.children}>
        {!catalog || catalog.status === "loading" ? (
          <li>
            <Loader label="Loading" />
          </li>
        ) : catalog.status === "error" ? (
          <li>
            <InlineNotice tone="danger">{`Failed to browse: ${catalog.error}`}</InlineNotice>
          </li>
        ) : rows.length === 0 ? (
          <li>
            <EmptyState size="compact" reason="empty" title="This marketplace has no plugins." />
          </li>
        ) : (
          rows.map((p) => {
            const pair = { plugin: p.name, marketplace: marketplace.name };
            const isRowExpanded = expandedRow !== null && samePluginTarget(expandedRow, pair);
            return (
              <li key={p.name} data-plugin={p.name} data-marketplace={marketplace.name}>
                <CatalogPluginRow
                  plugin={p}
                  detailBodyId={pluginDetailBodyId(p.name, marketplace.name)}
                  expanded={isRowExpanded}
                  installed={isInstalled(p.name, marketplace.name)}
                  installDisabled={installDisabled}
                  onOpen={() => onToggleRow(pair)}
                  onInstall={() => onInstall(p.name, marketplace.name)}
                />
                {isRowExpanded && expandedRow !== null && (
                  <PluginDetailBody
                    target={expandedRow}
                    onCollapse={() => onToggleRow(pair)}
                    onInstall={(target) => onInstall(target.plugin, target.marketplace)}
                  />
                )}
              </li>
            );
          })
        )}
      </ul>
      <div className={CLASS.metaList}>
        <PropertyList
          rows={[
            {
              label: "Install location",
              value: (
                <span
                  data-testid="marketplace-install-location"
                  data-state={marketplace.installLocation ? undefined : "unfetched"}
                >
                  {marketplace.installLocation || "not fetched yet"}
                </span>
              ),
              mono: true,
            },
            ...(sourcePin
              ? [
                  {
                    label: marketplace.source.sha ? "Pinned to" : "Ref",
                    value: <span data-testid="marketplace-pinned-to">{sourcePin}</span>,
                    mono: true,
                  },
                ]
              : []),
            {
              label: "Last updated",
              value: (
                <span
                  data-testid="marketplace-last-updated"
                  data-state={marketplace.lastUpdated > 0 ? undefined : "never"}
                >
                  {lastUpdatedText(marketplace.lastUpdated)}
                </span>
              ),
              mono: true,
            },
          ]}
        />
      </div>
      {editing && (
        <form
          className={CLASS.form}
          aria-label={`Edit ${marketplace.name}`}
          onSubmit={(event) => event.preventDefault()}
        >
          <FormRow label="Name" htmlFor={`${ids}-name`}>
            <Input
              id={`${ids}-name`}
              value={draft.name}
              onChange={(event) => update({ name: event.target.value })}
              disabled={hubWriteBusy}
            />
          </FormRow>
          <RadioGroup
            label="Source"
            value={draft.kind}
            onChange={(value) => update({ kind: value as MarketplaceSourceKind })}
            options={MARKETPLACE_SOURCE_OPTIONS}
            disabled={hubWriteBusy}
          />
          {draft.kind === "url" && (
            <FormRow label="Git URL" htmlFor={`${ids}-url`}>
              <Input
                id={`${ids}-url`}
                value={draft.url}
                onChange={(event) => update({ url: event.target.value })}
                placeholder="https://github.com/owner/repo.git"
                disabled={hubWriteBusy}
              />
            </FormRow>
          )}
          {draft.kind === "github" && (
            <FormRow label="owner/repo" htmlFor={`${ids}-repo`}>
              <Input
                id={`${ids}-repo`}
                value={draft.repo}
                onChange={(event) => update({ repo: event.target.value })}
                placeholder="owner/repo"
                disabled={hubWriteBusy}
              />
            </FormRow>
          )}
          {draft.kind === "directory" && (
            <FormRow label="Local path" htmlFor={`${ids}-path`}>
              <PathField
                ariaLabel="Local path"
                directory={directory}
                id={`${ids}-path`}
                value={draft.path}
                onChange={(value) => update({ path: value })}
                kind="dir"
                complete={(prefix, includeFiles) => store.getState().completePaths(prefix, includeFiles)}
                placeholder="/absolute/path"
                disabled={hubWriteBusy}
              />
            </FormRow>
          )}
          {sourceTouched && (
            <InlineNotice data-testid="marketplace-refetch-note">
              Saving re-fetches the marketplace. Installed plugins are unaffected.
            </InlineNotice>
          )}
          {formError !== null && <InlineNotice tone="danger">{formError}</InlineNotice>}
          {readError !== null && (
            <InlineNotice
              tone="danger"
              action={
                <Button
                  type="button"
                  size="sm"
                  disabled={reading}
                  onClick={() => void store.getState().fetchMarketplaces()}
                >
                  Retry marketplaces
                </Button>
              }
            >
              Could not refresh marketplaces. Your edits are retained. Refresh the list before saving.
            </InlineNotice>
          )}
          {conflict && (
            <InlineNotice
              action={
                <div className={CLASS.actions}>
                  <Button type="button" variant="quiet" disabled={busy} onClick={() => seed(marketplace)}>
                    Reload latest
                  </Button>
                  <Button type="button" variant="quiet" disabled={busy} onClick={() => setBaseline(marketplace)}>
                    Keep my edits
                  </Button>
                </div>
              }
            >
              This source changed elsewhere. Reload its latest values or keep your edits, then Save to apply them.
            </InlineNotice>
          )}
          <div className={CLASS.actions}>
            <Button type="button" onClick={() => void handleSave()} disabled={!canSave}>
              Save
            </Button>
            <Button
              type="button"
              variant="quiet"
              disabled={busy}
              onClick={() => {
                seed(marketplace);
                setEditing(false);
              }}
            >
              Cancel
            </Button>
          </div>
        </form>
      )}
      <div className={CLASS.actions}>
        <Button variant="quiet" onClick={() => void handleRefresh()} disabled={busy}>
          Refresh
        </Button>
        <Button variant="quiet" onClick={() => setEditing((current) => !current)} disabled={busy}>
          Edit
        </Button>
      </div>
      <hr className={CLASS.divider} />
      <div className={CLASS.actions}>
        <Button variant="danger" onClick={() => setPendingRemove(true)} disabled={removeDisabled}>
          Remove
        </Button>
        <Button variant="link" size="xs" type="button" onClick={onCollapse}>
          Hide details
        </Button>
      </div>
      <ConfirmDialog
        open={pendingRemove}
        title="Remove marketplace"
        confirmLabel="Remove"
        busy={hubWriteBusy}
        onConfirm={() => void handleConfirmRemove()}
        onCancel={() => setPendingRemove(false)}
      >
        {`Remove marketplace "${marketplace.name}"? Installed plugins from it are unaffected.`}
      </ConfirmDialog>
    </div>
  );
}
