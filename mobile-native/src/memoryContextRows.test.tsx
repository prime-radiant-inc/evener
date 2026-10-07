// Automatic memory refreshes on the phone, driven from what the daemon
// actually sends (agent/testdata/memorycontextwire) through the same pipeline a
// session screen runs: hydrate / live history-updated -> project -> group ->
// fold into session rows -> TimelineItem, mounted with the real presentation's
// expansion default. The refresh must render as a standalone "Refreshed my
// memory" disclosure, closed at every verbosity level with System events on or
// off and even where the general expansion default is on, opening on an
// explicit tap to the scope/state, the decoded Markdown index and a separately
// folded literal Source that keeps the complete recorded text. Only external /
// native-platform seams are mocked; the product pipeline runs for real.
import {
	applyNotification,
	hydrateThread,
	makeTranscriptDisplayConfig,
	MEMORY_CONTEXT_LABEL,
	shippedConfig,
	type AnyNotification,
	type Thread,
	type ThreadItem,
	type ThreadReadResponse,
	type TurnModel,
} from "@evener/appwire-client";
import {
	memoryContextWireCases,
	memoryContextWireItem,
} from "@evener/appwire-client/testing/memoryContextWireFixtures";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { wireThread } from "@evener/appwire-client/testing/notifications";
import { act, type ReactTestInstance, type ReactTestRenderer } from "react-test-renderer";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createConversationService } from "../../mobile/src/services/conversation";
import { createActivityStore } from "../../mobile/src/state/activity";
import { createConversationStore } from "../../mobile/src/state/conversation";
import { nativeDisclosureStore } from "./nativeDisclosure";
import { MAX_ITEM_BYTES, projectConversation } from "./projectedRows";
import { pressable, render, renderedText, textOf, unmountMountedTrees } from "./renderNative.testkit";
import { displayForLevel } from "./session/detailLevels";
import { hideAnswerMessages, sessionRows } from "./session/transcriptRows";
import { TimelineItem } from "./TimelineItem";
import type { TimelineRow } from "./timeline";
import { groupTimeline } from "./timeline";
import { projectNativeTranscript } from "./transcriptPresentation";

// The real product graph reaches Expo's dev-only async-require setup, which
// reads the bundler's `__DEV__` global. Define it before any import evaluates
// (vi.hoisted runs above the module's imports), so the product modules load for
// real instead of being mocked away.
vi.hoisted(() => {
	(globalThis as { __DEV__?: boolean }).__DEV__ = false;
});

