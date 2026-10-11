import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { act, type ReactTestInstance, type ReactTestRenderer } from "react-test-renderer";
import { beforeEach, expect, it, vi } from "vitest";
import type { HubProfile } from "../connection";
import { render, renderedText } from "../renderNative.testkit";
import { Row } from "../sheet/Grouped";
import { HubsPage } from "./HubsPage";
import type { HubRoutes } from "./hubSheetContext";

const connection = vi.hoisted(() => ({
	profiles: [] as HubProfile[],
	activeProfile: null as HubProfile | null,
	selectHub: vi.fn(),
	removeHub: vi.fn(),
	updateHub: vi.fn(),
	state: "ready",
	fatal: false,
	downSince: null as number | null,
	lastLiveAt: null as number | null,
}));
vi.mock("../ConnectionProvider", () => ({ useConnection: () => connection }));
vi.mock("./hubSheetContext", () => ({ useHubSheet: () => ({ ready: connection.state === "ready" }) }));
vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));

const MAGIC: HubProfile = { id: "hub-1", name: "magic-kingdom", origin: "https://magic-kingdom:9180" };
const PARADISE: HubProfile = { id: "hub-2", name: "paradise-park", origin: "http://100.113.28.18:9180" };

beforeEach(() => {
	connection.profiles = [MAGIC, PARADISE];
	connection.activeProfile = MAGIC;
	connection.selectHub.mockReset();
	connection.removeHub.mockReset();
	connection.updateHub.mockReset();
	connection.state = "ready";
	connection.downSince = null;
	connection.lastLiveAt = null;
});

function mount() {
	const navigation = { navigate: vi.fn(), goBack: vi.fn() };
	const route = { key: "Hubs", name: "Hubs", params: undefined } as const;
	const tree = render(
		<HubsPage
			navigation={navigation as unknown as NativeStackScreenProps<HubRoutes, "Hubs">["navigation"]}
			route={route}
		/>,
	);
	return { tree, navigation };
}

function hubRow(tree: ReactTestRenderer, name: string) {
	return tree.root.find(
		(node) => typeof node.props.accessibilityLabel === "string" && node.props.accessibilityLabel.startsWith(`${name},`),
	);
}

function symbols(node: ReactTestInstance, name: string) {
	return node.findAll((child) => String(child.type) === "SymbolView" && child.props.name === name);
}

function button(tree: ReactTestRenderer, label: string) {
	return tree.root.findByProps({ accessibilityRole: "button", accessibilityLabel: label });
}

function press(tree: ReactTestRenderer, label: string) {
	act(() => {
		button(tree, label).props.onPress();
	});
}

it("lists every saved hub with its address, and checks the selected one", () => {
	const { tree } = mount();
	const text = renderedText(tree);
	expect(text).toContain("Hubs");
	expect(text).toContain("magic-kingdom");
	expect(text).toContain("https://magic-kingdom:9180");
	expect(text).toContain("paradise-park");
	expect(text).toContain("http://100.113.28.18:9180");
	expect(symbols(tree.root, "checkmark")).toHaveLength(1);
	expect(symbols(hubRow(tree, "magic-kingdom"), "checkmark")).toHaveLength(1);
	expect(symbols(hubRow(tree, "paradise-park"), "checkmark")).toHaveLength(0);
});

it("shows each hub's address in Menlo", () => {
	const { tree } = mount();
	for (const line of ["https://magic-kingdom:9180", "http://100.113.28.18:9180"]) {
		const address = tree.root.find((node) => String(node.type) === "Text" && node.props.children === line);
		expect(JSON.stringify(address.props.style)).toContain("Menlo");
	}
});

it("selects another hub and leaves closing the sheet to the sheet", () => {
	const { tree, navigation } = mount();
	act(() => {
		hubRow(tree, "paradise-park").props.onPress();
	});
	expect(connection.selectHub).toHaveBeenCalledWith("hub-2");
	expect(navigation.navigate).not.toHaveBeenCalled();
	expect(navigation.goBack).not.toHaveBeenCalled();
});

it("offers nothing to press on the hub already selected, and tells VoiceOver it's the selected one", () => {
	const { tree } = mount();
	expect(hubRow(tree, "magic-kingdom").props.onPress).toBeUndefined();
	expect(hubRow(tree, "magic-kingdom").props.accessibilityState).toMatchObject({ selected: true });
	expect(hubRow(tree, "paradise-park").props.accessibilityState).toMatchObject({ selected: false });
});

it("opens a hub's details from its info button", () => {
	const { tree, navigation } = mount();
	const details = button(tree, "Details for paradise-park");
	expect(details.props.style).toMatchObject({ width: 44, height: 44 });
	expect(symbols(details, "info.circle")).toHaveLength(1);
	press(tree, "Details for paradise-park");
	expect(navigation.navigate).toHaveBeenCalledWith("HubDetails", { id: "hub-2" });
	press(tree, "Details for magic-kingdom");
	expect(navigation.navigate).toHaveBeenLastCalledWith("HubDetails", { id: "hub-1" });
	expect(connection.selectHub).not.toHaveBeenCalled();
});

it("adds a hub by scanning, pasting or typing its address", () => {
	const { tree, navigation } = mount();
	expect(renderedText(tree)).toContain("Add a hub");
	const ways: [string, string, string | undefined][] = [
		["Scan pairing code", "scan", "qrcode.viewfinder"],
		["Paste pairing link", "paste", "doc.on.clipboard"],
		["Enter the address", "address", undefined],
	];
	for (const [label, how, icon] of ways) {
		if (icon) expect(symbols(button(tree, label), icon)).toHaveLength(1);
		press(tree, label);
		expect(navigation.navigate).toHaveBeenLastCalledWith("AddHub", { how });
	}
});

it("says where the pairing code lives, and never asks to reconnect", () => {
	const { tree } = mount();
	const text = renderedText(tree);
	expect(text).toContain("In Evener on your computer, open Settings, then Mobile app, to show a pairing code.");
	expect(text).not.toMatch(/\bReconnect\b/);
});

function rowOf(tree: ReactTestRenderer, name: string) {
	return tree.root.findAll((node) => node.type === Row && node.props.label === name)[0]?.props;
}

it("says the selected hub is connected, in the UI font beside its address, and says nothing of the others", () => {
	const { tree } = mount();
	expect(rowOf(tree, "magic-kingdom")).toMatchObject({ sub: "https://magic-kingdom:9180", value: "Connected" });
	expect(rowOf(tree, "paradise-park")).toMatchObject({ sub: "http://100.113.28.18:9180", value: undefined });
	const word = tree.root.find((node) => String(node.type) === "Text" && node.props.children === "Connected");
	expect(JSON.stringify(word.props.style)).not.toContain("Menlo");
});

it("says the selected hub's connection state in the words the Hub's header uses", () => {
	vi.useFakeTimers();
	try {
		const now = Date.now();
		connection.state = "reconnecting";
		connection.downSince = now - 5_000;
		connection.lastLiveAt = now;
		const { tree } = mount();
		expect(rowOf(tree, "magic-kingdom")?.value).toBe("Reconnecting…");
		act(() => tree.unmount());
	} finally {
		vi.useRealTimers();
	}
});
