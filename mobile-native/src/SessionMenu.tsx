import { Pressable, ScrollView, View } from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import { HoldingModal } from "./alerts/HoldingModal";
import { Action, Copy, styles, useColors } from "./ui";

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
	const colors = useColors();
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
						backgroundColor: colors.surface,
						borderTopLeftRadius: 24,
						borderTopRightRadius: 24,
					}}
				>
					<ScrollView contentContainerStyle={{ padding: 20, gap: 8 }}>
						<Copy>{title}</Copy>
						<Copy muted>{hubName}</Copy>
						<Action onPress={() => choose("find")}>Find in session</Action>
						<Action onPress={() => choose("session")}>Session details</Action>
						<Action onPress={() => choose("pin")}>Pin to section</Action>
						<Action disabled={!connected} onPress={() => choose("tasks")}>
							Tasks
						</Action>
						<Action disabled={!connected} onPress={() => choose("subagents")}>
							Activity
						</Action>
						<Action tone="quiet" onPress={close}>
							Cancel
						</Action>
						{deletionAvailable ? <Action onPress={() => choose("delete")}>Delete saved session</Action> : null}
					</ScrollView>
				</SafeAreaView>
			</View>
		</HoldingModal>
	);
}
