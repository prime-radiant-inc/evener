import { mkdtempSync, rmSync } from "node:fs";
import { join } from "node:path";
import { tmpdir } from "node:os";
import type { ComponentProps, ReactNode } from "react";
import { createElement } from "react";
import { act, type ReactTestRenderer } from "react-test-renderer";
import { afterAll, afterEach, expect, it, vi } from "vitest";
import { buildComposerInput, MAX_ATTACHMENTS, type Thread } from "@evener/appwire-client";
import { ActionSheetIOS } from "react-native";
import { launchImageLibraryAsync } from "expo-image-picker";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { Storage } from "expo-sqlite/kv-store";
import { ConversationScreen } from "./screens";
import { NativeConversationSessionsProvider } from "./session/NativeConversationSessionsProvider";
import { drafts, nativeDrafts } from "./nativeDrafts";
import {
	NativeMutationRuntime,
	type NativeMutationRequest,
	getNativeMutationRuntime,
	nativeMutationTargetKey,
} from "./nativeMutationRuntime";
import { DraftDocument } from "./draftDocument";
import { DraftRepository } from "./draftRepository";
import { ImageSelection, type ImagePicker } from "./imageSelection";
import { openSqliteSyncDouble } from "./sqliteSync.testkit";
import type { SqliteSync } from "./sqliteSync";
import { editQueuedMessageIntoDraft, reconcileEditedResends } from "./queuedMessageControls";
import { createClientIdentity, reconcilePendingEntries } from "@evener/appwire-client/state/mutation";
import { randomUUID } from "node:crypto";
import { GhostRowContext, GhostRowView } from "./session/GhostRows";
import { ghosts, isQueuedGhost, type Ghost, type GhostAction } from "./session/ghosts";
import { withGhostRows, isGhostRow } from "./session/transcriptRows";
import { QueueSheet, queueHosts } from "./QueueSheet";
import { sheetKey } from "./sheet/sheetHosts";
import {
	alertRequests,
	render,
	settle,
	screenConnection,
	pressable,
	unmountMountedTrees,
} from "./renderNative.testkit";
import { answerFleetRead } from "./session/fleetTestUtils";
import { sessionInfoHosts } from "./session/SessionInfoSheet";
const harness = vi.hoisted(() => ({
	connection: {} as Record<string, unknown>,
	kvFailure: "" as "" | "read" | "write",
	handoffReadFailure: false,
	handoffWriteFailure: false,
	handoffEdgesReads: 0,
	handoffPendingnessReads: 0,
	handoffPendingnessReadFailFrom: 0,
	handoffEdgesReadFailFrom: 0,
	completionRoutingReadFailure: false,
	rpcFailure: "" as string,
	reopenKV: () => {},
	manipulate: vi.fn(),
}));
const navigationState = vi.hoisted(() => ({ state: { index: 0, routes: [] as { key: string; name: string }[] } }));
const sqlite = vi.hoisted(() => ({
	ports: new Map<string, unknown>(),
	handles: [] as { close(): void }[],
	reopen: [] as (() => void)[],
	directory: "",
}));
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
		useFocusEffect: (effect: () => void | (() => void)) => useEffect(effect, []),
		useNavigationState: <T,>(select: (state: typeof navigationState.state) => T) => select(navigationState.state),
		// A sheet route rendered beside the screen (NotesSheet) reads these.
		useNavigation: () => navigation,
		usePreventRemove: () => {},
	};
});
vi.mock("expo-web-browser", () => ({ openBrowserAsync: vi.fn(async () => ({ type: "dismiss" })) }));
vi.mock("react-native-gesture-handler", async () =>
	(await import("./renderNative.testkit")).gestureDetectorModuleMock(),
);
vi.mock("react-native-gesture-handler/ReanimatedSwipeable", async () =>
	(await import("./renderNative.testkit")).gestureHandlerModuleMock(),
);
vi.mock("expo-clipboard", () => ({
	setStringAsync: vi.fn(async () => {}),
	getStringAsync: vi.fn(async () => ""),
}));
vi.mock("expo-sqlite", async () => {
	const { openSqliteSyncDouble } = await import("./sqliteSync.testkit");
	const { mkdtempSync } = await import("node:fs");
	const { join } = await import("node:path");
	const { tmpdir } = await import("node:os");
	sqlite.directory ||= mkdtempSync(join(process.env.EVENER_SCRATCH_DIR ?? tmpdir(), "screen-queued-"));
	return {
		openDatabaseSync: (database: string) => {
			let port = sqlite.ports.get(database);
			if (!port) {
				let opened = openSqliteSyncDouble(join(sqlite.directory, database));
				// Failing the handoff ledger's queries models a ledger that
				// exists but cannot be read: ownership stays unknown, which
				// the screen must treat as defer, never as empty.
				const handoffReadFails = (sql: unknown) =>
					harness.handoffReadFailure &&
					((database === "evener-mutations.db" &&
						(String(sql).includes("draft_handoff") || String(sql).includes("draft_submission"))) ||
						(database === "evener-drafts.db" && String(sql).includes("draft_transfer_log")));
				// Fails the Nth-and-later handoff graph reads while every earlier
				// read succeeds, so a test can fail one specific ownership read in
				// the screen's sequence (the initial read, the serialized
				// coordinator's re-read, the pendingness read) in isolation.
				const edgesReadFails = (sql: unknown) => {
					if (!harness.handoffEdgesReadFailFrom || database !== "evener-mutations.db") return false;
					if (!String(sql).includes("FROM draft_submission_obligations")) return false;
					const index = ++harness.handoffEdgesReads;
					return index >= harness.handoffEdgesReadFailFrom;
				};
				// Fails the handoff ledger's row writes while its reads succeed:
				// a storage whose pending-handoff decisions cannot record, so a
				// claimed transfer strands until the writes recover. Schema
				// statements stay healthy - the ledger can open and read.
				const handoffWriteFails = (sql: unknown) => {
					if (!harness.handoffWriteFailure) return false;
					const text = String(sql).trimStart();
					if (!/^(INSERT|UPDATE|DELETE|REPLACE)\b/i.test(text)) return false;
					if (database === "evener-mutations.db")
						return text.includes("draft_handoff") || text.includes("draft_submission");
					return database === "evener-drafts.db" && text.includes("draft_transfer_log");
				};
				// Fails the Nth-and-later pendingness reads - the drafts
				// store's transfer-log lookups - so a test can fail the
				// hasPendingHandoff gate in isolation while every ownership
				// read before it succeeds.
				const pendingnessReadFails = (sql: unknown) => {
					if (!harness.handoffPendingnessReadFailFrom || database !== "evener-drafts.db") return false;
					if (!String(sql).includes("draft_transfer_log")) return false;
					const text = String(sql).trimStart();
					if (/^(INSERT|UPDATE|DELETE|REPLACE)\b/i.test(text)) return false;
					const index = ++harness.handoffPendingnessReads;
					return index >= harness.handoffPendingnessReadFailFrom;
				};
				// Fails the capture-routing read of the transfer log while every
				// ownership and pendingness read succeeds, so a test can leave a
				// resolved submission's completion undelivered in isolation.
				const completionRoutingFails = (sql: unknown) =>
					harness.completionRoutingReadFailure &&
					database === "evener-drafts.db" &&
					String(sql).includes("carried_capture_token");
				// A stable platform handle delegates to the reopened on-disk database.
				port = {
					execSync: (...args: Parameters<SqliteSync["execSync"]>) => {
						if (handoffWriteFails(args[0])) throw new Error("handoff write failed");
						return opened.port.execSync(...args);
					},
					runSync: (...args: Parameters<SqliteSync["runSync"]>) => {
						if (handoffWriteFails(args[0])) throw new Error("handoff write failed");
						return opened.port.runSync(...args);
					},
					getFirstSync: (...args: Parameters<SqliteSync["getFirstSync"]>) => {
						if (handoffReadFails(args[0])) throw new Error("handoff read failed");
						if (pendingnessReadFails(args[0])) throw new Error("pendingness read failed");
						if (completionRoutingFails(args[0])) throw new Error("completion routing read failed");
						return opened.port.getFirstSync(...args);
					},
					getAllSync: (...args: Parameters<SqliteSync["getAllSync"]>) => {
						if (handoffReadFails(args[0])) throw new Error("handoff read failed");
						if (edgesReadFails(args[0])) throw new Error(`handoff edges read #${harness.handoffEdgesReads} failed`);
						return opened.port.getAllSync(...args);
					},
				};
				sqlite.handles.push({ close: () => opened.database.close() });
				sqlite.reopen.push(() => {
					opened.database.close();
					opened = openSqliteSyncDouble(join(sqlite.directory, database));
				});
				sqlite.ports.set(database, port);
			}
			return port;
		},
	};
});
vi.mock("expo-sqlite/kv-store", async () => {
	const { openSqliteSyncDouble } = await import("./sqliteSync.testkit");
	const { mkdtempSync } = await import("node:fs");
	const { join } = await import("node:path");
	const { tmpdir } = await import("node:os");
	sqlite.directory ||= mkdtempSync(join(process.env.EVENER_SCRATCH_DIR ?? tmpdir(), "screen-queued-"));
	let db = openSqliteSyncDouble(join(sqlite.directory, "kv.sqlite")).database;
	db.exec("CREATE TABLE IF NOT EXISTS kv (key TEXT PRIMARY KEY, value TEXT NOT NULL)");
	sqlite.handles.push({ close: () => db.close() });
	harness.reopenKV = () => {
		db.close();
		db = openSqliteSyncDouble(join(sqlite.directory, "kv.sqlite")).database;
	};
	return {
		Storage: {
			getItemSync: (key: string) => {
				if (key.startsWith("queued-message-identity:") && harness.kvFailure === "read")
					throw new Error("KV read failed");
				return (
					(db.prepare("SELECT value FROM kv WHERE key=?").get(key) as { value: string } | undefined)?.value ?? null
				);
			},
			setItemSync: (key: string, value: string) => {
				if (key.startsWith("queued-message-identity:") && harness.kvFailure === "write")
					throw new Error("KV write failed");
				db.prepare("INSERT OR REPLACE INTO kv VALUES (?,?)").run(key, value);
			},
			removeItemSync: (key: string) => {
				db.prepare("DELETE FROM kv WHERE key=?").run(key);
			},
		},
	};
});
vi.mock("expo-file-system", () => ({
	File: class File {
		constructor(public uri: string) {}
	},
}));
vi.mock("expo-image-manipulator", () => ({
	ImageManipulator: {
		manipulate: (uri: string) => harness.manipulate(uri),
		manipulateAsync: vi.fn(async () => ({ uri: "manipulated" })),
	},
	SaveFormat: { JPEG: "jpeg" },
}));
harness.manipulate.mockImplementation(() => ({
	resize: () => {},
	renderAsync: async () => ({
		width: 100,
		height: 100,
		saveAsync: async () => ({ base64: "AQID", uri: "encoded" }),
		release: () => {},
	}),
	release: () => {},
}));
vi.mock("expo-image-picker", () => ({
	launchImageLibraryAsync: vi.fn(async () => ({ canceled: true, assets: [] })),
	launchCameraAsync: vi.fn(async () => ({ canceled: true, assets: [] })),
	requestCameraPermissionsAsync: vi.fn(async () => ({ granted: true })),
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
	useNativePreferences: () => ({ hubId: null, model: null, snapshot: null, config: null, connected: false }),
}));
type Props = ComponentProps<typeof ConversationScreen>;
function SessionScreen(props: Props) {
	return (
		<NativeConversationSessionsProvider>
			<ConversationScreen {...props} />
		</NativeConversationSessionsProvider>
	);
}
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
} as unknown as Props["navigation"];
let serial = 0;
afterEach(async () => {
	unmountMountedTrees();
	harness.kvFailure = "";
	harness.handoffReadFailure = false;
	harness.handoffWriteFailure = false;
	harness.handoffEdgesReads = 0;
	harness.handoffEdgesReadFailFrom = 0;
	harness.handoffPendingnessReads = 0;
	harness.handoffPendingnessReadFailFrom = 0;
	harness.completionRoutingReadFailure = false;
	harness.rpcFailure = "";
	await getNativeMutationRuntime().stop();
});
afterAll(() => {
	for (const handle of sqlite.handles) handle.close();
	rmSync(sqlite.directory, { recursive: true });
});
async function mount(
	selectionOnly = false,
	command = false,
	draftText?: string,
	selectionKind: "skill" | "command" = "skill",
	options: { goal?: { objective: string; status: "active"; iterations: number } } = {},
) {
	const ref = `queued-${++serial}`;
	if (selectionOnly)
		nativeDrafts().write(
			{ hubId: "hub-1", sessionRef: ref },
			{
				draft: draftText ?? (command ? "/queue body" : ""),
				unconfirmed: null,
				composer:
					draftText !== undefined && draftText !== ""
						? { commandNames: ["compact"], mentions: [{ kind: "command" as const, name: "compact", offset: 0 }] }
						: selectionKind === "skill"
							? { skillNames: ["selected-skill"] }
							: { commandNames: ["selected-command"] },
			},
		);
	const thread = {
		id: `thread-${ref}`,
		sessionId: `session-${ref}`,
		preview: "",
		ephemeral: false,
		modelProvider: "anthropic",
		createdAt: 0,
		updatedAt: 0,
		status: { type: command ? "active" : "idle" },
		cwd: "",
		cliVersion: "",
		source: "",
		turns: [],
		evener: {
			ref,
			instanceId: "instance",
			...(options.goal === undefined ? {} : { goal: options.goal }),
			capabilities: {
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
				goal: options.goal !== undefined,
				rename: false,
			},
			queue: { revision: 0, depth: 0, preview: [], texts: [], ids: [] },
		},
	} as unknown as Thread;
	const client = new FakeClient("ready");
	const requests: { method: string; params: Record<string, unknown> }[] = [];
	client.request = async (method, raw) => {
		const params = raw as Record<string, unknown>;
		requests.push({ method, params });
		if (method === harness.rpcFailure) throw new Error("scripted RPC failure");
		if (method === "thread/read") return { thread } as never;
		if (method === "model/list") return { data: [] } as never;
		if (method.startsWith("turn/"))
			return {
				receipt: {
					clientMutationId: params.clientMutationId,
					disposition: "applied",
					threadId: thread.id,
					turnId: "turn-1",
					projectionState: "pending",
					instanceId: "instance",
				},
			} as never;
		return (answerFleetRead({ live: [], needsYou: [] }, method, params) ?? {}) as never;
	};
	harness.connection = {
		...screenConnection(client, "ready"),
		profiles: [{ id: "hub-1", name: "Work hub", origin: "https://hub.test" }],
		error: null,
		disconnect: () => {},
	};
	const route = {
		key: `conversation-${ref}`,
		name: "Conversation",
		params: { hubId: "hub-1", ref, title: "Session" },
	} as Props["route"];
	navigationState.state = { index: 0, routes: [route] };
	let tree!: ReactTestRenderer;
	await act(async () => {
		tree = render(<SessionScreen route={route} navigation={navigation} />);
	});
	await settle();
	const document = drafts.open({ hubId: "hub-1", sessionRef: ref });
	const runtime = getNativeMutationRuntime();
	const targetKey = nativeMutationTargetKey("hub-1", ref);
	async function offline() {
		act(() => client.emitStateChange("reconnecting"));
		harness.connection = { ...harness.connection, state: "reconnecting" };
		await act(async () => {
			tree.update(<SessionScreen route={route} navigation={navigation} />);
		});
	}
	async function recreateOffline() {
		act(() => tree.unmount());
		harness.reopenKV();
		for (const reopen of sqlite.reopen) reopen();
		await act(async () => {
			tree = render(<SessionScreen route={route} navigation={navigation} />);
		});
		await settle();
		return tree;
	}
	return { ref, tree, route, thread, client, requests, document, runtime, targetKey, offline, recreateOffline };
}
it("actual screen enables selection-only Send and forwards offline composition", async () => {
	const f = await mount(true);
	await f.offline();
	const send = pressable(f.tree, "Send when you're back online");
	expect(send?.props.disabled).toBe(false);
	await act(async () => send?.props.onPress());
	await settle();
	expect((await f.runtime.read(f.targetKey)).outbox[0]).toMatchObject({
		composerText: "",
		payload: { input: [{ type: "skill", name: "selected-skill" }] },
	});
});
it.each(["skill", "command"] as const)("actual active screen offers Queue for selection-only %s work", async (kind) => {
	const f = await mount(false, true);
	await f.offline();
	const selection = await enqueueRecoveredSelection(f, kind);
	await reconnect(f);
	expect(f.document.getSnapshot().record.composer?.recoveryIds).toEqual([selection.clientMutationId]);
	expect(f.document.getSnapshot().record.composer).toMatchObject(
		kind === "skill" ? { skillNames: ["selected-skill"] } : { commandNames: ["selected-command"] },
	);
	const send = pressable(f.tree, "Queue message");
	expect(send?.props.disabled).toBe(false);
	expect(pressable(f.tree, "Stop")).toBeUndefined();
	await act(async () => send?.props.onPress());
	await settle();
	expect(f.requests.filter((r) => r.method.startsWith("turn/")).map((r) => r.params.input)).toEqual([
		[kind === "skill" ? { type: "skill", name: "selected-skill" } : { type: "command", name: "selected-command" }],
	]);
	expect(await f.runtime.storage.getRecovery(selection.clientMutationId)).toBeUndefined();
	expect((await f.runtime.read(f.targetKey)).recovery).toEqual([]);
	expect(f.document.getSnapshot().record.composer?.recoveryIds ?? []).toEqual([]);
	const sent = f.requests.filter((request) => request.method.startsWith("turn/"));
	expect(sent).toHaveLength(1);
	expect(sent[0].params.clientMutationId).not.toBe(selection.clientMutationId);
});
it("actual screen projects waiting local records into the QueueSheet host", async () => {
	const f = await mount();
	await f.offline();
	await act(async () => {
		await f.runtime.submit({
			kind: "send",
			hubId: "hub-1",
			targetRef: f.ref,
			threadId: `thread-${f.ref}`,
			instanceId: "instance",
			input: [{ type: "text", text: "same" }],
		});
	});
	await settle();
	const host = queueHosts.get(sheetKey("hub-1", f.ref));
	expect(host?.ghosts).toHaveLength(1);
	expect(host?.ghosts[0]).toMatchObject({ localQueued: true, menu: ["edit", "cancel"] });
});
it.each([false, true])("actual screen forwards selected composition online, command=%s", async (command) => {
	const f = await mount(true, command);
	const send = pressable(f.tree, command ? "Queue" : "Send");
	expect(send?.props.disabled).toBe(false);
	await act(async () => send?.props.onPress());
	await settle();
	expect(f.requests.filter((r) => r.method.startsWith("turn/")).map((r) => r.params.input)).toEqual([
		command
			? [
					{ type: "text", text: "body" },
					{ type: "skill", name: "selected-skill" },
				]
			: [{ type: "skill", name: "selected-skill" }],
	]);
});
it.each(["skill", "command"] as const)(
	"actual screen resends selected /compact %s recovery as ordinary composition",
	async (kind) => {
		const f = await mount();
		await f.offline();
		const selection = await enqueueRecoveredSelection(f, kind, "/compact", "compact");
		await reconnect(f);
		expect(f.document.getSnapshot().record.composer?.recoveryIds).toEqual([selection.clientMutationId]);
		const send = pressable(f.tree, "Send");
		expect(send?.props.disabled).toBe(false);
		await act(async () => send?.props.onPress());
		await settle();
		expect(f.requests.filter((r) => r.method.startsWith("turn/")).map((r) => r.params.input)).toEqual([
			[
				{ type: "text", text: "/compact", mentions: [{ kind, name: "compact", offset: 0 }] },
				{ type: kind, name: "compact" },
			],
		]);
		const state = await f.runtime.read(f.targetKey);
		expect(state.recovery).toEqual([]);
		expect(await f.runtime.storage.getRecovery(selection.clientMutationId)).toBeUndefined();
		const sent = f.requests.filter((request) => request.method.startsWith("turn/"));
		expect(sent).toHaveLength(1);
		expect(sent[0].params.clientMutationId).not.toBe(selection.clientMutationId);
	},
);
async function enqueueRecoveredSelection(
	f: Awaited<ReturnType<typeof mount>>,
	kind: "skill" | "command",
	text = "",
	name = kind === "skill" ? "selected-skill" : "selected-command",
) {
	const mentions = text === "/compact" ? [{ kind, name, offset: 0 }] : undefined;
	const composer = {
		text,
		attachments: [],
		...(kind === "skill" ? { skillNames: [name] } : { commandNames: [name] }),
		...(mentions === undefined ? {} : { mentions }),
	};
	await act(async () => {
		await f.runtime.submit({
			kind: "send",
			hubId: "hub-1",
			targetRef: f.ref,
			threadId: f.thread.id,
			instanceId: "instance",
			input: buildComposerInput(text, [], kind === "skill" ? [name] : [], kind === "command" ? [name] : [], mentions),
			composer,
		});
	});
	await settle();
	const source = (await f.runtime.read(f.targetKey)).outbox[0];
	if (!source) throw new Error("missing selected outbox source");
	const host = queueHosts.get(sheetKey("hub-1", f.ref));
	const ghost = host?.ghosts.find(
		(item) => item.origin.kind === "pending" && item.origin.clientMutationId === source.clientMutationId,
	);
	if (!host || !ghost) throw new Error("missing selected queue ghost");
	await act(async () => {
		await host.act(ghost, "edit");
	});
	return source;
}

