import { installActivityFixture } from "./sessionActivityTestUtils";
import { activityChangedNotification } from "@evener/appwire-client/testing/notifications";
// The Activity list (spec 9): a coordinator's subagents and shell jobs, read
// through typed activity reads, with live work and independent quiet histories,
// the strip, the chips, search, and each row's why and last line.
import type { NativeStackNavigationOptions } from "@react-navigation/native-stack";
import { WireError } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import type { ReactElement } from "react";
import { FlatList } from "react-native";
import { act, type ReactTestRenderer } from "react-test-renderer";
import { beforeEach, expect, it, vi } from "vitest";
import { playedHaptics, pressable, render, renderedText, screenConnection, settle } from "../renderNative.testkit";
import { Toast } from "../Toast";
import { forgetStopRequestsForHub, stopRequests } from "./nativeStopRequests";
import { flattenSubagents } from "./subagentModel";
import { forgetSubagentTrees } from "./subagentTree";
import { SubagentsScreen } from "./SubagentsScreen";
import { paletteFor } from "../design/tokens";
import { stopOffer } from "./stopOffer";

const harness = vi.hoisted(() => ({ connection: {} as Record<string, unknown>, kv: new Map<string, string>() }));

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
	TextInput: "TextInput",
	useWindowDimensions: () => ({ width: 393, height: 852, fontScale: 1, scale: 3 }),
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));
vi.mock("@react-navigation/native", async () => {
	const { useEffect } = await import("react");
	return {
		useFocusEffect: (effect: () => void | (() => void)) => useEffect(effect, [effect]),
		useIsFocused: () => true,
	};
});
vi.mock("../ConnectionProvider", () => ({ useConnection: () => harness.connection }));
vi.mock("expo-sqlite/kv-store", () => ({
	Storage: {
		getItemSync: (key: string) => harness.kv.get(key) ?? null,
		setItemSync: (key: string, value: string) => void harness.kv.set(key, value),
		removeItemSync: (key: string) => void harness.kv.delete(key),
	},
}));

const NOW = Date.now();
const MIN = 60_000;
const ago = (ms: number) => new Date(NOW - ms).toISOString();
const COORDINATOR = { ref: "local:coord", threadId: "coord", title: "Get PR 2138 Test Clean" };

const session = (ref: string, label: string, entries: unknown[], branch: Record<string, unknown> = {}) => ({
	kind: "session",
	sessionId: ref.slice(ref.indexOf(":") + 1),
	ref,
	label,
	aggregate: "working",
	counts: { active: 0, failed: 0, completed: 0, complete: true },
	entries,
	branch,
});
const delegate = (id: string, description: string, over: Record<string, unknown> = {}) => ({
	kind: "delegate",
	delegate: {
		runGeneration: 1,
		delegateId: id,
		childSessionId: id,
		childRef: `local:${id}`,
		type: "delegate",
		description,
		branch: {},
		...over,
	},
});
const runningOne = (id: string, description: string, over: Record<string, unknown> = {}) =>
	delegate(id, description, { runStartedAt: ago(4 * MIN), ...over });
const failedOne = (id: string, description: string, over: Record<string, unknown> = {}) =>
	delegate(id, description, {
		terminal: true,
		outcome: "failed",
		runStartedAt: ago(20 * MIN),
		runEndedAt: ago(6 * MIN),
		...over,
	});
const doneOne = (id: string, description: string) =>
	delegate(id, description, {
		terminal: true,
		outcome: "completed",
		runStartedAt: ago(30 * MIN),
		runEndedAt: ago(10 * MIN),
	});

/** The spec's coordinator: 2 failed (one with a running child), 32 running
 * and 21 done, 55 in all. */
function specTree() {
	const race = failedOne("race", "Fix race in tree settle", {
		reason: "go test exited 1 (3 times)",
		usage: { inputTokens: 1_000_000, outputTokens: 200_000, totalTokens: 1_200_000 },
		child: session("local:race", "Fix race in tree settle", [runningOne("repro", "Reproduce the race")]),
	});
	const running = Array.from({ length: 31 }, (_, index) =>
		runningOne(`run-${index}`, index === 0 ? "Port the drain to the new lock" : `Running task ${index}`, {
			...(index === 0
				? {
						model: "gpt-5",
						worktree: { path: "/w", branch: "fix/drain", headSha: "abc", ahead: 1, dirty: false },
						usage: { inputTokens: 300_000, outputTokens: 10_000, totalTokens: 310_000 },
					}
				: {}),
		}),
	);
	const done = Array.from({ length: 21 }, (_, index) => doneOne(`done-${index}`, `Finished task ${index}`));
	return {
		revision: 1,
		root: session("local:coord", COORDINATOR.title, [race, failedOne("lint", "Lint the tree"), ...running, ...done]),
	};
}

