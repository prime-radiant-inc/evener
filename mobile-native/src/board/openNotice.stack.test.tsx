// A notice's link while the Hub is open (openNotice, BoardNotices.tsx). The
// link names no `pop`, because React Navigation applies a nested navigate's
// `pop` without beforeRemove, so a pushed page's leave guard never runs and
// an unsaved edit would go silently. Without it, the link stacks a second
// Hosts page over the edit, which is the accepted trade-off: no one can tap a
// notice over the Hub today (#3524). This pins, on the Hub's own shape with
// React Navigation's real core (as HubSheet.unsavedEdit.test.tsx does), that
// a guarded page is never dropped by a link.
import {
	BaseNavigationContainer,
	createNavigatorFactory,
	type NavigationContainerRef,
	type ParamListBase,
	StackRouter,
	useNavigationBuilder,
	usePreventRemove,
} from "@react-navigation/core";
import { createRef, Fragment, type ReactNode } from "react";
import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import { render } from "../renderNative.testkit";
import { openNotice } from "./BoardNotices";

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));

/** A stack that renders every route it holds, as a native stack keeps the
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

// An edit with unsaved input: its guard holds any removal React Navigation
// runs through beforeRemove.
function UnsavedHostEdit() {
	usePreventRemove(true, () => {});
	return null;
}

function HubSheet() {
	return (
		<Hub.Navigator initialRouteName="HubHome">
			<Hub.Screen name="HubHome" component={() => null} />
			<Hub.Screen name="Hosts" component={() => null} />
			<Hub.Screen name="HostDetail" component={() => null} />
			<Hub.Screen name="HostEdit" component={UnsavedHostEdit} />
		</Hub.Navigator>
	);
}

it("never drops an unsaved host edit when a host notice's link arrives", async () => {
	const navigation = createRef<NavigationContainerRef<ParamListBase>>();
	render(
		<BaseNavigationContainer ref={navigation}>
			<Root.Navigator initialRouteName="Board">
				<Root.Screen name="Board" component={() => null} />
				<Root.Screen name="Hub" component={HubSheet} />
			</Root.Navigator>
		</BaseNavigationContainer>,
	);
	await act(async () => {});
	const hub = () => {
		const route = navigation.current?.getRootState()?.routes.at(-1);
		const routes = route?.state?.routes;
		if (!routes) throw new Error("the Hub has no stack yet");
		return routes.map((entry) => entry.name);
	};
	await act(async () =>
		navigation.current?.navigate("Hub", { screen: "Hosts", params: { hubId: "hub-1" }, initial: false }),
	);
	await act(async () => navigation.current?.navigate("Hub", { screen: "HostDetail", params: { name: "studio" } }));
	await act(async () => navigation.current?.navigate("Hub", { screen: "HostEdit", params: { name: "studio" } }));
	expect(hub()).toEqual(["HubHome", "Hosts", "HostDetail", "HostEdit"]);

	await act(async () =>
		openNotice(navigation.current as never, "hub-1", {
			key: "host:studio",
			text: "Studio Mac is offline",
			kind: "host",
			action: "Details",
			sourceId: "studio",
		}),
	);
	// The edit is still in the stack, under the Hosts page the link opened.
	expect(hub()).toEqual(["HubHome", "Hosts", "HostDetail", "HostEdit", "Hosts"]);
});
