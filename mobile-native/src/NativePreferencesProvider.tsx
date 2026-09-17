import * as Crypto from "expo-crypto";
import { Storage } from "expo-sqlite/kv-store";
import {
	createContext,
	type ReactNode,
	useContext,
	useEffect,
	useMemo,
	useState,
} from "react";
import {
	type AppwireClient,
	type KeybindingDraftCheckpoint,
	type TranscriptDisplayConfigV1,
	type TranscriptDraftCheckpoint,
} from "@evener/appwire-client";
import { bindNativePreferences } from "./bindNativePreferences";
import { draftBackend } from "./nativeDraftBackend";
import { useConnection } from "./ConnectionProvider";
import { nativePreferenceDrafts, retainingDraftStorage } from "./nativePreferenceDrafts";
import {
	type NativePreferences,
	type NativePreferencesSnapshot,
	snapshotAfterLocalDiscard,
} from "./nativePreferences";

interface Preferences {
	hubId: string | null;
	model: NativePreferences | null;
	snapshot: NativePreferencesSnapshot | null;
	config: TranscriptDisplayConfigV1 | null;
	connected: boolean;
	/** Throws away a stored draft this build cannot read. Rejects rather than
	 * throwing, so a screen routes a storage failure through the same error
	 * handler as every other operation.
	 *
	 * This is the ONE path out of an unreadable record, and it must work in every
	 * state the app can be in when a user meets one. Reachability, by state:
	 *
	 * | state                  | this method                    | screen button          | had it used `run` |
	 * |------------------------|---------------------------------|------------------------|-------------------|
	 * | live model              | store.discardDraft              | calls this directly    | would have run    |
	 * | no model, backgrounded  | retaining wrapper's discardLastLoaded + snapshot | calls this directly | NO-OP |
	 * | no model, reconnecting  | retaining wrapper's discardLastLoaded + snapshot | calls this directly | NO-OP |
	 * | no session (hubId null) | nothing to clear                | no hub, no screen      | NO-OP             |
	 *
	 * The no-model rows name the record by the identity `retainingDraftStorage`
	 * kept from the LAST time something (a now-disposed model's own
	 * repository, most often) actually loaded it - never a fresh reload, which
	 * could name a record another writer has since replaced (RoboRev round 17,
	 * eighth raise). Every row with a record ends the same way ONLY on a
	 * CONFIRMED removal: record removed, UI unlocked - the store publishes
	 * that when it has one, and the snapshot projection below does it when it
	 * does not. discardLastLoaded reports whether it actually removed the
	 * record (RoboRev round 18): a refusal (replaced by something else since
	 * it was loaded) re-classifies internally but leaves the exposed snapshot
	 * untouched, so the section keeps reporting unreadable and locked rather
	 * than claiming a discard that did not happen - it unlocks on the next
	 * connection, which builds a fresh model over the new record. The last
	 * column is why BOTH screens call this method directly rather than
	 * through their `run` helper: `run` exists for operations that go through
	 * the shared store, so it requires a model, which is exactly what these
	 * states lack. */
	discardUnreadableDraft(section: "transcript" | "keybindings"): Promise<void>;
}
const Context = createContext<Preferences | null>(null);
const backend = draftBackend(Storage, () => Crypto.randomUUID());

export function NativePreferencesProvider({
	children,
}: {
	children: ReactNode;
}) {
	const { activeProfile, client, state } = useConnection();
	const hubId = activeProfile?.id ?? null;
	const [bound, setBound] = useState<{
		hubId: string;
		client: AppwireClient;
		model: NativePreferences;
		snapshot: NativePreferencesSnapshot;
		config: TranscriptDisplayConfigV1 | null;
	} | null>(null);
	// One retaining wrapper per section, per hub - not per client, and not
	// disposed with any one model built over it: a model's own repository
	// classifies and discards through THIS SAME object, so the identity it
	// most recently loaded survives that model's disposal (a client dropped,
	// a hub reconnect) for the no-model discard path below to use.
	const drafts = useMemo(
		() =>
			hubId
				? {
						transcript: retainingDraftStorage<TranscriptDraftCheckpoint>(
							nativePreferenceDrafts("transcript", hubId, backend),
						),
						keybindings: retainingDraftStorage<KeybindingDraftCheckpoint>(
							nativePreferenceDrafts("keybindings", hubId, backend),
						),
					}
				: null,
		[hubId],
	);
	useEffect(() => {
		if (!client || !hubId || !drafts) return;
		let unsubscribe = () => {};
		const dispose = bindNativePreferences(
			client,
			drafts.transcript,
			(model) => {
				unsubscribe();
				const update = () => {
					const snapshot = model.getSnapshot();
					setBound((previous) => ({
						hubId,
						client,
						model,
						snapshot,
						config:
							snapshot.transcriptMobile.support === "unsupported"
								? null
								: (snapshot.transcriptMobile.confirmed?.config ??
									(previous?.hubId === hubId ? previous.config : null)),
					}));
				};
				unsubscribe = model.subscribe(update);
				update();
			},
			drafts.keybindings,
		);
		return () => {
			unsubscribe();
			dispose();
		};
	}, [client, hubId, drafts]);
	const selected = bound?.hubId === hubId ? bound : null;
	const liveModel = selected?.client === client ? selected.model : null;
	const discardUnreadableDraft = async (
		section: "transcript" | "keybindings",
	): Promise<void> => {
		if (!hubId || !drafts) return;
		if (liveModel) {
			await (section === "transcript"
				? liveModel.discardTranscriptDraft()
				: liveModel.discardKeybindingsDraft());
			return;
		}
		// The port can throw; awaiting inside an async function turns that into a
		// rejection the caller's error handler already knows how to show.
		const removed = drafts[
			section === "transcript" ? "transcript" : "keybindings"
		].discardLastLoaded();
		// A refusal means the record this wrapper named is gone, replaced by
		// something else since it was loaded: nothing to project. The section
		// keeps reporting unreadable and locked until the next connection
		// builds a model over whatever is actually stored now.
		if (!removed) return;
		// No store to publish the result, so the exposed snapshot is projected
		// here: without this the record is gone but the section still reports
		// draftUnreadable and stays locked until the next connection.
		setBound((previous) =>
			previous && previous.hubId === hubId
				? {
						...previous,
						snapshot: snapshotAfterLocalDiscard(previous.snapshot, section),
					}
				: previous,
		);
	};
	return (
		<Context.Provider
			value={{
				hubId,
				model: liveModel,
				snapshot: selected?.snapshot ?? null,
				config: selected?.config ?? null,
				connected: !!client && state === "ready" && selected?.client === client,
				discardUnreadableDraft,
			}}
		>
			{children}
		</Context.Provider>
	);
}

export function useNativePreferences(): Preferences {
	const value = useContext(Context);
	if (!value) throw new Error("NativePreferencesProvider is required.");
	return value;
}
