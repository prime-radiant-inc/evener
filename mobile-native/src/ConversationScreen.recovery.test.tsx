// The screen-level integration proof the #2223 panel follow-up recorded as
// missing: the conversation screen's recovery entry and the recovery modal it
// opens, mounted and driven as the real ConversationScreen renders them - not
// the pure gate (shouldOfferRecoveryEntry) and not the panel alone, which
// MutationRecoveryPanel.test.tsx already cover.
//
// What this pins, end to end, on the real screen:
// - the entry is row-conditional: a connected screen whose durable recovery
//   snapshot is empty renders NO Recovery affordance and no recovery modal
//   (the host Modal stub is visibility-aware, so the panel is only in the tree
//   when the modal is actually open);
// - when this target's durable recovery row lands, the screen renders the
//   Recovery entry, pressing it opens the recovery modal, and the modal mounts
//   the real MutationRecoveryPanel with that row's label, reason and text;
// - the #2247 contract, not the pre-#2247 one: the screen folds BOTH the
//   record-aware fence and the composer converter (document
//   .canRestoreRecoveredDraft) into the panel's actions, so Restore renders
//   only while an eligible rejected record meets an empty, loaded composer; an
//   occupied composer leaves the row offered-but-blocked - a disabled Restore
//   with the converter's own hint - and Discard is unconditional throughout.
//
// Imported test-file only: screens.tsx and every production file are untouched.
import type { ComponentProps, ReactNode } from "react";
import { createElement } from "react";
import { act, type ReactTestRenderer } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import {
	getNativeMutationRuntime,
	nativeMutationTargetKey,
} from "./nativeMutationRuntime";
import { render, renderedText, screenConnection } from "./renderNative.testkit";
import { ConversationScreen } from "./screens";

const harness = vi.hoisted(() => ({
	connection: {} as Record<string, unknown>,
}));

// One sqlite double per database name, keyed the way the singletons open them,
// so the test can read the same rows the screen's own recovery hook reads.
const sqlite = vi.hoisted(() => ({ ports: new Map<string, unknown>() }));

vi.mock("react-native", async () => {
	const mock = (await import("./renderNative.testkit")).nativeModuleMock();
	return {
		...mock,
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

function pressLabel(tree: ReactTestRenderer, label: string) {
	const target = pressables(tree).find(
		(node) => node.props.accessibilityLabel === label,
	);
	if (!target) throw new Error(`no pressable labelled ${label}`);
	act(() => target.props.onPress());
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

it("renders the Recovery entry and mounts the recovery panel end to end, row-conditional under the #2247 contract", async () => {
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

	const tree = render(
		<ConversationScreen
			route={conversationRoute(ref)}
			navigation={navigation}
		/>,
	);
	await flush();

	// PHASE 1 - empty snapshot, connected: no dead entry ships and the recovery
	// modal is not mounted at all (its visibility-aware Modal renders nothing).
	// Positive control first: the real connected screen rendered, so the absent
	// entry is a wiring verdict and not an empty tree.
	expect(
		tree.root
			.findAll((node) => String(node.type) === "TextInput")
			.some((node) => node.props.accessibilityLabel === "Message"),
	).toBe(true);
	expect(renderedText(tree)).toContain("Connected");
	expect(
		pressables(tree).find((n) => n.props.accessibilityLabel === "Reconnect"),
	).toBeUndefined();
	expect(
		pressables(tree).find((n) => n.props.accessibilityLabel === "Recovery"),
	).toBeUndefined();
	expect(renderedText(tree)).not.toContain("Review status");

	// PHASE 2 - this target's durable recovery row lands and the runtime
	// publishes the storage change the hook follows.
	const record = await runtime.storage.enqueueIntent({
		targetRef: targetKey,
		method: "turn/queue",
		payload: { ref, input: [{ type: "text", text: "recover this message" }] },
		attachments: [],
		optimisticDisplay: { method: "turn/queue" },
	});
	const recovery = await runtime.storage.transferToRecovery(
		record.clientMutationId,
		"rejected",
		"daemon refused",
	);
	if (!recovery) throw new Error("seeding the recovery row failed");
	await act(async () => {
		// A zero-row discard is the runtime's own publish: it notifies storage
		// listeners (the screen's hook re-reads) without touching the row.
		await runtime.discardRecovery("no-such-row", targetKey);
	});
	await flush();

	expect(
		pressables(tree).find((n) => n.props.accessibilityLabel === "Recovery"),
	).toBeDefined();
	expect(renderedText(tree)).not.toContain("Review status");

	// PHASE 3 - pressing the entry opens the recovery modal, which mounts the
	// real panel holding this row.
	pressLabel(tree, "Recovery");
	await flush();
	const text = renderedText(tree);
	expect(text).toContain("Review status");
	expect(text).toContain("Rejected");
	expect(text).toContain("daemon refused");
	expect(text).toContain("recover this message");
	expect(
		pressables(tree).find((n) => n.props.accessibilityLabel === "Discard"),
	).toBeDefined();

	// PHASE 4 - the #2247 converter gate at screen level. With an empty, loaded
	// composer the eligible record meets the converter's true, so Restore is
	// offered and enabled.
	const restore = () =>
		pressables(tree).find(
			(n) => n.props.accessibilityLabel === "Restore to draft",
		);
	expect(restore()).toBeDefined();
	expect(restore()?.props.accessibilityState).toMatchObject({
		disabled: false,
	});

	// An occupied composer is offered-but-blocked: the row still offers Restore
	// (the record fence passes) but the converter withholds, so the panel shows
	// the disabled affordance and the converter's own hint, and Discard is still
	// actionable.
	const composer = tree.root
		.findAll((node) => String(node.type) === "TextInput")
		.find((node) => node.props.accessibilityLabel === "Message");
	if (!composer) throw new Error("no composer input to occupy");
	act(() => composer.props.onChangeText("a draft already in progress"));
	await flush();
	expect(restore()?.props.accessibilityState).toMatchObject({ disabled: true });
	expect(renderedText(tree)).toContain(
		"Clear or send your current draft to restore this message.",
	);
	expect(
		pressables(tree).find((n) => n.props.accessibilityLabel === "Discard"),
	).toBeDefined();
});
