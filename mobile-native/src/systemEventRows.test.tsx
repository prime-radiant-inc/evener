// Failed turns, system events and the daemon's steers in the transcript
// (spec 8.2 "Error" and "System event"), driven from what the daemon actually
// sends (agent/testdata/systemeventwire) through the same pipeline
// ConversationScreen runs: hydrate, project, group, fold into session rows,
// then TimelineItem for each row.
import {
	hydrateThread,
	makeTranscriptDisplayConfig,
	shippedConfig,
	stripSystemReminder,
	type Thread,
	type ThreadItem,
	type Turn,
} from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { wireThread } from "@evener/appwire-client/testing/notifications";
import { summaryFailureWireFixture } from "@evener/appwire-client/testing/summaryFailureWireFixtures";
import {
	type SystemEventWireCase,
	systemEventWireFailedTurn,
	systemEventWireFailedTurnWithOtherError,
	systemEventWireItem,
} from "@evener/appwire-client/testing/systemEventWireFixtures";
import { act, type ReactTestRenderer } from "react-test-renderer";
import { describe, expect, it, vi } from "vitest";
import { createConversationService } from "../../mobile/src/services/conversation";
import { createActivityStore } from "../../mobile/src/state/activity";
import { createConversationStore } from "../../mobile/src/state/conversation";
import { palettes } from "./design/tokens";
import { MAX_ITEM_BYTES, projectConversation } from "./projectedRows";
import { withTestID } from "./markerTestUtils";
import { offersButton, render, renderedText, textOf } from "./renderNative.testkit";
import { displayForLevel } from "./session/detailLevels";
import { errorAction } from "./session/errorAction";
import { hideAnswerMessages, sessionRows } from "./session/transcriptRows";
import { TimelineItem } from "./TimelineItem";
import { groupTimeline, isCriticalNotice, type TimelineRow } from "./timeline";
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
const LEVELS = ["chat", "intent", "tools", "activity", "full"] as const;
type Level = (typeof LEVELS)[number];

function thread(turns: Turn[]): Thread {
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
		turns,
		evener: { ref: "ref-1", queue: { revision: 0 }, diagnostics: { delegates: [] } },
	} as unknown as Thread;
}

function completedTurn(items: ThreadItem[]): Turn {
	return {
		id: "turn_1",
		itemsView: "full",
		status: "completed",
		startedAt: T0,
		completedAt: T0 + 60_000,
		items: [{ id: "u1", turnId: "turn_1", type: "userMessage", text: "Fix the flaky test" }, ...items],
	} as unknown as Turn;
}

// The hub's display settings with system events on (Hub > Display), so a
// system event shows at every level.
function rowsAt(level: Level, turns: Turn[], systemEvents = true) {
	const model = hydrateThread({ thread: thread(turns) }, "ref-1", 0);
	const shipped = shippedConfig("mobile");
	const hub = makeTranscriptDisplayConfig(shipped.content, { ...shipped.advanced, systemEvents });
	const { config, justTheConversation } = displayForLevel(level, hub);
	const conversation = projectConversation(model, undefined, config ?? undefined);
	const presentation = projectNativeTranscript(conversation, config, { justTheConversation });
	return {
		rows: hideAnswerMessages(sessionRows(groupTimeline(presentation.items), conversation.turns, { config })),
		model,
		expandByDefault: presentation.expandByDefault,
	};
}

function rowFor(name: SystemEventWireCase, level: Level = "intent"): TimelineRow | undefined {
	const item = systemEventWireItem(name);
	return rowsAt(level, [completedTurn([item])]).rows.find((row) => row.id === item.id);
}

function show(name: SystemEventWireCase, level: Level = "intent"): ReactTestRenderer {
	const row = rowFor(name, level);
	if (!row) throw new Error(`no row for ${name}`);
	return render(<TimelineItem item={row} hubId="hub" sessionRef={`system-${name}`} />);
}

function press(tree: ReactTestRenderer) {
	act(() => tree.root.findAll((node) => node.props.accessibilityRole === "button")[0].props.onPress());
}

/** What an opened labelled event says beneath its label; undefined while it
 * is closed. */
