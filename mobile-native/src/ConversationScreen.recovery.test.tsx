// The conversation screen's recovery surface, mounted and driven as the real
// ConversationScreen renders it: a message the hub refused is a ghost above
// the composer (spec 8.5 and 14, ruling 3), not an entry that opens a modal.
//
// What this pins, end to end, on the real screen:
// - the ghost is row-conditional: a connected screen whose durable recovery
//   snapshot is empty shows no ghost, and never a Recovery, Review status,
//   Check delivery or Reconnect control;
// - when this target's durable recovery row lands, a ghost shows its text and
//   why the hub refused it;
// - the #2247 contract: the screen folds BOTH the record-aware fence and the
//   composer converter (document.canRestoreRecoveredDraft) into Edit, so Edit
//   acts only while an eligible rejected record meets an empty, loaded
//   composer; an occupied composer leaves Edit disabled with the converter's
//   own hint, and Discard is actionable throughout.
import type { ComponentProps, ReactNode } from "react";
import { createElement } from "react";
import { act, type ReactTestRenderer } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import { getNativeMutationRuntime, nativeMutationTargetKey } from "./nativeMutationRuntime";
import { render, renderedText, screenConnection } from "./renderNative.testkit";
import { ConversationScreen } from "./screens";

const harness = vi.hoisted(() => ({
	connection: {} as Record<string, unknown>,
}));

// The root stack the screen sits in, read by useScreenInFront and
// screenInFront (screens.tsx). Kept at the screen's own route on top, so the
// screen is in front the way a freshly opened conversation really is - this
// suite isn't exercising sheet coverage or a pushed screen, unlike
// ConversationScreen.sheets.test.tsx.
const navigationState = vi.hoisted(() => ({
	state: { index: 0, routes: [] as { key: string; name: string }[] },
}));

// One sqlite double per database name, keyed the way the singletons open them,
// so the test can read the same rows the screen's own recovery hook reads.
const sqlite = vi.hoisted(() => ({ ports: new Map<string, unknown>() }));
// The app's outbox flush, which the screen asks to look when it lets go.
const outbox = vi.hoisted(() => ({ flush: vi.fn(async () => {}) }));
vi.mock("./outbox/nativeOutboxFlush", () => ({ outboxFlush: outbox }));

