// What the Board sends when the connection returns (phase 6 ruling 18): the
// actions held while offline (boardHold.ts), in the order held, one at a
// time, in two streams.
// - At once, wherever you are: Stop, Shut down and Rename. A Stop never
//   waits behind an archive.
// - Through the Board's one organization journal, once the Board is focused
//   and the journal is free: archive, pin and the project changes (the
//   journal holds one change per hub, and a second writer would break it,
//   phase 2 part 3's ruling 16).
// Each stream reads the hold again before each send, so a hold forgotten
// with its hub ends the replay. A record goes once its request is answered
// or its check confirms it; one the connection loses stays for the next
// ready connection.
import type { AppwireClientLike, EvenerThread } from "@evener/appwire-client";
import type { NavigationActions } from "../navigationActions";
import type { BoardHold, HeldAction, HeldRecord } from "./boardHold";
import { turnStillSeen } from "./boardHold";
import { type StopOutcome, stopToast } from "./boardStops";
import {
	type JournalOutcome,
	journalOutcome,
	projectRequest,
	RENAMED,
	renameFailed,
	renameSession,
	SHUT_DOWN_DONE,
	shutDownFailed,
	shutDownSession,
} from "./rowActions";
import { organizationOpen } from "./organizationCheck";
import type { BoardOrganization } from "./useBoardOrganization";

export interface ReplayDeps {
	/** The Board's Stop (BoardStops.stop), with a held Stop's guard. */
	stop(client: AppwireClientLike, ref: string, guard: (thread: EvenerThread) => boolean): Promise<StopOutcome>;
	toast(text: string): void;
}

const AT_ONCE: ReadonlySet<HeldAction["kind"]> = new Set(["stop", "shutDown", "rename"]);

/** A held Shut down that finds a newer turn running than the one you saw. */
export const SHUT_DOWN_DROPPED = "A newer turn started, so the session wasn't shut down";

