import { WireError } from "../../cmd/evener-hub/frontend/src/protocol/errors";
import type { ModelListResponse } from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
import {
	effortOptionLevels,
	sessionEffortLevels,
} from "../../cmd/evener-hub/frontend/src/shell/reasoningEffort";
import type { MobileConversation } from "../../mobile/src/conversation/model";
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
interface ControlsState {
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
		private service: Pick<
			ConversationService,
			Exclude<Operation, "forceStop" | "resume">
		> &
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
	) {}
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
		const levels = sessionEffortLevels(
			current.reasoningEffortLevels,
			current.supportsReasoning,
		);
		const selected = current.reasoningEffort ?? "";
		if (
			!levels.length ||
			!effortOptionLevels(levels, selected).includes(effort) ||
			selected === effort
		)
			return Promise.resolve();
		return this.run("setReasoningEffort", () =>
			this.service.setReasoningEffort(effort),
		);
	}
	setVisionModel(visionModel: string) {
		return this.run("setVisionModel", () =>
			this.service.setVisionModel(visionModel),
		);
	}
	async loadModels() {
		if (
			this.disposed ||
			!this.isCurrent() ||
			this.state.loadingModels ||
			this.state.pending
		)
			return;
		this.publish({ catalog: null, loadingModels: true, modelError: null });
		try {
			const catalog = await this.service.models();
			if (this.disposed || !this.isCurrent()) return;
			this.publish({ catalog, loadingModels: false });
		} catch (error) {
			if (this.disposed || !this.isCurrent()) return;
			this.publish({
				loadingModels: false,
				modelError:
					error instanceof Error ? error.message : "Could not load models.",
			});
		}
	}
	changeModel(provider: string, model: string): Promise<boolean> {
		if (
			!this.state.catalog?.data.some(
				(entry) => entry.provider === provider && entry.model === model,
			)
		)
			return Promise.resolve(false);
		return this.run("changeModel", () =>
			this.service.changeModel(provider, model),
		);
	}
	changeVisionModel(provider: string, model: string): Promise<boolean> {
		if (
			!this.state.catalog?.data.some(
				(entry) =>
					entry.provider === provider &&
					entry.model === model &&
					entry.supportsVision !== false,
			)
		)
			return Promise.resolve(false);
		return this.run("setVisionModel", () =>
			this.service.setVisionModel(`${provider}/${model}`),
		);
	}
	private async run(
		kind: Operation,
		operation: () => Promise<void>,
	): Promise<boolean> {
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
				this.publish({
					pending: null,
					error: null,
					notice:
						kind === "forceStop"
							? "Runtime stopped. Saved history is available to resume."
							: "Runtime stop requested.",
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
			if (
				cause instanceof WireError &&
				cause.evenerErrorInfo === "actionUnavailable"
			) {
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
