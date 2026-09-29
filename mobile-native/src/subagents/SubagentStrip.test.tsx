// The Subagents list's strip (spec 9): one bar split by state, the chips'
// legend, read by VoiceOver as one sentence.
import { expect, it, vi } from "vitest";
import { render } from "../renderNative.testkit";
import { stripSegments } from "./subagentModel";
import { SubagentStrip } from "./SubagentStrip";

vi.mock("react-native", async () => (await import("../renderNative.testkit")).nativeModuleMock());

const tally = { total: 55, failed: 2, running: 32, done: 21 };

it("splits its width by state, and reads as one sentence", () => {
	const tree = render(<SubagentStrip tally={tally} width={361} />);
	const strip = tree.root.find((node) => node.props.accessibilityLabel === "2 failed, 32 running, 21 done");
	expect(strip.props.style.width).toBe(361);
	const segments = strip.children as unknown as { props: { style: { width: number } } }[];
	expect(segments.map((segment) => segment.props.style.width)).toEqual(
		stripSegments(tally, 361, 1).map((segment) => segment.width),
	);
});

it("draws nothing when every subagent is done", () => {
	const tree = render(<SubagentStrip tally={{ total: 3, failed: 0, running: 0, done: 3 }} width={361} />);
	expect(tree.toJSON()).toBeNull();
});
