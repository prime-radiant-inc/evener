import { describe, expect, it } from "vitest";
import { WireError } from "@evener/appwire-client";
import { type ConversationClientLike, createNewSessionService } from "../../mobile/src/services/newSession";
import { type CreationDraft, CreationDraftRepository } from "./creationDraftRepository";
import { creationImageDraft } from "./creationImageDraft";
import { ImageSelection } from "./imageSelection";
import { createNewSessionStore } from "./newSession";
import { openSqliteSyncDouble } from "./sqliteSync.testkit";

function deferred() {
	let resolve!: (value: unknown) => void;
	let reject!: (error: Error) => void;
	const promise = new Promise<unknown>((yes, no) => {
		resolve = yes;
		reject = no;
	});
	return { promise, resolve, reject };
}
const model = {
	provider: "p",
	model: "a",
	reasoningEffortLevels: ["low", "high"],
};
function setup() {
	const calls: {
		method: string;
		params: unknown;
		response: ReturnType<typeof deferred>;
	}[] = [];
	const service = createNewSessionService({
		request(method: string, params: unknown) {
			const response = deferred();
			calls.push({ method, params, response });
			return response.promise;
		},
	} as ConversationClientLike);
	const store = createNewSessionStore("hub-a");
	store.getState().bind(service);
	return { store, calls };
}
it("starts once with hub defaults and preserves exact prompt", async () => {
	const { store, calls } = setup();
	store.getState().setPrompt("  hello\n ");
	void store.getState().setCwd(" /project ");
	const pending = store.getState().submit();
	expect(await store.getState().submit()).toEqual({ status: "blocked" });
	const starts = calls.filter((c) => c.method === "thread/start");
	expect(starts).toHaveLength(1);
	expect(starts[0]?.params).toEqual({
		cwd: "/project",
		input: [{ type: "text", text: "  hello\n " }],
	});
	starts[0]?.response.resolve({
		thread: { id: "t", evener: { ref: "canonical/t" } },
		turn: {},
	});
	expect(await pending).toMatchObject({
		status: "created",
		hubId: "hub-a",
		thread: { evener: { ref: "canonical/t" } },
	});
});
it("resets dependent selections and suppresses obsolete model catalogs", async () => {
	const { store, calls } = setup();
	const first = store.getState().setCwd("/one");
	const second = store.getState().setCwd("/two");
	calls[1]?.response.resolve({ data: [model] });
	await second;
	store.getState().selectModel(model);
	store.getState().setReasoning("high");
	expect(store.getState().reasoning).toBe("high");
	const third = store.getState().setCwd("/three");
	expect(store.getState()).toMatchObject({
		model: null,
		reasoning: "",
		models: [],
	});
	calls[0]?.response.resolve({ data: [model] });
	await first;
	expect(store.getState().models).toEqual([]);
	calls[2]?.response.resolve({ data: [{ provider: "q", model: "b" }] });
	await third;
	store.getState().selectModel(model);
	expect(store.getState().model).toBeNull();
	store.getState().selectModel(store.getState().models[0] ?? null);
	store.getState().setReasoning("high");
	expect(store.getState().reasoning).toBe("");
	expect(calls[2]?.params).toEqual({ cwd: "/three" });
});
it("preserves input and explicitly reports failure without retry", async () => {
	const { store, calls } = setup();
	void store.getState().setCwd("/project");
	store.getState().setPrompt("keep me");
	const pending = store.getState().submit();
	calls[1]?.response.reject(new Error("lost reply"));
	expect(await pending).toEqual({ status: "failed" });
	expect(store.getState()).toMatchObject({
		cwd: "/project",
		prompt: "keep me",
		submitting: false,
	});
	expect(store.getState().error).toBeTruthy();
	expect(calls.filter((c) => c.method === "thread/start")).toHaveLength(1);
});
it("suppresses completion and metadata after disconnect", async () => {
	const { store, calls } = setup();
	const metadata = store.getState().loadMetadata();
	void store.getState().setCwd("/project");
	const pending = store.getState().submit();
	store.getState().bind(null);
	calls[0]?.response.resolve({ data: ["/obsolete"] });
	calls[1]?.response.resolve({ data: [model] });
	calls[2]?.response.resolve({ thread: { evener: { ref: "old" } }, turn: {} });
	await metadata;
	expect(await pending).toEqual({ status: "obsolete" });
	expect(store.getState()).toMatchObject({
		projects: [],
		models: [],
	});
});

