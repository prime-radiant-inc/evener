import type { AuthStatusResponse, InstanceEntry, UpdateCheckResponse } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { act } from "react-test-renderer";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { alertRequests, render, renderedText } from "../renderNative.testkit";
import { HostsController } from "../hosts/hostsController";
import { hostRow, type ScriptedFleet, scriptedFleet } from "../hosts/hostsTestUtils";
import { Tag } from "../sheet/Grouped";
import { HubHome } from "./HubHome";
import { type HubRoutes, type HubSheetContextValue, HubSheetProvider } from "./hubSheetContext";
import { createPhoneHubUpdates, createReadiness, type PhoneHubUpdates } from "./hubUpdates";

const status = { line: null as string | null };
vi.mock("../board/connectionStatus", async (importOriginal) => ({
	...(await importOriginal<typeof import("../board/connectionStatus")>()),
	useConnectionStatusText: () => status.line,
}));
// The connection the Providers row's credential store binds to; the hub's
// client when a test lists providers. Two saved hubs value the Hubs row
// (mount sets both).
const connection = vi.hoisted(() => ({
	value: { state: "ready", fatal: false, client: null, activeProfile: null } as Record<string, unknown>,
}));
vi.mock("../ConnectionProvider", () => ({ useConnection: () => connection.value }));
vi.mock("expo-crypto", () => ({ randomUUID: () => "fixture-uuid" }));
vi.mock("@react-navigation/native", async () => {
	const { useEffect } = await import("react");
	return {
		// Runs again only when its callback changes, as on a real focus.
		useFocusEffect: (effect: () => undefined | (() => void)) => useEffect(effect, [effect]),
		StackActions: {
			replace: (name: string, params: unknown) => ({ type: "REPLACE", payload: { name, params } }),
		},
	};
});
vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));
vi.mock("expo-application", () => ({ nativeApplicationVersion: "0.1.0", nativeBuildVersion: "5" }));

const UP_TO_DATE: UpdateCheckResponse = {
	channel: "release",
	buildChannel: "release",
	currentVersion: "0.9.412",
	currentCommit: "abc1234",
	updateAvailable: false,
	applicable: true,
};
const WAITING: UpdateCheckResponse = { ...UP_TO_DATE, updateAvailable: true, latestTag: "v0.9.413" };

/** A hub that answers update checks from `check` (or refuses them) and
 * restarts on apply. */
function hub(check: UpdateCheckResponse | Error) {
	const calls: string[] = [];
	const script = { failChecks: false };
	const client = {
		request: (async (method: string) => {
			calls.push(method);
			if (method === "evener/update/apply") return { restarting: true };
			if (check instanceof Error) throw check;
			if (script.failChecks) throw new Error("the hub is still starting");
			return check;
		}) as never,
	};
	return { client, calls, script };
}

/** A ready hub listing provider instances named `names`, with the sign-in
 * statuses `auth`. */
function providersHub(names: string[], auth: Pick<AuthStatusResponse, "provider" | "needsLogin">[]) {
	const fake = new FakeClient("ready");
	const instances = names.map(
		(name): InstanceEntry => ({
			name,
			providerId: name,
			protocol: "https",
			auth: "oauth",
			implicit: false,
			isDefault: false,
			activeSource: "oauth",
			hasStoredOAuth: true,
			credentialRequired: true,
		}),
	);
	fake.on("evener/instance/list", () => ({ instances, availableProviders: [] }));
	fake.on("evener/auth/list", () => ({ providers: auth as AuthStatusResponse[] }));
	return fake;
}

let updates: PhoneHubUpdates;
let context: HubSheetContextValue;