function eventBody(tree: ReactTestRenderer): string | undefined {
	const [body] = withTestID(tree, "system-event-body");
	return body ? textOf(body) : undefined;
}

/** The event's own line, as its button reads it. */
function eventLabel(tree: ReactTestRenderer): string {
	const [button] = tree.root.findAll((node) => node.props.accessibilityRole === "button");
	return button?.props.accessibilityLabel ?? "";
}

function inked(tree: ReactTestRenderer, color: string): boolean {
	return (
		tree.root.findAll(
			(node) =>
				(node.props.style?.color === color || node.props.style?.borderLeftColor === color) &&
				typeof node.type === "string",
		).length > 0
	);
}

describe("a failed turn (G5)", () => {
	it.each(LEVELS)("shows one failure, at the turn's end, with its one action, at %s", (level) => {
		const { rows, model } = rowsAt(level, [systemEventWireFailedTurn()], false);
		const failures = rows.filter(
			(row) => row.kind === "failure" || (row.kind === "notice" && row.eventKind === "error"),
		);
		expect(failures.map((row) => row.id)).toEqual(["failure:turn_2"]);
		expect(rows.at(-1)?.id).toBe("failure:turn_2");
		const failure = failures[0];
		if (failure?.kind !== "failure") throw new Error("not a failure row");
		expect(failure.title).toContain("Evener error");
		expect(failure.detail).toContain("Provider exploded: 529 overloaded");
		expect(errorAction(failure, model, true)).toBe("retry");
	});

	// Only the error that echoes turn.error goes: an earlier, distinct error in
	// the same turn is news of its own.
	it.each(LEVELS)("keeps a distinct earlier error in a failed turn, at %s", (level) => {
		const { rows } = rowsAt(level, [systemEventWireFailedTurnWithOtherError()], false);
		const errors = rows.filter((row) => row.kind === "failure" || (row.kind === "notice" && row.eventKind === "error"));
		expect(errors.map((row) => row.id)).toEqual(["notice_error_1", "failure:turn_2"]);
		const other = errors[0];
		if (other?.kind !== "notice") throw new Error("not a notice row");
		expect(other.text).toBe("MCP server github disconnected");
	});

	it("still shows a failure row with system events on", () => {
		const { rows } = rowsAt("full", [systemEventWireFailedTurn()], true);
		expect(
			rows.filter((row) => row.kind === "failure" || (row.kind === "notice" && row.eventKind === "error")),
		).toHaveLength(1);
	});
});

describe("recorded summary failure", () => {
	it.each(LEVELS)("retains the failed route in an attention row at %s", (level) => {
		const fixture = summaryFailureWireFixture();
		const { rows } = rowsAt(level, [completedTurn([fixture.item, fixture.recovery])], false);
		const row = rows.find((entry) => entry.id === fixture.item.id);
		if (row?.kind !== "notice") throw new Error("summary warning notice was lost");
		expect(isCriticalNotice(row)).toBe(true);
		const tree = render(<TimelineItem item={row} hubId="hub" sessionRef="summary-fixture" />);
		for (const identifier of ["openai/summary-model-8af1", "429", "rate_limit"])
			expect(renderedText(tree)).toContain(identifier);
		expect(inked(tree, palettes.light.attention)).toBe(true);
		expect(tree.root.findAll((node) => node.props.accessibilityRole === "button")).toHaveLength(0);
	});
});

