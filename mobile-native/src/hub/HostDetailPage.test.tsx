import { createHubUpdateController, type HostRow, WireError } from "@evener/appwire-client";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import { fonts, palettes, uiType } from "../design/tokens";
import { HostsController } from "../hosts/hostsController";
import { hostRow, liveSession, type ScriptedFleet, scriptedFleet } from "../hosts/hostsTestUtils";
import { VERSION_DRIFT_FOOTER } from "../hosts/hostStatus";
import { LiveSessionsReader } from "../hosts/liveCounts";
import { alertRequests, render, renderedText } from "../renderNative.testkit";
import { Group } from "../sheet/Grouped";
import { HostDetailPage } from "./HostDetailPage";
import { type HubRoutes, type HubSheetContextValue, HubSheetProvider } from "./hubSheetContext";

vi.mock("../ConnectionProvider", () => ({ useConnection: () => ({ state: "ready", fatal: false }) }));
vi.mock("@react-navigation/native", async () => {
	const { useEffect } = await import("react");
	return { useFocusEffect: (effect: () => undefined | (() => void)) => useEffect(effect, [effect]) };
});
vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));

const settle = () =>
	act(async () => {
		await new Promise((resolve) => setTimeout(resolve, 0));
	});

async function mount(fleet: ScriptedFleet, name: string, options: { ready?: boolean } = {}) {
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
	const hosts = new HostsController(fleet.client, fleet.newMutationId);
	const live = new LiveSessionsReader(fleet.client);
	const context: HubSheetContextValue = {
		hubId: "hub-1",
		hubName: "magic-kingdom",
		client: null,
		ready: options.ready ?? true,
		canUseConnection: () => options.ready ?? true,
		updates,
		hosts,
		live,
	};
	const navigation = { goBack: vi.fn(), navigate: vi.fn() };
	const tree = render(
		<HubSheetProvider value={context}>
			<HostDetailPage
				navigation={navigation as unknown as NativeStackScreenProps<HubRoutes, "HostDetail">["navigation"]}
				route={{ key: "HostDetail", name: "HostDetail", params: { hubId: "hub-1", name } }}
			/>
		</HubSheetProvider>,
	);
	await settle();
	const labelled = (label: string) =>
		tree.root.findAll(
			(node) => typeof node.props.accessibilityLabel === "string" && node.props.accessibilityLabel.startsWith(label),
		)[0] ?? null;
	const button = (label: string) =>
		tree.root.findAllByProps({ accessibilityRole: "button" }).find((node) => node.props.accessibilityLabel === label) ??
		null;
	return { tree, navigation, hosts, labelled, button, dispose: () => (hosts.dispose(), live.dispose()) };
}

const drifting: HostRow = hostRow("paradise-park", {
	os: "darwin",
	arch: "arm64",
	hubVersion: "0.9.409",
	roots: ["/Users/jesse/git", "/srv/work"],
});

it("shows a connected host's facts, its version beside the drift tag, and the drift footer (spec 12)", async () => {
	const page = await mount(
		scriptedFleet([drifting], [liveSession("paradise-park:a", "paradise-park")]),
		"paradise-park",
	);
	expect(page.labelled("Status")?.props.accessibilityLabel).toBe("Status, Connected");
	expect(page.labelled("Version")?.props.accessibilityLabel).toBe("Version, 0.9.409, Hub runs 0.9.412");
	expect(page.labelled("System")?.props.accessibilityLabel).toBe("System, macOS · arm64");
	expect(page.labelled("Sessions")?.props.accessibilityLabel).toBe("Sessions, 1 live");
	expect(page.labelled("Project roots")?.props.accessibilityLabel).toBe("Project roots, /Users/jesse/git\n/srv/work");
	expect(page.labelled("Defined in")?.props.accessibilityLabel).toBe("Defined in, hub.toml");
	const text = renderedText(page.tree);
	expect(text).toContain(VERSION_DRIFT_FOOTER);
	expect(text).not.toContain("Last error");
	expect(page.button("Connect")).toBeNull();
	page.dispose();
});

