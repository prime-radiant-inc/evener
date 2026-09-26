// The render-fence half of the recovery-hook contract, in a REAL
// ConversationScreen render: mounting the real screen - imported test-file
// only, screens.tsx untouched - must not construct a mutation runtime or
// register a target during the render pass, and the recovery surface mounted
// beside it must keep subscribe/read effect-owned, captured to the route's
// composite target key.
//
// Detectors, armed at the module boundaries every production path crosses:
// - the only production runtime construction is the getNativeMutationRuntime
//   singleton, whose openDatabaseSync("evener-mutations.db") is the one db the
//   screen must never open at any point of its lifecycle; the expo-sqlite mock
//   records every open with the phase it happened in (the screen's memoized
//   draft library legitimately opens evener-drafts.db during render).
// - the probe's runtime is the REAL NativeMutationRuntime over the testkit
//   double, with delegating wrappers on read/subscribeStorage/registerTarget/
//   start/discardRecovery that only record the phase of each call - every
//   behavior still executes through the real runtime.
import type { ComponentProps, ReactElement, ReactNode } from "react";
import { useEffect, useLayoutEffect, useState } from "react";
import { act, create, type ReactTestRenderer } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import {
	NativeMutationRuntime,
	type NativeMutationStorageListener,
	nativeMutationTargetKey,
} from "./nativeMutationRuntime";
import { renderedText } from "./renderNative.testkit";
import { ConversationScreen } from "./screens";
import { openSqliteSyncDouble } from "./sqliteSync.testkit";
import {
	type NativeMutationRecoveryProjection,
	useNativeMutationRecovery,
} from "./useNativeMutationRecovery";

const recorder = vi.hoisted(() => ({
	inRenderPass: true,
	opens: [] as { database: string; phase: "render" | "effect" }[],
}));
const harness = vi.hoisted(() => ({
	connection: {
		initialLocation: null,
		restorationError: null,
		profiles: [],
		activeProfile: null,
		client: null,
		state: "idle",
		fatal: false,
		error: null,
		loading: false,
		saveHub: async () => true,
		updateHub: async () => {},
		selectHub: () => {},
		removeHub: async () => {},
		disconnect: () => {},
		retry: () => {},
	},
	preferences: {
		hubId: null,
		model: null,
		snapshot: null,
		config: null,
		connected: false,
		offlineDraftUnreadable: false,
		offlineStorageUnavailable: false,
		discardUnreadableKeybindingsDraft: () => null,
	},
}));

vi.mock("react-native", async () => ({
	...(await import("./renderNative.testkit")).nativeModuleMock(),
	AccessibilityInfo: { announceForAccessibility: vi.fn() },
	ActionSheetIOS: { showActionSheetWithOptions: vi.fn() },
	AppState: {
		currentState: "active",
		addEventListener: () => ({ remove: () => {} }),
	},
	Image: "Image",
	Keyboard: { dismiss: vi.fn() },
	Linking: { openURL: vi.fn() },
	RefreshControl: "RefreshControl",
	StatusBar: "StatusBar",
}));
vi.mock("react-native-safe-area-context", () => ({
	SafeAreaView: "SafeAreaView",
	SafeAreaProvider: (props: { children?: ReactNode }) => props.children ?? null,
	useSafeAreaInsets: () => ({ top: 47, bottom: 34, left: 0, right: 0 }),
}));
// The enriched-markdown native component cannot load outside a device; as a
// host string its children render as passed, which is all the screen's
// timeline items need from it under this harness.
vi.mock("react-native-enriched-markdown", () => ({
	EnrichedMarkdownText: "EnrichedMarkdownText",
}));
vi.mock("@react-navigation/elements", () => ({ useHeaderHeight: () => 64 }));
vi.mock("@react-navigation/native", async () => {
	const { useEffect } = await import("react");
	return {
		useFocusEffect: (effect: () => void | (() => void)) =>
			useEffect(effect, []),
		useIsFocused: () => true,
	};
});
vi.mock("expo-clipboard", () => ({
	setStringAsync: vi.fn(async () => {}),
	getStringAsync: vi.fn(async () => ""),
}));
vi.mock("expo-crypto", () => ({
	randomUUID: () => "render-uuid",
	getRandomValues: (array: Uint8Array) => array,
}));
vi.mock("expo-sqlite", async () => {
	const { openSqliteSyncDouble } = await import("./sqliteSync.testkit");
	return {
		openDatabaseSync: (database: string) => {
			recorder.opens.push({
				database,
				phase: recorder.inRenderPass ? "render" : "effect",
			});
			return openSqliteSyncDouble().port;
		},
	};
});
vi.mock("expo-sqlite/kv-store", () => ({
	Storage: { getItemSync: () => null, setItemSync: () => {} },
}));
vi.mock("expo-file-system", () => ({
	File: class File {
		constructor(public uri: string) {}
	},
}));
vi.mock("expo-image-manipulator", () => ({
	ImageManipulator: {
		manipulateAsync: vi.fn(async () => ({ uri: "manipulated" })),
	},
	SaveFormat: { JPEG: "jpeg" },
}));
vi.mock("expo-image-picker", () => ({
	launchImageLibraryAsync: vi.fn(async () => ({ canceled: true, assets: [] })),
	UIImagePickerPreferredAssetRepresentationMode: { Current: "current" },
}));
vi.mock("expo-secure-store", () => ({
	getItemAsync: vi.fn(async () => null),
	setItemAsync: vi.fn(async () => {}),
	deleteItemAsync: vi.fn(async () => {}),
}));
vi.mock("./ConnectionProvider", () => ({
	useConnection: () => harness.connection,
}));
vi.mock("./NativePreferencesProvider", () => ({
	useNativePreferences: () => harness.preferences,
}));

