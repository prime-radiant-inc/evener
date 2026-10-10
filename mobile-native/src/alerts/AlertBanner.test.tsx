import { act } from "react-test-renderer";
import { beforeEach, expect, it, vi } from "vitest";
import { whyLine } from "../board/attention";
import { StateMark } from "../board/StateMark";
import { paletteFor } from "../design/tokens";
import { render, renderedText } from "../renderNative.testkit";
import type { Alert, Banner } from "./alertCenter";

const native = vi.hoisted(() => ({
	announced: [] as string[],
	reduceMotion: false,
	springs: 0,
	pan: null as null | {
		onMoveShouldSetPanResponder: (event: unknown, gesture: { dx: number; dy: number }) => boolean;
		onPanResponderGrant: () => void;
		onPanResponderRelease: (event: unknown, gesture: { dy: number; vy: number }) => void;
	},
}));
vi.mock("react-native", async () => {
	const mock = (await import("../renderNative.testkit")).nativeModuleMock();
	const settle = (value: { setValue(value: number): void }, config: { toValue: number }) => ({
		start: (done?: (result: { finished: boolean }) => void) => {
			value.setValue(config.toValue);
			done?.({ finished: true });
		},
	});
	return {
		...mock,
		AccessibilityInfo: {
			...mock.AccessibilityInfo,
			announceForAccessibility: (label: string) => native.announced.push(label),
			isReduceMotionEnabled: () => Promise.resolve(native.reduceMotion),
		},
		Animated: {
			...mock.Animated,
			spring: (value: { setValue(value: number): void }, config: { toValue: number }) => {
				native.springs += 1;
				return settle(value, config);
			},
			parallel: (animations: { start(): void }[]) => ({ start: () => animations.forEach((each) => each.start()) }),
		},
		PanResponder: {
			create: (config: NonNullable<typeof native.pan>) => {
				native.pan = config;
				return { panHandlers: {} };
			},
		},
	};
});
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));

import { AlertBanner, bannerLabel } from "./AlertBanner";

const palette = paletteFor("light");
const row = (ref: string) => ({
	ref,
	host_id: "local",
	session_id: ref,
	title: `Session ${ref}`,
	project: "evener",
	state: "awaiting",
	kind: "session",
	live: true,
	children: [],
});
const session = (ref: string, kind: "question" | "failed" | "finished" = "question"): Alert => ({
	kind,
	ref,
	title: `Session ${ref}`,
	why: kind === "finished" ? null : whyLine({ row: row(ref) as never, state: kind }),
});
const notice: Alert = { kind: "notice", key: "host:paradise-park", title: "paradise-park is offline · 3 sessions" };
const banner = (...alerts: Alert[]): Banner => ({ id: 1, alerts });

function mount(shown: Banner, handlers: Partial<Record<"onTap" | "onDismiss" | "onTouch", () => void>> = {}) {
	const calls: string[] = [];
	const tree = render(
		<AlertBanner
			banner={shown}
			onTap={handlers.onTap ?? (() => calls.push("tap"))}
			onDismiss={handlers.onDismiss ?? (() => calls.push("dismiss"))}
			onTouch={(down) => calls.push(`touch:${down}`)}
		/>,
	);
	const card = tree.root.find((node) => node.props.accessibilityRole === "button");
	return { tree, card, calls };
}
const texts = (tree: ReturnType<typeof render>) =>
	tree.root.findAll((node) => String(node.type) === "Text" && typeof node.props.children === "string");

beforeEach(() => {
	native.announced.length = 0;
	native.reduceMotion = false;
});

/** The card's two animated wrappers: the drop (opacity and translateY),
 * inside the drag. */
function drop(tree: ReturnType<typeof render>) {
	const style = tree.root.findAll((node) => String(node.type) === "Animated.View")[1]?.props.style;
	return { opacity: style.opacity.value, translateY: style.transform[0].translateY.value };
}

it("drops in from above with a spring, or only fades in with Reduce Motion (spec 16.6)", async () => {
	const moving = mount(banner(session("a")));
	await act(async () => {});
	expect(drop(moving.tree)).toEqual({ opacity: 1, translateY: 0 });
	expect(native.springs).toBe(1);
	native.reduceMotion = true;
	native.springs = 0;
	const still = mount(banner(session("b")));
	// Before the setting is read, the banner waits above, unseen.
	expect(drop(still.tree)).toEqual({ opacity: 0, translateY: -24 });
	await act(async () => {});
	expect(drop(still.tree)).toEqual({ opacity: 1, translateY: 0 });
	expect(native.springs).toBe(0);
});

it("shows a question's title, now, and its word in the attention ink with the reason", () => {
	const { tree } = mount(banner(session("a")));
	expect(renderedText(tree)).toContain("Session a");
	expect(renderedText(tree)).toContain("now");
	const word = texts(tree).find((node) => node.props.children === "Question");
	expect(word?.props.style).toMatchObject({ color: palette.attentionInk, fontWeight: "600" });
	expect(renderedText(tree)).toContain("waiting for your answer");
	const failed = mount(banner(session("b", "failed")));
	expect(texts(failed.tree).find((node) => node.props.children === "Failed")?.props.style).toMatchObject({
		color: palette.dangerInk,
	});
});

