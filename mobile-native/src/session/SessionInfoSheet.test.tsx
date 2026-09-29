// The Session sheet route (spec 8.6), rendered with a host in
// sessionInfoHosts the way ConversationScreen provides one. The host's
// controls are a SessionControls-shaped fake whose calls are recorded; only
// native edges are mocked.
import type { ThreadCapabilities } from "@evener/appwire-client";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { act, type ReactTestInstance, type ReactTestRenderer } from "react-test-renderer";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { paletteFor } from "../design/tokens";
import type { MobileConversation } from "../projectedRows";
import { alertRequests, playedHaptics, pressable, render, renderedText, textOf } from "../renderNative.testkit";
import type { Routes } from "../screens";
import type { SessionControls } from "../sessionControls";
import { sheetKey } from "../sheet/sheetHosts";
import type { ToastMessage } from "../Toast";
import { type SessionInfoAction, type SessionInfoHost, SessionInfoSheet, sessionInfoHosts } from "./SessionInfoSheet";

const navigation = vi.hoisted(() => ({ goBack: vi.fn(), navigate: vi.fn(), dispatch: vi.fn() }));
const guard = vi.hoisted(() => ({
	prevented: false,
	onPrevent: null as null | ((options: { data: { action: unknown } }) => void),
}));

