// External WebSocket fault injection shared by the browser and native journeys.
// Every delivered response still comes from the real hub.
export function installRetryWireFault() {
  const Original = globalThis.WebSocket;
  const sockets = new Set();
  const state = {
    mode: "normal", armed: false, requests: [], reads: [], dropped: 0, connections: 0,
    disconnect() {
      for (const socket of sockets) socket.close();
    },
  };
  globalThis.__retryWire = state;
  globalThis.WebSocket = class extends Original {
    constructor(...args) {
      super(...args);
      this.retryReads = new Map();
      sockets.add(this);
      this.addEventListener("close", () => sockets.delete(this));
      this.addEventListener("open", () => {
        this.retryConnection = ++state.connections;
      });
    }
    set onmessage(listener) {
      super.onmessage =
        listener === null
          ? null
          : (event) => {
              const message = JSON.parse(String(event.data));
              if (this.lostReplyID !== undefined && message.id === this.lostReplyID && message.result) {
                this.lostReplyID = undefined;
                state.dropped++;
                this.close();
                return;
              }
              listener(event);
              const read = this.retryReads.get(message.id);
              if (read) {
                this.retryReads.delete(message.id);
                if (message.result?.thread?.evener?.ref === read.ref) {
                  state.reads.push({ ...read, connection: this.retryConnection });
                }
              }
            };
    }
    get onmessage() {
      return super.onmessage;
    }
    send(data) {
      const message = JSON.parse(String(data));
      if (message.method === "thread/read") {
        this.retryReads.set(message.id, { ref: message.params.ref, subscribed: message.params.subscribe === true });
      }
      if (message.method === "turn/start" || message.method === "turn/queue") {
        state.requests.push({ method: message.method, params: message.params });
        if (state.armed) {
          state.armed = false;
          if (state.mode === "lost-reply") this.lostReplyID = message.id;
          if (state.mode === "refused") {
            super.send(
              JSON.stringify({
                ...message,
                params: { ...message.params, expectedInstanceId: "fixture-wrong-instance" },
              }),
            );
            return;
          }
        }
      }
      super.send(data);
    }
  };
  return state;
}
