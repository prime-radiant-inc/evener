import { BaseNavigationContainer, createNavigationContainerRef } from "@react-navigation/core";
import { act } from "react-test-renderer";
import { beforeEach, expect, it, vi } from "vitest";
import { onBoardJump } from "../board/boardJump";
import type { Notice } from "../board/notices";
import { BlankPage, createTestStack } from "../navigationTestStack.testkit";
import { alertRequests, render } from "../renderNative.testkit";
import type { Routes } from "../screens";
import { AlertCenter, type Alert } from "./alertCenter";
import type { OpenAlertTarget } from "./openAlertTarget";

const harness = vi.hoisted(() => ({
	platform: "ios",
	center: null as AlertCenter | null,
	notices: new Map<string, unknown>(),
	opened: [] as unknown[][],
	selected: [] as string[],
	// The selected hub, or none (disconnected, or the selected one removed).
	active: { id: "hub-1", name: "magic-kingdom" } as { id: string; name: string } | null,
}));
vi.mock("react-native", async () => {
	const mock = (await import("../renderNative.testkit")).nativeModuleMock();
	const settle = (value: { setValue(value: number): void }, config: { toValue: number }) => ({
		start: () => value.setValue(config.toValue),
	});
	return {
		...mock,
		Animated: {
			...mock.Animated,
			spring: settle,
			parallel: (animations: { start(): void }[]) => ({ start: () => animations.forEach((each) => each.start()) }),
		},
		PanResponder: { create: () => ({ panHandlers: {} }) },
	};
});
vi.mock("react-native-safe-area-context", () => ({
	useSafeAreaInsets: () => ({ top: 47, bottom: 34, left: 21, right: 21 }),
	useSafeAreaFrame: () => ({ x: 0, y: 0, width: 393, height: 852 }),
}));
// The package's index pulls in its image assets, which vitest can't load.
// The stand-in is its iPhone rule in portrait with no Dynamic Island: a 44pt
// bar under the status bar, which is the top inset.
vi.mock("@react-navigation/elements", () => ({
	getDefaultHeaderHeight: (_layout: unknown, modalPresentation: boolean, topInset: number) =>
		(modalPresentation ? 56 : 44) + topInset,
}));
// The native package loads React Native itself; its StackActions are the
// routers package's, which the test uses as they are.
vi.mock("@react-navigation/native", async () => ({
	StackActions: (await import("@react-navigation/routers")).StackActions,
}));
vi.mock("../ConnectionProvider", () => ({
	useConnection: () => ({
		selectHub: (id: string) => harness.selected.push(id),
		activeProfile: harness.active,
		profiles: [
			{ id: "hub-1", name: "magic-kingdom" },
			{ id: "hub-2", name: "paradise-park" },
		],
	}),
}));
vi.mock("./alertsContext", async () => {
	const { useSyncExternalStore } = await import("react");
	return {
		useAlertCenter: () => harness.center,
		useAlertSnapshot: () => {
			const center = harness.center as AlertCenter;
			return useSyncExternalStore(center.subscribe, center.getSnapshot);
		},
		useNoticeFor: () => (key: string) => harness.notices.get(key),
	};
});
vi.mock("../board/BoardNotices", () => ({
	openNotice: (...args: unknown[]) => harness.opened.push(args),
}));

// Vitest runs both Metro platform entries in one process.
vi.mock("./openAlertTarget", async (importOriginal) => {
	const iphone = await importOriginal<typeof import("./openAlertTarget")>();
	const android = await import("./openAlertTarget.android");
	const openAlertTarget: OpenAlertTarget = (...args) =>
		(harness.platform === "android" ? android : iphone).openAlertTarget(...args);
	return { openAlertTarget };
});

import { AlertBannerHost } from "./AlertBannerHost";

const timer = { now: () => Date.now(), setTimeout: () => 0, clearTimeout: () => {} };
const session = (ref: string): Alert => ({ kind: "question", ref, title: `Session ${ref}`, why: null });
const hostDown: Notice = {
	kind: "host",
	key: "host:paradise-park",
	text: "paradise-park is offline · 3 sessions",
	action: "Details",
	sourceId: "paradise-park",
};