vi.mock("react-native", async () => (await import("../renderNative.testkit")).nativeModuleMock());
vi.mock("@react-navigation/native", () => ({
	useNavigation: () => navigation,
	usePreventRemove: (prevent: boolean, callback: (options: { data: { action: unknown } }) => void) => {
		guard.prevented = prevent;
		guard.onPrevent = callback;
	},
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));

const palette = paletteFor("light");
const HUB = "hub-1";
const REF = "local:AbCdEfGhIjKlMnOpQrStUv";

const NONE: ThreadCapabilities = {
	send: true,
	steer: false,
	interrupt: false,
	compact: false,
	clear: false,
	forkFromTurn: false,
	shutdown: false,
	changeModel: false,
	changeVisionModel: false,
	sharedNotes: false,
	queue: false,
	goal: false,
	rename: false,
} as ThreadCapabilities;

function conversation(over: Partial<MobileConversation> = {}): MobileConversation {
	return {
		ref: REF,
		threadId: "thread-1",
		name: "Fix the settle race",
		status: { type: "idle" },
		askPending: false,
		pendingEscalations: [],
		turns: [],
		items: [],
		capabilities: NONE,
		resumeRequired: false,
		goal: null,
		tasks: null,
		humanNote: "",
		agentNote: "",
		sessionUrls: [],
		modelProvider: "anthropic/claude-sonnet-5",
		reasoningEffort: "high",
		reasoningEffortLevels: ["low", "high"],
		supportsReasoning: true,
		visionModel: "",
		cwd: "/Users/jesse/git/evener",
		projectPath: "/Users/jesse/git/evener",
		gitBranch: "fix-settle",
		usage: null,
		cost: undefined,
		workMillis: 0,
		contextUsed: 0,
		contextWindow: 0,
		failedToolCalls: undefined,
		...over,
	} as unknown as MobileConversation;
}

type ControlsState = ReturnType<SessionControls["getSnapshot"]>;

function fakeControls(over: Partial<ControlsState> = {}) {
	const state: ControlsState = {
		pending: null,
		lastAction: null,
		error: null,
		notice: null,
		catalog: null,
		loadingModels: false,
		modelError: null,
		...over,
	};
	return {
		subscribe: () => () => {},
		getSnapshot: () => state,
		rename: vi.fn(async () => true),
	};
}

let owner: object | undefined;
const mounted: ReactTestRenderer[] = [];

function provide(session: MobileConversation, over: Partial<SessionInfoHost> = {}) {
	const controls = fakeControls();
	const calls: string[] = [];
	const host: SessionInfoHost = {
		session,
		controls: controls as unknown as SessionControls,
		hostLabel: (id) => (id === "local" ? "Work hub" : id),
		modelLabel: "Claude Sonnet 5 · High",
		runMs: () => null,
		ready: true,
		editGoal: vi.fn(() => void calls.push("editGoal")),
		clearGoal: vi.fn(() => void calls.push("clearGoal")),
		act: vi.fn(async (action: SessionInfoAction) => {
			calls.push(`act:${action}`);
			return action === "compact" ? { text: "Compacting context" } : { text: `${action} done` };
		}),
		toast: vi.fn((message: ToastMessage) => void calls.push(`toast:${message.text}`)),
		...over,
	};
	// One screen at a time: a test that provides twice replaces the first.
	if (owner) sessionInfoHosts.release(sheetKey(HUB, REF), owner);
	owner = {};
	sessionInfoHosts.provide(sheetKey(HUB, REF), owner, host);
	navigation.goBack.mockImplementation(() => void calls.push("goBack"));
	return { host, controls, calls };
}

/** The screen provides a new host, as it does when its session changes. */
function replaceHost(host: SessionInfoHost) {
	sessionInfoHosts.provide(sheetKey(HUB, REF), owner as object, host);
}

function sheet(): ReactTestRenderer {
	const props = {
		route: { key: "session-info", name: "SessionInfoSheet", params: { hubId: HUB, ref: REF } },
		navigation,
	} as unknown as NativeStackScreenProps<Routes, "SessionInfoSheet">;
	const tree = render(<SessionInfoSheet {...props} />);
	mounted.push(tree);
	return tree;
}

async function flush() {
	await act(async () => {
		await new Promise((resolve) => setTimeout(resolve, 0));
	});
}

function press(tree: ReactTestRenderer, label: string) {
	const target = pressable(tree, label);
	if (!target) throw new Error(`no ${label}`);
	act(() => target.props.onPress());
}

const texts = (tree: ReactTestRenderer) =>
	tree.root.findAll((node) => String(node.type) === "Text").map((node: ReactTestInstance) => textOf(node));

const symbols = (tree: ReactTestRenderer) =>
	tree.root.findAll((node) => String(node.type) === "SymbolView").map((node) => node.props.name);

function styleOf(node: ReactTestInstance) {
	return [node.props.style].flat().reduce((all, style) => ({ ...all, ...style }), {});
}

function textNode(tree: ReactTestRenderer, text: string): ReactTestInstance {
	const found = tree.root.findAll((node) => String(node.type) === "Text" && textOf(node) === text)[0];
	if (!found) throw new Error(`no text ${text}`);
	return found;
}

beforeEach(() => {
	navigation.goBack.mockReset();
	navigation.navigate.mockReset();
	navigation.dispatch.mockReset();
	guard.onPrevent = null;
	alertRequests.length = 0;
});

afterEach(() => {
	for (const tree of mounted.splice(0)) act(() => tree.unmount());
	if (owner) sessionInfoHosts.release(sheetKey(HUB, REF), owner);
	owner = undefined;
});

describe("what the sheet shows (spec 8.6)", () => {
	it("leads with the name and the state line, with no header title", () => {
		provide(conversation());
		const tree = sheet();
		expect(
			tree.root.findAll((node) => node.props.accessibilityRole === "header" && textOf(node) === "Session"),
		).toEqual([]);
		expect(styleOf(textNode(tree, "Fix the settle race"))).toMatchObject({ fontSize: 20, fontWeight: "600" });
		expect(renderedText(tree)).toContain("Finished");
	});

	it("says where the session runs", () => {
		provide(conversation());
		const tree = sheet();
		const shown = texts(tree);
		expect(shown).toEqual(expect.arrayContaining(["Where", "Work hub", "evener", "fix-settle"]));
		// The directory is Menlo, and may wrap only after a slash.
		const directory = textNode(tree, "/​Users/​jesse/​git/​evener");
		expect(styleOf(directory)).toMatchObject({ fontFamily: "Menlo" });
		expect(symbols(tree)).toEqual(expect.arrayContaining(["server.rack", "folder", "arrow.triangle.branch"]));
	});

	it("labels its sections 12pt semibold uppercase in ink-mid", () => {
		provide(conversation());
		const tree = sheet();
		expect(styleOf(textNode(tree, "Where"))).toMatchObject({
			fontSize: 12,
			fontWeight: "600",
			textTransform: "uppercase",
			color: palette.inkMid,
		});
	});

	it("draws its groups as the shared grouped list, inset with hairlines between rows", () => {
		provide(conversation());
		const tree = sheet();
		// Grouped.tsx's Group is an inset surface card with a hairline between
		// rows; the private Section this sheet used to carry drew a borderTop
		// on each following row instead, so this pins the sheet to the shared one.
		expect(tree.root.findAllByProps({ testID: "hairline" }).length).toBeGreaterThan(0);
		const surface = tree.root.findAll(
			(node) => String(node.type) === "View" && styleOf(node).backgroundColor === palette.surface,
		);
		expect(surface.length).toBeGreaterThan(0);
		expect(styleOf(surface[0])).toMatchObject({ marginHorizontal: 16, borderRadius: 12 });
	});

	it("names the model, and opens the model sheet over this one", () => {
		provide(conversation({ capabilities: { ...NONE, changeModel: true } }));
		const tree = sheet();
		expect(symbols(tree)).toContain("cpu");
		press(tree, "Model, Claude Sonnet 5 · High");
		expect(navigation.navigate).toHaveBeenCalledWith("ModelSheet", { hubId: HUB, ref: REF, setting: "model" });
	});

	it("opens the model sheet for Effort when the model itself can't change", () => {
		provide(conversation());
		press(sheet(), "Model, Claude Sonnet 5 · High");
		expect(navigation.navigate).toHaveBeenCalledWith("ModelSheet", { hubId: HUB, ref: REF, setting: "model" });
	});

	it("names the model without offering a change the session can't make", () => {
		provide(conversation({ reasoningEffortLevels: [], supportsReasoning: false }), { modelLabel: "Claude Sonnet 5" });
		const tree = sheet();
		expect(texts(tree)).toContain("Claude Sonnet 5");
		expect(pressable(tree, "Model, Claude Sonnet 5")).toBeUndefined();
	});

	it("offers the vision model only when the session can change it", () => {
		provide(conversation());
		expect(pressable(sheet(), "Vision model, Session model")).toBeUndefined();
		act(() => mounted.pop()?.unmount());
		provide(conversation({ capabilities: { ...NONE, changeVisionModel: true }, visionModel: "off" }));
		const tree = sheet();
		press(tree, "Vision model, Off");
		expect(navigation.navigate).toHaveBeenCalledWith("ModelSheet", { hubId: HUB, ref: REF, setting: "vision" });
	});

	it("lists the plugins chosen at start, and leaves them out when the inventory is unknown", () => {
		provide(conversation({ diagnostics: { plugins: [{ name: "superpowers" }, { name: "go" }] } }));
		const tree = sheet();
		expect(texts(tree)).toEqual(
			expect.arrayContaining([
				"2 plugins · chosen at start",
				"go",
				"superpowers",
				"Plugins are chosen when a session starts. To change them, start a new session or fork this one.",
			]),
		);
		act(() => mounted.pop()?.unmount());
		provide(conversation({ diagnostics: undefined }));
		expect(renderedText(sheet())).not.toContain("chosen at start");
	});

	it("shows the sandbox mode and the network, read-only, when the hub reports them (S15)", () => {
		provide(conversation({ access: { sandbox: "workspace-write", network: false } }));
		const tree = sheet();
		expect(texts(tree)).toEqual(expect.arrayContaining(["Access", "Sandbox", "Workspace write", "Network", "Off"]));
		expect(pressable(tree, "Sandbox, Workspace write")).toBeUndefined();
		// VoiceOver reads each fact row as one element.
		const row = tree.root.find(
			(node) => String(node.type) === "View" && node.props.accessibilityLabel === "Sandbox, Workspace write",
		);
		expect(row.props.accessible).toBe(true);
		act(() => mounted.pop()?.unmount());
		provide(conversation({ access: undefined }));
		expect(texts(sheet())).not.toContain("Access");
	});

	it("says what the session used, with failed tool calls in red and a context gauge", () => {
		provide(
			conversation({
				usage: {
					totalTokens: 46_000_000,
					inputTokens: 12_000_000,
					outputTokens: 1_200_000,
					cacheReadTokens: 33_000_000,
				},
				cost: "~$4.12",
				workMillis: 3 * 3_600_000,
				contextUsed: 50_000,
				contextWindow: 200_000,
				failedToolCalls: 3,
			}),
		);
		const tree = sheet();
		expect(texts(tree)).toEqual(
			expect.arrayContaining([
				"46M tokens",
				"12M in · 1.2M out · 33M cached",
				"~$4.12",
				"3h",
				"50K of 200K",
				"3 failed tool calls",
			]),
		);
		expect(styleOf(textNode(tree, "3 failed tool calls"))).toMatchObject({ color: palette.dangerInk });
		expect(styleOf(textNode(tree, "50K of 200K"))).toMatchObject({ fontVariant: ["tabular-nums"] });
		const labels = tree.root
			.findAll((node) => String(node.type) === "View" && node.props.accessible === true)
			.map((node) => node.props.accessibilityLabel);
		expect(labels).toEqual(
			expect.arrayContaining(["46M tokens, 12M in · 1.2M out · 33M cached", "Context, 50K of 200K"]),
		);
		const fill = tree.root.find((node) => node.props.testID === "context-fill");
		expect(styleOf(fill)).toMatchObject({ width: "25%", backgroundColor: palette.inkMid });
	});

	it("shows the current task and the task count, which opens the Tasks sheet over this one", () => {
		provide(conversation({ tasks: { total: 7, done: 3, current: { id: 4, description: "Wire the chip" } } }));
		const tree = sheet();
		expect(texts(tree)).toContain("Wire the chip");
		press(tree, "Tasks · 3 of 7");
		expect(navigation.navigate).toHaveBeenCalledWith("TasksSheet", {
			hubId: HUB,
			ref: REF,
			threadId: "thread-1",
			hasTasks: true,
		});
	});

	it("sums up Notes & links, which opens that sheet over this one", () => {
		provide(
			conversation({
				capabilities: { ...NONE, sharedNotes: true },
				humanNote: "keep it",
				sessionUrls: [{ id: "u1", url: "https://example.com" }],
			}),
		);
		const tree = sheet();
		press(tree, "Notes & links, Your note · 1 link");
		expect(navigation.navigate).toHaveBeenCalledWith("NotesSheet", { hubId: HUB, ref: REF });
		act(() => mounted.pop()?.unmount());
		provide(conversation());
		expect(renderedText(sheet())).not.toContain("Notes & links");
	});

	it("never speaks of runtimes, refreshing or reconnecting, even when the controls leave a notice", () => {
		const everything = { ...NONE, rename: true, compact: true, shutdown: true, goal: true, forkFromTurn: true };
		const { host } = provide(conversation({ capabilities: everything, status: { type: "restartRequired" } }));
		host.controls = fakeControls({
			notice: "Runtime stopped. Saved history is available to resume.",
		}) as unknown as SessionControls;
		replaceHost({ ...host });
		expect(renderedText(sheet())).not.toMatch(/Runtime|Refresh|Reconnect/);
	});

	it("shows what went wrong as one line under the actions", () => {
		const { host } = provide(conversation({ capabilities: { ...NONE, compact: true } }));
		replaceHost({
			...host,
			controls: fakeControls({
				lastAction: "compact",
				error: "Could not confirm the action: refused",
			}) as unknown as SessionControls,
		});
		expect(renderedText(sheet())).toContain("Could not confirm the action: refused");
	});

	it("announces a failed action to VoiceOver, not just shows it", () => {
		const { host } = provide(conversation({ capabilities: { ...NONE, compact: true } }));
		replaceHost({
			...host,
			controls: fakeControls({
				lastAction: "compact",
				error: "Could not confirm the action: refused",
			}) as unknown as SessionControls,
		});
		const tree = sheet();
		expect(tree.root.findAll((node) => node.props.accessibilityRole === "alert")).toHaveLength(1);
	});

	it("stays open while the hub is away, showing what it knows with its actions held", () => {
		const { host } = provide(conversation({ capabilities: { ...NONE, compact: true } }));
		replaceHost({ ...host, controls: null, ready: false });
		const tree = sheet();
		expect(navigation.goBack).not.toHaveBeenCalled();
		expect(texts(tree)).toContain("Work hub");
		expect(pressable(tree, "Compact context")?.props.disabled).toBe(true);
	});

	it("leaves when its session's screen is gone", () => {
		provide(conversation());
		sheet();
		act(() => sessionInfoHosts.release(sheetKey(HUB, REF), owner as object));
		expect(navigation.goBack).toHaveBeenCalled();
	});
});

describe("renaming", () => {
	it("edits the name in place and saves it with Done on the keyboard", async () => {
		const { controls } = provide(conversation({ capabilities: { ...NONE, rename: true } }));
		const tree = sheet();
		press(tree, "Fix the settle race, rename");
		const field = tree.root.find((node) => String(node.type) === "TextInput");
		expect(field.props.value).toBe("Fix the settle race");
		act(() => field.props.onChangeText("Settle race"));
		await act(async () => field.props.onSubmitEditing());
		expect(controls.rename).toHaveBeenCalledWith("Settle race");
		expect(renderedText(tree)).toContain("Session renamed");
	});

	it("holds the sheet while the field has a new name, and asks before discarding it", () => {
		provide(conversation({ capabilities: { ...NONE, rename: true } }));
		const tree = sheet();
		expect(guard.prevented).toBe(false);
		press(tree, "Fix the settle race, rename");
		act(() => tree.root.find((node) => String(node.type) === "TextInput").props.onChangeText("Settle race"));
		expect(guard.prevented).toBe(true);
		act(() => guard.onPrevent?.({ data: { action: { type: "POP" } } }));
		expect(alertRequests.map((request) => request.title)).toEqual(["Discard the new name?"]);
	});

	it("can't rename without the capability", () => {
		provide(conversation());
		const tree = sheet();
		expect(pressable(tree, "Fix the settle race, rename")).toBeUndefined();
		expect(tree.root.findAll((node) => String(node.type) === "TextInput")).toEqual([]);
	});
});

describe("the goal", () => {
	it("shows the objective in the serif, and a blocked status in amber", () => {
		provide(
			conversation({
				capabilities: { ...NONE, goal: true },
				goal: { objective: "Ship the sheet", status: "blocked", iterations: 2 },
			}),
		);
		const tree = sheet();
		expect(styleOf(textNode(tree, "Ship the sheet"))).toMatchObject({ fontFamily: "SourceSerif4-Regular" });
		expect(styleOf(textNode(tree, "Blocked"))).toMatchObject({ color: palette.attentionInk });
	});

	it("closes the sheet before handing Edit goal to the composer", () => {
		const { calls } = provide(
			conversation({
				capabilities: { ...NONE, goal: true },
				goal: { objective: "Ship the sheet", status: "active", iterations: 2 },
			}),
		);
		const tree = sheet();
		expect(texts(tree)).toContain("Active");
		press(tree, "Edit goal");
		expect(calls).toEqual(["goBack", "editGoal"]);
	});

	it("clears the goal in place", () => {
		const { host } = provide(
			conversation({
				capabilities: { ...NONE, goal: true },
				goal: { objective: "Ship the sheet", status: "complete", iterations: 2 },
			}),
		);
		const tree = sheet();
		press(tree, "Clear goal");
		expect(host.clearGoal).toHaveBeenCalledOnce();
		expect(navigation.goBack).not.toHaveBeenCalled();
	});

	it("offers Set a goal without one, the same flow as Edit goal", () => {
		const { calls } = provide(conversation({ capabilities: { ...NONE, goal: true } }));
		const tree = sheet();
		press(tree, "Set a goal");
		expect(calls).toEqual(["goBack", "editGoal"]);
		act(() => mounted.pop()?.unmount());
		provide(conversation());
		expect(pressable(sheet(), "Set a goal")).toBeUndefined();
	});
});

describe("actions", () => {
	const labels = (tree: ReactTestRenderer) =>
		["Aside", "Fork from latest", "Compact context", "Pin to category…", "Archive", "Shut down", "Delete"].filter(
			(label) => pressable(tree, label) !== undefined,
		);
	const forkable = [{ kind: "user", id: "u", text: "run it", transcriptEntryIndex: 3 }];

	it("offers each action only when it can act", () => {
		provide(conversation());
		expect(labels(sheet())).toEqual(["Pin to category…", "Archive"]);
		act(() => mounted.pop()?.unmount());

		provide(conversation({ capabilities: { ...NONE, forkFromTurn: true, compact: true, shutdown: true } }));
		expect(labels(sheet())).toEqual(["Aside", "Compact context", "Pin to category…", "Archive", "Shut down"]);
		act(() => mounted.pop()?.unmount());

		provide(
			conversation({
				capabilities: { ...NONE, forkFromTurn: true },
				items: forkable as MobileConversation["items"],
			}),
		);
		expect(labels(sheet())).toEqual(["Aside", "Fork from latest", "Pin to category…", "Archive"]);
		act(() => mounted.pop()?.unmount());

		// Shut down already: no Shut down, even when the hub still reports the
		// capability, and Delete for a saved local session.
		provide(conversation({ capabilities: { ...NONE, shutdown: true }, status: { type: "notLoaded" } }));
		expect(labels(sheet())).toEqual(["Pin to category…", "Archive", "Delete"]);
		act(() => mounted.pop()?.unmount());

		// A remote session's saved copy isn't this hub's to delete.
		provide(conversation({ ref: "paradise-park:s2", status: { type: "notLoaded" } }));
		expect(labels(sheet())).toEqual(["Pin to category…", "Archive"]);
	});

	it.each(["closed", "ended"])(
		"offers no Shut down on a %s session, even when the hub still reports the capability",
		(status) => {
			provide(
				conversation({
					capabilities: { ...NONE, shutdown: true },
					status: { type: status } as MobileConversation["status"],
				}),
			);
			expect(pressable(sheet(), "Shut down")).toBeUndefined();
		},
	);

	it("times a subagent by its run, as the nav bar and its row do", () => {
		// Its own turn started two minutes ago (a steer, say), but it has run four.
		provide(
			conversation({
				status: { type: "active", activeFlags: [] } as MobileConversation["status"],
				activeTurnStartedAt: new Date(Date.now() - 2 * 60_000).toISOString(),
			}),
			{ runMs: () => 4 * 60_000 },
		);
		const text = renderedText(sheet());
		expect(text).toContain("Working · 4m");
		expect(text).not.toContain("Working · 2m");
	});

	it("draws Shut down and Delete as destructive", () => {
		provide(conversation({ capabilities: { ...NONE, shutdown: true } }));
		const tree = sheet();
		const shutDown = pressable(tree, "Shut down");
		if (!shutDown) throw new Error("no Shut down");
		expect(styleOf(shutDown.findByType("Text" as never))).toMatchObject({ color: palette.dangerInk });
	});

	it("asks before shutting down, then closes the sheet before shutting down, and the session says so", async () => {
		const { calls } = provide(conversation({ capabilities: { ...NONE, shutdown: true } }));
		const tree = sheet();
		press(tree, "Shut down");
		const [confirm] = alertRequests;
		expect(confirm).toMatchObject({
			title: "Shut down this session?",
			message: "It stops now and keeps its history. Sending a message resumes it.",
		});
		expect(calls).toEqual([]);
		playedHaptics.length = 0;
		act(() => confirm?.buttons?.[1]?.onPress?.());
		// Spec 16.6: rigid on a destructive confirmation.
		expect(playedHaptics).toEqual(["impact:rigid"]);
		await flush();
		expect(calls).toEqual(["goBack", "act:shutDown", "toast:shutDown done"]);
	});

	it("compacts in place, and shows its toast in the sheet", async () => {
		const { calls } = provide(conversation({ capabilities: { ...NONE, compact: true } }));
		const tree = sheet();
		press(tree, "Compact context");
		await flush();
		expect(calls).toEqual(["act:compact"]);
		expect(renderedText(tree)).toContain("Compacting context");
	});

	it.each([
		["Aside", "aside", { forkFromTurn: true }],
		["Pin to category…", "pin", {}],
		["Archive", "archive", {}],
	] as const)(
		"closes the sheet before %s, and leaves its toast to the session",
		async (label, action, capabilities) => {
			const { calls } = provide(conversation({ capabilities: { ...NONE, ...capabilities } }));
			const tree = sheet();
			press(tree, label);
			await flush();
			expect(calls).toEqual(["goBack", `act:${action}`, `toast:${action} done`]);
		},
	);

	it("forks from the latest message after closing the sheet", async () => {
		const { calls } = provide(
			conversation({
				capabilities: { ...NONE, forkFromTurn: true },
				items: forkable as MobileConversation["items"],
			}),
		);
		press(sheet(), "Fork from latest");
		await flush();
		expect(calls.slice(0, 2)).toEqual(["goBack", "act:fork"]);
	});

	it("holds every action but Pin while the session can't take one", () => {
		const { host } = provide(conversation({ capabilities: { ...NONE, compact: true, shutdown: true } }));
		replaceHost({ ...host, ready: false });
		const tree = sheet();
		expect(pressable(tree, "Compact context")?.props.disabled).toBe(true);
		expect(pressable(tree, "Shut down")?.props.disabled).toBe(true);
		expect(pressable(tree, "Archive")?.props.disabled).toBe(true);
		expect(pressable(tree, "Pin to category…")?.props.disabled).toBe(false);
	});
});
