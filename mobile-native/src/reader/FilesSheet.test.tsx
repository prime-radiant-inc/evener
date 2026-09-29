// Files & artifacts (spec 10.1, ruling 26): the session's documents, newest
// write first, each with its kind, title, path, length and age, and a blue dot
// when it's new or changed since you last opened it.
import { act, type ReactTestRenderer } from "react-test-renderer";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { pressable, render, renderedText } from "../renderNative.testkit";
import type { SyncStringStorage } from "../syncStringStorage";
import { DocumentMemory } from "./documentMemory";
import { forgetDocumentSummaries } from "./documentSummaries";
import { FilesSheet } from "./FilesSheet";
import type { SessionDocument } from "./sessionDocuments";

const harness = vi.hoisted(() => ({ connection: {} as Record<string, unknown>, memory: null as unknown }));
const sheetNavigation = vi.hoisted(() => ({ goBack: vi.fn(), dispatch: vi.fn(), navigate: vi.fn() }));

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
	AppState: { currentState: "active", addEventListener: () => ({ remove: () => {} }) },
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));
vi.mock("@react-navigation/native", () => ({
	useNavigation: () => sheetNavigation,
	usePreventRemove: () => {},
}));
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

const MINUTE = 60_000;
const PLAN = "docs/superpowers/plans/settle-race.md";
const DESIGN = "docs/design/flake-triage.md";
const CODE = "agent/retirement.go";
const bodies: Record<string, string> = {
	[PLAN]: `# Fix the settle/drain race\n\n${Array.from({ length: 140 }, (_, index) => `Line ${index + 1}.`).join("\n")}\n`,
	[DESIGN]: "# Flake triage\n\nOne line.\n",
	[CODE]: "package agent\n",
};

let memory: DocumentMemory;
const trees: ReactTestRenderer[] = [];

function storage(): SyncStringStorage {
	const data = new Map<string, string>();
	return {
		getItemSync: (key) => data.get(key) ?? null,
		setItemSync: (key, value) => void data.set(key, value),
		removeItemSync: (key) => void data.delete(key),
	};
}

beforeEach(() => {
	forgetDocumentSummaries("studio");
	sheetNavigation.goBack.mockReset();
	sheetNavigation.navigate.mockReset();
	harness.connection = { profiles: [{ id: "studio", name: "Studio", origin: "https://hub.test" }], state: "ready" };
	memory = new DocumentMemory(storage(), "studio");
	harness.memory = memory;
	vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
		const path = new URL(String(input)).searchParams.get("path") ?? "";
		return new Response(bodies[path] ?? "", { headers: { "Content-Type": "text/plain; charset=utf-8" } });
	});
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

const ago = (minutes: number) => new Date(Date.now() - minutes * MINUTE).toISOString();

async function sheet(documents: SessionDocument[]) {
	const params = { hubId: "studio", ref: "local:fix", title: "Fix race", documents };
	const tree = render(
		<FilesSheet route={{ key: "files", name: "FilesSheet", params } as never} navigation={sheetNavigation as never} />,
	);
	trees.push(tree);
	await settle();
	return tree;
}

const rows = (tree: ReactTestRenderer) =>
	tree.root.findAll(
		(node) =>
			String(node.type) === "Pressable" &&
			node.props.accessibilityRole === "button" &&
			node.props.testID === "document-row",
	);
const dotted = (row: ReturnType<typeof rows>[number]) =>
	row.findAll((node) => String(node.type) === "SymbolView" && node.props.name === "circle.fill").length > 0;

it("lists each document with its kind, title, path, length and age, in the order it opened with", async () => {
	const tree = await sheet([
		{ path: DESIGN, kind: "Doc", updatedAt: ago(1) },
		{ path: PLAN, kind: "Plan", updatedAt: ago(3) },
		{ path: CODE, kind: "Code" },
	]);
	const text = renderedText(tree);
	expect(text).toContain("Files & artifacts");
	expect(text).toContain("Fix the settle/drain race");
	expect(text).toContain(PLAN);
	expect(text).toContain("142 lines");
	expect(text).toContain("3m ago");
	const labels = rows(tree).map((row) => String(row.props.accessibilityLabel));
	expect(labels).toHaveLength(3);
	expect(labels[0]).toMatch(/^Doc, Flake triage, /);
	expect(labels[1]).toBe(`Plan, Fix the settle/drain race, ${PLAN}, 142 lines, 3 minutes ago, new`);
	expect(labels[2]).toMatch(/^Code, retirement\.go, agent\/retirement\.go, 1 line, new$/);
});

it("dots a document you haven't opened, and one written after your last read, and not one read since", async () => {
	const leave = (path: string, updatedAt: string) =>
		memory.left(
			{ sessionRef: "local:fix", path },
			{ title: "x", blocks: [], position: null, sessionTitle: "Fix race", updatedAt },
		);
	const written = ago(1);
	leave(PLAN, ago(10));
	leave(DESIGN, written);
	const tree = await sheet([
		{ path: CODE, kind: "Code", updatedAt: written },
		{ path: PLAN, kind: "Plan", updatedAt: written },
		{ path: DESIGN, kind: "Doc", updatedAt: written },
	]);
	expect(rows(tree).map(dotted)).toEqual([true, true, false]);
});

it("opens a document in the Reader over the session, after the sheet goes", async () => {
	const updatedAt = ago(3);
	const tree = await sheet([{ path: PLAN, kind: "Plan", updatedAt }]);
	const order: string[] = [];
	sheetNavigation.goBack.mockImplementation(() => order.push("goBack"));
	sheetNavigation.navigate.mockImplementation((name: string) => order.push(name));
	act(() => rows(tree)[0]?.props.onPress());
	expect(order).toEqual(["goBack", "Reader"]);
	expect(sheetNavigation.navigate).toHaveBeenCalledWith("Reader", {
		hubId: "studio",
		sessionRef: "local:fix",
		path: PLAN,
		sessionTitle: "Fix race",
		updatedAt,
	});
});

it("says so when the session has no documents", async () => {
	const tree = await sheet([]);
	expect(renderedText(tree)).toContain("This session hasn't written or linked any documents yet.");
});

it("closes with Done", async () => {
	const tree = await sheet([]);
	act(() => pressable(tree, "Done")?.props.onPress());
	expect(sheetNavigation.goBack).toHaveBeenCalledOnce();
});