it("puts an offline host's state in the attention ink, its last error in Menlo, and offers Connect", async () => {
	const page = await mount(
		scriptedFleet([hostRow("attic", { attached: false, origin: "sidecar", lastAttachError: "ssh: connect refused" })]),
		"attic",
	);
	const status = page.tree.root.findAll(
		(node) => node.props.children === "Offline" && typeof node.type === "string",
	)[0];
	expect(Object.assign({}, ...[status?.props.style].flat()).color).toBe(palettes.light.attentionInk);
	expect(page.labelled("Project roots")?.props.accessibilityLabel).toBe("Project roots, None");
	expect(page.labelled("Defined in")?.props.accessibilityLabel).toBe("Defined in, the app or web");
	const text = renderedText(page.tree);
	expect(text).toContain("Last error");
	expect(text).toContain("ssh: connect refused");
	expect(text).toContain("This host is offline, so its sessions can't be reached. Connect to reach them.");
	expect(page.button("Connect")).not.toBeNull();
	page.dispose();
});

it("keeps the last error inside its own group, in footnote Menlo and the danger ink (hub.js:67)", async () => {
	const page = await mount(
		scriptedFleet([hostRow("attic", { attached: false, origin: "sidecar", lastAttachError: "ssh: connect refused" })]),
		"attic",
	);
	const group = page.tree.root.findAll((node) => node.type === Group && node.props.label === "Last error");
	expect(group).toHaveLength(1);
	const error = group[0]?.find(
		(node) => typeof node.type === "string" && node.props.children === "ssh: connect refused",
	);
	const style = Object.assign({}, ...[error?.props.style].flat());
	expect(style.fontFamily).toBe(fonts.mono);
	expect(style.color).toBe(palettes.light.dangerInk);
	// Footnote-sized, as the prototype's 13px, not a 17pt row label.
	expect(style.fontSize).toBe(uiType.footnote.fontSize);
	page.dispose();
});

it("offers no Connect while the hub is already reaching for the host", async () => {
	const page = await mount(scriptedFleet([hostRow("studio", { attached: false, midAttach: true })]), "studio");
	expect(page.labelled("Status")?.props.accessibilityLabel).toBe("Status, Offline · reconnecting");
	expect(page.button("Connect")).toBeNull();
	expect(renderedText(page.tree)).toContain("The hub keeps trying to reach it.");
	page.dispose();
});

it("connects a host, reading Connecting… while the hub attaches it", async () => {
	const fleet = scriptedFleet([hostRow("attic", { attached: false })]);
	fleet.attach.hold = true;
	const page = await mount(fleet, "attic");
	await act(async () => page.button("Connect")?.props.onPress());
	expect(page.button("Connecting…")?.props.accessibilityState.disabled).toBe(true);
	expect(fleet.calls.some((call) => call.method === "evener/host/attach")).toBe(true);
	await act(async () => fleet.releaseAttach());
	await settle();
	expect(page.labelled("Status")?.props.accessibilityLabel).toBe("Status, Connected");
	expect(page.button("Connect")).toBeNull();
	page.dispose();
});

it("drops Connecting… as soon as the rows say the host is attached", async () => {
	const fleet = scriptedFleet([hostRow("attic", { attached: false })]);
	fleet.attach.hold = true;
	const page = await mount(fleet, "attic");
	await act(async () => page.button("Connect")?.props.onPress());
	// The hub's supervisor attaches it while the Connect is still out.
	fleet.hosts = [hostRow("attic")];
	await act(async () => page.hosts.read());
	expect(page.labelled("Status")?.props.accessibilityLabel).toBe("Status, Connected");
	expect(page.button("Connecting…")).toBeNull();
	await act(async () => fleet.releaseAttach());
	page.dispose();
});

it("says why the hub refused to connect a host", async () => {
	const fleet = scriptedFleet([hostRow("attic", { attached: false })]);
	fleet.attach.refuse = "host key mismatch";
	const page = await mount(fleet, "attic");
	await act(async () => page.button("Connect")?.props.onPress());
	await settle();
	expect(renderedText(page.tree)).toContain("host key mismatch");
	expect(page.button("Connect")).not.toBeNull();
	page.dispose();
});

