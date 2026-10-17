// Host-scoped plugin workspace. Sources keeps launch directories available
// independently of the plugin and marketplace inventory reads.

import type { AppwireClientLike } from "@evener/appwire-client";
import { useEffect, useState } from "react";
import { connectionStore, useConnectionStore } from "../../../../stores/connection";
import { LOCAL_HOST } from "../../../../stores/hostRouting";
import { InlineNotice, SegmentedControl, Skeleton } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { HostScopedSurface } from "../hostScopedSurface";
import { useHostScopedLoad } from "../useConnectedEffect";
import { BrowseSection } from "./BrowseSection";
import { ExtensionsStoreProvider, useExtensionsHostState, useExtensionsHostStore } from "./hostStore";
import { InstalledSection } from "./InstalledSection";
import { MarketplaceSheet } from "./MarketplaceSheet";
import styles from "./marketplacesPlugins.module.css";
import { PluginDetailSheet } from "./PluginDetailSheet";
import { SourcesSection } from "./SourcesSection";

const CLASS = {
  page: requireClass(styles.page, "marketplacesPlugins.module.css", "page"),
  help: requireClass(styles.help, "marketplacesPlugins.module.css", "help"),
};

type SegmentId = "installed" | "browse" | "sources";

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

  // useHostScopedLoad, which is that mount shape (wait for the shared client to
  // actually be ready, since AppShell mounts the pane tree independently of the
  // connect() handshake completing) and re-issues this read when the selected
  // host changes - or comes BACK after being away, which is what would otherwise
  // leave this pane on the failure its away host answered with.
  //
  // Once the plugin list has loaded (and only then), the view asks the hub
  // once whether any plugin has an update; nothing polls. The check waits on
  // every plugin's remote, so it starts after the list is on screen and the
  // load does not wait for it.
  useHostScopedLoad(host, async () => {
    await Promise.all([store.getState().fetchMarketplaces(), store.getState().fetchPlugins()]);
    if (store.getState().pluginsError === null) void store.getState().checkPluginUpdates();
  }, [store]);

  function handleSegmentChange(segment: SegmentId) {
    setActiveSegment(segment);
    // Each sheet belongs to the segment whose row opened it - the plugin
    // detail sheet to Installed, the marketplace editor to Sources.
    // Navigating away closes both rather than leaving an editor floating
    // over an unrelated list.
    setSelectedPlugin(null);
    setSelectedMarketplace(null);
  }

  const loadError = marketplacesError ?? pluginsError;
  const stillLoading = (marketplacesLoading || marketplaces === null) && (pluginsLoading || plugins === null);

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
          { value: "sources", label: `Sources (${marketplaceCount})` },
        ]}
        fullWidth
      />
      <p className={CLASS.help}>
        Installation makes a plugin available on this host. Enabled defaults apply to new sessions using the default
        plugin selection; custom launches choose their own plugins. Existing sessions keep their selected plugins.
      </p>
      {activeSegment === "sources" ? (
        <SourcesSection host={host} onSelectMarketplace={setSelectedMarketplace} />
      ) : loadError !== null ? (
        <InlineNotice tone="danger" live="status" data-reason="unavailable">
          Failed to load: {loadError}
        </InlineNotice>
      ) : stillLoading ? (
        <Skeleton />
      ) : (
        <>
          {activeSegment === "installed" && <InstalledSection onSelect={setSelectedPlugin} />}
          {activeSegment === "browse" && (
            <BrowseSection
              expandedMarketplaces={expandedMarketplaces}
              setExpandedMarketplaces={setExpandedMarketplaces}
            />
          )}
        </>
      )}
      {loadError === null && !stillLoading && (
        <PluginDetailSheet target={selectedPlugin} onClose={() => setSelectedPlugin(null)} />
      )}
      {marketplacesError === null && marketplaces !== null && !stillLoading && (
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
      )}
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

/** MarketplacesPluginsHostScope is the Plugins settings section
 * scoped to the settings route's selected host: the one shared HostPicker plus
 * MarketplacesPluginsSection. */
export function MarketplacesPluginsHostScope() {
  return <HostScopedSurface>{(host) => <MarketplacesPluginsSection host={host} />}</HostScopedSurface>;
}
