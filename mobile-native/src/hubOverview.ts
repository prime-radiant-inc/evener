import {
  createHubOverviewStore,
  type HubOverviewClient,
  type HubOverviewStore,
} from "@evener/appwire-client";

/** The hub settings screen renders the store's `error` verbatim, so a failed
 * read surfaces this copy rather than the transport's own message. */
const HUB_OVERVIEW_REFRESH_FAILED =
  "Could not refresh hub information. Try again when connected.";

/** The package's hub overview store with the native error copy; one per screen, disposed on unmount. */
export function createNativeHubOverview(
  client: HubOverviewClient,
): HubOverviewStore {
  return createHubOverviewStore(client, {
    describeError: () => HUB_OVERVIEW_REFRESH_FAILED,
  });
}
