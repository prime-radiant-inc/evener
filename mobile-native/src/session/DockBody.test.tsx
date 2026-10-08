// A dock's scrolling body (spec 8.4): it gives up all the height it must, so
// the dock's answer controls always stay on screen, and flashes its scroll
// indicator once when what it holds is taller than the room it got.
import { act, type ReactTestInstance } from "react-test-renderer";
import { describe, expect, it, vi } from "vitest";
import { render } from "../renderNative.testkit";
import { DockBody } from "./DockBody";
import { shrinkingScroller } from "./dockCard";

vi.mock("react-native", async () => (await import("../renderNative.testkit")).nativeModuleMock());

function mount() {
	const flashScrollIndicators = vi.fn();
	const tree = render(<DockBody>{"A long question"}</DockBody>, {
		createNodeMock: () => ({ flashScrollIndicators }),
	});
	const scroller = tree.root.findAll((node) => String(node.type) === "ScrollView")[0] as ReactTestInstance;
	const layout = (height: number) =>
		act(() => scroller.props.onLayout({ nativeEvent: { layout: { x: 0, y: 0, width: 360, height } } }));
	const content = (height: number) => act(() => scroller.props.onContentSizeChange(360, height));
	return { scroller, layout, content, flashScrollIndicators };
}

describe("a dock's scrolling body", () => {
	it("shrinks to whatever room is left, with no floor that could push the answer controls off the card", () => {
		expect(mount().scroller.props.style).toEqual(shrinkingScroller);
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
