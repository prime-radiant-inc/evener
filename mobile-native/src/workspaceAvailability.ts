import { Platform } from "react-native";

/** Each paired hub needs its own persistent WebKit store for credentials and
 * drafts. iOS 17 introduced independently removable website data stores. */
export function usesHubWorkspace(): boolean {
	return Platform.OS === "ios" && Number.parseInt(String(Platform.Version), 10) >= 17;
}