let client: FakeClient;
let navigation: {
	navigate: ReturnType<typeof vi.fn>;
	push: ReturnType<typeof vi.fn>;
	setOptions: ReturnType<typeof vi.fn>;
	options: NativeStackNavigationOptions[];
};

function hub(pages: (continuation: string | undefined) => unknown) {
	const next = new FakeClient("ready");
	installActivityFixture(next, pages);
	next.on(
		"thread/read",
		() => ({ thread: { id: "coord", modelProvider: "anthropic/claude-opus", status: { type: "active" } } }) as never,
	);
	next.on("model/list", () => ({ data: [{ model: "gpt-5", displayName: "GPT-5" }] }) as never);
	return next;
}

beforeEach(() => {
	forgetSubagentTrees("hub-1");
	forgetStopRequestsForHub("hub-1");
	client = hub(() => specTree());
	harness.connection = screenConnection(client, "ready");
	const options: NativeStackNavigationOptions[] = [];
	navigation = { navigate: vi.fn(), push: vi.fn(), setOptions: vi.fn((next) => options.push(next)), options };
});

// The activity store re-reads what a notification names at once.
async function treeUpdatedAndRead(hubClient: FakeClient) {
	act(() => hubClient.emitNotification(activityChangedNotification(COORDINATOR)));
	await settle();
}

async function mount(flush = true) {
	const params = { hubId: "hub-1", ...COORDINATOR };
	const tree = render(
		<SubagentsScreen
			route={{ key: "subagents", name: "Subagents", params } as never}
			navigation={navigation as never}
		/>,
	);
	if (flush) await settle();
	return tree;
}

function headerTitle(): string {
	const title = navigation.options.findLast((options) => options.headerTitle)?.headerTitle;
	if (typeof title !== "function") throw new Error("no header title");
	return renderedText(render(title({ children: "", tintColor: "" }) as ReactElement));
}

/** The list's text, in order: section headers, rows' titles, folds. */
const text = (tree: ReactTestRenderer) => renderedText(tree);

it("titles itself with the count over the coordinator's title", async () => {
	await mount();
	expect(headerTitle()).toBe("Activity · 55 Get PR 2138 Test Clean");
});

it("folds every terminal delegate below live work with terminal-inclusive counts", async () => {
	const tree = await mount();
	const shown = text(tree);
	const running = shown.indexOf("RUNNING · 32");
	const done = shown.indexOf("Done · 23");
	expect(running).toBeGreaterThanOrEqual(0);
	expect(shown).not.toContain("FAILED");
	expect(shown).not.toContain("Fix race in tree settle, Failed");
	expect(done).toBeGreaterThan(running);
	expect(shown).not.toContain("Finished task 0");
	for (const label of ["All, 55", "Running, 32", "Done, 23"]) expect(pressable(tree, label)).toBeDefined();
	expect(tree.root.find((node) => node.props.accessibilityLabel === "32 running, 23 done")).toBeDefined();
	expect(pressable(tree, "Failed, 2")).toBeUndefined();
});