it("retains hub A creation uncertainty when a delayed start completes after switching to hub B", async () => {
	const saved = new Map<string, CreationDraft>();
	const storage = () => ({
		read: (hubId: string) => saved.get(hubId) ?? null,
		write: (hubId: string, draft: CreationDraft) => {
			saved.set(hubId, structuredClone(draft));
		},
		clear: (hubId: string) => saved.delete(hubId),
	});
	const calls: ReturnType<typeof deferred>[] = [];
	const service = createNewSessionService({
		request(_method: string, _params: unknown) {
			const response = deferred();
			calls.push(response);
			return response.promise;
		},
	} as ConversationClientLike);
	const hubA = createNewSessionStore("hub-a", storage);
	hubA.getState().setCwd("/a");
	hubA.getState().setPrompt("keep A");
	hubA.getState().bind(service);
	const pending = hubA.getState().submit();
	expect(calls).toHaveLength(1);

	hubA.getState().bind(null);
	const hubB = createNewSessionStore("hub-b", storage);
	hubB.getState().setCwd("/b");
	hubB.getState().setPrompt("keep B");
	calls[0]?.resolve({
		thread: { evener: { ref: "hub-a/session" } },
		turn: {},
	});

	expect(await pending).toEqual({ status: "obsolete" });
	expect(hubB.getState()).toMatchObject({ cwd: "/b", prompt: "keep B" });
	expect(saved.get("hub-a")).toMatchObject({
		cwd: "/a",
		prompt: "keep A",
		unconfirmed: true,
	});
	expect(saved.get("hub-b")).toMatchObject({
		cwd: "/b",
		prompt: "keep B",
		unconfirmed: false,
	});

	const returnedA = createNewSessionStore("hub-a", storage);
	expect(returnedA.getState()).toMatchObject({
		cwd: "/a",
		prompt: "keep A",
		unconfirmedCreation: true,
	});
	expect(calls).toHaveLength(1);
});

it("keeps explicit choices when blurring an unchanged project directory", async () => {
	const { store, calls } = setup();
	const initial = store.getState().setCwd("/project");
	calls[0]?.response.resolve({ data: [model] });
	await initial;
	store.getState().selectModel(model);
	store.getState().setReasoning("high");
	const blur = store.getState().loadModels();
	calls[1]?.response.resolve({ data: [model] });
	await blur;
	expect(store.getState().model).toEqual(model);
	expect(store.getState().reasoning).toBe("high");
});

it("retains metadata failure across model success and clears it only after metadata succeeds", async () => {
	const { store, calls } = setup();
	const metadata = store.getState().loadMetadata();
	calls[0]?.response.reject(new Error("projects unavailable"));
	await metadata;
	expect(store.getState().metadataError).toBe("Couldn't load this host's recent projects.");
	const models = store.getState().loadModels();
	calls[1]?.response.resolve({ data: [model] });
	await models;
	expect(store.getState().metadataError).toBeTruthy();
	const retry = store.getState().loadMetadata();
	calls[2]?.response.resolve({ data: ["/project"] });
	await retry;
	expect(store.getState().metadataError).toBeNull();
	expect(store.getState().projects).toEqual(["/project"]);
});

it("retains model failure across metadata success until model recovery", async () => {
	const { store, calls } = setup();
	const models = store.getState().loadModels();
	calls[0]?.response.reject(new Error("models unavailable"));
	await models;
	expect(store.getState().modelError).toBe("Couldn't load this host's models. The hub's default model still works.");
	const metadata = store.getState().loadMetadata();
	calls[1]?.response.resolve({ data: [] });
	await metadata;
	expect(store.getState().modelError).toBeTruthy();
	const retry = store.getState().loadModels();
	calls[2]?.response.resolve({ data: [model] });
	await retry;
	expect(store.getState().modelError).toBeNull();
});

it("shows the hub rejection while retaining the draft without replay", async () => {
	const { store, calls } = setup();
	void store.getState().setCwd("/project");
	store.getState().setPrompt("keep me");
	const pending = store.getState().submit();
	calls[1]?.response.reject(new WireError("model is required", -32602));
	expect(await pending).toEqual({ status: "failed" });
	expect(store.getState().error).toContain("model is required");
	expect(store.getState()).toMatchObject({
		cwd: "/project",
		prompt: "keep me",
		submitting: false,
	});
	expect(calls.filter((c) => c.method === "thread/start")).toHaveLength(1);
});

it("retains available model and reasoning when refreshing the same project", async () => {
	const { store, calls } = setup();
	const initial = store.getState().setCwd("/project");
	calls[0]?.response.resolve({ data: [model] });
	await initial;
	store.getState().selectModel(model);
	store.getState().setReasoning("high");
	const refresh = store.getState().loadModels(true);
	expect(await store.getState().submit()).toEqual({ status: "blocked" });
	calls[1]?.response.resolve({ data: [model] });
	await refresh;
	expect(calls).toHaveLength(2);
	expect(store.getState()).toMatchObject({ model, reasoning: "high" });
	const changed = store.getState().loadModels(true);
	calls[2]?.response.resolve({
		data: [{ ...model, reasoningEffortLevels: ["low"] }],
	});
	await changed;
	expect(store.getState().model?.model).toBe("a");
	expect(store.getState().reasoning).toBe("");
	const removed = store.getState().loadModels(true);
	calls[3]?.response.resolve({ data: [] });
	await removed;
	expect(store.getState().model).toBeNull();
});

