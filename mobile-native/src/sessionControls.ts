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

/** How one controls action went.
 *
 * `accepted` is the action's own yes, with the notice the screen may show. A
 * resolved action is accepted whatever became of these controls meanwhile:
 * the reconnects a pick or a resume causes retire this binding, so the
 * answer is owed to whoever asked, not to whoever is current.
 *
 * `refused` is an action that did reach the wire and was refused - the
 * daemon's answer, or a confirmation that could not be read. It is derived
 * purely from the operation's catch; nothing here re-classifies a refusal
 * from the state the answer left behind.
 *
 * `notSent` is the phone's own refusal before anything left it: a catalog
 * that does not carry the pick, another action holding the session, a
 * binding that moved on, or nothing to change. Nothing left the phone, so
 * the caller may keep the user's text and offer a retry. An empty message
 * is the silent no-op a guard already explained on its own screen.
 *
 * An initiator never reads its outcome from `ControlsState.error`: that
 * channel is the behind-screen observer's, published only while the binding
 * is current, so a reconnect that replaces these controls would swallow
 * exactly the refusals this type exists to deliver. The promise is the only
 * channel an initiator reads. */
export type ActionOutcome =
	| { disposition: "accepted"; notice: string | null }
	| { disposition: "refused"; message: string }
	| { disposition: "notSent"; message: string };

export interface ControlsState {
	pending: Operation | null;
	lastAction: Operation | null;
	error: string | null;
	notice: string | null;
	catalog: ModelListResponse | null;
	loadingModels: boolean;
	modelError: string | null;
}

/** The notice each completed action offers the screen, by kind. A shutdown
 * narrates nothing: the session's own toast confirms it and the screen stays
 * (ruling 19). */
