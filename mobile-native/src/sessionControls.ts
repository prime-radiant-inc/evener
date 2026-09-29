import { useSyncExternalStore } from "react";
import { WireError } from "@evener/appwire-client";
import type { ModelListResponse } from "@evener/appwire-client";
import { effortOptionLevels, sessionEffortLevels } from "@evener/appwire-client";
import type { MobileConversation } from "./projectedRows";
import type {
	ConversationModelCatalog,
	ConversationRecoveryActions,
	ConversationService,
} from "../../mobile/src/services/conversation";

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
	constructor(
		private service: Pick<ConversationService, Exclude<Operation, "forceStop" | "resume">> &
			ConversationRecoveryActions &
			ConversationModelCatalog,
		private refresh: () => Promise<void>,
		private stopped: () => void,
		private isCurrent: (scope?: "destination") => boolean,
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
				if (!this.isCurrent("destination")) return false;
				await this.refresh();
			}
			if (this.disposed || !this.isCurrent()) return false;
			if (kind === "shutdown" || kind === "forceStop") {
				// A shutdown narrates nothing here: the session's own toast
				// confirms it and the screen stays (ruling 19).
				this.publish({
					pending: null,
					error: null,
					notice: kind === "forceStop" ? "Runtime stopped. Saved history is available to resume." : null,
				});
				this.stopped();
				return true;
			}
			if (kind !== "resume") await this.refresh();
			if (this.disposed || !this.isCurrent()) return false;
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
								: kind === "setVisionModel"
									? "Vision model updated."
									: kind === "changeModel"
										? "Model updated."
										: "Session renamed.",
			});
			return true;
		} catch (cause) {
			if (this.disposed || !this.isCurrent()) return false;
			if (cause instanceof WireError && cause.evenerErrorInfo === "actionUnavailable") {
				try {
					await this.refresh();
				} catch {
					// Preserve the original server rejection; refresh is confirmation only.
				}
				if (this.disposed || !this.isCurrent()) return false;
			}
			this.publish({
				pending: null,
				notice: null,
				error:
					cause instanceof WireError
						? `Could not confirm the action: ${cause.message}`
						: "Could not confirm the action. Check the session before trying again; it may have been applied.",
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
