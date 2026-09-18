import * as Crypto from "expo-crypto";
import { Storage } from "expo-sqlite/kv-store";
import {
	createContext,
	type ReactNode,
	useContext,
	useEffect,
	useState,
} from "react";
import type { AppwireClient, TranscriptDisplayConfigV1 } from "@evener/appwire-client";
import { bindNativePreferences } from "./bindNativePreferences";
import { useConnection } from "./ConnectionProvider";
import {
	nativeKeybindingDrafts,
	nativeTranscriptDrafts,
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
}
const Context = createContext<Preferences | null>(null);
/** Synchronous compare cannot interleave with a newer model's checkpoint,
 * so deleteIf and replaceIf below share it as their one atomic check. */
function matches(key: string, value: unknown): boolean {
	return Storage.getItemSync(key) === JSON.stringify(value);
}
const backend = {
	createId: () => Crypto.randomUUID(),
	get(key: string): unknown {
		const value = Storage.getItemSync(key);
		return value === null ? null : JSON.parse(value);
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
