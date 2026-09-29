// A dock's scrolling body (spec 8.4): it keeps room for about two option rows
// however little the screen has, and flashes its scroll indicator once when
// what it holds is taller than the room it got.
import { act, create, type ReactTestInstance } from "react-test-renderer";
import { describe, expect, it, vi } from "vitest";
import { DockBody } from "./DockBody";
import { DOCK_BODY_MIN_HEIGHT, shrinkingScroller } from "./dockCard";

vi.mock("react-native", async () => (await import("../renderNative.testkit")).nativeModuleMock());

function mount(scale = 1) {
	const flashScrollIndicators = vi.fn();
	let tree!: ReturnType<typeof create>;
	act(() => {
		tree = create(<DockBody scale={scale}>{"A long question"}</DockBody>, {
			createNodeMock: () => ({ flashScrollIndicators }),
		});
	});
	const scroller = tree.root.findAll((node) => String(node.type) === "ScrollView")[0] as ReactTestInstance;
	const layout = (height: number) =>
		act(() => scroller.props.onLayout({ nativeEvent: { layout: { x: 0, y: 0, width: 360, height } } }));
	const content = (height: number) => act(() => scroller.props.onContentSizeChange(360, height));
	return { scroller, layout, content, flashScrollIndicators };
}

describe("a dock's scrolling body", () => {
	it("shrinks to the room left, but never below about two option rows at the text size", () => {
		expect(mount().scroller.props.style).toEqual({ ...shrinkingScroller, minHeight: DOCK_BODY_MIN_HEIGHT });
		expect(mount(1.5).scroller.props.style).toMatchObject({ minHeight: DOCK_BODY_MIN_HEIGHT * 1.5 });
	});

	it("flashes its scroll indicator once when what it holds is taller than its room", () => {
		const { layout, content, flashScrollIndicators } = mount();
		content(400);
		expect(flashScrollIndicators).not.toHaveBeenCalled();
		layout(200);
		expect(flashScrollIndicators).toHaveBeenCalledTimes(1);
		content(600);
		layout(150);
		expect(flashScrollIndicators).toHaveBeenCalledTimes(1);
	});

	it("doesn't flash when everything fits", () => {
		const { layout, content, flashScrollIndicators } = mount();
		layout(300);
		content(300);
		expect(flashScrollIndicators).not.toHaveBeenCalled();
	});
});
