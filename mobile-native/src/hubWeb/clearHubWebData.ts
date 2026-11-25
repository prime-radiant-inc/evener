import { usesHubWorkspace } from "../workspaceAvailability";

export async function clearHubWebData(id: string): Promise<void> {
	if (!usesHubWorkspace()) return;
	const { default: web } = await import("react-native-webview/lib/NativeRNCWebViewModule");
	if (!web.clearWebsiteDataStore) throw new Error("The iPhone could not clear this hub’s web data.");
	await web.clearWebsiteDataStore(id);
}
