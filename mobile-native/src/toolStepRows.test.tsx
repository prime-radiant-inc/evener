// A run's steps say what each did (spec 8.2 "Activity run"), driven from the
// tool calls the daemon actually sends (agent/testdata/toolwire) through the
// same pipeline ConversationScreen runs: hydrate, project, group, fold into
// session rows, then TimelineItem for each row. A step with no intent reads
// the package's summary (the web's words), and neither a step nor a run's
// line ever shows a raw tool name or "N other steps".
import { toolWireModel } from "@evener/appwire-client/testing/toolWireFixtures";
import { describe, expect, it, vi } from "vitest";
import { projectConversation } from "./projectedRows";
import { render, renderedText, textOf } from "./renderNative.testkit";
import { RunRow } from "./session/RunRow";
import { displayForLevel } from "./session/detailLevels";
import { stepEvidence } from "./session/evidence";
import { answerTo, hideAnswerMessages, runSummary, runSummaryText, sessionRows } from "./session/transcriptRows";
import { TimelineItem } from "./TimelineItem";
import { groupTimeline, type TimelineRow } from "./timeline";
import { projectNativeTranscript } from "./transcriptPresentation";

vi.mock("react-native", async () => ({
	...(await import("./renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));
vi.mock("react-native-safe-area-context", () => ({ SafeAreaView: "SafeAreaView" }));
vi.mock("react-native-enriched-markdown", () => ({ EnrichedMarkdownText: "EnrichedMarkdownText" }));
vi.mock("expo-clipboard", () => ({ setStringAsync: async () => true }));
vi.mock("./TranscriptImages", () => ({ TranscriptImages: () => null }));
vi.mock("./session/StepEvidence", () => ({ StepEvidence: () => null }));

const LEVELS = ["chat", "intent", "tools", "full"] as const;
type Level = (typeof LEVELS)[number];
type Run = Extract<TimelineRow, { kind: "run" }>;

// Every tool the corpus calls, by the name the wire carries.
const RAW_NAMES =
	/\b(read_file|grep|glob|list_dir|edit_file|write_file|apply_patch|shell|web_fetch|web_search|use_skill|github__create_issue|linear_app__list_issues|compact_context)\b/;

function rowsAt(level: Level): TimelineRow[] {
	const model = toolWireModel();
	const { config, justTheConversation } = displayForLevel(level, null);
	const conversation = projectConversation(model, undefined, config ?? undefined);
	const presentation = projectNativeTranscript(conversation, config, { justTheConversation });
	return hideAnswerMessages(sessionRows(groupTimeline(presentation.items), conversation.turns));
}

const runsAt = (level: Level) => rowsAt(level).filter((row): row is Run => row.kind === "run");

// What a run's expanded steps say, one line per step.
// Each Text's own words, its nested runs (a Menlo target) joined as drawn.
function stepText(run: Run): string {
	const tree = render(
		<RunRow run={run} live={false} expanded onToggle={() => {}} hubId="hub" sessionRef="ref-tools" />,
	);
	return tree.root
		.findAll((node) => String(node.type) === "Text")
		.map(textOf)
		.join("\n");
}

describe("a step with no intent", () => {
	it.each(["intent", "tools", "full"] as const)("says what it did in the web's words, at %s", (level) => {
		const text = runsAt(level).map(stepText).join("\n");
		for (const summary of [
			"Read agent/tree.go · lines 1-4",
			"Read agent/tree.go · lines 2-3",
			'Searched "func settle" in agent (*.go) · 2 hits',
			"Matched agent/**/*_test.go · 2 matches",
			"Listed agent · 3 entries",
			"Wrote agent/tree_order.go",
			"Patched agent/tree.go, agent/tree_drain.go · +2 -0",
			// The session's own directory: its cd is noise, as on the web.
			"Ran cat agent/tree_order.go",
			'Searched the web for "go race detector settle drain" · 2 results',
			"Activated skill: systematic-debugging",
			"Used github: create issue",
			"Used linear app: list issues",
			"Used reindex workspace",
		]) {
			expect(text).toContain(summary);
		}
	});
});

// A transcript read serves each call and its result as two items sharing a
// callId: the call carries the arguments, the result the output. The package
// folds the two into one step (reducer.ts), so the step keeps both: its line
// names what it acted on, and an edit, a write and a patch open to their diff
// or path, which come from the arguments alone (#3306).
describe("a call and its result, served as two items", () => {
	const steps = (level: Level) => runsAt(level).flatMap((run) => run.steps);
	const step = (level: Level, label: string) => {
		const found = steps(level).find((candidate) => candidate.label === label);
		if (!found) throw new Error(`no ${label} step at ${level}`);
		return found;
	};

	it.each(["intent", "tools", "full"] as const)("keeps the call's arguments on every step, at %s", (level) => {
		for (const each of steps(level)) expect(each.detail.arguments, each.label).toBeDefined();
		expect(step(level, "read_file").detail.words?.target).toBe("agent/tree.go");
		// The session's own cd is left out of the command.
		expect(step(level, "shell").detail.words?.target).toBe("cat agent/tree_order.go");
	});

	// The result's half: its output, which the call never carries.
	it.each(["tools", "full"] as const)("keeps the result's output on the step, at %s", (level) => {
		expect(step(level, "read_file").detail.output).toContain("package agent");
		expect(stepEvidence(step(level, "shell"))).toEqual([{ kind: "output", text: "package agent", lines: 1 }]);
	});

	it.each(["tools", "full"] as const)("opens an edit, a write and a patch to what they changed, at %s", (level) => {
		expect(stepEvidence(step(level, "edit_file")).map((evidence) => evidence.kind)).toEqual(["diff"]);
		expect(stepEvidence(step(level, "write_file"))).toEqual([{ kind: "wrote", path: "agent/tree_order.go" }]);
		expect(stepEvidence(step(level, "apply_patch")).map((evidence) => evidence.kind)).toEqual(["diff"]);
	});
});

// agent/testdata/toolwire records a real ask_user call and your answer after
// it: the question is a row of its own, which reads the answer you gave, and
// your composed "[answers]" message stays out of the transcript.
describe("a question put to you, with the answer you gave", () => {
	it.each(["intent", "tools", "full"] as const)("reads as its own row with your answer, at %s", (level) => {
		const rows = rowsAt(level);
		const asked = rows.find((row) => row.kind === "activity" && row.label === "ask_user");
		expect(asked).toBeDefined();
		expect(
			runsAt(level)
				.flatMap((run) => run.steps)
				.some((step) => step.label === "ask_user"),
		).toBe(false);
		expect(answerTo(toolWireModel(), asked?.id ?? "")).toBe('"Ship tonight"');
		expect(rows.some((row) => row.kind === "user" && row.text.startsWith("[answers]"))).toBe(false);
	});
});

describe("no raw tool name, anywhere", () => {
	it.each(LEVELS)("in any row or run line, at %s", (level) => {
		for (const row of rowsAt(level)) {
			const text = renderedText(render(<TimelineItem item={row} hubId="hub" sessionRef="ref-tools" />));
			expect(text).not.toMatch(RAW_NAMES);
			expect(text).not.toMatch(/other steps?\b/);
		}
		for (const run of runsAt(level)) expect(stepText(run)).not.toMatch(RAW_NAMES);
	});

	it("names each tool family in a run's line", () => {
		const lines = runsAt("tools").map((run) => runSummaryText(runSummary(run.steps)));
		const all = lines.join("\n");
		for (const part of ["used skill systematic-debugging", "used 2 MCP tools", "used reindex workspace once"]) {
			expect(all).toContain(part);
		}
	});
});