async function reconnect(f: Awaited<ReturnType<typeof mount>>) {
	act(() => f.client.emitStateChange("ready"));
	harness.connection = { ...harness.connection, state: "ready" };
	await act(async () => f.tree.update(<SessionScreen route={f.route} navigation={navigation} />));
	await settle();
}

async function enqueue(f: Awaited<ReturnType<typeof mount>>, text = "source") {
	await act(async () => {
		await f.runtime.submit({
			kind: "send",
			hubId: "hub-1",
			targetRef: f.ref,
			threadId: `thread-${f.ref}`,
			instanceId: "instance",
			input: buildComposerInput(text, [], ["skill"]),
			composer: { text, attachments: [], skillNames: ["skill"] },
		});
	});
	await settle();
	const record = (await f.runtime.read(f.targetKey)).outbox.at(-1);
	if (!record) throw new Error("no durable record");
	return record;
}
it("actual screen edits offline into nonempty work without sending and removes by identity", async () => {
	const f = await mount();
	await f.offline();
	const first = await enqueue(f, "same");
	const second = await enqueue(f, "same");
	act(() => f.document.edit("existing"));
	const host = queueHosts.get(sheetKey("hub-1", f.ref));
	const ghost = host?.ghosts.find(
		(g) => g.origin.kind === "pending" && g.origin.clientMutationId === first.clientMutationId,
	);
	if (!host || !ghost) throw new Error("no local ghost");
	await act(async () => {
		await host.act(ghost, "edit");
	});
	expect(f.document.getSnapshot().record).toMatchObject({
		draft: "existing\n\nsame",
		composer: { recoveryIds: [first.clientMutationId], skillNames: ["skill"] },
	});
	expect(f.requests.filter((r) => r.method.startsWith("turn/"))).toEqual([]);
	const next = queueHosts.get(sheetKey("hub-1", f.ref));
	if (!next?.ghosts[0]) throw new Error("no second ghost");
	await act(async () => {
		await next.act(next.ghosts[0], "cancel");
	});
	expect(await f.runtime.storage.getOutbox(second.clientMutationId)).toBeUndefined();
});

it("actual QueueSheet Remove clears only the matching failed-confirmation capture", async () => {
	const f = await mount();
	await f.offline();
	act(() => f.document.edit("ordinary queued message"));
	const draftDb = sqlite.ports.get("evener-drafts.db") as SqliteSync | undefined;
	if (!draftDb) throw new Error("missing draft database");
	draftDb.execSync(`CREATE TRIGGER reject_confirmation BEFORE DELETE ON drafts
		WHEN OLD.unconfirmed IS NOT NULL
		BEGIN SELECT RAISE(ABORT, 'draft confirmation rejected'); END`);
	await act(async () => pressable(f.tree, "Send when you're back online")?.props.onPress());
	await settle();
	const [queued] = (await f.runtime.read(f.targetKey)).outbox;
	expect(queued?.draftCaptureToken).toEqual(expect.any(String));
	expect(nativeDrafts().read({ hubId: "hub-1", sessionRef: f.ref })).toMatchObject({
		unconfirmed: "ordinary queued message",
		unconfirmedCaptureToken: queued?.draftCaptureToken,
	});
	draftDb.execSync("DROP TRIGGER reject_confirmation");
	await act(async () => f.document.retry());
	await settle();
	const host = queueHosts.get(sheetKey("hub-1", f.ref));
	const ghost = host?.ghosts.find(
		(item) => item.origin.kind === "pending" && item.origin.clientMutationId === queued?.clientMutationId,
	);
	if (!host || !ghost) throw new Error("missing failed-confirmation QueueSheet row");
	await act(async () => {
		await host.act(ghost, "cancel");
	});
	expect(await f.runtime.storage.getOutbox(queued.clientMutationId)).toBeUndefined();
	expect(nativeDrafts().read({ hubId: "hub-1", sessionRef: f.ref })).toMatchObject({
		draft: "",
		unconfirmed: null,
	});
	expect(nativeDrafts().read({ hubId: "hub-1", sessionRef: f.ref }).unconfirmedCaptureToken).toBeUndefined();
});

