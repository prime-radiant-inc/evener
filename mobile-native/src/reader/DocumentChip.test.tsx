// A document chip under the agent's message (spec 8.2, ruling 28): the
// document's kind and title, its file name, its length and its age, read once
// per document and shared by every chip that shows it.
import { act, type ReactTestRenderer } from "react-test-renderer";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, renderedText } from "../renderNative.testkit";
import { palettes } from "../design/tokens";
import { DocumentChip } from "./DocumentChip";
import { DocumentMemory } from "./documentMemory";
import { forgetDocumentSummaries } from "./documentSummaries";
import type { SyncStringStorage } from "../syncStringStorage";

const harness = vi.hoisted(() => ({
	connection: {} as Record<string, unknown>,
	memory: null as unknown,
	scheme: "light" as "light" | "dark",
}));

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
	AppState: { currentState: "active", addEventListener: () => ({ remove: () => {} }) },
	useColorScheme: () => harness.scheme,
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));
vi.mock("../ConnectionProvider", () => ({ useConnection: () => harness.connection }));
vi.mock("./nativeDocumentMemory", () => ({ documentMemory: () => harness.memory }));
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
const trees: ReactTestRenderer[] = [];

beforeEach(() => {
	harness.scheme = "light";
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
	for (const tree of trees.splice(0)) act(() => tree.unmount());
	vi.restoreAllMocks();
});

async function settle() {
	await act(async () => {
		for (let turn = 0; turn < 10; turn += 1) await Promise.resolve();
		await new Promise((resolve) => setTimeout(resolve, 0));
	});
}

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
	trees.push(tree);
	return tree;
}

const button = (tree: ReactTestRenderer) =>
	tree.root.findAll((node) => String(node.type) === "Pressable" && node.props.accessibilityRole === "button")[0];

it("shows the kind and file name at once, then the title, the length and the age", async () => {
	const updatedAt = new Date(Date.now() - 3 * MINUTE).toISOString();
	const tree = chip({ updatedAt });
	expect(renderedText(tree)).toContain("Plan");
	expect(renderedText(tree)).toContain("settle-race.md");
	await settle();
	const text = renderedText(tree);
	expect(text).toContain("Fix the settle/drain race");
	expect(text).toContain("142 lines");
	expect(text).toContain("3m ago");
	expect(button(tree)?.props.accessibilityLabel).toBe(
		"Plan, Fix the settle/drain race, settle-race.md, 142 lines, 3 minutes ago",
	);
	expect(fetchSpy).toHaveBeenCalledWith(
		"https://hub.test/doc/file?format=raw&session=local%3Afix&path=docs%2Fsuperpowers%2Fplans%2Fsettle-race.md",
		{ headers: { Authorization: "Bearer secret" } },
	);
});

it("shows the file name once before its summary lands", () => {
	const tree = chip({ updatedAt: new Date(Date.now() - 3 * MINUTE).toISOString() });
	expect(renderedText(tree).match(/settle-race\.md/g)).toHaveLength(1);
	expect(button(tree)?.props.accessibilityLabel).toBe("Plan, settle-race.md, 3 minutes ago");
});

it("shows no age for a file the session only named", async () => {
	const tree = chip();
	await settle();
	expect(renderedText(tree)).not.toContain("ago");
	expect(button(tree)?.props.accessibilityLabel).toBe("Plan, Fix the settle/drain race, settle-race.md, 142 lines");
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
	expect(filled?.children.filter((child) => typeof child === "string").join("")).toContain("142 lines");
});

it("opens when pressed", async () => {
	const onOpen = vi.fn();
	const tree = chip({ onOpen });
	act(() => button(tree)?.props.onPress());
	expect(onOpen).toHaveBeenCalledOnce();
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
		read(OLDER);
		const tree = chip({ updatedAt: NEWER });
		await settle();
		expect(dots(tree)).toHaveLength(1);
		expect(dots(tree)[0]?.props.tintColor).toBe(palettes.light.accent);
		expect(renderedText(tree)).not.toContain("changed since you last read");
		expect(button(tree)?.props.accessibilityLabel).toContain("changed since you last read");
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
		const facts = lines?.[1]?.children.filter((child) => typeof child === "string").join("");
		expect(facts).toMatch(/^\S+ ago$/);
		expect(button(tree)?.props.accessibilityLabel).toContain("changed since you last read");
	});

	it.each([
		["you haven't opened it", undefined],
		["you read it since", NEWER],
	])("shows neither when %s", async (_name, lastRead) => {
		if (lastRead) read(lastRead);
		const tree = chip({ updatedAt: NEWER });
		await settle();
		expect(dots(tree)).toEqual([]);
		expect(renderedText(tree)).not.toContain("changed since you last read");
	});
});