it("keeps a terminal parent's running child and stop identity reachable with history closed", async () => {
	const tree = await mount();
	const child = tree.root.find((node) => node.props.row?.id === "repro").props.row;
	expect(child).toMatchObject({
		id: "repro",
		ref: "local:repro",
		state: "running",
		active: true,
		parentTitle: "Fix race in tree settle",
	});
	expect(stopOffer({ row: child, requested: false, direct: "yes" })).toBe("stop");
	act(() => pressable(tree, "Reproduce the race, Running, Working, 4 minutes")?.props.onPress());
	expect(navigation.push).toHaveBeenCalledWith("Subagent", {
		hubId: "hub-1",
		ref: "local:repro",
		title: "Reproduce the race",
		coordinator: COORDINATOR,
	});
	act(() => pressable(tree, "Done · 23")?.props.onPress());
	const parent = tree.root.find((node) => node.props.row?.id === "race").props.row;
	expect(parent).toMatchObject({
		state: "failed",
		active: true,
		delegate: { outcome: "failed", reason: "go test exited 1 (3 times)", runGeneration: 1 },
	});
	expect(stopOffer({ row: parent, requested: false, direct: "no" })).toBe("ask");
	const marks = tree.root.findAll(
		(node) => String(node.type) === "SymbolView" && node.props.name === "xmark.octagon.fill",
	);
	expect(marks).toHaveLength(2);
	expect(marks.every((node) => node.props.tintColor === paletteFor("light").inkLow)).toBe(true);
	const failedWords = tree.root.findAll((node) => String(node.type) === "Text" && node.props.children === "Failed");
	expect(failedWords).toHaveLength(2);
	expect(failedWords.every((node) => node.props.style.color === paletteFor("light").inkMid)).toBe(true);
});

it("lets a drag of the list put the filter's keyboard away", async () => {
	const tree = await mount();
	expect(tree.root.findByProps({ accessibilityLabel: "Filter activity" })).toBeDefined();
	expect(tree.root.findByType(FlatList).props.keyboardDismissMode).toBe("on-drag");
});

// Spec 9 draws the filter chips as one row, as the Board's and the Session's
// chip rows are: with 55 subagents they overflow a phone's width, so they
// scroll sideways in the shared strip, which fades at its trailing edge,
// instead of wrapping "Done" onto a row of its own (audit N10).
it("keeps its filter chips on one row that scrolls sideways", async () => {
	const tree = await mount();
	const strip = tree.root.findByProps({ testID: "subagent-filters" });
	const row = strip.findByType("ScrollView" as never);
	expect(row.props.horizontal).toBe(true);
	for (const label of ["All, 55", "Running, 32", "Done, 23"])
		expect(row.find((node) => node.props.accessibilityLabel === label)).toBeDefined();
});

it("filters to a chip's state, and offers no chip for a state with no subagents", async () => {
	const tree = await mount();
	playedHaptics.length = 0;
	act(() => pressable(tree, "Done, 23")?.props.onPress());
	expect(pressable(tree, "Done, 23")?.props.accessibilityState).toMatchObject({ selected: true });
	// Spec 16.6: a selection tick on a chip.
	expect(playedHaptics).toEqual(["selection"]);
	const shown = text(tree);
	expect(shown).toContain("Fix race in tree settle");
	expect(shown).not.toContain("Running task 1");
	expect(shown).not.toContain("RUNNING");

	client = hub(() => ({
		revision: 1,
		root: session("local:coord", COORDINATOR.title, [runningOne("solo", "Only one")]),
	}));
	harness.connection = screenConnection(client, "ready");
	act(() => forgetSubagentTrees("hub-1"));
	const other = await mount();
	expect(pressable(other, "Failed, 0")).toBeUndefined();
	expect(pressable(other, "Running, 1")).toBeDefined();
});

// Shell jobs sit among the subagents (Jesse's ruling on shell jobs): each
// in its state's section, counted by the title and the filter chips, while
// the strip stays the subagents' own.
const shellJob = (id: string, description: string, over: Record<string, unknown> = {}) => ({
	kind: "shell",
	job: {
		jobId: id,
		ownerSessionId: "coord",
		ownerRef: "local:coord",
		type: "shell",
		status: "running",
		terminal: false,
		background: true,
		hasOutput: true,
		description,
		command: description,
		startedAt: ago(3 * MIN),
		outputBytes: 0,
		...over,
	},
});
function treeWithJobs() {
	return {
		revision: 1,
		root: session("local:coord", COORDINATOR.title, [
			shellJob("j-lint", "npm run lint", {
				status: "command_exited_nonzero",
				outcome: "failure",
				terminal: true,
				exitCode: 1,
				endedAt: ago(MIN),
			}),
			runningOne("solo", "Only one", {
				child: session("local:solo", "Only one", [
					shellJob("j-docs", "Serving the docs", { ownerRef: "local:solo", ownerSessionId: "solo" }),
				]),
			}),
		]),
	};
}