vi.mock("react-native", async () => {
	const mock = (await import("./renderNative.testkit")).nativeModuleMock();
	return {
		...mock,
		ActionSheetIOS: { showActionSheetWithOptions: vi.fn() },
		AppState: {
			currentState: "active",
			addEventListener: () => ({ remove: () => {} }),
		},
		Image: "Image",
		Linking: { openURL: vi.fn() },
		RefreshControl: "RefreshControl",
		StatusBar: "StatusBar",
		// The real Modal renders its children only while visible; the inert host
		// string would render them always, so the panel would look mounted even
		// with the modal closed. This stub keeps the screen's open/closed state
		// observable in the tree: no visible modal, no panel.
		Modal: (props: { visible?: boolean; children?: ReactNode }) =>
			props.visible ? createElement("Modal", null, props.children) : null,
	};
});
vi.mock("react-native-safe-area-context", () => ({
	SafeAreaView: "SafeAreaView",
	SafeAreaProvider: (props: { children?: ReactNode }) => props.children ?? null,
	useSafeAreaInsets: () => ({ top: 47, bottom: 34, left: 0, right: 0 }),
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));
vi.mock("react-native-gesture-handler", async () =>
	(await import("./renderNative.testkit")).gestureDetectorModuleMock(),
);
vi.mock("react-native-gesture-handler/ReanimatedSwipeable", async () =>
	(await import("./renderNative.testkit")).gestureHandlerModuleMock(),
);
vi.mock("expo-web-browser", () => ({}));
vi.mock("react-native-webview", () => ({ WebView: "WebView" }));
vi.mock("react-native-enriched-markdown", () => ({
	EnrichedMarkdownText: "EnrichedMarkdownText",
}));
vi.mock("@react-navigation/elements", () => ({ useHeaderHeight: () => 64 }));
vi.mock("@react-navigation/native", async () => {
	const { useEffect } = await import("react");
	return {
		useFocusEffect: (effect: () => void | (() => void)) => useEffect(effect, []),
		useNavigationState: <T,>(select: (state: typeof navigationState.state) => T) => select(navigationState.state),
	};
});
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));
vi.mock("expo-clipboard", () => ({
	setStringAsync: vi.fn(async () => {}),
	getStringAsync: vi.fn(async () => ""),
}));
vi.mock("expo-crypto", () => ({
	randomUUID: () => "recovery-entry-uuid",
	getRandomValues: (array: Uint8Array) => array,
}));
vi.mock("expo-sqlite", async () => {
	const { openSqliteSyncDouble } = await import("./sqliteSync.testkit");
	return {
		openDatabaseSync: (database: string) => {
			let port = sqlite.ports.get(database);
			if (!port) {
				port = openSqliteSyncDouble().port;
				sqlite.ports.set(database, port);
			}
			return port;
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
vi.mock("expo-image-manipulator", () => ({}));
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
	getState: () => navigationState.state,
	navigate: vi.fn(),
	push: vi.fn(),
	goBack: vi.fn(),
	setParams: vi.fn(),
	setOptions: vi.fn(),
} as unknown as ConversationScreenProps["navigation"];

async function flush() {
	await act(async () => {
		await new Promise((resolve) => setTimeout(resolve, 0));
	});
}

function pressables(tree: ReactTestRenderer) {
	return tree.root.findAll((node) => String(node.type) === "Pressable");
}

// The client keeps the conversation read pending: the screen stays connected
// with no session error (which would set deliveryConcern and hide the entry),
// and the recovery surface is exercised on its own.
function pendingClient() {
	return {
		// The screen now registers this client with the durable mutation
		// runtime, whose registerTarget reads state and subscribes to
		// onStateChange. The read itself stays pending, so the screen stays
		// connected without ever opening the conversation.
		state: "ready",
		onStateChange: () => () => {},
		request: () => new Promise<never>(() => {}),
		onNotification: () => () => {},
	};
}

it("asks the outbox flush to look once it lets go of its session, so a message still waiting there goes (ruling 17)", async () => {
	harness.connection = { ...screenConnection(pendingClient(), "ready"), error: null, disconnect: () => {} };
	const ref = "ref-leave";
	const runtime = getNativeMutationRuntime();
	navigationState.state = { index: 0, routes: [conversationRoute(ref)] };
	outbox.flush.mockClear();
	const tree = render(<ConversationScreen route={conversationRoute(ref)} navigation={navigation} />);
	await flush();
	expect(runtime.targetClient("hub-1", ref)).toBeDefined();
	expect(outbox.flush).not.toHaveBeenCalled();

	act(() => tree.unmount());

	expect(runtime.targetClient("hub-1", ref)).toBeUndefined();
	expect(outbox.flush).toHaveBeenCalledTimes(1);
});

it("shows a refused message as a ghost above the composer, row-conditional under the #2247 contract", async () => {
	harness.connection = {
		...screenConnection(pendingClient(), "ready"),
		error: null,
		disconnect: () => {},
	};

	const ref = "ref-1";
	const targetKey = nativeMutationTargetKey("hub-1", ref);
	// The screen's own recovery hook acquires this singleton once connected;
	// reading it here is the same runtime the screen reads.
	const runtime = getNativeMutationRuntime();
	navigationState.state = { index: 0, routes: [conversationRoute(ref)] };

	const tree = render(<ConversationScreen route={conversationRoute(ref)} navigation={navigation} />);
	await flush();

	// PHASE 1 - empty snapshot, connected: no ghost. Positive control first:
	// the real connected screen rendered, so the absent ghost is a wiring
	// verdict and not an empty tree.
	const composer = () =>
		tree.root
			.findAll((node) => String(node.type) === "TextInput")
			.find((node) => node.props.accessibilityLabel === "Message");
	expect(composer()).toBeDefined();
	// A live connection says nothing about itself (spec 14).
	expect(renderedText(tree)).not.toContain("Connected");
	expect(renderedText(tree)).not.toContain("Couldn't send this");
	const retired = ["Recovery", "Review status", "Check delivery", "Reconnect", "Review error"];
	const expectNoRetiredControls = () => {
		for (const label of retired) {
			expect(pressables(tree).find((n) => n.props.accessibilityLabel === label)).toBeUndefined();
			expect(renderedText(tree)).not.toContain(label);
		}
	};
	expectNoRetiredControls();

	// PHASE 2 - this target's durable recovery row lands and the runtime
	// publishes the storage change the hook follows.
	const record = await runtime.storage.enqueueIntent({
		targetRef: targetKey,
		method: "turn/queue",
		payload: { ref, input: [{ type: "text", text: "recover this message" }] },
		attachments: [],
		optimisticDisplay: { method: "turn/queue" },
	});
	const recovery = await runtime.storage.transferToRecovery(record.clientMutationId, "rejected", "daemon refused");
	if (!recovery) throw new Error("seeding the recovery row failed");
	await act(async () => {
		// A zero-row discard is the runtime's own publish: it notifies storage
		// listeners (the screen's hook re-reads) without touching the row.
		await runtime.discardRecovery("no-such-row", targetKey);
	});
	await flush();

	const text = renderedText(tree);
	expect(text).toContain("recover this message");
	expect(text).toContain("Couldn't send this · daemon refused");
	expectNoRetiredControls();

	// PHASE 3 - the #2247 converter gate at screen level. With an empty,
	// loaded composer the eligible record meets the converter's true, so Edit
	// is enabled, and it puts the text back in the composer.
	const edit = () => pressables(tree).find((n) => n.props.accessibilityLabel === "Edit");
	const discard = () => pressables(tree).find((n) => n.props.accessibilityLabel === "Discard");
	expect(edit()?.props.accessibilityState).toMatchObject({ disabled: false });
	expect(discard()?.props.accessibilityState).toMatchObject({ disabled: false });
	await act(async () => edit()?.props.onPress());
	await flush();
	expect(composer()?.props.value).toBe("recover this message");

	// The composer is occupied now: the row still offers Edit (the record
	// fence passes) but the converter withholds, so Edit is disabled with the
	// converter's own hint, and Discard is still actionable.
	act(() => composer()?.props.onChangeText("a draft already in progress"));
	await flush();
	expect(edit()?.props.accessibilityState).toMatchObject({ disabled: true });
	expect(renderedText(tree)).toContain("Clear or send your current draft to restore this message.");
	expect(discard()?.props.accessibilityState).toMatchObject({ disabled: false });

	// PHASE 4 - Discard retires exactly this row, and its ghost goes.
	await act(async () => discard()?.props.onPress());
	await flush();
	await flush();
	expect(renderedText(tree)).not.toContain("recover this message");
	expect(await runtime.storage.getRecovery(record.clientMutationId)).toBeUndefined();
});
