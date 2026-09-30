// How the Board confirms an organization change against the hub, whichever
// screen journaled it. The organization journal holds one change per hub
// (navigationActionRepository.ts), so the Board's one NavigationActions has to
// settle archive and favorite changes (the Projects screen's read) and pin
// changes (the pin screens' read) alike (ruling 16).
import type { NavigationPinSectionDescriptor } from "@evener/appwire-client";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";
import type { NavigationActionCheckpoint, NavigationOperation } from "../navigationActionRepository";
import type { NavigationPages } from "../navigationPages";
import { readOrganizationNavigation } from "../organizationNavigation";
import { refreshPinNavigation } from "../pinNavigation";
import type { BoardOrganization } from "./useBoardOrganization";

/** Reads the hub's navigation for a journaled change. Resolves true when the
 * hub shows it, false when the hub shows otherwise (the Board then shows what
 * the hub has), and rejects for a change checked elsewhere (a session
 * deletion, on its own screen) or a read that fails. */
export async function checkOrganizationChange(
	client: ConversationClientLike,
	pinPages: NavigationPages<NavigationPinSectionDescriptor>,
	checkpoint: NavigationActionCheckpoint,
	current: () => boolean,
	confirmReceipt: boolean,
): Promise<boolean> {
	switch (checkpoint.operation.kind) {
		case "archive":
		case "favorite":
			return (await readOrganizationNavigation(client, checkpoint, current, confirmReceipt)).settled;
		case "assignPin":
		case "unpin":
		case "renamePinSection":
		case "deletePinSection":
			await refreshPinNavigation(client, pinPages, { checkpoint, current, confirmReceipt });
			return true;
		default:
			throw Error("This change is checked on the screen that made it.");
	}
}

/** Whether the journal can take a change now. */
export function organizationFree(
	state: { pending: boolean; uncertain: boolean; storageUnavailable: boolean } | null,
): boolean {
	return !!state && !state.pending && !state.uncertain && !state.storageUnavailable;
}

/** The organization operation the journal is holding, or null when none is.
 *
 * One gate for every reader that holds or dims an item while the journal has
 * it: an operation counts while its change is pending or unresolved (the
 * archivingSessionId rule). The three readers that used to restate this gate
 * independently had drifted, so an unresolved change dimmed sessions but not
 * categories or projects (issue #2703). */
export function journalOperation(
	state: { pending: boolean; uncertain: boolean; recovery: NavigationActionCheckpoint | null } | null,
): NavigationOperation | null {
	if (!state || (!state.pending && !state.uncertain)) return null;
	return state.recovery?.operation ?? null;
}

/** Whether an organization change can go out now: the binding still holds
 * and the journal is free. A menu checks again at every press, since the
 * connection or the journal may have moved while a sheet or an alert was up.
 * Here, apart from the hook, so a module without React can ask. */
export function organizationOpen(organization: Pick<BoardOrganization, "isCurrent" | "actions">): boolean {
	return organization.isCurrent() && !!organization.actions && organizationFree(organization.actions.getSnapshot());
}
