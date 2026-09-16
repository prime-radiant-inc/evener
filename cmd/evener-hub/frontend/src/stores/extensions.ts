// extensions.ts is the thin wire-truth store behind the Extensions settings
// cluster (Marketplaces & Plugins, Plugins/Skills directories, MCP servers -
// panes/settings/sections/{marketplacesPlugins,dirListSetting,pluginsDirs,
// skillsDirs,mcp}). It rides the single AppwireClientLike connection.ts wires
// via useConnectionStore.getState().connect(client), same as threads.ts/
// tree.ts - this store has no connect() of its own.
//
// Split, deliberately, into two halves with different failure conventions,
// mirroring the legacy JS's own two conventions (plugins-manager.html toasts
// on every mutation failure; plugins.html/skills.html/mcp.html show add-time
// validation failures inline instead):
//   - FETCHES (fetchMarketplaces/fetchPlugins/fetchLaunchLayer) never throw -
//     they track loading/error in state, exactly like tree.ts's refresh().
//   - MUTATIONS (add/remove/refresh/install/upgrade/.../setLaunchLayer)
//     reject on failure, exactly like threads.ts's send/steer/queue/
//     interrupt - this store is a plain module, not a hook, so it cannot
//     call useToasts() itself; the section components (which DO run inside
//     React) catch the rejection and toast, per the app's toast-on-failure
//     convention.

import type { AnyNotification, AppwireClientLike, PathValidateResponse } from "@evener/appwire-client";
import {
  createLaunchLayerStore,
  createMarketplacesStore,
  createPluginsStore,
  type LaunchLayerClient,
  type LaunchLayerState,
  type MarketplacesClient,
  type MarketplacesState,
  type PluginsClient,
  type PluginsState,
} from "@evener/appwire-client/state/extensions";
import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import { connectionStore, onConnectionNotification } from "./connection";
import { isLocalHost } from "./hostRouting";
import { launchConfigStore } from "./launchConfig";

export type { MarketplaceCatalogEntry } from "@evener/appwire-client/state/extensions";

export interface ExtensionsStoreState extends MarketplacesState, PluginsState, LaunchLayerState {
  // The filesystem three the sections' PathFields and directory pickers use;
  // the launch-config gateway answers all of them (see below).
  validatePath(path: string, kind: string): Promise<PathValidateResponse>;
  createDirectory(path: string): Promise<void>;
  completePaths(prefix: string, includeFiles: boolean): Promise<string[]>;
}

function requireClient(): AppwireClientLike {
  const client = connectionStore.getState().client;
  if (!client) {
    throw new Error("extensions store: no client connected; call useConnectionStore.getState().connect(client) first");
  }
  return client;
}

// The marketplaces, installed-plugins and global launch-layer stores proper
// live in the package; these are the app's one instance of each, over a
// client port that resolves connectionStore's CURRENT client at request time.
// They publish into extensionsStore (below the store) so the sections keep
// reading one store; the path helpers are still written here.

// onHubConfigNotification delivers the config notifications THIS hub's fetches
// are about. The controller's own arrive plainly. A host's own arrive wrapped
// in evener/host/notification tagged with the host that owns them
// (cmd/evener-hub/app_host_admin.go's relayHostNotifications, one fan-out per
// remote host, over the remoteHostConfigNotifications allowlist), and every
// fetch reached from here - evener/marketplace/list, evener/plugin/list,
// evener/launch/getLayer - reads this hub over the plain connection, so
// another host's change is no evidence about any of them and goes no further.
// A wrapper tagged with the controller itself is this hub's own change, and is
// delivered unwrapped: indistinguishable from the plain notification. The one
// thing a remote host's plugin change does move is pluginRevision (below).
function onHubConfigNotification(handler: (n: AnyNotification) => void): () => void {
  return onConnectionNotification((n) => {
    if (n.method !== "evener/host/notification") {
      handler(n);
      return;
    }
    if (!isLocalHost(n.params.host)) return;
    handler({ method: n.params.method, params: n.params.params } as AnyNotification);
  });
}

const hubClient = {
  request: (method, params, opts) => requireClient().request(method, params, opts),
  onNotification: onHubConfigNotification,
} satisfies MarketplacesClient & PluginsClient & LaunchLayerClient;
const marketplaces = createMarketplacesStore(hubClient);
marketplaces.start();
const plugins = createPluginsStore(hubClient);
plugins.start();
const launchLayer = createLaunchLayerStore(hubClient);
launchLayer.start();