describe("system events (G7, G9)", () => {
	// The shared projector shows a repair only at full.
	it("reads a tool repair as a quiet event: never red, never an action", () => {
		const row = rowFor("tool-repair", "full");
		if (row?.kind !== "notice") throw new Error("no tool repair notice");
		expect(isCriticalNotice(row)).toBe(false);
		const tree = show("tool-repair", "full");
		expect(renderedText(tree)).toContain(systemEventWireItem("tool-repair").text);
		expect(inked(tree, DANGER_INK)).toBe(false);
		expect(offersButton(tree, /Retry|Resume|Sign in/)).toBe(false);
	});

	it.each(["compaction-summary", "compaction-checkpoint"] as const)(
		"collapses a %s to Context summary, which opens to the summary as markdown",
		(name) => {
			const tree = show(name);
			expect(eventBody(tree)).toBeUndefined();
			expect(renderedText(tree)).not.toContain(systemEventWireItem(name).text);
			expect(tree.root.findAll((node) => String(node.type) === "EnrichedMarkdownText")).toHaveLength(0);
			press(tree);
			const markdown = tree.root.findAll((node) => String(node.type) === "EnrichedMarkdownText");
			expect(markdown).toHaveLength(1);
			expect(markdown[0]?.props.markdown).toBe(systemEventWireItem(name).text);
		},
	);

	it("names a loaded plugin, or says a plugin loaded, with no counts", () => {
		const named = renderedText(show("plugin-loaded"));
		const unnamed = renderedText(show("plugin-loaded-unnamed"));
		expect(named).toContain("superpowers");
		expect(unnamed).not.toBe("");
		expect(unnamed).not.toBe(named);
		// The recorded description counts skills, agents and servers.
		for (const line of [named, unnamed]) expect(line).not.toMatch(/\d/);
	});

	it("says how far a compaction brought the context: tokens, else turns, else just that it ran", () => {
		const tokens = renderedText(show("context-compaction"));
		const turns = renderedText(show("context-compaction-turns"));
		const bare = renderedText(show("context-compaction-bare"));
		// The recorded estimate (412000 -> 38000 tokens), compacted; the turns
		// (40 -> 5) only when it has no token estimate.
		expect(tokens).toMatch(/412K\D+38K/);
		expect(tokens).not.toMatch(/\b40\b/);
		expect(turns).toMatch(/\b40\D+5\b/);
		expect(bare).not.toMatch(/\d/);
	});
});

describe("approval history (spec 8.2)", () => {
	// The fixture's decisions were made at the daemon's fixture instant.
	const decidedAt = () => systemEventWireItem("approval-allowed").startedAt ?? 0;

	// A human's Allow or Deny is a decision they made, like a question's
	// answer: it shows at every level, system events off.
	it.each(LEVELS)("shows an Allow and a Deny with their words and when, at %s", (level) => {
		const items = [systemEventWireItem("approval-allowed"), systemEventWireItem("approval-denied")];
		const { rows } = rowsAt(level, [completedTurn(items)], false);
		const approvals = rows.filter((row) => row.kind === "notice" && row.family === "approval");
		expect(approvals).toEqual([
			expect.objectContaining({ text: "Allowed: write /Users/j/sites/docs/index.md", decidedAtMs: decidedAt() }),
			expect.objectContaining({ text: "Denied: read /etc/hosts", decidedAtMs: decidedAt() }),
		]);
		for (const approval of approvals) {
			if (approval.kind !== "notice") throw new Error("not a notice row");
			expect(isCriticalNotice(approval)).toBe(false);
		}
	});

	// Each render subscribes to the shared minute clock; unmounting releases
	// it, so the next test's fake time starts a fresh clock.
	function atTime(
		nowMs: number,
		name: "approval-allowed" | "approval-denied",
		check: (tree: ReactTestRenderer) => void,
	) {
		vi.useFakeTimers();
		vi.setSystemTime(nowMs);
		const tree = show(name, "chat");
		try {
			check(tree);
		} finally {
			act(() => tree.unmount());
			vi.useRealTimers();
		}
	}

	function spoken(tree: ReactTestRenderer): string | undefined {
		return tree.root.findAll((node) => typeof node.props.accessibilityLabel === "string")[0]?.props.accessibilityLabel;
	}

	// Spec 8.2: "Allowed: write …" or "Denied: …" in ink-mid with the
	// approval mark. Amber is for an approval still waiting on you (the dock).
	it.each([
		["approval-allowed", "Allowed: write /Users/j/sites/docs/index.md"],
		["approval-denied", "Denied: read /etc/hosts"],
	] as const)("reads %s in ink-mid with the approval mark and how long ago", (name, words) => {
		atTime(decidedAt() + 5 * 60_000, name, (tree) => {
			expect(renderedText(tree).replaceAll("\u200b", "")).toBe(`${words} · 5m ago`);
			expect(spoken(tree)).toBe(`${words}, 5 minutes ago`);
			const mark = tree.root.findAll((node) => String(node.type) === "SymbolView");
			expect(mark.map((node) => [node.props.name, node.props.tintColor])).toEqual([
				["hand.raised.circle.fill", palettes.light.inkMid],
			]);
			expect(inked(tree, palettes.light.inkMid)).toBe(true);
			expect(inked(tree, palettes.light.attention)).toBe(false);
			expect(inked(tree, DANGER_INK)).toBe(false);
		});
	});

	// The minute clock can lag the decision by up to a minute, or a skewed
	// clock put it ahead: either reads as just now, never "0s ago".
	it.each([
		["a moment ago", 20_000],
		["ahead of the phone's clock", -30_000],
	])("says just now for a decision made %s", (_, offsetMs) => {
		atTime(decidedAt() + offsetMs, "approval-allowed", (tree) => {
			expect(renderedText(tree).replaceAll("\u200b", "")).toBe(
				"Allowed: write /Users/j/sites/docs/index.md · just now",
			);
			expect(spoken(tree)).toBe("Allowed: write /Users/j/sites/docs/index.md, just now");
		});
	});

	it("leaves the time out when the decision has none", () => {
		const item = { ...systemEventWireItem("approval-denied"), startedAt: undefined };
		const row = rowsAt("chat", [completedTurn([item])], false).rows.find((candidate) => candidate.id === item.id);
		if (!row) throw new Error("no approval row");
		const tree = render(<TimelineItem item={row} hubId="hub" sessionRef="approval-untimed" />);
		expect(renderedText(tree).replaceAll("\u200b", "")).toBe("Denied: read /etc/hosts");
		expect(spoken(tree)).toBe("Denied: read /etc/hosts");
		act(() => tree.unmount());
	});

	// A long path breaks only at its slashes when it wraps, as the dock's does.
	it("lets the path wrap at its slashes", () => {
		atTime(decidedAt() + 5 * 60_000, "approval-allowed", (tree) => {
			expect(renderedText(tree)).toContain("/\u200bUsers/\u200bj/\u200bsites/\u200bdocs/\u200bindex.md");
		});
	});
});

