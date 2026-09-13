import { expect, it } from "vitest";
import { WireError } from "../../appwire-client/typescript/errors";
import {
  type ConversationClientLike,
  createNewSessionService,
} from "../../mobile/src/services/newSession";
import type { CreationDraft } from "./creationDraftRepository";
import { creationImageDraft } from "./creationImageDraft";
import { ImageSelection } from "./imageSelection";
import { createNewSessionStore } from "./newSession";

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
  const third = store.getState().setHarness("agent");
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
  expect(calls[2]?.params).toEqual({ cwd: "/two", harness: "agent" });
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
  calls[1]?.response.resolve({ data: [{ id: "old", label: "Old" }] });
  calls[2]?.response.resolve({ data: [model] });
  calls[3]?.response.resolve({ thread: { evener: { ref: "old" } }, turn: {} });
  await metadata;
  expect(await pending).toEqual({ status: "obsolete" });
  expect(store.getState()).toMatchObject({
    projects: [],
    harnesses: [],
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
  calls[1]?.response.resolve({ data: [] });
  await metadata;
  expect(store.getState().metadataError).toBeTruthy();
  const models = store.getState().loadModels();
  calls[2]?.response.resolve({ data: [model] });
  await models;
  expect(store.getState().metadataError).toBeTruthy();
  const retry = store.getState().loadMetadata();
  calls[3]?.response.resolve({ data: ["/project"] });
  calls[4]?.response.resolve({ data: [] });
  await retry;
  expect(store.getState().metadataError).toBeNull();
  expect(store.getState().projects).toEqual(["/project"]);
});

it("retains model failure across metadata success until model recovery", async () => {
  const { store, calls } = setup();
  const models = store.getState().loadModels();
  calls[0]?.response.reject(new Error("models unavailable"));
  await models;
  expect(store.getState().modelError).toBeTruthy();
  const metadata = store.getState().loadMetadata();
  calls[1]?.response.resolve({ data: [] });
  calls[2]?.response.resolve({ data: [] });
  await metadata;
  expect(store.getState().modelError).toBeTruthy();
  const retry = store.getState().loadModels();
  calls[3]?.response.resolve({ data: [model] });
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
  expect(
    calls.filter((c) => c.method === "evener/launch/setLayer"),
  ).toHaveLength(0);
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
  expect(document.imagePreviews()).toEqual([
    { marker: 2, name: "photo.jpg", mediaType: "image/png", data: "AQID" },
  ]);
  expect(store.getState().prompt).toBe("[image 2]");
  document.removeImage("photo");
  expect(document.imagePreviews()).toEqual([]);
  expect(store.getState().prompt).toBe("");
});
