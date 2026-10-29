import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { DocumentMemory } from "../reader/documentMemory";
// Imported at file scope, so the Reader's graph is transformed at collection
// time, outside the timeout of the test that opens a real Reader.
import { FreshDot } from "../reader/FreshDot";
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
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { ConversationClientLike } from "../conversation/services/conversation";
import {
	alertRequests,
	playedHaptics,
	render,
	renderedText,
	screenConnection,
	settleMicrotasks,
	systemGlass,
	unmountMountedTrees,
} from "../renderNative.testkit";
import { markerOf, markers, withTestID } from "../markerTestUtils";
import { ACTIVITY_POLL_MS } from "./activityPoll";
import { BoardRow } from "./BoardRow";
import { BoardScreen } from "./BoardScreen";
import { composeButtonRoom } from "./ComposeButton";
import { requestBoardJump } from "./boardJump";
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
	liftFinger,
	listEvent,
	listOrder,
	menuHost,
	menuLabels,
	pressChip,
	resultTitled,
	rowTitled,
	screenOverBoard,
	sheetOverBoard,
	skeletonRows,
	stateOf,
	texts,
	textsIn,
	toasts,
	whyReason,
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
	harness.actionSheet.mockClear();
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

async function mount(nav: Navigation) {
	const tree = render(screen(nav));
	await settleMicrotasks();
	return tree;
}

function rerender(tree: ReactTestRenderer, nav: Navigation) {
	act(() => tree.update(screen(nav)));
}

/** The band headers and the Idle fold, in screen order. */
function bandHeaders(tree: ReactTestRenderer): string[] {
	return tree.root
		.findAll((node) => typeof node.props.testID === "string" && node.props.testID.startsWith("board-band:"))
		.map((node) => node.props.testID.slice("board-band:".length));
}

/** The Draft tags a row draws. */
const draftTags = (row: ReactTestInstance) =>
	row.findAll((node) => node.type === ("Text" as never) && node.props.children === "Draft");

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

it("keeps own errors and live attention while settled delegate chips stay quiet", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	const settled = { running: 0, failed: 3, done: 0 };
	const rows = [
		session("local:error", { title: "Own error", state: "errored", ask_pending: true, subagents: settled }),
		session("local:question", {
			title: "Question",
			state: "awaiting",
			ask_pending: true,
			updated_at: minutesAgo(6),
			subagents: settled,
		}),
		session("local:approval", { title: "Approval", state: "active", approval_pending: true, subagents: settled }),
		session("local:mixed", { title: "Mixed work", state: "active", subagents: { running: 2, failed: 3, done: 0 } }),
		session("local:stale", { title: "Stale question", state: "active", ask_pending: true, subagents: settled }),
		session("local:offline", {
			title: "Offline error",
			state: "errored",
			offline: true,
			ask_pending: true,
			subagents: settled,
		}),
	];
	connect(id, hub({ ...fleet, live: [rows], needsYou: rows.slice(0, 3), pins: [], pinned: {} }).client, "ready");
	const nav = navigation();
	const tree = await mount(nav);
	expect(tree.root.findAllByType(BoardRow).map((node) => [node.props.item.row.title, node.props.item.state])).toEqual([
		["Own error", "failed"],
		["Question", "question"],
		["Approval", "approval"],
		["Mixed work", "working"],
		["Stale question", "working"],
	]);
	expect(renderedText(tree)).not.toContain("3 failed");
	expect(tree.root.findAll((node) => node.props.testID === "subagent-chip")).toHaveLength(1);
	expect(renderedText(tree)).toContain("2 running");
	for (const title of ["Own error", "Question", "Approval", "Mixed work", "Stale question"])
		expect(rowTitled(tree, title).props.accessibilityLabel).not.toContain("3 failed");
	// Each label speaks the reason its row shows, its own attention's.
	for (const title of ["Own error", "Question", "Approval"]) {
		const row = rowTitled(tree, title);
		expect(row.props.accessibilityLabel).toContain(whyReason(row));
	}
	expect(hasRow(tree, "Offline error")).toBe(false);
	act(() => rowTitled(tree, "Mixed work").props.onPress());
	expect(nav.navigate).toHaveBeenCalledWith("Conversation", { hubId: id, ref: "local:mixed", title: "Mixed work" });
});

