import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
// The Board screen mounted with only its native edges mocked: the navigation
// reads go through the real BoardController to a fake hub that answers by
// params, and the device memory is the real SeenMarkers over an in-memory
// kv-store.
import type {
	AnyNotification,
	AppwireClientLike,
	ConnectionState,
	HubNotice,
	NavigationInvalidationTarget,
	NavigationProjectSummary,
	NavigationReadParams,
	NavigationSessionSummary,
	SearchParams,
	SessionActivity,
	SessionSeenMark,
	SessionSeenSetParams,
	Thread,
} from "@evener/appwire-client";
import { STUCK_AFTER_MS, WireError } from "@evener/appwire-client";
import { manifest, wireSnapshot } from "@evener/appwire-client/testing/navigation";
import type { ReactTestInstance, ReactTestRenderer } from "react-test-renderer";
import { act, create } from "react-test-renderer";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";
import {
	alertRequests,
	playedHaptics,
	render,
	renderedText,
	screenConnection,
	swipeableCalls,
	swipeRowFully,
	systemGlass,
} from "../renderNative.testkit";
import { sheetKey } from "../sheet/sheetHosts";
import { ACTIVITY_POLL_MS, STALE_AFTER_MS } from "./activityPoll";
import { ROW_MOVE } from "./boardMotion";
import { BoardRow } from "./BoardRow";
import { BoardScreen } from "./BoardScreen";
import { SearchResults } from "./SearchResults";
import { requestBoardJump } from "./boardJump";
import { PulseMeter } from "./PulseMeter";
import { hubSeenMarks } from "./hubSeen";
import { forgetBoardForHub, seenMarkers } from "./nativeBoardMemory";
import { SESSION_ID } from "./organizationTestUtils";
import { ROW_ACTION_LABELS } from "./rowActions";
import { type RowMenuHost, rowMenuHosts } from "./RowMenu";
import { WASH_MS } from "./settledList";

const harness = vi.hoisted(() => ({
	connection: {} as Record<string, unknown>,
	kv: new Map<string, string>(),
	drafts: new Map<string, Set<string>>(),
	focused: true,
	/** The navigator's stack, for whether only sheets cover the Board. */
	stack: { index: 0, routes: [{ key: "Sessions", name: "Sessions" }] },
	focusListeners: new Set<(focused: boolean) => void>(),
	actionSheet: vi.fn(),
	prompt: vi.fn(),
	sqlite: new Map<string, unknown>(),
	/** What AccessibilityInfo says of Reduce Motion. */
	reduceMotion: false,
	/** Dynamic Type's scale, as useWindowDimensions reports it. */
	fontScale: 1,
	/** AppState's change listeners. */
	appState: new Set<(state: string) => void>(),
	announce: vi.fn(),
}));

vi.mock("react-native", async () => {
	const native = (await import("../renderNative.testkit")).nativeModuleMock();
	return {
		...native,
		Alert: { ...native.Alert, prompt: (...args: unknown[]) => harness.prompt(...args) },
		ActionSheetIOS: { showActionSheetWithOptions: (...args: unknown[]) => harness.actionSheet(...args) },
		AccessibilityInfo: {
			...native.AccessibilityInfo,
			announceForAccessibility: (...args: unknown[]) => harness.announce(...args),
			isReduceMotionEnabled: () => Promise.resolve(harness.reduceMotion),
		},
		AppState: {
			addEventListener: (_type: string, listener: (state: string) => void) => {
				harness.appState.add(listener);
				return { remove: () => harness.appState.delete(listener) };
			},
		},
		useWindowDimensions: () => ({ fontScale: harness.fontScale, scale: 2, width: 390, height: 844 }),
	};
});
vi.mock("react-native-reanimated", async () => (await import("../renderNative.testkit")).reanimatedModuleMock());
vi.mock("@react-navigation/elements", () => ({ useHeaderHeight: () => 64 }));
vi.mock("react-native-gesture-handler/ReanimatedSwipeable", async () =>
	(await import("../renderNative.testkit")).gestureHandlerModuleMock(),
);
vi.mock("react-native-gesture-handler", async () =>
	(await import("../renderNative.testkit")).gestureDetectorModuleMock(),
);
// Stop goes through the process's real mutation runtime, over an in-memory
// SQLite double (one per database name, as the device keeps one file). The
// runtime and its double live for the whole file, so every test shares one
// outbox: records are keyed by hub and session, and hubId() gives each test
// its own hub. A test must never assert on the outbox as a whole.
vi.mock("expo-sqlite", async () => {
	const { openSqliteSyncDouble } = await import("../sqliteSync.testkit");
	return {
		openDatabaseSync: (name: string) => {
			if (!harness.sqlite.has(name)) harness.sqlite.set(name, openSqliteSyncDouble().port);
			return harness.sqlite.get(name);
		},
	};
});
// The organization journal names each change it records.
vi.mock("expo-crypto", () => ({
	randomUUID: () => `change-${Math.random()}`,
	getRandomValues: (array: Uint8Array) => array,
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));
vi.mock("react-native-safe-area-context", () => ({
	useSafeAreaInsets: () => ({ top: 47, bottom: 34, left: 0, right: 0 }),
}));
// Focus follows harness.focused, which setFocused changes on demand; like
// the real hook, the effect runs on focus and its cleanup on blur.
vi.mock("@react-navigation/native", async () => {
	const { useEffect, useState } = await import("react");
	const useIsFocused = () => {
		const [focused, setFocused] = useState(harness.focused);
		useEffect(() => {
			harness.focusListeners.add(setFocused);
			return () => {
				harness.focusListeners.delete(setFocused);
			};
		}, []);
		return focused;
	};
	return {
		useFocusEffect: (effect: () => undefined | (() => void)) => {
			const focused = useIsFocused();
			useEffect(() => (focused ? effect() : undefined), [focused, effect]);
		},
		useIsFocused,
		useNavigationState: <T,>(select: (state: typeof harness.stack) => T) => select(harness.stack),
	};
});
vi.mock("expo-sqlite/kv-store", () => ({
	Storage: {
		getItemSync: (key: string) => harness.kv.get(key) ?? null,
		setItemSync: (key: string, value: string) => harness.kv.set(key, value),
		removeItemSync: (key: string) => harness.kv.delete(key),
	},
}));
vi.mock("../nativeDrafts", () => ({
	drafts: { refsWithDrafts: (hubId: string) => harness.drafts.get(hubId) ?? new Set<string>() },
}));
vi.mock("../ConnectionProvider", () => ({
	useConnection: () => harness.connection,
}));

const INCOMPATIBLE_TEXT =
	"This app and the hub need compatible versions. Update the app from TestFlight, or update Evener on the hub.";
const NOW = Date.UTC(2026, 8, 26, 12, 0);
const MINUTE = 60_000;
const minutesAgo = (minutes: number) => new Date(NOW - minutes * 60_000).toISOString();

beforeEach(() => {
	vi.useFakeTimers();
	vi.setSystemTime(NOW);
	harness.focused = true;
	harness.stack = { index: 0, routes: [{ key: "Sessions", name: "Sessions" }] };
	harness.reduceMotion = false;
	harness.fontScale = 1;
});
function setFocused(focused: boolean) {
	harness.focused = focused;
	act(() => {
		for (const listener of harness.focusListeners) listener(focused);
	});
}
// Every Board a test mounts, so one that fails before its own unmount
// can't leave a Board behind that reads the next test's connection.
const mounted: ReactTestRenderer[] = [];
afterEach(() => {
	for (const tree of mounted.splice(0)) if (tree.toJSON() !== null) act(() => tree.unmount());
	vi.useRealTimers();
	systemGlass.reset();
});

// Each test uses its own hub, because nativeBoardMemory keeps one SeenMarkers
// per hub, and the mutation runtime one outbox, for the life of the module.
let hubCount = 0;
function hubId() {
	hubCount += 1;
	return `hub-${hubCount}`;
}
/** A device that finished first run an hour ago, so rows updated since then
 * are unseen. */
function adoptedAnHourAgo(hub: string) {
	harness.kv.set(`evener.native.seen.${hub}`, JSON.stringify({ adopted: true, epoch: minutesAgo(60), sessions: {} }));
}

const session = (ref: string, over: Partial<NavigationSessionSummary> = {}): NavigationSessionSummary => ({
	ref,
	host_id: "local",
	session_id: ref,
	title: ref,
	project: "evener",
	state: "idle",
	kind: "session",
	live: true,
	children: [],
	updated_at: minutesAgo(5),
	...over,
});
const failing = session("local:fail", { title: "Fix retry loop", state: "errored", updated_at: minutesAgo(2) });
const asking = session("local:ask", {
	title: "Pick a name",
	state: "awaiting",
	ask_pending: true,
	updated_at: minutesAgo(3),
});
const working = session("local:work", { title: "Build docs", state: "active", updated_at: minutesAgo(1) });
const finished = session("local:done", { title: "Ship it", updated_at: minutesAgo(4) });
const idleOne = session("local:idle-1", { title: "Old chore", dormant: true, updated_at: minutesAgo(120) });
const idleTwo = session("local:idle-2", { title: "Older chore", dormant: true, updated_at: minutesAgo(240) });

interface Fleet {
	live: NavigationSessionSummary[][];
	needsYou: NavigationSessionSummary[];
	pins: Array<{ id: string; name: string; count: number }>;
	/** Each category's sessions, by id. */
	pinned: Record<string, NavigationSessionSummary[]>;
	manifest: ReturnType<typeof manifest>;
	/** Sessions only search finds, as past results: the Board doesn't list them. */
	searchOnly?: NavigationSessionSummary[];
	/** What evener/activity/read answers (S5): absent, the hub predates S5
	 * and answers method-not-found; null, the read fails as a timeout would. */
	activity?: SessionActivity[] | null;
	/** Whether evener/search fails. */
	searchFails?: boolean;
	/** What evener/notices/list answers (S11); absent, the hub predates S11
	 * and answers method-not-found. */
	notices?: HubNotice[];
	/** Whether evener/notices/list fails, as a timeout would. */
	noticesFail?: boolean;
	/** Each project catalog's projects; a catalog left out is empty. */
	catalogs?: Partial<Record<ProjectCatalogName, NavigationProjectSummary[]>>;
	/** Each project tier's sessions, keyed `${projectKey}:${tier}`, paged by the read's limit. */
	projectPages?: Record<string, NavigationSessionSummary[]>;
}
type ProjectCatalogName = "projects" | "archived_projects" | "test_runs";
const keptNote = session("local:kept", { title: "Kept note", live: false, updated_at: minutesAgo(600) });
const oldPlan = session("local:plan", { title: "Old plan", live: false, updated_at: minutesAgo(900) });
const releaseNotes = session("local:notes", { title: "Release notes", live: false, updated_at: minutesAgo(1200) });
const fleet: Fleet = {
	// The ask is in the hub's needs_you section only: bands union it.
	live: [[failing, working, finished, idleOne, idleTwo]],
	needsYou: [failing, asking],
	pins: [
		{ id: "pins-1", name: "Mine", count: 3 },
		{ id: "pins-2", name: "Empty", count: 0 },
	],
	pinned: { "pins-1": [keptNote, oldPlan, releaseNotes], "pins-2": [] },
	manifest: manifest({
		sources: [{ id: "local", label: "Laptop", kind: "local", online: true }],
		sections: { live: { count: 5 }, needs_you: { count: 2 }, pin_sections: { count: 2 } },
		catalogs: { projects: { count: 4 }, archived_projects: { count: 0 }, test_runs: { count: 0 } },
	}),
};

/** A hub that answers navigation reads by params, and search and the
 * notices from the fleet; `hold` keeps a navigation read
 * unanswered until the test releases it, and `fail` rejects it. It accepts
 * every category rename and delete and every project or session favorite and
 * archive (`mutations` records them, and a favorite or archive shows in the
 * catalog or the session's location after), unless `refuse` says to reject
 * one, and `holdChanges` keeps them unanswered until `release`. It accepts
 * every seen mark, and `seen` records each call's marks. It answers a
 * session's thread/read from its row's state and applies every
 * turn/interrupt, recording both in `threadCalls`. It records every
 * thread/shutdown and session rename in `mutations` too, and accepts each
 * unless `refuse` says to reject it with the hub's "session not found". */
function hub(
	shape: Fleet,
	hold: (params: NavigationReadParams) => boolean = () => false,
	fail: (params: NavigationReadParams) => boolean = () => false,
	{ holdChanges = false, refuse = false } = {},
) {
	const requests: NavigationReadParams[] = [];
	const activityReads: unknown[] = [];
	const noticeReads: string[] = [];
	const searches: string[] = [];
	const mutations: Array<{ method: string; params: unknown }> = [];
	const threadCalls: Array<{ method: string; params: unknown }> = [];
	// The sessions the hub has archived, by ref.
	const archivedRefs = new Set<string>();
	// The category each session the hub has pinned sits in, by ref.
	const pinnedRefs = new Map<string, string>();
	const sessionRows = () => [...shape.live.flat(), ...shape.needsYou, ...Object.values(shape.pinned).flat()];
	const seen: SessionSeenMark[][] = [];
	const listeners = new Set<(event: AnyNotification) => void>();
	const held: Array<() => void> = [];
	const answer = (params: NavigationReadParams) => {
		const offset = params.offset ?? 0;
		if (params.resource === "manifest") return shape.manifest;
		if (params.resource === "pin_catalog") return { pin_sections: shape.pins, remaining: 0 };
		if (params.resource === "location") {
			const row = sessionRows().find((candidate) => candidate.ref === params.ref);
			if (!row) return {};
			const pinSection = pinnedRefs.get(row.ref);
			return pinSection ? { session: row, pin_section_id: pinSection } : { session: row };
		}
		if (params.resource === "pin_section") {
			const rows = shape.pinned[params.sectionId ?? ""];
			if (!rows) throw new Error(`no category ${params.sectionId}`);
			return { sessions: rows, remaining: 0 };
		}
		if (params.resource === "catalog") {
			const projects = shape.catalogs?.[params.catalog as ProjectCatalogName] ?? [];
			const page = projects.slice(offset, offset + (params.limit ?? 50));
			return { projects: page, remaining: projects.length - offset - page.length };
		}
		if (params.resource === "project_page") {
			const rows = shape.projectPages?.[`${params.projectKey}:${params.tier}`] ?? [];
			const page = rows.slice(offset, offset + (params.limit ?? 50));
			return { sessions: page, remaining: rows.length - offset - page.length };
		}
		if (params.section === "needs_you") return { sessions: shape.needsYou, remaining: 0 };
		// Live pages are consecutive: each page's offset is the rows before it.
		let before = 0;
		for (const [index, page] of shape.live.entries()) {
			if (before === offset) {
				const after = shape.live.slice(index + 1).reduce((sum, rest) => sum + rest.length, 0);
				return { sessions: page, remaining: after };
			}
			before += page.length;
		}
		throw new Error(`no Live page at offset ${offset}`);
	};
	const client: ConversationClientLike & Pick<AppwireClientLike, "state" | "onStateChange"> = Object.assign(
		new FakeClient("ready"),
		{
			request: (method, params) =>
				new Promise((resolve, reject) => {
					if (method === "thread/read" || method === "turn/interrupt") {
						threadCalls.push({ method, params });
						const { ref } = params as { ref: string };
						if (method === "thread/read") {
							const row = sessionRows().find((candidate) => candidate.ref === ref);
							resolve({
								thread: threadOf(ref, row?.state === "active" ? "active" : "idle", row?.turn_ended_at),
							} as never);
						} else
							resolve({
								receipt: {
									clientMutationId: (params as { clientMutationId: string }).clientMutationId,
									disposition: "applied",
									threadId: `thread:${ref}`,
									projectionState: "pending",
								},
							} as never);
						return;
					}
					if (method === "thread/shutdown" || method === "evener/thread/name/set") {
						mutations.push({ method, params });
						if (refuse) reject(new Error("session not found"));
						else resolve({} as never);
						return;
					}
					if (
						method === "evener/pin-section/rename" ||
						method === "evener/pin-section/delete" ||
						method === "evener/favorite/set" ||
						method === "evener/archive/set" ||
						method === "evener/session-pin/assign"
					) {
						mutations.push({ method, params });
						const respond = () => {
							if (refuse) {
								reject(new Error("request timed out"));
								return;
							}
							if (method === "evener/archive/set") {
								const change = params as { kind: string; id: string; archived: boolean };
								if (change.kind === "session") {
									// This hub's session is named by its bare id, another host's by its ref.
									const ref = change.id.includes(":") ? change.id : `local:${change.id}`;
									if (change.archived) archivedRefs.add(ref);
									else archivedRefs.delete(ref);
								}
								for (const catalog of Object.values(shape.catalogs ?? {}))
									for (const project of catalog ?? [])
										if (project.key === change.id) project.is_archived = change.archived;
							}
							if (method === "evener/session-pin/assign") {
								// A new category's name makes it, or reuses one that has it.
								const pin = params as { sessionRef: string; sectionId?: string; sectionName?: string };
								let sectionId = pin.sectionId;
								if (!sectionId) {
									const name = pin.sectionName ?? "";
									const existing = shape.pins.find((section) => section.name === name);
									sectionId = existing?.id ?? `made-${name.toLowerCase()}`;
									if (!existing) shape.pins = [...shape.pins, { id: sectionId, name, count: 0 }];
								}
								pinnedRefs.set(pin.sessionRef, sectionId);
							}
							if (method === "evener/favorite/set") {
								const change = params as { id: string; favorited: boolean };
								for (const catalog of Object.values(shape.catalogs ?? {}))
									for (const project of catalog ?? [])
										if (project.key === change.id) project.favorite = change.favorited;
							}
							resolve({ ok: true, navigation: { generation_id: "generation-test", targets: [] } } as never);
						};
						if (holdChanges) held.push(respond);
						else respond();
						return;
					}
					if (method === "evener/session/seen/set") {
						seen.push((params as SessionSeenSetParams).sessions);
						resolve({
							ok: true,
							changed: true,
							navigation: { generation_id: "generation-test", targets: [] },
						} as never);
						return;
					}
					if (method === "evener/search") {
						// Search matches titles: the Board's sessions are live, and
						// sessions only search finds are past.
						const query = ((params as SearchParams).query ?? "").toLowerCase();
						searches.push(query);
						if (shape.searchFails) {
							reject(new Error("request timed out"));
							return;
						}
						const board = [...shape.live.flat(), ...shape.needsYou].filter(
							(row, index, all) => all.findIndex((other) => other.ref === row.ref) === index,
						);
						const found = (rows: NavigationSessionSummary[], state?: string) =>
							rows
								.filter((row) => row.title.toLowerCase().includes(query))
								.map((row) => ({
									id: row.session_id,
									title: row.title,
									project: row.project,
									state: state ?? row.state,
									age: "5m",
									ref: row.ref,
									...(row.ask_pending ? { askPending: true } : {}),
								}));
						resolve({ live: found(board), past: found(shape.searchOnly ?? [], "ended") } as never);
						return;
					}
					if (method === "evener/notices/list") {
						noticeReads.push(method);
						if (shape.noticesFail) reject(new Error("request timed out"));
						else if (shape.notices) resolve({ notices: shape.notices } as never);
						else reject(new WireError("no such method", -32601));
						return;
					}
					if (method === "evener/activity/read") {
						activityReads.push(params);
						if (shape.activity) resolve({ sessions: shape.activity } as never);
						else if (shape.activity === null) reject(new Error("request timed out"));
						else reject(new WireError("no such method", -32601));
						return;
					}
					if (method !== "evener/navigation/read") throw new Error(`unexpected ${method}`);
					const read = params as NavigationReadParams;
					requests.push(read);
					if (fail(read)) {
						reject(new Error("request timed out"));
						return;
					}
					const respond = () => {
						const response = wireSnapshot(
							{ ...read, representationVersion: 3, offset: read.offset ?? 0, limit: read.limit ?? 50 },
							answer(read),
							`etag-${read.resource}-${read.offset ?? 0}`,
							1,
							"generation-test",
						);
						if (read.resource === "location")
							(response.data as { metadata: Record<string, unknown> }).metadata.tier = archivedRefs.has(read.ref ?? "")
								? "archived"
								: "current";
						resolve(response);
					};
					if (hold(read)) held.push(respond);
					else respond();
				}),
			onNotification: (listener) => {
				listeners.add(listener);
				return () => listeners.delete(listener);
			},
		} as Omit<ConversationClientLike, "state" | "onReady" | "onStateChange">,
	);
	return {
		client,
		requests,
		activityReads,
		noticeReads,
		searches,
		noticesChanged: (notices: HubNotice[]) => {
			for (const listener of listeners)
				listener({ method: "evener/notices/changed", params: { notices } } as AnyNotification);
		},
		mutations,
		threadCalls,
		seen,
		invalidate: (sequence: number, targets: NavigationInvalidationTarget[]) => {
			for (const listener of listeners)
				listener({
					method: "evener/navigation/invalidated",
					params: { generationId: "generation-test", sequence, targets },
				});
		},
		release: () => {
			for (const respond of held.splice(0)) respond();
		},
	};
}

function navigation() {
	return { navigate: vi.fn(), setOptions: vi.fn(), dispatch: vi.fn(), getState: () => undefined };
}
type Navigation = ReturnType<typeof navigation>;

function connect(hub: string, client: unknown, state: ConnectionState, over: Record<string, unknown> = {}) {
	harness.connection = {
		...screenConnection(client, state),
		activeProfile: { id: hub, name: "Work hub" },
		...over,
	};
}
function screen(nav: Navigation) {
	return <BoardScreen navigation={nav as never} route={{ key: "Sessions", name: "Sessions" } as never} />;
}
async function settle() {
	await act(async () => {
		for (let step = 0; step < 30; step++) await Promise.resolve();
	});
}
async function mount(nav: Navigation) {
	const tree = render(screen(nav));
	mounted.push(tree);
	await settle();
	return tree;
}
function rerender(tree: ReactTestRenderer, nav: Navigation) {
	act(() => tree.update(screen(nav)));
}

/** Every string a Text renders on its own. */
function texts(tree: ReactTestRenderer): string[] {
	return textsIn(tree.root);
}
function textsIn(root: ReactTestInstance): string[] {
	return root
		.findAll((node) => node.type === ("Text" as never))
		.flatMap((node) => [node.props.children].flat().filter((child): child is string => typeof child === "string"));
}
function joinedText(node: ReactTestInstance): string {
	return node.children.map((child) => (typeof child === "string" ? child : joinedText(child))).join("");
}
/** The band headers and the Idle fold, in screen order. */
function bandHeaders(tree: ReactTestRenderer): string[] {
	return tree.root.findAll((node) => node.props.testID === "band-header").map(joinedText);
}
/** Whether a node is the Board row with this title. */
const isRowTitled = (title: string) => (node: ReactTestInstance) =>
	node.type === ("Pressable" as never) &&
	typeof node.props.accessibilityLabel === "string" &&
	node.props.accessibilityLabel.startsWith(`${title}, `);
/** A Board row, found by its title. */
function rowTitled(tree: ReactTestRenderer, title: string) {
	return tree.root.find(isRowTitled(title));
}
function hasRow(tree: ReactTestRenderer, title: string) {
	return tree.root.findAll(isRowTitled(title)).length > 0;
}
/** The Draft tags a row draws. */
const draftTags = (row: ReactTestInstance) =>
	row.findAll((node) => node.type === ("Text" as never) && node.props.children === "Draft");
const skeletonRows = (tree: ReactTestRenderer) => tree.root.findAll((node) => node.props.testID === "skeleton-row");
/** Presses the one control with this label, leaving out the chips, which
 * share their labels with the section rows they scroll to. */
function pressLabel(tree: ReactTestRenderer, label: string) {
	const target = tree.root.find(
		(node) =>
			node.type === ("Pressable" as never) && node.props.accessibilityLabel === label && node.props.testID !== "chip",
	);
	act(() => target.props.onPress());
}
function pressChip(tree: ReactTestRenderer, label: string) {
	const chip = tree.root.find((node) => node.props.testID === "chip" && node.props.accessibilityLabel === label);
	playedHaptics.length = 0;
	act(() => chip.props.onPress());
	// Spec 16.6: a selection tick on a chip.
	expect(playedHaptics).toEqual(["selection"]);
}
const chipLabels = (tree: ReactTestRenderer) =>
	tree.root
		.findAll((node) => node.type === ("Pressable" as never) && node.props.testID === "chip")
		.map((node) => node.props.accessibilityLabel);
const headerOptions = (nav: Navigation) => nav.setOptions.mock.calls.at(-1)?.[0];

it("renders the fleet's bands in order with their counts, and Idle starts folded", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	connect(id, hub(fleet).client, "ready");
	const tree = await mount(navigation());
	expect(bandHeaders(tree)).toEqual(["NEEDS YOU · 2", "FINISHED · 1", "WORKING · 1", "Idle · 2"]);
	expect(renderedText(tree)).toContain("2 need you");
	expect(texts(tree)).toEqual(expect.arrayContaining(["1 finished", "1 working", "2 idle"]));
	for (const title of ["Fix retry loop", "Pick a name", "Ship it", "Build docs"])
		expect(hasRow(tree, title)).toBe(true);
	expect(hasRow(tree, "Old chore")).toBe(false);
	pressLabel(tree, "Idle, 2 sessions");
	expect(hasRow(tree, "Old chore")).toBe(true);
	expect(hasRow(tree, "Older chore")).toBe(true);
	expect(JSON.parse(harness.kv.get(`evener.native.board-sections.${id}`) ?? "null")).toEqual({ idle: false });
	// Folded again, the summary's idle count unfolds it.
	pressLabel(tree, "Idle, 2 sessions");
	expect(hasRow(tree, "Old chore")).toBe(false);
	const idleCount = tree.root.find(
		(node) =>
			node.type === ("Pressable" as never) &&
			node.findAll((child) => child.type === ("Text" as never) && child.props.children === "2 idle").length > 0,
	);
	playedHaptics.length = 0;
	act(() => idleCount.props.onPress());
	// Spec 16.6: a selection tick on a summary count.
	expect(playedHaptics).toEqual(["selection"]);
	// The unfold applies when the scroll to Idle ends, as any change does.
	expect(hasRow(tree, "Old chore")).toBe(false);
	listEvent(tree, "onMomentumScrollEnd");
	expect(hasRow(tree, "Old chore")).toBe(true);
	act(() => tree.unmount());
});

/** Mounts the Board with instances for its scroller and search field, the
 * way the chip jumps and the Search item reach them. */
async function mountWithInstances(nav: Navigation) {
	const scrollTo = vi.fn();
	const focus = vi.fn();
	const blur = vi.fn();
	let tree!: ReactTestRenderer;
	act(() => {
		tree = create(screen(nav), {
			createNodeMock: (element) => (element.type === ("TextInput" as never) ? { focus, blur } : { scrollTo }),
		});
	});
	mounted.push(tree);
	await settle();
	return { tree, scrollTo, focus, blur };
}
/** The Board's scroller, not the chips' horizontal one. */
const boardScroller = (tree: ReactTestRenderer) =>
	tree.root.find((node) => node.type === ("ScrollView" as never) && !node.props.horizontal);

/** Lays the Board's toolbar out `height` tall; the toolbar is the bar
 * itself, laid over the Board's end. */
function layOutToolbar(tree: ReactTestRenderer, height: number) {
	const flat = (node: ReactTestInstance) => Object.assign({}, ...[node.props.style].flat(Number.POSITIVE_INFINITY));
	const toolbar = tree.root.find((node) => node.props.testID === "board-toolbar" && String(node.type) === "View");
	expect(flat(toolbar)).toMatchObject({ position: "absolute", left: 0, right: 0, bottom: 0, borderTopWidth: 0.5 });
	act(() => toolbar.props.onLayout({ nativeEvent: { layout: { x: 0, y: 700, width: 390, height } } }));
	const toastSlot = tree.root.find((node) => node.props.testID === "board-toast" && String(node.type) === "View");
	return { scroller: boardScroller(tree), toastBottom: flat(toastSlot).bottom };
}

it("runs the Board under its toolbar, insetting its end by the toolbar and floating the toast above it", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	connect(id, hub(fleet).client, "ready");
	const tree = await mount(navigation());
	const { scroller, toastBottom } = layOutToolbar(tree, 84);
	expect(scroller.props.contentInset).toEqual({ bottom: 84 });
	expect(scroller.props.contentContainerStyle).toMatchObject({ paddingBottom: 24 });
	expect(scroller.props.scrollIndicatorInsets).toEqual({ bottom: 84 });
	expect(toastBottom).toBe(84 + 10);
	act(() => tree.unmount());
});

