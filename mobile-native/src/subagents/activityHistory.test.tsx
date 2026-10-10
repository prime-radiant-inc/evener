// Recorded producers and real phone decisions, with only API and native platform edges substituted.
import { readFileSync } from "node:fs";
import { runInNewContext } from "node:vm";
import type { JobActivityJob, SessionDelegatesResponse, SessionJobsResponse } from "@evener/appwire-client";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { wireThread } from "@evener/appwire-client/testing/notifications";
import {
	subagentOutcomesDelegatesResponse,
	subagentResumedDelegatesResponse,
} from "@evener/appwire-client/testing/subagentWireFixtures";
import { act, type ReactTestRenderer } from "react-test-renderer";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { pressable, render, renderedText, screenConnection, unmountMountedTrees } from "../renderNative.testkit";
import { type ActivityListItem, activityListKey } from "./activityList";
import { forgetStopRequestsForHub } from "./nativeStopRequests";
import { flattenSubagents } from "./subagentModel";
import { forgetSubagentTrees, subagentTree, type SubagentTreeSnapshot } from "./subagentTree";
import { SubagentsScreen } from "./SubagentsScreen";

const harness = vi.hoisted(() => ({
	connection: {} as Record<string, unknown>,
	kv: new Map<string, string>(),
	focused: true,
}));
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
		useIsFocused: () => harness.focused,
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
const navigation = { push: vi.fn(), navigate: vi.fn(), setOptions: vi.fn() };

