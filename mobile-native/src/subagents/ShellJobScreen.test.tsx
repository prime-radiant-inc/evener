import { activityFixture, installActivityFixture } from "./sessionActivityTestUtils";
// A shell job's detail (Jesse's ruling on shell jobs, PR 2): the job as its
// coordinator's tree carries it (command, how it's doing, who started it),
// and its output's tail from evener/jobs/output, read again whenever the job
// writes more or changes state. It offers no Refresh and no Stop.
import type { NativeStackNavigationOptions } from "@react-navigation/native-stack";
import type { SessionActivityReadParams } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { connectJobOutputPeer } from "@evener/appwire-client/testing/jobOutputPeer";
import { activityChangedNotification, wireThread } from "@evener/appwire-client/testing/notifications";
import { act } from "react-test-renderer";
import { beforeEach, expect, it, vi } from "vitest";
import { render, renderedText, screenConnection, settle } from "../renderNative.testkit";
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
		outputBytes: 53,
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
const PAGE = {
	data: "=== RUN TestSettle\n--- FAIL: TestSettle (0.01s)\nFAIL\n",
	totalBytes: 53,
	bytesReturned: 53,
	offsetBytes: 0,
	retainedStartBytes: 0,
	encoding: "utf8",
};

let client: FakeClient;
let tree: unknown;
let output: (params: { ref?: string; jobId: string }) => unknown;
let navigation: { setOptions: ReturnType<typeof vi.fn>; options: NativeStackNavigationOptions[] };

beforeEach(() => {
	harness.focused = true;
	forgetSubagentTrees("hub-1");
	tree = treeWith(shellJob("j-test"));
	output = () => ({ data: PAGE });
	client = new FakeClient("ready");
	client.on("thread/read", ({ ref }) => ({
		thread: wireThread(ref, { id: "coord", sessionId: "coord", modelProvider: "scripted" }),
	}));
	installActivityFixture(client, (cursor) => {
		if (cursor === "page-2") throw new Error("offline");
		return tree;
	});
	client.on("evener/jobs/output", async (params) => output(params) as never);
	harness.connection = screenConnection(client, "ready");
	const options: NativeStackNavigationOptions[] = [];
	navigation = { setOptions: vi.fn((next) => options.push(next)), options };
});

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
	await settle();
	return screen;
}

