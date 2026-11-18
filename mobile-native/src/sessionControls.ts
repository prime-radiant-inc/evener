import { useSyncExternalStore } from "react";
import { WireError } from "@evener/appwire-client";
import type { ModelListResponse } from "@evener/appwire-client";
import { effortOptionLevels, sessionEffortLevels } from "@evener/appwire-client";
import type { MobileConversation } from "./projectedRows";
import type {
	ConversationModelCatalog,
	ConversationRecoveryActions,
	ConversationService,
} from "./conversation/services/conversation";

type Operation =
	| "rename"
	| "compact"
	| "shutdown"
	| "forceStop"
	| "resume"
	| "setReasoningEffort"
	| "setVisionModel"
	| "changeModel";
type Setting = "model" | "vision";

/** What a setting's last write established and no publication has confirmed.
 *
 * One entry per setting, because the two are independent facts: a model pick
 * says nothing about whether the vision value the hub holds is the one on
 * screen, and a single record would have to throw one away to keep the other.
 *
 * The two outcomes are not the same fact and must not be stored as one. An
 * acknowledged value is one the hub named: the projection may not show it yet,
 * but the hub holds it. An unknown outcome is a write whose reply never came
 * back - the hub may hold its value, the previous one, or nothing applied at
 * all - so the projection cannot answer anything about that setting until a
 * read lands.
 */
export type UnreadValue =
	/** The hub took this value; no publication has shown it yet. */
	| { state: "acknowledged"; value: string }
	/** A write was sent and its reply was lost, so the hub's value is not known. */
	| { state: "unknown" };

/** The value each setting was last written for and no publication has
 * established yet. Null means nothing is outstanding, so the projection's own
 * value is the answer.
 */
export interface Unread {
	model: UnreadValue | null;
	vision: UnreadValue | null;
}

/** Whether a row showing `value` may dismiss quietly.
 *
 * A match against the projection is only "already set" when the projection is
 * the truth: nothing outstanding, or an acknowledged value the projection
 * agrees with. A projection that does not agree with an acknowledged value is
 * one change behind, so tapping what it shows is a correction and must be
 * sent. An unknown outcome has no answer to compare: the projection may show
 * either value, and dismissing on a coincidence is how a change the reader
 * meant to make disappears.
 */
export function verifiedSetting(unread: Unread, setting: Setting, value: string): boolean {
	const record = unread[setting];
	return record === null || (record.state === "acknowledged" && record.value === value);
}

/** Whether a failed write's outcome is unknown rather than refused.
 *
 * A refusal is the hub's answer: it names what it would not do, so the session
 * still holds the value the projection shows. A reply that never arrived is not
 * an answer, and it covers two shapes. The transport can reject without the hub
 * ever speaking - a request deadline, a socket that closed mid-call - which is
 * any error that is not a wire rejection. The hub can also answer that it lost
 * its daemon's reply to a write it had already forwarded (sessionUnavailable),
 * which is its own way of saying the write may have applied.
 */
function isLostReply(cause: unknown): boolean {
	if (!(cause instanceof WireError)) return true;
	return cause.evenerErrorInfo === "sessionUnavailable";
}

/** The facts about one session that outlive any one binding of it.
 *
 * A controller is built per binding and thrown away when the screen rebinds -
 * a restart, a reconnect, a cold pick that replaces the runtime. Anything a
 * pick's answer has to reach afterwards cannot live on the instance that sent
 * it, or the replacement starts believing a projection that has not caught up.
 */
