// Automatic memory refreshes on the phone, driven from what the daemon
// actually sends (agent/testdata/memorycontextwire) through the same pipeline a
// session screen runs: hydrate -> project -> group -> fold into session rows ->
// TimelineItem. The refresh must render as a standalone "Refreshed my memory"
// disclosure, closed at every verbosity level with System events on or off,
// opening on an explicit tap to the scope/state, the decoded Markdown index and
// a separately folded literal Source that keeps the complete recorded text.
// These tests pin the behavior the native amendment requires; the old
// generic-notice pins live in projectedRows.test.ts.
import {
	applyNotification,
	hydrateThread,
	makeTranscriptDisplayConfig,
	shippedConfig,
	type AnyNotification,
	type Thread,
	type ThreadItem,
	type ThreadReadResponse,
} from "@evener/appwire-client";
import {
	memoryContextWireCases,
	memoryContextWireItem,
} from "@evener/appwire-client/testing/memoryContextWireFixtures";
import { act, type ReactTestInstance, type ReactTestRenderer } from "react-test-renderer";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { nativeDisclosureStore } from "./nativeDisclosure";
import { projectConversation } from "./projectedRows";
import { pressable, render, renderedText, textOf } from "./renderNative.testkit";
import { displayForLevel } from "./session/detailLevels";
import { sessionRows } from "./session/transcriptRows";
import { TimelineItem } from "./TimelineItem";
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

const LEVELS = ["chat", "intent", "tools", "activity", "full"] as const;
type Level = (typeof LEVELS)[number];
const T0 = Date.parse("2026-10-06T20:00:00Z");

function threadWith(items: ThreadItem[]): Thread {
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
				items,
			},
		],
		evener: { ref: "ref-1", queue: { revision: 0 } },
	} as unknown as Thread;
}

function rowsFor(level: Level, items: ThreadItem[], systemEvents: boolean) {
	const model = hydrateThread({ thread: threadWith(items) }, "ref-1", 0);
	const shipped = shippedConfig("mobile");
	const hub = makeTranscriptDisplayConfig(shipped.content, { ...shipped.advanced, systemEvents });
	const { config, justTheConversation } = displayForLevel(level, hub);
	const conversation = projectConversation(model, undefined, config ?? undefined);
	const presentation = projectNativeTranscript(conversation, config, { justTheConversation });
	return {
		rows: sessionRows(groupTimeline(presentation.items), conversation.turns),
		conversation,
		config,
	};
}

function rowFor(item: ThreadItem, level: Level, systemEvents: boolean) {
	return rowsFor(level, [item], systemEvents).rows.find((row) => row.id === item.id);
}

function show(row: ReturnType<typeof rowFor>, hubId = "hub", sessionRef = "session-1"): ReactTestRenderer {
	if (!row) throw new Error("no row to render");
	return render(<TimelineItem item={row} hubId={hubId} sessionRef={sessionRef} />);
}

function press(tree: ReactTestRenderer, label: string): void {
	const target = pressable(tree, label);
	if (!target) throw new Error(`no pressable labelled ${label}`);
	act(() => target.props.onPress());
}

function find(tree: ReactTestRenderer, testID: string): ReactTestInstance | undefined {
	return tree.root.findAllByProps({ testID })[0];
}

beforeEach(() => {
	nativeDisclosureStore.setState(nativeDisclosureStore.getInitialState());
});

// --- collapsed at every level, both System events settings ------------------

describe("collapsed 'Refreshed my memory' at every level and gate", () => {
	it.each(LEVELS)("stays closed with System events off at %s", (level) => {
		for (const name of memoryContextWireCases()) {
			nativeDisclosureStore.setState(nativeDisclosureStore.getInitialState());
			const item = memoryContextWireItem(name);
			const tree = show(rowFor(item, level, false));
			const row = rowFor(item, level, false);
			expect(row).toMatchObject({ kind: "notice", eventKind: "memory-context" });
			if (row?.kind === "notice") expect(row.label ?? "").toContain("Refreshed my memory");
			expect(find(tree, "memory-context-scope-state")).toBeUndefined();
			expect(renderedText(tree)).not.toContain("Quoted index data:");
			expect(renderedText(tree)).not.toContain("Memory scope");
		}
	});

	it.each(LEVELS)("stays closed with System events on at %s", (level) => {
		for (const name of memoryContextWireCases()) {
			nativeDisclosureStore.setState(nativeDisclosureStore.getInitialState());
			const item = memoryContextWireItem(name);
			const tree = show(rowFor(item, level, true));
			expect(find(tree, "memory-context-scope-state")).toBeUndefined();
		}
	});
});

