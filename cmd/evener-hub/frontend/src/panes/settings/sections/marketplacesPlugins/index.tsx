// marketplacesPlugins/index.tsx is the "Marketplaces & Plugins" settings
// section (#12 - parity-m7-settings.md §12), Segmented Workspace redesign:
// one list at a time behind a page-level SegmentedControl (Installed /
// Browse / Marketplaces) instead of the three stacked sub-sections the
// parity wave shipped. Per-plugin actions live in the PluginDetailSheet
// opened from an Installed row, and every per-marketplace action in the
// MarketplaceSheet opened from a Marketplaces row, so each segment stays a
// single-purpose list. The orchestrator's jobs are unchanged in kind: the
// initial parallel fetch (mirroring the legacy's own Promise.all), the
// top-level loading/error gate, and the lifted `expandedMarketplaces` (the
// sheet's Refresh reads it - see MarketplaceSheet's own comment); on top of
// those it owns the page-level states activeSegment, selectedPlugin and
// selectedMarketplace.

import type { AppwireClientLike } from "@evener/appwire-client";
import { friendlyErrorMessage } from "@evener/appwire-client";
import { useEffect, useState } from "react";
import { connectionStore, useConnectionStore } from "../../../../stores/connection";
import { LOCAL_HOST } from "../../../../stores/hostRouting";
import { EmptyState, SegmentedControl, Skeleton } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { HostScopedSurface } from "../hostScopedSurface";
import { BrowseSection } from "./BrowseSection";
import { ExtensionsStoreProvider, useExtensionsHostState, useExtensionsHostStore } from "./hostStore";
import { InstalledSection } from "./InstalledSection";
import { MarketplaceSheet } from "./MarketplaceSheet";
import { MarketplacesSection } from "./MarketplacesSection";
import styles from "./marketplacesPlugins.module.css";
import { PluginDetailSheet } from "./PluginDetailSheet";

const CLASS = {
  page: requireClass(styles.page, "marketplacesPlugins.module.css", "page"),
};

type SegmentId = "installed" | "browse" | "marketplaces";

type AppliedRemovalGuard = {
  host: string;
  client: AppwireClientLike | null;
  names: ReadonlyMap<string, number>;
};

const EMPTY_APPLIED_REMOVALS: ReadonlySet<string> = new Set();

/**
 * The settings section for #12. Every mutation across the segments goes
 * straight through stores/extensions.ts (each RPC response already carries
 * the updated list - see that store's own doc comment).
 */
