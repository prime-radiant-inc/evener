// The Board screen mounted with only its native edges mocked: the navigation
// reads go through the real BoardController to a fake hub that answers by
// params, and the device memory is the real SeenMarkers over an in-memory
// kv-store.
import type {
	AnyNotification,
	ConnectionState,
	NavigationInvalidationTarget,
	NavigationReadParams,
	NavigationSessionSummary,
} from "@evener/appwire-client";
import { manifest, wireV2 } from "@evener/appwire-client/testing/navigation";
import type { ReactTestInstance, ReactTestRenderer } from "react-test-renderer";
import { act } from "react-test-renderer";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";
import { alertRequests, render, renderedText, screenConnection } from "../renderNative.testkit";
import { BoardScreen } from "./BoardScreen";
import { PulseMeter } from "./PulseMeter";
import { seenMarkers } from "./nativeBoardMemory";

const harness = vi.hoisted(() => ({
	connection: {} as Record<string, unknown>,
	kv: new Map<string, string>(),
	drafts: new Map<string, Set<string>>(),
	focused: true,
	focusListeners: new Set<(focused: boolean) => void>(),
	actionSheet: vi.fn(),
}));

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
	ActionSheetIOS: { showActionSheetWithOptions: (...args: unknown[]) => harness.actionSheet(...args) },
	Keyboard: { dismiss: () => {} },
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
const minutesAgo = (minutes: number) => new Date(NOW - minutes * 60_000).toISOString();

beforeEach(() => {
	vi.useFakeTimers();
	vi.setSystemTime(NOW);
	harness.focused = true;
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
});

// Each test uses its own hub, because nativeBoardMemory keeps one SeenMarkers
// per hub for the life of the module.
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
const asking = session("local:ask", { title: "Pick a name", state: "awaiting", ask_pending: true, updated_at: minutesAgo(3) });
const working = session("local:work", { title: "Build docs", state: "active", updated_at: minutesAgo(1) });
const finished = session("local:done", { title: "Ship it", updated_at: minutesAgo(4) });
const idleOne = session("local:idle-1", { title: "Old chore", dormant: true, updated_at: minutesAgo(120) });
const idleTwo = session("local:idle-2", { title: "Older chore", dormant: true, updated_at: minutesAgo(240) });

interface Fleet {
	live: NavigationSessionSummary[][];
	needsYou: NavigationSessionSummary[];
	pins: Array<{ id: string; name: string; count: number }>;
	manifest: ReturnType<typeof manifest>;
}
const fleet: Fleet = {
	// The ask is in the hub's needs_you section only: bands union it.
	live: [[failing, working, finished, idleOne, idleTwo]],
	needsYou: [failing, asking],
	pins: [
		{ id: "pins-1", name: "Mine", count: 3 },
		{ id: "pins-2", name: "Empty", count: 0 },
	],
	manifest: manifest({
		sources: [{ id: "local", label: "Laptop", kind: "local", online: true }],
		sections: { live: { count: 5 }, needs_you: { count: 2 }, pin_sections: { count: 2 } },
		catalogs: { projects: { count: 4 }, archived_projects: { count: 0 }, test_runs: { count: 0 } },
	}),
};

/** A hub that answers navigation reads by params; `hold` keeps a read
 * unanswered until the test releases it, and `fail` rejects it. */
