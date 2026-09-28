// What a Board row can do (spec 7.3), and how each change reaches the hub:
// the path the Session or the Projects screen already takes for it (this
// plan's "How the Board's actions reach the hub"). Stop is BoardStops.
import type { ArchiveParams, NavigationSessionSummary, SessionPinAssignParams } from "@evener/appwire-client";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";
import type { NavigationActionCheckpoint } from "../navigationActionRepository";
import type { NavigationActions } from "../navigationActions";
import { localSessionId } from "../sessionDeletionResult";
import type { ClassifiedRow } from "./attention";
import { organizationFree } from "./organizationCheck";

export type RowAction = "pin" | "markRead" | "markUnread" | "stop" | "shutDown" | "archive" | "unarchive" | "rename";

export const ROW_ACTION_LABELS: Record<RowAction, string> = {
	pin: "Pin to category…",
	markRead: "Mark as read",
	markUnread: "Mark as unread",
	stop: "Stop",
	shutDown: "Shut down",
	archive: "Archive",
	unarchive: "Unarchive",
	rename: "Rename",
};

export interface RowActionContext {
	/** The hub connection is ready (ruling 21). */
	connected: boolean;
	/** The Board's organization journal can take a change now (ruling 16). */
	organizationReady: boolean;
	/** The row sits in an archived tier. */
	archived: boolean;
}

const NESTED = new Set(["subagent", "fork", "cluster"]);
/** A session of its own, not a subagent, fork or cluster: the rows the
 * organization changes act on. */
export function isTopLevel(row: NavigationSessionSummary): boolean {
	return !NESTED.has(row.kind);
}

/** The archive change for a row, or null for one the organization journal
 * can't confirm (ruling 20): a subagent, fork or cluster, or a row of this hub
 * whose ref doesn't name its own session. This hub's session goes by its bare
 * id, the rule ProjectsScreen applies (ProjectsScreen.tsx:749-757), and another
 * host's by its ref, as the web's rail sends it (archiveSessionIdentity);
 * readOrganizationNavigation reads either back by that row's location. */
export function archiveTarget(row: NavigationSessionSummary): Omit<ArchiveParams, "archived"> | null {
	if (!isTopLevel(row)) return null;
	if (row.host_id !== "local") return { kind: "session", id: row.ref };
	return localSessionId(row.ref) === row.session_id ? { kind: "session", id: row.session_id } : null;
}

/** The long-press menu, in spec 7.3's order. Copy link waits for a session
 * deep link (ruling 27). Offline, only the phone's own read marks remain. */
export function rowMenuActions({ row, state }: ClassifiedRow, context: RowActionContext): RowAction[] {
	const { connected } = context;
	const actions: RowAction[] = [];
	if (connected && isTopLevel(row)) actions.push("pin");
	if (state === "finished") actions.push("markRead");
	if (state === "idle") actions.push("markUnread");
	if (connected && state === "working") actions.push("stop");
	if (connected && row.live && !row.offline && row.state !== "restartRequired") actions.push("shutDown");
	if (connected && context.organizationReady && archiveTarget(row))
		actions.push(context.archived ? "unarchive" : "archive");
	if (connected && row.rename === true) actions.push("rename");
	return actions;
}

export interface SwipeActions {
	leading: "archive" | "unarchive" | null;
	trailing: ("stop" | "pin" | "more")[];
}

/** A swipe right archives, or unarchives in an archived tier; a swipe left
 * reveals Stop (while working), Pin and More (spec 7.3). */
export function swipeActions(item: ClassifiedRow, context: RowActionContext): SwipeActions {
	const menu = rowMenuActions(item, context);
	const leading = menu.includes("archive") ? "archive" : menu.includes("unarchive") ? "unarchive" : null;
	const trailing: SwipeActions["trailing"] = [];
	if (menu.includes("stop")) trailing.push("stop");
	if (menu.includes("pin")) trailing.push("pin");
	trailing.push("more");
	return { leading, trailing };
}

/** Runs one journaled change. True only when it ran and the hub confirmed
 * it; false when the journal refused it (busy, unresolved, or the Board not
 * on screen) or its outcome is unknown, which leaves the journal holding it
 * for the Board to settle. */
async function journaled(actions: NavigationActions, change: () => Promise<void>): Promise<boolean> {
	if (!organizationFree(actions.getSnapshot())) return false;
	let ran = false;
	const stop = actions.subscribe(() => {
		if (actions.getSnapshot().pending) ran = true;
	});
	try {
		await change();
	} finally {
		stop();
	}
	const state = actions.getSnapshot();
	return ran && organizationFree(state) && state.recovery === null;
}

/** Archive or Unarchive, as the Projects screen does it
 * (NavigationActions.archive and the organization journal). */
export function archiveSession(
	actions: NavigationActions,
	target: Omit<ArchiveParams, "archived">,
	archived: boolean,
): Promise<boolean> {
	return journaled(actions, () => actions.archive(target, archived));
}

/** Select mode's Pin (ruling 18); a single row's Pin opens PinAssignment. */
export function pinSession(actions: NavigationActions, target: SessionPinAssignParams): Promise<boolean> {
	return journaled(actions, () => actions.assignPin(target));
}

/** The session an unresolved archive is about, so its rows dim until the hub
 * confirms (spec 14). */
export function archivingSessionId(
	state: { pending: boolean; uncertain: boolean; recovery: NavigationActionCheckpoint | null } | null,
): string | null {
	const operation = state?.recovery?.operation;
	if (
		!state ||
		(!state.pending && !state.uncertain) ||
		operation?.kind !== "archive" ||
		operation.params.kind !== "session"
	)
		return null;
	return operation.params.id;
}

/** Shut down: the Session's own request (conversation.ts:1128-1132). */
export async function shutDownSession(client: ConversationClientLike, ref: string): Promise<void> {
	await client.request("thread/shutdown", { ref });
}

/** Rename: the Session's own request (conversation.ts:1197-1201). */
export async function renameSession(client: ConversationClientLike, ref: string, name: string): Promise<boolean> {
	const trimmed = name.trim();
	if (!trimmed) return false;
	await client.request("evener/thread/name/set", { ref, name: trimmed });
	return true;
}
