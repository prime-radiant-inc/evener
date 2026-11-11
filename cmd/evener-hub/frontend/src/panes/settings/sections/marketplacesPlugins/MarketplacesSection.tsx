// MarketplacesSection: the unified marketplace surface - the one place both
// to manage registered marketplaces and to install their plugins. Each row
// expands inline to its catalog (the shared plugin rows, installed state and
// Install action contextual) and to its management actions (Refresh, Edit,
// Remove), replacing the old separate Browse tree and Sources sheet. The
// filter auto-expands the marketplaces whose catalogs match.

import type { AppwireClientLike } from "@evener/appwire-client";
import { errorText, marketplaceSourceLabel, marketplaceSourceTakesRef } from "@evener/appwire-client";
import { HUB_WRITE_BUSY, isHubWriteBusy } from "@evener/appwire-client/state/extensions";
import { type Dispatch, type FormEvent, type SetStateAction, useEffect, useId, useRef, useState } from "react";
import {
  Button,
  Chip,
  ConfirmDialog,
  EmptyState,
  FormRow,
  Input,
  ListRow,
  Loader,
  PathField,
  RadioGroup,
  useToasts,
} from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { handleRowEscape, marketplaceDetailBodyId, usePluginRowFocusRehome } from "../../../plugins/pluginRowFocus";
import { useExtensionsHostState, useExtensionsHostStore, useHostDirectoryActions } from "./hostStore";
import { MarketplaceDetailBody } from "./MarketplaceDetailBody";
import { MARKETPLACE_SOURCE_OPTIONS, type MarketplaceSourceKind } from "./marketplaceEdit";
import styles from "./marketplacesPlugins.module.css";
import type { PluginDetailTarget } from "./PluginDetailBody";

const CLASS = {
  section: requireClass(styles.section, "marketplacesPlugins.module.css", "section"),
  filterRow: requireClass(styles.filterRow, "marketplacesPlugins.module.css", "filterRow"),
  list: requireClass(styles.list, "marketplacesPlugins.module.css", "list"),
  addForm: requireClass(styles.addForm, "marketplacesPlugins.module.css", "addForm"),
  formActions: requireClass(styles.formActions, "marketplacesPlugins.module.css", "formActions"),
};

const FILTER_DEBOUNCE_MS = 150;

function pluginMatchesFilter(plugin: { name: string; description?: string }, query: string): boolean {
  const q = query.toLowerCase();
  return plugin.name.toLowerCase().includes(q) || (plugin.description ?? "").toLowerCase().includes(q);
}

export interface MarketplacesSectionProps {
  /** Every marketplace whose body is expanded; the filter opens several. */
  expandedMarketplaces: Set<string>;
  setExpandedMarketplaces: Dispatch<SetStateAction<Set<string>>>;
  /** The one plugin row whose details are expanded, anywhere in the page. */
  expanded: PluginDetailTarget | null;
  onToggleRow: (target: PluginDetailTarget) => void;
  onCollapseMarketplace: (name: string) => void;
  onRenamedMarketplace: (oldName: string, newName: string) => void;
  appliedRemovalNames: ReadonlySet<string>;
  connectionClient: AppwireClientLike | null;
  onAppliedRemoval: (name: string, owner: AppwireClientLike | null, publicationVersion: number) => void;
}

