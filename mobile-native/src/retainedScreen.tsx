import { View } from "react-native";
import type { AppwireClient, ConnectionState } from "@evener/appwire-client";
import type { HubProfile } from "./connection";
import { useConnection } from "./ConnectionProvider";
import { ConnectionStatus } from "./ConnectionStatus";
import {
	type ConnectionDisplay,
	type LiveReadiness,
	useConnectionDisplay,
	useLiveReadiness,
	useRenderClient,
} from "./connectionDisplay";
import { Action, Copy, ErrorMessage } from "./ui";

/** What a retained ready-only screen says when the route names a hub the
 * active profile has moved off: the data behind the screen belongs to a hub
 * the connection no longer reports, so the screen offers the way back
 * instead of that data. */
export const HUB_NO_LONGER_SELECTED =
	"This hub is no longer selected. Return to Hubs to reconnect.";

/** The connection wiring every retained ready-only screen runs at the top of
 * its body - the block PluginsScreen, HubSettingsScreen and ProvidersScreen
 * each carried in their own copy (#1942): the active connection and its
 * manual retry, the display the connection yields for the screen, the live
 * readiness a deferred write or manual refresh checks against, and the client
 * a manual retry's dialing gap keeps the previous content rendered through.
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
 * `renderClient`, and its wall waits on the display alone (ProvidersScreen).
 * A screen that renders through a client keeps the previous one across a
 * manual retry's gap - never the not-yet-ready replacement, which the
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
	/** The connection's manual retry: what the wall's Reconnect action fires. */
	retry(): void;
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
export function useRetainedScreenConnection(
	routeHubId: string,
): RetainedScreenConnection {
	const { activeProfile, client, state, fatal, error, retry } = useConnection();
	const display = useConnectionDisplay(activeProfile?.id, state, fatal, client);
	const canUseConnection = useLiveReadiness(routeHubId, client, state);
	const renderClient = useRenderClient(client, state, activeProfile?.id);
	return {
		activeProfile,
		client,
		state,
		retry,
		error,
		display,
		canUseConnection,
		renderClient,
	};
}

/** The full-screen replacement a retained ready-only screen shows instead of
 * its content: the display is a wall - nothing has ever been shown, or a
 * close no retry can clear has taken the connection - and the screen offers
 * the way back. `purpose` is the screen's own phrase for what it shows once
 * connected ("manage plugins", "view hub settings"), and the action is the
 * connection's own manual retry. The connection's own error copy renders
 * beside it: on a fatal close (a protocol mismatch) that copy is the
 * compatibility message, and without it the wall offers a reconnect that
 * reads as ineffective - the reason a retry cannot clear the close yet is
 * the one thing the wall did not say. */
export function ConnectionWall({
	hubName,
	purpose,
	error,
	onReconnect,
}: {
	hubName: string;
	/** What the screen offers once connected, phrased to follow "Connect to
	 * <hub> to ". */
	purpose: string;
	/** The connection's own failure copy (useConnection's error): rendered
	 * here so a fatal wall says why the screen's content is gone. */
	error?: string | null;
	onReconnect(): void;
}) {
	return (
		<View style={{ padding: 20 }}>
			<Copy>{`Connect to ${hubName} to ${purpose}.`}</Copy>
			<Action onPress={onReconnect}>Reconnect</Action>
			<ErrorMessage message={error ?? null} />
		</View>
	);
}

/** The connection status a screen's open modal shows in place of the banner
 * it covers: a native modal hides the screen behind it, so while one is open
 * the status and the manual reconnect have to live inside it, or the user
 * would have to dismiss the modal to reach them. Renders nothing while the
 * connection is ready, exactly as the banner behind the modal would. */
export function ModalConnectionStatus({
	connectionState,
}: {
	connectionState: ConnectionState;
}) {
	return connectionState !== "ready" ? <ConnectionStatus /> : null;
}
