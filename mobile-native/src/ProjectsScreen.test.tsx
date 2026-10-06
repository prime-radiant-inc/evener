// Screen-level test for the shared page list the Projects, Project and
// SessionLocation screens render: the native app keeps itself current and
// never shows a refresh affordance, so the list carries no pull to refresh.
// Mirrors the hub settings screen's rule from phase 5 PR 13a.
//
// The test kit's FlatList is a stub that does not render refreshControl (it
// renders the rows instead), so the affordance is asserted on the props the
// screen hands the list, the way ConversationScreen.send.test.tsx does.
import type { ComponentProps } from "react";
import { AccessibilityInfo } from "react-native";
import { act } from "react-test-renderer";
import { describe, expect, it, vi } from "vitest";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { completeSession, manifest, wireSnapshot } from "@evener/appwire-client/testing/navigation";
import { archivedListStoreFor } from "./archivedLists";
import { NavigationPages } from "./navigationPages";
import { ProjectScreen, ProjectsScreen, unconfirmedReason, SessionLocationScreen } from "./ProjectsScreen";
import {
	alertRequests,
	flatListCalls,
	pressable,
	render,
	renderedText,
	screenConnection,
} from "./renderNative.testkit";

const harness = vi.hoisted(() => ({ connection: {} as Record<string, unknown>, kv: new Map<string, string>() }));
vi.mock("react-native", async () => ({
	...(await import("./renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("react-native-safe-area-context", () => ({ SafeAreaView: "SafeAreaView" }));
vi.mock("@react-navigation/native", async () => {
	const { useEffect } = await import("react");
	return {
		useFocusEffect: (effect: () => void | (() => void)) => useEffect(effect, [effect]),
		useIsFocused: () => true,
	};
});
vi.mock("./ConnectionProvider", () => ({ useConnection: () => harness.connection }));
vi.mock("expo-sqlite/kv-store", () => ({
	Storage: {
		getItemSync: (key: string) => harness.kv.get(key) ?? null,
		setItemSync: (key: string, value: string) => void harness.kv.set(key, value),
		removeItemSync: (key: string) => void harness.kv.delete(key),
		getAllKeysSync: () => [...harness.kv.keys()],
	},
}));
vi.mock("expo-crypto", () => ({ randomUUID: () => "uuid" }));

const props = {
	route: { params: { hubId: "hub-1" } },
	navigation: { navigate: () => {}, setParams: () => {} },
} as unknown as ComponentProps<typeof ProjectsScreen>;
/** A project page located for `revealRef`, as locating a session opens it. */
const locationProps = (revealRef = "local:a", tier = "current", ref = revealRef) =>
	({
		route: {
			params: {
				hubId: "hub-1",
				location: {
					ref,
					revealRef,
					title: "Project",
					params: { resource: "project_page", projectKey: "p", tier },
				},
			},
		},
		navigation: { navigate: () => {}, setParams: () => {} },
	}) as unknown as ComponentProps<typeof SessionLocationScreen>;
/** Project "p"'s screen, on the tier and catalog `params` name. */
const projectProps = (params: Record<string, unknown>) =>
	({
		route: { params: { hubId: "hub-1", projectKey: "p", title: "Project", ...params } },
		navigation: { navigate: () => {}, setParams: () => {} },
	}) as unknown as ComponentProps<typeof ProjectScreen>;
/** A hub whose project archived list holds Alpha, then Beta behind cursor
 * "c1", recording every archived list read it answers. */
const twoPageArchivedHub = () => {
	const hub = new FakeClient("ready");
	const archivedReads: unknown[] = [];
	hub.on("evener/archived/list", (params) => {
		archivedReads.push(params);
		return params.cursor
			? { sessions: [completeSession({ ref: "local:b", title: "Beta" })], total: 2 }
			: { sessions: [completeSession({ ref: "local:a", title: "Alpha" })], total: 2, nextCursor: "c1" };
	});
	return { hub, archivedReads };
};

it("offers no pull to refresh: the projects list keeps itself current", async () => {
	const hub = new FakeClient("ready");
	hub.on("evener/navigation/read", (params) =>
		wireSnapshot(params as never, { projects: [{ key: "p1", name: "Alpha", session_count: 2 }] }),
	);
	harness.connection = screenConnection(hub, "ready");
	const tree = render(<ProjectsScreen {...props} />);
	await act(async () => {});
	expect(renderedText(tree)).toContain("Alpha");

	// The list's pull-to-refresh is a prop, not a child it renders.
	const list = tree.root.findAll(
		(node) =>
			typeof node.type === "function" && Array.isArray(node.props.data) && typeof node.props.renderItem === "function",
	)[0];
	expect(list).toBeDefined();
	expect(list.props.onRefresh).toBeUndefined();
	expect(list.props.refreshing).toBeUndefined();
	expect(tree.root.findAll((node) => node.props.refreshControl !== undefined)).toEqual([]);
	tree.unmount();
});

it("announces that more of the list is loading, since iOS ignores accessibilityLiveRegion (#2903)", async () => {
	const hub = new FakeClient("ready");
	hub.on("evener/navigation/read", (params) => {
		// The next page never lands, so the footer stays in its loading state.
		if (((params as { offset?: number }).offset ?? 0) > 0) return new Promise(() => {});
		return wireSnapshot(params as never, { projects: [{ key: "p1", name: "Alpha", session_count: 2 }], remaining: 1 });
	});
	harness.connection = screenConnection(hub, "ready");
	const tree = render(<ProjectsScreen {...props} />);
	await act(async () => {});
	expect(renderedText(tree)).toContain("Alpha");
	const announce = vi.mocked(AccessibilityInfo.announceForAccessibility);
	announce.mockClear();
	const list = tree.root.findAll(
		(node) =>
			typeof node.type === "function" && Array.isArray(node.props.data) && typeof node.props.renderItem === "function",
	)[0];
	expect(list).toBeDefined();
	act(() => list.props.onEndReached());
	expect(announce).toHaveBeenCalledWith("Loading more…");
	tree.unmount();
});

it("renders a live tally's chip in the shared list and none without one", async () => {
	const hub = new FakeClient("ready");
	hub.on("evener/navigation/read", (params) =>
		wireSnapshot(params as never, {
			sessions: [
				{ ref: "local:a", title: "Alpha", live: true, subagents: { running: 2, failed: 0, done: 0 } },
				{ ref: "local:b", title: "Beta" },
			],
		}),
	);
	harness.connection = screenConnection(hub, "ready");
	const tree = render(<SessionLocationScreen {...locationProps()} />);
	await act(async () => {});
	expect(renderedText(tree)).toContain("Alpha");
	expect(renderedText(tree)).toContain("2 running");
	// Only the row with a tally gets a chip, not the one without.
	expect(tree.root.findAll((node) => node.props.testID === "subagent-chip")).toHaveLength(1);
	// The row's own label speaks it too: a Pressable override hides the chip's
	// text from VoiceOver.
	const labels = tree.root
		.findAll(
			(node) => typeof node.props.accessibilityLabel === "string" && node.props.accessibilityLabel.startsWith("Open "),
		)
		.map((node) => node.props.accessibilityLabel);
	expect(labels).toContain("Open Alpha, 2 running");
	expect(labels).toContain("Open Beta");
	tree.unmount();
});

it.each(["project", "location"] as const)("keeps settled delegate tallies quiet in the %s list", async (screen) => {
	const hub = new FakeClient("ready");
	hub.on("evener/navigation/read", (params) =>
		wireSnapshot(params as never, {
			sessions: [
				{ ref: "local:a", title: "Alpha", live: true, state: "active", subagents: { running: 2, failed: 3, done: 0 } },
				{ ref: "local:b", title: "Beta", live: true, state: "active", subagents: { running: 0, failed: 3, done: 0 } },
			],
		}),
	);
	harness.connection = screenConnection(hub, "ready");
	const opened: unknown[][] = [];
	const navigation = { navigate: (...args: unknown[]) => opened.push(args), setParams: () => {} };
	const tree = render(
		screen === "project" ? (
			<ProjectScreen {...projectProps({})} navigation={navigation as never} />
		) : (
			<SessionLocationScreen {...locationProps()} navigation={navigation as never} />
		),
	);
	await act(async () => {});
	expect(renderedText(tree)).toContain("2 running");
	expect(renderedText(tree)).not.toContain("3 failed");
	expect(tree.root.findAll((node) => node.props.testID === "subagent-chip")).toHaveLength(1);
	const alpha = pressable(tree, "Open Alpha, 2 running");
	const beta = pressable(tree, "Open Beta");
	expect(alpha).toBeDefined();
	expect(beta).toBeDefined();
	act(() => {
		alpha?.props.onPress();
		beta?.props.onPress();
	});
	expect(opened).toEqual([
		["Conversation", { hubId: "hub-1", ref: "local:a", title: "Alpha" }],
		["Conversation", { hubId: "hub-1", ref: "local:b", title: "Beta" }],
	]);
	act(() => tree.unmount());
});

// A v3 page the hub cut short by its node or byte budget is only paged: the
// rows it dropped count in `remaining` and arrive through Load more. The list
// says nothing about a partial tree or missing related sessions (a v3 row
// carries no children).
it("pages a page the hub cut short through Load more, with no partial-tree notice", async () => {
	const hub = new FakeClient("ready");
	hub.on("evener/navigation/read", (params) =>
		((params as { offset?: number }).offset ?? 0) > 0
			? wireSnapshot(params as never, { sessions: [{ ref: "local:b", title: "Beta" }], remaining: 0 })
			: wireSnapshot(params as never, {
					sessions: [{ ref: "local:a", title: "Alpha" }],
					remaining: 1,
					truncated: true,
				}),
	);
	harness.connection = screenConnection(hub, "ready");
	const tree = render(<SessionLocationScreen {...locationProps()} />);
	await act(async () => {});
	const shown = renderedText(tree);
	expect(shown).toContain("Alpha");
	expect(shown).not.toContain("partial session tree");
	expect(shown).not.toContain("related session");
	await act(async () => pressable(tree, "Load more · 1 remaining")?.props.onPress());
	expect(renderedText(tree)).toContain("Beta");
	tree.unmount();
});

// Navigation serves a project's archived tier empty, and a location names no
// catalog: an archived session is revealed from the project's archived list,
// read from whichever catalog holds the project now.
it("reveals a located archived session from the project's archived list", async () => {
	const { hub, archivedReads } = twoPageArchivedHub();
	harness.connection = screenConnection(hub, "ready");
	flatListCalls.length = 0;
	const tree = render(<SessionLocationScreen {...locationProps("local:b", "archived")} />);
	await act(async () => {});
	expect(archivedReads).toEqual([{ projectKey: "p" }, { projectKey: "p", cursor: "c1" }]);
	expect(hub.calls.filter((call) => call.method === "evener/navigation/read")).toEqual([]);
	expect(flatListCalls).toContainEqual({
		method: "scrollToIndex",
		args: { index: 1, animated: false, viewPosition: 0.3 },
	});
	expect(pressable(tree, "Open Beta")?.props.accessibilityState).toEqual({ selected: true });
	expect(renderedText(tree)).not.toContain("not in the returned list");
	tree.unmount();
});

// An archived fork original sits inside its continuation's row, so locating
// it reveals that row and says so.
it("reveals a located archived fork original through the row that carries it", async () => {
	const hub = new FakeClient("ready");
	const original = completeSession({ ref: "local:orig", title: "Original", kind: "fork" });
	hub.on("evener/archived/list", () => ({
		sessions: [completeSession({ ref: "local:cont", title: "Cont", children: [original] })],
		total: 1,
	}));
	harness.connection = screenConnection(hub, "ready");
	const tree = render(<SessionLocationScreen {...locationProps("local:cont", "archived", "local:orig")} />);
	await act(async () => {});
	expect(renderedText(tree)).toContain("Showing the row that owns this session");
	expect(pressable(tree, "Open Cont")?.props.accessibilityState).toEqual({ selected: true });
	expect(renderedText(tree)).not.toContain("not in the returned list");
	tree.unmount();
});

// Locating a session reveals its row on the flat list: the list scrolls to
// that row's place among the loaded rows and marks it selected.
it("scrolls to and selects the located row", async () => {
	const hub = new FakeClient("ready");
	hub.on("evener/navigation/read", (params) =>
		wireSnapshot(params as never, {
			sessions: [
				{ ref: "local:a", title: "Alpha" },
				{ ref: "local:b", title: "Beta" },
			],
		}),
	);
	harness.connection = screenConnection(hub, "ready");
	flatListCalls.length = 0;
	const tree = render(<SessionLocationScreen {...locationProps("local:b")} />);
	await act(async () => {});
	expect(flatListCalls).toContainEqual({
		method: "scrollToIndex",
		args: { index: 1, animated: false, viewPosition: 0.3 },
	});
	expect(pressable(tree, "Open Beta")?.props.accessibilityState).toEqual({ selected: true });
	expect(pressable(tree, "Open Alpha")?.props.accessibilityState).toEqual({ selected: false });
	tree.unmount();
});

// Navigation v3 serves a project's archived tier empty: its rows come from
// evener/archived/list, paged by cursor, from the project's catalog.
it("lists a project's archived sessions from the archived list, a page at a time", async () => {
	const { hub, archivedReads } = twoPageArchivedHub();
	harness.connection = screenConnection(hub, "ready");
	const tree = render(<ProjectScreen {...projectProps({ tier: "archived", archived: true })} />);
	await act(async () => {});
	expect(renderedText(tree)).toContain("Alpha");
	await act(async () => pressable(tree, "Load more · 1 remaining")?.props.onPress());
	expect(renderedText(tree)).toContain("Beta");
	expect(archivedReads).toEqual([
		{ catalog: "archived_projects", projectKey: "p" },
		{ catalog: "archived_projects", projectKey: "p", cursor: "c1" },
	]);
	expect(hub.calls.filter((call) => call.method === "evener/navigation/read")).toEqual([]);
	tree.unmount();
});

// Rows an earlier view loaded stay on screen while the connection is away,
// with no read the client would reject.
it("reads no archived list while the connection is not ready", async () => {
	const hub = new FakeClient("ready");
	hub.on("evener/archived/list", () => ({ sessions: [completeSession({ ref: "local:a", title: "Alpha" })], total: 1 }));
	await archivedListStoreFor(hub).refresh("archived_projects", "p");
	hub.emitStateChange("reconnecting");
	harness.connection = screenConnection(hub, "reconnecting");
	const tree = render(<ProjectScreen {...projectProps({ tier: "archived", archived: true })} />);
	await act(async () => {});
	expect(renderedText(tree)).toContain("Alpha");
	expect(renderedText(tree)).not.toContain("cannot call");
	expect(hub.calls.filter((call) => call.method === "evener/archived/list")).toHaveLength(1);
	tree.unmount();
});

// A recovered connection drops every archived list (archivedLists.ts), and
// the screen reads its list again once the connection is ready: the same
// read of an unloaded list it makes on every focus, so a screen out of view
// while the connection recovered reads it on its return too.
it("reads the archived list again once a dropped connection recovers", async () => {
	const hub = new FakeClient("ready");
	hub.on("evener/archived/list", () => ({ sessions: [completeSession({ ref: "local:a", title: "Alpha" })], total: 1 }));
	const reads = () => hub.calls.filter((call) => call.method === "evener/archived/list").length;
	const screen = () => <ProjectScreen {...projectProps({ tier: "archived", archived: true })} />;
	harness.connection = screenConnection(hub, "ready");
	const tree = render(screen());
	await act(async () => {});
	expect(reads()).toBe(1);

	hub.emitStateChange("reconnecting");
	harness.connection = screenConnection(hub, "reconnecting");
	await act(async () => tree.update(screen()));
	hub.emitReady();
	harness.connection = screenConnection(hub, "ready");
	await act(async () => tree.update(screen()));
	await act(async () => {});
	expect(reads()).toBe(2);
	expect(renderedText(tree)).toContain("Alpha");
	tree.unmount();
});

// An archived row's Unarchive runs through the same organize flow as any
// row's: the hub accepts it, the change is confirmed, and the archived list
// is read again, so the row leaves the tab. The organize flow reads every
// loaded archived list again and the page waits for that read, so a clean one
// is the only read after the change; when that read fails, the page reads
// again rather than fail to confirm the change.
it.each([
	["once", false, 2],
	["again when the organize flow's read failed", true, 3],
])("unarchives an archived row and reads the archived list %s", async (_name, organizeReadFails, reads) => {
	harness.kv.clear();
	const hub = new FakeClient("ready");
	// A local session id is 22 alphanumerics; the change is checked by its ref.
	const alpha = { ref: "local:AlphaSession0000000001", session_id: "AlphaSession0000000001", title: "Alpha" };
	let archived = true;
	let archivedReads = 0;
	hub.on("evener/archived/list", () => {
		archivedReads++;
		if (organizeReadFails && archivedReads === 2) throw new Error("offline");
		return archived ? { sessions: [completeSession(alpha)], total: 1 } : { sessions: [], total: 0 };
	});
	hub.on("evener/archive/set", () => {
		archived = false;
		return { ok: true, navigation: { generation_id: "generation_test", targets: [] } };
	});
	// The change is confirmed by reading the session's location, now in the
	// project's current tier, and the manifest.
	hub.on("evener/navigation/read", (params) => {
		if (params.resource !== "location") return wireSnapshot(params as never, manifest());
		const response = wireSnapshot(params as never, { session: alpha });
		Object.assign((response.data as { metadata: Record<string, unknown> }).metadata, {
			tier: "current",
			project_key: "p",
		});
		return response;
	});
	harness.connection = screenConnection(hub, "ready");
	const tree = render(<ProjectScreen {...projectProps({ tier: "archived" })} />);
	await act(async () => {});
	expect(renderedText(tree)).toContain("Alpha");
	alertRequests.length = 0;
	act(() => pressable(tree, "More actions for Alpha")?.props.onPress());
	const unarchive = alertRequests.at(-1)?.buttons?.find((button) => button.text === "Unarchive");
	await act(async () => unarchive?.onPress?.());
	await act(async () => {});

	expect(hub.calls.filter((call) => call.method === "evener/archive/set").map((call) => call.params)).toEqual([
		{ kind: "session", id: "AlphaSession0000000001", archived: false },
	]);
	expect(archivedReads).toBe(reads);
	expect(renderedText(tree)).not.toContain("Alpha");
	expect(renderedText(tree)).not.toContain("could not be confirmed");
	tree.unmount();
	harness.kv.clear();
});

// An organize change is confirmed against the page read after it. A
// navigation page must come from the generation the change was observed in;
// an archived list has no navigation version, so only its read is checked.
describe("confirming an organize change against the page", () => {
	const loaded = { loaded: true, rows: [], remaining: 0, loading: false, error: null as string | null, stale: false };
	const source = (navigationVersioned: boolean, generationId: string | null, page = loaded) =>
		({
			navigationVersioned,
			getSnapshot: () => page,
			getResourceVersion: () => (generationId ? { generationId, revision: 1 } : null),
		}) as unknown as Parameters<typeof unconfirmedReason>[0];

	// The real navigation page declares its version, so a change observed in
	// another generation is caught.
	it("checks the generation a real navigation page was read in", async () => {
		const hub = new FakeClient("ready");
		hub.on("evener/navigation/read", (params) =>
			wireSnapshot(params as never, { sessions: [], remaining: 0 }, '"one"', 1, "g1"),
		);
		const pages = new NavigationPages(
			hub,
			{ resource: "project_page", projectKey: "p", tier: "current" },
			"sessions",
			(row: { ref: string }) => row.ref,
		);
		await pages.refresh();
		expect(unconfirmedReason(pages, { generationId: "g1" })).toBeNull();
		expect(unconfirmedReason(pages, { generationId: "g2" })).toBe("The hub restarted during the check.");
	});

	it("checks a navigation page's generation", () => {
		expect(unconfirmedReason(source(true, "g1"), { generationId: "g1" })).toBeNull();
		expect(unconfirmedReason(source(true, "g2"), { generationId: "g1" })).toBe("The hub restarted during the check.");
		expect(unconfirmedReason(source(true, null), { generationId: "g1" })).toBe("The hub restarted during the check.");
	});

	it("checks only the read for a source navigation doesn't version", () => {
		expect(unconfirmedReason(source(false, null), { generationId: "g1" })).toBeNull();
		expect(unconfirmedReason(source(false, null, { ...loaded, error: "offline" }), { generationId: "g1" })).toBe(
			"The current navigation could not be confirmed.",
		);
	});
});
