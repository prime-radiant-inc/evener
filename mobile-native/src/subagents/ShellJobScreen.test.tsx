import { installActivityFixture } from "./sessionActivityTestUtils";
// A shell job's detail (Jesse's ruling on shell jobs, PR 2): the job as its
// coordinator's tree carries it (command, how it's doing, who started it),
// and its output's tail from evener/jobs/output, read again whenever the job
// writes more or changes state. It offers no Refresh and no Stop.
import type { NativeStackNavigationOptions } from "@react-navigation/native-stack";
import { ACTIVITY_REFRESH_MIN_INTERVAL_MS } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, type ReactTestRenderer } from "react-test-renderer";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { render, renderedText, screenConnection } from "../renderNative.testkit";
import { forgetSubagentTrees } from "./subagentTree";
import { ShellJobScreen } from "./ShellJobScreen";
import { JOB_OUTPUT_REREAD_MS } from "./useShellJobOutput";

const harness = vi.hoisted(() => ({ connection: {} as Record<string, unknown>, focused: true }));

vi.mock("react-native", async () => (await import("../renderNative.testkit")).nativeModuleMock());
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));
vi.mock("@react-navigation/native", async () => {
	const { useEffect } = await import("react");
	return {
		useFocusEffect: (effect: () => void | (() => void)) => useEffect(effect, [effect]),
		useIsFocused: () => harness.focused,
	};
});
vi.mock("../ConnectionProvider", () => ({ useConnection: () => harness.connection }));

const NOW = Date.now();
const MIN = 60_000;
const ago = (ms: number) => new Date(NOW - ms).toISOString();
const COORDINATOR = { ref: "local:coord", threadId: "coord", title: "Get PR 2138 Test Clean" };

const session = (ref: string, label: string, entries: unknown[]) => ({
	kind: "session",
	sessionId: ref.slice(ref.indexOf(":") + 1),
	ref,
	label,
	aggregate: "working",
	counts: { active: 0, failed: 0, completed: 0, complete: true },
	entries,
	branch: {},
});
const shellJob = (id: string, over: Record<string, unknown> = {}) => ({
	kind: "shell",
	job: {
		jobId: id,
		ownerSessionId: "fix",
		ownerRef: "local:fix",
		type: "shell",
		status: "command_exited_nonzero",
		outcome: "failure",
		terminal: true,
		exitCode: 1,
		background: true,
		hasOutput: true,
		description: "go test ./agent/...",
		command: "go test ./agent/... -run TestSettle",
		startedAt: ago(3 * MIN),
		endedAt: ago(MIN),
		outputBytes: 42,
		...over,
	},
});
const delegate = (id: string, description: string, child: unknown) => ({
	kind: "delegate",
	delegate: {
		delegateId: id,
		childSessionId: id,
		childRef: `local:${id}`,
		type: "delegate",
		description,
		branch: {},
		runStartedAt: ago(10 * MIN),
		child,
	},
});
const treeWith = (job: unknown, revision = 1) => ({
	revision,
	root: session("local:coord", COORDINATOR.title, [
		delegate("fix", "Fix race in tree settle", session("local:fix", "Fix race in tree settle", [job])),
	]),
});
const TAIL = { tail: "=== RUN TestSettle\n--- FAIL: TestSettle (0.01s)\nFAIL\n", totalBytes: 42, retainedStart: 0 };

let client: FakeClient;
let tree: unknown;
let output: (params: { ref?: string; jobId: string }) => unknown;
let navigation: { setOptions: ReturnType<typeof vi.fn>; options: NativeStackNavigationOptions[] };
const mounted: ReactTestRenderer[] = [];

beforeEach(() => {
	harness.focused = true;
	forgetSubagentTrees("hub-1");
	tree = treeWith(shellJob("j-test"));
	output = () => ({ data: TAIL });
	client = new FakeClient("ready");
	client.on("thread/read", () => ({ thread: { id: "coord", modelProvider: "scripted" } }) as never);
	installActivityFixture(client, (cursor) => {
		if (cursor === "page-2") throw new Error("offline");
		return tree;
	});
	client.on("evener/jobs/output", async (params) => output(params) as never);
	harness.connection = screenConnection(client, "ready");
	const options: NativeStackNavigationOptions[] = [];
	navigation = { setOptions: vi.fn((next) => options.push(next)), options };
});
afterEach(() => {
	for (const screen of mounted.splice(0)) act(() => screen.unmount());
});

async function settle() {
	await act(async () => {
		for (let turn = 0; turn < 20; turn += 1) await Promise.resolve();
		await new Promise((resolve) => setTimeout(resolve, 0));
	});
}

