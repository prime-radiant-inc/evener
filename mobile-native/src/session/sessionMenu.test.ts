import type {
	NativeStackHeaderItemMenu,
	NativeStackHeaderItemMenuAction,
	NativeStackHeaderItemMenuSubmenu,
} from "@react-navigation/native-stack";
import { describe, expect, it } from "vitest";
import { DETAIL_LEVELS } from "./detailLevels";
import { type SessionMenuAction, type SessionMenuInput, sessionMenu } from "./sessionMenu";

type Entry = NativeStackHeaderItemMenuAction | NativeStackHeaderItemMenuSubmenu;

function menu(over: Partial<SessionMenuInput> = {}) {
	const chosen: SessionMenuAction[] = [];
	const items = sessionMenu({
		current: "intent",
		hasDocuments: true,
		connected: true,
		sharedNotes: true,
		canAside: true,
		canShutDown: true,
		canStop: true,
		choose: (action) => chosen.push(action),
		...over,
	});
	return { items, chosen };
}

function only(items: ReturnType<typeof sessionMenu>): NativeStackHeaderItemMenu {
	expect(items).toHaveLength(1);
	const [item] = items;
	if (item?.type !== "menu") throw new Error("the header's one item is not a menu");
	return item;
}

const labels = (entries: Entry[]) => entries.map((entry) => entry.label);

/** What each of the menu's actions chooses when pressed, in order (the level
 * submenu holds no action of its own). */
function chosenKinds(over: Partial<SessionMenuInput> = {}): string[] {
	const { items, chosen } = menu(over);
	for (const entry of only(items).menu.items) if (entry.type === "action") entry.onPress();
	return chosen.map((action) => action.kind);
}

function action(entries: Entry[], label: string): NativeStackHeaderItemMenuAction {
	const found = entries.find((entry) => entry.label === label);
	if (found?.type !== "action") throw new Error(`no action labelled ${label}`);
	return found;
}

function levelSection(entries: Entry[]): NativeStackHeaderItemMenuSubmenu {
	const detail = entries[0];
	if (detail?.type !== "submenu") throw new Error("the first item is not the level submenu");
	const [section] = detail.items;
	if (section?.type !== "submenu") throw new Error("the level submenu has no inline section");
	return section;
}