export function MarketplacesSection({
  expandedMarketplaces,
  setExpandedMarketplaces,
  expanded,
  onToggleRow,
  onCollapseMarketplace,
  onRenamedMarketplace,
  appliedRemovalNames,
  connectionClient,
  onAppliedRemoval,
}: MarketplacesSectionProps) {
  const store = useExtensionsHostStore();
  const directory = useHostDirectoryActions();
  const marketplaces = useExtensionsHostState((s) => s.marketplaces) ?? [];
  const plugins = useExtensionsHostState((s) => s.plugins) ?? [];
  const pluginsUnavailable = useExtensionsHostState(
    (s) => s.plugins === null || s.pluginsError !== null || s.pluginsLoading,
  );
  const browseCatalogs = useExtensionsHostState((s) => s.browseCatalogs);
  const hubWriteBusy = useExtensionsHostState((s) => s.hubWriteBusy);
  const readUnavailable = useExtensionsHostState((s) => s.marketplacesError !== null || s.marketplacesLoading);
  const toasts = useToasts();

  const [filterQuery, setFilterQuery] = useState("");
  const [filterLoading, setFilterLoading] = useState(false);
  const [pendingInstall, setPendingInstall] = useState<{ plugin: string; marketplace: string } | null>(null);
  const sectionRef = useRef<HTMLElement>(null);
  // An inline install's fresh list reshapes the confirmed row, and the
  // confirm's own focus restore then loses its opener: the hook returns
  // focus to the row when that happened.
  usePluginRowFocusRehome(pendingInstall, sectionRef);

  const [addOpen, setAddOpen] = useState(false);
  const [kind, setKind] = useState<MarketplaceSourceKind>("url");
  const [urlValue, setUrlValue] = useState("");
  const [repoValue, setRepoValue] = useState("");
  const [pathValue, setPathValue] = useState("");
  const [refValue, setRefValue] = useState("");
  const [nameValue, setNameValue] = useState("");

  const urlId = useId();
  const repoId = useId();
  const pathId = useId();
  const refId = useId();
  const nameId = useId();

  const trimmedQuery = filterQuery.trim();

  // Debounced filter-driven auto-expand: clearing the query collapses
  // everything instantly (no debounce); a non-empty query, 150ms after the
  // last keystroke, first waits for every marketplace whose catalog is not
  // settled yet (loading the uncached ones itself - the loader shows
  // tree-wide meanwhile), then auto-expands every marketplace with a match -
  // by plugin name, description, or the marketplace's own name - and
  // collapses the rest. The empty-query collapse is skipped on the initial
  // mount so lifted expansion state survives the segment round trip.
  const filterTouched = useRef(false);
  const filterGeneration = useRef(0);
  useEffect(() => {
    if (trimmedQuery === "") {
      if (filterTouched.current) setExpandedMarketplaces(new Set());
      return;
    }
    filterTouched.current = true;
    let cancelled = false;
    const timer = setTimeout(() => {
      void (async () => {
        const generation = ++filterGeneration.current;
        try {
          const current = store.getState().marketplaces ?? [];
          const unsettled = current.filter((m) => {
            const cache = store.getState().browseCatalogs.get(m.name);
            return cache === undefined || cache.status === "loading";
          });
          if (unsettled.length > 0) {
            setFilterLoading(true);
            await Promise.all(unsettled.map((m) => store.getState().browseMarketplace(m.name)));
            if (cancelled) return;
          }
          const next = new Set<string>();
          for (const m of current) {
            const cache = store.getState().browseCatalogs.get(m.name);
            const matches =
              m.name.toLowerCase().includes(trimmedQuery.toLowerCase()) ||
              (cache?.status === "loaded" && cache.plugins.some((p) => pluginMatchesFilter(p, trimmedQuery)));
            if (matches) next.add(m.name);
          }
          if (!cancelled) setExpandedMarketplaces(next);
        } finally {
          if (filterGeneration.current === generation) setFilterLoading(false);
        }
      })();
    }, FILTER_DEBOUNCE_MS);
    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
  }, [trimmedQuery, setExpandedMarketplaces, store]);

  function toggleMarketplace(name: string): void {
    const wasExpanded = expandedMarketplaces.has(name);
    setExpandedMarketplaces((prev) => {
      const next = new Set(prev);
      if (wasExpanded) next.delete(name);
      else next.add(name);
      return next;
    });
    if (wasExpanded) {
      onCollapseMarketplace(name);
    } else if (!store.getState().browseCatalogs.has(name)) {
      void store.getState().browseMarketplace(name);
    }
  }

  function isInstalled(plugin: string, marketplace: string): boolean {
    return plugins.some((p) => p.plugin === plugin && p.marketplace === marketplace);
  }

  async function handleConfirmInstall() {
    const target = pendingInstall;
    if (target === null || pluginsUnavailable) return;
    if (isInstalled(target.plugin, target.marketplace)) {
      setPendingInstall(null);
      return;
    }
    try {
      await store.getState().installPlugin(target.plugin, target.marketplace);
      toasts.push("success", `Installed ${target.plugin}`);
      setPendingInstall(null);
    } catch (err) {
      toasts.push("error", isHubWriteBusy(err) ? HUB_WRITE_BUSY : `Install failed: ${errorText(err)}`);
    }
  }

  function marketplaceHasMatch(name: string): boolean {
    const cache = browseCatalogs.get(name);
    return cache?.status === "loaded" && cache.plugins.some((p) => pluginMatchesFilter(p, trimmedQuery));
  }

  function marketplaceUnresolved(name: string): boolean {
    const cache = browseCatalogs.get(name);
    return cache === undefined || cache.status === "loading" || cache.status === "error";
  }

  const visibleMarketplaces =
    trimmedQuery === ""
      ? marketplaces
      : marketplaces.filter(
          (m) =>
            m.name.toLowerCase().includes(trimmedQuery.toLowerCase()) ||
            marketplaceUnresolved(m.name) ||
            marketplaceHasMatch(m.name),
        );

  function resetAddForm() {
    setKind("url");
    setUrlValue("");
    setRepoValue("");
    setPathValue("");
    setRefValue("");
    setNameValue("");
  }

  function buildSource(): import("@evener/appwire-client").MarketplaceSourceInput {
    const ref = refValue.trim();
    if (kind === "github") return { kind: "github", repo: repoValue.trim(), ...(ref ? { ref } : {}) };
    if (kind === "directory") return { kind: "directory", path: pathValue.trim() };
    return { kind: "url", url: urlValue.trim(), ...(ref ? { ref } : {}) };
  }

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (readUnavailable || hubWriteBusy) return;
    const trimmedName = nameValue.trim();
    try {
      await store.getState().addMarketplace({ name: trimmedName, source: buildSource() });
      setAddOpen(false);
      resetAddForm();
      toasts.push("success", `Added marketplace${trimmedName ? ` ${trimmedName}` : ""}`);
    } catch (err) {
      toasts.push("error", isHubWriteBusy(err) ? HUB_WRITE_BUSY : `Add marketplace failed: ${errorText(err)}`);
    }
  }

  function handleCancelAdd() {
    setAddOpen(false);
    resetAddForm();
  }

  return (
    <section ref={sectionRef} className={CLASS.section}>
      <p>
        A marketplace is a repository or local directory carrying a <code>.claude-plugin/marketplace.json</code> plugin
        catalog. Open one to install its plugins or change where it comes from. See the{" "}
        <a href="https://github.com/prime-radiant-inc/evener/blob/main/docs/plugins.md#install-from-a-marketplace">
          plugin marketplace guide
        </a>
        .
      </p>
      <div className={CLASS.filterRow}>
        <Input
          value={filterQuery}
          onChange={(event) => setFilterQuery(event.target.value)}
          placeholder="Filter marketplaces and plugins…"
        />
      </div>
      <ul aria-label="Marketplaces" className={CLASS.list}>
        {marketplaces.length === 0 ? (
          <li>
            <EmptyState size="compact" reason="empty" title="No marketplaces registered. Add one below." />
          </li>
        ) : trimmedQuery !== "" && filterLoading ? (
          <li>
            <Loader label="Loading marketplaces" />
          </li>
        ) : trimmedQuery !== "" && visibleMarketplaces.length === 0 ? (
          <li>
            <EmptyState size="compact" title={`No plugins match "${trimmedQuery}".`} />
          </li>
        ) : (
          visibleMarketplaces.map((m) => {
            const isOpen = expandedMarketplaces.has(m.name);
            return (
              <li key={m.name} data-marketplace={m.name}>
                <ListRow
                  data-kind="marketplace-row"
                  aria-expanded={isOpen}
                  aria-controls={isOpen ? marketplaceDetailBodyId(m.name) : undefined}
                  onKeyDown={(event) => handleRowEscape(event, isOpen, () => toggleMarketplace(m.name))}
                  label={m.name}
                  chips={<Chip>{m.source.kind}</Chip>}
                  meta={marketplaceSourceLabel(m.source)}
                  onSelect={() => toggleMarketplace(m.name)}
                />
                {isOpen && (
                  <MarketplaceDetailBody
                    id={marketplaceDetailBodyId(m.name)}
                    marketplace={m}
                    catalog={browseCatalogs.get(m.name)}
                    isInstalled={isInstalled}
                    installDisabled={hubWriteBusy || pluginsUnavailable}
                    expandedRow={expanded}
                    onToggleRow={onToggleRow}
                    onInstall={(plugin, marketplace) => setPendingInstall({ plugin, marketplace })}
                    onRenamed={(oldName, newName) => {
                      // The rename came from this body, so the marketplace
                      // was expanded; add the new name unconditionally - the
                      // page's vanish prune can have already dropped the old
                      // name in the same batch as the list update.
                      setExpandedMarketplaces((current) => {
                        const next = new Set(current);
                        next.delete(oldName);
                        next.add(newName);
                        return next;
                      });
                      onRenamedMarketplace(oldName, newName);
                    }}
                    onCollapse={() => toggleMarketplace(m.name)}
                    appliedRemovalNames={appliedRemovalNames}
                    connectionClient={connectionClient}
                    onAppliedRemoval={onAppliedRemoval}
                  />
                )}
              </li>
            );
          })
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
                directory={directory}
                id={pathId}
                value={pathValue}
                onChange={setPathValue}
                kind="dir"
                complete={(prefix, includeFiles) => store.getState().completePaths(prefix, includeFiles)}
                placeholder="/absolute/path"
              />
            </FormRow>
          )}
          {marketplaceSourceTakesRef(kind) && (
            <FormRow
              label="Branch or tag (optional)"
              htmlFor={refId}
              help="Defaults to the repository's default branch."
            >
              <Input
                id={refId}
                value={refValue}
                onChange={(event) => setRefValue(event.target.value)}
                placeholder="main"
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
            <Button type="submit" disabled={hubWriteBusy || readUnavailable}>
              Add
            </Button>
            <Button type="button" variant="quiet" onClick={handleCancelAdd} disabled={hubWriteBusy}>
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
        open={pendingInstall !== null}
        title="Install plugin"
        confirmLabel="Install"
        destructive={false}
        busy={hubWriteBusy || pluginsUnavailable}
        onConfirm={() => void handleConfirmInstall()}
        onCancel={() => setPendingInstall(null)}
      >
        {pendingInstall !== null && (
          <>
            <p>{`Install "${pendingInstall.plugin}" from ${pendingInstall.marketplace}? This makes it available on this host. New sessions use their launch's plugin selection; existing sessions keep theirs.`}</p>
            {(() => {
              const source = marketplaces.find((m) => m.name === pendingInstall.marketplace)?.source;
              return source && <p>{`Source: ${marketplaceSourceLabel(source)}`}</p>;
            })()}
          </>
        )}
      </ConfirmDialog>
    </section>
  );
}
