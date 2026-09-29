import type { UpdateCheckResponse } from "@evener/appwire-client";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { act } from "react-test-renderer";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { alertRequests, render, renderedText } from "../renderNative.testkit";
import { HubHome } from "./HubHome";
import { type HubRoutes, type HubSheetContextValue, HubSheetProvider } from "./hubSheetContext";
import { createPhoneHubUpdates, createReadiness, type PhoneHubUpdates } from "./hubUpdates";

const status = { line: null as string | null };
vi.mock("../board/connectionStatus", async (importOriginal) => ({
	...(await importOriginal<typeof import("../board/connectionStatus")>()),
	useConnectionStatusText: () => status.line,
}));
vi.mock("../ConnectionProvider", () => ({
	useConnection: () => ({
		state: "ready",
		fatal: false,
		profiles: [
			{ id: "hub-1", name: "Work hub", origin: "https://work:9180" },
			{ id: "hub-2", name: "Home hub", origin: "https://home:9180" },
		],
	}),
}));
vi.mock("@react-navigation/native", () => ({
	StackActions: {
		replace: (name: string, params: unknown) => ({ type: "REPLACE", payload: { name, params } }),
	},
}));
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

let updates: PhoneHubUpdates;
let context: HubSheetContextValue;

async function mount(options: { check?: UpdateCheckResponse | Error; ready?: boolean } = {}) {
	const fake = hub(options.check ?? UP_TO_DATE);
	const readiness = createReadiness();
	readiness.set(true);
	const live = { usable: options.ready ?? true };
	updates = createPhoneHubUpdates(fake.client, readiness);
	context = {
		hubId: "hub-1",
		hubName: "Work hub",
		client: null,
		ready: options.ready ?? true,
		canUseConnection: () => live.usable,
		updates: updates.controller,
	};
	if (options.check) await updates.controller.runCheck();
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
	return { tree, root, sheet, find, press, calls: fake.calls, script: fake.script, readiness, live };
}

const ROWS = ["Providers", "Plugins", "Display", "Hubs, 2", "Keyboard shortcuts", "Launch defaults", "Hub settings"];

beforeEach(() => {
	status.line = null;
	alertRequests.length = 0;
});
afterEach(() => updates.dispose());

it("says the hub is connected and lists its pages", async () => {
	const { tree, find } = await mount();
	expect(renderedText(tree)).toContain("Connected");
	for (const label of ROWS) expect(find(label)).not.toBeNull();
});

it("leaves the sheet for today's screens until their pages land (rulings 10 and 12)", async () => {
	const { root, sheet, press } = await mount();
	const interim: [string, string][] = [
		["Providers", "Providers"],
		["Plugins", "Plugins"],
		["Display", "TranscriptPreferences"],
		["Keyboard shortcuts", "KeybindingPreferences"],
		["Launch defaults", "LaunchSettings"],
		["Hub settings", "HubSettings"],
	];
	for (const [label, screen] of interim) {
		press(label);
		expect(root.dispatch).toHaveBeenLastCalledWith({
			type: "REPLACE",
			payload: { name: screen, params: { hubId: "hub-1" } },
		});
	}
	expect(root.navigate).not.toHaveBeenCalled();
	expect(sheet.navigate).not.toHaveBeenCalled();
});

it("pushes Hubs inside the sheet, valued with the number of saved hubs", async () => {
	const { tree, root, sheet, press } = await mount();
	expect(renderedText(tree)).toContain("2");
	press("Hubs, 2");
	expect(sheet.navigate).toHaveBeenCalledWith("Hubs");
	expect(root.navigate).not.toHaveBeenCalled();
	expect(root.dispatch).not.toHaveBeenCalled();
});

it("keeps every row, pressable, while the connection is down, and never asks to reconnect (Review Focus 4)", async () => {
	status.line = "Reconnecting…";
	const { tree, root, find, press } = await mount();
	expect(renderedText(tree)).toContain("Reconnecting…");
	expect(renderedText(tree)).not.toMatch(/\bReconnect\b/);
	for (const label of ROWS) expect(find(label)?.props.disabled).toBe(false);
	press("Providers");
	expect(root.dispatch).toHaveBeenCalledTimes(1);
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
