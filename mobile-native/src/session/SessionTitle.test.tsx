import { act } from "react-test-renderer";
import { describe, expect, it, vi } from "vitest";
import { type PanGestureMock, render, renderedText } from "../renderNative.testkit";
import { NAV_BAR_BUTTON_SPAN, SessionTitle } from "./SessionTitle";

// The window the mocked react-native reports. useTextScale reads fontScale on
// iOS, so a test raises it to stand at an accessibility text size (AX1-AX5).
const mockWindow = vi.hoisted(() => ({ fontScale: 1 }));

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
	useWindowDimensions: () => ({ fontScale: mockWindow.fontScale, scale: 2, width: 390, height: 844 }),
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));
vi.mock("react-native-gesture-handler", async () =>
	(await import("../renderNative.testkit")).gestureDetectorModuleMock(),
);

const symbols = (tree: ReturnType<typeof render>) =>
	tree.root.findAllByType("SymbolView" as never).map((node) => node.props.name as string);

const base = { onPress: () => {}, onSwipe: () => {}, neighbors: { previous: false, next: false } };

const maxWidthOf = (tree: ReturnType<typeof render>) => {
	const button = tree.root.find((node) => node.props.accessibilityRole === "button");
	return (typeof button.props.style === "function" ? button.props.style({ pressed: false }) : button.props.style)
		.maxWidth;
};

describe("the Session's nav bar title (spec 8.1)", () => {
	it("shows the title, the state line and the chevron", () => {
		const tree = render(
			<SessionTitle title="Fix the flaky test" line={{ state: "idle", text: "Idle · 1h ago" }} {...base} />,
		);
		expect(renderedText(tree)).toBe("Fix the flaky test Idle · 1h ago");
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
		const tree = render(<SessionTitle title="S" line={{ state: "idle", text: "Idle" }} {...base} />);
		const [title, state] = tree.root.findAllByType("Text" as never);
		expect(title?.props).toMatchObject({ numberOfLines: 1, ellipsizeMode: "tail" });
		expect(title?.props.style).toMatchObject({ fontSize: 15, fontWeight: "600" });
		expect(state?.props.style).toMatchObject({ fontSize: 13, lineHeight: 18, fontVariant: ["tabular-nums"] });
	});

	it("stays between the nav bar's buttons, however long the title (#2956)", () => {
		// The native header sizes a custom title by its content, so an
		// unbounded one runs under Back and ⋯ instead of truncating. The test
		// kit's window is 390pt wide.
		const tree = render(
			<SessionTitle
				title="Audit Tool Descriptions for Implied Options"
				line={{ state: "idle", text: "Idle" }}
				{...base}
			/>,
		);
		expect(maxWidthOf(tree)).toBe(390 - 2 * NAV_BAR_BUTTON_SPAN);
	});

	it("widens the span with the Back count's digits, so a 3-digit count cannot overlap (#3063)", () => {
		// Back's pill (BackButton.tsx) shows the count's digits at 17pt tabular
		// figures, about 10pt each. The span measured for one digit is 80pt a
		// side, so two more digits add 20pt a side: 390 - 2 * (80 + 20).
		const tree = render(<SessionTitle title="S" line={{ state: "idle", text: "Idle" }} backCount={123} {...base} />);
		expect(maxWidthOf(tree)).toBe(390 - 2 * 100);
	});

	it("widens the span with the accessibility text size, so the scaled Back pill cannot overlap (#3063)", () => {
		// At fontScale 3 the chevron (20pt) and the one count digit (~10pt) are
		// three times their default size, so the span grows by 2 * 30.
		mockWindow.fontScale = 3;
		try {
			const tree = render(<SessionTitle title="S" line={{ state: "idle", text: "Idle" }} {...base} />);
			expect(maxWidthOf(tree)).toBe(390 - 2 * (80 + 60));
		} finally {
			mockWindow.fontScale = 1;
		}
	});

	it("never lets the reserved spans leave the title a negative width (#3063)", () => {
		// A 3-digit count at the largest accessibility size wants more span than
		// the window has; the title floors at zero instead of going negative.
		mockWindow.fontScale = 3;
		try {
			const tree = render(<SessionTitle title="S" line={{ state: "idle", text: "Idle" }} backCount={123} {...base} />);
			expect(maxWidthOf(tree)).toBe(0);
		} finally {
			mockWindow.fontScale = 1;
		}
	});

	it("moves through Live order on a horizontal pan, without taking the tap or a vertical drag (spec 6)", () => {
		const onSwipe = vi.fn();
		const onPress = vi.fn();
		const tree = render(
			<SessionTitle
				title="S"
				line={{ state: "idle", text: "Idle" }}
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
					line={{ state: "idle", text: "Idle" }}
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
