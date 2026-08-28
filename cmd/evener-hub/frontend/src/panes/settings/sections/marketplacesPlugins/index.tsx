// marketplacesPlugins/index.tsx is the "Marketplaces & Plugins" settings
// section (#12 - parity-m7-settings.md §12), Segmented Workspace redesign:
// one list at a time behind a page-level SegmentedControl (Installed /
// Browse / Marketplaces) instead of the three stacked sub-sections the
// parity wave shipped. Per-plugin actions live in the PluginDetailSheet
// opened from an Installed row, so each segment stays a single-purpose
// list. The orchestrator's jobs are unchanged in kind: the initial parallel
// fetch (mirroring the legacy's own Promise.all), the top-level
// loading/error gate, and the lifted `expandedMarketplaces` (see
// MarketplacesSection's own comment for why Refresh needs to read it); on
// top of those it owns the two new page-level states, activeSegment and
// selectedPlugin.
import { useEffect, useState } from "react";
import { friendlyErrorMessage } from "../../../../protocol/errors";
import { connectionStore } from "../../../../stores/connection";
import { extensionsStore, useExtensionsStore } from "../../../../stores/extensions";
import { EmptyState, SegmentedControl, Skeleton } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { BrowseSection } from "./BrowseSection";
import { InstalledSection } from "./InstalledSection";
import { MarketplacesSection } from "./MarketplacesSection";
import styles from "./marketplacesPlugins.module.css";
import { PluginDetailSheet } from "./PluginDetailSheet";

const CLASS = {
  page: requireClass(styles.page, "marketplacesPlugins.module.css", "page"),
};

type SegmentId = "installed" | "browse" | "marketplaces";

/**
 * The settings section for #12. Every mutation across the segments goes
 * straight through stores/extensions.ts (each RPC response already carries
 * the updated list - see that store's own doc comment).
 */
export function MarketplacesPluginsSection() {
  const marketplaces = useExtensionsStore((s) => s.marketplaces);
  const marketplacesLoading = useExtensionsStore((s) => s.marketplacesLoading);
  const marketplacesError = useExtensionsStore((s) => s.marketplacesError);
  const plugins = useExtensionsStore((s) => s.plugins);
  const pluginsLoading = useExtensionsStore((s) => s.pluginsLoading);
  const pluginsError = useExtensionsStore((s) => s.pluginsError);
  const [expandedMarketplaces, setExpandedMarketplaces] = useState<Set<string>>(new Set());
  const [activeSegment, setActiveSegment] = useState<SegmentId>("installed");
  const [selectedPlugin, setSelectedPlugin] = useState<{ plugin: string; marketplace: string } | null>(null);

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

  function handleSegmentChange(segment: SegmentId) {
    setActiveSegment(segment);
    // The detail sheet belongs to the Installed list; navigating away from
    // the segment that opened it closes it rather than leaving an inspector
    // floating over an unrelated list.
    setSelectedPlugin(null);
  }

  const loadError = marketplacesError ?? pluginsError;
  const stillLoading = (marketplacesLoading || marketplaces === null) && (pluginsLoading || plugins === null);

  if (loadError !== null) {
    return (
      <section className={CLASS.page}>
        <EmptyState title="Failed to load" hint={friendlyErrorMessage(loadError)} />
      </section>
    );
  }
  if (stillLoading) {
    return (
      <section className={CLASS.page}>
        <Skeleton />
      </section>
    );
  }

  const pluginCount = plugins?.length ?? 0;
  const marketplaceCount = marketplaces?.length ?? 0;

  return (
    <section className={CLASS.page}>
      <SegmentedControl
        label="View"
        value={activeSegment}
        onChange={(value) => handleSegmentChange(value as SegmentId)}
        options={[
          { value: "installed", label: `Installed (${pluginCount})` },
          { value: "browse", label: "Browse" },
          { value: "marketplaces", label: `Marketplaces (${marketplaceCount})` },
        ]}
        fullWidth
      />
      {activeSegment === "installed" && <InstalledSection onSelect={setSelectedPlugin} />}
      {activeSegment === "browse" && (
        <BrowseSection expandedMarketplaces={expandedMarketplaces} setExpandedMarketplaces={setExpandedMarketplaces} />
      )}
      {activeSegment === "marketplaces" && <MarketplacesSection expandedMarketplaces={expandedMarketplaces} />}
      <PluginDetailSheet target={selectedPlugin} onClose={() => setSelectedPlugin(null)} />
    </section>
  );
}
