import { act } from "react-test-renderer";
import { beforeEach, expect, it, vi } from "vitest";
import { onBoardJump } from "../board/boardJump";
import type { Notice } from "../board/notices";
import { render } from "../renderNative.testkit";
import { AlertCenter, type Alert } from "./alertCenter";

const harness = vi.hoisted(() => ({
	center: null as AlertCenter | null,
	notices: new Map<string, unknown>(),
	opened: [] as unknown[][],
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
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));
vi.mock("react-native-safe-area-context", () => ({
	useSafeAreaInsets: () => ({ top: 47, bottom: 34, left: 0, right: 0 }),
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
vi.mock("../ConnectionProvider", () => ({ useConnection: () => ({ activeProfile: { id: "hub-1" } }) }));
vi.mock("./AlertsProvider", async () => {
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
	const navigation = { dispatch: (action: unknown) => dispatched.push(action), navigate: vi.fn() };
	const tree = render(<AlertBannerHost navigation={navigation as never} />);
	const container = tree.root.find((node) => node.props.pointerEvents === "box-none");
	const card = () => tree.root.find((node) => node.props.accessibilityRole === "button");
	return { tree, dispatched, container, card };
}

beforeEach(() => {
	harness.center = new AlertCenter(timer);
	harness.notices.clear();
	harness.opened.length = 0;
});

it("sits just below the nav bar, and shows nothing without a banner", () => {
	const { container } = mount();
	expect(container.props.style).toMatchObject({ position: "absolute", left: 0, right: 0, top: 95 });
	expect(container.children).toHaveLength(0);
});

it("opens a session's banner on top of where you are", () => {
	const { card, dispatched } = mount();
	act(() => harness.center?.offer(session("local:a")));
	act(() => card().props.onPress());
	expect(dispatched).toEqual([
		expect.objectContaining({
			type: "PUSH",
			payload: expect.objectContaining({
				name: "Conversation",
				params: { hubId: "hub-1", ref: "local:a", title: "Session local:a" },
			}),
		}),
	]);
});

it("takes a coalesced banner to Needs you on the Board", () => {
	const { card, dispatched } = mount();
	act(() => {
		harness.center?.offer(session("local:a"));
		harness.center?.offer(session("local:b"));
	});
	const jumps: string[] = [];
	const stop = onBoardJump((section) => jumps.push(section));
	act(() => card().props.onPress());
	stop();
	expect(dispatched).toEqual([
		expect.objectContaining({ type: "POP_TO", payload: expect.objectContaining({ name: "Sessions" }) }),
	]);
	expect(jumps).toEqual(["needsYou"]);
});

it("opens a notice where the Board's notice row does, and nothing once it has resolved", () => {
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
