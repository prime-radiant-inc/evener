// Recorded producers and real phone decisions, with only API and native platform edges substituted.
import type { JobActivityJob, SessionDelegatesResponse } from "@evener/appwire-client";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { wireThread } from "@evener/appwire-client/testing/notifications";
import {
	subagentOutcomesDelegatesResponse,
	subagentResumedDelegatesResponse,
} from "@evener/appwire-client/testing/subagentWireFixtures";
import { act, type ReactTestRenderer } from "react-test-renderer";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { pressable, render, renderedText, screenConnection } from "../renderNative.testkit";
import { activityListKey } from "./activityList";
import { forgetStopRequestsForHub } from "./nativeStopRequests";
import { flattenSubagents } from "./subagentModel";
import { forgetSubagentTrees, subagentTree, type SubagentTreeSnapshot } from "./subagentTree";
import { SubagentsScreen } from "./SubagentsScreen";

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

const coordinator = { ref: "local:root", threadId: "root", title: "Recorded work" };
const hubId = "hub-1";
const context = subagentOutcomesDelegatesResponse().context;
const emptyCounts = { known: true, total: 0, active: 0, failed: 0, completed: 0 };
const delegateCounts = { known: true, total: 3, active: 0, failed: 1, completed: 2 };
const screens: ReactTestRenderer[] = [];
const navigation = { push: vi.fn(), navigate: vi.fn(), setOptions: vi.fn() };

beforeEach(() => {
	forgetSubagentTrees(hubId);
	forgetStopRequestsForHub(hubId);
	vi.clearAllMocks();
});
afterEach(() => {
	for (const screen of screens.splice(0)) act(() => screen.unmount());
	forgetSubagentTrees(hubId);
});

function hub(readDelegates = () => subagentOutcomesDelegatesResponse()) {
	const client = new FakeClient("ready");
	client.on("thread/read", ({ ref }) => ({
		thread: wireThread(ref, { id: "root", sessionId: "root", modelProvider: "scripted" }),
	}));
	client.on("thread/unsubscribe", () => ({}));
	client.on("model/list", () => ({ data: [] }));
	client.on("evener/thread/activity/read", ({ scope }) => ({
		context,
		scope: scope ?? "session",
		delegates: delegateCounts,
		jobs: emptyCounts,
		watches: emptyCounts,
	}));
	client.on("evener/thread/delegates/list", () => readDelegates());
	client.on("evener/thread/jobs/list", ({ scope }) => ({
		context,
		scope: scope ?? "session",
		jobs: [],
		page: { complete: true, issues: [] },
	}));
	return client;
}
function element() {
	return (
		<SubagentsScreen
			route={{ key: "history", name: "Subagents", params: { hubId, ...coordinator } } as never}
			navigation={navigation as never}
		/>
	);
}
async function mount(client: FakeClient) {
	harness.connection = screenConnection(client, "ready");
	const screen = render(element());
	screens.push(screen);
	await act(async () => {
		await subagentTree(hubId, coordinator.ref, "root").reload();
	});
	return screen;
}
async function reconnect(screen: ReactTestRenderer, old: FakeClient, next: FakeClient) {
	const binding = subagentTree(hubId, coordinator.ref, "root");
	act(() => {
		harness.connection = screenConnection(old, "reconnecting");
		screen.update(element());
	});
	await act(async () => {
		await binding.setClient(null);
	});
	await act(async () => {
		await binding.setClient(next);
		harness.connection = screenConnection(next, "ready");
		screen.update(element());
		await binding.reload();
	});
}
function press(screen: ReactTestRenderer, label: string) {
	const button = pressable(screen, label);
	if (!button) throw new Error(`missing button: ${label}`);
	act(() => button.props.onPress());
}
function projection() {
	const tree = subagentTree(hubId, coordinator.ref, "root").getSnapshot().tree;
	if (!tree) throw new Error("missing activity projection");
	return tree;
}
function published(check: (snapshot: SubagentTreeSnapshot) => boolean): Promise<void> {
	const binding = subagentTree(hubId, coordinator.ref, "root");
	if (check(binding.getSnapshot())) return Promise.resolve();
	return new Promise((resolve) => {
		const stop = binding.subscribe(() => {
			if (check(binding.getSnapshot())) {
				stop();
				resolve();
			}
		});
	});
}