it("starts with per-launch overrides using the web scalar precedence without saving defaults", async () => {
	const { store, calls } = setup();
	const loading = store.getState().setCwd("/project");
	calls[0]?.response.resolve({ data: [model] });
	await loading;
	store.getState().selectModel(model);
	store.getState().setReasoning("low");
	store.getState().setLaunchOverrides({
		model: "q/advanced",
		reasoningEffort: "high",
		maxRounds: 0,
		env: { EMPTY: "" },
		modelFallbacks: [],
	});
	const pending = store.getState().submit();
	const start = calls.find((c) => c.method === "thread/start");
	expect(start?.params).toEqual({
		cwd: "/project",
		model: "q/advanced",
		reasoningEffort: "high",
		launchOverrides: {
			model: "q/advanced",
			reasoningEffort: "high",
			maxRounds: 0,
			env: { EMPTY: "" },
			modelFallbacks: [],
		},
	});
	expect(calls.filter((c) => c.method === "evener/launch/setLayer")).toHaveLength(0);
	start?.response.reject(new Error("reply lost"));
	expect(await pending).toEqual({ status: "failed" });
	expect(store.getState().launchOverrides.maxRounds).toBe(0);
	store.getState().bind(null);
	expect(store.getState().launchOverrides.env).toEqual({ EMPTY: "" });
});

it("keeps per-launch drafts isolated by hub and snapshots caller-owned values", () => {
	const a = createNewSessionStore("a");
	const b = createNewSessionStore("b");
	const overrides = { env: { MODE: "test" } };
	a.getState().setLaunchOverrides(overrides);
	overrides.env.MODE = "changed";
	expect(a.getState().launchOverrides.env).toEqual({ MODE: "test" });
	expect(b.getState().launchOverrides).toEqual({});
	a.getState().setLaunchOverrides({});
	expect(a.getState().launchOverrides).toEqual({});
});

it("lets composer choices replace advanced model and reasoning without retaining a hidden override", async () => {
	const { store, calls } = setup();
	const loading = store.getState().setCwd("/project");
	calls[0]?.response.resolve({ data: [model] });
	await loading;
	store.getState().setLaunchOverrides({
		model: "q/old",
		reasoningEffort: "high",
		maxRounds: 7,
	});
	store.getState().selectModel(model);
	expect(store.getState()).toMatchObject({
		model,
		reasoning: "high",
		launchOverrides: { maxRounds: 7 },
	});
	store.getState().setLaunchOverrides({
		model: "p/a",
		reasoningEffort: "high",
		maxRounds: 7,
	});
	store.getState().setReasoning("low");
	expect(store.getState()).toMatchObject({
		reasoning: "low",
		launchOverrides: { model: "p/a", maxRounds: 7 },
	});
	store.getState().selectModel(null);
	expect(store.getState()).toMatchObject({
		model: null,
		reasoning: "",
		launchOverrides: { maxRounds: 7 },
	});
});

it("submits composer reasoning for a model selected through session options", async () => {
	const { store, calls } = setup();
	const loading = store.getState().setCwd("/project");
	calls[0]?.response.resolve({ data: [model] });
	await loading;
	store.getState().setLaunchOverrides({ model: "p/a", maxRounds: 7 });
	store.getState().setReasoning("low");
	const pending = store.getState().submit();
	const start = calls.find((call) => call.method === "thread/start");
	expect(start?.params).toMatchObject({
		model: "p/a",
		reasoningEffort: "low",
		launchOverrides: { maxRounds: 7 },
	});
	start?.response.resolve({
		thread: { id: "t", evener: { ref: "canonical/t" } },
		turn: {},
	});
	expect(await pending).toMatchObject({ status: "created" });
});

it("revalidates advanced-model composer reasoning on a project-settings round trip", async () => {
	const { store, calls } = setup();
	const loading = store.getState().setCwd("/project");
	calls[0]?.response.resolve({ data: [model] });
	await loading;
	store.getState().setLaunchOverrides({ model: "p/a", maxRounds: 7 });
	store.getState().setReasoning("low");
	const refresh = store.getState().loadModels(true);
	calls[1]?.response.resolve({ data: [model] });
	await refresh;
	expect(store.getState()).toMatchObject({
		model: null,
		reasoning: "low",
		launchOverrides: { model: "p/a", maxRounds: 7 },
	});
	const changed = store.getState().loadModels(true);
	calls[2]?.response.resolve({
		data: [{ ...model, reasoningEffortLevels: ["high"] }],
	});
	await changed;
	expect(store.getState().reasoning).toBe("");
});