// --- explicit tap opens decoded, formatted content and folded Source --------

describe("an explicit tap reveals the decoded refresh", () => {
	it("shows scope/state, the formatted index and the exact Source for a valid payload", () => {
		const item = memoryContextWireItem("current-personal");
		const tree = show(rowFor(item, "tools", true));
		press(tree, "Refreshed my memory");
		expect(textOf(find(tree, "memory-context-scope-state")!)).toContain("Personal memory · current");
		expect(find(tree, "memory-context-content")).toBeDefined();
		// The decoded index goes through the native Markdown renderer.
		const markdown = tree.root.findAll((node) => String(node.type) === "EnrichedMarkdownText")[0];
		expect(markdown?.props.markdown).toContain("a note");
		// The Source is separately folded: hidden until its own tap.
		expect(find(tree, "memory-context-source-text")).toBeUndefined();
		press(tree, "Source");
		expect(textOf(find(tree, "memory-context-source-text")!)).toBe(item.text);
	});

	it("names every scope truthfully", () => {
		for (const [name, expected] of [
			["current-project", "Project memory · current"],
			["current-session", "Session memory · current"],
		] as const) {
			nativeDisclosureStore.setState(nativeDisclosureStore.getInitialState());
			const tree = show(rowFor(memoryContextWireItem(name), "tools", true));
			press(tree, "Refreshed my memory");
			expect(textOf(find(tree, "memory-context-scope-state")!)).toContain(expected);
		}
	});
});

// --- states and truncation --------------------------------------------------

describe("states and truncation stay honest", () => {
	it("marks revoked and unavailable on the collapsed row", () => {
		for (const [name, state] of [
			["revoked-project", "revoked"],
			["unavailable-project", "unavailable"],
		] as const) {
			nativeDisclosureStore.setState(nativeDisclosureStore.getInitialState());
			const item = memoryContextWireItem(name);
			const row = rowFor(item, "tools", true);
			if (row?.kind === "notice") expect(row.label).toContain(state);
			const tree = show(row);
			const collapsed = pressable(tree, `Refreshed my memory · ${state}`);
			expect(collapsed).toBeDefined();
		}
	});

	it("distinguishes empty, missing and a truncated current index", () => {
		nativeDisclosureStore.setState(nativeDisclosureStore.getInitialState());
		let tree = show(rowFor(memoryContextWireItem("empty-project"), "tools", true));
		press(tree, "Refreshed my memory");
		expect(textOf(find(tree, "memory-context-empty")!)).toBe("Empty index");
		expect(find(tree, "memory-context-content")).toBeUndefined();

		nativeDisclosureStore.setState(nativeDisclosureStore.getInitialState());
		tree = show(rowFor(memoryContextWireItem("missing-project"), "tools", true));
		press(tree, "Refreshed my memory");
		expect(textOf(find(tree, "memory-context-scope-state")!)).toContain("missing");

		nativeDisclosureStore.setState(nativeDisclosureStore.getInitialState());
		tree = show(rowFor(memoryContextWireItem("truncated-project"), "tools", true));
		press(tree, "Refreshed my memory");
		expect(textOf(find(tree, "memory-context-truncated")!)).toBe("truncated");
	});
});

// --- malformed fallback and later valid recovery ----------------------------

describe("an invalid payload falls back, a later valid one still renders", () => {
	it("opens a malformed refresh as its complete recorded text, with no invented index", () => {
		const item = memoryContextWireItem("malformed-project");
		expect(item.raw).toBeFalsy();
		const tree = show(rowFor(item, "tools", true));
		press(tree, "Refreshed my memory");
		expect(textOf(find(tree, "memory-context-fallback")!)).toBe(item.text);
		expect(find(tree, "memory-context-scope-state")).toBeUndefined();
		expect(find(tree, "memory-context-content")).toBeUndefined();
	});

	it("keeps a later valid observation working beside a malformed one", () => {
		const malformed = { ...memoryContextWireItem("malformed-project"), id: "mem-bad" } as ThreadItem;
		const valid = { ...memoryContextWireItem("current-project"), id: "mem-good" } as ThreadItem;
		const rows = rowsFor("tools", [malformed, valid], true).rows;
		const malformedRow = rows.find((row) => row.id === malformed.id);
		const validRow = rows.find((row) => row.id === valid.id);
		expect(malformedRow).toBeDefined();
		expect(validRow).toBeDefined();
		const tree = show(validRow);
		press(tree, "Refreshed my memory");
		expect(textOf(find(tree, "memory-context-scope-state")!)).toContain("Project memory · current");
	});
});

