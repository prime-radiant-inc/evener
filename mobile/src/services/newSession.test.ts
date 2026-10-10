// NewSessionService tests with a fake AppwireClient. Covers:
// - start() wraps thread/start, forwards params, returns {thread, turn}
// - start() forwards optional model/effort fields
// - recentProjects() wraps evener/projects/recent and returns string[]
// - errors propagate

import { describe, expect, it } from "vitest";
import type {
	ModelDescriptor,
	ModelListResponse,
	ProjectsRecentResponse,
	Thread,
	ThreadStartResponse,
	Turn,
} from "@evener/appwire-client";
import { createNewSessionService } from "./newSession";

import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
class FakeAppwireClient extends FakeClient {
	constructor() {
		super("ready");
	}
}

// --- factories ---------------------------------------------------------------

function makeThread(over: Partial<Thread> = {}): Thread {
	return {
		id: "thread-new",
		sessionId: "session-new",
		preview: "started session",
		ephemeral: false,
		modelProvider: "anthropic",
		createdAt: 1_000_000,
		updatedAt: 1_000_000,
		status: { type: "idle" },
		cwd: "/tmp/project",
		cliVersion: "1.0.0",
		source: "local",
		evener: {
			ref: "ref-new",
			capabilities: {
				send: true,
				steer: true,
				interrupt: true,
				compact: true,
				clear: true,
				forkFromTurn: true,
				shutdown: true,
				changeModel: true,
				changeVisionModel: true,
				sharedNotes: false,
				queue: true,
				goal: true,
				rename: true,
			},
			queue: { revision: 0 },
		},
		...over,
	};
}

function makeTurn(over: Partial<Turn> = {}): Turn {
	return {
		id: "turn-1",
		itemsView: "default",
		status: "completed",
		...over,
	};
}

// --- tests ------------------------------------------------------------------

