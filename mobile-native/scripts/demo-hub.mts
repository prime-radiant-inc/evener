// Explicitly launched network fixture for native UI checks; no Evener or LLM runs.
import { once } from "node:events";
import { pathToFileURL } from "node:url";
import { WebSocket, WebSocketServer } from "ws";
import type { InitializeResponse, MutationReceipt, Thread, Turn, TurnStartParams } from "../../cmd/evener-hub/frontend/src/protocol/types.gen";

export async function createDemoHub(port = 9196) {
  const server = new WebSocketServer({ host: "127.0.0.1", port, path: "/rpc" });
  await once(server, "listening");
  const address = server.address();
  if (typeof address === "string" || !address) throw new Error("Missing demo address");
  const subscribers = new Set<WebSocket>();
  const thread: Thread = {
    id: "demo-thread", sessionId: "demo-session", name: "Mobile playground", preview: "Scripted demonstration only",
    ephemeral: true, modelProvider: "demonstration", createdAt: 1, updatedAt: 1,
    status: { type: "idle" }, cwd: "/demonstration", cliVersion: "demo", source: "demo", turns: [],
    evener: { ref: "demo:playground", instanceId: "demo-instance", queue: { revision: 0 }, capabilities: {
      send: true, steer: false, interrupt: false, compact: false, clear: false, forkFromTurn: false,
      shutdown: false, changeModel: false, changeVisionModel: false, queue: false, goal: false, rename: false,
    } },
  };
  const handshake: InitializeResponse = {
    serverInfo: { name: "Native UI demonstration", version: "1" }, protocolVersion: "evener-appwire-v3", sourceId: "demo",
    features: { threadList: true, threadTurnsList: true, turnStart: true, turnSteer: false, threadClear: false,
      threadShutdown: false, forkFromTurn: false, tasks: false, transcriptList: false, modelList: false, directoryComplete: false, auth: false },
  };
  let turnNumber = 0;
  function resync() {
    for (const socket of subscribers) if (socket.readyState === WebSocket.OPEN)
      socket.send(JSON.stringify({ jsonrpc: "2.0", method: "evener/thread/resync", params: { threadId: thread.id, ref: thread.evener.ref } }));
  }
  server.on("connection", socket => {
    socket.on("close", () => subscribers.delete(socket));
    socket.on("message", raw => {
      let id: unknown = null;
      try {
        const request = JSON.parse(raw.toString());
        id = request.id;
        if (id === undefined) return;
        const params = request.params ?? {};
        let result: unknown;
        let changed = false;
        switch (request.method) {
          case "initialize": result = handshake; break;
          case "ping": result = {}; break;
          case "thread/list": result = { data: [{ ...thread, turns: undefined }] }; break;
          case "thread/read":
            if (params.ref !== thread.evener.ref) throw new Error("Unknown demonstration session");
            if (params.subscribe) subscribers.add(socket);
            result = { thread: { ...thread, turns: params.includeTurns ? thread.turns : undefined } }; break;
          case "thread/turns/list": result = { data: [] }; break;
          case "turn/start":
          case "turn/interrupt": {
            const mutation = params as TurnStartParams;
            if (mutation.ref !== thread.evener.ref || mutation.expectedInstanceId !== thread.evener.instanceId)
              throw new Error("Demonstration session identity mismatch");
            if (!mutation.clientMutationId) throw new Error("Missing mutation identity");
            const starting = request.method === "turn/start";
            let turn = thread.turns?.at(-1);
            if (starting) {
              if (thread.status.type === "active") throw new Error("Stop the demonstration before another send");
              turnNumber += 1;
              turn = { id: `demo-turn-${turnNumber}`, itemsView: "full", status: "inProgress", items: [
                { id: `demo-user-${turnNumber}`, type: "userMessage", text: (mutation.input ?? []).map(item => item.text ?? "").join("\n") },
                { id: `demo-assistant-${turnNumber}`, type: "agentMessage", text: "Demonstration reply: your message reached this scripted test server. Tap Stop to end this demonstration turn.", status: "completed" },
              ] } satisfies Turn;
              thread.turns?.push(turn);
            } else {
              if (!turn || thread.status.type !== "active") throw new Error("No active demonstration turn");
              turn.status = "interrupted";
            }
            if (!turn) throw new Error("Missing demonstration turn");
            thread.status = { type: starting ? "active" : "idle" };
            thread.evener.capabilities.send = !starting;
            thread.evener.capabilities.interrupt = starting;
            thread.evener.activeTurnId = starting ? turn.id : undefined;
            thread.updatedAt += 1;
            const receipt: MutationReceipt = { clientMutationId: mutation.clientMutationId, disposition: "applied", threadId: thread.id,
              instanceId: thread.evener.instanceId, turnId: turn.id, projectionState: starting ? "pending" : "reflected" };
            result = starting ? { turn, receipt } : { receipt };
            changed = true; break;
          }
          default: throw new Error("Method not implemented by demonstration server");
        }
        socket.send(JSON.stringify({ jsonrpc: "2.0", id, result }));
        if (changed) resync();
      } catch (error) {
        socket.send(JSON.stringify({ jsonrpc: "2.0", id, error: { code: -32602, message: error instanceof Error ? error.message : "Invalid request" } }));
      }
    });
  });
  return { origin: `http://127.0.0.1:${address.port}`, close: () => new Promise<void>((resolve, reject) => {
    for (const socket of server.clients) socket.terminate();
    server.close(error => error ? reject(error) : resolve());
  }) };
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const hub = await createDemoHub();
  console.info(`Scripted native UI demonstration: ${hub.origin} (no token, no LLM).`);
  for (const signal of ["SIGINT", "SIGTERM"] as const) process.once(signal, () => { void hub.close(); });
}