const recordedCalls: {
	method: string;
	phase: "render" | "effect";
	args: unknown[];
}[] = [];
const subscriptionLifetime = { added: 0, removed: 0 };
let latestProjection: NativeMutationRecoveryProjection | null = null;

// The state-owner half of the detector: call from a component that owns
// state, and its state-triggered re-renders classify runtime calls the same
// way as the marker-level initial and route-change passes do.
function useRenderPassDetector() {
	recorder.inRenderPass = true;
	useLayoutEffect(() => {
		recorder.inRenderPass = false;
	});
}

// Delegating phase recorders: every wrapped call still executes through the
// real runtime's own method (the bound original), so the recorded behavior
// is the runtime's, not a reimplementation.
function instrumentRuntime(
	runtime: NativeMutationRuntime,
): NativeMutationRuntime {
	for (const method of [
		"read",
		"registerTarget",
		"start",
		"discardRecovery",
	] as const) {
		const original = (runtime[method] as (...args: never[]) => unknown).bind(
			runtime,
		);
		(runtime as unknown as Record<string, unknown>)[method] = (
			...args: never[]
		) => {
			recordedCalls.push({
				method,
				phase: recorder.inRenderPass ? "render" : "effect",
				args: [args[0]],
			});
			return original(...args);
		};
	}
	const subscribe = runtime.subscribeStorage.bind(runtime);
	(runtime as unknown as Record<string, unknown>).subscribeStorage = (
		listener: NativeMutationStorageListener,
	) => {
		recordedCalls.push({
			method: "subscribeStorage",
			phase: recorder.inRenderPass ? "render" : "effect",
			args: [],
		});
		subscriptionLifetime.added += 1;
		const stop = subscribe(listener);
		return () => {
			subscriptionLifetime.removed += 1;
			stop();
		};
	};
	return runtime;
}

function RecoveryProbe(props: {
	runtime: NativeMutationRuntime;
	hubId: string;
	targetRef: string;
}) {
	// The probe owns the hook's state, so its state-triggered re-renders
	// (every read or refresh landing) do not re-render PhaseMarker - the
	// state-owner arming below is what keeps those passes classified.
	useRenderPassDetector();
	latestProjection = useNativeMutationRecovery(
		props.runtime,
		nativeMutationTargetKey(props.hubId, props.targetRef),
	);
	return null;
}

type ConversationScreenProps = ComponentProps<typeof ConversationScreen>;

function conversationRoute(ref: string): ConversationScreenProps["route"] {
	return {
		key: `conversation-${ref}`,
		name: "Conversation",
		params: { hubId: "hub-1", ref, title: "Session" },
	} as unknown as ConversationScreenProps["route"];
}

const navigation = {
	isFocused: () => true,
	navigate: vi.fn(),
	push: vi.fn(),
	goBack: vi.fn(),
	setParams: vi.fn(),
	setOptions: vi.fn(),
} as unknown as ConversationScreenProps["navigation"];

