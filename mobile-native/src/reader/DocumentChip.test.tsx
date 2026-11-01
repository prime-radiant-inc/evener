// A document chip under the agent's message (spec 8.2, ruling 28): the
// document's kind and title, its file name, its length and its age, read once
// per document and shared by every chip that shows it.
import { act, type ReactTestRenderer } from "react-test-renderer";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, renderedText, settle, unmountMountedTrees } from "../renderNative.testkit";
import { palettes } from "../design/tokens";
import { DocumentChip } from "./DocumentChip";
import { documentKind } from "./documentSource";
import { DocumentMemory } from "./documentMemory";
import { forgetDocumentSummaries } from "./documentSummaries";
import type { SyncStringStorage } from "../syncStringStorage";

const harness = vi.hoisted(() => ({
	connection: {} as Record<string, unknown>,
	memory: null as unknown,
	scheme: "light" as "light" | "dark",
	menus: [] as { items: { label: string; run: () => void }[]; preview: string }[],
	shares: [] as string[],
}));

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
	AppState: { currentState: "active", addEventListener: () => ({ remove: () => {} }) },
	useColorScheme: () => harness.scheme,
}));
vi.mock("../ConnectionProvider", () => ({ useConnection: () => harness.connection }));
vi.mock("./nativeDocumentMemory", () => ({ documentMemory: () => harness.memory }));
vi.mock("../longPressMenu", () => ({
	showMenu: (items: { label: string; run: () => void }[], preview: string) => harness.menus.push({ items, preview }),
	menuPreview: (text: string) => text,
	menuAccessibility: () => ({}),
}));
vi.mock("./useShareDocument", () => ({
	useShareDocument: (_hubId: string, _sessionRef: string) => async (path: string) => {
		harness.shares.push(path);
	},
}));
vi.mock("expo-secure-store", () => ({
	getItemAsync: vi.fn(async (key: string) =>
		key === "evener.hub.studio"
			? JSON.stringify({ id: "studio", name: "Studio", origin: "https://hub.test", token: "secret" })
			: null,
	),
	setItemAsync: vi.fn(async () => {}),
	deleteItemAsync: vi.fn(async () => {}),
}));

const PATH = "docs/superpowers/plans/settle-race.md";
const PLAN = `# Fix the settle/drain race\n\n${Array.from({ length: 140 }, (_, index) => `Line ${index + 1}.`).join("\n")}\n`;
const MINUTE = 60_000;

let fetchSpy: ReturnType<typeof vi.spyOn>;
let memory: DocumentMemory;

function storage(): SyncStringStorage {
	const data = new Map<string, string>();
	return {
		getItemSync: (key) => data.get(key) ?? null,
		setItemSync: (key, value) => void data.set(key, value),
		removeItemSync: (key) => void data.delete(key),
	};
}
let answers: (() => Response)[];

beforeEach(() => {
	harness.scheme = "light";
	harness.menus = [];
	harness.shares = [];
	forgetDocumentSummaries("studio");
	harness.connection = { profiles: [{ id: "studio", name: "Studio", origin: "https://hub.test" }], state: "ready" };
	answers = [];
	memory = new DocumentMemory(storage(), "studio");
	harness.memory = memory;
	fetchSpy = vi
		.spyOn(globalThis, "fetch")
		.mockImplementation(
			async () =>
				answers.shift()?.() ?? new Response(PLAN, { headers: { "Content-Type": "text/plain; charset=utf-8" } }),
		);
});
afterEach(() => {
	unmountMountedTrees();
	vi.restoreAllMocks();
});

function chip(props: { updatedAt?: string; onOpen?: () => void; path?: string } = {}) {
	const tree = render(
		<DocumentChip
			hubId="studio"
			sessionRef="local:fix"
			path={props.path ?? PATH}
			{...(props.updatedAt === undefined ? {} : { updatedAt: props.updatedAt })}
			onOpen={props.onOpen ?? (() => {})}
		/>,
	);
	return tree;
}

const button = (tree: ReactTestRenderer) =>
	tree.root.findAll((node) => String(node.type) === "Pressable" && node.props.accessibilityRole === "button")[0];

/** What VoiceOver reads for the chip, part by part. */
const spoken = (tree: ReactTestRenderer) => String(button(tree)?.props.accessibilityLabel).split(", ");

/** The facts on the chip's second line (its length and its age), without the
 * file name that may lead it. */
const facts = (tree: ReactTestRenderer) =>
	(button(tree)?.findAll((node) => String(node.type) === "Text" && node.props.numberOfLines === 1)[1]?.children ?? [])
		.filter((child) => typeof child === "string")
		.join("")
		.split("·")
		.map((fact) => fact.trim())
		.filter((fact) => fact !== "" && fact !== "\u00a0");

