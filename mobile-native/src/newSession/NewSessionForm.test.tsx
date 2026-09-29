import { type HostRow, type ModelDescriptor, WireError } from "@evener/appwire-client";
import type { NativeStackNavigationOptions, NativeStackScreenProps } from "@react-navigation/native-stack";
import type { ReactElement } from "react";
import { act } from "react-test-renderer";
import { beforeEach, expect, it, vi } from "vitest";
import { createNewSessionService } from "../../../mobile/src/services/newSession";
import type { CreationDraft } from "../creationDraftRepository";
import { HostsController } from "../hosts/hostsController";
import { hostRow, scriptedFleet } from "../hosts/hostsTestUtils";
import { LiveSessionsReader } from "../hosts/liveCounts";
import { createNewSessionStore } from "../newSession";
import { alertRequests, playedHaptics, render, renderedText } from "../renderNative.testkit";
import { LaunchMemory } from "./launchMemory";
import { NewSessionForm } from "./NewSessionForm";
import { NewSessionProvider, type NewSessionRoutes } from "./newSessionContext";
import { memoryStorage, sheetContext } from "./newSessionTestUtils";

const status = vi.hoisted(() => ({ line: null as string | null }));
vi.mock("../board/connectionStatus", async (importOriginal) => ({
	...(await importOriginal<typeof import("../board/connectionStatus")>()),
	useConnectionStatusText: () => status.line,
}));
vi.mock("../ConnectionProvider", () => ({ useConnection: () => ({ state: "ready", fatal: false }) }));
vi.mock("@react-navigation/native", async () => {
	const { useEffect } = await import("react");
	return {
		useFocusEffect: (effect: () => undefined | (() => void)) => useEffect(effect, [effect]),
		StackActions: { replace: (name: string, params: unknown) => ({ type: "REPLACE", payload: { name, params } }) },
	};
});
vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));
vi.mock("react-native-safe-area-context", () => ({ SafeAreaView: "SafeAreaView" }));
vi.mock("../nativeImagePicker", () => ({
	nativeImagePicker: {
		pick: async () => [],
		capture: async () => [],
		encode: async () => "",
		id: () => "image-1",
	},
}));

const settle = () =>
	act(async () => {
		await new Promise((resolve) => setTimeout(resolve, 0));
	});

const glm: ModelDescriptor = {
	provider: "lunaroute",
	model: "glm-5.3-vision",
	displayName: "GLM 5.3 Vision",
	reasoningEffortLevels: ["low", "medium", "high", "xhigh", "max"],
};

interface Options {
	hosts?: HostRow[];
	draft?: Partial<CreationDraft>;
	/** How thread/start answers: a refusal, or the thread it made. */
	refuseStart?: Error;
}

/** The form inside a context as NewSessionSheet builds it: a real creation
 * store bound to a service over a scripted hub, a HostsController over the
 * same hub, and a LaunchMemory over memory. */