it("sends opening images with translated anchors and retains them after an uncertain start", async () => {
	const { store, calls } = setup();
	void store.getState().setCwd("/project");
	store.getState().setPrompt("Look: ");
	store.getState().addImage({
		id: "image-a",
		marker: 1,
		name: "sample.png",
		mediaType: "image/png",
		data: "AQID",
	});
	const pending = store.getState().submit();
	const start = calls.find((call) => call.method === "thread/start");
	expect(start?.params).toMatchObject({
		input: [
			{ type: "text", text: "Look: (attached image 1: sample.png)" },
			{
				type: "image",
				name: "sample.png",
				mediaType: "image/png",
				data: "AQID",
			},
		],
	});
	store.getState().removeImage("image-a");
	expect(store.getState().images).toHaveLength(1);
	start?.response.reject(new Error("lost reply"));
	expect(await pending).toEqual({ status: "failed" });
	store.getState().bind(null);
	expect(store.getState().images[0]?.data).toBe("AQID");
	expect(store.getState().prompt).toBe("Look: [image 1]");
	expect(createNewSessionStore("another-hub").getState().images).toEqual([]);
	store.getState().removeImage("image-a");
	expect(store.getState().images).toEqual([]);
	expect(store.getState().prompt).toBe("Look: ");
});

it("supports image-only creation and snapshots image bytes before sending", async () => {
	const { store, calls } = setup();
	void store.getState().setCwd("/project");
	const image = {
		id: "image-a",
		marker: 1,
		mediaType: "image/png",
		data: "AQID",
	};
	store.getState().addImage(image);
	image.data = "changed";
	store.getState().setPrompt("  ");
	const pending = store.getState().submit();
	const start = calls.find((call) => call.method === "thread/start");
	expect(start?.params).toMatchObject({
		input: [{ type: "image", mediaType: "image/png", data: "AQID" }],
	});
	start?.response.resolve({
		thread: { id: "t", evener: { ref: "local:t" } },
		turn: {},
	});
	expect(await pending).toMatchObject({ status: "created" });
});

it("uses the shared picker pipeline without attaching late results to an abandoned creation form", async () => {
	const { store } = setup();
	const document = creationImageDraft(store);
	const encoding = deferred();
	const started = deferred();
	const selection = new ImageSelection(document, {
		id: () => "photo",
		capture: async () => [],
		pick: async () => [
			{
				uri: "file:///photo.jpg",
				name: "photo.jpg",
				type: "image/jpeg",
				size: 10,
			},
		],
		encode: async () => {
			started.resolve(null);
			return (await encoding.promise) as string;
		},
	});
	const choosing = selection.choose();
	await started.promise;
	expect(selection.getSnapshot().busy).toBe(true);
	selection.cancel();
	encoding.resolve("AQID");
	await choosing;
	expect(store.getState().images).toEqual([]);
	await selection.choose();
	expect(document.imagePreviews()).toEqual([{ marker: 2, name: "photo.jpg", mediaType: "image/png", data: "AQID" }]);
	expect(store.getState().prompt).toBe("[image 2]");
	document.removeImage("photo");
	expect(document.imagePreviews()).toEqual([]);
	expect(store.getState().prompt).toBe("");
});

const answer = (calls: ReturnType<typeof setup>["calls"], method: string, forwarded: string | null, value: unknown) => {
	const call = calls.find(
		(c) => c.method === method && (forwarded === null || (c.params as { method?: string }).method === forwarded),
	);
	if (!call) throw new Error(`no ${forwarded ?? method} request`);
	calls.splice(calls.indexOf(call), 1);
	call.response.resolve(value);
};
const flush = () => new Promise((resolve) => setTimeout(resolve, 0));

// changeHost ends by reading the new host's models, so each test answers that
// read before awaiting the change.
it("reads another host's projects and models through the hub, and starts there (Review Focus 1)", async () => {
	const { store, calls } = setup();
	const moving = store.getState().changeHost("paradise-park", "paradise-park");
	answer(calls, "evener/host/request", "evener/projects/recent", { data: ["/Users/jesse/git/evener"] });
	await flush();
	expect(store.getState()).toMatchObject({ source: "paradise-park", cwd: "/Users/jesse/git/evener", hostNote: null });
	answer(calls, "evener/host/request", "model/list", { data: [model] });
	await moving;
	const started = store.getState().submit();
	const start = calls.find((c) => c.method === "thread/start");
	expect(start?.params).toMatchObject({ cwd: "/Users/jesse/git/evener", source: "paradise-park" });
	answer(calls, "thread/start", null, { thread: { id: "t", evener: { ref: "paradise-park:t" } }, turn: {} });
	expect(await started).toMatchObject({ status: "created" });
});

it("keeps the project when the new host has it, and moves it with a note when it doesn't (ruling 17)", async () => {
	const { store, calls } = setup();
	await store.getState().setCwd("/home/jesse/git/evener", false);
	const kept = store.getState().changeHost("paradise-park", "paradise-park");
	answer(calls, "evener/host/request", "evener/path/validate", { path: "/home/jesse/git/evener", valid: true });
	answer(calls, "evener/host/request", "evener/projects/recent", { data: ["/Users/jesse/git/docs"] });
	await flush();
	expect(store.getState()).toMatchObject({ cwd: "/home/jesse/git/evener", hostNote: null });
	answer(calls, "evener/host/request", "model/list", { data: [model] });
	await kept;
	const moved = store.getState().changeHost("local", "magic-kingdom");
	answer(calls, "evener/path/validate", null, { path: "/home/jesse/git/evener", valid: false });
	answer(calls, "evener/projects/recent", null, { data: ["/home/jesse/git/docs"] });
	await flush();
	expect(store.getState()).toMatchObject({
		cwd: "/home/jesse/git/docs",
		hostNote: "evener isn't on magic-kingdom, so the project changed to docs.",
	});
	answer(calls, "model/list", null, { data: [model] });
	await moved;
});