async function mount(
	options: {
		check?: UpdateCheckResponse | Error;
		ready?: boolean;
		/** Whether a control that needs the hub may act (a re-key window says no). */
		usable?: boolean;
		fleet?: ScriptedFleet;
		client?: HubSheetContextValue["client"];
		providers?: FakeClient;
	} = {},
) {
	const fake = hub(options.check ?? UP_TO_DATE);
	const readiness = createReadiness();
	readiness.set(true);
	const live = { usable: options.usable ?? options.ready ?? true };
	updates = createPhoneHubUpdates(fake.client, readiness);
	context = {
		hubId: "hub-1",
		hubName: "Work hub",
		client: options.client ?? ((options.providers ?? null) as HubSheetContextValue["client"]),
		ready: options.ready ?? true,
		canUseConnection: () => live.usable,
		updates: updates.controller,
		hosts: options.fleet ? new HostsController(options.fleet.client) : null,
		live: null,
	};
	if (options.check) await updates.controller.runCheck();
	connection.value = {
		state: "ready",
		fatal: false,
		client: options.providers ?? null,
		activeProfile: { id: "hub-1", name: "Work hub" },
		profiles: [
			{ id: "hub-1", name: "Work hub", origin: "https://work:9180" },
			{ id: "hub-2", name: "Home hub", origin: "https://home:9180" },
		],
	};
	const root = { dispatch: vi.fn(), navigate: vi.fn(), goBack: vi.fn() };
	const sheet = { getParent: () => root, navigate: vi.fn() };
	const navigation = sheet as unknown as NativeStackScreenProps<HubRoutes, "HubHome">["navigation"];
	const route = { key: "HubHome", name: "HubHome", params: { hubId: "hub-1" } } as const;
	const tree = render(
		<HubSheetProvider value={context}>
			<HubHome navigation={navigation} route={route} />
		</HubSheetProvider>,
	);
	const find = (label: string) =>
		tree.root.findAllByProps({ accessibilityRole: "button", accessibilityLabel: label })[0] ?? null;
	const press = (label: string) =>
		act(() => {
			find(label)?.props.onPress();
		});
	await act(async () => {
		await new Promise((resolve) => setTimeout(resolve, 0));
	});
	return { tree, root, sheet, find, press, calls: fake.calls, script: fake.script, readiness, live };
}

const ROWS = [
	"Hosts",
	"Providers",
	"Plugins",
	"Display, System",
	"In-app alerts",
	"Hubs, 2",
	"Keyboard shortcuts",
	"Launch defaults",
	"Hub settings",
];

beforeEach(() => {
	status.line = null;
	alertRequests.length = 0;
});
afterEach(() => {
	updates.dispose();
	context.hosts?.dispose();
});

it("opens Hosts inside the sheet, counting the hub's own machine and tagging the offline ones (spec 12)", async () => {
	const fleet = scriptedFleet([
		hostRow("paradise-park"),
		hostRow("attic", { attached: false }),
		hostRow("studio", { attached: false, midAttach: true }),
	]);
	const { find, press, sheet } = await mount({ check: UP_TO_DATE, fleet });
	expect(find("Hosts, 4, 2 offline")).not.toBeNull();
	press("Hosts, 4, 2 offline");
	expect(sheet.navigate).toHaveBeenCalledWith("Hosts", { hubId: "hub-1" });
	expect(fleet.calls.filter((call) => call.method === "evener/host/list")).toHaveLength(1);
});

it("opens Providers inside the sheet, counting them and tagging the ones to sign in (spec 12)", async () => {
	const providers = providersHub(
		["codex-jesse-fsck.com", "lunaroute", "meta"],
		[
			{ provider: "codex-jesse-fsck.com", needsLogin: true },
			{ provider: "meta", needsLogin: false },
		],
	);
	const { find, press, sheet } = await mount({ check: UP_TO_DATE, providers });
	expect(find("Providers, 3, 1 to sign in")).not.toBeNull();
	press("Providers, 3, 1 to sign in");
	expect(sheet.navigate).toHaveBeenCalledWith("Providers", { hubId: "hub-1" });
	const tags = find("Providers, 3, 1 to sign in")?.findAllByType(Tag);
	expect(tags?.map((tag) => tag.props)).toEqual([{ text: "1 to sign in", tone: "amber" }]);
});

it("reads no providers while the connection can't be used, even when it says ready", async () => {
	// A re-key window: the client may still be the previous hub's.
	const providers = providersHub(["codex-jesse-fsck.com"], [{ provider: "codex-jesse-fsck.com", needsLogin: true }]);
	await mount({ providers, usable: false });
	expect(providers.calls.map((call) => call.method)).not.toContain("evener/auth/list");
});

it("counts providers with no tag when none needs signing in", async () => {
	const providers = providersHub(["lunaroute", "meta"], []);
	const { find } = await mount({ providers });
	expect(find("Providers, 2")).not.toBeNull();
});

it("reads a count of none to VoiceOver as it shows it", async () => {
	const providers = providersHub([], []);
	const { find } = await mount({ providers });
	expect(find("Providers, 0")).not.toBeNull();
});

it("tags hosts on another version when none is offline", async () => {
	const fleet = scriptedFleet([
		hostRow("paradise-park", { hubVersion: "0.9.409" }),
		hostRow("attic", { hubVersion: "0.9.412" }),
	]);
	const { find } = await mount({ check: UP_TO_DATE, fleet });
	expect(find("Hosts, 3, 1 on another version")).not.toBeNull();
});

