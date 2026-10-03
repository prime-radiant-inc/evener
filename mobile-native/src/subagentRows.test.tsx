import { installActivityFixture } from "./subagents/sessionActivityTestUtils";
// Subagent rows in the transcript (spec 8.2 "Subagent", 9), driven from the
// coordinator's tool calls as history carries them (agent/testdata/
// subagentwire) through the same pipeline ConversationScreen runs, at every
// detail level. A real `delegate` call settles as soon as its launch receipt
// returns, so the row's state has to come from the subagent, never the call.
import {
	type EvenerDelegateInfo,
	parseActivityTree,
	hydrateThread,
	type JobsListResponse,
	type Thread,
	type ThreadItem,
} from "@evener/appwire-client";
import { notificationWireItem } from "@evener/appwire-client/testing/notificationWireFixtures";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import {
	subagentCallItems,
	subagentOutcomesResponse,
	subagentOutcomesDelegatesResponse,
	subagentResumedDelegatesResponse,
} from "@evener/appwire-client/testing/subagentWireFixtures";
import { act } from "react-test-renderer";
import { afterEach, describe, expect, it, vi } from "vitest";
import { projectConversation } from "./projectedRows";
import { render, renderedText, textOf } from "./renderNative.testkit";
import { displayForLevel } from "./session/detailLevels";
import { findMatches } from "./session/findInSession";
import { hideAnswerMessages, sessionRows } from "./session/transcriptRows";
import { TimelineItem } from "./TimelineItem";
import { groupTimeline, type TimelineRow } from "./timeline";
import { hasFinishedSubagentRow } from "./session/subagentLine";
import { forgetSubagentTrees, holdSubagentTree, subagentTree } from "./subagents/subagentTree";
import { useTranscriptSubagentTree } from "./subagents/useTranscriptSubagentTree";
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
// The recorded delegate call's receipt: the subagent it launched, which the
// hub reports under the same id and transcript.
const RECEIPT = JSON.parse(
	CALLS.find((item) => item.callId === DELEGATE_CALL_ID && item.output !== undefined)?.output ?? "{}",
) as { delegate_id: string; transcript_ref: string };
const LEVELS = ["chat", "intent", "tools", "full"] as const;
type Level = (typeof LEVELS)[number];

