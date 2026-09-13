import { FakeClient } from "../../protocol/testing/fakeClient";
import { navigationInvalidatedNotification } from "../../protocol/testing/notifications";
import type { InputItem, MethodTypes, MutationReceipt, Turn, TurnStartParams } from "../../protocol/types.gen";
import { wireV2 } from "../../stores/navigation/testing";
import { summaries as initialSummaries, initialThreads, PARENT, parentRefs, QUESTION } from "./data";

export { CHILD, PARENT, QUESTION, RESUMED } from "./data";

export class EditorialClient extends FakeClient {
  readonly rejectedRequests: string[] = [];
  override request<M extends keyof MethodTypes>(
    method: M,
    params: MethodTypes[M]["params"],
  ): Promise<MethodTypes[M]["result"]> {
    return super.request(method, params).catch((error) => {
      this.rejectedRequests.push(`${method}: ${String(error)}`);
      throw error;
    });
  }
}

export function createEditorialClient(): EditorialClient {
  const client = new EditorialClient("ready");
  const threads = initialThreads();
  const summaries = structuredClone(initialSummaries);
  let revision = 1;
  const applied = new Map<string, { turn: Turn; receipt: MutationReceipt }>();
  const parent = summaries.find((row) => row.ref === PARENT);
  if (!parent) throw new Error("Fixture parent is required");
  const sessionTree = (row: (typeof summaries)[number]): object => ({
    ...row,
    children: summaries.filter((child) => parentRefs[child.ref] === row.ref).map(sessionTree),
  });
  let serial = 0;
  const read = (ref?: string) => {
    const result = threads.get(ref ?? "");
    if (!result) throw new Error(`Unknown fixture thread: ${ref}`);
    return structuredClone(result);
  };
  client.scriptConnect(() => ({
    serverInfo: { name: "Editorial fixture — no live hub", version: "fixture" },
    protocolVersion: "evener-appwire-v5",
    sourceId: "fixture",
    features: {
      threadList: true,
      threadTurnsList: true,
      turnStart: true,
      turnSteer: true,
      threadClear: false,
      threadShutdown: false,
      forkFromTurn: false,
      tasks: true,
      transcriptList: true,
      modelList: true,
      directoryComplete: true,
      auth: true,
    },
    navigation: { version: 1, readVersions: [2], generationId: "generation_test", sequence: 0 },
  }));
  client.on("evener/navigation/read", (params) => {
    const wrap = (data: unknown) => wireV2(params, data, `"editorial-${revision}"`, revision);
    switch (params.resource) {
      case "manifest":
        return wrap({
          sources: [],
          attentionSummary: {
            needsYou: summaries.filter((row) => row.state === "awaiting").length,
            error: 0,
            working: 1,
          },
          sections: {
            live: { count: summaries.length },
            needs_you: { count: summaries.filter((row) => row.state === "awaiting").length },
            pin_sections: { count: 0 },
          },
          catalogs: { projects: { count: 1 }, archived_projects: { count: 0 }, test_runs: { count: 0 } },
        });
      case "section":
        return wrap({
          sessions: params.section === "live" ? summaries : summaries.filter((row) => row.state === "awaiting"),
          remaining: 0,
          truncated: false,
        });
      case "pin_catalog":
        return wrap({ pin_sections: [], remaining: 0 });
      case "catalog":
        return wrap({
          projects:
            params.catalog === "projects"
              ? [
                  {
                    key: "editorial",
                    name: "Fixture only · no live hub",
                    session_count: 2,
                    working_dir: "/fixture/editorial",
                  },
                ]
              : [],
          remaining: 0,
        });
      case "project":
        return wrap({
          key: "editorial",
          current: {
            sessions: [sessionTree(parent), summaries.find((row) => row.ref === QUESTION)],
            remaining: 0,
          },
          recent: { sessions: [], remaining: 0 },
          archived: { sessions: [], remaining: 0 },
          truncated: false,
        });
      case "location": {
        const session = summaries.find((row) => row.ref === params.ref);
        if (!session) throw new Error(`Unknown fixture location: ${params.ref}`);
        let owner = session.ref;
        for (let ancestor = parentRefs[owner]; ancestor; ancestor = parentRefs[owner]) owner = ancestor;
        const response = wrap({
          ref: params.ref,
          top_level_ref: owner,
          top_level: params.ref === owner,
          tier: "current",
          session,
        });
        // The shared test helper defaults locations to top-level. This fixture
        // serves real nested locations, so preserve the truthful v2 metadata.
        const data = response.data as { metadata: Record<string, unknown> };
        data.metadata.top_level = params.ref === owner;
        return response;
      }
      default:
        throw new Error(`Unexpected fixture navigation: ${JSON.stringify(params)}`);
    }
  });
  const subscriptions = new Set<string>();
  client.on("thread/read", (params) => {
    const snapshot = read(params.ref);
    if (params.subscribe) subscriptions.add(snapshot.thread.evener.ref);
    return snapshot;
  });
  client.on("thread/unsubscribe", ({ ref, threadId }) => {
    const snapshot = read(ref ?? `local:${threadId}`);
    subscriptions.delete(snapshot.thread.evener.ref);
    return {};
  });
  client.on("thread/turns/list", ({ ref }) => {
    read(ref);
    return { data: [] };
  });
  client.on("evener/subagentPreview", ({ ref }) => ({
    ref,
    items: read(ref).thread.turns?.flatMap((turn) => turn.items ?? []) ?? [],
    truncated: false,
  }));
  client.on("evener/jobs/list", ({ ref }) => {
    const snapshot = read(ref);
    const delegates = snapshot.thread.evener.diagnostics?.delegates ?? [];
    const active = delegates.filter((row) => row.status === "running").length;
    return {
      data: {
        revision,
        root: {
          sessionId: snapshot.thread.sessionId,
          ref: snapshot.thread.evener.ref,
          label: snapshot.thread.name,
          aggregate: active ? "running" : "idle",
          counts: {
            active,
            failed: 0,
            completed: delegates.filter((row) => row.terminal && row.outcome === "completed").length,
            complete: false,
          },
          entries: delegates.map((row) => ({
            kind: "delegate",
            delegate: { ...row, childRef: row.transcriptRef, turns: [], branch: {} },
          })),
          diagnostics: [
            "Fixture activity includes known owner projections only; unavailable collaborator is not counted.",
          ],
          branch: {},
        },
      },
    };
  });
  client.on("evener/tasks/list", () => ({
    data: [
      {
        id: 1,
        type: "verify",
        description: "Review fixture evidence",
        status: "in_progress",
        prompt: "Review fixture-only evidence",
      },
    ],
  }));
  function completeInput(params: TurnStartParams): { turn: Turn; receipt: MutationReceipt } {
    const previous = applied.get(params.clientMutationId);
    if (previous) return { ...previous, receipt: { ...previous.receipt, disposition: "replayed" } };
    const snapshot = threads.get(params.ref ?? "");
    if (!snapshot) throw new Error(`Unknown fixture mutation ref: ${params.ref}`);
    if (params.expectedInstanceId !== snapshot.thread.evener.instanceId) throw new Error("Fixture instance mismatch");
    const input = (params.input ?? []).map((item: InputItem) => item.text ?? "").join("\n");
    const turnId = `fixture-send-${++serial}`;
    const turn: Turn = {
      id: turnId,
      itemsView: "full",
      status: "completed",
      items: [
        { id: `${turnId}-user`, type: "userMessage", turnId, text: input, clientMutationId: params.clientMutationId },
        { id: `${turnId}-reply`, type: "agentMessage", turnId, text: `Fixture acknowledged: ${input}` },
      ],
    };
    snapshot.thread.turns = [...(snapshot.thread.turns ?? []), turn];
    snapshot.thread.status = { type: "idle" };
    snapshot.thread.evener.askPending = false;
    const ref = snapshot.thread.evener.ref;
    const threadId = snapshot.thread.id;
    client.emitNotification({
      method: "turn/started",
      params: { ref, threadId, turn: { ...turn, status: "inProgress", items: [] } },
    });
    for (const item of turn.items ?? [])
      client.emitNotification({ method: "item/completed", params: { ref, threadId, turnId, item } });
    client.emitNotification({ method: "turn/completed", params: { ref, threadId, turn } });
    const summary = summaries.find((row) => row.ref === ref);
    if (summary) summary.state = "idle";
    revision++;
    client.emitNotification(
      navigationInvalidatedNotification({
        generationId: "generation_test",
        sequence: revision - 1,
        targets: [
          { kind: "manifest", revision },
          { kind: "section", section: "live", revision },
          { kind: "section", section: "needs_you", revision },
          { kind: "project", projectKey: "editorial", revision },
        ],
      }),
    );
    const result = {
      turn,
      receipt: {
        clientMutationId: params.clientMutationId,
        disposition: "applied",
        projectionState: "reflected",
        threadId,
        instanceId: snapshot.thread.evener.instanceId,
        turnId,
      },
    };
    applied.set(params.clientMutationId, result);
    return result;
  }
  client.on("turn/start", completeInput);
  client.on("turn/steer", (params) => ({ receipt: completeInput(params).receipt }));
  client.on("evener/settings/overview", () => ({
    hub: { version: "fixture", listenAddr: "Fixture only; no hub listener", runDir: "/fixture/run" },
    storage: { stateDir: "/fixture/state" },
    agents: [{ name: "fixture" }],
  }));
  client.on("evener/instance/list", () => ({
    instances: [
      {
        name: "fixture-provider",
        providerId: "anthropic",
        protocol: "anthropic",
        auth: "bearer",
        implicit: true,
        isDefault: true,
        activeSource: "none",
        hasStoredOAuth: false,
        credentialRequired: true,
        authModes: ["apiKey"],
      },
    ],
    availableProviders: [
      { id: "anthropic", name: "Anthropic", protocol: "anthropic", auth: "bearer", implicit: false },
    ],
  }));
  client.on("model/list", () => ({ data: [{ provider: "fixture", model: "scripted" }] }));
  client.on("evener/harnesses/list", () => ({ data: [{ id: "evener", label: "Fixture only", kind: "evener" }] }));
  client.on("evener/launch/schema", () => ({ options: [] }));
  client.on("evener/projects/recent", () => ({ data: ["/fixture/editorial"] }));
  client.on("evener/paths/complete", () => ({ data: ["/fixture/editorial"] }));
  client.on("evener/path/validate", ({ path }) => ({
    path,
    valid: path === "/fixture/editorial",
    ...(path === "/fixture/editorial" ? {} : { error: "Only /fixture/editorial exists in this preview" }),
  }));
  client.on("evener/plugin/preview", () => ({ plugins: [] }));
  return client;
}
