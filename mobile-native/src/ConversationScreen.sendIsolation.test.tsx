// Regression for #4901: the mutation runtime is memoized for a whole unit-test
// file, so a suite that starts it and never stops it leaves its 2000ms
// MutationOutbox discovery interval armed across test cases. A lost admission
// reply leaves a durable row submitting, and that leaked interval re-sends it
// through whichever test next registers the same target: the hub then records
// two raw turn/* calls for one logical admission.
//
// The interval is faked before the runtime is first created, so the test owns
// every discovery tick. Only setInterval/clearInterval are faked: settle() and
// the screen's own deferred work still run on real timers.
import { nativePlatform } from "./conversationPlatform.testkit";
import type { ComponentProps } from "react";
import { act, type ReactTestRenderer } from "react-test-renderer";
import { afterAll, afterEach, expect, it, vi } from "vitest";
import type { AppwireClientLike, Thread, ThreadReadResponse } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { getNativeMutationRuntime, nativeMutationTargetKey } from "./nativeMutationRuntime";
import { pressable, render, screenConnection, settle, unmountMountedTrees } from "./renderNative.testkit";
import { ConversationScreen } from "./screens";
import { NativeConversationSessionsProvider } from "./session/NativeConversationSessionsProvider";

const harness = vi.hoisted(() => ({ connection: {} as Record<string, unknown> }));
const navigationState = vi.hoisted(() => ({
	state: { index: 0, routes: [] as { key: string; name: string }[] },
}));

vi.mock("@react-navigation/elements", () => ({ useHeaderHeight: () => 64 }));
vi.mock("@react-navigation/native", async () => {
	const { useEffect } = await import("react");
	return {
		useFocusEffect: (effect: () => void | (() => void)) => useEffect(effect, []),
		useNavigationState: <T,>(select: (state: typeof navigationState.state) => T) => select(navigationState.state),
		useNavigation: () => navigation,
		usePreventRemove: () => {},
	};
});
vi.mock("./ConnectionProvider", () => ({ useConnection: () => harness.connection }));
vi.mock("./alerts/alertsContext", async (importOriginal) => ({
	...(await importOriginal<typeof import("./alerts/alertsContext")>()),
	useAlertedRecently: () => [],
	useNextUsed: () => vi.fn(),
}));
vi.mock("./NativePreferencesProvider", () => ({
	useNativePreferences: () => ({
		hubId: null,
		model: null,
		snapshot: null,
		config: null,
		connected: false,
		offlineDraftUnreadable: false,
		offlineStorageUnavailable: false,
		discardUnreadableKeybindingsDraft: () => null,
	}),
}));

const navigation = {
	isFocused: () => true,
	getState: () => navigationState.state,
	navigate: vi.fn(),
	push: vi.fn(),
	replace: vi.fn(),
	pop: vi.fn(),
	goBack: vi.fn(),
	setParams: vi.fn(),
	setOptions: vi.fn(),
} as unknown as ComponentProps<typeof ConversationScreen>["navigation"];

const CAPABILITIES = {
	send: true,
	steer: true,
	interrupt: true,
	queue: true,
	compact: false,
	clear: false,
	forkFromTurn: false,
	shutdown: false,
	changeModel: false,
	changeVisionModel: false,
	sharedNotes: false,
	goal: false,
	rename: false,
};

function thread(ref: string, status: "idle" | "active"): Thread {
	return {
		id: `thread-${ref}`,
		sessionId: `session-${ref}`,
		preview: "",
		ephemeral: false,
		modelProvider: "anthropic",
		createdAt: 0,
		updatedAt: 0,
		status: { type: status },
		cwd: "",
		cliVersion: "",
		source: "",
		turns: [
			{
				id: "turn_1",
				status: "failed",
				itemsView: "default",
				error: { message: "go test exited 1" },
				items: [
					{ id: "u-1", turnId: "turn_1", type: "userMessage", status: "completed", text: "my original question" },
				],
			},
		],
		evener: {
			ref,
			instanceId: "instance",
			capabilities: CAPABILITIES,
			queue: { revision: 0, depth: 0, preview: [], texts: [], ids: [] },
		},
	} as unknown as Thread;
}

type Recorded = { method: string; params: Record<string, unknown> };

function lostAcceptanceClient(served: Thread) {
	const requests: Recorded[] = [];
	const client = Object.assign(new FakeClient("ready") as unknown as AppwireClientLike, {
		request: async (method: string, params: Record<string, unknown>) => {
			requests.push({ method, params });
			if (method === "thread/read") return { thread: served };
			if (method.startsWith("turn/")) throw new Error("connection lost after admission");
			if (method === "model/list") return { data: [] };
			return {};
		},
	});
	return {
		client,
		requests,
		turnStarts: () => requests.filter((request) => request.method === "turn/start"),
	};
}