export interface ControlsSession {
	/** Whether the session's store has published an authoritative projection.
	 * `refresh` resolving does not answer that: a read a newer one supersedes is
	 * discarded without throwing, and a discarded read leaves the projection
	 * showing the value from before it. */
	published(): boolean;
	/** What the published projection says `setting` holds, or null while its
	 * store is not open. This is the daemon's report, and it is a different fact
	 * from the daemon's acceptance of a write: a pick for the next turn is
	 * acknowledged while the projection correctly still names the one running,
	 * so a publication alone does not establish a write. Absent on a record with
	 * no store to ask, where a publication is the only answer there is. */
	projected?(setting: Setting): string | null;
	/** The values written for and not yet established by a publication. */
	unread(): Unread;
	/** Records one setting's outcome, or clears it once a publication catches up.
	 * `null` clears that setting and leaves the other alone. */
	record(setting: Setting, outcome: UnreadValue | null): void;
	/** Retires every outcome an authoritative publication has established, and
	 * only those: an outcome recorded while a read was already out survives that
	 * read's answer, because the read may have sampled the session before the
	 * write it cannot confirm. */
	retirePublished(): void;
	/** Notifies when the record changes. A set can settle after the controls
	 * that dispatched it were replaced, so the record has to reach a binding
	 * that already exists and not only one built afterwards. */
	watch(listener: () => void): () => void;
}

/** The session facts for a controller whose caller has no store to ask: it has
 * published everything, and keeps its own record. This is what a read that
 * returns normally always meant before the store was asked. */
function detachedSession(): ControlsSession {
	const facts = sessionFacts(() => true);
	return facts;
}

/** The session's shared facts, built by the entry that owns the session and
 * read by every view and binding of it.
 *
 * The record lives on the session entry, not on a view, because a write
 * invalidates the session's projection rather than one view's copy of it: two
 * mounted views of the same session read one projection, so a per-view record
 * would let one dismiss a correction the other knew was outstanding.
 */
export function sessionFacts(
	published: () => boolean,
	readsStarted: () => number = () => 0,
	// What the store shows for each setting. A record built without one has no
	// projection to compare against - its caller never had a store - so there a
	// publication is the answer, which is what every such caller meant before
	// the session entry started supplying one.
	projected?: (setting: Setting) => string | null,
): ControlsSession {
	let unread: Unread = { model: null, vision: null };
	// The read count each setting's outcome was recorded at. An outcome says the
	// projection cannot answer for that setting; only a read begun after it was
	// recorded can have observed the write it cannot confirm. Retiring on any
	// publication instead would let a read that was already out - one that may
	// have sampled the session before the write - clear the uncertainty, which
	// is the same stale-match bug one layer up.
	let recordedAt: Record<Setting, number> = { model: 0, vision: 0 };
	const listeners = new Set<() => void>();
	const changed = () => {
		for (const listener of listeners) listener();
	};
	const facts: ControlsSession = {
		published,
		...(projected === undefined ? {} : { projected }),
		unread: () => unread,
		record: (setting, outcome) => {
			// Every record is a write, including one that repeats the previous
			// outcome. Two vision writes can both lose their replies with a read
			// starting between them, and the second one matching the first is not
			// evidence they are the same write: keeping the first one's fence would
			// let that intervening read - which may have sampled the session before
			// the second write applied - retire both uncertainties at once. So the
			// fence moves on every record, and only the announcement waits for an
			// outcome the reader can see change.
			recordedAt = { ...recordedAt, [setting]: readsStarted() };
			const current = unread[setting];
			const same =
				current === null
					? outcome === null
					: outcome !== null &&
						current.state === outcome.state &&
						(current.state === "unknown" || (outcome.state === "acknowledged" && current.value === outcome.value));
			if (same) return;
			unread = { ...unread, [setting]: outcome };
			changed();
		},
		retirePublished: () => {
			const started = readsStarted();
			const kept: Setting[] = [];
			let retired = false;
			for (const setting of ["model", "vision"] as const) {
				const outcome = unread[setting];
				if (outcome === null) continue;
				// A record with no store behind it has no read count to fence on
				// and no projection to compare with; a publication is the whole of
				// the answer there, as it always was for such a caller.
				if (projected === undefined) {
					retired = true;
					continue;
				}
				// A read begun after the write answers it only in combination with
				// what it published. An acknowledged value is established when the
				// projection names it: a pick the daemon accepted for the next turn
				// leaves the projection naming the running model, and that report is
				// correct and says nothing about the queued change. An unknown is
				// established by any newer publication, which reports what the hub
				// actually holds whichever way the lost write went.
				const shown = projected(setting);
				const answered =
					recordedAt[setting] < started && (outcome.state === "unknown" || (shown !== null && shown === outcome.value));
				if (answered) retired = true;
				else if (unread[setting] !== null) kept.push(setting);
			}
			if (!retired) return;
			unread = {
				model: kept.includes("model") ? unread.model : null,
				vision: kept.includes("vision") ? unread.vision : null,
			};
			changed();
		},
		watch: (listener) => {
			listeners.add(listener);
			return () => {
				listeners.delete(listener);
			};
		},
	};
	return facts;
}