describe("the Session's ⋯ menu (spec 8.1)", () => {
	it("is one menu under the ellipsis.circle symbol, labelled Session actions", () => {
		const item = only(menu().items);
		expect(item.label).toMatch(/\S/);
		expect(item.accessibilityLabel).toBe(item.label);
		expect(item.icon).toEqual({ type: "sfSymbol", name: "ellipsis.circle" });
	});

	it("lists every item in order when every condition holds", () => {
		expect(labels(only(menu().items).menu.items)).toEqual([
			"Detail level · Intent",
			"Stop",
			"Find in session",
			"Files",
			"Activity",
			"Tasks",
			"Notes & links",
			"Session info",
			"Ask aside…",
			"Pin to category…",
			"Archive",
			"Shut down",
		]);
	});

	it("leaves Delete to the Session sheet", () => {
		expect(chosenKinds()).toEqual([
			"stop",
			"find",
			"files",
			"activity",
			"tasks",
			"notes",
			"info",
			"aside",
			"pin",
			"archive",
			"shutDown",
		]);
	});

	it("offers Stop only while the turn can be stopped", () => {
		expect(labels(only(menu({ canStop: false }).items).menu.items)).not.toContain("Stop");
		expect(labels(only(menu({ canStop: true }).items).menu.items)).toContain("Stop");
	});

	it("stops the turn from the menu, so a draft never blocks it", () => {
		const { items, chosen } = menu({ canStop: true });
		action(only(items).menu.items, "Stop").onPress();
		expect(chosen).toEqual([{ kind: "stop" }]);
	});

	it.each([
		["intent", "Detail level · Intent"],
		["custom", "Detail level · Custom"],
		[null, "Detail level"],
	] as const)("labels the level submenu for %s", (current, label) => {
		const detail = only(menu({ current }).items).menu.items[0];
		expect(detail).toMatchObject({ type: "submenu", label });
	});

	it("holds the levels in one inline section with the picker's question as its title", () => {
		const detail = only(menu().items).menu.items[0];
		if (detail?.type !== "submenu") throw new Error("no level submenu");
		expect(detail.items).toHaveLength(1);
		expect(detail.items[0]).toMatchObject({
			type: "submenu",
			inline: true,
			label: "How much of the agent's work this session shows",
		});
	});

	it("offers five levels, each described, with exactly the current one checked", () => {
		const section = levelSection(only(menu({ current: "tools" }).items).menu.items);
		expect(section.items).toHaveLength(5);
		expect(section.items).toEqual(
			DETAIL_LEVELS.map((level) =>
				expect.objectContaining({
					type: "action",
					label: level.label,
					description: level.description,
				}),
			),
		);
		const checked = section.items.filter((entry) => entry.type === "action" && entry.state === "on");
		expect(labels(checked)).toEqual(["Tools"]);
	});

	it.each([null, "custom"] as const)("checks no level when the current one is %s", (current) => {
		const section = levelSection(only(menu({ current }).items).menu.items);
		expect(section.items.filter((entry) => entry.type === "action" && entry.state === "on")).toEqual([]);
	});

	it.each([
		["hasDocuments", "Files"],
		["sharedNotes", "Notes & links"],
		["canAside", "Ask aside…"],
		["canShutDown", "Shut down"],
	] as const)("shows nothing for %s when it is false", (condition, label) => {
		const entries = only(menu({ [condition]: false }).items).menu.items;
		expect(labels(entries)).not.toContain(label);
		expect(entries).toHaveLength(11);
	});

	// Activity and Tasks both read the hub live, so both wait for a connection.
	it("shows neither Activity nor Tasks while disconnected", () => {
		const entries = only(menu({ connected: false }).items).menu.items;
		const kinds = chosenKinds({ connected: false });
		expect(kinds).not.toContain("activity");
		expect(kinds).not.toContain("tasks");
		expect(entries).toHaveLength(10);
	});

	it("opens Files", () => {
		const { items, chosen } = menu();
		action(only(items).menu.items, "Files").onPress();
		expect(chosen).toEqual([{ kind: "files" }]);
	});

	it("offers Notes & links without a connection: the notes are readable offline", () => {
		expect(chosenKinds({ connected: false })).toContain("notes");
	});

	it("describes Ask aside", () => {
		expect(action(only(menu().items).menu.items, "Ask aside…").description).toBe(
			"A side question in its own session; this one keeps working",
		);
	});

	it.each([
		["Stop", { kind: "stop" }],
		["Find in session", { kind: "find" }],
		["Activity", { kind: "activity" }],
		["Tasks", { kind: "tasks" }],
		["Notes & links", { kind: "notes" }],
		["Session info", { kind: "info" }],
		["Ask aside…", { kind: "aside" }],
		["Pin to category…", { kind: "pin" }],
		["Archive", { kind: "archive" }],
		["Shut down", { kind: "shutDown" }],
	] as const)("%s chooses its action", (label, expected) => {
		const { items, chosen } = menu();
		action(only(items).menu.items, label).onPress();
		expect(chosen).toEqual([expected]);
	});

	it("each level chooses that level", () => {
		const { items, chosen } = menu();
		for (const entry of levelSection(only(items).menu.items).items) if (entry.type === "action") entry.onPress();
		expect(chosen).toEqual(DETAIL_LEVELS.map(({ level }) => ({ kind: "level", level })));
	});

	it("draws Stop and Shut down as destructive, and nothing else", () => {
		const entries = only(menu().items).menu.items;
		expect(labels(entries.filter((entry) => entry.destructive))).toEqual(["Stop", "Shut down"]);
	});
});
