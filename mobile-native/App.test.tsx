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

vi.mock("react-native", async () => ({
	...(await import("./src/renderNative.testkit")).nativeModuleMock(),
	AccessibilityInfo: { announceForAccessibility: vi.fn() },
	ActionSheetIOS: { showActionSheetWithOptions: vi.fn() },
	AppState: {
		currentState: "active",
		addEventListener: () => ({ remove: () => {} }),
	},
	Image: "Image",
	Keyboard: { dismiss: vi.fn() },
	Linking: { openURL: vi.fn() },
	RefreshControl: "RefreshControl",
	StatusBar: "StatusBar",
}));
vi.mock("react-native-gesture-handler", () => ({
	GestureHandlerRootView: "GestureHandlerRootView",
}));
vi.mock("react-native-safe-area-context", () => ({
	SafeAreaView: "SafeAreaView",
	SafeAreaProvider: (props: { children?: ReactNode }) => props.children ?? null,
	useSafeAreaInsets: () => ({ top: 47, bottom: 34, left: 0, right: 0 }),
}));
vi.mock("react-native-enriched-markdown", () => ({
	EnrichedMarkdownText: "EnrichedMarkdownText",
}));
vi.mock("@react-navigation/elements", () => ({ useHeaderHeight: () => 64 }));
vi.mock("@react-navigation/native", () => ({
	DarkTheme: {},
	DefaultTheme: {},
	NavigationContainer: "NavigationContainer",
	useFocusEffect: () => {},
	useIsFocused: () => true,
	useNavigationState: () => undefined,
}));
vi.mock("@react-navigation/native-stack", () => ({
	createNativeStackNavigator: () => ({ Navigator: "Navigator", Screen: "Screen", Group: "Group" }),
}));
vi.mock("expo-status-bar", () => ({ StatusBar: "StatusBar" }));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));
vi.mock("expo-clipboard", () => ({
	setStringAsync: vi.fn(async () => {}),
	getStringAsync: vi.fn(async () => ""),
}));
vi.mock("expo-crypto", () => ({
	randomUUID: () => "app-root-uuid",
	getRandomValues: (array: Uint8Array) => array,
}));
vi.mock("expo-sqlite", () => ({ openDatabaseSync: vi.fn() }));
vi.mock("expo-sqlite/kv-store", () => {
	const Storage = { getItemSync: () => null, setItemSync: () => {}, removeItemSync: () => {} };
	// nativeLocation.ts takes the default export; the rest name Storage.
	return { default: Storage, Storage };
});
vi.mock("expo-file-system", () => ({
	File: class File {
		constructor(public uri: string) {}
	},
}));
vi.mock("expo-image-manipulator", () => ({
	ImageManipulator: { manipulate: vi.fn() },
	SaveFormat: { JPEG: "jpeg", PNG: "png" },
}));
vi.mock("expo-image-picker", () => ({
	launchImageLibraryAsync: vi.fn(async () => ({ canceled: true, assets: [] })),
	UIImagePickerPreferredAssetRepresentationMode: { Current: "current" },
}));
vi.mock("expo-secure-store", () => ({
	getItemAsync: vi.fn(async () => null),
	setItemAsync: vi.fn(async () => {}),
	deleteItemAsync: vi.fn(async () => {}),
}));
vi.mock("./src/ConnectionProvider", () => ({
	ConnectionProvider: (props: { children?: ReactNode }) => props.children ?? null,
	useConnection: () => ({
		loading: true,
		initialLocation: null,
		activeProfile: null,
		restorationError: null,
	}),
}));
vi.mock("./src/NativePreferencesProvider", () => ({
	NativePreferencesProvider: (props: { children?: ReactNode }) => props.children ?? null,
}));

it("roots the whole app in a full-screen GestureHandlerRootView", () => {
	const root = render(<App />).toJSON();
	expect(Array.isArray(root)).toBe(false);
	expect(root).toMatchObject({ type: "GestureHandlerRootView", props: { style: { flex: 1 } } });
});
