import type {
  InputItem,
  MethodTypes,
  MutationReceipt,
  SessionActivityContext,
  SessionDelegate,
  Turn,
  TurnStartParams,
} from "@evener/appwire-client";
import { APPWIRE_PROTOCOL_VERSION } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { wireSnapshot } from "@evener/appwire-client/testing/navigation";
import { navigationInvalidatedNotification } from "@evener/appwire-client/testing/notifications";
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
  let serial = 0;
  const read = (ref?: string) => {
    const result = threads.get(ref ?? "");
    if (!result) throw new Error(`Unknown fixture thread: ${ref}`);
    return structuredClone(result);
  };
  client.scriptConnect(() => ({
    serverInfo: { name: "Editorial fixture — no live hub", version: "fixture" },
    protocolVersion: APPWIRE_PROTOCOL_VERSION,
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
    navigation: { version: 1, readVersions: [3], generationId: "generation_test", sequence: 0 },
  }));
  client.on("evener/navigation/read", (params) => {
    const wrap = (data: unknown) => wireSnapshot(params, data, `"editorial-${revision}"`, revision);
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
            // Flat rows, like the real wire: activity hierarchy uses the typed activity API.
            sessions: [parent, summaries.find((row) => row.ref === QUESTION)].map((row) => ({ ...row, children: [] })),
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
        // serves real nested locations, so preserve the truthful location metadata.
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
  // The session location line resolves the branch and origin from
  // the session's cwd; a fixture hub must answer it like every other method the
  // mounted panes request. A fixture repo URL keeps the forge link renderable.
  client.on("evener/git/head", () => ({
    head: "fixture-branch",
    originUrl: "git@github.com:fixture/editorial.git",
  }));
  client.on("evener/subagentPreview", ({ ref }) => ({
    ref,
    items: read(ref).thread.turns?.flatMap((turn) => turn.items ?? []) ?? [],
    truncated: false,
  }));
  const activityContext = (ref: string): SessionActivityContext => {
    const snapshot = read(ref);
    const ancestors = [];
    let parent = parentRefs[ref];
    while (parent) {
      const owner = read(parent);
      ancestors.unshift({ ref: parent, sessionId: owner.thread.sessionId, title: owner.thread.name ?? parent });
      parent = parentRefs[parent];
    }
    return {
      ref,
      sessionId: snapshot.thread.sessionId,
      rootRef: ancestors[0]?.ref ?? ref,
      parentRef: parentRefs[ref],
      ancestors,
      ancestryKnown: true,
      epoch: `fixture-${revision}`,
      availability: "live",
    };
  };
  const activityDelegates = (ref: string): SessionDelegate[] =>
    (read(ref).thread.evener.diagnostics?.delegates ?? []).map((row) => ({
      ...row,
      ownerRef: ref,
      rootRef: activityContext(ref).rootRef,
      childRef: row.transcriptRef,
      description: row.task ?? "",
      task: row.task ?? "",
      phase: row.phase ?? row.status,
      lifecycle: row.lifecycle ?? row.status,
      terminal: row.terminal ?? false,
      resumable: row.resumable ?? false,
    }));
  client.on("evener/thread/activity/read", ({ ref, scope }) => {
    const rows = activityDelegates(ref);
    return {
      context: activityContext(ref),
      scope: scope ?? "session",
      delegates: {
        known: true,
        total: rows.length,
        active: rows.filter((row) => !row.terminal).length,
        failed: rows.filter((row) => row.outcome === "failed").length,
        completed: rows.filter((row) => row.terminal && row.outcome !== "failed").length,
      },
      jobs: { known: true, total: 0, active: 0, failed: 0, completed: 0 },
      watches: { known: true, total: 0, active: 0, failed: 0, completed: 0 },
    };
  });
  client.on("evener/thread/delegates/list", ({ ref, scope }) => ({
    context: activityContext(ref),
    scope: scope ?? "session",
    delegates: activityDelegates(ref),
    page: { complete: true, issues: [] },
  }));
  client.on("evener/thread/jobs/list", ({ ref, scope }) => ({
    context: activityContext(ref),
    scope: scope ?? "session",
    jobs: [],
    page: { complete: true, issues: [] },
  }));
  client.on("evener/thread/watches/list", ({ ref, scope }) => ({
    context: activityContext(ref),
    scope: scope ?? "session",
    watches: [],
    page: { complete: true, issues: [] },
  }));
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
      method: "history/updated",
      params: {
        ref,
        threadId,
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        turns: [{ ...turn, items: [] }],
        items: turn.items ?? [],
      },
    });
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
    hub: {
      version: "fixture",
      listenAddr: "Fixture only; no hub listener",
      runDir: "/fixture/run",
      daemonIdleTimeoutMillis: 3600000,
    },
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
  client.on("evener/spawn/slashCatalog", () => ({ commands: [], skills: [] }));
  // The hub-wide command catalog (stores/commandCatalog.ts): a ready
  // connection reads it without waiting for a palette open.
  client.on("evener/command/list", () => ({ commands: [] }));
  return client;
}