// The tree changes; the activity store re-reads what the notification names
// at once.
async function treeChanges(next: { revision: number; root: unknown }) {
	tree = next;
	act(() => client.emitNotification(activityChangedNotification(COORDINATOR)));
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
// so its detail can't name who started it and says so.
it("says a job's subagent isn't listed while that subagent's row isn't loaded", async () => {
	tree = {
		revision: 1,
		root: session("local:coord", COORDINATOR.title, [
			shellJob("j-test", { ownerRef: "local:later", ownerSessionId: "later" }),
		]),
	};
	const shown = renderedText(await mount({ ownerRef: "local:later" }));
	expect(shown).toContain("under a subagent that isn't listed");
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
	act(() => forgetSubagentTrees("hub-1"));
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

	output = () => ({ data: { ...PAGE, data: `${PAGE.data}ok\n`, totalBytes: 56, bytesReturned: 56 } });
	await treeChanges(
		treeWith(
			shellJob("j-test", {
				status: "running",
				outcome: undefined,
				terminal: false,
				exitCode: undefined,
				endedAt: undefined,
				outputBytes: 56,
			}),
			2,
		),
	);
	expect(outputCalls()).toHaveLength(2);
	expect(renderedText(screen)).toContain("ok");

	await treeChanges(treeWith(shellJob("j-test", { outputBytes: 56 }), 3));
	expect(outputCalls()).toHaveLength(3);

	await treeChanges({
		...treeWith(shellJob("j-test", { outputBytes: 56 }), 4),
		root: session("local:coord", "Renamed coordinator", [
			delegate(
				"fix",
				"Fix race in tree settle",
				session("local:fix", "Fix race in tree settle", [shellJob("j-test", { outputBytes: 56 })]),
			),
		]),
	});
	expect(outputCalls()).toHaveLength(3);
});

it("says when the job wrote nothing, when its output can't be read, and when it's no longer listed", async () => {
	output = () => ({
		data: { offsetBytes: 0, bytesReturned: 0, totalBytes: 0, retainedStartBytes: 0, encoding: "utf8", data: "" },
	});
	expect(renderedText(await mount())).toContain("No output.");

	act(() => forgetSubagentTrees("hub-1"));
	output = () => Promise.reject(new Error("job not found: j-test"));
	expect(renderedText(await mount())).toContain("The output couldn't be read right now.");

	act(() => forgetSubagentTrees("hub-1"));
	output = () => ({ data: { tail: 3 } });
	expect(renderedText(await mount())).toContain("The output couldn't be read right now.");

	act(() => forgetSubagentTrees("hub-1"));
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
		output = () => ({ data: { ...PAGE, data: `${PAGE.data}still going\n`, totalBytes: 65, bytesReturned: 65 } });
		await act(async () => {
			await vi.advanceTimersByTimeAsync(JOB_OUTPUT_REREAD_MS);
		});
		await settle();
		expect(outputCalls()).toHaveLength(2);
		expect(renderedText(screen)).toContain("still going");
		act(() => screen.unmount());

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
	output = () => ({ data: { ...PAGE, totalBytes: 9000, offsetBytes: 8947 } });
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

async function mountWireScreen() {
	const { client: realClient, peer } = await connectJobOutputPeer();
	const send = peer.send.bind(peer);
	peer.send = (frame) => {
		send(frame);
		const request = JSON.parse(frame) as { id?: number; method: string; params: SessionActivityReadParams };
		if (request.id === undefined || request.method === "evener/jobs/output") return;
		let result: unknown;
		if (request.method === "thread/read") {
			result = { thread: wireThread("local:coord", { id: "coord", sessionId: "coord", modelProvider: "scripted" }) };
		} else if (request.method === "evener/thread/activity/read") {
			result = activityFixture(tree, request.params).summary;
		} else if (request.method === "evener/thread/delegates/list" || request.method === "evener/thread/jobs/list") {
			const fixture = activityFixture(tree, request.params);
			result = {
				context: fixture.context,
				scope: fixture.scope,
				...(request.method === "evener/thread/jobs/list" ? { jobs: fixture.jobs } : { delegates: fixture.delegates }),
				page: { complete: true, issues: [] },
			};
		} else if (request.method === "evener/thread/watches/list") {
			const fixture = activityFixture(tree, request.params);
			result = { context: fixture.context, scope: fixture.scope, watches: [], page: { complete: true, issues: [] } };
		} else {
			result = {};
		}
		peer.receive({ jsonrpc: "2.0", id: request.id, result });
	};
	harness.connection = screenConnection(realClient, "ready");
	const screen = await mount();
	return { client: realClient, peer, screen };
}

it.each([
	[
		"UTF8",
		{ offsetBytes: 0, bytesReturned: 6, totalBytes: 6, retainedStartBytes: 0, encoding: "utf8", data: "hello\n" },
		"hello",
		false,
	],
	[
		"base64",
		{
			offsetBytes: 100,
			bytesReturned: 5,
			totalBytes: 105,
			retainedStartBytes: 100,
			encoding: "base64",
			data: "8J+YgAo=",
		},
		"😀",
		true,
	],
	[
		"empty",
		{ offsetBytes: 100, bytesReturned: 0, totalBytes: 100, retainedStartBytes: 100, encoding: "utf8", data: "" },
		"No output.",
		false,
	],
	[
		"bad count",
		{ offsetBytes: 0, bytesReturned: 5, totalBytes: 6, retainedStartBytes: 0, encoding: "utf8", data: "hello\n" },
		"The output couldn't be read right now.",
		false,
	],
	["old tail", { tail: "hello\n", totalBytes: 6, retainedStart: 0 }, "The output couldn't be read right now.", false],
])("renders the %s wire page through the actual native screen", async (_label, page, expected, partial) => {
	const { client: realClient, peer, screen } = await mountWireScreen();
	try {
		const request = await peer.request("evener/jobs/output");
		expect(request.params).toEqual({ ref: "local:fix", jobId: "j-test" });
		await act(async () => peer.reply(request, page));
		const shown = renderedText(screen);
		expect(shown).toContain(expected);
		expect(shown.includes("Showing the end of the output.")).toBe(partial);
		expect(shown).toContain("go test ./agent/... -run TestSettle");
	} finally {
		act(() => realClient.close());
	}
});

it("shows a structured pruning failure instead of an empty native output", async () => {
	const { client: realClient, peer, screen } = await mountWireScreen();
	try {
		const request = await peer.request("evener/jobs/output");
		await act(async () =>
			peer.fail(request, "job output is no longer retained", {
				evenerErrorInfo: "jobOutputPruned",
				retainedStartBytes: 100,
				totalBytes: 105,
			}),
		);
		const shown = renderedText(screen);
		expect(shown).toContain("The output couldn't be read right now.");
		expect(shown).not.toContain("No output.");
	} finally {
		act(() => realClient.close());
	}
});
