// External WebSocket fault injection shared by the browser and native journeys.
// Every delivered response still comes from the real hub.
export function installRetryWireFault() {
  const Original = globalThis.WebSocket;
  const state = { mode: "normal", armed: false, requests: [], dropped: 0, connections: 0 };
  globalThis.__retryWire = state;
  globalThis.WebSocket = class extends Original {
    constructor(...args) {
      super(...args);
      this.addEventListener("open", () => {
        state.connections++;
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
            };
    }
    get onmessage() {
      return super.onmessage;
    }
    send(data) {
      const message = JSON.parse(String(data));
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
