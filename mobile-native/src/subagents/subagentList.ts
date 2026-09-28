import {
	matchesSearch,
	type SubagentRow,
	type SubagentState,
	subagentSections,
} from "./subagentModel";

export type SubagentFilter = "all" | SubagentState;

export type SubagentListItem =
	| { kind: "section"; state: SubagentState; count: number }
	| { kind: "row"; row: SubagentRow }
	| { kind: "doneFold"; count: number; open: boolean }
	| { kind: "missing"; title: string };

const ORDER: readonly SubagentState[] = ["failed", "running", "done"];

/** The list's items for a filter and a search (spec 9): failed, then
 * running, then done, where done is one folded row under All until you open
 * it. Section counts follow the search; the chips and the strip don't (ruling
 * 8). What couldn't be listed comes last. */
export function subagentListItems(
	rows: readonly SubagentRow[],
	view: { filter: SubagentFilter; query: string; doneOpen: boolean; missing: readonly string[] },
): SubagentListItem[] {
	const sections = subagentSections(rows.filter((row) => matchesSearch(row, view.query)));
	const items: SubagentListItem[] = [];
	for (const state of ORDER) {
		if (view.filter !== "all" && view.filter !== state) continue;
		const section = sections[state];
		if (section.length === 0) continue;
		if (state === "done" && view.filter === "all") {
			items.push({ kind: "doneFold", count: section.length, open: view.doneOpen });
			if (view.doneOpen) for (const row of section) items.push({ kind: "row", row });
			continue;
		}
		items.push({ kind: "section", state, count: section.length });
		for (const row of section) items.push({ kind: "row", row });
	}
	for (const title of view.missing) items.push({ kind: "missing", title });
	return items;
}

export function subagentListKey(item: SubagentListItem): string {
	switch (item.kind) {
		case "section":
			return `section:${item.state}`;
		case "row":
			return item.row.id;
		case "doneFold":
			return "done-fold";
		case "missing":
			return `missing:${item.title}`;
	}
}