it("keeps the project when the new host can't say whether it has it", async () => {
	const { store, calls } = setup();
	await store.getState().setCwd("/home/jesse/git/evener", false);
	const moving = store.getState().changeHost("paradise-park", "paradise-park");
	const validate = calls.find((c) => (c.params as { method?: string }).method === "evener/path/validate");
	validate?.response.reject(new Error("host went away"));
	answer(calls, "evener/host/request", "evener/projects/recent", { data: ["/Users/jesse/git/docs"] });
	await flush();
	expect(store.getState()).toMatchObject({ cwd: "/home/jesse/git/evener", hostNote: null });
	answer(calls, "evener/host/request", "model/list", { data: [model] });
	await moving;
});

it("drops answers for the host it just left (Review Focus 2)", async () => {
	const { store, calls } = setup();
	const leaving = store.getState().changeHost("paradise-park", "paradise-park");
	const staleRecent = calls.find((c) => (c.params as { method?: string }).method === "evener/projects/recent");
	const arriving = store.getState().changeHost("local", "magic-kingdom");
	answer(calls, "evener/projects/recent", null, { data: ["/home/jesse/git/evener"] });
	await flush();
	answer(calls, "model/list", null, { data: [model] });
	await arriving;
	staleRecent?.response.resolve({ data: ["/Users/jesse/git/elsewhere"] });
	await leaving;
	expect(store.getState()).toMatchObject({ source: "local", cwd: "/home/jesse/git/evener" });
	expect(store.getState().projects).toEqual(["/home/jesse/git/evener"]);
	expect(calls.some((c) => (c.params as { method?: string }).method === "model/list")).toBe(false);
});

it("drops the host-change line once you choose a project yourself", async () => {
	const { store } = setup();
	store.setState({ hostNote: "evener isn't on paradise-park, so the project changed to docs." });
	await store.getState().setCwd("/Users/jesse/git/evener", false);
	expect(store.getState().hostNote).toBeNull();
});

it("applies a setup and settles its model against the host's list", async () => {
	const { store, calls } = setup();
	store.getState().applySetup({
		host: "local",
		cwd: "/project",
		model: { provider: "p", model: "a" },
		effort: "high",
		overrides: { sandbox: "read-only", enabledPlugins: ["superpowers"] },
	});
	expect(store.getState()).toMatchObject({
		source: "local",
		cwd: "/project",
		reasoning: "high",
		launchOverrides: { sandbox: "read-only", enabledPlugins: ["superpowers"] },
	});
	answer(calls, "model/list", null, { data: [model] });
	await flush();
	expect(store.getState().model).toEqual(model);
	expect(store.getState().reasoning).toBe("high");
});

it("applies a session's seed and finds its model once the host's models arrive", async () => {
	const { store, calls } = setup();
	store.getState().applySeed({ host: "local", cwd: "/project", model: "p/a", effort: "low" });
	answer(calls, "model/list", null, { data: [model] });
	await flush();
	expect(store.getState()).toMatchObject({ source: "local", cwd: "/project", model, reasoning: "low" });
});

it.each([
	[
		"a setup",
		(store: ReturnType<typeof setup>["store"]) =>
			store.getState().applySetup({ host: "local", cwd: "/project", model: null, effort: "", overrides: {} }),
	],
	[
		"a seed",
		(store: ReturnType<typeof setup>["store"]) => store.getState().applySeed({ host: "local", cwd: "/project" }),
	],
])("drops a stale per-launch model and effort when %s is applied, as choosing a model does", (_name, apply) => {
	const { store } = setup();
	store.getState().setLaunchOverrides({ model: "p/stale", reasoningEffort: "max", sandbox: "read-only" });
	apply(store);
	const overrides = store.getState().launchOverrides;
	expect(overrides.model).toBeUndefined();
	expect(overrides.reasoningEffort).toBeUndefined();
});

it("restores a draft's host, and a draft saved before hosts as the hub's own machine (ruling 28)", () => {
	const saved = new Map<string, CreationDraft>();
	const storage = () => ({
		read: (hubId: string) => saved.get(hubId) ?? null,
		write: (hubId: string, draft: CreationDraft) => {
			saved.set(hubId, structuredClone(draft));
		},
		clear: (hubId: string) => saved.delete(hubId),
	});
	const draft: CreationDraft = {
		cwd: "/project",
		prompt: "",
		harness: "",
		model: null,
		reasoning: "",
		launchOverrides: {},
		images: [],
		unconfirmed: false,
	};
	saved.set("before", draft);
	saved.set("remote", { ...draft, source: "paradise-park" });
	expect(createNewSessionStore("before", storage).getState().source).toBe("local");
	const remote = createNewSessionStore("remote", storage);
	expect(remote.getState().source).toBe("paradise-park");
	remote.getState().setPrompt("hello");
	expect(saved.get("remote")).toMatchObject({ source: "paradise-park", prompt: "hello" });
});

