import * as Crypto from "expo-crypto";
import { Storage } from "expo-sqlite/kv-store";
import {
	createContext,
	type ReactNode,
	useContext,
	useEffect,
	useState,
} from "react";
import type { AppwireClient } from "../../cmd/evener-hub/frontend/src/protocol/client";
import type { TranscriptDisplayConfigV1 } from "../../cmd/evener-hub/frontend/src/transcriptDisplay/config";
import { bindNativePreferences } from "./bindNativePreferences";
import { useConnection } from "./ConnectionProvider";
import { nativeTranscriptDrafts } from "./nativePreferenceDrafts";
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
}
const Context = createContext<Preferences | null>(null);
const backend = {
	createId: () => Crypto.randomUUID(),
	get(key: string): unknown {
		const value = Storage.getItemSync(key);
		return value === null ? null : JSON.parse(value);
	},
	set(key: string, value: unknown) {
		Storage.setItemSync(key, JSON.stringify(value));
	},
	delete(key: string) {
		Storage.removeItemSync(key);
	},
	deleteIf(key: string, value: unknown) {
		// Synchronous compare/remove cannot interleave with a newer model's checkpoint.
		if (Storage.getItemSync(key) === JSON.stringify(value))
			Storage.removeItemSync(key);
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
