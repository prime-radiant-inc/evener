// Native platform adapters shared by screen unit tests and real-hub journeys.
// AppWire, services, stores, drafts and mutation dispatch remain production code.
import { createElement, type ReactNode } from "react";
import { vi } from "vitest";

export const nativePlatform = {
	uuid: 0,
	kv: new Map<string, string>(),
	sqlitePorts: new Map<string, unknown>(),
	secure: new Map<string, string>(),
};

vi.mock("react-native", async () => {
	const mock = (await import("./renderNative.testkit")).nativeModuleMock();
	return {
		...mock,
		ActionSheetIOS: { showActionSheetWithOptions: vi.fn() },
		AppState: {
			currentState: "active",
			addEventListener: () => ({ remove: () => {} }),
		},
		Image: "Image",
		Linking: { openURL: vi.fn() },
		RefreshControl: "RefreshControl",
		StatusBar: "StatusBar",
		Modal: (props: { visible?: boolean; children?: ReactNode }) =>
			props.visible ? createElement("Modal", null, props.children) : null,
	};
});
vi.mock("react-native-safe-area-context", () => ({
	SafeAreaView: "SafeAreaView",
	SafeAreaProvider: (props: { children?: ReactNode }) => props.children ?? null,
	useSafeAreaInsets: () => ({ top: 47, bottom: 34, left: 0, right: 0 }),
}));
vi.mock("react-native-enriched-markdown", () => ({
	EnrichedMarkdownText: "EnrichedMarkdownText",
}));
vi.mock("expo-web-browser", () => ({ openBrowserAsync: vi.fn(async () => ({ type: "dismiss" })) }));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));
vi.mock("react-native-gesture-handler", async () =>
	(await import("./renderNative.testkit")).gestureDetectorModuleMock(),
);
vi.mock("react-native-gesture-handler/ReanimatedSwipeable", async () =>
	(await import("./renderNative.testkit")).gestureHandlerModuleMock(),
);
vi.mock("expo-clipboard", () => ({
	setStringAsync: vi.fn(async () => {}),
	getStringAsync: vi.fn(async () => ""),
}));
// Every durable mutation needs its own id, so the uuid counts.
vi.mock("expo-crypto", () => ({
	randomUUID: () => {
		nativePlatform.uuid += 1;
		return `send-test-uuid-${nativePlatform.uuid}`;
	},
	getRandomValues: (array: Uint8Array) => array,
}));
vi.mock("expo-sqlite", async () => {
	const { openSqliteSyncDouble } = await import("./sqliteSync.testkit");
	return {
		openDatabaseSync: (database: string) => {
			let port = nativePlatform.sqlitePorts.get(database);
			if (!port) {
				port = openSqliteSyncDouble().port;
				nativePlatform.sqlitePorts.set(database, port);
			}
			return port;
		},
	};
});
vi.mock("expo-sqlite/kv-store", () => {
	const Storage = {
		getItemSync: (key: string) => nativePlatform.kv.get(key) ?? null,
		setItemSync: (key: string, value: string) => void nativePlatform.kv.set(key, value),
		removeItemSync: (key: string) => void nativePlatform.kv.delete(key),
	};
	return { Storage, default: Storage };
});
vi.mock("expo-file-system", () => ({
	File: class File {
		constructor(public uri: string) {}
	},
}));
vi.mock("expo-image-manipulator", () => ({
	ImageManipulator: {
		manipulateAsync: vi.fn(async () => ({ uri: "manipulated" })),
	},
	SaveFormat: { JPEG: "jpeg" },
}));
vi.mock("expo-image-picker", () => ({
	launchImageLibraryAsync: vi.fn(async () => ({ canceled: true, assets: [] })),
	launchCameraAsync: vi.fn(async () => ({ canceled: true, assets: [] })),
	requestCameraPermissionsAsync: vi.fn(async () => ({ granted: true })),
	UIImagePickerPreferredAssetRepresentationMode: { Current: "current" },
}));
vi.mock("expo-secure-store", () => ({
	getItemAsync: vi.fn(async (key: string) => nativePlatform.secure.get(key) ?? null),
	setItemAsync: vi.fn(async (key: string, value: string) => {
		nativePlatform.secure.set(key, value);
	}),
	deleteItemAsync: vi.fn(async (key: string) => {
		nativePlatform.secure.delete(key);
	}),
}));
