import { describe, expect, it, vi } from "vitest";
import WebSocket from "ws";
import type { WebSocketLike } from "@evener/appwire-client";
import { type MutationOptimisticRecord, reconcilePendingEntries } from "@evener/appwire-client/state/mutation";
import { createConversationService } from "../../mobile/src/services/conversation";
import { createNewSessionService } from "../../mobile/src/services/newSession";
import { createActivityStore } from "../../mobile/src/state/activity";
import { createConversationStore } from "../../mobile/src/state/conversation";
import { createDemoHub } from "../scripts/demo-hub.mjs";
import { createHubClient } from "./connection";
import { type DemoFleetOptions, demoSessionId, fleetSessions } from "./dev/demoFleet.js";
import { DEMO_MODEL_LIST } from "./dev/demoSessions.js";
import { readOrganizationNavigation } from "./organizationNavigation";
import { ghosts } from "./session/ghosts";

describe("native demonstration hub", () => {
	it("changes the observed queue and rejects stale identities and revisions", async () => {
		const hub = await createDemoHub(0);
		const client = createHubClient(hub.origin, "", (url) => new WebSocket(url) as unknown as WebSocketLike);
		const service = createConversationService(client);
		try {
			await client.connect();
			await service.open("demo:playground");
			await service.send([{ type: "text", text: "Start" }]);
			await service.open("demo:playground");
			for (const text of ["First", "Second", "Third"]) await service.queue([{ type: "text", text }]);
			const observed = await service.open("demo:playground");
			expect(observed.queue?.texts).toEqual(["First", "Second", "Third"]);
			const [first, second] = observed.queue?.ids ?? [];
			if (!first || !second || !observed.instanceId || !observed.queue) throw new Error("Missing queue guards");
			await service.cancelQueued(0, first, observed.instanceId);
			await expect(service.cancelQueued(0, first, observed.instanceId)).rejects.toMatchObject({ code: -32013 });
			await expect(service.drainAsSteer(observed.queue.revision, observed.instanceId)).rejects.toMatchObject({
				code: -32013,
			});
			const remaining = await service.open("demo:playground");
			expect(remaining.queue?.texts).toEqual(["Second", "Third"]);
			await service.promoteQueuedAsSteer(0, second, observed.instanceId);
			const last = await service.open("demo:playground");
			expect(last.queue?.texts).toEqual(["Third"]);
			if (!last.queue) throw new Error("Missing queue guards");
			await service.drainAsSteer(last.queue.revision, observed.instanceId);
			expect((await service.open("demo:playground")).queue?.depth).toBe(0);
		} finally {
			service.close();
			client.close();
			await hub.close();
		}
	});

	it("projects exact reference Markdown through the real conversation service", async () => {
		const markdown = "## Reference\n\nA **bold** paragraph.\n\n```ts\nconst n = 1;\n```";
		const hub = await createDemoHub(0, markdown);
		const client = createHubClient(hub.origin, "", (url) => new WebSocket(url) as unknown as WebSocketLike);
		const service = createConversationService(client);
		try {
			await client.connect();
			const conversation = await service.open("demo:playground");
			expect(conversation.items).toEqual(
				expect.arrayContaining([expect.objectContaining({ kind: "assistant", markdown })]),
			);
		} finally {
			service.close();
			client.close();
			await hub.close();
		}
	});
	it("keeps matching session references on different hubs isolated", async () => {
		const first = await createDemoHub(0);
		const second = await createDemoHub(0);
		const clients = [first, second].map((hub) =>
			createHubClient(hub.origin, "", (url) => new WebSocket(url) as unknown as WebSocketLike),
		);
		const services = clients.map((client) => createConversationService(client));
		try {
			await Promise.all(clients.map((client) => client.connect()));
			const [firstService, secondService] = services;
			if (!firstService || !secondService) throw new Error("Missing test service");
			await Promise.all(services.map((service) => service.open("demo:playground")));
			await firstService.send([{ type: "text", text: "Only on the first hub" }]);
			expect((await firstService.open("demo:playground")).status.type).toBe("active");
			const untouched = await secondService.open("demo:playground");
			expect(untouched.status.type).toBe("idle");
			expect(untouched.items).toHaveLength(0);
		} finally {
			for (const service of services) service.close();
			for (const client of clients) client.close();
			await Promise.all([first.close(), second.close()]);
		}
	});
	it("creates distinct conversations with selected launch settings and preserves the opening input", async () => {
		const hub = await createDemoHub(0);
		const client = createHubClient(hub.origin, "", (url) => new WebSocket(url) as unknown as WebSocketLike);
		const conversation = createConversationService(client);
		try {
			await client.connect();
			const creation = createNewSessionService(client);
			const projects = await creation.recentProjects();
			const harnesses = await creation.harnesses();
			const harness = harnesses[0]?.id;
			expect(projects).toContain("/demonstration");
			expect(harness).toBe("demonstration");
			const models = await creation.models({ cwd: "/demonstration", harness });
			const model = models.data[0];
			expect(model).toBeDefined();
			const first = await creation.start({
				cwd: "/demonstration",
				harness,
				modelProvider: model?.provider,
				model: model?.model,
				input: [{ type: "text", text: "  native opening\n🦋  " }],
			});
			const second = await creation.start({ cwd: "/demonstration" });
			expect(first.thread.evener.ref).not.toBe(second.thread.evener.ref);
			expect(first.thread.modelProvider).toBe(model?.provider);
			const opened = await conversation.open(first.thread.evener.ref);
			expect(opened.items.find((item) => item.kind === "user")).toMatchObject({
				text: "  native opening\n🦋  ",
			});
			expect(opened.capabilities.interrupt).toBe(true);
			await conversation.interrupt();
			expect((await conversation.open(second.thread.evener.ref)).items).toHaveLength(0);
			const roster = await client.request("thread/list", { limit: 50 });
			expect(roster.data).toHaveLength(3);
			expect(roster.data.map((thread) => thread.evener.ref)).toContain(first.thread.evener.ref);
		} finally {
			conversation.close();
			client.close();
			await hub.close();
		}
	});
	it("drives the shared stores through notification-triggered active and idle projections", async () => {
		const hub = await createDemoHub(0);
		const client = createHubClient(hub.origin, "", (url) => new WebSocket(url) as unknown as WebSocketLike);
		const service = createConversationService(client);
		const store = createConversationStore();
		const sink = createActivityStore().getState();
		const statusChanged = (status: string) =>
			new Promise<void>((resolve) => {
				const unsubscribe = store.subscribe((state) => {
					if (state.conversation?.status.type !== status) return;
					unsubscribe();
					resolve();
				});
			});
		try {
			await client.connect();
			await store.getState().openProjected(service, sink, "demo:playground");
			const active = statusChanged("active");
			store.getState().setDraft("Scripted mobile turn");
			await store.getState().send(service, [{ type: "text", text: store.getState().draft }]);
			await active;
			expect(store.getState().conversation?.capabilities.interrupt).toBe(true);
			expect(store.getState().draft).toBe("");
			const idle = statusChanged("idle");
			await store.getState().interrupt(service);
			await idle;
			expect(store.getState().conversation?.capabilities.send).toBe(true);
		} finally {
			store.getState().close();
			service.close();
			client.close();
			await hub.close();
		}
	});

	it("uses the real handshake, instance-bound send, projection, and interrupt", async () => {
		const hub = await createDemoHub(0);
		const client = createHubClient(hub.origin, "", (url) => new WebSocket(url) as unknown as WebSocketLike);
		const service = createConversationService(client);
		try {
			await client.connect();
			const initial = await service.open("demo:playground");
			expect(initial.capabilities.send).toBe(true);
			const receipt = await service.send([{ type: "text", text: "  mobile input\n🦋  " }]);
			expect(receipt.projectionState).toBe("pending");
			const active = await service.open("demo:playground");
			expect(active.status.type).toBe("active");
			expect(active.items.find((item) => item.kind === "user")).toMatchObject({
				text: "  mobile input\n🦋  ",
			});
			expect(active.items.some((item) => item.kind === "assistant")).toBe(true);
			expect(active.capabilities.interrupt).toBe(true);
			const stopped = await service.interrupt();
			expect(stopped.projectionState).toBe("reflected");
			expect((await service.open("demo:playground")).status.type).toBe("idle");
			await expect(
				client.request("turn/start", {
					ref: "demo:playground",
					expectedInstanceId: "wrong",
					clientMutationId: "wrong",
					input: [],
				}),
			).rejects.toThrow();
			expect((await service.open("demo:playground")).items.filter((item) => item.kind === "user")).toHaveLength(1);
		} finally {
			service.close();
			client.close();
			await hub.close();
		}
	});

	it("stamps clientMutationId on the user message it creates for turn/start, like the real projector", async () => {
		const hub = await createDemoHub(0);
		const client = createHubClient(hub.origin, "", (url) => new WebSocket(url) as unknown as WebSocketLike);
		const service = createConversationService(client);
		try {
			await client.connect();
			const opened = await service.open("demo:playground");
			if (!opened.instanceId) throw new Error("Missing instance id");
			const clientMutationId = "demo-hub-turn-start-mutation-id";
			const started = await client.request("turn/start", {
				ref: "demo:playground",
				expectedInstanceId: opened.instanceId,
				clientMutationId,
				input: [{ type: "text", text: "Hello" }],
			});
			const userItem = started.turn.items?.find((item) => item.type === "userMessage");
			expect(userItem?.clientMutationId).toBe(clientMutationId);
		} finally {
			service.close();
			client.close();
			await hub.close();
		}
	});
});