it("shows Hosts without a count or tag before the hub has listed its hosts", async () => {
	const { find } = await mount();
	expect(find("Hosts")).not.toBeNull();
});

it("says the hub is connected and lists its pages", async () => {
	const { tree, find } = await mount();
	expect(renderedText(tree)).toContain("Connected");
	for (const label of ROWS) expect(find(label)).not.toBeNull();
});

it("opens today's screens inside the sheet, so Back returns to the Hub (audit M10)", async () => {
	const { root, sheet, press } = await mount();
	const interim: [string, string][] = [
		["Keyboard shortcuts", "KeybindingPreferences"],
		["Launch defaults", "LaunchSettings"],
		["Hub settings", "HubSettings"],
	];
	for (const [label, screen] of interim) {
		press(label);
		expect(sheet.navigate).toHaveBeenLastCalledWith(screen, { hubId: "hub-1" });
	}
	expect(root.dispatch).not.toHaveBeenCalled();
	expect(root.navigate).not.toHaveBeenCalled();
});

/** A hub with `count` plugins installed, which can say its plugins changed. */
function pluginHub(count: number) {
	const hub = { count, notify: (_notification: { method: string }) => {} };
	const client = {
		request: async (method: string) => {
			if (method !== "evener/plugin/list") throw new Error(`unexpected ${method}`);
			return { plugins: Array.from({ length: hub.count }, (_, index) => ({ plugin: `p${index}` })) };
		},
		onNotification: (listener: (notification: { method: string }) => void) => {
			hub.notify = listener;
			return () => {};
		},
	} as unknown as HubSheetContextValue["client"];
	return { hub, client };
}

it("pushes Plugins inside the sheet, valued with the number installed", async () => {
	const { hub, client } = pluginHub(3);
	const { root, sheet, find, press } = await mount({ client });
	// No update tag: the hub doesn't say which plugins have one (ruling 7).
	expect(find("Plugins, 3")).not.toBeNull();
	press("Plugins, 3");
	expect(sheet.navigate).toHaveBeenCalledWith("Plugins", { hubId: "hub-1" });
	expect(root.dispatch).not.toHaveBeenCalled();

	// The count follows the hub's own word that its plugins changed.
	hub.count = 4;
	await act(async () => {
		hub.notify({ method: "evener/plugin/updated" });
		await new Promise((resolve) => setTimeout(resolve, 0));
	});
	expect(find("Plugins, 4")).not.toBeNull();
});

it("reads no plugins while the connection can't be used, even when it says ready", async () => {
	// A re-key window: the client may still be the previous hub's.
	const { client } = pluginHub(3);
	const { find } = await mount({ client, usable: false });
	expect(find("Plugins, 3")).toBeNull();
	expect(find("Plugins")).not.toBeNull();
});

it("pushes Hubs inside the sheet, valued with the number of saved hubs", async () => {
	const { tree, root, sheet, press } = await mount();
	expect(renderedText(tree)).toContain("2");
	press("Hubs, 2");
	expect(sheet.navigate).toHaveBeenCalledWith("Hubs");
	expect(root.navigate).not.toHaveBeenCalled();
	expect(root.dispatch).not.toHaveBeenCalled();
});

it("opens In-app alerts inside the sheet, between Display and Hubs", async () => {
	const { tree, sheet, press } = await mount();
	const labels = tree.root
		.findAllByProps({ accessibilityRole: "button" })
		.map((node) => node.props.accessibilityLabel)
		.filter((label): label is string => ROWS.includes(label));
	const display = labels.indexOf("Display, System");
	expect(labels.slice(display, display + 3)).toEqual(["Display, System", "In-app alerts", "Hubs, 2"]);
	press("In-app alerts");
	expect(sheet.navigate).toHaveBeenLastCalledWith("Alerts", { hubId: "hub-1" });
});

it("keeps every row, pressable, while the connection is down, and never asks to reconnect (Review Focus 4)", async () => {
	status.line = "Reconnecting…";
	const { tree, sheet, find, press } = await mount();
	expect(renderedText(tree)).toContain("Reconnecting…");
	expect(renderedText(tree)).not.toMatch(/\bReconnect\b/);
	for (const label of ROWS) expect(find(label)?.props.disabled).toBe(false);
	// A row opening today's screen still works while the connection is down.
	press("Hub settings");
	expect(sheet.navigate).toHaveBeenLastCalledWith("HubSettings", { hubId: "hub-1" });
});

it("says Connecting… rather than Connected while the hub isn't ready and the line is still quiet", async () => {
	const { tree } = await mount({ ready: false });
	expect(renderedText(tree)).toContain("Connecting…");
	expect(renderedText(tree)).not.toContain("Connected");
});