it("renders the fleet's bands in order with their counts, and Idle starts folded", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	connect(id, hub(fleet).client, "ready");
	const tree = await mount(navigation());
	// A session whose turn ended rests in Idle, seen or not (#4093).
	expect(bandHeaders(tree)).toEqual(["needsYou:2", "working:1", "idle:3"]);
	expect(renderedText(tree)).toContain("2 need you");
	expect(texts(tree)).toEqual(expect.arrayContaining(["1 working", "3 idle"]));
	expect(texts(tree)).not.toContain("1 finished");
	for (const title of ["Fix retry loop", "Pick a name", "Build docs"]) expect(hasRow(tree, title)).toBe(true);
	expect(hasRow(tree, "Ship it")).toBe(false);
	expect(hasRow(tree, "Old chore")).toBe(false);
	pressLabel(tree, "Idle, 3 sessions, unread sessions inside");
	for (const title of ["Ship it", "Old chore", "Older chore"]) expect(hasRow(tree, title)).toBe(true);
	expect(dotted(tree, "Ship it")).toBe(true);
	expect(JSON.parse(harness.kv.get(`evener.native.board-sections.${id}`) ?? "null")).toEqual({ idle: false });
	// Folded again, the summary's idle count unfolds it.
	pressLabel(tree, "Idle, 3 sessions");
	expect(hasRow(tree, "Old chore")).toBe(false);
	const idleCount = tree.root.find(
		(node) =>
			node.type === ("Pressable" as never) &&
			node.findAll((child) => child.type === ("Text" as never) && child.props.children === "3 idle").length > 0,
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
	const tree = render(screen(nav), {
		createNodeMock: (element) => (element.type === ("TextInput" as never) ? { focus, blur } : { scrollTo }),
	});
	await settleMicrotasks();
	return { tree, scrollTo, focus, blur };
}

/** The floating compose button's room at the Board's end, from the mocked 34pt
 * home indicator (the button's own geometry lives in composeButtonRoom). */
const COMPOSE_ROOM = composeButtonRoom(34);

/** The Board's toast slot's bottom, above the compose button. */
function toastBottom(tree: ReactTestRenderer): number {
	const flat = (node: ReactTestInstance) => Object.assign({}, ...[node.props.style].flat(Number.POSITIVE_INFINITY));
	const slot = tree.root.find((node) => node.props.testID === "board-toast" && String(node.type) === "View");
	return flat(slot).bottom;
}

it("floats a compose button over the Board's end, inset by it, with the toast above it", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	connect(id, hub(fleet).client, "ready");
	const nav = navigation();
	const tree = await mount(nav);
	const button = tree.root.find(
		(node) => node.type === ("Pressable" as never) && node.props.accessibilityLabel === "New session",
	);
	expect(button.props.style({ pressed: false })).toMatchObject({ position: "absolute", right: 16, bottom: 16 + 34 });
	act(() => button.props.onPress());
	expect(nav.navigate).toHaveBeenLastCalledWith("NewSession", { hubId: id, hubName: "Work hub" });
	const scroller = boardScroller(tree);
	expect(scroller.props.contentInset).toEqual({ bottom: COMPOSE_ROOM });
	expect(scroller.props.contentContainerStyle).toMatchObject({ paddingBottom: 24 });
	expect(scroller.props.scrollIndicatorInsets).toEqual({ bottom: COMPOSE_ROOM });
	expect(toastBottom(tree)).toBe(COMPOSE_ROOM + 10);
	act(() => tree.unmount());
});

