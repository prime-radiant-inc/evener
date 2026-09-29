// The bar under a screen's content, above the home indicator (spec 7.1's
// Board toolbar, spec 8.1's session bar): full width, a hairline on top, the
// page fill down to the screen's edge, and room for the home indicator while
// the keyboard is down.
import { act } from "react-test-renderer";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Text } from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";
import { keyboard, render } from "../renderNative.testkit";
import { BarFrame } from "./BarFrame";
import { paletteFor } from "./tokens";

vi.mock("react-native", async () => (await import("../renderNative.testkit")).nativeModuleMock());
vi.mock("react-native-safe-area-context", () => ({
	useSafeAreaInsets: () => ({ top: 47, bottom: 34, left: 0, right: 0 }),
}));

afterEach(() => keyboard.reset());

const inset = () => useSafeAreaInsets().bottom;
const style = (node: { props: { style?: unknown } }) =>
	Object.assign({}, ...[node.props.style].flat(Number.POSITIVE_INFINITY));
// The frame's own host view, not the BarFrame element that carries its props.
const host = (tree: ReturnType<typeof render>) =>
	tree.root.find((node) => String(node.type) === "View" && node.props.testID === "bar");

describe("BarFrame", () => {
	it("draws the hairline and the page fill, and pads its content above the home indicator", () => {
		const tree = render(
			<BarFrame testID="bar">
				<Text>content</Text>
			</BarFrame>,
		);
		expect(style(host(tree))).toMatchObject({
			borderTopWidth: 0.5,
			borderColor: paletteFor("light").edge,
			backgroundColor: paletteFor("light").page,
			paddingBottom: inset(),
		});
	});

	it("drops the home indicator's room while the keyboard is up, which covers it", () => {
		const tree = render(<BarFrame testID="bar">{null}</BarFrame>);
		act(() => keyboard.show());
		expect(style(host(tree)).paddingBottom).toBe(0);
		act(() => keyboard.hide());
		expect(style(host(tree)).paddingBottom).toBe(inset());
	});

	it("takes a screen's own layout on top", () => {
		const tree = render(
			<BarFrame testID="bar" style={{ flexShrink: 1, maxHeight: "80%", paddingTop: 8 }}>
				{null}
			</BarFrame>,
		);
		expect(style(host(tree))).toMatchObject({
			flexShrink: 1,
			maxHeight: "80%",
			paddingTop: 8,
			paddingBottom: inset(),
		});
	});
});
