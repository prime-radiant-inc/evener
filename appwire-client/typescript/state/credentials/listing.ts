// The listing half of the credential instances state: normalizing an
// instance/list answer and comparing listings across store transitions. Pure
// functions over the listing fields.

import type { InstanceListResponse } from "../../types.gen";
import type { CredentialInstancesState } from "./instances";

/** The listing half of the state: InstanceListResponse with every optional
 * field present. */
const LISTING_FIELDS = ["instances", "availableProviders", "diagnostics", "userLayer", "writesRefused"] as const;
export type CredentialListing = Pick<CredentialInstancesState, (typeof LISTING_FIELDS)[number]>;

// listState normalizes one instance/list answer into the store's own always-
// present shape. Every reader of the listing goes through it, so a field
// added to InstanceListResponse is defaulted in exactly one place.
export function listState(resp: InstanceListResponse): CredentialListing {
  return {
    instances: resp.instances,
    availableProviders: resp.availableProviders,
    diagnostics: resp.diagnostics ?? [],
    userLayer: resp.userLayer ?? "",
    writesRefused: resp.writesRefused ?? false,
  };
}

/** listingOf picks the listing out of a store snapshot, for a consumer that
 * hands the rows on as one value rather than selecting fields. */
export function listingOf(state: CredentialListing): CredentialListing {
  // Spelled out rather than looped: CredentialListing derives from
  // LISTING_FIELDS, so a field added there fails to compile here.
  const { instances, availableProviders, diagnostics, userLayer, writesRefused } = state;
  return { instances, availableProviders, diagnostics, userLayer, writesRefused };
}

/** listingChanged reports whether a store transition replaced the listing:
 * every applied read or write installs fresh rows, while a loading or error
 * patch leaves the same arrays in place. */
export function listingChanged(state: CredentialListing, previous: CredentialListing): boolean {
  return LISTING_FIELDS.some((field) => state[field] !== previous[field]);
}

type ListingTransition = CredentialListing & Pick<CredentialInstancesState, "selfRefresh" | "loading" | "error">;

/** foreignListingChange reports whether a store transition is a change a
 * credential probe or a guided flow must not be trusted against: the rows
 * moved, or a read began or failed, and the transition is NOT the store's own
 * self-marked refresh (selfRefresh moves only on those). A host that shows a
 * probe result, or steers a flow on a listing, invalidates on exactly this. */
export function foreignListingChange(
  state: ListingTransition,
  previous: ListingTransition,
  moved: boolean = listingChanged(state, previous),
): boolean {
  return (
    state.selfRefresh === previous.selfRefresh &&
    (moved || state.loading !== previous.loading || state.error !== previous.error)
  );
}