it("actual screen-strip Remove clears only the matching failed-confirmation capture", async () => {
	const f = await mount();
	await f.offline();
	act(() => f.document.edit("ordinary queued message"));
	const draftDb = sqlite.ports.get("evener-drafts.db") as SqliteSync | undefined;
	if (!draftDb) throw new Error("missing draft database");
	draftDb.execSync(`CREATE TRIGGER reject_confirmation BEFORE DELETE ON drafts
		WHEN OLD.unconfirmed IS NOT NULL
		BEGIN SELECT RAISE(ABORT, 'draft confirmation rejected'); END`);
	await act(async () => pressable(f.tree, "Send when you're back online")?.props.onPress());
	await settle();
	const [queued] = (await f.runtime.read(f.targetKey)).outbox;
	expect(queued?.draftCaptureToken).toEqual(expect.any(String));
	draftDb.execSync("DROP TRIGGER reject_confirmation");
	await act(async () => f.document.retry());
	await settle();
	const row = f.tree.root.findAllByType(GhostRowView).find((node) => {
		const ghost = node.props.row.ghost as Ghost | undefined;
		return ghost?.origin.kind === "pending" && ghost.origin.clientMutationId === queued.clientMutationId;
	});
	if (!row) throw new Error("missing inline failed-confirmation queue ghost");
	const bubble = row.findAll(
		(node) =>
			String(node.type) === "Pressable" && String(node.props.accessibilityLabel).startsWith("ordinary queued message."),
	)[0];
	if (!bubble) throw new Error("missing inline queue bubble");
	vi.mocked(ActionSheetIOS.showActionSheetWithOptions).mockClear();
	await act(async () => bubble.props.onPress());
	const actionSheet = vi.mocked(ActionSheetIOS.showActionSheetWithOptions).mock.lastCall;
	if (!actionSheet) throw new Error("inline queue action sheet did not open");
	const removeIndex = actionSheet[0].options.findIndex((label) => label === "Remove");
	if (removeIndex < 0) throw new Error("inline queue action sheet has no Remove action");
	await act(async () => actionSheet[1](removeIndex));
	await settle();
	expect(await f.runtime.storage.getOutbox(queued.clientMutationId)).toBeUndefined();
	expect(nativeDrafts().read({ hubId: "hub-1", sessionRef: f.ref })).toMatchObject({
		draft: "",
		unconfirmed: null,
	});
	expect(nativeDrafts().read({ hubId: "hub-1", sessionRef: f.ref }).unconfirmedCaptureToken).toBeUndefined();
});

it("actual screen deselects an edited recovered label before explicit Send and persists it across reopen", async () => {
	const f = await mount();
	await f.offline();
	const text = "please /skill keep this";
	const mentions = [{ kind: "skill" as const, name: "skill", offset: 7 }];
	await act(async () => {
		await f.runtime.submit({
			kind: "send",
			hubId: "hub-1",
			targetRef: f.ref,
			threadId: `thread-${f.ref}`,
			instanceId: "instance",
			input: buildComposerInput(text, [], ["skill"], [], mentions),
			composer: { text, attachments: [], skillNames: ["skill"], mentions },
		});
	});
	await settle();
	const host = queueHosts.get(sheetKey("hub-1", f.ref));
	if (!host?.ghosts[0]) throw new Error("no queued source");
	await act(async () => {
		await host.act(host.ghosts[0], "edit");
	});
	const edited = "please  keep this /skill";
	const input = f.tree.root.findAll((node) => String(node.type) === "TextInput" && node.props.value === text)[0];
	if (!input) throw new Error("missing recovered composer input");
	await act(async () => input.props.onChangeText(edited));
	expect(f.document.getSnapshot().record.composer?.skillNames).toEqual([]);
	await act(async () => pressable(f.tree, "Send when you're back online")?.props.onPress());
	await settle();
	const persisted = await f.runtime.read(f.targetKey);
	expect(persisted.recovery).toEqual([]);
	expect(persisted.outbox).toHaveLength(1);
	expect(persisted.outbox[0].payload.input).toEqual([{ type: "text", text: edited }]);
	await f.recreateOffline();
	expect(f.document.getSnapshot().record.draft).toBe("");
	expect((await f.runtime.read(f.targetKey)).outbox[0].payload.input).toEqual([{ type: "text", text: edited }]);
});

it("actual screen reacquires interrupted editing ownership in an effect", async () => {
	const f = await mount();
	await f.offline();
	const source = await enqueue(f);
	act(() => f.tree.unmount());
	await act(async () => {
		// The committed storage boundary survives the claiming screen's lifetime.
		const claimed = await f.runtime.storage.transferToRecovery(source.clientMutationId, "editing");
		expect(claimed?.clientMutationId).toBe(source.clientMutationId);
	});
	await f.recreateOffline();
	await settle();
	expect(f.document.getSnapshot().record.composer?.recoveryIds).toEqual([source.clientMutationId]);
	expect(f.requests.filter((r) => r.method.startsWith("turn/"))).toEqual([]);
});

it("actual screen restores an interrupted Edit after cached identity Retry", async () => {
	const f = await mount();
	await f.offline();
	const source = await enqueue(f, "exact interrupted source text");
	act(() => f.tree.unmount());
	await act(async () => {
		const claimed = await f.runtime.storage.transferToRecovery(source.clientMutationId, "editing");
		expect(claimed?.clientMutationId).toBe(source.clientMutationId);
	});
	harness.kvFailure = "read";
	const tree = await f.recreateOffline();
	expect(f.document.getSnapshot().record.composer?.recoveryIds).toBeUndefined();
	expect(tree.root.findAllByProps({ accessibilityLabel: "Retry" })).toHaveLength(1);
	harness.kvFailure = "";
	await act(async () => pressable(tree, "Retry")?.props.onPress());
	await settle();
	expect(f.document.getSnapshot().record).toMatchObject({
		draft: "exact interrupted source text",
		composer: { recoveryIds: [source.clientMutationId] },
	});
	expect((await f.runtime.read(f.targetKey)).recovery.map((row) => row.clientMutationId)).toEqual([
		source.clientMutationId,
	]);
});

it("actual screen retries a deferred ownership read without user action", async () => {
	const f = await mount();
	await f.offline();
	const source = await enqueue(f, "ownership retry source");
	act(() => f.tree.unmount());
	await act(async () => {
		const claimed = await f.runtime.storage.transferToRecovery(source.clientMutationId, "editing");
		expect(claimed?.clientMutationId).toBe(source.clientMutationId);
	});
	// The ledger exists but its ownership read fails: unknown ownership defers
	// restoration rather than restoring a possibly running command's sources,
	// and the deferral is not the user's to repair.
	harness.handoffReadFailure = true;
	const tree = await f.recreateOffline();
	await settle();
	expect(tree.root.findAllByProps({ accessibilityLabel: "Retry" })).toHaveLength(0);
	expect(f.document.getSnapshot().record.composer?.recoveryIds).toBeUndefined();
	harness.handoffReadFailure = false;
	// No Retry press, no remount: the screen retries the deferred read on its
	// own timer and the interrupted capture converges to the draft.
	await act(async () => {
		await new Promise((resolve) => setTimeout(resolve, 1500));
	});
	await settle();
	expect(f.document.getSnapshot().record).toMatchObject({
		draft: "ownership retry source",
		composer: { recoveryIds: [source.clientMutationId] },
	});
	expect((await f.runtime.read(f.targetKey)).recovery.map((row) => row.clientMutationId)).toEqual([
		source.clientMutationId,
	]);
});

it("actual screen defers restoration while pending handoffs are unreadable", async () => {
	const f = await mount();
	await f.offline();
	const source = await enqueue(f, "pendingness retry source");
	act(() => f.tree.unmount());
	await act(async () => {
		const claimed = await f.runtime.storage.transferToRecovery(source.clientMutationId, "editing");
		expect(claimed?.clientMutationId).toBe(source.clientMutationId);
	});
	// The drafts store's transfer-log read backing the pendingness gate fails
	// while the ownership reads before it succeed: the screen must read that
	// as unknown pendingness and defer restoration, not as handoff-free.
	harness.handoffPendingnessReads = 0;
	harness.handoffPendingnessReadFailFrom = 1;
	const tree = await f.recreateOffline();
	await settle();
	expect(tree.root.findAllByProps({ accessibilityLabel: "Retry" })).toHaveLength(0);
	expect(f.document.getSnapshot().record.composer?.recoveryIds).toBeUndefined();
	harness.handoffPendingnessReadFailFrom = 0;
	// No user action, no remount: the deferred restoration retries itself.
	await act(async () => {
		await new Promise((resolve) => setTimeout(resolve, 1500));
	});
	await settle();
	expect(f.document.getSnapshot().record).toMatchObject({
		draft: "pendingness retry source",
		composer: { recoveryIds: [source.clientMutationId] },
	});
});

it("actual screen retries an undelivered completion without user action", async () => {
	const f = await mount();
	await f.offline();
	// A sibling identity holds a submitted message whose capture the handoff
	// carries into this screen.
	const sibling = `sibling-of-${f.ref}`;
	nativeDrafts().write(
		{ hubId: "hub-1", sessionRef: sibling },
		{
			draft: "handoff completion source",
			unconfirmed: null,
		},
	);
	const siblingDocument = drafts.open({ hubId: "hub-1", sessionRef: sibling });
	let finish!: (sent: boolean) => void;
	const submitted = siblingDocument.submit(
		() =>
			new Promise<boolean>((resolve) => {
				finish = resolve;
			}),
	);
	drafts.handoff({ hubId: "hub-1", sessionRef: sibling }, { hubId: "hub-1", sessionRef: f.ref });
	await settle();
	// The submission resolves while the transfer log's capture-routing read
	// fails: the completion is durable but undelivered, so the capture stays
	// held and Send blocked while every ownership and pendingness read
	// succeeds - the screen must keep retrying on its own.
	harness.completionRoutingReadFailure = true;
	await act(async () => {
		finish(true);
		await submitted;
	});
	await settle();
	expect(f.document.getSnapshot().record).toMatchObject({
		unconfirmed: "handoff completion source",
		unconfirmedCaptureToken: expect.any(String),
	});
	harness.completionRoutingReadFailure = false;
	// No Retry press, no remount: the screen's own timer re-runs the pass and
	// the recovered routing delivers the completion, clearing the capture.
	await act(async () => {
		await new Promise((resolve) => setTimeout(resolve, 1500));
	});
	await settle();
	expect(f.tree.root.findAllByProps({ accessibilityLabel: "Retry" })).toHaveLength(0);
	expect(f.document.getSnapshot().record).toMatchObject({
		unconfirmed: null,
		unconfirmedCaptureToken: undefined,
	});
});

it("actual screen restores its own rows while an unrelated completion stays undelivered", async () => {
	const f = await mount();
	await f.offline();
	const source = await enqueue(f, "unrelated blocked source");
	act(() => f.tree.unmount());
	await act(async () => {
		const claimed = await f.runtime.storage.transferToRecovery(source.clientMutationId, "editing");
		expect(claimed?.clientMutationId).toBe(source.clientMutationId);
	});
	// An unrelated identity's completion resolved, but its acknowledgment
	// cannot record while the mutation store refuses writes: the obligation
	// stays outstanding forever without touching this conversation.
	const ledger = getNativeMutationRuntime().storage.submissionLedger;
	ledger.beginSubmission("unrelated-token", { hubId: "hub-1", sessionRef: "elsewhere" }, ["unrelated-recovery"]);
	ledger.resolveSubmission("unrelated-token", "completed", ["unrelated-recovery"], []);
	harness.handoffWriteFailure = true;
	const tree = await f.recreateOffline();
	await settle();
	// No user action, no failure surfaced: this screen's own retained edit
	// restores even while the unrelated completion cannot converge.
	expect(tree.root.findAllByProps({ accessibilityLabel: "Retry" })).toHaveLength(0);
	expect(f.document.getSnapshot().record).toMatchObject({
		draft: "unrelated blocked source",
		composer: { recoveryIds: [source.clientMutationId] },
	});
});