it("renders the recorded reported, failed and stopped generations as truthful quiet history", async () => {
	let response = subagentOutcomesDelegatesResponse();
	const client = hub(() => response);
	const screen = await mount(client);
	expect(renderedText(screen)).not.toContain("provider returned 500");
	press(screen, "Done · 3");
	const rows = flattenSubagents(projection());
	expect(
		rows.map((row) => [
			row.id,
			row.state,
			row.delegate.status,
			row.delegate.outcome,
			row.delegate.reason,
			row.delegate.runGeneration,
		]),
	).toEqual([
		["dlg_stopped", "done", "idle", "cancelled", "cancelled", 1],
		["dlg_reported", "done", "idle", "completed", undefined, 1],
		["dlg_failed", "failed", "idle", "failed", "run_error", 1],
	]);
	expect(renderedText(screen)).toContain("Fixed the race: settle now waits for the drain.");
	expect(renderedText(screen)).toContain("provider returned 500");
	expect(renderedText(screen)).toContain("Stopped");
	expect(screen.root.find((node) => node.props.row?.id === "dlg_reported").props.row.delegate.reportPreview).toBe(
		"Fixed the race: settle now waits for the drain.\n\nThe new test covers both orders.",
	);
	response = subagentResumedDelegatesResponse();
	await act(async () => {
		await subagentTree(hubId, coordinator.ref, "root").reload();
	});
	expect(renderedText(screen)).toContain("Second run report.");
	expect(renderedText(screen)).not.toContain("Fixed the race:");
	expect(screen.root.find((node) => node.props.row?.id === "dlg_reported").props.row.delegate.runGeneration).toBe(2);
});

it("rejects a delayed prior report after a newer running generation reaches the phone", async () => {
	const old = hub(() => subagentResumedDelegatesResponse());
	const screen = await mount(old);
	press(screen, "Done · 3");
	const entered = deferred<void>();
	const answer = deferred<SessionDelegatesResponse>();
	old.on("evener/thread/delegates/list", () => {
		entered.resolve();
		return answer.promise;
	});
	const binding = subagentTree(hubId, coordinator.ref, "root");
	let pending = Promise.resolve();
	act(() => {
		pending = binding.reload();
	});
	await entered.promise;
	const running = subagentResumedDelegatesResponse();
	running.delegates = running.delegates.map((delegate) =>
		delegate.delegateId === "dlg_reported"
			? {
					...delegate,
					runGeneration: 3,
					terminal: false,
					lifecycle: "running",
					phase: "running",
					status: "running",
					outcome: undefined,
					reportPreview: undefined,
					runEndedAt: undefined,
				}
			: delegate,
	);
	const next = hub(() => running);
	next.on("evener/thread/activity/read", ({ scope }) => ({
		context,
		scope: scope ?? "session",
		delegates: { ...delegateCounts, active: 1, completed: 1 },
		jobs: emptyCounts,
		watches: emptyCounts,
	}));
	await reconnect(screen, old, next);
	const seen: unknown[] = [];
	const stop = binding.subscribe(() =>
		seen.push(flattenSubagents(projection()).find((row) => row.id === "dlg_reported")?.delegate.reportPreview),
	);
	await act(async () => {
		answer.resolve(subagentResumedDelegatesResponse());
		await pending;
	});
	stop();
	const row = screen.root.find((node) => node.props.row?.id === "dlg_reported").props.row;
	expect(row).toMatchObject({ state: "running", delegate: { runGeneration: 3, terminal: false } });
	expect(row.delegate.reportPreview).toBeUndefined();
	expect(seen.every((report) => report === undefined)).toBe(true);
	expect(renderedText(screen)).not.toContain("Second run report.");
	expect(renderedText(screen)).toContain("RUNNING · 1");
});

