// The New session sheet's own wiring: the creation store bound to the hub
// only while the connection is ready, and the form placed once as it opens.
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { act } from "react-test-renderer";
import { beforeEach, expect, it, vi } from "vitest";
import { type CreationDraft, CreationDraftRepository } from "../creationDraftRepository";
import { render, settle } from "../renderNative.testkit";
import { openSqliteSyncDouble } from "../sqliteSync.testkit";
import type { Routes } from "../screens";
import { LaunchMemory } from "./launchMemory";
import type { SessionSeed } from "./launchSetup";
import type { NewSessionContextValue } from "./newSessionContext";
import { forgetCreationForHub } from "./creations";
import { NewSessionSheet } from "./NewSessionSheet";
import { memoryStorage } from "./newSessionTestUtils";

const harness = vi.hoisted(() => ({
	connection: { state: "ready", ready: true },
	requests: [] as { method: string; params: unknown }[],
	// The real creation draft repository over an in-memory SQLite double.
	drafts: null as unknown as CreationDraftRepository,
	memory: null as unknown,
	context: null as unknown,
	// The phone's saved hubs.
	profiles: [{ id: "hub-1", name: "magic-kingdom" }] as { id: string; name: string }[],
	// A thread/start the test answers itself, as a slow hub would.
	heldStart: null as null | ((response: unknown) => void),
	// Who is listening to the hub's notifications.
	listeners: [] as ((notification: { method: string; params?: unknown }) => void)[],
}));
const client = {
	request: async (method: string, params: unknown) => {
		harness.requests.push({ method, params });
		if (method === "thread/start" && harness.heldStart === null)
			return new Promise((resolve) => (harness.heldStart = resolve));
		return { data: [] };
	},
	onNotification: (listener: (notification: { method: string; params?: unknown }) => void) => {
		harness.listeners.push(listener);
		return () => {
			harness.listeners = harness.listeners.filter((l) => l !== listener);
		};
	},
};
vi.mock("../ConnectionProvider", () => ({ useConnection: () => ({ profiles: harness.profiles }) }));
vi.mock("../retainedScreen", () => ({
	useRetainedScreenConnection: () => ({
		activeProfile: { id: "hub-1", name: "magic-kingdom" },
		client: harness.connection.ready ? client : null,
		state: harness.connection.state,
		renderClient: client,
	}),
}));
vi.mock("../nativeDrafts", () => ({ nativeDrafts: () => ({ creation: harness.drafts }) }));
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

