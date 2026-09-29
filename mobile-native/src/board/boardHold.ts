// Board actions taken offline wait here and go when the connection returns
// (spec 7.5; phase 6 ruling 18, Jesse: "board actions while offline: hold
// em"). A durable list per hub, in the order held.
//
// It is a hold of its own, not the mutation outbox: the outbox carries the
// four turn kinds with receipts and client mutation ids, while archive, pin
// and rename are set-style writes the hub answers without receipts (the
// organization journal's read-back confirms them), and thread/shutdown and
// the rename take no client mutation id. Set-style writes replay safely; the
// hold's job is order, durability, and keeping a Stop or Shut down from
// landing on a turn nobody saw.
import type { ArchiveParams, EvenerThread, SessionPinAssignParams } from "@evener/appwire-client";
import { isPlainObject } from "@evener/appwire-client";
import { readJson, writeJson } from "../deviceStorage";
import type { SyncStringStorage } from "../syncStringStorage";
import type { ProjectMenuAction } from "./projectMenu";

/** The turn you saw when you pressed: the row's turn_ended_at (when the
 * session's previous turn ended, or null while none has), and whether a turn
 * was running. The stamp alone can't tell a turn that began since a press on
 * a session at rest: no turn has ended in between. */
export interface TurnSeen {
	turnEndedAt: string | null;
	running: boolean;
}

export type HeldAction =
	/** `ref` is the row's, for finding what waits on it; the target is what
	 * the journal sends (a local session goes by its bare id). */
	| { kind: "archive"; ref: string; target: Omit<ArchiveParams, "archived">; archived: boolean }
	| { kind: "pin"; target: SessionPinAssignParams }
	| { kind: "project"; project: { key: string; workingDir?: string }; action: ProjectMenuAction }
	/** `title` is the row's when you pressed, for the toast the online
	 * action shows. */
	| { kind: "rename"; ref: string; title: string; name: string }
	| { kind: "stop" | "shutDown"; ref: string; title: string; seen: TurnSeen };

export interface HeldRecord {
	id: string;
	heldAt: number;
	action: HeldAction;
}

export const boardHoldKey = (hubId: string) => `evener.native.board-hold.${hubId}`;

/** What an action is about: holding a second action on the same subject
 * replaces the first (the last word wins). */
function subject(action: HeldAction): string {
	if (action.kind === "archive") return `archive:${action.ref}`;
	if (action.kind === "pin") return `pin:${action.target.sessionRef}`;
	if (action.kind === "project")
		return `project-${action.action === "pin" || action.action === "unpin" ? "pin" : "archive"}:${action.project.key}`;
	return `${action.kind}:${action.ref}`;
}

/** Whether `next` undoes `held`: an unarchive after an archive, an Unpin
 * after a Pin to top. The pair means "as it was", so neither goes. */
function undoes(next: HeldAction, held: HeldAction): boolean {
	if (next.kind === "archive" && held.kind === "archive") return next.archived !== held.archived;
	if (next.kind === "project" && held.kind === "project") return next.action !== held.action;
	return false;
}

/** The session a held action is about, when it is about one. */
export function heldRef(action: HeldAction): string | null {
	if (action.kind === "pin") return action.target.sessionRef;
	if (action.kind === "project") return null;
	return action.ref;
}

const PROJECT_VERBS: Record<ProjectMenuAction, string> = {
	pin: "Pin to top",
	unpin: "Unpin",
	archive: "Archive",
	unarchive: "Unarchive",
};

/** A held action's name, as its row and menu say it: "Archive waits for
 * the connection", "Cancel Archive". */
export function heldVerb(action: HeldAction): string {
	if (action.kind === "archive") return action.archived ? "Archive" : "Unarchive";
	if (action.kind === "pin") return "Pin";
	if (action.kind === "rename") return "Rename";
	if (action.kind === "project") return PROJECT_VERBS[action.action];
	return action.kind === "stop" ? "Stop" : "Shut down";
}

/** A row's second line while something waits for it: the latest held
 * action (spec 14), or null when nothing does. */
export function waitingLine(records: readonly HeldRecord[], ref: string): string | null {
	const latest = heldFor(records, ref).at(-1);
	return latest ? `${heldVerb(latest.action)} waits for the connection` : null;
}

/** What a project will be once the changes held for it go: a held Pin to
 * top or Unpin sets its favorite, a held Archive or Unarchive its archived
 * state, and neither is set when nothing is held. */
export function heldProjectState(
	records: readonly HeldRecord[],
	key: string,
): { favorite?: boolean; archived?: boolean } {
	const state: { favorite?: boolean; archived?: boolean } = {};
	for (const { action } of records) {
		if (action.kind !== "project" || action.project.key !== key) continue;
		if (action.action === "pin" || action.action === "unpin") state.favorite = action.action === "pin";
		else state.archived = action.action === "archive";
	}
	return state;
}

/** What waits for one session, in the order held. */
export function heldFor(records: readonly HeldRecord[], ref: string): HeldRecord[] {
	return records.filter((record) => heldRef(record.action) === ref);
}