it("keeps terminal shell jobs in their own fold with truthful output access and inclusive chip counts", async () => {
	client = hub(() => treeWithJobs());
	harness.connection = screenConnection(client, "ready");
	const tree = await mount();
	expect(headerTitle()).toBe("Activity · 3 Get PR 2138 Test Clean");
	for (const label of ["All, 3", "Running, 2", "Done, 1"]) expect(pressable(tree, label)).toBeDefined();
	expect(tree.root.find((node) => node.props.accessibilityLabel === "1 running, 0 done")).toBeDefined();
	expect(text(tree)).not.toContain("npm run lint");
	expect(pressable(tree, "Completed · 1")?.props.accessibilityState).toEqual({ expanded: false });
	act(() => pressable(tree, "Completed · 1")?.props.onPress());
	const shown = text(tree);
	expect(shown.indexOf("Completed · 1")).toBeLessThan(shown.indexOf("npm run lint"));
	expect(shown.indexOf("RUNNING · 2")).toBeLessThan(shown.indexOf("Serving the docs"));
	const labels = tree.root.findAll((node) => String(node.props.accessibilityLabel).startsWith("Shell job,"));
	expect(new Set(labels.map((node) => node.props.accessibilityLabel))).toEqual(
		new Set([
			`Shell job, npm run lint, Command failed, 2 minutes, under ${COORDINATOR.title}`,
			"Shell job, Serving the docs, running, 3 minutes, under Only one",
		]),
	);

	act(() => pressable(tree, "Done, 1")?.props.onPress());
	expect(text(tree)).toContain("npm run lint");
	expect(text(tree)).not.toContain("Serving the docs");
});

// A job whose subagent's row is on a later page sits at the top of the tree
// until that page loads. It says its subagent isn't listed rather than
// naming the coordinator, then names the subagent once its row arrives.
it("says a job's subagent isn't listed until that subagent's page loads, then names it", async () => {
	client = hub((continuation) =>
		continuation === "page-2"
			? { revision: 1, root: session("local:coord", COORDINATOR.title, [runningOne("later", "Later one")]) }
			: {
					revision: 1,
					root: session(
						"local:coord",
						COORDINATOR.title,
						[
							runningOne("first", "First one"),
							shellJob("j-later", "Serving the docs", { ownerRef: "local:later", ownerSessionId: "later" }),
						],
						{ truncated: true, continuation: "page-2" },
					),
				},
	);
	harness.connection = screenConnection(client, "ready");
	const screen = await mount();
	const jobLabel = () =>
		screen.root.find((node) => String(node.props.accessibilityLabel).startsWith("Shell job, Serving the docs")).props
			.accessibilityLabel;
	expect(jobLabel()).toBe("Shell job, Serving the docs, running, 3 minutes, under a subagent that isn't listed");
	expect(text(screen)).toContain("under a subagent that isn't listed");
	expect(text(screen)).not.toContain(`under ${COORDINATOR.title}`);
	await act(async () => {
		screen.root.findByType("FlatList" as never).props.onEndReached({ distanceFromEnd: 0 });
	});
	await settle();
	expect(jobLabel()).toBe("Shell job, Serving the docs, running, 3 minutes, under Later one");
});

// The Session's menu offers Activity whenever it's connected, so a session
// with nothing to list says so plainly.
it("says there is nothing yet when the session has no subagents or shell jobs", async () => {
	client = hub(() => ({ revision: 1, root: session("local:coord", COORDINATOR.title, []) }));
	harness.connection = screenConnection(client, "ready");
	const tree = await mount();
	expect(text(tree)).toContain("No subagents or shell jobs yet.");
	expect(headerTitle()).toBe("Activity · 0 Get PR 2138 Test Clean");
});

// A shell job's detail is its own screen over the list, reading the job
// from the same tree.
it("opens a shell job's detail over the list", async () => {
	client = hub(() => treeWithJobs());
	harness.connection = screenConnection(client, "ready");
	const tree = await mount();
	act(() => pressable(tree, "Completed · 1")?.props.onPress());
	act(() =>
		pressable(tree, `Shell job, npm run lint, Command failed, 2 minutes, under ${COORDINATOR.title}`)?.props.onPress(),
	);
	expect(navigation.push).toHaveBeenCalledWith("ShellJob", {
		hubId: "hub-1",
		jobId: "j-lint",
		ownerRef: "local:coord",
		title: "npm run lint",
		coordinator: COORDINATOR,
	});
});

