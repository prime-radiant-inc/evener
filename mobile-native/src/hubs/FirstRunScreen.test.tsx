import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { act, type ReactTestRenderer } from "react-test-renderer";
import { beforeEach, expect, it, vi } from "vitest";
import type { HubProfile } from "../connection";
import { palettes } from "../design/tokens";
import { hasTestID } from "../markerTestUtils";
import { offersButton, render, renderedText } from "../renderNative.testkit";
import type { Routes } from "../screens";
import { FirstRunScreen } from "./FirstRunScreen";

const mocks = vi.hoisted(() => ({
	connection: {
		profiles: [] as { id: string; name: string; origin: string }[],
		activeProfile: null as { id: string; name: string; origin: string } | null,
		selectHub: vi.fn(),
		removeHub: vi.fn(),
		updateHub: vi.fn(),
		saveHub: vi.fn(),
	},
	getStringAsync: vi.fn(),
}));
vi.mock("../ConnectionProvider", () => ({ useConnection: () => mocks.connection }));
vi.mock("expo-camera", () => ({
	CameraView: "CameraView",
	useCameraPermissions: () => [{ granted: true, canAskAgain: true, status: "granted" }, vi.fn()],
}));
vi.mock("expo-clipboard", () => ({ getStringAsync: mocks.getStringAsync }));
vi.mock("@react-navigation/native", () => {
	const navigation = { isFocused: () => true, addListener: () => () => {} };
	return { useNavigation: () => navigation };
});
vi.mock("expo/fetch", () => ({ fetch: vi.fn() }));
vi.mock("expo-crypto", () => ({ getRandomBytes: (count: number) => new Uint8Array(count).fill(7) }));
vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("react-native-safe-area-context", () => ({ SafeAreaView: "SafeAreaView" }));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));

const LINK = "https://magic-kingdom:9180/auth/s3cret";
const MAGIC: HubProfile = { id: "hub-1", name: "magic-kingdom", origin: "https://magic-kingdom:9180" };
const PARADISE: HubProfile = { id: "hub-2", name: "paradise-park", origin: "http://100.113.28.18:9180" };

beforeEach(() => {
	mocks.connection.profiles = [];
	mocks.connection.activeProfile = null;
	mocks.connection.selectHub.mockReset();
	mocks.connection.saveHub.mockReset();
	mocks.getStringAsync.mockReset();
});

function mount() {
	const navigation = { navigate: vi.fn() };
	const route = { key: "Hubs", name: "Hubs", params: undefined } as const;
	const tree = render(
		<FirstRunScreen
			navigation={navigation as unknown as NativeStackScreenProps<Routes, "Hubs">["navigation"]}
			route={route}
		/>,
	);
	return { tree, navigation };
}

function button(tree: ReactTestRenderer, label: string) {
	return tree.root.findByProps({ accessibilityRole: "button", accessibilityLabel: label });
}

async function press(tree: ReactTestRenderer, label: string) {
	await act(async () => {
		button(tree, label).props.onPress();
	});
}

/** The choices page's title and the line under it; [] off that page. */
function intro(tree: ReactTestRenderer) {
	const [page] = tree.root.findAll((node) => typeof node.type === "string" && node.props.testID === "first-run-intro");
	return page ? page.findAll((node) => String(node.type) === "Text") : [];
}

const onChoices = (tree: ReactTestRenderer) => intro(tree).length > 0;
const listsSavedHubs = (tree: ReactTestRenderer) => hasTestID(tree, "group:saved-hubs");

it("asks to connect to your hub, and says where the pairing code lives", () => {
	const { tree } = mount();
	const [title, where, ...others] = intro(tree);
	expect(others).toHaveLength(0);
	expect(title?.props.accessibilityRole).toBe("header");
	expect(title?.props.style).toMatchObject({ fontSize: 28, lineHeight: 34, fontWeight: "600" });
	expect(where?.props.style.color).toBe(palettes.light.inkMid);
	for (const label of ["Scan pairing code", "Paste pairing link", "Enter the address"])
		expect(button(tree, label)).toBeDefined();
	expect(listsSavedHubs(tree)).toBe(false);
});

it("scans a pairing code in place, and Back returns to the choices", async () => {
	const { tree } = mount();
	await press(tree, "Scan pairing code");
	expect(tree.root.findAll((node) => String(node.type) === "CameraView")).toHaveLength(1);
	expect(onChoices(tree)).toBe(false);
	await press(tree, "Back");
	expect(tree.root.findAll((node) => String(node.type) === "CameraView")).toHaveLength(0);
	expect(onChoices(tree)).toBe(true);
});

it("types an address in place", async () => {
	const { tree } = mount();
	await press(tree, "Enter the address");
	expect(
		tree.root.findAll((node) => String(node.type) === "TextInput" && node.props.accessibilityLabel === "Address"),
	).toHaveLength(1);
});

it("pastes a pairing link, connects, and opens the Board", async () => {
	mocks.getStringAsync.mockResolvedValue(LINK);
	mocks.connection.saveHub.mockResolvedValue(true);
	const { tree, navigation } = mount();
	await press(tree, "Paste pairing link");
	await press(tree, "Paste");
	await press(tree, "Connect");
	expect(mocks.connection.saveHub).toHaveBeenCalledWith({
		name: "magic-kingdom",
		origin: "https://magic-kingdom:9180",
		token: "s3cret",
	});
	expect(navigation.navigate).toHaveBeenCalledWith("Sessions");
	expect(onChoices(tree)).toBe(true);
});

it("stays where Back left it when a save it started finishes afterwards", async () => {
	mocks.getStringAsync.mockResolvedValue(LINK);
	let finish!: (selected: boolean) => void;
	mocks.connection.saveHub.mockReturnValue(
		new Promise<boolean>((resolve) => {
			finish = resolve;
		}),
	);
	const { tree, navigation } = mount();
	await press(tree, "Paste pairing link");
	await press(tree, "Paste");
	await press(tree, "Connect");
	await press(tree, "Back");
	await act(async () => {
		finish(true);
	});
	expect(navigation.navigate).not.toHaveBeenCalled();
	expect(onChoices(tree)).toBe(true);
});

it("lists saved hubs when none is selected, and opens the one chosen", async () => {
	mocks.connection.profiles = [MAGIC, PARADISE];
	const { tree, navigation } = mount();
	expect(listsSavedHubs(tree)).toBe(true);
	expect(renderedText(tree)).toContain("http://100.113.28.18:9180");
	await press(tree, "paradise-park, http://100.113.28.18:9180");
	expect(mocks.connection.selectHub).toHaveBeenCalledWith("hub-2");
	expect(navigation.navigate).toHaveBeenCalledWith("Sessions");
});

it("never asks to reconnect", async () => {
	mocks.connection.profiles = [MAGIC];
	const { tree } = mount();
	expect(offersButton(tree, /\bReconnect\b/)).toBe(false);
	await press(tree, "Scan pairing code");
	expect(offersButton(tree, /\bReconnect\b/)).toBe(false);
});

it("still lists saved hubs with one selected, since the Board can be swiped back to this screen", () => {
	mocks.connection.profiles = [MAGIC, PARADISE];
	mocks.connection.activeProfile = MAGIC;
	const { tree } = mount();
	expect(listsSavedHubs(tree)).toBe(true);
	expect(button(tree, "magic-kingdom, https://magic-kingdom:9180")).toBeDefined();
});