it("pads the Board's end by its toolbar on Android, which has no content inset", async () => {
	const { Platform } = (await import("react-native")) as unknown as { Platform: { OS: string } };
	Platform.OS = "android";
	try {
		const id = hubId();
		adoptedAnHourAgo(id);
		connect(id, hub(fleet).client, "ready");
		const tree = await mount(navigation());
		const { scroller, toastBottom } = layOutToolbar(tree, 84);
		expect(scroller.props.contentInset).toBeUndefined();
		expect(scroller.props.contentContainerStyle).toMatchObject({ paddingBottom: 24 + 84 });
		expect(scroller.props.scrollIndicatorInsets).toEqual({ bottom: 84 });
		expect(toastBottom).toBe(84 + 10);
		act(() => tree.unmount());
	} finally {
		Platform.OS = "ios";
	}
});

it("keeps the chips fixed above the Board's scroller, and jumps a chip's section to the top", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	connect(id, hub({ ...fleet, catalogs: { projects: [evenerProject()] } }).client, "ready");
	const { tree, scrollTo } = await mountWithInstances(navigation());
	const scroller = boardScroller(tree);
	// The chips sit outside the scroller, so they never scroll away.
	expect(scroller.findAll((node) => node.props.testID === "chips" && String(node.type) === "View")).toHaveLength(0);
	expect(tree.root.findAll((node) => node.props.testID === "chips" && String(node.type) === "View")).toHaveLength(1);
	expect(scroller.props.stickyHeaderIndices).toBeUndefined();
	const layout = (testID: string, y: number, height: number) =>
		tree.root
			.find((node) => node.props.testID === testID && node.props.onLayout)
			.props.onLayout({ nativeEvent: { layout: { x: 0, y, width: 390, height } } });
	act(() => {
		layout("live-block", 52, 400);
		layout("project-section:projects", 752, 48);
	});
	pressChip(tree, "Live, 5 sessions, 2 need you");
	expect(scrollTo).toHaveBeenLastCalledWith({ y: 52, animated: true });
	pressChip(tree, "Projects, 4 projects");
	expect(scrollTo).toHaveBeenLastCalledWith({ y: 752, animated: true });
	act(() => tree.unmount());
});

it("scrolls to Needs you when a coalesced banner asks, once the Board has laid out", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	connect(id, hub(fleet).client, "ready");
	// Asked before this Board existed: the banner popped to it.
	requestBoardJump("needsYou");
	const { tree, scrollTo } = await mountWithInstances(navigation());
	expect(scrollTo).not.toHaveBeenCalledWith(expect.objectContaining({ animated: true }));
	const band = tree.root
		.findAll(
			(node) =>
				typeof node.props.onLayout === "function" &&
				node.findAll((child) => child.props.children === "NEEDS YOU · 2").length > 0,
		)
		.at(-1);
	act(() => {
		tree.root
			.find((node) => node.props.testID === "live-block" && node.props.onLayout)
			.props.onLayout({ nativeEvent: { layout: { x: 0, y: 52, width: 390, height: 400 } } });
		band?.props.onLayout({ nativeEvent: { layout: { x: 0, y: 30, width: 390, height: 28 } } });
	});
	expect(scrollTo).toHaveBeenLastCalledWith({ y: 82, animated: true });
	// A Board already laid out scrolls at once.
	scrollTo.mockClear();
	act(() => requestBoardJump("needsYou"));
	expect(scrollTo).toHaveBeenLastCalledWith({ y: 82, animated: true });
	act(() => tree.unmount());
});

const EMPTY_CATEGORY = "Touch and hold a session and choose Pin to category.";
const pinHeaders = (tree: ReactTestRenderer) =>
	tree.root.findAll((node) => node.props.testID === "pin-header").map((node) => node.props.accessibilityLabel);
const menuLabels = (tree: ReactTestRenderer) =>
	tree.root
		.findAll(
			(node) =>
				node.type === ("Pressable" as never) &&
				typeof node.props.accessibilityLabel === "string" &&
				node.props.accessibilityLabel.endsWith(", category menu"),
		)
		.map((node) => node.props.accessibilityLabel);
const pinSection = (tree: ReactTestRenderer, name: string) =>
	tree.root.find(
		(node) =>
			node.props.testID === "pin-section" &&
			node.findAll(
				(child) => child.props.testID === "pin-header" && child.props.accessibilityLabel.startsWith(`${name}, `),
			).length > 0,
	);
const opacity = (tree: ReactTestRenderer, name: string) => pinSection(tree, name).props.style.opacity;
/** Opens a category's menu and chooses Delete; returns the confirmation. */
function confirmDelete(tree: ReactTestRenderer, name: string) {
	pressLabel(tree, `${name}, category menu`);
	act(() => harness.actionSheet.mock.calls.at(-1)?.[1](1));
	return alertRequests.at(-1);
}

it("shows chips for the sections that have sessions, and each pinned category inline with its sessions", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	connect(id, hub({ ...fleet, catalogs: { projects: [evenerProject()] } }).client, "ready");
	const nav = navigation();
	const tree = await mount(nav);
	expect(chipLabels(tree)).toEqual(["Live, 5 sessions, 2 need you", "Mine, 3 sessions", "Projects, 4 projects"]);
	expect(texts(tree)).not.toContain("Archived · 0");
	// Every category is a section in the hub's order, an empty one included
	// (spec 7.1: a category is a place, and an empty one says how to pin to it).
	expect(pinHeaders(tree)).toEqual(["Mine, 3 sessions", "Empty, 0 sessions"]);
	for (const title of ["Kept note", "Old plan", "Release notes"]) expect(hasRow(tree, title)).toBe(true);
	expect(textsIn(pinSection(tree, "Empty"))).toContain(EMPTY_CATEGORY);
	expect(textsIn(pinSection(tree, "Mine"))).not.toContain(EMPTY_CATEGORY);
	// The link rows to the category screens are gone.
	expect(texts(tree)).not.toContain("Mine · 3");
	act(() => rowTitled(tree, "Old plan").props.onPress());
	expect(nav.navigate).toHaveBeenLastCalledWith("Conversation", { hubId: id, ref: "local:plan", title: "Old plan" });
	expect(seenMarkers(id).isSeen(oldPlan)).toBe(true);
	// The header folds its category.
	pressLabel(tree, "Mine, 3 sessions");
	expect(hasRow(tree, "Kept note")).toBe(false);
	expect(nav.navigate.mock.calls.map(([route]) => route)).not.toContain("PinnedSection");
	// The Projects chip scrolls to the section on the Board; nothing opens another screen.
	const projectsChip = tree.root.find(
		(node) => node.props.testID === "chip" && node.props.accessibilityLabel === "Projects, 4 projects",
	);
	act(() => projectsChip.props.onPress());
	expect(nav.navigate.mock.calls.map(([route]) => route)).not.toContain("Projects");
	act(() => tree.unmount());
});

it("keeps a live pinned session in Live as well as in its category", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	connect(id, hub({ ...fleet, pinned: { ...fleet.pinned, "pins-1": [working, keptNote, oldPlan] } }).client, "ready");
	const tree = await mount(navigation());
	const copies = tree.root.findAll(isRowTitled("Build docs"));
	expect(copies).toHaveLength(2);
	expect(bandHeaders(tree)).toContain("WORKING · 1");
	act(() => tree.unmount());
});

it("unfolds a folded category when its chip is tapped", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	connect(id, hub(fleet).client, "ready");
	const tree = await mount(navigation());
	pressLabel(tree, "Mine, 3 sessions");
	expect(hasRow(tree, "Kept note")).toBe(false);
	const chip = tree.root.find(
		(node) => node.props.testID === "chip" && node.props.accessibilityLabel === "Mine, 3 sessions",
	);
	act(() => chip.props.onPress());
	// The unfold applies when the scroll to it ends, as any change does.
	expect(hasRow(tree, "Kept note")).toBe(false);
	listEvent(tree, "onMomentumScrollEnd");
	expect(hasRow(tree, "Kept note")).toBe(true);
	expect(JSON.parse(harness.kv.get(`evener.native.board-sections.${id}`) ?? "null")).toMatchObject({
		"pin:pins-1": false,
	});
	act(() => tree.unmount());
});

it("renames a category from its menu, sending the trimmed name", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	const fake = hub(fleet);
	connect(id, fake.client, "ready");
	const tree = await mount(navigation());
	expect(menuLabels(tree)).toEqual(["Mine, category menu", "Empty, category menu"]);
	const rename = (value: string) => {
		pressLabel(tree, "Mine, category menu");
		const [sheet, choose] = harness.actionSheet.mock.calls.at(-1) ?? [];
		expect(sheet).toEqual({
			title: "Mine",
			options: ["Rename", "Delete", "Cancel"],
			destructiveButtonIndex: 1,
			cancelButtonIndex: 2,
		});
		act(() => choose(0));
		const [title, message, buttons, type, current] = harness.prompt.mock.calls.at(-1) ?? [];
		expect([title, message, type, current]).toEqual(["Rename category", undefined, "plain-text", "Mine"]);
		expect(buttons.map((button: { text: string; style?: string }) => [button.text, button.style])).toEqual([
			["Cancel", "cancel"],
			["Rename", undefined],
		]);
		act(() => buttons[1].onPress(value));
	};
	// A blank or unchanged name sends nothing, and neither does a long one.
	rename("   ");
	rename(" Mine ");
	const alerts = alertRequests.length;
	rename("x".repeat(81));
	expect(alertRequests.slice(alerts).map((request) => request.title)).toEqual([
		"Category names can be up to 80 characters.",
	]);
	expect(fake.mutations).toEqual([]);
	rename("  Shipped  ");
	await settle();
	expect(fake.mutations).toEqual([
		{ method: "evener/pin-section/rename", params: { sectionId: "pins-1", name: "Shipped" } },
	]);
	act(() => tree.unmount());
});

it("deletes a category after the spec's confirmation", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	const fake = hub(fleet);
	connect(id, fake.client, "ready");
	const tree = await mount(navigation());
	const confirm = confirmDelete(tree, "Mine");
	expect(confirm?.title).toBe("Delete “Mine”?");
	expect(confirm?.message).toBe("Its sessions stay; they're only unpinned.");
	expect(confirm?.buttons?.map((button) => [button.text, button.style])).toEqual([
		["Cancel", "cancel"],
		["Delete", "destructive"],
	]);
	expect(fake.mutations).toEqual([]);
	act(() => confirm?.buttons?.[1].onPress?.());
	await settle();
	expect(fake.mutations).toEqual([{ method: "evener/pin-section/delete", params: { sectionId: "pins-1" } }]);
	act(() => tree.unmount());
});

it("sends no delete for a category that left the catalog while its confirmation was up", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	const shape = { ...fleet, pins: [...fleet.pins] };
	const fake = hub(shape);
	connect(id, fake.client, "ready");
	const tree = await mount(navigation());
	const confirm = confirmDelete(tree, "Mine");
	shape.pins = [fleet.pins[1]];
	// This fake hub answers every read at revision 1, so the change names none.
	act(() => fake.invalidate(1, [{ kind: "pin_catalog" }]));
	await settle();
	expect(pinHeaders(tree)).toEqual(["Empty, 0 sessions"]);
	act(() => confirm?.buttons?.[1].onPress?.());
	await settle();
	expect(fake.mutations).toEqual([]);
	act(() => tree.unmount());
});

it("offers only Delete off iOS, where there is no text prompt", async () => {
	const { Platform } = (await import("react-native")) as unknown as { Platform: { OS: string } };
	const id = hubId();
	adoptedAnHourAgo(id);
	const fake = hub(fleet);
	connect(id, fake.client, "ready");
	const tree = await mount(navigation());
	Platform.OS = "android";
	try {
		pressLabel(tree, "Mine, category menu");
		const menu = alertRequests.at(-1);
		expect(menu?.title).toBe("Mine");
		expect(menu?.buttons?.map((button) => [button.text, button.style])).toEqual([
			["Delete", "destructive"],
			["Cancel", "cancel"],
		]);
		act(() => menu?.buttons?.[0].onPress?.());
		expect(alertRequests.at(-1)?.title).toBe("Delete “Mine”?");
	} finally {
		Platform.OS = "ios";
	}
	act(() => tree.unmount());
});

it("hides ⋯ while disconnected, and a confirmation answered after the drop sends nothing", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	const fake = hub(fleet);
	connect(id, fake.client, "ready");
	const nav = navigation();
	const tree = await mount(nav);
	const confirm = confirmDelete(tree, "Mine");
	connect(id, null, "reconnecting");
	rerender(tree, nav);
	expect(menuLabels(tree)).toEqual([]);
	// The categories stay on screen with their rows.
	expect(pinHeaders(tree)).toEqual(["Mine, 3 sessions", "Empty, 0 sessions"]);
	expect(hasRow(tree, "Kept note")).toBe(true);
	act(() => confirm?.buttons?.[1].onPress?.());
	await settle();
	expect(fake.mutations).toEqual([]);
	act(() => tree.unmount());
});

it("dims a category while its change is on its way, and hides every ⋯ until it lands", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	const fake = hub(fleet, undefined, undefined, { holdChanges: true });
	connect(id, fake.client, "ready");
	const tree = await mount(navigation());
	expect(opacity(tree, "Mine")).toBe(1);
	const mine = confirmDelete(tree, "Mine");
	const empty = confirmDelete(tree, "Empty");
	act(() => mine?.buttons?.[1].onPress?.());
	await settle();
	// The second confirmation was up before the first change went out;
	// pressed while that change is pending, it sends nothing.
	act(() => empty?.buttons?.[1].onPress?.());
	await settle();
	expect(fake.mutations).toEqual([{ method: "evener/pin-section/delete", params: { sectionId: "pins-1" } }]);
	expect(opacity(tree, "Mine")).toBe(0.5);
	expect(opacity(tree, "Empty")).toBe(1);
	expect(menuLabels(tree)).toEqual([]);
	fake.release();
	await settle();
	expect(opacity(tree, "Mine")).toBe(1);
	expect(menuLabels(tree)).toEqual(["Mine, category menu", "Empty, category menu"]);
	act(() => tree.unmount());
});

it("never shows the journal's error, dims the category and hides ⋯ while a change can't be confirmed", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	const fake = hub(fleet, undefined, undefined, { refuse: true });
	connect(id, fake.client, "ready");
	const tree = await mount(navigation());
	const confirm = confirmDelete(tree, "Mine");
	act(() => confirm?.buttons?.[1].onPress?.());
	await settle();
	expect(fake.mutations).toHaveLength(1);
	expect(menuLabels(tree)).toEqual([]);
	expect(renderedText(tree)).not.toMatch(/Refresh|trying again|Could not confirm/);
	// An unresolved change dims its category as a pending one does (the unified
	// journal gate: pending or uncertain).
	expect(opacity(tree, "Mine")).toBe(0.5);
	act(() => tree.unmount());
});

it("gives every control a touch target at least 44pt tall", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	connect(id, hub(fleet).client, "ready");
	const tree = await mount(navigation());
	const flat = (style: unknown): Record<string, number> =>
		Object.assign(
			{},
			...[typeof style === "function" ? style({ pressed: false }) : style].flat(Number.POSITIVE_INFINITY),
		);
	const short = tree.root
		.findAll((node) => node.type === ("Pressable" as never))
		.map((node) => {
			const style = flat(node.props.style);
			const slop = node.props.hitSlop ?? {};
			return {
				label: node.props.accessibilityLabel,
				height: (style.minHeight ?? style.height ?? 0) + (slop.top ?? 0) + (slop.bottom ?? 0),
			};
		})
		.filter((target) => target.height < 44);
	expect(short).toEqual([]);
	// When the summary line wraps, its rows sit far enough apart that one
	// count's slop never reaches into the next row's.
	const summary = tree.root.find((node) => node.props.testID === "live-summary");
	const slop = summary.findAll((node) => node.type === ("Pressable" as never))[0].props.hitSlop;
	expect(flat(summary.props.style).rowGap).toBeGreaterThanOrEqual(slop.top + slop.bottom);
	act(() => tree.unmount());
});

// The summary wraps between its counts at large text sizes; each separator
// ends the count before it, so no wrapped line starts with one (spec 7.1).
it("ends each summary count but the last with its separator, so no wrapped line starts with a dot", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	connect(id, hub(fleet).client, "ready");
	const tree = await mount(navigation());
	const summary = tree.root.find((node) => node.props.testID === "live-summary" && String(node.type) === "View");
	const units = summary.children.filter((child): child is ReactTestInstance => typeof child !== "string");
	expect(units.length).toBeGreaterThan(1);
	const text = (unit: ReactTestInstance) =>
		unit.findAll((node) => String(node.type) === "Text").map((node) => [node.props.children].flat().join(""));
	units.forEach((unit, index) => {
		const parts = text(unit);
		expect(parts[0]).not.toBe(" · ");
		expect(parts.at(-1) === " · ").toBe(index < units.length - 1);
	});
	act(() => tree.unmount());
});

it("starts a fresh Board when you switch hubs, and stops the old hub's", async () => {
	const first = hubId();
	const second = hubId();
	adoptedAnHourAgo(first);
	adoptedAnHourAgo(second);
	const hubA = hub(fleet);
	const hubB = hub({ ...fleet, live: [[working, idleOne]], needsYou: [] });
	connect(first, hubA.client, "ready");
	const nav = navigation();
	const tree = await mount(nav);
	pressLabel(tree, "Idle, 2 sessions");
	expect(hasRow(tree, "Old chore")).toBe(true);
	connect(second, hubB.client, "ready");
	rerender(tree, nav);
	await settle();
	expect(hasRow(tree, "Fix retry loop")).toBe(false);
	expect(hasRow(tree, "Build docs")).toBe(true);
	// Hub B keeps its own fold state: Idle starts folded there.
	expect(bandHeaders(tree)).toContain("Idle · 1");
	expect(hasRow(tree, "Old chore")).toBe(false);
	// Hub A's Board is gone: its invalidations read nothing.
	const readsOnA = hubA.requests.length;
	hubA.invalidate(1, [{ kind: "section", section: "live", revision: 2 }]);
	await settle();
	expect(hubA.requests).toHaveLength(readsOnA);
	act(() => tree.unmount());
});

/** The search field at the top of the Board's scroller, driven the way a
 * person drives it. */
function searchField(tree: ReactTestRenderer) {
	const input = () =>
		tree.root.find(
			(node) => node.type === ("TextInput" as never) && node.props.accessibilityLabel === "Search sessions",
		);
	return {
		input,
		focus: () => act(() => input().props.onFocus()),
		type: async (text: string) => {
			act(() => input().props.onChangeText(text));
			await advance(250);
		},
		cancel: () => pressLabel(tree, "Cancel search"),
	};
}
/** A search result, found by its accessibility label's start. */
function resultTitled(tree: ReactTestRenderer, title: string) {
	return tree.root.find(
		(node) =>
			node.props.testID === "search-result" &&
			typeof node.props.accessibilityLabel === "string" &&
			node.props.accessibilityLabel.startsWith(`${title}, `),
	);
}
const resultTitles = (tree: ReactTestRenderer) =>
	tree.root
		.findAll((node) => node.props.testID === "search-result")
		.map((node) => node.props.accessibilityLabel.split(", ")[0]);
const hasCancel = (tree: ReactTestRenderer) =>
	tree.root.findAll((node) => node.type === ("Pressable" as never) && node.props.accessibilityLabel === "Cancel search")
		.length > 0;

it("puts the search field first in the Board's scroller, which starts scrolled just past it", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	connect(id, hub(fleet).client, "ready");
	const nav = navigation();
	const tree = await mount(nav);
	// No header search bar: it neither hid on scroll nor kept the chips pinned.
	expect(headerOptions(nav).headerSearchBarOptions).toBeUndefined();
	const scroller = boardScroller(tree);
	const [first] = scroller.findAll(
		(node) =>
			node.props.testID === "search-field" || node.props.testID === "notice" || node.props.testID === "live-block",
	);
	expect(first.props.testID).toBe("search-field");
	// Pulling down reveals the field (spec 7.3).
	const height = first.props.style.height;
	expect(height).toBeGreaterThan(0);
	expect(scroller.props.contentOffset).toEqual({ x: 0, y: height });
	// iOS keeps that offset only while the content is taller than the
	// viewport, so even a short Board is tall enough to hide the field (the
	// window here is 844pt).
	expect(scroller.props.contentContainerStyle.minHeight).toBeGreaterThanOrEqual(844 + height);
	expect(searchField(tree).input().props).toMatchObject({ placeholder: "Search sessions", autoCapitalize: "none" });
	// Cancel shows only while searching.
	expect(hasCancel(tree)).toBe(false);
	act(() => tree.unmount());
});

it("scrolls to the top and focuses the field when you tap Search", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	connect(id, hub(fleet).client, "ready");
	const nav = navigation();
	const { tree, scrollTo, focus } = await mountWithInstances(nav);
	const [search] = headerOptions(nav).unstable_headerRightItems({});
	expect(search).toMatchObject({
		type: "button",
		label: "Search",
		accessibilityLabel: "Search sessions",
		icon: { type: "sfSymbol", name: "magnifyingglass" },
	});
	act(() => search.onPress());
	expect(scrollTo).toHaveBeenLastCalledWith({ y: 0, animated: true });
	expect(focus).toHaveBeenCalledTimes(1);
	// Cancelling tucks the field back out of view.
	searchField(tree).focus();
	searchField(tree).cancel();
	const height = tree.root.find((node) => node.props.testID === "search-field").props.style.height;
	expect(scrollTo).toHaveBeenLastCalledWith({ y: height, animated: true });
	// Off iOS the header draws its own Search button.
	const fallback = render(headerOptions(nav).headerRight());
	act(() => fallback.root.findByType("Pressable" as never).props.onPress());
	expect(focus).toHaveBeenCalledTimes(2);
	act(() => fallback.unmount());
	act(() => tree.unmount());
});

it("searches the hub as you type, and a result opens the way the Board opens its row, marking it seen", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	const gone = session("local:gone", { title: "Old report" });
	const fake = hub({ ...fleet, searchOnly: [gone] });
	connect(id, fake.client, "ready");
	const nav = navigation();
	const tree = await mount(nav);
	expect(bandHeaders(tree)).toContain("FINISHED · 1");
	const bar = searchField(tree);
	bar.focus();
	// Search takes the Board's place under the field, in the Board's scroller.
	expect(hasRow(tree, "Ship it")).toBe(false);
	expect(tree.root.findAll((node) => node.props.testID === "chips" && String(node.type) === "View")).toHaveLength(0);
	expect(tree.root.findAll((node) => node.props.testID === "live-block")).toHaveLength(0);
	expect(boardScroller(tree).props.keyboardShouldPersistTaps).toBe("handled");
	expect(hasCancel(tree)).toBe(true);
	await bar.type("i");
	expect(fake.searches).toEqual(["i"]);
	expect(resultTitles(tree)).toEqual(["Fix retry loop", "Build docs", "Ship it", "Pick a name"]);
	expect(resultTitled(tree, "Pick a name").props.accessibilityLabel).toBe("Pick a name, Question, evener, 5 minutes");
	act(() => resultTitled(tree, "Ship it").props.onPress());
	expect(nav.navigate).toHaveBeenLastCalledWith("Conversation", { hubId: id, ref: "local:done", title: "Ship it" });
	expect(seenMarkers(id).isSeen(finished)).toBe(true);
	// A session only in Needs you (past Live's loaded pages) is found too.
	expect(seenMarkers(id).isSeen(asking)).toBe(false);
	act(() => resultTitled(tree, "Pick a name").props.onPress());
	expect(nav.navigate).toHaveBeenLastCalledWith("Conversation", { hubId: id, ref: "local:ask", title: "Pick a name" });
	expect(seenMarkers(id).isSeen(asking)).toBe(true);
	// Live results come first, then past ones.
	await bar.type("re");
	expect(resultTitles(tree)).toEqual(["Fix retry loop", "Old chore", "Older chore", "Old report"]);
	expect(resultTitled(tree, "Old report").props.accessibilityLabel).toBe("Old report, evener, 5 minutes");
	// A session the Board doesn't list has no Finished state to clear.
	act(() => resultTitled(tree, "Old report").props.onPress());
	expect(nav.navigate).toHaveBeenLastCalledWith("Conversation", { hubId: id, ref: "local:gone", title: "Old report" });
	// Cancel empties the field and brings the Board back.
	bar.cancel();
	expect(hasRow(tree, "Build docs")).toBe(true);
	expect(tree.root.findAll((node) => node.props.testID === "chips" && String(node.type) === "View")).toHaveLength(1);
	expect(tree.root.findAll((node) => node.props.testID === "search-result")).toHaveLength(0);
	expect(bar.input().props.value).toBe("");
	expect(hasCancel(tree)).toBe(false);
	act(() => tree.unmount());
});

it("narrows results to live sessions in the Live scope", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	connect(id, hub({ ...fleet, searchOnly: [session("local:gone", { title: "Old report" })] }).client, "ready");
	const nav = navigation();
	const tree = await mount(nav);
	const bar = searchField(tree);
	bar.focus();
	await bar.type("report");
	expect(resultTitles(tree)).toEqual(["Old report"]);
	pressLabel(tree, "Live");
	expect(resultTitles(tree)).toEqual([]);
	expect(texts(tree)).toContain("No sessions match.");
	pressLabel(tree, "All");
	expect(resultTitles(tree)).toEqual(["Old report"]);
	act(() => tree.unmount());
});

it("lists a live session once when the hub's past results name it too", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	// The real hub's past index holds live sessions' records too.
	connect(id, hub({ ...fleet, searchOnly: [finished] }).client, "ready");
	const tree = await mount(navigation());
	const bar = searchField(tree);
	bar.focus();
	await bar.type("ship");
	expect(resultTitles(tree)).toEqual(["Ship it"]);
	expect(texts(tree)).toContain("SESSIONS · 1");
	act(() => tree.unmount());
});

it("holds a scope picked before you type", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	connect(id, hub({ ...fleet, searchOnly: [session("local:gone", { title: "Old report" })] }).client, "ready");
	const nav = navigation();
	const tree = await mount(nav);
	const bar = searchField(tree);
	bar.focus();
	pressLabel(tree, "Live");
	await bar.type("report");
	expect(resultTitles(tree)).toEqual([]);
	expect(texts(tree)).toContain("No sessions match.");
	act(() => tree.unmount());
});

it("opens a result only a pinned category lists the way the Board opens its row, marking it seen", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	// Not live, so search finds it among past sessions.
	const pinnedOnly = session("local:pinned", { title: "Pinned draft", live: false, updated_at: minutesAgo(10) });
	const shape = { ...fleet, pinned: { ...fleet.pinned, "pins-1": [pinnedOnly] }, searchOnly: [pinnedOnly] };
	connect(id, hub(shape).client, "ready");
	const nav = navigation();
	const tree = await mount(nav);
	expect(seenMarkers(id).isSeen(pinnedOnly)).toBe(false);
	const bar = searchField(tree);
	bar.focus();
	await bar.type("pinned");
	act(() => resultTitled(tree, "Pinned draft").props.onPress());
	expect(nav.navigate).toHaveBeenLastCalledWith("Conversation", {
		hubId: id,
		ref: "local:pinned",
		title: "Pinned draft",
	});
	expect(seenMarkers(id).isSeen(pinnedOnly)).toBe(true);
	act(() => tree.unmount());
});

it("says a failed search failed, and never asks you to retry", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	const fake = hub({ ...fleet, searchFails: true });
	connect(id, fake.client, "ready");
	const nav = navigation();
	const tree = await mount(nav);
	const bar = searchField(tree);
	bar.focus();
	await bar.type("fix");
	expect(texts(tree)).toContain("Couldn't search this hub's sessions.");
	expect(renderedText(tree)).not.toMatch(/retry|try again|refresh/i);
	// A new query tries again.
	await bar.type("fixes");
	expect(fake.searches).toEqual(["fix", "fixes"]);
	act(() => tree.unmount());
});

it("searches nothing while connecting, and asks for the typed query once the connection is ready", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	const fake = hub(fleet);
	connect(id, fake.client, "connecting");
	const nav = navigation();
	const tree = await mount(nav);
	const bar = searchField(tree);
	bar.focus();
	await bar.type("ship");
	expect(fake.searches).toEqual([]);
	expect(texts(tree)).toContain("Search works when the hub is connected.");
	connect(id, fake.client, "ready");
	rerender(tree, nav);
	await settle();
	expect(fake.searches).toEqual(["ship"]);
	expect(resultTitles(tree)).toEqual(["Ship it"]);
	act(() => tree.unmount());
});