function job(index: number, over: Partial<JobActivityJob> = {}): JobActivityJob {
	return {
		jobId: index === 11 ? "later" : `job-${index}`,
		ownerSessionId: "root",
		ownerRef: "local:root",
		type: "shell",
		status: "completed",
		terminal: true,
		background: true,
		hasOutput: true,
		description: `History task ${index}`,
		command: "printf 'retained output'",
		transcriptRef: `job:${index === 11 ? "later" : `job-${index}`}`,
		startedAt: "2026-10-03T12:00:00Z",
		endedAt: "2026-10-03T12:01:00Z",
		outputBytes: 15,
		...over,
	};
}
function pagedHub(options: { description?: string; active?: boolean; failed?: boolean; missing?: boolean } = {}) {
	const client = hub();
	client.on("evener/thread/activity/read", ({ scope }) => ({
		context,
		scope: scope ?? "session",
		delegates: delegateCounts,
		jobs: {
			known: !options.missing,
			total: 12,
			active: options.active ? 1 : 0,
			failed: options.failed ? 1 : 0,
			completed: options.active || options.failed ? 11 : 12,
		},
		watches: emptyCounts,
	}));
	client.on("evener/thread/jobs/list", ({ scope, cursor }) => {
		const page = cursor === "page-3" ? 2 : cursor === "page-2" ? 1 : 0;
		return {
			context,
			scope: scope ?? "session",
			jobs: Array.from({ length: 4 }, (_, offset) => {
				const index = page * 4 + offset;
				return job(
					index,
					index !== 11
						? {}
						: {
								description: options.description ?? "History task 11",
								terminal: !options.active,
								status: options.active ? "running" : options.failed ? "command_exited_nonzero" : "completed",
								outcome: options.active ? undefined : options.failed ? "failure" : "success",
								endedAt: options.active ? undefined : "2026-10-03T12:01:00Z",
							},
				);
			}),
			page: {
				complete: page === 2 && !options.missing,
				issues: options.missing ? [{ ref: "local:child-dlg_failed", code: "unavailable" as const }] : [],
				...(page < 2 ? { nextCursor: `page-${page + 2}` } : {}),
			},
		};
	});
	return client;
}
const cursors = (client: FakeClient) =>
	client.calls
		.filter((call) => call.method === "evener/thread/jobs/list")
		.map((call) => (call.params as { cursor?: string }).cursor);
async function loadThirdPage() {
	const binding = subagentTree(hubId, coordinator.ref, "root");
	await act(async () => {
		await binding.loadMore();
		await binding.loadMore();
	});
	expect(binding.getSnapshot().hasMore).toBe(false);
}
function laterRow(screen: ReactTestRenderer) {
	const list = screen.root.find((node) => node.props.keyExtractor === activityListKey);
	const item = list.props.data.find(
		(item: { kind: string; row?: { id: string } }) => item.kind === "row" && item.row?.id === "later",
	);
	expect(item).toBeDefined();
	expect(list.props.keyExtractor(item)).toBe('job:["local:root","later"]');
	return screen.root.find((node) => node.props.row?.kind === "job" && node.props.row.id === "later");
}

it("retains an open later job history, filter, search and qualified row through refresh and client replacement", async () => {
	const options = { description: "History task 11" };
	const client = pagedHub(options);
	const screen = await mount(client);
	await loadThirdPage();
	expect(cursors(client)).toEqual([undefined, "page-2", "page-3"]);
	press(screen, "Completed · 12");
	act(() => screen.root.findByType("TextInput" as never).props.onChangeText("History task"));
	expect(laterRow(screen).props.row.title).toBe("History task 11");
	expect(pressable(screen, "Done · 3")).toBeUndefined();
	expect(pressable(screen, "Done, 15")).toBeDefined();
	options.description = "History task 11 refreshed";
	const binding = subagentTree(hubId, coordinator.ref, "root");
	await act(async () => {
		await binding.reload();
	});
	expect(laterRow(screen).props.row.title).toBe("History task 11 refreshed");
	expect(cursors(client).slice(-3)).toEqual([undefined, "page-2", "page-3"]);
	act(() => {
		harness.connection = screenConnection(client, "reconnecting");
		screen.update(element());
	});
	await act(async () => {
		await binding.setClient(null);
	});
	expect(laterRow(screen).props.row.title).toBe("History task 11 refreshed");
	expect(screen.root.findByType("TextInput" as never).props.value).toBe("History task");
	const next = pagedHub({ description: "History task 11 reconnected" });
	await reconnect(screen, client, next);
	expect(cursors(next).slice(-3)).toEqual([undefined, "page-2", "page-3"]);
	expect(laterRow(screen).props.row.title).toBe("History task 11 reconnected");
	expect(pressable(screen, "Completed · 12")?.props.accessibilityState).toEqual({ expanded: true });
	expect(pressable(screen, "All, 15")?.props.accessibilityState).toEqual({ selected: true });
	expect(screen.root.findByType("TextInput" as never).props.value).toBe("History task");
	press(screen, "Clear filter");
	expect(pressable(screen, "Done · 3")?.props.accessibilityState).toEqual({ expanded: false });
});

