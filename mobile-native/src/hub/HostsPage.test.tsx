import { createHubUpdateController, WireError } from "@evener/appwire-client";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { act } from "react-test-renderer";
import { beforeEach, expect, it, vi } from "vitest";
import { UPDATE_NEEDED } from "../board/connectionStatus";
import { HostsController } from "../hosts/hostsController";
import { hostRow, liveSession, type ScriptedFleet, scriptedFleet } from "../hosts/hostsTestUtils";
import { LiveSessionsReader } from "../hosts/liveCounts";
import { render, renderedText } from "../renderNative.testkit";
import { HostsPage } from "./HostsPage";
import { type HubRoutes, type HubSheetContextValue, HubSheetProvider } from "./hubSheetContext";

const status = vi.hoisted(() => ({ line: null as string | null }));
vi.mock("../board/connectionStatus", async (importOriginal) => ({
	...(await importOriginal<typeof import("../board/connectionStatus")>()),
	useConnectionStatusText: () => status.line,
}));
vi.mock("../ConnectionProvider", () => ({ useConnection: () => ({ state: "ready", fatal: false }) }));
vi.mock("@react-navigation/native", async () => {
	const { useEffect } = await import("react");
	return { useFocusEffect: (effect: () => undefined | (() => void)) => useEffect(effect, [effect]) };
});
vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));

const settle = () => new Promise((resolve) => setTimeout(resolve, 0));

async function mount(fleet: ScriptedFleet, options: { ready?: boolean; focus?: string; hubVersion?: string } = {}) {
	const updates = createHubUpdateController({
		client: () => ({
			request: (async () => ({
				channel: "release",
				buildChannel: "release",
				currentVersion: options.hubVersion ?? "0.9.412",
				currentCommit: "abc",
				updateAvailable: false,
				applicable: true,
			})) as never,
		}),
		awaitRestart: async () => true,
	});
	await updates.runCheck();
	const hosts = new HostsController(fleet.client);
	const live = new LiveSessionsReader(fleet.client);
	let context: HubSheetContextValue = {
		hubId: "hub-1",
		hubName: "magic-kingdom",
		client: null,
		ready: options.ready ?? true,
		canUseConnection: () => options.ready ?? true,
		updates,
		hosts,
		live,
	};
	const navigation = { navigate: vi.fn(), setParams: vi.fn() };
	let focus = options.focus;
	const page = () => (
		<HubSheetProvider value={context}>
			<HostsPage
				navigation={navigation as unknown as NativeStackScreenProps<HubRoutes, "Hosts">["navigation"]}
				route={{ key: "Hosts", name: "Hosts", params: { hubId: "hub-1", focus } }}
			/>
		</HubSheetProvider>
	);
	const tree = render(page());
	const setReady = async (ready: boolean) => {
		context = { ...context, ready, canUseConnection: () => ready };
		await act(async () => {
			tree.update(page());
			await settle();
		});
	};
	await act(async () => {
		await settle();
	});
	const row = (label: string) =>
		tree.root.findAll(
			(node) => typeof node.props.accessibilityLabel === "string" && node.props.accessibilityLabel.startsWith(label),
		)[0] ?? null;
	// A link's focus arriving, as the stack hands it to the mounted page.
	const setFocus = async (next: string | undefined) => {
		focus = next;
		await act(async () => {
			tree.update(page());
			await settle();
		});
	};
	return { tree, navigation, hosts, live, row, setReady, setFocus, dispose: () => (hosts.dispose(), live.dispose()) };
}

beforeEach(() => {
	status.line = null;
});

it("puts the hub's own machine first, named after the hub, with its live sessions and version (ruling 3)", async () => {
	const fleet = scriptedFleet([], [liveSession("local:a", "local"), liveSession("local:b", "local")]);
	const page = await mount(fleet);
	const own = page.row("magic-kingdom");
	expect(own?.props.accessibilityLabel).toBe("magic-kingdom, Connected · 2 live, 0.9.412");
	expect(renderedText(page.tree)).toContain("0.9.412");
	page.dispose();
});

it("words the hub's own machine the way the Hubs page words the hub", async () => {
	// Connected but too old for this app: the connection line speaks while
	// the connection still reads as ready.
	status.line = UPDATE_NEEDED;
	const page = await mount(scriptedFleet([], [liveSession("local:a", "local")]));
	expect(page.row("magic-kingdom")?.props.accessibilityLabel).toBe("magic-kingdom, Update needed · 1 live, 0.9.412");
	page.dispose();
});

it("opens the hub's own machine like any other host (audit M1)", async () => {
	const page = await mount(scriptedFleet([hostRow("paradise-park")]));
	const own = page.row("magic-kingdom");
	expect(own?.props.accessibilityRole).toBe("button");
	const chevrons = own?.findAll((node) => String(node.type) === "SymbolView" && node.props.name === "chevron.right");
	expect(chevrons).toHaveLength(1);
	act(() => own?.props.onPress());
	expect(page.navigation.navigate).toHaveBeenCalledWith("OwnHost", { hubId: "hub-1" });
	page.dispose();
});

