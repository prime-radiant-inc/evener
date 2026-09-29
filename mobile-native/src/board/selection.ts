// Select mode (spec 7.1): which of the chosen sessions each of the select
// bar's actions applies to.
import type { NavigationSessionSummary } from "@evener/appwire-client";
import type { ClassifiedRow } from "./attention";
import { archiveTarget, isTopLevel, organizes } from "./rowActions";

export interface SelectedRow {
	item: ClassifiedRow;
	/** The row was chosen in an archived tier. */
	archived: boolean;
}

export interface SelectionActions {
	archive: NavigationSessionSummary[];
	pin: NavigationSessionSummary[];
	markRead: NavigationSessionSummary[];
}

/** Each session once, however many sections it was chosen in. Archive takes
 * top-level sessions not already archived, on this hub or another host
 * (ruling 20), and Pin top-level sessions, both only while connected with the
 * journal free (ruling 21); Mark as read takes the finished ones. */
export function selectionActions(
	selected: readonly SelectedRow[],
	context: { connected: boolean; organizationReady: boolean },
): SelectionActions {
	const actions: SelectionActions = { archive: [], pin: [], markRead: [] };
	const organize = organizes(context);
	const seen = new Set<string>();
	for (const { item, archived } of selected) {
		if (seen.has(item.row.ref)) continue;
		seen.add(item.row.ref);
		if (organize && !archived && archiveTarget(item.row)) actions.archive.push(item.row);
		if (organize && isTopLevel(item.row)) actions.pin.push(item.row);
		if (item.state === "finished") actions.markRead.push(item.row);
	}
	return actions;
}

export function toggleSelected(selected: ReadonlySet<string>, ref: string): Set<string> {
	const next = new Set(selected);
	if (!next.delete(ref)) next.add(ref);
	return next;
}