it("pads the Board's end by the compose button on Android, which has no content inset", async () => {
	const { Platform } = (await import("react-native")) as unknown as { Platform: { OS: string } };
	Platform.OS = "android";
	try {
		const id = hubId();
		adoptedAnHourAgo(id);
		connect(id, hub(fleet).client, "ready");
		const tree = await mount(navigation());
		const scroller = boardScroller(tree);
		expect(scroller.props.contentInset).toBeUndefined();
		expect(scroller.props.contentContainerStyle).toMatchObject({ paddingBottom: 24 + COMPOSE_ROOM });
		expect(scroller.props.scrollIndicatorInsets).toEqual({ bottom: COMPOSE_ROOM });
		expect(toastBottom(tree)).toBe(COMPOSE_ROOM + 10);
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
		.findAll((node) => typeof node.props.onLayout === "function" && node.props.testID === "board-band:needsYou:2")
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
	expect(bandHeaders(tree)).toContain("working:1");
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
	await settleMicrotasks();
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
	await settleMicrotasks();
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
	await settleMicrotasks();
	expect(pinHeaders(tree)).toEqual(["Empty, 0 sessions"]);
	act(() => confirm?.buttons?.[1].onPress?.());
	await settleMicrotasks();
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
	await settleMicrotasks();
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
	await settleMicrotasks();
	// The second confirmation was up before the first change went out;
	// pressed while that change is pending, it sends nothing.
	act(() => empty?.buttons?.[1].onPress?.());
	await settleMicrotasks();
	expect(fake.mutations).toEqual([{ method: "evener/pin-section/delete", params: { sectionId: "pins-1" } }]);
	expect(opacity(tree, "Mine")).toBe(0.5);
	expect(opacity(tree, "Empty")).toBe(1);
	expect(menuLabels(tree)).toEqual([]);
	fake.release();
	await settleMicrotasks();
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
	await settleMicrotasks();
	expect(fake.mutations).toHaveLength(1);
	expect(menuLabels(tree)).toEqual([]);
	// The hub's error stays out of sight: no toast, no notice, nothing to press.
	expect(toasts(tree)).toEqual([]);
	expect(renderedText(tree)).not.toContain("request timed out");
	expect(tree.root.findAll((node) => node.props.testID === "notice")).toEqual([]);
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
	pressLabel(tree, "Idle, 3 sessions, unread sessions inside");
	expect(hasRow(tree, "Old chore")).toBe(true);
	connect(second, hubB.client, "ready");
	rerender(tree, nav);
	await settleMicrotasks();
	expect(hasRow(tree, "Fix retry loop")).toBe(false);
	expect(hasRow(tree, "Build docs")).toBe(true);
	// Hub B keeps its own fold state: Idle starts folded there.
	const fold = () => withTestID(tree, "idle-fold")[0];
	expect(fold()?.props.accessibilityState).toEqual({ expanded: false });
	expect(hasRow(tree, "Old chore")).toBe(false);
	// Hub A's Board is gone: its invalidations read nothing.
	const readsOnA = hubA.requests.length;
	hubA.invalidate(1, [{ kind: "section", section: "live", revision: 2 }]);
	await settleMicrotasks();
	expect(hubA.requests).toHaveLength(readsOnA);
	// Unfolded, Idle holds hub B's one idle session.
	act(() => fold()?.props.onPress());
	const [live] = withTestID(tree, "live-block");
	expect(live && markers(live, "board-row").filter((state) => state === "idle")).toHaveLength(1);
	expect(hasRow(tree, "Old chore")).toBe(true);
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

const resultTitles = (tree: ReactTestRenderer) =>
	tree.root
		.findAll((node) => node.props.testID === "search-result")
		.map((node) => node.props.accessibilityLabel.split(", ")[0]);

const buttonCount = (tree: ReactTestRenderer) =>
	tree.root.findAll((node) => node.type === ("Pressable" as never) && node.props.accessibilityRole === "button").length;

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
	idleUnfolded(id);
	const gone = session("local:gone", { title: "Old report" });
	const fake = hub({ ...fleet, searchOnly: [gone] });
	connect(id, fake.client, "ready");
	const nav = navigation();
	const tree = await mount(nav);
	expect(hasRow(tree, "Ship it")).toBe(true);
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
	expect(seenMarkers(id).isSeen(shipIt)).toBe(true);
	// A session only in Needs you (past Live's loaded pages) is found too.
	expect(seenMarkers(id).isSeen(asking)).toBe(false);
	act(() => resultTitled(tree, "Pick a name").props.onPress());
	expect(nav.navigate).toHaveBeenLastCalledWith("Conversation", { hubId: id, ref: "local:ask", title: "Pick a name" });
	expect(seenMarkers(id).isSeen(asking)).toBe(true);
	// Live results come first, then past ones.
	await bar.type("re");
	expect(resultTitles(tree)).toEqual(["Fix retry loop", "Old chore", "Older chore", "Old report"]);
	expect(resultTitled(tree, "Old report").props.accessibilityLabel).toBe("Old report, evener, 5 minutes");
	// A session the Board doesn't list has no blue dot to clear.
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
	expect(markers(tree, "search-note")).toEqual(["noMatch"]);
	pressLabel(tree, "All");
	expect(resultTitles(tree)).toEqual(["Old report"]);
	act(() => tree.unmount());
});

it("lists a live session once when the hub's past results name it too", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	// The real hub's past index holds live sessions' records too.
	connect(id, hub({ ...fleet, searchOnly: [shipIt] }).client, "ready");
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
	expect(markers(tree, "search-note")).toEqual(["noMatch"]);
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
	const controls = buttonCount(tree);
	await bar.type("fix");
	expect(markers(tree, "search-note")).toEqual(["failed"]);
	// The failure offers nothing to press.
	expect(buttonCount(tree)).toBe(controls);
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
	expect(markers(tree, "search-note")).toEqual(["disconnected"]);
	connect(id, fake.client, "ready");
	rerender(tree, nav);
	await settleMicrotasks();
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
	await settleMicrotasks();
	expect(fake.searches).toEqual(["ship"]);
	// Back in view, the field's query asks again.
	harness.stack = { index: 0, routes: [{ key: "Sessions", name: "Sessions" }] };
	rerender(tree, nav);
	await settleMicrotasks();
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
	await settleMicrotasks();
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
	await settleMicrotasks();
	expect(scrollTo).not.toHaveBeenCalled();
	// Scrolled down the list.
	scrollToY(400);
	harness.fontScale = 1;
	rerender(tree, nav);
	await settleMicrotasks();
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
	await settleMicrotasks();
	expect(fake.searches).toEqual(["ship"]);
	harness.stack = { index: 0, routes: [{ key: "Sessions", name: "Sessions" }] };
	setFocused(true);
	rerender(tree, nav);
	await settleMicrotasks();
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
	idleUnfolded(id);
	connect(id, hub(fleet).client, "ready");
	const nav = navigation();
	const tree = await mount(nav);
	expect(dotted(tree, "Ship it")).toBe(true);
	act(() => rowTitled(tree, "Ship it").props.onPress());
	expect(nav.navigate).toHaveBeenLastCalledWith("Conversation", { hubId: id, ref: "local:done", title: "Ship it" });
	expect(seenMarkers(id).isSeen(shipIt)).toBe(true);
	// Seen, the session stays in Idle and loses its blue dot.
	expect(bandHeaders(tree)).toEqual(["needsYou:2", "working:1", "idle:3"]);
	expect(dotted(tree, "Ship it")).toBe(false);
	act(() => tree.unmount());
});

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

