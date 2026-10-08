// A notice's link while the Hub is open (openNotice, BoardNotices.tsx). With
// the Hub the root stack's top, a link reaches the Hub's own stack through a
// guarded dispatch: a StackActions.popTo targeted at the Hub navigator, so a
// page it pops runs its beforeRemove guard and asks first, rather than being
// dropped silently or covered by a second list page (#3524). This pins, on the
// Hub's own shape with React Navigation's real core (as
// HubSheet.unsavedEdit.test.tsx does) and the real provider detail, that a
// guarded page is never dropped by a link: the host notice's, the sign-in
// notice's, or a sign-in error's, and that a detail that does let go pops to the
// one page the link names.
import {
	BaseNavigationContainer,
	type NavigationContainerRef,
	type ParamListBase,
	usePreventRemove,
} from "@react-navigation/core";
import { createRef } from "react";
import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import { ProvidersScreenSlotProvider, usePublishProviderDetail } from "../hub/hubScreenSlot";
import { ProviderDetailPage } from "../hub/ProviderDetailPage";
import { createTestStack } from "../navigationTestStack.testkit";
import { render } from "../renderNative.testkit";
import { openNotice, openProviders } from "./BoardNotices";

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));
// The real detail reads its guard from the native package, which vitest cannot
// render; its usePreventRemove is core's.
vi.mock("@react-navigation/native", async () => ({
	usePreventRemove: (await import("@react-navigation/core")).usePreventRemove,
}));

const Root = createTestStack();
const Hub = createTestStack();

/** Every guard callback a link ran: the "ask" a page holding unsaved input
 * makes when a link would pop it. A test clears it before the link it reads. */
const asked = vi.fn();
/** Whether the next ask lets the removal through, as confirming it would. */
let letGo = false;

// A page holding unsaved input (a host edit): its guard holds any removal that
// runs through beforeRemove, so the link's popTo is prevented and asks first.
function GuardedHostEdit() {
	usePreventRemove(true, () => asked());
	return null;
}

// A Providers page: it selects the provider its route focuses and holds a
// pasted key for it, as the real page publishes its selected detail.
function Providers({ route }: { route: { params: { hubId: string; focus?: string } } }) {
	const name = route.params.focus ?? null;
	usePublishProviderDetail({
		hubId: route.params.hubId,
		name,
		detail: `${name}'s detail, from the page focused on ${name}`,
		guarded: name !== null,
		leave: (then) => {
			asked();
			if (letGo) then();
		},
		onGone: () => {},
	});
	return null;
}

// A page whose content the test never reads. It is declared once, here: a
// component written inline in a Screen is a new type on every render.
function Blank() {
	return null;
}

function HubSheet() {
	return (
		<ProvidersScreenSlotProvider>
			<Hub.Navigator initialRouteName="HubHome">
				<Hub.Screen name="HubHome" component={Blank} />
				<Hub.Screen name="Hosts" component={Blank} />
				<Hub.Screen name="HostDetail" component={Blank} />
				<Hub.Screen name="HostEdit" component={GuardedHostEdit} />
				<Hub.Screen name="Providers" component={Providers as never} />
				<Hub.Screen name="ProviderDetail" component={ProviderDetailPage as never} />
			</Hub.Navigator>
		</ProvidersScreenSlotProvider>
	);
}

type Page = { screen: string; params: object };

/** The app with the Hub open at `pages` (after its home), and the Hub's stack. */
async function hubOpenAt(pages: Page[]) {
	const navigation = createRef<NavigationContainerRef<ParamListBase>>();
	render(
		<BaseNavigationContainer ref={navigation}>
			<Root.Navigator initialRouteName="Board">
				<Root.Screen name="Board" component={Blank} />
				<Root.Screen name="Hub" component={HubSheet} />
			</Root.Navigator>
		</BaseNavigationContainer>,
	);
	await act(async () => {});
	// Open the Hub at its home, so its stack holds HubHome under each page the
	// test pushes over it.
	await act(async () =>
		navigation.current?.navigate("Hub", { screen: "HubHome", params: { hubId: "hub-1" }, initial: false }),
	);
	for (const page of pages)
		await act(async () => navigation.current?.navigate("Hub", { screen: page.screen, params: page.params }));
	const hubRoutes = () => {
		const routes = navigation.current?.getRootState()?.routes.at(-1)?.state?.routes;
		if (!routes) throw new Error("the Hub has no stack yet");
		return routes;
	};
	const hub = () => hubRoutes().map((entry) => entry.name);
	expect(hub()).toEqual(["HubHome", ...pages.map((page) => page.screen)]);
	// The Board's navigation object, as openNotice and openProviders get it.
	return { links: navigation.current as never, hub, hubRoutes };
}

