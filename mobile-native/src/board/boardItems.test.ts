import type { NavigationSessionSummary } from "@evener/appwire-client";
import { expect, it } from "vitest";
import type { BoardState, ClassifiedRow, LiveBands } from "./attention";
import { type BoardItem, groupItems, liveItems, pinnedItems, projectItems } from "./boardItems";
import type { ProjectTreeItem } from "./projectTree";

const row = (ref: string): NavigationSessionSummary => ({
	ref,
	host_id: "local",
	session_id: ref,
	title: ref,
	project: "evener",
	state: "idle",
	kind: "session",
	live: true,
	children: [],
});
const classified = (ref: string, state: BoardState): ClassifiedRow => ({ row: row(ref), state });
const bands = (over: Partial<LiveBands> = {}): LiveBands => ({
	needsYou: [],
	working: [],
	idle: [],
	...over,
});
/** Each item as its key and what it says of itself, for comparing lists. */
const shape = (items: readonly BoardItem[]) =>
	items.map((item) => {
		if (item.kind === "row")
			return [
				item.key,
				item.group,
				item.variant,
				item.moving,
				item.needsYou,
				item.archived,
				item.depth,
				item.separated,
			];
		return [item.key, item.group, item.kind];
	});

it("lists Live as band headers and rows keyed by ref alone, marking which sit in Needs you", () => {
	const items = liveItems(
		bands({
			needsYou: [classified("local:ask", "question")],
			working: [classified("local:work", "working"), classified("local:build", "working")],
			idle: [classified("local:old", "idle")],
		}),
		false,
	);
	expect(shape(items)).toEqual([
		["band:needsYou", "live", "band"],
		["live:local:ask", "live", "signal", false, true, false, 0, true],
		["band:working", "live", "band"],
		["live:local:work", "live", "signal", true, false, false, 0, true],
		["live:local:build", "live", "signal", true, false, false, 0, true],
		["idle-fold", "live", "idleFold"],
		["live:local:old", "live", "quiet", false, false, false, 0, true],
	]);
	expect(items.filter((item) => item.kind === "band").map((item) => item.count)).toEqual([1, 2]);
});

it("leaves out empty bands, and Idle's rows while it is folded", () => {
	const items = liveItems(
		bands({ working: [classified("local:work", "working")], idle: [classified("local:old", "idle")] }),
		true,
	);
	expect(shape(items)).toEqual([
		["band:working", "live", "band"],
		["live:local:work", "live", "signal", true, false, false, 0, true],
		["idle-fold", "live", "idleFold"],
	]);
	expect(items.at(-1)).toMatchObject({ count: 1, folded: true, unseen: false });
	expect(liveItems(bands(), false)).toEqual([]);
});

it("says on Idle's fold whether any session inside is unseen", () => {
	const fold = (idle: ClassifiedRow[]) => liveItems(bands({ idle }), true).at(-1);
	expect(fold([classified("local:old", "idle")])).toMatchObject({ kind: "idleFold", unseen: false });
	expect(fold([classified("local:old", "idle"), { ...classified("local:new", "idle"), unseen: true }])).toMatchObject({
		kind: "idleFold",
		unseen: true,
	});
});

it("lists each pinned category as its header, then its sessions as quiet still rows, even a working one", () => {
	const mine = { id: "pins-1", name: "Mine", count: 2 };
	const items = pinnedItems(
		[mine],
		{ "pins-1": { loaded: true, rows: [row("local:work"), row("local:done")] } },
		() => false,
		(summary) => ({ row: summary, state: summary.ref === "local:work" ? "working" : "idle" }),
	);
	expect(shape(items)).toEqual([
		["pin:pins-1", "pin:pins-1", "pinHeader"],
		["pin:pins-1:local:work", "pin:pins-1", "quiet", false, undefined, false, 0, true],
		["pin:pins-1:local:done", "pin:pins-1", "quiet", false, undefined, false, 0, true],
	]);
	expect(items[0]).toMatchObject({ section: mine, folded: false });
	expect(items[1]).toMatchObject({ item: { state: "working" } });
});

it("says how to pin to an empty category only once its page has loaded, and nothing while it is folded", () => {
	const empty = { id: "pins-2", name: "Empty", count: 0 };
	const classify = (summary: NavigationSessionSummary): ClassifiedRow => ({ row: summary, state: "idle" });
	const kinds = (page: { loaded: boolean; rows: NavigationSessionSummary[] } | undefined, folded = false) =>
		pinnedItems([empty], page ? { "pins-2": page } : {}, () => folded, classify).map((item) => item.kind);
	expect(kinds(undefined)).toEqual(["pinHeader"]);
	expect(kinds({ loaded: false, rows: [] })).toEqual(["pinHeader"]);
	expect(kinds({ loaded: true, rows: [] })).toEqual(["pinHeader", "pinEmpty"]);
	expect(kinds({ loaded: true, rows: [] }, true)).toEqual(["pinHeader"]);
	expect(pinnedItems([empty], {}, () => true, classify)[0]).toMatchObject({ folded: true });
});

it("lists each project section as its header and its tree, session rows keyed within the section", () => {
	const tree: ProjectTreeItem[] = [
		{ kind: "tier", key: "evener:today", depth: 1, label: "Today" },
		{ kind: "session", key: "evener:today:local:a", depth: 1, row: row("local:a"), archived: false },
		{ kind: "session", key: "evener:archived:local:b", depth: 2, row: row("local:b"), archived: true },
	];
	const items = projectItems(
		[
			{ section: "projects", folded: false, items: tree },
			{ section: "archived", folded: true, items: [] },
		],
		(summary) => ({ row: summary, state: "idle" }),
	);
	expect(shape(items)).toEqual([
		["project:projects", "project:projects", "projectHeader"],
		["project:projects:evener:today", "project:projects", "tree"],
		["project:projects:evener:today:local:a", "project:projects", "quiet", false, undefined, false, 1, false],
		["project:projects:evener:archived:local:b", "project:projects", "quiet", false, undefined, true, 2, false],
		["project:archived", "project:archived", "projectHeader"],
	]);
	expect(items[4]).toMatchObject({ section: "archived", folded: true });
});

it("groups a list by section, in order", () => {
	const live = liveItems(bands({ working: [classified("local:work", "working")] }), false);
	const pinned = pinnedItems(
		[{ id: "pins-1", name: "Mine", count: 0 }],
		{},
		() => false,
		(summary) => ({ row: summary, state: "idle" }),
	);
	const groups = groupItems([...live, ...pinned]);
	expect([...groups.keys()]).toEqual(["live", "pin:pins-1"]);
	expect(groups.get("live")?.map((item) => item.key)).toEqual(["band:working", "live:local:work"]);
});

it("gives an archived project session no blue dot, though it may still be running", () => {
	const items = projectItems(
		[
			{
				section: { key: "p" } as never,
				folded: false,
				items: [{ kind: "session", key: "s", row: row("local:archived"), depth: 0, archived: true } as never],
			},
		],
		(summary) => ({ row: summary, state: "working", unseen: true }),
	);
	expect(items.flatMap((item) => (item.kind === "row" ? [item.item.unseen] : []))).toEqual([false]);
});
