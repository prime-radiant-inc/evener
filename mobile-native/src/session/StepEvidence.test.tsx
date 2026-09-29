import type { ReactTestInstance } from "react-test-renderer";
import { act } from "react-test-renderer";
import { toolWireStep } from "@evener/appwire-client/testing/toolWireFixtures";
import { describe, expect, it, vi } from "vitest";
import { render, textOf } from "../renderNative.testkit";
import type { RunStep } from "../timeline";
import { stepEvidence } from "./evidence";
import { StepEvidence } from "./StepEvidence";

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("react-native-safe-area-context", () => ({ SafeAreaView: "SafeAreaView" }));
vi.mock("../TranscriptImages", () => ({ TranscriptImages: "TranscriptImages" }));
vi.mock("react-native-enriched-markdown", () => ({ EnrichedMarkdownText: "EnrichedMarkdownText" }));
// MarkdownResponse renders a MermaidDiagram, whose WebView ships untranspiled
// Flow source; the other suites that render it mock it the same way.
vi.mock("react-native-webview", () => ({ WebView: "WebView" }));
vi.mock("expo-clipboard", () => ({ setStringAsync: async () => true }));
vi.mock("./evidence", async (importOriginal) => {
	const original = await importOriginal<typeof import("./evidence")>();
	return { ...original, stepEvidence: vi.fn(original.stepEvidence) };
});

const ALIVE_INK = "#12763B";
const DANGER_INK = "#C51D23";
const DIFF_ADD = "#E9F4EE";
const DIFF_DEL = "#F5EAF0";
const INK_LOW = "#6D6D64";

function step(label: string, detail: RunStep["detail"], over: Partial<RunStep> = {}): RunStep {
	return { kind: "activity", id: `${label}-1`, label, family: "tool", state: "completed", detail, ...over };
}

function texts(root: ReactTestInstance) {
	return root.findAll((node) => String(node.type) === "Text");
}

function byText(root: ReactTestInstance, value: string) {
	return texts(root).find((node) => textOf(node) === value);
}

// A Menlo line draws a tab with no width, so list_dir's "tree.go\t32" read
// "tree.go32" (#3317). Tabs expand to the next multiple of 8, as a terminal
// sets them.
describe("tabs in output", () => {
	it("expands each tab to the next tab stop, from the recorded list_dir output", () => {
		const item = toolWireStep("call_list_dir");
		const listed = step("list_dir", { arguments: item.argumentsJSON, output: item.output });
		const tree = render(<StepEvidence step={listed} evidence={stepEvidence(listed)} hubId="hub-1" />);
		const lines = texts(tree.root)
			.map((node) => node.props.accessibilityLabel)
			.filter((label): label is string => typeof label === "string");
		expect(lines).toEqual(
			expect.arrayContaining(["drain_test.go   14", "tree.go 32", "tree_test.go    39", "3 entries"]),
		);
		expect(texts(tree.root).map(textOf).join("\n")).not.toContain("\t");
	});
});

// Go source is tab-indented, so a diff drawn with zero-width tabs loses its
// indentation. The +/- column counts as column 0, as a terminal's diff does.
describe("tabs in a diff", () => {
	it("expands the recorded edit's and patch's tabs to the next stop", () => {
		for (const call of ["call_edit_file", "call_apply_patch"] as const) {
			const item = toolWireStep(call);
			const edited = step(item.toolName ?? "", { arguments: item.argumentsJSON, output: item.output });
			const tree = render(<StepEvidence step={edited} evidence={stepEvidence(edited)} hubId="hub-1" />);
			const lines = texts(tree.root).map(textOf);
			expect(lines).toContain(call === "call_edit_file" ? "+       drain()" : "+       log()");
			expect(lines.join("\n")).not.toContain("\t");
		}
	});
});

