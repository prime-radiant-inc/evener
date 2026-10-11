// The Hub sheet's own navigation: what it does when the hub it was opened for
// stops being the selected one (Review Focus 5).
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { act } from "react-test-renderer";
import { beforeEach, expect, it, vi } from "vitest";
import type { HubProfile } from "../connection";
import { render } from "../renderNative.testkit";
import type { Routes } from "../screens";
import { HubSheet } from "./HubSheet";

const connection = vi.hoisted(() => ({ activeProfile: null as HubProfile | null }));
vi.mock("../ConnectionProvider", () => ({ useConnection: () => connection }));
vi.mock("../retainedScreen", () => ({
	useRetainedScreenConnection: () => ({ state: "ready", canUseConnection: () => true, renderClient: null }),
}));
vi.mock("./hubUpdates", () => ({ useHubUpdates: () => ({}) }));
// The pages render inside the navigator, which isn't drawn here.
vi.mock("./HubHome", () => ({ HubHome: () => null }));
vi.mock("./HubsPage", () => ({ HubsPage: () => null }));
vi.mock("./HubDetailsPage", () => ({ HubDetailsPage: () => null }));
vi.mock("./AddHubPage", () => ({ AddHubPage: () => null }));
vi.mock("./ProvidersPage", () => ({ ProvidersPage: () => null }));
vi.mock("./ProviderDetailPage", () => ({ ProviderDetailPage: () => null }));
vi.mock("./HostsPage", () => ({ HostsPage: () => null }));
vi.mock("./PluginsPage", () => ({ PluginsPage: () => null }));
vi.mock("./MarketplacePage", () => ({ MarketplacePage: () => null }));
vi.mock("./DisplayPage", () => ({ DisplayPage: () => null }));
vi.mock("./DetailLevelPage", () => ({ DetailLevelPage: () => null }));
vi.mock("./HostDetailPage", () => ({ HostDetailPage: () => null }));
vi.mock("./HostEditPage", () => ({ HostEditPage: () => null }));
vi.mock("./OwnHostPage", () => ({ OwnHostPage: () => null }));
vi.mock("../KeybindingPreferencesScreen", () => ({ KeybindingPreferencesScreen: () => null }));
vi.mock("../LaunchSettingsScreen", () => ({ LaunchSettingsScreen: () => null }));
vi.mock("../HubSettingsScreen", () => ({ HubSettingsScreen: () => null }));
vi.mock("expo-crypto", () => ({ randomUUID: () => "hub-sheet-uuid" }));
vi.mock("../hosts/useHubFleet", () => ({ useHubFleet: () => ({}) }));
vi.mock("@react-navigation/native-stack", () => ({
	createNativeStackNavigator: () => ({ Navigator: () => null, Screen: () => null }),
}));
vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));

const MAGIC: HubProfile = { id: "hub-1", name: "magic-kingdom", origin: "https://magic-kingdom:9180" };
const PARADISE: HubProfile = { id: "hub-2", name: "paradise-park", origin: "http://100.113.28.18:9180" };

function mount() {
	const navigation = { goBack: vi.fn(), navigate: vi.fn(), reset: vi.fn() };
	const route = { key: "Hub", name: "Hub", params: undefined } as const;
	const props = {
		navigation: navigation as unknown as NativeStackScreenProps<Routes, "Hub">["navigation"],
		route: route as unknown as NativeStackScreenProps<Routes, "Hub">["route"],
	};
	const tree = render(<HubSheet {...props} />);
	const rerender = () =>
		act(() => {
			tree.update(<HubSheet {...props} />);
		});
	return { navigation, rerender };
}

beforeEach(() => {
	connection.activeProfile = MAGIC;
});

it("leaves the Board behind for the first-run screen when the selected hub is removed", () => {
	// React Navigation 7's navigate pushes a screen that's already in the
	// stack, so a navigate("Hubs") showed first run inside this sheet, over a
	// stale Board (seen on the simulator). The root stack is reset instead.
	const { navigation, rerender } = mount();
	connection.activeProfile = null;
	rerender();
	expect(navigation.reset).toHaveBeenCalledWith({ index: 0, routes: [{ name: "Hubs" }] });
	expect(navigation.navigate).not.toHaveBeenCalled();
	expect(navigation.goBack).not.toHaveBeenCalled();
});

it("closes onto the Board when another hub is selected", () => {
	const { navigation, rerender } = mount();
	connection.activeProfile = PARADISE;
	rerender();
	expect(navigation.goBack).toHaveBeenCalledTimes(1);
	expect(navigation.reset).not.toHaveBeenCalled();
});
