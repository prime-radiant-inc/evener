import type {
	AppwireClient,
	InitializeResponse,
	KeybindingDraftStorage,
	TranscriptDraftStorage,
} from "@evener/appwire-client";
import { NativePreferences } from "./nativePreferences";

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
