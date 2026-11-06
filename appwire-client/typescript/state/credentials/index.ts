// The credentials state layer, published at
// `@evener/appwire-client/state/credentials`: the framework-free listing core
// both apps' Providers & credentials surfaces build their store on. The
// credential mutations and sign-in sequencing join it here as they leave the
// apps' own stores.
//
// Every re-export is explicit, never `export *`, like the navigation barrel:
// the public surface is the names listed here.

export { REFETCH_DEBOUNCE_MS } from "../refetchDebounce";
export type { AuthStatusesClient, AuthStatusesState, AuthStatusesStore } from "./authStatuses";
export { createAuthStatusesStore } from "./authStatuses";
export type {
  CredentialInstancesClient,
  CredentialInstancesDeps,
  CredentialInstancesState,
  CredentialInstancesStore,
  EditSupersededReconcile,
} from "./instances";
export {
  createCredentialInstancesStore,
  isStaleListingRefusal,
  StaleListingRefusal,
  staleListingHeld,
} from "./instances";
export type { CredentialListing } from "./listing";
export { foreignListingChange, listingChanged, listingOf } from "./listing";
