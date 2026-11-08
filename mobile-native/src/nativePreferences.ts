import {
	configFingerprint,
	createKeybindingsStore,
	createTranscriptDisplayStore,
	type FeatureSet,
	type KeybindingsOverrides,
	type KeybindingsRule,
	type KeybindingsStore,
	type KeybindingsStoreState,
	type KeybindingsSupport,
	keybindingsSupport,
	type TranscriptDisplayClient,
	type TranscriptDisplayConfigV1,
	type TranscriptDisplayStoreState,
	transcriptDisplaySupport,
	type TranscriptDraftStorage,
} from "@evener/appwire-client";
import { checkedKeybindingChange } from "./keybindingRules";

type NativeFeatures = Pick<FeatureSet, "keybindingsSettings" | "transcriptDisplaySettings">;

type NativeTranscriptLocalStorage = import("./nativePreferenceDrafts").NativeTranscriptLocalStorage;
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
	/** The port answered but what it held could not be read - see the shared
	 * store's field of the same name. transcriptMobile is that store's
	 * projection: an unreadable record is distinct from an absent one, and
	 * discarding it is what clears the notice. */
	draftUnreadable: boolean;
}

/** The confirmed payload as the shared store holds it: its rule list is the
 * store's own (identity-stable until the next applied payload), read-only. */
export type ConfirmedKeybindings = Omit<KeybindingsOverrides, "rules"> & {
	rules: readonly KeybindingsRule[];
};

export type KeybindingsPreferenceState = Pick<
	PreferenceState<ConfirmedKeybindings>,
	"support" | "loading" | "confirmed" | "error" | "conflict"
> & {
	hubError: string | null;
	loadError: string | null;
};

export type TranscriptMobilePreferenceState = PreferenceState<{
	revision: number;
	config: TranscriptDisplayConfigV1;
}> & {
	/** The phone's local override for this hub, retained when an older hub
	 * cannot store a divergent presentation preference. Effective over the
	 * confirmed hub default. Absent on a snapshot built before it existed. */
	local?: TranscriptDisplayConfigV1 | null;
};

export interface NativePreferencesSnapshot {
	keybindings: KeybindingsPreferenceState;
	transcriptMobile: TranscriptMobilePreferenceState;
}

const KEYBINDINGS_LOAD_ERROR_MESSAGE =
	"The hub could not load its saved shortcuts. Repair the hub settings file before editing.";

export const HUB_UNCONFIRMED_MESSAGE = "The hub request could not be confirmed.";

export function keybindingsErrorMessage(hubError: string | null, loadError: string | null): string | null {
	if (loadError !== null) return KEYBINDINGS_LOAD_ERROR_MESSAGE;
	return hubError === null ? null : HUB_UNCONFIRMED_MESSAGE;
}

// The keybinding domain is a projection of the shared store's state: the
// confirmed payload is the store's revision and raw rules (the rule array is
// the store's own, a fresh one per applied payload and identity-stable
// otherwise, which the shortcut screen's preview memo keys on), and a
// hub-sourced failure surfaces as fixed copy (a raw client error may carry a
// token or a path).
function keybindingsDomain(state: KeybindingsStoreState): KeybindingsPreferenceState {
	return {
		support: state.hubSupport,
		loading: state.hubLoading,
		confirmed: state.loaded
			? {
					version: 1,
					revision: state.revision,
					rules: state.rawOverrides,
					...(state.loadError === null ? {} : { loadError: state.loadError }),
				}
			: null,
		error: keybindingsErrorMessage(state.hubError, state.loadError),
		conflict: state.conflict !== null,
		hubError: state.hubError,
		loadError: state.loadError,
	};
}

// The transcript domain is the same projection over the shared transcript
// display store's mobile layout. That store owns the hub get/patch, the draft
// checkpoint and the ready-generation fence; this half only maps its fields
// onto the screen's own snapshot. The default's `generation` stamp is
// stripped (the screen's type has no field for it), and a hub-sourced failure
// is fixed copy. An unconfirmed write is `writeUncertain`; its message is that
// fact, so the screen's error slot and its write-uncertain notice never
// disagree.
function transcriptErrorMessage(state: TranscriptDisplayStoreState): string | null {
	if (state.draftError !== null) return state.draftError;
	return state.hubError !== null || state.writeUncertain ? HUB_UNCONFIRMED_MESSAGE : null;
}

function transcriptDomain(
	state: TranscriptDisplayStoreState,
	local: TranscriptDisplayConfigV1 | null,
): TranscriptMobilePreferenceState {
	return {
		support: state.hubSupport,
		loading: state.hubLoading,
		saving: state.saving,
		confirmed: state.hub.mobile ?? null,
		local,
		draft:
			state.draft === null || state.draft.layout !== "mobile"
				? null
				: { revision: state.draft.revision, config: state.draft.config },
		error: transcriptErrorMessage(state),
		conflict: state.draftConflict,
		writeUncertain: state.writeUncertain,
		storageUnavailable: state.storageUnavailable,
		draftUnreadable: state.draftUnreadable,
	};
}

export class NativePreferences {
	private state: NativePreferencesSnapshot;
	private readonly transcriptLocalStorage: NativeTranscriptLocalStorage | undefined;
	private transcriptLocal: TranscriptDisplayConfigV1 | null;
	private readonly listeners = new Set<() => void>();
	private readonly keybindings: KeybindingsStore;
	private readonly transcripts: ReturnType<typeof createTranscriptDisplayStore>;
	private readonly unsubscribeKeybindings: () => void;
	private readonly unsubscribeTranscripts: () => void;
	private disposed = false;

