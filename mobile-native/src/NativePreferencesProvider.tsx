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
	DRAFT_RESTORE_FAILED_MESSAGE,
	errorText,
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
	rawStringDraftBackend,
	readDraftOutcome,
	readDraftOutcomeWithValue,
} from "./nativePreferenceDrafts";
import type {
	NativePreferences,
	NativePreferencesSnapshot,
} from "./nativePreferences";
import { keybindingsErrorMessage } from "./nativePreferences";

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
// The production draft backend: rawStringDraftBackend over the real
// Storage, the SAME function tests exercise over a Map-backed fake (see
// nativePreferenceDrafts.test.ts's rawBytesBackend) - one implementation,
// not a parallel reimplementation of the parse/compare logic.
const backend = rawStringDraftBackend(Storage, () => Crypto.randomUUID());

type DraftReadWithValue = ReturnType<typeof readDraftOutcomeWithValue>;

function reconcileRetainedDraftProjection(
	snapshot: NativePreferencesSnapshot,
	result: DraftReadWithValue,
): NativePreferencesSnapshot {
	const keybindings = snapshot.keybindings;
	let draft = keybindings.draft;
	let writeUncertain = keybindings.writeUncertain;
	let storageUnavailable = keybindings.storageUnavailable;
	let draftUnreadable = keybindings.draftUnreadable;
	let conflict = keybindings.conflict;
	let draftError = keybindings.draftError;
	switch (result.outcome) {
		case "storageUnavailable":
			storageUnavailable = true;
			break;
		case "absent":
			draft = null;
			writeUncertain = false;
			storageUnavailable = false;
			draftUnreadable = false;
			conflict = false;
			draftError = null;
			break;
		case "unreadable":
			draft = null;
			writeUncertain = false;
			storageUnavailable = true;
			draftUnreadable = true;
			conflict = false;
			draftError = DRAFT_RESTORE_FAILED_MESSAGE;
			break;
		case "readable": {
			const fields = decodeKeybindingDraftFields(result.value);
			draft = fields.draft;
			writeUncertain = fields.writeUncertain;
			storageUnavailable = false;
			draftUnreadable = false;
			conflict =
				draft !== null &&
				keybindings.confirmed !== null &&
				draft.revision !== keybindings.confirmed.revision;
			draftError = null;
			break;
		}
	}
	const error = keybindingsErrorMessage(
		draftError,
		keybindings.hubError,
		keybindings.loadError,
	);
	if (
		keybindings.draft === draft &&
		keybindings.writeUncertain === writeUncertain &&
		keybindings.storageUnavailable === storageUnavailable &&
		keybindings.draftUnreadable === draftUnreadable &&
		keybindings.conflict === conflict &&
		keybindings.draftError === draftError &&
		keybindings.error === error
	)
		return snapshot;
	return {
		...snapshot,
		keybindings: {
			...keybindings,
			draft,
			writeUncertain,
			storageUnavailable,
			draftUnreadable,
			conflict,
			draftError,
			error,
		},
	};
}

/** The live-model nudge's own rejection, projected onto the retained
 * snapshot: only the error seam changes. A refusal (the live store's discard
 * gate - say, a write the lost connection parked `writeUncertain`) or its
 * own port failure is not a storage observation, so every record-derived
 * field keeps what the last read classified; what the rejection adds is that
 * the live model was not told about the storage change and still disagrees
 * with the projection on screen. The message is the store's own fixed copy -
 * both the gate and the draft-port wrapper throw fixed words, never a raw
 * storage error - routed through the same keybindingsErrorMessage seam a
 * failed read surfaces through. */
