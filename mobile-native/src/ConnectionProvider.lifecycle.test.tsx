// ConnectionProvider's wiring for New session's creation stores (#3104): a
// removed hub's store is retired with the rest of that hub's data, and a start
// bound to a client the connection has left is let go. The provider is real;
// only its native edges (secure storage, the connection itself, device
// storage) are stand-ins.
import { act } from "react-test-renderer";
import { beforeEach, expect, it, vi } from "vitest";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { ConnectionProvider, useConnection } from "./ConnectionProvider";
import type { DraftStorage } from "./newSession";
import { bindCreation, creationStore } from "./newSession/creations";
import { render } from "./renderNative.testkit";

const harness = vi.hoisted(() => ({
	secure: new Map<string, string>(),
	client: null as unknown,
	ids: 0,
	connection: null as unknown,
}));
vi.mock("react-native", async () => ({
	...(await import("./renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("expo-secure-store", () => ({
	getItemAsync: async (key: string) => harness.secure.get(key) ?? null,
	setItemAsync: async (key: string, value: string) => void harness.secure.set(key, value),
	deleteItemAsync: async (key: string) => void harness.secure.delete(key),
}));
vi.mock("expo-crypto", () => ({ randomUUID: () => `hub-${++harness.ids}` }));
vi.mock("./hubConnection", () => ({
	useHubConnection: () => ({ client: harness.client, state: harness.client ? "ready" : "closed", fatal: false }),
}));
vi.mock("./nativeDrafts", () => ({ drafts: { removeHub: () => {} } }));
vi.mock("./nativeLocation", () => ({ locations: { read: () => null, write: () => {} } }));
vi.mock("./nativeReaderPosition", () => ({ readerPositions: { removeHub: () => {} } }));
vi.mock("./nativeOrganization", () => ({ removeOrganizationData: () => {} }));

const noDrafts: DraftStorage = () => ({ read: () => null, write: () => {}, clear: () => {} });
const settle = () =>
	act(async () => {
		await new Promise((resolve) => setTimeout(resolve, 0));
	});
/** A client whose requests never answer, as a connection that has gone quiet. */
const quietClient = () => ({ request: () => new Promise(() => {}) }) as unknown as ConversationClientLike;

function Probe() {
	harness.connection = useConnection();
	return null;
}
async function mount() {
	const tree = render(
		<ConnectionProvider>
			<Probe />
		</ConnectionProvider>,
	);
	await settle();
	return {
		tree,
		connection: () => harness.connection as ReturnType<typeof useConnection>,
		rerender: async () => {
			await act(async () => {
				tree.update(
					<ConnectionProvider>
						<Probe />
					</ConnectionProvider>,
				);
			});
			await settle();
		},
	};
}

beforeEach(() => {
	harness.secure.clear();
	harness.client = null;
});

it("retires a removed hub's New session store with the rest of its data", async () => {
	const app = await mount();
	await act(async () => {
		await app.connection().saveHub({ name: "magic-kingdom", origin: "https://magic-kingdom:9180", token: "t" });
	});
	const hubId = app.connection().profiles[0]?.id ?? "";
	const store = creationStore(hubId, noDrafts);
	await act(async () => app.connection().removeHub(hubId));
	await settle();
	expect(store.getState().retired).toBe(true);
	expect(creationStore(hubId, noDrafts)).not.toBe(store);
	app.tree.unmount();
});

it("lets go of a start bound to a client the connection has left", async () => {
	const first = quietClient();
	harness.client = first;
	const app = await mount();
	const store = creationStore("hub-held", noDrafts);
	bindCreation(store, first);
	await act(async () => {
		await store.getState().setCwd("/project", false);
		store.getState().setPrompt("go");
		void store.getState().submit();
	});
	await settle();
	expect(store.getState().submitting).toBe(true);
	// The connection comes back as a new client.
	harness.client = quietClient();
	await app.rerender();
	expect(store.getState()).toMatchObject({ submitting: false, unconfirmedCreation: true });
	app.tree.unmount();
});