it("opens the done fold in place", async () => {
	const tree = await mount();
	act(() => pressable(tree, "Done · 23")?.props.onPress());
	expect(text(tree)).toContain("Finished task 0");
});

it("opens delegate and job histories independently, then Done reveals both", async () => {
	client = hub(() => {
		const whole = specTree();
		return { ...whole, root: { ...whole.root, entries: [...whole.root.entries, ...treeWithJobs().root.entries] } };
	});
	harness.connection = screenConnection(client, "ready");
	const tree = await mount();
	for (const label of ["Done · 23", "Completed · 1"])
		expect(pressable(tree, label)?.props.accessibilityState).toEqual({ expanded: false });
	act(() => pressable(tree, "Completed · 1")?.props.onPress());
	expect(text(tree)).toContain("npm run lint");
	expect(text(tree)).not.toContain("Finished task 0");
	act(() => pressable(tree, "Done · 23")?.props.onPress());
	expect(text(tree)).toContain("Finished task 0");
	act(() => pressable(tree, "Completed · 1")?.props.onPress());
	expect(text(tree)).not.toContain("npm run lint");
	expect(text(tree)).toContain("Finished task 0");
	act(() => pressable(tree, "Done, 24")?.props.onPress());
	expect(text(tree)).toContain("npm run lint");
	expect(text(tree)).toContain("Finished task 0");
	expect(text(tree)).not.toContain("Reproduce the race");
	expect(pressable(tree, "Done · 23")).toBeUndefined();
	expect(pressable(tree, "Completed · 1")).toBeUndefined();
});

it("searches past eight subagents, filtering rows and section counts while the chips keep the whole", async () => {
	const tree = await mount();
	const field = tree.root.find((node) => String(node.type) === "TextInput");
	expect(field.props.placeholder).toBe("Filter activity");
	act(() => field.props.onChangeText("race"));
	const shown = text(tree);
	expect(shown).toContain("Done · 1");
	expect(shown).not.toContain("go test exited 1");
	act(() => pressable(tree, "Done · 1")?.props.onPress());
	expect(text(tree)).toContain("Fix race in tree settle");
	expect(shown).toContain("RUNNING · 1");
	expect(shown).toContain("Reproduce the race");
	expect(shown).not.toContain("Lint the tree");
	expect(pressable(tree, "All, 55")).toBeDefined();
	expect(pressable(tree, "Clear filter")).toBeDefined();
});

it("reads a quiet failure, a nested subagent, another model, a branch and a finished run's tokens on their rows", async () => {
	const tree = await mount();
	act(() => pressable(tree, "Done · 23")?.props.onPress());
	const shown = text(tree);
	expect(shown).toContain("go test exited 1 (3 times)");
	expect(shown).toContain("from Fix race in tree settle");
	expect(shown).toContain("GPT-5");
	expect(shown).toContain("fix/drain");
	expect(shown).toContain("1.2M tokens");
	// A running subagent's usage is an earlier run's, so its row shows none.
	expect(shown).not.toContain("310K tokens");
	expect(pressable(tree, "Fix race in tree settle, Failed, go test exited 1 (3 times), 6 minutes")).toBeDefined();
	expect(shown).toContain("6m");
});

// The list runs no clock, but a running subagent's row still turns Quiet
// when its silence crosses the threshold, with no new tree to re-render it.
it("turns a silent subagent's row Quiet on its own", async () => {
	// On fake timers, so the first read can take as long as it takes without
	// crossing the threshold before the list shows the row. It flushes the
	// read itself: settle() waits on a setTimeout that never fires
	// while setTimeout is faked.
	vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "Date"] });
	try {
		const at = (ms: number) => new Date(Date.now() - ms).toISOString();
		client = hub(() => ({
			revision: 1,
			root: session("local:coord", COORDINATOR.title, [
				runningOne("hush", "Wait for the build", { latestActivityAt: at(19_900) }),
			]),
		}));
		harness.connection = screenConnection(client, "ready");
		const tree = await mount(false);
		await act(async () => {
			await vi.advanceTimersByTimeAsync(0);
		});
		expect(text(tree)).toContain("Working");
		await act(async () => {
			await vi.advanceTimersByTimeAsync(99);
		});
		expect(text(tree)).toContain("Working");
		await act(async () => {
			await vi.advanceTimersByTimeAsync(1);
		});
		expect(text(tree)).toContain("Quiet 20s");
	} finally {
		vi.useRealTimers();
	}
});

