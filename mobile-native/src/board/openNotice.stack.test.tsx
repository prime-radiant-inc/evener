// A notice's link while the Hub is open (openNotice, BoardNotices.tsx). The
// link names no `pop`, because React Navigation applies a nested navigate's
// `pop` without beforeRemove, so a pushed page's leave guard never runs and
// an unsaved edit would go silently. Without it, the link stacks a second
// Hosts page over the edit, which is the accepted trade-off: no one can tap a
// notice over the Hub today (#3524). This pins, on the Hub's own shape with
// React Navigation's real core (as HubSheet.unsavedEdit.test.tsx does), that
// a guarded page is never dropped by a link: the host notice's, the sign-in
// notice's, or a sign-in error's.
import {
	BaseNavigationContainer,
	type NavigationContainerRef,
	type ParamListBase,
	usePreventRemove,
} from "@react-navigation/core";
import { createRef } from "react";
import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import { createTestStack } from "../navigationTestStack.testkit";
import { render } from "../renderNative.testkit";
import { openNotice, openProviders } from "./BoardNotices";

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));

const Root = createTestStack();
const Hub = createTestStack();

// A page holding unsaved input (a host edit, a provider detail with a pasted
// key): its guard holds any removal that runs through beforeRemove. The
// callback does nothing; the stack's shape after the link is the assertion.
function Guarded() {
	usePreventRemove(true, () => {});
	return null;
}

function HubSheet() {
	return (
		<Hub.Navigator initialRouteName="HubHome">
			<Hub.Screen name="HubHome" component={() => null} />
			<Hub.Screen name="Hosts" component={() => null} />
			<Hub.Screen name="HostDetail" component={() => null} />
			<Hub.Screen name="HostEdit" component={Guarded} />
			<Hub.Screen name="Providers" component={() => null} />
			<Hub.Screen name="ProviderDetail" component={Guarded} />
		</Hub.Navigator>
	);
}

/** The app with the Hub open at `pages` (after its home), and the Hub's stack. */
async function hubOpenAt(pages: string[]) {
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
	const [first, ...rest] = pages;
	await act(async () => navigation.current?.navigate("Hub", { screen: first, params: { hubId: "hub-1" }, initial: false }));
	for (const page of rest) {
		await act(async () => navigation.current?.navigate("Hub", { screen: page, params: { name: "x" } }));
	}
	const hub = () => {
		const routes = navigation.current?.getRootState()?.routes.at(-1)?.state?.routes;
		if (!routes) throw new Error("the Hub has no stack yet");
		return routes.map((entry) => entry.name);
	};
	expect(hub()).toEqual(["HubHome", ...pages]);
	// The Board's navigation object, as openNotice and openProviders get it.
	return { links: navigation.current as never, hub };
}

it("never drops an unsaved host edit when a host notice's link arrives", async () => {
	const { links, hub } = await hubOpenAt(["Hosts", "HostDetail", "HostEdit"]);
	await act(async () =>
		openNotice(links, "hub-1", {
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

it("never drops a provider detail holding a pasted key when a sign-in notice's link arrives", async () => {
	const { links, hub } = await hubOpenAt(["Providers", "ProviderDetail"]);
	await act(async () =>
		openNotice(links, "hub-1", {
			key: "signIn:codex",
			text: "codex sign-in expired",
			kind: "signIn",
			action: "Sign in",
			providerId: "codex",
		}),
	);
	expect(hub()).toEqual(["HubHome", "Providers", "ProviderDetail", "Providers"]);
});

it("never drops a provider detail holding a pasted key when a sign-in error opens Providers", async () => {
	const { links, hub } = await hubOpenAt(["Providers", "ProviderDetail"]);
	await act(async () => openProviders(links, "hub-1"));
	expect(hub()).toEqual(["HubHome", "Providers", "ProviderDetail", "Providers"]);
});
