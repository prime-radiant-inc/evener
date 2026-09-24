// extensions.ts is the thin wire-truth store behind the Extensions settings
// cluster (Marketplaces & Plugins, Plugins/Skills directories, MCP servers -
// panes/settings/sections/{marketplacesPlugins,dirListSetting,pluginsDirs,
// skillsDirs,mcp}). It rides the single AppwireClientLike connection.ts wires
// via connectionStore.getState().connect(client), same as threads.ts/
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

import type { AnyNotification, PathValidateResponse } from "@evener/appwire-client";
import {
  createLaunchLayerStore,
  createMarketplacesStore,
  createPluginsStore,
  type LaunchLayerClient,
  type LaunchLayerState,
  type LaunchLayerStore,
  type MarketplacesClient,
  type MarketplacesState,
  type MarketplacesStore,
  type PluginsClient,
  type PluginsState,
  type PluginsStore,
} from "@evener/appwire-client/state/extensions";
import { useStore } from "zustand";
import { createStore, type StoreApi } from "zustand/vanilla";
import {
  type ConnectionStoreState,
  connectedClientPort,
  connectionStore,
  onConnectionNotification,
} from "./connection";
import { isLocalHost } from "./hostRouting";
import { remoteHostStoreClient } from "./hostStoreClient";
import { currentHostRegistration, type HostRegistration, hostRegistrationChanged } from "./hosts";
import { launchConfigStore, launchConfigStoreForHost } from "./launchConfig";

export type { MarketplaceCatalogEntry } from "@evener/appwire-client/state/extensions";

export interface ExtensionsStoreState extends MarketplacesState, PluginsState, LaunchLayerState {
  // The filesystem three the sections' PathFields and directory pickers use;
  // the launch-config gateway answers all of them (see below).
  validatePath(path: string, kind: string): Promise<PathValidateResponse>;
  createDirectory(path: string): Promise<void>;
  completePaths(prefix: string, includeFiles: boolean): Promise<string[]>;
}

// The shared port's `request` forwards to connectionStore's CURRENT client on
// every call; only the notification side is extensions-specific (see below).
const { request } = connectedClientPort("extensions");

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

/** The filesystem three the sections' PathFields and directory pickers use; on
 * a merged store they are the launch-config gateway's own methods. */
type ExtensionsPathActions = Pick<ExtensionsStoreState, "validatePath" | "createDirectory" | "completePaths">;

/** ExtensionsInstance is one hub's three extensions cores plus the merged store
 * the sections read. A remote host gets its own instance (extensionsInstanceForHost
 * below), so every cache and revision it holds is that host's own. */
interface ExtensionsInstance {
  marketplaces: MarketplacesStore;
  plugins: PluginsStore;
  launchLayer: LaunchLayerStore;
  store: StoreApi<ExtensionsStoreState>;
}

// Each core's publish lands here synchronously, as the fields it changed: a
// whole-snapshot copy would make the core the owner of every one of its
// fields and overwrite a value written straight into this store (the section
// tests seed their fixtures that way).
function publishChangedFields<S extends Partial<ExtensionsStoreState>>(
  target: StoreApi<ExtensionsStoreState>,
  core: { subscribe(listener: (state: S, previous: S) => void): () => void },
): void {
  core.subscribe((state, previous) => {
    const changed: Partial<ExtensionsStoreState> = {};
    for (const key of Object.keys(state) as (keyof S & keyof ExtensionsStoreState)[]) {
      if (state[key] !== previous[key]) Object.assign(changed, { [key]: state[key] });
    }
    target.setState(changed);
  });
}

/** buildExtensionsInstance wires the three cores to `client` and publishes them
 * into one merged store over `paths` (the launch-config gateway's filesystem
 * three: the controller's own store for the local hub, a per-host instance for a
 * remote one). */