describe("native demonstration hub's redesign fleet", () => {
	it("answers no navigation, search, auth or plugin method without EVENER_DEMO_FLEET", async () => {
		const hub = await createDemoHub(0);
		const client = createHubClient(hub.origin, "", (url) => new WebSocket(url) as unknown as WebSocketLike);
		try {
			const handshake = await client.connect();
			expect(handshake.navigation).toBeUndefined();
			expect(handshake.features.auth).toBe(false);
			await expect(
				client.request("evener/navigation/read", {
					representationVersion: 2,
					resource: "manifest",
				}),
			).rejects.toThrow();
			await expect(client.request("evener/search", {})).rejects.toThrow();
			await expect(client.request("evener/auth/list", {})).rejects.toThrow();
			await expect(client.request("evener/plugin/list", {})).rejects.toThrow();
			// The flag being off doesn't touch the existing demo flows.
			const roster = await client.request("thread/list", { limit: 5 });
			expect(roster.data.map((thread) => thread.evener.ref)).toContain("demo:playground");
		} finally {
			client.close();
			await hub.close();
		}
	});

	it("serves the redesign's fleet once EVENER_DEMO_FLEET is on, alongside the existing demo thread", async () => {
		const hub = await createDemoHub(0, undefined, {});
		const client = createHubClient(hub.origin, "", (url) => new WebSocket(url) as unknown as WebSocketLike);
		try {
			const handshake = await client.connect();
			expect(handshake.navigation).toMatchObject({ version: 1, readVersions: [2] });
			expect(handshake.features.auth).toBe(true);
			const manifest = await client.request("evener/navigation/read", {
				representationVersion: 2,
				resource: "manifest",
			});
			expect(manifest.status).toBe("ok");
			const search = await client.request("evener/search", { query: "" });
			expect(search).toHaveProperty("live");
			const auth = await client.request("evener/auth/list", {});
			expect(auth.providers).toHaveLength(1);
			const plugins = await client.request("evener/plugin/list", {});
			expect(plugins.plugins).toHaveLength(14);
			const roster = await client.request("thread/list", { limit: 5 });
			expect(roster.data.map((thread) => thread.evener.ref)).toContain("demo:playground");
		} finally {
			client.close();
			await hub.close();
		}
	});

	it("marks paradise-park's manifest source offline when the fleet starts with offlineHost", async () => {
		const hub = await createDemoHub(0, undefined, { offlineHost: true });
		const client = createHubClient(hub.origin, "", (url) => new WebSocket(url) as unknown as WebSocketLike);
		try {
			await client.connect();
			const manifest = await client.request("evener/navigation/read", {
				representationVersion: 2,
				resource: "manifest",
			});
			const snapshot = manifest.data as {
				metadata: { sources: { id: string; online: boolean }[] };
			};
			expect(snapshot.metadata.sources.find((source) => source.id === "paradise-park")?.online).toBe(false);
		} finally {
			client.close();
			await hub.close();
		}
	});

	it("tells a connected client navigation changed when a working row asks its question", async () => {
		const hub = await createDemoHub(0, undefined, {});
		const client = createHubClient(hub.origin, "", (url) => new WebSocket(url) as unknown as WebSocketLike);
		const invalidated = navigationInvalidated(client);
		try {
			// The initialize answer means the hub holds this socket, so the
			// question below reaches it.
			const handshake = await client.connect();
			hub.askQuestion();
			const payload = (await invalidated) as {
				generationId: string;
				sequence: number;
				targets: { kind: string; section?: string; revision?: number }[];
			};
			expect(payload.generationId).toBe(handshake.navigation?.generationId);
			expect(payload.sequence).toBe(1);
			expect(payload.targets).toEqual(
				expect.arrayContaining([
					expect.objectContaining({ kind: "section", section: "live" }),
					expect.objectContaining({ kind: "section", section: "needs_you" }),
				]),
			);
			const needsYou = await client.request("evener/navigation/read", {
				representationVersion: 2,
				resource: "section",
				section: "needs_you",
			});
			expect(needsYou.revision).toBe(payload.targets[0]?.revision);
			const entities = (needsYou.data as { entities: { value: { session_id?: string } }[] }).entities;
			expect(entities.map((entity) => entity.value.session_id)).toContain(demoSessionId("s-gateway"));
		} finally {
			client.close();
			await hub.close();
		}
	});

	it("tells a client connecting after the question the sequence it already reached", async () => {
		const hub = await createDemoHub(0, undefined, {});
		hub.askQuestion();
		const client = createHubClient(hub.origin, "", (url) => new WebSocket(url) as unknown as WebSocketLike);
		try {
			// The navigation store refuses a reconnect whose sequence is below
			// the last one it accepted, so a phone connected through the
			// question must be told 1 again when it comes back.
			const handshake = await client.connect();
			expect(handshake.navigation?.sequence).toBe(1);
		} finally {
			client.close();
			await hub.close();
		}
	});

	it("archives a session, tells connected clients, and answers with a receipt the phone's check confirms", async () => {
		const hub = await createDemoHub(0, undefined, {});
		const client = createHubClient(hub.origin, "", (url) => new WebSocket(url) as unknown as WebSocketLike);
		const invalidated = navigationInvalidated(client);
		try {
			const handshake = await client.connect();
			const id = demoSessionId("s-deslop");
			const response = await client.request("evener/archive/set", {
				kind: "session",
				id,
				archived: true,
			});
			expect(response.ok).toBe(true);
			expect(response.navigation.generation_id).toBe(handshake.navigation?.generationId);
			expect(await invalidated).toEqual({
				generationId: response.navigation.generation_id,
				sequence: 1,
				targets: response.navigation.targets,
			});
			// The Board's journal settles an archive through this same check
			// (useBoardOrganization.ts, organizationCheck.ts).
			const observation = await readOrganizationNavigation(
				client,
				{
					id: "archive",
					operation: {
						kind: "archive",
						params: { kind: "session", id, archived: true },
					},
					receipt: response.navigation,
				},
				() => true,
				true,
			);
			expect(observation).toMatchObject({
				state: "archived",
				title: "Deslop README Pass",
				settled: true,
			});
		} finally {
			client.close();
			await hub.close();
		}
	});

	it("asks the question from its askAfterSeconds timer", async () => {
		// Only the timer functions are faked: the sockets still need real I/O.
		// The delay stays under the client's 20s heartbeat, which the same
		// advance would otherwise fire and time out, dropping the socket.
		vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
		const hub = await createDemoHub(0, undefined, { askAfterSeconds: 5 });
		const client = createHubClient(hub.origin, "", (url) => new WebSocket(url) as unknown as WebSocketLike);
		const invalidated = navigationInvalidated(client);
		try {
			await client.connect();
			vi.advanceTimersByTime(5_000);
			expect(await invalidated).toMatchObject({ sequence: 1 });
		} finally {
			vi.useRealTimers();
			client.close();
			await hub.close();
		}
	});
});