it("names this app's version in About", async () => {
	const { tree } = await mount();
	const about = tree.root.findByProps({ accessibilityLabel: "Evener for iPhone, 0.1.0 (5)" });
	expect(about).toBeTruthy();
});

it("sets its status line as the prototype does: 14pt, 20pt in from the edge (audit L1)", async () => {
	const { tree } = await mount({ check: UP_TO_DATE });
	const line = tree.root.find(
		(node) => String(node.type) === "Text" && node.props.children === "Connected · evener 0.9.412 · up to date",
	);
	expect(line.props.style).toMatchObject({ paddingHorizontal: 20, paddingTop: 2, paddingBottom: 6 });
	// At the prototype's 14: spec 16.2 names no role for this line.
	expect(line.props.style).toMatchObject({ fontSize: 14 });
});

it("says the hub is up to date, and offers no update", async () => {
	const { tree, find } = await mount({ check: UP_TO_DATE });
	expect(renderedText(tree)).toContain("Connected · evener 0.9.412 · up to date");
	expect(find("Update hub")).toBeNull();
});

it("offers a waiting update, confirms it, installs it, and says the hub is restarting", async () => {
	const { tree, find, press, calls } = await mount({ check: WAITING });
	expect(renderedText(tree)).toContain("Connected · evener 0.9.412 · Update available");
	press("Update hub");
	expect(alertRequests).toHaveLength(1);
	expect(alertRequests[0]?.title).toBe("Update Work hub?");
	expect(alertRequests[0]?.message).toBe(
		"Install evener v0.9.413 on Work hub. The hub restarts, and the app reconnects on its own.",
	);
	await act(async () => {
		alertRequests[0]?.buttons?.find((button) => button.text === "Update")?.onPress?.();
	});
	expect(calls).toContain("evener/update/apply");
	expect(find("Update hub")).toBeNull();
	expect(renderedText(tree)).toContain("Restarting into v0.9.413…");
});

it("holds the update while the hub isn't ready", async () => {
	const { find } = await mount({ check: WAITING, ready: false });
	expect(find("Update hub")?.props.disabled).toBe(true);
});

it("shows a failed check as a line, with nothing to press", async () => {
	const { tree } = await mount({ check: new Error("socket hung up") });
	const texts = tree.root.findAllByType("Text" as never).map((node) => node.props.children);
	expect(renderedText(tree)).toMatch(/\S/);
	expect(texts).not.toContain("Retry");
	expect(renderedText(tree)).not.toMatch(/\bReconnect\b/);
	expect(updates.controller.getState().checkError).not.toBeNull();
	expect(renderedText(tree)).toContain(updates.controller.getState().checkError ?? "");
});

it("sends no update confirmed after the connection went away", async () => {
	const { press, calls, live } = await mount({ check: WAITING });
	press("Update hub");
	live.usable = false;
	await act(async () => {
		alertRequests[0]?.buttons?.find((button) => button.text === "Update")?.onPress?.();
	});
	expect(calls).not.toContain("evener/update/apply");
});

it("says so when the hub restarts without the update", async () => {
	const { tree, press, readiness } = await mount({ check: WAITING });
	press("Update hub");
	await act(async () => {
		alertRequests[0]?.buttons?.find((button) => button.text === "Update")?.onPress?.();
	});
	// The hub comes back on the version it had.
	await act(async () => {
		readiness.set(false);
		readiness.set(true);
		await new Promise((resolve) => setTimeout(resolve, 0));
	});
	expect(renderedText(tree)).toContain("The hub restarted without the update. Check its logs.");
});

it("opens Display inside the sheet, valued with this phone's appearance", async () => {
	const { sheet, press, find } = await mount();
	expect(find("Display, System")).not.toBeNull();
	press("Display, System");
	expect(sheet.navigate).toHaveBeenCalledWith("Display", { hubId: "hub-1" });
});

it("says the update couldn't be confirmed when the hub's first answer after the restart fails", async () => {
	const { tree, press, readiness, script } = await mount({ check: WAITING });
	press("Update hub");
	await act(async () => {
		alertRequests[0]?.buttons?.find((button) => button.text === "Update")?.onPress?.();
	});
	script.failChecks = true;
	await act(async () => {
		readiness.set(false);
		readiness.set(true);
		await new Promise((resolve) => setTimeout(resolve, 0));
	});
	expect(renderedText(tree)).toContain("Couldn't confirm the update. Check the hub's version.");
	expect(renderedText(tree)).not.toContain("The hub restarted without the update.");
});
