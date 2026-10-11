import { WireError } from "@evener/appwire-client";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { Platform } from "react-native";
import { act } from "react-test-renderer";
import { beforeEach, expect, it, vi } from "vitest";
import { createNewSessionStore } from "../newSession";
import { pressable, promptRequests, render, renderedText, settle } from "../renderNative.testkit";
import { BrowseFolders } from "./BrowseFolders";
import { NewSessionProvider, type NewSessionRoutes } from "./newSessionContext";
import { sheetContext } from "./newSessionTestUtils";

vi.mock("../board/connectionStatus", async (importOriginal) => ({
	...(await importOriginal<typeof import("../board/connectionStatus")>()),
	useConnectionStatusText: () => null,
}));
vi.mock("../ConnectionProvider", () => ({ useConnection: () => ({ state: "ready", fatal: false }) }));
vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));

/** paradise-park's folders, answered only through evener/host/request for
 * that host: the hub's own machine has none of them. */
function paradisePark() {
	const folders = new Map<string, string[]>([
		["", ["/Users/jesse/git", "/Users/jesse/Documents"]],
		["/Users/jesse/", ["/Users/jesse/git", "/Users/jesse/Documents"]],
		["/Users/jesse/git/", ["/Users/jesse/git/evener", "/Users/jesse/git/docs"]],
		["/Users/jesse/git/evener/", []],
	]);
	const calls: { method: string; params: unknown }[] = [];
	const refuse: { create?: Error; list?: boolean } = {};
	const client = {
		request: async (method: string, params: { host: string; method: string; params: Record<string, string> }) => {
			calls.push({ method, params });
			if (method !== "evener/host/request" || params.host !== "paradise-park")
				throw new Error(`the hub's own machine was asked ${method}`);
			if (params.method === "evener/paths/complete") {
				if (refuse.list) throw new WireError("permission denied", -32000);
				return { data: folders.get(params.params.prefix) ?? [] };
			}
			if (params.method === "evener/dirs/create") {
				if (refuse.create) throw refuse.create;
				const path = params.params.path;
				folders.set(`${path}/`, []);
				return { path, created: true };
			}
			throw new Error(`no answer for ${params.method}`);
		},
		onNotification: () => () => {},
	};
	return { client, calls, refuse };
}

function mount(dir: string, setUp: (hub: ReturnType<typeof paradisePark>) => void = () => {}) {
	const hub = paradisePark();
	setUp(hub);
	const store = createNewSessionStore("hub-1");
	store.setState({ source: "paradise-park", cwd: "/Users/jesse/git/evener" });
	const navigation = { popTo: vi.fn() };
	const tree = render(
		<NewSessionProvider value={sheetContext(store, { client: hub.client as never })}>
			<BrowseFolders
				navigation={navigation as unknown as NativeStackScreenProps<NewSessionRoutes, "Browse">["navigation"]}
				route={{ key: "Browse", name: "Browse", params: { dir } }}
			/>
		</NewSessionProvider>,
	);
	const press = async (label: string) => {
		const target = pressable(tree, label);
		if (!target) throw new Error(`no ${label} here`);
		await act(async () => target.props.onPress());
		await settle();
	};
	return { tree, hub, store, navigation, press, text: () => renderedText(tree) };
}

beforeEach(() => {
	promptRequests.length = 0;
});

it("lists a folder on the chosen host, enters a folder and goes up in place", async () => {
	const page = mount("/Users/jesse/git");
	await settle();
	expect(page.text()).toContain("/Users/jesse/git");
	expect(pressable(page.tree, "evener")).toBeDefined();
	expect(pressable(page.tree, "docs")).toBeDefined();
	await page.press("evener");
	expect(page.text()).toContain("/Users/jesse/git/evener");
	expect(page.text()).toContain("Nothing here.");
	await page.press("Up one folder");
	await page.press("Up one folder");
	expect(page.text()).toContain("/Users/jesse");
	expect(pressable(page.tree, "Documents")).toBeDefined();
});

it("uses the folder it shows, and goes back to the form", async () => {
	const page = mount("/Users/jesse/git");
	await settle();
	await page.press("docs");
	await page.press("Use this folder");
	expect(page.store.getState().cwd).toBe("/Users/jesse/git/docs");
	expect(page.navigation.popTo).toHaveBeenCalledWith("Form");
});

it("offers no Use this folder at home, which the phone can't name", async () => {
	const page = mount("");
	await settle();
	expect(page.text()).toContain("Home");
	expect(pressable(page.tree, "Use this folder")).toBeUndefined();
	expect(pressable(page.tree, "Up one folder")).toBeUndefined();
});

it("makes a new folder on the host and moves into it", async () => {
	const page = mount("/Users/jesse/git");
	await settle();
	await page.press("New folder");
	expect(promptRequests.at(-1)).toMatchObject({ title: "New folder", message: "In /Users/jesse/git" });
	await act(async () => promptRequests.at(-1)?.callback?.("scratch"));
	await settle();
	expect(page.hub.calls.find((call) => (call.params as { method: string }).method === "evener/dirs/create")).toEqual({
		method: "evener/host/request",
		params: { host: "paradise-park", method: "evener/dirs/create", params: { path: "/Users/jesse/git/scratch" } },
	});
	expect(page.text()).toContain("/Users/jesse/git/scratch");
});

it("makes a new folder at home under ~/, the one relative form the hub expands", async () => {
	const page = mount("");
	await settle();
	await page.press("New folder");
	expect(promptRequests.at(-1)?.message).toBe("In your home folder");
	await act(async () => promptRequests.at(-1)?.callback?.("scratch"));
	await settle();
	const create = page.hub.calls.find((call) => (call.params as { method: string }).method === "evener/dirs/create");
	expect((create?.params as { params: { path: string } }).params.path).toBe("~/scratch");
});

it("shows the hub's refusal to make a folder, and stays where it was", async () => {
	const page = mount("/Users/jesse/git");
	await settle();
	page.hub.refuse.create = new WireError("permission denied", -32000);
	await page.press("New folder");
	await act(async () => promptRequests.at(-1)?.callback?.("scratch"));
	await settle();
	expect(page.text()).toContain("permission denied");
	expect(pressable(page.tree, "evener")).toBeDefined();
});

it("says a folder it couldn't open on the host, and offers only what the page can do", async () => {
	const page = mount("/Users/jesse/git", (hub) => {
		hub.refuse.list = true;
	});
	await settle();
	expect(page.text()).toContain("Couldn't open this folder on paradise-park. Go up a folder or choose another.");
	expect(page.text()).not.toMatch(/Try again|enter the path/);
});

it("offers New folder only on iOS, where Alert.prompt exists", async () => {
	const os = Platform.OS;
	(Platform as { OS: string }).OS = "android";
	try {
		const page = mount("/Users/jesse/git");
		await settle();
		expect(pressable(page.tree, "New folder")).toBeUndefined();
		expect(pressable(page.tree, "Use this folder")).toBeDefined();
	} finally {
		(Platform as { OS: string }).OS = os;
	}
});
