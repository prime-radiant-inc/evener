import { expect, it } from "vitest";
import { type ConversationClientLike, createNewSessionService } from "../../../mobile/src/services/newSession";
import type { CreationDraft } from "../creationDraftRepository";
import { createNewSessionStore } from "../newSession";
import type { RememberedSetup } from "./launchSetup";
import { openForm } from "./openForm";

const flush = () => new Promise((resolve) => setTimeout(resolve, 0));

/** A store over a hub that answers each read at once: `recent` for the recent
 * projects, and no models. */
function setup(draft: Partial<CreationDraft> | null = null, recent: string[] = []) {
	const saved = new Map<string, CreationDraft>();
	if (draft)
		saved.set("hub-1", {
			source: "local",
			cwd: "",
			prompt: "",
			harness: "",
			model: null,
			reasoning: "",
			launchOverrides: {},
			images: [],
			unconfirmed: false,
			...draft,
		});
	const store = createNewSessionStore("hub-1", () => ({
		read: (hubId: string) => saved.get(hubId) ?? null,
		write: (hubId: string, next: CreationDraft) => void saved.set(hubId, structuredClone(next)),
		clear: (hubId: string) => void saved.delete(hubId),
	}));
	const requests: { method: string; params: unknown }[] = [];
	const service = createNewSessionService({
		request: async (method: string, params: unknown) => {
			requests.push({ method, params });
			if (method === "evener/projects/recent") return { data: recent };
			if (method === "evener/host/request") {
				const forwarded = (params as { method: string }).method;
				if (forwarded === "evener/projects/recent") return { data: recent };
				return { data: [] };
			}
			return { data: [] };
		},
	} as unknown as ConversationClientLike);
	return { store, service, requests };
}

const remembered = (host: string, cwd: string, at: number): RememberedSetup => ({
	setup: { host, cwd, model: { provider: "lunaroute", model: "glm-5.3-vision" }, effort: "high", overrides: {} },
	at,
});

it("applies a session's seed when it opens like a session", () => {
	const { store } = setup({ cwd: "/home/jesse/git/docs", prompt: "a draft" });
	openForm(store, [remembered("local", "/home/jesse/git/evener", 1)], {
		host: "paradise-park",
		cwd: "/Users/jesse/git/evener",
		model: "lunaroute/glm-5.3-vision",
		effort: "high",
	});
	expect(store.getState()).toMatchObject({
		source: "paradise-park",
		cwd: "/Users/jesse/git/evener",
		reasoning: "high",
		prompt: "a draft",
	});
});

it("leaves a draft with a project or a prompt as it stands", () => {
	const { store } = setup({ cwd: "/home/jesse/git/docs" });
	openForm(store, [remembered("paradise-park", "/Users/jesse/git/evener", 1)], undefined);
	expect(store.getState()).toMatchObject({ source: "local", cwd: "/home/jesse/git/docs" });
	const prompted = setup({ prompt: "fix the flaky test" }).store;
	openForm(prompted, [remembered("paradise-park", "/Users/jesse/git/evener", 1)], undefined);
	expect(prompted.getState()).toMatchObject({ source: "local", cwd: "", prompt: "fix the flaky test" });
});

it("leaves a draft holding only an image as it stands", () => {
	const { store } = setup({ images: [{ id: "photo", marker: 1, mediaType: "image/png", data: "AQID" }] });
	openForm(store, [remembered("paradise-park", "/Users/jesse/git/evener", 1)], undefined);
	expect(store.getState()).toMatchObject({ source: "local", cwd: "" });
	expect(store.getState().images).toHaveLength(1);
});

it("opens an empty form on the newest remembered start", () => {
	const { store } = setup();
	openForm(
		store,
		[remembered("local", "/home/jesse/git/docs", 1), remembered("paradise-park", "/Users/jesse/git/evener", 2)],
		undefined,
	);
	expect(store.getState()).toMatchObject({
		source: "paradise-park",
		cwd: "/Users/jesse/git/evener",
		model: { provider: "lunaroute", model: "glm-5.3-vision" },
		reasoning: "high",
	});
});

it("takes the hub's most recent project when nothing was ever started here", async () => {
	const { store, service, requests } = setup(null, ["/home/jesse/git/evener", "/home/jesse/git/docs"]);
	const stop = openForm(store, [], undefined);
	expect(store.getState().cwd).toBe("");
	store.getState().bind(service);
	await store.getState().loadMetadata();
	await flush();
	expect(store.getState().cwd).toBe("/home/jesse/git/evener");
	// The project's own models are read, not the ones for no project.
	expect(requests).toContainEqual({ method: "model/list", params: { cwd: "/home/jesse/git/evener" } });
	stop();
});

it("keeps a project chosen before the hub's recent projects arrive", async () => {
	const { store, service } = setup(null, ["/home/jesse/git/evener"]);
	const stop = openForm(store, [], undefined);
	await store.getState().setCwd("/home/jesse/git/docs", false);
	store.getState().bind(service);
	await store.getState().loadMetadata();
	await flush();
	expect(store.getState().cwd).toBe("/home/jesse/git/docs");
	stop();
});

it("leaves a form whose start is on its way as it is, even opened like a session (#3104)", () => {
	const { store } = setup({ cwd: "/home/jesse/git/evener", prompt: "go" });
	store.setState({ submitting: true });
	openForm(store, [remembered("paradise-park", "/Users/jesse/git/evener", 1)], {
		host: "paradise-park",
		cwd: "/Users/jesse/git/docs",
	});
	expect(store.getState()).toMatchObject({ source: "local", cwd: "/home/jesse/git/evener", prompt: "go" });
});
