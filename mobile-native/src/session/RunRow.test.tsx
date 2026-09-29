import { toolStepSummary } from "@evener/appwire-client";
import { act, type ReactTestInstance } from "react-test-renderer";
import { describe, expect, it, vi } from "vitest";
import { render, textOf } from "../renderNative.testkit";
import type { RunStep, TimelineRow } from "../timeline";
import { stepEvidence } from "./evidence";
import { RunRow } from "./RunRow";

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));
vi.mock("./StepEvidence", () => ({ StepEvidence: "StepEvidence" }));
vi.mock("./evidence", async (importOriginal) => {
	const original = await importOriginal<typeof import("./evidence")>();
	return { ...original, stepEvidence: vi.fn(original.stepEvidence) };
});

type Run = Extract<TimelineRow, { kind: "run" }>;

const where = { hubId: "hub-1", sessionRef: "ref-1" };

const DANGER_INK = "#C51D23";
const INK_LOW = "#6D6D64";

// A step as projectedRows builds it: its words read from the whole step.
function step(id: string, label: string, args: Record<string, unknown>, over: Partial<RunStep> = {}): RunStep {
	const built: RunStep = {
		kind: "activity",
		id,
		label,
		family: "tool",
		state: "completed",
		detail: { arguments: JSON.stringify(args) },
		...over,
	};
	const { arguments: argumentsJSON, output } = built.detail;
	return {
		...built,
		detail: { ...built.detail, summary: toolStepSummary({ toolName: label, argumentsJSON, output }) },
	};
}

const run: Run = {
	kind: "run",
	id: "run:a",
	turnId: "t1",
	steps: [
		step(
			"a",
			"read_file",
			{ file_path: "agent/session.go" },
			{ detail: { arguments: '{"file_path":"agent/session.go"}', description: "Read the session loop" } },
		),
		step("b", "shell", { command: "go test ./agent/..." }, { state: "failed" }),
		step("c", "shell", { command: "go test ./agent/..." }, { state: "failed" }),
		step("d", "grep", { path: "agent", pattern: "Turn" }),
	],
};

const SUMMARY = "4 steps · read 1 file, ran go test (2 failed), searched once";

function texts(root: ReactTestInstance) {
	return root.findAll((node) => String(node.type) === "Text");
}

function header(root: ReactTestInstance) {
	return root.findAll((node) => node.props.accessibilityLabel?.startsWith?.("4 steps"))[0];
}

describe("a run folded into one line", () => {
	it("shows the summary behind a ▸ and draws the failed count in danger ink", () => {
		const tree = render(<RunRow run={run} live={false} expanded={false} onToggle={() => {}} {...where} />);
		const line = header(tree.root);
		expect(line.props.accessibilityLabel).toBe(`${SUMMARY}, collapsed`);
		expect(line.props.accessibilityRole).toBe("button");
		expect(textOf(line)).toBe(`▸ ${SUMMARY}`);
		const failed = texts(tree.root).find((node) => textOf(node) === " (2 failed)");
		expect(failed?.props.style).toMatchObject({ color: DANGER_INK });
		// Folded, no step shows.
		expect(texts(tree.root).some((node) => textOf(node) === "Read the session loop")).toBe(false);
	});

	// A part is keyed by its kind of step, never by its words: when "ran go
	// test" becomes "ran 3 commands" as the run grows, React updates that part
	// where it is instead of tearing it down and mounting a new one.
	it("keeps each part in place while its words change", () => {
		const props = { live: false, expanded: false, onToggle: () => {}, ...where };
		const tree = render(<RunRow run={run} {...props} />);
		const failedCount = () => texts(tree.root).find((node) => textOf(node) === " (2 failed)");
		const before = failedCount();
		expect(before).toBeDefined();
		const grown: Run = { ...run, steps: [...run.steps, step("e", "shell", { command: "ls" })] };
		act(() => tree.update(<RunRow run={grown} {...props} />));
		const line = tree.root.findAll((node) => node.props.accessibilityLabel?.startsWith?.("5 steps"))[0];
		expect(textOf(line)).toBe("▸ 5 steps · read 1 file, ran 3 commands (2 failed), searched once");
		expect(failedCount()).toBe(before);
	});

	it("is one 44pt pressable that asks to expand", () => {
		const onToggle = vi.fn();
		const tree = render(<RunRow run={run} live={false} expanded={false} onToggle={onToggle} {...where} />);
		const line = header(tree.root);
		expect(line.props.style).toMatchObject({ minHeight: 44 });
		line.props.onPress();
		expect(onToggle).toHaveBeenCalledTimes(1);
	});
});