/** The turn a Stop or Shut down pressed on this row saw: its stamp, and
 * whether a turn was running. A session asking a question counts as running
 * one, since the ask sits inside a live turn; an approval keeps the row
 * "active". Otherwise "awaiting" is at rest: the turn ended with the ball in
 * your court (agent/session_state.go). */
export function turnSeen(row: { state: string; turn_ended_at?: string; ask_pending?: boolean }): TurnSeen {
	const asking = row.state === "awaiting" && row.ask_pending === true;
	return { turnEndedAt: row.turn_ended_at ?? null, running: row.state === "active" || asking };
}

/** Whether a held Stop or Shut down still names the turn that runs now
 * (spec 7.5: dropped rather than sent if that turn ended before the
 * connection returned). True only while a turn runs and none has ended
 * since the press. The row's turn_ended_at and the thread's lastTurnEndedAt
 * are one stamp, the session daemon's (S4), so this never reads the phone's
 * clock: both absent means no turn has ended since the daemon began
 * stamping, and the running turn is the one seen. */
export function turnStillSeen(seen: TurnSeen, thread: Pick<EvenerThread, "activeTurnId" | "lastTurnEndedAt">): boolean {
	if (!thread.activeTurnId || !seen.running) return false;
	const seenEnd = seen.turnEndedAt === null ? null : Date.parse(seen.turnEndedAt);
	return (thread.lastTurnEndedAt ?? null) === seenEnd;
}

function isAction(value: unknown): value is HeldAction {
	if (!isPlainObject(value)) return false;
	const text = (field: unknown) => typeof field === "string";
	switch (value.kind) {
		case "archive":
			return (
				text(value.ref) &&
				isPlainObject(value.target) &&
				text(value.target.kind) &&
				text(value.target.id) &&
				typeof value.archived === "boolean"
			);
		case "pin":
			return isPlainObject(value.target) && text(value.target.sessionRef);
		case "project":
			return (
				isPlainObject(value.project) &&
				text(value.project.key) &&
				(value.action === "pin" ||
					value.action === "unpin" ||
					value.action === "archive" ||
					value.action === "unarchive")
			);
		case "rename":
			return text(value.ref) && text(value.title) && text(value.name);
		case "stop":
		case "shutDown":
			return (
				text(value.ref) &&
				text(value.title) &&
				isPlainObject(value.seen) &&
				(value.seen.turnEndedAt === null || text(value.seen.turnEndedAt)) &&
				typeof value.seen.running === "boolean"
			);
		default:
			return false;
	}
}

function parse(value: unknown): HeldRecord[] {
	if (!Array.isArray(value)) return [];
	return value.filter(
		(record): record is HeldRecord =>
			isPlainObject(record) &&
			typeof record.id === "string" &&
			typeof record.heldAt === "number" &&
			isAction(record.action),
	);
}

export class BoardHold {
	private records: readonly HeldRecord[];
	private listeners = new Set<() => void>();
	private forgotten = false;
	private nextId = 0;

	constructor(
		private readonly storage: SyncStringStorage,
		private readonly hubId: string,
	) {
		this.records = parse(readJson(storage, boardHoldKey(hubId)));
	}

	getSnapshot = (): readonly HeldRecord[] => this.records;

	subscribe = (listener: () => void): (() => void) => {
		this.listeners.add(listener);
		return () => {
			this.listeners.delete(listener);
		};
	};

	/** Holds an action, replacing a held one on the same subject in its place,
	 * or dropping both when the new one undoes it. */
	hold(action: HeldAction, now: number): HeldRecord {
		const record: HeldRecord = { id: `${now}-${this.nextId++}`, heldAt: now, action };
		if (this.forgotten) return record;
		const index = this.records.findIndex((held) => subject(held.action) === subject(action));
		const held = this.records[index];
		if (held === undefined) this.change([...this.records, record]);
		else if (undoes(action, held.action)) this.change(this.records.filter((_, at) => at !== index));
		else this.change(this.records.map((each, at) => (at === index ? record : each)));
		return record;
	}

	/** You took it back. */
	cancel(id: string): void {
		this.remove(id);
	}

	/** The hub answered it, or its check confirmed it. */
	settled(id: string): void {
		this.remove(id);
	}

	/** The hub was removed: everything goes, and the store stops writing or
	 * telling anyone, so a replay still answering can't write the key back. */
	forget(): void {
		if (this.forgotten) return;
		this.records = [];
		this.forgotten = true;
		for (const listener of [...this.listeners]) listener();
		this.listeners.clear();
	}

	private remove(id: string): void {
		if (this.forgotten || !this.records.some((record) => record.id === id)) return;
		this.change(this.records.filter((record) => record.id !== id));
	}

	private change(records: readonly HeldRecord[]): void {
		this.records = records;
		writeJson(this.storage, boardHoldKey(this.hubId), records);
		for (const listener of [...this.listeners]) listener();
	}
}
