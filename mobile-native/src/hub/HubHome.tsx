// The Hub's first page (spec 12): the hub's status line, then one row per
// page. A row whose page hasn't landed yet leaves the sheet for today's screen
// (ruling 10); each later PR swaps its row for a push.
import { StackActions } from "@react-navigation/native";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { Text } from "react-native";
import { Group, GroupedPage, GroupLabel, Row } from "../sheet/Grouped";
import { useConnectionLine } from "../sheet/SheetStatus";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import { hubStatusLine } from "./hubHeader";
import { type HubRoutes, useHubSheet } from "./hubSheetContext";

type InterimScreen = "Providers" | "Plugins" | "TranscriptPreferences" | "HubSettings";

export function HubHome({ navigation }: NativeStackScreenProps<HubRoutes, "HubHome">) {
	const { hubId, ready } = useHubSheet();
	const { palette } = useColors();
	const scale = useTextScale();
	const line = hubStatusLine(ready, useConnectionLine(), null);
	// The root stack: the sheet's own route sits on it, so a replace there
	// closes the sheet and opens the screen in one step.
	const root = navigation.getParent();
	const leaveFor = (screen: InterimScreen) => root?.dispatch(StackActions.replace(screen, { hubId }));
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
				<Row icon="point.3.connected.trianglepath.dotted" label="Hubs" chevron onPress={() => root?.navigate("Hubs")} />
			</Group>
			<GroupLabel>More</GroupLabel>
			<Group>
				<Row icon="info.circle" label="Hub settings" chevron onPress={() => leaveFor("HubSettings")} />
			</Group>
		</GroupedPage>
	);
}