describe("a run expanded into its steps", () => {
	it("lists each step's intent, its Menlo target and its status mark", () => {
		const tree = render(<RunRow run={run} live={false} expanded onToggle={() => {}} {...where} />);
		const line = header(tree.root);
		expect(line.props.accessibilityLabel).toBe(`${SUMMARY}, expanded`);
		expect(textOf(line)).toBe(`▾ ${SUMMARY}`);
		const all = texts(tree.root).map(textOf);
		// The model's intent when there is one, else the step's words (the
		// package's toolStepSummary), which already name what it acted on.
		expect(all).toEqual(
			expect.arrayContaining(["Read the session loop", "Ran go test ./agent/...", 'Searched "Turn" in agent']),
		);
		const target = (value: string) => texts(tree.root).find((node) => textOf(node) === value);
		expect(target("agent/session.go")?.props.style).toMatchObject({
			fontFamily: "Menlo",
			fontSize: 13,
			lineHeight: 18,
		});
		// A step that reads as its words has no second, Menlo line repeating them.
		expect(
			texts(tree.root)
				.filter((node) => node.props.style?.fontFamily === "Menlo")
				.map(textOf),
		).toEqual(["agent/session.go"]);
		const marks = tree.root.findAllByType("SymbolView" as never);
		expect(marks.map((mark) => [mark.props.name, mark.props.tintColor])).toEqual([
			["checkmark.circle.fill", INK_LOW],
			["xmark.octagon.fill", DANGER_INK],
			["xmark.octagon.fill", DANGER_INK],
			["checkmark.circle.fill", INK_LOW],
		]);
		expect(
			tree.root.findAll((node) => node.props.accessibilityLabel === "Read the session loop, agent/session.go, done")
				.length,
		).toBeGreaterThan(0);
		expect(
			tree.root.findAll((node) => node.props.accessibilityLabel === "Ran go test ./agent/..., failed").length,
		).toBeGreaterThan(0);
	});

	it("shows no target for a step whose arguments name none", () => {
		const bare: Run = { ...run, steps: [step("e", "web_search", { query: "evener" })] };
		const tree = render(<RunRow run={bare} live={false} expanded onToggle={() => {}} {...where} />);
		expect(texts(tree.root).filter((node) => node.props.style?.fontFamily === "Menlo")).toEqual([]);
	});
});

