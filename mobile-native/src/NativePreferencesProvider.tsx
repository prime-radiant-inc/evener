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
	discardStoredKeybindingDraft,
	isReadableKeybindingDraft,
} from "@evener/appwire-client";
import type {
	AppwireClient,
	DiscardStoredDraftResult,
	TranscriptDisplayConfigV1,
} from "@evener/appwire-client";
import { bindNativePreferences } from "./bindNativePreferences";
import { useConnection } from "./ConnectionProvider";
import {
	draftUnreadableAfterDiscard,
	nativeKeybindingDrafts,
	nativeTranscriptDrafts,
	parseDraftBytes,
} from "./nativePreferenceDrafts";
import type {
	NativePreferences,
	NativePreferencesSnapshot,
} from "./nativePreferences";

interface Preferences {
	hubId: string | null;
	model: NativePreferences | null;
	snapshot: NativePreferencesSnapshot | null;
	config: TranscriptDisplayConfigV1 | null;
	connected: boolean;
	/** Clears an unreadable keybindings draft record with no live model
	 * required - the store-free path discardStoredKeybindingDraft documents
	 * for a host with no connection to build one from (offline, or the
	 * connection dropped after the record was shown). Returns the discard's
	 * outcome (see DiscardStoredDraftResult); null when there is no hub to
	 * discard for. */
	discardUnreadableKeybindingsDraft(): DiscardStoredDraftResult | null;
}
const Context = createContext<Preferences | null>(null);
/** Synchronous compare cannot interleave with a newer model's checkpoint,
 * so deleteIf and replaceIf below share it as their one atomic check. An
 * unreadable record is handed back to us as the raw bytes `get` returned
 * (never re-parsed), so those bytes are what the comparison is against too;
 * a decoded checkpoint still compares by its own re-encoding as before. */
function matches(key: string, value: unknown): boolean {
	const stored = Storage.getItemSync(key);
	return stored !== null && (stored === value || stored === JSON.stringify(value));
}
const backend = {
	createId: () => Crypto.randomUUID(),
	get(key: string): unknown {
		const value = Storage.getItemSync(key);
		// Bytes this build cannot use as a record - including a stored JSON
		// null, distinct from no key at all - come back as the raw string
		// (parseDraftBytes), so the shared store's own decoder classifies them
		// as an unreadable RECORD (draftUnreadable, discardable) instead of
		// either a throw it can only read as a dead port, or a silent "no
		// record" that can never be discarded.
		return value === null ? null : parseDraftBytes(value);
	},
	set(key: string, value: unknown) {
		Storage.setItemSync(key, JSON.stringify(value));
	},
	delete(key: string) {
		Storage.removeItemSync(key);
	},
	deleteIf(key: string, value: unknown): boolean {
		if (!matches(key, value)) return false;
		Storage.removeItemSync(key);
		return true;
	},
	replaceIf(key: string, expected: unknown, next: unknown): boolean {
		if (!matches(key, expected)) return false;
		Storage.setItemSync(key, JSON.stringify(next));
		return true;
	},
};

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
	const discardUnreadableKeybindingsDraft = (): DiscardStoredDraftResult | null => {
		if (!hubId) return null;
		const storage = nativeKeybindingDrafts(hubId, backend);
		const outcome = discardStoredKeybindingDraft(storage, isReadableKeybindingDraft);
		// No live model to publish through (offline, or the connection
		// dropped after the record was shown) - the stale snapshot
		// NativePreferencesProvider otherwise keeps showing is updated here
		// directly, the same fields restoreDraft would publish on a live
		// store. Every outcome refreshes storageUnavailable/error, not only
		// "removed": talking to the port at all (any outcome) proves it is
		// reachable, and "absent" means the record the button named is
		// already gone. draftUnreadable is different: a "refused" outcome can
		// mean a concurrent writer replaced the record with something this
		// build CAN read, or with something STILL unreadable
		// (draftUnreadableAfterDiscard re-checks storage.load() to tell them
		// apart) - only the first makes the notice wrong to keep showing.
		const draftUnreadable = draftUnreadableAfterDiscard(outcome, isReadableKeybindingDraft(storage.load()));
		setBound((previous) =>
			previous?.hubId === hubId
				? {
						...previous,
						snapshot: {
							...previous.snapshot,
							keybindings: {
								...previous.snapshot.keybindings,
								draftUnreadable,
								storageUnavailable: false,
								error: null,
							},
						},
					}
				: previous,
		);
		return outcome;
	};
	return (
		<Context.Provider
			value={{
				hubId,
				model: selected?.client === client ? selected.model : null,
				snapshot: selected?.snapshot ?? null,
				config: selected?.config ?? null,
				connected: !!client && state === "ready" && selected?.client === client,
				discardUnreadableKeybindingsDraft,
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