async function mount(like?: SessionSeed) {
	const route = { key: "NewSession", name: "NewSession", params: { hubId: "hub-1", hubName: "magic-kingdom", like } };
	const goBack = vi.fn();
	const props = {
		navigation: { goBack } as unknown as NativeStackScreenProps<Routes, "NewSession">["navigation"],
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
	return { tree, context, rerender, goBack };
}

beforeEach(() => {
	forgetCreationForHub("hub-1");
	harness.connection = { state: "ready", ready: true };
	harness.requests.length = 0;
	harness.drafts = new CreationDraftRepository(openSqliteSyncDouble().port);
	harness.memory = new LaunchMemory(memoryStorage(), "hub-1");
	harness.context = null;
	harness.heldStart = null;
	harness.profiles = [{ id: "hub-1", name: "magic-kingdom" }];
	harness.listeners = [];
});

it("names the hub's own machine after the hub (ruling 3)", async () => {
	const sheet = await mount();
	expect(sheet.context().hostLabel("local")).toBe("magic-kingdom");
	expect(sheet.context().hostLabel("paradise-park")).toBe("paradise-park");
});

it("opens on the newest remembered start and reads that host's models", async () => {
	(harness.memory as LaunchMemory).recordStart({
		host: "paradise-park",
		cwd: "/Users/jesse/git/evener",
		model: null,
		effort: "",
		overrides: {},
	});
	const sheet = await mount();
	expect(sheet.context().store.getState()).toMatchObject({ source: "paradise-park", cwd: "/Users/jesse/git/evener" });
	expect(harness.requests).toContainEqual({
		method: "evener/host/request",
		params: { host: "paradise-park", method: "model/list", params: { cwd: "/Users/jesse/git/evener" } },
	});
});

// The hub announces a refreshed model list on evener/auth/updated (#3539):
// the form reads its list again, in place.
it("reads the form's model list again when the hub announces a refreshed one", async () => {
	await mount();
	const reads = () => harness.requests.filter((request) => request.method === "model/list").length;
	const before = reads();
	expect(before).toBeGreaterThan(0);
	await act(async () => {
		for (const listener of harness.listeners) listener({ method: "evener/auth/updated", params: {} });
	});
	await settle();
	expect(reads()).toBe(before + 1);
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
	harness.drafts.write("hub-1", draft);
	const sheet = await mount({ host: "paradise-park", cwd: "/Users/jesse/git/evener" });
	expect(sheet.context().store.getState()).toMatchObject({ source: "paradise-park", cwd: "/Users/jesse/git/evener" });
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
});

it("finishes a start the sheet was swiped away from, so the session it made is known (#3048)", async () => {
	const draft: CreationDraft = {
		source: "local",
		cwd: "/home/jesse/git/evener",
		prompt: "go",
		harness: "",
		model: null,
		reasoning: "",
		launchOverrides: {},
		images: [],
		unconfirmed: false,
	};
	harness.drafts.write("hub-1", draft);
	const sheet = await mount();
	const store = sheet.context().store;
	const outcome = store.getState().submit();
	await settle();
	expect(harness.heldStart).not.toBeNull();
	// The person swipes the sheet down while the hub is still starting it.
	act(() => sheet.tree.unmount());
	harness.heldStart?.({
		thread: { id: "t-1", name: "Demonstration 1", evener: { ref: "demo:created-1" } },
		turn: { id: "turn-1", status: "inProgress", items: [] },
	});
	expect(await outcome).toMatchObject({ status: "created", thread: { evener: { ref: "demo:created-1" } } });
	// The session exists, so its draft goes rather than waiting to start it twice.
	expect(harness.drafts.read("hub-1")).toBeNull();
});

const evenerDraft: CreationDraft = {
	source: "local",
	cwd: "/home/jesse/git/evener",
	prompt: "go",
	harness: "",
	model: null,
	reasoning: "",
	launchOverrides: {},
	images: [],
	unconfirmed: false,
};
const created = {
	thread: { id: "t-1", name: "Demonstration 1", evener: { ref: "demo:created-1" } },
	turn: { id: "turn-1", status: "inProgress", items: [] },
};

it("never lets a reopened sheet's edits be lost to the start it was swiped away from (#3104)", async () => {
	harness.drafts.write("hub-1", evenerDraft);
	const first = await mount();
	const outcome = first.context().store.getState().submit();
	await settle();
	expect(harness.heldStart).not.toBeNull();
	act(() => first.tree.unmount());
	const reopened = await mount();
	// The reopened sheet shows the start on its way, and takes no edits until it lands.
	await act(async () => reopened.context().store.getState().setPrompt("the next thing"));
	expect(reopened.context().store.getState()).toMatchObject({ submitting: true, prompt: "go" });
	harness.heldStart?.(created);
	expect(await outcome).toMatchObject({ status: "created" });
	await settle();
	// The landing clears only the draft it started; the form is ready for the next one.
	expect(harness.drafts.read("hub-1")).toBeNull();
	await act(async () => reopened.context().store.getState().setPrompt("the next thing"));
	expect(harness.drafts.read("hub-1")).toMatchObject({ prompt: "the next thing" });
});

it("shows a reopened sheet the start still on its way, and never starts it twice (#3104)", async () => {
	harness.drafts.write("hub-1", evenerDraft);
	const first = await mount();
	const outcome = first.context().store.getState().submit();
	await settle();
	act(() => first.tree.unmount());
	const reopened = await mount();
	expect(reopened.context().store.getState()).toMatchObject({ submitting: true, prompt: "go" });
	expect(await reopened.context().store.getState().submit()).toEqual({ status: "blocked" });
	expect(harness.requests.filter((request) => request.method === "thread/start")).toHaveLength(1);
	harness.heldStart?.(created);
	expect(await outcome).toMatchObject({ status: "created" });
	await settle();
	expect(reopened.context().store.getState()).toMatchObject({ submitting: false, prompt: "", cwd: "" });
});

it("closes at once, making no store, for a hub that has been removed (#3104)", async () => {
	harness.profiles = [];
	const sheet = await mount();
	expect(harness.context).toBeNull();
	expect(sheet.goBack).toHaveBeenCalledTimes(1);
});

it("closes when its hub is removed while it is open (#3104)", async () => {
	const sheet = await mount();
	expect(sheet.goBack).not.toHaveBeenCalled();
	harness.profiles = [];
	await sheet.rerender();
	expect(sheet.goBack).toHaveBeenCalledTimes(1);
});

it("makes no store for a hub that has been removed (#3104)", async () => {
	harness.profiles = [];
	// A store made for the hub would read its draft first.
	let reads = 0;
	harness.drafts = {
		read: () => {
			reads++;
			return null;
		},
		write: () => {},
		clear: () => {},
	} as unknown as CreationDraftRepository;
	await mount();
	expect(harness.context).toBeNull();
	expect(reads).toBe(0);
});