function hub(
	shape: Fleet,
	hold: (params: NavigationReadParams) => boolean = () => false,
	fail: (params: NavigationReadParams) => boolean = () => false,
) {
	const requests: NavigationReadParams[] = [];
	const listeners = new Set<(event: AnyNotification) => void>();
	const held: Array<() => void> = [];
	const answer = (params: NavigationReadParams) => {
		const offset = params.offset ?? 0;
		if (params.resource === "manifest") return shape.manifest;
		if (params.resource === "pin_catalog") return { pin_sections: shape.pins, remaining: 0 };
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
	const client: ConversationClientLike = {
		request: (method, params) =>
			new Promise((resolve, reject) => {
				if (method !== "evener/navigation/read") throw new Error(`unexpected ${method}`);
				const read = params as NavigationReadParams;
				requests.push(read);
				if (fail(read)) {
					reject(new Error("request timed out"));
					return;
				}
				const respond = () =>
					resolve(
						wireV2(
							{ ...read, representationVersion: 2, offset: read.offset ?? 0, limit: read.limit ?? 50 },
							answer(read),
							`etag-${read.resource}-${read.offset ?? 0}`,
							1,
							"generation-test",
						),
					);
				if (hold(read)) held.push(respond);
				else respond();
			}),
		onNotification: (listener) => {
			listeners.add(listener);
			return () => listeners.delete(listener);
		},
	};
	return {
		client,
		requests,
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
	return { navigate: vi.fn(), setOptions: vi.fn() };
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
	return tree.root
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
	for (const title of ["Fix retry loop", "Pick a name", "Ship it", "Build docs"]) expect(hasRow(tree, title)).toBe(true);
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
	act(() => idleCount.props.onPress());
	expect(hasRow(tree, "Old chore")).toBe(true);
	act(() => tree.unmount());
});

it("shows chips for the sections that have sessions, a row for every pinned category, and opens today's screens", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	connect(id, hub(fleet).client, "ready");
	const nav = navigation();
	const tree = await mount(nav);
	expect(chipLabels(tree)).toEqual(["Live, 5 sessions, 2 need you", "Mine, 3 sessions", "Projects, 4 projects"]);
	expect(texts(tree)).not.toContain("Archived · 0");
	pressLabel(tree, "Mine, 3 sessions");
	expect(nav.navigate).toHaveBeenLastCalledWith("PinnedSection", { hubId: id, sectionId: "pins-1", title: "Mine" });
	// An empty category has no chip, but it keeps its row (spec 7.1: a
	// category is a place, and an empty one says how to pin to it).
	pressLabel(tree, "Empty, 0 sessions");
	expect(nav.navigate).toHaveBeenLastCalledWith("PinnedSection", { hubId: id, sectionId: "pins-2", title: "Empty" });
	pressLabel(tree, "Projects, 4 projects");
	expect(nav.navigate).toHaveBeenLastCalledWith("Projects", { hubId: id, archived: false });
	act(() => tree.unmount());
});