it("shows the kind and file name at once, then the title, the length and the age", async () => {
	const updatedAt = new Date(Date.now() - 3 * MINUTE).toISOString();
	const tree = chip({ updatedAt });
	expect(renderedText(tree)).toContain(documentKind(PATH));
	expect(renderedText(tree)).toContain("settle-race.md");
	await settle();
	expect(renderedText(tree)).toContain("Fix the settle/drain race");
	const [length, age, ...more] = facts(tree);
	expect(length).toMatch(/^142\D/);
	expect(age).toMatch(/^3\D/);
	expect(more).toEqual([]);
	const [kind, title, name, spokenLength, spokenAge, ...rest] = spoken(tree);
	expect([kind, title, name]).toEqual([documentKind(PATH), "Fix the settle/drain race", "settle-race.md"]);
	expect(spokenLength).toMatch(/^142\D/);
	expect(spokenAge).toMatch(/^3\D/);
	expect(rest).toEqual([]);
	expect(fetchSpy).toHaveBeenCalledWith(
		"https://hub.test/doc/file?format=raw&session=local%3Afix&path=docs%2Fsuperpowers%2Fplans%2Fsettle-race.md",
		{ headers: { Authorization: "Bearer secret" } },
	);
});

it("shows the file name once before its summary lands", () => {
	const tree = chip({ updatedAt: new Date(Date.now() - 3 * MINUTE).toISOString() });
	expect(renderedText(tree).match(/settle-race\.md/g)).toHaveLength(1);
	const [kind, name, age, ...rest] = spoken(tree);
	expect([kind, name]).toEqual([documentKind(PATH), "settle-race.md"]);
	expect(age).toMatch(/^3\D/);
	expect(rest).toEqual([]);
});

// The minute clock can lag a write by up to a minute, and a hub clock ahead
// of the phone's puts the write in the future; either way it was just now.
it.each([
	["seconds ago", -10_000],
	["ahead of the phone's clock", 5_000],
])("says just now for a document written %s", (_name, offset) => {
	const tree = chip({ updatedAt: new Date(Date.now() + offset).toISOString() });
	// No count of minutes or seconds: the age reads as a moment.
	const [age, ...more] = facts(tree);
	expect(age).toMatch(/\S/);
	expect(age).not.toMatch(/\d/);
	expect(more).toEqual([]);
	const [kind, name, spokenAge, ...rest] = spoken(tree);
	expect([kind, name]).toEqual([documentKind(PATH), "settle-race.md"]);
	expect(spokenAge).toMatch(/\S/);
	expect(spokenAge).not.toMatch(/\d/);
	expect(rest).toEqual([]);
});

it("shows no age for a file the session only named", async () => {
	const tree = chip();
	await settle();
	const [length, ...more] = facts(tree);
	expect(length).toMatch(/^142\D/);
	expect(more).toEqual([]);
	const [kind, title, name, spokenLength, ...rest] = spoken(tree);
	expect([kind, title, name]).toEqual([documentKind(PATH), "Fix the settle/drain race", "settle-race.md"]);
	expect(spokenLength).toMatch(/^142\D/);
	expect(rest).toEqual([]);
});

// A chip that grew when its summary landed would move every row below it,
// and the rows above the reader must keep their height.
it("keeps its two lines from the start, so it never grows when its summary lands", async () => {
	const lines = (tree: ReactTestRenderer) =>
		button(tree)?.findAll((node) => String(node.type) === "Text" && node.props.numberOfLines === 1);
	const tree = chip();
	// Only the file name is known: the second line is held empty.
	expect(lines(tree)).toHaveLength(2);
	const held = lines(tree)?.[1];
	await settle();
	// The summary fills the line that was held, rather than adding one.
	const filled = lines(tree)?.[1];
	expect(filled).toBe(held);
	expect(facts(tree)[0]).toMatch(/^142\D/);
});

it("opens when pressed", async () => {
	const onOpen = vi.fn();
	const tree = chip({ onOpen });
	act(() => button(tree)?.props.onPress());
	expect(onOpen).toHaveBeenCalledOnce();
	// The chip's summary read is still out; let it land before the test ends.
	await settle();
});

it("reads a document once for every chip that shows it", async () => {
	chip();
	chip();
	await settle();
	expect(fetchSpy).toHaveBeenCalledOnce();
});

it("forgets an older write's summary once the document is written again", async () => {
	const older = new Date(Date.now() - 10 * MINUTE).toISOString();
	const newer = new Date(Date.now() - 3 * MINUTE).toISOString();
	chip({ updatedAt: older });
	await settle();
	expect(fetchSpy).toHaveBeenCalledTimes(1);
	chip({ updatedAt: newer });
	await settle();
	expect(fetchSpy).toHaveBeenCalledTimes(2);
	// The older write's summary is gone, so a chip still naming it reads afresh.
	chip({ updatedAt: older });
	await settle();
	expect(fetchSpy).toHaveBeenCalledTimes(3);
});

