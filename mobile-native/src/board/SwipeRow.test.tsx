import { Text } from "react-native";
import { act, type ReactTestRenderer } from "react-test-renderer";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { palettes } from "../design/tokens";
import {
	openSwipeRow,
	type PanGestureMock,
	releaseSwipeRow,
	render,
	renderedText,
	swipeableCalls,
} from "../renderNative.testkit";
import { type SwipeAction, SwipeRow, swipeAccessibility } from "./SwipeRow";

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));
vi.mock("react-native-gesture-handler/ReanimatedSwipeable", async () =>
	(await import("../renderNative.testkit")).gestureHandlerModuleMock(),
);
vi.mock("react-native-gesture-handler", async () =>
	(await import("../renderNative.testkit")).gestureDetectorModuleMock(),
);

const action = (key: string, label: string) => ({
	key,
	label,
	symbol: "archivebox" as const,
	fill: "inkMid" as const,
	run: vi.fn(),
});
/** The row's swipeable, and the finger's touch and release as the row sees
 * them: where the touch began, and how far it had dragged the row when it
 * let go. */
function driver(tree: ReactTestRenderer) {
	const swipeable = tree.root.findByType("ReanimatedSwipeable" as never);
	const tracker = tree.root.findAllByType("GestureDetector" as never)[0]?.props.gesture as PanGestureMock | undefined;
	const touchStart = (pageX: number) =>
		act(() => tree.root.findByProps({ testID: "swipe-row-content" }).props.onTouchStart({ nativeEvent: { pageX } }));
	const release = (translationX: number, velocityX = 0, via: readonly number[] = []) =>
		releaseSwipeRow(swipeable, translationX, { velocityX, via });
	const open = (direction: "left" | "right") => openSwipeRow(swipeable, direction);
	return { swipeable, tracker, touchStart, release, open };
}
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
	return { ...driver(tree), leading, pin, onActiveChange };
}
beforeEach(() => {
	swipeableCalls.closes = 0;
});

it("a full swipe right does the leading action once and closes the row", () => {
	const { swipeable, leading, touchStart, release, open } = mount();
	expect(swipeable.props.leftThreshold).toBe(195);
	touchStart(200);
	release(250, 400);
	open("right");
	expect(leading.run).toHaveBeenCalledOnce();
	expect(swipeableCalls.closes).toBe(1);
});

it("a short fast flick right that the swipeable opens by its velocity closes without acting", () => {
	const { leading, touchStart, release, open } = mount();
	touchStart(200);
	release(60, 2700);
	open("right");
	expect(leading.run).not.toHaveBeenCalled();
	expect(swipeableCalls.closes).toBe(1);
});

it("a drag right past half the row that came back short before release closes without acting", () => {
	const { leading, touchStart, release, open } = mount();
	touchStart(200);
	release(150, 1500, [250]);
	open("right");
	expect(leading.run).not.toHaveBeenCalled();
	expect(swipeableCalls.closes).toBe(1);
});

it("tracks the finger with a pan that activates as the row's own does and recognizes alongside it", () => {
	const { swipeable, tracker } = mount();
	expect(tracker?.config).toMatchObject({
		runOnJS: true,
		activeOffsetX: [-10, 10],
		hitSlop: { left: -24 },
		enabled: true,
	});
	expect(tracker?.config).not.toHaveProperty("activeOffsetY");
	expect(swipeable.props.simultaneousWithExternalGesture).toBe(tracker);
});

it("keeps a full swipe decided at release when a new touch lands while the row springs open", () => {
	const { swipeable, leading, tracker, touchStart, release } = mount();
	touchStart(200);
	release(250, 400);
	act(() => swipeable.props.onSwipeableWillOpen("right"));
	act(() => tracker?.handlers.onBegin?.());
	act(() => swipeable.props.onSwipeableOpen("right"));
	expect(leading.run).toHaveBeenCalledOnce();
	act(() => swipeable.props.onSwipeableOpen("right"));
	expect(leading.run).toHaveBeenCalledOnce();
	expect(swipeableCalls.closes).toBe(2);
});

it("holds its tracker still while the row is switched off", () => {
	const tree = render(
		<SwipeRow destructive={{ key: "cancel", label: "Cancel", run: vi.fn() }} enabled={false}>
			<Text>ghost</Text>
		</SwipeRow>,
	);
	expect(driver(tree).tracker?.config.enabled).toBe(false);
});

it("a swipe that began in the left edge band closes without acting, whichever way it opened", () => {
	const { leading, pin, touchStart, release, open } = mount();
	touchStart(10);
	release(250);
	open("right");
	release(-250);
	open("left");
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
		return { ...driver(tree), cancel };
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

	it("acts once on a full swipe left, past half the row at release, and closes the row", () => {
		const { swipeable, cancel, touchStart, release, open } = mountDestructive();
		expect(swipeable.props.rightThreshold).toBe(195);
		touchStart(200);
		release(-250, -400);
		open("left");
		expect(cancel.run).toHaveBeenCalledOnce();
		expect(swipeableCalls.closes).toBe(1);
	});

	it("never acts on a short fast flick left that the swipeable opens by its velocity", () => {
		const { cancel, touchStart, release, open } = mountDestructive();
		touchStart(200);
		release(-60, -2700);
		open("left");
		expect(cancel.run).not.toHaveBeenCalled();
		expect(swipeableCalls.closes).toBe(1);
	});

	it("never acts on a drag left past half the row that came back short before release", () => {
		const { cancel, touchStart, release, open } = mountDestructive();
		touchStart(200);
		release(-150, -1500, [-250]);
		open("left");
		expect(cancel.run).not.toHaveBeenCalled();
		expect(swipeableCalls.closes).toBe(1);
	});

	it("passes its finger tracker to the swipeable to recognize alongside it", () => {
		const { swipeable, tracker } = mountDestructive();
		expect(tracker).toBeDefined();
		expect(swipeable.props.simultaneousWithExternalGesture).toBe(tracker);
	});

	it("never acts on a swipe that began in the left edge band", () => {
		const { cancel, touchStart, release, open } = mountDestructive();
		touchStart(10);
		release(-250);
		open("left");
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

	it("stays mounted but still while switched off", () => {
		const tree = render(
			<SwipeRow destructive={{ key: "cancel", label: "Cancel", run: vi.fn() }} enabled={false}>
				<Text>ghost</Text>
			</SwipeRow>,
		);
		expect(tree.root.findByType("ReanimatedSwipeable" as never).props.enabled).toBe(false);
		expect(mount().swipeable.props.enabled).toBe(true);
	});

	it("keeps the row's own reveal threshold for revealed actions", () => {
		expect(mount().swipeable.props.rightThreshold).toBeUndefined();
	});
});