// The hub broadcasts a change to every CONNECTED client, so a change made
// while this browser was disconnected reaches it as nothing at all: the
// notification each core follows cannot recover it, and the reconnect can.
// Each core re-reads its list when this connection is ready again - only if
// something has read it already, so a section the user never opened still
// sends nothing.
connectionStore.subscribe((s) => {
  marketplaces.connectionChanged(s.client, s.state);
  plugins.connectionChanged(s.client, s.state);
  launchLayer.connectionChanged(s.client, s.state);
});

export const extensionsStore = createStore<ExtensionsStoreState>(() => ({
  ...marketplaces.getState(),
  ...plugins.getState(),
  ...launchLayer.getState(),

  // The three filesystem RPCs are the launch-config gateway's
  // (stores/launchConfig.ts over the package's createLaunchConfigStore), named
  // once there for every surface that picks a path. They stay on this store's
  // state because the sections reach them through it.
  validatePath: (path, kind) => launchConfigStore.getState().validatePath(path, kind),
  createDirectory: (path) => launchConfigStore.getState().createDirectory(path),
  completePaths: (prefix, includeFiles) => launchConfigStore.getState().completePaths(prefix, includeFiles),
}));

// Each core's publish lands here synchronously, as the fields it changed: a
// whole-snapshot copy would make the core the owner of every one of its
// fields and overwrite a value written straight into this store (the section
// tests seed their fixtures that way).
function publishChangedFields<S extends Partial<ExtensionsStoreState>>(core: {
  subscribe(listener: (state: S, previous: S) => void): () => void;
}): void {
  core.subscribe((state, previous) => {
    const changed: Partial<ExtensionsStoreState> = {};
    for (const key of Object.keys(state) as (keyof S & keyof ExtensionsStoreState)[]) {
      if (state[key] !== previous[key]) Object.assign(changed, { [key]: state[key] });
    }
    extensionsStore.setState(changed);
  });
}
publishChangedFields(marketplaces);
publishChangedFields(plugins);
publishChangedFields(launchLayer);

export function useExtensionsStore(): ExtensionsStoreState;
export function useExtensionsStore<T>(selector: (state: ExtensionsStoreState) => T): T;
export function useExtensionsStore<T>(selector?: (state: ExtensionsStoreState) => T): T | ExtensionsStoreState {
  // Not a real conditional hook call - see stores/connection.ts's own
  // useConnectionStore for the full explanation.
  // biome-ignore lint/correctness/useHookAtTopLevel: same hook both arms, JS default param not a real conditional - see stores/connection.ts
  return selector ? useStore(extensionsStore, selector) : useStore(extensionsStore);
}

// A REMOTE host's plugin change moves pluginRevision, and nothing else here.
//
// pluginRevision is the revision the HOST-SCOPED plugin consumers key their
// requests on: usePluginPreview and useSpawnSlashCatalog build their logical
// key from it and then issue their own evener/plugin/preview /
// evener/spawn/slashCatalog against the SELECTED host through
// evener/host/request. Without the bump a plugin enabled or disabled on that
// host is never observed, so the spawn form keeps rendering the pre-change
// list - and a plugin reconciled from that stale preview is sent as a
// thread/start launchOverride the host no longer has. So the revision moves
// for a remote host's update exactly as it does for the controller's own
// (component 07b review, round three), which the plugins core moves from its
// own subscription.
//
// The core's list refetch is a different matter, and is why this is written
// here rather than delivered to the core: evener/plugin/list reads THIS hub
// over the plain connection, and a remote host's change is no evidence about
// this hub's plugins.
onConnectionNotification((n) => {
  if (n.method !== "evener/host/notification") return;
  if (n.params.method !== "evener/plugin/updated" || isLocalHost(n.params.host)) return;
  plugins.setState((state) => ({ pluginRevision: state.pluginRevision + 1 }));
});

// resetExtensionsStoreForTests resets the store and every core behind it to
// their initial state. extensions.ts is a singleton store shared by the whole app, so
// extensions.test.ts must reset it between tests to keep them isolated - no
// production code should ever call this (mirrors threads.ts/tree.ts's own
// reset*StoreForTests precedent).
export function resetExtensionsStoreForTests(): void {
  marketplaces.reset();
  plugins.reset();
  launchLayer.reset();
  // The cores' fields are written here as well as by their resets: the mirror
  // above forwards only what a core changed, so a value a test seeded
  // straight into this store, over a core already at its initial state,
  // would otherwise survive the reset.
  extensionsStore.setState({
    ...marketplaces.getInitialState(),
    ...plugins.getInitialState(),
    ...launchLayer.getInitialState(),
  });
}

/** Directory actions shared by settings fields; the widget stays wire-free. */
export const directoryActions = {
  validatePath: (path: string, kind: string) => extensionsStore.getState().validatePath(path, kind),
  createDirectory: (path: string) => extensionsStore.getState().createDirectory(path),
};
