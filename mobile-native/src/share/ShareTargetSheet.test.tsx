import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import type { AnyNotification, SearchResponse } from "@evener/appwire-client";
import type { ReactTestRenderer } from "react-test-renderer";
import { act } from "react-test-renderer";
import { beforeEach, expect, it, vi } from "vitest";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";
import { DraftLibrary } from "../draftLibrary";
import { DraftRepository } from "../draftRepository";
import { DraftDocument } from "../draftDocument";
import { openSqliteSyncDouble, type SqliteDoubleDatabase } from "../sqliteSync.testkit";
import { answerFleetRead, type FleetShape, fleetSession } from "../session/fleetTestUtils";
import { alertRequests, render, settleMicrotasks, unmountMountedTrees, until } from "../renderNative.testkit";
import { ShareTargetSheet } from "./ShareTargetSheet";
import type { SharedPayload } from "./sharePayload";

const harness = vi.hoisted(() => ({
	connection: {} as Record<string, unknown>,
	kv: new Map<string, string>(),
}));

vi.mock("../ConnectionProvider", () => ({ useConnection: () => harness.connection }));
vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
	Linking: { getInitialURL: async () => null, addEventListener: () => ({ remove: () => {} }) },
}));
vi.mock("expo-file-system", () => ({ Paths: { appleSharedContainers: {} } }));
vi.mock("expo-sqlite/kv-store", () => ({
	Storage: {
		getItemSync: (key: string) => harness.kv.get(key) ?? null,
		setItemSync: (key: string, value: string) => harness.kv.set(key, value),
		removeItemSync: (key: string) => harness.kv.delete(key),
	},
}));
vi.mock("../board/hubSeen", async (importOriginal) => ({
	...(await importOriginal<typeof import("../board/hubSeen")>()),
	useBoardSeen: () => ({ isSeen: () => true, markSeen: () => {} }),
}));

const at = (minute: number) => new Date(Date.UTC(2026, 8, 26, 12, minute)).toISOString();
const failing = fleetSession("local:fail", { title: "Fix retry loop", state: "errored", updated_at: at(2) });
const working = fleetSession("local:work", { title: "Build the picker", state: "active", updated_at: at(1) });
const resting = fleetSession("local:rest", { title: "Old notes", state: "idle", updated_at: at(5) });
const fleet: FleetShape = { live: [resting, working, failing], needsYou: [failing], sources: [] };
const past = {
	id: "archived",
	title: "Quarterly report",
	project: "reports",
	state: "ended",
	age: "3d",
	ref: "local:report",
};

let database: SqliteDoubleDatabase;
let repository: DraftRepository;

beforeEach(() => {
	database?.close();
	harness.kv.clear();
	const opened = openSqliteSyncDouble();
	database = opened.database;
	repository = new DraftRepository(opened.port);
	unmountMountedTrees();
});

function hub() {
	const searches: string[] = [];
	const searcher = vi.fn();
	const listeners = new Set<(event: AnyNotification) => void>();
	const client: ConversationClientLike = Object.assign(new FakeClient("ready"), {
		request: (method: string, params: unknown) => {
			if (method === "evener/search") {
				searcher(method, params);
				const query = (params as { query: string }).query.toLowerCase();
				searches.push(query);
				const results: SearchResponse = {
					live: fleet.live
						.filter((row) => row.title.toLowerCase().includes(query))
						.map((row) => ({
							id: row.session_id,
							title: row.title,
							project: row.project,
							state: row.state,
							age: "2m",
							ref: row.ref,
						})),
					past: past.title.toLowerCase().includes(query) ? [past] : [],
				};
				return Promise.resolve(results as never);
			}
			const answer = answerFleetRead(fleet, method, params);
			if (answer === undefined) return Promise.resolve({} as never);
			return Promise.resolve(answer as never);
		},
		onNotification: (listener) => {
			listeners.add(listener);
			return () => listeners.delete(listener);
		},
	} as Omit<ConversationClientLike, "state" | "onReady" | "onStateChange">);
	return { client, searches, searcher };
}

function connect(client: unknown, state = "ready") {
	harness.connection = {
		client,
		state,
		activeProfile: { id: "hub-1", name: "Work hub" },
		fatal: false,
	};
}

const share: SharedPayload = { id: "share-1", text: "What I shared", createdAt: at(0) };