function MarketplacesPluginsBody({ host }: { host: string }) {
  // The selected host's extensions store, supplied by MarketplacesPluginsSection
  // below (component 07b): the controller's own for the local hub, a per-host
  // instance over evener/host/request for a remote one.
  const store = useExtensionsHostStore();
  const marketplaces = useExtensionsHostState((s) => s.marketplaces);
  const marketplacesLoading = useExtensionsHostState((s) => s.marketplacesLoading);
  const marketplacesError = useExtensionsHostState((s) => s.marketplacesError);
  const plugins = useExtensionsHostState((s) => s.plugins);
  const pluginsLoading = useExtensionsHostState((s) => s.pluginsLoading);
  const pluginsError = useExtensionsHostState((s) => s.pluginsError);
  const marketplacesPublicationVersion = useExtensionsHostState((s) => s.marketplacesPublicationVersion);
  const connectionClient = useConnectionStore((s) => s.client);
  const [expandedMarketplaces, setExpandedMarketplaces] = useState<Set<string>>(new Set());
  const [activeSegment, setActiveSegment] = useState<SegmentId>("installed");
  const [selectedPlugin, setSelectedPlugin] = useState<{ plugin: string; marketplace: string } | null>(null);
  const [selectedMarketplace, setSelectedMarketplace] = useState<string | null>(null);
  // The registry removal can finish while the sheet is unmounted by the page's
  // load-error gate. Keep its no-repeat marker at the page lifecycle, and tie
  // it to the host that owns the catalog AND the client that owns the
  // connection: a new host or a new hub starts unblocked.
  const [appliedRemovalGuard, setAppliedRemovalGuard] = useState<AppliedRemovalGuard>(() => ({
    host,
    client: connectionClient,
    names: new Map(),
  }));
  const appliedRemovalNames =
    appliedRemovalGuard.client === connectionClient && appliedRemovalGuard.host === host
      ? new Set(appliedRemovalGuard.names.keys())
      : EMPTY_APPLIED_REMOVALS;

  // Keyed on BOTH the connection client and the selected host: a different
  // host owns a different catalog, so its same-named marketplace must not
  // inherit the marker a removal on the previous host left behind.
  useEffect(() => {
    setAppliedRemovalGuard((current) =>
      current.client === connectionClient && current.host === host
        ? current
        : { host, client: connectionClient, names: new Map() },
    );
  }, [connectionClient, host]);

  useEffect(() => {
    if (appliedRemovalGuard.client !== connectionClient || appliedRemovalGuard.host !== host) return;
    setAppliedRemovalGuard((current) => {
      if (current.client !== connectionClient || current.host !== host) return current;
      const next = new Map([...current.names].filter(([, baseline]) => baseline >= marketplacesPublicationVersion));
      return next.size === current.names.size ? current : { host: current.host, client: current.client, names: next };
    });
  }, [appliedRemovalGuard.client, appliedRemovalGuard.host, connectionClient, host, marketplacesPublicationVersion]);

  function markAppliedRemoval(name: string, owner: AppwireClientLike | null, publicationVersion: number): void {
    if (connectionStore.getState().client !== owner) return;
    setAppliedRemovalGuard((current) => {
      if (current.client !== owner || current.host !== host) return current;
      const names = new Map(current.names);
      names.set(name, publicationVersion);
      return { host: current.host, client: owner, names };
    });
  }

  // Mirrors DirListSetting's own mount-effect shape (see that component's
  // comment): waits for the shared client to actually be ready before
  // firing the one initial fetch, since AppShell mounts the pane tree
  // independently of the connect() handshake completing.
  useEffect(() => {
    let started = false;
    function tryStart() {
      if (started || connectionStore.getState().state !== "ready") return;
      started = true;
      void store.getState().fetchMarketplaces();
      void store.getState().fetchPlugins();
    }
    tryStart();
    return connectionStore.subscribe(tryStart);
  }, [store]);

  function handleSegmentChange(segment: SegmentId) {
    setActiveSegment(segment);
    // Each sheet belongs to the segment whose row opened it - the plugin
    // detail sheet to Installed, the marketplace editor to Marketplaces.
    // Navigating away closes both rather than leaving an editor floating
    // over an unrelated list.
    setSelectedPlugin(null);
    setSelectedMarketplace(null);
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
      {activeSegment === "marketplaces" && <MarketplacesSection onSelect={setSelectedMarketplace} />}
      <PluginDetailSheet target={selectedPlugin} onClose={() => setSelectedPlugin(null)} />
      <MarketplaceSheet
        name={selectedMarketplace}
        onClose={() => setSelectedMarketplace(null)}
        onRenamed={setSelectedMarketplace}
        expandedMarketplaces={expandedMarketplaces}
        setExpandedMarketplaces={setExpandedMarketplaces}
        appliedRemovalNames={appliedRemovalNames}
        connectionClient={connectionClient}
        onAppliedRemoval={markAppliedRemoval}
      />
    </section>
  );
}

export interface MarketplacesPluginsSectionProps {
  /** The host whose own marketplaces and plugins this section edits (component
   * 07b). Defaults to the local hub, so a direct render is today's section. */
  host?: string;
}

/** MarketplacesPluginsSection provides the selected host's extensions store to
 * the whole subtree, so a remote host's marketplaces and plugins are both shown
 * and changed from here - never this hub's. */
export function MarketplacesPluginsSection({ host = LOCAL_HOST }: MarketplacesPluginsSectionProps) {
  return (
    <ExtensionsStoreProvider host={host}>
      <MarketplacesPluginsBody host={host} />
    </ExtensionsStoreProvider>
  );
}

/** MarketplacesPluginsHostScope is the "Marketplaces & Plugins" settings section
 * scoped to the settings route's selected host: the one shared HostPicker plus
 * MarketplacesPluginsSection. */
export function MarketplacesPluginsHostScope() {
  return <HostScopedSurface>{(host) => <MarketplacesPluginsSection host={host} />}</HostScopedSurface>;
}
