import { act } from "react-test-renderer";
import { beforeEach, expect, it, vi } from "vitest";
import { whyLine } from "../board/attention";
import { paletteFor } from "../design/tokens";
import { render, renderedText } from "../renderNative.testkit";
import type { Alert, Banner } from "./alertCenter";

const native = vi.hoisted(() => ({
	announced: [] as string[],
	pan: null as null | {
		onMoveShouldSetPanResponder: (event: unknown, gesture: { dx: number; dy: number }) => boolean;
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
		},
		Animated: {
			...mock.Animated,
			spring: settle,
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
	expect(mount(banner(session("d", "finished"))).card.props.style).toMatchObject({
		borderColor: palette.accentEdge,
	});
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
	act(() => pan.onPanResponderRelease({}, { dy: -10, vy: 0 }));
	expect(calls).toEqual([]);
	act(() => pan.onPanResponderRelease({}, { dy: -40, vy: 0 }));
	expect(calls).toEqual(["dismiss"]);
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