it("keeps the new host's projects when the old host's recent list answers late", async () => {
	const { store, calls } = setup();
	const metadata = store.getState().loadMetadata();
	const staleRecent = calls.find((c) => c.method === "evener/projects/recent");
	const moving = store.getState().changeHost("paradise-park", "paradise-park");
	answer(calls, "evener/host/request", "evener/projects/recent", { data: ["/Users/jesse/git/evener"] });
	await flush();
	answer(calls, "evener/host/request", "model/list", { data: [model] });
	await moving;
	if (staleRecent) calls.splice(calls.indexOf(staleRecent), 1);
	staleRecent?.response.resolve({ data: ["/home/jesse/git/evener"] });
	await metadata;
	expect(store.getState().projects).toEqual(["/Users/jesse/git/evener"]);
});

it("won't start while a host change is still placing the project, even on the hub's default model", async () => {
	const { store, calls } = setup();
	await store.getState().setCwd("/home/jesse/git/evener", false);
	const moving = store.getState().changeHost("paradise-park", "paradise-park");
	expect(store.getState().movingHost).toBe(true);
	expect(await store.getState().submit()).toEqual({ status: "blocked" });
	expect(calls.some((c) => c.method === "thread/start")).toBe(false);
	answer(calls, "evener/host/request", "evener/path/validate", { path: "/home/jesse/git/evener", valid: false });
	answer(calls, "evener/host/request", "evener/projects/recent", { data: ["/Users/jesse/git/docs"] });
	await flush();
	expect(store.getState().movingHost).toBe(false);
	answer(calls, "evener/host/request", "model/list", { data: [model] });
	await moving;
});

it("drops a host change's answers once the connection changes, and stops placing", async () => {
	const { store, calls } = setup();
	await store.getState().setCwd("/Users/jesse/git/evener", false);
	const moving = store.getState().changeHost("paradise-park", "paradise-park");
	const validate = calls.find((c) => (c.params as { method?: string }).method === "evener/path/validate");
	const recent = calls.find((c) => (c.params as { method?: string }).method === "evener/projects/recent");
	store.getState().bind(null);
	expect(store.getState().movingHost).toBe(false);
	validate?.response.resolve({ path: "/Users/jesse/git/evener", valid: false });
	recent?.response.resolve({ data: ["/Users/jesse/git/docs"] });
	await moving;
	expect(store.getState()).toMatchObject({ cwd: "/Users/jesse/git/evener", hostNote: null, movingHost: false });
});

it("keeps a project chosen while a host change was still answering (not the host's recent one)", async () => {
	const { store, calls } = setup();
	const moving = store.getState().changeHost("paradise-park", "paradise-park");
	await store.getState().setCwd("/Users/jesse/git/mine", false);
	answer(calls, "evener/host/request", "evener/projects/recent", { data: ["/Users/jesse/git/evener"] });
	await moving;
	expect(store.getState()).toMatchObject({ source: "paradise-park", cwd: "/Users/jesse/git/mine", hostNote: null });
	expect(store.getState().projects).toEqual(["/Users/jesse/git/evener"]);
	expect(store.getState().movingHost).toBe(false);
});

it("forgets a session's model once the form moves somewhere else", async () => {
	const { store, calls } = setup();
	store.getState().applySeed({ host: "local", cwd: "/project", model: "p/a" });
	const failed = calls.find((c) => c.method === "model/list");
	if (failed) calls.splice(calls.indexOf(failed), 1);
	failed?.response.reject(new Error("offline"));
	await flush();
	const moved = store.getState().setCwd("/other");
	answer(calls, "model/list", null, { data: [model] });
	await moved;
	expect(store.getState().model).toBeNull();
});

it("reads the recent projects of the host an applied setup names, dropping the old host's at once", async () => {
	const { store, calls } = setup();
	const metadata = store.getState().loadMetadata();
	answer(calls, "evener/projects/recent", null, { data: ["/home/jesse/git/evener"] });
	await metadata;
	expect(store.getState().projects).toEqual(["/home/jesse/git/evener"]);
	store
		.getState()
		.applySetup({ host: "paradise-park", cwd: "/Users/jesse/git/evener", model: null, effort: "", overrides: {} });
	expect(store.getState().projects).toEqual([]);
	answer(calls, "evener/host/request", "evener/projects/recent", { data: ["/Users/jesse/git/evener"] });
	answer(calls, "evener/host/request", "model/list", { data: [model] });
	await flush();
	expect(store.getState().projects).toEqual(["/Users/jesse/git/evener"]);
});