function mount() {
	const dispatched: unknown[] = [];
	const navigation = {
		dispatch: (action: unknown) => dispatched.push(action),
		navigate: vi.fn(),
	};
	const tree = render(<AlertBannerHost navigation={navigation as never} />);
	const container = tree.root.find((node) => node.props.pointerEvents === "box-none");
	const card = () => tree.root.find((node) => node.props.accessibilityRole === "button");
	return { tree, dispatched, navigate: navigation.navigate, container, card };
}

beforeEach(() => {
	harness.platform = "ios";
	harness.center = new AlertCenter(timer);
	harness.notices.clear();
	harness.opened.length = 0;
	harness.selected.length = 0;
	harness.active = { id: "hub-1", name: "magic-kingdom" };
});

it("sits just below the nav bar, and shows nothing without a banner", () => {
	const { container } = mount();
	expect(container.props.style).toMatchObject({ position: "absolute", left: 21, right: 21, top: 95 });
	expect(container.children).toHaveLength(0);
});

const Stack = createTestStack();
it.each(["Workspace", "Hubs"])(
	"opens an iPhone session alert from %s without duplicating or losing a route",
	async (screen) => {
		const navigation = createNavigationContainerRef<Routes>();
		render(
			<BaseNavigationContainer ref={navigation}>
				<Stack.Navigator initialRouteName="Hubs">
					<Stack.Screen name="Hubs" component={BlankPage} />
					<Stack.Screen name="Workspace" component={BlankPage} />
				</Stack.Navigator>
			</BaseNavigationContainer>,
		);
		if (screen === "Workspace") {
			await act(async () => navigation.navigate("Workspace", { hubId: "hub-1", path: "/" }));
		}
		const priorKeys = navigation.getRootState()?.routes.map((route) => route.key) ?? [];
		const tree = render(<AlertBannerHost navigation={navigation} />);
		act(() => harness.center?.offer(session("local:a")));
		await act(async () => tree.root.find((node) => node.props.accessibilityRole === "button").props.onPress());
		const routes = navigation.getRootState()?.routes ?? [];
		expect(routes.map((route) => route.name)).toEqual(["Hubs", "Workspace"]);
		expect(routes.slice(0, priorKeys.length).map((route) => route.key)).toEqual(priorKeys);
		expect(navigation.getCurrentRoute()?.params).toEqual({ hubId: "hub-1", path: "/s/local%3Aa" });
	},
);

it("keeps the on-device editor on the stack when opening a session alert", () => {
	harness.platform = "android";
	const { card, dispatched } = mount();
	act(() => harness.center?.offer(session("local:b")));
	act(() => card().props.onPress());
	expect(dispatched).toEqual([
		expect.objectContaining({
			type: "PUSH",
			payload: expect.objectContaining({
				name: "Conversation",
				params: expect.objectContaining({ hubId: "hub-1", ref: "local:b" }),
			}),
		}),
	]);
});

it.each(["ios", "android"])("opens the %s Needs you list when several sessions alert together", (platform) => {
	harness.platform = platform;
	const { card, dispatched, navigate } = mount();
	act(() => {
		harness.center?.offer(session("local:a"));
		harness.center?.offer(session("local:b"));
	});
	const jumps: string[] = [];
	const stop = onBoardJump((section) => jumps.push(section));
	act(() => card().props.onPress());
	stop();
	if (platform === "ios") {
		expect(navigate).toHaveBeenCalledWith(
			"Workspace",
			{ hubId: "hub-1", path: "/?attention=needs-you" },
			{ pop: true },
		);
		expect(jumps).toEqual([]);
		return;
	}
	expect(dispatched).toEqual([
		expect.objectContaining({ type: "POP_TO", payload: expect.objectContaining({ name: "Sessions" }) }),
	]);
	expect(jumps).toEqual(["needsYou"]);
});

