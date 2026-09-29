import type { ReactTestInstance } from "react-test-renderer";
import { act } from "react-test-renderer";
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

	it("says a nonzero exit in danger ink", () => {
		const tree = drawn([{ kind: "exit", code: 1 }]);
		expect(byText(tree.root, "Exited 1")?.props.style).toMatchObject({ color: DANGER_INK });
	});

	it("shows a fetched page's answer under where it came from and its size", () => {
		const tree = drawn([{ kind: "page", text: "Three fixes.", url: "https://example.com/notes", bytes: 48213 }]);
		expect(byText(tree.root, "Three fixes.")).toBeTruthy();
		expect(byText(tree.root, "https://example.com/notes")?.props.style).toMatchObject({ fontFamily: "Menlo" });
		expect(byText(tree.root, "48.2KB")).toBeTruthy();
	});

	it("heads a skill's instructions with its name, the instructions as markdown", () => {
		const tree = drawn([{ kind: "markdown", title: "systematic-debugging", markdown: "# Debugging" }]);
		expect(byText(tree.root, "systematic-debugging")).toBeTruthy();
		expect(tree.root.findAll((node) => node.props.markdown === "# Debugging").length).toBeGreaterThan(0);
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