it("drops a model list for the place the form left when a setup is applied", async () => {
	const { store, calls } = setup();
	const stale = store.getState().setCwd("/one");
	const staleModels = calls.find((c) => c.method === "model/list");
	store.getState().applySetup({ host: "local", cwd: "/two", model: null, effort: "", overrides: {} });
	if (staleModels) calls.splice(calls.indexOf(staleModels), 1);
	staleModels?.response.resolve({ data: [{ provider: "stale", model: "old" }] });
	await stale;
	expect(store.getState().models).toEqual([]);
	answer(calls, "model/list", null, { data: [model] });
	await flush();
	expect(store.getState().models).toEqual([model]);
});

it("restores a draft with an empty host as the hub's own machine", () => {
	const saved = new Map<string, CreationDraft>();
	const storage = () => ({
		read: (hubId: string) => saved.get(hubId) ?? null,
		write: (hubId: string, draft: CreationDraft) => {
			saved.set(hubId, structuredClone(draft));
		},
		clear: (hubId: string) => saved.delete(hubId),
	});
	saved.set("hub", {
		source: "",
		cwd: "/project",
		prompt: "",
		harness: "",
		model: null,
		reasoning: "",
		launchOverrides: {},
		images: [],
		unconfirmed: false,
	});
	expect(createNewSessionStore("hub", storage).getState().source).toBe("local");
});

function memoryDrafts() {
	const saved = new Map<string, CreationDraft>();
	const storage = () => ({
		read: (hubId: string) => saved.get(hubId) ?? null,
		write: (hubId: string, draft: CreationDraft) => {
			saved.set(hubId, structuredClone(draft));
		},
		clear: (hubId: string) => saved.delete(hubId),
	});
	return { saved, storage };
}

it("discards the saved draft and empties the form, leaving other hubs' drafts alone", async () => {
	const { saved, storage } = memoryDrafts();
	const other = createNewSessionStore("hub-b", storage);
	other.getState().setPrompt("keep me");
	const store = createNewSessionStore("hub-a", storage);
	await store.getState().setCwd("/project", false);
	store.getState().setPrompt("throw me away");
	store.getState().setLaunchOverrides({ enabledPlugins: ["superpowers"] });
	expect(saved.get("hub-a")).toMatchObject({ prompt: "throw me away" });
	store.getState().discard();
	expect(store.getState()).toMatchObject({
		source: "local",
		cwd: "",
		prompt: "",
		images: [],
		model: null,
		reasoning: "",
		launchOverrides: {},
		error: null,
	});
	expect(saved.has("hub-a")).toBe(false);
	expect(saved.get("hub-b")).toMatchObject({ prompt: "keep me" });
	expect(createNewSessionStore("hub-a", storage).getState().prompt).toBe("");
});

it("keeps the plugin selection and sends nothing when the plugin check fails", async () => {
	const { store, calls } = setup();
	await store.getState().setCwd("/project", false);
	store.getState().setLaunchOverrides({ enabledPlugins: ["superpowers"] });
	const started = store.getState().submit();
	const preview = calls.find((c) => c.method === "evener/plugin/preview");
	preview?.response.reject(new Error("socket closed"));
	expect(await started).toEqual({ status: "failed" });
	expect(calls.some((c) => c.method === "thread/start")).toBe(false);
	expect(store.getState().launchOverrides.enabledPlugins).toEqual(["superpowers"]);
	expect(store.getState().error).toBe(
		"Couldn't check the selected plugins, so no session was started. Your selection is kept.",
	);
});

it("never asks the hub for harnesses, and starts with none", async () => {
	const { store, calls } = setup();
	const metadata = store.getState().loadMetadata();
	answer(calls, "evener/projects/recent", null, { data: ["/project"] });
	await metadata;
	expect(calls.map((c) => c.method)).not.toContain("evener/harnesses/list");
	await store.getState().setCwd("/project", false);
	const started = store.getState().submit();
	const start = calls.find((c) => c.method === "thread/start");
	expect(start?.params).toEqual({ cwd: "/project" });
	answer(calls, "thread/start", null, { thread: { id: "t", evener: { ref: "canonical/t" } }, turn: {} });
	await started;
});

it("saves the empty form over the draft when the device won't delete it", async () => {
	const { saved, storage } = memoryDrafts();
	const store = createNewSessionStore("hub-a", () => ({
		...storage(),
		clear: () => {
			throw new Error("disk full");
		},
	}));
	await store.getState().setCwd("/project", false);
	store.getState().setPrompt("throw me away");
	store.getState().discard();
	expect(store.getState()).toMatchObject({ cwd: "", prompt: "" });
	expect(saved.get("hub-a")).toMatchObject({ cwd: "", prompt: "" });
});

it("keeps the host's recent models with its list, and drops both when the form moves", async () => {
	const { store, calls } = setup();
	const other = { provider: "q", model: "b" };
	const loading = store.getState().setCwd("/project");
	answer(calls, "model/list", null, { data: [model, other], recent: [other] });
	await loading;
	expect(store.getState().recentModels).toEqual([other]);
	const moving = store.getState().changeHost("paradise-park", "paradise-park");
	expect(store.getState().recentModels).toEqual([]);
	answer(calls, "evener/host/request", "evener/path/validate", { path: "/project", valid: true });
	answer(calls, "evener/host/request", "evener/projects/recent", { data: [] });
	await flush();
	answer(calls, "evener/host/request", "model/list", { data: [model] });
	await moving;
	expect(store.getState().recentModels).toEqual([]);
});

