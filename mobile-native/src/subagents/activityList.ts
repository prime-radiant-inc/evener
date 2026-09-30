import {
	type ActivityListRow,
	matchesSearch,
	STATE_ORDER,
	type SubagentState,
	subagentSections,
} from "./subagentModel";

export type ActivityFilter = "all" | SubagentState;

export type ActivityListItem =
	| { kind: "section"; state: SubagentState; count: number }
	| { kind: "row"; row: ActivityListRow }
	| { kind: "doneFold"; count: number; open: boolean }
	| { kind: "missing"; title: string };

/** The list's items for a filter and a search (spec 9): subagents and shell
 * jobs together, failed, then running, then done, where done is one folded
 * row under All until you open it. Section counts follow the search; the
 * chips and the strip don't (ruling 8). What couldn't be listed comes last. */
export function activityListItems(
	rows: readonly ActivityListRow[],
	view: { filter: ActivityFilter; query: string; doneOpen: boolean; missing: readonly string[] },
): ActivityListItem[] {
	const sections = subagentSections(rows.filter((row) => matchesSearch(row, view.query)));
	const items: ActivityListItem[] = [];
	for (const state of STATE_ORDER) {
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
	// Two branches can share a title; the line names the title once.
	for (const title of new Set(view.missing)) items.push({ kind: "missing", title });
	return items;
}

export function activityListKey(item: ActivityListItem): string {
	switch (item.kind) {
		case "section":
			return `section:${item.state}`;
		case "row":
			// A job's id is a job id; a subagent's is its delegate id.
			return item.row.kind === "job" ? `job:${item.row.id}` : item.row.id;
		case "doneFold":
			return "done-fold";
		case "missing":
			return `missing:${item.title}`;
	}
}
