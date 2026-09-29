import { createHubUpdateController, WireError } from "@evener/appwire-client";
import type { NativeStackNavigationOptions, NativeStackScreenProps } from "@react-navigation/native-stack";
import type { ReactElement } from "react";
import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import { HostsController } from "../hosts/hostsController";
import { hostRow, type ScriptedFleet, scriptedFleet } from "../hosts/hostsTestUtils";
import { LiveSessionsReader } from "../hosts/liveCounts";
import { render, renderedText } from "../renderNative.testkit";
import { HostEditPage } from "./HostEditPage";
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

async function mount(fleet: ScriptedFleet, name: string, options_: { ready?: boolean } = {}) {
	const hosts = new HostsController(fleet.client, fleet.newMutationId);
	const live = new LiveSessionsReader(fleet.client);
	const context: HubSheetContextValue = {
		hubId: "hub-1",
		hubName: "magic-kingdom",
		client: null,
		ready: options_.ready ?? true,
		canUseConnection: () => options_.ready ?? true,
		updates: createHubUpdateController({
			client: () => {
				throw new Error("not in this test");
			},
			awaitRestart: async () => true,
		}),
		hosts,
		live,
	};
	let options: NativeStackNavigationOptions = {};
	const navigation = {
		goBack: vi.fn(),
		setOptions: vi.fn((next: NativeStackNavigationOptions) => {
			options = { ...options, ...next };
		}),
	};
	const tree = render(
		<HubSheetProvider value={context}>
			<HostEditPage
				navigation={navigation as unknown as NativeStackScreenProps<HubRoutes, "HostEdit">["navigation"]}
				route={{ key: "HostEdit", name: "HostEdit", params: { hubId: "hub-1", name } }}
			/>
		</HubSheetProvider>,
	);
	await settle();
	const field = (label: string) => tree.root.findByProps({ accessibilityLabel: label, editable: true });
	const type = (label: string, text: string) => act(() => field(label).props.onChangeText(text));
	/** A header button as the stack would place it (a HeaderButton element). */
	const header = (side: "headerLeft" | "headerRight") =>
		(options[side] as (props: { canGoBack: boolean }) => ReactElement<{ onPress(): void; disabled?: boolean }>)({
			canGoBack: true,
		});
	const save = async () => {
		await act(async () => header("headerRight").props.onPress());
		await settle();
	};
	return { tree, navigation, hosts, field, type, header, save, options: () => options, dispose: () => hosts.dispose() };
}

const attic = hostRow("attic", {
	address: "attic.lan",
	user: "jesse",
	keyPath: "~/.ssh/attic",
	roots: ["/srv/b", "/srv/a"],
});

it("titles the page for its host and fills every field from the host's row", async () => {
	const page = await mount(scriptedFleet([attic]), "attic");
	expect(page.options().title).toBe("Edit attic");
	expect(page.field("SSH address").props.value).toBe("attic.lan");
	expect(page.field("User").props.value).toBe("jesse");
	expect(page.field("Key path").props.value).toBe("~/.ssh/attic");
	expect(page.field("Evener path").props.value).toBe("");
	expect(page.field("Roots").props.value).toBe("/srv/b\n/srv/a");
	expect(page.field("Roots").props.multiline).toBe(true);
	const text = renderedText(page.tree);
	expect(text).toContain("Hub config path");
	expect(text).toContain("Optional listen address of the host's hub, when it is not the default.");
	page.dispose();
});

it("saves every field trimmed and the roots one per line, then goes back", async () => {
	const fleet = scriptedFleet([attic]);
	const page = await mount(fleet, "attic");
	page.type("SSH address", "  attic.local ");
	page.type("Roots", " /srv/c \n\n/srv/a\n");
	await page.save();
	const sent = fleet.calls.find((call) => call.method === "evener/host/update")?.params;
	expect(sent).toMatchObject({
		name: "attic",
		entry: {
			address: "attic.local",
			user: "jesse",
			keyPath: "~/.ssh/attic",
			evenerPath: "",
			configPath: "",
			addr: "",
			roots: ["/srv/c", "/srv/a"],
		},
		expectedGeneration: 1,
	});
	expect(page.navigation.goBack).toHaveBeenCalledTimes(1);
	page.dispose();
});

