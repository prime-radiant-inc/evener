// BrowseSection: the marketplace browse tree + filter (parity-m7-
// settings.md §12d). `expandedMarketplaces` is lifted to the parent
// (marketplacesPlugins/index.tsx) - see MarketplacesSection's own comment
// for why (its Refresh action needs to read this component's expansion
// state).
import { type Dispatch, type SetStateAction, useEffect, useState } from "react";
import { errorText } from "../../../../protocol/errors";
import type { MarketplaceCatalogPlugin, MarketplaceEntry } from "../../../../protocol/types.gen";
import { extensionsStore, type MarketplaceCatalogEntry, useExtensionsStore } from "../../../../stores/extensions";
import { Button, Chevron, ConfirmDialog, Input, useToasts } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import styles from "./marketplacesPlugins.module.css";
import { sourceLabel } from "./sourceLabel";

const CLASS = {
  section: requireClass(styles.section, "marketplacesPlugins.module.css", "section"),
  header: requireClass(styles.header, "marketplacesPlugins.module.css", "header"),
  title: requireClass(styles.title, "marketplacesPlugins.module.css", "title"),
  filterRow: requireClass(styles.filterRow, "marketplacesPlugins.module.css", "filterRow"),
  tree: requireClass(styles.tree, "marketplacesPlugins.module.css", "tree"),
  treeNode: requireClass(styles.treeNode, "marketplacesPlugins.module.css", "treeNode"),
  treeToggle: requireClass(styles.treeToggle, "marketplacesPlugins.module.css", "treeToggle"),
  treeChevron: requireClass(styles.treeChevron, "marketplacesPlugins.module.css", "treeChevron"),
  treeCount: requireClass(styles.treeCount, "marketplacesPlugins.module.css", "treeCount"),
  treeChildren: requireClass(styles.treeChildren, "marketplacesPlugins.module.css", "treeChildren"),
  row: requireClass(styles.row, "marketplacesPlugins.module.css", "row"),
  rowMain: requireClass(styles.rowMain, "marketplacesPlugins.module.css", "rowMain"),
  rowText: requireClass(styles.rowText, "marketplacesPlugins.module.css", "rowText"),
  rowMeta: requireClass(styles.rowMeta, "marketplacesPlugins.module.css", "rowMeta"),
  rowActions: requireClass(styles.rowActions, "marketplacesPlugins.module.css", "rowActions"),
  installedBadge: requireClass(styles.installedBadge, "marketplacesPlugins.module.css", "installedBadge"),
  empty: requireClass(styles.empty, "marketplacesPlugins.module.css", "empty"),
};

const FILTER_DEBOUNCE_MS = 150;

function pluginMatchesFilter(plugin: MarketplaceCatalogPlugin, query: string): boolean {
  const q = query.toLowerCase();
  return plugin.name.toLowerCase().includes(q) || (plugin.description ?? "").toLowerCase().includes(q);
}

export interface BrowseSectionProps {
  expandedMarketplaces: Set<string>;
  setExpandedMarketplaces: Dispatch<SetStateAction<Set<string>>>;
}

