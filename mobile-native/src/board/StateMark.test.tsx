import { describe, expect, it, vi } from "vitest";
import { render } from "../renderNative.testkit";
import { markFor, StateMark } from "./StateMark";

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));

describe("state marks pair shape with color (spec 13.1)", () => {
	it.each([
		["failed", "xmark.octagon.fill", "danger"],
		["question", "questionmark.circle.fill", "attention"],
		["approval", "hand.raised.circle.fill", "attention"],
		["warning", "exclamationmark.triangle.fill", "attention"],
		["restartNeeded", "arrow.triangle.2.circlepath.circle.fill", "attention"],
		["finished", "circle.fill", "accent"],
	] as const)("%s", (state, name, tint) => {
		expect(markFor(state, false)).toMatchObject({ name, tint });
	});

	it("moves only on Live's working rows; elsewhere working is a still green dot", () => {
		expect(markFor("working", true)).toBe("meter");
		expect(markFor("working", false)).toMatchObject({ name: "circle.fill", tint: "alive", size: 8 });
	});

	it("draws nothing for idle and shut-down rows", () => {
		expect(markFor("idle", false)).toBeNull();
		expect(markFor("shutDown", false)).toBeNull();
	});

	it("names the state for VoiceOver and tints from the palette", () => {
		const tree = render(<StateMark state="failed" />);
		const symbol = tree.root.findByType("SymbolView" as never);
		expect(symbol.props.name).toBe("xmark.octagon.fill");
		expect(symbol.props.tintColor).toMatch(/^#/);
		expect(tree.root.findAll((node) => node.props.accessibilityLabel === "Failed").length).toBeGreaterThan(0);
	});
});