it("actual screen completes a pending handoff after its write failures recover", async () => {
	const f = await mount();
	await f.offline();
	// A sibling identity holds work destined for this screen: the retarget
	// records the identity change, so a pending handoff edge points here and
	// the screen's reconciliation must wait for it.
	const sibling = `sibling-of-${f.ref}`;
	nativeDrafts().write({ hubId: "hub-1", sessionRef: sibling }, { draft: "incoming work", unconfirmed: null });
	act(() => {
		f.runtime.storage.retarget(nativeMutationTargetKey("hub-1", sibling), {
			ref: f.targetKey,
			wireRef: f.ref,
			threadId: f.thread.id,
			instanceId: "instance",
		});
	});
	// The handoff ledger's writes fail: the claim cannot record, so the
	// effect's retry pass defers with the edge still pending. Its reads stay
	// healthy, so the pendingness is known - not unreadable.
	harness.handoffWriteFailure = true;
	// The screen hands the retired identity's draft over when the resume
	// resolves - the same call its identity-resolution path makes - into a
	// store whose handoff writes are failing.
	drafts.handoff({ hubId: "hub-1", sessionRef: sibling }, { hubId: "hub-1", sessionRef: f.ref });
	const tree = await f.recreateOffline();
	await settle();
	expect(drafts.hasPendingHandoff({ hubId: "hub-1", sessionRef: f.ref })).toBe(true);
	// No user-facing failure is surfaced for the user to repair.
	expect(tree.root.findAllByProps({ accessibilityLabel: "Retry" })).toHaveLength(0);
	harness.handoffWriteFailure = false;
	// No Retry press, no remount: the screen retries the pending handoff on
	// its own timer, and restoring the storage alone completes the transfer.
	await act(async () => {
		await new Promise((resolve) => setTimeout(resolve, 1500));
	});
	await settle();
	expect(drafts.hasPendingHandoff({ hubId: "hub-1", sessionRef: f.ref })).toBe(false);
	expect(f.document.getSnapshot().record.draft).toBe("incoming work");
});

it("actual screen retries a settle deferred on unknown ownership after awaits", async () => {
	const f = await mount();
	await f.offline();
	const source = await enqueue(f, "deferred settle source");
	act(() => f.tree.unmount());
	await act(async () => {
		const claimed = await f.runtime.storage.transferToRecovery(source.clientMutationId, "editing");
		expect(claimed?.clientMutationId).toBe(source.clientMutationId);
	});
	const tree = await f.recreateOffline();
	await settle();
	// The screen restored the captured edit into the live draft.
	expect(f.document.getSnapshot().record.composer?.recoveryIds).toEqual([source.clientMutationId]);
	await act(async () => {
		f.document.edit("edited recovered text");
	});
	await reconnect(f);
	act(() => tree.unmount());
	// The resubmitted recovered message resolves as an admission: its capture
	// stays for receipt reconciliation, and the resend receipt is durable -
	// planted with no screen mounted to consume it.
	await act(async () => {
		await f.document.submit(async (text, images, context) => {
			await f.runtime.submit({
				kind: "send",
				hubId: "hub-1",
				targetRef: f.ref,
				threadId: `thread-${f.ref}`,
				instanceId: "instance",
				recoverySourceIds: context.recoverySourceIds,
				input: buildComposerInput(
					text,
					images,
					context.composer?.skillNames ?? [],
					context.composer?.commandNames ?? [],
					context.composer?.mentions ?? [],
				),
				composer: { text, attachments: [], skillNames: [], commandNames: [], mentions: [] },
			});
			return true;
		});
	});
	await settle();
	expect(f.document.getSnapshot().record.unconfirmedComposer?.recoveryIds).toEqual([source.clientMutationId]);
	expect(await f.runtime.readRecoveryResend(f.targetKey, [source.clientMutationId])).toBeDefined();
	// The ownership read inside the serialized reconciliation fails while the
	// reads before it succeed: the settle defers, and only the screen's own
	// retry can finish it.
	harness.handoffEdgesReads = 0;
	harness.handoffEdgesReadFailFrom = 5;
	await f.recreateOffline();
	await settle();
	expect(f.document.getSnapshot().record.unconfirmedComposer?.recoveryIds).toEqual([source.clientMutationId]);
	harness.handoffEdgesReadFailFrom = 0;
	// No user action, no remount: the deferred settle retries itself and the
	// receipt reconciles.
	await act(async () => {
		await new Promise((resolve) => setTimeout(resolve, 1500));
	});
	await settle();
	expect(f.document.getSnapshot().record.unconfirmed).toBeNull();
	expect(f.document.getSnapshot().record.unconfirmedComposer).toBeUndefined();
});

it("actual screen releases owned recovery when text-only composition becomes empty", async () => {
	const f = await mount();
	await f.offline();
	await act(async () => {
		await f.runtime.submit({
			kind: "send",
			hubId: "hub-1",
			targetRef: f.ref,
			threadId: f.thread.id,
			instanceId: "instance",
			input: [{ type: "text", text: "text to clear" }],
			composer: { text: "text to clear", attachments: [] },
		});
	});
	const source = (await f.runtime.read(f.targetKey)).outbox[0];
	if (!source) throw new Error("missing text recovery source");
	act(() => f.tree.unmount());
	await act(async () => {
		await f.runtime.storage.transferToRecovery(source.clientMutationId, "editing");
	});
	const beforeClear = await f.recreateOffline();
	expect(f.document.getSnapshot().record.composer?.recoveryIds).toEqual([source.clientMutationId]);
	const input = beforeClear.root.findAll(
		(node) => String(node.type) === "TextInput" && node.props.value === "text to clear",
	)[0];
	if (!input) throw new Error("missing recovered text input");
	await act(async () => input.props.onChangeText(""));
	const tree = await f.recreateOffline();
	const recovered = tree.root.findAllByType(GhostRowView).map((node) => node.props.row.ghost as Ghost | undefined);
	expect(
		recovered.find(
			(row) => row?.origin.kind === "recovery" && row.origin.row.clientMutationId === source.clientMutationId,
		)?.buttons,
	).toEqual(["discard"]);
});

it("actual screen keeps a released source available when a later Edit claims another source", async () => {
	const f = await mount();
	await f.offline();
	await act(async () => {
		await f.runtime.submit({
			kind: "send",
			hubId: "hub-1",
			targetRef: f.ref,
			threadId: f.thread.id,
			instanceId: "instance",
			input: [{ type: "text", text: "source A text" }],
			composer: { text: "source A text", attachments: [] },
		});
	});
	await settle();
	const sourceA = (await f.runtime.read(f.targetKey)).outbox[0];
	if (!sourceA) throw new Error("missing source A recovery record");
	act(() => f.tree.unmount());
	await act(async () => {
		await f.runtime.storage.transferToRecovery(sourceA.clientMutationId, "editing");
	});
	const aTree = await f.recreateOffline();
	await settle();
	expect(f.document.getSnapshot().record.releasedRecoveryIds ?? []).toEqual([]);
	expect((await f.runtime.read(f.targetKey)).recovery.map((row) => row.clientMutationId)).toEqual([
		sourceA.clientMutationId,
	]);
	expect(f.document.getSnapshot().record.draft).toBe("source A text");
	const input = aTree.root.findAll(
		(node) => String(node.type) === "TextInput" && node.props.value === "source A text",
	)[0];
	if (!input) throw new Error("missing source A composer input");
	await act(async () => input.props.onChangeText(""));
	await f.recreateOffline();
	await act(async () => {
		await f.runtime.submit({
			kind: "send",
			hubId: "hub-1",
			targetRef: f.ref,
			threadId: f.thread.id,
			instanceId: "instance",
			input: [{ type: "text", text: "source B text" }],
			composer: { text: "source B text", attachments: [] },
		});
	});
	const sourceB = (await f.runtime.read(f.targetKey)).outbox.at(-1);
	if (!sourceB) throw new Error("missing source B recovery record");
	await act(async () => {
		await f.runtime.storage.transferToRecovery(sourceB.clientMutationId, "editing");
	});
	const tree = await f.recreateOffline();
	const record = f.document.getSnapshot().record;
	expect(record.draft).toBe("source B text");
	expect(record.composer?.recoveryIds).toEqual([sourceB.clientMutationId]);
	expect(record.releasedRecoveryIds).toEqual([sourceA.clientMutationId]);
	const sourceARow = tree.root
		.findAllByType(GhostRowView)
		.map((node) => node.props.row.ghost as Ghost | undefined)
		.find((row) => row?.origin.kind === "recovery" && row.origin.row.clientMutationId === sourceA.clientMutationId);
	expect(sourceARow?.buttons).toEqual(["discard"]);
});

it("actual screen releases owned recovery when image-only composition becomes empty", async () => {
	const f = await mount();
	await f.offline();
	await act(async () => {
		await f.runtime.submit({
			kind: "send",
			hubId: "hub-1",
			targetRef: f.ref,
			threadId: f.thread.id,
			instanceId: "instance",
			input: [{ type: "image", mediaType: "image/png", data: "AQID" }],
			composer: { text: "", attachments: [{ marker: 1, mediaType: "image/png", data: "AQID" }] },
		});
	});
	await settle();
	const source = (await f.runtime.read(f.targetKey)).outbox[0];
	if (!source) throw new Error("missing image recovery source");
	act(() => f.tree.unmount());
	await act(async () => {
		await f.runtime.storage.transferToRecovery(source.clientMutationId, "editing");
	});
	await f.recreateOffline();
	expect(f.document.getSnapshot().record.composer?.recoveryIds).toEqual([source.clientMutationId]);
	const image = f.document.getSnapshot().record.images?.[0];
	if (!image) throw new Error("missing recovered image");
	act(() => f.document.removeImage(image.id));
	const tree = await f.recreateOffline();
	const row = tree.root
		.findAllByType(GhostRowView)
		.map((node) => node.props.row.ghost as Ghost | undefined)
		.find(
			(candidate) =>
				candidate?.origin.kind === "recovery" && candidate.origin.row.clientMutationId === source.clientMutationId,
		);
	expect(row?.buttons).toEqual(["discard"]);
});

it("actual screen keeps uncertain Discard separate from local queue actions", async () => {
	const f = await mount();
	await f.offline();
	const source = await enqueue(f);
	await act(async () => {
		await f.runtime.storage.markAttempted(source.clientMutationId);
		await f.runtime.storage.markUnknown(source.clientMutationId, "blockedUnknown");
		await f.runtime.discardRecovery("missing", f.targetKey);
	});
	await settle();
	expect(queueHosts.get(sheetKey("hub-1", f.ref))?.ghosts).toEqual([]);
	const uncertain = f.tree.root
		.findAllByType(GhostRowView)
		.map((node) => node.props.row.ghost as Ghost | undefined)
		.find((g) => g?.origin.kind === "pending");
	expect(uncertain?.localQueued).not.toBe(true);
	expect(uncertain?.buttons).toEqual(["discard"]);
	await act(async () => pressable(f.tree, "Discard")?.props.onPress());
	expect(await f.runtime.storage.getOutbox(source.clientMutationId)).toBeUndefined();
	expect(f.document.getSnapshot().record.draft).toBe("");
});

it("actual screen preserves accepted-daemon text-only controls independently of local queue membership", async () => {
	const f = await mount();
	f.thread.evener.queue = {
		revision: 3,
		depth: 2,
		ids: ["daemon-text", "daemon-image"],
		texts: ["accepted text", ""],
		preview: ["accepted text", "image"],
	};
	await f.recreateOffline();
	const host = queueHosts.get(sheetKey("hub-1", f.ref));
	expect(host?.ghosts.map((g) => ({ origin: g.origin, menu: g.menu, local: g.localQueued === true }))).toEqual([
		{ origin: { kind: "queue", entry: { index: 0, id: "daemon-text" } }, menu: ["edit", "cancel"], local: false },
		{ origin: { kind: "queue", entry: { index: 1, id: "daemon-image" } }, menu: ["cancel"], local: false },
	]);
	expect((await f.runtime.read(f.targetKey)).outbox).toEqual([]);
});
it("actual screen retains cached local identity after offline screen recreation", async () => {
	const f = await mount();
	await f.offline();
	const source = await enqueue(f);
	const reads = f.requests.filter((r) => r.method === "thread/read").length;
	await f.recreateOffline();
	const host = queueHosts.get(sheetKey("hub-1", f.ref));
	expect(host?.ghosts.map((g) => g.origin)).toContainEqual({
		kind: "pending",
		clientMutationId: source.clientMutationId,
	});
	expect(f.requests.filter((r) => r.method.startsWith("turn/"))).toEqual([]);
	expect(f.requests.filter((r) => r.method === "thread/read")).toHaveLength(reads);
});

