// Subagent rows in the transcript (spec 8.2 "Subagent", 9), driven from the
// coordinator's tool calls as history carries them (agent/testdata/
// subagentwire) through the same pipeline ConversationScreen runs, at every
// detail level. A real `delegate` call settles as soon as its launch receipt
// returns, so the row's state has to come from the subagent, never the call.
import { type EvenerDelegateInfo, hydrateThread, type Thread, type ThreadItem } from "@evener/appwire-client";
import { notificationWireItem } from "@evener/appwire-client/testing/notificationWireFixtures";
import { subagentCallItems } from "@evener/appwire-client/testing/subagentWireFixtures";
import { act } from "react-test-renderer";
import { describe, expect, it, vi } from "vitest";
import { projectConversation } from "./projectedRows";
import { render, renderedText } from "./renderNative.testkit";
import { displayForLevel } from "./session/detailLevels";
import { findMatches } from "./session/findInSession";
import { hideAnswerMessages, sessionRows } from "./session/transcriptRows";
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

const T0 = Date.parse("2026-09-28T20:00:00Z");

// The delegate call's id, as the recorded corpus carries it.
// The recorded calls, parsed once: the inline tests pick items out of this list
// by identity, which a fresh parse would never match.
const CALLS = subagentCallItems();
const DELEGATE_CALL_ID = CALLS.find((item) => item.toolName === "delegate")?.callId;
const LEVELS = ["chat", "intent", "tools", "full"] as const;
type Level = (typeof LEVELS)[number];

// The subagent the delegate call launched, as the hub reports it. Its
// originItemId is the provider's item id, which never matches a wire item id,
// so only the call id can find it.
function subagent(over: Partial<EvenerDelegateInfo> = {}): EvenerDelegateInfo {
	return {
		delegateId: "dlg_1",
		type: "delegate",
		lifecycle: "stable",
		phase: "running",
		status: "running",
		terminal: false,
		resumable: false,
		needsAttention: false,
		projectionRevision: 1,
		description: "Fix race in tree settle",
		originToolCallId: DELEGATE_CALL_ID,
		originItemId: "fc_provider_item_1",
		transcriptRef: "local:child1",
		runStartedAt: new Date(T0).toISOString(),
		latestActivityAt: new Date(T0 + 50_000).toISOString(),
		...over,
	} as EvenerDelegateInfo;
}

const FAILED = subagent({
	phase: "settled",
	status: "failed",
	outcome: "failed",
	terminal: true,
	reason: "go test exited 1 three times",
	runEndedAt: new Date(T0 + 360_000).toISOString(),
});

interface Extra {
	/** The coordinator's calls, in place of the recorded ones. */
	calls?: ThreadItem[];
	/** More items at the end of the first turn. */
	items?: ThreadItem[];
	/** Turns after the first. */
	turns?: unknown[];
}

function thread(delegate: EvenerDelegateInfo, extra: Extra = {}): Thread {
	const items: ThreadItem[] = [
		{ id: "u1", turnId: "turn_1", type: "userMessage", text: "Fix the flaky test" } as ThreadItem,
		...(extra.calls ?? CALLS),
		{ id: "a1", turnId: "turn_1", type: "agentMessage", text: "Waiting on the subagent." } as ThreadItem,
		...(extra.items ?? []),
	];
	return {
		id: "thread-1",
		sessionId: "session-1",
		preview: "",
		ephemeral: false,
		modelProvider: "anthropic",
		createdAt: T0,
		updatedAt: T0,
		status: { type: "ready" },
		cwd: "/tmp",
		cliVersion: "1.0.0",
		source: "local",
		turns: [
			{ id: "turn_1", itemsView: "full", status: "completed", startedAt: T0, completedAt: T0 + 60_000, items },
			...(extra.turns ?? []),
		],
		evener: { ref: "ref-1", queue: { revision: 0 }, diagnostics: { delegates: [delegate] } },
	} as unknown as Thread;
}

function rowsAt(level: Level, delegate = subagent(), extra: Extra = {}) {
	const model = hydrateThread({ thread: thread(delegate, extra) }, "ref-1", 0);
	const { config, justTheConversation } = displayForLevel(level, null);
	const conversation = projectConversation(model, undefined, config ?? undefined);
	const presentation = projectNativeTranscript(conversation, config, { justTheConversation });
	return {
		rows: hideAnswerMessages(sessionRows(groupTimeline(presentation.items), conversation.turns)),
		delegates: conversation.delegates,
	};
}

const isSubagent = (row: TimelineRow) => row.kind === "activity" && row.label === "delegate";