it("coalesces sessions into a count that says a tap goes to the Board", () => {
	const { tree } = mount(banner(session("a"), session("b"), session("c", "failed")));
	expect(renderedText(tree)).toContain("3 sessions need you");
	expect(renderedText(tree)).toContain("Tap to see them on the Board.");
	expect(texts(tree).find((node) => node.props.children === "3")?.props.style).toMatchObject({
		color: palette.attentionInk,
	});
});

it("marks a notice with the triangle and no second line, and edges a finished result in the accent", () => {
	const { tree, card } = mount(banner(notice));
	expect(tree.root.findByType("SymbolView" as never).props.name).toBe("exclamationmark.triangle.fill");
	expect(renderedText(tree)).toBe("paradise-park is offline · 3 sessions now");
	expect(card.props.style).toMatchObject({ borderColor: palette.attentionEdge });
	const finished = mount(banner(session("d", "finished")));
	expect(finished.card.props.style).toMatchObject({ borderColor: palette.accentEdge });
	// The finished mark is the blue dot (spec 13.3).
	expect(finished.tree.root.findByType("SymbolView" as never).props).toMatchObject({
		name: "circle.fill",
		tintColor: palette.accent,
	});
});

it("says a session started, marks it working, and edges it in the accent", () => {
	const started: Alert = { kind: "started", ref: "local:s", title: "Fix the flaky test", why: null };
	const { tree, card } = mount(banner(started));
	expect(renderedText(tree)).toContain("Fix the flaky test");
	expect(renderedText(tree)).toContain("Session started. Tap to open it.");
	expect(bannerLabel(banner(started))).toBe("Fix the flaky test, Session started. Tap to open it.");
	expect(card.props.style).toMatchObject({ borderColor: palette.accentEdge });
});

it("keeps the banner while a finger is on it, and opens it on a tap", () => {
	const { card, calls } = mount(banner(session("a")));
	act(() => card.props.onPressIn());
	act(() => card.props.onPressOut());
	act(() => card.props.onPress());
	expect(calls).toEqual(["touch:true", "touch:false", "tap"]);
});

it("dismisses on an upward swipe past its threshold, and springs back short of it", () => {
	const { calls } = mount(banner(session("a")));
	const pan = native.pan;
	if (!pan) throw new Error("no pan responder");
	expect(pan.onMoveShouldSetPanResponder({}, { dx: 0, dy: -10 })).toBe(true);
	expect(pan.onMoveShouldSetPanResponder({}, { dx: 0, dy: 10 })).toBe(false);
	// The drag keeps the banner while the finger is down, as a press does.
	act(() => pan.onPanResponderGrant());
	act(() => pan.onPanResponderRelease({}, { dy: -10, vy: 0 }));
	expect(calls).toEqual(["touch:true", "touch:false"]);
	act(() => pan.onPanResponderGrant());
	act(() => pan.onPanResponderRelease({}, { dy: -40, vy: 0 }));
	expect(calls).toEqual(["touch:true", "touch:false", "touch:true", "touch:false", "dismiss"]);
});

it("is one button VoiceOver reads and can dismiss with escape", () => {
	const shown = banner(session("a"));
	expect(bannerLabel(shown)).toBe("Session a, Question, waiting for your answer");
	const { card, calls } = mount(shown);
	expect(card.props.accessibilityLabel).toBe("Session a, Question, waiting for your answer");
	act(() => card.props.onAccessibilityAction({ nativeEvent: { actionName: "escape" } }));
	act(() => card.props.onAccessibilityAction({ nativeEvent: { actionName: "activate" } }));
	expect(calls).toEqual(["dismiss", "tap"]);
});

it("announces a banner once, and again only when what it says changes", () => {
	const first = banner(session("a"));
	const { tree } = mount(first);
	const rerender = (next: Banner) =>
		act(() => tree.update(<AlertBanner banner={next} onTap={() => {}} onDismiss={() => {}} onTouch={() => {}} />));
	rerender({ ...first });
	expect(native.announced).toEqual(["Session a, Question, waiting for your answer"]);
	rerender(banner(session("a"), session("b")));
	expect(native.announced).toEqual([
		"Session a, Question, waiting for your answer",
		"2 sessions need you, Tap to see them on the Board.",
	]);
});

it("says a start failed on its hub in one short line, and that a tap opens New session, marked as a failure (#3104)", () => {
	const failed: Alert = { kind: "startFailed", hubId: "hub-a", hubName: "magic-kingdom", uncertain: false };
	const { tree, card } = mount(banner(failed));
	expect(renderedText(tree)).toContain("Couldn't start the new session");
	expect(bannerLabel(banner(failed))).toBe(
		"Couldn't start the new session, On magic-kingdom. Your draft is kept. Tap to open New session.",
	);
	expect(tree.root.findByType(StateMark).props.state).toBe("failed");
	expect(card.props.style).toMatchObject({ borderColor: palette.attentionEdge });
});

it("says a start that may have begun couldn't be confirmed, without claiming either way (#3104)", () => {
	const uncertain: Alert = { kind: "startFailed", hubId: "hub-a", hubName: "magic-kingdom", uncertain: true };
	expect(bannerLabel(banner(uncertain))).toBe(
		"Couldn't confirm the new session started, On magic-kingdom. It may have started. Tap to open New session.",
	);
});
