// The Hub's first page (spec 12): the hub's status line, then one row per
// page. MORE pushes today's administration screens inside the sheet until
// their grouped pages land (#2539), and ABOUT names this app's version and
// offers the hub's update (ruling 22).
import type { AuthStatusResponse, HostRow, InstanceEntry } from "@evener/appwire-client";
import { useFocusEffect } from "@react-navigation/native";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { nativeApplicationVersion, nativeBuildVersion } from "expo-application";
import { useCallback, useSyncExternalStore } from "react";
import { Alert, Text } from "react-native";
import { useConnection } from "../ConnectionProvider";
import { useConnectionStatusText } from "../board/connectionStatus";
import { whenReady } from "../connectionDisplay";
import { useCredentialStore } from "../credentialStore";
import { scaledType, uiType } from "../design/tokens";
import { useDisplayChoices } from "../display/displayContext";
import { APPEARANCE_LABELS } from "../display/displayPreferences";
import { versionDriftTag } from "../hosts/hostStatus";
import { useOptionalSnapshot } from "../hosts/useHubFleet";
import { statusOf } from "../providers/providerStatus";
import { Group, GroupedPage, GroupFooter, Row, RowValue } from "../sheet/Grouped";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import { appVersionText, hubStatusLine } from "./hubHeader";
import { type HubRoutes, useHubSheet } from "./hubSheetContext";
import { useAuthStatuses } from "./useAuthStatuses";
import { useInstalledPluginCount } from "./useInstalledPluginCount";

