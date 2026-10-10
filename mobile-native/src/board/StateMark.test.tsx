import { describe, expect, it, vi } from "vitest";
import { render } from "../renderNative.testkit";
import { PulseMeter } from "./PulseMeter";
import { markFor, StateMark } from "./StateMark";

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));

describe("state marks pair shape with color (spec 13.1)", () => {
	it.each([
		["failed", "xmark.octagon.fill", "danger", 20],
		["question", "questionmark.circle.fill", "attention", 20],
		["approval", "hand.raised.circle.fill", "attention", 20],
		["warning", "exclamationmark.triangle.fill", "attention", 20],
		["restartNeeded", "arrow.triangle.2.circlepath.circle.fill", "attention", 20],
	] as const)("%s", (state, name, tint, size) => {
		expect(markFor(state, false)).toMatchObject({ name, tint, size });
		expect(markFor(state, true)).toEqual(markFor(state, false));
	});

	it("moves only on Live's working rows; elsewhere working is a still green dot", () => {
		expect(markFor("working", true)).toBe("meter");
		expect(markFor("working", false)).toMatchObject({ name: "circle.fill", tint: "alive", size: 8 });
	});

	it("draws nothing for idle and shut-down rows: an idle row's dot is the row's unseen dot", () => {
		expect(markFor("idle", false)).toBeNull();
		expect(markFor("shutDown", false)).toBeNull();
	});

	it("draws the meter from the per-minute counts it is given", () => {
		const minutes = [0, 3, 9];
		const tree = render(<StateMark state="working" moving perMinute={minutes} />);
		expect(tree.root.findByType(PulseMeter).props.perMinute).toEqual(minutes);
	});

	it("turns the meter amber for a stuck row, but gray wins while disconnected (spec 13.1, 16.4)", () => {
		const alive = render(<StateMark state="working" moving />);
		expect(alive.root.findByType(PulseMeter).props.tone).toBe("alive");
		const stuck = render(<StateMark state="working" moving stuck />);
		expect(stuck.root.findByType(PulseMeter).props.tone).toBe("attention");
		const offlineAndStuck = render(<StateMark state="working" moving stuck connected={false} />);
		expect(offlineAndStuck.root.findByType(PulseMeter).props.tone).toBe("gray");
	});

	it("names the state for VoiceOver and tints from the palette", () => {
		const tree = render(<StateMark state="failed" />);
		const symbol = tree.root.findByType("SymbolView" as never);
		expect(symbol.props.name).toBe("xmark.octagon.fill");
		expect(symbol.props.tintColor).toMatch(/^#/);
		expect(tree.root.findAll((node) => node.props.accessibilityLabel === "Failed").length).toBeGreaterThan(0);
	});
});
