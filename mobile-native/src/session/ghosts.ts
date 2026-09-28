// The dashed ghost bubbles above the composer (spec 8.5 and 14):
// - steers on their way to the agent's next step;
// - your queued messages, in the order they will send, or held after a Stop
//   parked them;
// - your messages not yet reflected;
// - anything the phone couldn't confirm, or the hub refused.
import { isQueueParked, sessionControls, type ThreadModel } from "@evener/appwire-client";
import { type PendingTurnEntry, pendingEntryPreview } from "@evener/appwire-client/state/mutation";
import type { NativeMutationRecoveryRow } from "../MutationRecoveryPanel";

export type GhostState = "steering" | "queued" | "held" | "sending" | "unconfirmed" | "refused";

export type GhostAction = "steerNow" | "sendNow" | "edit" | "cancel" | "check" | "discard";

export interface QueueEntryRef {
	index: number;
	id: string;
}

export type RecoveryGhostRow = Pick<
	NativeMutationRecoveryRow,
	"clientMutationId" | "status" | "reason" | "text" | "actions"
> &
	Partial<Pick<NativeMutationRecoveryRow, "carriesAttachments">>;

export type GhostOrigin =
	| { kind: "queue"; entry: QueueEntryRef }
	| { kind: "pending"; clientMutationId: string }
	| { kind: "draft" }
	| { kind: "recovery"; row: RecoveryGhostRow };

export interface Ghost {
	key: string;
	state: GhostState;
	text: string;
	caption: string;
	/** The text buttons on the bubble itself. */
	buttons: GhostAction[];
	/** What tapping the bubble offers. */
	menu: GhostAction[];
	/** Why an action you'd expect isn't offered. */
	note?: string;
	origin: GhostOrigin;
}

export type GhostSource = Pick<ThreadModel, "status" | "capabilities" | "queue">;

const CAPTIONS: Record<GhostState, string> = {
	steering: "Steering · arrives at the next step",
	queued: "Queued · sends when this turn ends",
	held: "Held · you stopped this turn",
	sending: "Sending…",
	unconfirmed: "Couldn't confirm this was sent",
	refused: "Couldn't send this",
};

const STEERS = new Set(["steer", "drain", "promote"]);

/** `session` is null until the session first loads: the phone's own
 * unconfirmed send and refused messages still show. */
export function ghosts(
	session: GhostSource | null,
	pending: readonly PendingTurnEntry[] | null | undefined,
	unconfirmedDraft: string | null,
	recovery: readonly RecoveryGhostRow[],
): Ghost[] {
	// Another client's rows aren't yours to watch. A row Stop canceled before
	// it left the phone waits for phase 6's retry (ruling 3).
	const own = (pending ?? []).filter((entry) => entry.fromThisClient && entry.state !== "canceled");
	const steering = (entry: PendingTurnEntry) =>
		STEERS.has(entry.method) && (entry.state === "accepted" || entry.state === "claimed");
	const out: Ghost[] = own.filter(steering).map((entry) => pendingGhost(entry, "steering", []));
	if (session) out.push(...queueGhosts(session));
	for (const entry of own) {
		if (steering(entry)) continue;
		out.push(
			entry.state === "blockedUnknown"
				? pendingGhost(entry, "unconfirmed", ["check"])
				: pendingGhost(entry, "sending", []),
		);
	}
	if (unconfirmedDraft !== null)
		out.push({
			key: "draft:unconfirmed",
			state: "unconfirmed",
			text: unconfirmedDraft,
			caption: CAPTIONS.unconfirmed,
			buttons: ["check", "discard"],
			menu: ["edit"],
			origin: { kind: "draft" },
		});
	for (const row of recovery) out.push(recoveryGhost(row));
	return out;
}

function queueGhosts(session: GhostSource): Ghost[] {
	const queue = session.queue;
	const depth = queue?.depth ?? 0;
	const held = isQueueParked(session.status.type, depth);
	const canDrain = sessionControls(session.status.type, session.capabilities, depth).drain;
	const state: GhostState = held ? "held" : "queued";
	return Array.from({ length: depth }, (_, index): Ghost => {
		const id = queue?.ids?.[index];
		const fullText = queue?.texts?.[index] ?? "";
		const text = fullText || queue?.preview?.[index] || "Queued message";
		const buttons: GhostAction[] = held ? (canDrain ? ["sendNow", "cancel"] : ["cancel"]) : canDrain ? ["steerNow"] : [];
		// Edit restores text only, so an image-only message has nothing to edit.
		const menu: GhostAction[] = fullText.trim() ? ["edit", "cancel"] : ["cancel"];
		return {
			key: `queue:${id ?? `index-${index}`}`,
			state,
			text,
			caption: CAPTIONS[state],
			buttons: id ? buttons : [],
			menu: id ? menu : [],
			origin: { kind: "queue", entry: { index, id: id ?? "" } },
		};
	});
}

function pendingGhost(entry: PendingTurnEntry, state: GhostState, buttons: GhostAction[]): Ghost {
	return {
		key: `pending:${entry.id}`,
		state,
		text: pendingEntryPreview(entry) || "Message",
		caption: CAPTIONS[state],
		buttons,
		menu: [],
		origin: { kind: "pending", clientMutationId: entry.id },
	};
}

function recoveryGhost(row: RecoveryGhostRow): Ghost {
	const refused = row.status === "rejected";
	const canEdit = row.actions.includes("restore");
	const buttons: GhostAction[] = refused ? (canEdit ? ["edit", "discard"] : ["discard"]) : ["check", "discard"];
	return {
		key: `recovery:${row.clientMutationId}`,
		state: refused ? "refused" : "unconfirmed",
		text: row.text || "Message",
		caption: refused
			? row.reason
				? `${CAPTIONS.refused} · ${row.reason}`
				: CAPTIONS.refused
			: CAPTIONS.unconfirmed,
		buttons,
		menu: !refused && canEdit ? ["edit"] : [],
		// Edit brings back text only, so a refused message with an image offers
		// just Discard, and says why.
		...(refused && !canEdit && row.carriesAttachments
			? { note: "This message carried an image, so it can't be restored to the draft here." }
			: {}),
		origin: { kind: "recovery", row },
	};
}

/** The queue entry a ghost's button acts on, re-checked against the live queue
 * at the press: the same message at its current place, or null once it has
 * left the queue. A press never acts on whatever now sits at the index the
 * ghost rendered with (Review Focus 2). */
export function ghostActionTarget(queue: ThreadModel["queue"], entry: QueueEntryRef): QueueEntryRef | null {
	if (!entry.id) return null;
	const ids = queue?.ids ?? [];
	if (ids[entry.index] === entry.id) return entry;
	const index = ids.indexOf(entry.id);
	return index === -1 ? null : { index, id: entry.id };
}

/** At most three queued messages show above the composer (ruling 18); the
 * rest are counted, and open the Queue sheet. Every other ghost shows. */
export const SHOWN_QUEUED = 3;

export function shownGhosts(all: readonly Ghost[]): { shown: Ghost[]; moreQueued: number } {
	let queued = 0;
	const shown: Ghost[] = [];
	for (const ghost of all) {
		if (ghost.origin.kind === "queue") {
			queued += 1;
			if (queued > SHOWN_QUEUED) continue;
		}
		shown.push(ghost);
	}
	return { shown, moreQueued: Math.max(0, queued - SHOWN_QUEUED) };
}
