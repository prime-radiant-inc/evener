import { createHubUpdateController } from "@evener/appwire-client";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { act } from "react-test-renderer";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { HostsController } from "../hosts/hostsController";
import { liveSession, type ScriptedFleet, scriptedFleet } from "../hosts/hostsTestUtils";
import { LiveSessionsReader } from "../hosts/liveCounts";
import { render, renderedText, settle, unmountMountedTrees } from "../renderNative.testkit";
import { type HubRoutes, type HubSheetContextValue, HubSheetProvider } from "./hubSheetContext";
import { OwnHostPage } from "./OwnHostPage";

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

// Each mount's controllers, disposed after the test's trees unmount, so a
// failing assertion can't leak them into the next test.
const disposers: (() => void)[] = [];
afterEach(() => {
	unmountMountedTrees();
	for (const dispose of disposers.splice(0)) dispose();
});

async function mount(fleet: ScriptedFleet) {
	const updates = createHubUpdateController({
		client: () => ({
			request: (async () => ({
				channel: "release",
				buildChannel: "release",
				currentVersion: "0.9.412",
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
	disposers.push(() => {
		hosts.dispose();
		live.dispose();
	});
	const ready = true;
	let context: HubSheetContextValue = {
		hubId: "hub-1",
		hubName: "magic-kingdom",
		client: null,
		ready,
		canUseConnection: () => ready,
		updates,
		hosts,
		live,
	};
	const navigation = { navigate: vi.fn(), goBack: vi.fn() };
	const page = () => (
		<HubSheetProvider value={context}>
			<OwnHostPage
				navigation={navigation as unknown as NativeStackScreenProps<HubRoutes, "OwnHost">["navigation"]}
				route={{ key: "OwnHost", name: "OwnHost", params: { hubId: "hub-1" } }}
			/>
		</HubSheetProvider>
	);
	const tree = render(page());
	await settle();
	const setReady = async (next: boolean) => {
		context = { ...context, ready: next, canUseConnection: () => next };
		await act(async () => tree.update(page()));
		await settle();
	};
	const labelled = (label: string) =>
		tree.root.findAll(
			(node) => typeof node.props.accessibilityLabel === "string" && node.props.accessibilityLabel.startsWith(label),
		)[0]?.props.accessibilityLabel ?? null;
	return { tree, labelled, setReady };
}

beforeEach(() => {
	status.line = null;
});

it("shows the hub's own machine: its state, its version and its live sessions (audit M1)", async () => {
	const page = await mount(scriptedFleet([], [liveSession("local:a", "local"), liveSession("local:b", "local")]));
	expect(page.labelled("Status")).toBe("Status, Connected");
	expect(page.labelled("Version")).toBe("Version, 0.9.412");
	expect(page.labelled("Sessions")).toBe("Sessions, 2 live");
	// The wire carries no system or project roots for the hub's own machine.
	expect(renderedText(page.tree)).not.toContain("System");
	expect(renderedText(page.tree)).not.toContain("Project roots");
});

it("says the hub's own machine is reconnecting in the Hub header's words, its sessions out of reach", async () => {
	const page = await mount(scriptedFleet([], [liveSession("local:a", "local")]));
	status.line = "Reconnecting…";
	await page.setReady(false);
	expect(page.labelled("Status")).toBe("Status, Reconnecting…");
	expect(page.labelled("Sessions")).toBe("Sessions, 1 live, out of reach");
});
