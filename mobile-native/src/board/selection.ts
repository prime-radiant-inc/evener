// Select mode (spec 7.1): which of the chosen sessions each of the select
// bar's actions applies to.
import type { NavigationSessionSummary } from "@evener/appwire-client";
import type { ClassifiedRow } from "./attention";
import { archiveTarget, isTopLevel } from "./rowActions";

export interface SelectedRow {
	item: ClassifiedRow;
	/** The row was chosen in an archived tier. */
	archived: boolean;
}

export interface SelectionActions {
	archive: NavigationSessionSummary[];
	pin: NavigationSessionSummary[];
}

/** Each session once, however many sections it was chosen in. Archive takes
 * top-level sessions not already archived, on this hub or another host
 * (ruling 20), and Pin top-level sessions, held when they can't go now
 * (phase 6 ruling 18). */
export function selectionActions(selected: readonly SelectedRow[]): SelectionActions {
	const actions: SelectionActions = { archive: [], pin: [] };
	const seen = new Set<string>();
	for (const { item, archived } of selected) {
		if (seen.has(item.row.ref)) continue;
		seen.add(item.row.ref);
		if (!archived && archiveTarget(item.row)) actions.archive.push(item.row);
		if (isTopLevel(item.row)) actions.pin.push(item.row);
	}
	return actions;
}

export function toggleSelected(selected: ReadonlySet<string>, ref: string): Set<string> {
	const next = new Set(selected);
	if (!next.delete(ref)) next.add(ref);
	return next;
}
