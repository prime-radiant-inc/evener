// Delegate and job notifications in the transcript (spec 8.2, 9), driven from
// the frames the daemon actually writes (agent/testdata/notificationwire)
// through the same pipeline ConversationScreen runs: hydrate, project, group,
// fold into session rows, then TimelineItem for each row.
import { type EvenerDelegateInfo, hydrateThread, type Thread } from "@evener/appwire-client";
import {
	type NotificationWireCase,
	notificationWireItem,
	notificationWireItems,
} from "@evener/appwire-client/testing/notificationWireFixtures";
import { act, type ReactTestInstance, type ReactTestRenderer } from "react-test-renderer";
import { describe, expect, it, vi } from "vitest";
import { projectConversation } from "./projectedRows";
import { render, renderedText, textOf } from "./renderNative.testkit";
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

const DANGER_INK = "#C51D23";
const T0 = Date.parse("2026-09-28T20:00:00Z");

function delegate(over: Partial<EvenerDelegateInfo> & { delegateId: string }): EvenerDelegateInfo {
	return {
		type: "delegate",
		lifecycle: "stable",
		phase: "settled",
		status: "completed",
		terminal: true,
		resumable: false,
		needsAttention: false,
		projectionRevision: 1,
		...over,
	} as EvenerDelegateInfo;
}

// The subagents the session knows: dlg_2's frame carries no name, so its
// title can only come from here, and every tap target comes from here.
const DELEGATES = [
	delegate({ delegateId: "dlg_1", description: "Fix race in tree settle", transcriptRef: "local:child1" }),
	delegate({ delegateId: "dlg_2", description: "Split the retry loop", transcriptRef: "local:child2" }),
	delegate({ delegateId: "dlg_3", description: "Check drain ordering", transcriptRef: "local:child3" }),
];

function thread(items = notificationWireItems()): Thread {
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
			{
				id: "turn_1",
				itemsView: "full",
				status: "completed",
				startedAt: T0,
				completedAt: T0 + 60_000,
				items: [{ id: "u1", turnId: "turn_1", type: "userMessage", text: "Fix the flaky test" }, ...items],
			},
		],
		evener: { ref: "ref-1", queue: { revision: 0 }, diagnostics: { delegates: DELEGATES } },
	} as unknown as Thread;
}

const LEVELS = ["chat", "intent", "tools", "full"] as const;

function rowsAt(level: (typeof LEVELS)[number], items = notificationWireItems()) {
	const model = hydrateThread({ thread: thread(items) }, "ref-1", 0);
	const config = configForLevel(level, null);
	const conversation = projectConversation(model, undefined, config ?? undefined);
	const presentation = projectNativeTranscript(conversation, config);
	return {
		rows: hideAnswerMessages(sessionRows(groupTimeline(presentation.items), conversation.turns)),
		delegates: conversation.delegates,
	};
}

function show(name: NotificationWireCase, openSubagent = vi.fn()) {
	const item = notificationWireItem(name);
	const { rows, delegates } = rowsAt("intent", [item]);
	const row = rows.find((candidate) => candidate.id === item.id);
	if (!row) throw new Error(`no row for ${name}`);
	const tree = render(
		<TimelineItem
			item={row}
			hubId="hub"
			sessionRef={`notification-${name}`}
			delegates={delegates}
			openSubagent={openSubagent}
		/>,
	);
	return { tree, openSubagent };
}

function textNode(tree: ReactTestRenderer, text: string): ReactTestInstance | undefined {
	return tree.root.findAll((node) => String(node.type) === "Text").find((node) => textOf(node) === text);
}

function press(tree: ReactTestRenderer) {
	act(() => tree.root.findAll((node) => node.props.accessibilityRole === "button")[0].props.onPress());
}

describe("delegate and job notifications", () => {
	it.each(LEVELS)("never show notification markup at %s", (level) => {
		const { rows, delegates } = rowsAt(level);
		const notificationRows = rows.filter((row: TimelineRow) => row.id.startsWith("item_steering_"));
		expect(notificationRows).toHaveLength(notificationWireItems().length);
		for (const row of notificationRows) {
			const text = renderedText(
				render(<TimelineItem item={row} hubId="hub" sessionRef={`markup-${level}`} delegates={delegates} />),
			);
			expect(text).not.toMatch(/<\/?(delegate|job)-notification|&lt;|"kind"|excerpt:/);
		}
	});

	it("reads a subagent's report as who finished, with the report beneath, and opens the subagent", () => {
		const { tree, openSubagent } = show("delegate-reported");
		expect(renderedText(tree)).toContain("Fix race in tree settle finished");
		expect(renderedText(tree)).toContain("Done: the settle pass now waits for the drain.");
		expect(tree.root.findAllByType("SymbolView" as never)[0]?.props.name).toBe("diamond");
		press(tree);
		expect(openSubagent).toHaveBeenCalledWith("local:child1", "Fix race in tree settle");
	});

	it("names an unnamed subagent from the session's subagents and shows its failure in red", () => {
		const { tree } = show("delegate-failed-unnamed");
		expect(textNode(tree, "Split the retry loop failed")?.props.style).toMatchObject({ color: DANGER_INK });
		expect(renderedText(tree)).toContain("go test exited 1 three times");
	});

	it("says a subagent the user stopped was stopped", () => {
		const { tree } = show("delegate-stopped");
		expect(renderedText(tree)).toContain("Check drain ordering stopped");
		expect(renderedText(tree)).toContain("Stopped by the user.");
	});

	it("says how long a quiet subagent has been quiet", () => {
		const { tree } = show("delegate-quiet");
		expect(renderedText(tree)).toContain("Fix race in tree settle quiet · 10m");
	});

	it("reads a finished job by its intent and opens its output in place", () => {
		const { tree, openSubagent } = show("job-shell-completed");
		expect(renderedText(tree)).toContain("Run the agent tests finished");
		expect(renderedText(tree)).not.toContain("primeradiant.com/evener/agent");
		press(tree);
		expect(openSubagent).not.toHaveBeenCalled();
		expect(renderedText(tree)).toContain("primeradiant.com/evener/agent");
	});

	it("reads a failed job in red with what failed beneath", () => {
		const { tree } = show("job-shell-failed");
		expect(textNode(tree, "Run the tree tests under -race failed")?.props.style).toMatchObject({ color: DANGER_INK });
		expect(renderedText(tree)).toContain("Command failed · exit 1");
	});

	it("reads each notification a steer delivers, a timer by what fired and its note", () => {
		const { tree } = show("job-pair");
		expect(renderedText(tree)).toContain("Tail the hub log stopped");
		expect(renderedText(tree)).toContain("Timer fired");
		expect(renderedText(tree)).toContain("Note: Check whether CI finished.");
	});
});