	constructor(
		client: TranscriptDisplayClient,
		features: NativeFeatures,
		transcriptStorage?: TranscriptDraftStorage,
		transcriptLocalStorage?: NativeTranscriptLocalStorage,
	) {
		// One store per model, like one model per ready connection: the
		// features are the handshake's, so support is final and the one ready
		// generation lasts until dispose. The keybindings half is the
		// template; transcriptMobile is the same projection, over
		// createTranscriptDisplayStore rather than a hand-rolled state
		// machine - the shared store owns the hub reads/writes, the draft
		// checkpoint and the generation fence.
		this.keybindings = createKeybindingsStore({
			client,
		});
		this.keybindings.setSupport(keybindingsSupport(features));
		this.keybindings.beginReadyGeneration();
		this.transcripts = createTranscriptDisplayStore({
			client,
			drafts: transcriptStorage,
		});
		this.transcripts.setSupport(transcriptDisplaySupport(features));
		this.transcripts.beginReadyGeneration();
		this.transcriptLocalStorage = transcriptLocalStorage;
		this.transcriptLocal = transcriptLocalStorage?.load() ?? null;
		this.state = {
			keybindings: keybindingsDomain(this.keybindings.getState()),
			transcriptMobile: transcriptDomain(this.transcripts.getState(), this.transcriptLocal),
		};
		this.unsubscribeKeybindings = this.keybindings.subscribe((state) =>
			this.publish({ keybindings: keybindingsDomain(state) }),
		);
		this.unsubscribeTranscripts = this.transcripts.subscribe((state) =>
			this.publish({ transcriptMobile: transcriptDomain(state, this.transcriptLocal) }),
		);
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

	async refresh(): Promise<void> {
		if (this.disposed) return;
		// Both stores fence their own reads by the ready generation and the
		// support they were told.
		await Promise.all([
			this.keybindings.getState().refreshOverrides(),
			this.transcripts.getState().refreshHubDefaults(),
		]);
	}

	/** The phone's one shortcut mutation: the edit PATCHes the hub and the
	 * confirmed rules land in this snapshot - no draft and no second save.
	 * The rule set is composed when the write EXECUTES, against the store's
	 * then-current raw rules: a set composed at press time would race an
	 * in-flight edit it queues behind and silently drop that edit's change. */
	editKeybinding(actionId: string, chord?: string | null): Promise<KeybindingsOverrides> {
		return this.keybindings
			.getState()
			.patchOverrides(() => checkedKeybindingChange(this.keybindings.getState().rawOverrides, actionId, chord));
	}

	async saveTranscript(
		config?: TranscriptDisplayConfigV1,
	): Promise<{ revision: number; config: TranscriptDisplayConfigV1 }> {
		// The requested config survives the save only as the acknowledged one;
		// read it before saveDraft settles and clears the draft.
		const requested = config ?? this.transcripts.getState().draft?.config ?? null;
		const acknowledged = await this.transcripts.getState().saveDraft("mobile", config);
		this.recordTranscriptOverrides(requested, acknowledged.config);
		return acknowledged;
	}

	/** A hub that predates thinkingOpenByDefault acknowledges a divergent
	 * custom with its four-field reading, so the presentation preference
	 * survives only as this phone's local override - never silently reset.
	 * A hub that carries the field acknowledges the request exactly, which
	 * clears the override. */
	private recordTranscriptOverrides(
		requested: TranscriptDisplayConfigV1 | null,
		acknowledged: TranscriptDisplayConfigV1,
	): void {
		if (requested === null) return;
		if (configFingerprint(requested) === configFingerprint(acknowledged)) {
			this.setTranscriptLocal(null);
			return;
		}
		const requestedCustom = requested.content.kind === "custom" ? requested.content : null;
		const acknowledgedCustom = acknowledged.content.kind === "custom" ? acknowledged.content : null;
		if (
			requestedCustom !== null &&
			acknowledgedCustom !== null &&
			requestedCustom.thinkingOpenByDefault !== acknowledgedCustom.thinkingOpenByDefault
		) {
			this.setTranscriptLocal(requested);
		}
	}

	private setTranscriptLocal(config: TranscriptDisplayConfigV1 | null): void {
		if (this.transcriptLocal === null && config === null) return;
		this.transcriptLocal = config;
		this.transcriptLocalStorage?.save(config);
		this.publish({
			transcriptMobile: transcriptDomain(this.transcripts.getState(), this.transcriptLocal),
		});
	}

	async editTranscript(config: TranscriptDisplayConfigV1): Promise<void> {
		this.transcripts.getState().editDraft("mobile", config);
	}

	async discardTranscriptDraft(): Promise<void> {
		this.transcripts.getState().discardDraft();
	}

	async rebaseTranscriptDraft(reviewedRevision: number): Promise<void> {
		this.transcripts.getState().rebaseDraft(reviewedRevision);
	}

	dispose(): void {
		if (this.disposed) return;
		this.disposed = true;
		this.unsubscribeKeybindings();
		this.unsubscribeTranscripts();
		this.keybindings.dispose();
		this.transcripts.dispose();
		this.listeners.clear();
	}
}