async function mount(options: Options = {}) {
	const fleet = scriptedFleet(options.hosts ?? []);
	const calls: { method: string; params: unknown }[] = [];
	const client = {
		request: async (method: string, params: unknown) => {
			calls.push({ method, params });
			const forwarded = method === "evener/host/request" ? (params as { method: string }).method : method;
			if (forwarded === "evener/projects/recent") return { data: ["/home/jesse/git/evener"] };
			if (forwarded === "model/list") return { data: [glm] };
			if (forwarded === "evener/path/validate") return { path: "", valid: true };
			if (method === "thread/start") {
				if (options.refuseStart) throw options.refuseStart;
				return { thread: { id: "t", name: "Fix the flaky test", evener: { ref: "paradise-park:t" } }, turn: {} };
			}
			return fleet.client.request(method as never, params as never);
		},
		onNotification: () => () => {},
	};
	const drafts = new Map<string, CreationDraft>();
	if (options.draft)
		drafts.set("hub-1", {
			source: "local",
			cwd: "",
			prompt: "",
			harness: "",
			model: null,
			reasoning: "",
			launchOverrides: {},
			images: [],
			unconfirmed: false,
			...options.draft,
		});
	const store = createNewSessionStore("hub-1", () => ({
		read: (hubId: string) => drafts.get(hubId) ?? null,
		write: (hubId: string, draft: CreationDraft) => void drafts.set(hubId, structuredClone(draft)),
		clear: (hubId: string) => void drafts.delete(hubId),
	}));
	store.getState().bind(createNewSessionService(client as never));
	void store.getState().loadMetadata();
	void store.getState().loadModels(true);
	const hosts = new HostsController(client as never);
	const live = new LiveSessionsReader(client as never);
	const memory = new LaunchMemory(memoryStorage(), "hub-1");
	let context = sheetContext(store, { client: client as never, hosts, live, memory });
	let headerOptions: NativeStackNavigationOptions = {};
	const parent = { goBack: vi.fn(), dispatch: vi.fn() };
	const navigation = {
		isFocused: () => true,
		navigate: vi.fn(),
		getParent: () => parent,
		setOptions: vi.fn((next: NativeStackNavigationOptions) => {
			headerOptions = { ...headerOptions, ...next };
		}),
	};
	const form = () => (
		<NewSessionProvider value={context}>
			<NewSessionForm
				navigation={navigation as unknown as NativeStackScreenProps<NewSessionRoutes, "Form">["navigation"]}
				route={{ key: "Form", name: "Form", params: undefined }}
			/>
		</NewSessionProvider>
	);
	const tree = render(form());
	await settle();
	/** A header button as the stack would place it (a HeaderButton element). */
	const header = (side: "headerLeft" | "headerRight") =>
		(
			headerOptions[side] as (props: { canGoBack: boolean }) => ReactElement<{
				label: string;
				onPress(): void;
				disabled?: boolean;
			}>
		)({ canGoBack: false });
	const row = (label: string) =>
		tree.root.findAll(
			(node) =>
				typeof node.props.accessibilityLabel === "string" && node.props.accessibilityLabel.startsWith(`${label}, `),
		)[0] ?? null;
	const prompt = () => tree.root.findByProps({ accessibilityLabel: "What should the agent do?" });
	const setReady = async (ready: boolean) => {
		context = { ...context, ready };
		store.getState().bind(ready ? createNewSessionService(client as never) : null);
		await act(async () => {
			tree.update(form());
		});
		await settle();
	};
	return {
		tree,
		store,
		calls,
		drafts,
		memory,
		parent,
		navigation,
		headerOptions: () => headerOptions,
		header,
		row,
		prompt,
		setReady,
		text: () => renderedText(tree),
		dispose: () => (hosts.dispose(), live.dispose(), tree.unmount()),
	};
}

beforeEach(() => {
	status.line = null;
	alertRequests.length = 0;
});

it("has Cancel, New session and Start, then the prompt, WHERE and AGENT with their values", async () => {
	const form = await mount({ draft: { cwd: "/home/jesse/git/evener" } });
	expect(form.headerOptions().title).toBe("New session");
	expect(form.header("headerLeft").props.label).toBe("Cancel");
	expect(form.header("headerRight").props).toMatchObject({ label: "Start", disabled: false });
	expect(form.prompt().props.placeholder).toBe("What should the agent do?");
	expect(form.prompt().props.autoFocus).toBe(true);
	expect(form.row("Host")?.props.accessibilityLabel).toBe("Host, magic-kingdom");
	expect(form.row("Project")?.props.accessibilityLabel).toBe("Project, evener");
	expect(form.row("Model")?.props.accessibilityLabel).toBe("Model, Hub default");
	const text = form.text();
	expect(text).toContain("Where");
	expect(text).toContain("Agent");
	expect(text).toContain(
		"Host, plugins and access are fixed once the session starts. Model and effort can change later.",
	);
	for (const word of ["harness", "Harness", "thread", "source", "runtime", "daemon"]) expect(text).not.toContain(word);
	await act(async () => form.row("Host")?.props.onPress());
	expect(form.navigation.navigate).toHaveBeenLastCalledWith("Host");
	await act(async () => form.row("Project")?.props.onPress());
	expect(form.navigation.navigate).toHaveBeenLastCalledWith("Project");
	await act(async () => form.row("Model")?.props.onPress());
	expect(form.navigation.navigate).toHaveBeenLastCalledWith("Model");
	form.dispose();
});

