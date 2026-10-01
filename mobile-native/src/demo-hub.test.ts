import { PassThrough } from "node:stream";
import { describe, expect, it, vi } from "vitest";
import WebSocket from "ws";
import type { NavigationReadParams, WebSocketLike, SessionDelegatesResponse } from "@evener/appwire-client";
import { type MutationOptimisticRecord, reconcilePendingEntries } from "@evener/appwire-client/state/mutation";
import { createConversationService } from "../../mobile/src/services/conversation";
import { createNewSessionService } from "../../mobile/src/services/newSession";
import { createActivityStore } from "../../mobile/src/state/activity";
import { createConversationStore } from "../../mobile/src/state/conversation";
import { createDemoHub } from "../scripts/demo-hub.mjs";
import { createHubClient } from "./connection";
import { type DemoFleetOptions, demoSessionId, fleetSessionRef, fleetSessions } from "./dev/demoFleet.js";
import { DEMO_MODEL_LIST } from "./dev/demoSetup.js";
import { readOrganizationNavigation } from "./organizationNavigation";
import { ghosts } from "./session/ghosts";
import { SessionActivityStore, projectSessionActivity, parseJobLogTail } from "@evener/appwire-client";
import { readDocFile } from "@evener/appwire-client/docContent";
import { SETTLE_RACE_PLAN, SETTLE_RACE_PLAN_REVISED } from "./dev/demoSubagents";
import { nativeDocPort } from "./nativeDocPort";
import { flattenActivity, isJobRow, isSubagentRow } from "./subagents/subagentModel";
import { SubagentTree } from "./subagents/subagentTree";