it("gives every control a touch target at least 44pt tall", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	connect(id, hub(fleet).client, "ready");
	const tree = await mount(navigation());
	const flat = (style: unknown): Record<string, number> =>
		Object.assign({}, ...[typeof style === "function" ? style({ pressed: false }) : style].flat(Number.POSITIVE_INFINITY));
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

it("offers the hub menu as an alert off iOS, without Hub settings while the hub is out of reach", async () => {
	const { Platform } = (await import("react-native")) as unknown as { Platform: { OS: string } };
	const id = hubId();
	adoptedAnHourAgo(id);
	connect(id, hub(fleet).client, "ready");
	const nav = navigation();
	const tree = await mount(nav);
	Platform.OS = "android";
	try {
		const buttonLabels = () => (alertRequests.at(-1)?.buttons ?? []).map((button) => button.text);
		const pressHubButton = () => {
			const hubButton = render(headerOptions(nav).unstable_headerLeftItems({})[0].element);
			act(() => hubButton.root.findByType("Pressable" as never).props.onPress());
			act(() => hubButton.unmount());
		};
		pressHubButton();
		expect(alertRequests.at(-1)?.title).toBe("Work hub");
		expect(buttonLabels()).toEqual(["Hub settings", "Switch hub", "Cancel"]);
		act(() => alertRequests.at(-1)?.buttons?.[1].onPress?.());
		expect(nav.navigate).toHaveBeenLastCalledWith("Hubs");
		// An alert has no disabled buttons, so Hub settings leaves the list.
		connect(id, null, "connecting");
		rerender(tree, nav);
		pressHubButton();
		expect(buttonLabels()).toEqual(["Switch hub", "Cancel"]);
	} finally {
		Platform.OS = "ios";
	}
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
	expect(fake.requests.map((read) => read.section ?? read.resource).sort()).toEqual([
		"live",
		"manifest",
		"needs_you",
		"pin_catalog",
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
	// The hub button is a custom view: read what it draws and the menu it opens.
	const hubButton = render(headerItems[0].element);
	act(() => hubButton.root.findByType("Pressable" as never).props.onPress());
	const hubMenu: string[] = harness.actionSheet.mock.calls.at(-1)?.[0].options ?? [];
	expect(hubMenu.length).toBeGreaterThan(0);
	const labels = tree.root
		.findAll((node) => typeof node.props.accessibilityLabel === "string")
		.map((node) => node.props.accessibilityLabel as string);
	for (const text of [...texts(tree), ...labels, ...headerLabels, ...texts(hubButton), ...hubMenu])
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
	const fake = hub({ ...fleet, live: [[failing, older], [newest]], needsYou: [failing] }, (read) => (read.offset ?? 0) > 0);
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

it("puts the hub's name and menu on the left and search on the right", async () => {
	const id = hubId();
	adoptedAnHourAgo(id);
	connect(id, hub(fleet).client, "ready");
	const nav = navigation();
	const tree = await mount(nav);
	const options = headerOptions(nav);
	expect(options.title).toBe("");
	// One control, as spec 7.1 draws it: the hub's name and a chevron in a
	// single header item that opens the hub menu.
	const hubItems = options.unstable_headerLeftItems({});
	expect(hubItems).toHaveLength(1);
	expect(hubItems[0].type).toBe("custom");
	const hubButton = render(hubItems[0].element);
	expect(texts(hubButton)).toEqual(["Work hub"]);
	expect(hubButton.root.findAllByType("SymbolView" as never).map((node) => node.props.name)).toEqual(["chevron.down"]);
	const press = hubButton.root.findByType("Pressable" as never);
	expect(press.props.accessibilityLabel).toBe("Work hub, hub menu");
	// A long hub name truncates inside the capsule instead of growing it
	// into Search (the window is 390pt wide here).
	expect(press.props.style.maxWidth).toBeLessThanOrEqual(390 * 0.6);
	const hubName = hubButton.root.findByType("Text" as never);
	expect(hubName.props.numberOfLines).toBe(1);
	expect(hubName.props.style).toMatchObject({ flexShrink: 1 });
	act(() => press.props.onPress());
	const [sheet, choose] = harness.actionSheet.mock.calls.at(-1) ?? [];
	expect(sheet).toMatchObject({ options: ["Hub settings", "Switch hub", "Cancel"], cancelButtonIndex: 2 });
	expect(sheet.disabledButtonIndices).toEqual([]);
	act(() => choose(0));
	expect(nav.navigate).toHaveBeenLastCalledWith("HubSettings", { hubId: id });
	act(() => choose(1));
	expect(nav.navigate).toHaveBeenLastCalledWith("Hubs");
	act(() => hubButton.unmount());
	// Hub settings needs the hub; Switch hub doesn't.
	connect(id, null, "connecting");
	rerender(tree, nav);
	const offline = render(headerOptions(nav).unstable_headerLeftItems({})[0].element);
	act(() => offline.root.findByType("Pressable" as never).props.onPress());
	expect(harness.actionSheet.mock.calls.at(-1)?.[0].disabledButtonIndices).toEqual([0]);
	act(() => offline.unmount());
	const [search] = options.unstable_headerRightItems({});
	expect(search).toMatchObject({ type: "button", icon: { type: "sfSymbol", name: "magnifyingglass" } });
	expect(tree.root.findAll((node) => node.type === ("TextInput" as never))).toHaveLength(0);
	act(() => search.onPress());
	expect(tree.root.findAll((node) => node.type === ("TextInput" as never))).toHaveLength(1);
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
	// The row-age ticker and the retry.
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