it("says Choose a project with none, and blames Project quietly", async () => {
	const form = await mount({ draft: { prompt: "hello" } });
	expect(form.row("Project")?.props.accessibilityLabel).toBe("Project, Choose a project");
	expect(form.header("headerRight").props.disabled).toBe(true);
	form.dispose();
});

it("offers Effort only for a model that lists levels, lit on the chosen one", async () => {
	const form = await mount({ draft: { cwd: "/home/jesse/git/evener" } });
	expect(form.text()).not.toContain("How long it thinks before acting");
	await act(async () => form.store.getState().selectModel(glm));
	expect(form.row("Model")?.props.accessibilityLabel).toBe("Model, via lunaroute, GLM 5.3 Vision");
	expect(form.text()).toContain("How long it thinks before acting");
	const segments = form.tree.root.findAll((node) => node.props.accessibilityRole === "radio" && node.props.onPress);
	expect(segments.map((segment) => segment.props.accessibilityLabel)).toEqual(["Low", "Med", "High", "XHigh", "Max"]);
	expect(segments.some((segment) => segment.props.accessibilityState.checked)).toBe(false);
	await act(async () => segments[3]?.props.onPress());
	expect(form.store.getState().reasoning).toBe("xhigh");
	const lit = form.tree.root.findAll((node) => node.props.accessibilityRole === "radio" && node.props.onPress);
	expect(lit.find((segment) => segment.props.accessibilityState.checked)?.props.accessibilityLabel).toBe("XHigh");
	form.dispose();
});

it("never starts on an offline host, and says why (Review Focus 3)", async () => {
	const form = await mount({
		hosts: [hostRow("paradise-park", { attached: false })],
		draft: { source: "paradise-park", cwd: "/Users/jesse/git/evener", prompt: "go" },
	});
	expect(form.row("Host")?.props.accessibilityLabel).toBe("Host, paradise-park, Offline");
	expect(form.text()).toContain("Offline");
	expect(form.text()).toContain("paradise-park is offline. Connect it or choose another host.");
	const start = form.header("headerRight");
	expect(start.props.disabled).toBe(true);
	await act(async () => start.props.onPress());
	expect(form.calls.some((call) => call.method === "thread/start")).toBe(false);
	form.dispose();
});

it("never starts on a host the hub no longer lists (Review Focus 3)", async () => {
	const form = await mount({
		hosts: [],
		draft: { source: "paradise-park", cwd: "/Users/jesse/git/evener", prompt: "go" },
	});
	expect(form.text()).toContain("paradise-park is no longer a host on this hub. Choose another host.");
	const start = form.header("headerRight");
	expect(start.props.disabled).toBe(true);
	await act(async () => start.props.onPress());
	expect(form.calls.some((call) => call.method === "thread/start")).toBe(false);
	form.dispose();
});

it("starts on another host, remembers the start, and replaces the sheet with the session (Review Focus 1)", async () => {
	const form = await mount({
		hosts: [hostRow("paradise-park")],
		draft: { source: "paradise-park", cwd: "/Users/jesse/git/evener", prompt: "Fix the flaky test" },
	});
	await act(async () => form.store.getState().selectModel(glm));
	const start = form.header("headerRight");
	expect(start.props.disabled).toBe(false);
	await act(async () => start.props.onPress());
	await settle();
	expect(form.calls.find((call) => call.method === "thread/start")?.params).toMatchObject({
		source: "paradise-park",
		cwd: "/Users/jesse/git/evener",
		model: "glm-5.3-vision",
		modelProvider: "lunaroute",
	});
	expect(form.memory.history()[0]?.setup).toEqual({
		host: "paradise-park",
		cwd: "/Users/jesse/git/evener",
		model: { provider: "lunaroute", model: "glm-5.3-vision" },
		effort: "",
		overrides: {},
	});
	expect(form.parent.dispatch).toHaveBeenCalledWith({
		type: "REPLACE",
		payload: {
			name: "Conversation",
			params: { hubId: "hub-1", ref: "paradise-park:t", title: "Fix the flaky test" },
		},
	});
	form.dispose();
});