it("puts a refusal that names a field under that field, and stays", async () => {
	const fleet = scriptedFleet([attic]);
	fleet.refuse.update = new WireError('host "attic": missing ssh destination', -32602, {
		evenerErrorInfo: "invalidHostField",
		field: "address",
	});
	const page = await mount(fleet, "attic");
	page.type("SSH address", "");
	await page.save();
	const text = renderedText(page.tree);
	const message = 'host "attic": missing ssh destination';
	expect(text).toContain(message);
	// Under the SSH address field, before the next field's label.
	expect(text.indexOf(message)).toBeGreaterThan(text.indexOf("SSH destination, e.g."));
	expect(text.indexOf(message)).toBeLessThan(text.indexOf("User"));
	expect(page.navigation.goBack).not.toHaveBeenCalled();
	page.dispose();
});

it("puts any other refusal above the fields", async () => {
	const fleet = scriptedFleet([attic]);
	fleet.refuse.update = new WireError("the registry is read-only right now", -32000);
	const page = await mount(fleet, "attic");
	await page.save();
	const text = renderedText(page.tree);
	expect(text.indexOf("the registry is read-only right now")).toBeLessThan(text.indexOf("SSH address"));
	page.dispose();
});

it("holds Save only while it saves: the hub is the one validator", async () => {
	const fleet = scriptedFleet([attic]);
	const answer = fleet.client.request;
	let release = () => {};
	fleet.client.request = (async (method: string, params: unknown) => {
		if (method === "evener/host/update") await new Promise<void>((resolve) => (release = resolve));
		return answer(method as never, params as never);
	}) as never;
	const page = await mount(fleet, "attic");
	page.type("SSH address", "");
	expect(page.header("headerRight").props.disabled).toBe(false);
	await act(async () => page.header("headerRight").props.onPress());
	expect(page.header("headerRight").props.disabled).toBe(true);
	// Cancel holds too: the save would otherwise go back a second time.
	expect(page.header("headerLeft").props.disabled).toBe(true);
	await act(async () => release());
	await settle();
	page.dispose();
});

it("goes back without saving on Cancel", async () => {
	const fleet = scriptedFleet([attic]);
	const page = await mount(fleet, "attic");
	await act(async () => page.header("headerLeft").props.onPress());
	expect(page.navigation.goBack).toHaveBeenCalledTimes(1);
	expect(fleet.calls.some((call) => call.method === "evener/host/update")).toBe(false);
	page.dispose();
});

it("holds Save while the phone's connection is down", async () => {
	const page = await mount(scriptedFleet([attic]), "attic", { ready: false });
	expect(page.header("headerRight").props.disabled).toBe(true);
	page.dispose();
});

it("sends one update however fast Save is pressed twice", async () => {
	const fleet = scriptedFleet([attic]);
	const page = await mount(fleet, "attic");
	const save = page.header("headerRight").props.onPress;
	await act(async () => {
		save();
		save();
	});
	await settle();
	expect(fleet.calls.filter((call) => call.method === "evener/host/update")).toHaveLength(1);
	page.dispose();
});

it("goes back when its host leaves the hub's list while it is open", async () => {
	const fleet = scriptedFleet([attic]);
	const page = await mount(fleet, "attic");
	fleet.hosts = [];
	await act(async () => page.hosts.read());
	expect(page.navigation.goBack).toHaveBeenCalledTimes(1);
	page.dispose();
});

it("goes back once when the host is removed elsewhere while its edit saves", async () => {
	const fleet = scriptedFleet([attic]);
	const answer = fleet.client.request;
	fleet.client.request = (async (method: string, params: unknown) => {
		const result = await answer(method as never, params as never);
		// Another client removes the host as this edit lands.
		if (method === "evener/host/update") fleet.hosts = [];
		return result;
	}) as never;
	const page = await mount(fleet, "attic");
	await page.save();
	expect(page.navigation.goBack).toHaveBeenCalledTimes(1);
	page.dispose();
});