export class BoardReplay {
	#sending = new NewestAsk<[AppwireClientLike, () => boolean]>((client, isLive) => this.#sendAll(client, isLive));
	#organizing = new NewestAsk<[BoardOrganization, () => boolean]>((organization, isLive) =>
		this.#organizeAll(organization, isLive),
	);
	#disposed = false;

	constructor(
		private readonly hold: BoardHold,
		private readonly deps: ReplayDeps,
	) {}

	/** The Board went away or changed hub: a stream running ends at its next
	 * step. */
	dispose(): void {
		this.#disposed = true;
	}

	/** On a ready connection: Stop, Shut down and Rename, in the order held.
	 * `isLive` says whether the connection still is; a request that fails once
	 * it isn't stays held. */
	sendImmediate(client: AppwireClientLike, isLive: () => boolean): Promise<void> {
		return this.#sending.ask(client, isLive);
	}

	async #sendAll(client: AppwireClientLike, isLive: () => boolean): Promise<void> {
		for (;;) {
			if (this.#disposed || !isLive()) return;
			const record = this.hold.getSnapshot().find((held) => AT_ONCE.has(held.action.kind));
			// One another replay is sending (a Board unmounted mid-send) is that
			// replay's: its settling runs this stream again.
			if (!record || this.hold.isSending(record.id) || !(await this.#sendAtOnce(client, record, isLive))) return;
		}
	}

	/** With the Board focused and the journal free: archive, pin and the
	 * project changes, each confirmed by the journal before the next.
	 * `isLive` says whether the connection still is, as sendImmediate's. */
	organize(organization: BoardOrganization, isLive: () => boolean): Promise<void> {
		return this.#organizing.ask(organization, isLive);
	}

	async #organizeAll(organization: BoardOrganization, isLive: () => boolean): Promise<void> {
		for (;;) {
			const actions = organization.actions;
			if (this.#disposed || !actions || !isLive() || !organizationOpen(organization)) return;
			const record = this.hold.getSnapshot().find((held) => !AT_ONCE.has(held.action.kind));
			if (!record || this.hold.isSending(record.id)) return;
			this.hold.claim(record.id);
			const outcome = await organizationChange(actions, record.action);
			// Not taken, or the Board went away or the connection dropped: it
			// stays held, and goes when the journal is free again.
			if (outcome === "notTaken" || !organization.isCurrent() || !isLive()) {
				this.hold.release(record.id);
				return;
			}
			// Its outcome unknown: the journal holds it and settles it, as it
			// does an online change.
			// A settling that fails leaves the journal holding it, as online.
			if (outcome === "unconfirmed") void actions.reconcile().catch(() => undefined);
			this.hold.settled(record.id);
		}
	}

	/** One of the at-once actions; false when the connection lost it. */
	async #sendAtOnce(client: AppwireClientLike, record: HeldRecord, isLive: () => boolean): Promise<boolean> {
		const { action } = record;
		if (action.kind !== "stop" && action.kind !== "shutDown" && action.kind !== "rename") return true;
		const { toast } = this.deps;
		// A failure while the connection held is the hub's answer; one after it
		// dropped is unknown, even if the client is ready again by then.
		let dropped = false;
		const unwatch = client.onStateChange((state) => {
			if (state !== "ready") dropped = true;
		});
		const lost = () => dropped || !isLive();
		this.hold.claim(record.id);
		try {
			if (action.kind === "stop") {
				const outcome = await this.deps.stop(client, action.ref, (thread) => turnStillSeen(action.seen, thread));
				// Anything short of an admitted interrupt, decided over a connection
				// that dropped since, is unknown: it stays held.
				if (outcome !== "stopped" && lost()) return this.#keep(record);
				toast(stopToast(outcome, action.title));
			} else if (action.kind === "shutDown") {
				const { thread } = await client.request("thread/read", { ref: action.ref, includeTurns: false });
				// Read over a connection that dropped since, it may be stale.
				if (lost()) return this.#keep(record);
				// Dropped only when a turn runs that isn't the one you saw: with
				// none running, shutting down stops nothing you didn't see.
				if (thread.evener.activeTurnId && !turnStillSeen(action.seen, thread.evener)) toast(SHUT_DOWN_DROPPED);
				else {
					await shutDownSession(client, action.ref);
					toast(SHUT_DOWN_DONE);
				}
			} else if (action.kind === "rename" && (await renameSession(client, action.ref, action.name))) toast(RENAMED);
		} catch (error) {
			if (lost()) return this.#keep(record);
			if (action.kind === "stop") toast(stopToast("unavailable", action.title));
			else toast(action.kind === "rename" ? renameFailed(action.title, error) : shutDownFailed(action.title, error));
		} finally {
			unwatch();
		}
		this.hold.settled(record.id);
		return true;
	}

	/** A send the connection lost: it waits for the next ready one. */
	#keep(record: HeldRecord): false {
		this.hold.release(record.id);
		return false;
	}
}

/** One stream, run one at a time with the newest ask: an ask that arrives
 * while the stream runs waits, replacing any older waiting one, and runs when
 * it finishes, so a new connection's replay is never lost behind the old
 * one's last step. */
class NewestAsk<Args extends unknown[]> {
	#waiting: Args | null = null;
	/** The running stream, which every ask made while it runs waits on: it
	 * ends only once nothing waits. */
	#running: Promise<void> | null = null;

	constructor(private readonly run: (...args: Args) => Promise<void>) {}

	ask(...args: Args): Promise<void> {
		this.#waiting = args;
		this.#running ??= this.#drain();
		return this.#running;
	}

	async #drain(): Promise<void> {
		try {
			while (this.#waiting) {
				const next = this.#waiting;
				this.#waiting = null;
				await this.run(...next);
			}
		} finally {
			// Cleared as the loop ends, in the same step, so an ask arriving
			// after it starts a new stream rather than joining a finished one.
			this.#running = null;
		}
	}
}

/** One organization change through the journal. A throw means it never
 * reached the journal, whose run() settles its own failures. */
async function organizationChange(actions: NavigationActions, action: HeldAction): Promise<JournalOutcome> {
	try {
		return await journalOutcome(actions, () => organizationRequest(actions, action));
	} catch {
		return "notTaken";
	}
}

function organizationRequest(actions: NavigationActions, action: HeldAction): Promise<void> {
	if (action.kind === "archive") return actions.archive(action.target, action.archived);
	if (action.kind === "pin") return actions.assignPin(action.target);
	if (action.kind === "project") return projectRequest(actions, action.project, action.action);
	return Promise.resolve();
}
