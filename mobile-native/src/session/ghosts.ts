// The dashed ghost bubbles at the transcript's end, just above the composer
// (spec 8.5 and 14):
// - steers on their way to the agent's next step;
// - your queued messages, in the order they will send, or held after a Stop
//   parked them;
// - your messages not yet reflected;
// - anything the phone couldn't confirm, or the hub refused.
import { isQueueParked, sessionControls, type ThreadModel } from "@evener/appwire-client";
import { normalizeText, type PendingTurnEntry, pendingEntryPreview } from "@evener/appwire-client/state/mutation";
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
	Partial<Pick<NativeMutationRecoveryRow, "carriesAttachments" | "queuedText">>;

export type GhostOrigin =
	| { kind: "queue"; entry: QueueEntryRef }
	| { kind: "pending"; clientMutationId: string }
	// A draft that stands in for a matched outbox row carries that row's id, so
	// its Discard can clear both (the draft's uncertainty and the row).
	| { kind: "draft"; clientMutationId?: string }
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

/** A send the draft holds as uncertain: its text as you wrote it, and as it
 * went out, with image markers translated the way the outbox records it. */
export interface UnconfirmedSend {
	text: string;
	sentText: string;
}

const CAPTIONS: Record<GhostState, string> = {
	steering: "Steering · arrives at the next step",
	queued: "Queued · sends when this turn ends",
	held: "Held · you stopped this turn",
	sending: "Sending…",
	unconfirmed: "Couldn't confirm this was sent",
	refused: "Couldn't send this",
};

const STEER_REFUSED = "Couldn't steer with this";

/** A message the phone holds until it can send it (ruling 14): nothing is
 * sending while the connection is down. */
const WAITING_TO_SEND = "Will send when you're back online";

const STEERS = new Set(["steer", "drain", "promote"]);

/** `session` is null until the session first loads: the phone's own
 * unconfirmed send and refused messages still show. `connected` is whether
 * the hub is reachable now: Check needs it, Discard never does (spec
 * principle 2: a control shows only when it can act), and a message this
 * phone still holds says it waits for the connection. */
export function ghosts(
	session: GhostSource | null,
	pending: readonly PendingTurnEntry[] | null | undefined,
	unconfirmedDraft: UnconfirmedSend | null,
	recovery: readonly RecoveryGhostRow[],
	connected: boolean,
): Ghost[] {
	// Another client's rows aren't yours to watch.
	const own = (pending ?? []).filter((entry) => entry.fromThisClient);
	const confirmButtons: GhostAction[] = connected ? ["check", "discard"] : ["discard"];
	const steering = (entry: PendingTurnEntry) =>
		STEERS.has(entry.method) && (entry.state === "accepted" || entry.state === "claimed");
	const out: Ghost[] = own.filter(steering).map((entry) => pendingGhost(entry, "steering", []));
	// A queued message this phone is steering with shows once, as its steer.
	const promoted = new Set(own.flatMap((entry) => entry.queueEntryId ?? []));
	if (session)
		out.push(
			...queueGhosts(session).filter(
				(ghost) => !(ghost.origin.kind === "queue" && promoted.has(ghost.origin.entry.id)),
			),
		);
	// The draft keeps a send uncertain until the store confirms it, and the
	// outbox admits the same send first, so a binding change in between or a
	// crash leaves both holding it. One ghost shows: the outbox knows how far
	// the send got, and the draft keeps what you can do about it. The draft's
	// uncertainty is about its latest send, and the store orders pending rows
	// by when they were admitted (reconcilePendingEntries), so the last match
	// is the one: an earlier send of the same words keeps its own ghost.
	const sameSend =
		unconfirmedDraft === null
			? undefined
			: own.findLast(
					(entry) =>
						(entry.method === "send" || entry.method === "queue") &&
						// A message a Stop held never left the phone: it shows as its
						// own held ghost, never as the draft's send in flight.
						entry.state !== "canceled" &&
						normalizeText(entry.text) === normalizeText(unconfirmedDraft.sentText),
				);
	for (const entry of own) {
		if (steering(entry) || entry === sameSend) continue;
		// A message a Stop held before it left the phone comes back as held,
		// with Send now and Cancel, like a queue a Stop parks (spec 8.5).
		if (entry.state === "canceled") out.push(pendingGhost(entry, "held", ["sendNow", "cancel"]));
		else if (entry.state === "blockedUnknown") out.push(pendingGhost(entry, "unconfirmed", confirmButtons));
		else {
			const ghost = pendingGhost(entry, "sending", []);
			out.push(connected ? ghost : { ...ghost, caption: WAITING_TO_SEND });
		}
	}
	if (unconfirmedDraft !== null) {
		// While the outbox still carries the send, nothing is yours to do yet,
		// as with any send in flight.
		const sending = sameSend !== undefined && sameSend.state !== "blockedUnknown";
		const state: GhostState = sending ? "sending" : "unconfirmed";
		out.push({
			key: "draft:unconfirmed",
			state,
			text: unconfirmedDraft.text,
			caption: sending && !connected ? WAITING_TO_SEND : CAPTIONS[state],
			buttons: sending ? [] : confirmButtons,
			menu: sending ? [] : ["edit"],
			// The outbox row this ghost stands in for, when there is one: Discard
			// must clear both, or the row returns as its own ghost.
			origin: sameSend === undefined ? { kind: "draft" } : { kind: "draft", clientMutationId: sameSend.id },
		});
	}
	for (const row of recovery) out.push(recoveryGhost(row, confirmButtons));
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
		const buttons: GhostAction[] = held
			? canDrain
				? ["sendNow", "cancel"]
				: ["cancel"]
			: canDrain
				? ["steerNow"]
				: [];
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

function recoveryGhost(row: RecoveryGhostRow, confirmButtons: GhostAction[]): Ghost {
	const refused = row.status === "rejected";
	const canEdit = row.actions.includes("restore");
	const buttons: GhostAction[] = refused ? (canEdit ? ["edit", "discard"] : ["discard"]) : confirmButtons;
	const refusal = row.queuedText === undefined ? CAPTIONS.refused : STEER_REFUSED;
	return {
		key: `recovery:${row.clientMutationId}`,
		state: refused ? "refused" : "unconfirmed",
		text: row.queuedText || row.text || "Message",
		caption: refused ? (row.reason ? `${refusal} · ${row.reason}` : refusal) : CAPTIONS.unconfirmed,
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

/** The ghosts with only the actions that can act right now. A queued
 * message's actions go to the hub, so none show while it's away; the message
 * still does. Its Edit writes into the composer, so it waits for the composer
 * to load. Everything else acts on this phone. */
export function whatCanActNow(
	all: readonly Ghost[],
	{ connected, composerLoaded }: { connected: boolean; composerLoaded: boolean },
): Ghost[] {
	return all.map((ghost) => {
		if (ghost.origin.kind !== "queue") return ghost;
		if (!connected) return { ...ghost, buttons: [], menu: [] };
		if (composerLoaded) return ghost;
		const available = (action: GhostAction) => action !== "edit";
		return { ...ghost, buttons: ghost.buttons.filter(available), menu: ghost.menu.filter(available) };
	});
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

/** At most three queued messages show at the transcript's end (ruling 18); the
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