// A live session that moved after the person last looked carries the blue
// dot, resting in Idle included, and folded, Idle's header carries it for the
// sessions inside (Jesse, #4093). A session that never ran gets none.
it("dots the folded Idle header while a session inside moved since it was seen, and the row once unfolded", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	const moved = session("local:moved", {
		title: "Moved since seen",
		updated_at: minutesAgo(90),
		turn_ended_at: minutesAgo(90),
		unseen: false,
		seen_through: minutesAgo(60),
	});
	const neverRan = session("local:never-ran", { title: "Never ran", updated_at: minutesAgo(5), dormant: true });
	const fake = hub({
		...fleet,
		live: [[moved, hubSeen, neverRan]],
		needsYou: [],
		activity: [
			{
				ref: "local:moved",
				minutes: [0, 0, 0, 0, 0, 0, 0],
				runningSubagents: 0,
				lastMovedAt: Date.parse(minutesAgo(30)),
			},
		],
	});
	connect(id, fake.client, "ready");
	const tree = await mount(navigation());
	await settleMicrotasks();
	const fold = () =>
		tree.root.find(
			(node) =>
				node.type === ("Pressable" as never) &&
				node.props.accessibilityState?.expanded !== undefined &&
				String(node.props.accessibilityLabel).startsWith("Idle"),
		);
	expect(fold().props.accessibilityLabel).toBe("Idle, 3 sessions, unread sessions inside");
	expect(fold().findAllByType(FreshDot)).toHaveLength(1);
	act(() => fold().props.onPress());
	expect(fold().props.accessibilityLabel).toBe("Idle, 3 sessions");
	expect(fold().findAllByType(FreshDot)).toHaveLength(0);
	expect(dotted(tree, "Moved since seen")).toBe(true);
	expect(dotted(tree, "Hub seen")).toBe(false);
	expect(dotted(tree, "Never ran")).toBe(false);
	act(() => tree.unmount());
});

// Opening a row marks it seen through the later of its turn end and its last
// motion, so output that streamed after the turn's end is seen too.
it("marks a row it opens seen through its last motion when that is later than its turn end", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	const streamed = session("local:streamed", {
		title: "Streamed",
		updated_at: minutesAgo(10),
		turn_ended_at: minutesAgo(10),
		unseen: true,
		seen_through: minutesAgo(60),
	});
	const lastMovedAt = Date.parse(minutesAgo(2));
	const fake = hub({
		...fleet,
		live: [[streamed]],
		needsYou: [],
		activity: [{ ref: "local:streamed", minutes: [0, 0, 0, 0, 0, 0, 0], runningSubagents: 0, lastMovedAt }],
	});
	idleUnfolded(id);
	connect(id, fake.client, "ready");
	const tree = await mount(navigation());
	await settleMicrotasks();
	act(() => rowTitled(tree, "Streamed").props.onPress());
	await settleMicrotasks();
	expect(fake.seen).toEqual([[{ ref: "local:streamed", seenThrough: lastMovedAt }]]);
	act(() => tree.unmount());
});

it("takes the blue dot from the hub for a row that carries its turn end, whatever the device's markers say", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	idleUnfolded(id);
	const fake = hub(hubFleet);
	connect(id, fake.client, "ready");
	const tree = await mount(navigation());
	// Every session whose turn ended rests Idle; the dot says what's new.
	for (const title of ["Hub unseen", "Hub seen", "Ship it", "Pinned unseen"]) expect(stateOf(tree, title)).toBe("idle");
	expect(dotted(tree, "Hub unseen")).toBe(true);
	expect(dotted(tree, "Hub seen")).toBe(false);
	// A row without a turn end still follows the device: updated since its epoch.
	expect(dotted(tree, "Ship it")).toBe(true);
	// The categories classify the same way.
	expect(dotted(tree, "Pinned unseen")).toBe(true);
	expect(bandHeaders(tree)).toEqual(["needsYou:2", "working:1", "idle:3"]);
	expect(fake.seen).toEqual([]);
	act(() => tree.unmount());
});