const NOTICE: Record<Operation, string | null> = {
	rename: "Session renamed.",
	compact: "Compaction requested. Progress appears in the conversation.",
	shutdown: null,
	forceStop: "Runtime stopped. Saved history is available to resume.",
	resume: "Runtime resumed.",
	setReasoningEffort: "Reasoning effort updated.",
	setVisionModel: "Vision model updated.",
	changeModel: "Model updated.",
};

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
	};
	private listeners = new Set<() => void>();
	private disposed = false;
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
		private isCurrent: () => boolean,
		private getReasoning: () => Pick<
			MobileConversation,
			"supportsReasoning" | "reasoningEffort" | "reasoningEffortLevels"
		> | null,
		private canMutate: () => boolean,
		// The catalog the screen already knows, from its last binding: the
		// model's name needs no read to show.
		catalog: ModelListResponse | null = null,
	) {
		this.state = { ...this.state, catalog };
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
	dispose() {
		this.disposed = true;
		this.listeners.clear();
	}
	rename(name: string): Promise<ActionOutcome> {
		const trimmed = name.trim();
		if (!trimmed) return Promise.resolve({ disposition: "notSent", message: "" });
		return this.run("rename", () => this.service.rename(trimmed));
	}
	compact(): Promise<ActionOutcome> {
		return this.run("compact", () => this.service.compact());
	}
	shutdown(): Promise<ActionOutcome> {
		return this.run("shutdown", () => this.service.shutdown());
	}
	forceStop(): Promise<ActionOutcome> {
		return this.run("forceStop", () => this.service.forceStop());
	}
	resume(): Promise<ActionOutcome> {
		return this.run("resume", () => this.service.resume());
	}
	setReasoningEffort(effort: string): Promise<ActionOutcome> {
		const current = this.getReasoning();
		if (!current) return Promise.resolve({ disposition: "notSent", message: "" });
		const levels = sessionEffortLevels(current.reasoningEffortLevels, current.supportsReasoning);
		const selected = current.reasoningEffort ?? "";
		if (!levels.length || !effortOptionLevels(levels, selected).includes(effort) || selected === effort)
			return Promise.resolve({ disposition: "notSent", message: "" });
		return this.run("setReasoningEffort", () => this.service.setReasoningEffort(effort));
	}
	setVisionModel(visionModel: string): Promise<ActionOutcome> {
		return this.run("setVisionModel", () => this.service.setVisionModel(visionModel));
	}
	/** Reads the model catalog. The catalog it has stays until the new one
	 * lands, so a model's name never falls back to its id mid-read; a failed
	 * read clears it, so the picker offers no stale choices. A read the
	 * binding moved on from still ends the load, so the next one reads again
	 * rather than waiting on it forever. */
	async loadModels() {
		if (this.disposed || !this.isCurrent() || this.state.loadingModels || this.state.pending) return;
		// A load is the newest read: a refresh still out answers older.
		this.catalogReads++;
		this.publish({ loadingModels: true, modelError: null });
		try {
			const catalog = await this.service.models();
			if (this.disposed) return;
			this.publish(this.isCurrent() ? { catalog, loadingModels: false } : { loadingModels: false });
		} catch (error) {
			if (this.disposed) return;
			this.publish(
				this.isCurrent()
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
		if (this.disposed || !this.isCurrent() || !this.state.catalog) return;
		const read = ++this.catalogReads;
		try {
			const catalog = await this.service.models();
			if (!this.disposed && read === this.catalogReads && this.isCurrent()) this.publish({ catalog });
		} catch {
			// A failed refresh keeps the catalog on screen.
		}
	}
	/** A catalog the screen's last read no longer carries never reaches the
	 * wire: nothing left the phone, so the caller may offer the choice again
	 * once a fresh catalog lands. */
	changeModel(provider: string, model: string): Promise<ActionOutcome> {
		if (!this.state.catalog?.data.some((entry) => entry.provider === provider && entry.model === model))
			return Promise.resolve({
				disposition: "notSent",
				message: "The model list no longer carries that choice.",
			});
		return this.run("changeModel", () => this.service.changeModel(provider, model));
	}
	changeVisionModel(provider: string, model: string): Promise<ActionOutcome> {
		if (
			!this.state.catalog?.data.some(
				(entry) => entry.provider === provider && entry.model === model && entry.supportsVision !== false,
			)
		)
			return Promise.resolve({
				disposition: "notSent",
				message: "The model list no longer carries that choice.",
			});
		return this.run("setVisionModel", () => this.service.setVisionModel(`${provider}/${model}`));
	}
	/** Runs one action and answers with how it went. The promise is the
	 * initiator's channel; the state these controls publish is the
	 * behind-screen observer's, and only a current binding may write it. */
	private async run(kind: Operation, operation: () => Promise<void>): Promise<ActionOutcome> {
		if (
			this.disposed ||
			!this.isCurrent() ||
			(kind !== "forceStop" && kind !== "resume" && !this.canMutate()) ||
			this.state.pending
		)
			return { disposition: "notSent", message: "" };
		this.publish({
			pending: kind,
			lastAction: kind,
			error: null,
			notice: null,
		});
		try {
			await operation();
			// The reconnects a pick or a resume causes retire this binding, so
			// these controls can be disposed by the time the answer lands; the
			// answer is still accepted. The screen behind reports the session's
			// new state through its own read of whichever binding survived.
			if (kind === "resume" && !this.disposed && this.isCurrent()) {
				await this.refresh();
				if (this.disposed || !this.isCurrent()) return { disposition: "accepted", notice: NOTICE[kind] };
			}
			if (kind === "shutdown" || kind === "forceStop") {
				if (!this.disposed && this.isCurrent()) {
					this.publish({ pending: null, error: null, notice: NOTICE[kind] });
					this.stopped();
				}
				return { disposition: "accepted", notice: NOTICE[kind] };
			}
			if (kind !== "resume" && !this.disposed && this.isCurrent()) await this.refresh();
			this.publishIfCurrent({
				pending: null,
				error: null,
				notice: NOTICE[kind],
			});
			return { disposition: "accepted", notice: NOTICE[kind] };
		} catch (cause) {
			if (cause instanceof WireError && cause.evenerErrorInfo === "actionUnavailable") {
				try {
					await this.refresh();
				} catch {
					// Preserve the original server rejection; refresh is confirmation only.
				}
			}
			const message =
				cause instanceof WireError
					? `Could not confirm the action: ${cause.message}`
					: "Could not confirm the action. Check the session before trying again; it may have been applied.";
			this.publishIfCurrent({ pending: null, notice: null, error: message });
			return { disposition: "refused", message };
		}
	}
	/** The behind-screen observer channel: it only publishes while the
	 * binding is current, so the controls a reconnect replaced never write
	 * the state their successor owns. */
	private publishIfCurrent(state: Partial<ControlsState>) {
		if (this.disposed || !this.isCurrent()) return;
		this.publish(state);
	}
}

const noSubscription = () => () => {};
const noState = () => null;

/** The controls' state, or null while there are none: the hub is away, or
 * the session isn't in front. */
export function useControlsState(controls: SessionControls | null): ControlsState | null {
	return useSyncExternalStore(controls?.subscribe ?? noSubscription, controls?.getSnapshot ?? noState);
}