it("waits for the hub's origin before it reads, then reads from it", async () => {
	harness.connection = { profiles: [], state: "connecting" };
	const first = chip();
	await settle();
	expect(fetchSpy).not.toHaveBeenCalled();
	harness.connection = { profiles: [{ id: "studio", name: "Studio", origin: "https://hub.test" }], state: "ready" };
	act(() => first.update(<DocumentChip hubId="studio" sessionRef="local:fix" path={PATH} onOpen={() => {}} />));
	await settle();
	expect(fetchSpy).toHaveBeenCalledOnce();
	expect(String(fetchSpy.mock.calls[0]?.[0])).toMatch(/^https:\/\/hub\.test\/doc\/file/);
	expect(renderedText(first)).toContain("Fix the settle/drain race");
});

it("keeps the file name after a failed read, and reads again when a chip mounts next", async () => {
	answers.push(() => new Response("boom", { status: 500 }));
	const first = chip();
	await settle();
	expect(renderedText(first)).not.toContain("Fix the settle/drain race");
	expect(renderedText(first)).toContain("settle-race.md");
	const second = chip();
	await settle();
	expect(renderedText(second)).toContain("Fix the settle/drain race");
	expect(fetchSpy).toHaveBeenCalledTimes(2);
});

// Spec 8.2 marks a change; Files marks what's new (Task 19, requirement 8).
describe("since you last read it", () => {
	const OLDER = "2026-09-26T11:39:00.000Z";
	const NEWER = "2026-09-26T11:45:00.000Z";
	const read = (updatedAt: string) =>
		memory.left(
			{ sessionRef: "local:fix", path: PATH },
			{ title: "Plan", blocks: [], position: null, sessionTitle: "Fix", updatedAt },
		);
	const dots = (tree: ReactTestRenderer) =>
		tree.root.findAll((node) => String(node.type) === "SymbolView" && node.props.name === "circle.fill");

	// Jesse, 2026-09-30: the change shows as a blue dot only. The words cut
	// to "ch…" on the chip's one fact line; VoiceOver, which can't see the
	// dot, still says them.
	it("shows a blue dot, and says so only to VoiceOver, when the session wrote it after your last read", async () => {
		const tree = chip({ updatedAt: NEWER });
		await settle();
		const unchanged = { text: renderedText(tree), spoken: spoken(tree) };
		act(() => {
			read(OLDER);
		});
		expect(dots(tree)).toHaveLength(1);
		expect(dots(tree)[0]?.props.tintColor).toBe(palettes.light.accent);
		// On screen the dot alone; VoiceOver hears one more part at the end.
		expect(renderedText(tree)).toBe(unchanged.text);
		const [said, ...more] = spoken(tree).slice(unchanged.spoken.length);
		expect(spoken(tree).slice(0, unchanged.spoken.length)).toEqual(unchanged.spoken);
		expect(said).toMatch(/\S/);
		expect(more).toEqual([]);
	});

	it("takes the dark theme's blue for the dot", async () => {
		harness.scheme = "dark";
		read(OLDER);
		const tree = chip({ updatedAt: NEWER });
		await settle();
		expect(dots(tree)[0]?.props.tintColor).toBe(palettes.dark.accent);
	});

	// A changed chip always has an age, since the write that changed it gives
	// one; before its summary lands it has no length, and its fact line is the
	// age alone, still on the chip's second line.
	it("shows only its age before its summary lands", () => {
		read(OLDER);
		const tree = chip({ updatedAt: NEWER });
		const lines = button(tree)?.findAll((node) => String(node.type) === "Text" && node.props.numberOfLines === 1);
		expect(lines).toHaveLength(2);
		const [age, ...more] = facts(tree);
		expect(age).toMatch(/^\d+\D/);
		expect(more).toEqual([]);
		// Kind, name, age, and the change VoiceOver says in words.
		expect(spoken(tree)).toHaveLength(4);
	});

	it.each([
		["you haven't opened it", undefined],
		["you read it since", NEWER],
	])("shows neither when %s", async (_name, lastRead) => {
		if (lastRead) read(lastRead);
		const tree = chip({ updatedAt: NEWER });
		await settle();
		expect(dots(tree)).toEqual([]);
		// Kind, title, name, length and age: nothing about a change.
		expect(spoken(tree)).toHaveLength(5);
	});
});

// Sharing from the chip: a touch-and-hold offers the same system sheet the
// Reader does, since a chip is where a video or PDF the agent wrote first
// appears.
it("offers Share on a touch-and-hold, which shares that document", async () => {
	const tree = chip();
	await settle();
	act(() => button(tree).props.onLongPress());
	expect(harness.menus).toHaveLength(1);
	const share = harness.menus[0]?.items.find((item) => item.label === "Share");
	expect(share).toBeDefined();
	await act(async () => {
		await share?.run();
	});
	expect(harness.shares).toEqual([PATH]);
});
