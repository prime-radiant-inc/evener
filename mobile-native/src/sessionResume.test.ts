import { describe, expect, it } from "vitest";
import type { WebSocketLike } from "../../appwire-client/typescript/transport";
import type {
	AnyNotification,
	Thread,
} from "../../appwire-client/typescript/types.gen";
import { createConversationService } from "../../mobile/src/services/conversation";
import {
	createConversationStore,
	type LiveActivitySink,
} from "../../mobile/src/state/conversation";
import { createHubClient } from "./connection";
import { SessionControls } from "./sessionControls";

const thread: Thread = {
	id: "thread-1",
	sessionId: "session-1",
	preview: "saved",
	ephemeral: false,
	modelProvider: "scripted",
	createdAt: 1,
	updatedAt: 1,
	status: { type: "stopped" },
	cwd: "/tmp",
	cliVersion: "test",
	source: "local",
	turns: [],
	evener: {
		ref: "local:thread-1",
		instanceId: "instance-1",
		resumeRequired: true,
		queue: { revision: 0 },
		capabilities: {
			send: true,
			steer: false,
			interrupt: false,
			compact: false,
			clear: false,
			forkFromTurn: false,
			shutdown: false,
			changeModel: false,
			changeVisionModel: false,
			queue: false,
			goal: false,
			rename: false,
		},
	},
};

const initialize = {
	serverInfo: { name: "scripted", version: "1" },
	protocolVersion: "evener-appwire-v5" as const,
	sourceId: "local",
	features: Object.fromEntries(
		[
			"threadList",
			"threadTurnsList",
			"turnStart",
			"turnSteer",
			"threadClear",
			"threadShutdown",
			"forkFromTurn",
			"tasks",
			"transcriptList",
			"modelList",
			"directoryComplete",
			"auth",
		].map((key) => [key, true]),
	),
};

function deferred<T>() {
	let resolve!: (value: T) => void;
	const promise = new Promise<T>((r) => (resolve = r));
	return { promise, resolve };
}

class ExternalScriptedWebSocket implements WebSocketLike {
	onopen: (() => void) | null = null;
	onmessage: ((event: { data: unknown }) => void) | null = null;
	onerror: (() => void) | null = null;
	onclose: ((event: { code: number }) => void) | null = null;
	readonly methods: string[] = [];
	constructor(
		private readonly state: {
			readBarrier?: Promise<void>;
			ackBarrier?: Promise<void>;
			readCount: number;
			resumed: boolean;
		},
	) {}
	send(data: string) {
		const request = JSON.parse(data) as { id: number; method: string };
		this.methods.push(request.method);
		const result = async () => {
			if (request.method === "initialize") return initialize;
			if (request.method === "initialized") return {};
			if (request.method === "thread/read") {
				this.state.readCount += 1;
				if (this.state.readBarrier && this.state.readCount === 2)
					await this.state.readBarrier;
				return {
					thread: {
						...thread,
						evener: {
							...thread.evener,
							resumeRequired: !this.state.resumed,
						},
					},
				};
			}
			if (request.method === "thread/resume") {
				if (this.state.ackBarrier) await this.state.ackBarrier;
				this.state.resumed = true;
				return {
					thread: {
						...thread,
						evener: { ...thread.evener, resumeRequired: false },
					},
				};
			}
			throw new Error(`unexpected RPC ${request.method}`);
		};
		void result().then((value) =>
			queueMicrotask(() =>
				this.onmessage?.({
					data: JSON.stringify({
						jsonrpc: "2.0",
						id: request.id,
						result: value,
					}),
				}),
			),
		);
	}
	close() {
		this.onclose?.({ code: 1000 });
	}
}

const sink: LiveActivitySink = {
	setLiveView: () => true,
	applyLiveNotification: (_n: AnyNotification, _identity) => "applied",
	setLiveCapabilities: () => true,
	reset: () => {},
};

async function connectedFixture(
	readBarrier?: Promise<void>,
	ackBarrier?: Promise<void>,
) {
	const sockets: ExternalScriptedWebSocket[] = [];
	const state = { readBarrier, ackBarrier, readCount: 0, resumed: false };
	const client = createHubClient("https://hub.test", "", () => {
		const socket = new ExternalScriptedWebSocket(state);
		sockets.push(socket);
		queueMicrotask(() => socket.onopen?.());
		return socket;
	});
	await client.connect();
	const service = createConversationService(client);
	const store = createConversationStore();
	await store.getState().openProjected(service, sink, "local:thread-1");
	return { client, service, store, sockets, state };
}

describe("native resume recovery integration", () => {
	it.each([false, true])(
		"refreshes only the retained destination after recovery (navigated away: %s)",
		async (navigateAway) => {
			const barrier = deferred<void>();
			const ack = deferred<void>();
			const { client, service, store, sockets, state } = await connectedFixture(
				barrier.promise,
				ack.promise,
			);
			let controls!: SessionControls;
			let refreshes = 0;
			const ready = deferred<void>();
			const preAck = deferred<void>();
			let destination = true;
			const bindingGeneration = store.getState().conversationGeneration;
			const refresh = async () => {
				refreshes += 1;
				if (store.getState().status === "open")
					await store.getState().rehydrate(service, sink);
				else
					await store
						.getState()
						.resumeProjected(service, sink, "local:thread-1");
			};
			const unsubscribe = client.onStateChange((connectionState) => {
				if (connectionState === "reconnecting") {
					store.getState().suspendProjected();
					service.close();
					controls.dispose();
				}
				if (connectionState === "ready" && sockets.length === 2) {
					ready.resolve();
					void refresh().then(() => preAck.resolve());
				}
			});
			controls = new SessionControls(
				service,
				refresh,
				() => {},
				(scope) =>
					destination &&
					(scope === "destination" ||
						store.getState().conversationGeneration === bindingGeneration),
				() => null,
				() => false,
			);
			try {
				expect(store.getState().conversation?.resumeRequired).toBe(true);
				const resume = controls.resume();
				await ready.promise;
				expect(sockets).toHaveLength(2);
				expect(store.getState().status).toBe("opening");
				expect(sockets[1]?.methods).toContain("thread/read");
				barrier.resolve();
				await preAck.promise;
				expect(state.readCount).toBe(2);
				expect(store.getState().status).toBe("open");
				expect(store.getState().conversation?.resumeRequired).toBe(true);
				expect(store.getState().conversationGeneration).not.toBe(
					bindingGeneration,
				);
				// Navigation can retire the destination while the daemon acknowledgement is held.
				destination = !navigateAway;
				ack.resolve();
				await resume;
				expect(state.readCount).toBe(navigateAway ? 2 : 3);
				expect(refreshes).toBe(navigateAway ? 1 : 2);
				expect(store.getState().conversation?.resumeRequired).toBe(
					navigateAway,
				);
				expect(controls.getSnapshot().notice).toBe(null);
			} finally {
				barrier.resolve();
				ack.resolve();
				unsubscribe();
				store.getState().close();
				service.close();
				client.close();
			}
		},
	);
});