it.each([
	[hostDown, "hosts"],
	[{ kind: "signIn", key: "auth", providerId: "provider", text: "expired", action: "Sign in" }, "credentials"],
	[
		{ kind: "plugin", key: "broken", pluginId: "plugin", marketplace: "plugins", text: "invalid", action: "Plugins" },
		"plugins-manager",
	],
] as const)("opens each iPhone notice in local hub settings and skips a resolved notice", (notice, section) => {
	const { card, navigate } = mount();
	harness.notices.set(notice.key, notice);
	const tap = () => {
		act(() => harness.center?.offer({ kind: "notice", key: notice.key, title: notice.text }));
		act(() => card().props.onPress());
	};
	tap();
	expect(navigate).toHaveBeenCalledWith(
		"Workspace",
		{ hubId: "hub-1", path: `/settings/${section}?host=local` },
		{ pop: true },
	);
	harness.notices.clear();
	tap();
	expect(navigate).toHaveBeenCalledTimes(1);
});

it("opens an Android notice where the Board's notice row does, and nothing once it has resolved", () => {
	harness.platform = "android";
	const { card } = mount();
	harness.notices.set(hostDown.key, hostDown);
	act(() => harness.center?.offer({ kind: "notice", key: hostDown.key, title: hostDown.text }));
	act(() => card().props.onPress());
	expect(harness.opened).toEqual([[expect.anything(), "hub-1", hostDown]]);
	harness.notices.clear();
	act(() => harness.center?.offer({ kind: "notice", key: hostDown.key, title: hostDown.text }));
	act(() => card().props.onPress());
	expect(harness.opened).toHaveLength(1);
});

it("opens the New session of the hub whose start failed, with its draft, whichever hub is selected (#3104)", () => {
	harness.platform = "android";
	const { card, dispatched } = mount();
	act(() => harness.center?.offer({ kind: "startFailed", hubId: "hub-2", hubName: "paradise-park", uncertain: false }));
	act(() => card().props.onPress());
	// That hub is selected first, so its sheet opens on its own connection.
	expect(harness.selected).toEqual(["hub-2"]);
	expect(dispatched).toEqual([
		expect.objectContaining({
			type: "PUSH",
			payload: expect.objectContaining({
				name: "NewSession",
				params: { hubId: "hub-2", hubName: "paradise-park" },
			}),
		}),
	]);
});

it("says why a failed start's banner opens nothing once its hub has been removed, and takes it down (#3104)", () => {
	const { card, dispatched } = mount();
	alertRequests.length = 0;
	act(() => harness.center?.offer({ kind: "startFailed", hubId: "hub-gone", hubName: "attic", uncertain: false }));
	act(() => card().props.onPress());
	expect(dispatched).toEqual([]);
	expect(harness.center?.getSnapshot().banner).toBeNull();
	expect(alertRequests).toEqual([
		expect.objectContaining({
			title: "attic was removed",
			message: "Its New session draft was removed with it, so there's nothing to open.",
		}),
	]);
});

it("opens the selected hub's New session without selecting it again (#3104)", () => {
	harness.platform = "android";
	const { card, dispatched } = mount();
	act(() => harness.center?.offer({ kind: "startFailed", hubId: "hub-1", hubName: "magic-kingdom", uncertain: false }));
	act(() => card().props.onPress());
	expect(harness.selected).toEqual([]);
	expect(dispatched).toHaveLength(1);
});

it("opens a failed start's New session with no hub selected, selecting its hub (#3104)", () => {
	harness.platform = "android";
	harness.active = null;
	const { card, dispatched } = mount();
	act(() => harness.center?.offer({ kind: "startFailed", hubId: "hub-2", hubName: "paradise-park", uncertain: true }));
	act(() => card().props.onPress());
	expect(harness.selected).toEqual(["hub-2"]);
	expect(dispatched).toEqual([
		expect.objectContaining({
			type: "PUSH",
			payload: expect.objectContaining({ name: "NewSession", params: { hubId: "hub-2", hubName: "paradise-park" } }),
		}),
	]);
});

it("says a removed hub's failed start opens nothing even with no hub selected (#3104)", () => {
	harness.active = null;
	const { card, dispatched } = mount();
	alertRequests.length = 0;
	act(() => harness.center?.offer({ kind: "startFailed", hubId: "hub-gone", hubName: "attic", uncertain: false }));
	act(() => card().props.onPress());
	expect(dispatched).toEqual([]);
	expect(alertRequests).toEqual([expect.objectContaining({ title: "attic was removed" })]);
});
