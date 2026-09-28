import { act } from "react-test-renderer";
import { describe, expect, it, vi } from "vitest";
import { type PanGestureMock, render, renderedText } from "../renderNative.testkit";
import { SessionTitle } from "./SessionTitle";

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));
vi.mock("react-native-gesture-handler", async () =>
	(await import("../renderNative.testkit")).gestureDetectorModuleMock(),
);

const symbols = (tree: ReturnType<typeof render>) =>
	tree.root.findAllByType("SymbolView" as never).map((node) => node.props.name as string);

const base = { onPress: () => {}, onSwipe: () => {}, neighbors: { previous: false, next: false } };

describe("the Session's nav bar title (spec 8.1)", () => {
	it("shows the title, the state line and the chevron", () => {
		const tree = render(
			<SessionTitle title="Fix the flaky test" line={{ state: "idle", text: "Finished · 1h ago" }} {...base} />,
		);
		expect(renderedText(tree)).toBe("Fix the flaky test Finished · 1h ago");
		expect(symbols(tree)).toEqual(["chevron.right"]);
	});

	it("draws no mark for an idle or shut-down session", () => {
		for (const state of ["idle", "shutDown"] as const) {
			const tree = render(<SessionTitle title="S" line={{ state, text: "x" }} {...base} />);
			expect(symbols(tree)).toEqual(["chevron.right"]);
		}
	});

	it("draws a still green dot for a working session", () => {
		const tree = render(<SessionTitle title="S" line={{ state: "working", text: "Working · 38m" }} {...base} />);
		const [mark] = tree.root.findAllByType("SymbolView" as never);
		expect(mark?.props).toMatchObject({ name: "circle.fill", size: 12 });
		expect(mark?.props.tintColor).toMatch(/^#/);
		expect(symbols(tree)).toEqual(["circle.fill", "chevron.right"]);
	});

	it("is one button that opens session info, read title first", () => {
		const onPress = vi.fn();
		const tree = render(
			<SessionTitle
				title="Fix the flaky test"
				line={{ state: "working", text: "Working · 38m" }}
				{...base}
				onPress={onPress}
			/>,
		);
		const [button, ...others] = tree.root.findAll((node) => node.props.accessibilityRole === "button");
		expect(others).toEqual([]);
		expect(button?.props.accessibilityLabel).toBe("Fix the flaky test, Working · 38m, session info");
		act(() => button?.props.onPress());
		expect(onPress).toHaveBeenCalledTimes(1);
		expect(button?.props.style({ pressed: false })).toMatchObject({ minHeight: 44, opacity: 1 });
		expect(button?.props.style({ pressed: true })).toMatchObject({ opacity: 0.6 });
	});

	it("keeps the title to one tail-truncated line in tabular state figures", () => {
		const tree = render(<SessionTitle title="S" line={{ state: "idle", text: "Finished" }} {...base} />);
		const [title, state] = tree.root.findAllByType("Text" as never);
		expect(title?.props).toMatchObject({ numberOfLines: 1, ellipsizeMode: "tail" });
		expect(title?.props.style).toMatchObject({ fontSize: 15, fontWeight: "600" });
		expect(state?.props.style).toMatchObject({ fontSize: 13, lineHeight: 18, fontVariant: ["tabular-nums"] });
	});

	it("moves through Live order on a horizontal pan, without taking the tap or a vertical drag (spec 6)", () => {
		const onSwipe = vi.fn();
		const onPress = vi.fn();
		const tree = render(
			<SessionTitle
				title="S"
				line={{ state: "idle", text: "Finished" }}
				onPress={onPress}
				onSwipe={onSwipe}
				neighbors={{ previous: true, next: true }}
			/>,
		);
		const pan = tree.root.findByType("GestureDetector" as never).props.gesture as PanGestureMock;
		// It starts only once a drag has gone sideways, so a tap stays the
		// title's, and a drag that goes up or down first is never a swipe.
		expect(pan.config).toMatchObject({ activeOffsetX: [-10, 10], failOffsetY: [-10, 10], runOnJS: true });
		act(() => pan.handlers.onEnd?.({ translationX: -80, velocityX: 0 }, true));
		act(() => pan.handlers.onEnd?.({ translationX: 30, velocityX: 900 }, true));
		act(() => pan.handlers.onEnd?.({ translationX: 30, velocityX: 100 }, true));
		// A pan the system cancelled, or that failed, goes nowhere however far
		// it went.
		act(() => pan.handlers.onEnd?.({ translationX: -200, velocityX: -2000 }, false));
		expect(onSwipe.mock.calls).toEqual([[1], [-1]]);
		expect(onPress).not.toHaveBeenCalled();
	});

	it("offers VoiceOver Previous session and Next session, each only when there is one", () => {
		const onSwipe = vi.fn();
		const actionsOf = (neighbors: { previous: boolean; next: boolean }) => {
			const tree = render(
				<SessionTitle
					title="S"
					line={{ state: "idle", text: "Finished" }}
					onPress={() => {}}
					onSwipe={onSwipe}
					neighbors={neighbors}
				/>,
			);
			return tree.root.find((node) => node.props.accessibilityRole === "button");
		};
		const both = actionsOf({ previous: true, next: true });
		expect(both.props.accessibilityActions).toEqual([
			{ name: "previous", label: "Previous session" },
			{ name: "next", label: "Next session" },
		]);
		act(() => both.props.onAccessibilityAction({ nativeEvent: { actionName: "next" } }));
		act(() => both.props.onAccessibilityAction({ nativeEvent: { actionName: "previous" } }));
		expect(onSwipe.mock.calls).toEqual([[1], [-1]]);
		expect(actionsOf({ previous: false, next: true }).props.accessibilityActions).toEqual([
			{ name: "next", label: "Next session" },
		]);
		expect(actionsOf({ previous: true, next: false }).props.accessibilityActions).toEqual([
			{ name: "previous", label: "Previous session" },
		]);
		expect(actionsOf({ previous: false, next: false }).props.accessibilityActions).toEqual([]);
	});
});