it("actual screen recovers retained ownership once after Retry saving", async () => {
	const f = await mount();
	await f.offline();
	const source = await enqueue(f);
	act(() => f.document.edit("prior"));
	const db = sqlite.ports.get("evener-drafts.db") as SqliteSync;
	db.execSync("PRAGMA query_only = ON");
	const host = queueHosts.get(sheetKey("hub-1", f.ref));
	if (!host?.ghosts[0]) throw new Error("missing source");
	await act(async () => {
		await host.act(host.ghosts[0], "edit");
	});
	expect(f.document.getSnapshot().error).not.toBeNull();
	expect(f.tree.root.findAllByProps({ accessibilityLabel: "Discard" })).toHaveLength(1);
	expect(nativeDrafts().read({ hubId: "hub-1", sessionRef: f.ref }).draft).toBe("prior");
	expect((await f.runtime.read(f.targetKey)).recovery.map((r) => r.clientMutationId)).toEqual([
		source.clientMutationId,
	]);
	db.execSync("PRAGMA query_only = OFF");
	await act(async () => pressable(f.tree, "Retry saving")?.props.onPress());
	await settle();
	expect(f.document.getSnapshot().record).toMatchObject({
		draft: "prior\n\nsource",
		composer: { recoveryIds: [source.clientMutationId] },
	});
	const read = vi.spyOn(f.runtime, "read");
	await settle();
	await settle();
	expect(read).not.toHaveBeenCalled();
	read.mockRestore();
	expect(f.requests.filter((r) => r.method.startsWith("turn/"))).toEqual([]);
});

it("actual screen keeps an appended source protected through a later failed save and explicit resend", async () => {
	const f = await mount();
	await f.offline();
	const source = await enqueue(f);
	const host = queueHosts.get(sheetKey("hub-1", f.ref));
	if (!host?.ghosts[0]) throw new Error("missing source");
	await act(async () => {
		await host.act(host.ghosts[0], "edit");
	});
	expect(f.document.getSnapshot().record.composer?.recoveryIds).toEqual([source.clientMutationId]);

	const db = sqlite.ports.get("evener-drafts.db") as SqliteSync;
	db.execSync("PRAGMA query_only = ON");
	act(() => f.document.edit("later edit"));
	expect(f.document.getSnapshot().error).not.toBeNull();
	expect(f.document.getSnapshot().record.composer?.recoveryIds).toEqual([source.clientMutationId]);
	// The failed save must not offer the already-owned editing source for Discard.
	expect(f.tree.root.findAllByProps({ accessibilityLabel: "Discard" })).toHaveLength(0);
	expect(await f.runtime.storage.getRecovery(source.clientMutationId)).toBeDefined();

	db.execSync("PRAGMA query_only = OFF");
	await act(async () => pressable(f.tree, "Retry saving")?.props.onPress());
	await settle();
	expect(f.document.getSnapshot().record).toMatchObject({
		draft: "later edit",
		composer: { recoveryIds: [source.clientMutationId] },
	});
	await act(async () => pressable(f.tree, "Send when you're back online")?.props.onPress());
	await settle();
	const persisted = await f.runtime.read(f.targetKey);
	expect(persisted.recovery).toEqual([]);
	expect(persisted.outbox).toHaveLength(1);
	expect(persisted.outbox[0].clientMutationId).not.toBe(source.clientMutationId);
	await f.recreateOffline();
	expect(f.document.getSnapshot().record.draft).toBe("");
	expect((await f.runtime.read(f.targetKey)).outbox).toHaveLength(1);
});

it("actual screen retries retained editing source when image capacity becomes available", async () => {
	const f = await mount();
	await f.offline();
	act(() => {
		for (let i = 0; i < MAX_ATTACHMENTS; i++)
			f.document.addImage({ id: `existing-${i}`, marker: i + 1, data: "AQID", mediaType: "image/png" });
	});
	await act(async () => {
		await f.runtime.submit({
			kind: "send",
			hubId: "hub-1",
			targetRef: f.ref,
			threadId: f.thread.id,
			instanceId: "instance",
			input: [{ type: "image", mediaType: "image/png", data: "AP8H" }],
		});
	});
	await settle();
	const source = (await f.runtime.read(f.targetKey)).outbox[0];
	const host = queueHosts.get(sheetKey("hub-1", f.ref));
	if (!host?.ghosts[0]) throw new Error("missing source");
	await act(async () => {
		await host.act(host.ghosts[0], "edit");
	});
	expect(f.document.getSnapshot().record.composer?.recoveryIds ?? []).toEqual([]);
	await act(async () => f.document.removeImage("existing-0"));
	await settle();
	expect(f.document.getSnapshot().record.composer?.recoveryIds).toEqual([source.clientMutationId]);
	expect(f.document.imagePreviews().at(-1)?.data).toBe("AP8H");
});

it("actual screen sends image-only work and preserves newer typing during receipt cleanup", async () => {
	const f = await mount();
	await f.offline();
	await act(async () => {
		await f.runtime.submit({
			kind: "send",
			hubId: "hub-1",
			targetRef: f.ref,
			threadId: f.thread.id,
			instanceId: "instance",
			input: [{ type: "image", mediaType: "image/png", name: "only.png", data: "AP8H" }],
			composer: { text: "", attachments: [{ marker: 4, mediaType: "image/png", name: "only.png", data: "AP8H" }] },
		});
	});
	await settle();
	const host = queueHosts.get(sheetKey("hub-1", f.ref));
	if (!host?.ghosts[0]) throw new Error("missing image source");
	await act(async () => {
		await host.act(host.ghosts[0], "edit");
	});
	const send = pressable(f.tree, "Send when you're back online");
	expect(send?.props.disabled).toBe(false);
	await act(async () => {
		send?.props.onPress();
		f.document.edit("newer typing");
	});
	await settle();
	expect(f.document.getSnapshot().record.draft).toBe("newer typing");
	expect((await f.runtime.read(f.targetKey)).outbox[0].payload.input).toEqual([
		{ type: "image", mediaType: "image/png", name: "only.png", data: "AP8H" },
	]);
	expect((await f.runtime.read(f.targetKey)).recovery).toEqual([]);
});

it("actual screen retries failed receipt acknowledgment without resending or clearing newer work", async () => {
	const f = await mount();
	await f.offline();
	const source = await enqueue(f);
	const host = queueHosts.get(sheetKey("hub-1", f.ref));
	if (!host?.ghosts[0]) throw new Error("missing source");
	await act(async () => {
		await host.act(host.ghosts[0], "edit");
	});
	const db = sqlite.ports.get("evener-mutations.db") as SqliteSync;
	db.execSync(
		"CREATE TRIGGER fail_ack BEFORE UPDATE OF recovery_resends ON mutation_sequence WHEN NEW.recovery_resends = '[]' AND OLD.recovery_resends <> '[]' BEGIN SELECT RAISE(ABORT, 'ack storage failed'); END",
	);
	await act(async () => {
		pressable(f.tree, "Send when you're back online")?.props.onPress();
		f.document.edit("newer");
	});
	await settle();
	expect(f.document.getSnapshot().record.recoveryResendAcks).toHaveLength(1);
	expect(await f.runtime.readRecoveryResend(f.targetKey, [source.clientMutationId])).toBeDefined();
	db.execSync("DROP TRIGGER fail_ack");
	await act(async () => pressable(f.tree, "Retry")?.props.onPress());
	await settle();
	expect(f.document.getSnapshot().record.recoveryResendAcks ?? []).toEqual([]);
	expect(await f.runtime.readRecoveryResend(f.targetKey, [source.clientMutationId])).toBeUndefined();
	expect(f.document.getSnapshot().record.draft).toBe("newer");
	expect((await f.runtime.read(f.targetKey)).outbox).toHaveLength(1);
});

async function prepareRecoveredCompact() {
	const f = await mount();
	await f.offline();
	const source = await enqueue(f, "recover me");
	const host = queueHosts.get(sheetKey("hub-1", f.ref));
	if (!host?.ghosts[0]) throw new Error("missing source");
	await act(async () => {
		host.act(host.ghosts[0], "edit");
	});
	f.thread.evener.capabilities.compact = true;
	const input = f.tree.root.findAll(
		(node) => String(node.type) === "TextInput" && node.props.value === "recover me",
	)[0];
	if (!input) throw new Error("missing recovered composer input");
	await act(async () => input.props.onChangeText("/compact"));
	act(() => f.client.emitStateChange("ready"));
	harness.connection = { ...harness.connection, state: "ready" };
	await act(async () => f.tree.update(<SessionScreen route={f.route} navigation={navigation} />));
	await settle();
	return { f, source };
}

it("actual screen completes edited /compact without sending and does not replay after reopen", async () => {
	const { f, source } = await prepareRecoveredCompact();
	await act(async () => pressable(f.tree, "Compact transcript")?.props.onPress());
	await settle();
	expect(f.requests.filter((r) => r.method === "thread/compact/start")).toHaveLength(1);
	expect(f.requests.filter((r) => r.method.startsWith("turn/")).map((r) => r.method)).toEqual([]);
	expect((await f.runtime.read(f.targetKey)).recovery).toEqual([]);
	expect(f.document.getSnapshot().record.draft).toBe("");
	await f.recreateOffline();
	expect(f.document.getSnapshot().record.draft).toBe("");
	expect((await f.runtime.read(f.targetKey)).recovery).toEqual([]);
	expect(f.requests.filter((r) => r.method === "thread/compact/start")).toHaveLength(1);
	expect(await f.runtime.storage.getOutbox(source.clientMutationId)).toBeUndefined();
});

it("actual screen protects and retries cleanup after /compact success without rerunning it", async () => {
	const { f, source } = await prepareRecoveredCompact();
	const db = sqlite.ports.get("evener-mutations.db") as SqliteSync;
	// Fail only the cleanup delete: the ledger shares this database, and a
	// globally read-only store would (correctly) refuse to start a submission.
	db.execSync(
		`CREATE TRIGGER fail_compact_cleanup BEFORE DELETE ON mutation_recovery WHEN OLD.client_mutation_id = '${source.clientMutationId}' BEGIN SELECT RAISE(ABORT, 'cleanup failed'); END`,
	);
	await act(async () => pressable(f.tree, "Compact transcript")?.props.onPress());
	await settle();
	expect(f.requests.filter((r) => r.method === "thread/compact/start")).toHaveLength(1);
	expect(f.document.getSnapshot().record.completedCommandRecoveryIds).toEqual([source.clientMutationId]);
	expect(f.tree.root.findAllByProps({ accessibilityLabel: "Discard" })).toHaveLength(0);
	expect((await f.runtime.read(f.targetKey)).recovery.map((row) => row.clientMutationId)).toEqual([
		source.clientMutationId,
	]);
	const draftDb = sqlite.ports.get("evener-drafts.db") as SqliteSync;
	draftDb.execSync("PRAGMA query_only = ON");
	act(() => f.document.edit("newer draft"));
	expect(f.document.getSnapshot().error).not.toBeNull();
	expect(f.document.getSnapshot().record.completedCommandRecoveryIds).toEqual([source.clientMutationId]);
	expect(f.tree.root.findAllByProps({ accessibilityLabel: "Discard" })).toHaveLength(0);
	draftDb.execSync("PRAGMA query_only = OFF");
	await act(async () => pressable(f.tree, "Retry saving")?.props.onPress());
	await settle();
	expect(f.document.getSnapshot().record.completedCommandRecoveryIds).toEqual([source.clientMutationId]);
	db.execSync("DROP TRIGGER fail_compact_cleanup");
	await act(async () => pressable(f.tree, "Retry")?.props.onPress());
	await settle();
	expect(f.document.getSnapshot().record.completedCommandRecoveryIds ?? []).toEqual([]);
	expect(await f.runtime.storage.getOutbox(source.clientMutationId)).toBeUndefined();
	expect(f.requests.filter((r) => r.method === "thread/compact/start")).toHaveLength(1);
	expect(f.document.getSnapshot().record.draft).toBe("newer draft");
});

it("actual screen keeps recovered command work when the built-in RPC fails", async () => {
	const { f, source } = await prepareRecoveredCompact();
	harness.rpcFailure = "thread/compact/start";
	await act(async () => pressable(f.tree, "Compact transcript")?.props.onPress());
	await settle();
	expect(f.document.getSnapshot().record).toMatchObject({
		draft: "/compact",
		composer: { recoveryIds: [source.clientMutationId] },
	});
	expect((await f.runtime.read(f.targetKey)).recovery.map((row) => row.clientMutationId)).toEqual([
		source.clientMutationId,
	]);
	expect(f.requests.filter((r) => r.method.startsWith("turn/")).map((r) => r.method)).toEqual([]);
	await f.recreateOffline();
	expect(f.document.getSnapshot().record.draft).toBe("/compact");
	expect(f.requests.filter((r) => r.method === "thread/compact/start")).toHaveLength(1);
});