it("marks a hub row seen through its turn end when you open it, and clears its dot at once", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	idleUnfolded(id);
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
	expect(dotted(tree, "Hub unseen")).toBe(false);
	expect(bandHeaders(tree)).toEqual(["needsYou:2", "working:1", "idle:3"]);
	await settleMicrotasks();
	expect(fake.seen).toEqual([[{ ref: "local:hub-unseen", seenThrough: Date.parse(minutesAgo(90)) }]]);
	// The device's own markers are not touched for a hub row.
	expect(JSON.parse(harness.kv.get(`evener.native.seen.${id}`) ?? "{}").sessions).toEqual({});
	// Opening it again, now seen, costs no request.
	act(() => rowTitled(tree, "Hub unseen").props.onPress());
	await settleMicrotasks();
	expect(fake.seen).toHaveLength(1);
	// Opening a pinned hub row marks it the same way.
	act(() => rowTitled(tree, "Pinned unseen").props.onPress());
	await settleMicrotasks();
	expect(dotted(tree, "Pinned unseen")).toBe(false);
	expect(fake.seen.at(-1)).toEqual([{ ref: "local:pinned-unseen", seenThrough: Date.parse(minutesAgo(80)) }]);
	// The hub's rows catch up, and the pending mark goes: a later unseen for
	// the same turn would show again.
	shape.live = [[failing, working, { ...hubUnseen, unseen: false }, hubSeen, shipIt]];
	// This fake hub answers every read at revision 1, so the change names none.
	act(() => fake.invalidate(1, [{ kind: "section", section: "live" }]));
	await settleMicrotasks();
	expect(hubSeenMarks(id).isSeenOnHub(hubUnseen)).toBe(false);
	expect(hubSeenMarks(id).isSeenOnHub(pinnedUnseen)).toBe(true);
	act(() => tree.unmount());
});

it("marks a row without a turn end on the device, and sends the hub nothing", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	idleUnfolded(id);
	const fake = hub(hubFleet);
	connect(id, fake.client, "ready");
	const tree = await mount(navigation());
	act(() => rowTitled(tree, "Ship it").props.onPress());
	await settleMicrotasks();
	expect(seenMarkers(id).isSeen(shipIt)).toBe(true);
	expect(fake.seen).toEqual([]);
	act(() => tree.unmount());
});

it("sends a mark made while the connection was down once it's ready again", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	idleUnfolded(id);
	const fake = hub(hubFleet);
	connect(id, fake.client, "ready");
	const nav = navigation();
	const tree = await mount(nav);
	connect(id, null, "reconnecting");
	rerender(tree, nav);
	act(() => rowTitled(tree, "Hub unseen").props.onPress());
	await settleMicrotasks();
	expect(dotted(tree, "Hub unseen")).toBe(false);
	expect(fake.seen).toEqual([]);
	connect(id, fake.client, "ready");
	rerender(tree, nav);
	await settleMicrotasks();
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
	await settleMicrotasks();
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
	expect(bandHeaders(tree)).toEqual(["needsYou:2", "working:1", "idle:3"]);
	act(() => tree.unmount());
});

it("keeps its rows when the connection drops and grays the meters", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	connect(id, hub(fleet).client, "ready");
	const nav = navigation();
	const tree = await mount(nav);
	expect(tree.root.findAllByType(PulseMeter).map((meter) => meter.props.tone ?? "alive")).not.toContain("gray");
	connect(id, null, "reconnecting");
	rerender(tree, nav);
	expect(hasRow(tree, "Build docs")).toBe(true);
	expect(bandHeaders(tree)).toEqual(["needsYou:2", "working:1", "idle:3"]);
	act(() => {
		vi.advanceTimersByTime(2000);
	});
	const tones = tree.root.findAllByType(PulseMeter).map((meter) => meter.props.tone);
	expect(tones.length).toBeGreaterThan(0);
	expect(new Set(tones)).toEqual(new Set(["gray"]));
	act(() => tree.unmount());
});

it("offers no Reconnect or Refresh anywhere: a lost connection adds no control", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	connect(id, hub(fleet).client, "ready");
	const nav = navigation();
	const tree = await mount(nav);
	/** Every control the Board and its header offer, by label. */
	const controls = () => {
		const options = headerOptions(nav);
		const headerItems = [...options.unstable_headerLeftItems({}), ...options.unstable_headerRightItems({})];
		const headerLabels = headerItems.flatMap((item: { label?: string; menu?: { items: Array<{ label: string }> } }) => [
			item.label,
			...(item.menu?.items.map((entry) => entry.label) ?? []),
		]);
		// The hub button is a custom view: count what it offers. It opens the
		// Hub sheet, whose own pages pin that they never ask to reconnect.
		const hubButton = render(headerItems[0].element);
		const hubControls = hubButton.root.findAll((node) => node.type === ("Pressable" as never)).length;
		act(() => hubButton.unmount());
		// Rows say their age, which the minute moves on; they are not new controls.
		const pressables = tree.root
			.findAll((node) => node.type === ("Pressable" as never) && markerOf(node, "board-row") === null)
			.map((node) => node.props.accessibilityLabel);
		return { labels: [...pressables, ...headerLabels].filter((label) => typeof label === "string"), hubControls };
	};
	const live = controls();
	connect(id, null, "closed");
	rerender(tree, nav);
	act(() => {
		vi.advanceTimersByTime(60_000);
	});
	const lost = controls();
	expect(lost.labels.filter((label) => !live.labels.includes(label))).toEqual([]);
	expect(lost.hubControls).toBe(live.hubControls);
	// Nor does the list refresh on a pull.
	expect(tree.root.findAll((node) => node.props.refreshControl !== undefined || node.props.onRefresh)).toEqual([]);
	act(() => tree.unmount());
});

