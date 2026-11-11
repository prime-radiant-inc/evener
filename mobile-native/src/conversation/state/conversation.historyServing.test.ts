import type { ThreadHistoryReadResponse, ThreadHistoryTurnsListResponse, Turn } from "@evener/appwire-client";
import { FakeSocket } from "@evener/appwire-client/testing/fakeSocket";
import { describe, expect, it } from "vitest";
import { createHubClient } from "../../connection";
import { createConversationService } from "../services/conversation";
import { createConversationStore, olderPageKey } from "./conversation";
import { ALL_TRUE_CAPS, historyUpdated, makeThread } from "./conversationTestUtils";

const ref = "local:child";
const liveIdentity = { bootGeneration: "7", epoch: 3, snapshot: { incarnation: "live-child", length: 60 } };
const liveFacts = { servedFrom: "live-daemon", asOf: "2026-11-08T20:00:00Z", instanceId: "root-daemon" } as const;

function message(entry: number): Turn {
	return {
		id: `turn-${entry}`,
		status: "completed",
		itemsView: "fragment",
		items: [
			{
				id: `item-${entry}`,
				turnId: `turn-${entry}`,
				type: "agentMessage",
				text: `sentinel-${entry}`,
				version: entry,
				position: { entry, item: 0 },
			},
		],
	};
}

function tool(version: number, output?: string): Turn {
	return {
		id: "tool-turn",
		status: "completed",
		itemsView: "fragment",
		items: [
			{
				id: "tool",
				turnId: "tool-turn",
				type: "commandExecution",
				callId: "call",
				status: "completed",
				version,
				position: { entry: 30, item: 0 },
				...(output === undefined ? {} : { output }),
			},
		],
	};
}

class HistorySocket extends FakeSocket {
	read: ThreadHistoryReadResponse = {
		...liveIdentity,
		...liveFacts,
		olderCursor: "page-2",
		thread: makeThread({
			id: "child",
			sessionId: "child",
			turns: [tool(60, "newer-output"), message(40)],
			evener: { ref, instanceId: "root-daemon", capabilities: ALL_TRUE_CAPS, queue: { revision: 1 }, askPending: true },
		}),
	};
	readonly pages: string[] = [];
	// Every older-page request, so a test can assert the shape it asked with.
	readonly pageRequests: Array<{ cursor?: string; before?: unknown }> = [];
	// Pages served per cursor; a test can make a page omit a held identity
	// inside the range it covers.
	pageTurns: Record<string, Turn[]> = { "page-2": [message(20)], "page-3": [message(10)] };
	readonly reads: Array<{ incarnation: string; length: number } | undefined> = [];
	staleOnce = false;
	override send(data: string) {
		super.send(data);
		const request = JSON.parse(data) as {
			id?: number;
			method: string;
			params?: { cursor?: string; heldSnapshot?: { incarnation: string; length: number } };
		};
		if (request.id === undefined || request.method === "initialize" || request.method === "ping") return;
		let result: object;
		if (request.method === "evener/thread/history/read") {
			this.reads.push(request.params?.heldSnapshot);
			if (this.staleOnce) {
				this.staleOnce = false;
				queueMicrotask(() =>
					this.receive({
						jsonrpc: "2.0",
						id: request.id,
						error: { code: -32000, message: "stale", data: { evenerErrorInfo: "transcriptItemCursorStale" } },
					}),
				);
				return;
			}
			result = this.read;
		} else if (request.method === "evener/thread/history/turns/list" || request.method === "thread/turns/list") {
			const cursor = request.params?.cursor ?? "";
			this.pages.push(cursor);
			this.pageRequests.push(request.params ?? {});
			result = {
				...liveIdentity,
				...liveFacts,
				data: this.pageTurns[cursor] ?? [],
				nextCursor: cursor === "page-2" ? "page-3" : "page-4",
			} satisfies ThreadHistoryTurnsListResponse;
		} else if (request.method === "thread/unsubscribe") result = {};
		else throw new Error(`Unexpected RPC ${request.method}`);
		queueMicrotask(() => this.receive({ jsonrpc: "2.0", id: request.id, result }));
	}
}

