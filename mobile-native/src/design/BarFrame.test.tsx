// The bar under a screen's content, above the home indicator (spec 7.1's
// Board toolbar, spec 8.1's session bar): full width, a hairline on top, the
// page fill down to the screen's edge, and room for the home indicator while
// the keyboard is down.
import { act } from "react-test-renderer";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Text } from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";
import { keyboard, keyboardProgress, render, systemGlass, unmountMountedTrees } from "../renderNative.testkit";
import { BarFrame } from "./BarFrame";
import { paletteFor } from "./tokens";

// The app's color scheme, which a test switches to dark.
const appearance = vi.hoisted(() => ({ scheme: "light" as "light" | "dark" }));
vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
	useColorScheme: () => appearance.scheme,
}));
vi.mock("react-native-safe-area-context", () => ({
	useSafeAreaInsets: () => ({ top: 47, bottom: 34, left: 0, right: 0 }),
}));

afterEach(() => {
	unmountMountedTrees();
	keyboard.reset();
	keyboardProgress.at = null;
	systemGlass.reset();
	appearance.scheme = "light";
});

// These pin which fill the frame chooses; what the glass looks like is in
// the Release-build screenshots on #3136.

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

	// The room closes as the keyboard rises over the indicator, frame by
	// frame with it, so the bar's content never drops when it starts to move.
	it("closes the home indicator's room as far as the keyboard has risen", () => {
		keyboardProgress.at = 0.5;
		const tree = render(<BarFrame testID="bar">{null}</BarFrame>);
		expect(style(host(tree)).paddingBottom).toBe(inset() / 2);
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

	it("wears the system's Liquid Glass where the device has it, following the app's appearance", async () => {
		systemGlass.available = true;
		const tree = render(<BarFrame testID="bar">{null}</BarFrame>);
		await act(async () => {});
		const bar = host(tree);
		expect(String(bar.type)).toBe("GlassView");
		// "auto" follows the window's appearance, which the app's own light or
		// dark choice sets (Appearance.setColorScheme).
		expect(bar.props.glassEffectStyle).toBe("regular");
		expect(bar.props.colorScheme).toBe("auto");
		// The glass is the fill; the hairline and the home indicator's room stay.
		expect(style(bar).backgroundColor).toBe("transparent");
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
		const effect = () => host(tree).props.glassEffectStyle;
		expect(effect()).toBe("none");
		expect(style(host(tree)).backgroundColor).toBe(paletteFor("light").page);
		act(() => systemGlass.setReduceTransparency(false));
		expect(effect()).toBe("regular");
		act(() => systemGlass.setReduceTransparency(true));
		expect(effect()).toBe("none");
		expect(style(host(tree)).backgroundColor).toBe(paletteFor("light").page);
	});

	it("wears the glass in the dark too, and the dark page fill where it can't", async () => {
		appearance.scheme = "dark";
		systemGlass.available = true;
		const glass = render(<BarFrame testID="bar">{null}</BarFrame>);
		await act(async () => {});
		expect(String(host(glass).type)).toBe("GlassView");
		expect(style(host(glass))).toMatchObject({ borderColor: paletteFor("dark").edge });
		systemGlass.available = false;
		const opaque = render(<BarFrame testID="bar">{null}</BarFrame>);
		await act(async () => {});
		expect(style(host(opaque)).backgroundColor).toBe(paletteFor("dark").page);
	});

	it("stays opaque if the glass module can't be read (a binary without it)", async () => {
		systemGlass.available = "throws";
		const tree = render(<BarFrame testID="bar">{null}</BarFrame>);
		await act(async () => {});
		expect(String(host(tree).type)).toBe("View");
	});

	it("stays opaque until Reduce Transparency is known to be off, at a fresh launch", async () => {
		// A fresh launch: no setting known yet from an earlier mount.
		vi.resetModules();
		const { BarFrame: FreshBarFrame } = await import("./BarFrame");
		systemGlass.available = true;
		systemGlass.readPending = true;
		const tree = render(<FreshBarFrame testID="bar">{null}</FreshBarFrame>);
		await act(async () => {});
		expect(host(tree).props.glassEffectStyle).toBe("none");
		expect(style(host(tree)).backgroundColor).toBe(paletteFor("light").page);
		await act(async () => systemGlass.answerRead());
		expect(host(tree).props.glassEffectStyle).toBe("regular");
	});

	it("keeps the glass whatever fill a screen's style asks for", async () => {
		systemGlass.available = true;
		const tree = render(
			<BarFrame testID="bar" style={{ backgroundColor: "red" }}>
				{null}
			</BarFrame>,
		);
		await act(async () => {});
		expect(String(host(tree).type)).toBe("GlassView");
		expect(style(host(tree)).backgroundColor).toBe("transparent");
	});

	it("keeps its children mounted as the glass comes and goes, so the composer keeps its focus and draft", async () => {
		systemGlass.available = true;
		const tree = render(
			<BarFrame testID="bar">
				<Text testID="composer">draft</Text>
			</BarFrame>,
		);
		await act(async () => {});
		const composer = tree.root.findByProps({ testID: "composer" });
		act(() => systemGlass.setReduceTransparency(true));
		expect(String(host(tree).type)).toBe("GlassView");
		expect(host(tree).props.glassEffectStyle).toBe("none");
		expect(style(host(tree)).backgroundColor).toBe(paletteFor("light").page);
		expect(tree.root.findByProps({ testID: "composer" })).toBe(composer);
		act(() => systemGlass.setReduceTransparency(false));
		expect(host(tree).props.glassEffectStyle).toBe("regular");
		expect(tree.root.findByProps({ testID: "composer" })).toBe(composer);
	});
});