function retainedNudgeRejectionProjection(
	snapshot: NativePreferencesSnapshot,
	message: string,
): NativePreferencesSnapshot {
	const keybindings = snapshot.keybindings;
	if (keybindings.draftError === message) return snapshot;
	return {
		...snapshot,
		keybindings: {
			...keybindings,
			draftError: message,
			error: keybindingsErrorMessage(
				message,
				keybindings.hubError,
				keybindings.loadError,
			),
		},
	};
}

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
			if (probedHubId.current !== hubId) {
				setOfflineDraftUnreadable(false);
			}
			probedHubId.current = hubId;
			return;
		}
		probedHubId.current = hubId;
		setOfflineStorageUnavailable(false);
		setOfflineDraftUnreadable(outcome === "unreadable");
	}, [hubId, state]);
	// The one retained-snapshot patch seam: a projection lands only while
	// that snapshot is not owned by a live, connected model of the current
	// client (whose own publishes own the display), and only for the
	// connection it was computed against.
	const patchRetainedSnapshot = (
		expectedClient: AppwireClient | undefined,
		project: (snapshot: NativePreferencesSnapshot) => NativePreferencesSnapshot,
	) => {
		if (!hubId) return;
		setBound((previous) => {
			if (
				!previous ||
				previous.hubId !== hubId ||
				previous.client !== expectedClient ||
				(previous.client === client && state === "ready")
			)
				return previous;
			const snapshot = project(previous.snapshot);
			return snapshot === previous.snapshot ? previous : { ...previous, snapshot };
		});
	};
	const reconcileRetainedDraft = (
		expectedClient: AppwireClient | undefined,
		result: DraftReadWithValue,
	) =>
		patchRetainedSnapshot(expectedClient, (snapshot) =>
			reconcileRetainedDraftProjection(snapshot, result),
		);
	useEffect(() => {
		if (!hubId || !bound || (bound.client === client && state === "ready")) return;
		const expectedClient = bound.client;
		const result = readDraftOutcomeWithValue(
			nativeKeybindingDrafts(hubId, backend),
			isReadableKeybindingDraft,
		);
		reconcileRetainedDraft(expectedClient, result);
	}, [hubId, client, state, bound?.client]);
	const discardUnreadableKeybindingsDraft = (): DiscardStoredDraftResult | "storageUnavailable" | null => {
		if (!hubId) return null;
		const storage = nativeKeybindingDrafts(hubId, backend);
		const expectedClient = bound?.client;
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
			reconcileRetainedDraft(expectedClient, { outcome, value: undefined });
			return "storageUnavailable";
		}
		// A second, independent re-read (see draftUnreadableAfterDiscard): the
		// CURRENT record, not the one discardStoredDraft last saw, is what
		// decides whether the notice still belongs up.
		const currentResult = readDraftOutcomeWithValue(storage, isReadableKeybindingDraft);
		const { outcome: current } = currentResult;
		if (current === "storageUnavailable") {
			setOfflineStorageUnavailable(true);
			reconcileRetainedDraft(expectedClient, currentResult);
			// The follow-up read cannot say what is there now - preserve the
			// notice only if the discard itself was refused (something is still
			// there this build could not remove); a "removed"/"absent" outcome
			// already cleared or confirmed nothing was there, so a SUBSEQUENT
			// read failure must not resurrect the notice.
			setOfflineDraftUnreadable(outcome === "refused");
			return outcome;
		}
		setOfflineStorageUnavailable(false);
		reconcileRetainedDraft(expectedClient, currentResult);
		// Kept in sync regardless of whether `bound` exists: the cold-offline
		// signal below (offlineDraftUnreadable) is what the screen falls back
		// to before any model has published a snapshot, and a discard can
		// happen in exactly that state.
		setOfflineDraftUnreadable(draftUnreadableAfterDiscard(current));
		// A readable current record is a replacement the store-free CAS refused;
		// nudging a model that already restored it would discard its own readable
		// classification. A model still reporting storageUnavailable is the
		// unreadable-record case (or a genuine storage failure, which its own
		// discard gate safely refuses), so let the store reclassify through its
		// existing recovery seam.
		const liveKeybindings = bound?.model.getSnapshot().keybindings;
		if (
			bound?.hubId === hubId &&
			bound.client === client &&
			(current !== "readable" || liveKeybindings?.storageUnavailable === true)
		) {
			// A live model exists for this hub, and its OWN keybindings store
			// classifies this same record independently, from the same storage
			// this store-free path just changed - patching a COPY of its
			// snapshot here would only be overwritten the next time that store
			// publishes (bindNativePreferences' subscribe below always replaces
			// `bound.snapshot` outright with model.getSnapshot()), since the
			// live store never heard about the change. Nudging its own discard
			// keeps ITS state honest instead: the record it classified is now
			// gone (or replaced), so its CAS refuses and it re-reads storage
			// itself (see keybindingsStore.ts's discardDraft "refused, so
			// reclassify" branch) - the exact outcome this call needs, reached
			// through the store's own recovery path rather than a duplicate of
			// it. Fire-and-forget: discardKeybindingsDraft is synchronous work
			// wrapped in a promise, and this path's own return value already
			// reflects what discardStoredKeybindingDraft found - but the
			// NUDGE's own rejection is a fact that return value does not
			// carry. The store's discard can refuse (its gate: a write the
			// lost connection parked writeUncertain, a genuine port failure
			// its own gate safely refuses) or fail (its port, a failure the
			// store publishes itself), and either way the record-derived
			// projection just published above would otherwise read as
			// all-clear while the live model still disagrees with it. So the
			// rejection surfaces through the same retained error path a
			// failed read does - the store's own fixed words, via errorText,
			// never a raw storage error.
			bound.model.discardKeybindingsDraft().catch((error: unknown) => {
				patchRetainedSnapshot(expectedClient, (snapshot) =>
					retainedNudgeRejectionProjection(snapshot, errorText(error)),
				);
			});
		}
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
