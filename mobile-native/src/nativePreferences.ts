import {
	type AnyNotification,
	createKeybindingsStore,
	type FeatureSet,
	fromWireConfig,
	type KeybindingDraftStorage,
	type KeybindingsOverrides,
	type KeybindingsRule,
	type KeybindingsStore,
	type KeybindingsStoreState,
	type KeybindingsSupport,
	keybindingsSupport,
	normalizeConfig,
	toWireConfig,
	type TranscriptDisplayConfigV1,
} from "@evener/appwire-client";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import {
	type TranscriptDraftCheckpoint,
	TranscriptDraftRepository,
	type TranscriptDraftStorage,
} from "./preferenceDraftRepository";

type NativeFeatures = Pick<
	FeatureSet,
	"keybindingsSettings" | "transcriptDisplaySettings"
>;
export interface PreferenceState<T> {
	support: KeybindingsSupport;
	loading: boolean;
	saving: boolean;
	confirmed: T | null;
	draft: T | null;
	error: string | null;
	conflict: boolean;
	writeUncertain: boolean;
	storageUnavailable: boolean;
	/** The port answered but what it held could not be read - discardable
	 * (never a dead port). Only the keybindings domain sets this; transcript
	 * has no equivalent concept yet, so it stays false there. */
	draftUnreadable: boolean;
}

/** The confirmed payload as the shared store holds it: its rule list is the
 * store's own (identity-stable until the next applied payload), read-only. */
export type ConfirmedKeybindings = Omit<KeybindingsOverrides, "rules"> & {
	rules: readonly KeybindingsRule[];
};

export interface NativePreferencesSnapshot {
	keybindings: PreferenceState<ConfirmedKeybindings>;
	transcriptMobile: PreferenceState<{
		revision: number;
		config: TranscriptDisplayConfigV1;
	}>;
}

const initialDomain = <T>(): PreferenceState<T> => ({
	support: "unknown",
	loading: false,
	saving: false,
	confirmed: null,
	draft: null,
	error: null,
	conflict: false,
	writeUncertain: false,
	storageUnavailable: false,
	draftUnreadable: false,
});

function isRecord(value: unknown): value is Record<string, unknown> {
	return typeof value === "object" && value !== null && !Array.isArray(value);
}

function isRevision(value: unknown): value is number {
	return typeof value === "number" && Number.isSafeInteger(value) && value >= 0;
}

function decodeTranscript(value: unknown) {
	if (!isRecord(value) || !isRecord(value.mobile) || !isRecord(value.desktop))
		throw new Error("Hub returned invalid transcript display settings.");
	const decode = (entry: unknown) => {
		if (!isRecord(entry) || !isRevision(entry.revision))
			throw new Error("Hub returned invalid transcript display settings.");
		const config = fromWireConfig(entry.config);
		if (config === undefined)
			throw new Error("Hub returned invalid transcript display settings.");
		return { revision: entry.revision, config };
	};
	return { desktop: decode(value.desktop), mobile: decode(value.mobile) };
}

function decodeTranscriptPatch(value: unknown): {
	revision: number;
	config: TranscriptDisplayConfigV1;
} {
	if (!isRecord(value) || !isRevision(value.revision))
		throw new Error("Hub returned invalid transcript display settings.");
	const config = fromWireConfig(value.config);
	if (config === undefined)
		throw new Error("Hub returned invalid transcript display settings.");
	return { revision: value.revision, config };
}

const HUB_UNCONFIRMED_MESSAGE = "The hub request could not be confirmed.";

const KEYBINDINGS_LOAD_ERROR_MESSAGE =
	"The hub could not load its saved shortcuts. Repair the hub settings file before editing.";

function hubErrorMessage(state: KeybindingsStoreState): string | null {
	if (state.loadError !== null) return KEYBINDINGS_LOAD_ERROR_MESSAGE;
	return state.hubError === null ? null : HUB_UNCONFIRMED_MESSAGE;
}

