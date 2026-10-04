import { type HostRow, type ModelDescriptor, type PluginPreviewResponse, WireError } from "@evener/appwire-client";
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
import { AlertCenter } from "../alerts/alertCenter";
import { AlertsContext } from "../alerts/alertsContext";
import { alertRequests, playedHaptics, render, renderedText } from "../renderNative.testkit";
import { LaunchMemory } from "./launchMemory";
import { creationStore, forgetCreationForHub } from "./creations";
import { formFront } from "./formFront";
import { NewSessionForm } from "./NewSessionForm";
import type { NewSessionRoutes } from "./newSessionContext";
import { memoryStorage, sheetContext, TestSheet } from "./newSessionTestUtils";

const status = vi.hoisted(() => ({ line: null as string | null }));
vi.mock("../board/connectionStatus", async (importOriginal) => ({
	...(await importOriginal<typeof import("../board/connectionStatus")>()),
	useConnectionStatusText: () => status.line,
}));
vi.mock("../ConnectionProvider", () => ({ useConnection: () => ({ state: "ready", fatal: false }) }));
// Whether the form's screen is focused, for useFocusEffect: a picker pushed
// over it takes focus, and coming back gives it back.
const screen = vi.hoisted(() => ({ focused: true }));
vi.mock("@react-navigation/native", async () => {
	const { useEffect } = await import("react");
	return {
		useFocusEffect: (effect: () => undefined | (() => void)) =>
			// biome-ignore lint/correctness/useExhaustiveDependencies: the focus flag is the test's stand-in for navigation.
			useEffect(() => (screen.focused ? effect() : undefined), [effect, screen.focused]),
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
		encode: async () => ({ data: "", mediaType: "image/jpeg" }),
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
	/** model/list fails. */
	refuseModels?: boolean;
	/** evener/plugin/preview waits for the test's releaseStart too. */
	holdPreview?: boolean;
	/** thread/start waits for the test's releaseStart. */
	holdStart?: boolean;
	/** The form shows the hub's own creation store (creations.ts), as the
	 * sheet gives it, rather than one made for the test. */
	hubStore?: boolean;
	/** How often the form's hosts controller reads the hub's hosts. */
	hostPollMs?: number;
	/** How evener/plugin/preview answers; no plugins when absent, a refusal
	 * when an Error. */
	plugins?: PluginPreviewResponse | Error;
	/** The project's branch; "" outside a repository. */
	branch?: string;
	/** The hub's launch defaults for the project. */
	defaults?: Record<string, unknown>;
	/** evener/launch/resolve fails, so the hub's defaults stay unknown. */
	refuseDefaults?: boolean;
}

/** The form inside a context as NewSessionSheet builds it: a real creation
 * store bound to a service over a scripted hub, a HostsController over the
 * same hub, and a LaunchMemory over memory. */
async function mount(options: Options = {}) {
	const fleet = scriptedFleet(options.hosts ?? []);
	const calls: { method: string; params: unknown }[] = [];
	let releaseStart = () => {};
	const held = new Promise<void>((resolve) => (releaseStart = resolve));
	const client = {
		request: async (method: string, params: unknown) => {
			calls.push({ method, params });
			const forwarded = method === "evener/host/request" ? (params as { method: string }).method : method;
			if (forwarded === "evener/projects/recent") return { data: ["/home/jesse/git/evener"] };
			if (forwarded === "model/list") {
				if (options.refuseModels) throw new Error("models unavailable");
				return { data: [glm] };
			}
			if (forwarded === "evener/path/validate") return { path: "", valid: true };
			if (forwarded === "evener/plugin/preview") {
				if (options.holdPreview) await held;
				if (options.plugins instanceof Error) throw options.plugins;
				return options.plugins ?? { plugins: [] };
			}
			if (forwarded === "evener/git/head") return { head: options.branch ?? "" };
			if (forwarded === "evener/launch/resolve") {
				if (options.refuseDefaults) throw new Error("hub away");
				return { effective: options.defaults ?? {}, layers: {}, provenance: {} };
			}
			if (method === "thread/start") {
				if (options.holdStart) await held;
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
	const repository = {
		read: (hubId: string) => drafts.get(hubId) ?? null,
		write: (hubId: string, draft: CreationDraft) => void drafts.set(hubId, structuredClone(draft)),
		clear: (hubId: string) => void drafts.delete(hubId),
	};
	if (options.hubStore) forgetCreationForHub("hub-1");
	const store = options.hubStore
		? creationStore("hub-1", () => repository)
		: createNewSessionStore("hub-1", () => repository);
	store.getState().bind(createNewSessionService(client as never));
	void store.getState().loadMetadata();
	void store.getState().loadModels(true);
	const hosts = new HostsController(client as never, undefined, options.hostPollMs);
	const live = new LiveSessionsReader(client as never);
	const memory = new LaunchMemory(memoryStorage(), "hub-1");
	let context = sheetContext(store, { client: client as never, hosts, live, memory });
	// The app's real alert center, so a start the form can no longer open
	// reaches the banner.
	const alerts = new AlertCenter({
		now: () => Date.now(),
		setTimeout: (callback: () => void, ms: number) => setTimeout(callback, ms),
		clearTimeout: (handle: unknown) => clearTimeout(handle as ReturnType<typeof setTimeout>),
	});
	/** One mounted form: the sheet's stack gives each its own navigation. */
	function sheetNavigation() {
		let headerOptions: NativeStackNavigationOptions = {};
		const parent = { goBack: vi.fn(), dispatch: vi.fn() };
		const focus = { focused: true };
		const navigation = {
			isFocused: () => focus.focused,
			navigate: vi.fn(),
			getParent: () => parent,
			setOptions: vi.fn((next: NativeStackNavigationOptions) => {
				headerOptions = { ...headerOptions, ...next };
			}),
		};
		/** A header button as the stack would place it (a HeaderButton element). */
		const header = (side: "headerLeft" | "headerRight") =>
			(
				headerOptions[side] as (props: { canGoBack: boolean }) => ReactElement<{
					label: string;
					onPress(): void;
					disabled?: boolean;
				}>
			)({ canGoBack: false });
		return { parent, focus, navigation, header, headerOptions: () => headerOptions };
	}
	const first = sheetNavigation();
	const { parent, focus, navigation, header } = first;
	const form = (nav = navigation) => (
		<AlertsContext.Provider value={{ center: alerts, reportRoutes: () => {}, noticeFor: () => undefined }}>
			<TestSheet value={context}>
				<NewSessionForm
					navigation={nav as unknown as NativeStackScreenProps<NewSessionRoutes, "Form">["navigation"]}
					route={{ key: "Form", name: "Form", params: undefined }}
				/>
			</TestSheet>
		</AlertsContext.Provider>
	);
	const tree = render(form());
	await settle();
	/** The person swipes this sheet away and opens New session again: a new
	 * form on the hub's same store. */
	const reopen = async () => {
		focus.focused = false;
		act(() => tree.unmount());
		const next = sheetNavigation();
		const reopened = render(form(next.navigation));
		await settle();
		/** The connection drops, or comes back, under the reopened sheet. */
		const setReopenedReady = async (ready: boolean) => {
			context = { ...context, ready };
			store.getState().bind(ready ? createNewSessionService(client as never) : null);
			await act(async () => {
				reopened.update(form(next.navigation));
			});
			await settle();
		};
		return { ...next, tree: reopened, text: () => renderedText(reopened), setReady: setReopenedReady };
	};
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
	/** A new connection's client takes over the sheet. */
	const swapClient = async () => {
		context = { ...context, client: { ...client } as never };
		await act(async () => {
			tree.update(form());
		});
	};
	return {
		alerts,
		tree,
		focus,
		releaseStart,
		swapClient,
		store,
		calls,
		drafts,
		memory,
		parent,
		navigation,
		headerOptions: first.headerOptions,
		header,
		reopen,
		/** Renders the form again, as a focus change does. */
		rerender: () =>
			act(async () => {
				tree.update(form());
			}),
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
	screen.focused = true;
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

it("dismisses the keyboard when the form is dragged", async () => {
	const form = await mount();
	expect(form.tree.root.findByType("ScrollView" as never).props.keyboardDismissMode).toBe("on-drag");
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
	expect(form.memory.lastSetup()).toEqual({
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
	expect(form.memory.lastSetup()).toBeNull();
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

it("opens nothing when the form lost focus while the start was on its way", async () => {
	const form = await mount({ draft: { cwd: "/home/jesse/git/evener", prompt: "go" }, holdStart: true });
	await act(async () => void form.header("headerRight").props.onPress());
	form.focus.focused = false;
	await act(async () => form.releaseStart());
	await settle();
	expect(form.calls.filter((call) => call.method === "thread/start")).toHaveLength(1);
	expect(form.parent.dispatch).not.toHaveBeenCalled();
	// The session exists: the start is remembered and the form is empty for next time.
	expect(form.memory.lastSetup()).not.toBeNull();
	expect(form.store.getState().prompt).toBe("");
	// So it isn't started twice, a banner says so and opens it (#3048).
	const banner = form.alerts.getSnapshot().banner;
	expect(banner?.alerts).toEqual([{ kind: "started", ref: expect.any(String), title: expect.any(String), why: null }]);
	expect(form.alerts.tap()).toMatchObject({ kind: "session" });
	form.dispose();
});

it("says Session started when the sheet was swiped away while the start was on its way", async () => {
	const form = await mount({ draft: { cwd: "/home/jesse/git/evener", prompt: "go" }, holdStart: true });
	await act(async () => void form.header("headerRight").props.onPress());
	// The sheet is gone: its screens unmount, and react-navigation answers
	// isFocused() false for a screen whose route left the stack
	// (useNavigationCache's isFocused asks each parent in turn).
	form.focus.focused = false;
	form.dispose();
	await act(async () => form.releaseStart());
	await settle();
	expect(form.parent.dispatch).not.toHaveBeenCalled();
	expect(form.memory.lastSetup()).not.toBeNull();
	expect(form.alerts.getSnapshot().banner?.alerts).toEqual([
		{ kind: "started", ref: expect.any(String), title: expect.any(String), why: null },
	]);
});

it("opens nothing when another connection took over while the start was on its way", async () => {
	const form = await mount({ draft: { cwd: "/home/jesse/git/evener", prompt: "go" }, holdStart: true });
	await act(async () => void form.header("headerRight").props.onPress());
	await form.swapClient();
	await act(async () => form.releaseStart());
	await settle();
	expect(form.calls.filter((call) => call.method === "thread/start")).toHaveLength(1);
	expect(form.parent.dispatch).not.toHaveBeenCalled();
	expect(form.memory.lastSetup()).not.toBeNull();
	expect(form.alerts.getSnapshot().banner?.alerts[0]).toMatchObject({ kind: "started" });
	form.dispose();
});

it("keeps the draft when the sheet is swiped away (ruling 18)", async () => {
	const form = await mount({ draft: { cwd: "/home/jesse/git/evener" } });
	await act(async () => form.prompt().props.onChangeText("half a thought"));
	form.dispose();
	expect(alertRequests).toEqual([]);
	expect(form.parent.goBack).not.toHaveBeenCalled();
	expect(form.drafts.get("hub-1")).toMatchObject({ cwd: "/home/jesse/git/evener", prompt: "half a thought" });
});

it("asks before Cancel deletes a draft that holds only an image", async () => {
	const form = await mount({
		draft: {
			cwd: "/home/jesse/git/evener",
			images: [{ id: "photo", marker: 1, mediaType: "image/png", data: "AQID" }],
		},
	});
	await act(async () => form.header("headerLeft").props.onPress());
	expect(alertRequests.at(-1)?.title).toBe("Delete this draft?");
	expect(form.parent.goBack).not.toHaveBeenCalled();
	form.dispose();
});

it("reads the hub's hosts while the form is on screen, and stops when it leaves", async () => {
	const form = await mount({ draft: { cwd: "/home/jesse/git/evener" }, hostPollMs: 5 });
	const reads = () => form.calls.filter((call) => call.method === "evener/host/list").length;
	await act(async () => {
		await new Promise((resolve) => setTimeout(resolve, 30));
	});
	expect(reads()).toBeGreaterThan(1);
	act(() => form.tree.unmount());
	const after = reads();
	await act(async () => {
		await new Promise((resolve) => setTimeout(resolve, 30));
	});
	expect(reads()).toBe(after);
});

it("holds Start, and says why, when a chosen model can't be checked against the host's models", async () => {
	const form = await mount({
		draft: { cwd: "/home/jesse/git/evener", prompt: "go", model: { provider: "lunaroute", model: "glm-5.3-vision" } },
		refuseModels: true,
	});
	expect(form.header("headerRight").props.disabled).toBe(true);
	expect(form.text()).toContain(
		"Couldn't load this host's models, so glm-5.3-vision can't be used. Choose Hub default to start.",
	);
	expect(form.row("Model")?.props.accessibilityLabel).toBe("Model, via lunaroute, glm-5.3-vision");
	await act(async () => form.store.getState().selectModel(null));
	expect(form.header("headerRight").props.disabled).toBe(false);
	await act(async () => form.header("headerRight").props.onPress());
	await settle();
	expect(form.calls.some((call) => call.method === "thread/start")).toBe(true);
	form.dispose();
});

it("remembers the model and effort a per-launch setting started the session with", async () => {
	const form = await mount({ draft: { cwd: "/home/jesse/git/evener", prompt: "go" } });
	await act(async () =>
		form.store.getState().setLaunchOverrides({ model: "lunaroute/glm-5.3-vision", reasoningEffort: "high" }),
	);
	await act(async () => form.header("headerRight").props.onPress());
	await settle();
	expect(form.calls.find((call) => call.method === "thread/start")?.params).toMatchObject({
		model: "lunaroute/glm-5.3-vision",
		reasoningEffort: "high",
	});
	expect(form.memory.lastSetup()).toMatchObject({
		model: { provider: "lunaroute", model: "glm-5.3-vision" },
		effort: "high",
	});
	form.dispose();
});

/** Past usePluginPreview's 250ms debounce. */
const debounce = () =>
	act(async () => {
		await new Promise((resolve) => setTimeout(resolve, 300));
	});

const plugin = (name: string, selected: boolean) => ({
	name,
	source: "installed" as const,
	marketplace: "superpowers-marketplace",
	selected,
	skillCount: 1,
	agentCount: 0,
	commandCount: 0,
	hookCount: 0,
	mcpCount: 0,
});

it("counts the plugins the session starts with, and opens the checklist", async () => {
	const form = await mount({
		draft: { cwd: "/home/jesse/git/evener" },
		plugins: { plugins: [plugin("superpowers", true), plugin("go", false)] },
	});
	expect(form.row("Plugins")?.props.accessibilityLabel).toBe("Plugins, …");
	await debounce();
	expect(form.row("Plugins")?.props.accessibilityLabel).toBe("Plugins, 1 of 2");
	await act(async () => form.row("Plugins")?.props.onPress());
	expect(form.navigation.navigate).toHaveBeenLastCalledWith("Plugins");
	form.dispose();
});

it("holds Start while a chosen plugin has a blocking problem, and says which", async () => {
	const form = await mount({
		draft: {
			cwd: "/home/jesse/git/evener",
			prompt: "go",
			launchOverrides: { enabledPlugins: ["superpowers", "gone"] },
		},
		plugins: { plugins: [plugin("superpowers", true)] },
	});
	await debounce();
	expect(form.row("Plugins")?.props.accessibilityLabel).toBe("Plugins, 1 need attention, 1 of 1");
	expect(form.header("headerRight").props.disabled).toBe(true);
	expect(form.text()).toContain("gone: not present in current preview");
	form.dispose();
});

it("shows the project's branch after Project, with nothing to open", async () => {
	const form = await mount({ draft: { cwd: "/home/jesse/git/evener" }, branch: "main" });
	await settle();
	const branch = form.row("Branch");
	expect(branch?.props.accessibilityLabel).toBe("Branch, main");
	expect(branch?.props.onPress).toBeUndefined();
	expect(form.calls).toContainEqual({ method: "evener/git/head", params: { cwd: "/home/jesse/git/evener" } });
	form.dispose();
});

it("leaves Branch out for a folder that isn't a repository", async () => {
	const form = await mount({ draft: { cwd: "/home/jesse/git/evener" }, branch: "" });
	await settle();
	expect(form.row("Branch")).toBeNull();
	form.dispose();
});

it("names the access the session gets, the hub's own by default, and opens Access", async () => {
	const form = await mount({ draft: { cwd: "/home/jesse/git/evener" }, defaults: { sandbox: "workspace-write" } });
	await settle();
	expect(form.row("Access")?.props.accessibilityLabel).toBe(
		"Access, Writes only in the project; reads anywhere but secrets, Workspace write",
	);
	await act(async () => form.store.getState().setLaunchOverrides({ sandbox: "read-only" }));
	expect(form.row("Access")?.props.accessibilityLabel).toBe(
		"Access, Writes nothing; reads anywhere but secrets, Read-only",
	);
	await act(async () => form.row("Access")?.props.onPress());
	expect(form.navigation.navigate).toHaveBeenLastCalledWith("Access");
	form.dispose();
});

it("says More options is Custom once one of its settings is set, and opens it", async () => {
	const form = await mount({ draft: { cwd: "/home/jesse/git/evener" } });
	expect(form.row("More options")?.props.accessibilityLabel).toBe(
		"More options, Context strategy, subagent depth, turn limit",
	);
	await act(async () => form.store.getState().setLaunchOverrides({ maxRounds: 100 }));
	expect(form.row("More options")?.props.accessibilityLabel).toBe(
		"More options, Context strategy, subagent depth, turn limit, Custom",
	);
	await act(async () => form.row("More options")?.props.onPress());
	expect(form.navigation.navigate).toHaveBeenLastCalledWith("MoreOptions");
	expect(form.text()).not.toContain("Session options");
	form.dispose();
});

it("names no access until the hub says its default, unless the person chose one", async () => {
	const form = await mount({ draft: { cwd: "/home/jesse/git/evener" }, refuseDefaults: true });
	await settle();
	expect(form.row("Access")).toBeNull();
	expect(form.tree.root.findAll((node) => node.props.accessibilityLabel === "Access")).not.toHaveLength(0);
	expect(form.text()).not.toContain("Full access");
	await act(async () => form.store.getState().setLaunchOverrides({ sandbox: "restricted" }));
	expect(form.row("Access")?.props.accessibilityLabel).toBe("Access, Reads and writes only in the project, Restricted");
	form.dispose();
});

it("says the Plugins row waits for a project", async () => {
	const form = await mount({ draft: { prompt: "go" } });
	await debounce();
	expect(form.row("Plugins")?.props.accessibilityLabel).toBe("Plugins, Listed once a project is chosen");
	form.dispose();
});

it("says the host's plugins couldn't be listed when the first preview fails", async () => {
	const form = await mount({ draft: { cwd: "/home/jesse/git/evener" }, plugins: new Error("plugin cache locked") });
	await debounce();
	expect(form.row("Plugins")?.props.accessibilityLabel).toBe("Plugins, Couldn't list this host's plugins");
	form.dispose();
});

it("forgets the last project's plugin problems as soon as the project changes", async () => {
	const form = await mount({
		draft: { cwd: "/home/jesse/git/evener", prompt: "go", launchOverrides: { enabledPlugins: ["gone"] } },
		plugins: { plugins: [plugin("superpowers", true)] },
	});
	await debounce();
	expect(form.row("Plugins")?.props.accessibilityLabel).toBe("Plugins, 1 need attention, 0 of 1");
	expect(form.text()).toContain("gone: not present in current preview");
	await act(async () => form.store.setState({ cwd: "/home/jesse/git/docs" }));
	expect(form.row("Plugins")?.props.accessibilityLabel).toBe("Plugins, …");
	expect(form.text()).not.toContain("gone: not present in current preview");
	await debounce();
	expect(form.row("Plugins")?.props.accessibilityLabel).toBe("Plugins, 1 need attention, 0 of 1");
	form.dispose();
});

it("reads Starting… while its start is on its way, and Cancel then closes without discarding", async () => {
	const form = await mount({ draft: { cwd: "/home/jesse/git/evener", prompt: "go" }, holdStart: true });
	await act(async () => void form.header("headerRight").props.onPress());
	expect(form.header("headerRight").props).toMatchObject({ label: "Starting…", disabled: true });
	await act(async () => form.header("headerLeft").props.onPress());
	expect(alertRequests).toEqual([]);
	expect(form.parent.goBack).toHaveBeenCalledTimes(1);
	expect(form.store.getState()).toMatchObject({ submitting: true, prompt: "go" });
	expect(form.drafts.get("hub-1")).toMatchObject({ prompt: "go" });
	await act(async () => form.releaseStart());
	form.dispose();
});

it("lets a sheet reopened mid-start open the session its start makes (#3104)", async () => {
	const form = await mount({ draft: { cwd: "/home/jesse/git/evener", prompt: "go" }, holdStart: true });
	await act(async () => void form.header("headerRight").props.onPress());
	const reopened = await form.reopen();
	expect(reopened.header("headerRight").props).toMatchObject({ label: "Starting…", disabled: true });
	await act(async () => form.releaseStart());
	await settle();
	expect(form.calls.filter((call) => call.method === "thread/start")).toHaveLength(1);
	expect(form.parent.dispatch).not.toHaveBeenCalled();
	expect(reopened.parent.dispatch).toHaveBeenCalledWith({
		type: "REPLACE",
		payload: {
			name: "Conversation",
			params: { hubId: "hub-1", ref: "paradise-park:t", title: "Fix the flaky test" },
		},
	});
	expect(form.alerts.getSnapshot().banner).toBeNull();
	act(() => reopened.tree.unmount());
});

it("says so when a start fails after its sheet closed, and the alert opens New session with the draft (#3104)", async () => {
	const form = await mount({
		draft: { cwd: "/home/jesse/git/evener", prompt: "go" },
		holdStart: true,
		refuseStart: new WireError("the hub is shutting down", -32000),
	});
	await act(async () => void form.header("headerRight").props.onPress());
	form.focus.focused = false;
	form.dispose();
	await act(async () => form.releaseStart());
	await settle();
	expect(form.alerts.getSnapshot().banner?.alerts).toEqual([
		{ kind: "startFailed", hubId: "hub-1", hubName: "magic-kingdom", uncertain: true },
	]);
	expect(form.drafts.get("hub-1")).toMatchObject({ prompt: "go" });
	expect(form.alerts.tap()).toEqual({ kind: "newSession", hubId: "hub-1", hubName: "magic-kingdom" });
});

it("says a start the hub never got couldn't start, once, and New session answers it (#3104)", async () => {
	const form = await mount({
		draft: { cwd: "/home/jesse/git/evener", prompt: "go", launchOverrides: { enabledPlugins: ["superpowers"] } },
		plugins: new Error("plugin cache locked"),
	});
	await debounce();
	// The sheet closes before the hub answers the plugin check.
	await act(async () => {
		void form.header("headerRight").props.onPress();
		form.focus.focused = false;
		form.dispose();
	});
	await settle();
	expect(form.alerts.getSnapshot().banner?.alerts).toEqual([
		{ kind: "startFailed", hubId: "hub-1", hubName: "magic-kingdom", uncertain: false },
	]);
	// Opening New session shows the same reason in the form, so the alert goes.
	const reopened = await form.reopen();
	expect(form.alerts.getSnapshot().banner).toBeNull();
	expect(reopened.text()).toContain("Couldn't check the selected plugins, so no session was started.");
	act(() => reopened.tree.unmount());
});

it("raises no alert for a failure the form in front already shows", async () => {
	const form = await mount({
		draft: { cwd: "/home/jesse/git/evener", prompt: "go" },
		refuseStart: new WireError("the hub is shutting down", -32000),
	});
	await act(async () => form.header("headerRight").props.onPress());
	await settle();
	expect(form.text()).toContain("the hub is shutting down");
	expect(form.alerts.getSnapshot().banner).toBeNull();
	form.dispose();
});

it("says a start the connection lost after its sheet closed may exist (#3104)", async () => {
	const form = await mount({ draft: { cwd: "/home/jesse/git/evener", prompt: "go" }, holdStart: true });
	await act(async () => void form.header("headerRight").props.onPress());
	form.focus.focused = false;
	form.dispose();
	// The connection drops while the hub is still starting it.
	await act(async () => form.store.getState().bind(null));
	await act(async () => form.releaseStart());
	await settle();
	expect(form.alerts.getSnapshot().banner?.alerts).toEqual([
		{ kind: "startFailed", hubId: "hub-1", hubName: "magic-kingdom", uncertain: true },
	]);
	expect(form.drafts.get("hub-1")).toMatchObject({ prompt: "go", unconfirmed: true });
});

it("raises nothing for a start whose hub was removed while it was on its way (#3104)", async () => {
	const form = await mount({ draft: { cwd: "/home/jesse/git/evener", prompt: "go" }, holdStart: true, hubStore: true });
	await act(async () => void form.header("headerRight").props.onPress());
	form.focus.focused = false;
	form.dispose();
	// The hub is removed: its store goes, unbound, so the start comes back obsolete.
	await act(async () => forgetCreationForHub("hub-1"));
	await act(async () => form.releaseStart());
	await settle();
	expect(form.alerts.getSnapshot()).toMatchObject({ banner: null, held: 0 });
});

it("leaves a failure to a sheet reopened while the hub is away, which shows it itself (#3104)", async () => {
	const form = await mount({ draft: { cwd: "/home/jesse/git/evener", prompt: "go" }, holdStart: true });
	await act(async () => void form.header("headerRight").props.onPress());
	const reopened = await form.reopen();
	await reopened.setReady(false);
	await act(async () => form.releaseStart());
	await settle();
	expect(reopened.text()).toContain("Creation could not be confirmed.");
	expect(form.alerts.getSnapshot()).toMatchObject({ banner: null, held: 0 });
	act(() => reopened.tree.unmount());
});

it("raises nothing for a start the connection ended before the hub got it (#3104)", async () => {
	const form = await mount({
		draft: { cwd: "/home/jesse/git/evener", prompt: "go", launchOverrides: { enabledPlugins: ["superpowers"] } },
		holdPreview: true,
	});
	await act(async () => void form.header("headerRight").props.onPress());
	form.focus.focused = false;
	form.dispose();
	// The connection drops while the plugin check is still out: nothing was sent.
	await act(async () => form.store.getState().bind(null));
	expect(form.store.getState().error).toBeNull();
	await act(async () => form.releaseStart());
	await settle();
	expect(form.alerts.getSnapshot()).toMatchObject({ banner: null, held: 0 });
});

it("holds Start after a new connection leaves its start unconfirmed, until the draft changes (#3104)", async () => {
	const form = await mount({ draft: { cwd: "/home/jesse/git/evener", prompt: "go" }, holdStart: true });
	await act(async () => void form.header("headerRight").props.onPress());
	await form.setReady(false);
	await form.setReady(true);
	expect(form.header("headerRight").props).toMatchObject({ label: "Start", disabled: true });
	expect(form.text()).toContain(
		"Creation could not be confirmed. It may have started: check the Board before starting this draft again, or change the draft.",
	);
	await act(async () => form.prompt().props.onChangeText("go, and fix the docs"));
	expect(form.header("headerRight").props.disabled).toBe(false);
	await act(async () => form.releaseStart());
	form.dispose();
});

it("retires a failed start's alert when the form comes back into focus, not only when it opens (#3104)", async () => {
	const form = await mount({
		draft: { cwd: "/home/jesse/git/evener", prompt: "go" },
		holdStart: true,
		refuseStart: new WireError("the hub is shutting down", -32000),
	});
	await act(async () => void form.header("headerRight").props.onPress());
	// A picker is pushed over the form while the start is out.
	screen.focused = false;
	form.focus.focused = false;
	await form.rerender();
	await act(async () => form.releaseStart());
	await settle();
	expect(form.alerts.getSnapshot().banner?.alerts[0]).toMatchObject({ kind: "startFailed" });
	// Back on the form, which shows the reason itself.
	screen.focused = true;
	form.focus.focused = true;
	await form.rerender();
	expect(form.alerts.getSnapshot()).toMatchObject({ banner: null, held: 0 });
	expect(form.text()).toContain("the hub is shutting down");
	form.dispose();
});

it("never loses a failed start on a hub you switched away from (#3104)", async () => {
	const form = await mount({
		draft: { cwd: "/home/jesse/git/evener", prompt: "go" },
		holdStart: true,
		refuseStart: new WireError("the hub is shutting down", -32000),
	});
	await act(async () => void form.header("headerRight").props.onPress());
	form.focus.focused = false;
	form.dispose();
	// Another hub is selected: the alert center starts over for it.
	await act(async () => form.alerts.reset());
	await act(async () => form.releaseStart());
	await settle();
	const shown = form.alerts.getSnapshot().banner?.alerts;
	expect(shown).toEqual([{ kind: "startFailed", hubId: "hub-1", hubName: "magic-kingdom", uncertain: true }]);
	// Switching once more keeps it, and a tap goes to its own hub.
	await act(async () => form.alerts.reset());
	expect(form.alerts.tap()).toEqual({ kind: "newSession", hubId: "hub-1", hubName: "magic-kingdom" });
});

it("retires only its own hub's failed-start alert when it comes into focus (#3104)", async () => {
	const form = await mount({ draft: { cwd: "/home/jesse/git/evener", prompt: "go" } });
	screen.focused = false;
	await form.rerender();
	await act(async () => {
		form.alerts.offer({ kind: "startFailed", hubId: "hub-2", hubName: "paradise-park", uncertain: false });
		form.alerts.offer({ kind: "startFailed", hubId: "hub-1", hubName: "magic-kingdom", uncertain: false });
	});
	screen.focused = true;
	await form.rerender();
	// This form is hub-1's: hub-2's alert stays.
	expect(form.alerts.getSnapshot().banner?.alerts).toEqual([
		{ kind: "startFailed", hubId: "hub-2", hubName: "paradise-park", uncertain: false },
	]);
	expect(form.alerts.tap()).toEqual({ kind: "newSession", hubId: "hub-2", hubName: "paradise-park" });
	form.dispose();
});

it("says once why Start holds a draft whose last start may have worked (#3104)", async () => {
	const once = (text: string, line: string) => text.split(line).length - 1;
	const why = "It may have started: check the Board before starting this draft again, or change the draft.";
	const restored = await mount({ draft: { cwd: "/home/jesse/git/evener", prompt: "go", unconfirmed: true } });
	expect(restored.header("headerRight").props.disabled).toBe(true);
	expect(once(restored.text(), why)).toBe(1);
	expect(restored.text()).toContain("An earlier creation could not be confirmed");
	restored.dispose();

	const rebound = await mount({ draft: { cwd: "/home/jesse/git/evener", prompt: "go" }, holdStart: true });
	await act(async () => void rebound.header("headerRight").props.onPress());
	await rebound.setReady(false);
	await rebound.setReady(true);
	expect(once(rebound.text(), why)).toBe(1);
	// Changed, the draft is a new start: Start opens, and the store's line stays.
	await act(async () => rebound.prompt().props.onChangeText("go, and fix the docs"));
	expect(rebound.header("headerRight").props.disabled).toBe(false);
	expect(once(rebound.text(), why)).toBe(1);
	await act(async () => rebound.releaseStart());
	rebound.dispose();

	// A start that failed after it was sent says so in the same one line.
	const failed = await mount({
		draft: { cwd: "/home/jesse/git/evener", prompt: "go" },
		refuseStart: new WireError("the hub is shutting down", -32000),
	});
	await act(async () => failed.header("headerRight").props.onPress());
	await settle();
	expect(failed.header("headerRight").props.disabled).toBe(true);
	expect(once(failed.text(), why)).toBe(1);
	expect(failed.text()).toContain("the hub is shutting down");
	failed.dispose();
});

it("leaves Start open, with the hub's one reason, when the hub says no session was started (#3184)", async () => {
	const form = await mount({
		draft: { cwd: "/home/jesse/git/evener", prompt: "go" },
		refuseStart: new WireError("cwd is not a directory", -32602, {
			evenerErrorInfo: "invalidParams",
			mutationOutcome: "notAccepted",
		}),
	});
	await act(async () => form.header("headerRight").props.onPress());
	await settle();
	expect(form.header("headerRight").props.disabled).toBe(false);
	expect(form.text()).toContain("cwd is not a directory\n\nNo session was started. Your input is kept.");
	expect(form.text()).not.toContain("may have started");
	expect(form.drafts.get("hub-1")).toMatchObject({ prompt: "go", unconfirmed: false });
	form.dispose();
});

it("holds Start, with one line that says why, when the hub refused the start as invalid (#3184)", async () => {
	const form = await mount({
		draft: { cwd: "/home/jesse/git/evener", prompt: "go" },
		refuseStart: new WireError("skill input is not supported", -32602),
	});
	await act(async () => form.header("headerRight").props.onPress());
	await settle();
	expect(form.header("headerRight").props.disabled).toBe(true);
	const why = "It may have started: check the Board before starting this draft again, or change the draft.";
	expect(form.text().split(why)).toHaveLength(2);
	expect(form.text()).toContain("skill input is not supported");
	expect(form.drafts.get("hub-1")).toMatchObject({ prompt: "go", unconfirmed: true });
	form.dispose();
});

it("stops being the store's form in front once it unmounts (#3104)", async () => {
	const form = await mount({ draft: { cwd: "/home/jesse/git/evener" } });
	expect(formFront(form.store)).toBeDefined();
	act(() => form.dispose());
	expect(formFront(form.store)).toBeUndefined();
});

it("says a changed draft's failed start couldn't start, not that it may have (#3104)", async () => {
	const form = await mount({
		draft: {
			cwd: "/home/jesse/git/evener",
			prompt: "go",
			unconfirmed: true,
			launchOverrides: { enabledPlugins: ["superpowers"] },
		},
		plugins: new Error("plugin cache locked"),
	});
	await debounce();
	// Changed, the draft is a new start: its earlier start's doubt isn't this one's.
	await act(async () => form.prompt().props.onChangeText("go, and fix the docs"));
	await act(async () => {
		void form.header("headerRight").props.onPress();
		form.focus.focused = false;
		form.dispose();
	});
	await settle();
	expect(form.alerts.getSnapshot().banner?.alerts).toEqual([
		{ kind: "startFailed", hubId: "hub-1", hubName: "magic-kingdom", uncertain: false },
	]);
});

it("opens Start again, the draft intact, after a hub switch cut off a start the hub never got (#3104)", async () => {
	const form = await mount({
		draft: { cwd: "/home/jesse/git/evener", prompt: "go", launchOverrides: { enabledPlugins: ["superpowers"] } },
		holdPreview: true,
	});
	await debounce();
	await act(async () => void form.header("headerRight").props.onPress());
	expect(form.header("headerRight").props).toMatchObject({ label: "Starting…", disabled: true });
	// Another hub is selected while the plugin check is out, then this one again.
	await act(async () => form.alerts.reset());
	await form.setReady(false);
	await form.setReady(true);
	await act(async () => form.releaseStart());
	await settle();
	expect(form.header("headerRight").props).toMatchObject({ label: "Start", disabled: false });
	expect(form.prompt().props.value).toBe("go");
	expect(form.store.getState()).toMatchObject({
		error: null,
		launchOverrides: { enabledPlugins: ["superpowers"] },
	});
	expect(form.drafts.get("hub-1")).toMatchObject({ prompt: "go", unconfirmed: false });
	expect(form.alerts.getSnapshot().banner).toBeNull();
	form.dispose();
});
