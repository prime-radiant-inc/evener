import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { once } from "node:events";
import { createServer } from "node:http";
import { DocumentMemory } from "../reader/documentMemory";
// Imported at file scope, so the Reader's graph is transformed at collection
// time, outside the timeout of the test that opens a real Reader.
import { ReaderScreen } from "../reader/ReaderScreen";
import { FlatList } from "react-native";
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
import { WireError } from "@evener/appwire-client";
import { manifest, wireSnapshot } from "@evener/appwire-client/testing/navigation";
import type { ReactTestInstance, ReactTestRenderer } from "react-test-renderer";
import { act } from "react-test-renderer";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ConversationClientLike } from "../conversation/services/conversation";
import {
	alertRequests,
	playedHaptics,
	render,
	renderedText,
	screenConnection,
	settleMicrotasks,
	swipeableCalls,
	swipeRowFully,
	systemGlass,
	unmountMountedTrees,
	until,
	untilRendered,
} from "../renderNative.testkit";
import { hasTestID, markedNodes } from "../markerTestUtils";
import { sheetKey } from "../sheet/sheetHosts";
import { ROW_MOVE } from "./boardMotion";
import { BoardRow } from "./BoardRow";
import { BoardScreen } from "./BoardScreen";
import { ContinueReadingRow } from "./ContinueReadingRow";
import { boardHoldKey, type HeldRecord } from "./boardHold";
import { hubSeenMarks } from "./hubSeen";
import { forgetBoardForHub } from "./nativeBoardMemory";
import { SESSION_ID } from "./organizationTestUtils";
import { type RowMenuHost, rowMenuHosts } from "./RowMenu";
import { WASH_MS } from "./settledList";
import {
	boardScroller,
	chipLabels,
	hasRow,
	layOutAt,
	liftFinger,
	listEvent,
	listOrder,
	menuHost,
	pressChip,
	projectSection,
	revealTarget,
	rowTitled,
	screenOverBoard,
	sheetOverBoard,
	stateOf,
	swipeableOf,
	toasts,
	whyKind,
	workingOrder,
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

/** A device that unfolded Idle before, so the sessions resting there, seen
 * or not, are on screen from the first read. */
function idleUnfolded(hub: string) {
	harness.kv.set(`evener.native.board-sections.${hub}`, JSON.stringify({ idle: false }));
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

/** The titles of the rows whose second line says a held change waits on them. */
const waitingTitles = (tree: ReactTestRenderer) =>
	markedNodes(tree, "board-row")
		.filter((row) => hasTestID(row, "row-waiting"))
		.map((row) => row.props.accessibilityLabel.split(", ")[0]);

/** How many Continue reading rows the Board shows. */
const continueReadingRows = (tree: ReactTestRenderer) => tree.root.findAllByType(ContinueReadingRow).length;

/** The controls the showing toast offers. */
const toastActions = (tree: ReactTestRenderer) =>
	markedNodes(tree, "toast").flatMap((toast) =>
		toast.findAll((node) => node.type === ("Pressable" as never)).map((node) => node.props.accessibilityLabel),
	);

/** Presses the one control with this label, leaving out the chips, which
 * share their labels with the section rows they scroll to. */
function pressLabel(tree: ReactTestRenderer, label: string) {
	const target = tree.root.find(
		(node) =>
			node.type === ("Pressable" as never) && node.props.accessibilityLabel === label && node.props.testID !== "chip",
	);
	act(() => target.props.onPress());
}

const headerOptions = (nav: Navigation) => nav.setOptions.mock.calls.at(-1)?.[0];

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

const liveReads = (fake: ReturnType<typeof hub>) =>
	fake.requests.filter((read) => read.section === "live").map((read) => read.offset ?? 0);

async function advance(ms: number) {
	act(() => {
		vi.advanceTimersByTime(ms);
	});
	await settleMicrotasks();
}

// Projects, Test runs and Archived (spec 7.1).
const laptopSource = { id: "local", label: "Laptop", kind: "local", online: true };

const parkSource = { id: "paradise-park", label: "paradise-park", kind: "appwire", online: false };

const catalogCounts = (projects: number, archived: number, testRuns: number) => ({
	projects: { count: projects },
	archived_projects: { count: archived },
	test_runs: { count: testRuns },
});

const evenerProject = (over: Partial<NavigationProjectSummary> = {}): NavigationProjectSummary => ({
	key: "evener",
	name: "evener",
	working_dir: "/home/jesse/git/evener",
	session_count: 2,
	...over,
});

const localWork = session("local:lw", { title: "Local work", live: false, updated_at: minutesAgo(30) });

/** Opens search, types a query that finds the evener project, and taps it. */
async function revealFromSearch(tree: ReactTestRenderer, query = "even") {
	const bar = searchField(tree);
	bar.focus();
	await bar.type(query);
	const [result] = tree.root.findAll((node) => node.props.testID === "project-result");
	expect(result.props.accessibilityLabel).toBe("evener, project, /home/jesse/git/evener");
	await act(async () => result.props.onPress());
}

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

const swipeIdle = session(`local:${OTHER_SESSION_ID}`, {
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
	live: [[swipeWorking, swipeIdle, swipePark]],
	needsYou: [],
	pins: [],
	pinned: {},
	manifest: manifest({
		sources: [laptopSource, { ...parkSource, online: true }],
		sections: { live: { count: 3 }, needs_you: { count: 0 }, pin_sections: { count: 0 } },
		catalogs: catalogCounts(0, 0, 0),
	}),
});

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
	idleUnfolded(id);
	connect(id, fake.client, "ready");
	const tree = await mount(nav);
	return { id, tree, nav };
}

function menuItem(host: RowMenuHost, ref: string, archived = false) {
	const item = host.item(ref, archived);
	if (!item) throw new Error(`the Board shows no ${ref}`);
	return item;
}

const rowMenuActions = (host: RowMenuHost, ref: string, archived = false) =>
	host.actions(menuItem(host, ref, archived), archived);

it("gives a working row Archive on the right swipe and Stop, Pin and More on the left, an idle row no Stop, and another host's row Archive too", async () => {
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
	await settleMicrotasks();
	expect(fake.mutations).toEqual([
		{ method: "evener/archive/set", params: { kind: "session", id: SESSION_ID, archived: true } },
	]);
	const dimmed = rowTitled(tree, "Refactor parser");
	expect(dimmed.props.style({ pressed: false }).opacity).toBe(0.5);
	expect(dimmed.props.accessibilityState).toEqual({ busy: true });
	act(() => fake.release());
	await settleMicrotasks();
	expect(rowTitled(tree, "Refactor parser").props.style({ pressed: false }).opacity).toBe(1);
	expect(toasts(tree)).toEqual(["archive:done"]);
	pressLabel(tree, "Undo");
	await settleMicrotasks();
	act(() => fake.release());
	await settleMicrotasks();
	expect(fake.mutations.at(-1)).toEqual({
		method: "evener/archive/set",
		params: { kind: "session", id: SESSION_ID, archived: false },
	});
	expect(toasts(tree)).toEqual(["unarchive:done"]);
	// Its one control is its own Undo: nothing asks you to refresh or reconnect.
	expect(toastActions(tree)).toHaveLength(1);
});

it("archives another host's row by its ref", async () => {
	const fake = hub(swipeFleet());
	const { tree } = await mountSwipeFleet(fake);
	swipeRowFully(swipeableOf(tree, "Park chore"), "right");
	await settleMicrotasks();
	expect(fake.mutations).toEqual([
		{ method: "evener/archive/set", params: { kind: "session", id: "paradise-park:pp", archived: true } },
	]);
	expect(toasts(tree)).toEqual(["archive:done"]);
});

it("says nothing when an archive can't be confirmed, and settles the journal so the row can swipe again", async () => {
	const fake = hub(swipeFleet(), undefined, undefined, { refuse: true });
	const { tree } = await mountSwipeFleet(fake);
	swipeRowFully(swipeableOf(tree, "Refactor parser"), "right");
	await settleMicrotasks();
	expect(fake.mutations).toHaveLength(1);
	expect(toasts(tree)).toEqual([]);
	expect(renderedText(tree)).not.toContain("request timed out");
	// The Board read the session back, so its organization actions return.
	expect(revealedLabels(swipeableOf(tree, "Refactor parser"), "left")).toEqual(["Archive"]);
	expect(rowTitled(tree, "Refactor parser").props.style({ pressed: false }).opacity).toBe(1);
});

it("stops a working row through the durable runtime: a fresh read, then the interrupt, then Stopped", async () => {
	const fake = hub(swipeFleet());
	const { tree } = await mountSwipeFleet(fake);
	pressRevealed(swipeableOf(tree, "Refactor parser"), "right", "Stop");
	await settleMicrotasks();
	await vi.waitFor(() =>
		expect(fake.threadCalls.map((call) => call.method)).toEqual(["thread/read", "turn/interrupt"]),
	);
	expect(fake.threadCalls[1]?.params).toMatchObject({
		ref: `local:${SESSION_ID}`,
		expectedInstanceId: `instance:local:${SESSION_ID}`,
	});
	expect(toasts(tree)).toEqual(["stop:stopped"]);
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
	expect(rowMenuActions(menuHost(id), `local:${SESSION_ID}`)).toEqual(
		expect.arrayContaining(["pin", "stop", "shutDown", "archive"]),
	);
});

describe("Board actions held offline (phase 6 ruling 18)", () => {
	const interrupts = (fake: ReturnType<typeof hub>) =>
		fake.threadCalls.filter((call) => call.method === "turn/interrupt");
	async function reconnect(id: string, fake: ReturnType<typeof hub>, tree: ReactTestRenderer, nav: Navigation) {
		connect(id, fake.client, "ready");
		rerender(tree, nav);
		await settleMicrotasks();
		await settleMicrotasks();
	}

	// A row's Pin has no PinAssignment screen to open offline, so it asks the
	// pin catalog's categories in place and holds the choice until the hub is
	// back (chooseCategory).
	it("asks a row's Pin for its category offline, and holds the pin", async () => {
		const withCategories = (): Fleet => ({
			...swipeFleet(),
			pins: [{ id: "release", name: "Release", count: 0 }],
			pinned: { release: [] },
			manifest: manifest({
				sources: [laptopSource, { ...parkSource, online: true }],
				sections: { live: { count: 3 }, needs_you: { count: 0 }, pin_sections: { count: 1 } },
				catalogs: catalogCounts(0, 0, 0),
			}),
		});
		const fake = hub(withCategories());
		const { id, tree, nav } = await mountSwipeFleet(fake);
		connect(id, fake.client, "reconnecting");
		rerender(tree, nav);
		pressRevealed(swipeableOf(tree, "Refactor parser"), "right", "Pin");
		const [sheet, choose] = harness.actionSheet.mock.calls.at(-1) ?? [];
		expect(sheet).toMatchObject({ options: ["Release", "New category…", "Cancel"], cancelButtonIndex: 2 });
		act(() => choose(0));
		await settleMicrotasks();
		// Held, not sent: the row says what waits, and the hub hears nothing yet.
		expect(fake.mutations).toEqual([]);
		expect(waitingTitles(tree)).toEqual(["Refactor parser"]);
	});

	it("holds an archive taken offline, says so on the row, and sends it once back online", async () => {
		const fake = hub(swipeFleet());
		const { id, tree, nav } = await mountSwipeFleet(fake);
		connect(id, fake.client, "reconnecting");
		rerender(tree, nav);
		swipeRowFully(swipeableOf(tree, "Refactor parser"), "right");
		await settleMicrotasks();
		expect(fake.mutations).toEqual([]);
		expect(rowTitled(tree, "Refactor parser").props.style({ pressed: false }).opacity).toBe(0.5);
		expect(waitingTitles(tree)).toEqual(["Refactor parser"]);
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
		await settleMicrotasks();
		// The menu, opened offline, stays up across the reconnect.
		const menu = menuHost(id);
		const item = menuItem(menu, `local:${SESSION_ID}`);
		await reconnect(id, fake, tree, nav);
		await vi.waitFor(() => expect(archives).toBe(1));
		act(() => menu.act(item, "unarchive"));
		await settleMicrotasks();
		answerArchive();
		await until(() =>
			expect(fake.mutations.filter((m) => m.method === "evener/archive/set").map((m) => m.params)).toEqual([
				{ kind: "session", id: SESSION_ID, archived: true },
				{ kind: "session", id: SESSION_ID, archived: false },
			]),
		);
		await until(() => expect(heldIn(id)).toEqual([]));
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
		await settleMicrotasks();
		expect(waitingTitles(tree)).toEqual(["Write changelog"]);
		expect(heldIn(id)).toHaveLength(1);
		answerFirst();
		await until(() =>
			expect(writes(fake, "evener/archive/set")).toEqual([
				{ kind: "session", id: SESSION_ID, archived: true },
				{ kind: "session", id: OTHER_SESSION_ID, archived: true },
			]),
		);
		await until(() => expect(heldIn(id)).toEqual([]));
	});

	it("holds a row's Undo while the journal is busy with another change, and sends it once the journal is free", async () => {
		const fake = hub(swipeFleet());
		const { id, tree } = await mountSwipeFleet(fake);
		swipeRowFully(swipeableOf(tree, "Refactor parser"), "right");
		await settleMicrotasks();
		expect(toasts(tree)).toEqual(["archive:done"]);
		const answerNext = hangFirst(fake, "evener/archive/set");
		swipeRowFully(swipeableOf(tree, "Write changelog"), "right");
		await vi.waitFor(() => expect(writes(fake, "evener/archive/set")).toHaveLength(2));
		pressLabel(tree, "Undo");
		await settleMicrotasks();
		expect(heldIn(id)).toHaveLength(1);
		answerNext();
		await until(() =>
			expect(writes(fake, "evener/archive/set")).toEqual([
				{ kind: "session", id: SESSION_ID, archived: true },
				{ kind: "session", id: OTHER_SESSION_ID, archived: true },
				{ kind: "session", id: SESSION_ID, archived: false },
			]),
		);
		await until(() => expect(heldIn(id)).toEqual([]));
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
		await settleMicrotasks();
		await reconnect(id, fake, tree, nav);
		await vi.waitFor(() => expect(writes(fake, "thread/shutdown")).toHaveLength(1));
		shutDown();
		await settleMicrotasks();
		// The first is still out: the second waits in the hold behind it.
		expect(writes(fake, "thread/shutdown")).toHaveLength(1);
		expect(heldIn(id)).toHaveLength(2);
		answerFirst();
		await until(() => expect(heldIn(id)).toEqual([]));
	});

	it("offers the row menu's Cancel for a change held after the menu opened", async () => {
		const fake = hub(swipeFleet());
		const { id, tree, nav } = await mountSwipeFleet(fake);
		connect(id, fake.client, "reconnecting");
		rerender(tree, nav);
		const before = menuHost(id);
		const host = menuHost(id);
		act(() => host.act(menuItem(host, `local:${SESSION_ID}`), "stop"));
		await settleMicrotasks();
		// The sheet re-reads a host it is given anew: a hold alone gives it one.
		expect(menuHost(id)).not.toBe(before);
		// The menu offers to cancel the one held change: the Stop.
		const offered = menuHost(id).held(menuItem(menuHost(id), `local:${SESSION_ID}`));
		expect(offered.map((entry) => entry.id)).toEqual(heldIn(id).map((record) => record.id));
		expect(heldIn(id).map((record) => record.action.kind)).toEqual(["stop"]);
	});

	it("cancels a held archive from the row's menu, and the row is itself again", async () => {
		const fake = hub(swipeFleet());
		const { id, tree, nav } = await mountSwipeFleet(fake);
		connect(id, fake.client, "reconnecting");
		rerender(tree, nav);
		swipeRowFully(swipeableOf(tree, "Refactor parser"), "right");
		await settleMicrotasks();
		const menu = menuHost(id);
		const [held] = menu.held(menuItem(menu, `local:${SESSION_ID}`));
		expect(heldIn(id).map((record) => [record.id, record.action.kind])).toEqual([[held?.id, "archive"]]);
		act(() => menu.cancel(held?.id ?? ""));
		await settleMicrotasks();
		expect(waitingTitles(tree)).toEqual([]);
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
		await settleMicrotasks();
		expect(waitingTitles(tree)).toEqual(["Refactor parser"]);
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
		await settleMicrotasks();
		// Meanwhile that turn ended and another began.
		fleet.live[0] = [{ ...row, turn_ended_at: minutesAgo(1) }, ...(fleet.live[0] ?? []).slice(1)];
		await reconnect(id, fake, tree, nav);
		await vi.waitFor(() => expect(toasts(tree)).toEqual(["stop:dropped"]));
		expect(interrupts(fake)).toEqual([]);
		expect(heldIn(id)).toEqual([]);
	});

	it("sends what was held before a relaunch on the first ready connection, in the order held", async () => {
		const fake = hub(swipeFleet());
		const id = hubId();
		adoptedAnHourAgo(id);
		harness.kv.set(
			boardHoldKey(id),
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
		idleUnfolded(id);
		const ref = `local:${OTHER_SESSION_ID}`;
		harness.kv.set(
			boardHoldKey(id),
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
		expect(waitingTitles(tree)).toHaveLength(1);
		answer();
		await settleMicrotasks();
		expect(waitingTitles(tree)).toEqual([]);
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
			boardHoldKey(id),
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
		act(() => forgetBoardForHub(id));
		answer();
		await settleMicrotasks();
		await settleMicrotasks();
		expect(fake.mutations).toHaveLength(1);
		expect(harness.kv.has(boardHoldKey(id))).toBe(false);
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
	await settleMicrotasks();
	pressLabel(tree, "Archived, 1 session");
	await settleMicrotasks();
	expect(revealedLabels(swipeableOf(tree, "Old archived work"), "left")).toEqual(["Unarchive"]);
});

it("ends a row's trailing swipe with More, which opens the row menu sheet, as a long press does", async () => {
	const shape = swipeFleet();
	shape.live = [[{ ...swipeWorking, rename: true }, swipeIdle, swipePark]];
	const { id, tree, nav } = await mountSwipeFleet(hub(shape));
	pressRevealed(swipeableOf(tree, "Refactor parser"), "right", "More");
	expect(nav.navigate).toHaveBeenCalledWith("RowMenuSheet", { hubId: id, ref: `local:${SESSION_ID}`, archived: false });
	expect(rowMenuActions(menuHost(id), `local:${SESSION_ID}`)).toEqual(["pin", "stop", "shutDown", "archive", "rename"]);
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
	await settleMicrotasks();
	pressLabel(tree, "Archived, 1 session");
	await settleMicrotasks();
	const host = menuHost(id);
	expect(rowMenuActions(host, ref, true)).toContain("unarchive");
	expect(rowMenuActions(host, ref, true)).not.toContain("archive");
	expect(rowMenuActions(host, ref, false)).toContain("archive");
	expect(rowMenuActions(host, ref, false)).not.toContain("unarchive");
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
	shape.live = [[swipeIdle, swipePark]];
	act(() => fake.invalidate(1, [{ kind: "section", section: "live" }]));
	await settleMicrotasks();
	expect(hasRow(tree, "Refactor parser")).toBe(true);
	expect(menuItem(menuHost(id), ref).row.ref).toBe(ref);
});

it("offers Rename only on iOS, where Alert.prompt exists", async () => {
	const { Platform } = (await import("react-native")) as unknown as { Platform: { OS: string } };
	const shape = swipeFleet();
	shape.live = [[{ ...swipeWorking, rename: true }, swipeIdle, swipePark]];
	const { id } = await mountSwipeFleet(hub(shape));
	Platform.OS = "android";
	try {
		expect(rowMenuActions(menuHost(id), `local:${SESSION_ID}`)).not.toContain("rename");
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
	// It names the session, and says what shutting down does.
	expect(ask?.title).toContain("Refactor parser");
	expect(ask?.message).toEqual(expect.stringMatching(/\S/));
	expect(ask?.buttons?.map((button) => [button.text, button.style])).toEqual([
		["Cancel", "cancel"],
		["Shut down", "destructive"],
	]);
	playedHaptics.length = 0;
	act(() => ask?.buttons?.[1]?.onPress?.());
	// Spec 16.6: rigid on a destructive confirmation.
	expect(playedHaptics).toEqual(["impact:rigid"]);
	await settleMicrotasks();
	expect(fake.mutations).toEqual([{ method: "thread/shutdown", params: { ref: `local:${SESSION_ID}` } }]);
	expect(toasts(tree)).toEqual(["shutDown:done"]);
});

it("says why a shut down failed, in the hub's words", async () => {
	const fake = hub(swipeFleet(), undefined, undefined, { refuse: true });
	const { id, tree } = await mountSwipeFleet(fake);
	const host = menuHost(id);
	alertRequests.length = 0;
	act(() => host.act(menuItem(host, `local:${SESSION_ID}`), "shutDown"));
	act(() => alertRequests.at(-1)?.buttons?.[1]?.onPress?.());
	await settleMicrotasks();
	expect(toasts(tree)).toEqual(["shutDown:failed"]);
	// In the hub's words.
	expect(renderedText(tree)).toContain("session not found");
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
	await settleMicrotasks();
	expect(fake.mutations).toEqual([
		{ method: "evener/thread/name/set", params: { ref: `local:${SESSION_ID}`, name: "Parser rewrite" } },
	]);
	expect(toasts(tree)).toEqual(["rename:done"]);
});

it("says why a rename failed, in the hub's words", async () => {
	const fake = hub(swipeFleet(), undefined, undefined, { refuse: true });
	const { id, tree } = await mountSwipeFleet(fake);
	const host = menuHost(id);
	harness.prompt.mockClear();
	act(() => host.act(menuItem(host, `local:${SESSION_ID}`), "rename"));
	const buttons = harness.prompt.mock.calls[0]?.[2] as { onPress?: (name?: string) => void }[];
	act(() => buttons[1]?.onPress?.("Parser rewrite"));
	await settleMicrotasks();
	expect(toasts(tree)).toEqual(["rename:failed"]);
	// In the hub's words.
	expect(renderedText(tree)).toContain("session not found");
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
	await settleMicrotasks();
	// Opened while another hub is active: the read mark waits in this
	// Board's hub's pending marks.
	act(() => host.openSession(menuItem(host, "local:hub-unseen")));
	await settleMicrotasks();
	// The other hub's connection drops and comes back, handing the Board a
	// ready client again.
	connect(second, fakeB.client, "connecting");
	setFocused(false);
	setFocused(true);
	await settleMicrotasks();
	connect(second, fakeB.client, "ready");
	setFocused(false);
	setFocused(true);
	await settleMicrotasks();
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
	await settleMicrotasks();
	act(() => host.openSession(menuItem(host, ref)));
	await settleMicrotasks();
	expect(nav.navigate).toHaveBeenCalledWith("Conversation", { hubId: first, ref, title: "Hub unseen" });
	expect(fakeA.seen).toEqual([]);
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
	await settleMicrotasks();
	act(() => host.act(menuItem(host, ref), "stop"));
	await settleMicrotasks();
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
	await settleMicrotasks();
	alertRequests.length = 0;
	act(() => host.act(menuItem(host, ref), "shutDown"));
	act(() => alertRequests.at(-1)?.buttons?.[1]?.onPress?.());
	await settleMicrotasks();
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
	await settleMicrotasks();
	harness.prompt.mockClear();
	act(() => host.act(menuItem(host, ref), "rename"));
	const buttons = harness.prompt.mock.calls[0]?.[2] as { onPress?: (name?: string) => void }[];
	act(() => buttons[1]?.onPress?.("New name"));
	await settleMicrotasks();
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
	await settleMicrotasks();
	await vi.waitFor(() =>
		expect(fake.threadCalls.map((call) => call.method)).toEqual(["thread/read", "turn/interrupt"]),
	);
	expect(toasts(tree)).toEqual(["stop:stopped"]);
	act(() => host.act(menuItem(host, "paradise-park:pp"), "archive"));
	await settleMicrotasks();
	expect(fake.mutations).toEqual([
		{ method: "evener/archive/set", params: { kind: "session", id: "paradise-park:pp", archived: true } },
	]);
});

it("drops a row that left the Board from the menu's host", async () => {
	const shape = swipeFleet();
	const fake = hub(shape);
	const { id } = await mountSwipeFleet(fake);
	expect(menuHost(id).item("paradise-park:pp", false)).toBeDefined();
	shape.live = [[swipeWorking, swipeIdle]];
	act(() => fake.invalidate(1, [{ kind: "section", section: "live" }]));
	await settleMicrotasks();
	expect(menuHost(id).item("paradise-park:pp", false)).toBeUndefined();
	expect(menuHost(id).item(`local:${SESSION_ID}`, false)).toBeDefined();
});

it("stops providing the menu's host when the Board goes away", async () => {
	const { id, tree } = await mountSwipeFleet(hub(swipeFleet()));
	expect(rowMenuHosts.get(sheetKey(id))).toBeDefined();
	act(() => tree.unmount());
	expect(rowMenuHosts.get(sheetKey(id))).toBeUndefined();
});

const boardRowTitled = (tree: ReactTestRenderer, title: string) =>
	tree.root.findAll((node) => node.type === BoardRow && node.props.item.row.title === title)[0];

/** A Board over the default fleet whose rows can turn into questions ("Build
 * docs", the working row, unless the test names another). */
async function mountAskingFleet(nav = navigation(), withInstances = false) {
	const id = hubId();
	adoptedAnHourAgo(id);
	idleUnfolded(id);
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
		await settleMicrotasks();
	};
	return { id, ...mounted, ask };
}

/** Whether "Build docs" still sits in Working, where it was. */
const heldInWorking = (tree: ReactTestRenderer) =>
	expect(listOrder(tree)).toEqual(["needsYou:3", ...workingOrder.slice(1)]);

/** Whether "Build docs" moved into Needs you, and Working is gone. */
function movedToNeedsYou(tree: ReactTestRenderer) {
	const order = listOrder(tree);
	expect(order.indexOf("Build docs")).toBeLessThan(order.indexOf("idle:3"));
	expect(order).not.toContain("working:1");
}

it("keeps a row in its place while a finger is on the list, then moves it into Needs you, washed, 100ms after the finger lifts", async () => {
	const { tree, ask } = await mountAskingFleet();
	expect(listOrder(tree)).toEqual(workingOrder);
	listEvent(tree, "onTouchStart");
	await ask();
	heldInWorking(tree);
	// Held, the row shows what it is now.
	expect(boardRowTitled(tree, "Build docs").props.item.state).toBe("question");
	expect(stateOf(tree, "Build docs")).toBe("question");
	expect(whyKind(tree, "Build docs")).toBe("reason");
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
	await ask(shipIt);
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
	idleUnfolded(id);
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
	await settleMicrotasks();
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
	await settleMicrotasks();
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

// The Board's Continue reading row (spec 7.1, ruling 20).
it("opens the complete literal reference left by a real Reader after a memory restart", async () => {
	vi.useRealTimers();
	const id = hubId();
	const disk = {
		getItemSync: (key: string) => harness.kv.get(key) ?? null,
		setItemSync: (key: string, value: string) => {
			harness.kv.set(key, value);
		},
		removeItemSync: (key: string) => {
			harness.kv.delete(key);
		},
	};
	harness.readerMemory = new DocumentMemory(disk, id);
	const requests: { target: string | null; session: string | null; auth: string | undefined }[] = [];
	const server = createServer((request, response) => {
		const url = new URL(request.url ?? "", "http://fixture.test");
		requests.push({
			target: url.searchParams.get("path"),
			session: url.searchParams.get("session"),
			auth: request.headers.authorization,
		});
		response.writeHead(200, { "Content-Type": "text/plain; charset=utf-8" });
		response.end("# Literal raw file\n\nUseful saved bytes.");
	});
	server.listen(0, "127.0.0.1");
	await once(server, "listening");
	try {
		const address = server.address();
		if (!address || typeof address === "string") throw new Error("expected TCP fixture");
		const readerClient = new FakeClient("ready");
		readerClient.on(
			"thread/read",
			() =>
				({
					thread: {
						id: "owner",
						cwd: "/work/owner",
						status: { type: "idle" },
						modelProvider: "glm",
						evener: { ref: "remote:owner", instanceId: "owner", capabilities: {}, queue: { revision: 1 } },
					},
				}) as never,
		);
		connect(id, readerClient, "ready", {
			profiles: [{ id, name: "Owner", origin: `http://127.0.0.1:${address.port}` }],
		});
		harness.stack = { index: 0, routes: [{ key: "reader", name: "Reader" }] };
		const reference = {
			path: "docs/a raw.md",
			cwd: "/work/owner",
			readTarget: "/work/owner/docs/a raw.md",
			provenance: "relative",
		};
		const nav = { ...navigation(), setParams: vi.fn(), pop: vi.fn(), goBack: vi.fn(), getState: () => harness.stack };
		const reader = render(
			<ReaderScreen
				route={
					{
						key: "reader",
						name: "Reader",
						params: { hubId: id, sessionRef: "remote:owner", path: "docs/a raw.md", reference, sessionTitle: "Owner" },
					} as never
				}
				navigation={nav as never}
			/>,
		);
		await untilRendered(() =>
			expect(
				reader.root.findAll((node) => String(node.type) === "EnrichedMarkdownText").map((node) => node.props.markdown),
			).toContain("Useful saved bytes."),
		);
		const list = reader.root.findByType(FlatList as never);
		act(() => {
			list.props.onViewableItemsChanged({ viewableItems: [{ index: 1 }], changed: [] });
			list.props.onMomentumScrollEnd({
				nativeEvent: { contentOffset: { y: 100 }, layoutMeasurement: { height: 400 }, contentSize: { height: 1000 } },
			});
		});
		act(() => reader.unmount());
		harness.readerMemory = new DocumentMemory(disk, id);
		expect(harness.readerMemory.continueReading()?.reference).toEqual({
			path: "docs/a raw.md",
			cwd: "/work/owner",
			readTarget: "/work/owner/docs/a raw.md",
			provenance: "relative",
		});
		expect(harness.readerMemory.position({ sessionRef: "remote:owner", path: "docs/a raw.md" })?.progress).toBe(0.5);
		expect(harness.readerMemory.lastRead({ sessionRef: "remote:owner", path: "docs/a raw.md" })?.blocks).toHaveLength(
			2,
		);
		harness.stack = { index: 0, routes: [{ key: "Sessions", name: "Sessions" }] };
		connect(id, hub(fleet).client, "ready");
		const boardNav = { ...navigation(), push: vi.fn() };
		const board = await mount(boardNav);
		const row = board.root.find(
			(node) => node.props.accessibilityLabel === "Continue reading, 50 percent, Literal raw file",
		);
		act(() => row.props.onPress());
		expect(boardNav.push.mock.calls).toEqual([
			["Conversation", { hubId: id, ref: "remote:owner", title: "Owner" }],
			[
				"Reader",
				{
					hubId: id,
					sessionRef: "remote:owner",
					path: "docs/a raw.md",
					reference: {
						path: "docs/a raw.md",
						cwd: "/work/owner",
						readTarget: "/work/owner/docs/a raw.md",
						provenance: "relative",
					},
					sessionTitle: "Owner",
				},
			],
		]);
		expect(requests).toEqual([
			{ target: "/work/owner/docs/a raw.md", session: "remote:owner", auth: "Bearer reader-test-token" },
		]);
		act(() => board.unmount());
	} finally {
		server.closeAllConnections();
		await new Promise<void>((resolve, reject) => server.close((error) => (error ? reject(error) : resolve())));
		harness.readerMemory = null;
	}
});

it("hides an old unbound Board shortcut without deleting its records or drafts", async () => {
	const id = hubId();
	const old = JSON.stringify({
		sessionRef: "local:fix",
		path: "docs/old.md",
		title: "Old",
		sessionTitle: "Owner",
		progress: 0.5,
		leftAt: Date.now(),
	});
	harness.kv.set(`evener.native.continue-reading.${id}`, old);
	harness.kv.set(`evener.native.documents.${id}`, '{"preserved":true}');
	harness.drafts.set(id, new Set(["local:fix"]));
	connect(id, hub(fleet).client, "ready");
	const tree = await mount(navigation());
	expect(continueReadingRows(tree)).toBe(0);
	expect(harness.kv.get(`evener.native.continue-reading.${id}`)).toBe(old);
	expect(harness.kv.get(`evener.native.documents.${id}`)).toBe('{"preserved":true}');
	expect(harness.drafts.get(id)).toEqual(new Set(["local:fix"]));
});

/** A document left partway through, the trail DocumentMemory keeps for
 * the Board, written straight to its kv-store key with the time it was left. */
function leaveDocument(id: string, leftMinutesAgo: number) {
	harness.kv.set(
		`evener.native.continue-reading.${id}`,
		JSON.stringify({
			sessionRef: "local:fix",
			path: "docs/superpowers/plans/settle-race.md",
			reference: {
				path: "docs/superpowers/plans/settle-race.md",
				cwd: "/work/owner",
				readTarget: "/work/owner/docs/superpowers/plans/settle-race.md",
				provenance: "relative",
			},
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
				reference: {
					path: "docs/superpowers/plans/settle-race.md",
					cwd: "/work/owner",
					readTarget: "/work/owner/docs/superpowers/plans/settle-race.md",
					provenance: "relative",
				},
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
	expect(continueReadingRows(tree)).toBe(0);
	const other = hubId();
	adoptedAnHourAgo(other);
	connect(other, hub(fleet).client, "ready");
	expect(continueReadingRows(await mount(navigation()))).toBe(0);
});

it("drops the Continue reading row when its two hours run out, even on an idle Board", async () => {
	vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval", "Date"] });
	try {
		const id = hubId();
		adoptedAnHourAgo(id);
		leaveDocument(id, 119);
		connect(id, hub(fleet).client, "ready");
		const tree = await mount(navigation());
		expect(continueReadingRows(tree)).toBe(1);
		await act(async () => {
			vi.advanceTimersByTime(61_000);
		});
		expect(continueReadingRows(tree)).toBe(0);
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
		act(() => systemGlass.setReduceTransparency(true));
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
			live: [[failing, working], [shipIt]],
			catalogs: { projects: [evenerProject()] },
		});
		const scroller = boardScroller(tree);
		act(() => {
			scroller.props.onLayout({ nativeEvent: { layout: { x: 0, y: 0, width: 390, height: 700 } } });
			tree.root
				.find((node) => node.props.testID === "live-block")
				.props.onLayout({ nativeEvent: { layout: { x: 0, y: 0, width: 390, height: 1600 } } });
		});
		await settleMicrotasks();
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
		await settleMicrotasks();
		expect(liveReads(fake)).toEqual([0]);
		scrollAt(312);
		await settleMicrotasks();
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
		await settleMicrotasks();
		// Leaving search brings the chips back to the glass.
		measureGlass(tree, 64 + 48);
		layOutAt(revealTarget(tree), 60, 48);
		layOutAt(projectSection(tree, "projects"), 900, 400);
		expect(scrollTo).toHaveBeenLastCalledWith({ y: 900 + 60 - 0.3 * (600 - 112 - 48) - 112, animated: true });
	});
});

// The long-press card reads the shown row, so it follows a blue dot that
// changed alone.
it("hands the row menu a row whose blue dot changed though its state didn't", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	const turnEnded = minutesAgo(20);
	const workingUnseen = session("local:working-unseen", {
		title: "Working unseen",
		state: "active",
		updated_at: minutesAgo(1),
		turn_ended_at: turnEnded,
		unseen: true,
	});
	const fake = hub({ ...fleet, live: [[workingUnseen]], needsYou: [] });
	connect(id, fake.client, "ready");
	const tree = await mount(navigation());
	const host = menuHost(id);
	expect(menuItem(host, "local:working-unseen")).toMatchObject({ state: "working", unseen: true });
	act(() => hubSeenMarks(id).markSeen(null, [{ ref: "local:working-unseen", seenThrough: Date.parse(turnEnded) }]));
	expect(menuItem(menuHost(id), "local:working-unseen")).toMatchObject({ state: "working", unseen: false });
	act(() => tree.unmount());
});
