import { createHubUpdateController, WireError } from "@evener/appwire-client";
import type { NativeStackNavigationOptions, NativeStackScreenProps } from "@react-navigation/native-stack";
import type { ReactElement } from "react";
import { act } from "react-test-renderer";
import { afterEach, expect, it, vi } from "vitest";
import { HostsController } from "../hosts/hostsController";
import { hostRow, type ScriptedFleet, scriptedFleet } from "../hosts/hostsTestUtils";
import { LiveSessionsReader } from "../hosts/liveCounts";
import { alertRequests, render, renderedText, unmountMountedTrees } from "../renderNative.testkit";
import { HostEditPage } from "./HostEditPage";
import { type HubRoutes, type HubSheetContextValue, HubSheetProvider } from "./hubSheetContext";

vi.mock("../ConnectionProvider", () => ({ useConnection: () => ({ state: "ready", fatal: false }) }));
// What the page's removal guard (useSheet's usePreventRemove) holds, and
// the navigation object the hook reads: a test plays the stack's part.
const guard = vi.hoisted(() => ({
	prevented: false,
	onPrevent: null as null | ((options: { data: { action: unknown } }) => void),
	navigation: null as unknown,
}));
vi.mock("@react-navigation/native", async () => {
	const { useEffect } = await import("react");
	return {
		useFocusEffect: (effect: () => undefined | (() => void)) => useEffect(effect, [effect]),
		useNavigation: () => guard.navigation,
		usePreventRemove: (prevent: boolean, onPrevent: (options: { data: { action: unknown } }) => void) => {
			guard.prevented = prevent;
			guard.onPrevent = onPrevent;
		},
	};
});
vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));

const settle = () =>
	act(async () => {
		await new Promise((resolve) => setTimeout(resolve, 0));
	});

// Each mount's controllers, disposed after the test's trees unmount, so a
// failing assertion can't leak them into the next test.
const disposers: (() => void)[] = [];
afterEach(() => {
	unmountMountedTrees();
	for (const dispose of disposers.splice(0)) dispose();
});

async function mount(fleet: ScriptedFleet, name: string, options_: { ready?: boolean } = {}) {
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
	const focus = { focused: true };
	const navigation = {
		isFocused: () => focus.focused,
		goBack: vi.fn(),
		dispatch: vi.fn(),
		setOptions: vi.fn((next: NativeStackNavigationOptions) => {
			options = { ...options, ...next };
		}),
	};
	guard.navigation = navigation;
	// Where the page asked its scroller to go.
	const scrolls: unknown[] = [];
	const tree = render(
		<HubSheetProvider value={context}>
			<HostEditPage
				navigation={navigation as unknown as NativeStackScreenProps<HubRoutes, "HostEdit">["navigation"]}
				route={{ key: "HostEdit", name: "HostEdit", params: { hubId: "hub-1", name } }}
			/>
		</HubSheetProvider>,
		{
			createNodeMock: (element) =>
				element.type === "ScrollView" ? { scrollTo: (to: unknown) => scrolls.push(to) } : null,
		},
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
	return {
		tree,
		navigation,
		focus,
		hosts,
		field,
		type,
		header,
		save,
		options: () => options,
		scrolls,
	};
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
});

it("says once that only the SSH address is required, and what each empty field means (audit M4)", async () => {
	const page = await mount(scriptedFleet([attic]), "attic");
	const text = renderedText(page.tree);
	expect(text).toContain("SSH destination, e.g. host.example or user@host.example.");
	expect(text).toContain("Only the SSH address is required.");
	expect(text).not.toMatch(/\bOptional\b/);
	expect(page.field("User").props.placeholder).toBe("From the address or SSH config");
	expect(page.field("Key path").props.placeholder).toBe("From your SSH config");
	expect(page.field("Evener path").props.placeholder).toBe("evener on PATH");
	expect(page.field("Hub config path").props.placeholder).toBe("The default hub.toml");
	expect(page.field("Hub address").props.placeholder).toBe("The default address");
	expect(page.field("Roots").props.placeholder).toBe("No project roots. One per line.");
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
});

it("puts any other refusal above the fields", async () => {
	const fleet = scriptedFleet([attic]);
	fleet.refuse.update = new WireError("the registry is read-only right now", -32000);
	const page = await mount(fleet, "attic");
	await page.save();
	const text = renderedText(page.tree);
	expect(text.indexOf("the registry is read-only right now")).toBeLessThan(text.indexOf("SSH address"));
});

it("scrolls a refusal that names a field into view, however far down the page was", async () => {
	const fleet = scriptedFleet([attic]);
	fleet.refuse.update = new WireError('host "attic": missing ssh destination', -32602, {
		evenerErrorInfo: "invalidHostField",
		field: "address",
	});
	const page = await mount(fleet, "attic");
	// The page lays its SSH address field out 60 points down, and the person
	// has scrolled to Roots at the bottom before pressing Save.
	act(() =>
		page.tree.root
			.findByProps({ testID: "host-field-address" })
			.props.onLayout({ nativeEvent: { layout: { x: 0, y: 60, width: 393, height: 120 } } }),
	);
	await page.save();
	expect(page.scrolls).toEqual([{ y: 60, animated: true }]);
});

it("scrolls to the top for a refusal above the fields", async () => {
	const fleet = scriptedFleet([attic]);
	fleet.refuse.update = new WireError("the registry is read-only right now", -32000);
	const page = await mount(fleet, "attic");
	await page.save();
	expect(page.scrolls).toEqual([{ y: 0, animated: true }]);
});