it("doesn't search while the Board is out of view, and asks again when it returns", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	const fake = hub(fleet);
	connect(id, fake.client, "ready");
	const nav = navigation();
	const tree = await mount(nav);
	const bar = searchField(tree);
	bar.focus();
	await bar.type("ship");
	expect(fake.searches).toEqual(["ship"]);
	// A pushed screen covers the Board: a reconnect must not send the query.
	harness.stack = screenOverBoard;
	connect(id, fake.client, "ready");
	rerender(tree, nav);
	await settle();
	expect(fake.searches).toEqual(["ship"]);
	// Back in view, the field's query asks again.
	harness.stack = { index: 0, routes: [{ key: "Sessions", name: "Sessions" }] };
	rerender(tree, nav);
	await settle();
	expect(fake.searches).toEqual(["ship", "ship"]);
	act(() => tree.unmount());
});

it("gives the search field a 44pt touch target while it still draws 36pt", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	connect(id, hub(fleet).client, "ready");
	const tree = await mount(navigation());
	const row = tree.root.find((node) => node.props.testID === "search-field");
	expect(row.props.style.height).toBe(52);
	// The visible pill is still the stock 36pt.
	const pill = row.findAll((node) => node.type === ("View" as never) && node.props.style?.position === "absolute")[0];
	expect(pill?.props.style.height).toBe(36);
	// The input's own row is the target, so a slop wouldn't be clipped away.
	const input = row.find(
		(node) => node.type === ("TextInput" as never) && node.props.accessibilityLabel === "Search sessions",
	);
	expect(input.parent?.props.style.height).toBeGreaterThanOrEqual(44);
	act(() => tree.unmount());
});

it("re-tucks the search field when Dynamic Type changes its height", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	connect(id, hub(fleet).client, "ready");
	const nav = navigation();
	const { tree, scrollTo } = await mountWithInstances(nav);
	scrollTo.mockClear();
	harness.fontScale = 1.5;
	rerender(tree, nav);
	await settle();
	const height = tree.root.find((node) => node.props.testID === "search-field").props.style.height;
	expect(height).toBe(70);
	expect(scrollTo).toHaveBeenCalledWith({ y: 70, animated: false });
	act(() => tree.unmount());
});

it("leaves a field the reader revealed or scrolled past where it is on a Dynamic Type change", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	connect(id, hub(fleet).client, "ready");
	const nav = navigation();
	const { tree, scrollTo } = await mountWithInstances(nav);
	const scrollToY = (y: number) =>
		act(() =>
			boardScroller(tree).props.onScroll({
				nativeEvent: { contentOffset: { x: 0, y }, layoutMeasurement: { width: 390, height: 700 } },
			}),
		);
	// Revealed by pulling down, without focusing.
	scrollToY(0);
	scrollTo.mockClear();
	harness.fontScale = 1.5;
	rerender(tree, nav);
	await settle();
	expect(scrollTo).not.toHaveBeenCalled();
	// Scrolled down the list.
	scrollToY(400);
	harness.fontScale = 1;
	rerender(tree, nav);
	await settle();
	expect(scrollTo).not.toHaveBeenCalled();
	act(() => tree.unmount());
});

it("keeps one search through a sheet over the Board, without re-asking", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	const fake = hub(fleet);
	connect(id, fake.client, "ready");
	const nav = navigation();
	const tree = await mount(nav);
	const bar = searchField(tree);
	bar.focus();
	await bar.type("ship");
	expect(fake.searches).toEqual(["ship"]);
	// A sheet over the Board is still the Board (ruling 28): the binding holds,
	// so closing it asks nothing again.
	harness.stack = sheetOverBoard;
	setFocused(false);
	rerender(tree, nav);
	await settle();
	expect(fake.searches).toEqual(["ship"]);
	harness.stack = { index: 0, routes: [{ key: "Sessions", name: "Sessions" }] };
	setFocused(true);
	rerender(tree, nav);
	await settle();
	expect(fake.searches).toEqual(["ship"]);
	act(() => tree.unmount());
});

it("remembers the queries you opened a result from, per hub, and clears them", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	const fake = hub(fleet);
	connect(id, fake.client, "ready");
	const nav = navigation();
	const tree = await mount(nav);
	const bar = searchField(tree);
	bar.focus();
	// Nothing searched yet, so an empty field shows nothing.
	expect(texts(tree)).not.toContain("RECENT");
	await bar.type("ship");
	act(() => resultTitled(tree, "Ship it").props.onPress());
	await bar.type("build");
	act(() => resultTitled(tree, "Build docs").props.onPress());
	// A query you didn't open anything from isn't remembered.
	await bar.type("chore");
	await bar.type("");
	expect(texts(tree)).toContain("RECENT");
	expect(
		tree.root
			.findAll(
				(node) => node.type === ("Pressable" as never) && node.props.accessibilityLabel?.startsWith("Search for "),
			)
			.map((node) => node.props.accessibilityLabel),
	).toEqual(["Search for build", "Search for ship"]);
	expect(JSON.parse(harness.kv.get(`evener.native.recent-searches.${id}`) ?? "null")).toEqual(["build", "ship"]);
	// Tapping one searches for it again, and fills the search field.
	pressLabel(tree, "Search for ship");
	expect(bar.input().props.value).toBe("ship");
	await advance(250);
	expect(fake.searches.at(-1)).toBe("ship");
	expect(resultTitles(tree)).toEqual(["Ship it"]);
	await bar.type("");
	pressLabel(tree, "Clear recent searches");
	expect(texts(tree)).not.toContain("RECENT");
	expect(JSON.parse(harness.kv.get(`evener.native.recent-searches.${id}`) ?? "null")).toEqual([]);
	act(() => tree.unmount());
});

it("opens a session after marking it seen", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	connect(id, hub(fleet).client, "ready");
	const nav = navigation();
	const tree = await mount(nav);
	act(() => rowTitled(tree, "Ship it").props.onPress());
	expect(nav.navigate).toHaveBeenLastCalledWith("Conversation", { hubId: id, ref: "local:done", title: "Ship it" });
	expect(seenMarkers(id).isSeen(finished)).toBe(true);
	// Seen, the session leaves Finished for the folded Idle band.
	expect(bandHeaders(tree)).toEqual(["NEEDS YOU · 2", "WORKING · 1", "Idle · 3"]);
	act(() => tree.unmount());
});

// Rows the hub decides (S4): each carries its turn end, and the hub says
// whether it is unseen. The device's markers say the opposite of the hub for
// both: its epoch covers the first, and it holds an unread mark for the second.
const hubUnseen = session("local:hub-unseen", {
	title: "Hub unseen",
	updated_at: minutesAgo(90),
	turn_ended_at: minutesAgo(90),
	unseen: true,
});
const hubSeen = session("local:hub-seen", {
	title: "Hub seen",
	updated_at: minutesAgo(6),
	turn_ended_at: minutesAgo(6),
	unseen: false,
});
const pinnedUnseen = session("local:pinned-unseen", {
	title: "Pinned unseen",
	live: true,
	updated_at: minutesAgo(80),
	turn_ended_at: minutesAgo(80),
	unseen: true,
});
const hubFleet: Fleet = {
	...fleet,
	live: [[failing, working, hubUnseen, hubSeen, finished]],
	pinned: { ...fleet.pinned, "pins-1": [pinnedUnseen, keptNote] },
};
/** A device an hour past first run that marked Hub seen unread itself. */
function deviceDisagrees(hub: string) {
	adoptedAnHourAgo(hub);
	seenMarkers(hub).markUnread("local:hub-seen");
}
const stateOf = (tree: ReactTestRenderer, title: string) =>
	rowTitled(tree, title).props.accessibilityLabel.split(", ")[1];

it("takes Finished or Idle from the hub for a row that carries its turn end, whatever the device's markers say", async () => {
	const id = hubId();
	deviceDisagrees(id);
	const fake = hub(hubFleet);
	connect(id, fake.client, "ready");
	const tree = await mount(navigation());
	pressLabel(tree, "Idle, 1 session");
	expect(stateOf(tree, "Hub unseen")).toBe("Finished");
	expect(stateOf(tree, "Hub seen")).toBe("Idle");
	// A row without a turn end still follows the device: updated since its epoch.
	expect(stateOf(tree, "Ship it")).toBe("Finished");
	// The categories classify the same way.
	expect(stateOf(tree, "Pinned unseen")).toBe("Finished");
	expect(bandHeaders(tree)).toEqual(["NEEDS YOU · 2", "FINISHED · 2", "WORKING · 1", "Idle · 1"]);
	expect(fake.seen).toEqual([]);
	act(() => tree.unmount());
});

it("marks a hub row seen through its turn end when you open it, and clears its dot at once", async () => {
	const id = hubId();
	deviceDisagrees(id);
	const shape = { ...hubFleet };
	const fake = hub(shape);
	connect(id, fake.client, "ready");
	const nav = navigation();
	const tree = await mount(nav);
	act(() => rowTitled(tree, "Hub unseen").props.onPress());
	expect(nav.navigate).toHaveBeenLastCalledWith("Conversation", {
		hubId: id,
		ref: "local:hub-unseen",
		title: "Hub unseen",
	});
	// The hub's rows still say unseen; the pending mark wins until they catch up.
	expect(bandHeaders(tree)).toEqual(["NEEDS YOU · 2", "FINISHED · 1", "WORKING · 1", "Idle · 2"]);
	await settle();
	expect(fake.seen).toEqual([[{ ref: "local:hub-unseen", seenThrough: Date.parse(minutesAgo(90)) }]]);
	// The device's own markers are not touched for a hub row.
	expect(JSON.parse(harness.kv.get(`evener.native.seen.${id}`) ?? "{}").sessions).toEqual({
		"local:hub-seen": { unread: true },
	});
	// Opening it again, now from Idle, costs no request.
	pressLabel(tree, "Idle, 2 sessions");
	expect(stateOf(tree, "Hub unseen")).toBe("Idle");
	act(() => rowTitled(tree, "Hub unseen").props.onPress());
	await settle();
	expect(fake.seen).toHaveLength(1);
	// Opening a pinned hub row marks it the same way.
	act(() => rowTitled(tree, "Pinned unseen").props.onPress());
	await settle();
	expect(stateOf(tree, "Pinned unseen")).toBe("Idle");
	expect(fake.seen.at(-1)).toEqual([{ ref: "local:pinned-unseen", seenThrough: Date.parse(minutesAgo(80)) }]);
	// The hub's rows catch up, and the pending mark goes: a later unseen for
	// the same turn would show again.
	shape.live = [[failing, working, { ...hubUnseen, unseen: false }, hubSeen, finished]];
	// This fake hub answers every read at revision 1, so the change names none.
	act(() => fake.invalidate(1, [{ kind: "section", section: "live" }]));
	await settle();
	expect(hubSeenMarks(id).isSeenOnHub(hubUnseen)).toBe(false);
	expect(hubSeenMarks(id).isSeenOnHub(pinnedUnseen)).toBe(true);
	act(() => tree.unmount());
});

it("marks a row without a turn end on the device, and sends the hub nothing", async () => {
	const id = hubId();
	deviceDisagrees(id);
	const fake = hub(hubFleet);
	connect(id, fake.client, "ready");
	const tree = await mount(navigation());
	act(() => rowTitled(tree, "Ship it").props.onPress());
	await settle();
	expect(seenMarkers(id).isSeen(finished)).toBe(true);
	expect(fake.seen).toEqual([]);
	act(() => tree.unmount());
});

it("sends a mark made while the connection was down once it's ready again", async () => {
	const id = hubId();
	deviceDisagrees(id);
	const fake = hub(hubFleet);
	connect(id, fake.client, "ready");
	const nav = navigation();
	const tree = await mount(nav);
	connect(id, null, "reconnecting");
	rerender(tree, nav);
	act(() => rowTitled(tree, "Hub unseen").props.onPress());
	await settle();
	expect(bandHeaders(tree)).toContain("FINISHED · 1");
	expect(fake.seen).toEqual([]);
	connect(id, fake.client, "ready");
	rerender(tree, nav);
	await settle();
	expect(fake.seen).toEqual([[{ ref: "local:hub-unseen", seenThrough: Date.parse(minutesAgo(90)) }]]);
	act(() => tree.unmount());
});

it("reads nothing while connecting, and reads the Board once the connection is ready", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	const fake = hub(fleet);
	connect(id, fake.client, "connecting");
	const nav = navigation();
	const tree = await mount(nav);
	expect(fake.requests).toHaveLength(0);
	connect(id, fake.client, "ready");
	rerender(tree, nav);
	await settle();
	// The Board's reads, its categories', and the Projects section's catalog.
	// An empty organization journal needs no read before ⋯ shows.
	expect(fake.requests.map((read) => read.section ?? read.resource).sort()).toEqual([
		"catalog",
		"live",
		"manifest",
		"needs_you",
		"pin_catalog",
		"pin_section",
		"pin_section",
	]);
	expect(bandHeaders(tree)).toEqual(["NEEDS YOU · 2", "FINISHED · 1", "WORKING · 1", "Idle · 2"]);
	act(() => tree.unmount());
});

it("keeps its rows when the connection drops, grays the meters, and says Reconnecting… after 2 seconds", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	connect(id, hub(fleet).client, "ready");
	const nav = navigation();
	const tree = await mount(nav);
	expect(tree.root.findAllByType(PulseMeter).map((meter) => meter.props.tone ?? "alive")).not.toContain("gray");
	connect(id, null, "reconnecting");
	rerender(tree, nav);
	expect(hasRow(tree, "Build docs")).toBe(true);
	expect(bandHeaders(tree)).toEqual(["NEEDS YOU · 2", "FINISHED · 1", "WORKING · 1", "Idle · 2"]);
	expect(texts(tree)).not.toContain("Reconnecting…");
	act(() => {
		vi.advanceTimersByTime(2000);
	});
	expect(texts(tree)).toContain("Reconnecting…");
	const tones = tree.root.findAllByType(PulseMeter).map((meter) => meter.props.tone);
	expect(tones.length).toBeGreaterThan(0);
	expect(new Set(tones)).toEqual(new Set(["gray"]));
	act(() => tree.unmount());
});

it("offers no Reconnect or Refresh anywhere", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	connect(id, hub(fleet).client, "ready");
	const nav = navigation();
	const tree = await mount(nav);
	connect(id, null, "closed");
	rerender(tree, nav);
	act(() => {
		vi.advanceTimersByTime(60_000);
	});
	const options = headerOptions(nav);
	const headerItems = [...options.unstable_headerLeftItems({}), ...options.unstable_headerRightItems({})];
	const headerLabels = headerItems
		.flatMap((item: { label?: string; menu?: { items: Array<{ label: string }> } }) => [
			item.label,
			...(item.menu?.items.map((entry) => entry.label) ?? []),
		])
		.filter((label): label is string => typeof label === "string");
	// The hub button is a custom view: read what it draws. It opens the Hub
	// sheet, whose own pages pin that they never ask to reconnect.
	const hubButton = render(headerItems[0].element);
	const labels = tree.root
		.findAll((node) => typeof node.props.accessibilityLabel === "string")
		.map((node) => node.props.accessibilityLabel as string);
	for (const text of [...texts(tree), ...labels, ...headerLabels, ...texts(hubButton)])
		expect(text).not.toMatch(/^(Reconnect|Refresh|Retry)\b/);
	expect(renderedText(tree)).not.toMatch(/Reconnect\b|Refresh|pull/i);
	act(() => hubButton.unmount());
	act(() => tree.unmount());
});

it("says what to do on an empty Board, with a New session button", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	connect(id, hub({ ...fleet, live: [[]], needsYou: [] }).client, "ready");
	const nav = navigation();
	const tree = await mount(nav);
	expect(texts(tree)).toContain("Nothing's running. Start a session to put an agent to work.");
	expect(bandHeaders(tree)).toEqual([]);
	const button = tree.root.find(
		(node) =>
			node.type === ("Pressable" as never) &&
			node.findAll((child) => child.type === ("Text" as never) && child.props.children === "New session").length > 0,
	);
	act(() => button.props.onPress());
	expect(nav.navigate).toHaveBeenLastCalledWith("NewSession", { hubId: id, hubName: "Work hub" });
	act(() => tree.unmount());
});

it("shows three skeleton rows until the first read lands", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	const fake = hub(fleet, () => true);
	connect(id, fake.client, "ready");
	const tree = await mount(navigation());
	expect(skeletonRows(tree)).toHaveLength(3);
	fake.release();
	await settle();
	expect(skeletonRows(tree)).toHaveLength(0);
	act(() => tree.unmount());
});

it("shows the first read's rows at once under a finger that touched the skeleton, never the empty Board", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	const fake = hub(fleet, () => true);
	connect(id, fake.client, "ready");
	const tree = await mount(navigation());
	expect(skeletonRows(tree)).toHaveLength(3);
	listEvent(tree, "onTouchStart");
	fake.release();
	await settle();
	expect(texts(tree)).not.toContain("Nothing's running. Start a session to put an agent to work.");
	expect(listOrder(tree)).toEqual(workingOrder);
	liftFinger(tree);
	await advance(100);
	expect(listOrder(tree)).toEqual(workingOrder);
	act(() => tree.unmount());
});

it("says Update needed and why when no retry can fix the close", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	connect(id, hub(fleet).client, "ready");
	const nav = navigation();
	const tree = await mount(nav);
	connect(id, null, "closed", { fatal: true });
	rerender(tree, nav);
	expect(texts(tree)).toContain("Update needed");
	expect(texts(tree)).toContain(INCOMPATIBLE_TEXT);
	expect(hasRow(tree, "Build docs")).toBe(true);
	act(() => tree.unmount());
});

it("on a device's first run, reads every Live page before adopting, so nothing flashes Finished", async () => {
	const id = hubId();
	// Live is sorted by attention, so the newest session sits on page 2.
	const newest = session("local:newest", { title: "Newest", updated_at: minutesAgo(1) });
	const older = session("local:older", { title: "Older", updated_at: minutesAgo(30) });
	const fake = hub(
		{ ...fleet, live: [[failing, older], [newest]], needsYou: [failing] },
		(read) => (read.offset ?? 0) > 0,
	);
	connect(id, fake.client, "ready");
	const tree = await mount(navigation());
	expect(fake.requests.filter((read) => read.section === "live").map((read) => read.offset)).toEqual([0, 2]);
	expect(seenMarkers(id).adopted).toBe(false);
	expect(bandHeaders(tree)).not.toContain("FINISHED · 1");
	fake.release();
	await settle();
	expect(seenMarkers(id).adopted).toBe(true);
	expect(seenMarkers(id).isSeen(newest)).toBe(true);
	expect(bandHeaders(tree)).toEqual(["NEEDS YOU · 1", "Idle · 2"]);
	act(() => tree.unmount());
});

it("stops first-run paging while the Board is out of view, and finishes it on return", async () => {
	const id = hubId();
	let holdLater = true;
	const fake = hub(
		{ ...fleet, live: [[failing, working], [finished], [idleOne]], needsYou: [failing] },
		(read) => holdLater && (read.offset ?? 0) > 0,
	);
	connect(id, fake.client, "ready");
	const tree = await mount(navigation());
	expect(liveReads(fake)).toEqual([0, 2]);
	// Leaving the Board mid-way cancels the page in flight; nothing replaces it.
	setFocused(false);
	await settle();
	expect(liveReads(fake)).toEqual([0, 2]);
	holdLater = false;
	fake.release();
	await settle();
	expect(liveReads(fake)).toEqual([0, 2]);
	expect(seenMarkers(id).adopted).toBe(false);
	setFocused(true);
	await settle();
	expect(liveReads(fake)).toEqual([0, 2, 2, 3]);
	expect(seenMarkers(id).adopted).toBe(true);
	act(() => tree.unmount());
});

it("shows the Draft tag on sessions with a saved draft", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	harness.drafts.set(id, new Set(["local:work"]));
	connect(id, hub(fleet).client, "ready");
	const tree = await mount(navigation());
	expect(draftTags(rowTitled(tree, "Build docs"))).toHaveLength(1);
	expect(draftTags(rowTitled(tree, "Ship it"))).toHaveLength(0);
	act(() => tree.unmount());
});

it("puts the hub's name on the left, opening the Hub, and search on the right", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	connect(id, hub(fleet).client, "ready");
	const nav = navigation();
	const tree = await mount(nav);
	const options = headerOptions(nav);
	expect(options.title).toBe("");
	// One control, as spec 7.1 draws it: the hub's name and a chevron in a
	// single header item that opens the Hub sheet (spec 12).
	const hubItems = options.unstable_headerLeftItems({});
	expect(hubItems).toHaveLength(1);
	expect(hubItems[0].type).toBe("custom");
	const hubButton = render(hubItems[0].element);
	expect(texts(hubButton)).toEqual(["Work hub"]);
	expect(hubButton.root.findAllByType("SymbolView" as never).map((node) => node.props.name)).toEqual(["chevron.down"]);
	const press = hubButton.root.findByType("Pressable" as never);
	expect(press.props.accessibilityLabel).toBe("Work hub");
	expect(press.props.accessibilityHint).toBe("Opens the Hub");
	// A long hub name truncates inside the capsule instead of growing it
	// into Search (the window is 390pt wide here).
	expect(press.props.style.maxWidth).toBeLessThanOrEqual(390 * 0.6);
	const hubName = hubButton.root.findByType("Text" as never);
	expect(hubName.props.numberOfLines).toBe(1);
	expect(hubName.props.style).toMatchObject({ flexShrink: 1 });
	act(() => press.props.onPress());
	expect(nav.navigate).toHaveBeenLastCalledWith("Hub", { screen: "HubHome", params: { hubId: id } });
	expect(harness.actionSheet).not.toHaveBeenCalled();
	act(() => hubButton.unmount());
	// The Hub opens with the hub out of reach too: it keeps its last data.
	connect(id, null, "connecting");
	rerender(tree, nav);
	const offline = render(headerOptions(nav).unstable_headerLeftItems({})[0].element);
	act(() => offline.root.findByType("Pressable" as never).props.onPress());
	expect(nav.navigate).toHaveBeenLastCalledWith("Hub", { screen: "HubHome", params: { hubId: id } });
	act(() => offline.unmount());
	expect(options.unstable_headerRightItems({})).toEqual([expect.objectContaining({ label: "Search" })]);
	act(() => tree.unmount());
});

it("stops the hub button's label growing at xxxLarge and offers the full name in the Large Content Viewer (#3364)", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	// Accessibility XXXL: Body is 53pt, about 3.1 times its 17pt default. The
	// navigation bar's height is fixed, so a name that big would clip.
	harness.fontScale = 53 / 17;
	connect(id, hub(fleet).client, "ready");
	const nav = navigation();
	const tree = await mount(nav);
	const hubButton = render(headerOptions(nav).unstable_headerLeftItems({})[0].element);
	const label = hubButton.root.findByType("Text" as never);
	// Body's xxxLarge size, the largest before the accessibility sizes.
	expect(label.props.style.fontSize).toBe(23);
	// Text that stops growing offers the whole name in the Large Content Viewer.
	const press = hubButton.root.findByType("Pressable" as never);
	expect(press.props.accessibilityShowsLargeContentViewer).toBe(true);
	expect(press.props.accessibilityLargeContentTitle).toBe("Work hub");
	act(() => hubButton.unmount());
	act(() => tree.unmount());
});

const liveReads = (fake: ReturnType<typeof hub>) =>
	fake.requests.filter((read) => read.section === "live").map((read) => read.offset ?? 0);
const FIRST_READ_FAILED = "Couldn't load this hub's sessions. Trying again shortly.";
async function advance(ms: number) {
	act(() => {
		vi.advanceTimersByTime(ms);
	});
	await settle();
}

it("says a failed first read will be retried, and retries it with a growing backoff until it loads", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	let liveFails = true;
	const fake = hub(fleet, undefined, (read) => liveFails && read.section === "live");
	connect(id, fake.client, "ready");
	const tree = await mount(navigation());
	expect(texts(tree)).toContain(FIRST_READ_FAILED);
	expect(renderedText(tree)).not.toContain("request timed out");
	expect(skeletonRows(tree)).toHaveLength(0);
	expect(liveReads(fake)).toEqual([0]);
	// The first retry waits a second, the second two.
	await advance(999);
	expect(liveReads(fake)).toEqual([0]);
	await advance(1);
	expect(liveReads(fake)).toEqual([0, 0]);
	expect(texts(tree)).toContain(FIRST_READ_FAILED);
	liveFails = false;
	await advance(1999);
	expect(liveReads(fake)).toEqual([0, 0]);
	await advance(1);
	expect(liveReads(fake)).toEqual([0, 0, 0]);
	expect(texts(tree)).not.toContain(FIRST_READ_FAILED);
	expect(hasRow(tree, "Build docs")).toBe(true);
	// Loaded, the Board schedules no more retries.
	await advance(120_000);
	expect(liveReads(fake)).toEqual([0, 0, 0]);
	act(() => tree.unmount());
});

it("stops retrying a failed first read when it unmounts", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	const fake = hub(fleet, undefined, (read) => read.section === "live");
	connect(id, fake.client, "ready");
	const tree = await mount(navigation());
	expect(liveReads(fake)).toEqual([0]);
	// The row-age ticker and the retry. This fleet's hub predates S5, so the
	// activity poll has stopped and there is no read to recheck.
	expect(vi.getTimerCount()).toBe(2);
	act(() => tree.unmount());
	expect(vi.getTimerCount()).toBe(0);
	await advance(60_000);
	expect(liveReads(fake)).toEqual([0]);
});

it("schedules no retry while the Board is out of view", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	const fake = hub(fleet, undefined, (read) => read.section === "live");
	connect(id, fake.client, "ready");
	const tree = await mount(navigation());
	expect(liveReads(fake)).toEqual([0]);
	setFocused(false);
	await settle();
	// This fleet's hub predates S5, so no activity timers run either.
	expect(vi.getTimerCount()).toBe(0);
	await advance(60_000);
	expect(liveReads(fake)).toEqual([0]);
	act(() => tree.unmount());
});

it("retries a failed pin catalog read once Live's read is done, without saying a first read failed", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	let holdLive = true;
	let pinsFail = true;
	const fake = hub(
		fleet,
		(read) => holdLive && read.section === "live",
		(read) => pinsFail && read.resource === "pin_catalog",
	);
	connect(id, fake.client, "ready");
	const tree = await mount(navigation());
	expect(texts(tree)).not.toContain(FIRST_READ_FAILED);
	expect(skeletonRows(tree)).toHaveLength(3);
	// Live's read is still out, and a retry now would cancel it.
	await advance(60_000);
	expect(liveReads(fake)).toEqual([0]);
	holdLive = false;
	fake.release();
	await settle();
	expect(hasRow(tree, "Build docs")).toBe(true);
	expect(chipLabels(tree)).not.toContain("Mine, 3 sessions");
	pinsFail = false;
	await advance(1000);
	expect(chipLabels(tree)).toContain("Mine, 3 sessions");
	act(() => tree.unmount());
});

it("waits for a manifest read that's still out before it retries", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	let holdManifest = true;
	let needsYouFails = true;
	const fake = hub(
		fleet,
		(read) => holdManifest && read.resource === "manifest",
		(read) => needsYouFails && read.section === "needs_you",
	);
	connect(id, fake.client, "ready");
	const tree = await mount(navigation());
	const manifestReads = () => fake.requests.filter((read) => read.resource === "manifest").length;
	// A retry now would rebind, and cancel the manifest read that's still out.
	await advance(60_000);
	expect(manifestReads()).toBe(1);
	holdManifest = false;
	needsYouFails = false;
	fake.release();
	await settle();
	await advance(1000);
	expect(fake.requests.filter((read) => read.section === "needs_you")).toHaveLength(2);
	act(() => tree.unmount());
});

it("retries a failed Needs you read while the Board is in view, so first run completes", async () => {
	const id = hubId();
	let needsYouFails = true;
	const fake = hub(fleet, undefined, (read) => needsYouFails && read.section === "needs_you");
	connect(id, fake.client, "ready");
	const tree = await mount(navigation());
	expect(seenMarkers(id).adopted).toBe(false);
	needsYouFails = false;
	await advance(1000);
	expect(seenMarkers(id).adopted).toBe(true);
	act(() => tree.unmount());
});

/** Reports a 700pt viewport over a 300pt Live block by layout alone, which
 * reads Live's next page while there is one. */