describe("a command's output", () => {
	const output = Array.from({ length: 41 }, (_, n) => `line ${n + 1}`).join("\n");
	const shell = step("shell", { arguments: '{"command":"go test"}', description: "Run the tests", output });

	it("shows the first 40 lines, and the rest in a full log", () => {
		const tree = render(<StepEvidence step={shell} evidence={stepEvidence(shell)} hubId="hub-1" />);
		const shown = texts(tree.root).filter((node) => /^line \d+$/.test(node.props.accessibilityLabel ?? ""));
		expect(shown.map((node) => node.props.accessibilityLabel)).toEqual(
			Array.from({ length: 40 }, (_, n) => `line ${n + 1}`),
		);
		expect(tree.root.findAll((node) => String(node.type) === "Modal")).toEqual([]);
		const showAll = tree.root.findAll(
			(node) => String(node.type) === "Pressable" && node.props.accessibilityLabel === "Show all 41 lines",
		)[0];
		expect(textOf(showAll)).toBe("Show all 41 lines");
		act(() => showAll.props.onPress());
		const viewer = tree.root.findAll((node) => String(node.type) === "Modal")[0];
		expect(byText(viewer, "Run the tests")).toBeDefined();
		expect(viewer.findAll((node) => node.props.accessibilityLabel === "line 41").length).toBeGreaterThan(0);
	});

	it("offers no full log when every line already shows", () => {
		const short = step("shell", { output: "ok" });
		const tree = render(<StepEvidence step={short} evidence={stepEvidence(short)} hubId="hub-1" />);
		expect(tree.root.findAll((node) => /^Show all/.test(node.props.accessibilityLabel ?? ""))).toEqual([]);
	});
});

describe("an edit's diff", () => {
	const edit = step("edit_file", {
		arguments: JSON.stringify({ file_path: "a.go", old_string: "one\ntwo", new_string: "uno" }),
	});

	it("heads the diff with its counts, the minus a real minus sign", () => {
		const tree = render(<StepEvidence step={edit} evidence={stepEvidence(edit)} hubId="hub-1" />);
		const plus = byText(tree.root, "+1");
		const minus = byText(tree.root, "−2");
		expect(plus?.props.style).toMatchObject({ color: ALIVE_INK });
		expect(minus?.props.style).toMatchObject({ color: DANGER_INK });
	});

	it("washes added and removed lines, and leaves the file headers quiet", () => {
		const tree = render(<StepEvidence step={edit} evidence={stepEvidence(edit)} hubId="hub-1" />);
		const background = (value: string) => byText(tree.root, value)?.props.style.backgroundColor;
		expect(background("+uno")).toBe(DIFF_ADD);
		expect(background("-one")).toBe(DIFF_DEL);
		expect(background("-two")).toBe(DIFF_DEL);
		expect(byText(tree.root, "--- a.go")?.props.style).toMatchObject({ color: INK_LOW });
		expect(background("--- a.go")).toBeUndefined();
	});
});

describe("a write and an error", () => {
	it("says which file it wrote, the path in Menlo", () => {
		const write = step("write_file", { arguments: '{"file_path":"notes.md"}' });
		const tree = render(<StepEvidence step={write} evidence={stepEvidence(write)} hubId="hub-1" />);
		expect(byText(tree.root, "Wrote notes.md")).toBeDefined();
		expect(byText(tree.root, "notes.md")?.props.style).toMatchObject({ fontFamily: "Menlo" });
	});

	it("shows the error in danger ink, and its exit code", () => {
		const failed = step("shell", { error: "exit status 1", exitCode: 1 }, { state: "failed" });
		const tree = render(<StepEvidence step={failed} evidence={stepEvidence(failed)} hubId="hub-1" />);
		expect(byText(tree.root, "exit status 1")?.props.style).toMatchObject({
			color: DANGER_INK,
			fontSize: 15,
			lineHeight: 20,
		});
		expect(byText(tree.root, "Exit 1")?.props.style).toMatchObject({ color: INK_LOW });
	});
});

describe("a step's images", () => {
	it("show as thumbnails under its evidence", () => {
		const images = [{ id: "a:out:0", src: "/doc/image?1" }];
		const shot = { ...step("screenshot", {}), images };
		const tree = render(<StepEvidence step={shot} evidence={stepEvidence(shot)} hubId="hub-1" />);
		const thumbnails = tree.root.findAll((node) => String(node.type) === "TranscriptImages");
		expect(thumbnails.map((node) => [node.props.images, node.props.hubId])).toEqual([[images, "hub-1"]]);
	});
});

describe("the evidence it draws", () => {
	it("is the evidence its row worked out, never its own", () => {
		const shell = step("shell", { output: "ok" });
		const evidence = stepEvidence(shell);
		const calls = vi.mocked(stepEvidence).mock.calls.length;
		const tree = render(<StepEvidence step={shell} evidence={evidence} hubId="hub-1" />);
		act(() => tree.update(<StepEvidence step={{ ...shell }} evidence={evidence} hubId="hub-1" />));
		expect(vi.mocked(stepEvidence).mock.calls.length).toBe(calls);
		expect(texts(tree.root).some((node) => node.props.accessibilityLabel === "ok")).toBe(true);
	});
});