/** A provider detail pushed over its page, both real: the page selects `work`
 * and holds its pasted key, so the pushed detail is guarded. */
const pushedProviderDetail: Page[] = [
	{ screen: "Providers", params: { hubId: "hub-1", focus: "work" } },
	{ screen: "ProviderDetail", params: { hubId: "hub-1", name: "work" } },
];

it("never drops an unsaved host edit when a host notice's link arrives", async () => {
	const { links, hub } = await hubOpenAt([
		{ screen: "Hosts", params: { hubId: "hub-1" } },
		{ screen: "HostDetail", params: { hubId: "hub-1", name: "studio" } },
		{ screen: "HostEdit", params: { hubId: "hub-1", name: "studio" } },
	]);
	asked.mockClear();
	await act(async () =>
		openNotice(links, "hub-1", {
			key: "host:studio",
			text: "Studio Mac is offline",
			kind: "host",
			action: "Details",
			sourceId: "studio",
		}),
	);
	// The edit's guard held the popTo: the edit is still there, and it asked.
	expect(asked).toHaveBeenCalledTimes(1);
	expect(hub()).toEqual(["HubHome", "Hosts", "HostDetail", "HostEdit"]);
});

it("never drops a provider detail holding a pasted key when a sign-in notice's link arrives", async () => {
	const { links, hub } = await hubOpenAt(pushedProviderDetail);
	asked.mockClear();
	await act(async () =>
		openNotice(links, "hub-1", {
			key: "signIn:codex",
			text: "codex sign-in expired",
			kind: "signIn",
			action: "Sign in",
			providerId: "codex",
		}),
	);
	// One Providers page, not two: the pushed detail held the popTo, which asked.
	expect(asked).toHaveBeenCalledTimes(1);
	expect(hub()).toEqual(["HubHome", "Providers", "ProviderDetail"]);
});

it("never drops a provider detail holding a pasted key when a sign-in error opens Providers", async () => {
	const { links, hub } = await hubOpenAt(pushedProviderDetail);
	asked.mockClear();
	await act(async () => openProviders(links, "hub-1"));
	// One Providers page, not two: the pushed detail held the popTo, which asked.
	expect(asked).toHaveBeenCalledTimes(1);
	expect(hub()).toEqual(["HubHome", "Providers", "ProviderDetail"]);
});

it("pops a detail that lets go to the one page the link names, showing its provider", async () => {
	const { links, hub, hubRoutes } = await hubOpenAt(pushedProviderDetail);
	letGo = true;
	asked.mockClear();
	await act(async () => openProviders(links, "hub-1"));
	letGo = false;
	// The link's popTo landed on the Providers page the link named, with the
	// link's params: one Providers page, not a second stacked over the detail.
	expect(asked).toHaveBeenCalledTimes(1);
	expect(hub()).toEqual(["HubHome", "Providers"]);
	expect(hubRoutes().find((route) => route.name === "Providers")?.params).toEqual({ hubId: "hub-1" });
});

it("opens the page a link names over the Hub's home, keeping the home under it", async () => {
	const { links, hub, hubRoutes } = await hubOpenAt([]);
	await act(async () => openProviders(links, "hub-1"));
	// The Hub holds no Providers page, so the link is a push, not a popTo: a
	// popTo with no page to land on would drop the Hub's home.
	expect(hub()).toEqual(["HubHome", "Providers"]);
	expect(hubRoutes().find((route) => route.name === "Providers")?.params).toEqual({ hubId: "hub-1" });
});

it("keeps the Hub's home and the branch it is on when a link names a page they do not hold", async () => {
	const { links, hub } = await hubOpenAt([{ screen: "Hosts", params: { hubId: "hub-1" } }]);
	asked.mockClear();
	await act(async () => openProviders(links, "hub-1"));
	// Nothing is popped, so no guard runs, and the home and Hosts stay under
	// the page the link pushed.
	expect(asked).not.toHaveBeenCalled();
	expect(hub()).toEqual(["HubHome", "Hosts", "Providers"]);
});
