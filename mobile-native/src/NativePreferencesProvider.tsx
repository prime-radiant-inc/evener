import * as Crypto from "expo-crypto";
import { Storage } from "expo-sqlite/kv-store";
import {
	createContext,
	type ReactNode,
	useContext,
	useEffect,
	useState,
} from "react";
import {
	type AppwireClient,
	discardStoredKeybindingDraft,
	discardStoredTranscriptDraft,
	type TranscriptDisplayConfigV1,
} from "@evener/appwire-client";
import { bindNativePreferences } from "./bindNativePreferences";
import { draftBackend } from "./nativeDraftBackend";
import { useConnection } from "./ConnectionProvider";
import {
	nativeKeybindingDrafts,
	nativeTranscriptDrafts,
} from "./nativePreferenceDrafts";
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
	/** Throws away an unreadable stored draft. Local-only, so it works with no
	 * connection and no live model: the client goes away while backgrounded or
	 * reconnecting, and that is exactly when a user is stuck behind such a
	 * record. With a live model the store does it and publishes the state; with
	 * none the port is cleared directly. */
	/** Throws away a stored draft this build cannot read. Rejects rather than
	 * throwing, so a screen routes a storage failure through the same error
	 * handler as every other operation.
	 *
	 * This is the ONE path out of an unreadable record, and it must work in every
	 * state the app can be in when a user meets one. Reachability, by state:
	 *
	 * | state                  | this method            | screen button          | via `run` |
	 * |------------------------|------------------------|------------------------|-----------|
	 * | live model             | store.discardDraft     | calls this method      | would run |
	 * | no model, backgrounded | port clear + snapshot  | calls this method      | NO-OP     |
	 * | no model, reconnecting | port clear + snapshot  | calls this method      | NO-OP     |
	 * | no session (hubId null)| nothing to clear       | no hub, no screen      | NO-OP     |
	 *
	 * Every row with a record ends the same way: record removed, UI unlocked -
	 * the store publishes that when it has one, and the snapshot projection
	 * below does it when it does not. The `run` column is why neither screen's
	 * unreadable-discard goes through `run`: its first guard early-exits on a
	 * null model, which is exactly the state this exists for. */
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
	useEffect(() => {
		if (!client || !hubId) return;
		let unsubscribe = () => {};
		const dispose = bindNativePreferences(
			client,
			nativeTranscriptDrafts(hubId, backend),
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
			nativeKeybindingDrafts(hubId, backend),
		);
		return () => {
			unsubscribe();
			dispose();
		};
	}, [client, hubId]);
	const selected = bound?.hubId === hubId ? bound : null;
	const liveModel = selected?.client === client ? selected.model : null;
	const discardUnreadableDraft = async (
		section: "transcript" | "keybindings",
	): Promise<void> => {
		if (!hubId) return;
		if (liveModel) {
			await (section === "transcript"
				? liveModel.discardTranscriptDraft()
				: liveModel.discardKeybindingsDraft());
			return;
		}
		// The port can throw; awaiting inside an async function turns that into a
		// rejection the caller's error handler already knows how to show.
		if (section === "transcript")
			discardStoredTranscriptDraft(nativeTranscriptDrafts(hubId, backend));
		else discardStoredKeybindingDraft(nativeKeybindingDrafts(hubId, backend));
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