describe("shared-notes snapshots", () => {
	const text =
		"<shared-notes>\nHuman: **keep literal**\nAgent: context & <tags>\nURLs: https://example.com/\n</shared-notes>";
	const notes = {
		id: "notes",
		turnId: "turn_1",
		type: "systemMessage",
		eventKind: "notes-context",
		text,
	} as ThreadItem;

	it.each(["intent", "tools", "activity", "full"] as const)(
		"folds notes at %s and opens to the complete literal snapshot",
		(level) => {
			const { rows, expandByDefault } = rowsAt(level, [completedTurn([notes])]);
			const row = rows.find((entry) => entry.id === notes.id);
			if (!row) throw new Error("no notes row");
			const tree = render(
				<TimelineItem item={row} hubId="hub" sessionRef={`notes-${level}`} expandByDefault={expandByDefault} />,
			);
			expect(eventBody(tree)).toBeUndefined();
			press(tree);
			expect(eventBody(tree)).toBe(text);
			expect(tree.root.findAll((node) => String(node.type) === "EnrichedMarkdownText")).toHaveLength(0);
		},
	);

	it.each(["missing turn", "missing item", "different identity"] as const)(
		"keeps available notes text when the canonical source has a %s",
		(missing) => {
			const keyedNotes = { ...notes, transcriptKey: "notes-lookup-key" };
			const { rows, model } = rowsAt("full", [completedTurn([keyedNotes])]);
			const row = rows.find((entry) => entry.id === keyedNotes.id);
			if (row?.kind !== "notice") throw new Error("no notes notice");
			const sourceTurns =
				missing === "missing turn"
					? []
					: model.turns.map((turn) => ({
							...turn,
							items:
								missing === "missing item"
									? turn.items.filter((item) => item.id !== keyedNotes.id)
									: turn.items.map((item) =>
											item.id === keyedNotes.id
												? { ...item, transcriptKey: "another-notes-key", text: "another snapshot" }
												: item,
										),
						}));
			const tree = render(
				<TimelineItem item={row} hubId="hub" sessionRef={`notes-lookup-${missing}`} sourceTurns={sourceTurns} />,
			);
			expect(eventBody(tree)).toBeUndefined();
			press(tree);
			expect(eventBody(tree)).toBe(text);
			act(() => tree.unmount());
		},
	);

	it("hides notes at Conversation with System events on, and elsewhere with them off", () => {
		expect(rowsAt("chat", [completedTurn([notes])]).rows.find((row) => row.id === notes.id)).toBeUndefined();
		for (const level of ["intent", "tools", "activity", "full"] as const) {
			expect(rowsAt(level, [completedTurn([notes])], false).rows.find((row) => row.id === notes.id)).toBeUndefined();
		}
	});

	it("opens a store-bounded large link snapshot without losing literal text after remount", async () => {
		// Supported producer limits: 50 links, each at most 2048 characters
		// (agent/session_notes.go). Expected text is the supplied opaque payload,
		// not the row projector's result.
		const links = Array.from({ length: 50 }, (_, index) => `https://example.com/${index}/`.padEnd(1600, "a"));
		const linkLines = links.map((url, index) => `Link: ${url} [id: url_${index}]`);
		const snapshot = `<shared-notes>\nHuman: **opaque**  \n\nAgent: <tags> & data\n${linkLines.join("\n")}\n</shared-notes>`;
		const largeNotes = { ...notes, id: "notes-large", text: snapshot, transcriptKey: "notes-large-key" };
		const input = thread([completedTurn([notes, largeNotes])]);
		input.evener.capabilities = { ...wireThread("ref-1").evener.capabilities, pageBefore: true };
		const shipped = shippedConfig("mobile");
		const hub = makeTranscriptDisplayConfig(shipped.content, { ...shipped.advanced, systemEvents: true });
		const { config } = displayForLevel("full", hub);
		const client = new FakeClient();
		client.on("thread/read", () => ({ thread: input }));
		client.on("thread/unsubscribe", () => ({}));
		const service = createConversationService(client, { now: () => T0, resolveDisplayConfig: () => config });
		const store = createConversationStore({ displayConfig: config });
		const activity = createActivityStore();
		try {
			await store.getState().openProjected(service, activity.getState(), "ref-1");
			expect(store.getState().status).toBe("open");
			const conversation = store.getState().conversation;
			if (!conversation) throw new Error("no stored conversation");
			const presentation = projectNativeTranscript(conversation, config);
			const rows = hideAnswerMessages(sessionRows(groupTimeline(presentation.items), conversation.turns));
			const row = rows.find((entry) => entry.id === largeNotes.id);
			if (row?.kind !== "notice") throw new Error("no large notes notice");
			expect(new TextEncoder().encode(snapshot).length).toBeGreaterThan(MAX_ITEM_BYTES);
			expect(links.every((url) => url.length <= 2048)).toBe(true);
			expect(new TextEncoder().encode(row.text).length).toBe(MAX_ITEM_BYTES);
			expect(row.text === snapshot).toBe(false);
			expect(conversation.turns[0]?.items.find((item) => item.id === largeNotes.id)?.text === snapshot).toBe(true);
			const mount = (sessionRef: string, item: TimelineRow = row) =>
				render(
					<TimelineItem
						item={item}
						hubId="hub"
						sessionRef={sessionRef}
						sourceTurns={conversation.turns}
						expandByDefault={presentation.expandByDefault}
					/>,
				);
			const first = mount("notes-large-persist");
			expect(eventBody(first)).toBeUndefined();
			press(first);
			expect(eventBody(first) === snapshot, "expanded snapshot preserves all source text").toBe(true);
			expect(eventBody(first)?.includes("<shared-notes>\nHuman: **opaque**  \n\nAgent: <tags> & data\n")).toBe(true);
			expect(eventBody(first)?.endsWith(`${linkLines[49]}\n</shared-notes>`)).toBe(true);
			expect(first.root.findAll((node) => String(node.type) === "EnrichedMarkdownText")).toHaveLength(0);
			expect(new TextEncoder().encode(row.text).length).toBe(MAX_ITEM_BYTES);
			act(() => first.unmount());
			const remounted = mount("notes-large-persist");
			expect(eventBody(remounted) === snapshot, "remounted snapshot preserves all source text").toBe(true);
			expect(eventBody(remounted)?.endsWith(`${linkLines[49]}\n</shared-notes>`)).toBe(true);
			const otherSession = mount("notes-large-other");
			expect(eventBody(otherSession)).toBeUndefined();
			const otherRow = rows.find((entry) => entry.id === notes.id);
			if (!otherRow) throw new Error("no other notes row");
			const otherItem = mount("notes-large-persist", otherRow);
			expect(eventBody(otherItem)).toBeUndefined();
			act(() => {
				remounted.unmount();
				otherSession.unmount();
				otherItem.unmount();
			});
		} finally {
			store.getState().close();
			service.close();
			client.close();
		}
	});

	it("remembers explicit expansion per session and item", () => {
		const row = rowsAt("tools", [completedTurn([notes])]).rows.find((entry) => entry.id === notes.id);
		if (!row) throw new Error("no notes row");
		const first = render(<TimelineItem item={row} hubId="hub" sessionRef="notes-persist" />);
		press(first);
		act(() => first.unmount());
		expect(eventBody(render(<TimelineItem item={row} hubId="hub" sessionRef="notes-persist" />))).toBe(text);
		expect(eventBody(render(<TimelineItem item={row} hubId="hub" sessionRef="notes-other" />))).toBeUndefined();
	});
});

