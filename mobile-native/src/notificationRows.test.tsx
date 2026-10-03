// Delegate and job notifications in the transcript (spec 8.2, 9), driven from
// the frames the daemon actually writes (agent/testdata/notificationwire)
// through the same pipeline ConversationScreen runs: hydrate, project, group,
// fold into session rows, then TimelineItem for each row.
import { type EvenerDelegateInfo, hydrateThread, type Thread, type ThreadItem } from "@evener/appwire-client";
import {
	type NotificationWireCase,
	notificationWireItem,
	notificationWireItems,
} from "@evener/appwire-client/testing/notificationWireFixtures";
import { act, type ReactTestInstance, type ReactTestRenderer } from "react-test-renderer";
import { describe, expect, it, vi } from "vitest";
import { projectConversation } from "./projectedRows";
import { render, renderedText, textOf } from "./renderNative.testkit";
import { displayForLevel } from "./session/detailLevels";
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
const INK_LOW = "#6D6D64";
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
	const { config, justTheConversation } = displayForLevel(level, null);
	const conversation = projectConversation(model, undefined, config ?? undefined);
	const presentation = projectNativeTranscript(conversation, config, { justTheConversation });
	return {
		rows: hideAnswerMessages(sessionRows(groupTimeline(presentation.items), conversation.turns)),
		delegates: conversation.delegates,
	};
}

function show(name: NotificationWireCase, openSubagent = vi.fn()) {
	return showItem(notificationWireItem(name), openSubagent);
}

