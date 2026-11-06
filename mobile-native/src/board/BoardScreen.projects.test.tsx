import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { DocumentMemory } from "../reader/documentMemory";
// Imported at file scope, so the Reader's graph is transformed at collection
// time, outside the timeout of the test that opens a real Reader.
// The Board screen mounted with only its native edges mocked: the navigation
// reads go through the real BoardController to a fake hub that answers by
// params, and the device memory is the real SeenMarkers over an in-memory
// kv-store.
import type {
	AnyNotification,
	AppwireClientLike,
	ArchivedListParams,
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
import { act } from "react-test-renderer";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { ConversationClientLike } from "../conversation/services/conversation";
import {
	alertRequests,
	render,
	screenConnection,
	settleMicrotasks,
	systemGlass,
	unmountMountedTrees,
	until,
} from "../renderNative.testkit";
import { markers } from "../markerTestUtils";
import { ACTIVITY_POLL_MS, STALE_AFTER_MS } from "./activityPoll";
import { BoardScreen } from "./BoardScreen";
import { SearchResults } from "./SearchResults";
import { SwipeRow } from "./SwipeRow";
import { boardHoldKey, type HeldRecord } from "./boardHold";
import { PulseMeter } from "./PulseMeter";
import { hubSeenMarks } from "./hubSeen";
import { seenMarkers } from "./nativeBoardMemory";
import {
	INCOMPATIBLE_TEXT,
	boardAlone,
	boardScroller,
	chipLabels,
	dotted,
	hasCancel,
	hasRow,
	isRowTitled,
	layOutAt,
	menuLabels,
	projectSection,
	resultTitled,
	revealTarget,
	rowTitled,
	screenOverBoard,
	skeletonRows,
	stateOf,
	swipeableOf,
	texts,
	textsIn,
	whyKind,
	whyReason,
} from "./boardScreenTestUtils";

const harness = vi.hoisted(() => ({
	connection: {} as Record<string, unknown>,
	readerMemory: null as DocumentMemory | null,
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
			currentState: "active",
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
vi.mock("react-native-enriched-markdown", () => ({ EnrichedMarkdownText: "EnrichedMarkdownText" }));

vi.mock("expo-clipboard", () => ({ setStringAsync: async () => {} }));

vi.mock("expo-secure-store", () => ({
	getItemAsync: async (key: string) =>
		JSON.stringify({
			id: key.replace("evener.hub.", ""),
			name: "Owner",
			origin: "https://unused.test",
			token: "reader-test-token",
		}),
}));

// A process relaunch replaces only the singleton, with real DocumentMemory
// re-reading unchanged platform storage. No trail or callback result is canned.
vi.mock("../reader/nativeDocumentMemory", async (importOriginal) => {
	const actual = await importOriginal<typeof import("../reader/nativeDocumentMemory")>();
	return { ...actual, documentMemory: (id: string) => harness.readerMemory ?? actual.documentMemory(id) };
});

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
	harness.readerMemory = null;
});

function setFocused(focused: boolean) {
	harness.focused = focused;
	act(() => {
		for (const listener of harness.focusListeners) listener(focused);
	});
}

afterEach(() => {
	unmountMountedTrees();
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

const shipIt = session("local:done", { title: "Ship it", updated_at: minutesAgo(4) });

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
	/** Each project tier's sessions, keyed `${projectKey}:${tier}`, paged by the
	 * read's limit. As on the hub, navigation serves the archived tier empty and
	 * evener/archived/list serves its rows. */
	projectPages?: Record<string, NavigationSessionSummary[]>;
}

type ProjectCatalogName = "projects" | "archived_projects" | "test_runs";

const keptNote = session("local:kept", { title: "Kept note", live: false, updated_at: minutesAgo(600) });

const oldPlan = session("local:plan", { title: "Old plan", live: false, updated_at: minutesAgo(900) });

const releaseNotes = session("local:notes", { title: "Release notes", live: false, updated_at: minutesAgo(1200) });

const fleet: Fleet = {
	// The ask is in the hub's needs_you section only: bands union it.
	live: [[failing, working, shipIt, idleOne, idleTwo]],
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
	const archivedReads: ArchivedListParams[] = [];
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
			const rows =
				params.tier === "archived" ? [] : (shape.projectPages?.[`${params.projectKey}:${params.tier}`] ?? []);
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
					if (method === "evener/archived/list") {
						// The cursor is the offset of the next page.
						const read = params as ArchivedListParams;
						archivedReads.push(read);
						const rows = shape.projectPages?.[`${read.projectKey}:archived`] ?? [];
						const offset = Number(read.cursor ?? 0);
						const page = rows.slice(offset, offset + (read.limit || 50));
						const next = offset + page.length;
						resolve({
							sessions: page,
							total: rows.length,
							...(next < rows.length ? { nextCursor: String(next) } : {}),
						} as never);
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
		archivedReads,
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

/** What the Board holds for hub `id`, as it stored it. */
const heldIn = (id: string) => JSON.parse(harness.kv.get(boardHoldKey(id)) ?? "[]") as HeldRecord[];

async function mount(nav: Navigation) {
	const tree = render(screen(nav));
	await settleMicrotasks();
	return tree;
}

function rerender(tree: ReactTestRenderer, nav: Navigation) {
	act(() => tree.update(screen(nav)));
}

function joinedText(node: ReactTestInstance): string {
	return node.children.map((child) => (typeof child === "string" ? child : joinedText(child))).join("");
}

/** Presses the one control with this label, leaving out the chips, which
 * share their labels with the section rows they scroll to. */
function pressLabel(tree: ReactTestRenderer, label: string) {
	const target = tree.root.find(
		(node) =>
			node.type === ("Pressable" as never) && node.props.accessibilityLabel === label && node.props.testID !== "chip",
	);
	act(() => target.props.onPress());
}

/** Mounts the Board with instances for its scroller and search field, the
 * way the chip jumps and the Search item reach them. */
async function mountWithInstances(nav: Navigation) {
	const scrollTo = vi.fn();
	const focus = vi.fn();
	const blur = vi.fn();
	const tree = render(screen(nav), {
		createNodeMock: (element) => (element.type === ("TextInput" as never) ? { focus, blur } : { scrollTo }),
	});
	await settleMicrotasks();
	return { tree, scrollTo, focus, blur };
} /** The search field at the top of the Board's scroller, driven the way a
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

// Rows the hub decides (S4): each carries its turn end, and the hub says
// whether it is unseen. The device's markers say the opposite of the hub for
// both: its epoch covers the first, and the second updated after it.
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

// A device adopted an hour ago (adoptedAnHourAgo) reads Hub seen as unseen.
const hubFleet: Fleet = {
	...fleet,
	live: [[failing, working, hubUnseen, hubSeen, shipIt]],
	pinned: { ...fleet.pinned, "pins-1": [pinnedUnseen, keptNote] },
};

async function advance(ms: number) {
	act(() => {
		vi.advanceTimersByTime(ms);
	});
	await settleMicrotasks();
}

const migrating = session("local:migrate", { title: "Migrate schema", state: "active", updated_at: minutesAgo(1) });

const tidying = session("local:tidy", { title: "Tidy imports", state: "active", updated_at: minutesAgo(1) });

const busyFleet: Fleet = {
	...fleet,
	live: [[failing, { ...working, subagents: { running: 1, failed: 0, done: 0 } }, tidying, migrating, shipIt]],
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
	expect(whyKind(tree, "Build docs")).toBe("subagents");
	// The read's three, not the row's own one.
	expect(whyReason(building)).toContain("3");
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

// The Board holds no transcript of its own, so a working row's words about the
// work come from the hub's activity read, which carries the daemon's own latest
// tool intent (the agent states one for every call). A session whose read names
// none keeps the bare state word.
it("words a working row from the session's latest tool intent, where it would read Working", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	const shape: Fleet = {
		...busyFleet,
		activity: [
			{ ref: "local:tidy", minutes: [1, 0, 0], runningSubagents: 0, latestIntent: "Reading the board's row tests." },
			{ ref: "local:migrate", minutes: [0, 0, 0], runningSubagents: 0 },
		],
	};
	const fake = hub(shape);
	connect(id, fake.client, "ready");
	const tree = await mount(navigation());
	expect(textsIn(rowTitled(tree, "Tidy imports"))).toContain("Reading the board's row tests.");
	expect(whyKind(tree, "Migrate schema")).toBe("state");
	act(() => tree.unmount());
});

// The session's own words lead the job it is running, because they say what
// the job is for. A read that states none still leaves the job to say itself.
it("shows the latest tool intent over the job a working row is running", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	const rebuilding = session("local:rebuild", {
		title: "Rebuild index",
		state: "active",
		running_job_count: 1,
		running_job_command: "go test ./agent/...",
		updated_at: minutesAgo(1),
	});
	const shape: Fleet = {
		...busyFleet,
		live: [[rebuilding]],
		activity: [
			{ ref: "local:rebuild", minutes: [1, 0, 0], runningSubagents: 0, latestIntent: "Reading the board's row tests." },
		],
	};
	const fake = hub(shape);
	connect(id, fake.client, "ready");
	const tree = await mount(navigation());
	const row = rowTitled(tree, "Rebuild index");
	expect(textsIn(row)).toContain("Reading the board's row tests.");
	expect(textsIn(row)).not.toContain("Running go test ./agent/...");
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
	expect(whyKind(tree, "Migrate schema")).toBe("state");
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
	expect(whyKind(tree, "Migrate schema")).toBe("state");
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
	expect(whyKind(tree, "Migrate schema")).toBe("state");

	await settleMicrotasks();
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
	expect(whyKind(tree, "Migrate schema")).toBe("state");
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

it("keeps a fresh read through leaving front, so coming back shows each row's activity at once", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	const shape: Fleet = { ...busyFleet, activity: [migrateRead(4)] };
	const fake = hub(shape);
	connect(id, fake.client, "ready");
	const nav = navigation();
	const tree = await mount(nav);
	expect(textsIn(rowTitled(tree, "Migrate schema"))).toContain("Quiet 4m");

	harness.stack = screenOverBoard;
	rerender(tree, nav);
	await settleMicrotasks();
	// Back within the read's freshness window, with the first read after the
	// return failing: the read from before still shows, with no round trip of
	// fallback lines.
	shape.activity = null;
	harness.stack = boardAlone;
	rerender(tree, nav);
	await settleMicrotasks();
	expect(fake.activityReads).toHaveLength(2);
	expect(textsIn(rowTitled(tree, "Migrate schema"))).toContain("Quiet 4m");
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
	expect(whyKind(tree, "Migrate schema")).toBe("state");
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
	expect(whyKind(tree, "Tidy imports")).toBe("state");
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
	await settleMicrotasks();
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
	await settleMicrotasks();
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
	expect(markers(tree, "empty-state")).not.toContain("firstReadFailed");
	act(() => tree.unmount());
});

it("says nothing when the notice read fails, and doesn't retry the Board over it", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	const fake = hub({ ...troubledFleet(), noticesFail: true });
	connect(id, fake.client, "ready");
	const tree = await mount(navigation());
	expect(noticeTexts(tree)).toEqual([]);
	expect(markers(tree, "empty-state")).not.toContain("firstReadFailed");
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
	tree.root
		.findAll((node) => typeof node.props.testID === "string" && node.props.testID.startsWith("project-section:"))
		.map((node) => node.props.testID.slice("project-section:".length));

const organizeControls = (tree: ReactTestRenderer) =>
	tree.root.findAll(
		(node) =>
			node.type === ("Pressable" as never) &&
			typeof node.props.accessibilityLabel === "string" &&
			node.props.accessibilityLabel.startsWith("Organize by: "),
	);

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

const pageReads = (fake: ReturnType<typeof hub>) => [
	...fake.requests.filter((read) => read.resource === "project_page").map((read) => `${read.tier}@${read.offset ?? 0}`),
	...fake.archivedReads.map((read) => `archived list ${read.catalog}@${read.cursor ?? 0}`),
];

const rowOpacity = (node: ReactTestInstance) =>
	(typeof node.props.style === "function" ? node.props.style({ pressed: false }) : node.props.style).opacity ?? 1;

it("offers Organize by only once the hub names a second host, and saves the choice", async () => {
	const single = hubId();
	adoptedAnHourAgo(single);
	connect(single, hub({ ...fleet, catalogs: { projects: [evenerProject()] } }).client, "ready");
	const alone = await mount(navigation());
	expect(sectionHeaders(alone)).toEqual(["projects"]);
	expect(organizeControls(alone)).toEqual([]);
	act(() => alone.unmount());

	const id = hubId();
	adoptedAnHourAgo(id);
	connect(id, hub({ ...fleet, manifest: twoHosts(), catalogs: { projects: [evenerProject()] } }).client, "ready");
	const tree = await mount(navigation());
	expect(sectionHeaders(tree)).toEqual(["projects"]);
	const [control] = organizeControls(tree);
	expect(control.props.accessibilityLabel).toBe("Organize by: Project, then host");
	expect(control.props.accessibilityHint).toBe("Changes to Host, then project");
	expect(textsIn(control)).toEqual(["Project, then host"]);
	expect(control.findAll((node) => node.type === ("SymbolView" as never)).map((node) => node.props.name)).toEqual([
		"arrow.left.arrow.right",
	]);
	act(() => control.props.onPress());
	expect(sectionHeaders(tree)).toEqual(["projects"]);
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
		expect.any(String),
		expect.any(String),
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
	await settleMicrotasks();
	expect(pageReads(fake).sort()).toEqual(["archived list projects@0", "current@0", "recent@0"]);
	expect(hasRow(tree, "Local work")).toBe(true);
	pressLabel(tree, "evener");
	await settleMicrotasks();
	expect(hasRow(tree, "Local work")).toBe(false);
	pressLabel(tree, "evener");
	await settleMicrotasks();
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

it("shifts a depth-1 project row's swipe band by its indent, keeping only the screen's 24 points back", async () => {
	const flatId = hubId();
	adoptedAnHourAgo(flatId);
	connect(
		flatId,
		hub({ ...fleet, catalogs: { projects: [evenerProject()] }, projectPages: { "evener:current": [localWork] } })
			.client,
		"ready",
	);
	const flatTree = await mount(navigation());
	pressLabel(flatTree, "evener");
	await settleMicrotasks();
	expectSwipeBand(flatTree, "Local work", { marginLeft: 16, hitSlop: { left: -8 }, edgeInset: 16 });
	act(() => flatTree.unmount());
});

it("leaves a depth-2 project row's swipeable past the screen's 24-point band", async () => {
	// Host, then project, then the session: 32pt in, already past the band.
	const deepId = hubId();
	adoptedAnHourAgo(deepId);
	harness.kv.set(`evener.native.board-organize.${deepId}`, JSON.stringify("host-project"));
	const shared = evenerProject({ sources: ["local", "paradise-park"], default_expanded: true });
	connect(
		deepId,
		hub({
			...fleet,
			manifest: twoHosts(),
			catalogs: { projects: [shared] },
			projectPages: { "evener:current": [localWork, parkWork] },
		}).client,
		"ready",
	);
	const deepTree = await mount(navigation());
	expectSwipeBand(deepTree, "Local work", { marginLeft: 32, hitSlop: undefined, edgeInset: 32 });
	act(() => deepTree.unmount());
});

it("dots a project's session rows by the hub's seen marker too (S4)", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	const fake = hub({
		...hubFleet,
		catalogs: { projects: [evenerProject()] },
		projectPages: {
			"evener:current": [
				// The device's first run predates this ref; the hub says seen.
				session("local:hub-seen", {
					title: "Project hub seen",
					updated_at: minutesAgo(6),
					turn_ended_at: minutesAgo(6),
					unseen: false,
				}),
				// The device's epoch covers this one; the hub says unseen.
				session("local:project-unseen", {
					title: "Project hub unseen",
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
	await settleMicrotasks();
	expect(stateOf(tree, "Project hub seen")).toBe("idle");
	expect(stateOf(tree, "Project hub unseen")).toBe("idle");
	expect(dotted(tree, "Project hub seen")).toBe(false);
	expect(dotted(tree, "Project hub unseen")).toBe(true);
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
	await settleMicrotasks();
	act(() => rowTitled(tree, "Project hub unseen").props.onPress());
	await settleMicrotasks();
	expect(fake.seen).toEqual([[{ ref: "local:project-unseen", seenThrough: Date.parse(minutesAgo(90)) }]]);
	expect(stateOf(tree, "Project hub unseen")).toBe("idle");
	// The project's page catches up, and the pending mark goes: a later
	// unseen for the same turn would show again. This fake hub answers every
	// read at revision 1, so the change names none.
	shape.projectPages = { "evener:current": [{ ...projectUnseen, unseen: false }] };
	act(() => fake.invalidate(1, [{ kind: "project", projectKey: "evener" }]));
	await settleMicrotasks();
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
	await act(async () => result.props.onPress());
}

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
	await settleMicrotasks();
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
	expect(sectionHeaders(tree)).toEqual(["projects", "test-runs", "archived"]);
	expect(catalogReads(fake)).toEqual(["projects"]);
	expect(projectRows(tree, "hub-test-env")).toHaveLength(0);
	pressLabel(tree, "Test runs, 3 projects");
	await settleMicrotasks();
	expect(catalogReads(fake)).toEqual(["projects", "test_runs"]);
	expect(projectRows(tree, "hub-test-env")).toHaveLength(1);
	pressLabel(tree, "Archived, 271 projects");
	await settleMicrotasks();
	expect(catalogReads(fake)).toEqual(["projects", "test_runs", "archived_projects"]);
	expect(projectRows(tree, "old-site")).toHaveLength(1);
	// Folded again and reopened, a section reads nothing more.
	pressLabel(tree, "Test runs, 3 projects");
	pressLabel(tree, "Test runs, 3 projects");
	await settleMicrotasks();
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

it("omits the project section before its manifest lands and when its catalog is empty", async () => {
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
	await settleMicrotasks();
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
	await settleMicrotasks();
	expect(projectRows(tree, "evener")).toHaveLength(1);
	expect(hasRow(tree, "Local work")).toBe(true);
	let holding = true;
	const next = hub(shape, () => holding);
	connect(id, next.client, "ready");
	rerender(tree, nav);
	await settleMicrotasks();
	expect(catalogReads(next)).toEqual(["projects"]);
	expect(projectRows(tree, "evener")).toHaveLength(1);
	expect(hasRow(tree, "Local work")).toBe(true);
	holding = false;
	next.release();
	await settleMicrotasks();
	expect(pageReads(next).sort()).toEqual(["archived list projects@0", "current@0", "recent@0"]);
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
	await settleMicrotasks();
	expect(fake.mutations).toEqual([
		{ method: "evener/favorite/set", params: { kind: "project", id: "evener", favorited: true } },
	]);
	// While the change is out, the project dims and every organization action hides.
	expect(rowOpacity(projectRows(tree, "evener")[0])).toBe(0.5);
	expect(menuLabels(tree)).toEqual([]);
	fake.release();
	await settleMicrotasks();
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
	await settleMicrotasks();
	expect(rowOpacity(projectRows(tree, "evener")[0])).toBe(0.5);
	const [held, undo] = openMenu();
	expect(held.options).toEqual(["Unpin", "Archive project", "Cancel"]);
	act(() => undo(0));
	await settleMicrotasks();
	expect(rowOpacity(projectRows(tree, "evener")[0])).toBe(1);
	expect(heldIn(id)).toEqual([]);
	connect(id, fake.client, "ready");
	rerender(tree, nav);
	await settleMicrotasks();
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
	await settleMicrotasks();
	// The menu, opened offline, offers Unpin, and stays up across the
	// reconnect while the held Pin to top goes out.
	const [, unpin] = openMenu();
	connect(id, fake.client, "ready");
	rerender(tree, nav);
	await until(() => expect(favorites).toBe(1));
	act(() => unpin(0));
	await settleMicrotasks();
	// The Pin to top is still out: the Unpin waits in the hold behind it.
	expect(favorites).toBe(1);
	expect(heldIn(id)).toMatchObject([
		{ action: { kind: "project", action: "pin" } },
		{ action: { kind: "project", action: "unpin" } },
	]);
	answerPin();
	await until(() =>
		expect(fake.mutations.filter((m) => m.method === "evener/favorite/set").map((m) => m.params)).toEqual([
			{ kind: "project", id: "evener", favorited: true },
			{ kind: "project", id: "evener", favorited: false },
		]),
	);
	await until(() => expect(heldIn(id)).toEqual([]));
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
	act(() => choose(sheet.options.indexOf("Archive project")));
	await settleMicrotasks();
	expect(heldIn(id)).toMatchObject([{ action: { kind: "project", action: "archive" } }]);
	answerFirst();
	await until(() =>
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
	await settleMicrotasks();
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
	await settleMicrotasks();
	expect(pageReads(fake)).not.toContain("recent@20");
	// Scrolled so only a quarter of the row shows: still nothing.
	act(() =>
		scroller.props.onScroll({
			nativeEvent: { contentOffset: { x: 0, y: 1611 - 700 }, layoutMeasurement: { width: 390, height: 700 } },
		}),
	);
	await settleMicrotasks();
	expect(pageReads(fake)).not.toContain("recent@20");
	// Half of it on screen.
	act(() =>
		scroller.props.onScroll({
			nativeEvent: { contentOffset: { x: 0, y: 1622 - 700 }, layoutMeasurement: { width: 390, height: 700 } },
		}),
	);
	await settleMicrotasks();
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
	await settleMicrotasks();
	searchField(tree).focus();
	// Search results now cover where the more row last sat.
	act(() =>
		boardScroller(tree).props.onScroll({
			nativeEvent: { contentOffset: { x: 0, y: 1622 - 700 }, layoutMeasurement: { width: 390, height: 700 } },
		}),
	);
	await settleMicrotasks();
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
	const nav = navigation();
	const tree = await mount(nav);
	expect(sectionHeaders(tree)).toEqual(["projects", "archived"]);
	const projects = () => tree.root.findByProps({ accessibilityRole: "button", accessibilityLabel: "Projects" });
	expect(projects().props.accessibilityState.expanded).toBe(true);
	expect(projectRows(tree, "evener")).toHaveLength(1);
	act(() => projects().props.onPress());
	expect(projects().props.accessibilityState.expanded).toBe(false);
	expect(projectRows(tree, "evener")).toHaveLength(0);
	act(() => projects().props.onPress());
	expect(projects().props.accessibilityState.expanded).toBe(true);
	expect(projectRows(tree, "evener")).toHaveLength(1);
	expect(nav.navigate).not.toHaveBeenCalled();
	expect(nav.dispatch).not.toHaveBeenCalled();
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
	await settleMicrotasks();
	setFocused(true);
	await settleMicrotasks();
	expect(recentReads()).toBe(2);
	expect(texts(tree)).toContain(COULDNT_LOAD);
	recentFails = false;
	pressLabel(tree, "evener");
	await settleMicrotasks();
	pressLabel(tree, "evener");
	await settleMicrotasks();
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
	expect(sectionHeaders(tree)).toEqual(["projects"]);
	expect(projectRows(tree, "evener")).toHaveLength(0);
	catalogFails = false;
	setFocused(false);
	await settleMicrotasks();
	setFocused(true);
	await settleMicrotasks();
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
		await settleMicrotasks();
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
	await settleMicrotasks();
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
	await settleMicrotasks();
	expect(projectCatalogPages(fake)).toEqual([0]);
	// The row's top half is on screen.
	act(() =>
		scroller.props.onScroll({
			nativeEvent: { contentOffset: { x: 0, y: 3022 - 700 }, layoutMeasurement: { width: 390, height: 700 } },
		}),
	);
	await settleMicrotasks();
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
	await settleMicrotasks();
	expect(fake.mutations).toEqual([
		{
			method: "evener/archive/set",
			params: { kind: "project", id: "evener", workingDir: "/home/jesse/git/evener", archived: true },
		},
	]);
	expect(rowOpacity(projectRows(tree, "evener")[0])).toBe(0.5);
	expect(menuLabels(tree)).toEqual([]);
	fake.release();
	await settleMicrotasks();
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

/** The SwipeRow around the Board row with this title. */
function swipeRowOf(tree: ReactTestRenderer, title: string) {
	const row = tree.root.findAllByType(SwipeRow).find((node) => node.findAll(isRowTitled(title)).length > 0);
	if (!row) throw new Error(`no swipe row for ${title}`);
	return row;
}

/** A Board row's swipe geometry: both hit frames, its wrapper's indent and
 * the inset the Board passed down. */
function expectSwipeBand(
	tree: ReactTestRenderer,
	title: string,
	expected: { marginLeft: number; hitSlop: { left: number } | undefined; edgeInset: number },
) {
	const swipeable = swipeableOf(tree, title);
	const tracker = swipeable.props.simultaneousWithExternalGesture as { config: { hitSlop?: unknown } };
	expect(swipeable.props.hitSlop).toEqual(expected.hitSlop);
	expect(tracker.config.hitSlop).toEqual(expected.hitSlop);
	expect(rowWrapper(swipeable).props.style).toMatchObject({ marginLeft: expected.marginLeft });
	expect(swipeRowOf(tree, title).props.edgeInset).toBe(expected.edgeInset);
}

/** The indented View around a Board row's swipeable, for its marginLeft. */
function rowWrapper(swipeable: ReactTestInstance) {
	let wrapper = swipeable.parent;
	while (wrapper && wrapper.props.style?.marginLeft === undefined) wrapper = wrapper.parent;
	if (!wrapper) throw new Error("no indented row wrapper");
	return wrapper;
}
