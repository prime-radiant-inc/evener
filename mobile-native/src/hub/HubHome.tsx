// The Hub's first page (spec 12): the hub's status line, then one row per
// page. MORE pushes today's administration screens inside the sheet until
// their grouped pages land (#2539), and ABOUT names this app's version and
// offers the hub's update (ruling 22).
import type { HostRow } from "@evener/appwire-client";
import { useFocusEffect } from "@react-navigation/native";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { nativeApplicationVersion, nativeBuildVersion } from "expo-application";
import { useCallback, useEffect, useSyncExternalStore } from "react";
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
import { Group, GroupedPage, GroupFooter, Row, RowValue } from "../sheet/Grouped";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import { appVersionText, hubConnectionState, hubStatusLine } from "./hubHeader";
import { type HubRoutes, useHubSheet } from "./hubSheetContext";
import { useInstalledPluginCount } from "./useInstalledPluginCount";

export function HubHome({ navigation }: NativeStackScreenProps<HubRoutes, "HubHome">) {
	const { hubId, hubName, client, ready, canUseConnection, updates, hosts } = useHubSheet();
	const update = useSyncExternalStore(updates.subscribe, updates.getState);
	const { profiles } = useConnection();
	const { appearance } = useDisplayChoices();
	const { palette } = useColors();
	const scale = useTextScale();
	const connectionLine = useConnectionStatusText();
	const line = hubStatusLine(ready, connectionLine, update.check);
	const check = update.check;
	const legacyUpdate =
		!!check &&
		(check as { restartSupported?: boolean }).restartSupported === undefined &&
		!!check.applicable &&
		!!check.updateAvailable;
	const installWaiting =
		!legacyUpdate && !!check?.applicable && (check.installAvailable ?? check.updateAvailable) && !update.restarting;
	const restartAvailable = check?.restartSupported === true;
	const confirmInstall = () =>
		Alert.alert(
			`Install an update on ${hubName}?`,
			`Download and install evener ${check?.latestTag ?? "the latest release"} on ${hubName}. The running hub is not restarted; use Restart hub when you are ready.`,
			[
				{ text: "Cancel", style: "cancel" },
				// The alert can outlive the connection it opened on.
				{ text: "Install", onPress: whenReady(canUseConnection, () => void updates.install()) },
			],
		);
	const confirmRestart = () =>
		Alert.alert(
			`Restart ${hubName}?`,
			`Restart into ${check?.onDiskCommit ?? "the binary on disk"} (running ${check?.currentVersion ?? "the current build"}). The app reconnects on its own; running sessions keep going.`,
			[
				{ text: "Cancel", style: "cancel" },
				{ text: "Restart", onPress: whenReady(canUseConnection, () => void updates.restart()) },
			],
		);
	const confirmLegacyUpdate = () =>
		Alert.alert(
			`Update ${hubName}?`,
			`Install evener ${check?.latestTag ?? "the latest release"} on ${hubName}. The hub restarts, and the app reconnects on its own.`,
			[
				{ text: "Cancel", style: "cancel" },
				{ text: "Update", onPress: whenReady(canUseConnection, () => void updates.apply()) },
			],
		);
	const updateProblem =
		update.installError ?? update.restartError ?? update.applyError ?? update.checkError ?? check?.checkError;
	useEffect(() => {
		if (update.installReconcile && ready) void updates.runCheck();
	}, [update.installReconcile, ready, updates]);
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
	// The number of providers and nothing more: which need a sign-in or have
	// an error is the Providers page's to say, per row (Jesse, 2026-09-30).
	const providerCount = listing.listingEstablished ? listing.instances.length : null;
	// Gated on canUseConnection, so a re-key window never reads the previous
	// hub's list.
	const installedPlugins = useInstalledPluginCount(canUseConnection() ? client : null);
	return (
		<GroupedPage>
			<Text
				allowFontScaling={allowFontScaling}
				testID={`hub-connection-${hubConnectionState(ready, connectionLine)}`}
				style={{
					color: palette.inkMid,
					...scaledType(uiType.statusLine, scale),
					// 20pt in, 2 above and 6 below, as the prototype's (hub.js:17).
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
					value={providerCount ?? undefined}
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
					label="Web keyboard shortcuts"
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
				{installWaiting ? (
					<Row
						label={update.installing ? "Installing…" : "Install update"}
						accessibilityLabel="Install update"
						tone="accent"
						disabled={!ready || update.installing || update.restarting}
						onPress={confirmInstall}
					/>
				) : null}
				{legacyUpdate ? (
					<Row
						label={update.applying ? "Updating…" : "Update and restart"}
						accessibilityLabel="Update and restart"
						tone="accent"
						disabled={!ready || update.applying}
						onPress={confirmLegacyUpdate}
					/>
				) : null}
				{restartAvailable ? (
					<Row
						label={update.restarting ? "Restarting…" : "Restart hub"}
						accessibilityLabel="Restart hub"
						tone="accent"
						disabled={!ready || update.restarting || update.installing}
						onPress={confirmRestart}
					/>
				) : null}
			</Group>
			{check?.onDiskCommit ? (
				<GroupFooter testID="hub-update-on-disk">{`On disk ${check.onDiskCommit}`}</GroupFooter>
			) : null}
			{check?.onDiskError ? (
				<GroupFooter tone="danger" testID="hub-update-on-disk-error">
					{`Can't read the on-disk build: ${check.onDiskError}`}
				</GroupFooter>
			) : null}
			{check?.applicable === true && check.installTargetMatchesLaunch === false ? (
				<GroupFooter tone="danger" testID="hub-update-install-target">
					{`Install is unavailable: it would write ${check.installTarget ?? "another binary"}. Use the managed installation.`}
				</GroupFooter>
			) : null}
			{check?.restartPending ? (
				<GroupFooter testID="hub-update-pending">
					{`Installed ${check.onDiskCommit ?? "a new build"}, running ${check.currentVersion}. Restart to apply.`}
				</GroupFooter>
			) : null}
			{update.restarting ? <GroupFooter testID="hub-update-restarting">Restarting the hub…</GroupFooter> : null}
			{update.restartTimedOut ? (
				<GroupFooter tone="danger" testID="hub-update-restart-timeout">
					The hub didn't come back. Check its version.
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