// The keybinding domain is a projection of the shared store's state: the
// confirmed payload is the store's revision and raw rules (the rule array is
// the store's own, a fresh one per applied payload and identity-stable
// otherwise, which the shortcut screen's preview memo keys on), and a
// hub-sourced failure surfaces as fixed copy (a raw client error may carry a
// token or a path) while the draft port's own messages pass through. An
// unconfirmed write is the `writeUncertain` fact, rendered as its own notice.
function keybindingsDomain(
	state: KeybindingsStoreState,
): PreferenceState<ConfirmedKeybindings> {
	return {
		support: state.hubSupport,
		loading: state.hubLoading,
		saving: state.saving,
		confirmed: state.loaded
			? {
					version: 1,
					revision: state.revision,
					rules: state.rawOverrides,
					...(state.loadError === null ? {} : { loadError: state.loadError }),
				}
			: null,
		draft: state.draft,
		error: state.draftError ?? hubErrorMessage(state),
		conflict: state.draftConflict,
		writeUncertain: state.writeUncertain,
		storageUnavailable: state.storageUnavailable,
		draftUnreadable: state.draftUnreadable,
	};
}

export class NativePreferences {
	private state: NativePreferencesSnapshot;
	private readonly listeners = new Set<() => void>();
	private readonly client: ConversationClientLike;
	private readonly unsubscribe: () => void;
	private readonly keybindings: KeybindingsStore;
	private readonly unsubscribeKeybindings: () => void;
	private generation = 0;
	private disposed = false;
	private readonly transcriptDrafts?: TranscriptDraftRepository;

	constructor(
		client: ConversationClientLike,
		features: NativeFeatures,
		transcriptStorage?: TranscriptDraftStorage,
		keybindingStorage?: KeybindingDraftStorage,
	) {
		this.client = client;
		this.transcriptDrafts = transcriptStorage
			? new TranscriptDraftRepository(transcriptStorage)
			: undefined;
		// One store per model, like one model per ready connection: the
		// features are the handshake's, so support is final and the one ready
		// generation lasts until dispose.
		this.keybindings = createKeybindingsStore({
			client,
			drafts: keybindingStorage,
		});
		this.keybindings.setSupport(keybindingsSupport(features));
		this.keybindings.beginReadyGeneration();
		this.state = {
			keybindings: keybindingsDomain(this.keybindings.getState()),
			transcriptMobile: {
				...initialDomain(),
				support:
					features.transcriptDisplaySettings === true
						? "supported"
						: "unsupported",
			},
		};
		this.unsubscribeKeybindings = this.keybindings.subscribe((state) =>
			this.publish({ keybindings: keybindingsDomain(state) }),
		);
		this.unsubscribe = client.onNotification((notification) =>
			this.onNotification(notification),
		);
		if (this.transcriptDrafts) this.restoreTranscriptDraft();
	}

	getSnapshot = (): NativePreferencesSnapshot => this.state;

	subscribe = (listener: () => void): (() => void) => {
		this.listeners.add(listener);
		return () => this.listeners.delete(listener);
	};

	private publish(change: Partial<NativePreferencesSnapshot>): void {
		if (this.disposed) return;
		this.state = { ...this.state, ...change };
		for (const listener of this.listeners) listener();
	}

	private onNotification(notification: AnyNotification): void {
		if (this.disposed) return;
		if (
			notification.method === "evener/settings/transcriptDisplay/changed" &&
			this.state.transcriptMobile.support === "supported"
		) {
			const params = notification.params;
			if (!isRecord(params) || params.layout !== "mobile") return;
			try {
				const value = decodeTranscriptPatch(params);
				const current = this.state.transcriptMobile.confirmed;
				if (current && value.revision < current.revision) return;
				const draft = this.state.transcriptMobile.draft;
				this.publish({
					transcriptMobile: {
						...this.state.transcriptMobile,
						confirmed: value,
						draft,
						error:
							draft || this.state.transcriptMobile.storageUnavailable
								? this.state.transcriptMobile.error
								: null,
						conflict: draft ? value.revision > draft.revision : false,
						writeUncertain: this.state.transcriptMobile.writeUncertain,
					},
				});
			} catch {
				this.publish({
					transcriptMobile: {
						...this.state.transcriptMobile,
						error:
							"Transcript display settings changed. Refresh to inspect the current value.",
					},
				});
			}
		}
	}

	async refresh(): Promise<void> {
		if (this.disposed) return;
		const generation = ++this.generation;
		if (this.state.transcriptMobile.storageUnavailable)
			this.restoreTranscriptDraft();
		const reads: Promise<void>[] = [
			this.keybindings.getState().refreshOverrides(),
		];
		if (
			this.state.transcriptMobile.support === "supported" &&
			!this.state.transcriptMobile.storageUnavailable
		)
			reads.push(this.refreshTranscript(generation));
		await Promise.all(reads);
	}