// The render-pass detector, driven by the tree's actual render rather than
// a flag flipped around the act call. Two levels arm it: PhaseMarker for
// the whole tree's initial and route-change render passes, and each state
// owner (the probe below, and any control) for its own state-triggered
// re-renders - those re-render without the marker, but they propagate
// downward from the state owner, whose render body arms the detector for
// the whole pass. The layout effect disarms at commit, before any passive
// effect runs, so the hook's effect-owned subscribe and read are recorded
// "effect" while a render-time runtime call is recorded "render". A
// layout-effect call would classify conservatively as "render" - a fence
// failure, never a silent pass. Screen-state-only re-renders that neither
// the marker nor the probe joins are outside call classification (see the
// probe's own arming above); runtime CONSTRUCTION is policed
// phase-independently by the never-anywhere evener-mutations.db assertion.
function PhaseMarker(props: { children?: ReactNode }) {
	recorder.inRenderPass = true;
	useLayoutEffect(() => {
		recorder.inRenderPass = false;
	});
	return props.children ?? null;
}

async function seedRecoveryRow(
	runtime: NativeMutationRuntime,
	targetKey: string,
) {
	const record = await runtime.storage.enqueueIntent({
		targetRef: targetKey,
		method: "turn/queue",
		payload: { ref: "ref-1" },
		attachments: [],
		optimisticDisplay: { method: "turn/queue" },
	});
	const recovery = await runtime.storage.transferToRecovery(
		record.clientMutationId,
		"rejected",
		"daemon refused",
	);
	if (!recovery) throw new Error("seeding recovery failed");
	return recovery;
}

async function flush() {
	await act(async () => {
		await new Promise((resolve) => setTimeout(resolve, 0));
	});
}

it("renders the real ConversationScreen without constructing a runtime or registering a target during the render pass", async () => {
	const opened = openSqliteSyncDouble();
	let next = 0;
	const runtime = instrumentRuntime(
		new NativeMutationRuntime(opened.port, {
			createMutationId: () => `render-${++next}`,
			now: () => 1700000000000,
		}),
	);
	const firstKey = nativeMutationTargetKey("hub-1", "ref-1");
	await seedRecoveryRow(runtime, firstKey);

	const hubId = "hub-1";
	let targetRef = "ref-1";
	function tree(): ReactElement {
		return (
			<PhaseMarker>
				<ConversationScreen
					route={conversationRoute(targetRef)}
					navigation={navigation}
				/>
				<RecoveryProbe
					key={targetRef}
					runtime={runtime}
					hubId={hubId}
					targetRef={targetRef}
				/>
			</PhaseMarker>
		);
	}

	let renderer!: ReactTestRenderer;
	act(() => {
		renderer = create(tree());
	});

	// THE RENDER-PASS FENCE: no runtime construction, no target registration,
	// no subscribe and no read during the render pass - and the mutations
	// database is never opened at any phase of the screen's lifecycle. The
	// drafts database the screen's render pass legitimately opens is
	// classified "render" (the detector's own arming proof: it sees render
	// passes), while the hook's subscribe/read land "effect" only.
	expect(recordedCalls.filter((call) => call.phase === "render")).toEqual([]);
	expect(recorder.opens).toContainEqual({
		database: "evener-drafts.db",
		phase: "render",
	});
	expect(
		recordedCalls.find((call) => call.method === "subscribeStorage")?.phase,
	).toBe("effect");
	expect(recordedCalls.find((call) => call.method === "read")?.phase).toBe(
		"effect",
	);
	expect(recorder.opens.map((open) => open.database)).not.toContain(
		"evener-mutations.db",
	);
	expect(new Set(recorder.opens.map((open) => open.database))).toEqual(
		new Set(["evener-drafts.db"]),
	);

	// EFFECT-OWNED: subscribe and read happen in the mount effects, with the
	// read captured to the route's exact composite target key.
	expect(
		recordedCalls.filter((call) => call.method === "subscribeStorage"),
	).toHaveLength(1);
	const reads = recordedCalls.filter((call) => call.method === "read");
	expect(reads).toHaveLength(1);
	expect(reads[0].args).toEqual([firstKey]);
	expect(
		recordedCalls.filter(
			(call) => call.method === "registerTarget" || call.method === "start",
		),
	).toEqual([]);

	// The recovery surface really enumerated the seeded durable row through
	// the real runtime's read pipeline, inside the real screen's tree.
	await flush();
	expect(latestProjection?.loading).toBe(false);
	expect(
		latestProjection?.snapshot?.recovery.map((row) => row.clientMutationId),
	).toEqual(["render-1"]);
	// The screen rendered its actual content, not a stub.
	expect(renderedText(renderer)).toContain("Reconnect");

	// A route change remounts the recovery surface (screen generation): the
	// old generation's subscription is released and the new one reads the
	// new route's composite key only.
	targetRef = "ref-2";
	act(() => {
		renderer.update(tree());
	});
	await flush();

	const readsAfter = recordedCalls.filter((call) => call.method === "read");
	expect(readsAfter).toHaveLength(2);
	expect(readsAfter[1].args).toEqual([
		nativeMutationTargetKey("hub-1", "ref-2"),
	]);
	expect(subscriptionLifetime.added - subscriptionLifetime.removed).toBe(1);

	// A storage change for the OLD route's key never re-reads the new
	// generation, even though both generations shared this runtime.
	const secondKey = nativeMutationTargetKey("hub-1", "ref-2");
	const readsAfterRouteChange = recordedCalls.filter(
		(call) => call.method === "read",
	).length;
	await seedRecoveryRow(runtime, firstKey);
	await act(async () => {
		await runtime.discardRecovery("missing", firstKey);
	});
	await flush();
	const readsSinceRouteChange = recordedCalls
		.filter((call) => call.method === "read")
		.slice(readsAfterRouteChange);
	expect(readsSinceRouteChange).toEqual([]);
	expect(latestProjection?.snapshot?.recovery).toEqual([]);

	// The new route's own storage change refreshes the new generation.
	await seedRecoveryRow(runtime, secondKey);
	await act(async () => {
		await runtime.discardRecovery("missing", secondKey);
	});
	await flush();
	const readsSinceRefresh = recordedCalls
		.filter((call) => call.method === "read")
		.slice(readsAfterRouteChange);
	expect(readsSinceRefresh.map((call) => call.args[0])).toEqual([secondKey]);
	expect(
		latestProjection?.snapshot?.recovery.map((row) => row.clientMutationId),
	).toEqual(["render-3"]);

	// The render-pass fence held across the route-change render too.
	expect(recordedCalls.filter((call) => call.phase === "render")).toEqual([]);
	expect(recorder.opens.map((open) => open.database)).not.toContain(
		"evener-mutations.db",
	);
});