it("says what to do on an empty Board, with a New session button", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	connect(id, hub({ ...fleet, live: [[]], needsYou: [] }).client, "ready");
	const nav = navigation();
	const tree = await mount(nav);
	expect(markers(tree, "empty-state")).toEqual(["noSessions"]);
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
	await settleMicrotasks();
	expect(skeletonRows(tree)).toHaveLength(0);
	act(() => tree.unmount());
});

it("shows the first read's rows at once under a finger that touched the skeleton, never the empty Board", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	idleUnfolded(id);
	const fake = hub(fleet, () => true);
	connect(id, fake.client, "ready");
	const tree = await mount(navigation());
	expect(skeletonRows(tree)).toHaveLength(3);
	listEvent(tree, "onTouchStart");
	fake.release();
	await settleMicrotasks();
	expect(markers(tree, "empty-state")).toEqual([]);
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
	expect(texts(tree)).toContain(INCOMPATIBLE_TEXT);
	expect(hasRow(tree, "Build docs")).toBe(true);
	act(() => tree.unmount());
});

it("carries a non-fatal close's own sentence in the notice row until the connection returns", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	connect(id, hub(fleet).client, "ready");
	const nav = navigation();
	const tree = await mount(nav);
	// A distinctive marker, not the production sentence: the row must carry
	// whatever the connection says, not a copy of one hardcoded message.
	const marker = "the board notice must carry this marker sentence";
	connect(id, null, "closed", { error: marker });
	rerender(tree, nav);
	expect(texts(tree)).toContain(marker);
	expect(texts(tree)).not.toContain(INCOMPATIBLE_TEXT);
	// The retained rows stay, and the marker goes when the connection returns.
	expect(hasRow(tree, "Build docs")).toBe(true);
	connect(id, hub(fleet).client, "ready");
	rerender(tree, nav);
	await settleMicrotasks();
	expect(texts(tree)).not.toContain(marker);
	act(() => tree.unmount());
});

it("on a device's first run, reads every Live page before adopting, so no row flashes a blue dot", async () => {
	const id = hubId();
	// Live is sorted by attention, so the newest session sits on page 2.
	const newest = session("local:newest", { title: "Newest", updated_at: minutesAgo(1) });
	const older = session("local:older", { title: "Older", updated_at: minutesAgo(30) });
	const fake = hub(
		{ ...fleet, live: [[failing, older], [newest]], needsYou: [failing] },
		(read) => (read.offset ?? 0) > 0,
	);
	idleUnfolded(id);
	connect(id, fake.client, "ready");
	const tree = await mount(navigation());
	expect(fake.requests.filter((read) => read.section === "live").map((read) => read.offset)).toEqual([0, 2]);
	expect(seenMarkers(id).adopted).toBe(false);
	expect(dotted(tree, "Older")).toBe(false);
	fake.release();
	await settleMicrotasks();
	expect(seenMarkers(id).adopted).toBe(true);
	expect(seenMarkers(id).isSeen(newest)).toBe(true);
	expect(bandHeaders(tree)).toEqual(["needsYou:1", "idle:2"]);
	expect(dotted(tree, "Older")).toBe(false);
	expect(dotted(tree, "Newest")).toBe(false);
	act(() => tree.unmount());
});

it("stops first-run paging while the Board is out of view, and finishes it on return", async () => {
	const id = hubId();
	let holdLater = true;
	const fake = hub(
		{ ...fleet, live: [[failing, working], [shipIt], [idleOne]], needsYou: [failing] },
		(read) => holdLater && (read.offset ?? 0) > 0,
	);
	connect(id, fake.client, "ready");
	const tree = await mount(navigation());
	expect(liveReads(fake)).toEqual([0, 2]);
	// Leaving the Board mid-way cancels the page in flight; nothing replaces it.
	setFocused(false);
	await settleMicrotasks();
	expect(liveReads(fake)).toEqual([0, 2]);
	holdLater = false;
	fake.release();
	await settleMicrotasks();
	expect(liveReads(fake)).toEqual([0, 2]);
	expect(seenMarkers(id).adopted).toBe(false);
	setFocused(true);
	await settleMicrotasks();
	expect(liveReads(fake)).toEqual([0, 2, 2, 3]);
	expect(seenMarkers(id).adopted).toBe(true);
	act(() => tree.unmount());
});

