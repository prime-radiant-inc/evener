import { createHubUpdateController, type HostRow, WireError } from "@evener/appwire-client";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { act } from "react-test-renderer";
import { afterEach, expect, it, vi } from "vitest";
import { fonts, palettes, uiType } from "../design/tokens";
import { HostsController } from "../hosts/hostsController";
import { hostRow, liveSession, type ScriptedFleet, scriptedFleet } from "../hosts/hostsTestUtils";
import { LiveSessionsReader } from "../hosts/liveCounts";
import {
	alertRequests,
	render,
	renderedText,
	settle,
	testIDSuffixes,
	textOf,
	unmountMountedTrees,
} from "../renderNative.testkit";
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

// Each mount's controllers, disposed after the test's trees unmount, so a
// failing assertion can't leak them into the next test.
const disposers: (() => void)[] = [];
afterEach(() => {
	unmountMountedTrees();
	for (const dispose of disposers.splice(0)) dispose();
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
		currentIncarnation: async () => null,
		awaitRestart: async () => true,
	});
	await updates.runCheck();
	const hosts = new HostsController(fleet.client, fleet.newMutationId);
	const live = new LiveSessionsReader(fleet.client);
	disposers.push(() => {
		hosts.dispose();
		live.dispose();
	});
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
	return {
		tree,
		navigation,
		hosts,
		labelled,
		button,
		/** The host's state, as its Status row reports it. */
		status: () => testIDSuffixes(tree, "host-status-"),
		/** Why the page's footer speaks up: drift, or an offline state. */
		footers: () => testIDSuffixes(tree, "host-footer-"),
		lastError: () =>
			tree.root.findAll((node) => typeof node.type === "string" && node.props.testID === "host-last-error"),
	};
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
	expect(page.status()).toEqual(["connected"]);
	expect(page.labelled("Version")?.props.accessibilityLabel).toContain("0.9.409");
	expect(page.labelled("System")?.props.accessibilityLabel).toBe("System, macOS · arm64");
	expect(page.labelled("Sessions")?.props.accessibilityLabel).toBe("Sessions, 1 live");
	expect(page.labelled("Project roots")?.props.accessibilityLabel).toBe("Project roots, /Users/jesse/git\n/srv/work");
	// Spec 12 lists no "Defined in": every host can be edited and removed.
	expect(page.labelled("Defined in")).toBeNull();
	expect(page.footers()).toEqual(["drift"]);
	expect(page.lastError()).toHaveLength(0);
	expect(page.button("Connect")).toBeNull();
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
	expect(page.status()).toEqual(["offline"]);
	expect(page.lastError().map((node) => node.props.children)).toEqual(["ssh: connect refused"]);
	expect(page.footers()).toEqual(["offline"]);
	expect(page.button("Connect")).not.toBeNull();
});

it("keeps the last error inside its own group, in footnote Menlo and the danger ink (hub.js:67)", async () => {
	const page = await mount(
		scriptedFleet([hostRow("attic", { attached: false, origin: "sidecar", lastAttachError: "ssh: connect refused" })]),
		"attic",
	);
	const [error] = page.lastError();
	expect(
		page.tree.root.findAll((node) => node.type === Group && node.findAll((inner) => inner === error).length > 0),
	).toHaveLength(1);
	expect(error?.props.children).toBe("ssh: connect refused");
	const style = Object.assign({}, ...[error?.props.style].flat());
	expect(style.fontFamily).toBe(fonts.mono);
	expect(style.color).toBe(palettes.light.dangerInk);
	// Footnote-sized, as the prototype's 13px, not a 17pt row label.
	expect(style.fontSize).toBe(uiType.footnote.fontSize);
});

it("offers no Connect while the hub is already reaching for the host", async () => {
	const page = await mount(scriptedFleet([hostRow("studio", { attached: false, midAttach: true })]), "studio");
	expect(page.status()).toEqual(["reconnecting"]);
	expect(page.button("Connect")).toBeNull();
	expect(page.footers()).toEqual(["reconnecting"]);
});

it("connects a host, reading Connecting… while the hub attaches it", async () => {
	const fleet = scriptedFleet([hostRow("attic", { attached: false })]);
	fleet.attach.hold = true;
	const page = await mount(fleet, "attic");
	await act(async () => page.button("Connect")?.props.onPress());
	expect(page.button("Connecting…")?.props.accessibilityState.disabled).toBe(true);
	expect(fleet.calls.some((call) => call.method === "evener/host/attach")).toBe(true);
	expect(page.status()).toEqual(["connecting"]);
	await act(async () => fleet.releaseAttach());
	await settle();
	expect(page.status()).toEqual(["connected"]);
	expect(page.button("Connect")).toBeNull();
});

it("drops Connecting… as soon as the rows say the host is attached", async () => {
	const fleet = scriptedFleet([hostRow("attic", { attached: false })]);
	fleet.attach.hold = true;
	const page = await mount(fleet, "attic");
	await act(async () => page.button("Connect")?.props.onPress());
	// The hub's supervisor attaches it while the Connect is still out.
	fleet.hosts = [hostRow("attic")];
	await act(async () => page.hosts.read());
	expect(page.status()).toEqual(["connected"]);
	expect(page.button("Connecting…")).toBeNull();
	await act(async () => fleet.releaseAttach());
});