// The subagent the delegate call launched, as the hub reports it. Its
// originItemId is the provider's item id, which never matches a wire item id,
// so only the call id can find it.
function subagent(over: Partial<EvenerDelegateInfo> = {}): EvenerDelegateInfo {
	return {
		delegateId: RECEIPT.delegate_id,
		type: "delegate",
		lifecycle: "stable",
		phase: "running",
		status: "running",
		terminal: false,
		resumable: false,
		needsAttention: false,
		projectionRevision: 1,
		runGeneration: 1,
		description: "Fix race in tree settle",
		originToolCallId: DELEGATE_CALL_ID,
		originItemId: "fc_provider_item_1",
		transcriptRef: RECEIPT.transcript_ref,
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
		expect(openSubagent).toHaveBeenCalledWith(RECEIPT.transcript_ref, "Fix race in tree settle");
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
		// Its line names the delegate it messaged, set apart, and the status
		// the send's footer reports.
		expect(steps.find((step) => step.label === "delegate_send")?.detail?.words).toEqual({
			verb: "Sent a message to delegate",
			target: RECEIPT.delegate_id,
			detail: "running",
		});
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

	// A daemon steer is instructions to the agent, never the conversation, so
	// Chat drops its notice; Intent and above keep it (Jesse, 2026-10-03).
	it("keeps a question and the turn's failure, and drops the steering notice", () => {
		const { rows } = rowsAt("chat", subagent(), extra);
		expect(rows.some((row) => row.kind === "activity" && row.label === "ask_user")).toBe(true);
		expect(rows.some((row) => row.kind === "notice" && row.id === "steer-1")).toBe(false);
		expect(rows.some((row) => row.kind === "failure" && row.title === "Provider exploded")).toBe(true);
		expect(rowsAt("intent", subagent(), extra).rows.some((row) => row.kind === "notice" && row.id === "steer-1")).toBe(
			true,
		);
	});

	it("drops a delegate notification's card at Chat, and keeps it at Intent", () => {
		const report = { ...notificationWireItem("delegate-reported"), turnId: "turn_1" };
		const chat = rowsAt("chat", subagent(), { items: [report] });
		expect(chat.rows.find((row) => row.id === report.id)).toBeUndefined();
		const intent = rowsAt("intent", subagent(), { items: [report] });
		expect(intent.rows.find((row) => row.id === report.id)).toMatchObject({
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

// A finished subagent's row reads its outcome as the Subagents list does
// (spec 8.2: "it reads the same as in the Subagents list"). thread/read's
// roster carries no report (appwire.SlimDelegateForRoster), so the screen
// holds the coordinator's delegates/list projection, recorded in
// agent/testdata/subagentwire/outcomes.json, and a row says what the roster
// knows until the tree shows it done. The corpus's subagents have no child
// sessions on disk, so each carries a branch.error and the tree reads as
// partial; the outcome is on the delegate itself either way.
describe("a finished subagent's outcome (audit G13)", () => {
	const COORDINATOR = { ref: "local:root", threadId: "root" };
	const finished = (delegateId: string, over: Partial<EvenerDelegateInfo> = {}) =>
		subagent({
			delegateId,
			phase: "idle",
			status: "idle",
			outcome: "completed",
			terminal: true,
			runEndedAt: new Date(T0 + 30_000).toISOString(),
			...over,
		});
	const harness: { client: FakeClient | null } = { client: null };
	const jobsLists = (client: FakeClient) =>
		client.calls.filter((call) => call.method === "evener/thread/delegates/list");

	function hub(answer: () => unknown = () => subagentOutcomesResponse()) {
		const client = new FakeClient("ready");
		installActivityFixture(client, async () => ((await answer()) as JobsListResponse).data);
		client.on("evener/thread/delegates/list", async () => {
			const old = (await answer()) as JobsListResponse;
			const parsed = parseActivityTree(old.data);
			if (!parsed) throw new Error("invalid recorded activity");
			const actual = subagentOutcomesDelegatesResponse();
			return {
				...actual,
				delegates: actual.delegates.map((row) => {
					const entry = parsed.root.entries.find(
						(entry) => entry.kind === "delegate" && entry.delegate.delegateId === row.delegateId,
					);
					const recorded = entry?.kind === "delegate" ? entry.delegate : undefined;
					return recorded?.terminal ? row : { ...row, terminal: false, reportPreview: undefined };
				}),
			};
		});
		client.on("thread/read", () => ({ thread: { id: "root", modelProvider: "scripted" } }) as never);
		harness.client = client;
		return client;
	}

	type Activity = Extract<TimelineRow, { kind: "activity" }>;
	/** One subagent row per delegate, each from its own recorded transcript. */
	function subagentRows(delegates: EvenerDelegateInfo[]) {
		return delegates.map((delegate) => {
			const at = rowsAt("intent", delegate);
			return { row: { ...(at.rows.find(isSubagent) as Activity), id: delegate.delegateId }, delegates: at.delegates };
		});
	}

	/** The session screen's part: it holds the tree while its transcript has a
	 * finished subagent row, and renders the rows the list has mounted. */
	function Transcript({
		delegates,
		mounted = delegates.map((delegate) => delegate.delegateId),
		inFront = true,
		receivesUpdates = true,
	}: {
		delegates: EvenerDelegateInfo[];
		mounted?: string[];
		inFront?: boolean;
		receivesUpdates?: boolean;
	}) {
		const rows = subagentRows(delegates);
		const target = hasFinishedSubagentRow(
			rows.map((entry) => entry.row),
			delegates,
		)
			? COORDINATOR
			: null;
		const tree = useTranscriptSubagentTree("hub-1", target, harness.client, { inFront, receivesUpdates });
		return (
			<>
				{rows
					.filter((entry) => mounted.includes(entry.row.id))
					.map((entry) => (
						<TimelineItem
							key={entry.row.id}
							item={entry.row}
							hubId="hub-1"
							sessionRef="local:root"
							delegates={entry.delegates}
							subagentTree={tree}
						/>
					))}
			</>
		);
	}

	const screens: ReturnType<typeof render>[] = [];
	function transcript(props: Parameters<typeof Transcript>[0]) {
		const screen = render(<Transcript {...props} />);
		screens.push(screen);
		return screen;
	}
	const rowText = (screen: ReturnType<typeof render>, delegateId: string) =>
		textOf(screen.root.find((node) => node.type === TimelineItem && node.props.item.id === delegateId));

	afterEach(() => {
		for (const screen of screens.splice(0)) act(() => screen.unmount());
		forgetSubagentTrees("hub-1");
	});

	it("says Finished until the tree comes, then the report's opening line", async () => {
		const client = hub();
		const screen = transcript({ delegates: [finished("dlg_reported")] });
		expect(renderedText(screen)).toContain("Finished");
		await act(async () => {});
		expect(renderedText(screen)).toContain("Fixed the race: settle now waits for the drain.");
		expect(renderedText(screen)).not.toContain("The new test covers both orders.");
		expect(jobsLists(client)).toHaveLength(1);
	});

	it("does not borrow the prior generation report while a resumed completion awaits its activity read", async () => {
		vi.useFakeTimers();
		const client = hub();
		const old = subagentOutcomesDelegatesResponse();
		const next = subagentResumedDelegatesResponse();
		const prior = old.delegates.find((row) => row.delegateId === "dlg_reported");
		const resumed = next.delegates.find((row) => row.delegateId === "dlg_reported");
		if (!prior || !resumed) throw new Error("producer corpus missing resumed run");
		expect(resumed.runStartedAt).toBe(prior.runStartedAt);
		expect(resumed.runEndedAt).toBe(prior.runEndedAt);
		let reply = old;
		client.on("evener/thread/delegates/list", () => reply);
		const screen = transcript({ delegates: [finished("dlg_reported", { runGeneration: prior.runGeneration })] });
		await act(async () => {});
		expect(renderedText(screen)).toContain("Fixed the race: settle now waits for the drain.");
		const running = subagent({ delegateId: "dlg_reported", runGeneration: resumed.runGeneration });
		act(() => screen.update(<Transcript delegates={[running]} />));
		const done = finished("dlg_reported", { runGeneration: resumed.runGeneration });
		act(() => screen.update(<Transcript delegates={[done]} />));
		expect(renderedText(screen)).toContain("Finished");
		expect(renderedText(screen)).not.toContain("Fixed the race: settle now waits for the drain.");
		reply = next;
		await act(async () => {
			client.emitNotification({
				method: "evener/thread/activity/changed",
				params: { ref: COORDINATOR.ref, threadId: "root", sessionId: "root", resources: ["delegates"] },
			});
			await vi.runOnlyPendingTimersAsync();
		});
		expect(renderedText(screen)).toContain("Second run report.");
	});

	it("does not borrow a recorded report belonging to another owner with the same delegate ID", async () => {
		const client = hub();
		const actual = subagentOutcomesDelegatesResponse();
		client.on("evener/thread/delegates/list", () => ({
			...actual,
			delegates: actual.delegates.map((row) => ({ ...row, ownerRef: "local:unrelated" })),
		}));
		const screen = transcript({ delegates: [finished("dlg_reported")] });
		await act(async () => {});
		expect(renderedText(screen)).toContain("Finished");
		expect(renderedText(screen)).not.toContain("Fixed the race: settle now waits for the drain.");
	});

	it("says Stopped for a subagent the user stopped, with no tree to read", async () => {
		const client = hub();
		const screen = transcript({ delegates: [finished("dlg_stopped", { outcome: "cancelled" })] });
		await act(async () => {});
		expect(renderedText(screen)).toContain("Stopped");
		expect(renderedText(screen)).not.toContain("Stopped by the user.");
		expect(jobsLists(client)).toHaveLength(0);
	});

	it("keeps saying Finished, with nothing loading, when jobs/list fails", async () => {
		const client = hub(() => {
			throw new Error("jobs/list unavailable");
		});
		const screen = transcript({ delegates: [finished("dlg_reported")] });
		await act(async () => {});
		expect(jobsLists(client)).toHaveLength(1);
		expect(renderedText(screen)).toContain("Finished");
		expect(screen.root.findAll((node) => String(node.type) === "ActivityIndicator")).toHaveLength(0);
	});

	it("reads no tree while no subagent has finished", async () => {
		const client = hub();
		transcript({ delegates: [subagent({ delegateId: "dlg_reported" })] });
		await act(async () => {});
		expect(jobsLists(client)).toHaveLength(0);
	});

	it("leaves a failed row's reason as the roster gives it", async () => {
		hub();
		const failed = finished("dlg_failed", { status: "failed", outcome: "failed", reason: "go test exited 1" });
		const screen = transcript({ delegates: [finished("dlg_reported"), failed] });
		await act(async () => {});
		expect(rowText(screen, "dlg_reported")).toContain("Fixed the race: settle now waits for the drain.");
		expect(rowText(screen, "dlg_failed")).toContain("go test exited 1");
		expect(rowText(screen, "dlg_failed")).not.toContain("provider returned 500");
	});

	it("shares the Subagents list's tree, so the list and two rows ask for it once", async () => {
		const client = hub();
		// The Subagents list holds the coordinator's tree, as useSubagentTree does.
		const held = subagentTree("hub-1", COORDINATOR.ref, COORDINATOR.threadId);
		const release = holdSubagentTree(held);
		await act(async () => {
			await held.setClient(client as never);
		});
		const screen = transcript({
			delegates: [finished("dlg_reported"), finished("dlg_stopped", { outcome: "completed" })],
		});
		await act(async () => {});
		expect(rowText(screen, "dlg_reported")).toContain("Fixed the race: settle now waits for the drain.");
		expect(jobsLists(client)).toHaveLength(1);
		release();
	});

	it("reads the tree once while finished rows scroll off and back on", async () => {
		const client = hub();
		const delegates = [finished("dlg_reported")];
		const screen = transcript({ delegates });
		await act(async () => {});
		// The list recycles every finished row off screen, then brings one back.
		act(() => screen.update(<Transcript delegates={delegates} mounted={[]} />));
		act(() => screen.update(<Transcript delegates={delegates} />));
		await act(async () => {});
		expect(renderedText(screen)).toContain("Fixed the race: settle now waits for the drain.");
		expect(jobsLists(client)).toHaveLength(1);
	});

	it("reads the report once the subagent finishes while the tree is held", async () => {
		const recorded = subagentOutcomesResponse() as { data: { revision: number; root: { entries: unknown[] } } };
		// The first read lands a revision earlier, while the subagent still runs:
		// no outcome, no report.
		const running = structuredClone(recorded);
		running.data.revision -= 1;
		for (const entry of running.data.root.entries as { delegate?: Record<string, unknown> }[]) {
			if (entry.delegate?.delegateId !== "dlg_reported") continue;
			entry.delegate.projectionRevision = running.data.revision;
			for (const key of ["outcome", "terminal", "packetKind", "message", "runEndedAt"]) delete entry.delegate[key];
		}
		let answer: unknown = running;
		const client = hub(() => answer);
		const screen = transcript({ delegates: [finished("dlg_reported")] });
		await act(async () => {});
		expect(renderedText(screen)).toContain("Finished");
		answer = recorded;
		await act(async () => {
			client.emitNotification({
				method: "evener/thread/activity/changed",
				params: {
					ref: COORDINATOR.ref,
					threadId: COORDINATOR.threadId,
					sessionId: COORDINATOR.threadId,
					resources: ["delegates"],
				},
			});
		});
		expect(jobsLists(client)).toHaveLength(2);
		expect(renderedText(screen)).toContain("Fixed the race: settle now waits for the drain.");
	});

	// A subagent's screen follows the subagent, so nothing announces that one
	// of its own subagents finished to its coordinator's tree.
	it.each([
		["reads again when a subagent's screen comes back to the front", false, 2],
		["leaves a coordinator's own screen to its delegates' updates", true, 1],
	] as const)("%s", async (_name, receivesUpdates, reads) => {
		const client = hub();
		const delegates = [finished("dlg_reported")];
		const screen = transcript({ delegates, receivesUpdates });
		await act(async () => {});
		act(() => screen.update(<Transcript delegates={delegates} receivesUpdates={receivesUpdates} inFront={false} />));
		await act(async () => {
			screen.update(<Transcript delegates={delegates} receivesUpdates={receivesUpdates} />);
		});
		expect(jobsLists(client)).toHaveLength(reads);
	});
});
