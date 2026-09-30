// What a Board row can do (spec 7.3), and how each change reaches the hub:
// the path the Session or the Projects screen already takes for it (this
// plan's "How the Board's actions reach the hub"). Stop is BoardStops.
import { type ArchiveParams, errorText, type NavigationSessionSummary } from "@evener/appwire-client";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";
import type { NavigationActionCheckpoint } from "../navigationActionRepository";
import type { NavigationActions } from "../navigationActions";
import { localSessionId } from "../sessionDeletionResult";
import type { ClassifiedRow } from "./attention";
import { journalOperation, organizationFree } from "./organizationCheck";
import type { ProjectMenuAction } from "./projectMenu";

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

/** What a row's actions depend on. Neither the connection nor the journal
 * is here: a change that can't go now is held and sent when it can (phase 6
 * ruling 18), never refused. */
export interface RowActionContext {
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
 * deep link (ruling 27). Offline, or with the journal busy, it offers the
 * same actions, held until they can go (phase 6 ruling 18). */
export function rowMenuActions({ row, state }: ClassifiedRow, context: RowActionContext): RowAction[] {
	const actions: RowAction[] = [];
	if (isTopLevel(row)) actions.push("pin");
	if (state === "finished") actions.push("markRead");
	if (state === "idle") actions.push("markUnread");
	if (state === "working") actions.push("stop");
	if (row.live && !row.offline && row.state !== "restartRequired") actions.push("shutDown");
	if (archiveTarget(row)) actions.push(context.archived ? "unarchive" : "archive");
	if (row.rename === true) actions.push("rename");
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

/** How a journaled change went: "confirmed" when the hub confirmed it;
 * "unconfirmed" when the journal took it but its outcome is unknown, which
 * leaves the journal holding it for the Board to settle; "notTaken" when the
 * journal refused it (busy, unresolved, its storage failing, or the Board
 * not on screen), so nothing was sent. */
export type JournalOutcome = "confirmed" | "unconfirmed" | "notTaken";

export async function journalOutcome(actions: NavigationActions, change: () => Promise<void>): Promise<JournalOutcome> {
	if (!organizationFree(actions.getSnapshot())) return "notTaken";
	let ran = false;
	const stop = actions.subscribe(() => {
		if (actions.getSnapshot().pending) ran = true;
	});
	try {
		await change();
	} finally {
		stop();
	}
	if (!ran) return "notTaken";
	const state = actions.getSnapshot();
	return organizationFree(state) && state.recovery === null ? "confirmed" : "unconfirmed";
}

/** Runs one journaled change: true only when the hub confirmed it. */
async function journaled(actions: NavigationActions, change: () => Promise<void>): Promise<boolean> {
	return (await journalOutcome(actions, change)) === "confirmed";
}

/** A project's Pin to top, Unpin, Archive or Unarchive (ruling 15), through
 * the same journal, as a held one replays. */
export function projectChange(
	actions: NavigationActions,
	project: { key: string; workingDir?: string },
	action: ProjectMenuAction,
): Promise<boolean> {
	return journaled(actions, () => projectRequest(actions, project, action));
}

/** The journal request a project change makes. */
export function projectRequest(
	actions: NavigationActions,
	project: { key: string; workingDir?: string },
	action: ProjectMenuAction,
): Promise<void> {
	return action === "pin" || action === "unpin"
		? actions.favorite(project.key, action === "pin")
		: actions.archive({ kind: "project", id: project.key, workingDir: project.workingDir }, action === "archive");
}

/** The session an unresolved archive is about, so its rows dim until the hub
 * confirms (spec 14). */
export function archivingSessionId(
	state: { pending: boolean; uncertain: boolean; recovery: NavigationActionCheckpoint | null } | null,
): string | null {
	const operation = journalOperation(state);
	if (operation?.kind !== "archive" || operation.params.kind !== "session") return null;
	return operation.params.id;
}

/** What Shut down and Rename say afterwards, online or replayed. */
export const SHUT_DOWN_DONE = "Session shut down";
export const RENAMED = "Renamed";
export const shutDownFailed = (title: string, error: unknown) => `Couldn't shut down “${title}”: ${errorText(error)}`;
export const renameFailed = (title: string, error: unknown) => `Couldn't rename “${title}”: ${errorText(error)}`;

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
