import { expect, it } from "vitest";
import {
  type ConversationClientLike,
  createNewSessionService,
} from "../../mobile/src/services/newSession";
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
