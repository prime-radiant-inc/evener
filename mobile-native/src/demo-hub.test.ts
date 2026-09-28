import { describe, expect, it, vi } from "vitest";
import WebSocket from "ws";
import type { WebSocketLike } from "@evener/appwire-client";
import { createConversationService } from "../../mobile/src/services/conversation";
import { createNewSessionService } from "../../mobile/src/services/newSession";
import { createActivityStore } from "../../mobile/src/state/activity";
import { createConversationStore } from "../../mobile/src/state/conversation";
import { createDemoHub } from "../scripts/demo-hub.mjs";
import { createHubClient } from "./connection";
import { demoSessionId } from "./dev/demoFleet.js";
import { readOrganizationNavigation } from "./organizationNavigation";

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
			// The Board's expired sign-in, and the Hub's other account sign-ins.
			expect(auth.providers).toHaveLength(3);
			const plugins = await client.request("evener/plugin/list", {});
			expect(plugins.plugins).toHaveLength(14);
			const roster = await client.request("thread/list", { limit: 5 });
			expect(roster.data.map((thread) => thread.evener.ref)).toContain("demo:playground");
		} finally {
			client.close();
			await hub.close();
		}
	});

	it("serves New session's reads for each host and starts a session on paradise-park", async () => {
		const hub = await createDemoHub(0, undefined, {});
		const client = createHubClient(hub.origin, "", (url) => new WebSocket(url) as unknown as WebSocketLike);
		try {
			await client.connect();
			const recent = await client.request("evener/projects/recent", {});
			expect(recent.data?.[0]).toBe("/home/jesse/git/prime-radiant-inc/evener");
			expect(
				await client.request("evener/host/request", {
					host: "paradise-park",
					method: "evener/projects/recent",
					params: {},
				}),
			).toEqual({ data: ["/Users/jesse/git/evener", "/Users/jesse/git/c-to-wasm"] });
			expect((await client.request("model/list", {})).data).toHaveLength(15);
			expect((await client.request("evener/host/list", {})).hosts[0]?.attached).toBe(true);
			const started = await client.request("thread/start", {
				cwd: "/Users/jesse/git/evener",
				source: "paradise-park",
			});
			expect(started.thread.evener.ref).toBe("paradise-park:created-1");
			expect(started.thread.cwd).toBe("/Users/jesse/git/evener");
			const local = await client.request("thread/start", { cwd: "/home/jesse/git/prime-radiant-inc/evener" });
			expect(local.thread.evener.ref).toBe("demo:created-2");
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

function navigationInvalidated(client: ReturnType<typeof createHubClient>): Promise<unknown> {
	return new Promise((resolve) => {
		client.onNotification((notification) => {
			if (notification.method === "evener/navigation/invalidated") resolve(notification.params);
		});
	});
}
