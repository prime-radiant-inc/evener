// The fake boundary is the hub's WebSocket, not the client, stores, or UI.
import { AppwireClient, errorText, type ThreadReadResponse, WireError } from "@evener/appwire-client";
import { FakeSocket } from "@evener/appwire-client/testing/fakeSocket";

export interface RecoveryWireRequest {
  method: string;
  params: Record<string, unknown>;
}

export function recoveryClientFixture(options: {
  ref: string;
  snapshot: () => ThreadReadResponse;
  read?: () => ThreadReadResponse | Promise<ThreadReadResponse>;
  resume: () => void | Promise<void>;
  stop?: () => void;
  mutation?: (params: Record<string, unknown>) => unknown;
}) {
  const requests: RecoveryWireRequest[] = [];
  let announceResume!: () => void;
  const resumeReceived = new Promise<void>((resolve) => {
    announceResume = resolve;
  });
  const client = new AppwireClient({
    url: "ws://hub/rpc",
    socketFactory: () => {
      const socket = new FakeSocket({ autoInitialize: true });
      const send = socket.send.bind(socket);
      socket.send = (raw) => {
        send(raw);
        const request = JSON.parse(raw);
        if (!request.id || request.method === "initialize" || request.method === "ping") return;
        requests.push(request);
        const respond = async (): Promise<unknown> => {
          switch (request.method) {
            case "thread/read":
              return options.read ? options.read() : options.snapshot();
            case "thread/resume":
              announceResume();
              await options.resume();
              return options.snapshot();
            case "evener/thread/forceStop":
              options.stop?.();
              return {};
            case "evener/jobs/list":
              return {
                data: {
                  revision: 1,
                  root: {
                    sessionId: options.snapshot().thread.sessionId,
                    ref: options.ref,
                    label: "Root session",
                    aggregate: "completed",
                    counts: { active: 0, failed: 0, completed: 0, complete: true },
                    entries: [],
                    branch: {},
                  },
                },
              };
            case "thread/turns/list":
              return { data: [], nextCursor: null };
            case "turn/start":
              return (
                options.mutation?.(request.params) ?? {
                  turn: { id: "new-turn", status: "inProgress", itemsView: "full" },
                  receipt: {
                    clientMutationId: request.params.clientMutationId,
                    threadId: options.snapshot().thread.id,
                    disposition: "applied",
                    projectionState: "reflected",
                  },
                }
              );
            default:
              return {};
          }
        };
        void respond().then(
          (result) => socket.receive({ id: request.id, result }),
          (error: unknown) =>
            socket.receive({
              id: request.id,
              error: {
                code: error instanceof WireError ? error.code : -32000,
                message: errorText(error),
                data: error instanceof WireError ? error.data : undefined,
              },
            }),
        );
      };
      queueMicrotask(() => socket.open());
      return socket;
    },
  });
  return { client, requests, resumeReceived };
}
