import type { AppwireClient, ConnectionState } from "@evener/appwire-client";
import type { HubProfile } from "./connection";
import { useConnection } from "./ConnectionProvider";
import {
	type ConnectionDisplay,
	type LiveReadiness,
	useConnectionDisplay,
	useLiveReadiness,
	useRenderClient,
} from "./connectionDisplay";

/** What a retained ready-only screen says when the route names a hub the
 * active profile has moved off: the data behind the screen belongs to a hub
 * the connection no longer reports, so the screen offers the way back
 * instead of that data. */
export const HUB_NO_LONGER_SELECTED = "This hub is no longer selected. Choose it again in Hubs.";

/** The connection wiring every retained ready-only screen runs at the top of
 * its body - the block PluginsPage, HubSettingsScreen and ProvidersPage
 * each carried in their own copy (#1942): the active connection, the display
 * the connection yields for the screen, the live readiness a deferred write
 * or a re-read checks against, and the client a fresh attempt's dialing gap
 * keeps the previous content rendered through.
 *
 * Every value is scoped to a hub, and the two scopes are deliberately
 * different. The display's banner history and the retained client follow the
 * ACTIVE profile's hub - useConnectionDisplay's and useRenderClient's own
 * docs - because a screen the route re-keys to another hub must inherit
 * neither (the screens' `activeProfile?.id !== route.params.hubId` early
 * return hides the window where the two disagree, and the screens' keyed
 * wrappers remount this wiring whole when the route's hub changes), while
 * readiness answers for the hub the ROUTE names, so a deferred confirmation
 * stays tied to the screen's own route even if the profile moves underneath
 * it.
 *
 * A mounted screen re-keyed to another hub is a fresh screen: the retention
 * below belongs to the hub it was built for, and none of it may survive a hub
 * the route now names. React Navigation can update a mounted instance's
 * params (setParams on a focused screen is this app's own idiom - see
 * KeybindingPreferencesScreen), so each screen keys its body to the hub id
 * and a re-key remounts it whole.
 *
 * A screen whose store rebinds itself across a flap (useCredentialStore,
 * credentialStore.ts) needs no retained client: it never reads
 * `renderClient`, and its wall waits on the display alone (hub/ProvidersPage).
 * A screen that renders through a client keeps the previous one across a
 * fresh attempt's gap - never the not-yet-ready replacement, which the
 * connection layer reports while it is still dialing - rather than dropping
 * to the wall for a moment the banner covers just as well as a passive
 * reconnect does. See useRenderClient's own doc for that adoption contract,
 * including why it is scoped to the active hub. */
export interface RetainedScreenConnection {
	/** The profile the connection reports for - never the route's own claim. */
	activeProfile: HubProfile | null;
	/** The live client the connection layer holds, null while a retry dials. */
	client: AppwireClient | null;
	/** The connection's current state. */
	state: ConnectionState;
	/** The connection's own failure copy - on a fatal close, the
	 * compatibility message that says why the wall replaced the screen's
	 * content (see `fatal`). */
	error: string | null;
	/** What the screen shows for the connection: "wall" before anything has
	 * been shown or after a fatal close, "banner" over the mounted screen
	 * for a flap it survives, "none" while ready. */
	display: ConnectionDisplay;
	/** Whether an affordance that issues a request may run right now, held
	 * live for the hub the route names. */
	canUseConnection: LiveReadiness;
	/** The client a ready-only child renders through: the live one while
	 * ready, the last adopted one through a retry's dialing gap. */
	renderClient: AppwireClient | null;
}

/** Runs the retained ready-only screen wiring for the hub `routeHubId` the
 * route names; RetainedScreenConnection carries the contract. */
export function useRetainedScreenConnection(routeHubId: string): RetainedScreenConnection {
	const { activeProfile, client, state, fatal, error } = useConnection();
	const display = useConnectionDisplay(activeProfile?.id, state, fatal, client);
	const canUseConnection = useLiveReadiness(routeHubId, client, state);
	const renderClient = useRenderClient(client, state, activeProfile?.id);
	return {
		activeProfile,
		client,
		state,
		error,
		display,
		canUseConnection,
		renderClient,
	};
}
