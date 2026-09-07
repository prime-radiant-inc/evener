import type { AppwireClient } from "../../cmd/evener-hub/frontend/src/protocol/client";
import type { InitializeResponse } from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type { KeybindingDraftStorage } from "./keybindingDraftRepository";
import { NativePreferences } from "./nativePreferences";
import type { TranscriptDraftStorage } from "./preferenceDraftRepository";

export type PreferencesClient = Pick<
	AppwireClient,
	"connect" | "onReady" | "request" | "onNotification"
>;

// One model owns each negotiated connection. Reconnects restore the persisted proposal.
export function bindNativePreferences(
	client: PreferencesClient,
	storage: TranscriptDraftStorage,
	publish: (model: NativePreferences) => void,
	keybindingStorage?: KeybindingDraftStorage,
): () => void {
	let disposed = false;
	let model: NativePreferences | null = null;
	const ready = (hello: InitializeResponse) => {
		if (disposed) return;
		model?.dispose();
		model = new NativePreferences(
			client,
			hello.features,
			storage,
			keybindingStorage,
		);
		publish(model);
		void model.refresh();
	};
	const unsubscribe = client.onReady(ready);
	// connect shares the client's existing handshake and also covers an already-ready client.
	void client
		.connect()
		.then((hello) => {
			if (!model) ready(hello);
		})
		.catch(() => {
			// ConnectionProvider owns connection failure and retry presentation.
		});
	return () => {
		disposed = true;
		unsubscribe();
		model?.dispose();
	};
}
