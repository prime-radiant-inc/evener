// The New session sheet's own wiring: the creation store bound to the hub
// only while the connection is ready, and the form placed once as it opens.
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { act } from "react-test-renderer";
import { beforeEach, expect, it, vi } from "vitest";
import type { CreationDraft } from "../creationDraftRepository";
import { render } from "../renderNative.testkit";
import type { Routes } from "../screens";
import { LaunchMemory } from "./launchMemory";
import type { SessionSeed } from "./launchSetup";
import type { NewSessionContextValue } from "./newSessionContext";
import { NewSessionSheet } from "./NewSessionSheet";
import { memoryStorage } from "./newSessionTestUtils";

const harness = vi.hoisted(() => ({
	connection: { state: "ready", ready: true },
	requests: [] as { method: string; params: unknown }[],
	drafts: new Map<string, unknown>(),
	memory: null as unknown,
	context: null as unknown,
}));
const client = {
	request: async (method: string, params: unknown) => {
		harness.requests.push({ method, params });
		return { data: [] };
	},
	onNotification: () => () => {},
};
vi.mock("../retainedScreen", () => ({
	useRetainedScreenConnection: () => ({
		activeProfile: { id: "hub-1", name: "magic-kingdom" },
		client: harness.connection.ready ? client : null,
		state: harness.connection.state,
		renderClient: client,
	}),
}));
vi.mock("../nativeDrafts", () => ({
	nativeDrafts: () => ({
		creation: {
			read: (hubId: string) => harness.drafts.get(hubId) ?? null,
			write: (hubId: string, draft: unknown) => void harness.drafts.set(hubId, draft),
			clear: (hubId: string) => void harness.drafts.delete(hubId),
		},
	}),
}));
vi.mock("./nativeLaunchMemory", () => ({ launchMemory: () => harness.memory }));
vi.mock("expo-crypto", () => ({ randomUUID: () => "sheet-uuid" }));
vi.mock("../hosts/useHubFleet", () => ({ useHubFleet: () => ({ hosts: null, live: null }) }));
// The pages render inside the navigator, which isn't drawn here; it reads
// the context the sheet gives them.
vi.mock("@react-navigation/native-stack", async () => {
	const { useNewSession } = await import("./newSessionContext");
	return {
		createNativeStackNavigator: () => ({
			Navigator: () => {
				harness.context = useNewSession();
				return null;
			},
			Screen: () => null,
		}),
	};
});
vi.mock("./NewSessionForm", () => ({ NewSessionForm: () => null }));
vi.mock("./HostPicker", () => ({ HostPicker: () => null }));
vi.mock("./ProjectPicker", () => ({ ProjectPicker: () => null }));
vi.mock("./BrowseFolders", () => ({ BrowseFolders: () => null }));
vi.mock("./ModelPicker", () => ({ ModelPicker: () => null }));
vi.mock("./PluginChecklist", () => ({ PluginChecklist: () => null }));
vi.mock("./AccessPicker", () => ({ AccessPicker: () => null }));
vi.mock("./MoreOptions", () => ({ MoreOptions: () => null }));
vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));

const settle = () =>
	act(async () => {
		await new Promise((resolve) => setTimeout(resolve, 0));
	});

async function mount(like?: SessionSeed) {
	const route = { key: "NewSession", name: "NewSession", params: { hubId: "hub-1", hubName: "magic-kingdom", like } };
	const props = {
		navigation: {} as NativeStackScreenProps<Routes, "NewSession">["navigation"],
		route: route as NativeStackScreenProps<Routes, "NewSession">["route"],
	};
	const tree = render(<NewSessionSheet {...props} />);
	await settle();
	const context = () => harness.context as NewSessionContextValue;
	const rerender = async () => {
		await act(async () => {
			tree.update(<NewSessionSheet {...props} />);
		});
		await settle();
	};
	return { tree, context, rerender };
}

beforeEach(() => {
	harness.connection = { state: "ready", ready: true };
	harness.requests.length = 0;
	harness.drafts.clear();
	harness.memory = new LaunchMemory(memoryStorage(), "hub-1");
	harness.context = null;
});

it("names the hub's own machine after the hub (ruling 3)", async () => {
	const sheet = await mount();
	expect(sheet.context().hostLabel("local")).toBe("magic-kingdom");
	expect(sheet.context().hostLabel("paradise-park")).toBe("paradise-park");
	sheet.tree.unmount();
});

it("opens on the newest remembered start and reads that host's models", async () => {
	(harness.memory as LaunchMemory).recordStart(
		{ host: "paradise-park", cwd: "/Users/jesse/git/evener", model: null, effort: "", overrides: {} },
		1,
	);
	const sheet = await mount();
	expect(sheet.context().store.getState()).toMatchObject({ source: "paradise-park", cwd: "/Users/jesse/git/evener" });
	expect(harness.requests).toContainEqual({
		method: "evener/host/request",
		params: { host: "paradise-park", method: "model/list", params: { cwd: "/Users/jesse/git/evener" } },
	});
	sheet.tree.unmount();
});

it("opens like a session when New session like this opened it", async () => {
	const draft: CreationDraft = {
		source: "local",
		cwd: "/home/jesse/git/docs",
		prompt: "",
		harness: "",
		model: null,
		reasoning: "",
		launchOverrides: {},
		images: [],
		unconfirmed: false,
	};
	harness.drafts.set("hub-1", draft);
	const sheet = await mount({ host: "paradise-park", cwd: "/Users/jesse/git/evener" });
	expect(sheet.context().store.getState()).toMatchObject({ source: "paradise-park", cwd: "/Users/jesse/git/evener" });
	sheet.tree.unmount();
});

it("asks the hub nothing until the connection is ready, then loads projects and models", async () => {
	harness.connection = { state: "reconnecting", ready: false };
	const sheet = await mount();
	expect(sheet.context().ready).toBe(false);
	expect(harness.requests).toEqual([]);
	harness.connection = { state: "ready", ready: true };
	await sheet.rerender();
	expect(sheet.context().ready).toBe(true);
	expect(harness.requests.map((request) => request.method)).toEqual(
		expect.arrayContaining(["evener/projects/recent", "model/list"]),
	);
	sheet.tree.unmount();
});
