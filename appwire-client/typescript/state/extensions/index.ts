// The extensions state layer, published at
// `@evener/appwire-client/state/extensions`: the framework-free stores behind
// both apps' plugin marketplace and installed-plugin settings.
// Each is a factory taking a narrow client port; the apps build the instances
// they wire to their view layers.
//
// Every re-export is explicit, never `export *`, like the navigation barrel:
// the public surface is the names listed here.

export { REFETCH_DEBOUNCE_MS } from "../refetchDebounce";
export type { GatedMutationOutcome, HubWriteGate } from "./hubWriteGate";
export {
  createHubWriteGate,
  HUB_WRITE_BUSY,
  HubWriteBusyError,
  isHubWriteBusy,
  runGatedMutation,
} from "./hubWriteGate";
export type { LaunchLayerClient, LaunchLayerState, LaunchLayerStore } from "./launchLayer";
export { createLaunchLayerStore } from "./launchLayer";
export type {
  MarketplaceCatalogEntry,
  MarketplaceRemovalOutcome,
  MarketplacesClient,
  MarketplacesState,
  MarketplacesStore,
} from "./marketplaces";
export { createMarketplacesStore, marketplaceRemovalOutcome } from "./marketplaces";
export type { PluginsClient, PluginsState, PluginsStore } from "./plugins";
export { createPluginsStore, PLUGIN_UPDATE_CHECK_TIMEOUT_MS } from "./plugins";
