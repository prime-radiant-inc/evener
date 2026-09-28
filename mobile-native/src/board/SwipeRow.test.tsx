import { Text } from "react-native";
import { act, type ReactTestRenderer } from "react-test-renderer";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { palettes } from "../design/tokens";
import { render, renderedText, swipeableCalls } from "../renderNative.testkit";
import { type SwipeAction, SwipeRow, swipeAccessibility } from "./SwipeRow";

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));
vi.mock("react-native-gesture-handler/ReanimatedSwipeable", async () =>
	(await import("../renderNative.testkit")).gestureHandlerModuleMock(),
);

const action = (key: string, label: string) => ({
	key,
	label,
	symbol: "archivebox" as const,
	fill: "inkMid" as const,
	run: vi.fn(),
});
function mount(options: { leading?: SwipeAction | null } = {}) {
	const leading = action("archive", "Archive");
	const pin = action("pin", "Pin");
	const onActiveChange = vi.fn();
	const tree: ReactTestRenderer = render(
		<SwipeRow
			leading={options.leading === null ? undefined : leading}
			trailing={[pin]}
			onActiveChange={onActiveChange}
		>
			<Text>row</Text>
		</SwipeRow>,
	);
	const swipeable = tree.root.findByType("ReanimatedSwipeable" as never);
	const touchStart = (pageX: number) =>
		act(() => tree.root.findByProps({ testID: "swipe-row-content" }).props.onTouchStart({ nativeEvent: { pageX } }));
	return { swipeable, leading, pin, onActiveChange, touchStart };
}
beforeEach(() => {
	swipeableCalls.closes = 0;
});

it("a full swipe right does the leading action once and closes the row", () => {
	const { swipeable, leading, touchStart } = mount();
	expect(swipeable.props.leftThreshold).toBe(195);
	touchStart(200);
	act(() => swipeable.props.onSwipeableOpen("right"));
	expect(leading.run).toHaveBeenCalledOnce();
	expect(swipeableCalls.closes).toBe(1);
});

it("a swipe that began in the left edge band closes without acting, whichever way it opened", () => {
	const { swipeable, leading, pin, touchStart } = mount();
	touchStart(10);
	act(() => swipeable.props.onSwipeableWillOpen("right"));
	act(() => swipeable.props.onSwipeableOpen("right"));
	act(() => swipeable.props.onSwipeableWillOpen("left"));
	act(() => swipeable.props.onSwipeableOpen("left"));
	expect(leading.run).not.toHaveBeenCalled();
	expect(pin.run).not.toHaveBeenCalled();
	expect(swipeableCalls.closes).toBe(4);
});

it("takes the left edge band out of the row's hit frame", () => {
	expect(mount().swipeable.props.hitSlop).toEqual({ left: -24 });
});

it("a revealed action acts and closes the row", () => {
	const { swipeable, pin } = mount();
	const panel = render(swipeable.props.renderRightActions());
	const button = panel.root.find(
		(node) => String(node.type) === "Pressable" && node.props.accessibilityLabel === "Pin",
	);
	act(() => button.props.onPress());
	expect(pin.run).toHaveBeenCalledOnce();
	expect(swipeableCalls.closes).toBe(1);
});

it("holds the list from a swipe's first drag until the row is closed again", () => {
	const { swipeable, onActiveChange } = mount();
	act(() => swipeable.props.onSwipeableOpenStartDrag("left"));
	expect(onActiveChange).toHaveBeenLastCalledWith(true);
	act(() => swipeable.props.onSwipeableClose("left"));
	expect(onActiveChange).toHaveBeenLastCalledWith(false);
});

it("has no leading panel without a leading action", () => {
	expect(mount({ leading: null }).swipeable.props.renderLeftActions).toBeUndefined();
});

it("gives VoiceOver every action", () => {
	const archive = action("archive", "Archive");
	const pin = action("pin", "Pin");
	const accessibility = swipeAccessibility(archive, [pin]);
	expect(accessibility.accessibilityActions).toEqual([
		{ name: "archive", label: "Archive" },
		{ name: "pin", label: "Pin" },
	]);
	accessibility.onAccessibilityAction({ nativeEvent: { actionName: "pin" } } as never);
	expect(pin.run).toHaveBeenCalledOnce();
	expect(archive.run).not.toHaveBeenCalled();
});

describe("a destructive swipe (spec 8.5, 8.8)", () => {
	function mountDestructive() {
		const cancel = { key: "cancel", label: "Cancel", run: vi.fn() };
		const tree = render(
			<SwipeRow destructive={cancel}>
				<Text>ghost</Text>
			</SwipeRow>,
		);
		const swipeable = tree.root.findByType("ReanimatedSwipeable" as never);
		const touchStart = (pageX: number) =>
			act(() => tree.root.findByProps({ testID: "swipe-row-content" }).props.onTouchStart({ nativeEvent: { pageX } }));
		return { swipeable, cancel, touchStart };
	}

	it("reveals its label in danger ink on the danger wash as the row drags left", () => {
		const { swipeable } = mountDestructive();
		expect(swipeable.props.renderLeftActions).toBeUndefined();
		const panel = render(swipeable.props.renderRightActions());
		expect(renderedText(panel)).toBe("Cancel");
		const [wash] = panel.root.findAllByType("View" as never);
		expect(wash?.props.style).toMatchObject({ backgroundColor: palettes.light.dangerBg });
		expect(panel.root.findByType("Text" as never).props.style).toMatchObject({ color: palettes.light.dangerInk });
	});

	it("acts once on a full swipe left, past half the row, and closes the row", () => {
		const { swipeable, cancel, touchStart } = mountDestructive();
		expect(swipeable.props.rightThreshold).toBe(195);
		touchStart(200);
		act(() => swipeable.props.onSwipeableOpen("left"));
		expect(cancel.run).toHaveBeenCalledOnce();
		expect(swipeableCalls.closes).toBe(1);
	});

	it("never acts on a swipe that began in the left edge band", () => {
		const { swipeable, cancel, touchStart } = mountDestructive();
		touchStart(10);
		act(() => swipeable.props.onSwipeableWillOpen("left"));
		act(() => swipeable.props.onSwipeableOpen("left"));
		expect(cancel.run).not.toHaveBeenCalled();
		expect(swipeableCalls.closes).toBe(2);
	});

	it("paints the content the color it sits on, so the panel behind never shows through it", () => {
		const tree = render(
			<SwipeRow destructive={{ key: "cancel", label: "Cancel", run: vi.fn() }} backdrop="#123456">
				<Text>ghost</Text>
			</SwipeRow>,
		);
		expect(tree.root.findByProps({ testID: "swipe-row-content" }).props.style).toEqual({ backgroundColor: "#123456" });
	});

	it("keeps the row's own reveal threshold for revealed actions", () => {
		expect(mount().swipeable.props.rightThreshold).toBeUndefined();
	});
});
