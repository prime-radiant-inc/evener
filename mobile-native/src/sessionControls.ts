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
export interface ControlsState {
	pending: Operation | null;
	lastAction: Operation | null;
	error: string | null;
	notice: string | null;
	catalog: ModelListResponse | null;
	loadingModels: boolean;
	modelError: string | null;
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
		private isCurrent: (scope?: "destination" | "route" | "session") => boolean,
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
	private ends(error: string | null = null): boolean {
		if (this.disposed) return true;
		if (this.isCurrent()) return false;
		if (this.isCurrent("session")) this.publish({ pending: null, error });
		return true;
	}
	dispose() {
		this.disposed = true;
		this.listeners.clear();
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
	changeModel(provider: string, model: string): Promise<boolean> {
		if (!this.state.catalog?.data.some((entry) => entry.provider === provider && entry.model === model))
			return Promise.resolve(false);
		return this.run("changeModel", () => this.service.changeModel(provider, model));
	}
	changeVisionModel(provider: string, model: string): Promise<boolean> {
		if (
			!this.state.catalog?.data.some(
				(entry) => entry.provider === provider && entry.model === model && entry.supportsVision !== false,
			)
		)
			return Promise.resolve(false);
		return this.run("setVisionModel", () => this.service.setVisionModel(`${provider}/${model}`));
	}
	private async run(kind: Operation, operation: () => Promise<void>): Promise<boolean> {
		if (
			this.disposed ||
			!this.isCurrent() ||
			(kind !== "forceStop" && kind !== "resume" && !this.canMutate()) ||
			this.state.pending
		)
			return false;
		this.publish({
			pending: kind,
			lastAction: kind,
			error: null,
			notice: null,
		});
		try {
			await operation();
			// resumeThread may recycle the transport, which can dispose these
			// controls before its acknowledgement arrives. The bound screen still
			// owns this refresh when its identity fence is current.
			if (kind === "resume") {
				if (!this.isCurrent("destination")) {
					if (!this.disposed) this.publish({ pending: null });
					return false;
				}
				await this.refresh();
			}
			if (kind === "changeModel" || kind === "setVisionModel") {
				// An acknowledged pick can outlive the binding that sent it: a
				// re-render may dispose these controls, and a read can fail or wait
				// out a reconnect. The acknowledgement, not the read, is the record
				// that the change applied, so the follow-up read is best effort and
				// one path serves every binding state. The reader still being on this
				// session decides whether the change is announced; the transport
				// decides only whether it can be read back. A reconnecting phone has
				// not left the screen it is looking at, and its reconnect read is
				// what brings the change on screen.
				let stayed = this.isCurrent("route");
				if (stayed) {
					// Readiness is a separate question from the reader's presence: a
					// transport that is away reads nothing, and the session's own
					// reconnect read catches the screen up afterwards.
					if (this.isCurrent("destination")) {
						try {
							await this.refresh();
						} catch {
							// The screen's own read catches up; the pick applied regardless.
						}
					}
					// A read can outlast the reader's stay: whoever left while it ran
					// is owed no confirmation on the screen they left, even when the
					// read itself failed.
					stayed = this.isCurrent("route");
				}
				// Any still-live binding settles its action here, so its pending
				// state never outlives the acknowledgement. The notice follows the
				// reader's presence, which the return value also reports.
				if (!this.disposed) {
					this.publish({
						pending: null,
						error: null,
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
			if (this.ends()) return false;
			if (kind !== "resume") await this.refresh();
			if (this.ends()) return false;
			this.publish({
				pending: null,
				error: null,
				notice:
					kind === "resume"
						? "Runtime resumed."
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
			if (this.ends(failure)) return false;
			if (cause instanceof WireError && cause.evenerErrorInfo === "actionUnavailable") {
				try {
					await this.refresh();
				} catch {
					// Preserve the original server rejection; refresh is confirmation only.
				}
				if (this.ends(failure)) return false;
			}
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