it("shows the Draft tag on sessions with a saved draft", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	idleUnfolded(id);
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
	// VoiceOver hears where the button goes.
	expect(press.props.accessibilityHint).toEqual(expect.any(String));
	expect(press.props.accessibilityHint).not.toBe("");
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

async function advance(ms: number) {
	act(() => {
		vi.advanceTimersByTime(ms);
	});
	await settleMicrotasks();
}

it("says a failed first read will be retried, and retries it with a growing backoff until it loads", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	let liveFails = true;
	const fake = hub(fleet, undefined, (read) => liveFails && read.section === "live");
	connect(id, fake.client, "ready");
	const tree = await mount(navigation());
	expect(markers(tree, "empty-state")).toContain("firstReadFailed");
	expect(renderedText(tree)).not.toContain("request timed out");
	expect(skeletonRows(tree)).toHaveLength(0);
	expect(liveReads(fake)).toEqual([0]);
	// The first retry waits a second, the second two.
	await advance(999);
	expect(liveReads(fake)).toEqual([0]);
	await advance(1);
	expect(liveReads(fake)).toEqual([0, 0]);
	expect(markers(tree, "empty-state")).toContain("firstReadFailed");
	liveFails = false;
	await advance(1999);
	expect(liveReads(fake)).toEqual([0, 0]);
	await advance(1);
	expect(liveReads(fake)).toEqual([0, 0, 0]);
	expect(markers(tree, "empty-state")).not.toContain("firstReadFailed");
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
	await settleMicrotasks();
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
	expect(markers(tree, "empty-state")).not.toContain("firstReadFailed");
	expect(skeletonRows(tree)).toHaveLength(3);
	// Live's read is still out, and a retry now would cancel it.
	await advance(60_000);
	expect(liveReads(fake)).toEqual([0]);
	holdLive = false;
	fake.release();
	await settleMicrotasks();
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
	await settleMicrotasks();
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
	await settleMicrotasks();
}

const writing = session("local:write", { title: "Write tests", state: "active", updated_at: minutesAgo(1) });

it("keeps a later-page parent's Working pulse and quiet failure history through questions, refresh and reconnect", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	idleUnfolded(id);
	// A parent whose child is still running rests idle (#4093); the child's
	// work is what the row shows.
	const parent = session("local:parent", {
		title: "Coordinate children",
		state: "idle",
		subagents: { running: 1, failed: 1, done: 0 },
	});
	const shape: Fleet = { ...fleet, live: [[working], [parent]], needsYou: [], pins: [], pinned: {} };
	const fake = hub(shape);
	connect(id, fake.client, "ready");
	const nav = navigation();
	const tree = await mount(nav);
	await layOut(tree);
	expect(liveReads(fake)).toEqual([0, 1]);
	const assertQuietFailure = () => {
		const row = rowTitled(tree, parent.title);
		expect(row.props.accessibilityLabel).not.toContain("1 failed");
		expect(textsIn(row)).not.toContain("1 failed");
	};
	const assertWorking = () => {
		const row = rowTitled(tree, parent.title);
		expect(stateOf(tree, parent.title)).toBe("working");
		expect(row.findAllByType(PulseMeter)).toHaveLength(1);
		expect(row.props.accessibilityLabel).toContain("Waiting on 1 subagent");
		expect(textsIn(row)).toContain("1 running");
		expect(
			tree.root.findAllByType(BoardRow).find((node) => node.props.item.row.ref === parent.ref)?.props.item.row
				.subagents,
		).toEqual({ running: 1, failed: 1, done: 0 });
		assertQuietFailure();
	};
	assertWorking();
	const refresh = async (sequence: number) => {
		act(() =>
			fake.invalidate(sequence, [
				{ kind: "section", section: "live" },
				{ kind: "section", section: "needs_you" },
			]),
		);
		await settleMicrotasks();
		await layOut(tree);
	};
	const question = { ...parent, state: "awaiting", ask_pending: true };
	shape.live = [[working], [question]];
	shape.needsYou = [question];
	await refresh(1);
	expect(stateOf(tree, parent.title)).toBe("question");
	expect(bandHeaders(tree)).toContain("needsYou:1");
	expect(rowTitled(tree, parent.title).findAllByType(PulseMeter)).toEqual([]);
	expect(rowTitled(tree, parent.title).props.accessibilityLabel).toContain(whyReason(rowTitled(tree, parent.title)));
	expect(textsIn(rowTitled(tree, parent.title))).toContain("1 running");
	assertQuietFailure();
	shape.live = [[working], [parent]];
	shape.needsYou = [];
	await refresh(2);
	assertWorking();
	// An unchanged refresh must retain the later page's classification too.
	await refresh(3);
	assertWorking();
	const beforeReconnect = fake.requests.length;
	connect(id, null, "reconnecting");
	rerender(tree, nav);
	expect(stateOf(tree, parent.title)).toBe("working");
	connect(id, fake.client, "ready");
	rerender(tree, nav);
	await settleMicrotasks();
	await layOut(tree);
	assertWorking();
	expect(
		fake.requests
			.slice(beforeReconnect)
			.filter((read) => read.section === "live")
			.map((read) => read.offset),
	).toEqual([0, 1]);
	shape.live = [[working], [{ ...parent, subagents: { running: 0, failed: 1, done: 1 } }]];
	await refresh(4);
	expect(stateOf(tree, parent.title)).toBe("idle");
	expect(dotted(tree, parent.title)).toBe(true);
	expect(rowTitled(tree, parent.title).findAllByType(PulseMeter)).toEqual([]);
	expect(rowTitled(tree, parent.title).findAll((node) => node.props.testID === "subagent-chip")).toHaveLength(0);
	assertQuietFailure();
	// The retained failure still has the same session and row-menu entry points.
	act(() => rowTitled(tree, parent.title).props.onLongPress());
	expect(nav.navigate).toHaveBeenLastCalledWith("RowMenuSheet", { hubId: id, ref: parent.ref, archived: false });
	const menu = menuHost(id);
	expect(menu.item(parent.ref, false)?.row.subagents).toEqual({ running: 0, failed: 1, done: 1 });
	act(() => menu.closed());
	act(() => rowTitled(tree, parent.title).props.onPress());
	expect(nav.navigate).toHaveBeenLastCalledWith("Conversation", { hubId: id, ref: parent.ref, title: parent.title });
	await settleMicrotasks();
	expect(stateOf(tree, parent.title)).toBe("idle");
	expect(dotted(tree, parent.title)).toBe(false);
	expect(rowTitled(tree, parent.title).findAll((node) => node.props.testID === "subagent-chip")).toHaveLength(0);
	assertQuietFailure();
	act(() => tree.unmount());
});

