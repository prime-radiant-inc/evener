import { installActivityFixture } from "./sessionActivityTestUtils";
import { activityChangedNotification } from "@evener/appwire-client/testing/notifications";
// The Agents and Jobs lists (spec 9): each reads one kind from the
// coordinator's tree through typed activity reads, with live work above a
// quiet history, the strip, the chips, search, and each row's why and last
// line. Agents lists subagents; Jobs lists shell jobs.
import type { NativeStackNavigationOptions } from "@react-navigation/native-stack";
import {
	type JobActivityJob,
	type SessionActivityAncestor,
	type SessionActivityContext,
	type SessionDelegate,
	WireError,
} from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import type { ReactElement } from "react";
import { FlatList } from "react-native";
import { act, type ReactTestRenderer } from "react-test-renderer";
import { beforeEach, expect, it, vi } from "vitest";
import { markers, withTestID } from "../markerTestUtils";
import {
	offersButton,
	playedHaptics,
	pressable,
	render,
	renderedText,
	screenConnection,
	settle,
	textOf,
} from "../renderNative.testkit";
import { Toast } from "../Toast";
import { forgetStopRequestsForHub, stopRequests } from "./nativeStopRequests";
import { flattenSubagents } from "./subagentModel";
import { forgetSubagentTrees } from "./subagentTree";
import { ActivityScreen } from "./ActivityScreen";
import type { ActivityKind } from "./activityList";
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