it("replacement identity supersedes cache and failed replacement write removes stale authority", async () => {
	const f = await mount();
	const key = `queued-message-identity:v1:${f.targetKey}`;
	f.thread.id = "replacement-thread";
	f.thread.evener.instanceId = "replacement-instance";
	harness.kvFailure = "write";
	const tree = await f.recreateOffline();
	expect(Storage.getItemSync(key)).toBeNull();
	harness.kvFailure = "";
	await act(async () => pressable(tree, "Retry")?.props.onPress());
	expect(JSON.parse(Storage.getItemSync(key) as string)).toEqual({
		threadId: "replacement-thread",
		instanceId: "replacement-instance",
	});
});

it.each(["read", "invalid", "hub", "ref"])(
	"offline cache %s failure cannot grant authority and can be repaired",
	async (failure) => {
		const f = await mount();
		await f.offline();
		const source = await enqueue(f);
		const key = `queued-message-identity:v1:${f.targetKey}`;
		const identity = Storage.getItemSync(key) as string;
		if (failure === "read") harness.kvFailure = "read";
		else if (failure === "invalid") Storage.setItemSync(key, JSON.stringify({ threadId: 7 }));
		else {
			Storage.removeItemSync(key);
			Storage.setItemSync(
				`queued-message-identity:v1:${nativeMutationTargetKey(failure === "hub" ? "other" : "hub-1", failure === "ref" ? "other" : f.ref)}`,
				identity,
			);
		}
		const tree = await f.recreateOffline();
		expect(queueHosts.get(sheetKey("hub-1", f.ref))?.ghosts.some((g) => g.localQueued)).toBe(false);
		expect(await f.runtime.storage.getOutbox(source.clientMutationId)).toBeDefined();
		harness.kvFailure = "";
		Storage.setItemSync(key, identity);
		if (failure === "read" || failure === "invalid") await act(async () => pressable(tree, "Retry")?.props.onPress());
		else await f.recreateOffline();
		await settle();
		const host = queueHosts.get(sheetKey("hub-1", f.ref));
		const ghost = host?.ghosts.find((g) => g.localQueued);
		expect(ghost).toBeDefined();
		await act(async () => {
			if (ghost) await host?.act(ghost, "cancel");
		});
		expect(await f.runtime.storage.getOutbox(source.clientMutationId)).toBeUndefined();
		expect(f.requests.filter((r) => r.method.startsWith("turn/"))).toEqual([]);
	},
);
it("failed identity write is not durable success, Retry repairs offline recreation", async () => {
	harness.kvFailure = "write";
	const f = await mount();
	const key = `queued-message-identity:v1:${f.targetKey}`;
	expect(Storage.getItemSync(key)).toBeNull();
	harness.kvFailure = "";
	await act(async () => pressable(f.tree, "Retry")?.props.onPress());
	expect(JSON.parse(Storage.getItemSync(key) as string)).toEqual({ threadId: f.thread.id, instanceId: "instance" });
	await f.offline();
	const source = await enqueue(f);
	await f.recreateOffline();
	const host = queueHosts.get(sheetKey("hub-1", f.ref));
	const ghost = host?.ghosts.find((g) => g.localQueued);
	expect(ghost).toBeDefined();
	await act(async () => {
		if (ghost) await host?.act(ghost, "edit");
	});
	expect(f.document.getSnapshot().record.composer?.recoveryIds).toEqual([source.clientMutationId]);
});

it("actual screen preserves mentions through Edit ordinary typing and Send", async () => {
	const f = await mount();
	await f.offline();
	const text = "/inspect then /inspect";
	const composer = {
		text,
		attachments: [],
		skillNames: ["inspect"],
		commandNames: ["inspect"],
		mentions: [
			{ kind: "skill" as const, name: "inspect", offset: 0 },
			{ kind: "command" as const, name: "inspect", offset: 14 },
		],
	};
	// Equal text is not ownership: only the chosen source may be consumed.
	for (let i = 0; i < 2; i++) {
		await act(async () => {
			await f.runtime.submit({
				kind: "send",
				hubId: "hub-1",
				targetRef: f.ref,
				threadId: f.thread.id,
				instanceId: "instance",
				composer,
				input: buildComposerInput(text, [], composer.skillNames, composer.commandNames, composer.mentions),
			});
		});
	}
	await settle();
	const [source, untouched] = (await f.runtime.read(f.targetKey)).outbox;
	if (!source || !untouched) throw new Error("missing sources");
	await act(async () => {
		f.document.edit("current ");
		f.document.addImage({ id: "current", marker: 3, name: "current.png", mediaType: "image/png", data: "AQID" });
	});
	const host = queueHosts.get(sheetKey("hub-1", f.ref));
	const ghost = host?.ghosts.find(
		(g) => g.origin.kind === "pending" && g.origin.clientMutationId === source.clientMutationId,
	);
	if (!host || !ghost) throw new Error("missing editable ghost");
	await act(async () => {
		await host.act(ghost, "edit");
	});
	const restored = "current [image 3]\n\n/inspect then /inspect";
	expect(f.document.getSnapshot().record.draft).toBe(restored);
	const revised = `😀 please ${restored}`;
	const input = f.tree.root.findAll((node) => String(node.type) === "TextInput" && node.props.value === restored)[0];
	if (!input) throw new Error("missing actual composer input");
	await act(async () => input.props.onChangeText(revised));
	await act(async () => pressable(f.tree, "Send when you're back online")?.props.onPress());
	await settle();
	const state = await f.runtime.read(f.targetKey);
	const fresh = state.outbox.find((record) => record.clientMutationId !== untouched.clientMutationId);
	expect(fresh?.clientMutationId).not.toBe(source.clientMutationId);
	const wireText = "😀 please current (attached image 3: current.png)\n\n/inspect then /inspect";
	expect(fresh?.payload.input).toEqual([
		{
			type: "text",
			text: wireText,
			mentions: [
				{ kind: "skill", name: "inspect", offset: wireText.indexOf("/inspect") },
				{ kind: "command", name: "inspect", offset: wireText.lastIndexOf("/inspect") },
			],
		},
		{ type: "image", name: "current.png", mediaType: "image/png", data: "AQID" },
		{ type: "skill", name: "inspect" },
		{ type: "command", name: "inspect" },
	]);
	expect(await f.runtime.storage.getOutbox(untouched.clientMutationId)).toEqual(untouched);
	expect(await f.runtime.storage.getOutbox(source.clientMutationId)).toBeUndefined();
	expect(state.recovery).toEqual([]);
	expect(f.document.getSnapshot().record.composer?.recoveryIds ?? []).toEqual([]);
	expect(f.document.getSnapshot().record.unconfirmedComposer?.recoveryIds ?? []).toEqual([]);
});

it.each(["offline", "online", "command"])(
	"actual screen sends full edited composition through %s with exact source ownership",
	async (mode) => {
		const f = await mount(false, mode === "command");
		await f.offline();
		const text = mode === "command" ? "/queue /skill" : "/skill [image 7]";
		const composer = {
			text,
			attachments:
				mode === "command" ? [] : [{ marker: 7, name: "original.png", mediaType: "image/png", data: "AP8H" }],
			skillNames: ["skill"],
			commandNames: ["command"],
			mentions: [{ kind: "skill" as const, name: "skill", offset: mode === "command" ? 7 : 0 }],
		};
		await act(async () => {
			await f.runtime.submit({
				kind: "send",
				hubId: "hub-1",
				targetRef: f.ref,
				threadId: f.thread.id,
				instanceId: "instance",
				input: buildComposerInput(
					text,
					composer.attachments,
					composer.skillNames,
					composer.commandNames,
					composer.mentions,
				),
				composer,
			});
		});
		await settle();
		const source = (await f.runtime.read(f.targetKey)).outbox[0];
		const host = queueHosts.get(sheetKey("hub-1", f.ref));
		if (!host?.ghosts[0]) throw new Error("missing source");
		await act(async () => {
			await host.act(host.ghosts[0], "edit");
		});
		expect(f.document.getSnapshot().record.composer?.recoveryIds).toEqual([source.clientMutationId]);
		if (mode !== "offline") {
			act(() => f.client.emitStateChange("ready"));
			harness.connection = { ...harness.connection, state: "ready" };
			await act(async () => f.tree.update(<SessionScreen route={f.route} navigation={navigation} />));
			await settle();
		}
		const send = pressable(
			f.tree,
			mode === "offline" ? "Send when you're back online" : mode === "command" ? "Queue" : "Send",
		);
		expect(send?.props.disabled).toBe(false);
		await act(async () => send?.props.onPress());
		await settle();
		const expected = [
			{
				type: "text",
				text: mode === "command" ? "/skill" : "/skill (attached image 1: original.png)",
				mentions: [{ kind: "skill", name: "skill", offset: 0 }],
			},
			...(mode === "command" ? [] : [{ type: "image", name: "original.png", mediaType: "image/png", data: "AP8H" }]),
			{ type: "skill", name: "skill" },
			{ type: "command", name: "command" },
		];
		if (mode === "offline") expect((await f.runtime.read(f.targetKey)).outbox[0]?.payload.input).toEqual(expected);
		else expect(f.requests.filter((r) => r.method.startsWith("turn/")).map((r) => r.params.input)).toEqual([expected]);
		expect((await f.runtime.read(f.targetKey)).recovery).toEqual([]);
		expect(f.document.getSnapshot().record.unconfirmedComposer?.recoveryIds ?? []).toEqual([]);
	},
);

