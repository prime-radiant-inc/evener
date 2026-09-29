import { expect, it, vi } from "vitest";
import { createNewSessionStore } from "../newSession";
import { render, renderedText } from "../renderNative.testkit";
import { LaunchMemory } from "./launchMemory";
import { type NewSessionContextValue, NewSessionProvider } from "./newSessionContext";
import { SessionOptionsPage } from "./SessionOptionsPage";

vi.mock("../board/connectionStatus", async (importOriginal) => ({
	...(await importOriginal<typeof import("../board/connectionStatus")>()),
	useConnectionStatusText: () => null,
}));
vi.mock("../ConnectionProvider", () => ({ useConnection: () => ({ state: "ready", fatal: false }) }));
vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));
vi.mock("react-native-safe-area-context", () => ({ SafeAreaView: "SafeAreaView" }));

it("shows today's session options open, for the form's project", () => {
	const store = createNewSessionStore("hub-1");
	store.setState({ cwd: "/home/jesse/git/evener" });
	const context: NewSessionContextValue = {
		store,
		hubId: "hub-1",
		hubName: "magic-kingdom",
		client: null,
		ready: false,
		hosts: null,
		live: null,
		memory: new LaunchMemory({ getItemSync: () => null, setItemSync: () => {}, removeItemSync: () => {} }, "hub-1"),
		hostLabel: (host) => host,
	};
	const tree = render(
		<NewSessionProvider value={context}>
			<SessionOptionsPage />
		</NewSessionProvider>,
	);
	const text = renderedText(tree);
	expect(text).toContain("Apply only to this new session. Hub and project defaults stay unchanged.");
	expect(tree.root.findByProps({ accessibilityLabel: "Search session options" })).toBeTruthy();
});