// Each tool's evidence drawn as its words (evidence.ts reads them from what the
// tools print; agent/testdata/toolwire).
describe("each tool's evidence, drawn", () => {
	const drawn = (evidence: Parameters<typeof StepEvidence>[0]["evidence"]) =>
		render(<StepEvidence step={step("tool", {})} evidence={evidence} hubId="hub-1" />);

	it("says what the shell tool's footer notes, quietly", () => {
		const tree = drawn([{ kind: "note", text: "Timed out" }]);
		expect(byText(tree.root, "Timed out")?.props.style).toMatchObject({ color: INK_LOW });
	});

	it("says a nonzero exit in danger ink", () => {
		const tree = drawn([{ kind: "exit", code: 1 }]);
		expect(byText(tree.root, "Exited 1")?.props.style).toMatchObject({ color: DANGER_INK });
	});

	it("shows a fetched page's answer under where it came from and its size", () => {
		const tree = drawn([{ kind: "page", text: "Three fixes.", url: "https://example.com/notes", bytes: 48213 }]);
		expect(byText(tree.root, "Three fixes.")).toBeTruthy();
		expect(byText(tree.root, "https://example.com/notes")?.props.style).toMatchObject({ fontFamily: "Menlo" });
		// The web's size, formatByteCount.
		expect(byText(tree.root, "48213 bytes")).toBeTruthy();
	});

	it("heads a skill's instructions with its name, the instructions as markdown", () => {
		const tree = drawn([{ kind: "markdown", title: "systematic-debugging", markdown: "# Debugging" }]);
		expect(byText(tree.root, "systematic-debugging")).toBeTruthy();
		expect(tree.root.findAll((node) => node.props.markdown === "# Debugging").length).toBeGreaterThan(0);
	});

	it("draws a task list as the Tasks sheet does, with the note the call added under its task", () => {
		const tree = drawn([
			{
				kind: "tasks",
				tasks: [
					{ id: 1, status: "done", description: "Reproduce the race", note: "Seen in 3 of 20 runs." },
					{ id: 2, status: "in_progress", description: "Fix the drain" },
					{ id: 3, status: "open", description: "Run it again" },
					{ id: 4, status: "cancelled", description: "Bisect" },
				],
			},
		]);
		expect(byText(tree.root, "✓ Reproduce the race")?.props.accessibilityLabel).toBe("Done: Reproduce the race");
		expect(byText(tree.root, "● Fix the drain")?.props.accessibilityLabel).toBe("In progress: Fix the drain");
		expect(byText(tree.root, "○ Run it again")?.props.accessibilityLabel).toBe("Open: Run it again");
		expect(byText(tree.root, "× Bisect")?.props.accessibilityLabel).toBe("Cancelled: Bisect");
		expect(byText(tree.root, "Seen in 3 of 20 runs.")?.props.style).toMatchObject({ color: INK_LOW });
	});

	it("shows the first 40 tasks of a longer list, and the rest on request", () => {
		const tasks = Array.from({ length: 41 }, (_, n) => ({
			id: n + 1,
			status: "open" as const,
			description: `task ${n + 1}`,
		}));
		const tree = drawn([{ kind: "tasks", tasks }]);
		const shown = () => texts(tree.root).filter((node) => /^○ task \d+$/.test(textOf(node)));
		expect(shown().map(textOf)).toEqual(Array.from({ length: 40 }, (_, n) => `○ task ${n + 1}`));
		const showAll = tree.root.findAll(
			(node) => String(node.type) === "Pressable" && node.props.accessibilityLabel === "Show all 41 tasks",
		)[0];
		expect(textOf(showAll)).toBe("Show all 41 tasks");
		act(() => showAll.props.onPress());
		expect(shown()).toHaveLength(41);
	});

	it("labels a tool's arguments and result, each in Menlo", () => {
		const tree = drawn([
			{ kind: "json", label: "Arguments", text: '{\n  "a": 1\n}' },
			{ kind: "json", label: "Result", text: '{\n  "b": 2\n}' },
		]);
		expect(byText(tree.root, "Arguments")).toBeTruthy();
		expect(byText(tree.root, "Result")).toBeTruthy();
		expect(byText(tree.root, '  "a": 1')?.props.style).toMatchObject({ fontFamily: "Menlo" });
	});
});