// Component/adapter journey: ownership is the production runtime and document,
// not a scripted action result. The actual-screen cases above cover routing.
const journeyCleanups: Array<() => Promise<void>> = [];
afterEach(async () => {
	for (const cleanup of journeyCleanups.splice(0).reverse()) await cleanup();
});
function journey() {
	const directory = mkdtempSync(join(process.env.EVENER_SCRATCH_DIR ?? tmpdir(), "rendered-queued-"));
	const destination = { hubId: "journey-hub", sessionRef: `journey-${++serial}` };
	const key = nativeMutationTargetKey(destination.hubId, destination.sessionRef);
	const identity = createClientIdentity(undefined, { randomUUID });
	let mutationDb = openSqliteSyncDouble(join(directory, "mutations.sqlite"));
	let draftDb = openSqliteSyncDouble(join(directory, "drafts.sqlite"));
	let count = 0;
	const options = {
		createMutationId: () => `journey-${++count}`,
		getOwnClientId: identity.ownClientId,
		setInterval: () => 1,
		clearInterval: () => {},
	};
	let runtime = new NativeMutationRuntime(mutationDb.port, options);
	let repository = new DraftRepository(draftDb.port);
	let document = new DraftDocument(() => repository, destination);
	let imageSelection: ImageSelection | undefined;
	let lastError: unknown;
	const registration = () =>
		runtime.registerTarget(destination.hubId, destination.sessionRef, null, {
			threadId: "thread",
			instanceId: "instance",
		});
	registration();
	const request = (extra: Partial<NativeMutationRequest> = {}): NativeMutationRequest =>
		({
			kind: "send",
			hubId: destination.hubId,
			targetRef: destination.sessionRef,
			threadId: "thread",
			instanceId: "instance",
			input: [{ type: "text", text: "same" }],
			...extra,
		}) as NativeMutationRequest;
	const owner = {};
	const hostKey = sheetKey(destination.hubId, destination.sessionRef);
	let list: Ghost[] = [];
	let tree: ReactTestRenderer | undefined;
	let surface: "rows" | "sheet" = "rows";
	const actOn = async (g: Ghost, a: GhostAction) => {
		if (g.origin.kind !== "pending") throw new Error("not a local source");
		try {
			if (a === "edit") await editQueuedMessageIntoDraft(runtime, key, g.origin.clientMutationId, document);
			else if (a === "cancel") await runtime.removeQueuedMessage(g.origin.clientMutationId, key);
			else if (a === "sendNow") await runtime.releaseCanceled(g.origin.clientMutationId, key);
		} catch (error) {
			lastError = error;
		}
		return null;
	};
	const element = () =>
		surface === "sheet" ? (
			<QueueSheet
				route={
					{
						key: "queue",
						name: "QueueSheet",
						params: { hubId: destination.hubId, ref: destination.sessionRef },
					} as never
				}
				navigation={navigation as never}
			/>
		) : (
			<GhostRowContext.Provider
				value={{
					disabled: false,
					canEdit: false,
					editHint: null,
					canEditParked: false,
					parkedEditHint: null,
					onAction: actOn,
					onMore: () => {},
				}}
			>
				{withGhostRows([], list)
					.filter(isGhostRow)
					.map((row) => (
						<GhostRowView key={row.id} row={row} />
					))}
			</GhostRowContext.Provider>
		);
	async function refresh(nextSurface = surface) {
		surface = nextSurface;
		const persisted = await runtime.read(key);
		const pending = reconcilePendingEntries(
			key,
			[...persisted.outbox, ...persisted.optimistic],
			undefined,
			runtime.submittedHere(key),
			() => true,
		);
		list = ghosts(null, pending, null, [], false, {
			targetRef: key,
			threadId: "thread",
			instanceId: "instance",
			records: persisted.outbox,
			readiness: { document: document.getSnapshot(), encodingAttachments: imageSelection?.getSnapshot().busy ?? false },
		});
		await act(async () => {
			queueHosts.provide(hostKey, owner, {
				ghosts: list.filter(isQueuedGhost),
				disabled: false,
				act: actOn,
				showOnSession: () => {},
			});
			if (tree) tree.update(element());
			else tree = render(element());
		});
	}
	async function choose(id: string, action: GhostAction, beforeAction?: () => Promise<void>) {
		const ghost = list.find((g) => g.origin.kind === "pending" && g.origin.clientMutationId === id);
		if (!tree || !ghost) throw new Error("missing rendered identity");
		vi.mocked(ActionSheetIOS.showActionSheetWithOptions).mockClear();
		const bubble = tree.root.findAllByProps({ ghost })[0];
		const press = bubble?.findAll(
			(node) =>
				String(node.type) === "Pressable" && node.props.accessibilityLabel === `${ghost.text}. ${ghost.caption}`,
		)[0];
		act(() => press?.props.onPress());
		const call = vi.mocked(ActionSheetIOS.showActionSheetWithOptions).mock.calls[0];
		if (!call) throw new Error("menu did not open");
		const index = ghost.menu.indexOf(action);
		if (index < 0) throw new Error("action unavailable");
		await beforeAction?.();
		await act(async () => call[1](index));
		await settle();
		await refresh();
	}
	const f = {
		destination,
		key,
		request,
		get runtime() {
			return runtime;
		},
		get document() {
			return document;
		},
		get repository() {
			return repository;
		},
		get mutationDb() {
			return mutationDb.database;
		},
		get draftDb() {
			return draftDb.database;
		},
		get list() {
			return list;
		},
		get lastError() {
			return lastError;
		},
		selectImages(picker: ImagePicker) {
			imageSelection = new ImageSelection(document, picker);
			return imageSelection;
		},
		refresh,
		choose,
		async enqueue(extra: Partial<NativeMutationRequest> = {}) {
			await runtime.submit(request(extra));
			return (await runtime.read(key)).outbox.at(-1)?.clientMutationId as string;
		},
		async reopen() {
			if (tree) {
				act(() => tree?.unmount());
				tree = undefined;
			}
			queueHosts.release(hostKey, owner);
			await runtime.stop();
			document.forget();
			mutationDb.database.close();
			draftDb.database.close();
			mutationDb = openSqliteSyncDouble(join(directory, "mutations.sqlite"));
			draftDb = openSqliteSyncDouble(join(directory, "drafts.sqlite"));
			runtime = new NativeMutationRuntime(mutationDb.port, options);
			repository = new DraftRepository(draftDb.port);
			document = new DraftDocument(() => repository, destination);
			registration();
		},
	};
	journeyCleanups.push(async () => {
		act(() => queueHosts.release(hostKey, owner));
		await runtime.stop();
		mutationDb.database.close();
		draftDb.database.close();
		rmSync(directory, { recursive: true });
	});
	return f;
}
it("rendered legacy edit preserves literal and gapped markers beside existing images after reopen", async () => {
	const f = journey();
	f.document.edit("[image 1] existing");
	f.document.addImage({ id: "current", marker: 9, mediaType: "image/png", data: "AQID" });
	const id = await f.enqueue({
		input: [
			{ type: "text", text: "literal [image 9] gap [image 3]" },
			{ type: "image", mediaType: "image/png", data: "AP8H" },
		],
	});
	await f.refresh("sheet");
	await f.choose(id, "edit");
	await f.reopen();
	expect(f.document.getSnapshot().record.draft).toBe("[image 1] existing[image 2]\n\nliteral [image 9] gap [image 3]");
	expect(f.document.imagePreviews().map(({ marker, data }) => [marker, data])).toEqual([
		[2, "AQID"],
		[4, "AP8H"],
	]);
});

it.each(["rows", "sheet"] as const)(
	"%s keeps Remove available during real encoding and restores Edit after encoding",
	async (surface) => {
		const f = journey();
		const id = await f.enqueue();
		const removed = await f.enqueue();
		let finish!: (image: { mediaType: "image/png"; data: string }) => void;
		const selection = f.selectImages({
			pick: async () => [{ uri: "image", name: "picked.png", type: "image/png", size: 3 }],
			capture: async () => [],
			encode: () =>
				new Promise((resolve) => {
					finish = resolve;
				}),
			id: () => "picked",
		});
		const selecting = selection.choose();
		await settle();
		await f.refresh(surface);
		expect(selection.getSnapshot().busy).toBe(true);
		expect(f.list.every((g) => g.menu.join() === "cancel")).toBe(true);
		await f.choose(removed, "cancel");
		finish({ mediaType: "image/png", data: "AQID" });
		await selecting;
		await f.refresh(surface);
		await f.choose(id, "edit");
		expect(f.document.imagePreviews()[0].data).toBe("AQID");
		expect(f.document.getSnapshot().record.composer?.recoveryIds).toEqual([id]);
		expect(await f.runtime.storage.getOutbox(removed)).toBeUndefined();
	},
);

it.each(["promote", "empty-drain", "content-drain"])(
	"rendered %s separates control preview from owned composition",
	async (kind) => {
		const f = journey();
		const id = await f.enqueue(
			kind === "promote"
				? {
						kind: "promote",
						input: [],
						queueEntry: { index: 0, id: "daemon" },
						display: [{ type: "text", text: "preview" }],
					}
				: {
						kind: "steer",
						input: kind === "content-drain" ? [{ type: "text", text: "owned" }] : [],
						expectedQueueRevision: 3,
						display: [{ type: "text", text: "preview" }],
					},
		);
		await f.refresh("sheet");
		expect(f.list[0].menu).toEqual(kind === "content-drain" ? ["edit", "cancel"] : ["cancel"]);
		await f.choose(id, kind === "content-drain" ? "edit" : "cancel");
		await f.reopen();
		expect(await f.runtime.storage.getOutbox(id)).toBeUndefined();
		expect(f.document.getSnapshot().record.draft).toBe(kind === "content-drain" ? "owned" : "");
	},
);

it.each(["edit", "cancel"] as const)(
	"rendered %s rolls back mutation write failure, then works after repair",
	async (action) => {
		const f = journey();
		const id = await f.enqueue();
		await f.refresh("rows");
		f.mutationDb.exec("PRAGMA query_only = ON");
		await f.choose(id, action);
		expect(f.lastError).toBeDefined();
		expect(await f.runtime.storage.getOutbox(id)).toBeDefined();
		expect(f.document.getSnapshot().record.composer?.recoveryIds).toBeUndefined();
		f.mutationDb.exec("PRAGMA query_only = OFF");
		await f.refresh("sheet");
		await f.choose(id, action);
		await f.reopen();
		expect(await f.runtime.storage.getOutbox(id)).toBeUndefined();
		expect(f.document.getSnapshot().record.composer?.recoveryIds ?? []).toEqual(action === "edit" ? [id] : []);
	},
);

it("rendered edit retains complete claimed source after draft write failure and recovers after reopen", async () => {
	const f = journey();
	f.document.edit("prior");
	const id = await f.enqueue({
		input: [
			{ type: "text", text: "source" },
			{ type: "image", name: "source.png", mediaType: "image/png", data: "AP8H" },
		],
	});
	await f.refresh("sheet");
	f.draftDb.exec("PRAGMA query_only = ON");
	await f.choose(id, "edit");
	expect(f.document.getSnapshot().error).not.toBeNull();
	expect(f.repository.read(f.destination).draft).toBe("prior");
	expect((await f.runtime.read(f.key)).recovery.map((r) => r.clientMutationId)).toEqual([id]);
	f.draftDb.exec("PRAGMA query_only = OFF");
	await f.reopen();
	expect(await editQueuedMessageIntoDraft(f.runtime, f.key, id, f.document)).toBe("restored");
	expect(f.document.imagePreviews()[0].data).toBe("AP8H");
	expect(f.document.getSnapshot().record.composer?.recoveryIds).toEqual([id]);
});

it.each(["stop", "retry", "claim", "admit", "identity"])(
	"rendered stale Edit after %s cannot duplicate or consume the wrong source",
	async (race) => {
		const f = journey();
		const id = await f.enqueue();
		if (race === "retry")
			await f.runtime.storage.enqueueInterruptAndCancel({
				targetRef: f.key,
				method: "turn/interrupt",
				payload: {},
				attachments: [],
				optimisticDisplay: null,
			});
		await f.refresh("rows");
		await f.choose(id, "edit", async () => {
			if (race === "stop")
				await f.runtime.storage.enqueueInterruptAndCancel({
					targetRef: f.key,
					method: "turn/interrupt",
					payload: {},
					attachments: [],
					optimisticDisplay: null,
				});
			if (race === "retry") await f.runtime.releaseCanceled(id, f.key);
			if (race === "claim") await f.runtime.editQueuedMessage(id, f.key);
			if (race === "admit") await f.runtime.storage.markAttempted(id);
			if (race === "identity")
				f.runtime.registerTarget(f.destination.hubId, f.destination.sessionRef, null, {
					threadId: "other",
					instanceId: "other",
				});
		});
		const persisted = await f.runtime.read(f.key);
		if (race === "identity" || race === "admit") {
			expect(persisted.outbox.some((r) => r.clientMutationId === id)).toBe(true);
			expect(f.document.getSnapshot().record.draft).toBe("");
		} else {
			expect(f.document.getSnapshot().record.composer?.recoveryIds).toEqual([id]);
			expect(persisted.recovery.filter((r) => r.clientMutationId === id)).toHaveLength(1);
		}
	},
);

it.each([false, true])("nativeQueuedControlsPreserveAndResendOnce held=%s", async (held) => {
	const f = journey();
	const original = {
		text: "same [image 7] /skill",
		attachments: [{ marker: 7, name: "source.png", mediaType: "image/png", data: "AP8H" }],
		skillNames: ["skill"],
		commandNames: ["command"],
		mentions: [{ kind: "skill" as const, name: "skill", offset: 15 }],
	};
	const extra = {
		input: buildComposerInput(original.text, original.attachments, original.skillNames, original.commandNames),
		composer: original,
	};
	const first = await f.enqueue(extra);
	const removed = await f.enqueue(extra);
	if (held)
		await f.runtime.storage.enqueueInterruptAndCancel({
			targetRef: f.key,
			method: "turn/interrupt",
			payload: { ref: f.destination.sessionRef },
			attachments: [],
			optimisticDisplay: null,
		});
	f.document.edit("existing");
	await f.refresh("rows");
	expect(f.list.map((g) => g.origin)).toEqual([
		{ kind: "pending", clientMutationId: first },
		{ kind: "pending", clientMutationId: removed },
	]);
	await f.choose(first, "edit");
	expect(f.document.getSnapshot().record.composer?.recoveryIds).toEqual([first]);
	await f.refresh("sheet");
	await f.choose(removed, "cancel");
	const expectedPreservedDraft = "existing\n\nsame [image 1] /skill";
	await f.reopen();
	expect(await f.runtime.storage.getOutbox(removed)).toBeUndefined();
	expect(f.document.getSnapshot().record.draft).toBe(expectedPreservedDraft);
	expect(f.repository.imageInputs(f.destination, f.document.getSnapshot().record.images ?? [])).toMatchObject([
		{ data: "AP8H", name: "source.png", marker: 1 },
	]);
	const client = new FakeClient("ready");
	client.on(
		"turn/interrupt",
		(params) =>
			({
				receipt: {
					clientMutationId: params.clientMutationId,
					disposition: "applied",
					threadId: "thread",
					instanceId: "instance",
					projectionState: "pending",
				},
			}) as never,
	);
	client.on(
		"turn/start",
		(params) =>
			({
				receipt: {
					clientMutationId: params.clientMutationId,
					disposition: "applied",
					threadId: "thread",
					turnId: "turn",
					projectionState: "pending",
					instanceId: "instance",
				},
				turn: { id: "turn" },
			}) as never,
	);
	f.runtime.registerTarget(f.destination.hubId, f.destination.sessionRef, client, {
		threadId: "thread",
		instanceId: "instance",
	});
	// A fresh edited submit must not reuse either original identity.
	await f.document.submit(async (text, images, context) => {
		await f.runtime.submit(
			f.request({
				input: buildComposerInput(text, images, context.composer?.skillNames, context.composer?.commandNames),
				...context,
			}),
		);
		return true;
	});
	await reconcileEditedResends(f.runtime, f.key, f.document);
	expect(f.document.getSnapshot().record.unconfirmedComposer?.recoveryIds).toBeUndefined();
	expect((await f.runtime.read(f.key)).recovery).toEqual([]);
	expect(await f.runtime.readRecoveryResend(f.key, [first])).toBeUndefined();
	const rows = (await f.runtime.read(f.key)).outbox.filter((row) => row.method !== "turn/interrupt");
	expect(rows).toHaveLength(1);
	expect([first, removed]).not.toContain(rows[0].clientMutationId);
	// Admission awaits an authoritative read before transport is released.
	const lease = f.runtime.beginAuthoritativeRead(f.destination.hubId, f.destination.sessionRef, client);
	if (!lease) throw new Error("no read lease");
	await f.runtime.reconcileAuthoritativeRead(lease, {
		thread: {
			id: "thread",
			status: { type: "idle" },
			evener: {
				ref: f.destination.sessionRef,
				instanceId: "instance",
				mutationStateAuthoritative: true,
				queue: { revision: 0, clientMutationIds: [] },
			},
		},
	} as never);
	await settle();
	await vi.waitFor(() =>
		expect(
			client.calls.filter((c) => c.method === "turn/start").map((c) => (c.params as { input: unknown }).input),
		).toEqual([
			[
				{ type: "text", text: "existing\n\nsame (attached image 1: source.png) /skill" },
				{ type: "image", name: "source.png", mediaType: "image/png", data: "AP8H" },
				{ type: "skill", name: "skill" },
				{ type: "command", name: "command" },
			],
		]),
	);
	await f.reopen();
	expect(await f.runtime.storage.getOutbox(removed)).toBeUndefined();
	expect(f.document.getSnapshot().record.draft).toBe("");
});