async function layOut(tree: ReactTestRenderer) {
	// The Board's scroller, not the chips' horizontal one.
	const scroller = tree.root.find((node) => node.type === ("ScrollView" as never) && !node.props.horizontal);
	const liveBlock = tree.root.find((node) => node.props.testID === "live-block");
	act(() => {
		scroller.props.onLayout?.({ nativeEvent: { layout: { x: 0, y: 0, width: 390, height: 700 } } });
		liveBlock.props.onLayout?.({ nativeEvent: { layout: { x: 0, y: 0, width: 390, height: 300 } } });
		scroller.props.onContentSizeChange?.(390, 400);
	});
	await settle();
}
const writing = session("local:write", { title: "Write tests", state: "active", updated_at: minutesAgo(1) });

it("reads a failed later Live page again after the backoff, keeping the loaded rows on screen", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	let lastPageFails = true;
	let holdLive = false;
	const fake = hub(
		{ ...fleet, live: [[failing, working], [finished], [writing]] },
		(read) => holdLive && read.section === "live",
		(read) => lastPageFails && read.section === "live" && read.offset === 3,
	);
	connect(id, fake.client, "ready");
	const tree = await mount(navigation());
	await layOut(tree);
	await layOut(tree);
	expect(liveReads(fake)).toEqual([0, 2, 3]);
	const loadedRowsShown = () => ["Fix retry loop", "Build docs", "Ship it"].every((title) => hasRow(tree, title));
	expect(loadedRowsShown()).toBe(true);
	expect(hasRow(tree, "Write tests")).toBe(false);
	// Loaded once, a failure keeps the rows and says nothing.
	expect(texts(tree)).not.toContain(FIRST_READ_FAILED);
	// Layout alone never re-reads a failed page.
	await layOut(tree);
	expect(liveReads(fake)).toEqual([0, 2, 3]);
	lastPageFails = false;
	holdLive = true;
	await advance(999);
	expect(liveReads(fake)).toEqual([0, 2, 3]);
	await advance(1);
	// The retry rebinds: Live starts over from its first page.
	expect(liveReads(fake)).toEqual([0, 2, 3, 0]);
	expect(loadedRowsShown()).toBe(true);
	holdLive = false;
	fake.release();
	await settle();
	await layOut(tree);
	await layOut(tree);
	expect(liveReads(fake)).toEqual([0, 2, 3, 0, 2, 3]);
	expect(loadedRowsShown()).toBe(true);
	expect(hasRow(tree, "Write tests")).toBe(true);
	act(() => tree.unmount());
});

it("lets a slow retry finish instead of starting another over it", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	let lastPageFails = true;
	let holdLive = false;
	const fake = hub(
		{ ...fleet, live: [[failing, working], [finished]] },
		(read) => holdLive && read.section === "live",
		(read) => lastPageFails && read.section === "live" && read.offset === 2,
	);
	connect(id, fake.client, "ready");
	const tree = await mount(navigation());
	await layOut(tree);
	expect(liveReads(fake)).toEqual([0, 2]);
	lastPageFails = false;
	holdLive = true;
	await advance(1000);
	expect(liveReads(fake)).toEqual([0, 2, 0]);
	// The retry's read is still out: later backoff steps start nothing.
	await advance(60_000);
	expect(liveReads(fake)).toEqual([0, 2, 0]);
	holdLive = false;
	fake.release();
	await settle();
	await layOut(tree);
	expect(liveReads(fake)).toEqual([0, 2, 0, 2]);
	expect(hasRow(tree, "Ship it")).toBe(true);
	act(() => tree.unmount());
});

it("starts the backoff over once a Live read succeeds", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	let firstPageFails = true;
	let secondPageFails = false;
	const fake = hub(
		{ ...fleet, live: [[failing, working], [finished]] },
		undefined,
		(read) => read.section === "live" && ((read.offset ?? 0) === 0 ? firstPageFails : secondPageFails),
	);
	connect(id, fake.client, "ready");
	const tree = await mount(navigation());
	expect(texts(tree)).toContain(FIRST_READ_FAILED);
	firstPageFails = false;
	await advance(1000);
	expect(liveReads(fake)).toEqual([0, 0]);
	expect(hasRow(tree, "Build docs")).toBe(true);
	secondPageFails = true;
	await layOut(tree);
	expect(liveReads(fake)).toEqual([0, 0, 2]);
	// A second failure in a row would wait two seconds; this one waits one.
	await advance(999);
	expect(liveReads(fake)).toEqual([0, 0, 2]);
	await advance(1);
	expect(liveReads(fake)).toEqual([0, 0, 2, 0]);
	act(() => tree.unmount());
});

it("keeps its backoff when a blur cancels a retry's read", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	let live: "fail" | "hold" = "fail";
	const fake = hub(
		fleet,
		(read) => live === "hold" && read.section === "live",
		(read) => live === "fail" && read.section === "live",
	);
	connect(id, fake.client, "ready");
	const tree = await mount(navigation());
	expect(texts(tree)).toContain(FIRST_READ_FAILED);
	live = "hold";
	await advance(1000);
	expect(liveReads(fake)).toEqual([0, 0]);
	// Leaving the Board pauses it, which cancels the retry's read.
	setFocused(false);
	await settle();
	live = "fail";
	setFocused(true);
	await settle();
	expect(liveReads(fake)).toEqual([0, 0, 0]);
	// Two failures in a row: the next retry waits two seconds, not one.
	await advance(1999);
	expect(liveReads(fake)).toEqual([0, 0, 0]);
	await advance(1);
	expect(liveReads(fake)).toEqual([0, 0, 0, 0]);
	act(() => tree.unmount());
});

it("shows neither skeleton rows nor the failed-read sentence beside Update needed before anything loaded", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	connect(id, null, "closed", { fatal: true });
	const tree = await mount(navigation());
	expect(texts(tree)).toContain(INCOMPATIBLE_TEXT);
	expect(skeletonRows(tree)).toHaveLength(0);
	expect(texts(tree)).not.toContain(FIRST_READ_FAILED);
	act(() => tree.unmount());
});

it("keeps reading Live's pages while they don't fill the screen, without any scrolling", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	const fake = hub({ ...fleet, live: [[failing, working], [finished], [idleOne, idleTwo]] });
	connect(id, fake.client, "ready");
	const tree = await mount(navigation());
	expect(liveReads(fake)).toEqual([0]);
	// A 700pt viewport over a 300pt Live block, reported by layout alone.
	await layOut(tree);
	expect(liveReads(fake)).toEqual([0, 2]);
	await layOut(tree);
	expect(liveReads(fake)).toEqual([0, 2, 3]);
	expect(hasRow(tree, "Ship it")).toBe(true);
	// Remaining is 0: nothing more to read.
	await layOut(tree);
	expect(liveReads(fake)).toEqual([0, 2, 3]);
	act(() => tree.unmount());
});

it("reads no more of Live while search results fill the scroller", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	const fake = hub({ ...fleet, live: [[failing, working], [finished]] });
	connect(id, fake.client, "ready");
	const tree = await mount(navigation());
	act(() =>
		tree.root
			.find((node) => node.props.testID === "live-block")
			.props.onLayout({ nativeEvent: { layout: { x: 0, y: 52, width: 390, height: 300 } } }),
	);
	await settle();
	expect(liveReads(fake)).toEqual([0]);
	searchField(tree).focus();
	act(() =>
		boardScroller(tree).props.onScroll({
			nativeEvent: { contentOffset: { x: 0, y: 900 }, layoutMeasurement: { width: 390, height: 700 } },
		}),
	);
	await settle();
	expect(liveReads(fake)).toEqual([0]);
	act(() => tree.unmount());
});

it("reads nothing while blurred, and on refocus catches up and re-reads drafts", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	const fake = hub(fleet);
	connect(id, fake.client, "ready");
	const tree = await mount(navigation());
	const manifestReads = () => fake.requests.filter((read) => read.resource === "manifest").length;
	expect(manifestReads()).toBe(1);
	expect(draftTags(rowTitled(tree, "Build docs"))).toHaveLength(0);
	setFocused(false);
	act(() => fake.invalidate(1, [{ kind: "manifest", revision: 2 }]));
	await settle();
	expect(manifestReads()).toBe(1);
	harness.drafts.set(id, new Set(["local:work"]));
	setFocused(true);
	await settle();
	expect(manifestReads()).toBe(2);
	expect(draftTags(rowTitled(tree, "Build docs"))).toHaveLength(1);
	act(() => tree.unmount());
});

const sheetOverBoard = {
	index: 1,
	routes: [
		{ key: "Sessions", name: "Sessions" },
		{ key: "tasks", name: "TasksSheet" },
	],
};
const screenOverBoard = {
	index: 1,
	routes: [
		{ key: "Sessions", name: "Sessions" },
		{ key: "conversation", name: "Conversation" },
	],
};

it("polls activity while the Board is in front and connected, a sheet over it included, and stops otherwise", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	const fake = hub({ ...fleet, activity: [] });
	connect(id, fake.client, "ready");
	const nav = navigation();
	const tree = await mount(nav);
	// The Board reads every live session: no refs.
	expect(fake.activityReads).toEqual([{}]);
	await advance(ACTIVITY_POLL_MS);
	expect(fake.activityReads).toHaveLength(2);

	harness.stack = sheetOverBoard;
	setFocused(false);
	await advance(ACTIVITY_POLL_MS);
	expect(fake.activityReads).toHaveLength(3);

	harness.stack = screenOverBoard;
	rerender(tree, nav);
	await advance(ACTIVITY_POLL_MS * 3);
	expect(fake.activityReads).toHaveLength(3);

	harness.stack = { index: 0, routes: [{ key: "Sessions", name: "Sessions" }] };
	setFocused(true);
	expect(fake.activityReads).toHaveLength(4);

	connect(id, fake.client, "reconnecting");
	rerender(tree, nav);
	await advance(ACTIVITY_POLL_MS * 3);
	expect(fake.activityReads).toHaveLength(4);
	connect(id, fake.client, "ready");
	rerender(tree, nav);
	expect(fake.activityReads).toHaveLength(5);

	act(() => tree.unmount());
	await advance(ACTIVITY_POLL_MS * 3);
	expect(fake.activityReads).toHaveLength(5);
});

const migrating = session("local:migrate", { title: "Migrate schema", state: "active", updated_at: minutesAgo(1) });
const tidying = session("local:tidy", { title: "Tidy imports", state: "active", updated_at: minutesAgo(1) });
const busyFleet: Fleet = {
	...fleet,
	live: [[failing, { ...working, subagents: { running: 1, failed: 0, done: 0 } }, tidying, migrating, finished]],
};
const workingTitles = (tree: ReactTestRenderer) =>
	tree.root
		.findAll((node) => ["Build docs", "Tidy imports", "Migrate schema"].some((title) => isRowTitled(title)(node)))
		.map((node) => node.props.accessibilityLabel.split(", ")[0]);
const meterIn = (row: ReactTestInstance) => row.findByType(PulseMeter);
/** Migrate schema's activity read, quiet for `quietMinutes` when it lands. */
const migrateRead = (quietMinutes: number): SessionActivity => ({
	ref: "local:migrate",
	minutes: [0, 0, 0],
	runningSubagents: 0,
	quietForMs: quietMinutes * MINUTE,
});

it("shows each working row's activity read: its meter, the hub's subagent tally, Quiet, and May be stuck first", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	const shape: Fleet = {
		...busyFleet,
		activity: [
			{ ref: "local:work", minutes: [2, 4, 8], runningSubagents: 3 },
			{ ref: "local:tidy", minutes: [1, 0, 0], runningSubagents: 0, quietForMs: 4 * MINUTE },
			{ ref: "local:migrate", minutes: [0, 0, 0], runningSubagents: 0, quietForMs: 11 * MINUTE },
		],
	};
	const fake = hub(shape);
	connect(id, fake.client, "ready");
	const tree = await mount(navigation());
	expect(workingTitles(tree)).toEqual(["Migrate schema", "Build docs", "Tidy imports"]);
	const building = rowTitled(tree, "Build docs");
	expect(meterIn(building).props.perMinute).toEqual([2, 4, 8]);
	expect(textsIn(building)).toContain("Waiting on 3 subagents");
	expect(textsIn(building)).not.toContain("Waiting on 1 subagent");
	expect(textsIn(rowTitled(tree, "Tidy imports"))).toContain("Quiet 4m");
	expect(textsIn(rowTitled(tree, "Migrate schema"))).toContain("May be stuck · no updates for 11m");

	// A failed poll inside STALE_AFTER_MS keeps the last read (a longer run
	// of failures has its own test).
	shape.activity = null;
	await advance(STALE_AFTER_MS - 1_000);
	expect(textsIn(rowTitled(tree, "Tidy imports"))).toContain("Quiet 4m");
	expect(meterIn(rowTitled(tree, "Build docs")).props.perMinute).toEqual([2, 4, 8]);
	act(() => tree.unmount());
});

it("shows no stuck label or reordering from a stale read while offline (Jesse's ruling)", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	const shape: Fleet = { ...busyFleet, activity: [migrateRead(3)] };
	const fake = hub(shape);
	connect(id, fake.client, "ready");
	const nav = navigation();
	const tree = await mount(nav);
	expect(textsIn(rowTitled(tree, "Migrate schema"))).toContain("Quiet 3m");
	expect(workingTitles(tree)).toEqual(["Build docs", "Tidy imports", "Migrate schema"]);

	// Offline: polling stops, but msSinceRead would otherwise keep counting
	// from the last read - 3m (at read) plus 10m elapsed would cross the
	// stuck threshold if it were trusted while disconnected, when really it's
	// the connection that's quiet, not the session.
	connect(id, fake.client, "reconnecting");
	rerender(tree, nav);
	await advance(10 * MINUTE);
	expect(textsIn(rowTitled(tree, "Migrate schema"))).not.toContain("May be stuck · no updates for 13m");
	expect(textsIn(rowTitled(tree, "Migrate schema"))).toContain("Working");
	expect(meterIn(rowTitled(tree, "Migrate schema")).props.perMinute).toBeUndefined();
	expect(workingTitles(tree)).toEqual(["Build docs", "Tidy imports", "Migrate schema"]);
	act(() => tree.unmount());
});

it("falls back once the last successful read goes stale, even while the connection reports ready (Jesse's ruling)", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	const shape: Fleet = { ...busyFleet, activity: [migrateRead(4)] };
	const fake = hub(shape);
	connect(id, fake.client, "ready");
	const tree = await mount(navigation());
	expect(textsIn(rowTitled(tree, "Migrate schema"))).toContain("Quiet 4m");

	// The connection reports "ready" throughout - reads simply stop landing,
	// the failure mode a bare connected check can't catch (a hub that has
	// gone quiet, not a client that knows it's disconnected).
	shape.activity = null;
	await advance(STALE_AFTER_MS + 1_000);
	expect(textsIn(rowTitled(tree, "Migrate schema"))).not.toContain("Quiet 4m");
	expect(textsIn(rowTitled(tree, "Migrate schema"))).toContain("Working");
	expect(meterIn(rowTitled(tree, "Migrate schema")).props.perMinute).toBeUndefined();
	expect(workingTitles(tree)).toEqual(["Build docs", "Tidy imports", "Migrate schema"]);
	act(() => tree.unmount());
});

it("keeps the fallback after a reconnect until a new read actually lands", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	const shape: Fleet = { ...busyFleet, activity: [migrateRead(4)] };
	const fake = hub(shape);
	connect(id, fake.client, "ready");
	const nav = navigation();
	const tree = await mount(nav);
	expect(textsIn(rowTitled(tree, "Migrate schema"))).toContain("Quiet 4m");

	// Long enough offline that the cached read is provably stale by the time
	// the connection returns.
	connect(id, fake.client, "reconnecting");
	rerender(tree, nav);
	await advance(STALE_AFTER_MS * 2);

	// The reconnect polls at once, and the fake answers from shape.activity
	// as the request goes out, so the new read is set first. Until it lands,
	// the stale read stays unused.
	shape.activity = [migrateRead(3)];
	connect(id, fake.client, "ready");
	rerender(tree, nav);
	expect(textsIn(rowTitled(tree, "Migrate schema"))).not.toContain("Quiet 4m");
	expect(textsIn(rowTitled(tree, "Migrate schema"))).toContain("Working");

	await settle();
	expect(textsIn(rowTitled(tree, "Migrate schema"))).toContain("Quiet 3m");
	act(() => tree.unmount());
});

it("stays out of Working's stuck slot for as long as reads keep failing, not just at the moment staleness is first crossed", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	const shape: Fleet = { ...busyFleet, activity: [migrateRead(11)] };
	const fake = hub(shape);
	connect(id, fake.client, "ready");
	const tree = await mount(navigation());
	expect(textsIn(rowTitled(tree, "Migrate schema"))).toContain("May be stuck · no updates for 11m");
	expect(workingTitles(tree)).toEqual(["Migrate schema", "Build docs", "Tidy imports"]);

	shape.activity = null;
	await advance(STALE_AFTER_MS + 1_000);
	expect(workingTitles(tree)).toEqual(["Build docs", "Tidy imports", "Migrate schema"]);
	// Several more poll cycles of continued failure: still no stuck label,
	// still in hub order - not just true for a moment right at the threshold.
	await advance(ACTIVITY_POLL_MS * 5);
	expect(textsIn(rowTitled(tree, "Migrate schema"))).toContain("Working");
	expect(workingTitles(tree)).toEqual(["Build docs", "Tidy imports", "Migrate schema"]);
	// With no fresh read on screen there is nothing to recheck: only the
	// row-age ticker and the activity poll itself are left.
	expect(vi.getTimerCount()).toBe(2);
	act(() => tree.unmount());
});

it("keeps a row's Working order in step with its label when elapsed time alone crosses the stuck threshold", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	const shape: Fleet = {
		...busyFleet,
		activity: [
			{ ref: "local:migrate", minutes: [0, 0, 0], runningSubagents: 0, quietForMs: STUCK_AFTER_MS - ACTIVITY_POLL_MS },
		],
	};
	const fake = hub(shape);
	connect(id, fake.client, "ready");
	const tree = await mount(navigation());
	expect(textsIn(rowTitled(tree, "Migrate schema"))).toContain("Quiet 9m");
	expect(workingTitles(tree)).toEqual(["Build docs", "Tidy imports", "Migrate schema"]);

	// The next poll times out, so nothing refreshes the read: the row crosses
	// into stuck from elapsed time on the read already on screen, one poll
	// interval later - still well under STALE_AFTER_MS, so it's trusted.
	shape.activity = null;
	await advance(ACTIVITY_POLL_MS);
	expect(textsIn(rowTitled(tree, "Migrate schema"))).toContain("May be stuck · no updates for 10m");
	// The label and the Working order must agree the moment the label turns,
	// not up to a poll interval later (the label reads msSinceRead live; the
	// sort order used to wait for the next successful read).
	expect(workingTitles(tree)).toEqual(["Migrate schema", "Build docs", "Tidy imports"]);
	act(() => tree.unmount());
});

it("still lets a read on screen go stale once the hub stops answering activity reads", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	const shape: Fleet = { ...busyFleet, activity: [migrateRead(4)] };
	const fake = hub(shape);
	connect(id, fake.client, "ready");
	const tree = await mount(navigation());

	// A hub rolled back to before S5 answers method-not-found: the poll stops
	// for good at its next attempt, with its last read still fresh on screen.
	shape.activity = undefined;
	await advance(ACTIVITY_POLL_MS);
	expect(textsIn(rowTitled(tree, "Migrate schema"))).toContain("Quiet 4m");

	await advance(ACTIVITY_POLL_MS + 1_000);
	expect(fake.activityReads).toHaveLength(2);
	expect(textsIn(rowTitled(tree, "Migrate schema"))).not.toContain("Quiet 4m");
	expect(textsIn(rowTitled(tree, "Migrate schema"))).toContain("Working");
	act(() => tree.unmount());
});

it("keeps every working row as it was before S5 on a hub that has no activity read, and stops asking", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	const fake = hub(busyFleet);
	connect(id, fake.client, "ready");
	const tree = await mount(navigation());
	expect(workingTitles(tree)).toEqual(["Build docs", "Tidy imports", "Migrate schema"]);
	const building = rowTitled(tree, "Build docs");
	expect(meterIn(building).props.perMinute).toBeUndefined();
	expect(textsIn(building)).toContain("Waiting on 1 subagent");
	expect(textsIn(rowTitled(tree, "Tidy imports"))).toContain("Working");
	// With no read to go stale, nothing rechecks one either: an old hub never
	// gets the Board re-rendered every ACTIVITY_POLL_MS. Only the row-age
	// ticker is left.
	expect(vi.getTimerCount()).toBe(1);
	await advance(ACTIVITY_POLL_MS * 3);
	expect(fake.activityReads).toHaveLength(1);
	act(() => tree.unmount());
});

const fleetMeter = (tree: ReactTestRenderer) =>
	tree.root.find((node) => node.props.testID === "live-summary").findByType(PulseMeter);

it("sums the working sessions' activity into the summary's meter, bar by bar", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	const shape: Fleet = {
		...busyFleet,
		activity: [
			{ ref: "local:work", minutes: [2, 4, 8], runningSubagents: 3 },
			{ ref: "local:tidy", minutes: [1, 0, 0], runningSubagents: 0 },
			// A session that isn't working adds nothing to the fleet meter.
			{ ref: "local:done", minutes: [50, 50, 50], runningSubagents: 0 },
		],
	};
	connect(id, hub(shape).client, "ready");
	const tree = await mount(navigation());
	// Migrate schema has no read yet: the meter sums the sessions that do.
	expect(fleetMeter(tree).props.perMinute).toEqual([0, 0, 0, 0, 3, 4, 8]);
	act(() => tree.unmount());
});

it("keeps the summary's meter still until a working session has an activity read", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	connect(id, hub(busyFleet).client, "ready");
	const tree = await mount(navigation());
	expect(fleetMeter(tree).props.perMinute).toBeUndefined();
	act(() => tree.unmount());
});

// The hub's notices (S11), as evener/notices/list carries them.
const signInNotice: HubNotice = { id: "signInRequired:openai", kind: "signInRequired", subject: "openai" };
const hostNotice: HubNotice = {
	id: "hostOffline:studio",
	kind: "hostOffline",
	subject: "studio",
	affectedSessions: 2,
};
const pluginNotice: HubNotice = {
	id: "pluginBroken:superpowers@evener",
	kind: "pluginBroken",
	subject: "superpowers",
	marketplace: "evener",
};
const noticeTexts = (tree: ReactTestRenderer) =>
	tree.root.findAll((node) => node.props.testID === "notice").map(joinedText);
/** A fleet with a host offline: one of its sessions is in Live and Needs you
 * both, and one only in Live. */
const troubledFleet = (): Fleet => {
	const stuck = session("studio:stuck", { host_id: "studio", title: "Stuck", state: "errored" });
	const quiet = session("studio:quiet", { host_id: "studio", title: "Quiet" });
	return {
		...fleet,
		live: [[failing, working, stuck, quiet]],
		needsYou: [failing, stuck],
		manifest: manifest({
			sources: [
				{ id: "local", label: "Laptop", kind: "local", online: true },
				{ id: "studio", label: "Studio Mac", kind: "ssh", online: false },
			],
			sections: { live: { count: 4 }, needs_you: { count: 2 }, pin_sections: { count: 2 } },
			catalogs: { projects: { count: 4 }, archived_projects: { count: 0 }, test_runs: { count: 0 } },
		}),
		notices: [signInNotice, hostNotice, pluginNotice],
	};
};

it("shows the hub's notices under the chips, above Live, after Update needed, and opens each one's screen", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	connect(id, hub(troubledFleet()).client, "ready");
	const nav = navigation();
	const tree = await mount(nav);
	expect(noticeTexts(tree)).toEqual([
		"openai sign-in expiredSign in",
		"Studio Mac is offline · 2\u00a0sessionsDetails",
		"superpowers is brokenPlugins",
	]);
	// The notices sit in the scroller, before the Live block.
	const order = boardScroller(tree).findAll(
		(node) => node.props.testID === "notice" || node.props.testID === "live-block",
	);
	expect(order.map((node) => node.props.testID)).toEqual(["notice", "notice", "notice", "live-block"]);
	pressLabel(tree, "Sign in, openai sign-in expired");
	expect(nav.navigate).toHaveBeenLastCalledWith("Hub", {
		screen: "Providers",
		params: { hubId: id, focus: "openai", signIn: true },
		initial: false,
	});
	pressLabel(tree, "Details, Studio Mac is offline · 2\u00a0sessions");
	expect(nav.navigate).toHaveBeenLastCalledWith("Hub", {
		screen: "Hosts",
		params: { hubId: id, focus: "studio" },
		initial: false,
	});
	pressLabel(tree, "Plugins, superpowers is broken");
	expect(nav.navigate).toHaveBeenLastCalledWith("Hub", {
		screen: "Plugins",
		params: { hubId: id, focus: { plugin: "superpowers", marketplace: "evener" } },
		initial: false,
	});
	// Update needed comes first.
	connect(id, null, "closed", { fatal: true });
	rerender(tree, nav);
	expect(noticeTexts(tree)[0]).toBe(INCOMPATIBLE_TEXT);
	expect(noticeTexts(tree)).toHaveLength(4);
	act(() => tree.unmount());
});

it("hides notice actions while the hub is out of reach, keeping the notice rows", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	connect(id, hub(troubledFleet()).client, "ready");
	const nav = navigation();
	const tree = await mount(nav);
	expect(noticeTexts(tree)).toContain("openai sign-in expiredSign in");
	// Ruling 21: the Board's hub-facing actions show only while connected.
	connect(id, null, "closed");
	rerender(tree, nav);
	expect(noticeTexts(tree)).toEqual([
		"openai sign-in expired",
		"Studio Mac is offline · 2\u00a0sessions",
		"superpowers is broken",
	]);
	act(() => tree.unmount());
});

it("drops a notice once evener/notices/changed leaves it out, reading nothing for it", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	const fake = hub(troubledFleet());
	connect(id, fake.client, "ready");
	const tree = await mount(navigation());
	expect(noticeTexts(tree)).toContain("openai sign-in expiredSign in");
	act(() => fake.noticesChanged([hostNotice, pluginNotice]));
	await settle();
	expect(noticeTexts(tree)).toEqual(["Studio Mac is offline · 2\u00a0sessionsDetails", "superpowers is brokenPlugins"]);
	expect(fake.noticeReads).toHaveLength(1);
	act(() => tree.unmount());
});

it("reads the notices again when the Board comes back into view, and not while blurred", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	const fake = hub(troubledFleet());
	connect(id, fake.client, "ready");
	const tree = await mount(navigation());
	expect(fake.noticeReads).toHaveLength(1);
	setFocused(false);
	await advance(15 * 60_000);
	expect(fake.noticeReads).toHaveLength(1);
	setFocused(true);
	await settle();
	expect(fake.noticeReads).toHaveLength(2);
	act(() => tree.unmount());
});

it("shows no notices and no error on a hub without evener/notices/list", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	const { notices: _none, ...older } = troubledFleet();
	const fake = hub(older);
	connect(id, fake.client, "ready");
	const tree = await mount(navigation());
	expect(fake.noticeReads).toHaveLength(1);
	expect(noticeTexts(tree)).toEqual([]);
	expect(texts(tree)).not.toContain(FIRST_READ_FAILED);
	act(() => tree.unmount());
});

it("says nothing when the notice read fails, and doesn't retry the Board over it", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	const fake = hub({ ...troubledFleet(), noticesFail: true });
	connect(id, fake.client, "ready");
	const tree = await mount(navigation());
	expect(noticeTexts(tree)).toEqual([]);
	expect(texts(tree)).not.toContain(FIRST_READ_FAILED);
	const reads = fake.requests.length;
	await advance(60_000);
	expect(fake.requests).toHaveLength(reads);
	act(() => tree.unmount());
});

// Projects, Test runs and Archived (spec 7.1).
const laptopSource = { id: "local", label: "Laptop", kind: "local", online: true };
const parkSource = { id: "paradise-park", label: "paradise-park", kind: "appwire", online: false };
const catalogCounts = (projects: number, archived: number, testRuns: number) => ({
	projects: { count: projects },
	archived_projects: { count: archived },
	test_runs: { count: testRuns },
});
const fleetSections = { live: { count: 5 }, needs_you: { count: 2 }, pin_sections: { count: 2 } };
const twoHosts = (counts = catalogCounts(1, 0, 0)) =>
	manifest({ sources: [laptopSource, parkSource], sections: fleetSections, catalogs: counts });
