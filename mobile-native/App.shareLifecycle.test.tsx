import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { type ComponentProps, createElement, type ReactNode } from "react";
import { act } from "react-test-renderer";
import { beforeEach, expect, it, vi } from "vitest";
import App from "./App";
import type { WorkspaceScreen } from "./src/WorkspaceScreen";
import { render, settleMicrotasks, unmountMountedTrees } from "./src/renderNative.testkit";
import { answerFleetRead, fleetSession } from "./src/session/fleetTestUtils";
import { share, sharePort } from "./src/share/shareTestkit";
import type { SharePort } from "./src/share/sharePayload";

const harness = vi.hoisted(() => ({
	connection: {} as Record<string, unknown>,
	port: null as SharePort | null,
}));

vi.mock("@react-navigation/native-stack", async () => ({
	createNativeStackNavigator: (await import("./src/navigationTestStack.testkit")).createTestStack,
}));
vi.mock("@react-navigation/native", async () => ({
	...(await import("@react-navigation/core")),
	NavigationContainer: (await import("@react-navigation/core")).BaseNavigationContainer,
	StackActions: (await import("@react-navigation/routers")).StackActions,
	DarkTheme: {},
	DefaultTheme: {},
}));
vi.mock("react-native", async () => ({
	...(await import("./src/renderNative.testkit")).nativeModuleMock(),
	Linking: { getInitialURL: async () => null, addEventListener: () => ({ remove: () => {} }) },
	AppState: { currentState: "active", addEventListener: () => ({ remove: () => {} }) },
}));
vi.mock("react-native-safe-area-context", () => ({
	SafeAreaProvider: (props: { children?: ReactNode }) => props.children ?? null,
}));
vi.mock("expo-status-bar", () => ({ StatusBar: () => null }));
vi.mock("expo-file-system", () => ({ Paths: { appleSharedContainers: {} } }));
vi.mock("expo-sqlite/kv-store", () => ({
	default: {},
	Storage: { getItemSync: () => null, setItemSync: () => {}, removeItemSync: () => {} },
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));
vi.mock("expo", () => ({ requireOptionalNativeModule: () => null }));
vi.mock("expo-camera", () => ({}));
vi.mock("expo-clipboard", () => ({}));
vi.mock("expo-crypto", () => ({ randomUUID: () => "app-root-uuid" }));
vi.mock("expo/fetch", () => ({}));
vi.mock("expo-sqlite", () => ({}));
vi.mock("expo-image-manipulator", () => ({}));
vi.mock("expo-image-picker", () => ({}));
vi.mock("expo-secure-store", () => ({}));
vi.mock("expo-web-browser", () => ({}));
vi.mock("react-native-enriched-markdown", () => ({}));
vi.mock("@react-navigation/elements", () => ({}));
vi.mock("react-native-reanimated", async () => (await import("./src/renderNative.testkit")).reanimatedModuleMock());
vi.mock("./src/ConnectionProvider", () => ({
	ConnectionProvider: (props: { children?: ReactNode }) => props.children ?? null,
	useConnection: () => harness.connection,
}));
vi.mock("./src/share/appGroupSharePort", () => ({ appGroupSharePort: () => harness.port as SharePort }));
vi.mock("./src/WorkspaceScreen", () => ({
	WorkspaceScreen: (props: ComponentProps<typeof WorkspaceScreen>) => createElement("WorkspaceShare", props),
}));
vi.mock("./src/hub/HubSheet", () => ({ HubSheet: () => null }));
vi.mock("./src/hubs/FirstRunScreen", () => ({ FirstRunScreen: () => null }));
vi.mock("./src/alerts/AlertBannerHost", () => ({ AlertBannerHost: () => null }));
vi.mock("./src/alerts/AlertsProvider", () => ({
	AlertsProvider: (props: { children?: ReactNode }) => props.children ?? null,
}));
vi.mock("./src/alerts/alertsContext", () => ({ useReportRoutes: () => () => {} }));
vi.mock("./src/rootStack", () => ({ useRootStackOptions: () => ({}) }));
vi.mock("./src/ui", () => ({
	ErrorMessage: () => null,
	useColors: () => ({
		background: "white",
		palette: { canvas: "white", inkMid: "black", accentInk: "blue", pressed: "gray", page: "white" },
	}),
	useTextScale: () => 1,
	allowFontScaling: true,
}));

beforeEach(() => {
	unmountMountedTrees();
	const incoming = share("incoming", Date.UTC(2026, 10, 27));
	harness.port = sharePort([incoming]);
	harness.connection = {
		loading: false,
		initialLocation: { hubId: "hub-1", webPath: "/" },
		activeProfile: { id: "hub-1", name: "Work hub" },
		state: "ready",
		restorationError: null,
	};
});

it("routes an incoming share through its picker target and waits for the matching workspace receipt", async () => {
	const session = fleetSession("local:work", { title: "Build the picker", state: "active" });
	const fleet = { live: [session], needsYou: [], sources: [] };
	const client = Object.assign(new FakeClient("ready"), {
		request: (method: string, params: unknown) => Promise.resolve(answerFleetRead(fleet, method, params) as never),
		onNotification: () => () => {},
	});
	harness.connection.client = client;
	const tree = render(<App />);
	const port = harness.port as SharePort;
	await settleMicrotasks();
	const chooseSession = () =>
		tree.root.find(
			(node) =>
				node.props.accessibilityRole === "button" &&
				typeof node.props.accessibilityLabel === "string" &&
				(node.props.accessibilityLabel as string).startsWith("Build the picker,"),
		);
	await act(async () => {
		chooseSession().props.onPress();
		await settleMicrotasks();
	});
	const workspace = () => tree.root.find((node) => (node.type as string) === "WorkspaceShare");
	expect(workspace().props.route).toMatchObject({
		name: "Workspace",
		params: { hubId: "hub-1", path: "/s/local%3Awork" },
	});
	expect(tree.root.findAllByProps({ testID: "share-new-session" })).toHaveLength(0);
	expect(workspace().props.share).toMatchObject({ id: "incoming", hubId: "hub-1", path: "/s/local%3Awork" });
	expect(await port.list()).toHaveLength(1);

	await act(async () => {
		workspace().props.onSharePlaced("another-share");
		await settleMicrotasks();
	});
	expect(workspace().props.share).toMatchObject({ id: "incoming", hubId: "hub-1", path: "/s/local%3Awork" });
	expect(await port.list()).toHaveLength(1);

	await act(async () => {
		workspace().props.onSharePlaced("incoming");
		await settleMicrotasks();
	});
	expect(workspace().props.share).toBeUndefined();
	expect(await port.list()).toHaveLength(0);
});
