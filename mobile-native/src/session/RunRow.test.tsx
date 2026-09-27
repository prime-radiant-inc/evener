import type { ReactTestInstance } from "react-test-renderer";
import { describe, expect, it, vi } from "vitest";
import { render } from "../renderNative.testkit";
import type { RunStep, TimelineRow } from "../timeline";
import { RunRow } from "./RunRow";

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));

type Run = Extract<TimelineRow, { kind: "run" }>;

const DANGER_INK = "#C51D23";
const INK_LOW = "#6D6D64";

function step(id: string, label: string, args: Record<string, unknown>, over: Partial<RunStep> = {}): RunStep {
	return {
		kind: "activity",
		id,
		label,
		family: "tool",
		state: "completed",
		detail: { arguments: JSON.stringify(args) },
		...over,
	};
}

const run: Run = {
	kind: "run",
	id: "run:a",
	turnId: "t1",
	steps: [
		step("a", "read_file", { file_path: "agent/session.go" }, { detail: { arguments: '{"file_path":"agent/session.go"}', description: "Read the session loop" } }),
		step("b", "shell", { command: "go test ./agent/..." }, { state: "failed" }),
		step("c", "shell", { command: "go test ./agent/..." }, { state: "failed" }),
		step("d", "grep", { path: "agent", pattern: "Turn" }),
	],
};

const SUMMARY = "4 steps · read 1 file, ran go test (2 failed), searched once";

function texts(root: ReactTestInstance) {
	return root.findAll((node) => String(node.type) === "Text");
}

function textOf(node: ReactTestInstance): string {
	return node.children.map((child) => (typeof child === "string" ? child : textOf(child))).join("");
}

function header(root: ReactTestInstance) {
	return root.findAll((node) => node.props.accessibilityLabel?.startsWith?.("4 steps"))[0];
}

describe("a run folded into one line", () => {
	it("shows the summary behind a ▸ and draws the failed count in danger ink", () => {
		const tree = render(<RunRow run={run} live={false} expanded={false} onToggle={() => {}} onStep={() => {}} />);
		const line = header(tree.root);
		expect(line.props.accessibilityLabel).toBe(`${SUMMARY}, collapsed`);
		expect(line.props.accessibilityRole).toBe("button");
		expect(textOf(line)).toBe(`▸ ${SUMMARY}`);
		const failed = texts(tree.root).find((node) => textOf(node) === " (2 failed)");
		expect(failed?.props.style).toMatchObject({ color: DANGER_INK });
		// Folded, no step shows.
		expect(texts(tree.root).some((node) => textOf(node) === "Read the session loop")).toBe(false);
	});

	it("is one 44pt pressable that asks to expand", () => {
		const onToggle = vi.fn();
		const tree = render(<RunRow run={run} live={false} expanded={false} onToggle={onToggle} onStep={() => {}} />);
		const line = header(tree.root);
		expect(line.props.style).toMatchObject({ minHeight: 44 });
		line.props.onPress();
		expect(onToggle).toHaveBeenCalledTimes(1);
	});
});

describe("a run expanded into its steps", () => {
	it("lists each step's intent, its Menlo target and its status mark", () => {
		const tree = render(<RunRow run={run} live={false} expanded onToggle={() => {}} onStep={() => {}} />);
		const line = header(tree.root);
		expect(line.props.accessibilityLabel).toBe(`${SUMMARY}, expanded`);
		expect(textOf(line)).toBe(`▾ ${SUMMARY}`);
		const all = texts(tree.root).map(textOf);
		// The description when there is one, else the label.
		expect(all).toEqual(expect.arrayContaining(["Read the session loop", "shell", "grep"]));
		const target = (value: string) => texts(tree.root).find((node) => textOf(node) === value);
		expect(target("agent/session.go")?.props.style).toMatchObject({ fontFamily: "Menlo", fontSize: 13, lineHeight: 18 });
		expect(target("go test ./agent/...")?.props.style).toMatchObject({ fontFamily: "Menlo" });
		expect(target("agent")?.props.style).toMatchObject({ fontFamily: "Menlo" });
		const marks = tree.root.findAllByType("SymbolView" as never);
		expect(marks.map((mark) => [mark.props.name, mark.props.tintColor])).toEqual([
			["checkmark.circle.fill", INK_LOW],
			["xmark.octagon.fill", DANGER_INK],
			["xmark.octagon.fill", DANGER_INK],
			["checkmark.circle.fill", INK_LOW],
		]);
		expect(tree.root.findAll((node) => node.props.accessibilityLabel === "Read the session loop, agent/session.go, done").length).toBeGreaterThan(0);
		expect(tree.root.findAll((node) => node.props.accessibilityLabel === "shell, go test ./agent/..., failed").length).toBeGreaterThan(0);
	});

	it("shows no target for a step whose arguments name none", () => {
		const bare: Run = { ...run, steps: [step("e", "web_search", { query: "evener" })] };
		const tree = render(<RunRow run={bare} live={false} expanded onToggle={() => {}} onStep={() => {}} />);
		expect(texts(tree.root).filter((node) => node.props.style?.fontFamily === "Menlo")).toEqual([]);
	});

	it("hands a pressed step to onStep", () => {
		const onStep = vi.fn();
		const tree = render(<RunRow run={run} live={false} expanded onToggle={() => {}} onStep={onStep} />);
		tree.root.findAll((node) => node.props.accessibilityLabel === "grep, agent, done" && typeof node.props.onPress === "function")[0].props.onPress();
		expect(onStep).toHaveBeenCalledWith(run.steps[3]);
	});
});

describe("a live run", () => {
	it("shows its steps with no fold control, whatever the stored fold says", () => {
		const onToggle = vi.fn();
		const tree = render(<RunRow run={run} live expanded={false} onToggle={onToggle} onStep={() => {}} />);
		expect(texts(tree.root).map(textOf)).toEqual(expect.arrayContaining(["Read the session loop", "agent/session.go"]));
		expect(tree.root.findAll((node) => node.props.accessibilityRole === "button")).toEqual([]);
		expect(tree.root.findAll((node) => typeof node.props.onPress === "function" && node.props.accessibilityLabel?.startsWith?.("4 steps"))).toEqual([]);
		expect(texts(tree.root).some((node) => /[▸▾]/.test(textOf(node)))).toBe(false);
		expect(texts(tree.root).map(textOf)).toContain(SUMMARY);
	});
});
