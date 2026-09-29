// The Hub's first page (spec 12): the hub's status line, then one row per
// page. A row whose page hasn't landed yet leaves the sheet for today's screen
// (ruling 10); each later PR swaps its row for a push. MORE keeps today's
// administration screens reachable (ruling 12), and ABOUT names this app's
// version and offers the hub's update (ruling 22).
import { StackActions } from "@react-navigation/native";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { nativeApplicationVersion, nativeBuildVersion } from "expo-application";
import { useSyncExternalStore } from "react";
import { Alert, Text } from "react-native";
import { useConnection } from "../ConnectionProvider";
import { useConnectionStatusText } from "../board/connectionStatus";
import { whenReady } from "../connectionDisplay";
import { Group, GroupedPage, GroupFooter, GroupLabel, Row } from "../sheet/Grouped";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import { appVersionText, hubStatusLine } from "./hubHeader";
import { type HubRoutes, useHubSheet } from "./hubSheetContext";

type InterimScreen =
	| "Providers"
	| "Plugins"
	| "TranscriptPreferences"
	| "KeybindingPreferences"
	| "LaunchSettings"
	| "HubSettings";

export function HubHome({ navigation }: NativeStackScreenProps<HubRoutes, "HubHome">) {
	const { hubId, hubName, ready, canUseConnection, updates } = useHubSheet();
	const update = useSyncExternalStore(updates.subscribe, updates.getState);
	const { profiles } = useConnection();
	const { palette } = useColors();
	const scale = useTextScale();
	const line = hubStatusLine(ready, useConnectionStatusText(), update.check);
	// The root stack: the sheet's own route sits on it, so a replace there
	// closes the sheet and opens the screen in one step.
	const root = navigation.getParent();
	const leaveFor = (screen: InterimScreen) => root?.dispatch(StackActions.replace(screen, { hubId }));
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
	return (
		<GroupedPage>
			<Text
				allowFontScaling={allowFontScaling}
				style={{
					color: palette.inkMid,
					fontSize: 15 * scale,
					lineHeight: 20 * scale,
					paddingHorizontal: 32,
					paddingTop: 4,
				}}
			>
				{line}
			</Text>
			<GroupLabel>Setup</GroupLabel>
			<Group>
				<Row icon="key" label="Providers" chevron onPress={() => leaveFor("Providers")} />
				<Row icon="puzzlepiece.extension" label="Plugins" chevron onPress={() => leaveFor("Plugins")} />
			</Group>
			<GroupLabel>This phone</GroupLabel>
			<Group>
				<Row icon="textformat.size" label="Display" chevron onPress={() => leaveFor("TranscriptPreferences")} />
				<Row
					icon="point.3.connected.trianglepath.dotted"
					label="Hubs"
					value={profiles.length}
					chevron
					onPress={() => navigation.navigate("Hubs")}
				/>
			</Group>
			<GroupLabel>More</GroupLabel>
			<Group>
				<Row icon="keyboard" label="Keyboard shortcuts" chevron onPress={() => leaveFor("KeybindingPreferences")} />
				<Row icon="slider.horizontal.3" label="Launch defaults" chevron onPress={() => leaveFor("LaunchSettings")} />
				<Row icon="gearshape" label="Hub settings" chevron onPress={() => leaveFor("HubSettings")} />
			</Group>
			<GroupLabel>About</GroupLabel>
			<Group>
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
				<GroupFooter tone="danger">The hub restarted without the update. Check its logs.</GroupFooter>
			) : null}
			{updateProblem ? <GroupFooter tone="danger">{updateProblem}</GroupFooter> : null}
		</GroupedPage>
	);
}
