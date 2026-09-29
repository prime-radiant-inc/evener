import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import { createNewSessionStore } from "../newSession";
import { render, renderedText } from "../renderNative.testkit";
import { NewSessionProvider } from "./newSessionContext";
import { sheetContext } from "./newSessionTestUtils";
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
	const context = sheetContext(store, { ready: false });
	const tree = render(
		<NewSessionProvider value={context}>
			<SessionOptionsPage />
		</NewSessionProvider>,
	);
	const text = renderedText(tree);
	expect(text).toContain("Apply only to this new session. Hub and project defaults stay unchanged.");
	expect(tree.root.findByProps({ accessibilityLabel: "Search session options" })).toBeTruthy();
});

it("reads the chosen host's options, and reads again when the host changes", async () => {
	const store = createNewSessionStore("hub-1");
	store.setState({ source: "paradise-park", cwd: "/Users/jesse/git/evener" });
	const calls: { method: string; params: unknown }[] = [];
	const client = {
		request: async (method: string, params: unknown) => {
			calls.push({ method, params });
			const forwarded = method === "evener/host/request" ? (params as { method: string }).method : method;
			if (forwarded === "evener/launch/schema") return { options: [] };
			return { effective: {}, layers: {}, provenance: {} };
		},
		onNotification: () => () => {},
	};
	render(
		<NewSessionProvider value={sheetContext(store, { client: client as never })}>
			<SessionOptionsPage />
		</NewSessionProvider>,
	);
	await act(async () => {
		await new Promise((resolve) => setTimeout(resolve, 0));
	});
	expect(calls).toEqual([
		{
			method: "evener/host/request",
			params: { host: "paradise-park", method: "evener/launch/schema", params: {} },
		},
		{
			method: "evener/host/request",
			params: {
				host: "paradise-park",
				method: "evener/launch/resolve",
				params: { cwd: "/Users/jesse/git/evener", launchOverrides: {} },
			},
		},
	]);
	calls.length = 0;
	await act(async () => {
		store.setState({ source: "local" });
		await new Promise((resolve) => setTimeout(resolve, 0));
	});
	expect(calls.map((call) => call.method)).toEqual(["evener/launch/schema", "evener/launch/resolve"]);
});