it("says the hub's own machine is reconnecting while the connection is down", async () => {
	status.line = "Reconnecting…";
	const page = await mount(scriptedFleet([]), { ready: false });
	expect(page.row("magic-kingdom")?.props.accessibilityLabel).toBe("magic-kingdom, Reconnecting…, 0.9.412");
	page.dispose();
});

it("lists each host with its state, system and live sessions, and its version with a drift tag (spec 12)", async () => {
	const fleet = scriptedFleet(
		[
			hostRow("paradise-park", { os: "darwin", arch: "arm64", hubVersion: "0.9.409" }),
			hostRow("studio", { hubVersion: "0.9.412", attached: false, midAttach: true }),
			hostRow("attic", { attached: false }),
		],
		[liveSession("paradise-park:a", "paradise-park"), liveSession("studio:b", "studio")],
	);
	const page = await mount(fleet);
	expect(page.row("paradise-park")?.props.accessibilityLabel).toBe(
		"paradise-park, Connected · macOS · arm64 · 1 live, 0.9.409, Hub runs 0.9.412",
	);
	expect(page.row("studio")?.props.accessibilityLabel).toBe(
		"studio, Offline · reconnecting · 1 live, out of reach, 0.9.412",
	);
	expect(page.row("attic")?.props.accessibilityLabel).toBe("attic, Offline · No live sessions");
	const text = renderedText(page.tree);
	expect(text).toContain("0.9.409");
	expect(text).toContain("Hub runs 0.9.412");
	// Only the drifting host carries the tag; a host of unknown version shows neither.
	expect(text.match(/Hub runs 0\.9\.412/g)).toHaveLength(1);
	page.dispose();
});

it("opens a host's detail", async () => {
	const page = await mount(scriptedFleet([hostRow("paradise-park")]));
	act(() => page.row("paradise-park")?.props.onPress());
	expect(page.navigation.navigate).toHaveBeenCalledWith("HostDetail", { hubId: "hub-1", name: "paradise-park" });
	page.dispose();
});

// The page clears a link's focus once it acts, so the same host named again
// by a later link opens again.
it("opens a host again when a later link names it again", async () => {
	const page = await mount(scriptedFleet([hostRow("paradise-park")]), { focus: "paradise-park" });
	expect(page.navigation.navigate).toHaveBeenCalledTimes(1);
	await page.setFocus(undefined);
	await page.setFocus("paradise-park");
	expect(page.navigation.navigate).toHaveBeenCalledTimes(2);
	expect(page.navigation.navigate).toHaveBeenLastCalledWith("HostDetail", { hubId: "hub-1", name: "paradise-park" });
	page.dispose();
});

it("opens the host a notice named, once, and clears the request", async () => {
	const page = await mount(scriptedFleet([hostRow("paradise-park")]), { focus: "paradise-park" });
	expect(page.navigation.navigate).toHaveBeenCalledWith("HostDetail", { hubId: "hub-1", name: "paradise-park" });
	expect(page.navigation.setParams).toHaveBeenCalledWith({ focus: undefined });
	page.dispose();
});

it("says where hosts come from, and never asks to reconnect", async () => {
	const page = await mount(scriptedFleet([hostRow("paradise-park", { attached: false })]));
	expect(renderedText(page.tree)).toContain(
		"Hosts come from hub.toml or were added in the web app. Add hosts from the web app; they need an SSH address and a key.",
	);
	expect(renderedText(page.tree)).not.toMatch(/\bReconnect\b/);
	page.dispose();
});

it("says why the hub's hosts didn't load when its first answer is a refusal", async () => {
	const fleet = scriptedFleet([]);
	fleet.client.request = (async () => {
		throw new WireError("the hub is still starting", -32000);
	}) as never;
	const page = await mount(fleet);
	expect(renderedText(page.tree)).toContain("Couldn't list this hub's hosts: the hub is still starting");
	page.dispose();
});

it("reads the live sessions again when the connection comes back", async () => {
	const fleet = scriptedFleet([hostRow("paradise-park")], [liveSession("paradise-park:a", "paradise-park")]);
	const answer = fleet.client.request;
	let down = true;
	fleet.client.request = (async (method: string, params: unknown) => {
		if (down && method === "evener/navigation/read") throw new WireError("not connected", -32000);
		return answer(method as never, params as never);
	}) as never;
	const page = await mount(fleet, { ready: false });
	expect(page.row("paradise-park")?.props.accessibilityLabel).toContain("No live sessions");
	down = false;
	await page.setReady(true);
	expect(page.row("paradise-park")?.props.accessibilityLabel).toContain("1 live");
	page.dispose();
});

it("waits quietly, connected, before the hub has listed its hosts (spec 14)", async () => {
	const fleet = scriptedFleet([]);
	fleet.client.request = (() => new Promise(() => {})) as never;
	const page = await mount(fleet);
	expect(renderedText(page.tree)).not.toContain("Connecting");
	expect(page.tree.root.findAllByProps({ accessibilityLabel: "Loading hosts" })).not.toHaveLength(0);
	page.dispose();
});
