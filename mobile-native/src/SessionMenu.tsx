import { Pressable, ScrollView, View } from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import { HoldingModal } from "./alerts/HoldingModal";
import { space } from "./design/tokens";
import { Group, Row } from "./sheet/Grouped";
import { Copy, styles, useColors } from "./ui";

export type SessionDestination = "find" | "session" | "tasks" | "notes" | "subagents" | "pin" | "delete";

export function SessionMenu({
	title,
	hubName,
	connected,
	deletionAvailable,
	close,
	choose,
}: {
	title: string;
	hubName: string;
	connected: boolean;
	deletionAvailable: boolean;
	close: () => void;
	choose: (destination: SessionDestination) => void;
}) {
	const { palette } = useColors();
	return (
		<HoldingModal transparent onRequestClose={close}>
			<View style={[styles.fill, { justifyContent: "flex-end" }]}>
				<Pressable
					accessibilityRole="button"
					accessibilityLabel="Dismiss session actions"
					onPress={close}
					style={{
						position: "absolute",
						inset: 0,
						backgroundColor: "#00000066",
					}}
				/>
				<SafeAreaView
					edges={["bottom", "left", "right"]}
					style={{
						maxHeight: "80%",
						backgroundColor: palette.canvas,
						borderTopLeftRadius: 24,
						borderTopRightRadius: 24,
					}}
				>
					<ScrollView contentContainerStyle={{ paddingBottom: 12 }}>
						<View style={{ paddingHorizontal: space.margin, paddingTop: 20, gap: 4 }}>
							<Copy>{title}</Copy>
							<Copy muted>{hubName}</Copy>
						</View>
						<Group>
							<Row label="Find in session" tone="accent" onPress={() => choose("find")} />
							<Row label="Session details" tone="accent" onPress={() => choose("session")} />
							<Row label="Pin to section" tone="accent" onPress={() => choose("pin")} />
							<Row label="Tasks" tone="accent" disabled={!connected} onPress={() => choose("tasks")} />
							<Row label="Activity" tone="accent" disabled={!connected} onPress={() => choose("subagents")} />
							<Row label="Cancel" onPress={close} />
							{deletionAvailable ? (
								<Row label="Delete saved session" tone="danger" onPress={() => choose("delete")} />
							) : null}
						</Group>
					</ScrollView>
				</SafeAreaView>
			</View>
		</HoldingModal>
	);
}