it("keeps the remote build separate from the controller through Connect and refresh", async () => {
	const fleet = scriptedFleet([hostRow("attic", { attached: false })]);
	const page = await mount(fleet, "attic");
	for (const hubVersion of ["remote-build-a", undefined, "remote-build-b"]) {
		fleet.hosts = [hostRow("attic", { attached: false, hubVersion })];
		await act(async () => page.hosts.read());
		await act(async () => page.button("Connect")?.props.onPress());
		await settle();
		await act(async () => page.hosts.read());
		expect(page.status()).toEqual(["connected"]);
		expect(page.hosts.getSnapshot().rows?.[0]?.hubVersion).toBe(hubVersion);
		const [valueNode] = page.labelled("Version")?.findAllByProps({ testID: "row-value" }) ?? [];
		if (!valueNode) throw new Error("version value missing");
		// RowValue renders the host value before its separate controller tag.
		const [versionText] = valueNode.findAll((node) => node.type === ("Text" as never));
		if (!versionText) throw new Error("version text missing");
		if (hubVersion !== undefined) expect(textOf(versionText)).toBe(hubVersion);
		else {
			expect(textOf(versionText)).not.toContain("remote-build-a");
			expect(page.footers()).not.toContain("drift");
		}
		expect(textOf(versionText)).not.toContain("0.9.412");
	}
	expect(fleet.calls.filter((call) => call.method === "evener/host/attach")).toHaveLength(3);
	expect(fleet.calls.some((call) => call.method === "evener/host/deploy")).toBe(false);
});

it("says why the hub refused to connect a host", async () => {
	const fleet = scriptedFleet([hostRow("attic", { attached: false })]);
	fleet.attach.refuse = "host key mismatch";
	const page = await mount(fleet, "attic");
	await act(async () => page.button("Connect")?.props.onPress());
	await settle();
	expect(renderedText(page.tree)).toContain("host key mismatch");
	expect(page.button("Connect")).not.toBeNull();
});

it("drops a refused Connect's message once the hub attaches the host on its own", async () => {
	const fleet = scriptedFleet([hostRow("attic", { attached: false })]);
	fleet.attach.refuse = "host key mismatch";
	const page = await mount(fleet, "attic");
	await act(async () => page.button("Connect")?.props.onPress());
	await settle();
	fleet.hosts = [hostRow("attic")];
	await act(async () => page.hosts.read());
	expect(page.status()).toEqual(["connected"]);
	expect(renderedText(page.tree)).not.toContain("host key mismatch");
});

it("says a host's version is unknown when the hub doesn't know it", async () => {
	const page = await mount(scriptedFleet([hostRow("attic", { os: "linux", arch: "x86_64" })]), "attic");
	// The row still says something, with no version and no drift tag.
	const [valueNode] = page.labelled("Version")?.findAllByProps({ testID: "row-value" }) ?? [];
	if (!valueNode) throw new Error("no Version value");
	const value = textOf(valueNode);
	expect(value).not.toBe("");
	expect(value).not.toMatch(/\d/);
});

it("holds Connect while the phone's connection is down", async () => {
	const page = await mount(scriptedFleet([hostRow("attic", { attached: false })]), "attic", { ready: false });
	expect(page.button("Connect")?.props.accessibilityState.disabled).toBe(true);
});

it("goes back when the host leaves the hub's list", async () => {
	const fleet = scriptedFleet([hostRow("attic")]);
	const page = await mount(fleet, "attic");
	expect(page.navigation.goBack).not.toHaveBeenCalled();
	fleet.hosts = [];
	await act(async () => page.hosts.read());
	expect(page.navigation.goBack).toHaveBeenCalledTimes(1);
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
	// Edit pushes its own page, so it carries a chevron.
	expect(
		page.button("Edit")?.findAll((node) => String(node.type) === "SymbolView" && node.props.name === "chevron.right"),
	).toHaveLength(1);
	await act(async () => page.button("Edit")?.props.onPress());
	expect(page.navigation.navigate).toHaveBeenCalledWith("HostEdit", { hubId: "hub-1", name: "attic" });
	expect(page.button("Remove")).not.toBeNull();
});

it("removes a host once confirmed, then goes back as it leaves the list", async () => {
	alertRequests.length = 0;
	const fleet = scriptedFleet([hostRow("attic")]);
	const page = await mount(fleet, "attic");
	await act(async () => page.button("Remove")?.props.onPress());
	expect(alertRequests.at(-1)?.title).toContain("attic");
	expect(alertRequests.at(-1)?.buttons?.find((button) => button.text === "Remove")?.style).toBe("destructive");
	expect(fleet.calls.some((call) => call.method === "evener/host/remove")).toBe(false);
	await confirmRemove();
	expect(fleet.calls.some((call) => call.method === "evener/host/remove")).toBe(true);
	expect(page.navigation.goBack).toHaveBeenCalledTimes(1);
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
});

it("holds Edit and Remove while the phone's connection is down", async () => {
	const page = await mount(scriptedFleet([hostRow("attic")]), "attic", { ready: false });
	expect(page.button("Edit")?.props.accessibilityState.disabled).toBe(true);
	expect(page.button("Remove")?.props.accessibilityState.disabled).toBe(true);
});