function sheet(
	navigation: { replace: ReturnType<typeof vi.fn>; goBack: ReturnType<typeof vi.fn> },
	onPlaced = vi.fn(),
) {
	return (
		<ShareTargetSheet
			navigation={navigation as never}
			route={{ key: "ShareTarget", name: "ShareTarget", params: { item: share } } as never}
			drafts={new DraftLibrary(() => repository)}
			onPlaced={onPlaced}
		/>
	);
}

/** Every accessibility label the sheet renders. */
const labels = (tree: ReactTestRenderer) =>
	tree.root
		.findAll((node) => typeof node.props.accessibilityLabel === "string")
		.map((node) => node.props.accessibilityLabel as string);

/** The row for a session, by the first part of its spoken label: the Board's
 * own row and result row lead with the title and add state, age and project. */
const rowTitled = (tree: ReactTestRenderer, title: string) =>
	tree.root.find(
		(node) =>
			node.props.accessibilityRole === "button" &&
			typeof node.props.accessibilityLabel === "string" &&
			(node.props.accessibilityLabel as string).startsWith(`${title},`),
	);

it("lists live sessions in attention order", async () => {
	const { client } = hub();
	connect(client);
	const navigation = { replace: vi.fn(), goBack: vi.fn() };
	const tree = render(sheet(navigation));
	await settleMicrotasks();
	const rendered = labels(tree);
	const position = (title: string) => rendered.findIndex((label) => label.startsWith(`${title},`));
	expect(position("Fix retry loop")).toBeLessThan(position("Build the picker"));
	expect(position("Build the picker")).toBeLessThan(position("Old notes"));
});

it("opens the chosen session with the shared text in its draft", async () => {
	const { client } = hub();
	connect(client);
	const navigation = { replace: vi.fn(), goBack: vi.fn() };
	const onPlaced = vi.fn();
	const tree = render(sheet(navigation, onPlaced));
	await settleMicrotasks();
	const row = rowTitled(tree, "Build the picker");
	act(() => row.props.onPress());
	await settleMicrotasks();
	expect(navigation.replace).toHaveBeenCalledWith("Conversation", {
		hubId: "hub-1",
		ref: "local:work",
		title: "Build the picker",
	});
	const opened = new DraftDocument(() => repository, { hubId: "hub-1", sessionRef: "local:work" });
	expect(opened.getSnapshot().record.draft).toBe("What I shared");
	expect(onPlaced).toHaveBeenCalledWith(share);
});

it("appends to a session's existing draft instead of replacing it", async () => {
	const { client } = hub();
	connect(client);
	const existing = new DraftDocument(() => repository, { hubId: "hub-1", sessionRef: "local:work" });
	existing.edit("What I was writing");
	const navigation = { replace: vi.fn(), goBack: vi.fn() };
	const tree = render(sheet(navigation));
	await settleMicrotasks();
	const row = rowTitled(tree, "Build the picker");
	act(() => row.props.onPress());
	await settleMicrotasks();
	expect(
		new DraftDocument(() => repository, { hubId: "hub-1", sessionRef: "local:work" }).getSnapshot().record.draft,
	).toBe("What I was writing\n\nWhat I shared");
});

it("starts a new session with the share when New session is chosen", async () => {
	const { client } = hub();
	connect(client);
	const navigation = { replace: vi.fn(), goBack: vi.fn() };
	const tree = render(sheet(navigation));
	await settleMicrotasks();
	act(() => tree.root.find((node) => node.props.testID === "share-new-session").props.onPress());
	expect(navigation.replace).toHaveBeenCalledWith("NewSession", {
		hubId: "hub-1",
		hubName: "Work hub",
		share,
	});
});

it("searches with the Board's backend and offers a past result", async () => {
	const { client, searches } = hub();
	connect(client);
	const navigation = { replace: vi.fn(), goBack: vi.fn() };
	const tree = render(sheet(navigation));
	await settleMicrotasks();
	const field = tree.root.find((node) => node.props.accessibilityLabel === "Search sessions");
	act(() => field.props.onChangeText("report"));
	await until(() => expect(searches.at(-1)).toBe("report"));
	expect(searches.at(-1)).toBe("report");
	const row = rowTitled(tree, "Quarterly report");
	act(() => row.props.onPress());
	await settleMicrotasks();
	expect(
		new DraftDocument(() => repository, { hubId: "hub-1", sessionRef: "local:report" }).getSnapshot().record.draft,
	).toBe("What I shared");
});

