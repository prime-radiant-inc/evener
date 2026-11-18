import { APPWIRE_PROTOCOL_VERSION } from "@evener/appwire-client";
import { describe, expect, it } from "vitest";
import type { Thread, WebSocketLike } from "@evener/appwire-client";
import { createConversationService } from "./conversation/services/conversation";
import { createConversationStore } from "./conversation/state/conversation";
import { createHubClient, ResumeAbandoned, ResumeNotDispatched } from "./connection";
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
			sharedNotes: false,
			queue: false,
			goal: false,
			rename: false,
		},
	},
};

const initialize = {
	serverInfo: { name: "scripted", version: "1" },
	protocolVersion: APPWIRE_PROTOCOL_VERSION,
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
			readStarted: () => void;
			readCount: number;
			resumed: boolean;
		},
	) {}
	send(data: string) {
		const request = JSON.parse(data) as { id: number; method: string };
		this.methods.push(request.method);
		if (request.method === "evener/thread/history/read" || request.method === "evener/thread/history/turns/list") {
			queueMicrotask(() =>
				this.onmessage?.({
					data: JSON.stringify({
						jsonrpc: "2.0",
						id: request.id,
						error: { code: -32601, message: "Method not found" },
					}),
				}),
			);
			return;
		}
		const result = async () => {
			if (request.method === "initialize") return initialize;
			if (request.method === "initialized" || request.method === "thread/unsubscribe") return {};
			if (request.method === "thread/read") {
				this.state.readCount += 1;
				if (this.state.readCount === 2) this.state.readStarted();
				if (this.state.readBarrier && this.state.readCount === 2) await this.state.readBarrier;
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

async function connectedFixture(readBarrier?: Promise<void>, ackBarrier?: Promise<void>, haltDialAfter?: number) {
	const sockets: ExternalScriptedWebSocket[] = [];
	const readStarted = deferred<void>();
	const secondDial = deferred<void>();
	const state = { readBarrier, ackBarrier, readStarted: () => readStarted.resolve(), readCount: 0, resumed: false };
	const client = createHubClient("https://hub.test", "", () => {
		const socket = new ExternalScriptedWebSocket(state);
		sockets.push(socket);
		if (sockets.length === 2) secondDial.resolve();
		// A dial past this point never opens: the handshake a recycling resume
		// waits on is one that does not finish in this test's lifetime, which is
		// what a phone that lost its connection mid-recycle looks like.
		if (haltDialAfter !== undefined && sockets.length > haltDialAfter) return socket;
		queueMicrotask(() => socket.onopen?.());
		return socket;
	});
	await client.connect();
	const service = createConversationService(client);
	const store = createConversationStore();
	await store.getState().openProjected(service, "local:thread-1");
	return { client, service, store, sockets, state, readStarted, secondDial };
}

describe("native resume recovery integration", () => {
	it.each([false, true])(
		"refreshes only the retained destination after recovery (navigated away: %s)",
		async (navigateAway) => {
			const barrier = deferred<void>();
			const ack = deferred<void>();
			const { client, service, store, sockets, state, readStarted } = await connectedFixture(
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
				if (store.getState().status === "open") await store.getState().rehydrate(service);
				else await store.getState().resumeProjected(service, "local:thread-1");
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
					destination && (scope === "destination" || store.getState().conversationGeneration === bindingGeneration),
				() => null,
				() => false,
			);
			try {
				expect(store.getState().conversation?.resumeRequired).toBe(true);
				const resume = controls.resume();
				await ready.promise;
				await readStarted.promise;
				expect(sockets).toHaveLength(2);
				expect(store.getState().status).toBe("opening");
				expect(sockets[1]?.methods).toContain("thread/read");
				barrier.resolve();
				await preAck.promise;
				expect(state.readCount).toBe(2);
				expect(store.getState().status).toBe("open");
				expect(store.getState().conversation?.resumeRequired).toBe(true);
				expect(store.getState().conversationGeneration).not.toBe(bindingGeneration);
				// Navigation can retire the destination while the daemon acknowledgement is held.
				destination = !navigateAway;
				ack.resolve();
				await resume;
				expect(state.readCount).toBe(navigateAway ? 2 : 3);
				expect(refreshes).toBe(navigateAway ? 1 : 2);
				expect(store.getState().conversation?.resumeRequired).toBe(navigateAway);
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

// A connection takes one explicit resume at a time, and the client refuses a
// second before sending it. Two panes can owe a resume at once, and a refusal
// that never reached the hub is not the hub's answer: the later one waits for
// the slot instead of ending as a failed attempt the restart would drop.
describe("two resumes owed on one connection", () => {
	// The collateral case, on the real client: a resume the caller decides is no
	// longer wanted must not retire the transport, because retiring it is what
	// drops every request the socket is carrying. The ordering is the
	// connection's, so this observes the socket rather than the call.
	it("does not retire the transport when the resume is abandoned first", async () => {
		const { client, sockets } = await connectedFixture();
		try {
			// One socket so far: the connected one. A resume that got past the
			// abandonment would retire it and dial a second.
			expect(sockets).toHaveLength(1);
			const before = sockets[0].methods.filter((method) => method === "thread/resume").length;
			// `beforeRecycle` is the connection wrapper's addition, so it is not on
			// the client class's own signature; the caller that needs it is the
			// runtime, which types the seam itself.
			const admitting = client as unknown as {
				resumeThread(
					ref: string,
					options?: { beforeRecycle?: () => void | Promise<void> },
				): ReturnType<typeof client.resumeThread>;
			};
			await admitting
				.resumeThread("local:thread-1", {
					beforeRecycle: () => {
						throw new ResumeAbandoned("blocked");
					},
				})
				.catch(() => undefined);
			// No replacement transport was dialed, so nothing in flight was dropped,
			// and no resume frame was written on the one that was already there.
			expect(sockets).toHaveLength(1);
			expect(sockets[0].methods.filter((method) => method === "thread/resume")).toHaveLength(before);
		} finally {
			client.close();
		}
	});

	// Outbox recovery reaches the client directly rather than through a
	// service, so a queue kept in the service would not see it: the restart behind
	// it would be refused before dispatch and end with the daemon stopped. The
	// admission has to be the connection's own, which is why this drives the
	// client for one of the two.
	it("waits for a slot the outbox holds rather than refusing before dispatch", async () => {
		const ack = deferred<void>();
		const { client, service, sockets, state } = await connectedFixture(undefined, ack.promise);
		try {
			const outbox = client.resumeThread("local:thread-1");
			const restart = service.resume();
			await Promise.resolve();
			expect(state.resumed).toBe(false);
			ack.resolve();
			await expect(Promise.all([outbox, restart])).resolves.toBeDefined();
			const resumes = sockets.flatMap((socket) => socket.methods).filter((method) => method === "thread/resume");
			expect(resumes).toHaveLength(2);
		} finally {
			state.resumed = true;
			service.close();
			client.close();
		}
	});

	it("waits for the slot rather than refusing the second before it is sent", async () => {
		const ack = deferred<void>();
		const { client, service, sockets, state } = await connectedFixture(undefined, ack.promise);
		try {
			const first = service.resume();
			const second = service.resume();
			await Promise.resolve();
			// The first resume is out and has not been answered yet.
			expect(state.resumed).toBe(false);
			ack.resolve();
			await expect(Promise.all([first, second])).resolves.toBeDefined();
			const resumes = sockets.flatMap((socket) => socket.methods).filter((method) => method === "thread/resume");
			expect(resumes).toHaveLength(2);
		} finally {
			state.resumed = true;
			service.close();
			client.close();
		}
	});

	// Whether the hub was asked is a fact about one attempt's frame. A resume
	// waiting for the slot has not written anything, so it must not answer with
	// the record of the attempt that went before it: whichever ordering the two
	// land in, only the attempt that wrote its own frame may be reported as
	// possibly delivered, and only the one that did not may be sent again.
	it("answers each attempt from its own frame, not the one ahead of it in the slot", async () => {
		const { client, sockets } = await connectedFixture();
		try {
			// The attempt that holds the slot and loses its reply after the frame
			// has gone out: it must be reported as delivered-but-unknown.
			const sent = client.resumeThread("local:thread-1", {
				beforeRequest: () => {
					// The frame has not been written yet here; the close below lands
					// after it, which is the uncertain case.
				},
			});
			// Queued behind it: this one writes no frame of its own.
			const refused = client.resumeThread("local:thread-1", {
				beforeRequest: () => {
					client.close();
				},
			});
			const outcomes = await Promise.all(
				[sent, refused].map((resume) =>
					resume.then(
						() => "sent" as const,
						(cause: unknown) => cause,
					),
				),
			);
			// The second never reached the wire, so the step it stands for is still
			// owed. Inheriting the first's mark would report it as though the hub
			// had answered, and the restart would drop it with the daemon stopped.
			expect(outcomes[1]).toBeInstanceOf(ResumeNotDispatched);
			expect(sockets.flatMap((socket) => socket.methods).filter((method) => method === "thread/resume")).toHaveLength(
				1,
			);
		} finally {
			client.close();
		}
	});
});

// The resume recycles the socket and waits for a fresh handshake before it
// sends. That wait can end without a resume on the wire: the phone lost its
// connection mid-recycle, or the handshake never finished. The connection is
// the only thing that can tell that apart from the hub answering, so it says
// so with its own error and the restart keeps the step owed instead of
// dropping it with a daemon stopped.
describe("a resume whose own reconnect never completed", () => {
	// The resume can get as far as a finished handshake and still never be
	// admitted: the client checks its readiness at the request boundary, and a
	// transport that dropped in between refuses there without a frame leaving.
	// Everything earlier looks like progress — the recycle happened, the
	// handshake completed, the caller's own hook ran — and none of it means the
	// hub was asked. Reported as though it had been, the restart drops the step
	// and the daemon stays stopped after connectivity returns.
	it("keeps a resume owed when the transport refuses it before the frame", async () => {
		const { client } = await connectedFixture();
		try {
			const resume = client.resumeThread("local:thread-1", {
				beforeRequest: () => {
					// The phone loses the connection inside the last window there is:
					// after the handshake the resume waited for, before the frame.
					client.close();
				},
			});
			const outcome = await resume.then(
				() => "sent",
				(cause: unknown) => cause,
			);
			expect(outcome).toBeInstanceOf(ResumeNotDispatched);
		} finally {
			client.close();
		}
	});

	it("reports a resume that never reached the wire as not dispatched", async () => {
		// The dial the resume's recycle starts never opens, so the handshake it
		// waits on is still outstanding and nothing has been sent when the
		// connection goes away underneath it.
		const { client, secondDial } = await connectedFixture(undefined, undefined, 1);
		try {
			const resume = client.resumeThread("local:thread-1");
			// The resume has recycled its socket and is waiting on the dial that
			// never finishes; only now is the failure the reconnect's, not the
			// precondition's.
			await secondDial.promise;
			client.close();
			const cause = await resume.then(
				() => undefined,
				(error: unknown) => error,
			);
			// Nothing was sent, so the step is still owed and the caller may retry
			// it. Reported as the hub's answer instead, the restart drops it and
			// leaves the daemon stopped.
			expect(cause).toBeInstanceOf(ResumeNotDispatched);
		} finally {
			client.close();
		}
	});
});