async function mount(flush = true, kind: ActivityKind = "agents") {
	const params = { hubId: "hub-1", ...COORDINATOR, kind };
	const tree = render(
		<ActivityScreen route={{ key: kind, name: "Activity", params } as never} navigation={navigation as never} />,
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
/** What each subagent row's why line reports, in list order. */
const whys = (tree: ReactTestRenderer) => markers(tree, "subagent-why");
/** Why the list shows a notice instead of rows, if it does. */
const notices = (tree: ReactTestRenderer) => markers(tree, "activity-notice");
/** A row's leading dot tint, found by its delegate id. */
const dotTint = (tree: ReactTestRenderer, id: string) =>
	tree.root.find((node) => node.props.row?.id === id).findByProps({ name: "circle.fill" }).props.tintColor;

it("titles itself with the count over the coordinator's title", async () => {
	await mount();
	expect(headerTitle()).toBe("Agents · 55 Get PR 2138 Test Clean");
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
		runStartedAt: ago(4 * MIN),
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
		(node) =>
			String(node.type) === "SymbolView" &&
			node.props.name === "circle.fill" &&
			node.props.tintColor === paletteFor("light").danger,
	);
	expect(marks).toHaveLength(2);
	const failedWords = tree.root.findAll((node) => String(node.type) === "Text" && node.props.children === "Failed");
	expect(failedWords).toHaveLength(2);
	expect(failedWords.every((node) => node.props.style.color === paletteFor("light").inkMid)).toBe(true);
});

it("lets a drag of the list put the filter's keyboard away", async () => {
	const tree = await mount();
	expect(tree.root.findByProps({ accessibilityLabel: "Filter agents" })).toBeDefined();
	expect(tree.root.findByType(FlatList).props.keyboardDismissMode).toBe("on-drag");
});

// Spec 9 draws the filter chips as one row, as the Board's and the Session's
// chip rows are: with 55 subagents they overflow a phone's width, so they
// scroll sideways in the shared strip, which fades at its trailing edge,
// instead of wrapping "Done" onto a row of its own (audit N10).
it("keeps its filter chips on one row that scrolls sideways", async () => {
	const tree = await mount();
	const strip = tree.root.findByProps({ testID: "activity-filters" });
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

// Jobs lists shell jobs alone (Jesse: split apart). Its rows sit in their
// state's band, are counted by the title and the filter chips, and never show
// a subagent.
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

it("lists shell jobs alone, with truthful output access and their own counts", async () => {
	client = hub(() => treeWithJobs());
	harness.connection = screenConnection(client, "ready");
	const tree = await mount(true, "jobs");
	expect(headerTitle()).toBe("Jobs · 2 Get PR 2138 Test Clean");
	for (const label of ["All, 2", "Running, 1", "Done, 1"]) expect(pressable(tree, label)).toBeDefined();
	expect(tree.root.find((node) => node.props.accessibilityLabel === "1 running, 1 done")).toBeDefined();
	// A subagent is the Agents list's row, never the Jobs list's; here its name
	// only says which subagent ran the job.
	expect(tree.root.findAll((node) => node.props.row?.kind === "subagent")).toHaveLength(0);
	expect(text(tree)).not.toContain("npm run lint");
	expect(pressable(tree, "Completed · 1")?.props.accessibilityState).toEqual({ expanded: false });
	act(() => pressable(tree, "Completed · 1")?.props.onPress());
	const shown = text(tree);
	expect(shown.indexOf("Completed · 1")).toBeLessThan(shown.indexOf("npm run lint"));
	expect(shown.indexOf("RUNNING · 1")).toBeLessThan(shown.indexOf("Serving the docs"));
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
	const screen = await mount(true, "jobs");
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

// The Session's menu offers Agents and Jobs whenever it's connected, so a
// session with nothing to list says so plainly, each in its own words.
it("says there is nothing yet when the session has no subagents", async () => {
	client = hub(() => ({ revision: 1, root: session("local:coord", COORDINATOR.title, []) }));
	harness.connection = screenConnection(client, "ready");
	const tree = await mount();
	expect(notices(tree)).toEqual(["empty"]);
	expect(headerTitle()).toBe("Agents · 0 Get PR 2138 Test Clean");
	expect(text(tree)).toContain("No subagents yet.");
});

it("says there is nothing yet when the session has no shell jobs", async () => {
	client = hub(() => ({
		revision: 1,
		root: session("local:coord", COORDINATOR.title, [runningOne("solo", "Only one")]),
	}));
	harness.connection = screenConnection(client, "ready");
	const tree = await mount(true, "jobs");
	expect(notices(tree)).toEqual(["empty"]);
	expect(headerTitle()).toBe("Jobs · 0 Get PR 2138 Test Clean");
	expect(text(tree)).toContain("No shell jobs yet.");
});

// A disconnect clears the pages but keeps the last tree: an empty list stays
// empty rather than falling back to loading rows.
it("keeps saying there are none when the connection drops after an empty list", async () => {
	client = hub(() => ({ revision: 1, root: session("local:coord", COORDINATOR.title, []) }));
	harness.connection = screenConnection(client, "ready");
	const tree = await mount();
	expect(notices(tree)).toEqual(["empty"]);
	act(() => {
		harness.connection = screenConnection(client, "reconnecting");
		tree.update(
			<ActivityScreen
				route={{ key: "agents", name: "Activity", params: { hubId: "hub-1", ...COORDINATOR, kind: "agents" } } as never}
				navigation={navigation as never}
			/>,
		);
	});
	await settle();
	expect(notices(tree)).toEqual(["empty"]);
	expect(tree.root.findAll((node) => node.props.testID === "subagent-skeleton")).toEqual([]);
});

/** Drops the connection and re-renders this screen at the same kind. */
function disconnect(view: ReactTestRenderer, kind: "agents" | "jobs", connection: FakeClient): void {
	act(() => {
		harness.connection = screenConnection(connection, "reconnecting");
		view.update(
			<ActivityScreen
				route={{ key: kind, name: "Activity", params: { hubId: "hub-1", ...COORDINATOR, kind } } as never}
				navigation={navigation as never}
			/>,
		);
	});
}

// A disconnect keeps each collection's own last answer: proven emptiness stays
// empty, and a failure stays a failure, even though the kept tree can't tell
// them apart.
it("keeps a failed jobs read a failure through a disconnect while delegates loaded", async () => {
	client = pagedHub({
		counts: { delegates: { known: true, total: 1, active: 1, failed: 0, completed: 0 } },
		delegates: () => [
			{
				runGeneration: 1,
				projectionRevision: 1,
				delegateId: "solo",
				ownerRef: "local:coord",
				rootRef: "local:coord",
				childRef: "local:solo",
				type: "delegate",
				description: "Only one",
				task: "Only one",
				lifecycle: "running",
				phase: "running",
				status: "running",
				terminal: false,
				resumable: false,
			},
		],
	});
	client.on("evener/thread/jobs/list", () => Promise.reject(new Error("offline")) as never);
	harness.connection = screenConnection(client, "ready");
	const jobs = await mount(true, "jobs");
	expect(notices(jobs)).toEqual(["failed"]);
	disconnect(jobs, "jobs", client);
	await settle();
	expect(notices(jobs)).toEqual(["failed"]);
});

it("keeps an empty agents list empty through a disconnect while jobs failed", async () => {
	client = pagedHub({ counts: { jobs: { known: false, total: 0, active: 0, failed: 0, completed: 0 } } });
	client.on("evener/thread/jobs/list", () => Promise.reject(new Error("offline")) as never);
	harness.connection = screenConnection(client, "ready");
	const agents = await mount();
	expect(notices(agents)).toEqual(["empty"]);
	disconnect(agents, "agents", client);
	await settle();
	expect(notices(agents)).toEqual(["empty"]);
	expect(agents.root.findAll((node) => node.props.testID === "subagent-skeleton")).toEqual([]);
});

// A gap in the other collection is not this list's: a disconnect must not
// turn a healthy Agents list into one that reports missing activity.
it("keeps the other collection's missing branch off a healthy list through a disconnect", async () => {
	client = pagedHub({
		counts: { delegates: { known: true, total: 1, active: 1, failed: 0, completed: 0 } },
		delegates: () => [
			{
				runGeneration: 1,
				projectionRevision: 1,
				delegateId: "solo",
				ownerRef: "local:coord",
				rootRef: "local:coord",
				childRef: "local:solo",
				type: "delegate",
				description: "Only one",
				task: "Only one",
				lifecycle: "running",
				phase: "running",
				status: "running",
				terminal: false,
				resumable: false,
			},
		],
	});
	client.on("evener/thread/jobs/list", ({ scope }) => ({
		context: activityContext,
		scope: scope ?? "session",
		jobs: [],
		page: { complete: false, issues: [{ ref: "local:unread-jobs", code: "unavailable" as const }] },
	}));
	harness.connection = screenConnection(client, "ready");
	const agents = await mount();
	expect(withTestID(agents, "activity-missing")).toEqual([]);
	expect(text(agents)).toContain("Only one");
	disconnect(agents, "agents", client);
	await settle();
	expect(withTestID(agents, "activity-missing")).toEqual([]);
	expect(text(agents)).toContain("Only one");
});

// A shell job's detail is its own screen over the list, reading the job
// from the same tree.
it("opens a shell job's detail over the list", async () => {
	client = hub(() => treeWithJobs());
	harness.connection = screenConnection(client, "ready");
	const tree = await mount(true, "jobs");
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

it("keeps delegate and job histories on their own screens", async () => {
	client = hub(() => {
		const whole = specTree();
		return { ...whole, root: { ...whole.root, entries: [...whole.root.entries, ...treeWithJobs().root.entries] } };
	});
	harness.connection = screenConnection(client, "ready");
	const agents = await mount();
	expect(pressable(agents, "Done · 23")?.props.accessibilityState).toEqual({ expanded: false });
	expect(pressable(agents, "Completed · 1")).toBeUndefined();
	expect(text(agents)).not.toContain("npm run lint");
	act(() => pressable(agents, "Done · 23")?.props.onPress());
	expect(text(agents)).toContain("Finished task 0");
	expect(text(agents)).not.toContain("npm run lint");

	act(() => forgetSubagentTrees("hub-1"));
	const jobs = await mount(true, "jobs");
	expect(pressable(jobs, "Completed · 1")?.props.accessibilityState).toEqual({ expanded: false });
	expect(pressable(jobs, "Done · 23")).toBeUndefined();
	expect(text(jobs)).not.toContain("Finished task 0");
	act(() => pressable(jobs, "Completed · 1")?.props.onPress());
	expect(text(jobs)).toContain("npm run lint");
	expect(text(jobs)).not.toContain("Finished task 0");
	expect(text(jobs)).not.toContain("Reproduce the race");
});

it("searches past eight subagents, filtering rows and section counts while the chips keep the whole", async () => {
	const tree = await mount();
	const field = tree.root.find((node) => String(node.type) === "TextInput");
	expect(field.props.placeholder).toMatch(/\S/);
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
	// The done checkmark retires with the dot: the band header carries the
	// state, and a settled run shows no mark at all.
	expect(
		tree.root.findAll((node) => String(node.type) === "SymbolView" && node.props.name === "checkmark"),
	).toHaveLength(0);
});

// The list runs no clock, but a running subagent's dot still dims when its
// silence crosses the threshold, with no new tree to re-render it.
it("dims a silent running subagent's dot on its own", async () => {
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
		// The line has nothing to say while the dot alone speaks its clock.
		expect(whys(tree)).toEqual([]);
		expect(dotTint(tree, "hush")).toBe(paletteFor("light").alive);
		await act(async () => {
			await vi.advanceTimersByTimeAsync(99);
		});
		expect(dotTint(tree, "hush")).toBe(paletteFor("light").alive);
		await act(async () => {
			await vi.advanceTimersByTimeAsync(1);
		});
		expect(dotTint(tree, "hush")).toBe(paletteFor("light").inkLow);
		// The row's corner keeps its own clock: time in state, not silence.
		expect(text(tree)).toContain("4m");
	} finally {
		vi.useRealTimers();
	}
});

// A subagent waiting on its human reads amber, the shared rule's attention
// state - the same dot vocabulary the web sidebar shows.
it("tints an attention subagent's dot amber", async () => {
	client = hub(() => ({
		revision: 1,
		root: session("local:coord", COORDINATOR.title, [
			runningOne("approval", "Ask before merging", { phase: "awaiting_approval" }),
		]),
	}));
	harness.connection = screenConnection(client, "ready");
	const tree = await mount();
	expect(dotTint(tree, "approval")).toBe(paletteFor("light").attention);
	// Attention is spoken too: a human being needed is the one state a
	// screen reader must never miss.
	expect(pressable(tree, "Ask before merging, Running, Needs you, 4 minutes")).toBeDefined();
});

// The why line shows the subagent's stated intent, while the accessible label
// keeps the state word and the intent together.
it("shows a running subagent's stated intent as its row's why line", async () => {
	client = hub(() => ({
		revision: 1,
		root: session("local:coord", COORDINATOR.title, [
			runningOne("intent", "Inspect the cache", { latestIntent: "Reading the board's row tests." }),
		]),
	}));
	harness.connection = screenConnection(client, "ready");
	const tree = await mount();
	const shown = text(tree);
	expect(shown).toContain("Reading the board's row tests.");
	expect(shown).not.toContain("Working");
	// The label speaks the dot's state word beside the state word, so the
	// fresh-versus-quiet distinction stays reachable without the visible line.
	expect(
		pressable(tree, "Inspect the cache, Running, Working, Reading the board's row tests., 4 minutes"),
	).toBeDefined();
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
				<ActivityScreen
					route={
						{ key: "agents", name: "Activity", params: { hubId: "hub-1", ...COORDINATOR, kind: "agents" } } as never
					}
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
			<ActivityScreen
				route={{ key: "agents", name: "Activity", params: { hubId: "hub-1", ...COORDINATOR, kind: "agents" } } as never}
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
	expect(headerTitle()).toBe("Agents · … Get PR 2138 Test Clean");
	expect(notices(tree)).toEqual([]);
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
	const tree = await mount();
	const missing = withTestID(tree, "activity-missing").map(textOf);
	expect(missing).toHaveLength(3);
	expect(missing.filter((line) => line.includes(COORDINATOR.title))).toHaveLength(1);
	expect(missing.filter((line) => line.includes("Only one"))).toHaveLength(1);
	// The branch no loaded row names goes unnamed.
	expect(missing.filter((line) => !line.includes(COORDINATOR.title) && !line.includes("Only one"))).toHaveLength(1);
	expect(missing.join(" ")).not.toContain("Unlisted");
	expect(text(tree)).not.toContain("local:");
});

it.each([
	[
		"can't list its activity",
		new WireError("unavailable", -32603, { evenerErrorInfo: "actionUnavailable" }),
		"unsupported",
	],
	[
		"is shut down",
		new WireError("thread not found: coord", -32603, { evenerErrorInfo: "sessionUnavailable" }),
		"unsupported",
	],
])("says so when the session %s, and offers nothing to press", async (_name, error, reason) => {
	client = new FakeClient("ready");
	installActivityFixture(client, () => Promise.reject(error));
	client.on("thread/read", () => ({ thread: { id: "coord", modelProvider: "", status: { type: "active" } } }) as never);
	client.on("model/list", () => ({ data: [] }) as never);
	harness.connection = screenConnection(client, "ready");
	const tree = await mount();
	expect(notices(tree)).toEqual([reason]);
	expect(offersButton(tree, /\b(Retry|Refresh|Reconnect)\b/)).toBe(false);
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
	expect(notices(tree)).toEqual(["failed"]);
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
	expect(whys(tree)).toContain("stop-requested");
	stopped = true;
	await treeUpdatedAndRead(client);
	// Failure and cancellation both belong to the same still-open history.
	expect(pressable(tree, "Done · 23")?.props.accessibilityState).toEqual({ expanded: true });
	expect(whys(tree)).toContain("stop-settled");
	const toasts = () => tree.root.findAllByType(Toast).map((toast) => toast.props.toast?.text);
	expect(toasts()).toEqual(["“Fix race in tree settle” stopped"]);
});

it("says a stop you sent directly is pending without naming the coordinator (S6)", async () => {
	const tree = await mount();
	const drain = flattenSubagents(specTree() as never).find((row) => row.ref === "local:run-0");
	if (!drain) throw new Error("no running row");
	act(() => stopRequests("hub-1").request(COORDINATOR.ref, drain, Date.now(), { direct: true }));
	await settle();
	expect(whys(tree)).toContain("stop-requested-direct");
	expect(whys(tree)).not.toContain("stop-requested");
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
					projectionRevision: 1,
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
	expect(headerTitle()).toBe("Agents · 501 Get PR 2138 Test Clean");
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
	expect(headerTitle()).toBe("Agents · 501 Get PR 2138 Test Clean");
	expect(text(screen)).toContain("second task");
});

const activityContext: SessionActivityContext = {
	ref: "local:coord",
	sessionId: "coord",
	rootRef: "local:coord",
	ancestors: [] as SessionActivityAncestor[],
	ancestryKnown: true,
	epoch: "pages",
	availability: "retained",
};
const noCounts = { known: true, total: 0, active: 0, failed: 0, completed: 0 };

/** A hub whose two collections page on their own cursors. */
function pagedHub(read: {
	delegates?: (cursor: string | undefined) => SessionDelegate[];
	jobs?: (cursor: string | undefined) => JobActivityJob[];
	jobsNext?: string | undefined;
	counts?: { delegates?: typeof noCounts; jobs?: typeof noCounts };
}) {
	const next = new FakeClient("ready");
	next.on(
		"thread/read",
		() => ({ thread: { id: "coord", modelProvider: "anthropic/claude-opus", status: { type: "active" } } }) as never,
	);
	next.on("model/list", () => ({ data: [] }) as never);
	next.on("evener/thread/activity/read", ({ scope }) => ({
		context: activityContext,
		scope: scope ?? "session",
		delegates: read.counts?.delegates ?? noCounts,
		jobs: read.counts?.jobs ?? noCounts,
		watches: noCounts,
	}));
	next.on("evener/thread/delegates/list", ({ cursor, scope }) => ({
		context: activityContext,
		scope: scope ?? "session",
		delegates: read.delegates?.(cursor) ?? [],
		page: { complete: true, issues: [] },
	}));
	next.on("evener/thread/jobs/list", ({ cursor, scope }) => ({
		context: activityContext,
		scope: scope ?? "session",
		jobs: read.jobs?.(cursor) ?? [],
		page: {
			complete: cursor === read.jobsNext || !read.jobsNext,
			issues: [],
			...(cursor === undefined && read.jobsNext ? { nextCursor: read.jobsNext } : {}),
		},
	}));
	return next;
}

// A running subagent's why line reads the command its child ran, which is a
// shell job. The Agents list therefore advances jobs pages too, or that line
// stays blank for a command on a later page.
it("advances jobs pages on the Agents list, where a running subagent's command comes from", async () => {
	client = pagedHub({
		counts: {
			delegates: { known: true, total: 1, active: 1, failed: 0, completed: 0 },
			jobs: { known: true, total: 1, active: 1, failed: 0, completed: 0 },
		},
		delegates: () => [
			{
				runGeneration: 1,
				projectionRevision: 1,
				delegateId: "solo",
				ownerRef: "local:coord",
				rootRef: "local:coord",
				childRef: "local:solo",
				type: "delegate",
				description: "Only one",
				task: "Only one",
				lifecycle: "running",
				phase: "running",
				status: "running",
				terminal: false,
				resumable: false,
			},
		],
		jobs: (cursor) =>
			cursor === "jobs-2"
				? [
						{
							jobId: "j-cmd",
							ownerSessionId: "solo",
							ownerRef: "local:solo",
							type: "shell",
							status: "running",
							terminal: false,
							background: true,
							hasOutput: true,
							description: "go test ./...",
							command: "go test ./...",
							startedAt: ago(2 * MIN),
							outputBytes: 0,
						},
					]
				: [
						{
							jobId: "j-other",
							ownerSessionId: "coord",
							ownerRef: "local:coord",
							type: "shell",
							status: "running",
							terminal: false,
							background: true,
							hasOutput: true,
							description: "npm run docs",
							command: "npm run docs",
							startedAt: ago(3 * MIN),
							outputBytes: 0,
						},
					],
		jobsNext: "jobs-2",
	});
	harness.connection = screenConnection(client, "ready");
	const screen = await mount();
	expect(text(screen)).toContain("Only one");
	act(() => screen.root.findByType("FlatList" as never).props.onEndReached({ distanceFromEnd: 0 }));
	await settle();
	act(() => screen.root.findByType("FlatList" as never).props.onEndReached({ distanceFromEnd: 0 }));
	await settle();
	expect(
		client.calls
			.filter((call) => call.method === "evener/thread/jobs/list")
			.map((call) => (call.params as { cursor?: string }).cursor),
	).toContain("jobs-2");
	expect(text(screen)).toContain("Running go test ./...");
});

// The Agents list's emptiness is its own: shell jobs still loading say
// nothing about whether the session has subagents.
it("says there are no subagents while jobs pages remain", async () => {
	client = pagedHub({
		counts: { jobs: { known: true, total: 40, active: 0, failed: 0, completed: 40 } },
		jobs: () => [shellJob("j-docs", "Serving the docs").job],
		jobsNext: "jobs-2",
	});
	harness.connection = screenConnection(client, "ready");
	const tree = await mount();
	expect(notices(tree)).toEqual(["empty"]);
	expect(text(tree)).toContain("No subagents yet.");
});

// The same holds when the other collection fails outright: delegates read as
// an empty success, so Agents says so rather than reporting a failure.
it("says there are no subagents when the jobs read fails", async () => {
	client = pagedHub({ counts: { jobs: { known: false, total: 0, active: 0, failed: 0, completed: 0 } } });
	client.on("evener/thread/jobs/list", () => Promise.reject(new Error("offline")) as never);
	harness.connection = screenConnection(client, "ready");
	const tree = await mount();
	expect(notices(tree)).toEqual(["empty"]);
	expect(text(tree)).toContain("No subagents yet.");
});

// And the mirror image: a failed delegates read leaves Jobs saying it has
// none, not reporting the other collection's failure.
it("says there are no shell jobs when the delegates read fails", async () => {
	client = pagedHub({ counts: { delegates: { known: false, total: 0, active: 0, failed: 0, completed: 0 } } });
	client.on("evener/thread/delegates/list", () => Promise.reject(new Error("offline")) as never);
	harness.connection = screenConnection(client, "ready");
	const jobs = await mount(true, "jobs");
	expect(notices(jobs)).toEqual(["empty"]);
	expect(text(jobs)).toContain("No shell jobs yet.");
});
