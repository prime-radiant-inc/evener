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
	createNavigatorFactory,
	type NavigationContainerRef,
	type ParamListBase,
	StackActions,
	StackRouter,
	useNavigationBuilder,
	usePreventRemove,
} from "@react-navigation/core";
import { createRef, Fragment, type ReactNode } from "react";
import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import { render } from "../renderNative.testkit";

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));

/** A stack that renders every route it holds, like a native stack keeps the
 * screens under the top one mounted. */
function TestStack({ children, initialRouteName }: { children: ReactNode; initialRouteName?: string }) {
	const { state, descriptors, NavigationContent } = useNavigationBuilder(StackRouter, { children, initialRouteName });
	return (
		<NavigationContent>
			{state.routes.map((route) => (
				<Fragment key={route.key}>{descriptors[route.key]?.render()}</Fragment>
			))}
		</NavigationContent>
	);
}
const createTestStack = createNavigatorFactory(TestStack);
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
	// The sheet's route is the root's top; dismissing it pops that route.
	const hubKey = navigation.current?.getRootState().routes.at(-1)?.key;
	await act(async () => {
		navigation.current?.dispatch({
			...StackActions.pop(),
			source: hubKey,
			target: navigation.current?.getRootState().key,
		});
	});
	expect(asked).toHaveBeenCalledTimes(1);
	expect(navigation.current?.getRootState().routes.map((route) => route.name)).toEqual(["Sessions", "Hub"]);
});
