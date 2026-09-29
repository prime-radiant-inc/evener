// ChipStrip: the shared row's fill and trailing overflow fade, apart from the
// chips each screen supplies.
import { Text } from "react-native";
import { act } from "react-test-renderer";
import { describe, expect, it, vi } from "vitest";
import { render } from "../renderNative.testkit";
import { ChipStrip } from "./ChipStrip";
import { paletteFor } from "./tokens";

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));

const palette = paletteFor("light");

const strip = (over: { onGlass?: boolean } = {}) =>
	render(
		<ChipStrip testID="chips" onGlass={over.onGlass ?? false}>
			<Text>One</Text>
			<Text>Two</Text>
		</ChipStrip>,
	);

const fill = (tree: ReturnType<typeof render>) =>
	tree.root.findAll((node) => node.props.testID === "chips" && node.props.style)[0]?.props.style.backgroundColor;
const scroller = (tree: ReturnType<typeof render>) => tree.root.findByType("ScrollView" as never);

describe("ChipStrip", () => {
	it("fills the row with the page off the glass, and clear on it", () => {
		expect(fill(strip())).toBe(palette.page);
		expect(fill(strip({ onGlass: true }))).toBe("transparent");
	});

	it("scrolls sideways with 16pt sides and an 8pt gap", () => {
		const scroll = scroller(strip());
		expect(scroll.props.horizontal).toBe(true);
		expect(scroll.props.contentContainerStyle).toMatchObject({ paddingHorizontal: 16, paddingVertical: 8, gap: 8 });
	});

	it("fades its trailing edge only once the chips overflow the row", () => {
		const tree = strip();
		const scroll = scroller(tree);
		const fade = () => tree.root.findAll((node) => node.props.testID === "chips-fade");
		expect(fade()).toEqual([]);
		act(() => {
			scroll.props.onLayout({ nativeEvent: { layout: { width: 390, height: 48 } } });
			scroll.props.onContentSizeChange(300, 48);
		});
		expect(fade()).toEqual([]);
		act(() => scroll.props.onContentSizeChange(520, 48));
		expect(fade()).toHaveLength(1);
		expect(fade()[0]?.props.pointerEvents).toBe("none");
		expect(fade()[0]?.props.style.experimental_backgroundImage).toBe(
			`linear-gradient(to right, ${palette.page}00, ${palette.page})`,
		);
	});

	it("draws no fade on the glass, however far the chips overflow", () => {
		const tree = strip({ onGlass: true });
		const scroll = scroller(tree);
		act(() => {
			scroll.props.onLayout({ nativeEvent: { layout: { width: 300, height: 48 } } });
			scroll.props.onContentSizeChange(520, 48);
		});
		expect(tree.root.findAll((node) => node.props.testID === "chips-fade")).toEqual([]);
	});
});