it("closed histories discover later live work, then retain one terminal output target after recovery", async () => {
	const options = { active: true, failed: false };
	const client = pagedHub(options);
	const screen = await mount(client);
	expect(pressable(screen, "Done · 3")?.props.accessibilityState).toEqual({ expanded: false });
	expect(pressable(screen, "Completed · 4")?.props.accessibilityState).toEqual({ expanded: false });
	const binding = subagentTree(hubId, coordinator.ref, "root");
	const second = published(
		(snapshot) => !!snapshot.tree?.root.entries.some((entry) => entry.kind === "shell" && entry.job.jobId === "job-7"),
	);
	await act(async () => {
		screen.root.findByType("FlatList" as never).props.onEndReached({ distanceFromEnd: 0 });
		await second;
	});
	const third = published(
		(snapshot) => !!snapshot.tree?.root.entries.some((entry) => entry.kind === "shell" && entry.job.jobId === "later"),
	);
	await act(async () => {
		screen.root.findByType("FlatList" as never).props.onEndReached({ distanceFromEnd: 0 });
		await third;
	});
	expect(laterRow(screen).props.row).toMatchObject({
		state: "running",
		job: { terminal: false, ownerRef: "local:root" },
	});
	expect(pressable(screen, "Completed · 11")?.props.accessibilityState).toEqual({ expanded: false });
	options.active = false;
	options.failed = true;
	await act(async () => {
		await binding.reload();
	});
	expect(renderedText(screen)).not.toContain("History task 11");
	await reconnect(screen, client, pagedHub(options));
	press(screen, "Completed · 12");
	const row = laterRow(screen);
	expect(row.props.row).toMatchObject({
		state: "failed",
		job: { status: "command_exited_nonzero", terminal: true, transcriptRef: "job:later" },
	});
	const buttons = screen.root.findAll(
		(node) =>
			String(node.type) === "Pressable" &&
			String(node.props.accessibilityLabel).startsWith("Shell job, History task 11,"),
	);
	expect(buttons).toHaveLength(1);
	act(() => buttons[0]?.props.onPress());
	expect(navigation.push).toHaveBeenCalledWith("ShellJob", {
		hubId,
		jobId: "later",
		ownerRef: "local:root",
		title: "History task 11",
		coordinator,
	});
});

it("keeps healthy rows and unknown counts until a missing owner recovers", async () => {
	const options = { missing: true };
	const client = pagedHub(options);
	const screen = await mount(client);
	press(screen, "Completed · 4");
	expect(renderedText(screen)).toContain("History task 0");
	expect(renderedText(screen)).toContain("Some activity under “failed-delegate” isn't listed.");
	expect(pressable(screen, "All, …")).toBeDefined();
	options.missing = false;
	await act(async () => {
		await subagentTree(hubId, coordinator.ref, "root").reload();
	});
	await loadThirdPage();
	expect(laterRow(screen).props.row.title).toBe("History task 11");
	expect(pressable(screen, "All, 15")).toBeDefined();
	expect(renderedText(screen)).not.toContain("isn't listed");
	expect(pressable(screen, "Completed · 12")?.props.accessibilityState).toEqual({ expanded: true });
});
