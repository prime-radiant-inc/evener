// Subagent rows in the transcript (spec 8.2 "Subagent", 9), driven from the
// coordinator's tool calls as history carries them (agent/testdata/
// subagentwire) through the same pipeline ConversationScreen runs, at every
// detail level. A real `delegate` call settles as soon as its launch receipt
// returns, so the row's state has to come from the subagent, never the call.
import { type EvenerDelegateInfo, hydrateThread, type Thread, type ThreadItem } from "@evener/appwire-client";
import { subagentCallItems } from "@evener/appwire-client/testing/subagentWireFixtures";
import { act } from "react-test-renderer";
import { describe, expect, it, vi } from "vitest";
import { projectConversation } from "./projectedRows";
import { render, renderedText } from "./renderNative.testkit";
import { configForLevel } from "./session/detailLevels";
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
		originToolCallId: "call_delegate_1",
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
	terminal: true,
	reason: "go test exited 1 three times",
	runEndedAt: new Date(T0 + 360_000).toISOString(),
});

function thread(delegate: EvenerDelegateInfo): Thread {
	const items: ThreadItem[] = [
		{ id: "u1", turnId: "turn_1", type: "userMessage", text: "Fix the flaky test" } as ThreadItem,
		...subagentCallItems(),
		{ id: "a1", turnId: "turn_1", type: "agentMessage", text: "Waiting on the subagent." } as ThreadItem,
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
		turns: [{ id: "turn_1", itemsView: "full", status: "completed", startedAt: T0, completedAt: T0 + 60_000, items }],
		evener: { ref: "ref-1", queue: { revision: 0 }, diagnostics: { delegates: [delegate] } },
	} as unknown as Thread;
}

function rowsAt(level: Level, delegate = subagent()) {
	const model = hydrateThread({ thread: thread(delegate) }, "ref-1", 0);
	const config = configForLevel(level, null);
	const conversation = projectConversation(model, undefined, config ?? undefined);
	const presentation = projectNativeTranscript(conversation, config, { justTheConversation: level === "chat" });
	return {
		rows: hideAnswerMessages(sessionRows(groupTimeline(presentation.items), conversation.turns)),
		delegates: conversation.delegates,
	};
}

const isSubagent = (row: TimelineRow) => row.kind === "activity" && row.label === "delegate";

function subagentRow(level: Level, delegate = subagent()) {
	const { rows, delegates } = rowsAt(level, delegate);
	const found = rows.filter(isSubagent);
	expect(found).toHaveLength(1);
	const openSubagent = vi.fn();
	const tree = render(
		<TimelineItem
			item={found[0] as TimelineRow}
			hubId="hub"
			sessionRef={`subagent-${level}`}
			delegates={delegates}
			openSubagent={openSubagent}
		/>,
	);
	return { tree, openSubagent };
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

describe("a message sent to a subagent", () => {
	it.each(["intent", "tools", "full"] as const)("is a step in the run at %s, never a subagent row", (level) => {
		const { rows } = rowsAt(level);
		expect(rows.filter(isSubagent)).toHaveLength(1);
		const steps = rows.flatMap((row) => (row.kind === "run" ? row.steps : []));
		expect(steps.map((step) => step.label)).toContain("delegate_send");
	});
});

describe("Chat", () => {
	it("shows the conversation and its subagents, and no steps", () => {
		const { rows } = rowsAt("chat");
		expect(rows.filter((row) => row.kind === "run")).toEqual([]);
		expect(
			rows.filter((row) => row.kind === "activity").map((row) => (row.kind === "activity" ? row.label : "")),
		).toEqual(["delegate"]);
		expect(rows.map((row) => row.kind)).toEqual(expect.arrayContaining(["user", "assistant"]));
	});
});
