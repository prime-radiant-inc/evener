import * as Crypto from "expo-crypto";
import { Storage } from "expo-sqlite/kv-store";
import { createContext, type ReactNode, useContext, useEffect, useMemo, useState } from "react";
import { resolveEffectiveConfig } from "@evener/appwire-client";
import type { AppwireClient, TranscriptDisplayConfigV1 } from "@evener/appwire-client";
import { bindNativePreferences } from "./bindNativePreferences";
import { useConnection } from "./ConnectionProvider";
import {
	type LegacyKeybindingDiscardResult,
	type LegacyKeybindingDraft,
	nativeLegacyKeybindingDraft,
	nativeTranscriptDrafts,
	nativeTranscriptLocal,
	rawStringDraftBackend,
} from "./nativePreferenceDrafts";
import type { NativePreferences, NativePreferencesSnapshot } from "./nativePreferences";

interface Preferences {
	hubId: string | null;
	model: NativePreferences | null;
	snapshot: NativePreferencesSnapshot | null;
	config: TranscriptDisplayConfigV1 | null;
	connected: boolean;
	/** The keybinding proposal a pre-ruling build left on this phone. It is
	 * read-only until the user applies or discards it; nothing here replays
	 * it automatically. Null before a hub is selected. */
	legacyKeybindingDraft: LegacyKeybindingDraft | null;
	/** Removes the legacy record through its compare-and-swap. Works without
	 * a live hub, because discarding touches only this phone. Null when no
	 * hub is selected. */
	discardLegacyKeybindingDraft(): LegacyKeybindingDiscardResult | null;
}
const Context = createContext<Preferences | null>(null);
// The production draft backend: rawStringDraftBackend over the real Storage,
// the SAME function tests exercise over a Map-backed fake (see
// nativePreferenceDrafts.test.ts's rawBytesBackend) - one implementation, not
// a parallel reimplementation of the parse/compare logic.
const backend = rawStringDraftBackend(Storage, () => Crypto.randomUUID());

export function NativePreferencesProvider({ children }: { children: ReactNode }) {
	const { activeProfile, client, state } = useConnection();
	const hubId = activeProfile?.id ?? null;
	const [bound, setBound] = useState<{
		hubId: string;
		client: AppwireClient;
		model: NativePreferences;
		snapshot: NativePreferencesSnapshot;
		config: TranscriptDisplayConfigV1 | null;
	} | null>(null);
	const [legacyKeybindingDraft, setLegacyKeybindingDraft] = useState<LegacyKeybindingDraft | null>(null);
	// One handle per selected hub: its discard() removes the exact record its
	// read() returned, so a record replaced meanwhile is refused, not deleted.
	const legacyKeybindingStorage = useMemo(
		() => (hubId === null ? null : nativeLegacyKeybindingDraft(hubId, backend)),
		[hubId],
	);
	useEffect(() => {
		setLegacyKeybindingDraft(legacyKeybindingStorage?.read() ?? null);
	}, [legacyKeybindingStorage, state]);
	const discardLegacyKeybindingDraft = (): LegacyKeybindingDiscardResult | null => {
		if (legacyKeybindingStorage === null) return null;
		const outcome = legacyKeybindingStorage.discard();
		setLegacyKeybindingDraft(legacyKeybindingStorage.read());
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
					setBound((previous) => {
						const transcript = snapshot.transcriptMobile;
						let config: TranscriptDisplayConfigV1 | null;
						if (transcript.support === "unsupported") config = null;
						else if (transcript.local != null || transcript.confirmed !== null)
							config = resolveEffectiveConfig({
								local: transcript.local ?? null,
								hub: transcript.confirmed,
								layout: "mobile",
							});
						else config = previous?.hubId === hubId ? previous.config : null;
						return { hubId, client, model, snapshot, config };
					});
				};
				unsubscribe = model.subscribe(update);
				update();
			},
			nativeTranscriptLocal(hubId, backend),
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
				legacyKeybindingDraft,
				discardLegacyKeybindingDraft,
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