vi.mock("react-native", async () => ({
	...(await import("./renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));
vi.mock("react-native-safe-area-context", () => ({ SafeAreaView: "SafeAreaView" }));
vi.mock("react-native-enriched-markdown", () => ({ EnrichedMarkdownText: "EnrichedMarkdownText" }));
vi.mock("expo-clipboard", () => ({ setStringAsync: async () => true }));
// An external native seam: the real TranscriptImages module reaches SecureStore
// at import time, so it is faked here rather than mocking the product module.
vi.mock("expo-secure-store", () => ({
	getItemAsync: async () => null,
	setItemAsync: async () => undefined,
	deleteItemAsync: async () => undefined,
}));
vi.mock("expo-crypto", () => ({
	randomUUID: () => "test-uuid",
	getRandomValues: (array: Uint8Array) => array,
}));
vi.mock("expo-sqlite", () => ({ openDatabaseSync: vi.fn() }));
vi.mock("expo-sqlite/kv-store", () => {
	const values = new Map<string, string>();
	const Storage = {
		getItemSync: (key: string) => values.get(key) ?? null,
		setItemSync: (key: string, value: string) => void values.set(key, value),
		removeItemSync: (key: string) => void values.delete(key),
	};
	return { default: Storage, Storage };
});

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

interface Projected {
	rows: TimelineRow[];
	expandByDefault: boolean;
}

function projectRows(level: Level, items: ThreadItem[], systemEvents: boolean): Projected {
	const model = hydrateThread({ thread: threadWith(items) }, "ref-1", 0);
	return projectModel(level, model, systemEvents);
}

function projectModel(level: Level, model: ReturnType<typeof hydrateThread>, systemEvents: boolean): Projected {
	const shipped = shippedConfig("mobile");
	const hub = makeTranscriptDisplayConfig(shipped.content, { ...shipped.advanced, systemEvents });
	const { config, justTheConversation } = displayForLevel(level, hub);
	const conversation = projectConversation(model, undefined, config ?? undefined);
	const presentation = projectNativeTranscript(conversation, config, { justTheConversation });
	return {
		rows: sessionRows(groupTimeline(presentation.items), conversation.turns),
		expandByDefault: presentation.expandByDefault,
	};
}

interface MountOptions {
	hubId?: string;
	sessionRef?: string;
	expandByDefault?: boolean;
	sourceTurns?: readonly TurnModel[];
}

function mountRow(
	row: TimelineRow,
	{ hubId = "hub", sessionRef = "session-1", expandByDefault = false, sourceTurns }: MountOptions = {},
) {
	const tree = render(
		<TimelineItem
			item={row}
			hubId={hubId}
			sessionRef={sessionRef}
			expandByDefault={expandByDefault}
			sourceTurns={sourceTurns}
		/>,
	);
	return tree;
}

function findRow(rows: TimelineRow[], id: string): TimelineRow {
	const row = rows.find((candidate) => candidate.id === id);
	if (!row) throw new Error(`no row ${id}`);
	return row;
}

function mountItem(item: ThreadItem, level: Level, systemEvents: boolean, options: MountOptions = {}) {
	const { rows, expandByDefault } = projectRows(level, [item], systemEvents);
	const row = findRow(rows, item.id);
	return {
		tree: mountRow(row, { ...options, expandByDefault: options.expandByDefault ?? expandByDefault }),
		row,
		expandByDefault,
	};
}

function find(tree: ReactTestRenderer, testID: string): ReactTestInstance | undefined {
	return tree.root.findAllByProps({ testID })[0];
}

function requireFind(tree: ReactTestRenderer, testID: string): ReactTestInstance {
	const node = find(tree, testID);
	if (!node) throw new Error(`no node ${testID}`);
	return node;
}

function absent(tree: ReactTestRenderer, testID: string): boolean {
	return tree.root.findAllByProps({ testID }).length === 0;
}

function press(tree: ReactTestRenderer, label: string): void {
	const target = pressable(tree, label);
	if (!target) throw new Error(`no pressable labelled ${label}`);
	act(() => target.props.onPress());
}

function markdownSource(tree: ReactTestRenderer): string {
	const markdown = tree.root.findAll((node) => String(node.type) === "EnrichedMarkdownText")[0];
	if (!markdown) throw new Error("no Markdown node");
	return String(markdown.props.markdown);
}

function resetDisclosure(): void {
	act(() => nativeDisclosureStore.setState(nativeDisclosureStore.getInitialState()));
}

function unmount(tree: ReactTestRenderer): void {
	act(() => tree.unmount());
}

beforeEach(() => {
	resetDisclosure();
});

afterEach(() => {
	unmountMountedTrees();
});

// --- collapsed at every level, both System events settings ------------------

describe("collapsed 'Refreshed my memory' at every level and gate", () => {
	it.each(LEVELS)("stays closed with System events off at %s", (level) => {
		for (const name of memoryContextWireCases()) {
			resetDisclosure();
			const item = memoryContextWireItem(name);
			const { tree, row } = mountItem(item, level, false);
			expect(row).toMatchObject({ kind: "notice", eventKind: "memory-context" });
			if (row.kind !== "notice") throw new Error("not a notice row");
			expect(row.label ?? "").toContain(MEMORY_CONTEXT_LABEL);
			expect(absent(tree, "memory-context-scope-state")).toBe(true);
			expect(renderedText(tree)).not.toContain("Quoted index data:");
			expect(renderedText(tree)).not.toContain("Memory scope");
			unmount(tree);
		}
	});

	it.each(LEVELS)("stays closed with System events on at %s", (level) => {
		for (const name of memoryContextWireCases()) {
			resetDisclosure();
			const { tree } = mountItem(memoryContextWireItem(name), level, true);
			expect(absent(tree, "memory-context-scope-state")).toBe(true);
			unmount(tree);
		}
	});

	it("stays closed at Activity and Full where the general expansion default is on", () => {
		for (const level of ["activity", "full"] as const) {
			resetDisclosure();
			const { tree, expandByDefault } = mountItem(memoryContextWireItem("current-project"), level, true);
			// Activity and Full set the general expand-everything baseline.
			expect(expandByDefault).toBe(true);
			expect(absent(tree, "memory-context-scope-state")).toBe(true);
			unmount(tree);
		}
	});
});

// --- explicit tap opens decoded, formatted content and folded Source --------

describe("an explicit tap reveals the decoded refresh", () => {
	it("shows scope/state, the formatted index and the exact Source for a valid payload", () => {
		const item = memoryContextWireItem("current-personal");
		const { tree } = mountItem(item, "tools", true);
		press(tree, MEMORY_CONTEXT_LABEL);
		expect(textOf(requireFind(tree, "memory-context-scope-state"))).toContain("Personal memory · current");
		expect(markdownSource(tree)).toContain("a note");
		// The Source is separately folded: hidden until its own tap.
		expect(absent(tree, "memory-context-source-text")).toBe(true);
		press(tree, "Source");
		expect(textOf(requireFind(tree, "memory-context-source-text"))).toBe(item.text);
		unmount(tree);
	});

	it("names every scope truthfully", () => {
		for (const [name, expected] of [
			["current-project", "Project memory · current"],
			["current-session", "Session memory · current"],
		] as const) {
			resetDisclosure();
			const { tree } = mountItem(memoryContextWireItem(name), "tools", true);
			press(tree, MEMORY_CONTEXT_LABEL);
			expect(textOf(requireFind(tree, "memory-context-scope-state"))).toContain(expected);
			unmount(tree);
		}
	});

	it("keeps the complete recorded Source for every decodable case, including the delegate suffix and a truncation", () => {
		for (const name of memoryContextWireCases()) {
			const item = memoryContextWireItem(name);
			if (!item.raw) continue; // the malformed case has its own fallback test
			resetDisclosure();
			const { tree, row } = mountItem(item, "tools", true);
			if (row.kind !== "notice") throw new Error("not a notice row");
			// Unavailable/revoked carry their state on the collapsed label.
			press(tree, row.label ?? MEMORY_CONTEXT_LABEL);
			press(tree, "Source");
			expect(textOf(requireFind(tree, "memory-context-source-text"))).toBe(item.text);
			unmount(tree);
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
			resetDisclosure();
			const item = memoryContextWireItem(name);
			const { tree, row } = mountItem(item, "tools", true);
			if (row.kind !== "notice") throw new Error("not a notice row");
			expect(row.label).toContain(state);
			expect(pressable(tree, `${MEMORY_CONTEXT_LABEL} · ${state}`)).toBeDefined();
			unmount(tree);
		}
	});

	it("distinguishes empty, missing and a truncated current index", () => {
		resetDisclosure();
		let { tree } = mountItem(memoryContextWireItem("empty-project"), "tools", true);
		press(tree, MEMORY_CONTEXT_LABEL);
		expect(textOf(requireFind(tree, "memory-context-empty"))).toBe("Empty index");
		expect(absent(tree, "memory-context-content")).toBe(true);
		unmount(tree);

		resetDisclosure();
		({ tree } = mountItem(memoryContextWireItem("missing-project"), "tools", true));
		press(tree, MEMORY_CONTEXT_LABEL);
		expect(textOf(requireFind(tree, "memory-context-scope-state"))).toContain("missing");
		unmount(tree);

		// truncated-project says the index is too long; legacy-truncated-project
		// is an earlier build's explicit "truncated true". Both decode as truncated.
		for (const name of ["truncated-project", "legacy-truncated-project"] as const) {
			resetDisclosure();
			({ tree } = mountItem(memoryContextWireItem(name), "tools", true));
			press(tree, MEMORY_CONTEXT_LABEL);
			expect(textOf(requireFind(tree, "memory-context-truncated"))).toBe("truncated");
			unmount(tree);
		}
	});
});

// --- malformed fallback and later valid recovery ----------------------------

describe("an invalid payload falls back, a later valid one still renders", () => {
	it("opens a malformed refresh as its complete recorded text, with no invented index", () => {
		const item = memoryContextWireItem("malformed-project");
		expect(item.raw).toBeFalsy();
		const { tree } = mountItem(item, "tools", true);
		press(tree, MEMORY_CONTEXT_LABEL);
		expect(textOf(requireFind(tree, "memory-context-fallback"))).toBe(item.text);
		expect(absent(tree, "memory-context-scope-state")).toBe(true);
		expect(absent(tree, "memory-context-content")).toBe(true);
		unmount(tree);
	});

	it("opens both a malformed fallback and a later valid observation beside it", () => {
		const malformed = { ...memoryContextWireItem("malformed-project"), id: "mem-bad" } as ThreadItem;
		const valid = { ...memoryContextWireItem("current-project"), id: "mem-good" } as ThreadItem;
		const { rows, expandByDefault } = projectRows("tools", [malformed, valid], true);
		// Both are standalone notices in recorded order, never folded together.
		const noticeRows = rows.filter((row) => row.kind === "notice" && row.eventKind === "memory-context");
		expect(noticeRows.map((row) => row.id)).toEqual(["mem-bad", "mem-good"]);

		const badTree = mountRow(findRow(rows, "mem-bad"), { expandByDefault });
		press(badTree, MEMORY_CONTEXT_LABEL);
		expect(textOf(requireFind(badTree, "memory-context-fallback"))).toBe(malformed.text);

		const goodTree = mountRow(findRow(rows, "mem-good"), { expandByDefault });
		press(goodTree, MEMORY_CONTEXT_LABEL);
		expect(textOf(requireFind(goodTree, "memory-context-scope-state"))).toContain("Project memory · current");
		unmount(badTree);
		unmount(goodTree);
	});
});

// --- disclosure persistence -------------------------------------------------

describe("explicit choices survive remount, verbosity and stay per hub/session", () => {
	it("keeps an explicit open through a remount and across the expansion default", () => {
		const item = memoryContextWireItem("current-personal");
		const first = mountItem(item, "tools", true);
		expect(first.expandByDefault).toBe(false);
		press(first.tree, MEMORY_CONTEXT_LABEL);
		expect(find(first.tree, "memory-context-scope-state")).toBeDefined();
		unmount(first.tree);

		const remounted = mountItem(item, "tools", true);
		expect(find(remounted.tree, "memory-context-scope-state")).toBeDefined();
		unmount(remounted.tree);

		// Activity/Full turn the general expansion default on; the explicit open
		// is unaffected.
		const atFull = mountItem(item, "full", true);
		expect(atFull.expandByDefault).toBe(true);
		expect(find(atFull.tree, "memory-context-scope-state")).toBeDefined();
		unmount(atFull.tree);
	});

	it("keeps an explicit close through the expansion default switching on", () => {
		const item = memoryContextWireItem("current-personal");
		const tree = mountItem(item, "tools", true);
		press(tree.tree, MEMORY_CONTEXT_LABEL);
		press(tree.tree, MEMORY_CONTEXT_LABEL);
		expect(absent(tree.tree, "memory-context-scope-state")).toBe(true);
		unmount(tree.tree);

		const atFull = mountItem(item, "full", true);
		expect(atFull.expandByDefault).toBe(true);
		expect(absent(atFull.tree, "memory-context-scope-state")).toBe(true);
		unmount(atFull.tree);
	});

	it("does not open the same item id in another session or hub", () => {
		const item = memoryContextWireItem("current-personal");
		const tree = mountItem(item, "tools", true, { hubId: "hub", sessionRef: "session-1" });
		press(tree.tree, MEMORY_CONTEXT_LABEL);
		expect(find(tree.tree, "memory-context-scope-state")).toBeDefined();
		unmount(tree.tree);

		const otherSession = mountItem(item, "tools", true, { hubId: "hub", sessionRef: "session-2" });
		expect(absent(otherSession.tree, "memory-context-scope-state")).toBe(true);
		unmount(otherSession.tree);

		const otherHub = mountItem(item, "tools", true, { hubId: "hub-2", sessionRef: "session-1" });
		expect(absent(otherHub.tree, "memory-context-scope-state")).toBe(true);
		unmount(otherHub.tree);
	});
});

// --- the folded Source keeps its own explicit choice ------------------------

describe("the folded Source keeps its own explicit choice", () => {
	const hasSource = (tree: ReactTestRenderer) => find(tree, "memory-context-source-text") !== undefined;

	it("keeps an explicit Source open through row remount, outer fold and a verbosity remount", () => {
		const item = memoryContextWireItem("current-personal");
		const first = mountItem(item, "tools", true);
		press(first.tree, MEMORY_CONTEXT_LABEL);
		press(first.tree, "Source");
		expect(hasSource(first.tree)).toBe(true);
		// The nested fold is independent of the outer one: folding the outer
		// refresh and reopening it leaves the Source as the reader set it.
		expect(find(first.tree, "memory-context-scope-state")).toBeDefined();
		press(first.tree, MEMORY_CONTEXT_LABEL);
		expect(absent(first.tree, "memory-context-scope-state")).toBe(true);
		press(first.tree, MEMORY_CONTEXT_LABEL);
		expect(hasSource(first.tree)).toBe(true);
		unmount(first.tree);

		const remounted = mountItem(item, "tools", true);
		// The outer choice persisted too, so the remount is already open.
		expect(find(remounted.tree, "memory-context-scope-state")).toBeDefined();
		expect(hasSource(remounted.tree)).toBe(true);
		unmount(remounted.tree);

		const atFull = mountItem(item, "full", true);
		expect(atFull.expandByDefault).toBe(true);
		expect(find(atFull.tree, "memory-context-scope-state")).toBeDefined();
		expect(hasSource(atFull.tree)).toBe(true);
		unmount(atFull.tree);
	});

	it("keeps an explicit Source closed by default and through remount and the outer reopening", () => {
		const item = memoryContextWireItem("current-personal");
		// A fresh Full mount under its own scope: the Source defaults closed,
		// independently of the verbosity/expansion baseline.
		const freshFull = mountItem(item, "full", true, { hubId: "hub", sessionRef: "source-fresh-full" });
		expect(freshFull.expandByDefault).toBe(true);
		press(freshFull.tree, MEMORY_CONTEXT_LABEL);
		expect(hasSource(freshFull.tree)).toBe(false);
		unmount(freshFull.tree);

		const first = mountItem(item, "tools", true);
		press(first.tree, MEMORY_CONTEXT_LABEL);
		expect(hasSource(first.tree)).toBe(false);
		press(first.tree, "Source");
		expect(hasSource(first.tree)).toBe(true);
		press(first.tree, "Source"); // explicit close
		expect(hasSource(first.tree)).toBe(false);
		// Folding the outer refresh and reopening it leaves the explicit close
		// in force.
		press(first.tree, MEMORY_CONTEXT_LABEL);
		expect(absent(first.tree, "memory-context-scope-state")).toBe(true);
		press(first.tree, MEMORY_CONTEXT_LABEL);
		expect(find(first.tree, "memory-context-scope-state")).toBeDefined();
		expect(hasSource(first.tree)).toBe(false);
		unmount(first.tree);

		const remounted = mountItem(item, "full", true);
		expect(remounted.expandByDefault).toBe(true);
		// The outer choice persisted open; the Source stays explicitly closed.
		expect(find(remounted.tree, "memory-context-scope-state")).toBeDefined();
		expect(hasSource(remounted.tree)).toBe(false);
		unmount(remounted.tree);
	});

	it("does not share a Source choice across hub, session or item", () => {
		const item = memoryContextWireItem("current-personal");
		const first = mountItem(item, "tools", true, { hubId: "hub", sessionRef: "session-1" });
		press(first.tree, MEMORY_CONTEXT_LABEL);
		press(first.tree, "Source");
		expect(hasSource(first.tree)).toBe(true);
		unmount(first.tree);

		const otherSession = mountItem(item, "tools", true, { hubId: "hub", sessionRef: "session-2" });
		press(otherSession.tree, MEMORY_CONTEXT_LABEL);
		expect(hasSource(otherSession.tree)).toBe(false);
		unmount(otherSession.tree);

		const otherHub = mountItem(item, "tools", true, { hubId: "hub-2", sessionRef: "session-1" });
		press(otherHub.tree, MEMORY_CONTEXT_LABEL);
		expect(hasSource(otherHub.tree)).toBe(false);
		unmount(otherHub.tree);

		// Fixture items share an id, so give the other item its own.
		const otherItem = { ...memoryContextWireItem("current-project"), id: "mem-other" } as ThreadItem;
		const itemTree = mountItem(otherItem, "tools", true, { hubId: "hub", sessionRef: "session-1" });
		press(itemTree.tree, MEMORY_CONTEXT_LABEL);
		expect(hasSource(itemTree.tree)).toBe(false);
		unmount(itemTree.tree);
	});
});

// --- live reduction and history hydration reach the same presentation -------

describe("live reduction and history hydration agree", () => {
	it("mounts the same opened disclosure from a live history/updated notification", () => {
		const item = memoryContextWireItem("current-personal");
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
					items: [item],
				},
			} as unknown as AnyNotification,
			0,
		);

		// Live reduction reaches the mounted renderer, not just the projected row.
		const liveProjected = projectModel("tools", live, true);
		const liveTree = mountRow(findRow(liveProjected.rows, item.id), { expandByDefault: liveProjected.expandByDefault });
		press(liveTree, MEMORY_CONTEXT_LABEL);
		expect(textOf(requireFind(liveTree, "memory-context-scope-state"))).toContain("Personal memory · current");
		expect(markdownSource(liveTree)).toContain("a note");
		press(liveTree, "Source");
		const liveSource = textOf(requireFind(liveTree, "memory-context-source-text"));

		// History hydration of the same recorded item produces the same disclosure.
		resetDisclosure();
		const historyProjected = projectRows("tools", [item], true);
		const historyTree = mountRow(findRow(historyProjected.rows, item.id), {
			expandByDefault: historyProjected.expandByDefault,
		});
		press(historyTree, MEMORY_CONTEXT_LABEL);
		expect(textOf(requireFind(historyTree, "memory-context-scope-state"))).toBe(
			textOf(requireFind(liveTree, "memory-context-scope-state")),
		);
		press(historyTree, "Source");
		expect(textOf(requireFind(historyTree, "memory-context-source-text"))).toBe(liveSource);
		expect(liveSource).toBe(item.text);
		unmount(liveTree);
		unmount(historyTree);
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
		const { tree, row } = mountItem(plain, "tools", true);
		expect(row).toMatchObject({ kind: "notice" });
		if (row.kind !== "notice") throw new Error("not a notice row");
		expect(row.label).toBeUndefined();
		expect(row.memoryContext).toBeUndefined();
		expect(absent(tree, "memory-context-scope-state")).toBe(true);
		unmount(tree);
	});
});

// --- a store-bounded refresh keeps the complete original in Source ----------

describe("a store-bounded refresh keeps the complete original beyond the row bound", () => {
	const TAIL = "SOURCE_TAIL_SENTINEL";
	const oversizedText = "x".repeat(MAX_ITEM_BYTES) + TAIL;
	const oversized = (id: string, raw: unknown): ThreadItem =>
		({
			id,
			turnId: "turn_1",
			type: "systemMessage",
			eventKind: "memory-context",
			status: "completed",
			text: oversizedText,
			...(raw ? { raw } : {}),
		}) as unknown as ThreadItem;

	it("opens the malformed fallback and the later valid Source to the complete original, default closed", async () => {
		const malformed = oversized("mem-bad", undefined);
		const valid = oversized("mem-good", {
			memoryContext: { scope: "personal", state: "current", truncated: false, content: "a note" },
		});
		// Malformed first, valid later: the later valid observation still recovers.
		const input = threadWith([malformed, valid]);
		input.evener = { ...input.evener, capabilities: wireThread("ref-1").evener.capabilities };
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
			const conversation = store.getState().conversation;
			if (!conversation) throw new Error("no stored conversation");
			const presentation = projectNativeTranscript(conversation, config);
			const rows = hideAnswerMessages(sessionRows(groupTimeline(presentation.items), conversation.turns));
			const byteLen = (text: string) => new TextEncoder().encode(text).length;

			for (const [id, item] of [
				["mem-bad", malformed],
				["mem-good", valid],
			] as const) {
				const row = findRow(rows, id);
				if (row.kind !== "notice") throw new Error(`no notice row ${id}`);
				const original = item.text;
				if (original === undefined) throw new Error(`no recorded text on ${id}`);
				// The published display row is bounded; the canonical turn keeps it whole.
				expect(byteLen(original)).toBeGreaterThan(MAX_ITEM_BYTES);
				expect(byteLen(row.text)).toBe(MAX_ITEM_BYTES);
				expect(row.text === original).toBe(false);
				expect(row.text.endsWith(TAIL)).toBe(false);
				const canonical = conversation.turns[0]?.items.find((source) => source.id === id);
				expect(canonical?.text === original).toBe(true);

				const tree = mountRow(row, {
					sessionRef: `mem-bound-${id}`,
					expandByDefault: presentation.expandByDefault,
					sourceTurns: conversation.turns,
				});
				// Default closed at the expansion default.
				expect(absent(tree, "memory-context-scope-state")).toBe(true);
				expect(absent(tree, "memory-context-fallback")).toBe(true);
				press(tree, MEMORY_CONTEXT_LABEL);
				if (id === "mem-bad") {
					// The malformed fallback opens as the complete original, tail included.
					expect(textOf(requireFind(tree, "memory-context-fallback"))).toBe(original);
				} else {
					expect(textOf(requireFind(tree, "memory-context-scope-state"))).toContain("Personal memory · current");
					press(tree, "Source");
					expect(textOf(requireFind(tree, "memory-context-source-text"))).toBe(original);
				}
				unmount(tree);
			}
		} finally {
			store.getState().close();
			service.close();
			client.close();
		}
	});
});