describe("the demo fleet's subagents and documents (phase 4, PR 9)", () => {
	const PR2138 = `local:${demoSessionId("s-pr2138")}`;
	const PLAN = "docs/superpowers/plans/2026-09-25-settle-race.md";

	async function withFleetHub(
		run: (hub: Awaited<ReturnType<typeof createDemoHub>>, client: ReturnType<typeof createHubClient>) => Promise<void>,
		options: { planRevised?: boolean; long?: boolean } = {},
	) {
		const hub = await createDemoHub(0, undefined, { now: Date.now(), ...options });
		const client = createHubClient(hub.origin, "", (url) => new WebSocket(url) as unknown as WebSocketLike);
		try {
			await client.connect();
			await run(hub, client);
		} finally {
			client.close();
			await hub.close();
		}
	}

	it("serves typed direct and subtree activity with stable paged identities over a real socket", async () => {
		await withFleetHub(async (_hub, client) => {
			const summary = await client.request("evener/thread/activity/read", { ref: PR2138, scope: "subtree" });
			expect(summary).toMatchObject({
				context: {
					ref: PR2138,
					sessionId: demoSessionId("s-pr2138"),
					rootRef: PR2138,
					ancestryKnown: true,
					availability: "live",
					ancestors: [],
				},
				scope: "subtree",
				delegates: { known: true, total: 55 },
			});
			expect(summary.context.epoch).not.toBe("");
			const direct = await client.request("evener/thread/delegates/list", { ref: PR2138, limit: 200 });
			expect(direct.scope).toBe("session");
			expect(direct.delegates).toHaveLength(54);
			const rows: SessionDelegatesResponse["delegates"] = [];
			let cursor: string | undefined;
			do {
				const page = await client.request("evener/thread/delegates/list", {
					ref: PR2138,
					scope: "subtree",
					cursor,
					limit: 7,
				});
				expect(page.context).toEqual(summary.context);
				expect(page.page.issues).toEqual([]);
				rows.push(...page.delegates);
				cursor = page.page.nextCursor;
				if (page.page.complete) expect(cursor).toBeUndefined();
			} while (cursor);
			expect(rows).toHaveLength(55);
			expect(new Set(rows.map((row) => row.delegateId)).size).toBe(55);
			const failedDelegate = rows.find((row) => row.outcome === "failed");
			if (!failedDelegate) throw new Error("missing failed delegate fixture");
			expect(failedDelegate).toMatchObject({ terminal: true, lifecycle: "idle", status: "idle", outcome: "failed" });
			expect(failedDelegate.reason).not.toBe("");
			const parent = rows.find((row) => row.delegateId === "g-settle");
			if (!parent) throw new Error("missing parent fixture");
			const selected = await client.request("evener/thread/delegates/list", { ref: parent.childRef });
			expect(selected.context).toMatchObject({
				ref: parent.childRef,
				sessionId: demoSessionId("g-settle"),
				rootRef: PR2138,
				parentRef: PR2138,
				ancestryKnown: true,
				ancestors: [{ ref: PR2138, sessionId: demoSessionId("s-pr2138") }],
			});
			expect(selected.delegates).toHaveLength(1);
			expect(selected.delegates[0]).toMatchObject({
				delegateId: "g-settle-1",
				ownerRef: parent.childRef,
				rootRef: PR2138,
				parentDelegateId: "g-settle",
			});
			const ownJobs = await client.request("evener/thread/jobs/list", { ref: PR2138 });
			expect(ownJobs.jobs.map((job) => job.command)).toEqual(["go build ./..."]);
			expect(ownJobs.jobs[0]).toMatchObject({
				ownerRef: PR2138,
				ownerSessionId: demoSessionId("s-pr2138"),
				transcriptRef: PR2138,
			});
			const jobs = await client.request("evener/thread/jobs/list", { ref: PR2138, scope: "subtree", limit: 200 });
			expect(jobs.jobs.length).toBeGreaterThan(ownJobs.jobs.length);
			const failed = jobs.jobs.find((job) => job.status === "command_exited_nonzero");
			if (!failed) throw new Error("missing failed shell fixture");
			expect(failed.ownerRef).not.toBe(PR2138);
			await expect(
				client.request("evener/jobs/output", { ref: failed.ownerRef, jobId: failed.jobId }),
			).resolves.toHaveProperty("data");
			const watches = await client.request("evener/thread/watches/list", { ref: parent.childRef });
			expect(watches).toMatchObject({
				context: selected.context,
				scope: "session",
				watches: [],
				page: { complete: true, issues: [] },
			});
		});
	});

	it("loads the typed demo through the shared observed store and projects all pages", async () => {
		await withFleetHub(async (_hub, client) => {
			const store = new SessionActivityStore(client, PR2138, { scope: "subtree" });
			store.start();
			const releases = [store.observe("delegates"), store.observe("jobs"), store.observe("watches")];
			try {
				await Promise.all([
					store.refresh("summary"),
					store.load("delegates"),
					store.load("jobs"),
					store.load("watches"),
				]);
				expect(store.getSnapshot().summary?.delegates.total).toBe(55);
				expect(store.getSnapshot().delegates.rows).toHaveLength(50);
				expect(store.getSnapshot().delegates.hasMore).toBe(true);
				await store.loadMore("delegates");
				const snapshot = store.getSnapshot();
				expect(snapshot.delegates.rows).toHaveLength(55);
				expect(snapshot.delegates.complete).toBe(true);
				for (const resource of [snapshot.summaryState, snapshot.delegates, snapshot.jobs, snapshot.watches])
					expect(resource.error).toBeNull();
				const presentation = projectSessionActivity(snapshot);
				expect(presentation.complete).toBe(true);
				expect(presentation.tree?.root.ref).toBe(PR2138);
				expect(presentation.tree?.root.entries.filter((entry) => entry.kind === "delegate")).toHaveLength(54);
			} finally {
				for (const release of releases) release();
				store.dispose();
			}
		});
	});

	it("recovers an evicted continuation through only the affected shared-store collection", async () => {
		await withFleetHub(async (_hub, client) => {
			const requests = vi.spyOn(client, "request");
			const store = new SessionActivityStore(client, PR2138, { scope: "subtree" });
			store.start();
			const releases = [store.observe("delegates"), store.observe("jobs"), store.observe("watches")];
			try {
				await Promise.all([
					store.refresh("summary"),
					store.load("delegates"),
					store.load("jobs"),
					store.load("watches"),
				]);
				expect(store.getSnapshot().delegates.rows).toHaveLength(50);
				const initialMembership = store.getSnapshot().delegates.rows.map((row) => row.delegateId);
				for (let i = 0; i < 129; i++)
					await client.request("evener/thread/delegates/list", { ref: PR2138, scope: "subtree", limit: 1 });
				requests.mockClear();
				const memberships: string[][] = [];
				const leave = store.subscribe(() =>
					memberships.push(store.getSnapshot().delegates.rows.map((row) => row.delegateId)),
				);
				await store.loadMore("delegates");
				leave();
				expect(store.getSnapshot().delegates).toMatchObject({ error: null, permanent: false, hasMore: true });
				expect(memberships.length).toBeGreaterThan(0);
				for (const membership of memberships) expect(membership).toEqual(initialMembership);
				expect(requests.mock.calls.map(([method]) => method)).toEqual([
					"evener/thread/delegates/list",
					"evener/thread/delegates/list",
				]);
				expect(requests.mock.calls[0]?.[1]).toHaveProperty("cursor");
				expect(requests.mock.calls[1]?.[1]).not.toHaveProperty("cursor");
				await store.loadMore("delegates");
				const rows = store.getSnapshot().delegates.rows;
				expect(rows).toHaveLength(55);
				expect(new Set(rows.map((row) => row.delegateId)).size).toBe(55);
				expect(store.getSnapshot().delegates.complete).toBe(true);
				expect(requests.mock.calls.every(([method]) => method === "evener/thread/delegates/list")).toBe(true);
			} finally {
				for (const release of releases) release();
				store.dispose();
			}
		});
	});

	it("keeps a coordinator's running job consistent with its compact navigation summary", async () => {
		await withFleetHub(async (_hub, client) => {
			const ref = `local:${demoSessionId("s-tasklist")}`;
			const summary = await client.request("evener/thread/activity/read", { ref });
			const jobs = await client.request("evener/thread/jobs/list", { ref });
			expect(summary.jobs).toMatchObject({ known: true, total: 1, active: 1 });
			expect(jobs.jobs).toHaveLength(1);
			expect(jobs.jobs[0]).toMatchObject({
				ownerRef: ref,
				transcriptRef: ref,
				command: "go test ./cmd/evener-hub/...",
				terminal: false,
			});
		});
	});

	it("binds cursors to the selected resource, scope and ref without changing page membership", async () => {
		await withFleetHub(async (_hub, client) => {
			const first = await client.request("evener/thread/delegates/list", { ref: PR2138, scope: "subtree", limit: 1 });
			const cursor = first.page.nextCursor;
			if (!cursor) throw new Error("missing demo continuation");
			const rest = await client.request("evener/thread/delegates/list", {
				ref: PR2138,
				scope: "subtree",
				cursor,
				limit: 200,
			});
			expect(rest.delegates).toHaveLength(54);
			expect(rest.page.complete).toBe(true);
			expect(new Set([...first.delegates, ...rest.delegates].map((row) => row.delegateId)).size).toBe(55);
			const invalid = { code: -32602, data: { evenerErrorInfo: "invalidParams" } };
			await expect(
				client.request("evener/thread/jobs/list", { ref: PR2138, scope: "subtree", cursor }),
			).rejects.toMatchObject(invalid);
			await expect(client.request("evener/thread/delegates/list", { ref: PR2138, cursor })).rejects.toMatchObject(
				invalid,
			);
			await expect(
				client.request("evener/thread/delegates/list", {
					ref: first.delegates[0]?.childRef ?? "",
					scope: "subtree",
					cursor,
				}),
			).rejects.toMatchObject(invalid);
			await expect(
				client.request("evener/thread/delegates/list", { ref: PR2138, cursor: "malformed" }),
			).rejects.toMatchObject(invalid);
			await expect(client.request("evener/thread/delegates/list", { ref: PR2138, limit: -1 })).rejects.toMatchObject(
				invalid,
			);
			await expect(client.request("evener/thread/activity/read", { ref: "local:missing" })).rejects.toMatchObject({
				code: -32602,
				data: { evenerErrorInfo: "resourceNotFound" },
			});
		});
	});

	it("preserves remote selected identities and retained availability", async () => {
		await withFleetHub(async (_hub, client) => {
			const ref = fleetSessionRef("s-wasm");
			expect(ref).not.toBe(`local:${demoSessionId("s-wasm")}`);
			const summary = await client.request("evener/thread/activity/read", { ref, scope: "subtree" });
			const jobs = await client.request("evener/thread/jobs/list", { ref, scope: "subtree" });
			expect(summary.context).toMatchObject({
				ref,
				rootRef: ref,
				sessionId: demoSessionId("s-wasm"),
				ancestryKnown: true,
			});
			expect(
				jobs.jobs.some((job) => job.ownerRef === ref && job.transcriptRef === ref && job.command === "make test-wasm"),
			).toBe(true);
			const retained = fleetSessionRef("s-roster");
			await expect(client.request("evener/thread/activity/read", { ref: retained })).resolves.toMatchObject({
				context: { ref: retained, rootRef: retained, availability: "retained", ancestryKnown: true },
				delegates: { known: true, total: 0 },
			});
		});
	});

	// The Activity list's own binding over the typed activity reads, every
	// page loaded, as the list loads them while you scroll.
	it("lists Get PR 2138 Test Clean's 55 subagents, and who ran each shell job, over a real socket", async () => {
		await withFleetHub(async (_hub, client) => {
			const coordinator = "Get PR 2138 Test Clean";
			const binding = new SubagentTree(PR2138, demoSessionId("s-pr2138"));
			const release = binding.observeActivity();
			try {
				await binding.setClient(client);
				for (let page = 0; page < 5 && binding.getSnapshot().hasMore; page++) await binding.loadMore();
				const tree = binding.getSnapshot().tree;
				if (!tree) throw new Error("no tree");
				const activity = flattenActivity(tree, coordinator);
				expect(activity.filter(isSubagentRow)).toHaveLength(55);
				// Its own finished build names it by its title, and a subagent's
				// failed test names that subagent: never a ref. With every page
				// loaded, every job can name who ran it.
				const jobs = activity.filter(isJobRow);
				const owned = jobs.map((row) => [row.title, row.state, row.owner]);
				expect(owned).toContainEqual(["go build ./...", "done", coordinator]);
				expect(owned).toContainEqual(["go test", "failed", "Fix race in tree settle"]);
				const titles = new Set([coordinator, ...activity.filter(isSubagentRow).map((row) => row.title)]);
				expect(jobs.filter((row) => row.owner === undefined || !titles.has(row.owner))).toEqual([]);
			} finally {
				release();
				await binding.setClient(null);
			}
		});
	});

	// A shell job's detail reads its output from the session that owns it.
	it("serves a listed shell job's output tail to the session that owns it", async () => {
		await withFleetHub(async (_hub, client) => {
			const listed = await client.request("evener/thread/jobs/list", { ref: PR2138, scope: "subtree", limit: 200 });
			const failed = listed.jobs.find((job) => job.status === "command_exited_nonzero");
			if (!failed) throw new Error("no failed job");
			const response = await client.request("evener/jobs/output", { ref: failed.ownerRef, jobId: failed.jobId });
			const tail = parseJobLogTail((response as { data: unknown }).data);
			expect(tail?.tail).toContain("FAIL");
			expect(tail?.totalBytes).toBe(new TextEncoder().encode(tail?.tail ?? "").length);
			// Only the owning session answers for the job, as on a real hub.
			await expect(client.request("evener/jobs/output", { ref: PR2138, jobId: failed.jobId })).rejects.toThrow(
				`job not found: ${failed.jobId}`,
			);
		});
	});

	it("opens a subagent's own session through the real conversation service", async () => {
		await withFleetHub(async (_hub, client) => {
			const listed = await client.request("evener/thread/delegates/list", {
				ref: PR2138,
				scope: "subtree",
				limit: 200,
			});
			const child = listed.delegates.find((row) => row.description === "Check drain ordering in tests");
			if (!child) throw new Error("no nested subagent");
			const service = createConversationService(client);
			try {
				const conversation = await service.open(child.childRef);
				expect(conversation.items.length).toBeGreaterThan(0);
			} finally {
				service.close();
			}
		});
	});

	it("gives Get PR 2138 Test Clean's transcript the same subagent refs as its Activity list", async () => {
		await withFleetHub(async (_hub, client) => {
			const listed = (
				await client.request("evener/thread/delegates/list", { ref: PR2138, scope: "subtree", limit: 200 })
			).delegates.map((row) => row.childRef);
			const read = await client.request("thread/read", { ref: PR2138, includeTurns: false });
			const transcript = (read.thread.evener.diagnostics?.delegates ?? []).map((delegate) => delegate.transcriptRef);
			expect(transcript.length).toBeGreaterThan(0);
			for (const ref of transcript) expect(listed).toContain(ref);
			expect(read.thread.id).toBe(PR2138.slice(PR2138.indexOf(":") + 1));
		});
	});

	// The Project screen's Archived tab reads a project's archived rows from
	// evener/archived/list, as on a real hub; navigation's archived tier is empty.
	it("serves a project's archived sessions through the archived list over a real socket", async () => {
		await withFleetHub(async (_hub, client) => {
			const first = await client.request("evener/archived/list", { catalog: "archived_projects", projectKey: "evener" });
			expect(first.total).toBe(271);
			expect(first.sessions).toHaveLength(50);
			const next = await client.request("evener/archived/list", {
				catalog: "archived_projects",
				projectKey: "evener",
				cursor: first.nextCursor,
			});
			expect(next.sessions).toHaveLength(50);
		});
	});

	it("serves the plan its link names, by the session's own folder", async () => {
		await withFleetHub(async (hub, client) => {
			const read = await client.request("thread/read", { ref: PR2138, includeTurns: false });
			const link = (read.thread.evener.sessionUrls ?? []).find((url) => url.url.endsWith("settle-race.md"));
			if (!link) throw new Error("no plan link");
			const absolute = decodeURIComponent(new URL(link.url).pathname);
			expect(absolute.startsWith(`${read.thread.cwd}/`)).toBe(true);
			const text = await readDocFile(PR2138, absolute, nativeDocPort(hub.origin, ""));
			expect(text.text).toBe(SETTLE_RACE_PLAN);
		});
	});

	it("serves the plan on the same port, however often it's read", async () => {
		// A document chip and a Files row read the plan too, so a version that
		// changed with the read count would reach the Reader already revised.
		await withFleetHub(async (hub) => {
			const port = nativeDocPort(hub.origin, "");
			for (let read = 0; read < 3; read++) expect((await readDocFile(PR2138, PLAN, port)).text).toBe(SETTLE_RACE_PLAN);
		});
	});

	it("serves the plan's revision once restarted with EVENER_DEMO_FLEET_PLAN_REVISED", async () => {
		await withFleetHub(
			async (hub) => {
				expect((await readDocFile(PR2138, PLAN, nativeDocPort(hub.origin, ""))).text).toBe(SETTLE_RACE_PLAN_REVISED);
			},
			{ planRevised: true },
		);
	});

	it("serves the long questions, approval and messages with EVENER_DEMO_LONG", async () => {
		const AUDIT = `local:${demoSessionId("s-audit")}`;
		const pendingCount = async (client: ReturnType<typeof createHubClient>) =>
			(await client.request("thread/read", { ref: AUDIT, includeTurns: false })).thread.evener.pendingQuestion?.count;
		await withFleetHub(async (_hub, client) => expect(await pendingCount(client)).toBe(4), { long: true });
		await withFleetHub(async (_hub, client) => expect(await pendingCount(client)).toBe(2));
	});
});

