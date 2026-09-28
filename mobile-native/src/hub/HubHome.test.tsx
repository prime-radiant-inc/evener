import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { act } from "react-test-renderer";
import { beforeEach, expect, it, vi } from "vitest";
import { render, renderedText } from "../renderNative.testkit";
import { HubHome } from "./HubHome";
import { type HubRoutes, type HubSheetContextValue, HubSheetProvider } from "./hubSheetContext";

const status = { line: null as string | null };
vi.mock("../board/connectionStatus", async (importOriginal) => ({
	...(await importOriginal<typeof import("../board/connectionStatus")>()),
	useConnectionStatusText: () => status.line,
}));
vi.mock("../ConnectionProvider", () => ({ useConnection: () => ({ state: "ready", fatal: false }) }));
vi.mock("@react-navigation/native", () => ({
	StackActions: {
		replace: (name: string, params: unknown) => ({ type: "REPLACE", payload: { name, params } }),
	},
}));
vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));

let context: HubSheetContextValue;
const liveContext: HubSheetContextValue = {
	hubId: "hub-1",
	hubName: "Work hub",
	client: null,
	ready: true,
	canUseConnection: () => true,
};

function mount() {
	const root = { dispatch: vi.fn(), navigate: vi.fn(), goBack: vi.fn() };
	const navigation = { getParent: () => root } as unknown as NativeStackScreenProps<HubRoutes, "HubHome">["navigation"];
	const route = { key: "HubHome", name: "HubHome", params: { hubId: "hub-1" } } as const;
	const tree = render(
		<HubSheetProvider value={context}>
			<HubHome navigation={navigation} route={route} />
		</HubSheetProvider>,
	);
	const press = (label: string) =>
		act(() => {
			tree.root.findByProps({ accessibilityRole: "button", accessibilityLabel: label }).props.onPress();
		});
	const disabled = (label: string) =>
		tree.root.findByProps({ accessibilityRole: "button", accessibilityLabel: label }).props.disabled;
	return { tree, root, press, disabled };
}

const ROWS = ["Providers", "Plugins", "Display", "Hubs", "Hub settings"];

beforeEach(() => {
	status.line = null;
	context = liveContext;
});

it("says the hub is connected and lists its pages", () => {
	const { tree } = mount();
	expect(renderedText(tree)).toContain("Connected");
	for (const label of ROWS) expect(tree.root.findAllByProps({ accessibilityLabel: label })).not.toHaveLength(0);
});

it("leaves the sheet for today's screens until their pages land (ruling 10)", () => {
	const { root, press } = mount();
	const interim: [string, string][] = [
		["Providers", "Providers"],
		["Plugins", "Plugins"],
		["Display", "TranscriptPreferences"],
		["Hub settings", "HubSettings"],
	];
	for (const [label, screen] of interim) {
		press(label);
		expect(root.dispatch).toHaveBeenLastCalledWith({
			type: "REPLACE",
			payload: { name: screen, params: { hubId: "hub-1" } },
		});
	}
	press("Hubs");
	expect(root.navigate).toHaveBeenLastCalledWith("Hubs");
});

it("keeps every row, pressable, while the connection is down, and never asks to reconnect (Review Focus 4)", () => {
	status.line = "Reconnecting…";
	const { tree, root, press, disabled } = mount();
	expect(renderedText(tree)).toContain("Reconnecting…");
	expect(renderedText(tree)).not.toMatch(/\bReconnect\b/);
	for (const label of ROWS) expect(disabled(label)).toBe(false);
	press("Providers");
	expect(root.dispatch).toHaveBeenCalledTimes(1);
});

it("says Connecting… rather than Connected while the hub isn't ready and the line is still quiet", () => {
	context = { ...liveContext, ready: false };
	const { tree } = mount();
	expect(renderedText(tree)).toContain("Connecting…");
	expect(renderedText(tree)).not.toContain("Connected");
});