it("stays where it is when a save lands", async () => {
	const page = await mount(scriptedFleet([attic]), "attic");
	await page.save();
	expect(page.scrolls).toEqual([]);
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
});

it("goes back without saving on Cancel", async () => {
	const fleet = scriptedFleet([attic]);
	const page = await mount(fleet, "attic");
	await act(async () => page.header("headerLeft").props.onPress());
	expect(page.navigation.goBack).toHaveBeenCalledTimes(1);
	expect(fleet.calls.some((call) => call.method === "evener/host/update")).toBe(false);
});

it("holds Save while the phone's connection is down", async () => {
	const page = await mount(scriptedFleet([attic]), "attic", { ready: false });
	expect(page.header("headerRight").props.disabled).toBe(true);
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
});

it("goes back when its host leaves the hub's list while it is open", async () => {
	const fleet = scriptedFleet([attic]);
	const page = await mount(fleet, "attic");
	fleet.hosts = [];
	await act(async () => page.hosts.read());
	expect(page.navigation.goBack).toHaveBeenCalledTimes(1);
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
});

it("leaves the stack alone when a save lands after the page was swiped away", async () => {
	const fleet = scriptedFleet([attic]);
	const answer = fleet.client.request;
	let release = () => {};
	fleet.client.request = (async (method: string, params: unknown) => {
		if (method === "evener/host/update") await new Promise<void>((resolve) => (release = resolve));
		return answer(method as never, params as never);
	}) as never;
	const page = await mount(fleet, "attic");
	await act(async () => page.header("headerRight").props.onPress());
	// The back gesture took the page off screen while the hub held the edit.
	page.focus.focused = false;
	await act(async () => release());
	await settle();
	expect(page.navigation.goBack).not.toHaveBeenCalled();
});

it("refuses an edit made while someone else changed the host, and never saves over theirs", async () => {
	const fleet = scriptedFleet([attic]);
	const page = await mount(fleet, "attic");
	// Another client edits attic, and the page's poll brings it in while this
	// form is still open on generation 1.
	fleet.hosts = [{ ...attic, address: "attic.other", generation: 2 }];
	await act(async () => page.hosts.read());
	page.type("SSH address", "attic.local");
	await page.save();
	const updates = fleet.calls.filter((call) => call.method === "evener/host/update");
	expect(updates.map((call) => (call.params as { expectedGeneration: number }).expectedGeneration)).toEqual([1]);
	expect(fleet.hosts[0]?.address).toBe("attic.other");
	const text = renderedText(page.tree);
	expect(text).toContain("This host changed since you opened it. Cancel, then open it again to see the change.");
	expect(text.indexOf("This host changed")).toBeLessThan(text.indexOf("SSH address"));
	expect(page.navigation.goBack).not.toHaveBeenCalled();
});

const leave = { type: "GO_BACK" };

it("leaves at once when nothing was changed", async () => {
	const page = await mount(scriptedFleet([attic]), "attic");
	expect(guard.prevented).toBe(false);
	await act(async () => page.header("headerLeft").props.onPress());
	expect(page.navigation.goBack).toHaveBeenCalledTimes(1);
});

it("asks before a Cancel, a swipe or Back throws away an edit (spec 6)", async () => {
	alertRequests.length = 0;
	const page = await mount(scriptedFleet([attic]), "attic");
	page.type("User", "root");
	expect(guard.prevented).toBe(true);
	// The stack routes Cancel's goBack, a swipe and Back through the guard.
	act(() => guard.onPrevent?.({ data: { action: leave } }));
	expect(alertRequests.at(-1)?.title).toBe("Discard your changes?");
	expect(alertRequests.at(-1)?.buttons?.map((button) => button.text)).toEqual(["Keep editing", "Discard"]);
	expect(page.navigation.dispatch).not.toHaveBeenCalled();
	act(() =>
		alertRequests
			.at(-1)
			?.buttons?.find((button) => button.text === "Discard")
			?.onPress?.(),
	);
	expect(page.navigation.dispatch).toHaveBeenCalledWith(leave);
});

it("stops asking once an edit is typed back to what the host had", async () => {
	const page = await mount(scriptedFleet([attic]), "attic");
	page.type("User", "root");
	page.type("User", "jesse");
	expect(guard.prevented).toBe(false);
});

it("leaves without asking once its save lands", async () => {
	alertRequests.length = 0;
	const page = await mount(scriptedFleet([attic]), "attic");
	page.type("User", "root");
	await page.save();
	expect(page.navigation.goBack).toHaveBeenCalledTimes(1);
	// The save's own leaving passes the guard untouched.
	if (guard.prevented) act(() => guard.onPrevent?.({ data: { action: leave } }));
	expect(alertRequests).toHaveLength(0);
});

it("holds Back and a swipe while its save is in flight, without asking", async () => {
	alertRequests.length = 0;
	const fleet = scriptedFleet([attic]);
	const page = await mount(fleet, "attic");
	page.type("User", "root");
	fleet.client.request = (() => new Promise(() => {})) as never;
	await act(async () => page.header("headerRight").props.onPress());
	expect(guard.prevented).toBe(true);
	act(() => guard.onPrevent?.({ data: { action: leave } }));
	expect(alertRequests).toHaveLength(0);
	expect(page.navigation.dispatch).not.toHaveBeenCalled();
});

it("counts roots that save the same as no change", async () => {
	const page = await mount(scriptedFleet([attic]), "attic");
	// A blank line and spaces drop out of the saved roots.
	page.type("Roots", "/srv/b\n\n  /srv/a  \n");
	expect(guard.prevented).toBe(false);
});