const evenerProject = (over: Partial<NavigationProjectSummary> = {}): NavigationProjectSummary => ({
	key: "evener",
	name: "evener",
	working_dir: "/home/jesse/git/evener",
	session_count: 2,
	...over,
});
const localWork = session("local:lw", { title: "Local work", live: false, updated_at: minutesAgo(30) });
const parkWork = session("paradise-park:pw", {
	title: "Park work",
	host_id: "paradise-park",
	live: false,
	updated_at: minutesAgo(40),
});
/** The Projects, Test runs and Archived headers, in screen order. */
const sectionHeaders = (tree: ReactTestRenderer) =>
	tree.root.findAll((node) => node.props.testID === "project-section-header").map(joinedText);
const organizeControls = (tree: ReactTestRenderer) =>
	tree.root.findAll(
		(node) =>
			node.type === ("Pressable" as never) &&
			typeof node.props.accessibilityLabel === "string" &&
			node.props.accessibilityLabel.startsWith("Organize by: "),
	);
const projectSection = (tree: ReactTestRenderer, section: string) =>
	tree.root.find((node) => node.type === ("View" as never) && node.props.testID === `project-section:${section}`);
/** The project row with this accessibility label, its first copy when hosts come first. */
const projectRows = (tree: ReactTestRenderer, label: string) =>
	tree.root.findAll(
		(node) =>
			node.type === ("Pressable" as never) &&
			node.props.testID === "project-row" &&
			node.props.accessibilityLabel === label,
	);
const catalogReads = (fake: ReturnType<typeof hub>) =>
	fake.requests.filter((read) => read.resource === "catalog").map((read) => read.catalog);
const pageReads = (fake: ReturnType<typeof hub>) =>
	fake.requests.filter((read) => read.resource === "project_page").map((read) => `${read.tier}@${read.offset ?? 0}`);
const rowOpacity = (node: ReactTestInstance) =>
	(typeof node.props.style === "function" ? node.props.style({ pressed: false }) : node.props.style).opacity ?? 1;

it("offers Organize by only once the hub names a second host, and saves the choice", async () => {
	const single = hubId();
	adoptedAnHourAgo(single);
	connect(single, hub({ ...fleet, catalogs: { projects: [evenerProject()] } }).client, "ready");
	const alone = await mount(navigation());
	expect(sectionHeaders(alone)).toEqual(["PROJECTS"]);
	expect(organizeControls(alone)).toEqual([]);
	act(() => alone.unmount());

	const id = hubId();
	adoptedAnHourAgo(id);
	connect(id, hub({ ...fleet, manifest: twoHosts(), catalogs: { projects: [evenerProject()] } }).client, "ready");
	const tree = await mount(navigation());
	expect(sectionHeaders(tree)).toEqual(["PROJECTS"]);
	const [control] = organizeControls(tree);
	expect(control.props.accessibilityLabel).toBe("Organize by: Project, then host");
	expect(control.props.accessibilityHint).toBe("Changes to Host, then project");
	expect(textsIn(control)).toEqual(["Project, then host"]);
	expect(control.findAll((node) => node.type === ("SymbolView" as never)).map((node) => node.props.name)).toEqual([
		"arrow.left.arrow.right",
	]);
	act(() => control.props.onPress());
	expect(sectionHeaders(tree)).toEqual(["HOSTS"]);
	expect(harness.kv.get(`evener.native.board-organize.${id}`)).toBe(JSON.stringify("host-project"));
	const [flipped] = organizeControls(tree);
	expect(flipped.props.accessibilityLabel).toBe("Organize by: Host, then project");
	expect(flipped.props.accessibilityHint).toBe("Changes to Project, then host");
	act(() => tree.unmount());
});

it("organized by host, shows each session of a shared project once, under its own host", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	harness.kv.set(`evener.native.board-organize.${id}`, JSON.stringify("host-project"));
	const shared = evenerProject({ sources: ["local", "paradise-park"], default_expanded: true });
	connect(
		id,
		hub({
			...fleet,
			manifest: twoHosts(),
			catalogs: { projects: [shared] },
			projectPages: { "evener:current": [localWork, parkWork] },
		}).client,
		"ready",
	);
	const tree = await mount(navigation());
	expect(tree.root.findAll(isRowTitled("Local work"))).toHaveLength(1);
	expect(tree.root.findAll(isRowTitled("Park work"))).toHaveLength(1);
	// Laptop counts its sessions in Live and Needs you, once each; the offline host says so.
	expect(textsIn(projectSection(tree, "projects"))).toEqual([
		"HOSTS",
		"Host, then project",
		"Laptop",
		"6 live",
		"evener",
		"Today",
		"Local work",
		"30m",
		"paradise-park",
		"Offline",
		"evener",
		"Today",
		"Park work",
		"40m",
	]);
	act(() => tree.unmount());
});

it("reads an unfolded project's pages once, reads nothing to fold it, and remembers the fold", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	const fake = hub({
		...fleet,
		catalogs: { projects: [evenerProject()] },
		projectPages: { "evener:current": [localWork] },
	});
	connect(id, fake.client, "ready");
	const nav = navigation();
	const tree = await mount(nav);
	expect(pageReads(fake)).toEqual([]);
	expect(projectRows(tree, "evener")[0].props.accessibilityState).toEqual({ expanded: false });
	pressLabel(tree, "evener");
	await settle();
	expect(pageReads(fake).sort()).toEqual(["archived@0", "current@0", "recent@0"]);
	expect(hasRow(tree, "Local work")).toBe(true);
	pressLabel(tree, "evener");
	await settle();
	expect(hasRow(tree, "Local work")).toBe(false);
	pressLabel(tree, "evener");
	await settle();
	expect(pageReads(fake)).toHaveLength(3);
	expect(hasRow(tree, "Local work")).toBe(true);
	act(() => tree.unmount());
	expect(JSON.parse(harness.kv.get(`evener.native.board-sections.${id}`) ?? "null")).toMatchObject({
		"project:evener": false,
	});
	const again = await mount(nav);
	expect(projectRows(again, "evener")[0].props.accessibilityState).toEqual({ expanded: true });
	expect(hasRow(again, "Local work")).toBe(true);
	act(() => again.unmount());
});

it("classifies a project's session rows by the hub's seen marker too (S4)", async () => {
	const id = hubId();
	deviceDisagrees(id);
	const fake = hub({
		...hubFleet,
		catalogs: { projects: [evenerProject()] },
		projectPages: {
			"evener:current": [
				// The device holds an unread mark for this ref; the hub says seen.
				session("local:hub-seen", {
					title: "Project hub seen",
					live: false,
					updated_at: minutesAgo(6),
					turn_ended_at: minutesAgo(6),
					unseen: false,
				}),
				// The device's epoch covers this one; the hub says unseen.
				session("local:project-unseen", {
					title: "Project hub unseen",
					live: false,
					updated_at: minutesAgo(90),
					turn_ended_at: minutesAgo(90),
					unseen: true,
				}),
			],
		},
	});
	connect(id, fake.client, "ready");
	const tree = await mount(navigation());
	pressLabel(tree, "evener");
	await settle();
	expect(stateOf(tree, "Project hub seen")).toBe("Idle");
	expect(stateOf(tree, "Project hub unseen")).toBe("Finished");
	act(() => tree.unmount());
});

it("drops a project row's pending mark once the project's page shows it landed (S4)", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	const projectUnseen = session("local:project-unseen", {
		title: "Project hub unseen",
		live: false,
		updated_at: minutesAgo(90),
		turn_ended_at: minutesAgo(90),
		unseen: true,
	});
	const shape: Fleet = {
		...hubFleet,
		catalogs: { projects: [evenerProject()] },
		projectPages: { "evener:current": [projectUnseen] },
	};
	const fake = hub(shape);
	connect(id, fake.client, "ready");
	const tree = await mount(navigation());
	pressLabel(tree, "evener");
	await settle();
	act(() => rowTitled(tree, "Project hub unseen").props.onPress());
	await settle();
	expect(fake.seen).toEqual([[{ ref: "local:project-unseen", seenThrough: Date.parse(minutesAgo(90)) }]]);
	expect(stateOf(tree, "Project hub unseen")).toBe("Idle");
	// The project's page catches up, and the pending mark goes: a later
	// unseen for the same turn would show again. This fake hub answers every
	// read at revision 1, so the change names none.
	shape.projectPages = { "evener:current": [{ ...projectUnseen, unseen: false }] };
	act(() => fake.invalidate(1, [{ kind: "project", projectKey: "evener" }]));
	await settle();
	expect(fake.requests.filter((read) => read.resource === "project_page" && read.tier === "current")).toHaveLength(2);
	expect(hubSeenMarks(id).isSeenOnHub(projectUnseen)).toBe(false);
	act(() => tree.unmount());
});

/** Opens search, types a query that finds the evener project, and taps it. */
async function revealFromSearch(tree: ReactTestRenderer, query = "even") {
	const bar = searchField(tree);
	bar.focus();
	await bar.type(query);
	const [result] = tree.root.findAll((node) => node.props.testID === "project-result");
	expect(result.props.accessibilityLabel).toBe("evener, project, /home/jesse/git/evener");
	act(() => result.props.onPress());
}
const layOutAt = (node: ReactTestInstance, y: number, height: number) =>
	act(() => node.props.onLayout({ nativeEvent: { layout: { x: 0, y, width: 390, height } } }));
const revealTarget = (tree: ReactTestRenderer) => tree.root.find((node) => node.props.testID === "project-reveal");

it("opens a project from search: leaves search, unfolds the project and scrolls it a third of the way down", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	const fake = hub({
		...fleet,
		catalogs: { projects: [evenerProject()] },
		projectPages: { "evener:current": [localWork] },
	});
	connect(id, fake.client, "ready");
	const { tree, scrollTo } = await mountWithInstances(navigation());
	layOutAt(boardScroller(tree), 0, 600);
	expect(projectRows(tree, "evener")[0].props.accessibilityState).toEqual({ expanded: false });
	await revealFromSearch(tree);
	// Search is gone, and the query counts as a recent search.
	expect(hasCancel(tree)).toBe(false);
	expect(tree.root.findAll((node) => node.props.testID === "search-result")).toHaveLength(0);
	expect(JSON.parse(harness.kv.get(`evener.native.recent-searches.${id}`) ?? "[]")).toEqual(["even"]);
	expect(projectRows(tree, "evener")[0].props.accessibilityState).toEqual({ expanded: true });
	await settle();
	expect(hasRow(tree, "Local work")).toBe(true);
	// It scrolls once both the project row and its section have laid out, in
	// either order: the row sits 30% of the way down the viewport.
	const calls = scrollTo.mock.calls.length;
	layOutAt(revealTarget(tree), 60, 48);
	expect(scrollTo.mock.calls.length).toBe(calls);
	layOutAt(projectSection(tree, "projects"), 900, 400);
	expect(scrollTo).toHaveBeenLastCalledWith({ y: 900 + 60 - 0.3 * (600 - 48), animated: true });
	// Once there, the reveal is done.
	expect(tree.root.findAll((node) => node.props.testID === "project-reveal")).toHaveLength(0);
	act(() => tree.unmount());
});

it("scrolls to a project from search that was already unfolded", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	const project = evenerProject({ default_expanded: true });
	connect(id, hub({ ...fleet, catalogs: { projects: [project] } }).client, "ready");
	const { tree, scrollTo } = await mountWithInstances(navigation());
	layOutAt(boardScroller(tree), 0, 600);
	expect(projectRows(tree, "evener")[0].props.accessibilityState).toEqual({ expanded: true });
	await revealFromSearch(tree);
	// Leaving search mounts the sections afresh, so both layouts arrive.
	layOutAt(projectSection(tree, "projects"), 900, 400);
	layOutAt(revealTarget(tree), 60, 48);
	expect(scrollTo).toHaveBeenLastCalledWith({ y: 900 + 60 - 0.3 * (600 - 48), animated: true });
	act(() => tree.unmount());
});

it("scrolls a project row taller than the viewport to its top, never past it", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	const fake = hub({
		...fleet,
		catalogs: { projects: [evenerProject()] },
		projectPages: { "evener:current": [localWork] },
	});
	connect(id, fake.client, "ready");
	const { tree, scrollTo } = await mountWithInstances(navigation());
	layOutAt(boardScroller(tree), 0, 600);
	await revealFromSearch(tree);
	layOutAt(projectSection(tree, "projects"), 900, 400);
	// The row is taller than the 600pt viewport, so there is nowhere to sit it
	// a third of the way down: the Board shows its top.
	layOutAt(revealTarget(tree), 60, 700);
	expect(scrollTo).toHaveBeenLastCalledWith({ y: 900 + 60, animated: true });
	act(() => tree.unmount());
});

it("stays in search when a tapped project is no longer in the loaded catalog", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	const fake = hub({ ...fleet, catalogs: { projects: [evenerProject()] } });
	connect(id, fake.client, "ready");
	const { tree } = await mountWithInstances(navigation());
	const bar = searchField(tree);
	bar.focus();
	await bar.type("even");
	// The catalog goes stale between the result's render and the tap: the
	// project is no longer loaded, so there is nothing to reveal.
	act(() => tree.root.findByType(SearchResults).props.onOpenProject({ key: "gone", name: "Gone" }));
	expect(hasCancel(tree)).toBe(true);
	expect(tree.root.findAll((node) => node.props.testID === "project-reveal")).toHaveLength(0);
	act(() => tree.unmount());
});

it("scrolls to a project from search without animating while Reduce Motion is on", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	harness.reduceMotion = true;
	try {
		connect(id, hub({ ...fleet, catalogs: { projects: [evenerProject()] } }).client, "ready");
		const { tree, scrollTo } = await mountWithInstances(navigation());
		layOutAt(boardScroller(tree), 0, 600);
		await revealFromSearch(tree);
		layOutAt(projectSection(tree, "projects"), 100, 400);
		layOutAt(revealTarget(tree), 40, 48);
		// Near the top, it scrolls no further up than the Board's start.
		expect(scrollTo).toHaveBeenLastCalledWith({ y: 0, animated: false });
		act(() => tree.unmount());
	} finally {
		harness.reduceMotion = false;
	}
});

it("opens a project from search inside its host when hosts come first, unfolding the way there", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	harness.kv.set(`evener.native.board-organize.${id}`, JSON.stringify("host-project"));
	// Its host is folded; the Projects section is open, so its catalog is read.
	harness.kv.set(`evener.native.board-sections.${id}`, JSON.stringify({ "host:local": true }));
	connect(id, hub({ ...fleet, manifest: twoHosts(), catalogs: { projects: [evenerProject()] } }).client, "ready");
	const { tree } = await mountWithInstances(navigation());
	await revealFromSearch(tree);
	expect(JSON.parse(harness.kv.get(`evener.native.board-sections.${id}`) ?? "{}")).toMatchObject({
		projects: false,
		"host:local": false,
		"project:evener@local": false,
	});
	expect(revealTarget(tree)).toBeTruthy();
	act(() => tree.unmount());
});

it("lists no projects in search that the Board hasn't loaded", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	connect(id, hub({ ...fleet, catalogs: { projects: [evenerProject()] } }).client, "ready");
	const tree = await mount(navigation());
	const bar = searchField(tree);
	bar.focus();
	await bar.type("nothing like it");
	expect(tree.root.findAll((node) => node.props.testID === "project-result")).toHaveLength(0);
	act(() => tree.unmount());
});

it("starts Test runs and Archived folded, reading neither catalog until it is unfolded", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	const fake = hub({
		...fleet,
		manifest: manifest({ sources: [laptopSource], sections: fleetSections, catalogs: catalogCounts(1, 271, 3) }),
		catalogs: {
			projects: [evenerProject()],
			test_runs: [{ key: "hub-test-env", name: "hub-test-env", session_count: 3 }],
			archived_projects: [evenerProject({ key: "old-site", name: "old-site", is_archived: true })],
		},
	});
	connect(id, fake.client, "ready");
	const tree = await mount(navigation());
	expect(sectionHeaders(tree)).toEqual(["PROJECTS", "Test runs · 3", "ARCHIVED · 271"]);
	expect(catalogReads(fake)).toEqual(["projects"]);
	expect(projectRows(tree, "hub-test-env")).toHaveLength(0);
	pressLabel(tree, "Test runs, 3 projects");
	await settle();
	expect(catalogReads(fake)).toEqual(["projects", "test_runs"]);
	expect(projectRows(tree, "hub-test-env")).toHaveLength(1);
	pressLabel(tree, "Archived, 271 projects");
	await settle();
	expect(catalogReads(fake)).toEqual(["projects", "test_runs", "archived_projects"]);
	expect(projectRows(tree, "old-site")).toHaveLength(1);
	// Folded again and reopened, a section reads nothing more.
	pressLabel(tree, "Test runs, 3 projects");
	pressLabel(tree, "Test runs, 3 projects");
	await settle();
	expect(catalogReads(fake)).toEqual(["projects", "test_runs", "archived_projects"]);
	act(() => tree.unmount());
});

it("shows no section while the manifest counts none, reading no catalog", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	const fake = hub({ ...fleet, manifest: manifest({ sources: [laptopSource], catalogs: catalogCounts(0, 0, 0) }) });
	connect(id, fake.client, "ready");
	const tree = await mount(navigation());
	expect(catalogReads(fake)).toEqual([]);
	expect(sectionHeaders(tree)).toEqual([]);
	act(() => tree.unmount());
});

it("shows no PROJECTS header before the manifest lands, and hides it once its catalog loads empty", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	let holding = true;
	const fake = hub(fleet, (read) => holding && (read.resource === "manifest" || read.section === "live"));
	connect(id, fake.client, "ready");
	const tree = await mount(navigation());
	expect(skeletonRows(tree)).toHaveLength(3);
	expect(sectionHeaders(tree)).toEqual([]);
	expect(catalogReads(fake)).toEqual([]);
	holding = false;
	fake.release();
	await settle();
	// The manifest counts 4 projects, but the catalog comes back empty.
	expect(catalogReads(fake)).toEqual(["projects"]);
	expect(sectionHeaders(tree)).toEqual([]);
	act(() => tree.unmount());
});

it("keeps every project row on screen from a dropped client until the new client's reads land", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	const shape = {
		...fleet,
		catalogs: { projects: [evenerProject({ default_expanded: true })] },
		projectPages: { "evener:current": [localWork] },
	};
	connect(id, hub(shape).client, "ready");
	const nav = navigation();
	const tree = await mount(nav);
	expect(hasRow(tree, "Local work")).toBe(true);
	connect(id, null, "reconnecting");
	rerender(tree, nav);
	await settle();
	expect(projectRows(tree, "evener")).toHaveLength(1);
	expect(hasRow(tree, "Local work")).toBe(true);
	let holding = true;
	const next = hub(shape, () => holding);
	connect(id, next.client, "ready");
	rerender(tree, nav);
	await settle();
	expect(catalogReads(next)).toEqual(["projects"]);
	expect(projectRows(tree, "evener")).toHaveLength(1);
	expect(hasRow(tree, "Local work")).toBe(true);
	holding = false;
	next.release();
	await settle();
	expect(pageReads(next).sort()).toEqual(["archived@0", "current@0", "recent@0"]);
	expect(hasRow(tree, "Local work")).toBe(true);
	act(() => tree.unmount());
});

it("pins a project to the top from its long-press menu, dimming it until the hub confirms", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	const fake = hub({ ...fleet, catalogs: { projects: [evenerProject()] } }, undefined, undefined, {
		holdChanges: true,
	});
	connect(id, fake.client, "ready");
	const tree = await mount(navigation());
	const [row] = projectRows(tree, "evener");
	expect(rowOpacity(row)).toBe(1);
	act(() => row.props.onLongPress());
	const [sheet, choose] = harness.actionSheet.mock.calls.at(-1) ?? [];
	expect(sheet).toEqual({
		title: "evener",
		options: ["Pin to top", "Archive project", "Cancel"],
		cancelButtonIndex: 2,
	});
	act(() => choose(0));
	await settle();
	expect(fake.mutations).toEqual([
		{ method: "evener/favorite/set", params: { kind: "project", id: "evener", favorited: true } },
	]);
	// While the change is out, the project dims and every organization action hides.
	expect(rowOpacity(projectRows(tree, "evener")[0])).toBe(0.5);
	expect(menuLabels(tree)).toEqual([]);
	fake.release();
	await settle();
	expect(rowOpacity(projectRows(tree, "evener")[0])).toBe(1);
	expect(menuLabels(tree)).toEqual(["Mine, category menu", "Empty, category menu"]);
	expect(fake.mutations).toHaveLength(1);
	act(() => tree.unmount());
});

it("reflects a project change held offline in its menu and on its row, and its opposite cancels it (phase 6 ruling 18)", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	const fake = hub({ ...fleet, catalogs: { projects: [evenerProject()] } });
	connect(id, fake.client, "ready");
	const nav = navigation();
	const tree = await mount(nav);
	connect(id, fake.client, "reconnecting");
	rerender(tree, nav);
	const openMenu = () => {
		act(() => projectRows(tree, "evener")[0]?.props.onLongPress());
		return harness.actionSheet.mock.calls.at(-1) ?? [];
	};
	const [sheet, choose] = openMenu();
	expect(sheet.options).toEqual(["Pin to top", "Archive project", "Cancel"]);
	act(() => choose(0));
	await settle();
	expect(rowOpacity(projectRows(tree, "evener")[0])).toBe(0.5);
	const [held, undo] = openMenu();
	expect(held.options).toEqual(["Unpin", "Archive project", "Cancel"]);
	act(() => undo(0));
	await settle();
	expect(rowOpacity(projectRows(tree, "evener")[0])).toBe(1);
	expect(JSON.parse(harness.kv.get(`evener.native.board-hold.${id}`) ?? "[]")).toEqual([]);
	connect(id, fake.client, "ready");
	rerender(tree, nav);
	await settle();
	expect(fake.mutations).toEqual([]);
	act(() => tree.unmount());
});

it("queues a project change behind the held one it answers, even once back online", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	const fake = hub({ ...fleet, catalogs: { projects: [evenerProject()] } });
	let answerPin: () => void = () => {};
	const request = fake.client.request;
	let favorites = 0;
	fake.client.request = ((method: string, params: unknown) => {
		// The held Pin to top's write hangs while it is on its way.
		if (method === "evener/favorite/set" && favorites++ === 0) {
			const answer = request(method as never, params as never);
			return new Promise((resolve) => {
				answerPin = () => resolve(answer as never);
			});
		}
		return request(method as never, params as never);
	}) as typeof request;
	connect(id, fake.client, "ready");
	const nav = navigation();
	const tree = await mount(nav);
	connect(id, fake.client, "reconnecting");
	rerender(tree, nav);
	const openMenu = () => {
		act(() => projectRows(tree, "evener")[0]?.props.onLongPress());
		return harness.actionSheet.mock.calls.at(-1) ?? [];
	};
	const [, pin] = openMenu();
	act(() => pin(0));
	await settle();
	// The menu, opened offline, offers Unpin, and stays up across the
	// reconnect while the held Pin to top goes out.
	const [sheet, unpin] = openMenu();
	expect(sheet.options?.[0]).toBe("Unpin");
	connect(id, fake.client, "ready");
	rerender(tree, nav);
	await vi.waitFor(() => expect(favorites).toBe(1));
	act(() => unpin(0));
	await settle();
	answerPin();
	await vi.waitFor(() =>
		expect(fake.mutations.filter((m) => m.method === "evener/favorite/set").map((m) => m.params)).toEqual([
			{ kind: "project", id: "evener", favorited: true },
			{ kind: "project", id: "evener", favorited: false },
		]),
	);
	await vi.waitFor(() => expect(JSON.parse(harness.kv.get(`evener.native.board-hold.${id}`) ?? "[]")).toEqual([]));
	act(() => tree.unmount());
});

it("holds a project change while the journal is busy, and sends it once the journal is free", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	const fake = hub({ ...fleet, catalogs: { projects: [evenerProject()] } });
	let answerFirst: () => void = () => {};
	let favorites = 0;
	const request = fake.client.request;
	fake.client.request = ((method: string, params: unknown) => {
		if (method !== "evener/favorite/set" || favorites++ > 0) return request(method as never, params as never);
		const answered = request(method as never, params as never);
		return new Promise((resolve) => {
			answerFirst = () => resolve(answered as never);
		});
	}) as typeof request;
	connect(id, fake.client, "ready");
	const tree = await mount(navigation());
	const openMenu = () => {
		act(() => projectRows(tree, "evener")[0]?.props.onLongPress());
		return harness.actionSheet.mock.calls.at(-1) ?? [];
	};
	const [, pin] = openMenu();
	act(() => pin(0));
	await vi.waitFor(() => expect(favorites).toBe(1));
	// The journal is busy with the Pin to top; the menu still offers Archive,
	// which is held until the journal is free.
	const [sheet, choose] = openMenu();
	expect(sheet.options).toContain("Archive project");
	act(() => choose(sheet.options.indexOf("Archive project")));
	await settle();
	expect(JSON.parse(harness.kv.get(`evener.native.board-hold.${id}`) ?? "[]")).toHaveLength(1);
	answerFirst();
	await vi.waitFor(() =>
		expect(fake.mutations.map((mutation) => mutation.method)).toEqual(["evener/favorite/set", "evener/archive/set"]),
	);
	act(() => tree.unmount());
});

it("offers no menu for a project another host shares", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	connect(
		id,
		hub({
			...fleet,
			manifest: twoHosts(),
			catalogs: { projects: [evenerProject({ sources: ["local", "paradise-park"] })] },
		}).client,
		"ready",
	);
	const tree = await mount(navigation());
	const [row] = projectRows(tree, "evener");
	expect(row.props.onLongPress).toBeUndefined();
	act(() => tree.unmount());
});

const recentRows = Array.from({ length: 32 }, (_, index) =>
	session(`local:r${index}`, { title: `Recent ${index}`, live: false, updated_at: minutesAgo(2000 + index) }),
);
const longProject = {
	...fleet,
	catalogs: { projects: [evenerProject({ default_expanded: true })] },
	projectPages: { "evener:recent": recentRows },
};

it("reads a tier's next page when its more row is pressed", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	const fake = hub(longProject);
	connect(id, fake.client, "ready");
	const tree = await mount(navigation());
	expect(texts(tree)).toContain("12 more");
	pressLabel(tree, "12 more");
	await settle();
	expect(pageReads(fake)).toContain("recent@20");
	expect(hasRow(tree, "Recent 31")).toBe(true);
	expect(texts(tree)).not.toContain("12 more");
	act(() => tree.unmount());
});

it("reads a tier's next page once its more row is at least half on screen", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	const fake = hub(longProject);
	connect(id, fake.client, "ready");
	const tree = await mount(navigation());
	const scroller = tree.root.find((node) => node.type === ("ScrollView" as never) && !node.props.horizontal);
	const more = tree.root.find((node) => node.props.testID === "project-more");
	act(() => {
		scroller.props.onLayout?.({ nativeEvent: { layout: { x: 0, y: 0, width: 390, height: 700 } } });
		projectSection(tree, "projects").props.onLayout({
			nativeEvent: { layout: { x: 0, y: 600, width: 390, height: 1400 } },
		});
		// 600 + 1000 is well past the 700pt viewport.
		more.props.onLayout({ nativeEvent: { layout: { x: 0, y: 1000, width: 390, height: 44 } } });
	});
	await settle();
	expect(pageReads(fake)).not.toContain("recent@20");
	// Scrolled so only a quarter of the row shows: still nothing.
	act(() =>
		scroller.props.onScroll({
			nativeEvent: { contentOffset: { x: 0, y: 1611 - 700 }, layoutMeasurement: { width: 390, height: 700 } },
		}),
	);
	await settle();
	expect(pageReads(fake)).not.toContain("recent@20");
	// Half of it on screen.
	act(() =>
		scroller.props.onScroll({
			nativeEvent: { contentOffset: { x: 0, y: 1622 - 700 }, layoutMeasurement: { width: 390, height: 700 } },
		}),
	);
	await settle();
	expect(pageReads(fake).filter((read) => read === "recent@20")).toHaveLength(1);
	act(() => tree.unmount());
});

