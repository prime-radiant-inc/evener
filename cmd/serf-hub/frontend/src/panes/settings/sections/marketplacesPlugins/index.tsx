// marketplacesPlugins/index.tsx is the "Marketplaces & Plugins" settings
// section (#12 - parity-m7-settings.md §12) - the wave's second-dominant
// piece after Credentials. Orchestrates the 3 sub-sections
// (Marketplaces/Browse/Installed): fetches both lists in parallel on mount
// (mirroring the legacy's own `Promise.all([refreshMarketplaces(),
// refreshInstalled()])`), and owns `expandedMarketplaces` - lifted here
// rather than owned by BrowseSection alone, because MarketplacesSection's
// Refresh action needs to read it too (see that component's own comment).
import { useEffect, useState } from "react";
import { connectionStore } from "../../../../stores/connection";
import { extensionsStore, useExtensionsStore } from "../../../../stores/extensions";
import { EmptyState, Skeleton } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { BrowseSection } from "./BrowseSection";
import { InstalledSection } from "./InstalledSection";
import { MarketplacesSection } from "./MarketplacesSection";
import styles from "./marketplacesPlugins.module.css";

const CLASS = {
  page: requireClass(styles.page, "marketplacesPlugins.module.css", "page"),
  pageTitle: requireClass(styles.pageTitle, "marketplacesPlugins.module.css", "pageTitle"),
  pageHelp: requireClass(styles.pageHelp, "marketplacesPlugins.module.css", "pageHelp"),
};

/**
 * The settings section for #12. Every mutation across the 3 sub-sections
 * goes straight through stores/extensions.ts (each RPC response already
 * carries the updated list - see that store's own doc comment), so this
 * orchestrator's only jobs are the initial parallel fetch, the top-level
 * loading/error gate (matching the legacy's own "initial load throws ->
 * replace the entire root with one message, no partial UI, no retry"
 * behavior - templates/partials/settings/plugins-manager.html:326-329),
 * and lifting `expandedMarketplaces`.
 */
export function MarketplacesPluginsSection() {
  const marketplaces = useExtensionsStore((s) => s.marketplaces);
  const marketplacesLoading = useExtensionsStore((s) => s.marketplacesLoading);
  const marketplacesError = useExtensionsStore((s) => s.marketplacesError);
  const plugins = useExtensionsStore((s) => s.plugins);
  const pluginsLoading = useExtensionsStore((s) => s.pluginsLoading);
  const pluginsError = useExtensionsStore((s) => s.pluginsError);
  const [expandedMarketplaces, setExpandedMarketplaces] = useState<Set<string>>(new Set());

  // Mirrors DirListSetting's own mount-effect shape (see that component's
  // comment): waits for the shared client to actually be ready before
  // firing the one initial fetch, since AppShell mounts the pane tree
  // independently of the connect() handshake completing.
  useEffect(() => {
    let started = false;
    function tryStart() {
      if (started || connectionStore.getState().state !== "ready") return;
      started = true;
      void extensionsStore.getState().fetchMarketplaces();
      void extensionsStore.getState().fetchPlugins();
    }
    tryStart();
    return connectionStore.subscribe(tryStart);
  }, []);

  const loadError = marketplacesError ?? pluginsError;
  const stillLoading = (marketplacesLoading || marketplaces === null) && (pluginsLoading || plugins === null);

  return (
    <section className={CLASS.page}>
      <h2 className={CLASS.pageTitle}>Marketplaces &amp; Plugins</h2>
      <p className={CLASS.pageHelp}>
        Marketplaces are catalogs of installable plugins. Register a marketplace, browse its catalog, and install what
        you need — installed, enabled plugins load into every new serf session.
      </p>
      {loadError !== null ? (
        <EmptyState title="Failed to load" hint={`Failed to load: ${loadError}`} />
      ) : stillLoading ? (
        <Skeleton />
      ) : (
        <>
          <MarketplacesSection expandedMarketplaces={expandedMarketplaces} />
          <BrowseSection
            expandedMarketplaces={expandedMarketplaces}
            setExpandedMarketplaces={setExpandedMarketplaces}
          />
          <InstalledSection />
        </>
      )}
    </section>
  );
}