beforeEach(() => {
	forgetSubagentTrees(hubId);
	forgetStopRequestsForHub(hubId);
	vi.clearAllMocks();
	harness.focused = true;
});
afterEach(() => {
	unmountMountedTrees();
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

// Characterize the installed platform's content-length latch, not a second
// callback supplied by the test. Product paging and rendered decisions stay real.
function nativeEndBoundary(screen: ReactTestRenderer, visibleLength = 800, offset = 0) {
	const source = readFileSync(
		new URL("../../node_modules/@react-native/virtualized-lists/Lists/VirtualizedList.js", import.meta.url),
		"utf8",
	);
	const start = source.indexOf("  _maybeCallOnEdgeReached() {");
	const end = source.indexOf("\n  _onContentSizeChange =", start);
	if (start < 0 || end < start) throw new Error("installed native edge method not found");
	const platform = runInNewContext(`({${source.slice(start, end)}})`, { ON_EDGE_REACHED_EPSILON: 0.001 }) as {
		_maybeCallOnEdgeReached(): void;
	};
	let callbacks = 0;
	const list = () => screen.root.find((node) => node.props.keyExtractor === activityListKey);
	const owner = {
		props: {
			data: [] as ActivityListItem[],
			getItemCount: (data: ActivityListItem[]) => data.length,
			onEndReached: (event: { distanceFromEnd: number }) => {
				callbacks += 1;
				list().props.onEndReached(event);
			},
		},
		state: { pendingScrollUpdateCount: 0, cellsAroundViewport: { first: 0, last: 0 } },
		_listMetrics: { hasContentLength: () => true, getContentLength: () => 180 },
		_scrollMetrics: { visibleLength, offset },
		_sentEndForContentLength: 0,
		_sentStartForContentLength: 0,
	};
	return {
		check() {
			owner.props.data = list().props.data;
			owner.state.cellsAroundViewport.last = owner.props.data.length - 1;
			platform._maybeCallOnEdgeReached.call(owner);
		},
		get callbacks() {
			return callbacks;
		},
	};
}

function heldSecondPage() {
	const client = pagedHub({ active: true });
	const entered = deferred<void>();
	const answer = deferred<SessionJobsResponse>();
	client.on("evener/thread/jobs/list", ({ scope, cursor }) => {
		if (cursor === "page-2") {
			entered.resolve();
			return answer.promise;
		}
		const page = cursor === "page-3" ? 2 : 0;
		return {
			context,
			scope: scope ?? "session",
			jobs: Array.from({ length: 4 }, (_, offset) =>
				job(page * 4 + offset, page === 2 && offset === 3 ? { terminal: false, status: "running" } : {}),
			),
			page: { complete: page === 2, issues: [], ...(page === 0 ? { nextCursor: "page-2" } : {}) },
		};
	});
	return {
		client,
		entered: entered.promise,
		answer,
		respond(over: Partial<JobActivityJob> = {}) {
			answer.resolve({
				context,
				scope: "subtree",
				jobs: Array.from({ length: 4 }, (_, offset) => job(4 + offset, offset === 3 ? over : {})),
				page: { complete: false, issues: [], nextCursor: "page-3" },
			});
		},
	};
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
	const boundary = nativeEndBoundary(screen);
	act(() => boundary.check());
	await act(async () => {
		await second;
	});
	act(() => boundary.check());
	expect(boundary.callbacks).toBe(1);
	expect(cursors(client)).toEqual([undefined, "page-2", "page-3"]);
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

it("one native edge callback discovers a terminal parent's page-three active descendant with Done closed", async () => {
	const recorded = subagentOutcomesDelegatesResponse().delegates.find((delegate) => delegate.outcome === "failed");
	if (!recorded) throw new Error("missing recorded failed producer");
	const client = hub();
	client.on("evener/thread/activity/read", () => ({
		context,
		scope: "subtree",
		delegates: { known: true, total: 101, active: 1, failed: 100, completed: 0 },
		jobs: emptyCounts,
		watches: emptyCounts,
	}));
	client.on("evener/thread/delegates/list", ({ cursor }) => {
		const page = cursor === "page-3" ? 2 : cursor === "page-2" ? 1 : 0;
		return {
			context,
			scope: "subtree",
			delegates: Array.from({ length: page === 2 ? 1 : 50 }, (_, offset) => {
				const index = page * 50 + offset;
				return page === 2
					? {
							...recorded,
							delegateId: "active-descendant",
							ownerRef: "local:child-0",
							childRef: "local:active-descendant",
							parentDelegateId: "failed-parent-0",
							name: "Active descendant",
							lifecycle: "running",
							phase: "running",
							status: "running",
							terminal: false,
							outcome: undefined,
							reason: undefined,
							reportPreview: undefined,
							runEndedAt: undefined,
						}
					: {
							...recorded,
							delegateId: `failed-parent-${index}`,
							ownerRef: "local:root",
							childRef: `local:child-${index}`,
							name: `Failed parent ${index}`,
						};
			}),
			page: { complete: page === 2, issues: [], ...(page < 2 ? { nextCursor: `page-${page + 2}` } : {}) },
		};
	});
	const screen = await mount(client);
	expect(pressable(screen, "Done · 50")?.props.accessibilityState).toEqual({ expanded: false });
	const boundary = nativeEndBoundary(screen);
	const second = published((snapshot) => flattenSubagents(snapshot.tree!).some((row) => row.id === "failed-parent-99"));
	act(() => boundary.check());
	await act(async () => {
		await second;
	});
	act(() => boundary.check());
	expect(boundary.callbacks).toBe(1);
	expect(
		client.calls
			.filter((call) => call.method === "evener/thread/delegates/list")
			.map((call) => (call.params as { cursor?: string }).cursor),
	).toEqual([undefined, "page-2", "page-3"]);
	const row = screen.root.find((node) => node.props.row?.id === "active-descendant").props.row;
	expect(row).toMatchObject({
		state: "running",
		active: true,
		parentTitle: "Failed parent 0",
		ref: "local:active-descendant",
	});
	expect(pressable(screen, "Done · 100")?.props.accessibilityState).toEqual({ expanded: false });
	expect(renderedText(screen)).toContain("RUNNING · 1");
});

it("new visible rows retire old closed-fold demand until the native edge is observed again", async () => {
	const held = heldSecondPage();
	const screen = await mount(held.client);
	act(() => nativeEndBoundary(screen).check());
	await held.entered;
	await act(async () => held.respond({ terminal: false, status: "running", endedAt: undefined }));
	expect(cursors(held.client)).toEqual([undefined, "page-2"]);
	expect(screen.root.find((node) => node.props.row?.id === "job-7").props.row.state).toBe("running");
	expect(subagentTree(hubId, coordinator.ref, "root").getSnapshot().hasMore).toBe(true);
});

it("scrolling away retires admitted demand before a same-height page completes", async () => {
	const held = heldSecondPage();
	const screen = await mount(held.client);
	act(() => nativeEndBoundary(screen, 100, 80).check());
	await held.entered;
	act(() =>
		screen.root
			.find((node) => node.props.keyExtractor === activityListKey)
			.props.onScroll({
				nativeEvent: {
					contentSize: { width: 393, height: 180 },
					contentOffset: { x: 0, y: 0 },
					layoutMeasurement: { width: 393, height: 100 },
				},
			}),
	);
	await act(async () => held.respond());
	expect(cursors(held.client)).toEqual([undefined, "page-2"]);
	expect(pressable(screen, "Completed · 8")?.props.accessibilityState).toEqual({ expanded: false });
});

it("changed content geometry retires old demand until the native list observes its edge again", async () => {
	const held = heldSecondPage();
	const screen = await mount(held.client);
	const list = () => screen.root.find((node) => node.props.keyExtractor === activityListKey);
	act(() => list().props.onContentSizeChange(393, 180));
	act(() => nativeEndBoundary(screen).check());
	await held.entered;
	act(() => list().props.onContentSizeChange(393, 1000));
	await act(async () => held.respond());
	expect(cursors(held.client)).toEqual([undefined, "page-2"]);
	expect(pressable(screen, "Completed · 8")?.props.accessibilityState).toEqual({ expanded: false });
});

it("an unfocused list pauses demand and returning resumes the same visible boundary", async () => {
	const held = heldSecondPage();
	const screen = await mount(held.client);
	act(() => nativeEndBoundary(screen).check());
	await held.entered;
	act(() => {
		harness.focused = false;
		screen.update(element());
	});
	await act(async () => held.respond());
	expect(cursors(held.client)).toEqual([undefined, "page-2"]);
	await act(async () => {
		harness.focused = true;
		screen.update(element());
	});
	expect(cursors(held.client)).toEqual([undefined, "page-2", "page-3"]);
	expect(laterRow(screen).props.row.state).toBe("running");
});

it("a failed continuation retains healthy history and recovers through shared backoff, not view retries", async () => {
	vi.useFakeTimers();
	try {
		const held = heldSecondPage();
		const screen = await mount(held.client);
		act(() => nativeEndBoundary(screen).check());
		await held.entered;
		const error = new Error("temporary owner failure");
		await act(async () => held.answer.reject(error));
		expect(cursors(held.client)).toEqual([undefined, "page-2"]);
		expect(pressable(screen, "Completed · 4")?.props.accessibilityState).toEqual({ expanded: false });
		expect(subagentTree(hubId, coordinator.ref, "root").getSnapshot().pages?.jobs.error).toBe(error);
		held.client.on("evener/thread/jobs/list", ({ cursor }) => {
			const page = cursor === "page-3" ? 2 : cursor === "page-2" ? 1 : 0;
			return {
				context,
				scope: "subtree",
				jobs: Array.from({ length: 4 }, (_, offset) =>
					job(page * 4 + offset, page === 2 && offset === 3 ? { terminal: false, status: "running" } : {}),
				),
				page: { complete: page === 2, issues: [], ...(page < 2 ? { nextCursor: `page-${page + 2}` } : {}) },
			};
		});
		await act(async () => {
			await vi.advanceTimersByTimeAsync(999);
		});
		expect(cursors(held.client)).toEqual([undefined, "page-2"]);
		await act(async () => {
			await vi.advanceTimersByTimeAsync(1);
		});
		expect(cursors(held.client)).toEqual([undefined, "page-2", "page-2", "page-3"]);
		expect(laterRow(screen).props.row.state).toBe("running");
		expect(pressable(screen, "Completed · 11")?.props.accessibilityState).toEqual({ expanded: false });
	} finally {
		vi.useRealTimers();
	}
});

it("disposing the list drops demand and rejects the delayed admitted page", async () => {
	const held = heldSecondPage();
	const screen = await mount(held.client);
	const binding = subagentTree(hubId, coordinator.ref, "root");
	act(() => nativeEndBoundary(screen).check());
	await held.entered;
	act(() => screen.unmount());
	await act(async () => held.respond());
	expect(cursors(held.client)).toEqual([undefined, "page-2"]);
	expect(binding.getSnapshot().tree?.root.entries.filter((entry) => entry.kind === "shell")).toHaveLength(4);
	expect(binding.getSnapshot().pages).toBeNull();
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