function SessionScreen(props: ComponentProps<typeof ConversationScreen>) {
	return (
		<NativeConversationSessionsProvider>
			<ConversationScreen {...props} />
		</NativeConversationSessionsProvider>
	);
}

async function mount(served: Thread, hub: ReturnType<typeof lostAcceptanceClient>) {
	const ref = String((served as unknown as { evener: { ref: string } }).evener.ref);
	harness.connection = {
		...screenConnection(hub.client, "ready"),
		profiles: [{ id: "hub-1", name: "Work hub", origin: "https://hub.test" }],
		error: null,
		disconnect: () => {},
	};
	const route = {
		key: `conversation-${ref}`,
		name: "Conversation",
		params: { hubId: "hub-1", ref, title: "Session" },
	} as unknown as ComponentProps<typeof ConversationScreen>["route"];
	navigationState.state = { index: 0, routes: [route] };
	let tree!: ReactTestRenderer;
	await act(async () => {
		tree = render(<SessionScreen route={route} navigation={navigation} />);
	});
	await settle();
	return { tree, ref };
}

async function press(tree: ReactTestRenderer, label: string) {
	const target = pressable(tree, label);
	if (!target) throw new Error(`no pressable labelled ${label}`);
	act(() => target.props.onPress());
	await settle();
}

async function flushDeferredWork() {
	for (let turn = 0; turn < 4; turn += 1) await settle();
}

// The runtime's discovery interval must be faked before the runtime exists.
// getNativeMutationRuntime() is first called by the first mount below.
vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });

afterEach(async () => {
	unmountMountedTrees();
	// The isolation under test: a memoized runtime must not outlive the case,
	// or its discovery interval re-sends this case's durable rows under a
	// later case's client.
	await getNativeMutationRuntime().stop();
});

afterAll(() => {
	vi.useRealTimers();
	for (const database of nativePlatform.sqliteDatabases) database.close();
});

const REF = "ref-retry-isolation";

it("leaves a lost turn/start acceptance as one admission with one clientMutationId", async () => {
	const served = thread(REF, "idle");
	const hub = lostAcceptanceClient(served);
	const { tree } = await mount(served, hub);
	await press(tree, "Retry");
	const turnStarts = hub.turnStarts();
	expect(turnStarts).toHaveLength(1);
	expect(new Set(turnStarts.map((request) => request.params.clientMutationId)).size).toBe(1);
	const rows = await getNativeMutationRuntime().storage.listOutbox(nativeMutationTargetKey("hub-1", REF));
	expect(rows).toHaveLength(1);
	expect(rows[0]?.clientMutationId).toBe(turnStarts[0]?.params.clientMutationId);
});

// A later case may register the same target through the same memoized
// runtime. If the case above leaked its discovery interval, advancing the
// clock here re-sends that case's durable row, which the new client records
// as a second admission of the same clientMutationId.
it("does not re-send the earlier case's row from a leaked discovery interval", async () => {
	const runtime = getNativeMutationRuntime();
	const target = nativeMutationTargetKey("hub-1", REF);
	// The case above left one durable row, and its teardown must have cleared
	// the interval that would keep rediscovering it.
	expect(vi.getTimerCount()).toBe(0);
	const rows = await runtime.storage.listOutbox(target);
	expect(rows.map((row) => row.clientMutationId)).toHaveLength(1);
	const clientMutationId = rows[0]?.clientMutationId;

	const requests: Recorded[] = [];
	const client = Object.assign(new FakeClient("ready") as unknown as AppwireClientLike, {
		request: async (method: string, params: Record<string, unknown>) => {
			requests.push({ method, params });
			throw new Error("connection lost after admission");
		},
	});
	const unregister = runtime.registerTarget("hub-1", REF, client);
	const lease = runtime.beginAuthoritativeRead("hub-1", REF, client);
	if (!lease) throw new Error("no authoritative read lease");
	// A mutation-authoritative snapshot leaves the submitting row in place
	// instead of parking it blockedUnknown, as a hub after a restart does.
	const response: ThreadReadResponse = {
		thread: {
			...thread(REF, "idle"),
			evener: { ...thread(REF, "idle").evener, mutationStateAuthoritative: true },
		},
	};
	await runtime.reconcileAuthoritativeRead(lease, response);
	await flushDeferredWork();
	await act(async () => {
		await vi.advanceTimersByTimeAsync(2_000);
	});
	await flushDeferredWork();
	unregister();

	expect(requests).toEqual([]);
	expect((await runtime.storage.listOutbox(target)).map((row) => row.clientMutationId)).toEqual([clientMutationId]);
});