function buildExtensionsInstance(
  client: MarketplacesClient & PluginsClient & LaunchLayerClient,
  paths: ExtensionsPathActions,
): ExtensionsInstance {
  const marketplaces = createMarketplacesStore(client);
  marketplaces.start();
  const plugins = createPluginsStore(client);
  plugins.start();
  const launchLayer = createLaunchLayerStore(client);
  launchLayer.start();

  const store = createStore<ExtensionsStoreState>(() => ({
    ...marketplaces.getState(),
    ...plugins.getState(),
    ...launchLayer.getState(),

    // The three filesystem RPCs are the launch-config gateway's
    // (stores/launchConfig.ts over the package's createLaunchConfigStore), named
    // once there for every surface that picks a path. They stay on this store's
    // state because the sections reach them through it.
    validatePath: (path, kind) => paths.validatePath(path, kind),
    createDirectory: (path) => paths.createDirectory(path),
    completePaths: (prefix, includeFiles) => paths.completePaths(prefix, includeFiles),
  }));
  publishChangedFields(store, marketplaces);
  publishChangedFields(store, plugins);
  publishChangedFields(store, launchLayer);
  return { marketplaces, plugins, launchLayer, store };
}

const hubClient = {
  request,
  onNotification: onHubConfigNotification,
} satisfies MarketplacesClient & PluginsClient & LaunchLayerClient;

// The controller's own three cores, over the plain port, published into the one
// merged store today's sections read. The local hub is byte-for-byte the store
// that existed before host scoping; a remote host gets its own instance below.
const localInstance = buildExtensionsInstance(hubClient, launchConfigStore.getState());
export const extensionsStore = localInstance.store;

/** goneInstance is what a lookup for a host the registry does not list returns:
 * ONE shared instance whose three cores run over a port that refuses every call
 * and subscribes to nothing. Nothing kept for the previous registration, and
 * nothing built per lookup (each core's `start()` wires a subscription, so an
 * instance per call would wire three of them for a host that does not exist). */
const goneClient = {
  request: async () => {
    throw new Error("this host is not registered: the registry lists no such host, so it has no extensions store");
  },
  onNotification: () => () => {},
} satisfies MarketplacesClient & PluginsClient & LaunchLayerClient;
const gonePaths: ExtensionsPathActions = {
  validatePath: async () => {
    throw new Error("this host is not registered: the registry lists no such host, so it has no paths");
  },
  createDirectory: async () => {
    throw new Error("this host is not registered: the registry lists no such host, so it has no paths");
  },
  completePaths: async () => {
    throw new Error("this host is not registered: the registry lists no such host, so it has no paths");
  },
};
const goneInstance = buildExtensionsInstance(goneClient, gonePaths);

// The hub broadcasts a change to every CONNECTED client, so a change made
// while this browser was disconnected reaches it as nothing at all: the
// notification each core follows cannot recover it, and the reconnect can.
// Each core re-reads its list when this connection is ready again - only if
// something has read it already, so a section the user never opened still
// sends nothing.
function syncConnection(state: Pick<ConnectionStoreState, "client" | "state">): void {
  localInstance.marketplaces.connectionChanged(state.client, state.state);
  localInstance.plugins.connectionChanged(state.client, state.state);
  localInstance.launchLayer.connectionChanged(state.client, state.state);
}
connectionStore.subscribe(syncConnection);

// --- Host-scoped instances (component 07b) ----------------------------------
//
// A REMOTE host's marketplaces, plugins and global launch layer are that host's
// own, and each core caches server-side data (the browse catalogs, the list
// revisions, the layer). One instance with a per-call routing port would serve
// host A's cached catalog for host B, so each remote host gets its own instance
// over a client that forwards through evener/host/request
// (stores/hostStoreClient.ts). The controller's own singleton above is
// untouched: the local hub stays byte-for-byte today's store.