export function HubHome({ navigation }: NativeStackScreenProps<HubRoutes, "HubHome">) {
	const { hubId, hubName, client, ready, canUseConnection, updates, hosts } = useHubSheet();
	const update = useSyncExternalStore(updates.subscribe, updates.getState);
	const { profiles } = useConnection();
	const { appearance } = useDisplayChoices();
	const { palette } = useColors();
	const scale = useTextScale();
	const line = hubStatusLine(ready, useConnectionStatusText(), update.check);
	const check = update.check;
	const updateWaiting = !!check?.applicable && check.updateAvailable && !update.restarting;
	const confirmUpdate = () =>
		Alert.alert(
			`Update ${hubName}?`,
			`Install evener ${check?.latestTag ?? "the latest release"} on ${hubName}. The hub restarts, and the app reconnects on its own.`,
			[
				{ text: "Cancel", style: "cancel" },
				// The alert can outlive the connection it opened on.
				{ text: "Update", onPress: whenReady(canUseConnection, () => void updates.apply()) },
			],
		);
	const updateProblem = update.applyError ?? update.checkError;
	// The home reads the hosts and the providers each time it shows; only the
	// Hosts pages poll, and the provider list follows the hub's own
	// notifications while the home is up.
	const credentials = useCredentialStore();
	useFocusEffect(
		useCallback(() => {
			void hosts?.read();
			// A store with no connection yet refuses the read; the next focus,
			// or the connection turning ready, reads again.
			if (ready && canUseConnection())
				credentials
					.getState()
					.fetch()
					.catch(() => {});
		}, [hosts, credentials, ready, canUseConnection]),
	);
	const fleet = fleetSummary(useOptionalSnapshot(hosts)?.rows ?? null, check?.currentVersion);
	const listing = useSyncExternalStore(credentials.subscribe, credentials.getState);
	const providers = providersSummary(
		listing.listingEstablished ? listing.instances : null,
		// Gated as the Providers page gates it, so a re-key window never reads
		// the previous hub's statuses.
		useAuthStatuses(canUseConnection() ? client : null),
	);
	// Gated on canUseConnection, so a re-key window never reads the previous
	// hub's list.
	const installedPlugins = useInstalledPluginCount(canUseConnection() ? client : null);
	return (
		<GroupedPage>
			<Text
				allowFontScaling={allowFontScaling}
				style={{
					color: palette.inkMid,
					// The status line role: 14pt as the prototype's (hub.js:17), on
					// the 20pt line of its Live summary (styles.css:285). 20pt in, 2
					// above and 6 below, as hub.js:17.
					...scaledType(uiType.statusLine, scale),
					paddingHorizontal: 20,
					paddingTop: 2,
					paddingBottom: 6,
				}}
			>
				{line}
			</Text>
			<Group label="Fleet">
				<Row
					icon="server.rack"
					label="Hosts"
					value={fleet ? <RowValue text={String(fleet.count)} tag={fleet.tag} /> : undefined}
					accessibilityLabel={fleet ? ["Hosts", fleet.count, fleet.tag?.text].filter(Boolean).join(", ") : "Hosts"}
					chevron
					onPress={() => navigation.navigate("Hosts", { hubId })}
				/>
			</Group>
			<Group label="Setup">
				<Row
					icon="key"
					label="Providers"
					value={providers ? <RowValue text={String(providers.count)} tag={providers.tag} /> : undefined}
					accessibilityLabel={
						providers
							? ["Providers", String(providers.count), providers.tag?.text].filter(Boolean).join(", ")
							: "Providers"
					}
					chevron
					onPress={() => navigation.navigate("Providers", { hubId })}
				/>
				<Row
					icon="puzzlepiece.extension"
					label="Plugins"
					value={installedPlugins ?? undefined}
					chevron
					onPress={() => navigation.navigate("Plugins", { hubId })}
				/>
			</Group>
			<Group label="This phone">
				<Row
					icon="textformat.size"
					label="Display"
					value={APPEARANCE_LABELS[appearance]}
					chevron
					onPress={() => navigation.navigate("Display", { hubId })}
				/>
				<Row
					icon="bubble.left"
					label="In-app alerts"
					chevron
					onPress={() => navigation.navigate("Alerts", { hubId })}
				/>
				<Row
					icon="point.3.connected.trianglepath.dotted"
					label="Hubs"
					value={profiles.length}
					chevron
					onPress={() => navigation.navigate("Hubs")}
				/>
			</Group>
			<Group label="More">
				<Row
					icon="keyboard"
					label="Keyboard shortcuts"
					chevron
					onPress={() => navigation.navigate("KeybindingPreferences", { hubId })}
				/>
				<Row
					icon="slider.horizontal.3"
					label="Launch defaults"
					chevron
					onPress={() => navigation.navigate("LaunchSettings", { hubId })}
				/>
				<Row
					icon="gearshape"
					label="Hub settings"
					chevron
					onPress={() => navigation.navigate("HubSettings", { hubId })}
				/>
			</Group>
			<Group label="About">
				<Row
					icon="info.circle"
					label="Evener for iPhone"
					value={appVersionText(nativeApplicationVersion, nativeBuildVersion)}
				/>
				{updateWaiting ? (
					<Row
						label={update.applying ? "Updating…" : "Update hub"}
						accessibilityLabel="Update hub"
						tone="accent"
						disabled={!ready || update.applying}
						onPress={confirmUpdate}
					/>
				) : null}
			</Group>
			{update.restarting ? (
				<GroupFooter>{`Restarting into ${check?.latestTag ?? "the new release"}…`}</GroupFooter>
			) : null}
			{update.restartTimedOut ? (
				<GroupFooter tone="danger">
					{/* With no answer since the restart, the app can't say which version the hub runs. */}
					{check
						? "The hub restarted without the update. Check its logs."
						: "Couldn't confirm the update. Check the hub's version."}
				</GroupFooter>
			) : null}
			{updateProblem ? <GroupFooter tone="danger">{updateProblem}</GroupFooter> : null}
		</GroupedPage>
	);
}

/** The Hosts row's value: every machine, the hub's own included, tagged amber
 * with how many are offline, or else gray with how many run another version. */
function fleetSummary(rows: readonly HostRow[] | null, hubVersion: string | undefined) {
	if (!rows) return null;
	const offline = rows.filter((row) => !row.attached).length;
	const drifting = rows.filter((row) => versionDriftTag(row, hubVersion)).length;
	const tag =
		offline > 0
			? { text: `${offline} offline`, tone: "amber" as const }
			: drifting > 0
				? { text: `${drifting} on another version`, tone: "gray" as const }
				: null;
	return { count: rows.length + 1, tag };
}

/** The Providers row's value: every instance, tagged amber with how many
 * need a sign-in (only "Sign-in expired" does, ruling 6). */
function providersSummary(
	instances: readonly InstanceEntry[] | null,
	auth: ReadonlyMap<string, AuthStatusResponse> | null,
) {
	if (!instances) return null;
	// No tag until the statuses are read: only they say a sign-in expired.
	const toSignIn = instances.filter((instance) => statusOf(instance, auth)?.tone === "attention").length;
	return {
		count: instances.length,
		tag: toSignIn > 0 ? { text: `${toSignIn} to sign in`, tone: "amber" as const } : null,
	};
}
