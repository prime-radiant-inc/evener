import { describe, expect, it } from "vitest";
import WebSocket from "ws";
import type { WebSocketLike } from "../../cmd/evener-hub/frontend/src/protocol/transport";
import { createConversationService } from "../../mobile/src/services/conversation";
import { createNewSessionService } from "../../mobile/src/services/newSession";
import { createActivityStore } from "../../mobile/src/state/activity";
import { createConversationStore } from "../../mobile/src/state/conversation";
import { createDemoHub } from "../scripts/demo-hub.mjs";
import { createHubClient } from "./connection";

describe("native demonstration hub", () => {
	it("keeps matching session references on different hubs isolated", async () => {
		const first = await createDemoHub(0);
		const second = await createDemoHub(0);
		const clients = [first, second].map((hub) =>
			createHubClient(
				hub.origin,
				"",
				(url) => new WebSocket(url) as unknown as WebSocketLike,
			),
		);
		const services = clients.map((client) => createConversationService(client));
		try {
			await Promise.all(clients.map((client) => client.connect()));
			const [firstService, secondService] = services;
			if (!firstService || !secondService)
				throw new Error("Missing test service");
			await Promise.all(
				services.map((service) => service.open("demo:playground")),
			);
			await firstService.send([
				{ type: "text", text: "Only on the first hub" },
			]);
			expect((await firstService.open("demo:playground")).status).toBe(
				"active",
			);
			const untouched = await secondService.open("demo:playground");
			expect(untouched.status).toBe("idle");
			expect(untouched.items).toHaveLength(0);
		} finally {
			for (const service of services) service.close();
			for (const client of clients) client.close();
			await Promise.all([first.close(), second.close()]);
		}
	});
	it("creates distinct conversations with selected launch settings and preserves the opening input", async () => {
		const hub = await createDemoHub(0);
		const client = createHubClient(
			hub.origin,
			"",
			(url) => new WebSocket(url) as unknown as WebSocketLike,
		);
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
			expect(
				(await conversation.open(second.thread.evener.ref)).items,
			).toHaveLength(0);
			const roster = await client.request("thread/list", { limit: 50 });
			expect(roster.data).toHaveLength(3);
			expect(roster.data.map((thread) => thread.evener.ref)).toContain(
				first.thread.evener.ref,
			);
		} finally {
			conversation.close();
			client.close();
			await hub.close();
		}
	});
	it("drives the shared stores through notification-triggered active and idle projections", async () => {
		const hub = await createDemoHub(0);
		const client = createHubClient(
			hub.origin,
			"",
			(url) => new WebSocket(url) as unknown as WebSocketLike,
		);
		const service = createConversationService(client);
		const store = createConversationStore();
		const sink = createActivityStore().getState();
		const statusChanged = (status: string) =>
			new Promise<void>((resolve) => {
				const unsubscribe = store.subscribe((state) => {
					if (state.conversation?.status !== status) return;
					unsubscribe();
					resolve();
				});
			});
		try {
			await client.connect();
			await store.getState().openProjected(service, sink, "demo:playground");
			const active = statusChanged("active");
			store.getState().setDraft("Scripted mobile turn");
			await store
				.getState()
				.send(service, [{ type: "text", text: store.getState().draft }]);
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
		const client = createHubClient(
			hub.origin,
			"",
			(url) => new WebSocket(url) as unknown as WebSocketLike,
		);
		const service = createConversationService(client);
		try {
			await client.connect();
			const initial = await service.open("demo:playground");
			expect(initial.capabilities.send).toBe(true);
			const receipt = await service.send([
				{ type: "text", text: "  mobile input\n🦋  " },
			]);
			expect(receipt.projectionState).toBe("pending");
			const active = await service.open("demo:playground");
			expect(active.status).toBe("active");
			expect(active.items.find((item) => item.kind === "user")).toMatchObject({
				text: "  mobile input\n🦋  ",
			});
			expect(active.items.some((item) => item.kind === "assistant")).toBe(true);
			expect(active.capabilities.interrupt).toBe(true);
			const stopped = await service.interrupt();
			expect(stopped.projectionState).toBe("reflected");
			expect((await service.open("demo:playground")).status).toBe("idle");
			await expect(
				client.request("turn/start", {
					ref: "demo:playground",
					expectedInstanceId: "wrong",
					clientMutationId: "wrong",
					input: [],
				}),
			).rejects.toThrow();
			expect(
				(await service.open("demo:playground")).items.filter(
					(item) => item.kind === "user",
				),
			).toHaveLength(1);
		} finally {
			service.close();
			client.close();
			await hub.close();
		}
	});
});
