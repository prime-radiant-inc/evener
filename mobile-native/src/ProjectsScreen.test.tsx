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
import { completeSession, wireSnapshot } from "@evener/appwire-client/testing/navigation";
import { ProjectScreen, ProjectsScreen, pageConfirmationError, SessionLocationScreen } from "./ProjectsScreen";
import { flatListCalls, pressable, render, renderedText, screenConnection } from "./renderNative.testkit";

const harness = vi.hoisted(() => ({ connection: {} as Record<string, unknown> }));
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
	Storage: { getItemSync: () => null, setItemSync: () => {}, removeItemSync: () => {} },
}));
vi.mock("expo-crypto", () => ({ randomUUID: () => "uuid" }));

const props = {
	route: { params: { hubId: "hub-1" } },
	navigation: { navigate: () => {}, setParams: () => {} },
} as unknown as ComponentProps<typeof ProjectsScreen>;
/** A project page located for `revealRef`, as locating a session opens it. */
const locationProps = (revealRef = "local:a") =>
	({
		route: {
			params: {
				hubId: "hub-1",
				location: {
					ref: revealRef,
					revealRef,
					title: "Project",
					params: { resource: "project_page", projectKey: "p", tier: "current" },
				},
			},
		},
		navigation: { navigate: () => {}, setParams: () => {} },
	}) as unknown as ComponentProps<typeof SessionLocationScreen>;

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
	const hub = new FakeClient("ready");
	const archivedReads: unknown[] = [];
	hub.on("evener/navigation/read", (params) => wireSnapshot(params as never, { sessions: [], remaining: 0 }));
	hub.on("evener/archived/list", (params) => {
		archivedReads.push(params);
		return params.cursor
			? { sessions: [completeSession({ ref: "local:b", title: "Beta" })], total: 2 }
			: { sessions: [completeSession({ ref: "local:a", title: "Alpha" })], total: 2, nextCursor: "c1" };
	});
	harness.connection = screenConnection(hub, "ready");
	const projectProps = {
		route: { params: { hubId: "hub-1", projectKey: "p", title: "Project", tier: "archived", archived: true } },
		navigation: { navigate: () => {}, setParams: () => {} },
	} as unknown as ComponentProps<typeof ProjectScreen>;
	const tree = render(<ProjectScreen {...projectProps} />);
	await act(async () => {});
	expect(renderedText(tree)).toContain("Alpha");
	await act(async () => pressable(tree, "Load more · 1 remaining")?.props.onPress());
	expect(renderedText(tree)).toContain("Beta");
	expect(archivedReads).toEqual([
		{ catalog: "archived_projects", projectKey: "p" },
		{ catalog: "archived_projects", projectKey: "p", cursor: "c1" },
	]);
	tree.unmount();
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
		}) as unknown as Parameters<typeof pageConfirmationError>[0];

	it("checks a navigation page's generation", () => {
		expect(pageConfirmationError(source(true, "g1"), { generationId: "g1" })).toBeNull();
		expect(pageConfirmationError(source(true, "g2"), { generationId: "g1" })).toBe(
			"The hub restarted during the check.",
		);
		expect(pageConfirmationError(source(true, null), { generationId: "g1" })).toBe(
			"The hub restarted during the check.",
		);
	});

	it("checks only the read for a source navigation doesn't version", () => {
		expect(pageConfirmationError(source(false, null), { generationId: "g1" })).toBeNull();
		expect(pageConfirmationError(source(false, null, { ...loaded, error: "offline" }), { generationId: "g1" })).toBe(
			"The current navigation could not be confirmed.",
		);
	});
});