export interface ControlsState {
	pending: Operation | null;
	lastAction: Operation | null;
	error: string | null;
	notice: string | null;
	catalog: ModelListResponse | null;
	loadingModels: boolean;
	modelError: string | null;
	/** The value each setting was written for and no publication has
	 * established yet. The session's projection still shows the value from
	 * before that write, and a picker may not read a match against it as
	 * "already set": that matches the stale value, not the one the hub took.
	 * Cleared per setting by a publication that lands, so the comparison is
	 * trusted again as soon as the projection catches up.
	 *
	 * It is the session's fact, not this binding's: the write can settle after
	 * the controls that sent it were replaced, so the screen keeps one record
	 * for the session and every binding reads it. */
	unread: Unread;
}

/** Own actions for one conversation binding, independently of its sheet. */
export class SessionControls {
	private state: ControlsState = {
		pending: null,
		lastAction: null,
		error: null,
		notice: null,
		catalog: null,
		loadingModels: false,
		modelError: null,
		unread: { model: null, vision: null },
	};
	private listeners = new Set<() => void>();
	private disposed = false;
	// The record can change after this binding was built, so the session's
	// record is a subscription rather than a value sampled in the constructor.
	private unwatch: (() => void) | null = null;
	// Counts catalog reads (a refresh or a load), so a refresh publishes only
	// when no newer read has started.
	private catalogReads = 0;
	// An announcement arrived while a load was out, to be read once it settles.
	private refreshAfterLoad = false;
	constructor(
		private service: Pick<ConversationService, Exclude<Operation, "forceStop" | "resume">> &
			ConversationRecoveryActions &
			ConversationModelCatalog,
		private refresh: () => Promise<void>,
		private stopped: () => void,
		private isCurrent: (scope?: "destination" | "route" | "send" | "session") => boolean,
		private getReasoning: () => Pick<
			MobileConversation,
			"supportsReasoning" | "reasoningEffort" | "reasoningEffortLevels"
		> | null,
		private canMutate: () => boolean,
		// The catalog the screen already knows, from its last binding: the
		// model's name needs no read to show.
		catalog: ModelListResponse | null = null,
		// The facts about the session that outlive one binding: what its store
		// has published, and what a pick was acknowledged for and never read
		// back. Both belong to the session rather than to this instance - a cold
		// pick can replace the runtime, and the answer to the pick lands on the
		// set that is being retired.
		private session: ControlsSession = detachedSession(),
	) {
		this.state = { ...this.state, catalog, unread: session.unread() };
		// A set this binding dispatched can settle after it is superseded, or
		// before it was built: either way the record changes underneath it and
		// the picker reading this snapshot has to hear about it.
		this.unwatch = session.watch(() => {
			if (!this.disposed) this.publish({ unread: session.unread() });
		});
	}
	getSnapshot = () => this.state;
	subscribe = (listener: () => void) => {
		this.listeners.add(listener);
		return () => {
			this.listeners.delete(listener);
		};
	};
	private publish(state: Partial<ControlsState>) {
		this.state = { ...this.state, ...state };
		for (const listener of this.listeners) listener();
	}
	/** Whether the action ends here without a report: this controller was
	 * disposed, or its screen is no longer the one in front. Settlement is not
	 * visibility, so an ending that leaves the same session's controls live
	 * still clears their in-flight state, and a failure keeps its reason for
	 * the sheet mounted under the covering screen: a pending state left behind
	 * shows every picker row disabled when the reader returns. Only the notice
	 * and the return value follow the route, since announcing a change applied
	 * to a reader who has left is what that fence is for. A binding that moved
	 * to another conversation publishes nothing at all, which is why this asks
	 * the session's identity rather than the disposed flag alone. */
	/** Whether this binding has nothing left to settle. Another conversation
	 * taking the session ends it; a pane another screen merely covers does not,
	 * because the binding is still that session's and the reply still belongs
	 * to it. A failure keeps its report for the reader who comes back, and
	 * returns before a notice would overwrite it. */
	private ends(error: string | null = null, kind: Operation | null = null): boolean {
		if (this.disposed) {
			// A disposed controller has no listeners, so nothing is told. Its own
			// record still has to say what the hub answered: the step that asked
			// this controller reads the outcome from here, and a rejection left
			// unrecorded reads as an acknowledgement, which is how a refused stop
			// used to be followed by a resume.
			this.state = { ...this.state, pending: null, ...(error === null ? {} : { error }) };
			return true;
		}
		if (this.isCurrent()) return false;
		if (error === null) return false;
		if (this.isCurrent("session")) {
			this.publish({ pending: null, error });
			return true;
		}
		// A binding that moved to another conversation tells nobody: its
		// listeners belong to the session it now shows. A stop is the exception,
		// because the restart step reads the outcome back off the object that
		// sent it rather than off a screen, and a refusal kept out of that record
		// reads as an acknowledged stop - which is how a refused stop came to be
		// followed by a resume. Every other action's outcome belongs to the
		// screen, and a screen that has moved on is not owed it.
		if (kind === "shutdown" || kind === "forceStop") {
			this.state = { ...this.state, pending: null, error };
		}
		return true;
	}
	dispose() {
		this.disposed = true;
		this.listeners.clear();
		this.unwatch?.();
		this.unwatch = null;
	}
	rename(name: string) {
		const trimmed = name.trim();
		if (!trimmed) return Promise.resolve();
		return this.run("rename", () => this.service.rename(trimmed));
	}
	compact() {
		return this.run("compact", () => this.service.compact());
	}
	shutdown() {
		return this.run("shutdown", () => this.service.shutdown());
	}
	forceStop() {
		return this.run("forceStop", () => this.service.forceStop());
	}
	resume() {
		return this.run("resume", () => this.service.resume());
	}
	setReasoningEffort(effort: string) {
		const current = this.getReasoning();
		if (!current) return Promise.resolve();
		const levels = sessionEffortLevels(current.reasoningEffortLevels, current.supportsReasoning);
		const selected = current.reasoningEffort ?? "";
		if (!levels.length || !effortOptionLevels(levels, selected).includes(effort) || selected === effort)
			return Promise.resolve();
		return this.run("setReasoningEffort", () => this.service.setReasoningEffort(effort));
	}
	setVisionModel(visionModel: string) {
		return this.run("setVisionModel", () => this.service.setVisionModel(visionModel), visionModel);
	}
	/** Reads the model catalog. The catalog it has stays until the new one
	 * lands, so a model's name never falls back to its id mid-read; a failed
	 * read clears it, so the picker offers no stale choices. A read the
	 * binding moved on from still ends the load, so the next one reads again
	 * rather than waiting on it forever. */
	async loadModels() {
		// A catalog read is the read the picker is made of, not an action the
		// reader starts and not a thing anyone is told: it belongs to the live
		// session and a ready transport. A pane another screen covers has to have
		// its choices ready when the reader comes back, and a reply from a
		// replaced binding is the one thing that must be dropped.
		if (this.disposed || !this.isCurrent("destination") || this.state.loadingModels || this.state.pending) return;
		// A load is the newest read: a refresh still out answers older.
		this.catalogReads++;
		this.publish({ loadingModels: true, modelError: null });
		try {
			const catalog = await this.service.models();
			if (this.disposed) return;
			this.publish(this.isCurrent("destination") ? { catalog, loadingModels: false } : { loadingModels: false });
		} catch (error) {
			if (this.disposed) return;
			this.publish(
				this.isCurrent("destination")
					? {
							catalog: null,
							loadingModels: false,
							modelError: error instanceof Error ? error.message : "Could not load models.",
						}
					: { loadingModels: false },
			);
		}
		if (this.refreshAfterLoad) {
			this.refreshAfterLoad = false;
			void this.refreshModels();
		}
	}
	/** Reads a loaded catalog again after the hub announced a refreshed list
	 * (evener/auth/updated), replacing it in place. Unlike a load, a failed
	 * read keeps the catalog: the list the hub last served still stands. With
	 * no catalog loaded there is nothing on screen to refresh. A load in
	 * flight may have read the list before the hub refreshed it, so an
	 * announcement during one reads again once it settles. Each announcement
	 * reads again, and the newest read's catalog is the one that stays. */
	async refreshModels() {
		if (this.state.loadingModels) {
			this.refreshAfterLoad = true;
			return;
		}
		if (this.disposed || !this.isCurrent("destination") || !this.state.catalog) return;
		const read = ++this.catalogReads;
		try {
			const catalog = await this.service.models();
			if (!this.disposed && read === this.catalogReads && this.isCurrent("destination")) this.publish({ catalog });
		} catch {
			// A failed refresh keeps the catalog on screen.
		}
	}
	changeModel(provider: string, model: string): Promise<boolean> {
		if (!this.state.catalog?.data.some((entry) => entry.provider === provider && entry.model === model))
			return Promise.resolve(false);
		return this.run("changeModel", () => this.service.changeModel(provider, model), `${provider}/${model}`);
	}
	changeVisionModel(provider: string, model: string): Promise<boolean> {
		if (
			!this.state.catalog?.data.some(
				(entry) => entry.provider === provider && entry.model === model && entry.supportsVision !== false,
			)
		)
			return Promise.resolve(false);
		return this.run(
			"setVisionModel",
			() => this.service.setVisionModel(`${provider}/${model}`),
			`${provider}/${model}`,
		);
	}
	private async run(kind: Operation, operation: () => Promise<void>, value = ""): Promise<boolean> {
		// A resume is the second half of a restart the reader already asked for,
		// so it is admitted on its binding even when another screen covers the
		// pane: refusing it there leaves the daemon stopped with nothing left to
		// retry it. Every other action is the reader's to start, so it keeps the
		// route fence.
		//
		// The resume needs a binding that can actually send. A ready transport is
		// not enough: a reconnect restores the client before the replacement
		// service has opened its thread, and a resume written into that gap throws
		// without reaching the hub. Marking it pending there is what makes the
		// restart call it a failure and stop for good. It refuses before dispatch
		// instead, so the retry survives until the binding is ready.
		const owed = kind === "resume" ? this.isCurrent("send") : this.isCurrent();
		if (
			this.disposed ||
			!owed ||
			(kind !== "forceStop" && kind !== "resume" && !this.canMutate()) ||
			this.state.pending
		)
			return false;
		this.publish({
			pending: kind,
			lastAction: kind,
			error: null,
			notice: null,
			// The projection is still behind on whatever the last acknowledged set
			// changed; only a read that lands, or the settle below, says otherwise.
		});
		try {
			await operation();
			// resumeThread may recycle the transport, which can dispose these
			// controls before its acknowledgement arrives. The bound screen still
			// owns this refresh when its identity fence is current.
			if (kind === "resume") {
				// The resume's own reply is the acknowledgement, and it is what the
				// restart settles on. The read that follows is catch-up: it brings
				// the running session on screen, and the live binding's own recovery
				// does that whenever a read cannot. Rejecting here on a failed read
				// reports a resume the hub already took as one that could not start
				// the session, which is how a restart landed on a daemon that was
				// running while saying it had stopped.
				//
				if (this.isCurrent("destination")) {
					try {
						await this.refresh();
					} catch {
						// Acknowledged already; the read is catch-up.
					}
				}
				if (!this.isCurrent("session")) return false;
				if (!this.disposed) this.publish({ pending: null, error: null });
				return true;
			}
			if (kind === "changeModel" || kind === "setVisionModel") {
				// An acknowledged pick can outlive the binding that sent it: a
				// re-render may dispose these controls, and a read can fail or wait
				// out a reconnect. The acknowledgement, not the read, is the record
				// that the change applied, so the follow-up read is best effort.
				//
				// The readback and the announcement answer different questions. The
				// readback belongs to the live session and a ready transport: a pane
				// another screen covers still has to show the new choice when the
				// reader returns, and a pick that never reattaches its relay applies
				// with nothing on screen to say so. Visibility decides only whether
				// the change is announced, which is the reader's question.
				// The acknowledgement is the record that the hub took the value, so
				// it is written down before the read rather than after it. Another
				// mounted view of this session reads the record to decide whether the
				// row the projection draws is still the truth; waiting for the
				// readback leaves that view trusting a projection this write has
				// already invalidated, and tapping the selected row there dismisses
				// the picker without sending the correction.
				const setting: Setting = kind === "changeModel" ? "model" : "vision";
				this.session.record(setting, { state: "acknowledged", value });
				let resolved = false;
				if (this.isCurrent("destination")) {
					// Readiness is a separate question from the reader's presence: a
					// transport that is away reads nothing, and the session's own
					// reconnect read catches the screen up afterwards.
					try {
						await this.refresh();
						resolved = true;
					} catch {
						// The screen's own read catches up; the pick applied regardless.
					}
				}
				// The publication, not this controller, is what retires the record:
				// only a read of the session can say what the hub now holds, and the
				// entry retires it when that read shows this value. A read that
				// published nothing, or one that stopped at the running model while a
				// next-turn pick is queued, leaves the record standing.
				if (resolved && this.session.published()) this.session.retirePublished();
				// A read can outlast the reader's stay: whoever left while it ran
				// is owed no confirmation on the screen they left, even when the
				// read itself failed.
				const stayed = this.isCurrent("route");
				// Any still-live binding settles its action here, so its pending
				// state never outlives the acknowledgement. The notice follows the
				// reader's presence, which the return value also reports.
				// The acknowledgement says the hub took the value; the read is what
				// puts it on screen. Without one the projection still shows what the
				// hub held before, so the value it answers with is recorded against
				// the session - a replacement binding has to see it too, and the one
				// that sent this may be the one being retired right now.
				if (!this.disposed) {
					this.publish({
						pending: null,
						error: null,
						unread: this.session.unread(),
						notice: stayed ? (kind === "changeModel" ? "Model updated." : "Vision model updated.") : null,
					});
				}
				return stayed;
			}
			if (kind === "shutdown" || kind === "forceStop") {
				// An accepted shutdown is a fact about the runtime, so it outranks
				// this binding's visibility: another screen can open over the
				// conversation while the reply is out, and an accepted stop
				// reported as a failure is the wrong answer to something the hub
				// already did. Only a binding that now belongs to another
				// conversation has nothing to settle here. A shutdown narrates
				// nothing itself: the session's own toast confirms it and the
				// screen stays (ruling 19).
				if (!this.isCurrent("session")) return false;
				if (!this.disposed) {
					this.publish({
						pending: null,
						error: null,
						notice: kind === "forceStop" ? "Runtime stopped. Saved history is available to resume." : null,
					});
					this.stopped();
				}
				return true;
			}
			// The hub acknowledged the action, and that acknowledgement is what
			// settles it. Controls replaced while the reply was out still settle
			// the screen's editor: the action is a fact about the session, and the
			// replacement binding runs its own readback, which owns the service
			// that is still open. Only a binding that moved to another
			// conversation has nothing left to settle here.
			if (!this.isCurrent("session")) return false;
			// The hub's receipt is the acknowledgement, and it is what settles
			// this action. The read that follows is catch-up: it brings the
			// session's new state on screen, and a read that fails leaves the
			// acknowledgement standing rather than turning an accepted rename,
			// compaction or effort change into an unconfirmed one. The session's
			// own read recovery surfaces anything the read would have said. A
			// refusal arrives as a rejection below, where it stays visible.
			//
			// This runs whichever controller sent the action, including one the
			// push disposed: the read is for the session's shared binding, and a
			// cold rename lands in the hub's past index, so a session whose read
			// is still considered current never fetches the new title on return.
			// The read is best effort, so a binding that cannot read is skipped
			// rather than reported.
			if (this.isCurrent("destination")) {
				try {
					await this.refresh();
				} catch {
					// The action was acknowledged; the read is catch-up.
				}
			}
			// A push can dispose these controls while that read is out. The hub
			// already took the action, so the caller still settles: reporting a
			// refusal here leaves a retained editor open over a rename the hub
			// accepted. A binding that moved to another conversation is the one
			// case with nothing to report.
			if (!this.isCurrent("session")) return false;
			// The hub took the action, so this binding settles it here whichever
			// screen is in front: a covered pane's editor must not stay dirty over
			// a rename the hub accepted. The notice is the reader's question, and
			// only a view still in front has one to tell.
			const stayed = this.isCurrent("route");
			// A disposed controller has no listeners left, so it publishes
			// nothing: the replacement binding owns that state now.
			if (!this.disposed)
				this.publish({
					pending: null,
					error: null,
					notice: !stayed
						? null
						: kind === "compact"
							? "Compaction requested. Progress appears in the conversation."
							: kind === "setReasoningEffort"
								? "Reasoning effort updated."
								: "Session renamed.",
				});
			return true;
		} catch (cause) {
			const failure =
				cause instanceof WireError
					? `Could not confirm the action: ${cause.message}`
					: "Could not confirm the action. Check the session before trying again; it may have been applied.";
			// A refused action leaves the picker offering it again only if nothing
			// reads the session back. The read follows the binding that can still
			// read, not the pane that happens to be in front, and it sends no
			// second mutation: the original refusal is what the reader sees. It
			// runs before this binding reports, because a covered pane ends here
			// and a picker that never caught up keeps offering the refused action
			// when the reader returns to it.
			if (
				cause instanceof WireError &&
				cause.evenerErrorInfo === "actionUnavailable" &&
				this.isCurrent("destination")
			) {
				try {
					await this.refresh();
				} catch {
					// Preserve the original server rejection; refresh is confirmation only.
				}
			}
			// A write the hub never answered may still have been applied: a
			// request that timed out while the phone was reconnecting reached the
			// hub after the client gave up, and a cold set is applied inside the
			// hub's own resume. That is not an acknowledgement and it is not the
			// value: the hub may hold what was written, what it held before, or
			// nothing. Recording the value would let the next tap on the row that
			// already shows it dismiss as "already set" - the corrective Off the
			// reader sends when vision was enabled behind a stale Off tick - and
			// leave vision on. The setting is recorded as unknown instead, so the
			// projection is not trusted for it until a read lands.
			//
			// The hub's own "the reply to a write that may have applied is gone"
			// rejection is the same fact and takes the same path. A refusal that
			// says nothing applied leaves the projection truthful and stays out of
			// this record entirely.
			if ((kind === "changeModel" || kind === "setVisionModel") && isLostReply(cause) && this.isCurrent("session")) {
				// One setting's write says nothing about the other's: a timed-out
				// model pick must not clear the record of a vision write that is
				// still outstanding, or the vision row goes back to trusting a
				// projection that write may have invalidated.
				this.session.record(kind === "changeModel" ? "model" : "vision", { state: "unknown" });
				if (!this.disposed) this.publish({ unread: this.session.unread() });
			}
			if (this.ends(failure, kind)) return false;
			this.publish({
				pending: null,
				notice: null,
				error: failure,
			});
			return false;
		}
	}
}

const noSubscription = () => () => {};
const noState = () => null;

/** The controls' state, or null while there are none: the hub is away, or
 * the session isn't in front. */
export function useControlsState(controls: SessionControls | null): ControlsState | null {
	return useSyncExternalStore(controls?.subscribe ?? noSubscription, controls?.getSnapshot ?? noState);
}