// A subagent's screen is its session (ruling 30), over this list.
it("opens a subagent's own session over its coordinator", async () => {
	const tree = await mount();
	act(() => pressable(tree, "Done · 23")?.props.onPress());
	act(() => pressable(tree, "Fix race in tree settle, Failed, go test exited 1 (3 times), 6 minutes")?.props.onPress());
	expect(navigation.push).toHaveBeenCalledWith("Subagent", {
		hubId: "hub-1",
		ref: "local:race",
		title: "Fix race in tree settle",
		coordinator: COORDINATOR,
	});
});

it("follows the coordinator when it comes into focus", async () => {
	await mount();
	expect(client.calls.find((call) => call.method === "thread/read")?.params).toMatchObject({
		ref: "local:coord",
		subscribe: true,
		replaceSubscription: false,
	});
});

it("follows the coordinator again when the connection comes back while it's in front", async () => {
	const tree = await mount();
	const follows = () =>
		client.calls.filter(
			(call) => call.method === "thread/read" && (call.params as { subscribe?: boolean }).subscribe === true,
		).length;
	expect(follows()).toBe(1);
	const again = () =>
		act(() =>
			tree.update(
				<SubagentsScreen
					route={{ key: "subagents", name: "Subagents", params: { hubId: "hub-1", ...COORDINATOR } } as never}
					navigation={navigation as never}
				/>,
			),
		);
	harness.connection = screenConnection(client, "reconnecting");
	again();
	await settle();
	harness.connection = screenConnection(client, "ready");
	again();
	await settle();
	expect(follows()).toBe(2);
});

it("follows the coordinator on a new client when the connection changes under it without leaving ready", async () => {
	const tree = await mount();
	const follows = (on: FakeClient) =>
		on.calls.filter(
			(call) => call.method === "thread/read" && (call.params as { subscribe?: boolean }).subscribe === true,
		).length;
	const next = hub(() => specTree());
	harness.connection = screenConnection(next, "ready");
	act(() =>
		tree.update(
			<SubagentsScreen
				route={{ key: "subagents", name: "Subagents", params: { hubId: "hub-1", ...COORDINATOR } } as never}
				navigation={navigation as never}
			/>,
		),
	);
	await settle();
	expect(follows(next)).toBe(1);
});

it("shows three quiet rows until the first read answers, and never offers Retry, Refresh or Reconnect", async () => {
	let answer: (value: unknown) => void = () => {};
	client = new FakeClient("ready");
	installActivityFixture(
		client,
		() =>
			new Promise((resolve) => {
				answer = resolve;
			}),
	);
	client.on("thread/read", () => ({ thread: { id: "coord", modelProvider: "", status: { type: "active" } } }) as never);
	client.on("model/list", () => ({ data: [] }) as never);
	harness.connection = screenConnection(client, "ready");
	const tree = await mount();
	expect(tree.root.findAll((node) => node.props.testID === "subagent-skeleton")).toHaveLength(3);
	expect(tree.root.findAll((node) => node.props.accessibilityLabel === "Loading activity")).not.toEqual([]);
	for (const word of ["Retry", "Refresh", "Reconnect"]) expect(text(tree)).not.toContain(word);
	await act(async () => answer(specTree()));
	await settle();
	expect(tree.root.findAll((node) => node.props.testID === "subagent-skeleton")).toEqual([]);
});

it("says the count is partial and whose subagents are missing when a later page fails", async () => {
	client = hub((continuation) => {
		if (continuation === "page-2") throw new Error("offline");
		return {
			revision: 1,
			root: session("local:coord", COORDINATOR.title, [runningOne("a", "First"), runningOne("b", "Second")], {
				truncated: true,
				continuation: "page-2",
			}),
		};
	});
	harness.connection = screenConnection(client, "ready");
	const tree = await mount();
	expect(headerTitle()).toBe("Activity · … Get PR 2138 Test Clean");
	expect(text(tree)).not.toContain("No subagents or shell jobs yet.");
	expect(tree.root.findByType("FlatList" as never).props.onEndReached).toBeTypeOf("function");
});

