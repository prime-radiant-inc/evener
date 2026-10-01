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
import { expect, it, vi } from "vitest";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { wireSnapshot } from "@evener/appwire-client/testing/navigation";
import { ProjectsScreen, SessionLocationScreen } from "./ProjectsScreen";
import { render, renderedText, screenConnection } from "./renderNative.testkit";

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
	const screenProps = {
		route: {
			params: {
				hubId: "hub-1",
				location: {
					ref: "local:a",
					revealRef: "local:a",
					title: "Project",
					params: { resource: "project_page", projectKey: "p", tier: "current" },
				},
			},
		},
		navigation: { navigate: () => {}, setParams: () => {} },
	} as unknown as ComponentProps<typeof SessionLocationScreen>;
	const tree = render(<SessionLocationScreen {...screenProps} />);
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
// says nothing about a partial tree or missing related sessions.
it("pages a page the hub cut short through Load more, with no partial-tree notice", async () => {
	const hub = new FakeClient("ready");
	hub.on("evener/navigation/read", (params) =>
		wireSnapshot(params as never, { sessions: [{ ref: "local:a", title: "Alpha" }], remaining: 1, truncated: true }),
	);
	harness.connection = screenConnection(hub, "ready");
	const screenProps = {
		route: {
			params: {
				hubId: "hub-1",
				location: {
					ref: "local:a",
					revealRef: "local:a",
					title: "Project",
					params: { resource: "project_page", projectKey: "p", tier: "current" },
				},
			},
		},
		navigation: { navigate: () => {}, setParams: () => {} },
	} as unknown as ComponentProps<typeof SessionLocationScreen>;
	const tree = render(<SessionLocationScreen {...screenProps} />);
	await act(async () => {});
	const shown = renderedText(tree);
	expect(shown).toContain("Alpha");
	expect(shown).toContain("Load more · 1 remaining");
	expect(shown).not.toContain("partial session tree");
	expect(shown).not.toContain("related session");
	tree.unmount();
});