async function mount(over: { ownerRef?: string } = {}) {
	const params = {
		hubId: "hub-1",
		jobId: "j-test",
		ownerRef: "local:fix",
		title: "go test ./agent/...",
		coordinator: COORDINATOR,
		...over,
	};
	const screen = render(
		<ShellJobScreen route={{ key: "job", name: "ShellJob", params } as never} navigation={navigation as never} />,
	);
	mounted.push(screen);
	await settle();
	return screen;
}

// The tree changes; the list paces its reads, so the read runs once the
// minimum interval has passed. Each change starts its fake clock past the
// last one's, since the pacing measures from when the last read ended.
let clock = Date.now();
async function treeChanges(next: { revision: number; root: unknown }) {
	tree = next;
	clock += 10 * ACTIVITY_REFRESH_MIN_INTERVAL_MS;
	vi.useFakeTimers({ now: clock });
	client.emitNotification({
		method: "evener/thread/activity/changed",
		params: { threadId: "coord", sessionId: "coord", ref: "local:coord", resources: ["summary", "delegates", "jobs"] },
	} as never);
	await act(async () => {
		await vi.advanceTimersByTimeAsync(ACTIVITY_REFRESH_MIN_INTERVAL_MS);
	});
	vi.useRealTimers();
	await settle();
}

const outputCalls = () =>
	client.calls.filter((call) => call.method === "evener/jobs/output") as { params: { ref?: string; jobId: string } }[];

it("shows the job's command, how it ended, its exit code and who started it, over its output's tail", async () => {
	const screen = await mount();
	const shown = renderedText(screen);
	expect(shown).toContain("go test ./agent/... -run TestSettle");
	expect(shown).toContain("Command failed · 2m");
	expect(shown).toContain("Exited 1");
	expect(shown).toContain("under Fix race in tree settle");
	expect(shown).toContain("--- FAIL: TestSettle (0.01s)");
	// The output is the owning session's: its ref, not the coordinator's.
	expect(outputCalls().map((call) => call.params)).toEqual([{ ref: "local:fix", jobId: "j-test" }]);
	for (const word of ["Refresh", "Stop", "Retry"]) expect(shown).not.toContain(word);
});

// The coordinator's own job names the coordinator by its title, never its ref.
it("names the coordinator by its title for a job it started itself", async () => {
	tree = {
		revision: 1,
		root: session("local:coord", COORDINATOR.title, [
			shellJob("j-test", { ownerRef: COORDINATOR.ref, ownerSessionId: COORDINATOR.threadId }),
		]),
	};
	const shown = renderedText(await mount({ ownerRef: COORDINATOR.ref }));
	expect(shown).toContain(`under ${COORDINATOR.title}`);
	expect(shown).not.toContain("under local:");
});

// A job whose subagent's row is on a later page sits at the top of the tree,
// so its detail can't name who started it yet and says so.
it("says a job's subagent isn't listed yet while that subagent's row isn't loaded", async () => {
	tree = {
		revision: 1,
		root: session("local:coord", COORDINATOR.title, [
			shellJob("j-test", { ownerRef: "local:later", ownerSessionId: "later" }),
		]),
	};
	const shown = renderedText(await mount({ ownerRef: "local:later" }));
	expect(shown).toContain("under a subagent that isn't listed yet");
	expect(shown).not.toContain(`under ${COORDINATOR.title}`);
});

it("names no exit code for a job that is running or exited cleanly", async () => {
	tree = treeWith(
		shellJob("j-test", {
			status: "running",
			outcome: undefined,
			terminal: false,
			exitCode: undefined,
			endedAt: undefined,
		}),
	);
	expect(renderedText(await mount())).not.toContain("Exited");
	forgetSubagentTrees("hub-1");
	tree = treeWith(shellJob("j-test", { status: "completed", outcome: "success", exitCode: 0 }));
	expect(renderedText(await mount())).not.toContain("Exited");
});

// Behind another screen the running job isn't read on a pace, so what reads
// it again here is the tree showing the job changed.
it("reads the output again when the job writes more or ends, and not when the tree changes elsewhere", async () => {
	harness.focused = false;
	tree = treeWith(
		shellJob("j-test", {
			status: "running",
			outcome: undefined,
			terminal: false,
			exitCode: undefined,
			endedAt: undefined,
		}),
	);
	const screen = await mount();
	expect(outputCalls()).toHaveLength(1);

	output = () => ({ data: { ...TAIL, tail: `${TAIL.tail}ok\n`, totalBytes: 45 } });
	await treeChanges(
		treeWith(
			shellJob("j-test", {
				status: "running",
				outcome: undefined,
				terminal: false,
				exitCode: undefined,
				endedAt: undefined,
				outputBytes: 45,
			}),
			2,
		),
	);
	expect(outputCalls()).toHaveLength(2);
	expect(renderedText(screen)).toContain("ok");

	await treeChanges(treeWith(shellJob("j-test", { outputBytes: 45 }), 3));
	expect(outputCalls()).toHaveLength(3);

	await treeChanges({
		...treeWith(shellJob("j-test", { outputBytes: 45 }), 4),
		root: session("local:coord", "Renamed coordinator", [
			delegate(
				"fix",
				"Fix race in tree settle",
				session("local:fix", "Fix race in tree settle", [shellJob("j-test", { outputBytes: 45 })]),
			),
		]),
	});
	expect(outputCalls()).toHaveLength(3);
});