function subagentRow(level: Level, delegate = subagent(), extra: Extra = {}) {
	const { rows, delegates } = rowsAt(level, delegate, extra);
	const found = rows.filter(isSubagent);
	expect(found).toHaveLength(1);
	const openSubagent = vi.fn();
	const row = found[0] as Extract<TimelineRow, { kind: "activity" }>;
	const tree = render(
		<TimelineItem
			item={row}
			hubId="hub"
			sessionRef={`subagent-${level}`}
			delegates={delegates}
			openSubagent={openSubagent}
		/>,
	);
	return { tree, openSubagent, row };
}

describe("a subagent row", () => {
	it.each(LEVELS)("reads a running subagent as running, and opens it, at %s", (level) => {
		const { tree, openSubagent } = subagentRow(level);
		expect(renderedText(tree)).toContain("Fix race in tree settle");
		expect(renderedText(tree)).toMatch(/running · \d+/);
		act(() => tree.root.findAll((node) => node.props.accessibilityRole === "button")[0]?.props.onPress());
		expect(openSubagent).toHaveBeenCalledWith("local:child1", "Fix race in tree settle");
	});

	it.each(LEVELS)("reads a failed subagent as failed at %s, though its call succeeded", (level) => {
		const { tree } = subagentRow(level, FAILED);
		expect(renderedText(tree)).toMatch(/failed · \d+/);
		expect(renderedText(tree)).toContain("go test exited 1 three times");
	});
});

// A subagent a stop ended is stopped, never failed (the transcript rows
// ruling): its row keeps the low-emphasis rail and ink a finished one has.
const stoppedBy = (status: "stopped" | "cancelled") =>
	subagent({
		phase: "settled",
		status,
		outcome: status,
		terminal: true,
		runEndedAt: new Date(T0 + 90_000).toISOString(),
	});
const DANGER = "#E3474C";
const rail = (tree: ReturnType<typeof subagentRow>["tree"]) =>
	tree.root.findAll((node) => node.props.style?.borderLeftWidth === 2)[0]?.props.style.borderLeftColor;

describe("a stopped subagent", () => {
	it.each([
		["a parent's stop", "stopped"],
		["the user's stop", "cancelled"],
	] as const)("reads as stopped after %s, at every level", (_why, status) => {
		for (const level of LEVELS) {
			const { tree } = subagentRow(level, stoppedBy(status));
			expect(renderedText(tree)).toMatch(/stopped · \d+/);
			expect(renderedText(tree)).not.toMatch(/failed/);
			expect(rail(tree)).not.toBe(DANGER);
		}
	});
});

// A resumable subagent with no ended run (queued, or its run still open) is
// not terminal, so it reads running here as it does in the Subagents list and
// on its chip: one rule (subagentState) for all three. Once a run ends the
// wire marks it terminal (agent/subagent_tally.go delegateRunTerminal), idle
// or not.
describe("an idle subagent", () => {
	it.each(LEVELS)("reads as running, as the list and chip count it, at %s", (level) => {
		const idle = subagent({ phase: "idle", status: "idle", terminal: false, resumable: true });
		const { tree } = subagentRow(level, idle);
		expect(renderedText(tree)).toMatch(/running · \d+/);
	});
});

// In inline mode the delegate call stays open while its subagent runs, and
// settles when the subagent ends; the row still reads the subagent.
describe("an inline subagent", () => {
	// The delegate call's result, which settles it.
	const result = CALLS.find((item) => item.callId === DELEGATE_CALL_ID && item.id.startsWith("item_tool_result_"));

	it("has a recorded result to leave out or fail", () => {
		expect(result).toMatchObject({ toolName: "delegate", status: "completed" });
	});

	it.each(LEVELS)("reads as running, with its time, while its call is still open, at %s", (level) => {
		const calls = CALLS.filter((item) => item !== result);
		const { tree, row } = subagentRow(level, subagent(), { calls });
		// The scenario: the call has no result yet, so it is still running.
		expect(row.state).toBe("running");
		// Only the subagent can say how long it has run: a row that read the
		// call would say a bare "running".
		expect(renderedText(tree)).toMatch(/running · \d+/);
	});

	it.each(LEVELS)("reads as stopped when its call settled as a tool error, at %s", (level) => {
		const failed = { ...result, status: "failed", error: "stopped by parent", output: undefined } as ThreadItem;
		const calls = CALLS.map((item) => (item === result ? failed : item));
		const { tree, row } = subagentRow(level, stoppedBy("stopped"), { calls });
		// The scenario: the call itself failed.
		expect(row.state).toBe("failed");
		expect(renderedText(tree)).toMatch(/stopped · \d+/);
	});
});

describe("a message sent to a subagent", () => {
	it.each(["intent", "tools", "full"] as const)("is a step in the run at %s, never a subagent row", (level) => {
		const { rows } = rowsAt(level);
		expect(rows.filter(isSubagent)).toHaveLength(1);
		const steps = rows.flatMap((row) => (row.kind === "run" ? row.steps : []));
		expect(steps.map((step) => step.label)).toContain("delegate_send");
	});
});

