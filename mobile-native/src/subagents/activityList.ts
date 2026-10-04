import { activityNodeID } from "@evener/appwire-client";
import { type ActivityListRow, isSubagentRow, matchesSearch, newestFirst } from "./subagentModel";

export type ActivityFilter = "all" | "running" | "done";

export type ActivityListItem =
	| { kind: "section"; state: "running" | "done" | "completed"; count: number }
	| { kind: "row"; row: ActivityListRow }
	| { kind: "doneFold"; count: number; open: boolean }
	| { kind: "completedJobsFold"; count: number; open: boolean }
	// A branch whose subagent isn't loaded yet has no title to give.
	| { kind: "missing"; title?: string };

/** Live work followed by independent delegate and job histories. Section
 * counts follow the search; authoritative chips and the strip don't.
 * What couldn't be listed comes last,
 * each branch (`missing` holds their session refs) named by whose it is: the
 * coordinator by its title, a subagent by its row's. */
export function activityListItems(
	rows: readonly ActivityListRow[],
	view: {
		filter: ActivityFilter;
		query: string;
		doneOpen: boolean;
		completedJobsOpen: boolean;
		missing: readonly string[];
		coordinator: { ref: string; title: string };
	},
): ActivityListItem[] {
	const matching = rows.filter((row) => matchesSearch(row, view.query));
	const running = matching.filter((row) => row.state === "running").sort(newestFirst);
	const delegates = matching.filter((row) => row.kind === "subagent" && row.state !== "running").sort(newestFirst);
	const jobs = matching.filter((row) => row.kind === "job" && row.state !== "running").sort(newestFirst);
	const items: ActivityListItem[] = [];
	if (view.filter !== "done" && running.length > 0) {
		items.push({ kind: "section", state: "running", count: running.length });
		for (const row of running) items.push({ kind: "row", row });
	}
	if (view.filter !== "running") {
		const histories = [
			{ state: "done", kind: "doneFold", rows: delegates, open: view.doneOpen },
			{ state: "completed", kind: "completedJobsFold", rows: jobs, open: view.completedJobsOpen },
		] as const;
		for (const history of histories) {
			if (history.rows.length === 0) continue;
			if (view.filter === "all") {
				items.push({ kind: history.kind, count: history.rows.length, open: history.open });
				if (!history.open) continue;
			} else {
				items.push({ kind: "section", state: history.state, count: history.rows.length });
			}
			for (const row of history.rows) items.push({ kind: "row", row });
		}
	}
	const titleOf = (ref: string) =>
		ref === view.coordinator.ref
			? view.coordinator.title
			: rows.find((row) => isSubagentRow(row) && row.ref === ref)?.title;
	// Two branches can share a title; the line names the title once.
	for (const title of new Set(view.missing.map(titleOf))) items.push({ kind: "missing", title });
	return items;
}

export function activityListKey(item: ActivityListItem): string {
	switch (item.kind) {
		case "section":
			return `section:${item.state}`;
		case "row":
			// A job's id is a job id; a subagent's is its delegate id.
			return item.row.kind === "job"
				? activityNodeID({ kind: "shell", ...item.row.job })
				: activityNodeID({ kind: "delegate", ...item.row.delegate });
		case "doneFold":
			return "done-fold";
		case "completedJobsFold":
			return "completed-jobs-fold";
		case "missing":
			return item.title === undefined ? "missing" : `missing:${item.title}`;
	}
}
