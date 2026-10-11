// The panel under a screen's nav bar where the bar is the system's glass: one
// glass from the screen's top under the bar and the rows beneath it.
import { Animated, Text } from "react-native";
import { describe, expect, it, vi } from "vitest";
import { render } from "../renderNative.testkit";
import { GlassHeaderPanel } from "./GlassHeaderPanel";

vi.mock("react-native", async () => (await import("../renderNative.testkit")).nativeModuleMock());

const glass = (tree: ReturnType<typeof render>) => tree.root.findAll((node) => String(node.type) === "GlassView");
const room = (tree: ReturnType<typeof render>) => tree.root.findAll((node) => node.props.testID === "nav-bar-room");

describe("GlassHeaderPanel", () => {
	it("draws one glass behind the bar's room and its rows", () => {
		const onLayout = vi.fn();
		const tree = render(
			<GlassHeaderPanel testID="panel" glassTop={64} onLayout={onLayout} style={{ position: "absolute", top: 0 }}>
				<Text>chips</Text>
			</GlassHeaderPanel>,
		);
		expect(glass(tree)).toHaveLength(1);
		expect(glass(tree)[0]?.props.glassEffectStyle).toBe("regular");
		expect(room(tree)[0]?.props.style.height).toBe(64);
		const panel = tree.root.find((node) => node.props.testID === "panel" && String(node.type) === "View");
		expect(panel.props).toMatchObject({ onLayout, style: { position: "absolute", top: 0 }, pointerEvents: "box-none" });
	});

	it("moves the glass by the slide it's given, so its lower edge follows rows sliding away", () => {
		const slide = new Animated.Value(-48);
		const tree = render(
			<GlassHeaderPanel glassTop={64} slideOffset={slide}>
				<Text>chips</Text>
			</GlassHeaderPanel>,
		);
		const layer = glass(tree)[0]?.parent;
		expect(layer?.props.style.transform).toEqual([{ translateY: slide }]);
	});

	it("is a plain container off the glass", () => {
		const tree = render(
			<GlassHeaderPanel>
				<Text>chips</Text>
			</GlassHeaderPanel>,
		);
		expect(glass(tree)).toEqual([]);
		expect(room(tree)).toEqual([]);
	});
});