it("offers no sessions and searches nothing while the hub is not ready", async () => {
	const { searcher } = hub();
	// No hub configured at all, which is the case New session cannot answer.
	harness.connection = { client: null, state: "reconnecting", activeProfile: null, fatal: false };
	const navigation = { replace: vi.fn(), goBack: vi.fn() };
	const tree = render(sheet(navigation));
	await settleMicrotasks();
	// No live rows: none of the fleet's titles is a row.
	const row = (title: string) =>
		tree.root.findAll(
			(node) =>
				node.props.accessibilityRole === "button" &&
				typeof node.props.accessibilityLabel === "string" &&
				(node.props.accessibilityLabel as string).startsWith(`${title},`),
		);
	expect(row("Build the picker")).toEqual([]);
	// Typing starts no search: there is no client to ask.
	const field = tree.root.find((node) => node.props.accessibilityLabel === "Search sessions");
	act(() => field.props.onChangeText("report"));
	await settleMicrotasks();
	expect(searcher).not.toHaveBeenCalled();
	// New session needs a hub to start on; with none the row is disabled, so
	// the share is not handed to a sheet that would immediately close.
	const newSession = tree.root.find((node) => node.props.testID === "share-new-session");
	expect(newSession.props.disabled).toBe(true);
});

it("keeps the share when the draft write does not land", async () => {
	const { client } = hub();
	connect(client);
	const broken = new DraftRepository({
		execSync: () => {},
		runSync: () => ({ changes: 0 }),
		getFirstSync: () => {
			throw new Error("draft storage unavailable");
		},
		getAllSync: () => [],
	});
	const navigation = { replace: vi.fn(), goBack: vi.fn() };
	const onPlaced = vi.fn();
	const tree = render(
		<ShareTargetSheet
			navigation={navigation as never}
			route={{ key: "ShareTarget", name: "ShareTarget", params: { item: share } } as never}
			drafts={new DraftLibrary(() => broken)}
			onPlaced={onPlaced}
		/>,
	);
	await settleMicrotasks();
	const row = rowTitled(tree, "Build the picker");
	act(() => row.props.onPress());
	await settleMicrotasks();
	expect(onPlaced).not.toHaveBeenCalled();
	expect(navigation.replace).not.toHaveBeenCalled();
	expect(tree.root.findAll((node) => node.props.testID === "share-error").length).toBeGreaterThan(0);
});

it("releases the intake when the sheet closes without choosing", async () => {
	const { client } = hub();
	connect(client);
	const navigation = { replace: vi.fn(), goBack: vi.fn() };
	const onClosed = vi.fn();
	const tree = render(
		<ShareTargetSheet
			navigation={navigation as never}
			route={{ key: "ShareTarget", name: "ShareTarget", params: { item: share } } as never}
			drafts={new DraftLibrary(() => repository)}
			onPlaced={vi.fn()}
			onClosed={onClosed}
		/>,
	);
	await settleMicrotasks();
	act(() => tree.unmount());
	expect(onClosed).toHaveBeenCalledTimes(1);
});

it("discards the share only when the confirmation says so", async () => {
	const { client } = hub();
	connect(client);
	const navigation = { replace: vi.fn(), goBack: vi.fn(), setOptions: vi.fn() };
	const onDiscarded = vi.fn();
	render(
		<ShareTargetSheet
			navigation={navigation as never}
			route={{ key: "ShareTarget", name: "ShareTarget", params: { item: share } } as never}
			drafts={new DraftLibrary(() => repository)}
			onPlaced={vi.fn()}
			onDiscarded={onDiscarded}
		/>,
	);
	await settleMicrotasks();
	const cancel = navigation.setOptions.mock.calls.at(-1)?.[0]?.headerLeft as () => { props: { onPress(): void } };
	act(() => cancel().props.onPress());
	const request = alertRequests.at(-1);
	expect(request?.buttons?.map((button) => button.text)).toEqual(["Keep share", "Discard share"]);
	act(() => request?.buttons?.[1]?.onPress?.());
	expect(onDiscarded).toHaveBeenCalledWith(share);
	expect(navigation.goBack).toHaveBeenCalled();
});
