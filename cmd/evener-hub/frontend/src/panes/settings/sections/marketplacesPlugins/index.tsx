// Host-scoped plugin workspace: Installed lists what this host has, and
// Marketplaces is the one screen for both installing plugins and managing
// marketplace sources (plugin directories stay available independently of
// both reads).

import type { AppwireClientLike } from "@evener/appwire-client";
import { LOCAL_HOST } from "@evener/appwire-client";
import { useEffect, useRef, useState } from "react";
import { connectionStore, useConnectionStore } from "../../../../stores/connection";
import { Button, InlineNotice, SegmentedControl, Skeleton } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { samePluginTarget, usePluginRowFocusRehome } from "../../../plugins/pluginRowFocus";
import { HostScopedSurface } from "../hostScopedSurface";
import { PluginsDirsSection } from "../pluginsDirs";
import { useHostScopedLoad } from "../useConnectedEffect";
import { ExtensionsStoreProvider, useExtensionsHostState, useExtensionsHostStore } from "./hostStore";
import { InstalledSection } from "./InstalledSection";
import { MarketplacesSection } from "./MarketplacesSection";
import styles from "./marketplacesPlugins.module.css";
import type { PluginDetailTarget } from "./PluginDetailBody";

const CLASS = {
  page: requireClass(styles.page, "marketplacesPlugins.module.css", "page"),
  help: requireClass(styles.help, "marketplacesPlugins.module.css", "help"),
};

type SegmentId = "installed" | "marketplaces" | "directories";

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
  // The one row whose details are expanded, across every segment: a row tap
  // expands its inline detail body, another row's tap swaps which one is
  // open, and the same row's tap again collapses it.
  const [expandedPair, setExpandedPair] = useState<PluginDetailTarget | null>(null);
  const sectionRef = useRef<HTMLElement>(null);
  // The registry removal can finish after its sheet closes. Keep its
  // no-repeat marker at the page lifecycle, and tie
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

  // A collapse over a completed install or remove reshapes the row whose
  // details were open, unmounting the control that held the keyboard, and
  // the modal confirm's own focus restore then loses its opener: the hook
  // returns focus to the row when that happened.
  usePluginRowFocusRehome(expandedPair, sectionRef);

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
    // Each expansion belongs to the segment whose row opened it. Navigating
    // away collapses the plugin details rather than leaving them floating
    // over an unrelated list; marketplace expansions survive, keyed by name.
    setExpandedPair(null);
  }

  // Marketplace rows and the plugin rows inside them are keyed by name or
  // pair. A marketplace that vanishes (its own remove, or another client's)
  // takes its expansion - and any plugin details opened inside it - with it.
  useEffect(() => {
    if (marketplaces === null) return;
    const marketplaceNames = new Set(marketplaces.map((m) => m.name));
    setExpandedMarketplaces((current) => {
      const next = new Set([...current].filter((name) => marketplaceNames.has(name)));
      return next.size === current.size ? current : next;
    });
    setExpandedPair((current) => (current !== null && !marketplaceNames.has(current.marketplace) ? null : current));
  }, [marketplaces]);

  /** A marketplace row collapsing takes any plugin details open inside it. */
  function collapseMarketplace(name: string): void {
    setExpandedPair((current) => (current !== null && current.marketplace === name ? null : current));
  }

  /** A rename moves an open plugin detail's pair with its marketplace. */
  function renameMarketplace(oldName: string, newName: string): void {
    setExpandedPair((current) =>
      current !== null && current.marketplace === oldName ? { plugin: current.plugin, marketplace: newName } : current,
    );
  }

  // Both lists toggle the same accordion state: the tapped pair opens
  // unless it is the pair already open, which collapses. The body derives
  // its own mode from the installed list, so no resolution happens here.
  function toggleDetail(pair: PluginDetailTarget): void {
    setExpandedPair((current) =>
      current !== null && samePluginTarget(current, pair)
        ? null
        : { plugin: pair.plugin, marketplace: pair.marketplace },
    );
  }

  const pluginCount = plugins?.length ?? 0;
  const marketplaceCount = marketplaces?.length ?? 0;

  return (
    <section ref={sectionRef} className={CLASS.page}>
      <SegmentedControl
        label="View"
        value={activeSegment}
        onChange={(value) => handleSegmentChange(value as SegmentId)}
        options={[
          { value: "installed", label: `Installed (${pluginCount})` },
          { value: "marketplaces", label: `Marketplaces (${marketplaceCount})` },
          { value: "directories", label: "Directories" },
        ]}
        fullWidth
      />
      <p className={CLASS.help}>
        Installation makes a plugin available on this host. Enabled defaults apply to new sessions using the default
        plugin selection; custom launches choose their own plugins. Existing sessions keep their selected plugins.
      </p>
      {pluginsError !== null && (
        <InlineNotice
          tone="danger"
          live="status"
          data-reason="unavailable"
          action={
            <Button size="sm" onClick={() => void store.getState().fetchPlugins()} disabled={pluginsLoading}>
              Retry plugins
            </Button>
          }
        >
          Failed to load plugins: {pluginsError}
        </InlineNotice>
      )}
      {marketplacesError !== null && (
        <InlineNotice
          tone="danger"
          live="status"
          data-reason="unavailable"
          action={
            <Button size="sm" onClick={() => void store.getState().fetchMarketplaces()} disabled={marketplacesLoading}>
              Retry marketplaces
            </Button>
          }
        >
          Failed to load marketplaces: {marketplacesError}
        </InlineNotice>
      )}
      {activeSegment === "directories" && <PluginsDirsSection host={host} />}
      {activeSegment === "installed" &&
        (plugins === null ? (
          pluginsError === null && <Skeleton />
        ) : (
          <InstalledSection
            expanded={expandedPair}
            onToggle={toggleDetail}
            onBrowse={() => handleSegmentChange("marketplaces")}
          />
        ))}
      {activeSegment === "marketplaces" &&
        (marketplaces === null ? (
          marketplacesError === null && <Skeleton />
        ) : (
          <MarketplacesSection
            expandedMarketplaces={expandedMarketplaces}
            setExpandedMarketplaces={setExpandedMarketplaces}
            expanded={expandedPair}
            onToggleRow={toggleDetail}
            onCollapseMarketplace={collapseMarketplace}
            onRenamedMarketplace={renameMarketplace}
            appliedRemovalNames={appliedRemovalNames}
            connectionClient={connectionClient}
            onAppliedRemoval={markAppliedRemoval}
          />
        ))}
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