describe("NewSessionService", () => {
	it("start() calls thread/start with cwd and input", async () => {
		const client = new FakeAppwireClient();
		const thread = makeThread();
		const turn = makeTurn();
		client.on("thread/start", () => ({ thread, turn }) as ThreadStartResponse);

		const service = createNewSessionService(client);
		const result = await service.start({
			cwd: "/home/jesse/work",
			input: [{ type: "text", text: "fix the bug" }],
		});

		expect(client.calls[0]?.method).toBe("thread/start");
		const params = client.calls[0]?.params as { cwd: string; input?: unknown };
		expect(params.cwd).toBe("/home/jesse/work");
		expect(params.input).toEqual([{ type: "text", text: "fix the bug" }]);
		expect(result.thread.id).toBe("thread-new");
		expect(result.turn.id).toBe("turn-1");
	});

	it("start() forwards optional model and effort fields", async () => {
		const client = new FakeAppwireClient();
		client.on(
			"thread/start",
			() =>
				({
					thread: makeThread(),
					turn: makeTurn(),
				}) as ThreadStartResponse,
		);

		const service = createNewSessionService(client);
		await service.start({
			cwd: "/tmp",
			modelProvider: "openai",
			model: "gpt-4",
			reasoningEffort: "high",
		});

		const params = client.calls[0]?.params as {
			modelProvider?: string;
			model?: string;
			reasoningEffort?: string;
		};
		expect(params.modelProvider).toBe("openai");
		expect(params.model).toBe("gpt-4");
		expect(params.reasoningEffort).toBe("high");
	});

	it("start() with empty input sends no input field", async () => {
		const client = new FakeAppwireClient();
		client.on(
			"thread/start",
			() =>
				({
					thread: makeThread(),
					turn: makeTurn(),
				}) as ThreadStartResponse,
		);

		const service = createNewSessionService(client);
		await service.start({ cwd: "/tmp" });

		const params = client.calls[0]?.params as { input?: unknown };
		expect(params.input).toBeUndefined();
	});

	it("start() propagates errors from thread/start", async () => {
		const client = new FakeAppwireClient();
		client.on("thread/start", () => {
			throw new Error("invalid project path");
		});

		const service = createNewSessionService(client);
		await expect(service.start({ cwd: "/bad" })).rejects.toThrow("invalid project path");
	});

	it("recentProjects() calls evener/projects/recent and returns string[]", async () => {
		const client = new FakeAppwireClient();
		client.on("evener/projects/recent", () => ({ data: ["/a", "/b", "/c"] }) as ProjectsRecentResponse);

		const service = createNewSessionService(client);
		const projects = await service.recentProjects();

		expect(client.calls[0]?.method).toBe("evener/projects/recent");
		expect(projects).toEqual(["/a", "/b", "/c"]);
	});

	it("models() calls model/list with its scope and returns the response", async () => {
		const client = new FakeAppwireClient();
		const models: ModelDescriptor[] = [
			{ provider: "anthropic", model: "claude-sonnet-4-5" },
			{ provider: "openai", model: "gpt-5" },
		];
		const response: ModelListResponse = {
			data: models,
			recent: [models[1] as ModelDescriptor],
			diagnostics: [],
		};
		client.on("model/list", () => response);

		const service = createNewSessionService(client);
		const result = await service.models({
			cwd: "/tmp/project",
			harness: "evener",
		});

		expect(client.calls[0]).toEqual({
			method: "model/list",
			params: { cwd: "/tmp/project", harness: "evener" },
		});
		expect(result).toEqual(response);
	});

	it("models() defaults to an empty scope object", async () => {
		const client = new FakeAppwireClient();
		client.on("model/list", () => ({ data: [] }) as ModelListResponse);

		await createNewSessionService(client).models();

		expect(client.calls[0]).toEqual({ method: "model/list", params: {} });
	});

	it("recentProjects() propagates errors", async () => {
		const client = new FakeAppwireClient();
		client.on("evener/projects/recent", () => {
			throw new Error("unavailable");
		});

		const service = createNewSessionService(client);
		await expect(service.recentProjects()).rejects.toThrow("unavailable");
	});

	it("asks the hub's own machine directly", async () => {
		const client = new FakeAppwireClient();
		client.on("evener/projects/recent", () => ({ data: ["/home/jesse/git/evener"] }) as ProjectsRecentResponse);
		const service = createNewSessionService(client);
		expect(await service.recentProjects()).toEqual(["/home/jesse/git/evener"]);
		expect(await service.recentProjects("local")).toEqual(["/home/jesse/git/evener"]);
		expect(client.calls.map((call) => call.method)).toEqual(["evener/projects/recent", "evener/projects/recent"]);
	});

	it("asks another host through the hub, method by method", async () => {
		const client = new FakeAppwireClient();
		client.on("evener/host/request", (request) => {
			switch (request.method) {
				case "evener/projects/recent":
					return { data: ["/Users/jesse/git/evener"] };
				case "model/list":
					return { data: [{ provider: "lunaroute", model: "glm-5.3-vision" }] };
				case "evener/plugin/preview":
					return { plugins: [] };
				case "evener/path/validate":
					return { path: "/Users/jesse/git/evener", valid: true };
				case "evener/dirs/create":
					return { path: "/Users/jesse/git/scratch", created: true };
				case "evener/git/head":
					return { head: "main" };
				case "evener/launch/resolve":
					return { effective: { sandbox: "workspace-write" }, layers: {}, provenance: {} };
				default:
					throw new Error(`unexpected ${request.method}`);
			}
		});
		const service = createNewSessionService(client);
		const cwd = "/Users/jesse/git/evener";
		expect(await service.recentProjects("paradise-park")).toEqual([cwd]);
		await service.models({ cwd }, "paradise-park");
		await service.previewPlugins({ cwd }, "paradise-park");
		expect(await service.directoryExists("paradise-park", cwd)).toBe(true);
		expect(await service.createDirectory("paradise-park", "/Users/jesse/git/scratch")).toBe("/Users/jesse/git/scratch");
		expect(await service.branch("paradise-park", cwd)).toBe("main");
		expect((await service.resolveLaunch("paradise-park", cwd, {})).effective.sandbox).toBe("workspace-write");
		expect(client.calls.map((call) => call.params)).toEqual([
			{ host: "paradise-park", method: "evener/projects/recent", params: {} },
			{ host: "paradise-park", method: "model/list", params: { cwd } },
			{ host: "paradise-park", method: "evener/plugin/preview", params: { cwd } },
			{ host: "paradise-park", method: "evener/path/validate", params: { path: cwd, kind: "dir" } },
			{ host: "paradise-park", method: "evener/dirs/create", params: { path: "/Users/jesse/git/scratch" } },
			{ host: "paradise-park", method: "evener/git/head", params: { cwd } },
			{ host: "paradise-park", method: "evener/launch/resolve", params: { cwd } },
		]);
	});

	it("says a folder that isn't a repository has no branch", async () => {
		const client = new FakeAppwireClient();
		client.on("evener/git/head", () => ({ head: "" }));
		expect(await createNewSessionService(client).branch("local", "/tmp")).toBeNull();
	});

	it("starts on another host with its source, and on the hub's own machine without one", async () => {
		const client = new FakeAppwireClient();
		client.on("thread/start", () => ({ thread: makeThread(), turn: makeTurn() }) as ThreadStartResponse);
		const service = createNewSessionService(client);
		await service.start({ cwd: "/Users/jesse/git/evener", source: "paradise-park" });
		await service.start({ cwd: "/home/jesse/git/evener", source: "local" });
		await service.start({ cwd: "/home/jesse/git/evener" });
		expect(client.calls.map((call) => call.params)).toEqual([
			{ cwd: "/Users/jesse/git/evener", source: "paradise-park" },
			{ cwd: "/home/jesse/git/evener" },
			{ cwd: "/home/jesse/git/evener" },
		]);
	});
});