	private async refreshTranscript(generation: number): Promise<void> {
		this.publish({
			transcriptMobile: {
				...this.state.transcriptMobile,
				loading: true,
				error: null,
			},
		});
		try {
			const value = decodeTranscript(
				await this.client.request("evener/settings/transcriptDisplay/get", {}),
			);
			if (generation !== this.generation || this.disposed) return;
			if (
				this.state.transcriptMobile.saving ||
				(this.state.transcriptMobile.confirmed?.revision ?? -1) >
					value.mobile.revision
			) {
				this.publish({
					transcriptMobile: { ...this.state.transcriptMobile, loading: false },
				});
				return;
			}
			const draft = this.state.transcriptMobile.draft;
			if (draft && this.state.transcriptMobile.writeUncertain)
				this.persistTranscriptDraft({
					baseRevision: draft.revision,
					config: draft.config,
					writeUncertain: false,
				});
			this.publish({
				transcriptMobile: {
					...this.state.transcriptMobile,
					loading: false,
					confirmed: value.mobile,
					draft,
					error: null,
					conflict: draft ? value.mobile.revision > draft.revision : false,
					writeUncertain: false,
				},
			});
		} catch (error) {
			if (generation === this.generation && !this.disposed)
				this.publish({
					transcriptMobile: {
						...this.state.transcriptMobile,
						loading: false,
						error: HUB_UNCONFIRMED_MESSAGE,
					},
				});
		}
	}

	// The draft editor is the shared store's; these stay async so a refused
	// edit rejects the way the screen awaits it.
	async editKeybindings(rules: readonly KeybindingsRule[]): Promise<void> {
		this.keybindings.getState().editDraft(rules);
	}

	async saveKeybindings(
		rules?: readonly KeybindingsRule[],
	): Promise<KeybindingsOverrides> {
		return this.keybindings.getState().saveDraft(rules);
	}

	async discardKeybindingsDraft(): Promise<void> {
		this.keybindings.getState().discardDraft();
	}

	async rebaseKeybindingsDraft(reviewedRevision: number): Promise<void> {
		this.keybindings.getState().rebaseDraft(reviewedRevision);
	}

	async saveTranscript(
		config?: TranscriptDisplayConfigV1,
	): Promise<{ revision: number; config: TranscriptDisplayConfigV1 }> {
		const current = this.state.transcriptMobile.confirmed;
		const existing = this.state.transcriptMobile.draft;
		if (
			this.disposed ||
			this.state.transcriptMobile.saving ||
			this.state.transcriptMobile.storageUnavailable ||
			this.state.transcriptMobile.writeUncertain ||
			this.state.transcriptMobile.conflict ||
			this.state.transcriptMobile.support !== "supported" ||
			current === null
		)
			throw new Error("Hub transcript display settings are unavailable.");
		const normalized = normalizeConfig(
			config ?? existing?.config ?? current.config,
		);
		const baseRevision = existing?.revision ?? current.revision;
		let checkpoint: TranscriptDraftCheckpoint;
		const pending = {
			baseRevision,
			config: normalized,
			writeUncertain: true,
		};
		this.publish({
			transcriptMobile: {
				...this.state.transcriptMobile,
				saving: true,
				draft: { revision: baseRevision, config: normalized },
				error: null,
				conflict: false,
			},
		});
		try {
			checkpoint = this.persistTranscriptDraft(pending);
		} catch (error) {
			this.publish({
				transcriptMobile: {
					...this.state.transcriptMobile,
					saving: false,
					error:
						error instanceof Error
							? error.message
							: "Could not save the transcript draft locally.",
				},
			});
			throw error;
		}
		if (this.disposed)
			throw new Error("Transcript preference save was cancelled.");
		let value: { revision: number; config: TranscriptDisplayConfigV1 };
		try {
			value = decodeTranscriptPatch(
				await this.client.request("evener/settings/transcriptDisplay/patch", {
					layout: "mobile",
					expectedRevision: baseRevision,
					config: toWireConfig(normalized),
				}),
			);
		} catch (error) {
			this.publish({
				transcriptMobile: {
					...this.state.transcriptMobile,
					saving: false,
					error: HUB_UNCONFIRMED_MESSAGE,
					conflict: true,
					writeUncertain: true,
				},
			});
			throw error;
		}
		const latest = this.state.transcriptMobile.confirmed;
		const conflict =
			!this.disposed && !!latest && latest.revision > value.revision;
		let storageError: string | null = null;
		try {
			if (conflict)
				this.persistTranscriptDraft({ ...checkpoint, writeUncertain: false });
			else this.transcriptDrafts?.removeIf(checkpoint);
		} catch {
			storageError =
				"The hub confirmed this save, but the local draft could not be updated. Refresh settings to retry.";
		}
		this.publish({
			transcriptMobile: {
				...this.state.transcriptMobile,
				saving: false,
				confirmed: conflict ? latest : value,
				draft: conflict ? this.state.transcriptMobile.draft : null,
				conflict,
				writeUncertain: false,
				storageUnavailable: storageError !== null,
				error: storageError,
			},
		});
		return value;
	}

