import type { HostRow } from "@evener/appwire-client";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import { createNewSessionService } from "../../../mobile/src/services/newSession";
import { HostsController } from "../hosts/hostsController";
import { hostRow, liveSession, scriptedFleet } from "../hosts/hostsTestUtils";
import { LiveSessionsReader } from "../hosts/liveCounts";
import { createNewSessionStore } from "../newSession";
import { pressable, render, renderedText, settle, textOf, unmountMountedTrees } from "../renderNative.testkit";
import { HostPicker } from "./HostPicker";
import { NewSessionProvider, type NewSessionRoutes } from "./newSessionContext";
import { sheetContext } from "./newSessionTestUtils";

vi.mock("../board/connectionStatus", async (importOriginal) => ({
	...(await importOriginal<typeof import("../board/connectionStatus")>()),
	useConnectionStatusText: () => null,
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

async function mount(hosts: HostRow[], source = "local") {
	const fleet = scriptedFleet(hosts, [
		liveSession("local:a", "local"),
		liveSession("local:b", "local"),
		liveSession("paradise-park:c", "paradise-park"),
	]);
	const client = {
		request: async (method: string, params: unknown) => {
			if (method === "evener/host/request" || method.startsWith("evener/projects") || method === "model/list") {
				fleet.calls.push({ method, params });
				const forwarded = method === "evener/host/request" ? (params as { method: string }).method : method;
				if (forwarded === "evener/path/validate") return { path: "", valid: true };
				return { data: [] };
			}
			return fleet.client.request(method as never, params as never);
		},
		onNotification: () => () => {},
	};
	const store = createNewSessionStore("hub-1");
	store.getState().bind(createNewSessionService(client as never));
	store.setState({ source, cwd: "/home/jesse/git/evener" });
	const hostsController = new HostsController(client as never);
	const live = new LiveSessionsReader(client as never);
	const context = sheetContext(store, { client: client as never, hosts: hostsController, live });
	const navigation = { goBack: vi.fn() };
	const tree = render(
		<NewSessionProvider value={context}>
			<HostPicker
				navigation={navigation as unknown as NativeStackScreenProps<NewSessionRoutes, "Host">["navigation"]}
				route={{ key: "Host", name: "Host", params: undefined }}
			/>
		</NewSessionProvider>,
	);
	await settle();
	const row = (label: string) =>
		tree.root.findAll(
			(node) =>
				node.props.accessibilityRole !== "radio" &&
				typeof node.props.accessibilityLabel === "string" &&
				node.props.accessibilityLabel.startsWith(`${label}, `),
		)[0] ?? null;
	return {
		tree,
		fleet,
		store,
		navigation,
		row,
		dispose: () => {
			unmountMountedTrees();
			hostsController.dispose();
			live.dispose();
		},
	};
}

it("lists the hub's own machine first, then each host, and checks the chosen one", async () => {
	const picker = await mount(
		[hostRow("paradise-park", { os: "darwin", arch: "arm64" }), hostRow("attic", { attached: false })],
		"paradise-park",
	);
	const own = picker.row("magic-kingdom");
	expect(own?.props.accessibilityLabel).toBe("magic-kingdom, 2 live");
	expect(own?.props.accessibilityState).toEqual({ disabled: false, selected: false });
	const park = picker.row("paradise-park");
	expect(park?.props.accessibilityLabel).toBe("paradise-park, macOS · arm64 · 1 live");
	expect(park?.props.accessibilityState).toEqual({ disabled: false, selected: true });
	expect(picker.row("attic")?.props.accessibilityLabel).toBe("attic, Offline");
	expect(picker.fleet.calls.map((call) => call.method)).toContain("evener/host/list");
	picker.dispose();
});

it("won't choose an offline host, and connects it from its Connect", async () => {
	const picker = await mount([hostRow("attic", { attached: false })]);
	const attic = picker.row("attic");
	expect(attic?.props.accessibilityState).toEqual({ disabled: true, selected: false });
	expect(attic?.props.onPress).toBeUndefined();
	picker.fleet.attach.hold = true;
	const connect = pressable(picker.tree, "Connect attic");
	expect(connect && textOf(connect)).toBe("Connect");
	await act(async () => connect?.props.onPress());
	expect(picker.fleet.calls.find((call) => call.method === "evener/host/attach")?.params).toEqual({ host: "attic" });
	const connecting = pressable(picker.tree, "Connect attic");
	expect(connecting && textOf(connecting)).toBe("Connecting…");
	expect(connecting?.props.disabled).toBe(true);
	await act(async () => picker.fleet.releaseAttach());
	await settle();
	expect(pressable(picker.tree, "Connect attic")).toBeUndefined();
	expect(picker.row("attic")?.props.accessibilityState).toEqual({ disabled: false, selected: false });
	picker.dispose();
});

it("offers no Connect for a host the hub is already reaching for", async () => {
	const picker = await mount([hostRow("studio", { attached: false, midAttach: true })]);
	expect(picker.row("studio")?.props.accessibilityLabel).toBe("studio, Offline · reconnecting");
	expect(pressable(picker.tree, "Connect studio")).toBeUndefined();
	picker.dispose();
});

it("changes the host and goes back to the form when one is chosen", async () => {
	const picker = await mount([hostRow("paradise-park")]);
	await act(async () => picker.row("paradise-park")?.props.onPress());
	await settle();
	expect(picker.store.getState().source).toBe("paradise-park");
	expect(picker.fleet.calls.some((call) => call.method === "evener/host/request")).toBe(true);
	expect(picker.navigation.goBack).toHaveBeenCalledTimes(1);
	await act(async () => picker.row("magic-kingdom")?.props.onPress());
	await settle();
	expect(picker.store.getState().source).toBe("local");
	expect(picker.navigation.goBack).toHaveBeenCalledTimes(2);
	picker.dispose();
});

it("says why the hub refused to connect a host", async () => {
	const picker = await mount([hostRow("attic", { attached: false })]);
	picker.fleet.attach.refuse = "ssh: connect to host attic port 22: Connection refused";
	await act(async () => pressable(picker.tree, "Connect attic")?.props.onPress());
	await settle();
	expect(renderedText(picker.tree)).toContain("attic: ssh: connect to host attic port 22: Connection refused");
	expect(textOf(pressable(picker.tree, "Connect attic") as never)).toBe("Connect");
	picker.dispose();
});