it("says when the job wrote nothing, when its output can't be read, and when it's no longer listed", async () => {
	output = () => ({ data: { tail: "", totalBytes: 0, retainedStart: 0 } });
	expect(renderedText(await mount())).toContain("No output.");

	forgetSubagentTrees("hub-1");
	output = () => Promise.reject(new Error("job not found: j-test"));
	expect(renderedText(await mount())).toContain("The output couldn't be read right now.");

	forgetSubagentTrees("hub-1");
	output = () => ({ data: { tail: 3 } });
	expect(renderedText(await mount())).toContain("The output couldn't be read right now.");

	forgetSubagentTrees("hub-1");
	tree = { revision: 1, root: session("local:coord", COORDINATOR.title, []) };
	const gone = await mount();
	expect(renderedText(gone)).toContain("This job is no longer listed.");
	expect(outputCalls().filter((call) => call.params.jobId === "j-test")).toHaveLength(3);
});

// A job that leaves the tree takes its output with it: the screen says it's
// no longer listed and shows nothing it read before.
it("drops the output it showed once the job leaves the tree", async () => {
	const screen = await mount();
	expect(renderedText(screen)).toContain("--- FAIL: TestSettle (0.01s)");
	await treeChanges({ revision: 2, root: session("local:coord", COORDINATOR.title, []) });
	expect(renderedText(screen)).toContain("This job is no longer listed.");
	expect(renderedText(screen)).not.toContain("--- FAIL: TestSettle (0.01s)");
});

// The hub announces a job's start and finish but not its output, so a
// running job's tail is read again on a pace while the detail is in front;
// an ended job's tail isn't.
it("reads a running job's output again on its own, and an ended job's only once", async () => {
	tree = treeWith(
		shellJob("j-test", {
			status: "running",
			outcome: undefined,
			terminal: false,
			exitCode: undefined,
			endedAt: undefined,
		}),
	);
	vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"], shouldAdvanceTime: true });
	try {
		const screen = await mount();
		expect(outputCalls()).toHaveLength(1);
		output = () => ({ data: { ...TAIL, tail: `${TAIL.tail}still going\n`, totalBytes: 54 } });
		await act(async () => {
			await vi.advanceTimersByTimeAsync(JOB_OUTPUT_REREAD_MS);
		});
		await settle();
		expect(outputCalls()).toHaveLength(2);
		expect(renderedText(screen)).toContain("still going");
		act(() => screen.unmount());
		mounted.splice(mounted.indexOf(screen), 1);

		forgetSubagentTrees("hub-1");
		tree = treeWith(shellJob("j-test"));
		await mount();
		await act(async () => {
			await vi.advanceTimersByTimeAsync(3 * JOB_OUTPUT_REREAD_MS);
		});
		expect(outputCalls()).toHaveLength(3);
	} finally {
		vi.useRealTimers();
	}
});

// A tree that couldn't be listed whole may hold the job in the part it
// couldn't read, so the detail doesn't call the job gone.
it("says the job can't be listed right now while the tree is partial, not that it's gone", async () => {
	tree = {
		revision: 1,
		root: { ...session("local:coord", COORDINATOR.title, []), branch: { truncated: true, continuation: "page-2" } },
	};
	const screen = await mount();
	expect(renderedText(screen)).not.toContain("This job is no longer listed.");
	expect(renderedText(screen)).toContain("This job couldn't be read right now.");
});

it("says when the kept output starts partway through", async () => {
	output = () => ({ data: { ...TAIL, totalBytes: 9000, retainedStart: 8958, truncated: true, hasEarlier: true } });
	expect(renderedText(await mount())).toContain("Showing the end of the output");
});

it("reads output for the selected owner when another session has the same raw job ID", async () => {
	const selected = treeWith(shellJob("j-test"));
	selected.root.entries.unshift(
		delegate(
			"other",
			"Other owner",
			session("local:other", "Other owner", [
				shellJob("j-test", {
					ownerSessionId: "other",
					ownerRef: "local:other",
					description: "Other owner command",
					command: "echo other",
				}),
			]),
		) as never,
	);
	tree = selected;
	const screen = await mount();
	expect(renderedText(screen)).toContain("go test ./agent/... -run TestSettle");
	expect(renderedText(screen)).not.toContain("echo other");
	expect(outputCalls().map((call) => call.params)).toEqual([{ ref: "local:fix", jobId: "j-test" }]);
});