export function BrowseSection({ expandedMarketplaces, setExpandedMarketplaces }: BrowseSectionProps) {
  const marketplaces = useExtensionsStore((s) => s.marketplaces) ?? [];
  const plugins = useExtensionsStore((s) => s.plugins) ?? [];
  const browseCatalogs = useExtensionsStore((s) => s.browseCatalogs);
  const toasts = useToasts();

  const [filterQuery, setFilterQuery] = useState("");
  const [filterLoading, setFilterLoading] = useState(false);
  const [pendingInstall, setPendingInstall] = useState<{ plugin: string; marketplace: string } | null>(null);
  const [installBusy, setInstallBusy] = useState(false);

  const trimmedQuery = filterQuery.trim();

  // Debounced filter-driven auto-expand (§12d): clearing the query collapses
  // everything instantly (no debounce); a non-empty query, 150ms after the
  // last keystroke, first lazily loads every not-yet-cached marketplace's
  // catalog (filterLoading shows "Loading marketplaces…" tree-wide meanwhile
  // - see the render below), then auto-expands every marketplace with a
  // match and collapses the rest.
  useEffect(() => {
    if (trimmedQuery === "") {
      setExpandedMarketplaces(new Set());
      return;
    }
    let cancelled = false;
    const timer = setTimeout(() => {
      void (async () => {
        const current = extensionsStore.getState().marketplaces ?? [];
        const missing = current.filter((m) => !extensionsStore.getState().browseCatalogs.has(m.name));
        if (missing.length > 0) {
          setFilterLoading(true);
          await Promise.all(missing.map((m) => extensionsStore.getState().browseMarketplace(m.name)));
          if (cancelled) return;
          setFilterLoading(false);
        }
        const next = new Set<string>();
        for (const m of current) {
          const cache = extensionsStore.getState().browseCatalogs.get(m.name);
          if (cache?.status === "loaded" && cache.plugins.some((p) => pluginMatchesFilter(p, trimmedQuery))) {
            next.add(m.name);
          }
        }
        if (!cancelled) setExpandedMarketplaces(next);
      })();
    }, FILTER_DEBOUNCE_MS);
    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
    // setExpandedMarketplaces is a useState setter (stable identity, either
    // this component's own or the parent's - see BrowseSectionProps), so
    // listing it changes nothing behaviorally; only trimmedQuery actually
    // varying is what should ever re-arm this debounce.
  }, [trimmedQuery, setExpandedMarketplaces]);

  function toggleExpanded(name: string) {
    const wasExpanded = expandedMarketplaces.has(name);
    setExpandedMarketplaces((prev) => {
      const next = new Set(prev);
      if (wasExpanded) next.delete(name);
      else next.add(name);
      return next;
    });
    if (!wasExpanded && !extensionsStore.getState().browseCatalogs.has(name)) {
      void extensionsStore.getState().browseMarketplace(name);
    }
  }

  function isInstalled(plugin: string, marketplace: string): boolean {
    return plugins.some((p) => p.plugin === plugin && p.marketplace === marketplace);
  }

  function findMarketplace(name: string): MarketplaceEntry | undefined {
    return marketplaces.find((m) => m.name === name);
  }

  async function handleConfirmInstall() {
    const target = pendingInstall;
    if (target === null) return;
    setInstallBusy(true);
    try {
      await extensionsStore.getState().installPlugin(target.plugin, target.marketplace);
      toasts.push("success", `Installed ${target.plugin}`);
      setPendingInstall(null);
    } catch (err) {
      toasts.push("error", `Install failed: ${errorText(err)}`);
    } finally {
      setInstallBusy(false);
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
      : marketplaces.filter((m) => marketplaceUnresolved(m.name) || marketplaceHasMatch(m.name));

  return (
    <section className={CLASS.section}>
      <header className={CLASS.header}>
        <h3 className={CLASS.title}>Browse</h3>
      </header>
      <div className={CLASS.filterRow}>
        <Input
          value={filterQuery}
          onChange={(event) => setFilterQuery(event.target.value)}
          placeholder="Filter plugins…"
        />
      </div>
      <ul aria-label="Marketplace browse tree" className={CLASS.tree}>
        {marketplaces.length === 0 ? (
          <li className={CLASS.empty}>No marketplaces registered. Add one above to browse plugins.</li>
        ) : trimmedQuery !== "" && filterLoading ? (
          <li className={CLASS.empty}>Loading marketplaces…</li>
        ) : trimmedQuery !== "" && visibleMarketplaces.length === 0 ? (
          <li className={CLASS.empty}>{`No plugins match "${trimmedQuery}".`}</li>
        ) : (
          visibleMarketplaces.map((m) => (
            <MarketplaceNode
              key={m.name}
              marketplace={m}
              expanded={expandedMarketplaces.has(m.name)}
              cache={browseCatalogs.get(m.name)}
              query={trimmedQuery}
              isInstalled={isInstalled}
              onToggle={() => toggleExpanded(m.name)}
              onInstall={(plugin, marketplace) => setPendingInstall({ plugin, marketplace })}
            />
          ))
        )}
      </ul>
      <ConfirmDialog
        open={pendingInstall !== null}
        title="Install plugin"
        confirmLabel="Install"
        destructive={false}
        busy={installBusy}
        onConfirm={() => void handleConfirmInstall()}
        onCancel={() => setPendingInstall(null)}
      >
        {pendingInstall !== null && (
          <>
            <p>{`Install "${pendingInstall.plugin}" from ${pendingInstall.marketplace}? It will run in every new session once installed.`}</p>
            {(() => {
              const source = findMarketplace(pendingInstall.marketplace)?.source;
              return source && <p className={CLASS.rowMeta}>{`Source: ${sourceLabel(source)}`}</p>;
            })()}
          </>
        )}
      </ConfirmDialog>
    </section>
  );
}

function MarketplaceNode({
  marketplace,
  expanded,
  cache,
  query,
  isInstalled,
  onToggle,
  onInstall,
}: {
  marketplace: MarketplaceEntry;
  expanded: boolean;
  cache: MarketplaceCatalogEntry | undefined;
  query: string;
  isInstalled: (plugin: string, marketplace: string) => boolean;
  onToggle: () => void;
  onInstall: (plugin: string, marketplace: string) => void;
}) {
  const rows =
    cache?.status === "loaded"
      ? query
        ? cache.plugins.filter((p) => pluginMatchesFilter(p, query))
        : cache.plugins
      : [];

  return (
    <li className={CLASS.treeNode}>
      <button type="button" className={CLASS.treeToggle} aria-expanded={expanded} onClick={onToggle}>
        <span className={CLASS.treeChevron} aria-hidden="true">
          <Chevron direction="right" />
        </span>
        {marketplace.name}
        {cache?.status === "loaded" && <span className={CLASS.treeCount}> ({cache.plugins.length})</span>}
      </button>
      {expanded && (
        <ul aria-label={`${marketplace.name} plugins`} className={CLASS.treeChildren}>
          {!cache || cache.status === "loading" ? (
            <li className={CLASS.empty}>Loading…</li>
          ) : cache.status === "error" ? (
            <li className={CLASS.empty}>{`Failed to browse: ${cache.error}`}</li>
          ) : rows.length === 0 ? (
            <li className={CLASS.empty}>This marketplace has no plugins.</li>
          ) : (
            rows.map((p) => (
              <li key={p.name} className={CLASS.row}>
                <div className={CLASS.rowMain}>
                  <div className={CLASS.rowText}>{p.name}</div>
                  <div className={CLASS.rowMeta}>
                    {p.description}
                    {p.category ? ` · ${p.category}` : ""}
                  </div>
                </div>
                <div className={CLASS.rowActions}>
                  {isInstalled(p.name, marketplace.name) ? (
                    <span className={CLASS.installedBadge}>Installed</span>
                  ) : (
                    <Button size="sm" onClick={() => onInstall(p.name, marketplace.name)}>
                      Install
                    </Button>
                  )}
                </div>
              </li>
            ))
          )}
        </ul>
      )}
    </li>
  );
}