describe("native demonstration hub", () => {
	it("starts a turn when a resting playground is steered or its held message is sent", async () => {
		const hub = await createDemoHub(0);
		const client = createHubClient(hub.origin, "", (url) => new WebSocket(url) as unknown as WebSocketLike);
		const read = async () =>
			(await client.request("thread/read", { ref: "demo:playground", includeTurns: true })).thread;
		const identity = { ref: "demo:playground", expectedInstanceId: "demo-instance" };
		const lastUserText = (thread: Awaited<ReturnType<typeof read>>) =>
			thread.turns?.at(-1)?.items?.find((item) => item.type === "userMessage")?.text;
		try {
			await client.connect();
			await client.request("turn/start", {
				...identity,
				clientMutationId: "start-1",
				input: [{ type: "text", text: "Go" }],
			});
			await client.request("turn/queue", {
				...identity,
				clientMutationId: "queue-1",
				input: [{ type: "text", text: "Held one" }],
			});
			await client.request("turn/interrupt", { ...identity, clientMutationId: "stop-1" });
			const held = await read();
			expect(held.status.type).toBe("idle");
			await client.request("turn/promoteQueuedAsSteer", {
				...identity,
				clientMutationId: "promote-1",
				index: 0,
				expectedEntryId: held.evener.queue.ids?.[0] ?? "",
			});
			const sent = await read();
			expect(sent.status.type).toBe("active");
			expect(lastUserText(sent)).toBe("Held one");
			await client.request("turn/interrupt", { ...identity, clientMutationId: "stop-2" });
			const steered = await client.request("turn/steer", {
				...identity,
				clientMutationId: "steer-1",
				input: [{ type: "text", text: "Steered while resting" }],
			});
			const woken = await read();
			expect(woken.status.type).toBe("active");
			expect(steered.receipt.turnId).toBe(woken.evener.activeTurnId);
			expect(lastUserText(woken)).toBe("Steered while resting");
		} finally {
			client.close();
			await hub.close();
		}
	});

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
			expect(projects).toContain("/demonstration");
			const models = await creation.models({ cwd: "/demonstration" });
			const model = models.data[0];
			expect(model).toBeDefined();
			const first = await creation.start({
				cwd: "/demonstration",
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
	it("says the hub is up to date without EVENER_DEMO_FLEET, so the Hub's About shows real text", async () => {
		const hub = await createDemoHub(0);
		const client = createHubClient(hub.origin, "", (url) => new WebSocket(url) as unknown as WebSocketLike);
		try {
			await client.connect();
			expect(await client.request("evener/update/check", { channel: "" })).toMatchObject({
				updateAvailable: false,
				applicable: true,
			});
		} finally {
			client.close();
			await hub.close();
		}
	});

	it("answers no navigation, search, auth or plugin method without EVENER_DEMO_FLEET", async () => {
		const hub = await createDemoHub(0);
		const client = createHubClient(hub.origin, "", (url) => new WebSocket(url) as unknown as WebSocketLike);
		try {
			const handshake = await client.connect();
			expect(handshake.navigation).toBeUndefined();
			expect(handshake.features.auth).toBe(false);
			expect(handshake.features.transcriptDisplaySettings).toBe(false);
			await expect(
				client.request("evener/navigation/read", {
					representationVersion: 3,
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
			expect(handshake.navigation).toMatchObject({ version: 1, readVersions: [3] });
			expect(handshake.features.auth).toBe(true);
			expect(handshake.features.transcriptDisplaySettings).toBe(true);
			const manifest = await client.request("evener/navigation/read", {
				representationVersion: 3,
				resource: "manifest",
			});
			expect(manifest.status).toBe("ok");
			const search = await client.request("evener/search", { query: "" });
			expect(search).toHaveProperty("live");
			const auth = await client.request("evener/auth/list", {});
			// The Board's expired sign-in, and the Hub's other account sign-in.
			expect(auth.providers).toHaveLength(2);
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
			// A real hub names a remote session's source by its host
			// (remote_hub_refs.go's fromRemoteThread), matching the ref.
			expect(started.thread.source).toBe("paradise-park");
			expect(started.thread.cwd).toBe("/Users/jesse/git/evener");
			const local = await client.request("thread/start", { cwd: "/home/jesse/git/prime-radiant-inc/evener" });
			expect(local.thread.evener.ref).toBe("demo:created-2");
		} finally {
			client.close();
			await hub.close();
		}
	});

	it("stays up when a client leaves while its start is held", async () => {
		const hub = await createDemoHub(0, undefined, {}, { startDelaySeconds: 0.2 });
		const first = createHubClient(hub.origin, "", (url) => new WebSocket(url) as unknown as WebSocketLike);
		const second = createHubClient(hub.origin, "", (url) => new WebSocket(url) as unknown as WebSocketLike);
		try {
			await first.connect();
			void first.request("thread/start", { cwd: "/home/jesse/git/prime-radiant-inc/evener" }).catch(() => {});
			first.close();
			await new Promise((resolve) => setTimeout(resolve, 300));
			// The hub still answers another client after the held reply's moment passed.
			await second.connect();
			expect(await second.request("evener/projects/recent", {})).toBeDefined();
		} finally {
			second.close();
			await hub.close();
		}
	});

	it("answers thread/start late with startDelaySeconds, so the sheet can be swiped away first", async () => {
		const hub = await createDemoHub(0, undefined, {}, { startDelaySeconds: 0.3 });
		const client = createHubClient(hub.origin, "", (url) => new WebSocket(url) as unknown as WebSocketLike);
		try {
			await client.connect();
			// Only the hub's hold timer runs on fake time; socket I/O stays real.
			vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
			const order: string[] = [];
			const starting = client
				.request("thread/start", { cwd: "/home/jesse/git/prime-radiant-inc/evener" })
				.then((started) => (order.push("start"), started));
			// Every other method answers while the start is still held.
			await client.request("evener/projects/recent", {}).then(() => order.push("recent"));
			vi.advanceTimersByTime(299);
			// A reply the hub sent before this round trip would have arrived ahead of it.
			await client.request("evener/projects/recent", {});
			expect(order).toEqual(["recent"]);
			vi.advanceTimersByTime(1);
			const started = await starting;
			expect(order).toEqual(["recent", "start"]);
			expect(started.thread.evener.ref).toBe("demo:created-1");
		} finally {
			vi.useRealTimers();
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
				representationVersion: 3,
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
				representationVersion: 3,
				resource: "section",
				section: "needs_you",
			});
			const needsYouTarget = payload.targets.find(
				(target) => target.kind === "section" && target.section === "needs_you",
			);
			expect(needsYou.revision).toBe(needsYouTarget?.revision);
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

	it("keeps notifying the other clients when one socket fails mid-broadcast", async () => {
		const hub = await createDemoHub(0, undefined, {});
		const dropping = createHubClient(hub.origin, "", (url) => new WebSocket(url) as unknown as WebSocketLike);
		const listening = createHubClient(hub.origin, "", (url) => new WebSocket(url) as unknown as WebSocketLike);
		const invalidated = navigationInvalidated(listening);
		// The first socket the broadcast reaches throws, as a socket that goes
		// away mid-send does; the rest must still get the notification.
		const originalSend = WebSocket.prototype.send;
		let dropped = false;
		WebSocket.prototype.send = function (this: WebSocket, ...args: unknown[]) {
			const [data] = args;
			if (!dropped && typeof data === "string" && data.includes("evener/navigation/invalidated")) {
				dropped = true;
				throw new Error("socket is gone");
			}
			return (originalSend as (...sendArgs: unknown[]) => void).apply(this, args);
		} as unknown as typeof WebSocket.prototype.send;
		try {
			await dropping.connect();
			await listening.connect();
			expect(() => hub.askQuestion()).not.toThrow();
			expect(await invalidated).toMatchObject({ sequence: 1 });
		} finally {
			WebSocket.prototype.send = originalSend;
			dropping.close();
			listening.close();
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
		body: (client: ReturnType<typeof createHubClient>, hub: Awaited<ReturnType<typeof createDemoHub>>) => Promise<void>,
	) {
		const hub = await createDemoHub(0, undefined, fleetOptions);
		const client = createHubClient(hub.origin, "", (url) => new WebSocket(url) as unknown as WebSocketLike);
		try {
			await client.connect();
			await body(client, hub);
		} finally {
			client.close();
			await hub.close();
		}
	}
	// withHub, with the conversation service open on its client, closed after
	// `body` runs.
	async function withSession(
		fleetOptions: DemoFleetOptions | undefined,
		body: (
			service: ReturnType<typeof createConversationService>,
			client: ReturnType<typeof createHubClient>,
			hub: Awaited<ReturnType<typeof createDemoHub>>,
		) => Promise<void>,
	) {
		await withHub(fleetOptions, async (client, hub) => {
			const service = createConversationService(client);
			try {
				await body(service, client, hub);
			} finally {
				service.close();
			}
		});
	}

	it("opens every live fleet session, so the title's swipe lands on real neighbors", async () => {
		await withSession({}, async (service, client) => {
			for (const session of fleetSessions().filter((candidate) => candidate.state !== "shutdown")) {
				const conversation = await service.open(session.ref);
				expect(conversation.name).toBe(session.title);
				expect(conversation.items.length).toBeGreaterThan(0);
			}
			const turns = await client.request("thread/turns/list", {
				ref: fleetSessionRef("s-pr2138"),
			});
			expect(turns.data).toEqual([]);
		});
	});

	it("queues, steers, stops and holds a fleet session's messages, then sends one held", async () => {
		await withSession({}, async (service) => {
			const opened = await service.open(fleetSessionRef("s-pr2138"));
			const instanceId = opened.instanceId;
			if (!instanceId) throw new Error("Missing instance id");
			await service.queue([{ type: "text", text: "One more thing" }]);
			const queued = await service.open(fleetSessionRef("s-pr2138"));
			expect(queued.queue?.texts).toEqual(["When CI is green, post a summary on the PR.", "One more thing"]);
			// Steer now, while the turn runs.
			const [first, second] = queued.queue?.ids ?? [];
			if (!first || !second) throw new Error("Missing queue ids");
			expect(ghosts(queued, [], null, [], true).map((ghost) => ghost.buttons)).toEqual([["steerNow"], ["steerNow"]]);
			await service.promoteQueuedAsSteer(0, first, instanceId);
			// Stop with a message queued holds it, and Send now releases it.
			await service.interrupt();
			const stopped = await service.open(fleetSessionRef("s-pr2138"));
			expect(stopped.status.type).toBe("idle");
			expect(stopped.queue?.texts).toEqual(["One more thing"]);
			expect(ghosts(stopped, [], null, [], true)).toEqual([
				expect.objectContaining({ state: "held", buttons: ["sendNow", "cancel"] }),
			]);
			await service.promoteQueuedAsSteer(0, second, instanceId);
			const sent = await service.open(fleetSessionRef("s-pr2138"));
			expect(sent.queue?.depth).toBe(0);
			expect(sent.status.type).toBe("active");
			expect(sent.items.filter((item) => item.kind === "user").at(-1)).toMatchObject({
				text: "One more thing",
			});
		});
	});

	it("reflects each mutation it takes, so the phone's pending ghosts clear", async () => {
		await withSession({}, async (service, client) => {
			const ref = fleetSessionRef("s-pr2138");
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
					true,
				);
			};
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
		});
	});

	it("cancels one queued message and steers with the rest from a long queue", async () => {
		await withSession({}, async (service) => {
			const opened = await service.open(fleetSessionRef("s-stumble"));
			const [first] = opened.queue?.ids ?? [];
			if (!first || !opened.instanceId || !opened.queue) throw new Error("Missing queue guards");
			await service.cancelQueued(0, first, opened.instanceId);
			const cancelled = await service.open(fleetSessionRef("s-stumble"));
			expect(cancelled.queue?.depth).toBe(4);
			if (!cancelled.queue) throw new Error("Missing queue");
			await service.drainAsSteer(cancelled.queue.revision, opened.instanceId);
			expect((await service.open(fleetSessionRef("s-stumble"))).queue?.depth).toBe(0);
		});
	});

	it("settles the stopped turn's open items and times, and times the next turn", async () => {
		await withSession({}, async (service, client) => {
			const ref = fleetSessionRef("s-tasklist");
			const read = async () => (await client.request("thread/read", { ref, includeTurns: true })).thread;
			const before = await read();
			await service.open(ref);
			await service.interrupt();
			const stopped = await read();
			const turn = stopped.turns?.at(-1);
			if (!turn?.startedAt) throw new Error("the stopped turn needs its start");
			expect(turn.status).toBe("interrupted");
			expect(turn.items?.filter((item) => item.status === "inProgress")).toEqual([]);
			// Its subagent calls settled when their launch receipts returned, so
			// only the step still running is interrupted.
			expect(turn.items?.filter((item) => item.status === "interrupted").map((item) => item.toolName)).toEqual([
				"shell",
			]);
			expect(turn.completedAt).toBeGreaterThanOrEqual(turn.startedAt);
			expect(turn.durationMs).toBe((turn.completedAt ?? 0) - turn.startedAt);
			expect(stopped.evener.lastTurnEndedAt).toBe(turn.completedAt);
			expect(stopped.evener.workMillis).toBeGreaterThan(before.evener.workMillis ?? 0);
			await service.open(ref);
			await service.send([{ type: "text", text: "Go on" }]);
			expect((await read()).turns?.at(-1)?.startedAt).toBeGreaterThanOrEqual(turn.completedAt ?? 0);
		});
	});

	it("measures the fleet's rows and its sessions' threads from one clock", async () => {
		// Every read of the clock moves it a second on, so two clocks would disagree.
		let clock = Date.parse("2026-09-28T21:00:00.000Z");
		const now = vi.spyOn(Date, "now").mockImplementation(() => {
			clock += 1000;
			return clock;
		});
		try {
			await withHub({}, async (client) => {
				const ref = fleetSessionRef("s-pr2138");
				// The hub has built its fleet; the rest runs on the real clock.
				now.mockRestore();
				const live = await client.request("evener/navigation/read", {
					representationVersion: 3,
					resource: "section",
					section: "live",
				});
				const { thread } = await client.request("thread/read", { ref, includeTurns: false });
				// The row's own object in the section's payload, wherever it sits.
				const found: { updated_at?: string }[] = [];
				JSON.stringify(live.data, (_key, value) => {
					if (value?.session_id === thread.sessionId && value?.kind === "session") found.push(value);
					return value;
				});
				expect(found.map((row) => row.updated_at)).toEqual([new Date(thread.updatedAt * 1000).toISOString()]);
			});
		} finally {
			now.mockRestore();
		}
	});

	it("stamps a thread's updatedAt with the second a mutation changed it", async () => {
		await withSession({}, async (service, client) => {
			const ref = fleetSessionRef("s-gateway");
			await service.open(ref);
			const at = Date.parse("2026-09-28T22:00:00.000Z");
			const now = vi.spyOn(Date, "now").mockReturnValue(at);
			try {
				await service.interrupt();
			} finally {
				now.mockRestore();
			}
			const { thread } = await client.request("thread/read", { ref, includeTurns: false });
			expect(thread.updatedAt).toBe(Math.floor(at / 1000));
		});
	});

	it("refuses notes writes on a session that needs a restart, as the hub does", async () => {
		// cmd/evener-hub/app_restart_required.go's daemonRestartRequiredError:
		// withSessionResume doesn't resume such a session.
		await withHub({}, async (client) => {
			const ref = fleetSessionRef("s-namer");
			const { thread } = await client.request("thread/read", { ref, includeTurns: false });
			const expectedInstanceId = thread.evener.instanceId ?? "";
			const refused = {
				code: -32013,
				message: expect.stringMatching(/^Session restart required: daemon pid \d+ uses an incompatible protocol;/),
				data: {
					evenerErrorInfo: "conflict",
					cause: "daemonRestartRequired",
					mutationOutcome: "unknown",
					retryDisposition: "blocked",
				},
			};
			await expect(
				client.request("notes/human/set", { ref, clientMutationId: "note-namer", expectedInstanceId, note: "x" }),
			).rejects.toMatchObject({ ...refused, data: { ...refused.data, clientMutationId: "note-namer" } });
			await expect(
				client.request("urls/remove", { ref, clientMutationId: "link-namer", expectedInstanceId, id: "u-1" }),
			).rejects.toMatchObject({ ...refused, data: { ...refused.data, clientMutationId: "link-namer" } });
			expect((await client.request("thread/read", { ref, includeTurns: false })).thread.status.type).toBe(
				"restartRequired",
			);
		});
	});

	it("clears frame 8's question once you answer it", async () => {
		await withSession({}, async (service, client) => {
			const ref = fleetSessionRef("s-audit");
			await service.open(ref);
			await service.send([{ type: "text", text: "[answers]\n1. [Implied options] → Drop them" }]);
			const { thread } = await client.request("thread/read", { ref, includeTurns: false });
			expect(thread.evener.askPending).toBeFalsy();
			expect(thread.evener).not.toHaveProperty("pendingQuestion");
		});
	});

	it("asks the working session's question in its thread too, when the fleet asks it", async () => {
		await withHub({}, async (client, hub) => {
			const ref = fleetSessionRef("s-gateway");
			const before = (await client.request("thread/read", { ref, includeTurns: false })).thread;
			hub.askQuestion();
			const { thread } = await client.request("thread/read", { ref, includeTurns: true });
			// The ended turn's time joins the session's work, as a Stop's does.
			const ended = thread.turns?.at(-1);
			expect(thread.evener.workMillis).toBe((before.evener.workMillis ?? 0) + (ended?.durationMs ?? Number.NaN));
			expect(thread.evener.lastTurnEndedAt).toBe(ended?.completedAt);
			expect(thread.status.type).toBe("awaiting");
			expect(thread.evener.askPending).toBe(true);
			expect(thread.evener.pendingQuestion).toMatchObject({
				question: "Where should the gateway token command store tokens?",
				count: 1,
			});
			const turn = thread.turns?.at(-1);
			expect(turn?.status).toBe("completed");
			expect(turn?.items?.at(-1)).toMatchObject({ toolName: "ask_user", status: "completed" });
		});
	});

	it("keeps Clear off while a held queue waits, and offers it once the queue empties", async () => {
		await withSession({}, async (service) => {
			const ref = fleetSessionRef("s-pr2138");
			await service.open(ref);
			await service.interrupt();
			const held = await service.open(ref);
			expect(held.capabilities.clear).toBe(false);
			const [entry] = held.queue?.ids ?? [];
			if (!entry || !held.instanceId) throw new Error("Missing queue guards");
			await service.cancelQueued(0, entry, held.instanceId);
			expect((await service.open(ref)).capabilities.clear).toBe(true);
		});
	});

	it("gives a shut-down session a live daemon's capabilities once you send to it", async () => {
		await withSession({}, async (service) => {
			const ref = fleetSessionRef("s-roster");
			await service.open(ref);
			await service.send([{ type: "text", text: "Measure it again" }]);
			const working = await service.open(ref);
			expect(working.status.type).toBe("active");
			expect(working.capabilities).toMatchObject({
				send: false,
				steer: true,
				interrupt: true,
				clear: false,
				forkFromTurn: false,
			});
			await service.interrupt();
			expect((await service.open(ref)).status.type).toBe("idle");
		});
	});

	it("keeps a stopped fleet session sendable, like a daemon", async () => {
		await withSession({}, async (service) => {
			await service.open(fleetSessionRef("s-gateway"));
			await service.interrupt();
			const stopped = await service.open(fleetSessionRef("s-gateway"));
			expect(stopped.capabilities).toMatchObject({
				send: true,
				clear: true,
				steer: true,
				interrupt: true,
				queue: true,
			});
			await service.send([{ type: "text", text: "Keep going" }]);
			const working = await service.open(fleetSessionRef("s-gateway"));
			expect(working.status.type).toBe("active");
			expect(working.capabilities).toMatchObject({ send: false, clear: false, steer: true, interrupt: true });
			expect(working.activeTurnStartedAt).toBeDefined();
		});
	});

	it("moves a fleet session's Board row with its turn, out of Working on Stop and back on send", async () => {
		await withHub({}, async (client) => {
			const ref = fleetSessionRef("s-pr2138");
			const rowState = async (resource: { resource: string; section?: string; sectionId?: string }) => {
				const read = await client.request("evener/navigation/read", {
					representationVersion: 3,
					...resource,
				} as NavigationReadParams);
				const entities = (read.data as { entities: { value: { session_id?: string; state?: string } }[] }).entities;
				return entities.find((entity) => entity.value.session_id === demoSessionId("s-pr2138"))?.value.state;
			};
			const liveState = () => rowState({ resource: "section", section: "live" });
			// s-pr2138 is pinned in the release section, whose rows carry the
			// same state, so its turn reaches that section too.
			const pinnedState = () => rowState({ resource: "pin_section", sectionId: "release" });
			const { thread } = await client.request("thread/read", { ref, includeTurns: false });
			const expectedInstanceId = thread.evener.instanceId ?? "";
			expect(await liveState()).toBe("active");
			expect(await pinnedState()).toBe("active");
			const stopped = navigationInvalidated(client);
			await client.request("turn/interrupt", { ref, expectedInstanceId, clientMutationId: "stop-pr2138" });
			const stoppedPayload = (await stopped) as { targets: { kind: string; sectionId?: string }[] };
			expect(stoppedPayload.targets).toEqual(
				expect.arrayContaining([expect.objectContaining({ kind: "pin_section", sectionId: "release" })]),
			);
			expect(await liveState()).toBe("idle");
			expect(await pinnedState()).toBe("idle");
			const restarted = navigationInvalidated(client);
			await client.request("turn/start", {
				ref,
				expectedInstanceId,
				clientMutationId: "start-pr2138",
				input: [{ type: "text", text: "Keep going" }],
			});
			await restarted;
			expect(await liveState()).toBe("active");
			expect(await pinnedState()).toBe("active");
		});
	});

	it("wakes a resting session with a changed note, in a real turn Stop can end", async () => {
		await withHub({}, async (client) => {
			const ref = fleetSessionRef("s-diff");
			const { thread } = await client.request("thread/read", { ref, includeTurns: false });
			expect(thread.status.type).toBe("idle");
			const saved = await client.request("notes/human/set", {
				ref,
				clientMutationId: "note-wake",
				expectedInstanceId: thread.evener.instanceId ?? "",
				note: "Cite the web version too.",
			});
			const after = (await client.request("thread/read", { ref, includeTurns: true })).thread;
			expect(after.status.type).toBe("active");
			expect(after.evener.capabilities.interrupt).toBe(true);
			expect(saved.receipt.turnId).toBe(after.evener.activeTurnId);
			const turn = after.turns?.find((candidate) => candidate.id === after.evener.activeTurnId);
			expect(turn?.items).toContainEqual(
				expect.objectContaining({ type: "steering", steeringKind: "human-note", clientMutationId: "note-wake" }),
			);
		});
	});

	it("resumes a shut-down session to take a notes write, as the hub does", async () => {
		// cmd/evener-hub/app_session_resume.go: setNotesHumanWithResume and
		// removeURLWithResume resume an exited session, then apply the write.
		await withHub({}, async (client) => {
			const ref = fleetSessionRef("s-roster");
			const read = async () => (await client.request("thread/read", { ref, includeTurns: false })).thread;
			const expectedInstanceId = (await read()).evener.instanceId ?? "";
			await client.request("urls/remove", { ref, clientMutationId: "link-roster", expectedInstanceId, id: "u-2290" });
			const resumed = await read();
			expect(resumed.status.type).toBe("idle");
			expect(resumed.evener.sessionUrls ?? []).toEqual([]);
			await client.request("notes/human/set", {
				ref,
				clientMutationId: "note-roster",
				expectedInstanceId,
				note: "Measure it again on magic-kingdom.",
			});
			const woken = await read();
			expect(woken.status.type).toBe("active");
			expect(woken.evener.humanNote).toBe("Measure it again on magic-kingdom.");
		});
	});

	it("saves your note and removes a link on a fleet session", async () => {
		await withHub({}, async (client) => {
			const ref = fleetSessionRef("s-pr2138");
			const { thread } = await client.request("thread/read", { ref, includeTurns: false });
			const expectedInstanceId = thread.evener.instanceId ?? "";
			const saved = await client.request("notes/human/set", {
				ref,
				clientMutationId: "note-1",
				expectedInstanceId,
				// Saved as the daemon stores it: whitespace runs collapsed.
				note: "  Fix causes,\n and   say which. ",
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
			const after = await client.request("thread/read", { ref, includeTurns: true });
			expect(after.thread.evener.humanNote).toBe("Fix causes, and say which.");
			// The changed note steers the agent, as the daemon records it.
			expect(after.thread.turns?.at(-1)?.items).toContainEqual(
				expect.objectContaining({
					type: "steering",
					source: "user",
					steeringKind: "human-note",
					text: "human updated their whiteboard: Fix causes, and say which.",
					clientMutationId: "note-1",
				}),
			);
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
			const ref = fleetSessionRef("s-mirror");
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

	it("lists data.js's model catalog, with its recent models, for the model sheet", async () => {
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
					ref: fleetSessionRef("s-pr2138"),
					includeTurns: false,
				}),
			).rejects.toThrow("Unknown demonstration session");
		});
	});
});

describe("the demo hub's staged events for the phase 6 screenshots", () => {
	it("plays a step typed on its command input for every connected client, and three for burst", async () => {
		const commands = new PassThrough();
		const hub = await createDemoHub(0, undefined, {}, { commands });
		const client = createHubClient(hub.origin, "", (url) => new WebSocket(url) as unknown as WebSocketLike);
		const sequences: number[] = [];
		client.onNotification((notification) => {
			if (notification.method === "evener/navigation/invalidated")
				sequences.push((notification.params as { sequence: number }).sequence);
		});
		try {
			await client.connect();
			commands.write("question\n");
			await vi.waitFor(() => expect(sequences).toEqual([1]));
			commands.write("burst\n");
			await vi.waitFor(() => expect(sequences).toEqual([1, 2, 3, 4]));
		} finally {
			client.close();
			await hub.close();
		}
	});

	it("runs a message queued for a resting fleet session as its own turn, as the daemon does", async () => {
		const hub = await createDemoHub(0, undefined, {});
		const client = createHubClient(hub.origin, "", (url) => new WebSocket(url) as unknown as WebSocketLike);
		const ref = fleetSessionRef("s-flakes");
		try {
			await client.connect();
			const { thread } = await client.request("thread/read", { ref, includeTurns: false });
			expect(thread.status.type).not.toBe("active");
			const queued = (await client.request("turn/queue", {
				ref,
				expectedInstanceId: thread.evener.instanceId ?? "",
				clientMutationId: "queued-1",
				input: [{ type: "text", text: "Check the nightly runs too." }],
			})) as { receipt: { turnId?: string; queueEntryIds?: string[] } };
			expect(queued.receipt.turnId).toBeDefined();
			expect(queued.receipt.queueEntryIds).toBeUndefined();
			const after = (await client.request("thread/read", { ref, includeTurns: true })).thread;
			expect(after.status.type).toBe("active");
			expect(after.evener.queue.depth ?? 0).toBe(0);
			expect(after.turns?.at(-1)?.items?.find((item) => item.type === "userMessage")?.text).toBe(
				"Check the nightly runs too.",
			);
		} finally {
			client.close();
			await hub.close();
		}
	});

	it("runs a message queued after Stop, since queueing again releases the Stop's hold", async () => {
		const hub = await createDemoHub(0);
		const client = createHubClient(hub.origin, "", (url) => new WebSocket(url) as unknown as WebSocketLike);
		const identity = { ref: "demo:playground", expectedInstanceId: "demo-instance" };
		try {
			await client.connect();
			await client.request("turn/start", {
				...identity,
				clientMutationId: "start-1",
				input: [{ type: "text", text: "Go" }],
			});
			await client.request("turn/interrupt", { ...identity, clientMutationId: "stop-1" });
			const queued = (await client.request("turn/queue", {
				...identity,
				clientMutationId: "queue-1",
				input: [{ type: "text", text: "One more" }],
			})) as { receipt: { turnId?: string } };
			expect(queued.receipt.turnId).toBeDefined();
			const thread = (await client.request("thread/read", { ref: identity.ref, includeTurns: false })).thread;
			expect(thread.status.type).toBe("active");
			expect(thread.evener.queue.depth ?? 0).toBe(0);
		} finally {
			client.close();
			await hub.close();
		}
	});

	it("runs the message a Stop parked first when another is queued, keeping the new one behind it", async () => {
		const hub = await createDemoHub(0);
		const client = createHubClient(hub.origin, "", (url) => new WebSocket(url) as unknown as WebSocketLike);
		const identity = { ref: "demo:playground", expectedInstanceId: "demo-instance" };
		const read = async () => (await client.request("thread/read", { ref: identity.ref, includeTurns: true })).thread;
		try {
			await client.connect();
			await client.request("turn/start", {
				...identity,
				clientMutationId: "start-1",
				input: [{ type: "text", text: "Go" }],
			});
			await client.request("turn/queue", {
				...identity,
				clientMutationId: "queue-1",
				input: [{ type: "text", text: "Parked one" }],
			});
			await client.request("turn/interrupt", { ...identity, clientMutationId: "stop-1" });
			expect((await read()).evener.queue.depth).toBe(1);
			await client.request("turn/queue", {
				...identity,
				clientMutationId: "queue-2",
				input: [{ type: "text", text: "New one" }],
			});
			const thread = await read();
			expect(thread.status.type).toBe("active");
			expect(thread.turns?.at(-1)?.items?.find((item) => item.type === "userMessage")?.text).toBe("Parked one");
			expect(thread.evener.queue.texts).toEqual(["New one"]);
		} finally {
			client.close();
			await hub.close();
		}
	});

	it("answers a queued send with an unknown outcome too, when told its outcomes can't be recorded", async () => {
		const hub = await createDemoHub(0, undefined, undefined, { unconfirmed: true });
		const client = createHubClient(hub.origin, "", (url) => new WebSocket(url) as unknown as WebSocketLike);
		try {
			await client.connect();
			await expect(
				client.request("turn/queue", {
					ref: "demo:playground",
					expectedInstanceId: "demo-instance",
					clientMutationId: "queue-1",
					input: [{ type: "text", text: "Go" }],
				}),
			).rejects.toMatchObject({ code: -32603, data: { clientMutationId: "queue-1", mutationOutcome: "unknown" } });
		} finally {
			client.close();
			await hub.close();
		}
	});

	it("stages each step on the session's own thread too, so a banner opens a session that agrees with its row", async () => {
		const commands = new PassThrough();
		const hub = await createDemoHub(0, undefined, {}, { commands });
		const client = createHubClient(hub.origin, "", (url) => new WebSocket(url) as unknown as WebSocketLike);
		const read = async (slug: string) =>
			(await client.request("thread/read", { ref: fleetSessionRef(slug), includeTurns: false })).thread;
		const sequences: number[] = [];
		client.onNotification((notification) => {
			if (notification.method === "evener/navigation/invalidated")
				sequences.push((notification.params as { sequence: number }).sequence);
		});
		try {
			await client.connect();
			commands.write("failure\napproval\nfinish\n");
			await vi.waitFor(() => expect(sequences).toEqual([1, 2, 3]));
			expect((await read("s-readintent")).status.type).toBe("systemError");
			const landing = await read("s-landing");
			expect(landing.status.type).toBe("active");
			expect(landing.evener.pendingEscalations).toHaveLength(1);
			const resume = await read("s-resume");
			expect(resume.status.type).toBe("idle");
			expect(resume.evener.activeTurnId).toBeUndefined();
		} finally {
			client.close();
			await hub.close();
		}
	});

	// The hub's notices (S11): evener/notices/list answers them, and a host
	// going offline or coming back announces the new list to every client.
	it("lists its notices, and announces the new list when paradise-park goes offline and comes back", async () => {
		const commands = new PassThrough();
		const hub = await createDemoHub(0, undefined, {}, { commands });
		const client = createHubClient(hub.origin, "", (url) => new WebSocket(url) as unknown as WebSocketLike);
		const announced: string[][] = [];
		client.onNotification((notification) => {
			if (notification.method === "evener/notices/changed")
				announced.push((notification.params as { notices: { id: string }[] }).notices.map((notice) => notice.id));
		});
		try {
			await client.connect();
			expect((await client.request("evener/notices/list", {})).notices.map((notice) => notice.id)).toEqual([
				"signInRequired:codex-jesse-fsck.com",
			]);
			commands.write("host-offline\nhost-online\n");
			await vi.waitFor(() =>
				expect(announced).toEqual([
					["signInRequired:codex-jesse-fsck.com", "hostOffline:paradise-park"],
					["signInRequired:codex-jesse-fsck.com"],
				]),
			);
		} finally {
			client.close();
			await hub.close();
		}
	});

	it("grows the working session by a finished step at a time on grow, for scroll checks", async () => {
		const commands = new PassThrough();
		const hub = await createDemoHub(0, undefined, {}, { commands, growEveryMs: 5 });
		const client = createHubClient(hub.origin, "", (url) => new WebSocket(url) as unknown as WebSocketLike);
		const ref = fleetSessionRef("s-pr2138");
		const items = async () =>
			((await client.request("thread/read", { ref, includeTurns: true, subscribe: true })).thread.turns ?? []).flatMap(
				(turn) => turn.items ?? [],
			);
		let resyncs = 0;
		client.onNotification((notification) => {
			if (notification.method === "evener/thread/resync") resyncs += 1;
		});
		try {
			await client.connect();
			const before = await items();
			commands.write("grow\n");
			await vi.waitFor(() => expect(resyncs).toBe(15));
			const after = await items();
			expect(after).toHaveLength(before.length + 15);
			expect(after.filter((item) => item.id.startsWith("demo-grow-"))).toHaveLength(15);
			expect(after.every((item, index) => index < before.length || item.status === "completed")).toBe(true);
		} finally {
			client.close();
			await hub.close();
		}
	});

	it("pages Get PR 2138's older history by item with EVENER_DEMO_FLEET_OLDER", async () => {
		const hub = await createDemoHub(0, undefined, { olderHistory: true });
		const client = createHubClient(hub.origin, "", (url) => new WebSocket(url) as unknown as WebSocketLike);
		const ref = fleetSessionRef("s-pr2138");
		const ids = (turns: { items?: { id: string }[] }[] | undefined) =>
			(turns ?? []).flatMap((turn) => (turn.items ?? []).map((item) => item.id));
		try {
			await client.connect();
			const full = await client.request("thread/read", { ref, includeTurns: true });
			const all = ids(full.thread.turns);
			expect(all.filter((id) => id.startsWith("demo-older-"))).toHaveLength(15 * 7);
			const first = await client.request("thread/read", { ref, includeTurns: true, itemLimit: 40 });
			const seen = ids(first.thread.turns);
			expect(seen).toEqual(all.slice(-40));
			let cursor = first.olderCursor;
			const pages: string[][] = [];
			while (cursor) {
				const page = await client.request("thread/turns/list", { ref, cursor, itemLimit: 40 });
				pages.unshift(ids(page.data));
				cursor = page.nextCursor;
			}
			expect([...pages.flat(), ...seen]).toEqual(all);
			// A page that starts inside a turn says so.
			const partial = await client.request("thread/read", { ref, includeTurns: true, itemLimit: 3 });
			expect(partial.thread.turns?.[0]).toMatchObject({ hasEarlierItems: true });
		} finally {
			client.close();
			await hub.close();
		}
	});

	it("says an unknown session is unknown, with or without EVENER_DEMO_FLEET_OLDER", async () => {
		for (const options of [{}, { olderHistory: true }]) {
			const hub = await createDemoHub(0, undefined, options);
			const client = createHubClient(hub.origin, "", (url) => new WebSocket(url) as unknown as WebSocketLike);
			try {
				await client.connect();
				await expect(
					client.request("thread/read", { ref: "local:nobody", includeTurns: true, itemLimit: 40 }),
				).rejects.toThrow("Unknown demonstration session");
			} finally {
				client.close();
				await hub.close();
			}
		}
	});

	it("serves the whole history, with no cursor, without EVENER_DEMO_FLEET_OLDER", async () => {
		const hub = await createDemoHub(0, undefined, {});
		const client = createHubClient(hub.origin, "", (url) => new WebSocket(url) as unknown as WebSocketLike);
		try {
			await client.connect();
			const read = await client.request("thread/read", {
				ref: fleetSessionRef("s-pr2138"),
				includeTurns: true,
				itemLimit: 3,
			});
			expect(read.olderCursor).toBeUndefined();
			expect(read.thread.turns?.flatMap((turn) => turn.items ?? []).length).toBeGreaterThan(3);
		} finally {
			client.close();
			await hub.close();
		}
	});

	it("lists the commands when it doesn't know the one typed", async () => {
		const commands = new PassThrough();
		const info = vi.spyOn(console, "info").mockImplementation(() => {});
		const hub = await createDemoHub(0, undefined, {}, { commands });
		try {
			commands.write("dance\n");
			await vi.waitFor(() =>
				expect(info).toHaveBeenCalledWith(
					"Unknown command dance. Commands: question, failure, approval, finish, host-offline, host-online, burst, grow",
				),
			);
		} finally {
			info.mockRestore();
			await hub.close();
		}
	});

	it("answers a send with an unknown outcome when told its outcomes can't be recorded", async () => {
		const hub = await createDemoHub(0, undefined, undefined, { unconfirmed: true });
		const client = createHubClient(hub.origin, "", (url) => new WebSocket(url) as unknown as WebSocketLike);
		try {
			await client.connect();
			await expect(
				client.request("turn/start", {
					ref: "demo:playground",
					expectedInstanceId: "demo-instance",
					clientMutationId: "start-1",
					input: [{ type: "text", text: "Go" }],
				}),
			).rejects.toMatchObject({
				code: -32603,
				data: {
					clientMutationId: "start-1",
					mutationOutcome: "unknown",
					retryDisposition: "blocked",
					cause: "persistenceUnavailable",
				},
			});
		} finally {
			client.close();
			await hub.close();
		}
	});

	it("advertises the protocol version it is told to, so the phone sees a mismatch", async () => {
		const hub = await createDemoHub(0, undefined, undefined, { protocolVersion: "evener-appwire-v0" });
		const socket = new WebSocket(`${hub.origin.replace("http", "ws")}/rpc`);
		try {
			await new Promise((resolve) => socket.once("open", resolve));
			const answer = new Promise<{ result?: { protocolVersion?: string } }>((resolve) =>
				socket.once("message", (data) => resolve(JSON.parse(String(data)))),
			);
			socket.send(JSON.stringify({ jsonrpc: "2.0", id: 1, method: "initialize", params: {} }));
			expect((await answer).result?.protocolVersion).toBe("evener-appwire-v0");
		} finally {
			socket.close();
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
