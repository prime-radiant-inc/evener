// Dismissing the Hub sheet while a page pushed inside it holds an unsaved edit
// (Launch defaults' usePreventRemove) must ask first, the way leaving that page
// by its own Back does. React Navigation carries a nested screen's prevention
// up to the route that holds its navigator: the root stack treats the Hub
// sheet's route as prevented, so removing it runs the nested page's callback
// instead. This pins that on the Hub's own shape (the sheet's stack nested in
// the root stack's Hub route) with React Navigation's real core; the native
// swipe then reaches the same removal through native-stack's
// preventNativeDismiss, which vitest cannot drive.
import {
	BaseNavigationContainer,
	type NavigationContainerRef,
	type ParamListBase,
	StackActions,
	usePreventRemove,
} from "@react-navigation/core";
import { createRef } from "react";
import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import { createTestStack } from "../navigationTestStack.testkit";
import { render } from "../renderNative.testkit";

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));

const Root = createTestStack();
const Hub = createTestStack();

const asked = vi.fn();

function UnsavedLaunchDefaults() {
	usePreventRemove(true, () => asked());
	return null;
}

function HubSheet() {
	return (
		<Hub.Navigator initialRouteName="LaunchSettings">
			<Hub.Screen name="HubHome" component={() => null} />
			<Hub.Screen name="LaunchSettings" component={UnsavedLaunchDefaults} />
		</Hub.Navigator>
	);
}

it("asks before the Hub sheet goes while a page inside it holds an unsaved edit", async () => {
	const navigation = createRef<NavigationContainerRef<ParamListBase>>();
	render(
		<BaseNavigationContainer ref={navigation}>
			<Root.Navigator initialRouteName="Sessions">
				<Root.Screen name="Sessions" component={() => null} />
				<Root.Screen name="Hub" component={HubSheet} />
			</Root.Navigator>
		</BaseNavigationContainer>,
	);
	await act(async () => {});
	await act(async () => navigation.current?.navigate("Hub"));
	const root = () => {
		const state = navigation.current?.getRootState();
		if (!state) throw new Error("the container has no state");
		return state;
	};
	// The sheet's route is the root's top; dismissing it pops that route.
	const hubKey = root().routes.at(-1)?.key;
	await act(async () => {
		navigation.current?.dispatch({ ...StackActions.pop(), source: hubKey, target: root().key });
	});
	expect(asked).toHaveBeenCalledTimes(1);
	expect(root().routes.map((route) => route.name)).toEqual(["Sessions", "Hub"]);
});