function showItem(item: ThreadItem, openSubagent = vi.fn()) {
	const name = item.id;
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
	it.each(LEVELS.filter((level) => level !== "chat"))("never show notification markup at %s", (level) => {
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

	// The 2026-10-03 ruling at conversationOnly: Chat shows no steering rows,
	// cards included.
	it("shows no steering rows at Chat", () => {
		const { rows } = rowsAt("chat");
		expect(rows.filter((row: TimelineRow) => row.id.startsWith("item_steering_"))).toHaveLength(0);
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

	// A legacy attribute-shaped frame (recorded before packets) carries its
	// whole report in the body's excerpt and a raw reason code in the reason
	// attr: the line shows the report, never the code. Packet frames have an
	// empty excerpt, so the redesign's reason fallback still reads for them.
	it("shows a legacy failed frame's report, not its reason code", () => {
		const { tree } = showItem({
			...notificationWireItem("delegate-failed-unnamed"),
			id: "item_legacy_failed",
			text: `<delegate-notification delegate_id="dlg_2" name="Split the retry loop" status="failed" reason="exit_nonzero">
The delegate failed.
excerpt:
The retry loop splits reads from writes; the flaky test needs a fixed seed.
</delegate-notification>`,
		});
		expect(renderedText(tree)).toContain("Split the retry loop failed");
		expect(renderedText(tree)).toContain("The retry loop splits reads from writes; the flaky test needs a fixed seed.");
		expect(renderedText(tree)).not.toContain("exit_nonzero");
	});

	// A legacy failed frame with nothing beneath the headline says nothing:
	// its reason attr is a raw producer code (exit_nonzero), never display
	// prose - only a packet frame's `ending` earns the detail seat.
	it("never shows a legacy failed frame's raw reason code", () => {
		const { tree } = showItem({
			...notificationWireItem("delegate-failed-unnamed"),
			id: "item_legacy_code_only",
			text: `<delegate-notification delegate_id="dlg_2" name="Split the retry loop" status="failed" reason="exit_nonzero"></delegate-notification>`,
		});
		expect(renderedText(tree)).toContain("Split the retry loop failed");
		expect(renderedText(tree)).not.toContain("exit_nonzero");
	});

	it("says a subagent the user stopped was stopped", () => {
		const { tree } = show("delegate-stopped");
		expect(renderedText(tree)).toContain("Check drain ordering stopped");
		// The stop stub never renders as a report (the delegate redesign): the
		// headline says the stop.
		expect(renderedText(tree)).not.toContain("Stopped by the user.");
	});

	it("reads a parent's stop of a run that left its own packet as stopped", () => {
		expect(renderedText(show("delegate-stopped-by-parent-mid-run").tree)).toContain("Index the docs stopped");
	});

	it("reads a parent's stop as stopped, not failed", () => {
		const { tree } = show("delegate-stopped-by-parent");
		expect(textNode(tree, "Tail the hub log stopped")?.props.style).toMatchObject({ color: INK_LOW });
		// The bare stop packet's stub phrase is machinery, not a report: the
		// headline carries the stop and the stub never renders.
		expect(renderedText(tree)).not.toContain("stopped by parent");
		// The humanized ending says who stopped it - the hub's static head
		// shows the same words, and the raw stub stays retired.
		expect(renderedText(tree)).toContain("stopped by its coordinator");
	});

	it.each([
		["delegate-exhausted", "Sweep the flaky tests failed"],
		["job-shell-killed", "Serve the docs preview failed"],
		["job-shell-killed", "Command killed"],
		["job-shell-cancelled", "Rebuild the fuzz corpus stopped"],
		["job-shell-attention", "Run the settle tests finished"],
		["job-watch-send", "Watch delivered"],
	] as const)("reads the recorded %s frame as %j", (name, headline) => {
		expect(renderedText(show(name).tree)).toContain(headline);
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

describe("notification steers the parser can't fully read", () => {
	it("draws two identical frames in one steer as two cards", () => {
		const error = vi.spyOn(console, "error").mockImplementation(() => {});
		const item = notificationWireItem("job-shell-completed");
		const { tree } = showItem({ ...item, text: `${item.text}\n${item.text}` });
		expect(renderedText(tree).match(/Run the agent tests finished/g)).toHaveLength(2);
		expect(error).not.toHaveBeenCalled();
		error.mockRestore();
	});

	it("shows a truncated frame as a neutral line, never its markup", () => {
		const item = notificationWireItem("delegate-reported");
		const { tree } = showItem({ ...item, text: (item.text ?? "").slice(0, 60) });
		expect(renderedText(tree)).not.toMatch(/<\/?(delegate|job)-notification/);
		expect(renderedText(tree)).toContain("A notification couldn't be read");
	});

	it("never shows a killed command's -1 signal sentinel as an exit code", () => {
		expect(renderedText(show("job-shell-killed").tree)).not.toContain("exit -1");
	});

	it("opens a delegate job's report rather than its raw output", () => {
		const envelope = JSON.stringify({ message: "Settled the drain race.", data: { status: "done" } });
		const { tree } = showItem({
			...notificationWireItem("job-shell-completed"),
			id: "item_delegate_job",
			text: `<job-notification job_id="job_4" event="completed" job_type="delegate" status="completed" reason="" intent="Audit the store" output_bytes="80">
Job job_4 completed.
excerpt:
${envelope.replaceAll('"', "&quot;")}
</job-notification>`,
		});
		press(tree);
		expect(renderedText(tree)).toContain("Settled the drain race.");
		expect(renderedText(tree)).not.toContain('"data"');
	});

	// No producer writes this header any more, but durable transcripts recorded
	// while one did still replay it (steeringNotifications.ts, parseObserverCallback).
	it("reads a replayed observer callback with its message", () => {
		const { tree } = showItem({
			...notificationWireItem("delegate-quiet"),
			id: "item_observer",
			text: "Observer callback:\nmessage: The build went green.",
		});
		expect(renderedText(tree)).toContain("Observer callback");
		expect(renderedText(tree)).toContain("The build went green.");
	});

	it("says a subagent reported when its outcome is one the phone doesn't know", () => {
		const packet = JSON.stringify({
			kind: "terminal_error",
			message: "Timed out.",
			metadata: { outcome: "timed_out_someday" },
		});
		const { tree } = showItem({
			...notificationWireItem("delegate-reported"),
			text: `<delegate-notification delegate_id="dlg_1" name="Fix race in tree settle">${packet}</delegate-notification>`,
		});
		expect(renderedText(tree)).toContain("Fix race in tree settle reported");
	});
});
