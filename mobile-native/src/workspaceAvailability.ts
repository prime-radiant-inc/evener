import { Platform } from "react-native";

export function usesHubWorkspace(): boolean {
	return Platform.OS === "ios";
}