async function editRecoveryIntoDraft(f: Awaited<ReturnType<typeof mount>>, text: string) {
	await f.offline();
	const source = await enqueue(f, text);
	const host = queueHosts.get(sheetKey("hub-1", f.ref));
	const ghost = host?.ghosts.find(
		(item) => item.origin.kind === "pending" && item.origin.clientMutationId === source.clientMutationId,
	);
	if (!host || !ghost) throw new Error("no local ghost");
	await act(async () => {
		await host.act(ghost, "edit");
	});
	expect(f.document.getSnapshot().record.composer?.recoveryIds).toEqual([source.clientMutationId]);
	await reconnect(f);
	return source;
}

async function replaceDraftWithGoal(f: Awaited<ReturnType<typeof mount>>) {
	vi.stubGlobal("requestAnimationFrame", (frame: (time: number) => void) => {
		frame(0);
		return 0;
	});
	alertRequests.length = 0;
	await act(async () => {
		sessionInfoHosts.get(sheetKey("hub-1", f.ref))?.editGoal();
	});
	expect(alertRequests).toHaveLength(1);
	const replace = alertRequests[0].buttons?.find((button) => button.text === "Replace draft");
	expect(replace).toBeDefined();
	await act(async () => {
		replace?.onPress?.();
	});
	await settle();
	vi.unstubAllGlobals();
}

it.each([
	["without parked images", false],
	["with parked images", true],
] as const)("goal edit replaces an edited recovery %s", async (_label, withImage) => {
	const f = await mount(false, false, undefined, "skill", {
		goal: { objective: "ship", status: "active", iterations: 1 },
	});
	const source = await editRecoveryIntoDraft(f, "old goal work");
	if (withImage)
		await act(async () => {
			f.document.addImage({ id: "old-image", marker: 3, mediaType: "image/png", data: "AQID" }, 0);
		});
	const before = f.document.getSnapshot().record;
	expect(before.draft).toContain("old goal work");
	expect(before.images?.map((image) => image.id)).toEqual(withImage ? ["old-image"] : []);

	await replaceDraftWithGoal(f);

	let record = f.document.getSnapshot().record;
	expect(record.draft).toBe("/goal ship");
	expect(record.composer).toBeUndefined();
	expect(record.images).toBeUndefined();
	expect(record.unconfirmed).toBeNull();
	expect(record.parkedCompositions?.[0]?.text).toBe(before.draft);
	expect(record.parkedCompositions?.[0]?.composer?.recoveryIds).toEqual([source.clientMutationId]);
	expect(record.parkedCompositions?.[0]?.images?.map((image) => image.id)).toEqual(withImage ? ["old-image"] : []);
	if (withImage)
		expect(f.tree.root.findAll((node) => node.props.accessibilityLabel === "Image 3: attachment")).not.toHaveLength(0);

	// The screen's own reconciliation must leave the replacement alone.
	await act(async () => {
		await reconcileEditedResends(f.runtime, f.targetKey, f.document);
		const retained = (await f.runtime.read(f.targetKey)).recovery.filter((row) => row.recoveryKind === "editing");
		for (const row of retained)
			await editQueuedMessageIntoDraft(f.runtime, f.targetKey, row.clientMutationId, f.document);
	});
	record = f.document.getSnapshot().record;
	expect(record.draft).toBe("/goal ship");
	expect(record.parkedCompositions?.[0]?.text).toBe(before.draft);

	// The parked image must not route the goal through ordinary message dispatch.
	const request = f.client.request;
	f.client.request = async (method, params) => {
		if (method !== "goal/set") return request(method, params);
		f.requests.push({ method, params: params as Record<string, unknown> });
		return { started: true } as never;
	};
	const send = pressable(f.tree, "Set goal");
	expect(send?.props.disabled).toBe(false);
	await act(async () => send?.props.onPress());
	await settle();
	expect(f.requests.filter((item) => item.method === "goal/set")).toHaveLength(1);
	expect(f.requests.filter((item) => item.method.startsWith("turn/"))).toHaveLength(0);

	// Sending the goal leaves the parked work recoverable as its own composition.
	record = f.document.getSnapshot().record;
	expect(record.parkedCompositions?.[0]?.text).toBe(before.draft);
	expect(record.parkedCompositions?.[0]?.images?.map((image) => image.id)).toEqual(withImage ? ["old-image"] : []);
	await act(async () => {
		f.document.restoreParked(record.parkedCompositions?.[0]?.id ?? "");
	});
	record = f.document.getSnapshot().record;
	expect(record.draft).toBe(before.draft);
	expect(record.parkedCompositions).toBeUndefined();
	expect(record.composer?.recoveryIds).toEqual([source.clientMutationId]);
});

it("parked ghost Discard keeps an unrelated unconfirmed send", async () => {
	const f = await mount(false, false, undefined, "skill", {
		goal: { objective: "ship", status: "active", iterations: 1 },
	});
	await editRecoveryIntoDraft(f, "old goal work");
	await replaceDraftWithGoal(f);
	await act(async () => {
		await f.document.submit(async () => false);
	});
	await settle();

	const parked = f.tree.root.findAll((node) => node.props.ghost?.origin?.kind === "parked")[0];
	expect(parked).toBeDefined();
	await act(async () => {
		await parked?.props.onAction("discard");
	});

	const record = f.document.getSnapshot().record;
	expect(record.parkedCompositions).toBeUndefined();
	expect(record.unconfirmed).toBe("/goal ship");
});

it("text-only parked ghost never shows an unrelated send's images", async () => {
	const f = await mount(false, false, undefined, "skill", {
		goal: { objective: "ship", status: "active", iterations: 1 },
	});
	await editRecoveryIntoDraft(f, "old goal work");
	await replaceDraftWithGoal(f);
	await act(async () => {
		f.document.addImage({ id: "other", marker: 5, mediaType: "image/png", data: "AQID" });
		await f.document.submit(async () => false);
	});
	await settle();

	const record = f.document.getSnapshot().record;
	expect(record.parkedCompositions?.[0]?.images ?? []).toEqual([]);
	expect(record.unconfirmedImages?.map((image) => image.id)).toEqual(["other"]);
	const parked = f.tree.root.findAll((node) => node.props.ghost?.origin?.kind === "parked")[0];
	expect(parked).toBeDefined();
	expect(parked?.props.attachments?.props.images).toEqual([]);
});

it("parked Edit waits for a deferred encoder and keeps the pending image", async () => {
	const f = await mount(false, false, undefined, "skill", {
		goal: { objective: "ship", status: "active", iterations: 1 },
	});
	await editRecoveryIntoDraft(f, "old goal work");
	await act(async () => {
		for (let index = 0; index < 8; index++)
			f.document.addImage({
				id: `parked-${index}`,
				marker: index + 1,
				mediaType: "image/png",
				data: "AQID",
			});
	});
	await replaceDraftWithGoal(f);
	await act(async () => {
		f.document.edit("");
	});
	const parked = f.tree.root.findAll((node) => node.props.ghost?.origin?.kind === "parked")[0];
	expect(parked).toBeDefined();
	expect(f.document.getSnapshot().record.parkedCompositions?.[0]?.images).toHaveLength(8);

	vi.mocked(launchImageLibraryAsync).mockResolvedValueOnce({
		canceled: false,
		assets: [
			{
				uri: "photo.jpg",
				fileName: "picked.jpg",
				mimeType: "image/jpeg",
				fileSize: 100,
				width: 4000,
				height: 3000,
			},
		],
	} as never);
	let finish!: () => void;
	harness.manipulate.mockImplementationOnce(() => ({
		resize: () => {},
		renderAsync: () =>
			new Promise((resolve) => {
				finish = () =>
					resolve({
						width: 100,
						height: 100,
						saveAsync: async () => ({ base64: "AQID", uri: "encoded" }),
						release: () => {},
					});
			}),
		release: () => {},
	}));
	const show = vi.mocked(ActionSheetIOS.showActionSheetWithOptions);
	show.mockClear();
	await act(async () => pressable(f.tree, "Add")?.props.onPress());
	const chooseLibrary = show.mock.calls.at(-1)?.[1] as ((index: number) => void) | undefined;
	expect(chooseLibrary).toBeDefined();
	await act(async () => chooseLibrary?.(0));
	await settle();

	// Encoding is in flight: parked Edit is disabled and refuses a stale press.
	const busyParked = f.tree.root.findAll((node) => node.props.ghost?.origin?.kind === "parked")[0];
	expect(busyParked?.props.canEdit).toBe(false);
	await act(async () => {
		parked?.props.onAction("edit");
	});
	await settle();
	expect(f.document.getSnapshot().record.draft).toBe("");
	expect(f.document.getSnapshot().record.parkedCompositions).toHaveLength(1);

	await act(async () => {
		finish();
	});
	await settle();
	await vi.waitFor(() =>
		expect(f.document.getSnapshot().record.images?.map((image) => image.name)).toEqual(["picked.jpg"]),
	);
	expect(f.document.getSnapshot().record.parkedCompositions?.[0]?.images).toHaveLength(8);
});

it("actual screen keeps an occupied destination when the resume handoff is refused", async () => {
	const f = await mount();
	f.document.edit("existing destination work");
	const oldRef = `${f.ref}-old`;
	const oldDestination = { hubId: "hub-1", sessionRef: oldRef };
	const oldKey = nativeMutationTargetKey("hub-1", oldRef);
	async function submitOld(text: string) {
		await f.runtime.submit({
			kind: "send",
			hubId: "hub-1",
			targetRef: oldRef,
			threadId: `thread-${oldRef}`,
			instanceId: "instance",
			input: buildComposerInput(text, []),
			composer: { text, attachments: [] },
		});
	}
	await act(async () => {
		await submitOld("original recovered");
	});
	const source = (await f.runtime.read(oldKey)).outbox[0];
	if (!source) throw new Error("missing recovered source");
	await act(async () => {
		await f.runtime.storage.transferToRecovery(source.clientMutationId, "editing");
	});
	nativeDrafts().write(oldDestination, {
		draft: "edited recovered",
		unconfirmed: null,
		composer: { recoveryIds: [source.clientMutationId] },
	});
	await act(async () => {
		await submitOld("waiting message");
	});
	// The resume moved the editing row and its waiting message to the new ref.
	await act(async () => {
		f.runtime.storage.retarget(oldKey, {
			ref: f.targetKey,
			wireRef: f.ref,
			threadId: f.thread.id,
			instanceId: "instance",
		});
	});

	// The occupied destination refuses the handoff, so its own work stays and
	// the migrated editing row is marked released rather than auto-appended.
	act(() => {
		expect(drafts.handoff(oldDestination, { hubId: "hub-1", sessionRef: f.ref })).toBe(false);
	});
	expect(f.document.getSnapshot().record.releasedRecoveryIds).toContain(source.clientMutationId);

	await f.recreateOffline();
	await settle();
	expect(f.document.getSnapshot().record.draft).toBe("existing destination work");
	expect(f.document.getSnapshot().record.composer?.recoveryIds).toBeUndefined();
	expect((await f.runtime.read(f.targetKey)).recovery.map((row) => row.clientMutationId)).toEqual([
		source.clientMutationId,
	]);
	expect(nativeDrafts().read(oldDestination).draft).toBe("edited recovered");
});
