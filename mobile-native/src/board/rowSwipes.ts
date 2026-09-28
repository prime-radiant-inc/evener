// What a Board row's swipes show (spec 7.3): the swipe actions rowActions
// allows, each with its glyph and ink fill (spec 16.1), and the toast and
// Undo after an archive.
import type { NavigationSessionSummary } from "@evener/appwire-client";
import type { ToastController } from "../Toast";
import type { ClassifiedRow } from "./attention";
import { archiveSession, archiveTarget, type RowActionContext, swipeActions } from "./rowActions";
import type { SwipeAction } from "./SwipeRow";
import { type BoardOrganization, organizationOpen } from "./useBoardOrganization";

/** The actions a Board row's swipes can do. */
export type SwipeRowAction = "archive" | "unarchive" | "stop" | "pin" | "more";

const LOOKS: Record<SwipeRowAction, Pick<SwipeAction, "label" | "symbol" | "fill">> = {
	archive: { label: "Archive", symbol: "archivebox", fill: "inkMid" },
	unarchive: { label: "Unarchive", symbol: "archivebox", fill: "inkMid" },
	stop: { label: "Stop", symbol: "stop.fill", fill: "inkHi" },
	pin: { label: "Pin", symbol: "pin.fill", fill: "inkMid" },
	more: { label: "More", symbol: "ellipsis.circle", fill: "inkLow" },
};

export interface RowSwipes {
	leading?: SwipeAction;
	trailing: readonly SwipeAction[];
	/** An archive of this row is on its way (spec 14). */
	dimmed: boolean;
}

/** A row's swipe actions, each running `run` with its action, and whether
 * it dims for `archivingId`, the session an unresolved archive is about.
 * More, which opens the row menu, ends the trailing side, online or not. */
export function rowSwipes(
	item: ClassifiedRow,
	context: RowActionContext,
	archivingId: string | null,
	run: (action: SwipeRowAction) => void,
): RowSwipes {
	const { leading, trailing } = swipeActions(item, context);
	const action = (key: SwipeRowAction): SwipeAction => ({ key, ...LOOKS[key], run: () => run(key) });
	return {
		leading: leading ? action(leading) : undefined,
		trailing: trailing.map(action),
		dimmed: archivingId !== null && archiveTarget(item.row)?.id === archivingId,
	};
}

/** Archive or Unarchive a row through the Board's organization journal.
 * Confirmed, the toast says so and offers Undo, which makes the opposite
 * change the same way. Not confirmed, it says nothing: the journal is settled
 * once, and the row then shows wherever the hub has it. */
export async function archiveRow(
	organization: BoardOrganization,
	row: NavigationSessionSummary,
	archived: boolean,
	toast: Pick<ToastController, "show">,
): Promise<void> {
	const target = archiveTarget(row);
	const actions = organization.actions;
	if (!target || !actions || !organizationOpen(organization)) return;
	if (await archiveSession(actions, target, archived))
		toast.show({
			text: archived ? "Archived" : "Unarchived",
			action: { label: "Undo", run: () => void archiveRow(organization, row, !archived, toast) },
		});
	else void actions.reconcile();
}