interface ExtensionsHostEntry {
  instance: ExtensionsInstance;
  /** What the registry said REGISTERED this host when the instance was built
   * (stores/hosts.ts's HostRegistration). The instance is current only while
   * the registry still gives the same answer, so a host removed and re-added
   * under the same name gets a fresh instance rather than the previous
   * registration's cached catalogs and revisions - and a response the old
   * registration had in flight lands in the instance it was issued for. */
  registration: HostRegistration;
}

const hostInstances = new Map<string, ExtensionsHostEntry>();

/** syncInstance tells one instance's three cores which connection is current.
 * A repeat of the connection it already has is a no-op (their own
 * connectionChanged says so), so this is safe to call on every transition. */
function syncInstance(instance: ExtensionsInstance, state: Pick<ConnectionStoreState, "client" | "state">): void {
  instance.marketplaces.connectionChanged(state.client, state.state);
  instance.plugins.connectionChanged(state.client, state.state);
  instance.launchLayer.connectionChanged(state.client, state.state);
}

function syncHostInstances(state: Pick<ConnectionStoreState, "client" | "state">): void {
  for (const entry of hostInstances.values()) syncInstance(entry.instance, state);
  // The shared gone instance is not an entry - a host the registry does not list
  // keeps nothing - but its cores still have to see the connection: without it
  // they hold "no connection" and a call on them would return before reaching
  // the refusing port at all.
  syncInstance(goneInstance, state);
}
connectionStore.subscribe(syncHostInstances);

/** disposeHostInstance ends a per-host instance's three cores. Their
 * `start()` wired live notification subscriptions, so an instance no consumer
 * can reach any more - a re-registration replaced it, or a test ended - must be
 * disposed rather than dropped. `dispose()` is terminal. */
function disposeHostInstance(instance: ExtensionsInstance): void {
  instance.marketplaces.dispose();
  instance.plugins.dispose();
  instance.launchLayer.dispose();
}

/** extensionsInstanceForHost returns the extensions instance for `host`: the
 * controller's own instance for the local hub (and for an absent host), and a
 * per-host instance for a remote one. Nothing here falls back to the
 * controller's data: a remote host's sections read only its own instance. */
export function extensionsInstanceForHost(host: string | null | undefined): ExtensionsInstance {
  if (isLocalHost(host)) return localInstance;
  const name = host as string;
  const registration = currentHostRegistration(name);
  const recorded = hostInstances.get(name);
  // The registry does not list this host: nothing is kept for it. A
  // null-registration placeholder would have held a live instance - three cores
  // with three live subscriptions, each willing to read that host's catalogs -
  // behind a host that is not registered; instead the previous registration's
  // cores are disposed and the entry goes (so a re-add builds a fresh one).
  if (registration === null) {
    if (recorded !== undefined) {
      disposeHostInstance(recorded.instance);
      hostInstances.delete(name);
    }
    return goneInstance;
  }
  if (recorded !== undefined && !hostRegistrationChanged(recorded.registration, registration)) {
    // The first answer after an instance was built before the registry had one
    // identifies it from here on; every other unchanged answer is the same one.
    if (registration !== undefined) recorded.registration = registration;
    return recorded.instance;
  }
  // A re-registration: the instance this replaces belongs to a registration the
  // registry no longer names, so its cores are unwired before it is dropped -
  // otherwise its subscriptions would keep refetching for a host no section can
  // reach.
  if (recorded !== undefined) disposeHostInstance(recorded.instance);
  const instance = buildExtensionsInstance(remoteHostStoreClient(name), launchConfigStoreForHost(name).getState());
  hostInstances.set(name, { instance, registration });
  // The connection that already exists: this module is loaded after the client
  // may be ready, so a fresh instance reads it rather than waiting for the NEXT
  // transition (mirrors syncConnection's own initial pass). A no-op for the
  // instances already there - connectionChanged ignores a repeat of the
  // connection it already has.
  syncHostInstances(connectionStore.getState());
  return instance;
}

/** extensionsStoreForHost returns the merged store the settings sections read
 * for `host`. */