it("stays open with the hub's reason when the hub refuses the start", async () => {
	const form = await mount({
		draft: { cwd: "/home/jesse/git/evener", prompt: "go" },
		refuseStart: new WireError("the hub is shutting down", -32000),
	});
	await act(async () => form.header("headerRight").props.onPress());
	await settle();
	expect(form.parent.dispatch).not.toHaveBeenCalled();
	expect(form.text()).toContain("the hub is shutting down");
	expect(form.prompt().props.value).toBe("go");
	expect(form.memory.history()).toEqual([]);
	form.dispose();
});

it("keeps the typed prompt and holds Start while the connection is down (Review Focus 4)", async () => {
	const form = await mount({ draft: { cwd: "/home/jesse/git/evener" } });
	await act(async () => form.prompt().props.onChangeText("half a thought"));
	status.line = "Reconnecting…";
	await form.setReady(false);
	expect(form.prompt().props.value).toBe("half a thought");
	expect(form.header("headerRight").props.disabled).toBe(true);
	await act(async () => form.header("headerRight").props.onPress());
	expect(form.calls.some((call) => call.method === "thread/start")).toBe(false);
	expect(form.text()).toContain("Reconnecting…");
	expect(form.text()).not.toMatch(/\bReconnect\b/);
	form.dispose();
});

it("asks before Cancel deletes a draft with a prompt, and Delete draft empties it (ruling 18)", async () => {
	const form = await mount({ draft: { cwd: "/home/jesse/git/evener", prompt: "a thought" } });
	await act(async () => form.header("headerLeft").props.onPress());
	expect(form.parent.goBack).not.toHaveBeenCalled();
	const ask = alertRequests.at(-1);
	expect(ask?.title).toBe("Delete this draft?");
	expect(ask?.buttons?.map((button) => [button.text, button.style])).toEqual([
		["Keep draft", "cancel"],
		["Delete draft", "destructive"],
	]);
	playedHaptics.length = 0;
	await act(async () => ask?.buttons?.[1]?.onPress?.());
	expect(playedHaptics).toEqual(["impact:rigid"]);
	expect(form.parent.goBack).toHaveBeenCalledTimes(1);
	expect(form.store.getState()).toMatchObject({ prompt: "", cwd: "" });
	expect(form.drafts.has("hub-1")).toBe(false);
	form.dispose();
});

it("keeps the draft when Cancel's question is answered Keep draft", async () => {
	const form = await mount({ draft: { cwd: "/home/jesse/git/evener", prompt: "a thought" } });
	await act(async () => form.header("headerLeft").props.onPress());
	await act(async () => alertRequests.at(-1)?.buttons?.[0]?.onPress?.());
	expect(form.parent.goBack).toHaveBeenCalledTimes(1);
	expect(form.drafts.get("hub-1")).toMatchObject({ prompt: "a thought" });
	form.dispose();
});

it("closes at once on Cancel with nothing to lose", async () => {
	const form = await mount({ draft: { cwd: "/home/jesse/git/evener" } });
	await act(async () => form.header("headerLeft").props.onPress());
	expect(alertRequests).toEqual([]);
	expect(form.parent.goBack).toHaveBeenCalledTimes(1);
	form.dispose();
});

it("puts the Host row's note under WHERE when the project moved with the host", async () => {
	const form = await mount({ draft: { cwd: "/home/jesse/git/evener" } });
	await act(async () => form.store.setState({ hostNote: "evener isn't on paradise-park. Choose a project." }));
	expect(form.text()).toContain("evener isn't on paradise-park. Choose a project.");
	form.dispose();
});
