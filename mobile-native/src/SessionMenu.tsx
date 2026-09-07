import { Modal, Pressable, ScrollView, View } from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import { Action, Copy, styles, useColors } from "./ui";

export type SessionDestination = "session" | "tasks" | "activity" | "pin";

export function SessionMenu({
	title,
	hubName,
	connected,
	close,
	choose,
}: {
	title: string;
	hubName: string;
	connected: boolean;
	close: () => void;
	choose: (destination: SessionDestination) => void;
}) {
	const colors = useColors();
	return (
		<Modal transparent onRequestClose={close}>
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
						<Action onPress={() => choose("session")}>Session details</Action>
						<Action onPress={() => choose("pin")}>Pin to section</Action>
						<Action disabled={!connected} onPress={() => choose("tasks")}>
							Tasks
						</Action>
						<Action disabled={!connected} onPress={() => choose("activity")}>
							Activity
						</Action>
						<Action tone="quiet" onPress={close}>
							Cancel
						</Action>
					</ScrollView>
				</SafeAreaView>
			</View>
		</Modal>
	);
}
