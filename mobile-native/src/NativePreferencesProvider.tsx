import * as Crypto from "expo-crypto";
import { Storage } from "expo-sqlite/kv-store";
import {
	createContext,
	type ReactNode,
	useContext,
	useEffect,
	useRef,
	useState,
} from "react";
import {
	decodeKeybindingDraftFields,
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
	matchesStoredBytes,
	nativeKeybindingDrafts,
	nativeTranscriptDrafts,
	parseDraftBytes,
	readDraftOutcome,
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
	/** A locally stored keybindings draft is present but unreadable, checked
	 * independently of `snapshot` (which stays null until a ready client
	 * publishes a model - see bindNativePreferences). A cold offline start
	 * never reaches that point, so this is what lets the "Discard unreadable
	 * draft" action render before any hub has answered. Superseded by
	 * `snapshot.keybindings.draftUnreadable` the moment a model publishes. */
	offlineDraftUnreadable: boolean;
	/** The offline draft probe or the store-free discard's own re-read hit a
	 * genuine port failure (Storage.getItemSync threw) rather than an
	 * unreadable record - the same distinction restoreDraft draws on a live
	 * store. Nothing about `offlineDraftUnreadable` is known when this is
	 * true; it is left at whatever it last was rather than guessed at. */
	offlineStorageUnavailable: boolean;
	/** Clears an unreadable keybindings draft record with no live model
	 * required - the store-free path discardStoredKeybindingDraft documents
	 * for a host with no connection to build one from (offline, or the
	 * connection dropped after the record was shown). Returns the discard's
	 * outcome (see DiscardStoredDraftResult); "storageUnavailable" when the
	 * port itself failed (distinct from a genuine outcome, and distinct from
	 * `null`'s "there was no hub to discard for" - a caller that conflated
	 * the two could not tell a failed discard from an action that never
	 * ran); null when there is no hub to discard for. */
	discardUnreadableKeybindingsDraft(): DiscardStoredDraftResult | "storageUnavailable" | null;
}
const Context = createContext<Preferences | null>(null);
/** Synchronous compare cannot interleave with a newer model's checkpoint,
 * so deleteIf and replaceIf below share it as their one atomic check. See
 * matchesStoredBytes for why a byte-for-byte re-encoding compare is not
 * enough on its own. */
function matches(key: string, value: unknown): boolean {
	return matchesStoredBytes(Storage.getItemSync(key), value);
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
	insertIfAbsent(key: string, value: unknown): boolean {
		if (Storage.getItemSync(key) !== null) return false;
		Storage.setItemSync(key, JSON.stringify(value));
		return true;
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
	const [offlineDraftUnreadable, setOfflineDraftUnreadable] = useState(false);
	const [offlineStorageUnavailable, setOfflineStorageUnavailable] = useState(false);
	// The hub the two flags above were last probed for - a genuine port
	// failure on a NEW hub says nothing about whether THIS hub's record is
	// unreadable, and even less about a PREVIOUS hub's; without this, a
	// failed probe right after switching hubs would keep showing the old
	// hub's classification under the new hub's name.
	const probedHubId = useRef<string | null>(null);
	useEffect(() => {
		// Runs independently of connection state (see the field's own comment
		// on Preferences.offlineDraftUnreadable): a cold offline start never
		// fires bindNativePreferences' `ready` callback, so this is the only
		// place the local record gets inspected before a model exists. Also
		// re-runs on a connectivity change (`state`), not only a hub switch: a
		// record that becomes corrupt while offline with no model would
		// otherwise never surface the recovery action until `hubId` itself
		// changed.
		if (!hubId) {
			probedHubId.current = null;
			setOfflineDraftUnreadable(false);
			setOfflineStorageUnavailable(false);
			return;
		}
		const outcome = readDraftOutcome(nativeKeybindingDrafts(hubId, backend), isReadableKeybindingDraft);
		if (outcome === "storageUnavailable") {
			// A genuine port failure, not a record this build cannot decode -
			// nothing about draftUnreadable is known, so it is left alone
			// rather than guessed at, the same posture restoreDraft takes - but
			// only for a re-probe of the SAME hub (state flapping); a failure on
			// a NEWLY selected hub has no earlier classification of ITS OWN to
			// preserve.
			setOfflineStorageUnavailable(true);
			if (probedHubId.current !== hubId) setOfflineDraftUnreadable(false);
			probedHubId.current = hubId;
			return;
		}
		probedHubId.current = hubId;
		setOfflineStorageUnavailable(false);
		setOfflineDraftUnreadable(outcome === "unreadable");
	}, [hubId, state]);
	const discardUnreadableKeybindingsDraft = (): DiscardStoredDraftResult | "storageUnavailable" | null => {
		if (!hubId) return null;
		const storage = nativeKeybindingDrafts(hubId, backend);
		// discardStoredKeybindingDraft degrades a genuine port failure (not a
		// record this build cannot decode) to "storageUnavailable" itself -
		// see its own docs - so no outer catch is needed here.
		const outcome = discardStoredKeybindingDraft(storage, isReadableKeybindingDraft);
		if (outcome === "storageUnavailable") {
			// Distinct from `null` ("no hub to discard for"): the port was
			// reached and reachable enough to attempt the discard, but failed.
			// Returning the same `null` either way would make a failed discard
			// indistinguishable from an action that never ran.
			setOfflineStorageUnavailable(true);
			return "storageUnavailable";
		}
		// A second, independent re-read (see draftUnreadableAfterDiscard): the
		// CURRENT record, not the one discardStoredDraft last saw, is what
		// decides whether the notice still belongs up. This may also throw.
		const current = readDraftOutcome(storage, isReadableKeybindingDraft);
		if (current === "storageUnavailable") {
			setOfflineStorageUnavailable(true);
			// The follow-up read cannot say what is there now - preserve the
			// notice only if the discard itself was refused (something is still
			// there this build could not remove); a "removed"/"absent" outcome
			// already cleared or confirmed nothing was there, so a SUBSEQUENT
			// read failure must not resurrect the notice.
			setOfflineDraftUnreadable(outcome === "refused");
			return outcome;
		}
		setOfflineStorageUnavailable(false);
		const draftUnreadable = draftUnreadableAfterDiscard(current);
		// Kept in sync regardless of whether `bound` exists: the cold-offline
		// signal below (offlineDraftUnreadable) is what the screen falls back
		// to before any model has published a snapshot, and a discard can
		// happen in exactly that state.
		setOfflineDraftUnreadable(draftUnreadable);
		// The follow-up read already has whatever record is there now - a
		// "refused" outcome whose replacement decodes as valid (current is
		// "readable") is a NEW draft the store-free path just discovered, and
		// restoreDraft would publish its draft/writeUncertain fields on a live
		// store; decodeKeybindingDraftFields does the same decode here so this
		// path is not left at stale nulls until a reconnect.
		const { draft, writeUncertain } =
			current === "readable" ? decodeKeybindingDraftFields(storage.load()) : { draft: null, writeUncertain: false };
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
								draft,
								writeUncertain,
							},
						},
					}
				: previous,
		);
		return outcome;
	};
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
	return (
		<Context.Provider
			value={{
				hubId,
				model: selected?.client === client ? selected.model : null,
				snapshot: selected?.snapshot ?? null,
				config: selected?.config ?? null,
				connected: !!client && state === "ready" && selected?.client === client,
				offlineDraftUnreadable,
				offlineStorageUnavailable,
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