// A branch whose shell jobs couldn't be read is named by whose it is: the
// coordinator by its title, a subagent by its row's title, never by a ref.
// A branch no loaded row names, as when its subagent's row is on a later
// page, has no title to give, so it goes unnamed. The fixture can't leave a
// row out of the tree, so that child's ref differs from its delegate's
// childRef instead.
it("names whose activity isn't listed by title", async () => {
	client = hub(() => ({
		revision: 1,
		root: session(
			"local:coord",
			COORDINATOR.title,
			[
				runningOne("solo", "Only one", { child: session("local:solo", "Only one", [], { error: "unavailable" }) }),
				runningOne("pair", "Second one", {
					child: session("local:unlisted", "Unlisted", [], { error: "unavailable" }),
				}),
			],
			{ error: "unavailable" },
		),
	}));
	harness.connection = screenConnection(client, "ready");
	const shown = text(await mount());
	expect(shown).toContain(`Some activity under “${COORDINATOR.title}” isn't listed.`);
	expect(shown).toContain("Some activity under “Only one” isn't listed.");
	expect(shown).toContain("Some activity isn't listed.");
	expect(shown).not.toContain("local:");
});

it.each([
	[
		"can't list its activity",
		new WireError("unavailable", -32603, { evenerErrorInfo: "actionUnavailable" }),
		"This session can't list its activity.",
	],
	[
		"is shut down",
		new WireError("thread not found: coord", -32603, { evenerErrorInfo: "sessionUnavailable" }),
		"This session can't list its activity.",
	],
])("says so when the session %s, and offers nothing to press", async (_name, error, words) => {
	client = new FakeClient("ready");
	installActivityFixture(client, () => Promise.reject(error));
	client.on("thread/read", () => ({ thread: { id: "coord", modelProvider: "", status: { type: "active" } } }) as never);
	client.on("model/list", () => ({ data: [] }) as never);
	harness.connection = screenConnection(client, "ready");
	const tree = await mount();
	expect(text(tree)).toContain(words);
	for (const word of ["Retry", "Refresh", "Reconnect"]) expect(text(tree)).not.toContain(word);
});

it("keeps the search field while it has words, even once the list shrinks", async () => {
	let small = false;
	client = hub(() =>
		small
			? { revision: 2, root: session("local:coord", COORDINATOR.title, [runningOne("solo", "Only one")]) }
			: specTree(),
	);
	harness.connection = screenConnection(client, "ready");
	const tree = await mount();
	act(() => tree.root.find((node) => String(node.type) === "TextInput").props.onChangeText("race"));
	small = true;
	await treeUpdatedAndRead(client);
	expect(pressable(tree, "Clear filter")).toBeDefined();
	act(() => pressable(tree, "Clear filter")?.props.onPress());
	expect(text(tree)).toContain("Only one");
});

it("says why it can't list them when the read fails", async () => {
	client = new FakeClient("ready");
	installActivityFixture(client, () => Promise.reject(new Error("boom")));
	client.on("thread/read", () => ({ thread: { id: "coord", modelProvider: "", status: { type: "active" } } }) as never);
	client.on("model/list", () => ({ data: [] }) as never);
	harness.connection = screenConnection(client, "ready");
	const tree = await mount();
	expect(text(tree)).toContain("The activity couldn't be listed right now.");
});

it("says a stop you asked for is pending, then that it stopped, with the toast once", async () => {
	let stopped = false;
	client = hub(() => {
		const whole = specTree();
		if (stopped) {
			const race = (whole.root.entries[0] as { delegate: Record<string, unknown> }).delegate;
			Object.assign(race, { outcome: "cancelled", projectionRevision: 2, child: undefined });
			whole.revision = 2;
		}
		return whole;
	});
	harness.connection = screenConnection(client, "ready");
	const tree = await mount();
	act(() => pressable(tree, "Done · 23")?.props.onPress());
	const rows = flattenSubagents(specTree() as never);
	const race = rows.find((row) => row.ref === "local:race");
	if (!race) throw new Error("no race row");
	act(() => stopRequests("hub-1").request(COORDINATOR.ref, race, Date.now()));
	await settle();
	expect(text(tree)).toContain("Stop requested from the coordinator");
	stopped = true;
	await treeUpdatedAndRead(client);
	// Failure and cancellation both belong to the same still-open history.
	expect(pressable(tree, "Done · 23")?.props.accessibilityState).toEqual({ expanded: true });
	expect(text(tree)).toContain("Stopped at your request");
	const toasts = () => tree.root.findAllByType(Toast).map((toast) => toast.props.toast?.text);
	expect(toasts()).toEqual(["“Fix race in tree settle” stopped"]);
});