it("opens an offline host's session only a project section loaded from search, marking it seen", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	// Only the evener project's current tier lists this session.
	const projectOnly = session("studio:project-only", {
		host_id: "studio",
		title: "Studio report",
		live: false,
		updated_at: minutesAgo(10),
	});
	const shape = {
		...troubledFleet(),
		catalogs: { projects: [evenerProject({ default_expanded: true })] },
		projectPages: { "evener:current": [projectOnly] },
		searchOnly: [projectOnly],
	};
	connect(id, hub(shape).client, "ready");
	const nav = navigation();
	const tree = await mount(nav);
	expect(hasRow(tree, "Studio report")).toBe(true);
	expect(seenMarkers(id).isSeen(projectOnly)).toBe(false);
	const bar = searchField(tree);
	bar.focus();
	await bar.type("studio report");
	act(() => resultTitled(tree, "Studio report").props.onPress());
	expect(nav.navigate).toHaveBeenLastCalledWith("Conversation", {
		hubId: id,
		ref: "studio:project-only",
		title: "Studio report",
	});
	expect(seenMarkers(id).isSeen(projectOnly)).toBe(true);
	act(() => tree.unmount());
});

it("reads no more of a project section while search results fill the scroller", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	const fake = hub(longProject);
	connect(id, fake.client, "ready");
	const tree = await mount(navigation());
	const more = tree.root.find((node) => node.props.testID === "project-more");
	act(() => {
		boardScroller(tree).props.onLayout?.({ nativeEvent: { layout: { x: 0, y: 0, width: 390, height: 700 } } });
		projectSection(tree, "projects").props.onLayout({
			nativeEvent: { layout: { x: 0, y: 600, width: 390, height: 1400 } },
		});
		more.props.onLayout({ nativeEvent: { layout: { x: 0, y: 1000, width: 390, height: 44 } } });
	});
	await settle();
	searchField(tree).focus();
	// Search results now cover where the more row last sat.
	act(() =>
		boardScroller(tree).props.onScroll({
			nativeEvent: { contentOffset: { x: 0, y: 1622 - 700 }, layoutMeasurement: { width: 390, height: 700 } },
		}),
	);
	await settle();
	expect(pageReads(fake)).not.toContain("recent@20");
	act(() => tree.unmount());
});

it("replaces the links to other screens with the sections themselves", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	connect(
		id,
		hub({
			...fleet,
			manifest: manifest({ sources: [laptopSource], sections: fleetSections, catalogs: catalogCounts(1, 2, 0) }),
			catalogs: { projects: [evenerProject()] },
		}).client,
		"ready",
	);
	const tree = await mount(navigation());
	for (const gone of ["Projects ›", "Archived ›", "Projects · 1", "Archived · 2", "Reconnect", "Refresh"])
		expect(texts(tree)).not.toContain(gone);
	expect(sectionHeaders(tree)).toEqual(["PROJECTS", "ARCHIVED · 2"]);
	act(() => tree.unmount());
});

const COULDNT_LOAD = "Couldn't load these sessions.";

it("reads a project's failed tier again on its own: when the Board comes back into view, and when the project unfolds", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	let recentFails = true;
	const fake = hub(
		{
			...fleet,
			catalogs: { projects: [evenerProject({ default_expanded: true })] },
			projectPages: { "evener:current": [localWork] },
		},
		undefined,
		(read) => recentFails && read.resource === "project_page" && read.tier === "recent",
	);
	connect(id, fake.client, "ready");
	const tree = await mount(navigation());
	const recentReads = () => pageReads(fake).filter((read) => read === "recent@0").length;
	expect(texts(tree)).toContain(COULDNT_LOAD);
	expect(recentReads()).toBe(1);
	// Staying in view, nothing reads it again on a timer.
	await advance(60_000);
	expect(recentReads()).toBe(1);
	setFocused(false);
	await settle();
	setFocused(true);
	await settle();
	expect(recentReads()).toBe(2);
	expect(texts(tree)).toContain(COULDNT_LOAD);
	recentFails = false;
	pressLabel(tree, "evener");
	await settle();
	pressLabel(tree, "evener");
	await settle();
	expect(recentReads()).toBe(3);
	expect(texts(tree)).not.toContain(COULDNT_LOAD);
	expect(hasRow(tree, "Local work")).toBe(true);
	act(() => tree.unmount());
});

it("reads a failed catalog again when the Board comes back into view", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	let catalogFails = true;
	const fake = hub(
		{ ...fleet, catalogs: { projects: [evenerProject()] } },
		undefined,
		(read) => catalogFails && read.resource === "catalog",
	);
	connect(id, fake.client, "ready");
	const tree = await mount(navigation());
	expect(catalogReads(fake)).toEqual(["projects"]);
	expect(sectionHeaders(tree)).toEqual(["PROJECTS"]);
	expect(projectRows(tree, "evener")).toHaveLength(0);
	catalogFails = false;
	setFocused(false);
	await settle();
	setFocused(true);
	await settle();
	expect(catalogReads(fake)).toEqual(["projects", "projects"]);
	expect(projectRows(tree, "evener")).toHaveLength(1);
	act(() => tree.unmount());
});

it("offers the project menu as an alert off iOS, archiving the project", async () => {
	const { Platform } = (await import("react-native")) as unknown as { Platform: { OS: string } };
	const id = hubId();
	adoptedAnHourAgo(id);
	const fake = hub({ ...fleet, catalogs: { projects: [evenerProject()] } }, undefined, undefined, {
		holdChanges: true,
	});
	connect(id, fake.client, "ready");
	const tree = await mount(navigation());
	Platform.OS = "android";
	try {
		act(() => projectRows(tree, "evener")[0].props.onLongPress());
		const menu = alertRequests.at(-1);
		expect(menu?.title).toBe("evener");
		expect(menu?.buttons?.map((button) => [button.text, button.style])).toEqual([
			["Pin to top", undefined],
			["Archive project", undefined],
			["Cancel", "cancel"],
		]);
		act(() => menu?.buttons?.[1].onPress?.());
		await settle();
	} finally {
		Platform.OS = "ios";
	}
	expect(fake.mutations).toEqual([
		{
			method: "evener/archive/set",
			params: { kind: "project", id: "evener", workingDir: "/home/jesse/git/evener", archived: true },
		},
	]);
	act(() => tree.unmount());
});

const manyProjects = Array.from({ length: 70 }, (_, index) =>
	evenerProject({ key: `project-${index}`, name: `project-${index}`, working_dir: `/home/jesse/git/project-${index}` }),
);
const manyProjectsFleet = {
	...fleet,
	manifest: manifest({ sources: [laptopSource], sections: fleetSections, catalogs: catalogCounts(70, 0, 0) }),
	catalogs: { projects: manyProjects },
};
const projectCatalogPages = (fake: ReturnType<typeof hub>) =>
	fake.requests.filter((read) => read.resource === "catalog").map((read) => read.offset ?? 0);

it("reads the catalog's next page when its more projects row is pressed", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	const fake = hub(manyProjectsFleet);
	connect(id, fake.client, "ready");
	const tree = await mount(navigation());
	expect(projectRows(tree, "project-49")).toHaveLength(1);
	expect(projectRows(tree, "project-50")).toHaveLength(0);
	pressLabel(tree, "20 more projects");
	await settle();
	expect(projectCatalogPages(fake)).toEqual([0, 50]);
	expect(projectRows(tree, "project-69")).toHaveLength(1);
	expect(texts(tree)).not.toContain("20 more projects");
	act(() => tree.unmount());
});

it("reads the catalog's next page once its more projects row is at least half on screen", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	const fake = hub(manyProjectsFleet);
	connect(id, fake.client, "ready");
	const tree = await mount(navigation());
	const scroller = tree.root.find((node) => node.type === ("ScrollView" as never) && !node.props.horizontal);
	const more = tree.root.find((node) => node.props.testID === "project-more-projects");
	act(() => {
		scroller.props.onLayout?.({ nativeEvent: { layout: { x: 0, y: 0, width: 390, height: 700 } } });
		projectSection(tree, "projects").props.onLayout({
			nativeEvent: { layout: { x: 0, y: 600, width: 390, height: 2500 } },
		});
		more.props.onLayout({ nativeEvent: { layout: { x: 0, y: 2400, width: 390, height: 44 } } });
	});
	await settle();
	expect(projectCatalogPages(fake)).toEqual([0]);
	// The row's top half is on screen.
	act(() =>
		scroller.props.onScroll({
			nativeEvent: { contentOffset: { x: 0, y: 3022 - 700 }, layoutMeasurement: { width: 390, height: 700 } },
		}),
	);
	await settle();
	expect(projectCatalogPages(fake)).toEqual([0, 50]);
	expect(projectRows(tree, "project-69")).toHaveLength(1);
	act(() => tree.unmount());
});

it("archives a project from its long-press menu, dimming it until the hub confirms", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	const fake = hub({ ...fleet, catalogs: { projects: [evenerProject()] } }, undefined, undefined, {
		holdChanges: true,
	});
	connect(id, fake.client, "ready");
	const tree = await mount(navigation());
	act(() => projectRows(tree, "evener")[0].props.onLongPress());
	const [sheet, choose] = harness.actionSheet.mock.calls.at(-1) ?? [];
	expect(sheet.options).toEqual(["Pin to top", "Archive project", "Cancel"]);
	act(() => choose(1));
	await settle();
	expect(fake.mutations).toEqual([
		{
			method: "evener/archive/set",
			params: { kind: "project", id: "evener", workingDir: "/home/jesse/git/evener", archived: true },
		},
	]);
	expect(rowOpacity(projectRows(tree, "evener")[0])).toBe(0.5);
	expect(menuLabels(tree)).toEqual([]);
	fake.release();
	await settle();
	expect(rowOpacity(projectRows(tree, "evener")[0])).toBe(1);
	expect(menuLabels(tree)).toEqual(["Mine, category menu", "Empty, category menu"]);
	expect(fake.mutations).toHaveLength(1);
	act(() => tree.unmount());
});

it("drops the Projects chip once the projects catalog loads empty, as the section goes", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	// The manifest counts 4 projects, but the catalog comes back empty.
	connect(id, hub(fleet).client, "ready");
	const tree = await mount(navigation());
	expect(sectionHeaders(tree)).toEqual([]);
	expect(chipLabels(tree)).toEqual(["Live, 5 sessions, 2 need you", "Mine, 3 sessions"]);
	act(() => tree.unmount());
});

/** A session's thread as thread/read answers it: `status` is its turn. */
/** A session's read: running a turn while `status` is active, its last turn
 * ended at `turnEndedAt` (the row's turn_ended_at, the daemon's one stamp). */
function threadOf(ref: string, status: string, turnEndedAt?: string): Thread {
	return {
		id: `thread:${ref}`,
		sessionId: ref,
		preview: "",
		ephemeral: false,
		modelProvider: "anthropic",
		createdAt: 1,
		updatedAt: 1,
		status: { type: status },
		cwd: "/tmp",
		cliVersion: "1.0.0",
		source: "local",
		turns: [],
		evener: {
			ref,
			instanceId: `instance:${ref}`,
			capabilities: {
				send: true,
				steer: true,
				interrupt: true,
				compact: true,
				clear: true,
				forkFromTurn: true,
				shutdown: true,
				changeModel: true,
				changeVisionModel: true,
				sharedNotes: true,
				queue: true,
				goal: true,
				rename: true,
			},
			queue: { revision: 0, depth: 0, preview: [] },
			...(status === "active" ? { activeTurnId: `turn:${ref}` } : {}),
			...(turnEndedAt ? { lastTurnEndedAt: Date.parse(turnEndedAt) } : {}),
		},
	};
}

// Rows the organization journal can archive: this hub's sessions carry real
// session ids (archiveTarget checks their shape), and another host's goes by
// its ref.
const OTHER_SESSION_ID = "1bCdEfGhIjKlMnOpQrStUv";
const swipeWorking = session(`local:${SESSION_ID}`, {
	session_id: SESSION_ID,
	title: "Refactor parser",
	state: "active",
	updated_at: minutesAgo(1),
});
const swipeFinished = session(`local:${OTHER_SESSION_ID}`, {
	session_id: OTHER_SESSION_ID,
	title: "Write changelog",
	updated_at: minutesAgo(4),
});
const swipePark = session("paradise-park:pp", {
	host_id: "paradise-park",
	session_id: "pp",
	title: "Park chore",
	updated_at: minutesAgo(6),
});
const swipeFleet = (): Fleet => ({
	live: [[swipeWorking, swipeFinished, swipePark]],
	needsYou: [],
	pins: [],
	pinned: {},
	manifest: manifest({
		sources: [laptopSource, { ...parkSource, online: true }],
		sections: { live: { count: 3 }, needs_you: { count: 0 }, pin_sections: { count: 0 } },
		catalogs: catalogCounts(0, 0, 0),
	}),
});
/** The swipeable around the Board row with this title. */
function swipeableOf(tree: ReactTestRenderer, title: string) {
	return tree.root
		.findAll((node) => node.type === ("ReanimatedSwipeable" as never))
		.filter((node) => node.findAll(isRowTitled(title)).length > 0)[0];
}
/** The buttons a swipe reveals on one side, rendered as the swipeable would. */
function revealed(swipeable: ReactTestInstance, side: "left" | "right"): ReactTestInstance[] {
	const panel = swipeable.props[side === "left" ? "renderLeftActions" : "renderRightActions"];
	if (!panel) return [];
	return render(panel()).root.findAll((node) => node.type === ("Pressable" as never));
}
const revealedLabels = (swipeable: ReactTestInstance, side: "left" | "right") =>
	revealed(swipeable, side).map((button) => button.props.accessibilityLabel);
function pressRevealed(swipeable: ReactTestInstance, side: "left" | "right", label: string) {
	const button = revealed(swipeable, side).find((node) => node.props.accessibilityLabel === label);
	if (!button) throw new Error(`no ${label} on the ${side}`);
	act(() => button.props.onPress());
}
async function mountSwipeFleet(fake: ReturnType<typeof hub>, nav = navigation()) {
	const id = hubId();
	adoptedAnHourAgo(id);
	connect(id, fake.client, "ready");
	const tree = await mount(nav);
	return { id, tree, nav };
}
/** The row menu host the Board provides, read as the sheet reads it. */
function menuHost(id: string): RowMenuHost {
	const host = rowMenuHosts.get(sheetKey(id));
	if (!host) throw new Error("the Board provides no row menu host");
	return host;
}
function menuItem(host: RowMenuHost, ref: string, archived = false) {
	const item = host.item(ref, archived);
	if (!item) throw new Error(`the Board shows no ${ref}`);
	return item;
}
const rowMenuLabels = (host: RowMenuHost, ref: string, archived = false) =>
	host.actions(menuItem(host, ref, archived), archived).map((action) => ROW_ACTION_LABELS[action]);

it("gives a working row Archive on the right swipe and Stop, Pin and More on the left, a finished row no Stop, and another host's row Archive too", async () => {
	const { tree } = await mountSwipeFleet(hub(swipeFleet()));
	const working = swipeableOf(tree, "Refactor parser");
	expect(revealedLabels(working, "left")).toEqual(["Archive"]);
	expect(revealedLabels(working, "right")).toEqual(["Stop", "Pin", "More"]);
	expect(revealedLabels(swipeableOf(tree, "Write changelog"), "right")).toEqual(["Pin", "More"]);
	expect(revealedLabels(swipeableOf(tree, "Park chore"), "left")).toEqual(["Archive"]);
	// VoiceOver reaches the same actions on the row itself.
	expect(rowTitled(tree, "Refactor parser").props.accessibilityActions).toEqual([
		{ name: "archive", label: "Archive" },
		{ name: "stop", label: "Stop" },
		{ name: "pin", label: "Pin" },
		{ name: "more", label: "More" },
	]);
});

it("archives a local row on a full swipe right, dims it until the hub confirms, and Undo unarchives it", async () => {
	const fake = hub(swipeFleet(), undefined, undefined, { holdChanges: true });
	const { tree } = await mountSwipeFleet(fake);
	swipeableCalls.closes = 0;
	swipeRowFully(swipeableOf(tree, "Refactor parser"), "right");
	expect(swipeableCalls.closes).toBe(1);
	await settle();
	expect(fake.mutations).toEqual([
		{ method: "evener/archive/set", params: { kind: "session", id: SESSION_ID, archived: true } },
	]);
	const dimmed = rowTitled(tree, "Refactor parser");
	expect(dimmed.props.style({ pressed: false }).opacity).toBe(0.5);
	expect(dimmed.props.accessibilityState).toEqual({ busy: true });
	act(() => fake.release());
	await settle();
	expect(rowTitled(tree, "Refactor parser").props.style({ pressed: false }).opacity).toBe(1);
	expect(texts(tree)).toContain("Archived");
	pressLabel(tree, "Undo");
	await settle();
	act(() => fake.release());
	await settle();
	expect(fake.mutations.at(-1)).toEqual({
		method: "evener/archive/set",
		params: { kind: "session", id: SESSION_ID, archived: false },
	});
	expect(texts(tree)).toContain("Unarchived");
	expect(texts(tree)).not.toContain("Refresh");
	expect(texts(tree)).not.toContain("Reconnect");
});

it("archives another host's row by its ref", async () => {
	const fake = hub(swipeFleet());
	const { tree } = await mountSwipeFleet(fake);
	swipeRowFully(swipeableOf(tree, "Park chore"), "right");
	await settle();
	expect(fake.mutations).toEqual([
		{ method: "evener/archive/set", params: { kind: "session", id: "paradise-park:pp", archived: true } },
	]);
	expect(texts(tree)).toContain("Archived");
});

it("says nothing when an archive can't be confirmed, and settles the journal so the row can swipe again", async () => {
	const fake = hub(swipeFleet(), undefined, undefined, { refuse: true });
	const { tree } = await mountSwipeFleet(fake);
	swipeRowFully(swipeableOf(tree, "Refactor parser"), "right");
	await settle();
	expect(fake.mutations).toHaveLength(1);
	expect(texts(tree)).not.toContain("Archived");
	expect(renderedText(tree)).not.toMatch(/Refresh|Reconnect|confirm/);
	// The Board read the session back, so its organization actions return.
	expect(revealedLabels(swipeableOf(tree, "Refactor parser"), "left")).toEqual(["Archive"]);
	expect(rowTitled(tree, "Refactor parser").props.style({ pressed: false }).opacity).toBe(1);
});

it("stops a working row through the durable runtime: a fresh read, then the interrupt, then Stopped", async () => {
	const fake = hub(swipeFleet());
	const { tree } = await mountSwipeFleet(fake);
	pressRevealed(swipeableOf(tree, "Refactor parser"), "right", "Stop");
	await settle();
	await vi.waitFor(() =>
		expect(fake.threadCalls.map((call) => call.method)).toEqual(["thread/read", "turn/interrupt"]),
	);
	expect(fake.threadCalls[1]?.params).toMatchObject({
		ref: `local:${SESSION_ID}`,
		expectedInstanceId: `instance:local:${SESSION_ID}`,
	});
	expect(texts(tree)).toContain("Stopped");
});

it("opens Pin to category for a row's Pin", async () => {
	const { id, tree, nav } = await mountSwipeFleet(hub(swipeFleet()));
	pressRevealed(swipeableOf(tree, "Write changelog"), "right", "Pin");
	expect(nav.navigate).toHaveBeenCalledWith("PinAssignment", {
		hubId: id,
		ref: `local:${OTHER_SESSION_ID}`,
		title: "Write changelog",
	});
});

it("offers a row's actions offline too, to hold until the connection returns (phase 6 ruling 18)", async () => {
	const fake = hub(swipeFleet());
	const { id, tree, nav } = await mountSwipeFleet(fake);
	connect(id, fake.client, "reconnecting");
	rerender(tree, nav);
	const working = swipeableOf(tree, "Refactor parser");
	expect(revealedLabels(working, "left")).toEqual(["Archive"]);
	expect(revealedLabels(working, "right")).toEqual(["Stop", "Pin", "More"]);
	expect(rowMenuLabels(menuHost(id), `local:${SESSION_ID}`)).toEqual(
		expect.arrayContaining(["Pin to category…", "Stop", "Shut down", "Archive"]),
	);
});

describe("Board actions held offline (phase 6 ruling 18)", () => {
	const heldIn = (id: string) => JSON.parse(harness.kv.get(`evener.native.board-hold.${id}`) ?? "[]") as unknown[];
	const interrupts = (fake: ReturnType<typeof hub>) =>
		fake.threadCalls.filter((call) => call.method === "turn/interrupt");
	async function reconnect(id: string, fake: ReturnType<typeof hub>, tree: ReactTestRenderer, nav: Navigation) {
		connect(id, fake.client, "ready");
		rerender(tree, nav);
		await settle();
		await settle();
	}

	it("holds an archive taken offline, says so on the row, and sends it once back online", async () => {
		const fake = hub(swipeFleet());
		const { id, tree, nav } = await mountSwipeFleet(fake);
		connect(id, fake.client, "reconnecting");
		rerender(tree, nav);
		swipeRowFully(swipeableOf(tree, "Refactor parser"), "right");
		await settle();
		expect(fake.mutations).toEqual([]);
		expect(rowTitled(tree, "Refactor parser").props.style({ pressed: false }).opacity).toBe(0.5);
		expect(texts(tree)).toContain("Archive waits for the connection");
		await reconnect(id, fake, tree, nav);
		await vi.waitFor(() => expect(heldIn(id)).toEqual([]));
		expect(fake.mutations).toEqual([
			{ method: "evener/archive/set", params: { kind: "session", id: SESSION_ID, archived: true } },
		]);
	});

	it("queues a row's change behind the held one it answers, even once back online", async () => {
		const fake = hub(swipeFleet());
		const { id, tree, nav } = await mountSwipeFleet(fake);
		let answerArchive: () => void = () => {};
		const request = fake.client.request;
		let archives = 0;
		fake.client.request = ((method: string, params: unknown) => {
			// The held archive's write hangs while it is on its way.
			if (method === "evener/archive/set" && archives++ === 0) {
				const answer = request(method as never, params as never);
				return new Promise((resolve) => {
					answerArchive = () => resolve(answer as never);
				});
			}
			return request(method as never, params as never);
		}) as typeof request;
		connect(id, fake.client, "reconnecting");
		rerender(tree, nav);
		swipeRowFully(swipeableOf(tree, "Refactor parser"), "right");
		await settle();
		// The menu, opened offline, stays up across the reconnect.
		const menu = menuHost(id);
		const item = menuItem(menu, `local:${SESSION_ID}`);
		await reconnect(id, fake, tree, nav);
		await vi.waitFor(() => expect(archives).toBe(1));
		act(() => menu.act(item, "unarchive"));
		await settle();
		answerArchive();
		await vi.waitFor(() =>
			expect(fake.mutations.filter((m) => m.method === "evener/archive/set").map((m) => m.params)).toEqual([
				{ kind: "session", id: SESSION_ID, archived: true },
				{ kind: "session", id: SESSION_ID, archived: false },
			]),
		);
		await vi.waitFor(() => expect(heldIn(id)).toEqual([]));
	});

	/** Holds the first request of `method` until the returned function
	 * answers it; the hub records it at once, as it does any write. */
	function hangFirst(fake: ReturnType<typeof hub>, method: string): () => void {
		let answer: () => void = () => {};
		let count = 0;
		const request = fake.client.request;
		fake.client.request = ((name: string, params: unknown) => {
			if (name !== method || count++ > 0) return request(name as never, params as never);
			const answered = request(name as never, params as never);
			return new Promise((resolve) => {
				answer = () => resolve(answered as never);
			});
		}) as typeof request;
		return () => answer();
	}
	const writes = (fake: ReturnType<typeof hub>, method: string) =>
		fake.mutations.filter((mutation) => mutation.method === method).map((mutation) => mutation.params);

	it("holds a row's archive while the journal is busy with another, and sends it once the journal is free", async () => {
		const fake = hub(swipeFleet());
		const answerFirst = hangFirst(fake, "evener/archive/set");
		const { id, tree } = await mountSwipeFleet(fake);
		swipeRowFully(swipeableOf(tree, "Refactor parser"), "right");
		await vi.waitFor(() => expect(writes(fake, "evener/archive/set")).toHaveLength(1));
		// The journal is busy, and Archive is still there to take: it is held,
		// never refused.
		swipeRowFully(swipeableOf(tree, "Write changelog"), "right");
		await settle();
		expect(texts(tree)).toContain("Archive is waiting to send");
		expect(heldIn(id)).toHaveLength(1);
		answerFirst();
		await vi.waitFor(() =>
			expect(writes(fake, "evener/archive/set")).toEqual([
				{ kind: "session", id: SESSION_ID, archived: true },
				{ kind: "session", id: OTHER_SESSION_ID, archived: true },
			]),
		);
		await vi.waitFor(() => expect(heldIn(id)).toEqual([]));
	});

	it("holds a row's Undo while the journal is busy with another change, and sends it once the journal is free", async () => {
		const fake = hub(swipeFleet());
		const { id, tree } = await mountSwipeFleet(fake);
		swipeRowFully(swipeableOf(tree, "Refactor parser"), "right");
		await settle();
		expect(texts(tree)).toContain("Archived");
		const answerNext = hangFirst(fake, "evener/archive/set");
		swipeRowFully(swipeableOf(tree, "Write changelog"), "right");
		await vi.waitFor(() => expect(writes(fake, "evener/archive/set")).toHaveLength(2));
		pressLabel(tree, "Undo");
		await settle();
		expect(heldIn(id)).toHaveLength(1);
		answerNext();
		await vi.waitFor(() =>
			expect(writes(fake, "evener/archive/set")).toEqual([
				{ kind: "session", id: SESSION_ID, archived: true },
				{ kind: "session", id: OTHER_SESSION_ID, archived: true },
				{ kind: "session", id: SESSION_ID, archived: false },
			]),
		);
		await vi.waitFor(() => expect(heldIn(id)).toEqual([]));
	});

	it("queues a Shut down behind the held one that is on its way, rather than sending it beside it", async () => {
		const fake = hub(swipeFleet());
		const { id, tree, nav } = await mountSwipeFleet(fake);
		const answerFirst = hangFirst(fake, "thread/shutdown");
		connect(id, fake.client, "reconnecting");
		rerender(tree, nav);
		const shutDown = () => {
			const host = menuHost(id);
			act(() => host.act(menuItem(host, `local:${SESSION_ID}`), "shutDown"));
			act(() => alertRequests.at(-1)?.buttons?.[1]?.onPress?.());
		};
		shutDown();
		await settle();
		await reconnect(id, fake, tree, nav);
		await vi.waitFor(() => expect(writes(fake, "thread/shutdown")).toHaveLength(1));
		shutDown();
		await settle();
		// The first is still out: the second waits in the hold behind it.
		expect(writes(fake, "thread/shutdown")).toHaveLength(1);
		expect(heldIn(id)).toHaveLength(2);
		answerFirst();
		await vi.waitFor(() => expect(heldIn(id)).toEqual([]));
	});

	it("offers the row menu's Cancel for a change held after the menu opened", async () => {
		const fake = hub(swipeFleet());
		const { id, tree, nav } = await mountSwipeFleet(fake);
		connect(id, fake.client, "reconnecting");
		rerender(tree, nav);
		const before = menuHost(id);
		const host = menuHost(id);
		act(() => host.act(menuItem(host, `local:${SESSION_ID}`), "stop"));
		await settle();
		// The sheet re-reads a host it is given anew: a hold alone gives it one.
		expect(menuHost(id)).not.toBe(before);
		expect(menuHost(id).held(menuItem(menuHost(id), `local:${SESSION_ID}`))).toEqual([
			{ id: expect.any(String), label: "Cancel Stop" },
		]);
	});

	it("cancels a held archive from the row's menu, and the row is itself again", async () => {
		const fake = hub(swipeFleet());
		const { id, tree, nav } = await mountSwipeFleet(fake);
		connect(id, fake.client, "reconnecting");
		rerender(tree, nav);
		swipeRowFully(swipeableOf(tree, "Refactor parser"), "right");
		await settle();
		const menu = menuHost(id);
		const [held] = menu.held(menuItem(menu, `local:${SESSION_ID}`));
		expect(held?.label).toBe("Cancel Archive");
		act(() => menu.cancel(held?.id ?? ""));
		await settle();
		expect(texts(tree)).not.toContain("Archive waits for the connection");
		expect(rowTitled(tree, "Refactor parser").props.style({ pressed: false }).opacity).toBe(1);
		await reconnect(id, fake, tree, nav);
		expect(fake.mutations).toEqual([]);
	});

	it("sends a held Stop whose turn is the one seen, even with the Board out of view", async () => {
		const fake = hub(swipeFleet());
		const { id, tree, nav } = await mountSwipeFleet(fake);
		connect(id, fake.client, "reconnecting");
		rerender(tree, nav);
		pressRevealed(swipeableOf(tree, "Refactor parser"), "right", "Stop");
		await settle();
		expect(texts(tree)).toContain("Stop waits for the connection");
		setFocused(false);
		await reconnect(id, fake, tree, nav);
		await vi.waitFor(() => expect(interrupts(fake)).toHaveLength(1));
		expect(heldIn(id)).toEqual([]);
	});

	it("drops a held Stop once a newer turn runs, and says so", async () => {
		const fleet = swipeFleet();
		const [row] = fleet.live[0] ?? [];
		if (!row) throw new Error("no working row");
		fleet.live[0] = [{ ...row, turn_ended_at: minutesAgo(10) }, ...(fleet.live[0] ?? []).slice(1)];
		const fake = hub(fleet);
		const { id, tree, nav } = await mountSwipeFleet(fake);
		connect(id, fake.client, "reconnecting");
		rerender(tree, nav);
		pressRevealed(swipeableOf(tree, "Refactor parser"), "right", "Stop");
		await settle();
		// Meanwhile that turn ended and another began.
		fleet.live[0] = [{ ...row, turn_ended_at: minutesAgo(1) }, ...(fleet.live[0] ?? []).slice(1)];
		await reconnect(id, fake, tree, nav);
		await vi.waitFor(() => expect(texts(tree)).toContain("The turn you stopped ended before you were back online"));
		expect(interrupts(fake)).toEqual([]);
		expect(heldIn(id)).toEqual([]);
	});

	it("sends what was held before a relaunch on the first ready connection, in the order held", async () => {
		const fake = hub(swipeFleet());
		const id = hubId();
		adoptedAnHourAgo(id);
		harness.kv.set(
			`evener.native.board-hold.${id}`,
			JSON.stringify([
				{
					id: "a",
					heldAt: 1,
					action: { kind: "rename", ref: `local:${OTHER_SESSION_ID}`, title: "Write changelog", name: "Notes" },
				},
				{
					id: "b",
					heldAt: 2,
					action: {
						kind: "shutDown",
						ref: `local:${OTHER_SESSION_ID}`,
						title: "Write changelog",
						seen: { turnEndedAt: null, running: false },
					},
				},
			]),
		);
		connect(id, fake.client, "ready");
		await mount(navigation());
		await vi.waitFor(() => expect(heldIn(id)).toEqual([]));
		expect(fake.mutations.map((mutation) => mutation.method)).toEqual(["evener/thread/name/set", "thread/shutdown"]);
	});

	it("offers no Cancel for a held action already on its way", async () => {
		const fake = hub(swipeFleet());
		const id = hubId();
		adoptedAnHourAgo(id);
		const ref = `local:${OTHER_SESSION_ID}`;
		harness.kv.set(
			`evener.native.board-hold.${id}`,
			JSON.stringify([
				{
					id: "a",
					heldAt: 1,
					action: { kind: "shutDown", ref, title: "Write changelog", seen: { turnEndedAt: null, running: false } },
				},
			]),
		);
		let answer: () => void = () => {};
		const request = fake.client.request;
		fake.client.request = ((method: string, params: unknown) => {
			if (method !== "thread/shutdown") return request(method as never, params as never);
			fake.mutations.push({ method, params });
			return new Promise((resolve) => {
				answer = () => resolve({} as never);
			});
		}) as typeof request;
		connect(id, fake.client, "ready");
		const tree = await mount(navigation());
		await vi.waitFor(() => expect(fake.mutations).toHaveLength(1));
		const menu = menuHost(id);
		expect(menu.held(menuItem(menu, ref))).toEqual([]);
		expect(texts(tree)).toContain("Shut down is waiting to send");
		answer();
		await settle();
		expect(texts(tree)).not.toContain("Shut down is waiting to send");
	});

	it("stops replaying a removed hub: its next Shut down never goes, and its hold's key stays gone", async () => {
		const fake = hub(swipeFleet());
		const id = hubId();
		adoptedAnHourAgo(id);
		const shutDown = (key: string) => ({
			id: key,
			heldAt: 1,
			action: {
				kind: "shutDown",
				ref: `local:${OTHER_SESSION_ID}`,
				title: "Write changelog",
				seen: { turnEndedAt: null, running: false },
			},
		});
		harness.kv.set(
			`evener.native.board-hold.${id}`,
			JSON.stringify([
				shutDown("a"),
				{ ...shutDown("b"), action: { ...shutDown("b").action, ref: "paradise-park:pp" } },
			]),
		);
		let answer: () => void = () => {};
		const request = fake.client.request;
		fake.client.request = ((method: string, params: unknown) => {
			if (method !== "thread/shutdown") return request(method as never, params as never);
			fake.mutations.push({ method, params });
			return new Promise((resolve) => {
				answer = () => resolve({} as never);
			});
		}) as typeof request;
		connect(id, fake.client, "ready");
		await mount(navigation());
		await vi.waitFor(() => expect(fake.mutations).toHaveLength(1));
		forgetBoardForHub(id);
		answer();
		await settle();
		await settle();
		expect(fake.mutations).toHaveLength(1);
		expect(harness.kv.has(`evener.native.board-hold.${id}`)).toBe(false);
	});
});

