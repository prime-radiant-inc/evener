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
import { hasTestID, markers, withTestID } from "./markerTestUtils";
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
const INK_LOW = "#62626D";
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
		rows: hideAnswerMessages(sessionRows(groupTimeline(presentation.items), conversation.turns, { config })),
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

/** What each notification card in the tree says happened, in order. */
function outcomes(tree: ReactTestRenderer): string[] {
	return markers(tree, "notification");
}

/** The card's one headline: who, and what happened. */
function headline(tree: ReactTestRenderer): ReactTestInstance {
	const [node, ...others] = withTestID(tree, "notification-headline");
	if (!node || others.length > 0) throw new Error("expected one notification headline");
	return node;
}

/** What the card says beneath its headline, or undefined with nothing there. */
function detail(tree: ReactTestRenderer): string | undefined {
	const [node] = withTestID(tree, "notification-detail");
	return node ? textOf(node) : undefined;
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
			const tree = render(<TimelineItem item={row} hubId="hub" sessionRef={`markup-${level}`} delegates={delegates} />);
			const text = renderedText(tree);
			expect(text).not.toMatch(/<\/?(delegate|job)-notification|"kind"|excerpt:/);
			// Literal entities the sender typed are content, not leaked framing.
			const sender = `& < &lt; &amp; &amp;lt; " > ' &quot; &gt; &#39;`;
			if (row.id === notificationWireItem("delegate-update-literal-entities").id) {
				expect(outcomes(tree)).toEqual(["update"]);
				expect(textOf(headline(tree))).toContain(sender);
				expect(detail(tree)).toBe(sender);
			} else if (row.id === notificationWireItem("watch-note-literal-entities").id) {
				expect(text).toContain(sender);
			} else {
				expect(text).not.toContain("&lt;");
			}
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
		expect(outcomes(tree)).toEqual(["completed"]);
		expect(textOf(headline(tree))).toContain("Fix race in tree settle");
		expect(detail(tree)).toContain("Done: the settle pass now waits for the drain.");
		expect(tree.root.findAllByType("SymbolView" as never)[0]?.props.name).toBe("diamond");
		press(tree);
		expect(openSubagent).toHaveBeenCalledWith("local:child1", "Fix race in tree settle");
	});

	it("names an unnamed subagent from the session's subagents and shows its failure in red", () => {
		const { tree } = show("delegate-failed-unnamed");
		expect(outcomes(tree)).toEqual(["failed"]);
		expect(textOf(headline(tree))).toContain("Split the retry loop");
		expect(headline(tree).props.style).toMatchObject({ color: DANGER_INK });
		expect(detail(tree)).toContain("go test exited 1 three times");
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
		expect(outcomes(tree)).toEqual(["failed"]);
		expect(textOf(headline(tree))).toContain("Split the retry loop");
		expect(detail(tree)).toBe("The retry loop splits reads from writes; the flaky test needs a fixed seed.");
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
		expect(outcomes(tree)).toEqual(["failed"]);
		expect(detail(tree)).toBeUndefined();
		expect(renderedText(tree)).not.toContain("exit_nonzero");
	});

	it("says a subagent the user stopped was stopped", () => {
		const { tree } = show("delegate-stopped");
		expect(outcomes(tree)).toEqual(["stopped"]);
		expect(textOf(headline(tree))).toContain("Check drain ordering");
		// The stop stub never renders as a report (the delegate redesign): the
		// headline says the stop.
		expect(renderedText(tree)).not.toContain("Stopped by the user.");
	});

	it("reads a parent's stop of a run that left its own packet as stopped", () => {
		const { tree } = show("delegate-stopped-by-parent-mid-run");
		expect(outcomes(tree)).toEqual(["stopped"]);
		expect(textOf(headline(tree))).toContain("Index the docs");
	});

	it("reads a parent's stop as stopped, not failed", () => {
		const { tree } = show("delegate-stopped-by-parent");
		expect(outcomes(tree)).toEqual(["stopped"]);
		expect(textOf(headline(tree))).toContain("Tail the hub log");
		expect(headline(tree).props.style).toMatchObject({ color: INK_LOW });
		// The bare stop packet's stub phrase is machinery, not a report: the
		// headline carries the stop and the stub never renders.
		expect(renderedText(tree)).not.toContain("stopped by parent");
		// The humanized ending says who stopped it - the hub's static head
		// shows the same words, and the raw stub stays retired.
		expect(detail(tree)).toBeTruthy();
	});

	it.each([
		["delegate-exhausted", "failed", "Sweep the flaky tests"],
		["job-shell-killed", "failed", "Serve the docs preview"],
		["job-shell-cancelled", "stopped", "Rebuild the fuzz corpus"],
		["job-shell-attention", "completed", "Run the settle tests"],
		["job-watch-send", "notice", ""],
	] as const)("reads the recorded %s frame as %s", (name, outcome, subject) => {
		const { tree } = show(name);
		expect(outcomes(tree)).toEqual([outcome]);
		expect(textOf(headline(tree))).toContain(subject);
	});

	it("says beneath a killed command what killed it", () => {
		expect(detail(show("job-shell-killed").tree)).toBeTruthy();
	});

	it("says how long a quiet subagent has been quiet", () => {
		const { tree } = show("delegate-quiet");
		expect(outcomes(tree)).toEqual(["quiet"]);
		expect(textOf(headline(tree))).toContain("Fix race in tree settle");
		expect(textOf(headline(tree))).toContain("10m");
	});

	it("reads a subagent's update as who sent it, with the message beneath, never as a report", () => {
		const { tree } = show("delegate-update");
		expect(outcomes(tree)).toEqual(["update"]);
		expect(textOf(headline(tree))).toContain("Fix race in tree settle");
		expect(detail(tree)).toContain("Which table holds the drain cursor");
	});

	it("reads a finished job by its intent and opens its output in place", () => {
		const { tree, openSubagent } = show("job-shell-completed");
		expect(outcomes(tree)).toEqual(["completed"]);
		expect(textOf(headline(tree))).toContain("Run the agent tests");
		expect(renderedText(tree)).not.toContain("primeradiant.com/evener/agent");
		press(tree);
		expect(openSubagent).not.toHaveBeenCalled();
		expect(renderedText(tree)).toContain("primeradiant.com/evener/agent");
	});

	it("reads a failed job in red with what failed beneath", () => {
		const { tree } = show("job-shell-failed");
		expect(outcomes(tree)).toEqual(["failed"]);
		expect(textOf(headline(tree))).toContain("Run the tree tests under -race");
		expect(headline(tree).props.style).toMatchObject({ color: DANGER_INK });
		// What failed, with the command's own exit status.
		expect(detail(tree)).toMatch(/\b1$/);
	});

	it("reads each notification a steer delivers, a timer by what fired and its note", () => {
		const { tree } = show("job-pair");
		expect(outcomes(tree)).toEqual(["stopped", "watch"]);
		expect(renderedText(tree)).toContain("Tail the hub log");
		expect(renderedText(tree)).toContain("Note: Check whether CI finished.");
	});
});