it("says a stop you sent directly is pending without naming the coordinator (S6)", async () => {
	const tree = await mount();
	const drain = flattenSubagents(specTree() as never).find((row) => row.ref === "local:run-0");
	if (!drain) throw new Error("no running row");
	act(() => stopRequests("hub-1").request(COORDINATOR.ref, drain, Date.now(), { direct: true }));
	await settle();
	expect(text(tree)).toContain("Stop requested");
	expect(text(tree)).not.toContain("Stop requested from the coordinator");
});

it("toasts a stop recorded after the tree already shows it", async () => {
	client = hub(() => {
		const whole = specTree();
		const race = (whole.root.entries[0] as { delegate: Record<string, unknown> }).delegate;
		Object.assign(race, { outcome: "cancelled", child: undefined });
		return whole;
	});
	harness.connection = screenConnection(client, "ready");
	const tree = await mount();
	const race = flattenSubagents(specTree() as never).find((row) => row.ref === "local:race");
	if (!race) throw new Error("no race row");
	act(() => stopRequests("hub-1").request(COORDINATOR.ref, race, Date.now()));
	await settle();
	expect(tree.root.findAllByType(Toast).map((toast) => toast.props.toast?.text)).toEqual([
		"“Fix race in tree settle” stopped",
	]);
});

it("keeps authoritative counts while visible end-of-list demand loads the next subtree page", async () => {
	const fixture = { revision: 1, root: session("local:coord", COORDINATOR.title, [runningOne("first", "First task")]) };
	client = hub(() => fixture);
	const context = {
		ref: "local:coord",
		sessionId: "coord",
		rootRef: "local:coord",
		ancestors: [],
		ancestryKnown: true,
		epoch: "pages",
		availability: "retained",
	};
	const count = { known: true, total: 501, active: 501, failed: 0, completed: 0 };
	client.on("evener/thread/activity/read", ({ scope }) => ({
		context,
		scope: scope ?? "session",
		delegates: count,
		jobs: { ...count, total: 0, active: 0 },
		watches: { ...count, total: 0, active: 0 },
	}));
	let admit = () => {};
	const admitted = new Promise<void>((resolve) => {
		admit = resolve;
	});
	client.on("evener/thread/delegates/list", (params) => {
		if (params.cursor) admit();
		const id = params.cursor ? "second" : "first";
		return {
			context,
			scope: params.scope ?? "session",
			delegates: [
				{
					runGeneration: 1,
					delegateId: id,
					ownerRef: "local:coord",
					rootRef: "local:coord",
					childRef: `local:${id}`,
					type: "delegate",
					task: id,
					description: `${id} task`,
					lifecycle: "running",
					phase: "running",
					status: "running",
					terminal: false,
					resumable: false,
				},
			],
			page: { complete: false, issues: [], nextCursor: params.cursor ? "later" : "next" },
		};
	});
	harness.connection = screenConnection(client, "ready");
	const screen = await mount();
	expect(headerTitle()).toBe("Activity · 501 Get PR 2138 Test Clean");
	expect(client.calls.filter((call) => call.method === "evener/thread/delegates/list")).toHaveLength(1);
	act(() => {
		screen.root.findByType("FlatList" as never).props.onEndReached({ distanceFromEnd: 0 });
	});
	await act(async () => {
		await admitted;
	});
	expect(
		client.calls.filter((call) => call.method === "evener/thread/delegates/list").map((call) => call.params),
	).toEqual([
		{ ref: "local:coord", scope: "subtree" },
		{ ref: "local:coord", scope: "subtree", cursor: "next" },
	]);
	expect(headerTitle()).toBe("Activity · 501 Get PR 2138 Test Clean");
	expect(text(screen)).toContain("second task");
});