it("classifies a deliberate render-time runtime call as render-pass, proving the detector is armed", () => {
	const opened = openSqliteSyncDouble();
	let next = 0;
	const runtime = instrumentRuntime(
		new NativeMutationRuntime(opened.port, {
			createMutationId: () => `armed-${++next}`,
			now: () => 1700000000000,
		}),
	);
	const key = nativeMutationTargetKey("hub-1", "armed");
	recordedCalls.length = 0;
	// The exact violation the render fence forbids: a runtime read issued
	// from a component's render body. If the phase detector can catch this,
	// the main test's empty render-phase filter means something.
	function ViolatingProbe() {
		void runtime.read(key);
		return null;
	}
	act(() => {
		create(
			<PhaseMarker>
				<ViolatingProbe />
			</PhaseMarker>,
		);
	});
	const renderReads = recordedCalls.filter(
		(call) => call.method === "read" && call.phase === "render",
	);
	expect(renderReads).toHaveLength(1);
	expect(renderReads[0].args).toEqual([key]);
});

it("classifies a forbidden runtime call during a state-triggered rerender as render-pass", async () => {
	const opened = openSqliteSyncDouble();
	let next = 0;
	const runtime = instrumentRuntime(
		new NativeMutationRuntime(opened.port, {
			createMutationId: () => `rerender-${++next}`,
			now: () => 1700000000000,
		}),
	);
	const key = nativeMutationTargetKey("hub-1", "rerender");
	recordedCalls.length = 0;
	// The state owner arms the detector for its own state-triggered
	// re-render exactly as RecoveryProbe does: the re-render below the
	// state owner must not slip past the render-pass classification just
	// because no parent rendered.
	function ArmedViolatingChild() {
		const [tick, setTick] = useState(0);
		useEffect(() => {
			setTick(1);
		}, []);
		useRenderPassDetector();
		if (tick === 1) void runtime.read(key);
		return null;
	}
	act(() => {
		create(
			<PhaseMarker>
				<ArmedViolatingChild />
			</PhaseMarker>,
		);
	});
	await flush();
	const renderReads = recordedCalls.filter(
		(call) => call.method === "read" && call.phase === "render",
	);
	expect(renderReads).toHaveLength(1);
	expect(renderReads[0].args).toEqual([key]);
});