export function extensionsStoreForHost(host: string | null | undefined): StoreApi<ExtensionsStoreState> {
  return extensionsInstanceForHost(host).store;
}

/** useExtensionsStoreForHost reads the merged extensions store for `host`: the
 * controller's own store for the local hub, a per-host instance for a remote
 * one. This is how the host-scoped sections read (component 07b); the plain
 * useExtensionsStore above keeps reading the controller's, so a surface that is
 * not host-scoped is unchanged. */
export function useExtensionsStoreForHost<T>(
  host: string | null | undefined,
  selector: (state: ExtensionsStoreState) => T,
): T {
  return useStore(extensionsStoreForHost(host), selector);
}

// And the connection that already exists. This module is lazily loaded, so
// initializing after the client is ready is the common case: without this pass
// the cores' first sight of the connection is the NEXT transition, which they
// read as a first connection - invalidating nothing and moving no revision,
// exactly when a disconnection has just hidden changes from them. Same shape
// as stores/credentials.ts's own initial pass.
//
// Last in the module, after the store and its mirrors exist: the pass can
// reach a core's state (a recovery read for a list something has already read,
// which cannot be true at first load but is one refactor away from being), and
// whatever a core publishes has to have somewhere to land.
syncConnection(connectionStore.getState());

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
// for a remote host's update exactly as it does for the controller's own, which
// the plugins core moves from its own subscription.
//
// The core's list refetch is a different matter, and is why this is written
// here rather than delivered to the core: evener/plugin/list reads THIS hub
// over the plain connection, and a remote host's change is no evidence about
// this hub's plugins.
onConnectionNotification((n) => {
  if (n.method !== "evener/host/notification") return;
  if (n.params.method !== "evener/plugin/updated" || isLocalHost(n.params.host)) return;
  localInstance.plugins.setState((state) => ({ pluginRevision: state.pluginRevision + 1 }));
});

// resetExtensionsStoreForTests resets the local store and every core behind it
// to their reset state, and disposes every per-host instance so a test's remote
// hosts do not leak into the next test. Core fields explicitly retained across
// reset, such as the marketplace publication version, survive here too.
// extensions.ts is a singleton store shared by the whole app, so
// extensions.test.ts must reset it between tests to keep them isolated - no
// production code should ever call this (mirrors threads.ts/tree.ts's own
// reset*StoreForTests precedent).
export function resetExtensionsStoreForTests(): void {
  // A per-host instance's cores hold live notification subscriptions (each
  // core.start() wires the port), so a reset disposes them before forgetting
  // them: a leaked subscription would refetch for a host no section can reach.
  // dispose() is terminal, so the instance is dropped rather than reused and
  // extensionsInstanceForHost builds a fresh one.
  for (const entry of hostInstances.values()) disposeHostInstance(entry.instance);
  hostInstances.clear();

  localInstance.marketplaces.reset();
  localInstance.plugins.reset();
  localInstance.launchLayer.reset();
  // The cores' fields are written here as well as by their resets: the mirror
  // above forwards only what a core changed, so a value a test seeded
  // straight into this store, over a core already at its initial state,
  // would otherwise survive the reset.
  extensionsStore.setState({
    ...localInstance.marketplaces.getInitialState(),
    marketplacesPublicationVersion: localInstance.marketplaces.getState().marketplacesPublicationVersion,
    ...localInstance.plugins.getInitialState(),
    ...localInstance.launchLayer.getInitialState(),
  });
  // A fresh module load reads the connection that already exists; a reset puts
  // this singleton back the way that load leaves it, so it reads it too.
  syncConnection(connectionStore.getState());
}

/** Directory actions shared by settings fields; the widget stays wire-free. */
export const directoryActions = {
  validatePath: (path: string, kind: string) => extensionsStore.getState().validatePath(path, kind),
  createDirectory: (path: string) => extensionsStore.getState().createDirectory(path),
};
