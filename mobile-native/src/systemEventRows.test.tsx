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
import {
	type SystemEventWireCase,
	systemEventWireFailedTurn,
	systemEventWireFailedTurnWithOtherError,
	systemEventWireItem,
} from "@evener/appwire-client/testing/systemEventWireFixtures";
import { act, type ReactTestRenderer } from "react-test-renderer";
import { describe, expect, it, vi } from "vitest";
import { projectConversation } from "./projectedRows";
import { render, renderedText } from "./renderNative.testkit";
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
const LEVELS = ["chat", "intent", "tools", "full"] as const;
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
	return { rows: hideAnswerMessages(sessionRows(groupTimeline(presentation.items), conversation.turns)), model };
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

describe("system events (G7, G9)", () => {
	// The shared projector shows a repair only at full.
	it("reads a tool repair as a quiet event: never red, never an action", () => {
		const row = rowFor("tool-repair", "full");
		if (row?.kind !== "notice") throw new Error("no tool repair notice");
		expect(isCriticalNotice(row)).toBe(false);
		const tree = show("tool-repair", "full");
		expect(renderedText(tree)).toContain('Fixed the shell call: removed the unrecognized "timeout" field.');
		expect(inked(tree, DANGER_INK)).toBe(false);
		expect(renderedText(tree)).not.toMatch(/Retry|Resume|Sign in/);
	});

	it.each(["compaction-summary", "compaction-checkpoint"] as const)(
		"collapses a %s to Context summary, which opens to the summary as markdown",
		(name) => {
			const tree = show(name);
			expect(renderedText(tree)).toBe("Context summary");
			expect(tree.root.findAll((node) => String(node.type) === "EnrichedMarkdownText")).toHaveLength(0);
			press(tree);
			const markdown = tree.root.findAll((node) => String(node.type) === "EnrichedMarkdownText");
			expect(markdown).toHaveLength(1);
			expect(markdown[0]?.props.markdown).toBe(systemEventWireItem(name).text);
		},
	);

	it("names a loaded plugin, or says a plugin loaded, with no counts", () => {
		expect(renderedText(show("plugin-loaded"))).toBe("Plugin superpowers loaded");
		expect(renderedText(show("plugin-loaded-unnamed"))).toBe("Plugin loaded");
	});

	it("says how far a compaction brought the context: tokens, else turns, else just that it ran", () => {
		expect(renderedText(show("context-compaction"))).toBe("Context compacted · 412K → 38K tokens");
		expect(renderedText(show("context-compaction-turns"))).toBe("Context compacted · 40 → 5 turns");
		expect(renderedText(show("context-compaction-bare"))).toBe("Context compacted");
	});
});

// Each steer says it is the system's, then what it did: the engineering
// labels in plain words (spec 5), the rest in the web's.
const STEER_LABELS: Array<[SystemEventWireCase, string]> = [
	["steer-hook-context", "System steered: Hook context"],
	["steer-precompact-hook", "System steered: Hook context before compacting"],
	["steer-compact-nudge", "System steered: Running low on context"],
	["steer-no-tool-calls", "System steered: Reminded to keep working"],
	["steer-loop-detected", "System steered: Loop detected"],
	["steer-provider-failure", "System steered: Provider failed"],
	["steer-transcript-pointer", "System steered: Where to find the full transcript"],
	["steer-task-nudge", "System steered: Task reminder"],
	["steer-note-handoff", "System steered: Note to self"],
];

describe("the daemon's steers (G8)", () => {
	it.each(STEER_LABELS)("collapses %s to its label, which opens to what it said", (name, label) => {
		const tree = show(name);
		expect(renderedText(tree)).toBe(label);
		press(tree);
		expect(renderedText(tree)).toBe(`${label} ${stripSystemReminder(systemEventWireItem(name).text ?? "")}`);
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