/** A store over `saved`, bound to a hub whose model list is `models`, with every
 * request recorded. */
function restored(draft: Partial<CreationDraft>, models: unknown[]) {
	const { saved, storage } = memoryDrafts();
	saved.set("hub-a", {
		source: "local",
		cwd: "/project",
		prompt: "go",
		harness: "",
		model: null,
		reasoning: "",
		launchOverrides: {},
		images: [],
		unconfirmed: false,
		...draft,
	});
	const requests: { method: string; params: unknown }[] = [];
	const store = createNewSessionStore("hub-a", storage);
	store.getState().bind(
		createNewSessionService({
			request: async (method: string, params: unknown) => {
				requests.push({ method, params });
				if (method === "model/list") return { data: models };
				return { thread: { id: "t", evener: { ref: "local:t" } }, turn: {} };
			},
		} as ConversationClientLike),
	);
	return { store, requests };
}

it("makes a saved per-launch model the form's choice when the host lists it", async () => {
	const { store, requests } = restored({ launchOverrides: { model: "p/a", reasoningEffort: "high", maxRounds: 7 } }, [
		model,
	]);
	await store.getState().loadModels(true);
	expect(store.getState()).toMatchObject({ model, reasoning: "high", launchOverrides: { maxRounds: 7 } });
	await store.getState().submit();
	expect(requests.find((r) => r.method === "thread/start")?.params).toMatchObject({
		model: "a",
		modelProvider: "p",
		reasoningEffort: "high",
		launchOverrides: { maxRounds: 7 },
	});
});

it("never sends a saved per-launch model the host doesn't list", async () => {
	const { store, requests } = restored({ launchOverrides: { model: "gone/old", maxRounds: 7 } }, [model]);
	await store.getState().loadModels(true);
	expect(store.getState().model).toBeNull();
	await store.getState().submit();
	const start = requests.find((r) => r.method === "thread/start")?.params as Record<string, unknown>;
	expect(start).not.toHaveProperty("model");
	expect(start.launchOverrides).toEqual({ maxRounds: 7 });
});

// A per-launch effort with no model, which only an older build could save,
// is dropped: the form's effort belongs to its model, and with Hub default
// there is no model whose levels could take it.
it("drops a saved per-launch effort that came with no model", async () => {
	const { store, requests } = restored({ launchOverrides: { reasoningEffort: "high", maxRounds: 7 } }, [model]);
	await store.getState().loadModels(true);
	expect(store.getState()).toMatchObject({ model: null, reasoning: "", launchOverrides: { maxRounds: 7 } });
	await store.getState().submit();
	const start = requests.find((r) => r.method === "thread/start")?.params as Record<string, unknown>;
	expect(start).not.toHaveProperty("reasoningEffort");
	expect(start.launchOverrides).toEqual({ maxRounds: 7 });
});

describe("a start that lands clears only the draft it started", () => {
	function repository() {
		const { port } = openSqliteSyncDouble();
		const drafts = new CreationDraftRepository(port);
		return () => drafts;
	}
	function heldHub() {
		const starts: ReturnType<typeof deferred>[] = [];
		const service = createNewSessionService({
			request(method: string) {
				if (method === "thread/start") {
					const start = deferred();
					starts.push(start);
					return start.promise;
				}
				return Promise.resolve({ data: [] });
			},
		} as unknown as ConversationClientLike);
		return { service, starts };
	}
	const landed = { thread: { id: "t", evener: { ref: "local:t" } }, turn: {} };

	it("clears the draft and empties the form when the draft is still the one it started", async () => {
		const storage = repository();
		const { service, starts } = heldHub();
		const store = createNewSessionStore("hub-a", storage);
		store.getState().bind(service);
		await store.getState().setCwd("/project", false);
		store.getState().setPrompt("fix the flaky test");
		const started = store.getState().submit();
		await flush();
		starts[0]?.resolve(landed);
		expect(await started).toMatchObject({ status: "created" });
		expect(storage().read("hub-a")).toBeNull();
		expect(store.getState()).toMatchObject({ cwd: "", prompt: "" });
	});

	it("keeps a newer draft another form saved while the start was on its way", async () => {
		const storage = repository();
		const { service, starts } = heldHub();
		const first = createNewSessionStore("hub-a", storage);
		first.getState().bind(service);
		await first.getState().setCwd("/project", false);
		first.getState().setPrompt("fix the flaky test");
		const started = first.getState().submit();
		await flush();
		// Another form on this hub reads the draft and edits it.
		const second = createNewSessionStore("hub-a", storage);
		second.getState().setPrompt("something else entirely");
		starts[0]?.resolve(landed);
		expect(await started).toMatchObject({ status: "created" });
		expect(storage().read("hub-a")).toMatchObject({ prompt: "something else entirely" });
		expect(second.getState().prompt).toBe("something else entirely");
	});
});