it("reads a failed later Live page again after the backoff, keeping the loaded rows on screen", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	idleUnfolded(id);
	let lastPageFails = true;
	let holdLive = false;
	const fake = hub(
		{ ...fleet, live: [[failing, working], [shipIt], [writing]] },
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
	expect(markers(tree, "empty-state")).not.toContain("firstReadFailed");
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
	await settleMicrotasks();
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
	idleUnfolded(id);
	let lastPageFails = true;
	let holdLive = false;
	const fake = hub(
		{ ...fleet, live: [[failing, working], [shipIt]] },
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
	await settleMicrotasks();
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
		{ ...fleet, live: [[failing, working], [shipIt]] },
		undefined,
		(read) => read.section === "live" && ((read.offset ?? 0) === 0 ? firstPageFails : secondPageFails),
	);
	connect(id, fake.client, "ready");
	const tree = await mount(navigation());
	expect(markers(tree, "empty-state")).toContain("firstReadFailed");
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
	expect(markers(tree, "empty-state")).toContain("firstReadFailed");
	live = "hold";
	await advance(1000);
	expect(liveReads(fake)).toEqual([0, 0]);
	// Leaving the Board pauses it, which cancels the retry's read.
	setFocused(false);
	await settleMicrotasks();
	live = "fail";
	setFocused(true);
	await settleMicrotasks();
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
	expect(markers(tree, "empty-state")).not.toContain("firstReadFailed");
	act(() => tree.unmount());
});

it("keeps reading Live's pages while they don't fill the screen, without any scrolling", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	idleUnfolded(id);
	const fake = hub({ ...fleet, live: [[failing, working], [shipIt], [idleOne, idleTwo]] });
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
	const fake = hub({ ...fleet, live: [[failing, working], [shipIt]] });
	connect(id, fake.client, "ready");
	const tree = await mount(navigation());
	act(() =>
		tree.root
			.find((node) => node.props.testID === "live-block")
			.props.onLayout({ nativeEvent: { layout: { x: 0, y: 52, width: 390, height: 300 } } }),
	);
	await settleMicrotasks();
	expect(liveReads(fake)).toEqual([0]);
	searchField(tree).focus();
	act(() =>
		boardScroller(tree).props.onScroll({
			nativeEvent: { contentOffset: { x: 0, y: 900 }, layoutMeasurement: { width: 390, height: 700 } },
		}),
	);
	await settleMicrotasks();
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
	await settleMicrotasks();
	expect(manifestReads()).toBe(1);
	harness.drafts.set(id, new Set(["local:work"]));
	setFocused(true);
	await settleMicrotasks();
	expect(manifestReads()).toBe(2);
	expect(draftTags(rowTitled(tree, "Build docs"))).toHaveLength(1);
	act(() => tree.unmount());
});

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

	harness.stack = boardAlone;
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

const evenerProject = (over: Partial<NavigationProjectSummary> = {}): NavigationProjectSummary => ({
	key: "evener",
	name: "evener",
	working_dir: "/home/jesse/git/evener",
	session_count: 2,
	...over,
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