describe("Chat", () => {
	// Ids follow the wire's convention (apptranscript: item_tool_<entry>_<part>
	// and item_tool_result_<entry>_<part>), which is how the reducer folds a
	// result into its call.
	const shell = (id: string, entry: number, status: "completed" | "failed", image: string): ThreadItem[] => [
		{
			id: `item_tool_${entry}_0`,
			turnId: "turn_1",
			type: "commandExecution",
			toolName: "shell",
			callId: id,
			description: `Run ${id}`,
			argumentsJson: JSON.stringify({ command: "go test ./...", intent: `Run ${id}` }),
			status: "inProgress",
		} as ThreadItem,
		{
			id: `item_tool_result_${entry + 1}_0`,
			turnId: "turn_1",
			type: "commandExecution",
			toolName: "shell",
			callId: id,
			status,
			...(status === "failed" ? { error: "exit status 1", exitCode: 1 } : { output: "ok" }),
			outputImages: [{ source: "screenshot", name: image, url: `http://hub/${image}` }],
		} as ThreadItem,
	];
	const question: ThreadItem = {
		id: "ask-1",
		turnId: "turn_1",
		type: "commandExecution",
		toolName: "ask_user",
		callId: "ask-1",
		argumentsJson: JSON.stringify({
			questions: [{ header: "Store", question: "Which store?", options: [{ label: "SQLite" }, { label: "Postgres" }] }],
		}),
		status: "completed",
		output: "SQLite",
	} as ThreadItem;
	const notice: ThreadItem = {
		id: "steer-1",
		turnId: "turn_1",
		type: "steering",
		source: "",
		steeringKind: "interrupted",
		text: "The user interrupted.",
		status: "completed",
	} as ThreadItem;
	const failedTurn = {
		id: "turn_2",
		itemsView: "full",
		status: "failed",
		startedAt: T0 + 120_000,
		completedAt: T0 + 130_000,
		error: { message: "Provider exploded" },
		items: [{ id: "u2", turnId: "turn_2", type: "userMessage", text: "Go on" }],
	};
	const extra: Extra = {
		items: [
			...shell("passed", 5, "completed", "passed.png"),
			...shell("broke", 7, "failed", "broke.png"),
			question,
			notice,
		],
		turns: [failedTurn],
	};
	// Every image the transcript shows: an attachments row's, and those a step
	// carries itself (sessionRows seats a step's images on the step).
	const images = (rows: TimelineRow[]) =>
		rows.flatMap((row) =>
			row.kind === "attachments"
				? row.items.map((image) => image.name)
				: row.kind === "run"
					? row.steps.flatMap((step) => (step.images ?? []).map((image) => image.name))
					: [],
		);

	it("drops settled steps and keeps its subagents", () => {
		const { rows } = rowsAt("chat");
		const steps = rows.flatMap((row) => (row.kind === "run" ? row.steps : []));
		expect(steps).toEqual([]);
		expect(rows.filter(isSubagent)).toHaveLength(1);
		expect(rows.map((row) => row.kind)).toEqual(expect.arrayContaining(["user", "assistant"]));
	});

	it("keeps a failed step, with its image, and drops a successful one with its image", () => {
		const { rows } = rowsAt("chat", subagent(), extra);
		const steps = rows.flatMap((row) => (row.kind === "run" ? row.steps : []));
		expect(steps.map((step) => [step.label, step.state])).toEqual([["shell", "failed"]]);
		expect(images(rows)).toEqual(["broke.png"]);
	});

	it("keeps a question, a steering notice and the turn's failure", () => {
		const { rows } = rowsAt("chat", subagent(), extra);
		expect(rows.some((row) => row.kind === "activity" && row.label === "ask_user")).toBe(true);
		expect(rows.some((row) => row.kind === "notice" && row.id === "steer-1")).toBe(true);
		expect(rows.some((row) => row.kind === "failure" && row.title === "Provider exploded")).toBe(true);
	});

	it("keeps a delegate notification's card", () => {
		const report = { ...notificationWireItem("delegate-reported"), turnId: "turn_1" };
		const { rows } = rowsAt("chat", subagent(), { items: [report] });
		expect(rows.find((row) => row.id === report.id)).toMatchObject({
			kind: "notice",
			notifications: [{ kind: "notification" }],
		});
	});

	it("finds a subagent by its title", () => {
		const { rows, delegates } = rowsAt("chat");
		const hits = findMatches(rows, "fix race in tree settle", delegates);
		expect(hits.map((index) => rows[index]?.kind)).toContain("activity");
	});
});