// Each steer says it is the system's, then what it did: the engineering
// labels in plain words (spec 5), the rest in the web's.
const STEERS: SystemEventWireCase[] = [
	"steer-hook-context",
	"steer-precompact-hook",
	"steer-compact-nudge",
	"steer-no-tool-calls",
	"steer-loop-detected",
	"steer-provider-failure",
	"steer-transcript-pointer",
	"steer-note-handoff",
	"steer-from-parent",
];

describe("the daemon's steers (G8)", () => {
	it.each(STEERS)("collapses %s to its label, which opens to what it said", (name) => {
		const tree = show(name);
		expect(eventBody(tree)).toBeUndefined();
		expect(eventLabel(tree)).not.toBe("");
		press(tree);
		expect(eventBody(tree)).toBe(stripSystemReminder(systemEventWireItem(name).text ?? ""));
	});

	it("labels each steer by what it did", () => {
		const labels = STEERS.map((name) => eventLabel(show(name)));
		expect(new Set(labels).size).toBe(STEERS.length);
	});

	it.each(LEVELS)("keeps a parent's steer, which is conversation, at every level (%s)", (level) => {
		expect(rowFor("steer-from-parent", level)).toBeDefined();
	});

	// A parent's message is conversation, not the daemon's markup: it shows
	// whole under "From parent" even when it quotes notification tags.
	it.each([
		"Inspect how <job-notification> is rendered",
		`See <delegate-notification delegate_id="dlg_42" event="completed" status="completed" reason="" transcript_ref="local:sess_child">
Delegate dlg_42 completed.
</delegate-notification> for the format`,
	])("shows a parent's message that quotes notification markup whole: %s", (text) => {
		const item = { ...systemEventWireItem("steer-from-parent"), text };
		const row = rowsAt("intent", [completedTurn([item])]).rows.find((r) => r.id === item.id);
		if (!row) throw new Error("no row for the parent's message");
		const tree = render(<TimelineItem item={row} hubId="hub" sessionRef="system-parent-markup" />);
		// A short message opens folded and a longer one starts open; either
		// way pressing opens it, and it reads whole.
		if (eventBody(tree) === undefined) press(tree);
		expect(eventBody(tree)).toBe(text);
	});

	it.each(["steer-loop-detected", "steer-provider-failure"] as const)(
		"reads %s as a quiet labelled event, since the failure itself is the turn's error",
		(name) => {
			const row = rowFor(name);
			if (row?.kind !== "notice") throw new Error(`no ${name} notice`);
			expect(isCriticalNotice(row)).toBe(false);
			expect(inked(show(name), DANGER_INK)).toBe(false);
		},
	);

	it.each(LEVELS)("leaves out the current task and the task list, as the web does, at %s", (level) => {
		for (const name of ["steer-current-task", "steer-task-list"] as const) {
			expect(rowFor(name, level)).toBeUndefined();
		}
	});
});
