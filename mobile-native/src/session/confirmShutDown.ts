import { Alert } from "react-native";
import { destructiveButton } from "../haptics";

/** Asks before shutting a session down: the Board's row menu, the ⋯ menu and
 * the info sheet ask the same question. `title` names the session when the
 * caller has it. */
export function confirmShutDown(shutDown: () => void, title?: string): void {
	Alert.alert(
		title === undefined ? "Shut down this session?" : `Shut down “${title}”?`,
		"It stops now and keeps its history. Sending a message resumes it.",
		[{ text: "Cancel", style: "cancel" }, destructiveButton("Shut down", () => shutDown())],
	);
}
