import { BaseNavigationContainer, type NavigationContainerRef, type ParamListBase } from "@react-navigation/core";
import type { NativeStackNavigationOptions, NativeStackScreenProps } from "@react-navigation/native-stack";
import { createRef } from "react";
import { act } from "react-test-renderer";
import { beforeEach, expect, it, vi } from "vitest";
import type { HubProfile } from "../connection";
import { render } from "../renderNative.testkit";
import type { Routes } from "../screens";
import { HubSheet } from "./HubSheet";

const connection = vi.hoisted(() => ({
	profiles: [] as HubProfile[],
	activeProfile: null as HubProfile | null,
	state: "ready",
}));
vi.mock("../ConnectionProvider", () => ({ useConnection: () => connection }));
vi.mock("./HubDetailsPage", () => ({ HubDetailsPage: () => null }));
vi.mock("./AddHubPage", () => ({ AddHubPage: () => null }));
vi.mock("@react-navigation/native-stack", async () => ({
	createNativeStackNavigator: (await import("../navigationTestStack.testkit")).createTestStack,
}));
vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));

const MAGIC: HubProfile = { id: "hub-1", name: "magic-kingdom", origin: "https://magic-kingdom:9180" };
const PARADISE: HubProfile = { id: "hub-2", name: "paradise-park", origin: "http://100.113.28.18:9180" };

function mount() {
	const navigation = { goBack: vi.fn(), navigate: vi.fn(), reset: vi.fn() };
	const route = { key: "Hub", name: "Hub", params: undefined } as const;
	const props = {
		navigation: navigation as unknown as NativeStackScreenProps<Routes, "Hub">["navigation"],
		route: route as unknown as NativeStackScreenProps<Routes, "Hub">["route"],
	};
	const stack = createRef<NavigationContainerRef<ParamListBase>>();
	const sheet = () => (
		<BaseNavigationContainer ref={stack}>
			<HubSheet {...props} />
		</BaseNavigationContainer>
	);
	const tree = render(sheet());
	const rerender = () =>
		act(() => {
			tree.update(sheet());
		});
	return { navigation, rerender, stack };
}

beforeEach(() => {
	connection.profiles = [MAGIC, PARADISE];
	connection.activeProfile = MAGIC;
});

it("opens the saved Hubs list with pairing and profile details as its only destinations", () => {
	const { stack, navigation } = mount();
	expect(stack.current?.getCurrentRoute()?.name).toBe("Hubs");
	expect(new Set(stack.current?.getRootState()?.routeNames)).toEqual(new Set(["Hubs", "AddHub", "HubDetails"]));
	const options = stack.current?.getCurrentOptions() as NativeStackNavigationOptions;
	const done = render(<>{options.headerRight?.({ canGoBack: false })}</>);
	act(() => done.root.findByProps({ accessibilityRole: "button" }).props.onPress());
	expect(navigation.goBack).toHaveBeenCalledOnce();
});

it("leaves the workspace for the first-run screen when the selected hub is removed", () => {
	const { navigation, rerender } = mount();
	connection.activeProfile = null;
	rerender();
	expect(navigation.reset).toHaveBeenCalledWith({ index: 0, routes: [{ name: "Hubs" }] });
	expect(navigation.navigate).not.toHaveBeenCalled();
	expect(navigation.goBack).not.toHaveBeenCalled();
});

it("closes onto the workspace when another hub is selected", () => {
	const { navigation, rerender } = mount();
	connection.activeProfile = PARADISE;
	rerender();
	expect(navigation.goBack).toHaveBeenCalledTimes(1);
	expect(navigation.reset).not.toHaveBeenCalled();
});
