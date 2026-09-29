// The app's root view. Gesture handlers (the Board's swipes, the long-press
// menu, the Session's title bar) only recognize touches inside a
// GestureHandlerRootView, so the app mounts one at its root, outside every
// provider and screen, and it fills the screen. The providers are inert
// here: ConnectionProvider reports its saved hubs still loading, so the
// tree under the root is the loading view.
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import type { ReactNode } from "react";
import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import App from "./App";
import { render } from "./src/renderNative.testkit";

vi.mock("react-native", async () => (await import("./src/renderNative.testkit")).nativeModuleMock());
vi.mock("react-native-gesture-handler", () => ({
	GestureHandlerRootView: "GestureHandlerRootView",
}));
vi.mock("react-native-gesture-handler/ReanimatedSwipeable", async () =>
	(await import("./src/renderNative.testkit")).gestureHandlerModuleMock(),
);
vi.mock("react-native-reanimated", async () => (await import("./src/renderNative.testkit")).reanimatedModuleMock());
vi.mock("react-native-safe-area-context", () => ({
	SafeAreaProvider: (props: { children?: ReactNode }) => props.children ?? null,
}));
// The other native edges App's imports reach. Only the loading view renders,
// so each factory returns just what its importers read as they load, and
// vitest throws if anything reads another export.
vi.mock("react-native-enriched-markdown", () => ({}));
vi.mock("@react-navigation/elements", () => ({}));
vi.mock("@react-navigation/native", () => ({
	createNavigationContainerRef: () => ({ getRootState: () => undefined }),
}));
vi.mock("@react-navigation/native-stack", () => ({ createNativeStackNavigator: () => ({}) }));
vi.mock("expo-status-bar", () => ({}));
vi.mock("expo-camera", () => ({}));
vi.mock("expo-clipboard", () => ({}));
vi.mock("expo-crypto", () => ({ randomUUID: () => "app-root-uuid" }));
vi.mock("expo-sqlite", () => ({}));
// nativeLocation.ts takes the default export; the rest name Storage.
vi.mock("expo-sqlite/kv-store", () => ({ default: {}, Storage: {} }));
vi.mock("expo-file-system", () => ({}));
vi.mock("expo-image-manipulator", () => ({}));
vi.mock("expo-image-picker", () => ({}));
vi.mock("expo-secure-store", () => ({}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));
vi.mock("expo-application", () => ({ nativeApplicationVersion: "0.1.0", nativeBuildVersion: "5" }));
vi.mock("expo-web-browser", () => ({}));
const connection = vi.hoisted(() => ({ value: { loading: true } as Record<string, unknown> }));
vi.mock("./src/ConnectionProvider", () => ({
	ConnectionProvider: (props: { children?: ReactNode }) => props.children ?? null,
	// The phone's saved hubs, which the alerts read to retire a removed hub's.
	useConnection: () => ({ profiles: [], ...connection.value }),
}));
const outbox = vi.hoisted(() => ({ bind: vi.fn() }));
vi.mock("./src/outbox/nativeOutboxFlush", () => ({ outboxFlush: outbox }));
vi.mock("./src/NativePreferencesProvider", () => ({
	NativePreferencesProvider: (props: { children?: ReactNode }) => props.children ?? null,
}));

// The keyboard controller reports the keyboard's frames to the screens
// that move with it (the Session's composer), so it wraps the whole app.
it("roots the whole app in a full-screen GestureHandlerRootView, under the keyboard controller", () => {
	const root = render(<App />).toJSON();
	expect(root).toMatchObject({
		type: "GestureHandlerRootView",
		props: { style: { flex: 1 } },
		children: [
			{
				type: "KeyboardProvider",
				children: [
					{
						type: "View",
						children: [{ type: "ActivityIndicator", props: { accessibilityLabel: "Loading saved hubs" } }],
					},
				],
			},
		],
	});
});

it("hands the outbox flush the active hub's client while it is ready, and nothing otherwise (ruling 17)", () => {
	const client = new FakeClient("ready");
	connection.value = { loading: true, activeProfile: { id: "hub-1" }, client, state: "ready" };
	const tree = render(<App />);
	expect(outbox.bind).toHaveBeenLastCalledWith("hub-1", client);
	connection.value = { loading: true, activeProfile: { id: "hub-1" }, client, state: "reconnecting" };
	act(() => tree.update(<App />));
	expect(outbox.bind).toHaveBeenLastCalledWith("hub-1", null);
	connection.value = { loading: true };
});
