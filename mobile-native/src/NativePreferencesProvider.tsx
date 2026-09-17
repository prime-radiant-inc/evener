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
import type { AppwireClient, TranscriptDisplayConfigV1 } from "@evener/appwire-client";
import { bindNativePreferences } from "./bindNativePreferences";
import { draftBackend } from "./nativeDraftBackend";
import { useConnection } from "./ConnectionProvider";
import { LocalPreferencesController } from "./localPreferencesController";
import {
	nativeKeybindingDrafts,
	nativeTranscriptDrafts,
} from "./nativePreferenceDrafts";
import type { NativePreferences, NativePreferencesSnapshot } from "./nativePreferences";

interface Preferences {
	hubId: string | null;
	model: NativePreferences | null;
	snapshot: NativePreferencesSnapshot | null;
	config: TranscriptDisplayConfigV1 | null;
	connected: boolean;
	/** Discards one section's persisted draft, readable or not. Local-only, so
	 * it works with no connection: routes through the live model's own discard
	 * when one is bound (which also clears its in-memory draft/conflict state),
	 * or through `LocalPreferencesController` directly when none is - cold
	 * start, backgrounded, and reconnecting are exactly the states `bound`
	 * cannot cover, and the controller exists whether or not one ever binds.
	 * The no-model path names the record by the identity `LocalPreferencesController`
	 * kept from the LAST time something classified it - never a fresh reload,
	 * which could name a record another writer has since replaced (RoboRev
	 * round 17, eighth raise; round 18 extended the byte identity itself to a
	 * concurrent replacement that only compares semantically equal). A refused
	 * discard leaves nothing to project: `getSnapshot()` always re-reads
	 * storage fresh rather than caching a result, so `refreshLocalSnapshot`
	 * forcing a re-render shows whatever is actually stored now either way. */
	discardDraft(section: "transcript" | "keybindings"): Promise<void>;
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
	// One controller per hub, independent of `client`: it is what a screen
	// reads and discards through until (and after) a model is bound.
	const controller = useMemo(
		() => (hubId ? new LocalPreferencesController(hubId, backend) : null),
		[hubId],
	);
	// The controller has no store of its own to publish through; this forces a
	// re-render (and so a fresh `controller.getSnapshot()`) after a local-only
	// discard changes the disk record out of band.
	const [, refreshLocalSnapshot] = useState(0);
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
	// The connected store's domain is the richer superset once one exists
	// (confirmed hub values, conflict, an authoritative writeUncertain); the
	// local controller covers exactly the gap it cannot - no model bound yet,
	// or no longer bound - so `snapshot` is populated as soon as `hubId` is,
	// never only once a client has connected (round 14 claim b).
	const snapshot: NativePreferencesSnapshot | null =
		selected?.snapshot ?? controller?.getSnapshot() ?? null;
	const discardDraft = async (
		section: "transcript" | "keybindings",
	): Promise<void> => {
		if (!hubId) return;
		if (liveModel) {
			await (section === "transcript"
				? liveModel.discardTranscriptDraft()
				: liveModel.discardKeybindingsDraft());
			return;
		}
		if (!controller) return;
		// The port can throw; awaiting inside an async function turns that into a
		// rejection the caller's error handler already knows how to show. A
		// refusal (the record this controller classified is gone, replaced by
		// something else) has nothing to project: refreshLocalSnapshot forces a
		// re-render either way, and getSnapshot() always re-reads storage fresh.
		await (section === "transcript"
			? controller.discardTranscript()
			: controller.discardKeybindings());
		refreshLocalSnapshot((version) => version + 1);
	};
	return (
		<Context.Provider
			value={{
				hubId,
				model: liveModel,
				snapshot,
				config: selected?.config ?? null,
				connected: !!client && state === "ready" && selected?.client === client,
				discardDraft,
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