describe("native demonstration hub's fleet sessions", () => {
	// A hub (a fleet hub unless fleetOptions is undefined) and a connected
	// client, closed after `body` runs.
	async function withHub(
		fleetOptions: DemoFleetOptions | undefined,
		body: (client: ReturnType<typeof createHubClient>) => Promise<void>,
	) {
		const hub = await createDemoHub(0, undefined, fleetOptions);
		const client = createHubClient(hub.origin, "", (url) => new WebSocket(url) as unknown as WebSocketLike);
		try {
			await client.connect();
			await body(client);
		} finally {
			client.close();
			await hub.close();
		}
	}
	const refOf = (slug: string) => {
		const session = fleetSessions().find((candidate) => candidate.slug === slug);
		if (!session) throw new Error(`no fleet session ${slug}`);
		return session.ref;
	};

	it("opens every live fleet session, so the title's swipe lands on real neighbors", async () => {
		await withHub({}, async (client) => {
			const service = createConversationService(client);
			try {
				for (const session of fleetSessions().filter((candidate) => candidate.state !== "shutdown")) {
					const conversation = await service.open(session.ref);
					expect(conversation.name).toBe(session.title);
					expect(conversation.items.length).toBeGreaterThan(0);
				}
				const turns = await client.request("thread/turns/list", {
					ref: refOf("s-pr2138"),
				});
				expect(turns.data).toEqual([]);
			} finally {
				service.close();
			}
		});
	});

	it("queues, steers, stops and holds a fleet session's messages, then sends one held", async () => {
		await withHub({}, async (client) => {
			const service = createConversationService(client);
			try {
				const opened = await service.open(refOf("s-pr2138"));
				const instanceId = opened.instanceId;
				if (!instanceId) throw new Error("Missing instance id");
				await service.queue([{ type: "text", text: "One more thing" }]);
				const queued = await service.open(refOf("s-pr2138"));
				expect(queued.queue?.texts).toEqual(["When CI is green, post a summary on the PR.", "One more thing"]);
				// Steer now, while the turn runs.
				const [first, second] = queued.queue?.ids ?? [];
				if (!first || !second) throw new Error("Missing queue ids");
				expect(ghosts(queued, [], null, []).map((ghost) => ghost.buttons)).toEqual([["steerNow"], ["steerNow"]]);
				await service.promoteQueuedAsSteer(0, first, instanceId);
				// Stop with a message queued holds it, and Send now releases it.
				await service.interrupt();
				const stopped = await service.open(refOf("s-pr2138"));
				expect(stopped.status.type).toBe("idle");
				expect(stopped.queue?.texts).toEqual(["One more thing"]);
				expect(ghosts(stopped, [], null, [])).toEqual([
					expect.objectContaining({ state: "held", buttons: ["sendNow", "cancel"] }),
				]);
				await service.promoteQueuedAsSteer(0, second, instanceId);
				const sent = await service.open(refOf("s-pr2138"));
				expect(sent.queue?.depth).toBe(0);
				expect(sent.status.type).toBe("active");
				expect(sent.items.filter((item) => item.kind === "user").at(-1)).toMatchObject({
					text: "One more thing",
				});
			} finally {
				service.close();
			}
		});
	});

	it("reflects each mutation it takes, so the phone's pending ghosts clear", async () => {
		await withHub({}, async (client) => {
			const service = createConversationService(client);
			const ref = refOf("s-pr2138");
			// The phone's own accepted record for a mutation, as its outbox keeps
			// it until the hub reflects the mutation.
			const accepted = (clientMutationId: string, method: string, text: string): MutationOptimisticRecord => ({
				version: 1,
				clientMutationId,
				targetRef: ref,
				method,
				payload: { input: [{ type: "text", text }] },
				attachments: [],
				optimisticDisplay: null,
				intentSequence: 1,
				createdAt: 1,
				state: "accepted",
			});
			const ghostsFor = async (records: MutationOptimisticRecord[]) => {
				const model = await service.open(ref);
				return ghosts(
					model,
					reconcilePendingEntries(ref, records, model, new Map(), () => true),
					null,
					[],
				);
			};
			try {
				const opened = await service.open(ref);
				const expectedInstanceId = opened.instanceId ?? "";
				await client.request("turn/queue", {
					ref,
					clientMutationId: "queue-1",
					expectedInstanceId,
					input: [{ type: "text", text: "From the phone" }],
				});
				const queuedHere = accepted("queue-1", "turn/queue", "From the phone");
				// The queued message shows once, as the queue's ghost.
				expect((await ghostsFor([queuedHere])).map((ghost) => [ghost.state, ghost.text])).toEqual([
					["queued", "When CI is green, post a summary on the PR."],
					["queued", "From the phone"],
				]);
				// Steer now: the steered message lands in the running turn. The
				// phone retired its queue record once the queue reflected it.
				const queued = await service.open(ref);
				const promote = accepted("promote-1", "turn/promoteQueuedAsSteer", "");
				await client.request("turn/promoteQueuedAsSteer", {
					ref,
					clientMutationId: "promote-1",
					expectedInstanceId,
					index: 1,
					expectedEntryId: queued.queue?.ids?.[1] ?? "",
				});
				expect((await ghostsFor([promote])).map((ghost) => ghost.text)).toEqual([
					"When CI is green, post a summary on the PR.",
				]);
				// Steer all: the receipt names every queued message it consumed.
				const drained = await client.request("turn/drainAsSteer", {
					ref,
					clientMutationId: "drain-1",
					expectedInstanceId,
					expectedQueueRevision: (await service.open(ref)).queue?.revision ?? -1,
				});
				expect(drained.receipt.consumedClientMutationIds).toEqual(["s-pr2138-queued-0"]);
				await client.request("turn/steer", {
					ref,
					clientMutationId: "steer-1",
					expectedInstanceId,
					input: [{ type: "text", text: "And check Linux too" }],
				});
				const steered = accepted("steer-1", "turn/steer", "And check Linux too");
				expect(await ghostsFor([promote, accepted("drain-1", "turn/drainAsSteer", ""), steered])).toEqual([]);
				expect((await service.open(ref)).items.filter((item) => item.kind === "user").map((item) => item.text)).toEqual(
					expect.arrayContaining([
						"From the phone",
						"When CI is green, post a summary on the PR.",
						"And check Linux too",
					]),
				);
			} finally {
				service.close();
			}
		});
	});

	it("cancels one queued message and steers with the rest from a long queue", async () => {
		await withHub({}, async (client) => {
			const service = createConversationService(client);
			try {
				const opened = await service.open(refOf("s-stumble"));
				const [first] = opened.queue?.ids ?? [];
				if (!first || !opened.instanceId || !opened.queue) throw new Error("Missing queue guards");
				await service.cancelQueued(0, first, opened.instanceId);
				const cancelled = await service.open(refOf("s-stumble"));
				expect(cancelled.queue?.depth).toBe(4);
				if (!cancelled.queue) throw new Error("Missing queue");
				await service.drainAsSteer(cancelled.queue.revision, opened.instanceId);
				expect((await service.open(refOf("s-stumble"))).queue?.depth).toBe(0);
			} finally {
				service.close();
			}
		});
	});

	it("settles the stopped turn's open items and times, and times the next turn", async () => {
		await withHub({}, async (client) => {
			const service = createConversationService(client);
			const ref = refOf("s-tasklist");
			const read = async () => (await client.request("thread/read", { ref, includeTurns: true })).thread;
			try {
				const before = await read();
				await service.open(ref);
				await service.interrupt();
				const stopped = await read();
				const turn = stopped.turns?.at(-1);
				if (!turn?.startedAt) throw new Error("the stopped turn needs its start");
				expect(turn.status).toBe("interrupted");
				expect(turn.items?.filter((item) => item.status === "inProgress")).toEqual([]);
				expect(turn.items?.filter((item) => item.status === "interrupted").map((item) => item.toolName)).toEqual([
					"delegate",
					"delegate",
					"shell",
				]);
				expect(turn.completedAt).toBeGreaterThanOrEqual(turn.startedAt);
				expect(turn.durationMs).toBe((turn.completedAt ?? 0) - turn.startedAt);
				expect(stopped.evener.lastTurnEndedAt).toBe(turn.completedAt);
				expect(stopped.evener.workMillis).toBeGreaterThan(before.evener.workMillis ?? 0);
				await service.open(ref);
				await service.send([{ type: "text", text: "Go on" }]);
				expect((await read()).turns?.at(-1)?.startedAt).toBeGreaterThanOrEqual(turn.completedAt ?? 0);
			} finally {
				service.close();
			}
		});
	});

	it("keeps a stopped fleet session sendable, like a daemon", async () => {
		await withHub({}, async (client) => {
			const service = createConversationService(client);
			try {
				await service.open(refOf("s-gateway"));
				await service.interrupt();
				const stopped = await service.open(refOf("s-gateway"));
				expect(stopped.capabilities).toMatchObject({
					send: true,
					clear: true,
					steer: true,
					interrupt: true,
					queue: true,
				});
				await service.send([{ type: "text", text: "Keep going" }]);
				const working = await service.open(refOf("s-gateway"));
				expect(working.status.type).toBe("active");
				expect(working.capabilities).toMatchObject({ send: false, clear: false, steer: true, interrupt: true });
				expect(working.activeTurnStartedAt).toBeDefined();
			} finally {
				service.close();
			}
		});
	});

	it("saves your note and removes a link on a fleet session", async () => {
		await withHub({}, async (client) => {
			const ref = refOf("s-pr2138");
			const { thread } = await client.request("thread/read", { ref, includeTurns: false });
			const expectedInstanceId = thread.evener.instanceId ?? "";
			const saved = await client.request("notes/human/set", {
				ref,
				clientMutationId: "note-1",
				expectedInstanceId,
				note: "Fix causes, and say which.",
			});
			expect(saved).toMatchObject({
				note: "Fix causes, and say which.",
				receipt: {
					clientMutationId: "note-1",
					disposition: "applied",
					projectionState: "pending",
					turnId: expect.any(String),
				},
			});
			// An unchanged note wakes no one and starts no turn.
			const unchanged = await client.request("notes/human/set", {
				ref,
				clientMutationId: "note-same",
				expectedInstanceId,
				note: "Fix causes, and say which.",
			});
			expect(unchanged.receipt.projectionState).toBe("removed");
			expect(unchanged.receipt).not.toHaveProperty("turnId");
			await client.request("urls/remove", { ref, clientMutationId: "link-1", expectedInstanceId, id: "u-checks" });
			const after = await client.request("thread/read", { ref, includeTurns: false });
			expect(after.thread.evener.humanNote).toBe("Fix causes, and say which.");
			expect(after.thread.evener.sessionUrls?.map((link) => link.id)).toEqual(["u-2138", "u-splan"]);
			// The phone reads this message as "already gone" (sessionNotes.ts).
			await expect(
				client.request("urls/remove", { ref, clientMutationId: "link-2", expectedInstanceId, id: "u-checks" }),
			).rejects.toThrow('no URL entry with id "u-checks"');
			// The daemon's MutationNotAccepted for a stale instance.
			await expect(
				client.request("notes/human/set", { ref, clientMutationId: "note-2", expectedInstanceId: "stale", note: "x" }),
			).rejects.toMatchObject({
				code: -32013,
				message: "thread instance is stale",
				data: {
					evenerErrorInfo: "conflict",
					clientMutationId: "note-2",
					mutationOutcome: "notAccepted",
					retryDisposition: "none",
				},
			});
			// The playground has no shared notes, as its capabilities say.
			await expect(
				client.request("notes/human/set", {
					ref: "demo:playground",
					clientMutationId: "note-3",
					expectedInstanceId: "demo-instance",
					note: "x",
				}),
			).rejects.toThrow("This session doesn't take shared notes");
		});
	});

	it("resolves a fleet session's approval", async () => {
		await withHub({}, async (client) => {
			const ref = refOf("s-mirror");
			const { thread } = await client.request("thread/read", { ref, includeTurns: false });
			const [escalation] = thread.evener.pendingEscalations ?? [];
			if (!escalation) throw new Error("frame 9 needs an escalation");
			expect(
				await client.request("evener/sandbox/escalation/resolve", {
					ref,
					escalationId: escalation.escalationId,
					approve: true,
				}),
			).toEqual({});
			const after = await client.request("thread/read", { ref, includeTurns: false });
			expect(after.thread.evener.pendingEscalations ?? []).toEqual([]);
			await expect(
				client.request("evener/sandbox/escalation/resolve", {
					ref,
					escalationId: escalation.escalationId,
					approve: true,
				}),
			).rejects.toThrow(`No pending escalation ${escalation.escalationId}`);
		});
		await withHub(undefined, async (client) => {
			await expect(
				client.request("evener/sandbox/escalation/resolve", {
					ref: "demo:playground",
					escalationId: "x",
					approve: false,
				}),
			).rejects.toThrow("Method not implemented by demonstration server");
		});
	});

	it("lists two providers' models, with a recent one, for the model sheet", async () => {
		await withHub({}, async (client) => {
			expect(await client.request("model/list", {})).toEqual(DEMO_MODEL_LIST);
		});
		await withHub(undefined, async (client) => {
			const models = await client.request("model/list", {});
			expect(models.data.map((model) => model.provider)).toEqual(["demonstration"]);
			expect(models.recent).toBeUndefined();
		});
	});

	it("answers no notes or links method without EVENER_DEMO_FLEET", async () => {
		await withHub(undefined, async (client) => {
			await expect(
				client.request("notes/human/set", {
					ref: "demo:playground",
					clientMutationId: "note-1",
					expectedInstanceId: "demo-instance",
					note: "x",
				}),
			).rejects.toThrow("Method not implemented by demonstration server");
			await expect(
				client.request("urls/remove", {
					ref: "demo:playground",
					clientMutationId: "link-1",
					expectedInstanceId: "demo-instance",
					id: "u-1",
				}),
			).rejects.toThrow("Method not implemented by demonstration server");
		});
	});

	it("serves no fleet session without EVENER_DEMO_FLEET", async () => {
		await withHub(undefined, async (client) => {
			await expect(
				client.request("thread/read", {
					ref: refOf("s-pr2138"),
					includeTurns: false,
				}),
			).rejects.toThrow("Unknown demonstration session");
		});
	});
});

function navigationInvalidated(client: ReturnType<typeof createHubClient>): Promise<unknown> {
	return new Promise((resolve) => {
		client.onNotification((notification) => {
			if (notification.method === "evener/navigation/invalidated") resolve(notification.params);
		});
	});
}