it("puts swipes on pinned categories' rows and project sessions too", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	const pinnedRow = session(`local:${OTHER_SESSION_ID}`, {
		session_id: OTHER_SESSION_ID,
		title: "Pinned note",
		live: false,
		updated_at: minutesAgo(600),
	});
	const archivedRow = session(`local:${SESSION_ID}`, {
		session_id: SESSION_ID,
		title: "Old archived work",
		live: false,
		updated_at: minutesAgo(3000),
	});
	const shape: Fleet = {
		live: [[working]],
		needsYou: [],
		pins: [{ id: "pins-1", name: "Mine", count: 1 }],
		pinned: { "pins-1": [pinnedRow] },
		manifest: manifest({
			sources: [laptopSource],
			sections: { live: { count: 1 }, needs_you: { count: 0 }, pin_sections: { count: 1 } },
			catalogs: catalogCounts(1, 0, 0),
		}),
		catalogs: { projects: [evenerProject()] },
		projectPages: { "evener:archived": [archivedRow] },
	};
	connect(id, hub(shape).client, "ready");
	const tree = await mount(navigation());
	expect(revealedLabels(swipeableOf(tree, "Pinned note"), "left")).toEqual(["Archive"]);
	// Unfold the project, then its Archived group.
	pressLabel(tree, "evener");
	await settle();
	pressLabel(tree, "Archived, 1 session");
	await settle();
	expect(revealedLabels(swipeableOf(tree, "Old archived work"), "left")).toEqual(["Unarchive"]);
});

it("ends a row's trailing swipe with More, which opens the row menu sheet, as a long press does", async () => {
	const shape = swipeFleet();
	shape.live = [[{ ...swipeWorking, rename: true }, swipeFinished, swipePark]];
	const { id, tree, nav } = await mountSwipeFleet(hub(shape));
	pressRevealed(swipeableOf(tree, "Refactor parser"), "right", "More");
	expect(nav.navigate).toHaveBeenCalledWith("RowMenuSheet", { hubId: id, ref: `local:${SESSION_ID}`, archived: false });
	expect(rowMenuLabels(menuHost(id), `local:${SESSION_ID}`)).toEqual([
		"Pin to category…",
		"Stop",
		"Shut down",
		"Archive",
		"Rename",
	]);
	nav.navigate.mockClear();
	const row = rowTitled(tree, "Refactor parser");
	expect(row.props.delayLongPress).toBe(500);
	act(() => row.props.onLongPress());
	expect(nav.navigate).toHaveBeenCalledWith("RowMenuSheet", { hubId: id, ref: `local:${SESSION_ID}`, archived: false });
});

it("gives the row menu the copy it opened from, when a session shows in both Live and a project's Archived tier", async () => {
	const ref = `local:${SESSION_ID}`;
	const shape = swipeFleet();
	shape.catalogs = { projects: [evenerProject()] };
	shape.projectPages = {
		"evener:archived": [
			session(ref, {
				session_id: SESSION_ID,
				title: "Refactor parser (archived tier)",
				live: false,
				updated_at: minutesAgo(3000),
			}),
		],
	};
	shape.manifest = manifest({
		sources: [laptopSource, { ...parkSource, online: true }],
		sections: { live: { count: 3 }, needs_you: { count: 0 }, pin_sections: { count: 0 } },
		catalogs: catalogCounts(1, 0, 0),
	});
	const { id, tree, nav } = await mountSwipeFleet(hub(shape));
	// Unfold the project, then its Archived group, so both copies are on screen.
	pressLabel(tree, "evener");
	await settle();
	pressLabel(tree, "Archived, 1 session");
	await settle();
	const host = menuHost(id);
	expect(rowMenuLabels(host, ref, true)).toContain("Unarchive");
	expect(rowMenuLabels(host, ref, true)).not.toContain("Archive");
	expect(rowMenuLabels(host, ref, false)).toContain("Archive");
	expect(rowMenuLabels(host, ref, false)).not.toContain("Unarchive");
	pressRevealed(swipeableOf(tree, "Refactor parser (archived tier)"), "right", "More");
	expect(nav.navigate).toHaveBeenCalledWith("RowMenuSheet", { hubId: id, ref, archived: true });
	nav.navigate.mockClear();
	act(() => rowTitled(tree, "Refactor parser (archived tier)").props.onLongPress());
	expect(nav.navigate).toHaveBeenCalledWith("RowMenuSheet", { hubId: id, ref, archived: true });
});

it("keeps the row menu's row while the list is held, even once the read drops it", async () => {
	const shape = swipeFleet();
	const fake = hub(shape);
	const { id, tree, nav } = await mountSwipeFleet(fake);
	// Opening the menu from the row holds the list (ruling 22).
	pressRevealed(swipeableOf(tree, "Refactor parser"), "right", "More");
	const ref = `local:${SESSION_ID}`;
	expect(nav.navigate).toHaveBeenCalledWith("RowMenuSheet", { hubId: id, ref, archived: false });
	// A later read no longer has the row, but the held list keeps showing it,
	// so the menu that is about it must still resolve one.
	shape.live = [[swipeFinished, swipePark]];
	act(() => fake.invalidate(1, [{ kind: "section", section: "live" }]));
	await settle();
	expect(hasRow(tree, "Refactor parser")).toBe(true);
	expect(menuItem(menuHost(id), ref).row.ref).toBe(ref);
});

it("offers Rename only on iOS, where Alert.prompt exists", async () => {
	const { Platform } = (await import("react-native")) as unknown as { Platform: { OS: string } };
	const shape = swipeFleet();
	shape.live = [[{ ...swipeWorking, rename: true }, swipeFinished, swipePark]];
	const { id } = await mountSwipeFleet(hub(shape));
	Platform.OS = "android";
	try {
		expect(rowMenuLabels(menuHost(id), `local:${SESSION_ID}`)).not.toContain("Rename");
	} finally {
		Platform.OS = "ios";
	}
});

it("asks before shutting a session down from the menu, then says it shut down", async () => {
	const fake = hub(swipeFleet());
	const { id, tree } = await mountSwipeFleet(fake);
	const host = menuHost(id);
	alertRequests.length = 0;
	act(() => host.act(menuItem(host, `local:${SESSION_ID}`), "shutDown"));
	expect(fake.mutations).toEqual([]);
	const ask = alertRequests.at(-1);
	expect(ask?.title).toBe("Shut down “Refactor parser”?");
	expect(ask?.message).toBe("The agent stops. Send it a message to resume it.");
	expect(ask?.buttons?.map((button) => [button.text, button.style])).toEqual([
		["Cancel", "cancel"],
		["Shut down", "destructive"],
	]);
	playedHaptics.length = 0;
	act(() => ask?.buttons?.[1]?.onPress?.());
	// Spec 16.6: rigid on a destructive confirmation.
	expect(playedHaptics).toEqual(["impact:rigid"]);
	await settle();
	expect(fake.mutations).toEqual([{ method: "thread/shutdown", params: { ref: `local:${SESSION_ID}` } }]);
	expect(texts(tree)).toContain("Session shut down");
});

it("says why a shut down failed, in the hub's words", async () => {
	const fake = hub(swipeFleet(), undefined, undefined, { refuse: true });
	const { id, tree } = await mountSwipeFleet(fake);
	const host = menuHost(id);
	alertRequests.length = 0;
	act(() => host.act(menuItem(host, `local:${SESSION_ID}`), "shutDown"));
	act(() => alertRequests.at(-1)?.buttons?.[1]?.onPress?.());
	await settle();
	expect(texts(tree)).toContain("Couldn't shut down “Refactor parser”: session not found");
});

it("renames a session from the menu with the prompt's text", async () => {
	const fake = hub(swipeFleet());
	const { id, tree } = await mountSwipeFleet(fake);
	const host = menuHost(id);
	harness.prompt.mockClear();
	act(() => host.act(menuItem(host, `local:${SESSION_ID}`), "rename"));
	expect(harness.prompt).toHaveBeenCalledOnce();
	const [title, message, buttons, type, value] = harness.prompt.mock.calls[0] as [
		string,
		undefined,
		{ text: string; style?: string; onPress?: (name?: string) => void }[],
		string,
		string,
	];
	expect([title, message, type, value]).toEqual(["Rename session", undefined, "plain-text", "Refactor parser"]);
	expect(buttons.map((button) => [button.text, button.style])).toEqual([
		["Cancel", "cancel"],
		["Rename", undefined],
	]);
	act(() => buttons[1]?.onPress?.("  Parser rewrite "));
	await settle();
	expect(fake.mutations).toEqual([
		{ method: "evener/thread/name/set", params: { ref: `local:${SESSION_ID}`, name: "Parser rewrite" } },
	]);
	expect(texts(tree)).toContain("Renamed");
});

it("says why a rename failed, in the hub's words", async () => {
	const fake = hub(swipeFleet(), undefined, undefined, { refuse: true });
	const { id, tree } = await mountSwipeFleet(fake);
	const host = menuHost(id);
	harness.prompt.mockClear();
	act(() => host.act(menuItem(host, `local:${SESSION_ID}`), "rename"));
	const buttons = harness.prompt.mock.calls[0]?.[2] as { onPress?: (name?: string) => void }[];
	act(() => buttons[1]?.onPress?.("Parser rewrite"));
	await settle();
	expect(texts(tree)).toContain("Couldn't rename “Refactor parser”: session not found");
});

it("marks a finished row read from the menu, moving it to Idle, and unread again", async () => {
	const { id, tree } = await mountSwipeFleet(hub(swipeFleet()));
	const host = menuHost(id);
	const ref = `local:${OTHER_SESSION_ID}`;
	expect(bandHeaders(tree)).toEqual(["FINISHED · 2", "WORKING · 1"]);
	act(() => host.act(menuItem(host, ref), "markRead"));
	await settle();
	expect(bandHeaders(tree)).toEqual(["FINISHED · 1", "WORKING · 1", "Idle · 1"]);
	const read = menuHost(id);
	expect(menuItem(read, ref).state).toBe("idle");
	act(() => read.act(menuItem(read, ref), "markUnread"));
	await settle();
	expect(bandHeaders(tree)).toEqual(["FINISHED · 2", "WORKING · 1"]);
});

it("sends no read mark through another hub's client when this Board's hub isn't the active one", async () => {
	const first = hubId();
	const second = hubId();
	adoptedAnHourAgo(first);
	adoptedAnHourAgo(second);
	// A row the hub decides (it carries a turn end), so marking it read is a
	// hub write (BoardSeen.markRead), not just the device's own marker.
	const row = session("local:hub-unseen", {
		title: "Hub unseen",
		updated_at: minutesAgo(90),
		turn_ended_at: minutesAgo(90),
		unseen: true,
	});
	const shape: Fleet = { ...fleet, live: [[row]], needsYou: [] };
	const fakeA = hub(shape);
	const fakeB = hub(shape);
	connect(first, fakeA.client, "ready");
	const nav = navigation();
	const tree = await mount(nav);
	const host = menuHost(first);
	const ref = "local:hub-unseen";
	// The active hub changes to a different one, but this Board (mounted for
	// "first") stays up, as it would underneath a freshly-selected hub's own
	// screen; its next render reads the new connection.
	connect(second, fakeB.client, "ready");
	setFocused(false);
	setFocused(true);
	await settle();
	act(() => host.act(menuItem(host, ref), "markRead"));
	await settle();
	expect(fakeA.seen).toEqual([]);
	expect(fakeB.seen).toEqual([]);
	act(() => tree.unmount());
});

it("flushes a hidden Board's pending read marks through no other hub's client when the connection re-binds", async () => {
	const first = hubId();
	const second = hubId();
	adoptedAnHourAgo(first);
	adoptedAnHourAgo(second);
	const row = session("local:hub-unseen", {
		title: "Hub unseen",
		updated_at: minutesAgo(90),
		turn_ended_at: minutesAgo(90),
		unseen: true,
	});
	const shape: Fleet = { ...fleet, live: [[row]], needsYou: [] };
	const fakeA = hub(shape);
	const fakeB = hub(shape);
	connect(first, fakeA.client, "ready");
	const tree = await mount(navigation());
	const host = menuHost(first);
	connect(second, fakeB.client, "ready");
	setFocused(false);
	setFocused(true);
	await settle();
	// Marked while another hub is active: the mark waits in this Board's
	// hub's pending marks.
	act(() => host.act(menuItem(host, "local:hub-unseen"), "markRead"));
	await settle();
	// The other hub's connection drops and comes back, handing the Board a
	// ready client again.
	connect(second, fakeB.client, "connecting");
	setFocused(false);
	setFocused(true);
	await settle();
	connect(second, fakeB.client, "ready");
	setFocused(false);
	setFocused(true);
	await settle();
	expect(fakeA.seen).toEqual([]);
	expect(fakeB.seen).toEqual([]);
	act(() => tree.unmount());
});

it("opens a session from the menu's card without marking it through another hub's client", async () => {
	const first = hubId();
	const second = hubId();
	adoptedAnHourAgo(first);
	adoptedAnHourAgo(second);
	const row = session("local:hub-unseen", {
		title: "Hub unseen",
		updated_at: minutesAgo(90),
		turn_ended_at: minutesAgo(90),
		unseen: true,
	});
	const shape: Fleet = { ...fleet, live: [[row]], needsYou: [] };
	const fakeA = hub(shape);
	const fakeB = hub(shape);
	connect(first, fakeA.client, "ready");
	const nav = navigation();
	const tree = await mount(nav);
	const host = menuHost(first);
	const ref = "local:hub-unseen";
	connect(second, fakeB.client, "ready");
	setFocused(false);
	setFocused(true);
	await settle();
	act(() => host.openSession(menuItem(host, ref)));
	await settle();
	expect(nav.navigate).toHaveBeenCalledWith("Conversation", { hubId: first, ref, title: "Hub unseen" });
	expect(fakeB.seen).toEqual([]);
	act(() => tree.unmount());
});

it("sends no stop through another hub's client when this Board's hub isn't the active one", async () => {
	const first = hubId();
	const second = hubId();
	adoptedAnHourAgo(first);
	adoptedAnHourAgo(second);
	const fakeA = hub(swipeFleet());
	const fakeB = hub(swipeFleet());
	connect(first, fakeA.client, "ready");
	const nav = navigation();
	const tree = await mount(nav);
	const host = menuHost(first);
	const ref = `local:${SESSION_ID}`;
	connect(second, fakeB.client, "ready");
	setFocused(false);
	setFocused(true);
	await settle();
	act(() => host.act(menuItem(host, ref), "stop"));
	await settle();
	expect(fakeA.threadCalls).toEqual([]);
	expect(fakeB.threadCalls).toEqual([]);
	act(() => tree.unmount());
});

it("sends no shutdown through another hub's client when this Board's hub isn't the active one", async () => {
	const first = hubId();
	const second = hubId();
	adoptedAnHourAgo(first);
	adoptedAnHourAgo(second);
	const fakeA = hub(swipeFleet());
	const fakeB = hub(swipeFleet());
	connect(first, fakeA.client, "ready");
	const nav = navigation();
	const tree = await mount(nav);
	const host = menuHost(first);
	const ref = `local:${SESSION_ID}`;
	connect(second, fakeB.client, "ready");
	setFocused(false);
	setFocused(true);
	await settle();
	alertRequests.length = 0;
	act(() => host.act(menuItem(host, ref), "shutDown"));
	act(() => alertRequests.at(-1)?.buttons?.[1]?.onPress?.());
	await settle();
	expect(fakeA.mutations).toEqual([]);
	expect(fakeB.mutations).toEqual([]);
	act(() => tree.unmount());
});

it("sends no rename through another hub's client when this Board's hub isn't the active one", async () => {
	const first = hubId();
	const second = hubId();
	adoptedAnHourAgo(first);
	adoptedAnHourAgo(second);
	const fakeA = hub(swipeFleet());
	const fakeB = hub(swipeFleet());
	connect(first, fakeA.client, "ready");
	const nav = navigation();
	const tree = await mount(nav);
	const host = menuHost(first);
	const ref = `local:${SESSION_ID}`;
	connect(second, fakeB.client, "ready");
	setFocused(false);
	setFocused(true);
	await settle();
	harness.prompt.mockClear();
	act(() => host.act(menuItem(host, ref), "rename"));
	const buttons = harness.prompt.mock.calls[0]?.[2] as { onPress?: (name?: string) => void }[];
	act(() => buttons[1]?.onPress?.("New name"));
	await settle();
	expect(fakeA.mutations).toEqual([]);
	expect(fakeB.mutations).toEqual([]);
	act(() => tree.unmount());
});

it("opens the session from the menu's card as a tap does", async () => {
	const { id, nav } = await mountSwipeFleet(hub(swipeFleet()));
	const host = menuHost(id);
	act(() => host.openSession(menuItem(host, `local:${OTHER_SESSION_ID}`)));
	expect(nav.navigate).toHaveBeenCalledWith("Conversation", {
		hubId: id,
		ref: `local:${OTHER_SESSION_ID}`,
		title: "Write changelog",
	});
	expect(menuItem(menuHost(id), `local:${OTHER_SESSION_ID}`).state).toBe("idle");
});

it("stops, pins and archives from the menu as the swipes do", async () => {
	const fake = hub(swipeFleet());
	const { id, tree, nav } = await mountSwipeFleet(fake);
	const host = menuHost(id);
	act(() => host.act(menuItem(host, `local:${OTHER_SESSION_ID}`), "pin"));
	expect(nav.navigate).toHaveBeenCalledWith("PinAssignment", {
		hubId: id,
		ref: `local:${OTHER_SESSION_ID}`,
		title: "Write changelog",
	});
	act(() => host.act(menuItem(host, `local:${SESSION_ID}`), "stop"));
	await settle();
	await vi.waitFor(() =>
		expect(fake.threadCalls.map((call) => call.method)).toEqual(["thread/read", "turn/interrupt"]),
	);
	expect(texts(tree)).toContain("Stopped");
	act(() => host.act(menuItem(host, "paradise-park:pp"), "archive"));
	await settle();
	expect(fake.mutations).toEqual([
		{ method: "evener/archive/set", params: { kind: "session", id: "paradise-park:pp", archived: true } },
	]);
});

it("drops a row that left the Board from the menu's host", async () => {
	const shape = swipeFleet();
	const fake = hub(shape);
	const { id } = await mountSwipeFleet(fake);
	expect(menuHost(id).item("paradise-park:pp", false)).toBeDefined();
	shape.live = [[swipeWorking, swipeFinished]];
	act(() => fake.invalidate(1, [{ kind: "section", section: "live" }]));
	await settle();
	expect(menuHost(id).item("paradise-park:pp", false)).toBeUndefined();
	expect(menuHost(id).item(`local:${SESSION_ID}`, false)).toBeDefined();
});

it("stops providing the menu's host when the Board goes away", async () => {
	const { id, tree } = await mountSwipeFleet(hub(swipeFleet()));
	expect(rowMenuHosts.get(sheetKey(id))).toBeDefined();
	act(() => tree.unmount());
	expect(rowMenuHosts.get(sheetKey(id))).toBeUndefined();
});

// The list holds still (spec 7.3, ruling 22): while a finger is on it, it
// scrolls or glides, the app scrolls it, a row's swipe or the row menu is
// open, and it applies every change at once when it settles.

/** Live's band headers and rows in screen order: a header as its text, a
 * row as its title. */
function listOrder(tree: ReactTestRenderer): string[] {
	return tree.root
		.find((node) => node.props.testID === "live-block")
		.findAll(
			(node) => node.type === BoardRow || (node.type === ("Text" as never) && node.props.testID === "band-header"),
		)
		.map((node) => (node.type === BoardRow ? node.props.item.row.title : joinedText(node)));
}
const boardRowTitled = (tree: ReactTestRenderer, title: string) =>
	tree.root.findAll((node) => node.type === BoardRow && node.props.item.row.title === title)[0];
/** Sends one of the list's touch or scroll events, as the scroller would. */
function listEvent(tree: ReactTestRenderer, handler: string, nativeEvent: Record<string, unknown> = {}) {
	act(() => boardScroller(tree).props[handler]({ nativeEvent }));
}
const liftFinger = (tree: ReactTestRenderer) => listEvent(tree, "onTouchEnd", { touches: [] });
const workingOrder = [
	"NEEDS YOU · 2",
	"Fix retry loop",
	"Pick a name",
	"FINISHED · 1",
	"Ship it",
	"WORKING · 1",
	"Build docs",
	"Idle · 2",
];
/** A Board over the default fleet whose rows can turn into questions ("Build
 * docs", the working row, unless the test names another). */
async function mountAskingFleet(nav = navigation(), withInstances = false) {
	const id = hubId();
	adoptedAnHourAgo(id);
	const shape: Fleet = { ...fleet, live: [[...fleet.live[0]]], needsYou: [...fleet.needsYou] };
	const fake = hub(shape);
	connect(id, fake.client, "ready");
	const mounted = withInstances ? await mountWithInstances(nav) : { tree: await mount(nav), scrollTo: vi.fn() };
	/** The row stops to ask a question, in the hub's next invalidation. */
	let sequence = 0;
	const ask = async (asker = working) => {
		const question = { ...asker, state: "awaiting" as const, ask_pending: true };
		shape.live[0] = shape.live[0].map((row) => (row.ref === asker.ref ? question : row));
		shape.needsYou = [...shape.needsYou, question];
		act(() =>
			fake.invalidate(++sequence, [
				{ kind: "section", section: "live" },
				{ kind: "section", section: "needs_you" },
			]),
		);
		await settle();
	};
	return { id, ...mounted, ask };
}
/** Whether "Build docs" still sits in Working, where it was. */
const heldInWorking = (tree: ReactTestRenderer) =>
	expect(listOrder(tree)).toEqual(["NEEDS YOU · 3", ...workingOrder.slice(1)]);
/** Whether "Build docs" moved into Needs you, and Working is gone. */
function movedToNeedsYou(tree: ReactTestRenderer) {
	const order = listOrder(tree);
	expect(order.indexOf("Build docs")).toBeLessThan(order.indexOf("FINISHED · 1"));
	expect(order).not.toContain("WORKING · 1");
}

it("keeps a row in its place while a finger is on the list, then moves it into Needs you, washed, 100ms after the finger lifts", async () => {
	const { tree, ask } = await mountAskingFleet();
	expect(listOrder(tree)).toEqual(workingOrder);
	listEvent(tree, "onTouchStart");
	await ask();
	heldInWorking(tree);
	// Held, the row shows what it is now.
	expect(boardRowTitled(tree, "Build docs").props.item.state).toBe("question");
	expect(texts(tree)).toContain("Question");
	liftFinger(tree);
	await advance(99);
	heldInWorking(tree);
	await advance(1);
	movedToNeedsYou(tree);
	expect(boardRowTitled(tree, "Build docs").props.wash).toBeGreaterThan(0);
	expect(boardRowTitled(tree, "Ship it").props.wash).toBe(0);
	await advance(WASH_MS);
	expect(boardRowTitled(tree, "Build docs").props.wash).toBe(0);
});

it("lets a washed row finish its fade when another row enters Needs you after it", async () => {
	const { tree, ask } = await mountAskingFleet();
	await ask();
	const wash = boardRowTitled(tree, "Build docs").props.wash;
	expect(wash).toBeGreaterThan(0);
	await advance(300);
	await ask(finished);
	expect(boardRowTitled(tree, "Ship it").props.wash).toBeGreaterThan(0);
	expect(boardRowTitled(tree, "Build docs").props.wash).toBe(wash);
	await advance(WASH_MS - 300);
	expect(boardRowTitled(tree, "Build docs").props.wash).toBe(0);
	expect(boardRowTitled(tree, "Ship it").props.wash).toBeGreaterThan(0);
});

