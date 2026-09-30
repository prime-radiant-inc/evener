import { createHubUpdateController } from "@evener/appwire-client";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { act } from "react-test-renderer";
import { beforeEach, expect, it, vi } from "vitest";
import { DisplayProvider } from "../display/displayContext";
import { DISPLAY_KEY, DisplayPreferences } from "../display/displayPreferences";
import type { NativePreferencesSnapshot } from "../nativePreferences";
import { render, renderedText } from "../renderNative.testkit";
import { Group } from "../sheet/Grouped";
import { DisplayPage } from "./DisplayPage";
import { type HubRoutes, type HubSheetContextValue, HubSheetProvider } from "./hubSheetContext";

const preferences = vi.hoisted(() => ({ value: {} as Record<string, unknown> }));
vi.mock("../NativePreferencesProvider", () => ({ useNativePreferences: () => preferences.value }));
vi.mock("../board/connectionStatus", async (importOriginal) => ({
	...(await importOriginal<typeof import("../board/connectionStatus")>()),
	useConnectionStatusText: () => null,
}));
vi.mock("../ConnectionProvider", () => ({ useConnection: () => ({ state: "ready", fatal: false }) }));
vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));

const CONFIG = {
	version: 1 as const,
	content: { kind: "preset" as const, level: "intent" as const },
	advanced: {
		roundTimings: false,
		tokenCounts: false,
		estimatedCost: false,
		systemEvents: false,
		promptEvents: false,
		hookExits: "none" as const,
	},
};

function transcript(
	over: Partial<NativePreferencesSnapshot["transcriptMobile"]> = {},
): NativePreferencesSnapshot["transcriptMobile"] {
	return {
		support: "supported",
		loading: false,
		saving: false,
		confirmed: { revision: 3, config: CONFIG },
		draft: null,
		error: null,
		conflict: false,
		writeUncertain: false,
		storageUnavailable: false,
		draftUnreadable: false,
		...over,
	};
}

const context: HubSheetContextValue = {
	hubId: "hub-1",
	hubName: "Work hub",
	client: null,
	ready: true,
	canUseConnection: () => true,
	updates: createHubUpdateController({
		client: () => {
			throw new Error("not in this test");
		},
		awaitRestart: async () => true,
	}),
	hosts: null,
	live: null,
};

function mount(state = transcript(), storage = new Map<string, string>()) {
	preferences.value = { model: {}, snapshot: { transcriptMobile: state }, connected: true };
	const prefs = new DisplayPreferences({
		getItemSync: (key) => storage.get(key) ?? null,
		setItemSync: (key, value) => {
			storage.set(key, value);
		},
	});
	const navigation = { navigate: vi.fn() };
	const tree = render(
		<DisplayProvider value={prefs}>
			<HubSheetProvider value={context}>
				<DisplayPage
					navigation={navigation as unknown as NativeStackScreenProps<HubRoutes, "Display">["navigation"]}
					route={{ key: "Display", name: "Display", params: { hubId: "hub-1" } }}
				/>
			</HubSheetProvider>
		</DisplayProvider>,
	);
	const choose = (label: string) =>
		act(() => {
			tree.root.findByProps({ accessibilityRole: "radio", accessibilityLabel: label }).props.onPress();
		});
	return { tree, navigation, prefs, storage, choose };
}

beforeEach(() => {
	preferences.value = {};
});

it("stores a dark appearance for this phone", () => {
	const { choose, storage, prefs } = mount();
	choose("Dark");
	expect(prefs.getSnapshot().appearance).toBe("dark");
	expect(JSON.parse(storage.get(DISPLAY_KEY) ?? "{}")).toMatchObject({ appearance: "dark" });
});

it("stores the Sans reading font and says what it applies to", () => {
	const { choose, storage, tree } = mount();
	choose("Sans");
	expect(JSON.parse(storage.get(DISPLAY_KEY) ?? "{}")).toMatchObject({ readingFont: "sans" });
	expect(renderedText(tree)).toContain("For what agents write: messages, plans and documents.");
});

it("says a choice applies now but won't survive a restart when the phone can't store it", () => {
	const storage = new Map<string, string>();
	const { tree, prefs } = mount(transcript(), storage);
	prefs.set = ((set) => (change: Parameters<typeof set>[0]) => {
		set(change);
		throw new Error("disk full");
	})(prefs.set.bind(prefs));
	act(() => {
		tree.root.findByProps({ accessibilityRole: "radio", accessibilityLabel: "Dark" }).props.onPress();
	});
	expect(renderedText(tree)).toContain("This choice applies now but couldn't be saved on this phone.");
});

it("names the hub's default detail level, or Custom, and opens its page", () => {
	const intent = mount();
	const row = intent.tree.root.findByProps({
		accessibilityRole: "button",
		accessibilityLabel: "Default detail level, Intent",
	});
	expect(renderedText(intent.tree)).toContain("Each session can override this from its menu.");
	act(() => row.props.onPress());
	expect(intent.navigation.navigate).toHaveBeenCalledWith("DetailLevel", { hubId: "hub-1" });
	const custom = mount(
		transcript({
			confirmed: {
				revision: 3,
				config: {
					...CONFIG,
					content: {
						kind: "custom",
						toolIntent: true,
						toolCalls: false,
						reasoning: false,
						expandByDefault: false,
					} as never,
				},
			},
		}),
	);
	expect(
		custom.tree.root.findAllByProps({ accessibilityLabel: "Default detail level, Custom" }).length,
	).toBeGreaterThan(0);
});

it("names the default detail level once: its row says it, with no label repeating it above (audit M12)", () => {
	const { tree } = mount();
	expect(tree.root.findAll((node) => node.type === Group && node.props.label === "Default detail level")).toHaveLength(
		0,
	);
	expect(renderedText(tree).match(/Default detail level/g)).toHaveLength(1);
});

// Absence is the signal (spec 14): a hub that keeps no default detail level
// leaves no label or sentence behind (audit N5).
it("shows nothing about a default detail level when the hub doesn't keep one", () => {
	const { tree } = mount(transcript({ support: "unsupported" }));
	expect(renderedText(tree)).not.toMatch(/default detail level/i);
	expect(tree.root.findAllByProps({ accessibilityLabel: "Default detail level, Intent" })).toHaveLength(0);
});

// S17 put the model's display name on navigation summaries, so the toggle
// now changes something (spec 12, ruling 8's condition met; audit N5).
it("offers Show model on Board rows, off at first, and stores it for this phone", () => {
	const { tree, storage, prefs } = mount();
	const toggle = tree.root.findByProps({ accessibilityLabel: "Show model on Board rows", value: false });
	act(() => toggle.props.onValueChange(true));
	expect(prefs.getSnapshot().showModel).toBe(true);
	expect(JSON.parse(storage.get(DISPLAY_KEY) ?? "{}")).toMatchObject({ showModel: true });
	expect(tree.root.findByProps({ accessibilityLabel: "Show model on Board rows" }).props.value).toBe(true);
});

it("never asks to reconnect or refresh", () => {
	const { tree } = mount();
	expect(renderedText(tree)).not.toMatch(/\bReconnect\b|\bRefresh\b|transcript display/i);
});

it("names the level the hub has saved, not a change still waiting to save", () => {
	const draft = { revision: 3, config: { ...CONFIG, content: { kind: "preset" as const, level: "full" as const } } };
	const { tree } = mount(transcript({ draft }));
	expect(tree.root.findAllByProps({ accessibilityLabel: "Default detail level, Intent" }).length).toBeGreaterThan(0);
});