it("drops a refused Connect's message once the hub attaches the host on its own", async () => {
	const fleet = scriptedFleet([hostRow("attic", { attached: false })]);
	fleet.attach.refuse = "host key mismatch";
	const page = await mount(fleet, "attic");
	await act(async () => page.button("Connect")?.props.onPress());
	await settle();
	fleet.hosts = [hostRow("attic")];
	await act(async () => page.hosts.read());
	expect(page.labelled("Status")?.props.accessibilityLabel).toBe("Status, Connected");
	expect(renderedText(page.tree)).not.toContain("host key mismatch");
	page.dispose();
});

it("says a host's version is unknown when the hub doesn't know it", async () => {
	const page = await mount(scriptedFleet([hostRow("attic", { os: "linux", arch: "x86_64" })]), "attic");
	expect(page.labelled("Version")?.props.accessibilityLabel).toBe("Version, Unknown");
	expect(renderedText(page.tree)).toContain("Unknown");
	page.dispose();
});

it("holds Connect while the phone's connection is down", async () => {
	const page = await mount(scriptedFleet([hostRow("attic", { attached: false })]), "attic", { ready: false });
	expect(page.button("Connect")?.props.accessibilityState.disabled).toBe(true);
	page.dispose();
});

it("goes back when the host leaves the hub's list", async () => {
	const fleet = scriptedFleet([hostRow("attic")]);
	const page = await mount(fleet, "attic");
	expect(page.navigation.goBack).not.toHaveBeenCalled();
	fleet.hosts = [];
	await act(async () => page.hosts.read());
	expect(page.navigation.goBack).toHaveBeenCalledTimes(1);
	page.dispose();
});

const confirmRemove = async () => {
	const alert = alertRequests.at(-1);
	await act(async () => alert?.buttons?.find((button) => button.text === "Remove")?.onPress?.());
	await settle();
};

it.each([
	["a host the app or web added", "sidecar"],
	["a host from hub.toml", "hub.toml"],
])("offers Edit and Remove on %s (spec 12)", async (_name, origin) => {
	const page = await mount(scriptedFleet([hostRow("attic", { origin })]), "attic");
	await act(async () => page.button("Edit")?.props.onPress());
	expect(page.navigation.navigate).toHaveBeenCalledWith("HostEdit", { hubId: "hub-1", name: "attic" });
	expect(page.button("Remove")).not.toBeNull();
	page.dispose();
});

it("removes a host once confirmed, then goes back as it leaves the list", async () => {
	alertRequests.length = 0;
	const fleet = scriptedFleet([hostRow("attic")]);
	const page = await mount(fleet, "attic");
	await act(async () => page.button("Remove")?.props.onPress());
	expect(alertRequests.at(-1)?.title).toBe("Remove attic?");
	expect(alertRequests.at(-1)?.message).toBe("The hub forgets this host. Add it again from the web app.");
	expect(alertRequests.at(-1)?.buttons?.find((button) => button.text === "Remove")?.style).toBe("destructive");
	expect(fleet.calls.some((call) => call.method === "evener/host/remove")).toBe(false);
	await confirmRemove();
	expect(fleet.calls.some((call) => call.method === "evener/host/remove")).toBe(true);
	expect(page.navigation.goBack).toHaveBeenCalledTimes(1);
	page.dispose();
});

it("says why the hub refused to remove a host, and stays", async () => {
	alertRequests.length = 0;
	const fleet = scriptedFleet([hostRow("attic")]);
	fleet.refuse.remove = new WireError('host "attic" has live sessions', -32000);
	const page = await mount(fleet, "attic");
	await act(async () => page.button("Remove")?.props.onPress());
	await confirmRemove();
	expect(renderedText(page.tree)).toContain('host "attic" has live sessions');
	expect(page.navigation.goBack).not.toHaveBeenCalled();
	page.dispose();
});

it("holds Edit and Remove while the phone's connection is down", async () => {
	const page = await mount(scriptedFleet([hostRow("attic")]), "attic", { ready: false });
	expect(page.button("Edit")?.props.accessibilityState.disabled).toBe(true);
	expect(page.button("Remove")?.props.accessibilityState.disabled).toBe(true);
	page.dispose();
});