it("waits for a fling's glide to end before applying a change", async () => {
	const { tree, ask } = await mountAskingFleet();
	listEvent(tree, "onScrollBeginDrag");
	listEvent(tree, "onScrollEndDrag");
	listEvent(tree, "onMomentumScrollBegin");
	await ask();
	await advance(1000);
	heldInWorking(tree);
	listEvent(tree, "onMomentumScrollEnd");
	movedToNeedsYou(tree);
});

it("holds the list while a chip's scroll animates, until the scroll ends", async () => {
	const { tree, scrollTo, ask } = await mountAskingFleet(navigation(), true);
	pressChip(tree, chipLabels(tree)[0]);
	expect(scrollTo).toHaveBeenLastCalledWith({ y: 0, animated: true });
	await ask();
	await advance(500);
	heldInWorking(tree);
	listEvent(tree, "onMomentumScrollEnd");
	movedToNeedsYou(tree);
});

it("holds the list while a search's project reveal scrolls, until the scroll ends", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	const shape: Fleet = {
		...fleet,
		live: [[...fleet.live[0]]],
		needsYou: [...fleet.needsYou],
		catalogs: { projects: [evenerProject()] },
		projectPages: { "evener:current": [localWork] },
	};
	const fake = hub(shape);
	connect(id, fake.client, "ready");
	const { tree, scrollTo } = await mountWithInstances(navigation());
	layOutAt(boardScroller(tree), 0, 600);
	await revealFromSearch(tree);
	await settle();
	layOutAt(revealTarget(tree), 60, 48);
	layOutAt(projectSection(tree, "projects"), 900, 400);
	expect(scrollTo).toHaveBeenLastCalledWith({ y: 900 + 60 - 0.3 * (600 - 48), animated: true });
	const question = { ...working, state: "awaiting" as const, ask_pending: true };
	shape.live[0] = shape.live[0].map((row) => (row.ref === working.ref ? question : row));
	shape.needsYou = [...shape.needsYou, question];
	act(() =>
		fake.invalidate(1, [
			{ kind: "section", section: "live" },
			{ kind: "section", section: "needs_you" },
		]),
	);
	await settle();
	await advance(500);
	heldInWorking(tree);
	listEvent(tree, "onMomentumScrollEnd");
	movedToNeedsYou(tree);
	act(() => tree.unmount());
});

it("holds the list while a row's swipe actions are open, until the row closes", async () => {
	const { tree, ask } = await mountAskingFleet();
	act(() => swipeableOf(tree, "Ship it").props.onSwipeableOpenStartDrag());
	await ask();
	await advance(1000);
	heldInWorking(tree);
	act(() => swipeableOf(tree, "Ship it").props.onSwipeableClose());
	movedToNeedsYou(tree);
});

it("holds the list while the row menu is open, until it closes", async () => {
	const nav = navigation();
	const { id, tree, ask } = await mountAskingFleet(nav);
	act(() => rowTitled(tree, "Ship it").props.onLongPress());
	expect(nav.navigate).toHaveBeenLastCalledWith("RowMenuSheet", { hubId: id, ref: "local:done", archived: false });
	await ask();
	await advance(1000);
	heldInWorking(tree);
	act(() => menuHost(id).closed());
	movedToNeedsYou(tree);
});

it("lets go of a held change when a screen is pushed over the Board, but not for its own sheet", async () => {
	const nav = navigation();
	const { tree, ask } = await mountAskingFleet(nav);
	listEvent(tree, "onTouchStart");
	await ask();
	harness.stack = sheetOverBoard;
	rerender(tree, nav);
	heldInWorking(tree);
	harness.stack = screenOverBoard;
	rerender(tree, nav);
	movedToNeedsYou(tree);
});

it("lets go of a held change when the app leaves the foreground", async () => {
	const { tree, ask } = await mountAskingFleet();
	listEvent(tree, "onTouchStart");
	await ask();
	heldInWorking(tree);
	act(() => {
		for (const listener of harness.appState) listener("inactive");
	});
	movedToNeedsYou(tree);
});

it("moves rows with the spring, and without it under Reduce Motion, when app scrolls don't animate or hold", async () => {
	const { tree } = await mountAskingFleet();
	const moving = () =>
		boardScroller(tree).findAll((node) => node.type === ("Animated.View" as never) && "layout" in node.props);
	expect(moving().length).toBeGreaterThan(0);
	for (const view of moving()) expect(view.props.layout).toBe(ROW_MOVE);

	harness.reduceMotion = true;
	const calm = await mountAskingFleet(navigation(), true);
	for (const view of calm.tree.root.findAll(
		(node) => node.type === ("Animated.View" as never) && "layout" in node.props,
	))
		expect(view.props.layout).toBeUndefined();
	pressChip(calm.tree, chipLabels(calm.tree)[0]);
	expect(calm.scrollTo).toHaveBeenLastCalledWith({ y: 0, animated: false });
	await calm.ask();
	movedToNeedsYou(calm.tree);
});

// Select mode (spec 7.1; rulings 18, 21, 22, 26).

const selectFleet = (): Fleet => ({
	...swipeFleet(),
	pins: [{ id: "release", name: "Release", count: 0 }],
	pinned: { release: [] },
	manifest: manifest({
		sources: [laptopSource, { ...parkSource, online: true }],
		sections: { live: { count: 3 }, needs_you: { count: 0 }, pin_sections: { count: 1 } },
		catalogs: catalogCounts(0, 0, 0),
	}),
});
const pressables = (tree: ReactTestRenderer, label: string) =>
	tree.root.findAll((node) => node.type === ("Pressable" as never) && node.props.accessibilityLabel === label);
/** Turns select mode on and chooses these rows. */
function select(tree: ReactTestRenderer, ...titles: string[]) {
	pressLabel(tree, "Select");
	for (const title of titles) act(() => rowTitled(tree, title).props.onPress());
}
const inSelectMode = (tree: ReactTestRenderer) => pressables(tree, "Done").length > 0;

it("leads the toolbar with Select, which puts a checkbox on every session row and the select bar in the toolbar's place", async () => {
	const nav = navigation();
	const { tree } = await mountSwipeFleet(hub(selectFleet()), nav);
	expect(pressables(tree, "New session")).toHaveLength(1);
	pressLabel(tree, "Select");
	expect(pressables(tree, "New session")).toHaveLength(0);
	expect(["Done", "Archive", "Pin", "Mark as read"].map((label) => pressables(tree, label).length)).toEqual([
		1, 1, 1, 1,
	]);
	const rows = tree.root.findAllByType(BoardRow);
	expect(rows.map((row) => row.props.selected)).toEqual([false, false, false]);
	// Select mode's rows neither swipe nor open a menu.
	expect(tree.root.findAll((node) => node.type === ("ReanimatedSwipeable" as never))).toHaveLength(0);
	expect(rowTitled(tree, "Write changelog").props.onLongPress).toBeUndefined();
	// Nothing chosen: nothing to act on.
	expect(pressables(tree, "Archive")[0].props.disabled).toBe(true);
});

it("shows no Select while the Board has no session row", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	const empty: Fleet = { ...fleet, live: [[]], needsYou: [], pins: [], pinned: {} };
	connect(id, hub(empty).client, "ready");
	const tree = await mount(navigation());
	expect(pressables(tree, "Select")).toHaveLength(0);
});

it("toggles a row's checkbox on a tap, says how many are chosen, and opens nothing", async () => {
	const nav = navigation();
	const { tree } = await mountSwipeFleet(hub(selectFleet()), nav);
	harness.announce.mockClear();
	select(tree, "Write changelog");
	expect(rowTitled(tree, "Write changelog").props.accessibilityState).toEqual({ busy: false, selected: true });
	expect(harness.announce).toHaveBeenLastCalledWith("1 selected");
	act(() => rowTitled(tree, "Park chore").props.onPress());
	expect(harness.announce).toHaveBeenLastCalledWith("2 selected");
	act(() => rowTitled(tree, "Write changelog").props.onPress());
	expect(rowTitled(tree, "Write changelog").props.accessibilityState).toEqual({ busy: false, selected: false });
	expect(harness.announce).toHaveBeenLastCalledWith("1 selected");
	expect(nav.navigate).not.toHaveBeenCalledWith("Conversation", expect.anything());
});

it("holds the list while selecting, and Done applies what changed", async () => {
	const { tree, ask } = await mountAskingFleet();
	pressLabel(tree, "Select");
	await ask();
	await advance(1000);
	heldInWorking(tree);
	pressLabel(tree, "Done");
	movedToNeedsYou(tree);
	expect(inSelectMode(tree)).toBe(false);
	// The rows swipe again.
	expect(swipeableOf(tree, "Ship it")).toBeDefined();
});

it("holds the list again when the app comes back while selecting", async () => {
	const { tree, ask } = await mountAskingFleet();
	pressLabel(tree, "Select");
	act(() => {
		for (const listener of harness.appState) listener("inactive");
	});
	act(() => {
		for (const listener of harness.appState) listener("active");
	});
	await ask();
	await advance(1000);
	heldInWorking(tree);
	pressLabel(tree, "Done");
	movedToNeedsYou(tree);
});

it("archives the chosen sessions one by one, leaves select mode, and Undo unarchives them", async () => {
	const fake = hub(selectFleet());
	const { tree } = await mountSwipeFleet(fake);
	select(tree, "Refactor parser", "Write changelog");
	pressLabel(tree, "Archive");
	await settle();
	// In the order the Board shows them: Finished, then Working.
	expect(fake.mutations).toEqual([
		{ method: "evener/archive/set", params: { kind: "session", id: OTHER_SESSION_ID, archived: true } },
		{ method: "evener/archive/set", params: { kind: "session", id: SESSION_ID, archived: true } },
	]);
	expect(inSelectMode(tree)).toBe(false);
	expect(texts(tree)).toContain("Archived 2 sessions");
	pressLabel(tree, "Undo");
	await settle();
	expect(fake.mutations.slice(2)).toEqual([
		{ method: "evener/archive/set", params: { kind: "session", id: OTHER_SESSION_ID, archived: false } },
		{ method: "evener/archive/set", params: { kind: "session", id: SESSION_ID, archived: false } },
	]);
	expect(texts(tree)).toContain("Unarchived 2 sessions");
});

it("says nothing archived when the hub can't confirm one, and holds the rest for when the journal is free", async () => {
	const fake = hub(selectFleet(), undefined, undefined, { refuse: true });
	const { tree } = await mountSwipeFleet(fake);
	select(tree, "Refactor parser", "Write changelog");
	pressLabel(tree, "Archive");
	await settle();
	expect(inSelectMode(tree)).toBe(false);
	expect(texts(tree).filter((text) => text.startsWith("Archived"))).toEqual([]);
	// The one after it was held, not dropped: it goes once the journal has
	// settled the first.
	await vi.waitFor(() =>
		expect(fake.mutations.map((mutation) => mutation.params)).toEqual([
			{ kind: "session", id: OTHER_SESSION_ID, archived: true },
			{ kind: "session", id: SESSION_ID, archived: true },
		]),
	);
});

it("pins the chosen sessions with the Board's connection as it is when you pick, not as it was when the sheet opened", async () => {
	const fake = hub(selectFleet());
	const { id, tree, nav } = await mountSwipeFleet(fake);
	select(tree, "Refactor parser");
	harness.actionSheet.mockClear();
	pressLabel(tree, "Pin");
	const [, choose] = harness.actionSheet.mock.calls[0] as [unknown, (index: number) => void];
	// The connection blips and returns while the sheet is up.
	connect(id, fake.client, "reconnecting");
	rerender(tree, nav);
	connect(id, fake.client, "ready");
	rerender(tree, nav);
	await settle();
	act(() => choose(0));
	await settle();
	expect(fake.mutations.map((mutation) => mutation.method)).toEqual(["evener/session-pin/assign"]);
});

it("pins the chosen sessions to a category picked from the sheet", async () => {
	const fake = hub(selectFleet());
	const { tree } = await mountSwipeFleet(fake);
	select(tree, "Refactor parser", "Park chore");
	harness.actionSheet.mockClear();
	pressLabel(tree, "Pin");
	const [options, choose] = harness.actionSheet.mock.calls[0] as [
		{ title: string; options: string[]; cancelButtonIndex: number },
		(index: number) => void,
	];
	expect(options).toMatchObject({
		title: "Pin to category",
		options: ["Release", "New category…", "Cancel"],
		cancelButtonIndex: 2,
	});
	act(() => choose(0));
	await settle();
	expect(fake.mutations).toEqual([
		{ method: "evener/session-pin/assign", params: { sessionRef: "paradise-park:pp", sectionId: "release" } },
		{ method: "evener/session-pin/assign", params: { sessionRef: `local:${SESSION_ID}`, sectionId: "release" } },
	]);
	expect(inSelectMode(tree)).toBe(false);
	expect(texts(tree)).toContain("Pinned 2 sessions to Release");
});

it("stays in select mode when the category sheet is cancelled", async () => {
	const fake = hub(selectFleet());
	const { tree } = await mountSwipeFleet(fake);
	select(tree, "Refactor parser");
	harness.actionSheet.mockClear();
	pressLabel(tree, "Pin");
	const choose = harness.actionSheet.mock.calls[0]?.[1] as (index: number) => void;
	act(() => choose(2));
	await settle();
	expect(fake.mutations).toEqual([]);
	expect(inSelectMode(tree)).toBe(true);
});

it("pins the chosen sessions to a new category named in the prompt", async () => {
	const fake = hub(selectFleet());
	const { tree } = await mountSwipeFleet(fake);
	select(tree, "Refactor parser", "Write changelog");
	harness.actionSheet.mockClear();
	harness.prompt.mockClear();
	pressLabel(tree, "Pin");
	act(() => (harness.actionSheet.mock.calls[0]?.[1] as (index: number) => void)(1));
	const [title, message, buttons, type] = harness.prompt.mock.calls[0] as [
		string,
		undefined,
		{ text: string; style?: string; onPress?: (name?: string) => void }[],
		string,
	];
	expect([title, message, type]).toEqual(["New category", undefined, "plain-text"]);
	expect(buttons.map((button) => [button.text, button.style])).toEqual([
		["Cancel", "cancel"],
		["Create", undefined],
	]);
	act(() => buttons[1]?.onPress?.("  Ideas "));
	await settle();
	expect(fake.mutations).toEqual([
		{ method: "evener/session-pin/assign", params: { sessionRef: `local:${OTHER_SESSION_ID}`, sectionName: "Ideas" } },
		{ method: "evener/session-pin/assign", params: { sessionRef: `local:${SESSION_ID}`, sectionName: "Ideas" } },
	]);
	expect(texts(tree)).toContain("Pinned 2 sessions to Ideas");
});

it("pins nothing to a new category whose name is too long, and says why", async () => {
	const fake = hub(selectFleet());
	const { tree } = await mountSwipeFleet(fake);
	select(tree, "Refactor parser");
	harness.actionSheet.mockClear();
	harness.prompt.mockClear();
	pressLabel(tree, "Pin");
	act(() => (harness.actionSheet.mock.calls[0]?.[1] as (index: number) => void)(1));
	const buttons = harness.prompt.mock.calls[0]?.[2] as { onPress?: (name?: string) => void }[];
	act(() => buttons[1]?.onPress?.("x".repeat(81)));
	await settle();
	expect(fake.mutations).toEqual([]);
	expect(texts(tree)).toContain("Category names can be up to 80 characters.");
});

it("marks the chosen finished sessions read and leaves select mode", async () => {
	const { id, tree } = await mountSwipeFleet(hub(selectFleet()));
	expect(bandHeaders(tree)).toEqual(["FINISHED · 2", "WORKING · 1"]);
	select(tree, "Write changelog", "Refactor parser");
	expect(pressables(tree, "Archive")[0].props.disabled).toBe(false);
	pressLabel(tree, "Mark as read");
	await settle();
	expect(inSelectMode(tree)).toBe(false);
	expect(bandHeaders(tree)).toEqual(["FINISHED · 1", "WORKING · 1", "Idle · 1"]);
	expect(menuItem(menuHost(id), `local:${OTHER_SESSION_ID}`).state).toBe("idle");
});

it("keeps the select bar's actions offline, and holds an archive of what's chosen (phase 6 ruling 18)", async () => {
	const fake = hub(selectFleet());
	const { id, tree } = await mountSwipeFleet(fake);
	connect(id, fake.client, "reconnecting");
	rerender(tree, navigation());
	select(tree, "Write changelog");
	expect(["Archive", "Pin", "Mark as read"].map((label) => pressables(tree, label)[0].props.disabled)).toEqual([
		false,
		false,
		false,
	]);
	pressLabel(tree, "Archive");
	await settle();
	expect(inSelectMode(tree)).toBe(false);
	expect(fake.mutations).toEqual([]);
	expect(texts(tree)).toContain("Archive waits for the connection");
});

// The Board's Continue reading row (spec 7.1, ruling 20).
/** A document left partway through, the trail DocumentMemory keeps for
 * the Board, written straight to its kv-store key with the time it was left. */
function leaveDocument(id: string, leftMinutesAgo: number) {
	harness.kv.set(
		`evener.native.continue-reading.${id}`,
		JSON.stringify({
			sessionRef: "local:fix",
			path: "docs/superpowers/plans/settle-race.md",
			title: "Fix the settle/drain race",
			sessionTitle: "Fix race",
			progress: 0.62,
			leftAt: Date.now() - leftMinutesAgo * 60_000,
		}),
	);
}

it("offers to continue a document you left in the last two hours, under the notices", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	leaveDocument(id, 90);
	// A notice above the Board (Update needed) shows where the row sits.
	connect(id, hub(fleet).client, "ready", { fatal: true });
	const nav = { ...navigation(), push: vi.fn() };
	const tree = await mount(nav);
	const row = tree.root.findAll(
		(node) => node.props.accessibilityLabel === "Continue reading, 62 percent, Fix the settle/drain race",
	)[0];
	if (!row) throw new Error("no Continue reading row");
	const notice = tree.root.findAll((node) => node.props.testID === "notice")[0];
	const live = tree.root.find((node) => node.props.testID === "live-block");
	if (!notice) throw new Error("no notice");
	expect(tree.root.findAll((node) => node === notice || node === row || node === live)).toEqual([notice, row, live]);
	act(() => row.props.onPress());
	expect(nav.push.mock.calls).toEqual([
		["Conversation", { hubId: id, ref: "local:fix", title: "Fix race" }],
		[
			"Reader",
			{
				hubId: id,
				sessionRef: "local:fix",
				path: "docs/superpowers/plans/settle-race.md",
				sessionTitle: "Fix race",
			},
		],
	]);
});

it("offers nothing for a document left two hours ago, or with none left", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	leaveDocument(id, 120);
	connect(id, hub(fleet).client, "ready");
	const tree = await mount(navigation());
	expect(renderedText(tree)).not.toContain("Continue reading");
	const other = hubId();
	adoptedAnHourAgo(other);
	connect(other, hub(fleet).client, "ready");
	expect(renderedText(await mount(navigation()))).not.toContain("Continue reading");
});

it("drops the Continue reading row when its two hours run out, even on an idle Board", async () => {
	vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval", "Date"] });
	try {
		const id = hubId();
		adoptedAnHourAgo(id);
		leaveDocument(id, 119);
		connect(id, hub(fleet).client, "ready");
		const tree = await mount(navigation());
		expect(renderedText(tree)).toContain("Continue reading");
		await act(async () => {
			vi.advanceTimersByTime(61_000);
		});
		expect(renderedText(tree)).not.toContain("Continue reading");
	} finally {
		vi.useRealTimers();
	}
});

// Where the device has Liquid Glass (iOS 26 and later), one glass spans the
// nav bar and the section chips under it (spec 16.3), as on the Session: the
// Board scrolls under both, its content inset by them, and every scroll it
// makes itself lands clear of them. Elsewhere, and while Reduce Transparency
// is on, the bar is opaque and the chips sit above the scroller as before.
describe("the nav bar's glass (spec 16.3)", () => {
	const glassBlock = (tree: ReactTestRenderer) =>
		tree.root.find((node) => node.props.testID === "board-header" && String(node.type) === "View");
	const measureGlass = (tree: ReactTestRenderer, height: number) =>
		act(() => glassBlock(tree).props.onLayout({ nativeEvent: { layout: { x: 0, y: 0, width: 390, height } } }));
	const fieldHeight = (tree: ReactTestRenderer) =>
		tree.root.find((node) => node.props.testID === "search-field").props.style.height;
	/** A Board on the glass, measured with the bar's room and a 48pt chip row. */
	async function mountOnGlass(shape: Fleet = { ...fleet, catalogs: { projects: [evenerProject()] } }) {
		systemGlass.available = true;
		const id = hubId();
		adoptedAnHourAgo(id);
		const fake = hub(shape);
		connect(id, fake.client, "ready");
		const nav = navigation();
		const mounted = await mountWithInstances(nav);
		await act(async () => {});
		measureGlass(mounted.tree, 64 + 48);
		return { ...mounted, nav, fake };
	}

	it("runs the Board under one glass spanning the bar and the chips, and scrolls clear of it", async () => {
		systemGlass.available = true;
		const id = hubId();
		adoptedAnHourAgo(id);
		connect(id, hub({ ...fleet, catalogs: { projects: [evenerProject()] } }).client, "ready");
		const nav = navigation();
		const { tree, scrollTo } = await mountWithInstances(nav);
		await act(async () => {});
		expect(headerOptions(nav)).toMatchObject({
			headerTransparent: true,
			headerStyle: { backgroundColor: "transparent" },
			scrollEdgeEffects: { top: "hidden" },
		});
		// The chips sit on the glass, clear, below the bar's room.
		const block = glassBlock(tree);
		expect(block.props.style).toMatchObject({ position: "absolute", top: 0, left: 0, right: 0 });
		expect(block.findAll((node) => String(node.type) === "GlassView")).toHaveLength(1);
		expect(block.find((node) => node.props.testID === "nav-bar-room").props.style.height).toBe(64);
		expect(
			block.find((node) => node.props.testID === "chips" && String(node.type) === "View").props.style.backgroundColor,
		).toBe("transparent");
		const height = tree.root.find((node) => node.props.testID === "search-field").props.style.height;
		// Before the glass has measured, the Board is inset by the bar's room,
		// and the field it keeps tucked stays tucked just under the glass.
		expect(boardScroller(tree).props.contentInset).toMatchObject({ top: 64 });
		expect(scrollTo).toHaveBeenLastCalledWith({ y: height - 64, animated: false });
		measureGlass(tree, 64 + 48);
		expect(boardScroller(tree).props.contentInset).toMatchObject({ top: 112 });
		expect(boardScroller(tree).props.scrollIndicatorInsets).toMatchObject({ top: 112 });
		expect(scrollTo).toHaveBeenLastCalledWith({ y: height - 112, animated: false });
		// A chip's section lands just under the glass.
		act(() =>
			tree.root
				.find((node) => node.props.testID === "project-section:projects" && node.props.onLayout)
				.props.onLayout({ nativeEvent: { layout: { x: 0, y: 752, width: 390, height: 48 } } }),
		);
		pressChip(tree, "Projects, 4 projects");
		expect(scrollTo).toHaveBeenLastCalledWith({ y: 752 - 112, animated: true });
		// Search brings the field down under the glass.
		act(() => headerOptions(nav).unstable_headerRightItems({})[0].onPress());
		expect(scrollTo).toHaveBeenLastCalledWith({ y: -112, animated: true });
	});

	it("keeps the opaque bar and the chips above the scroller without the glass, following Reduce Transparency", async () => {
		// The Board as it is where the device has no glass.
		const layoutOf = async () => {
			const id = hubId();
			adoptedAnHourAgo(id);
			connect(id, hub(fleet).client, "ready");
			const nav = navigation();
			const tree = await mount(nav);
			await act(async () => {});
			const { contentOffset, contentInset, scrollIndicatorInsets, contentContainerStyle } = boardScroller(tree).props;
			return {
				nav,
				tree,
				layout: {
					scroller: { contentOffset, contentInset, scrollIndicatorInsets, contentContainerStyle },
					header: glassBlock(tree).props.style,
					glass: glassBlock(tree).findAll((node) => String(node.type) === "GlassView").length,
					chipsInScroller: boardScroller(tree).findAll(
						(node) => node.props.testID === "chips" && String(node.type) === "View",
					).length,
					chipsFill: tree.root.find((node) => node.props.testID === "chips" && String(node.type) === "View").props.style
						.backgroundColor,
				},
			};
		};
		const withoutGlass = await layoutOf();
		expect(withoutGlass.layout.glass).toBe(0);
		expect(withoutGlass.layout.chipsInScroller).toBe(0);
		systemGlass.available = true;
		systemGlass.setReduceTransparency(true);
		const { nav, layout } = await layoutOf();
		expect(headerOptions(nav)).toMatchObject({ headerTransparent: false, scrollEdgeEffects: { top: "automatic" } });
		expect(layout).toEqual(withoutGlass.layout);
	});

	it("starts the Board just under a glass already known when it mounts", async () => {
		// A Board already following Reduce Transparency makes it known to the
		// next one from its first render.
		const first = await mountOnGlass();
		const { tree, scrollTo } = await mountOnGlass();
		expect(scrollTo).toHaveBeenLastCalledWith({ y: fieldHeight(tree) - 112, animated: false });
		act(() => first.tree.unmount());
	});

	it("reads Live's next page by what shows below the glass", async () => {
		const { tree, fake } = await mountOnGlass({
			...fleet,
			live: [[failing, working], [finished]],
			catalogs: { projects: [evenerProject()] },
		});
		const scroller = boardScroller(tree);
		act(() => {
			scroller.props.onLayout({ nativeEvent: { layout: { x: 0, y: 0, width: 390, height: 700 } } });
			tree.root
				.find((node) => node.props.testID === "live-block")
				.props.onLayout({ nativeEvent: { layout: { x: 0, y: 0, width: 390, height: 1600 } } });
		});
		await settle();
		const scrollAt = (y: number) =>
			act(() =>
				scroller.props.onScroll({
					nativeEvent: {
						contentOffset: { x: 0, y },
						contentSize: { width: 390, height: 1600 },
						layoutMeasurement: { width: 390, height: 700 },
					},
				}),
			);
		// Below the glass shows 700 - 112 of the Board from 112 past the
		// scroller's offset: two screens of that from 300 end short of Live's.
		scrollAt(300);
		await settle();
		expect(liveReads(fake)).toEqual([0]);
		scrollAt(312);
		await settle();
		expect(liveReads(fake)).toEqual([0, 2]);
	});

	it("lands Search's reveal under the glass the chips leave when search starts", async () => {
		const { tree, nav, scrollTo } = await mountOnGlass();
		act(() => headerOptions(nav).unstable_headerRightItems({})[0].onPress());
		expect(scrollTo).toHaveBeenLastCalledWith({ y: -112, animated: true });
		// The field's focus starts search, which hides the chips: the glass
		// shrinks to the bar while the reveal is still under way.
		searchField(tree).focus();
		// The reveal is still under way, reporting where it has got to so far.
		act(() =>
			boardScroller(tree).props.onScroll({
				nativeEvent: {
					contentOffset: { x: 0, y: -40 },
					contentSize: { width: 390, height: 1600 },
					layoutMeasurement: { width: 390, height: 700 },
				},
			}),
		);
		measureGlass(tree, 64);
		expect(scrollTo).toHaveBeenLastCalledWith({ y: -64, animated: true });
	});

	it("tucks the field back under the glass on Cancel as the chips return", async () => {
		const { tree, scrollTo } = await mountOnGlass();
		searchField(tree).focus();
		measureGlass(tree, 64);
		listEvent(tree, "onMomentumScrollEnd");
		searchField(tree).cancel();
		expect(scrollTo).toHaveBeenLastCalledWith({ y: fieldHeight(tree) - 64, animated: true });
		measureGlass(tree, 64 + 48);
		expect(scrollTo).toHaveBeenLastCalledWith({ y: fieldHeight(tree) - 112, animated: true });
	});

	it("reveals a project from search a third of the way down what shows below the glass", async () => {
		const { tree, scrollTo } = await mountOnGlass({
			...fleet,
			catalogs: { projects: [evenerProject()] },
			projectPages: { "evener:current": [localWork] },
		});
		layOutAt(boardScroller(tree), 0, 600);
		await revealFromSearch(tree);
		await settle();
		// Leaving search brings the chips back to the glass.
		measureGlass(tree, 64 + 48);
		layOutAt(revealTarget(tree), 60, 48);
		layOutAt(projectSection(tree, "projects"), 900, 400);
		expect(scrollTo).toHaveBeenLastCalledWith({ y: 900 + 60 - 0.3 * (600 - 112 - 48) - 112, animated: true });
	});
});
