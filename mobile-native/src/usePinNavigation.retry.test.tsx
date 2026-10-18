// The pin navigation hook owns the full reconciliation's retry: a failed
// first reconcile (the initial catalog read failing) must not strand the pin
// screens on a manual "Check again" while the app sits idle. The catalog
// pages' own readRetry re-fetches rows, but only a successful reconciliation
// read clears the action's uncertainty and publishes the readback, so the
// hook re-runs reconcile() itself on the same capped backoff the rest of the
// app's failed reads use.
import { afterEach, expect, it, vi } from "vitest";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { wireSnapshot } from "@evener/appwire-client/testing/navigation";
import { reconnectDelay } from "./hubConnection";
import { usePinNavigation } from "./usePinNavigation";
import { renderHook, unmountMountedTrees } from "./renderNative.testkit";

const harness = vi.hoisted(() => ({
	connection: {
		client: null as unknown,
		activeProfile: { id: "hub" },
		state: "ready" as const,
	},
}));

vi.mock("react-native", async () => (await import("./renderNative.testkit")).nativeModuleMock());
vi.mock("expo-sqlite/kv-store", () => ({
	Storage: {
		getItemSync: (key: string) => {
			if (journalFailures > 0 && key.includes("navigation-action")) {
				// eslint-disable-next-line no-console
				console.log("journal read fails:", key);
				journalFailures--;
				throw new Error("storage unavailable");
			}
			return kv.get(key) ?? null;
		},
		setItemSync: (key: string, value: string) => void kv.set(key, value),
		removeItemSync: (key: string) => void kv.delete(key),
		getAllKeysSync: () => [...kv.keys()],
	},
}));
vi.mock("expo-crypto", () => ({ randomUUID: () => "uuid" }));
vi.mock("./ConnectionProvider", () => ({ useConnection: () => harness.connection }));
vi.mock("@react-navigation/native", () => ({ useIsFocused: () => true }));

/** The kv-store double the journal store reads. */
const kv = new Map<string, string>();
/** How many journal reads fail before one works. */
let journalFailures = 0;

afterEach(() => {
	unmountMountedTrees();
	vi.useRealTimers();
	harness.connection.client = null;
	kv.clear();
	journalFailures = 0;
});

/** The navigation client the recovery tests prove out: a location read that
 * fails its first call, a decodable catalog, and counted navigation reads. */
function client(catalogFailures: number) {
	let catalogReads = 0;
	const reads = { count: 0 };
	const fake = {
		reads,
		onNotification: () => () => {},
		request: async (method: string, params: Record<string, unknown>) => {
			reads.count++;
			if (method === "evener/navigation/read" && params.resource === "location") {
				return wireSnapshot(
					params as never,
					{
						top_level: true,
						session: {
							ref: "session",
							host_id: "local",
							session_id: "id",
							title: "Session",
							project: "p",
							state: "idle",
							kind: "session",
						},
					},
					'"location"',
					3,
					"g",
				);
			}
			catalogReads++;
			if (catalogReads <= catalogFailures) throw new Error("catalog unavailable");
			return wireSnapshot(
				params as never,
				{
					pin_sections: [{ id: "focus", name: "Focus", count: 1 }],
					remaining: 0,
				},
				'"catalog"',
				2,
				"g",
			);
		},
	} as unknown as ConversationClientLike & { reads: { count: number } };
	return fake;
}

it("re-runs a failed reconciliation on its own until the read lands", async () => {
	vi.useFakeTimers();
	const fake = client(1);
	harness.connection.client = fake;
	const hook = renderHook(() => usePinNavigation("hub"));
	await vi.advanceTimersByTimeAsync(0);
	expect(hook.result.current.action?.uncertain).toBe(true);
	// The failed reconcile re-arms on the capped backoff; the second read
	// succeeds, so the uncertainty clears without any manual press.
	await vi.advanceTimersByTimeAsync(1000);
	await vi.advanceTimersByTimeAsync(10);
	expect(hook.result.current.action?.uncertain).toBe(false);
	expect(hook.result.current.confirmed).toBe(true);
	hook.unmount();
});

it("keeps re-arming when the journal read itself fails, recovering without a press", async () => {
	vi.useFakeTimers();
	// The journal store's read throws twice before it works: the pin action
	// stays uncertain with no pending, and the hook's own retry must keep
	// asking rather than stranding the controls.
	journalFailures = 2;
	harness.connection.client = client(0);
	const hook = renderHook(() => usePinNavigation("hub"));
	await vi.advanceTimersByTimeAsync(0);
	expect(hook.result.current.action?.uncertain).toBe(true);
	// However many attempts the failing journal eats, the hook's own retry
	// keeps asking, and the flow recovers with no manual press.
	await vi.advanceTimersByTimeAsync(reconnectDelay(1) + reconnectDelay(2) + reconnectDelay(3));
	expect(hook.result.current.action?.uncertain).toBe(false);
	expect(hook.result.current.confirmed).toBe(true);
	hook.unmount();
});
