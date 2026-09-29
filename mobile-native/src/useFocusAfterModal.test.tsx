// AppState's "focus"/"blur" pair is Android-only (issue #2576): subscribing
// on iOS raises a dev-mode red box and the handler never fires there. This
// pins useFocusAfterModal's platform gate directly, without mounting the
// whole ConversationScreen (which needs a working connection, mutation
// runtime and sqlite double - see ConversationScreen.recovery.test.tsx).
import { useRef } from "react";
import type { TextInput } from "react-native";
import { expect, it, vi } from "vitest";
import { renderHook } from "./renderNative.testkit";
import { useFocusAfterModal } from "./screens";

// vi.mock's factory is hoisted above this file's imports and may not close
// over a module-level `let`, so the mutable Platform.OS and the tracked
// addEventListener spy live in a vi.hoisted cell both sides can reach.
const native = vi.hoisted(() => ({
	platform: { OS: "ios" as "ios" | "android" },
	addEventListener: vi.fn(() => ({ remove: vi.fn() })),
}));

vi.mock("react-native", async () => {
	const mock = (await import("./renderNative.testkit")).nativeModuleMock();
	return {
		...mock,
		Platform: native.platform,
		ActionSheetIOS: { showActionSheetWithOptions: vi.fn() },
		AppState: {
			currentState: "active",
			addEventListener: native.addEventListener,
		},
		Image: "Image",
		Keyboard: { dismiss: vi.fn() },
		Linking: { openURL: vi.fn() },
		RefreshControl: "RefreshControl",
		StatusBar: "StatusBar",
	};
});
vi.mock("react-native-safe-area-context", () => ({
	SafeAreaView: "SafeAreaView",
	SafeAreaProvider: (props: { children?: unknown }) => props.children ?? null,
	useSafeAreaInsets: () => ({ top: 47, bottom: 34, left: 0, right: 0 }),
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));
vi.mock("react-native-gesture-handler", async () =>
	(await import("./renderNative.testkit")).gestureDetectorModuleMock(),
);
vi.mock("react-native-gesture-handler/ReanimatedSwipeable", async () =>
	(await import("./renderNative.testkit")).gestureHandlerModuleMock(),
);
vi.mock("expo-web-browser", () => ({}));
vi.mock("react-native-enriched-markdown", () => ({
	EnrichedMarkdownText: "EnrichedMarkdownText",
}));
vi.mock("@react-navigation/elements", () => ({ useHeaderHeight: () => 64 }));
vi.mock("@react-navigation/native", async () => {
	const { useEffect } = await import("react");
	return {
		useFocusEffect: (effect: () => void | (() => void)) => useEffect(effect, []),
		useIsFocused: () => true,
	};
});
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));
vi.mock("expo-clipboard", () => ({
	setStringAsync: vi.fn(async () => {}),
	getStringAsync: vi.fn(async () => ""),
}));
vi.mock("expo-crypto", () => ({
	randomUUID: () => "test-uuid",
	getRandomValues: (array: Uint8Array) => array,
}));
vi.mock("expo-sqlite", async () => {
	const { openSqliteSyncDouble } = await import("./sqliteSync.testkit");
	return { openDatabaseSync: () => openSqliteSyncDouble().port };
});
vi.mock("expo-sqlite/kv-store", () => ({
	Storage: { getItemSync: () => null, setItemSync: () => {} },
}));
vi.mock("expo-file-system", () => ({
	File: class File {
		constructor(public uri: string) {}
	},
}));
vi.mock("expo-image-manipulator", () => ({}));
vi.mock("expo-image-picker", () => ({
	launchImageLibraryAsync: vi.fn(async () => ({ canceled: true, assets: [] })),
	UIImagePickerPreferredAssetRepresentationMode: { Current: "current" },
}));
vi.mock("expo-secure-store", () => ({
	getItemAsync: vi.fn(async () => null),
	setItemAsync: vi.fn(async () => {}),
	deleteItemAsync: vi.fn(async () => {}),
}));
vi.mock("./ConnectionProvider", () => ({ useConnection: () => ({}) }));
vi.mock("./NativePreferencesProvider", () => ({
	useNativePreferences: () => ({
		hubId: null,
		model: null,
		snapshot: null,
		config: null,
		connected: false,
		offlineDraftUnreadable: false,
		offlineStorageUnavailable: false,
		discardUnreadableKeybindingsDraft: () => null,
	}),
}));

function mountHook(navigation: { isFocused: () => boolean }) {
	return renderHook(() => {
		const focusAfterModal = useRef(false);
		const composerInput = useRef<TextInput>(null);
		useFocusAfterModal(navigation, focusAfterModal, composerInput);
	});
}

it('does not subscribe to AppState "focus" on iOS', () => {
	native.platform.OS = "ios";
	native.addEventListener.mockClear();

	mountHook({ isFocused: () => true });

	expect(native.addEventListener).not.toHaveBeenCalled();
});

it('subscribes to AppState "focus" on Android, and unsubscribes on unmount', () => {
	native.platform.OS = "android";
	native.addEventListener.mockClear();
	const remove = vi.fn();
	native.addEventListener.mockReturnValueOnce({ remove });

	const { unmount } = mountHook({ isFocused: () => true });

	expect(native.addEventListener).toHaveBeenCalledTimes(1);
	expect(native.addEventListener).toHaveBeenCalledWith("focus", expect.any(Function));
	expect(remove).not.toHaveBeenCalled();

	unmount();
	expect(remove).toHaveBeenCalledTimes(1);
});
