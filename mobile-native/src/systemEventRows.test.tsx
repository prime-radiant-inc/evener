// Failed turns, system events and the daemon's steers in the transcript
// (spec 8.2 "Error" and "System event"), driven from what the daemon actually
// sends (agent/testdata/systemeventwire) through the same pipeline
// ConversationScreen runs: hydrate, project, group, fold into session rows,
// then TimelineItem for each row.
import {
	hydrateThread,
	makeTranscriptDisplayConfig,
	shippedConfig,
	type Thread,
	type Turn,
} from "@evener/appwire-client";
import { systemEventWireFailedTurn } from "@evener/appwire-client/testing/systemEventWireFixtures";
import { describe, expect, it, vi } from "vitest";
import { projectConversation } from "./projectedRows";
import { displayForLevel } from "./session/detailLevels";
import { errorAction } from "./session/errorAction";
import { hideAnswerMessages, sessionRows } from "./session/transcriptRows";
import { groupTimeline } from "./timeline";
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
		expect(errorAction(failure, model, true)).toBe("retry");
	});

	it("still shows a failure row with system events on", () => {
		const { rows } = rowsAt("full", [systemEventWireFailedTurn()], true);
		expect(rows.filter((row) => row.kind === "failure" || (row.kind === "notice" && row.eventKind === "error"))).toHaveLength(1);
	});
});
