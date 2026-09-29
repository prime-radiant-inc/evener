// The bar under a screen's content, above the home indicator (spec 7.1's
// Board toolbar, spec 8.1's session bar): full width, a hairline on top, the
// page fill down to the screen's edge, and room for the home indicator while
// the keyboard is down.
import { act } from "react-test-renderer";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Text } from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";
import { keyboard, render, systemGlass } from "../renderNative.testkit";
import { BarFrame } from "./BarFrame";
import { paletteFor } from "./tokens";

vi.mock("react-native", async () => (await import("../renderNative.testkit")).nativeModuleMock());
vi.mock("react-native-safe-area-context", () => ({
	useSafeAreaInsets: () => ({ top: 47, bottom: 34, left: 0, right: 0 }),
}));

afterEach(() => {
	keyboard.reset();
	systemGlass.reset();
});

const inset = () => useSafeAreaInsets().bottom;
const style = (node: { props: { style?: unknown } }) =>
	Object.assign({}, ...[node.props.style].flat(Number.POSITIVE_INFINITY));
// The frame's own host view (glass or plain), not the BarFrame element that
// carries its props.
const host = (tree: ReturnType<typeof render>) =>
	tree.root.find(
		(node) => (String(node.type) === "View" || String(node.type) === "GlassView") && node.props.testID === "bar",
	);

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

	it("keeps the home indicator's room, hairline and fill whatever a screen's style says", () => {
		const tree = render(
			<BarFrame testID="bar" style={{ paddingBottom: 99, borderTopWidth: 3, backgroundColor: "red" }}>
				{null}
			</BarFrame>,
		);
		expect(style(host(tree))).toMatchObject({
			paddingBottom: inset(),
			borderTopWidth: 0.5,
			backgroundColor: paletteFor("light").page,
		});
	});

	it("wears the system's Liquid Glass where the device has it, in the app's color scheme", async () => {
		systemGlass.available = true;
		const tree = render(<BarFrame testID="bar">{null}</BarFrame>);
		await act(async () => {});
		const bar = host(tree);
		expect(String(bar.type)).toBe("GlassView");
		expect(bar.props).toMatchObject({ glassEffectStyle: "regular", colorScheme: "light" });
		// The glass is the fill; the hairline and the home indicator's room stay.
		expect(style(bar).backgroundColor).toBeUndefined();
		expect(style(bar)).toMatchObject({ borderTopWidth: 0.5, paddingBottom: inset() });
	});

	it("stays opaque where the device has no Liquid Glass (before iOS 26)", async () => {
		const tree = render(<BarFrame testID="bar">{null}</BarFrame>);
		await act(async () => {});
		expect(String(host(tree).type)).toBe("View");
		expect(style(host(tree)).backgroundColor).toBe(paletteFor("light").page);
	});

	it("gives the glass up for the opaque fill while Reduce Transparency is on, following the setting", async () => {
		systemGlass.available = true;
		systemGlass.setReduceTransparency(true);
		const tree = render(<BarFrame testID="bar">{null}</BarFrame>);
		await act(async () => {});
		expect(String(host(tree).type)).toBe("View");
		act(() => systemGlass.setReduceTransparency(false));
		expect(String(host(tree).type)).toBe("GlassView");
		act(() => systemGlass.setReduceTransparency(true));
		expect(String(host(tree).type)).toBe("View");
		expect(style(host(tree)).backgroundColor).toBe(paletteFor("light").page);
	});
});