// --- disclosure persistence -------------------------------------------------

describe("explicit choices survive remount, verbosity and stay per hub/session", () => {
	it("keeps an explicit open through a remount and a verbosity change", () => {
		const item = memoryContextWireItem("current-personal");
		const first = show(rowFor(item, "tools", true));
		press(first, "Refreshed my memory");
		expect(find(first, "memory-context-scope-state")).toBeDefined();
		act(() => first.unmount());

		const remounted = show(rowFor(item, "tools", true));
		expect(find(remounted, "memory-context-scope-state")).toBeDefined();
		act(() => remounted.unmount());

		const atFull = show(rowFor(item, "full", true));
		expect(find(atFull, "memory-context-scope-state")).toBeDefined();
	});

	it("keeps an explicit close through a verbosity change", () => {
		const item = memoryContextWireItem("current-personal");
		const tree = show(rowFor(item, "tools", true));
		press(tree, "Refreshed my memory");
		press(tree, "Refreshed my memory");
		expect(find(tree, "memory-context-scope-state")).toBeUndefined();
		act(() => tree.unmount());

		const atFull = show(rowFor(item, "full", true));
		expect(find(atFull, "memory-context-scope-state")).toBeUndefined();
	});

	it("does not open the same item id in another session or hub", () => {
		const item = memoryContextWireItem("current-personal");
		const tree = show(rowFor(item, "tools", true), "hub", "session-1");
		press(tree, "Refreshed my memory");
		expect(find(tree, "memory-context-scope-state")).toBeDefined();
		act(() => tree.unmount());

		const otherSession = show(rowFor(item, "tools", true), "hub", "session-2");
		expect(find(otherSession, "memory-context-scope-state")).toBeUndefined();
		act(() => otherSession.unmount());

		const otherHub = show(rowFor(item, "tools", true), "hub-2", "session-1");
		expect(find(otherHub, "memory-context-scope-state")).toBeUndefined();
	});
});

// --- live reduction and history hydration reach the same presentation -------

describe("live reduction and history hydration agree", () => {
	it("renders the same disclosure from a live history/updated notification", () => {
		const model = hydrateThread(
			{
				thread: threadWith([]),
				requestGeneration: 1,
				bootGeneration: "1",
				epoch: 0,
				snapshot: { incarnation: "inc_a", length: 0 },
				overlay: [],
			} as unknown as ThreadReadResponse,
			"ref-1",
			0,
		);
		const live = applyNotification(
			model,
			{
				method: "history/updated",
				params: {
					threadId: "thread-1",
					ref: "ref-1",
					bootGeneration: "1",
					epoch: 0,
					snapshot: { incarnation: "inc_a", length: 1 },
					turns: [{ id: "turn_1", itemsView: "full", status: "completed", version: 1, startedAt: T0, completedAt: T0 }],
					items: [memoryContextWireItem("current-personal")],
				},
			} as unknown as AnyNotification,
			0,
		);
		const shipped = shippedConfig("mobile");
		const hub = makeTranscriptDisplayConfig(shipped.content, { ...shipped.advanced, systemEvents: true });
		const { config, justTheConversation } = displayForLevel("tools", hub);
		const conversation = projectConversation(live, undefined, config ?? undefined);
		const presentation = projectNativeTranscript(conversation, config, { justTheConversation });
		const rows = sessionRows(groupTimeline(presentation.items), conversation.turns);
		const row = rows.find((candidate) => candidate.id === "item_memory_context_1");
		expect(row).toMatchObject({ kind: "notice", eventKind: "memory-context" });
		if (row?.kind === "notice") expect(row.label ?? "").toContain("Refreshed my memory");
	});
});

// --- unrelated system rows keep their treatment -----------------------------

describe("ordinary system rows keep their existing treatment", () => {
	it("leaves a non-memory system message a plain notice", () => {
		const plain: ThreadItem = {
			id: "sys-1",
			turnId: "turn_1",
			type: "systemMessage",
			text: "Plugin superpowers loaded",
			status: "completed",
			eventKind: "plugin_loaded",
		} as unknown as ThreadItem;
		const row = rowFor(plain, "tools", true);
		expect(row).toMatchObject({ kind: "notice" });
		if (row?.kind === "notice") {
			expect(row.label).toBeUndefined();
			expect((row as { memoryContext?: unknown }).memoryContext).toBeUndefined();
		}
	});
});