describe("notification steers the parser can't fully read", () => {
	it("draws two identical frames in one steer as two cards", () => {
		const error = vi.spyOn(console, "error").mockImplementation(() => {});
		const item = notificationWireItem("job-shell-completed");
		const { tree } = showItem({ ...item, text: `${item.text}\n${item.text}` });
		expect(outcomes(tree)).toEqual(["completed", "completed"]);
		expect(error).not.toHaveBeenCalled();
		error.mockRestore();
	});

	it("shows a truncated frame as a neutral line, never its markup", () => {
		const item = notificationWireItem("delegate-reported");
		const { tree } = showItem({ ...item, text: (item.text ?? "").slice(0, 60) });
		expect(renderedText(tree)).not.toMatch(/<\/?(delegate|job)-notification/);
		expect(outcomes(tree)).toEqual([]);
		expect(hasTestID(tree, "notification-unreadable")).toBe(true);
	});

	it("never shows a killed command's -1 signal sentinel as an exit code", () => {
		expect(detail(show("job-shell-killed").tree)).not.toContain("-1");
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
		expect(outcomes(tree)).toEqual(["notice"]);
		expect(detail(tree)).toBe("The build went green.");
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
		expect(outcomes(tree)).toEqual(["reported"]);
		expect(textOf(headline(tree))).toContain("Fix race in tree settle");
	});
});