async function fixture() {
	const socket = new HistorySocket({ autoInitialize: true });
	const client = createHubClient("https://scripted.test", "", () => {
		queueMicrotask(() => socket.open());
		return socket;
	});
	await client.connect();
	const service = createConversationService(client);
	const store = createConversationStore();
	await store.getState().openProjected(service, ref);
	await store.getState().loadOlder(service);
	await store.getState().loadOlder(service);
	store.getState().setDraft("unsent-sentinel");
	return {
		socket,
		service,
		store,
		close: () => {
			store.getState().close();
			service.close();
			client.close();
		},
	};
}

describe("native history serving through the real client and service", () => {
	it.each([
		["child", false],
		["root-daemon", false],
		["child", true],
		["root-daemon", true],
	] as const)(
		"accumulates fallback with %s instance, stale retry %s, without losing payload, pages or fresh controls, then recovers live",
		async (savedInstance, staleRetry) => {
			const { socket, service, store, close } = await fixture();
			try {
				const retained = () => {
					const conversation = store.getState().conversation;
					expect(conversation?.turns.flatMap((turn) => turn.items).map((item) => item.id)).toEqual([
						"item-10",
						"item-20",
						"tool",
						"item-40",
					]);
					expect(conversation?.items.some((item) => item.id === "item-10")).toBe(true);
					expect(conversation?.turns.flatMap((turn) => turn.items).find((item) => item.id === "tool")).toMatchObject({
						version: 60,
						output: "newer-output",
					});
					expect(conversation?.history).toMatchObject(liveIdentity.snapshot);
					expect(store.getState().olderCursor).toBe("page-4");
					expect(store.getState().draft).toBe("unsent-sentinel");
				};
				expect(socket.pages).toEqual(["page-2", "page-3"]);
				retained();
				socket.read = {
					servedFrom: "saved-index",
					fallbackReason: "live-read-failed",
					asOf: "2026-11-08T19:00:00Z",
					snapshot: { incarnation: "saved-child", length: 31 },
					olderCursor: "saved-page",
					thread: makeThread({
						id: "child",
						sessionId: "child",
						status: { type: "stopped" },
						turns: [tool(30)],
						evener: {
							ref,
							instanceId: savedInstance,
							capabilities: { ...ALL_TRUE_CAPS, send: false },
							queue: { revision: 9 },
							askPending: false,
							resumeRequired: true,
						},
					}),
				};
				const readCount = socket.reads.length;
				socket.staleOnce = staleRetry;
				await store.getState().rehydrate(service);
				expect(store.getState().error).toBeNull();
				expect(socket.reads.slice(readCount)).toEqual(
					staleRetry ? [liveIdentity.snapshot, undefined] : [liveIdentity.snapshot],
				);
				retained();
				expect(store.getState().conversation).toMatchObject({
					instanceId: savedInstance,
					capabilities: { send: false },
					queue: { revision: 9 },
					askPending: false,
					resumeRequired: true,
					status: { type: "stopped" },
					servingFacts: { servedFrom: "saved-index", fallbackReason: "live-read-failed" },
				});
				socket.read = {
					...liveIdentity,
					snapshot: { ...liveIdentity.snapshot, length: 61 },
					...liveFacts,
					olderCursor: "page-2",
					thread: makeThread({
						id: "child",
						sessionId: "child",
						turns: [tool(61, "recovered-output"), message(40)],
						evener: {
							ref,
							instanceId: "root-daemon",
							capabilities: ALL_TRUE_CAPS,
							queue: { revision: 10 },
							askPending: false,
							resumeRequired: false,
						},
					}),
				};
				await store.getState().rehydrate(service);
				expect(store.getState().error).toBeNull();
				expect(store.getState().conversation).toMatchObject({
					instanceId: "root-daemon",
					capabilities: { send: true },
					queue: { revision: 10 },
					resumeRequired: false,
					servingFacts: { servedFrom: "live-daemon" },
				});
				expect(
					store
						.getState()
						.conversation?.turns.flatMap((turn) => turn.items)
						.map((item) => item.id),
				).toEqual(["item-10", "item-20", "tool", "item-40"]);
				expect(
					store
						.getState()
						.conversation?.turns.flatMap((turn) => turn.items)
						.find((item) => item.id === "tool"),
				).toMatchObject({ version: 61, output: "recovered-output" });
				expect(store.getState().olderCursor).toBe("page-4");
			} finally {
				close();
			}
		},
	);

	it.each([
		["thread", "live-read-failed"],
		["thread", "live-read-empty"],
		["session", "live-read-failed"],
		["session", "live-read-empty"],
	] as const)("replaces history when the resolved %s owner changes under %s fallback", async (owner, reason) => {
		const { socket, service, store, close } = await fixture();
		try {
			const threadId = owner === "thread" ? "replacement-thread" : "child";
			const sessionId = owner === "session" ? "replacement-session" : "child";
			socket.read = {
				servedFrom: "saved-index",
				fallbackReason: reason,
				asOf: "2026-11-08T19:00:00Z",
				snapshot: { incarnation: "saved-replacement", length: 81 },
				olderCursor: "replacement-page",
				thread: makeThread({
					id: threadId,
					sessionId,
					status: { type: "stopped" },
					turns: [message(80)],
					evener: {
						ref,
						instanceId: "root-daemon",
						capabilities: { ...ALL_TRUE_CAPS, send: false },
						queue: { revision: 9 },
						resumeRequired: true,
					},
				}),
			};
			const readCount = socket.reads.length;
			await store.getState().rehydrate(service);
			expect(socket.reads.length).toBe(readCount + 1);
			expect(store.getState().error).toBeNull();
			const conversation = store.getState().conversation;
			expect(conversation).toMatchObject({
				threadId,
				imageSessionId: sessionId,
				instanceId: "root-daemon",
				queue: { revision: 9 },
				capabilities: { send: false },
				servingFacts: { servedFrom: "saved-index", fallbackReason: reason },
			});
			expect(conversation?.turns.flatMap((turn) => turn.items).map((item) => item.id)).toEqual(["item-80"]);
			expect(conversation?.history).toMatchObject({ incarnation: "saved-replacement", length: 81 });
			expect(store.getState().olderCursor).toBe("replacement-page");
			expect(store.getState().draft).toBe("unsent-sentinel");
		} finally {
			close();
		}
	});

	it("pages a saved fallback cursor-free and keeps the live continuation", async () => {
		const socket = new HistorySocket({ autoInitialize: true });
		const live = {
			...liveIdentity,
			...liveFacts,
			olderCursor: "live-cursor",
			thread: makeThread({
				id: "child",
				sessionId: "child",
				turns: [message(40)],
				evener: {
					ref,
					instanceId: "root-daemon",
					capabilities: ALL_TRUE_CAPS,
					queue: { revision: 1 },
					askPending: true,
				},
			}),
		};
		socket.read = live;
		socket.pageTurns = { "": [message(20)] };
		const client = createHubClient("https://scripted.test", "", () => {
			queueMicrotask(() => socket.open());
			return socket;
		});
		await client.connect();
		const service = createConversationService(client);
		const store = createConversationStore();
		try {
			await store.getState().openProjected(service, ref);
			// The hub falls back to the saved index; the live continuation stays.
			socket.read = {
				servedFrom: "saved-index",
				fallbackReason: "live-read-failed",
				asOf: "2026-11-08T19:00:00Z",
				snapshot: { incarnation: "saved-child", length: 41 },
				olderCursor: "live-cursor",
				thread: makeThread({
					id: "child",
					sessionId: "child",
					status: { type: "stopped" },
					turns: [message(40)],
					evener: {
						ref,
						instanceId: "child",
						capabilities: { ...ALL_TRUE_CAPS, send: false },
						queue: { revision: 1 },
						askPending: true,
					},
				}),
			};
			await store.getState().rehydrate(service);
			expect(store.getState().olderCursor).toBe("live-cursor");
			socket.pageRequests.length = 0;
			await store.getState().loadOlder(service);
			const request = socket.pageRequests.at(-1);
			expect(request?.cursor).toBeUndefined();
			expect(request?.before).toBeDefined();
			const ids = store
				.getState()
				.conversation?.turns.flatMap((turn) => turn.items)
				.map((item) => item.id);
			expect(ids).toContain("item-20");
			expect(store.getState().olderCursor).toBe("live-cursor");
			// The saved view's own boundary advances, so paging is not stuck on
			// the retained live cursor.
			expect(olderPageKey(store.getState())).toBe(JSON.stringify(["saved", "child/child/child", 20, 0]));
			// An empty saved page ends the view instead of re-requesting forever.
			socket.pageTurns = { "": [] };
			await store.getState().loadOlder(service);
			expect(olderPageKey(store.getState())).toBeNull();
			// Retention trimming drops the rows the view had paged down to,
			// moving its next-page boundary: exhaustion belonged to the old
			// boundary and must not keep scrollback off.
			const exhausted = store.getState().conversation;
			if (exhausted === null) throw new Error("the saved view must be installed");
			const liveRows = Array.from({ length: 600 }, (_, i) => ({
				kind: "user" as const,
				id: `live-item-${i}`,
				text: `live ${i}`,
				position: { entry: 100 + i, item: 0 },
			}));
			const liveTurns = liveRows.map((row) => ({
				id: `live-turn-${row.position.entry}`,
				status: "completed",
				items: [
					{
						id: row.id,
						turnId: `live-turn-${row.position.entry}`,
						type: "agentMessage",
						text: row.text,
						position: row.position,
					},
				],
			}));
			store.setState({ conversation: { ...exhausted, turns: liveTurns, items: liveRows } });
			store.getState().setFollowingLiveEnd(false);
			store.getState().setFollowingLiveEnd(true);
			expect(store.getState().conversation?.items).toHaveLength(500);
			expect(olderPageKey(store.getState())).not.toBeNull();
		} finally {
			store.getState().close();
			service.close();
			client.close();
		}
	});

	it("keeps a page omission when a delayed fallback still carries the omitted row", async () => {
		const socket = new HistorySocket({ autoInitialize: true });
		socket.read = {
			...liveIdentity,
			...liveFacts,
			snapshot: { incarnation: "live-child", length: 60 },
			olderCursor: "page-2",
			thread: makeThread({
				id: "child",
				sessionId: "child",
				turns: [message(20), message(25), message(30)],
				evener: {
					ref,
					instanceId: "root-daemon",
					capabilities: ALL_TRUE_CAPS,
					queue: { revision: 1 },
					askPending: true,
				},
			}),
		};
		// A later page covers entries 20..30 while omitting entry 25.
		socket.pageTurns = { "page-2": [message(20), message(30)], "page-3": [message(10)] };
		const client = createHubClient("https://scripted.test", "", () => {
			queueMicrotask(() => socket.open());
			return socket;
		});
		await client.connect();
		const service = createConversationService(client);
		const store = createConversationStore();
		try {
			await store.getState().openProjected(service, ref);
			await store.getState().loadOlder(service);
			const ids = () =>
				store
					.getState()
					.conversation?.turns.flatMap((turn) => turn.items)
					.map((item) => item.id);
			expect(ids()).toEqual(["item-20", "item-30"]);
			// A delayed saved fallback still carries entry 25; the newer page's
			// observation of that range must keep it omitted.
			socket.read = {
				servedFrom: "saved-index",
				fallbackReason: "live-read-failed",
				asOf: "2026-11-08T19:00:00Z",
				snapshot: { incarnation: "live-child", length: 31 },
				olderCursor: "page-2",
				thread: makeThread({
					id: "child",
					sessionId: "child",
					turns: [message(20), message(25), message(30)],
					evener: {
						ref,
						instanceId: "root-daemon",
						capabilities: ALL_TRUE_CAPS,
						queue: { revision: 1 },
						askPending: true,
					},
				}),
			};
			await store.getState().rehydrate(service);
			expect(ids()).not.toContain("item-25");
			expect(store.getState().conversation?.history?.replacementScopes?.length).toBeGreaterThan(0);
		} finally {
			store.getState().close();
			service.close();
			client.close();
		}
	});

	it("keeps a newer live tool output when a delayed read's older copy omits it", async () => {
		const socket = new HistorySocket({ autoInitialize: true });
		socket.read = {
			...liveIdentity,
			...liveFacts,
			olderCursor: "page-2",
			thread: makeThread({
				id: "child",
				sessionId: "child",
				turns: [tool(60), message(40)],
				evener: {
					ref,
					instanceId: "root-daemon",
					capabilities: ALL_TRUE_CAPS,
					queue: { revision: 1 },
					askPending: true,
				},
			}),
		};
		const client = createHubClient("https://scripted.test", "", () => {
			queueMicrotask(() => socket.open());
			return socket;
		});
		await client.connect();
		const service = createConversationService(client);
		const store = createConversationStore();
		try {
			await store.getState().openProjected(service, ref);
			// A live frame settles the tool at a newer version, output included,
			// while the read below still carries the earlier version without it.
			store.getState().applyNotification(
				historyUpdated({
					threadId: "child",
					ref,
					bootGeneration: "7",
					epoch: 3,
					snapshot: { incarnation: "live-child", length: 61 },
					items: [
						{
							id: "tool",
							turnId: "tool-turn",
							type: "commandExecution",
							callId: "call",
							status: "completed",
							version: 61,
							position: { entry: 30, item: 0 },
							output: "live-output",
						},
					],
				}),
			);
			socket.read = {
				...liveIdentity,
				snapshot: { incarnation: "live-child", length: 61 },
				...liveFacts,
				olderCursor: "page-2",
				thread: makeThread({
					id: "child",
					sessionId: "child",
					turns: [tool(60), message(40)],
					evener: {
						ref,
						instanceId: "root-daemon",
						capabilities: ALL_TRUE_CAPS,
						queue: { revision: 1 },
						askPending: true,
					},
				}),
			};
			await store.getState().rehydrate(service);
			const heldTool = store
				.getState()
				.conversation?.turns.flatMap((turn) => turn.items)
				.find((item) => item.id === "tool");
			expect(heldTool).toMatchObject({ version: 61, output: "live-output" });
		} finally {
			store.getState().close();
			service.close();
			client.close();
		}
	});

	it("replaces a genuinely new nonfallback instance and its older cursor", async () => {
		const { socket, service, store, close } = await fixture();
		try {
			socket.read = {
				...liveFacts,
				instanceId: "replacement",
				bootGeneration: "8",
				epoch: 1,
				snapshot: { incarnation: "replacement", length: 81 },
				olderCursor: "replacement-page",
				thread: makeThread({
					id: "child",
					sessionId: "child",
					turns: [message(80)],
					evener: { ref, instanceId: "replacement", capabilities: ALL_TRUE_CAPS, queue: { revision: 0 } },
				}),
			};
			await store.getState().rehydrate(service);
			expect(store.getState().conversation?.instanceId).toBe("replacement");
			expect(
				store
					.getState()
					.conversation?.turns.flatMap((turn) => turn.items)
					.map((item) => item.id),
			).toEqual(["item-80"]);
			expect(store.getState().olderCursor).toBe("replacement-page");
			expect(store.getState().draft).toBe("unsent-sentinel");
		} finally {
			close();
		}
	});
});
