// The app's root view. Gesture handlers (the Board's swipes, the long-press
// menu, the Session's title bar) only recognize touches inside a
// GestureHandlerRootView, so the app mounts one at its root, outside every
// provider and screen, and it fills the screen. The providers are inert
// here: ConnectionProvider reports its saved hubs still loading, so the
// tree under the root is the loading view.
import type { ReactNode } from "react";
import { expect, it, vi } from "vitest";
import App from "./App";
import { render } from "./src/renderNative.testkit";

vi.mock("react-native", async () => (await import("./src/renderNative.testkit")).nativeModuleMock());
vi.mock("react-native-gesture-handler", () => ({
	GestureHandlerRootView: "GestureHandlerRootView",
}));
vi.mock("react-native-safe-area-context", () => ({
	SafeAreaProvider: (props: { children?: ReactNode }) => props.children ?? null,
}));
// The other native edges App's imports reach. Only the loading view renders,
// so each factory returns just what its importers read as they load, and
// vitest throws if anything reads another export.
vi.mock("react-native-enriched-markdown", () => ({}));
vi.mock("@react-navigation/elements", () => ({}));
vi.mock("@react-navigation/native", () => ({}));
vi.mock("@react-navigation/native-stack", () => ({ createNativeStackNavigator: () => ({}) }));
vi.mock("expo-status-bar", () => ({}));
vi.mock("expo-clipboard", () => ({}));
vi.mock("expo-crypto", () => ({ randomUUID: () => "app-root-uuid" }));
vi.mock("expo-sqlite", () => ({}));
// nativeLocation.ts takes the default export; the rest name Storage.
vi.mock("expo-sqlite/kv-store", () => ({ default: {}, Storage: {} }));
vi.mock("expo-file-system", () => ({}));
vi.mock("expo-image-manipulator", () => ({}));
vi.mock("expo-image-picker", () => ({}));
vi.mock("expo-secure-store", () => ({}));
vi.mock("./src/ConnectionProvider", () => ({
	ConnectionProvider: (props: { children?: ReactNode }) => props.children ?? null,
	useConnection: () => ({ loading: true }),
}));
vi.mock("./src/NativePreferencesProvider", () => ({
	NativePreferencesProvider: (props: { children?: ReactNode }) => props.children ?? null,
}));

it("roots the whole app in a full-screen GestureHandlerRootView", () => {
	const root = render(<App />).toJSON();
	expect(root).toMatchObject({ type: "GestureHandlerRootView", props: { style: { flex: 1 } } });
});