	async editTranscript(config: TranscriptDisplayConfigV1): Promise<void> {
		const current = this.state.transcriptMobile.confirmed;
		if (
			this.disposed ||
			this.state.transcriptMobile.saving ||
			this.state.transcriptMobile.storageUnavailable ||
			this.state.transcriptMobile.writeUncertain ||
			this.state.transcriptMobile.support !== "supported" ||
			!current
		)
			throw new Error("Hub transcript display settings are unavailable.");
		const normalized = normalizeConfig(config);
		const baseRevision =
			this.state.transcriptMobile.draft?.revision ?? current.revision;
		this.persistTranscriptDraft({
			baseRevision,
			config: normalized,
			writeUncertain: false,
		});
		this.publish({
			transcriptMobile: {
				...this.state.transcriptMobile,
				draft: { revision: baseRevision, config: normalized },
				conflict: current.revision > baseRevision,
				error: null,
			},
		});
	}

	async discardTranscriptDraft(): Promise<void> {
		if (
			this.disposed ||
			this.state.transcriptMobile.saving ||
			this.state.transcriptMobile.writeUncertain ||
			this.state.transcriptMobile.storageUnavailable
		)
			throw new Error(
				"Check current transcript settings before discarding the draft.",
			);
		this.transcriptDrafts?.remove();
		this.publish({
			transcriptMobile: {
				...this.state.transcriptMobile,
				draft: null,
				conflict: false,
				writeUncertain: false,
				error: null,
			},
		});
	}

	async rebaseTranscriptDraft(reviewedRevision: number): Promise<void> {
		const domain = this.state.transcriptMobile;
		if (
			this.disposed ||
			!domain.draft ||
			domain.storageUnavailable ||
			domain.loading ||
			domain.saving ||
			domain.writeUncertain ||
			domain.support !== "supported"
		)
			throw new Error(
				"Review the current transcript settings before rebasing.",
			);
		const current = domain.confirmed;
		if (!current || current.revision !== reviewedRevision)
			throw new Error("The reviewed transcript settings are stale.");
		const draft = domain.draft;
		this.persistTranscriptDraft({
			...draft,
			baseRevision: reviewedRevision,
			writeUncertain: false,
		});
		this.publish({
			transcriptMobile: {
				...this.state.transcriptMobile,
				draft: { ...draft, revision: current.revision },
				conflict: false,
				writeUncertain: false,
			},
		});
	}

	private persistTranscriptDraft(
		input: Omit<TranscriptDraftCheckpoint, "id">,
	): TranscriptDraftCheckpoint {
		try {
			const checkpoint = {
				...input,
				id: this.transcriptDrafts?.createId() ?? "memory",
			};
			this.transcriptDrafts?.save(checkpoint);
			return checkpoint;
		} catch {
			throw new Error("Could not save the transcript draft locally.");
		}
	}

	private restoreTranscriptDraft(): void {
		try {
			const checkpoint = this.transcriptDrafts?.load();
			if (this.disposed) return;
			this.publish({
				transcriptMobile: {
					...this.state.transcriptMobile,
					draft: checkpoint
						? { revision: checkpoint.baseRevision, config: checkpoint.config }
						: null,
					writeUncertain: checkpoint?.writeUncertain ?? false,
					storageUnavailable: false,
					error: null,
					conflict: checkpoint
						? (this.state.transcriptMobile.confirmed?.revision ?? -1) >
							checkpoint.baseRevision
						: false,
				},
			});
		} catch {
			this.publish({
				transcriptMobile: {
					...this.state.transcriptMobile,
					storageUnavailable: true,
					error:
						"Could not restore the saved transcript draft. Refresh settings to retry.",
				},
			});
		}
	}

	dispose(): void {
		if (this.disposed) return;
		this.disposed = true;
		this.generation += 1;
		this.unsubscribe();
		this.unsubscribeKeybindings();
		this.keybindings.dispose();
		this.listeners.clear();
	}
}
