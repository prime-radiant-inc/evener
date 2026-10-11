// The Subagents list's strip (spec 9): one bar split by state, the chips'
// legend, read by VoiceOver as one sentence.
import { expect, it, vi } from "vitest";
import { render } from "../renderNative.testkit";
import { paletteFor } from "../design/tokens";
import { SubagentStrip } from "./SubagentStrip";

vi.mock("react-native", async () => (await import("../renderNative.testkit")).nativeModuleMock());

const tally = { total: 10, failed: 3, running: 2, done: 5 };

it("shows running work and neutral terminal history without a failure segment or spoken callout", () => {
	const tree = render(<SubagentStrip tally={tally} width={361} />);
	const strip = tree.root.find((node) => node.props.accessibilityLabel === "2 running, 8 done");
	expect(strip.props.style.width).toBe(361);
	const segments = strip.children as unknown as { props: { style: { width: number; backgroundColor: string } } }[];
	expect(segments.map((segment) => segment.props.style.width)).toEqual([72, 288]);
	expect(segments.map((segment) => segment.props.style.backgroundColor)).toEqual([
		paletteFor("light").alive,
		paletteFor("light").edge,
	]);
});

it.each([0, 3])("draws nothing when every subagent is terminal, including %s failures", (failed) => {
	const tree = render(<SubagentStrip tally={{ total: 3, failed, running: 0, done: 3 - failed }} width={361} />);
	expect(tree.toJSON()).toBeNull();
});