describe("a step's evidence", () => {
	const withOutput: Run = {
		kind: "run",
		id: "run:s",
		turnId: "t1",
		steps: [
			step("s", "shell", { command: "go test" }, { detail: { arguments: '{"command":"go test"}', output: "ok" } }),
			step("g", "grep", { path: "agent" }),
		],
	};
	const line = (root: ReactTestInstance, label: string) =>
		root.findAll((node) => node.props.accessibilityLabel === label && node.props.accessibilityRole !== undefined)[0] ??
		root.findAll((node) => node.props.accessibilityLabel === label)[0];
	const shown = (root: ReactTestInstance) => root.findAll((node) => String(node.type) === "StepEvidence");
	const chevrons = (root: ReactTestInstance) =>
		root
			.findAllByType("SymbolView" as never)
			.map((mark) => mark.props.name)
			.filter((name: string) => name.startsWith("chevron"));

	it("opens under a step that has some, and closes again", () => {
		const tree = render(
			<RunRow run={withOutput} live={false} expanded onToggle={() => {}} hubId="hub-1" sessionRef="ref-open" />,
		);
		const shell = line(tree.root, "Ran go test, done");
		expect(shell.props.accessibilityRole).toBe("button");
		expect(shell.props.accessibilityState).toEqual({ expanded: false });
		expect(shown(tree.root)).toEqual([]);
		expect(chevrons(tree.root)).toEqual(["chevron.right"]);
		act(() => shell.props.onPress());
		expect(shown(tree.root).map((node) => [node.props.step, node.props.evidence, node.props.hubId])).toEqual([
			[withOutput.steps[0], [{ kind: "output", text: "ok", lines: 1 }], "hub-1"],
		]);
		expect(chevrons(tree.root)).toEqual(["chevron.down"]);
		act(() => line(tree.root, "Ran go test, done").props.onPress());
		expect(shown(tree.root)).toEqual([]);
	});

	// A tool the overlay showed carries tool:call:<callId> until history records
	// its call as item_tool_<entry>_<part>; the call id is the same on both.
	it("stays open when history records the step's call", () => {
		const recorded = (id: string): Run => ({
			...withOutput,
			steps: [
				step(
					"s",
					"shell",
					{ command: "go test" },
					{ detail: { arguments: '{"command":"go test"}', output: "ok", callId: "call-7" } },
				),
				...withOutput.steps.slice(1),
			].map((candidate, index) => (index === 0 ? { ...candidate, id } : candidate)),
		});
		const tree = render(
			<RunRow
				run={recorded("tool:call:call-7")}
				live={false}
				expanded
				onToggle={() => {}}
				hubId="hub-1"
				sessionRef="ref-recorded"
			/>,
		);
		act(() => line(tree.root, "Ran go test, done").props.onPress());
		expect(shown(tree.root)).toHaveLength(1);
		act(() =>
			tree.update(
				<RunRow
					run={recorded("item_tool_3_1")}
					live={false}
					expanded
					onToggle={() => {}}
					hubId="hub-1"
					sessionRef="ref-recorded"
				/>,
			),
		);
		expect(shown(tree.root)).toHaveLength(1);
	});

	it("leaves a step with nothing to show unpressable, with no chevron", () => {
		const tree = render(
			<RunRow run={withOutput} live={false} expanded onToggle={() => {}} hubId="hub-1" sessionRef="ref-none" />,
		);
		const grep = line(tree.root, "Searched files, done");
		expect(grep.props.onPress).toBeUndefined();
		expect(grep.props.accessibilityRole).toBeUndefined();
		expect(chevrons(tree.root)).toHaveLength(1);
	});

	// sessionRows copies every step on every publish (each streaming frame), so
	// a step's evidence is worked out from its data, never its object.
	it("works out a step's evidence once while its data stays the same", () => {
		const props = { live: false, expanded: true, onToggle: () => {}, hubId: "hub-1", sessionRef: "ref-memo" };
		const tree = render(<RunRow run={withOutput} {...props} />);
		const calls = vi.mocked(stepEvidence).mock.calls.length;
		for (let frame = 0; frame < 3; frame += 1) {
			const copied: Run = { ...withOutput, steps: withOutput.steps.map((copy) => ({ ...copy })) };
			act(() => tree.update(<RunRow run={copied} {...props} />));
		}
		expect(vi.mocked(stepEvidence).mock.calls.length).toBe(calls);
	});

	it("opens every step's evidence by default at the levels that open output as it arrives", () => {
		const tree = render(
			<RunRow
				run={withOutput}
				live={false}
				expanded
				onToggle={() => {}}
				hubId="hub-1"
				sessionRef="ref-default"
				evidenceOpenByDefault
			/>,
		);
		expect(shown(tree.root)).toHaveLength(1);
	});
});

describe("a live run", () => {
	it("shows its steps with no fold control, whatever the stored fold says", () => {
		const onToggle = vi.fn();
		const tree = render(<RunRow run={run} live expanded={false} onToggle={onToggle} {...where} />);
		expect(texts(tree.root).map(textOf)).toEqual(expect.arrayContaining(["Read the session loop", "agent/session.go"]));
		expect(tree.root.findAll((node) => node.props.accessibilityRole === "button")).toEqual([]);
		expect(
			tree.root.findAll(
				(node) => typeof node.props.onPress === "function" && node.props.accessibilityLabel?.startsWith?.("4 steps"),
			),
		).toEqual([]);
		expect(texts(tree.root).some((node) => /[▸▾]/.test(textOf(node)))).toBe(false);
		expect(texts(tree.root).map(textOf)).toContain(SUMMARY);
	});
});
